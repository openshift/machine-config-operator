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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
