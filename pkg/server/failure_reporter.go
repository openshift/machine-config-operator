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

// FailureReporter defines the interface for reporting firstboot failures
type FailureReporter interface {
	// ReportFailure processes a firstboot failure report
	// Returns error only for logging; HTTP handler always returns 202
	ReportFailure(ctx context.Context, report *ctrlcommon.FirstbootFailureReport) error
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

func (r *clusterFailureReporter) ReportFailure(_ context.Context, report *ctrlcommon.FirstbootFailureReport) error {
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
func formatFailureMessage(report *ctrlcommon.FirstbootFailureReport) string {
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

	// Strip port for comparison (e.g., quay.io:443 -> quay.io)
	hostWithoutPort := host
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		hostWithoutPort = host[:idx]
	}

	// Allowlist of public registries safe to include in events
	publicRegistries := []string{
		"quay.io",
		"registry.redhat.io",
		"registry.access.redhat.com",
		"docker.io",
		"ghcr.io",
		"gcr.io",
		"registry.k8s.io",
	}

	for _, allowed := range publicRegistries {
		if hostWithoutPort == allowed || strings.HasSuffix(hostWithoutPort, "."+allowed) {
			return hostWithoutPort
		}
	}

	// All other registries are considered private
	return "private-registry"
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
		offset := 0
		for {
			idx := strings.Index(strings.ToLower(result[offset:]), pattern.prefix)
			if idx == -1 {
				break
			}
			idx += offset // Adjust for offset
			start := idx + len(pattern.prefix)
			end := strings.Index(result[start:], pattern.suffix)
			if end == -1 {
				end = len(result) - start
			}
			result = result[:start] + "[REDACTED]" + result[start+end:]
			// Move offset past the redacted section to avoid infinite loop
			offset = start + len("[REDACTED]")
		}
	}

	return result
}
