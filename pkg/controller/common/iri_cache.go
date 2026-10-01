package common

import (
	"context"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
)

// IRIResourceName is the plural resource name of InternalReleaseImage, as it
// appears in discovery.
const IRIResourceName = "internalreleaseimages"

// IRICacheSyncTimeout is the default bound on how long a controller waits for
// the optional InternalReleaseImage caches before starting without them.
const IRICacheSyncTimeout = 30 * time.Second

// IRICRDServed reports whether the API server serves InternalReleaseImage. The
// CRD is not installed on every cluster, and where it is absent its informers
// can never sync, so a controller uses this to decide whether waiting for those
// caches is worth doing at all. A discovery failure counts as not served:
// starting without the IRI credentials is always recoverable, because the IRI
// informer re-enqueues a render as soon as it does sync.
func IRICRDServed(discoveryClient discovery.DiscoveryInterface) bool {
	groupVersion := mcfgv1.SchemeGroupVersion.String()
	resources, err := discoveryClient.ServerResourcesForGroupVersion(groupVersion)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			klog.Warningf("Could not discover %s resources, assuming InternalReleaseImage is not served: %v", groupVersion, err)
		}
		return false
	}
	for _, resource := range resources.APIResources {
		if resource.Name == IRIResourceName {
			return true
		}
	}
	return false
}

// WaitForIRICaches waits up to timeout for the IRI informer caches, reporting
// whether they synced. Unlike a bare cache.WaitForCacheSync it always returns,
// so a CRD that is served but whose informers are slow (or that is removed
// between the discovery check and here) cannot wedge the caller's Run.
//
// The timeout is a parameter rather than a mutable package variable so that
// tests can shorten it without the knob becoming public mutable state; callers
// that have no reason to pick their own should pass IRICacheSyncTimeout.
func WaitForIRICaches(ctx context.Context, timeout time.Duration, synced ...cache.InformerSynced) bool {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return cache.WaitForCacheSync(waitCtx.Done(), synced...)
}
