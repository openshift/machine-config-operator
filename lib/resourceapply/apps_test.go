package resourceapply

import (
	"errors"
	"fmt"
	"net"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestIsApplyErrorRetriable(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		retriable bool
	}{
		{
			name:      "rpc error",
			err:       errors.New("rpc error: connection reset"),
			retriable: true,
		},
		{
			name:      "conflict",
			err:       apierrors.NewConflict(schema.GroupResource{Resource: "machineconfignodes"}, "worker-0", errors.New("conflict")),
			retriable: true,
		},
		{
			name:      "service unavailable",
			err:       fmt.Errorf("wrapped: %w", apierrors.NewServiceUnavailable("unavailable")),
			retriable: true,
		},
		{
			name:      "storage reinitializing",
			err:       errors.New("storage is (re)initializing"),
			retriable: true,
		},
		{
			name:      "too many requests",
			err:       apierrors.NewTooManyRequests("throttled", 1),
			retriable: true,
		},
		{
			name: "wrapped temporary network error",
			err: fmt.Errorf("request failed: %w", &net.DNSError{
				Err:         "temporary failure",
				Name:        "api.example.test",
				IsTemporary: true,
			}),
			retriable: true,
		},
		{
			name: "wrapped timeout network error",
			err: fmt.Errorf("request failed: %w", &net.DNSError{
				Err:       "i/o timeout",
				Name:      "api.example.test",
				IsTimeout: true,
			}),
			retriable: true,
		},
		{
			name: "wrapped permanent DNS error",
			err: fmt.Errorf("request failed: %w", &net.DNSError{
				Err:  "no such host",
				Name: "api.example.test",
			}),
			retriable: false,
		},
		{
			name:      "bad request",
			err:       apierrors.NewBadRequest("invalid request"),
			retriable: false,
		},
		{
			name:      "ordinary error",
			err:       errors.New("permanent failure"),
			retriable: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if actual := IsApplyErrorRetriable(test.err); actual != test.retriable {
				t.Fatalf("expected retriable=%t, got %t for %v", test.retriable, actual, test.err)
			}
		})
	}
}
