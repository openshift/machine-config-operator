package server

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"

	v1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
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
	eventRecorder           record.EventRecorder
	machineConfigPoolLister v1.MachineConfigPoolLister
}

// NewClusterFailureReporter creates a FailureReporter for cluster mode
func NewClusterFailureReporter(eventRecorder record.EventRecorder, machineConfigPoolLister v1.MachineConfigPoolLister) FailureReporter {
	return &clusterFailureReporter{
		eventRecorder:           eventRecorder,
		machineConfigPoolLister: machineConfigPoolLister,
	}
}

func (r *clusterFailureReporter) ReportFailure(_ context.Context, report *ctrlcommon.FirstbootFailureReport) error {
	// Attach the event to the live MachineConfigPool rather than a synthetic
	// reference. The real object carries the pool's UID, and leaves the
	// namespace empty because MachineConfigPool is cluster-scoped. Clients
	// select events by name, kind, namespace and UID together, so a synthetic
	// reference missing the UID is never surfaced by "oc describe
	// machineconfigpool".
	pool, err := r.machineConfigPoolLister.Get(report.Pool)
	if err != nil {
		return fmt.Errorf("could not get MachineConfigPool %q to attach firstboot failure event: %w", report.Pool, err)
	}

	message := formatFailureMessage(report)

	// EventRecorder handles event creation, updates, and deduplication
	r.eventRecorder.Event(pool, corev1.EventTypeWarning, "FirstbootFailed", message)

	return nil
}

// formatFailureMessage creates a human-readable event message with sanitized content
func formatFailureMessage(report *ctrlcommon.FirstbootFailureReport) string {
	// The pullspec is reported verbatim. Redacting it would be pointless: the
	// pivot failure is wrapped as "failed to update OS to <pullspec>", so the
	// error message carries it anyway, and OSImageURL is already readable from
	// the MachineConfig.
	imageURL := report.ImageURL
	if imageURL == "" {
		imageURL = "none"
	}
	return fmt.Sprintf("Node %s failed during firstboot at stage '%s': %s (image: %s)",
		report.NodeID, report.Stage, sanitizeErrorMessage(report.ErrorMessage), imageURL)
}

// maxErrorMessageLength bounds how much of the raw error is kept in the event
// message. 1 KiB is enough to retain the actual failure (e.g. a full podman or
// skopeo invocation plus its error) while still bounding event size.
const maxErrorMessageLength = 1024

// Bounds for the remaining fields, all of which arrive from an unauthenticated
// node. The name fields use the DNS-1123 subdomain ceiling.
const (
	maxPoolLength     = 253
	maxNodeIDLength   = 253
	maxStageLength    = 64
	maxImageURLLength = 2048
)

// truncate bounds s to maxLen bytes, dropping a trailing partial rune so the
// result stays valid UTF-8.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return strings.ToValidUTF8(s[:maxLen], "") + "... (truncated)"
}

// sanitizeErrorMessage redacts potential secrets from error messages. Redaction
// runs first so a credential straddling the limit is not cut in half and kept.
func sanitizeErrorMessage(errMsg string) string {
	if errMsg == "" {
		return "unknown error"
	}
	return truncate(redactSecrets(errMsg), maxErrorMessageLength)
}

// sanitizeField bounds an untrusted single-line field and strips the control
// characters a node could use to forge extra log lines or event text.
func sanitizeField(s string, maxLen int) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
	// Trimming leaves a control-only field empty, so validation rejects it.
	return truncate(redactSecrets(strings.TrimSpace(s)), maxLen)
}

// sanitizeReport normalizes the untrusted fields in place. ErrorMessage is left
// to sanitizeErrorMessage: its newlines are content and reach no log line.
func sanitizeReport(report *ctrlcommon.FirstbootFailureReport) {
	report.Pool = sanitizeField(report.Pool, maxPoolLength)
	report.NodeID = sanitizeField(report.NodeID, maxNodeIDLength)
	report.Stage = sanitizeField(report.Stage, maxStageLength)
	report.ImageURL = sanitizeField(report.ImageURL, maxImageURLLength)
}

var (
	// urlUserinfoRe matches a URL's userinfo component (scheme://user:pass@host).
	// This is the one credential form that realistically reaches us: the proxy
	// settings in /etc/mco/proxy.env may embed credentials and the firstboot unit
	// loads that file. The character class excludes "/" so that image digests
	// such as quay.io/repo@sha256:... are left intact.
	urlUserinfoRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)[^\s/@]+@`)
	// bearerTokenRe matches a bearer token, with or without a preceding
	// Authorization header.
	bearerTokenRe = regexp.MustCompile(`(?i)bearer\s+\S+`)
)

// redactSecrets removes credential patterns from error text. It deliberately
// covers only the two forms that can appear here: broader keyword matching
// over-redacts, swallowing the remainder of the message, without catching
// anything extra.
func redactSecrets(s string) string {
	s = urlUserinfoRe.ReplaceAllString(s, "${1}[REDACTED]@")
	return bearerTokenRe.ReplaceAllString(s, "bearer [REDACTED]")
}
