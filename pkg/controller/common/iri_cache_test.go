package common

import (
	"context"
	"fmt"
	"testing"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	core "k8s.io/client-go/testing"
)

// alwaysReady stands in for an informer cache that has already synced.
var alwaysReady = func() bool { return true }

// testIRICacheSyncTimeout keeps these tests fast. Production callers pass
// IRICacheSyncTimeout instead.
const testIRICacheSyncTimeout = 50 * time.Millisecond

// TestWaitForIRICaches pins the backstop behind the discovery gate its callers
// use: even once the CRD is known to be served, the wait is bounded, so an
// informer that never syncs cannot wedge the calling controller and block every
// MachineConfig from rendering.
func TestWaitForIRICaches(t *testing.T) {
	neverSyncs := func() bool { return false }

	t.Run("returns true once caches sync", func(t *testing.T) {
		if !WaitForIRICaches(context.Background(), testIRICacheSyncTimeout, alwaysReady, alwaysReady) {
			t.Error("WaitForIRICaches() = false, want true when both caches are synced")
		}
	})

	t.Run("gives up instead of blocking when a cache never syncs", func(t *testing.T) {
		done := make(chan bool, 1)
		go func() {
			done <- WaitForIRICaches(context.Background(), testIRICacheSyncTimeout, alwaysReady, neverSyncs)
		}()

		select {
		case synced := <-done:
			if synced {
				t.Error("WaitForIRICaches() = true, want false when a cache never syncs")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("WaitForIRICaches blocked past the timeout; Run would wedge on a cluster without the IRI CRD")
		}
	})

	t.Run("returns when the parent context is cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if WaitForIRICaches(ctx, testIRICacheSyncTimeout, neverSyncs) {
			t.Error("WaitForIRICaches() = true, want false on a cancelled context")
		}
	})
}

// TestIRICRDServed covers the discovery gate callers use to decide whether
// waiting for the InternalReleaseImage caches is worth doing. Anything short of
// the resource being positively discovered has to read as "not served",
// otherwise the caller spends its whole timeout budget waiting on caches that
// can never sync.
func TestIRICRDServed(t *testing.T) {
	mcfgGroupVersion := mcfgv1.SchemeGroupVersion.String()

	tests := []struct {
		name      string
		resources []*metav1.APIResourceList
		reactor   core.ReactionFunc
		want      bool
	}{
		{
			name: "resource is discovered",
			resources: []*metav1.APIResourceList{{
				GroupVersion: mcfgGroupVersion,
				APIResources: []metav1.APIResource{
					{Name: "controllerconfigs"},
					{Name: IRIResourceName},
				},
			}},
			want: true,
		},
		{
			name: "group version is served without the resource",
			resources: []*metav1.APIResourceList{{
				GroupVersion: mcfgGroupVersion,
				APIResources: []metav1.APIResource{{Name: "controllerconfigs"}},
			}},
			want: false,
		},
		{
			name:      "group version is not served at all",
			resources: nil,
			want:      false,
		},
		{
			name: "discovery fails",
			reactor: func(core.Action) (bool, runtime.Object, error) {
				return true, nil, fmt.Errorf("the server is currently unable to handle the request")
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := k8sfake.NewSimpleClientset()
			client.Resources = tt.resources
			if tt.reactor != nil {
				client.PrependReactor("get", "resource", tt.reactor)
			}

			if got := IRICRDServed(client.Discovery()); got != tt.want {
				t.Errorf("IRICRDServed() = %v, want %v", got, tt.want)
			}
		})
	}
}
