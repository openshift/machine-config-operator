package daemon

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/workqueue"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
)

// newConnRefusedError builds an error shaped like the ones client-go returns when
// the API server is down: *url.Error -> *net.OpError -> *os.SyscallError -> errno.
// The rendered message matches the real-world "dial tcp 172.30.0.1:443: connect:
// connection refused" seen during SNO upgrades.
func newConnRefusedError(errno syscall.Errno) error {
	return &url.Error{
		Op:  "Get",
		URL: "https://172.30.0.1:443/apis/machineconfiguration.openshift.io/v1/machineconfignodes/node-0",
		Err: &net.OpError{
			Op:   "dial",
			Net:  "tcp",
			Addr: &net.TCPAddr{IP: net.IPv4(172, 30, 0, 1), Port: 443},
			Err:  os.NewSyscallError("connect", errno),
		},
	}
}

func TestIsAPIServerUnreachableError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "unrelated error", err: errors.New("something else went wrong"), want: false},
		{name: "real API NotFound error", err: apierrors.NewNotFound(schema.GroupResource{Group: "machineconfiguration.openshift.io", Resource: "machineconfignodes"}, "node-0"), want: false},
		{name: "unrelated errno (EPERM)", err: syscall.EPERM, want: false},

		{name: "bare ECONNREFUSED", err: syscall.ECONNREFUSED, want: true},
		{name: "wrapped ECONNREFUSED", err: fmt.Errorf("apply status failed: %w", syscall.ECONNREFUSED), want: true},
		{name: "client-go style connection refused", err: newConnRefusedError(syscall.ECONNREFUSED), want: true},
		{name: "connection reset by peer", err: newConnRefusedError(syscall.ECONNRESET), want: true},
		{name: "host unreachable", err: newConnRefusedError(syscall.EHOSTUNREACH), want: true},
		{name: "network unreachable", err: newConnRefusedError(syscall.ENETUNREACH), want: true},
		{name: "wrapped host unreachable", err: fmt.Errorf("dialing: %w", syscall.EHOSTUNREACH), want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isAPIServerUnreachableError(tc.err))
		})
	}
}

func TestShouldReportUnreachable(t *testing.T) {
	// True only on the first failure and on powers of two thereafter.
	wantTrue := map[int]bool{0: true, 1: true, 2: true, 4: true, 8: true, 16: true, 32: true, 64: true}
	for n := 0; n <= 64; n++ {
		got := shouldReportUnreachable(n)
		if wantTrue[n] {
			assert.Truef(t, got, "shouldReportUnreachable(%d) should be true (power of two)", n)
		} else {
			assert.Falsef(t, got, "shouldReportUnreachable(%d) should be false", n)
		}
	}
}

func TestShouldReportSyncErrorBackoff(t *testing.T) {
	connErr := newConnRefusedError(syscall.ECONNREFUSED)

	t.Run("collapses a long outage to a handful of reports", func(t *testing.T) {
		dn := &Daemon{}

		// Simulate a ~5-10 minute API outage: 28 consecutive connection-refused sync
		// failures (the count observed tripping the pathological-events monitor).
		const outageFailures = 28
		var reportedAt []int
		for i := 1; i <= outageFailures; i++ {
			if dn.shouldReportSyncError(connErr) {
				reportedAt = append(reportedAt, i)
			}
		}

		// Without backoff this would emit 28 failure events; with exponential backoff
		// we report only on the 1st, 2nd, 4th, 8th and 16th consecutive failure.
		assert.Equal(t, []int{1, 2, 4, 8, 16}, reportedAt)
		assert.Lenf(t, reportedAt, 5, "expected a handful of reports, got %d for %d failures", len(reportedAt), outageFailures)
		assert.Less(t, len(reportedAt), outageFailures/4, "backoff should drastically reduce the number of reports")
	})

	t.Run("non-connectivity error is always reported and resets the counter", func(t *testing.T) {
		dn := &Daemon{}

		// Build up some consecutive connection failures.
		assert.True(t, dn.shouldReportSyncError(connErr))  // failure 1 -> report
		assert.True(t, dn.shouldReportSyncError(connErr))  // failure 2 -> report
		assert.False(t, dn.shouldReportSyncError(connErr)) // failure 3 -> suppressed
		assert.Equal(t, 3, dn.apiUnreachableFailures)

		// A genuine, actionable error must always be reported and must reset the
		// backoff so the next outage starts fresh.
		assert.True(t, dn.shouldReportSyncError(errors.New("reconcile failed")))
		assert.Equal(t, 0, dn.apiUnreachableFailures)

		// The connection-refused schedule restarts from the first failure.
		assert.True(t, dn.shouldReportSyncError(connErr)) // failure 1 again -> report
		assert.Equal(t, 1, dn.apiUnreachableFailures)
	})

	t.Run("nil error resets the counter and is treated as success", func(t *testing.T) {
		dn := &Daemon{apiUnreachableFailures: 7}
		// isAPIServerUnreachableError(nil) is false, so the counter resets.
		assert.True(t, dn.shouldReportSyncError(nil))
		assert.Equal(t, 0, dn.apiUnreachableFailures)
	})
}

// fakeNodeWriter is a minimal NodeWriter used to drive handleErr in tests. Its
// SetDegraded deliberately returns an error so updateErrorState short-circuits
// before reaching cluster-dependent code (primary-pool lookup and MCN status
// updates). The apiUnreachableFailures bookkeeping under test happens in
// handleErr itself -- for SNO via shouldReportSyncError, and for HA via the
// explicit reset -- both of which run before updateErrorState, so short-circuiting
// the annotation write does not affect what these tests verify.
type fakeNodeWriter struct{}

func (f *fakeNodeWriter) Run(_ <-chan struct{})            {}
func (f *fakeNodeWriter) SetDone(_ *stateAndConfigs) error { return nil }
func (f *fakeNodeWriter) SetWorking() error                { return nil }
func (f *fakeNodeWriter) SetUnreconcilable(_ error) error  { return nil }
func (f *fakeNodeWriter) SetDegraded(_ error) error {
	return errors.New("fake node writer: annotation writes are not wired up in this test")
}
func (f *fakeNodeWriter) SetAnnotations(_ map[string]string) (*corev1.Node, error) { return nil, nil }
func (f *fakeNodeWriter) SetDesiredDrainer(_ string) error                         { return nil }
func (f *fakeNodeWriter) Eventf(_, _, _ string, _ ...interface{})                  {}

// TestHandleErrTopologyTransitionResetsBackoff is a regression test for a
// SNO -> HA -> SNO control-plane topology transition. The node controller can
// rewrite the controlPlaneTopology annotation during the daemon's lifetime, and
// handleErr reads it fresh on every call via getControlPlaneTopology(). Before
// the fix, the API-unreachable backoff counter accumulated on SNO was never
// cleared when the cluster became HA, so a later return to SNO would resume the
// exponential backoff from a stale count and wrongly suppress or delay
// legitimate MachineConfigNode failure reporting. handleErr must reset the
// counter whenever the HA branch handles a sync error.
func TestHandleErrTopologyTransitionResetsBackoff(t *testing.T) {
	connErr := newConnRefusedError(syscall.ECONNREFUSED)

	// The daemon derives its topology from the node annotation, so mutating the
	// annotation on this shared node object simulates the node controller flipping
	// the cluster topology at runtime.
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "node-0",
			Annotations: map[string]string{},
		},
	}
	dn := &Daemon{
		node:       node,
		nodeWriter: &fakeNodeWriter{},
		queue: workqueue.NewTypedRateLimitingQueueWithConfig[string](
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "backoff-topology-transition-test"}),
	}
	defer dn.queue.ShutDown()

	setTopology := func(mode configv1.TopologyMode) {
		node.Annotations[constants.ClusterControlPlaneTopologyAnnotationKey] = string(mode)
	}

	const key = "test/node-0"

	// 1. SNO: a multi-minute API outage accumulates the backoff counter across
	//    consecutive connection-refused sync failures.
	setTopology(configv1.SingleReplicaTopologyMode)
	const snoOutageFailures = 10
	for i := 0; i < snoOutageFailures; i++ {
		dn.handleErr(connErr, key)
	}
	require.Equal(t, snoOutageFailures, dn.apiUnreachableFailures,
		"SNO outage should accumulate the API-unreachable backoff counter")

	// 2. Topology flips to HA (node controller rewrote the annotation). Handling a
	//    sync error on HA must clear the stale SNO backoff counter, because HA
	//    reports every sync error immediately and must not carry SNO backoff state.
	setTopology(configv1.HighlyAvailableTopologyMode)
	dn.handleErr(connErr, key)
	require.Equal(t, 0, dn.apiUnreachableFailures,
		"HA branch must reset the stale SNO backoff counter (SNO -> HA transition)")

	// 3. Topology flips back to SNO. Because HA cleared the counter, the backoff
	//    schedule starts fresh from the first failure rather than resuming from the
	//    stale pre-transition count.
	setTopology(configv1.SingleReplicaTopologyMode)
	dn.handleErr(connErr, key)
	require.Equal(t, 1, dn.apiUnreachableFailures,
		"SNO backoff must restart fresh after returning from HA, not resume the stale count")
}
