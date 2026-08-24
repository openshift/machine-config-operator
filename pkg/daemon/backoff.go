package daemon

import (
	"errors"
	"syscall"

	utilnet "k8s.io/apimachinery/pkg/util/net"
)

// isAPIServerUnreachableError reports whether err indicates that the API server is
// currently unreachable over the network, as opposed to a genuine, actionable
// reconcile error.
//
// The canonical case is a single-node OpenShift (SNO) cluster whose only node
// reboots during an upgrade: the kube-apiserver goes away with it, so every sync
// attempt fails with "dial tcp <service-ip>:443: connect: connection refused" until
// the API server comes back. Host- and network-unreachable errors show up the same
// way while the node's networking is still coming up after the reboot. These are
// transient connectivity failures that should be backed off rather than surfaced as
// a node-degraded failure event on every single attempt.
func isAPIServerUnreachableError(err error) bool {
	if err == nil {
		return false
	}

	// client-go surfaces these wrapped in *url.Error/*net.OpError chains; utilnet
	// unwraps that chain and matches the underlying syscall errno.
	if utilnet.IsConnectionRefused(err) || utilnet.IsConnectionReset(err) {
		return true
	}

	// Host/network unreachable are the other transient forms of "the API server is
	// not reachable right now" that occur while a rebooted node re-establishes its
	// networking.
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.EHOSTUNREACH || errno == syscall.ENETUNREACH
	}

	return false
}

// shouldReportUnreachable implements an exponential backoff schedule for
// re-reporting a node's degraded state while the API server is unreachable. Given
// the number of consecutive connection failures, it returns true only for the first
// failure and thereafter on powers of two (1, 2, 4, 8, 16, ...).
//
// Combined with the sync queue's own exponential rate limiter (which spaces out the
// retries themselves), this collapses a multi-minute outage that would otherwise
// emit one MachineConfigNode failure event per sync attempt (dozens of identical
// events, tripping the pathological-events monitor) down to a handful of events.
func shouldReportUnreachable(consecutiveFailures int) bool {
	if consecutiveFailures <= 1 {
		return true
	}
	// A positive integer is a power of two iff exactly one bit is set, i.e.
	// n & (n-1) == 0.
	return consecutiveFailures&(consecutiveFailures-1) == 0
}

// shouldReportSyncError is the decision core of handleErr's exponential backoff for
// API-unreachable errors. It updates the consecutive-failure counter for err and
// returns whether the caller should (re-)report the error this time.
//
// Non-connectivity errors reset the counter and are always reported (they represent
// real, actionable failures). Connectivity errors increment the counter and are only
// reported on the exponential schedule defined by shouldReportUnreachable.
//
// It is only called from the single sync worker goroutine, so the counter needs no
// additional locking.
func (dn *Daemon) shouldReportSyncError(err error) bool {
	if !isAPIServerUnreachableError(err) {
		dn.apiUnreachableFailures = 0
		return true
	}
	dn.apiUnreachableFailures++
	return shouldReportUnreachable(dn.apiUnreachableFailures)
}
