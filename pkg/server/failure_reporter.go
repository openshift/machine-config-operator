package server

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
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

// clusterFailureReporter creates Kubernetes Events for firstboot failures using EventRecorder
type clusterFailureReporter struct {
	eventRecorder record.EventRecorder
}

// NewClusterFailureReporter creates a FailureReporter for cluster mode
func NewClusterFailureReporter(eventRecorder record.EventRecorder) FailureReporter {
	return &clusterFailureReporter{
		eventRecorder: eventRecorder,
	}
}

func (r *clusterFailureReporter) ReportFailure(ctx context.Context, report *FirstbootFailureReport) error {
	// Create a reference object (MachineConfigPool) to attach the event to
	// EventRecorder handles deduplication and count increments automatically
	poolRef := &mcfgv1.MachineConfigPool{}
	poolRef.SetNamespace(ctrlcommon.MCONamespace)
	poolRef.SetName(report.Pool)
	poolRef.SetGroupVersionKind(mcfgv1.GroupVersion.WithKind("MachineConfigPool"))

	message := formatFailureMessage(report)

	// EventRecorder handles event creation, updates, and deduplication
	r.eventRecorder.Event(poolRef, corev1.EventTypeWarning, "FirstbootFailed", message)

	return nil
}

// formatFailureMessage creates a human-readable event message with sanitized content
func formatFailureMessage(report *FirstbootFailureReport) string {
	sanitizedError := sanitizeErrorMessage(report.ErrorMessage)
	sanitizedImage := sanitizeImageURL(report.ImageURL)
	return fmt.Sprintf("Node %s failed during firstboot at stage '%s': %s (image: %s)",
		report.NodeID, report.Stage, sanitizedError, sanitizedImage)
}

// sanitizeErrorMessage redacts potential secrets from error messages
func sanitizeErrorMessage(errMsg string) string {
	if errMsg == "" {
		return "unknown error"
	}
	// Truncate very long error messages to avoid event bloat
	maxLength := 200
	if len(errMsg) > maxLength {
		errMsg = errMsg[:maxLength] + "... (truncated)"
	}
	// Remove common secret patterns
	errMsg = redactSecrets(errMsg)
	return errMsg
}

// sanitizeImageURL redacts credentials and internal hostnames from image URLs
func sanitizeImageURL(imageURL string) string {
	if imageURL == "" {
		return "none"
	}

	// Image URLs might not have a scheme (e.g., "quay.io/repo:tag")
	// Prepend a scheme if missing to help url.Parse
	urlToParse := imageURL
	if !strings.Contains(imageURL, "://") {
		urlToParse = "https://" + imageURL
	}

	parsed, err := url.Parse(urlToParse)
	if err != nil || parsed.Host == "" {
		// If parsing fails or no host found, just return a generic placeholder
		return "invalid-url"
	}

	// Clear any embedded credentials
	parsed.User = nil

	// Redact internal hostnames by showing only the registry type
	host := parsed.Host
	if strings.Contains(host, "image-registry.openshift-image-registry") {
		return "internal-registry"
	}

	// For external registries, show only the host (registry)
	// Strip path/query/fragment to avoid exposing repo details
	parsed.Path = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Scheme = ""

	// Return just the host part
	result := parsed.String()
	// Clean up the "//" that remains after clearing scheme
	result = strings.TrimPrefix(result, "//")

	return result
}

// redactSecrets removes common secret patterns from strings
func redactSecrets(s string) string {
	// Redact potential tokens, passwords, keys
	patterns := []struct {
		prefix string
		suffix string
	}{
		{"token=", "&"},
		{"password=", "&"},
		{"key=", "&"},
		{"secret=", "&"},
		{"authorization:", "\n"},
		{"bearer ", " "},
	}

	result := s
	for _, pattern := range patterns {
		if idx := strings.Index(strings.ToLower(result), pattern.prefix); idx != -1 {
			start := idx + len(pattern.prefix)
			end := strings.Index(result[start:], pattern.suffix)
			if end == -1 {
				end = len(result) - start
			}
			result = result[:start] + "[REDACTED]" + result[start+end:]
		}
	}

	return result
}
