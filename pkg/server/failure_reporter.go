package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

// FirstbootFailureReport represents a failure report from a node during firstboot
type FirstbootFailureReport struct {
	Pool         string `json:"pool"`         // MachineConfigPool name (e.g., "worker", "master")
	NodeID       string `json:"nodeID"`       // Node identifier (hostname or MAC address)
	Stage        string `json:"stage"`        // Failure stage (e.g., "pivot", "kargs", "updateLayeredOS")
	ImageURL     string `json:"imageURL"`     // OS image URL that failed to apply
	ErrorMessage string `json:"errorMessage"` // Detailed error message
}

// FailureReporter defines the interface for reporting firstboot failures
type FailureReporter interface {
	// ReportFailure processes a firstboot failure report
	// Returns error only for logging; HTTP handler always returns 202
	ReportFailure(ctx context.Context, report *FirstbootFailureReport) error
}

// clusterFailureReporter creates Kubernetes Events for firstboot failures
type clusterFailureReporter struct {
	kubeclient kubernetes.Interface
}

// NewClusterFailureReporter creates a FailureReporter for cluster mode
func NewClusterFailureReporter(kubeclient kubernetes.Interface) FailureReporter {
	return &clusterFailureReporter{
		kubeclient: kubeclient,
	}
}

func (r *clusterFailureReporter) ReportFailure(ctx context.Context, report *FirstbootFailureReport) error {
	// Event name is deterministic based on pool + nodeID to enable patching
	eventName := fmt.Sprintf("firstboot-failure-%s-%s", report.Pool, sanitizeForEventName(report.NodeID))

	namespace := ctrlcommon.MCONamespace // "openshift-machine-config-operator"

	// Check if event already exists
	existingEvent, err := r.kubeclient.CoreV1().Events(namespace).Get(ctx, eventName, metav1.GetOptions{})

	now := metav1.NewTime(time.Now())

	if err == nil {
		// Event exists, update it (increment count, update timestamp and message)
		existingEvent.Count++
		existingEvent.LastTimestamp = now
		existingEvent.Message = formatFailureMessage(report)

		_, updateErr := r.kubeclient.CoreV1().Events(namespace).Update(ctx, existingEvent, metav1.UpdateOptions{})
		if updateErr != nil {
			klog.Errorf("Failed to update firstboot failure event %s: %v", eventName, updateErr)
			return updateErr
		}
		klog.Infof("Updated firstboot failure event for pool=%s node=%s (count=%d)",
			report.Pool, report.NodeID, existingEvent.Count)
	} else {
		// Event doesn't exist, create it
		event := &corev1.Event{
			ObjectMeta: metav1.ObjectMeta{
				Name:      eventName,
				Namespace: namespace,
			},
			InvolvedObject: corev1.ObjectReference{
				Kind:      "MachineConfigPool",
				Name:      report.Pool,
				Namespace: namespace,
			},
			Reason:         "FirstbootFailed",
			Type:           corev1.EventTypeWarning,
			Message:        formatFailureMessage(report),
			FirstTimestamp: now,
			LastTimestamp:  now,
			Count:          1,
			Source: corev1.EventSource{
				Component: "machine-config-server",
			},
		}

		_, createErr := r.kubeclient.CoreV1().Events(namespace).Create(ctx, event, metav1.CreateOptions{})
		if createErr != nil {
			klog.Errorf("Failed to create firstboot failure event %s: %v", eventName, createErr)
			return createErr
		}
		klog.Infof("Created firstboot failure event for pool=%s node=%s stage=%s",
			report.Pool, report.NodeID, report.Stage)
	}

	return nil
}

// formatFailureMessage creates a human-readable event message
func formatFailureMessage(report *FirstbootFailureReport) string {
	return fmt.Sprintf("Node %s failed during firstboot at stage '%s': %s (image: %s)",
		report.NodeID, report.Stage, report.ErrorMessage, report.ImageURL)
}

// sanitizeForEventName converts nodeID to a valid Kubernetes name
// (lowercase alphanumeric, '-', max 253 chars)
func sanitizeForEventName(nodeID string) string {
	// Replace invalid characters with '-'
	// Kubernetes event names must match: [a-z0-9]([-a-z0-9]*[a-z0-9])?
	sanitized := strings.ToLower(nodeID)
	sanitized = strings.ReplaceAll(sanitized, ":", "-")
	sanitized = strings.ReplaceAll(sanitized, "_", "-")
	sanitized = strings.ReplaceAll(sanitized, ".", "-")

	// Truncate if needed
	if len(sanitized) > 200 {
		sanitized = sanitized[:200]
	}

	return sanitized
}

// bootstrapFailureReporter logs failures (no API access during bootstrap)
type bootstrapFailureReporter struct{}

// NewBootstrapFailureReporter creates a FailureReporter for bootstrap mode
func NewBootstrapFailureReporter() FailureReporter {
	return &bootstrapFailureReporter{}
}

func (r *bootstrapFailureReporter) ReportFailure(ctx context.Context, report *FirstbootFailureReport) error {
	// During bootstrap, we can only log - no Kubernetes API access
	klog.Warningf("Firstboot failure (bootstrap mode, log only): pool=%s node=%s stage=%s image=%s error=%s",
		report.Pool, report.NodeID, report.Stage, report.ImageURL, report.ErrorMessage)
	return nil
}
