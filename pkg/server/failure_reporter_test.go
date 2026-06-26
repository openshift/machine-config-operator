package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
)

// mockFailureReporter for testing
type mockFailureReporter struct {
	reports []*ctrlcommon.FirstbootFailureReport
	err     error
}

func (m *mockFailureReporter) ReportFailure(ctx context.Context, report *ctrlcommon.FirstbootFailureReport) error {
	m.reports = append(m.reports, report)
	return m.err
}

func TestNodeFailureHandler_POST(t *testing.T) {
	reporter := &mockFailureReporter{}
	handler := NewNodeFailureHandler(reporter)

	report := ctrlcommon.FirstbootFailureReport{
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

	report := ctrlcommon.FirstbootFailureReport{
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

	report := ctrlcommon.FirstbootFailureReport{
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
			name:     "proxy URL with embedded credentials",
			input:    "proxyconnect: http://bob:hunter2@proxy.corp:3128 refused",
			expected: "proxyconnect: http://[REDACTED]@proxy.corp:3128 refused",
		},
		{
			name:     "registry URL with embedded credentials",
			input:    "source docker://user:s3cr3t@registry.internal/ocp:latest: auth failed",
			expected: "source docker://[REDACTED]@registry.internal/ocp:latest: auth failed",
		},
		{
			name:     "message with bearer token",
			input:    "Authorization failed bearer eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9 invalid",
			expected: "Authorization failed bearer [REDACTED] invalid",
		},
		{
			name:     "image digest is not mistaken for userinfo",
			input:    "failed to update OS to docker://quay.io/openshift/release@sha256:abc123 after retries",
			expected: "failed to update OS to docker://quay.io/openshift/release@sha256:abc123 after retries",
		},
		{
			name:     "incidental key= is left alone",
			input:    "exec: --tls-private-key=/etc/pki/tls/private/x.pem failed",
			expected: "exec: --tls-private-key=/etc/pki/tls/private/x.pem failed",
		},
		{
			name:     "non-ASCII input is handled without mangling",
			input:    "Ⱥkey= failed",
			expected: "Ⱥkey= failed",
		},
		{
			name:     "message under the limit is kept whole",
			input:    strings.Repeat("a", maxErrorMessageLength),
			expected: strings.Repeat("a", maxErrorMessageLength),
		},
		{
			name:     "very long message",
			input:    strings.Repeat("a", maxErrorMessageLength+1),
			expected: strings.Repeat("a", maxErrorMessageLength) + "... (truncated)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeErrorMessage(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFormatFailureMessage(t *testing.T) {
	tests := []struct {
		name     string
		report   *ctrlcommon.FirstbootFailureReport
		expected string
	}{
		{
			name: "normal report",
			report: &ctrlcommon.FirstbootFailureReport{
				NodeID:       "node-1",
				Stage:        "pivot",
				ErrorMessage: "Failed to pivot",
				ImageURL:     "quay.io/openshift/image:latest",
			},
			expected: "Node node-1 failed during firstboot at stage 'pivot': Failed to pivot (image: quay.io/openshift/image:latest)",
		},
		{
			name: "report with disconnected registry",
			report: &ctrlcommon.FirstbootFailureReport{
				NodeID:       "node-2",
				Stage:        "updateLayeredOS",
				ErrorMessage: "Pull failed",
				ImageURL:     "registry.example.internal/ocp/release@sha256:abc123",
			},
			expected: "Node node-2 failed during firstboot at stage 'updateLayeredOS': Pull failed (image: registry.example.internal/ocp/release@sha256:abc123)",
		},
		{
			name: "report with internal registry",
			report: &ctrlcommon.FirstbootFailureReport{
				NodeID:       "node-3",
				Stage:        "pivot",
				ErrorMessage: "Network timeout",
				ImageURL:     "image-registry.openshift-image-registry.svc:5000/test/image:v1",
			},
			expected: "Node node-3 failed during firstboot at stage 'pivot': Network timeout (image: image-registry.openshift-image-registry.svc:5000/test/image:v1)",
		},
		{
			name: "report with secret in error message",
			report: &ctrlcommon.FirstbootFailureReport{
				NodeID:       "node-4",
				Stage:        "kargs",
				ErrorMessage: "Auth failed for http://admin:hunter2@proxy.corp:3128",
				ImageURL:     "",
			},
			expected: "Node node-4 failed during firstboot at stage 'kargs': Auth failed for http://[REDACTED]@proxy.corp:3128 (image: none)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatFailureMessage(tt.report)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// notFoundMCPLister reports every pool as missing. The shared mockMCPLister
// returns a nil pool and a nil error instead, which is not how a real lister
// behaves on a cache miss.
type notFoundMCPLister struct{}

func (notFoundMCPLister) List(_ labels.Selector) ([]*mcfgv1.MachineConfigPool, error) {
	return nil, nil
}

func (notFoundMCPLister) Get(name string) (*mcfgv1.MachineConfigPool, error) {
	return nil, apierrors.NewNotFound(schema.GroupResource{
		Group:    mcfgv1.GroupName,
		Resource: "machineconfigpools",
	}, name)
}

// eventDrainTimeout bounds the wait for any event beyond the expected ones.
// The recorder dispatches asynchronously, but it has already queued the send by
// the time the call under test returns, so this only needs to cover scheduling.
const eventDrainTimeout = 500 * time.Millisecond

// recordEvents runs fn against a recorder wired to a live broadcaster and
// returns the events it emitted, waiting for want of them before draining for
// any extras. Going through a real broadcaster rather than a stub keeps the
// reference resolution that fills in the involved object's kind, namespace and
// UID, which is the part worth asserting on.
func recordEvents(t *testing.T, want int, fn func(record.EventRecorder)) []*corev1.Event {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, mcfgv1.AddToScheme(scheme))

	broadcaster := record.NewBroadcaster()
	defer broadcaster.Shutdown()

	recorded := make(chan *corev1.Event, 10)
	watcher := broadcaster.StartEventWatcher(func(e *corev1.Event) { recorded <- e })
	defer watcher.Stop()

	fn(broadcaster.NewRecorder(scheme, corev1.EventSource{Component: "machine-config-server"}))

	var events []*corev1.Event
	for {
		timeout := eventDrainTimeout
		if len(events) < want {
			timeout = 10 * time.Second
		}
		select {
		case e := <-recorded:
			events = append(events, e)
		case <-time.After(timeout):
			return events
		}
	}
}

func TestClusterFailureReporter_ReportFailure(t *testing.T) {
	pool := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{
			Name: "infra",
			UID:  types.UID("e6e0a0d1-8d1f-4a2f-9f0e-2b7a6c5d4e3f"),
		},
	}

	report := &ctrlcommon.FirstbootFailureReport{
		Pool:         "infra",
		NodeID:       "test-node-1",
		Stage:        "firstboot-update",
		ImageURL:     "quay.io/openshift/release@sha256:abc123",
		ErrorMessage: "failed to update OS",
	}

	t.Run("creates a warning event on the pool", func(t *testing.T) {
		var reportErr error
		events := recordEvents(t, 1, func(recorder record.EventRecorder) {
			reporter := NewClusterFailureReporter(recorder, &mockMCPLister{pools: []*mcfgv1.MachineConfigPool{pool}})
			reportErr = reporter.ReportFailure(context.Background(), report)
		})

		require.NoError(t, reportErr)
		require.Len(t, events, 1)
		event := events[0]

		assert.Equal(t, "FirstbootFailed", event.Reason)
		assert.Equal(t, corev1.EventTypeWarning, event.Type)
		assert.Equal(t, formatFailureMessage(report), event.Message)

		// The event has to be discoverable from the pool itself. Clients such as
		// "oc describe machineconfigpool" select events by name, kind, namespace
		// and UID together, so a reference that sets a namespace on this
		// cluster-scoped object, or that omits the UID, is silently never
		// surfaced there.
		assert.Equal(t, "MachineConfigPool", event.InvolvedObject.Kind)
		assert.Equal(t, pool.Name, event.InvolvedObject.Name)
		assert.Empty(t, event.InvolvedObject.Namespace, "MachineConfigPool is cluster scoped, so involvedObject.namespace must be empty")
		assert.Equal(t, pool.UID, event.InvolvedObject.UID, "event must carry the pool UID to be associated with it")
	})

	t.Run("reports an error and emits nothing for an unknown pool", func(t *testing.T) {
		// A node can name any pool it likes, so an unknown one must not produce a
		// dangling event against a reference the server had to invent.
		var reportErr error
		events := recordEvents(t, 0, func(recorder record.EventRecorder) {
			reporter := NewClusterFailureReporter(recorder, notFoundMCPLister{})
			reportErr = reporter.ReportFailure(context.Background(), report)
		})

		assert.Error(t, reportErr)
		assert.Empty(t, events)
	})
}
