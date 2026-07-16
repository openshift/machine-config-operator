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
