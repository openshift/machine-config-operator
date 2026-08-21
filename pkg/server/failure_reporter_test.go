package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockFailureReporter for testing
type mockFailureReporter struct {
	reports []*FirstbootFailureReport
	err     error
}

func (m *mockFailureReporter) ReportFailure(ctx context.Context, report *FirstbootFailureReport) error {
	m.reports = append(m.reports, report)
	return m.err
}

func TestNodeFailureHandler_POST(t *testing.T) {
	reporter := &mockFailureReporter{}
	handler := NewNodeFailureHandler(reporter)

	report := FirstbootFailureReport{
		Pool:         "worker",
		NodeID:       "test-node-1",
		Stage:        "updateLayeredOS",
		ImageURL:     "quay.io/test:latest",
		ErrorMessage: "Image pull failed",
	}

	body, err := json.Marshal(report)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v1/node-failure", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, 1, len(reporter.reports))
	assert.Equal(t, "worker", reporter.reports[0].Pool)
	assert.Equal(t, "test-node-1", reporter.reports[0].NodeID)
}

func TestNodeFailureHandler_MethodNotAllowed(t *testing.T) {
	handler := NewNodeFailureHandler(&mockFailureReporter{})

	req := httptest.NewRequest(http.MethodGet, "/v1/node-failure", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestNodeFailureHandler_InvalidJSON(t *testing.T) {
	handler := NewNodeFailureHandler(&mockFailureReporter{})

	req := httptest.NewRequest(http.MethodPost, "/v1/node-failure", bytes.NewReader([]byte("invalid json")))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestNodeFailureHandler_MissingFields(t *testing.T) {
	handler := NewNodeFailureHandler(&mockFailureReporter{})

	report := FirstbootFailureReport{
		Pool: "worker",
		// Missing NodeID and Stage
	}

	body, _ := json.Marshal(report)
	req := httptest.NewRequest(http.MethodPost, "/v1/node-failure", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestNodeFailureHandler_ReporterError(t *testing.T) {
	// Even if reporter fails, handler should return 202 (best-effort)
	reporter := &mockFailureReporter{err: fmt.Errorf("reporter error")}
	handler := NewNodeFailureHandler(reporter)

	report := FirstbootFailureReport{
		Pool:   "worker",
		NodeID: "test",
		Stage:  "pivot",
	}

	body, _ := json.Marshal(report)
	req := httptest.NewRequest(http.MethodPost, "/v1/node-failure", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// Still returns 202 even though reporter failed
	assert.Equal(t, http.StatusAccepted, w.Code)
}

func TestSanitizeErrorMessage(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty message",
			input:    "",
			expected: "unknown error",
		},
		{
			name:     "normal message",
			input:    "Image pull failed",
			expected: "Image pull failed",
		},
		{
			name:     "message with token",
			input:    "Failed to authenticate with token=abc123&other=value",
			expected: "Failed to authenticate with token=[REDACTED]&other=value",
		},
		{
			name:     "message with password",
			input:    "Login failed: password=secret123&user=admin",
			expected: "Login failed: password=[REDACTED]&user=admin",
		},
		{
			name:     "message with bearer token",
			input:    "Authorization failed bearer eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9 invalid",
			expected: "Authorization failed bearer [REDACTED] invalid",
		},
		{
			name:     "very long message",
			input:    "This is a very long error message that exceeds the maximum length limit and should be truncated to avoid bloating the event storage with unnecessary details that are not helpful for debugging and this text keeps going on and on with more content",
			expected: "This is a very long error message that exceeds the maximum length limit and should be truncated to avoid bloating the event storage with unnecessary details that are not helpful for debugging and this... (truncated)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeErrorMessage(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSanitizeImageURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty URL",
			input:    "",
			expected: "none",
		},
		{
			name:     "invalid URL",
			input:    "not a valid url",
			expected: "invalid-url",
		},
		{
			name:     "internal registry",
			input:    "image-registry.openshift-image-registry.svc:5000/openshift/custom:latest",
			expected: "internal-registry",
		},
		{
			name:     "external registry - quay",
			input:    "quay.io/openshift/custom:latest",
			expected: "quay.io",
		},
		{
			name:     "URL with credentials",
			input:    "https://user:password@registry.example.com/repo/image:tag",
			expected: "registry.example.com",
		},
		{
			name:     "URL with query parameters",
			input:    "https://registry.example.com/repo?token=secret&key=value",
			expected: "registry.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeImageURL(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFormatFailureMessage(t *testing.T) {
	tests := []struct {
		name     string
		report   *FirstbootFailureReport
		expected string
	}{
		{
			name: "normal report",
			report: &FirstbootFailureReport{
				NodeID:       "node-1",
				Stage:        "pivot",
				ErrorMessage: "Failed to pivot",
				ImageURL:     "quay.io/openshift/image:latest",
			},
			expected: "Node node-1 failed during firstboot at stage 'pivot': Failed to pivot (image: quay.io)",
		},
		{
			name: "report with credentials in URL",
			report: &FirstbootFailureReport{
				NodeID:       "node-2",
				Stage:        "updateLayeredOS",
				ErrorMessage: "Pull failed",
				ImageURL:     "https://user:pass@registry.com/repo:tag",
			},
			expected: "Node node-2 failed during firstboot at stage 'updateLayeredOS': Pull failed (image: registry.com)",
		},
		{
			name: "report with internal registry",
			report: &FirstbootFailureReport{
				NodeID:       "node-3",
				Stage:        "pivot",
				ErrorMessage: "Network timeout",
				ImageURL:     "image-registry.openshift-image-registry.svc:5000/test/image:v1",
			},
			expected: "Node node-3 failed during firstboot at stage 'pivot': Network timeout (image: internal-registry)",
		},
		{
			name: "report with secret in error message",
			report: &FirstbootFailureReport{
				NodeID:       "node-4",
				Stage:        "kargs",
				ErrorMessage: "Auth failed with token=abc123&other=data",
				ImageURL:     "",
			},
			expected: "Node node-4 failed during firstboot at stage 'kargs': Auth failed with token=[REDACTED]&other=data (image: none)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatFailureMessage(tt.report)
			assert.Equal(t, tt.expected, result)
		})
	}
}
