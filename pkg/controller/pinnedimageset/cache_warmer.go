package pinnedimageset

import (
	"context"
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/imageutils"
	"github.com/openshift/machine-config-operator/pkg/osimagestream"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
)

const releaseImageStreamPath = "/release-manifests/image-references"

// CacheWarmer proactively warms the inspection cache for images referenced by
// PinnedImageSets. It inspects labels of all PIS images and fetches the
// ImageStream file from release payload images. It also implements CacheEvicter
// to retain PIS-referenced digests.
type CacheWarmer struct {
	pisLister        mcfglistersv1.PinnedImageSetLister
	inspectorFactory osimagestream.ImagesInspectorFactory
	sysCtxFactory    imageutils.SysContextFactory
	signal           chan struct{}
}

// NewCacheWarmer creates a CacheWarmer that will inspect images from PinnedImageSets.
func NewCacheWarmer(
	pisLister mcfglistersv1.PinnedImageSetLister,
	inspectorFactory osimagestream.ImagesInspectorFactory,
	sysCtxFactory imageutils.SysContextFactory,
) *CacheWarmer {
	return &CacheWarmer{
		pisLister:        pisLister,
		inspectorFactory: inspectorFactory,
		sysCtxFactory:    sysCtxFactory,
		signal:           make(chan struct{}, 1),
	}
}

// Retain implements CacheEvicter — it retains all digests referenced by any PinnedImageSet.
func (w *CacheWarmer) Retain(digests []string) []string {
	pisList, err := w.pisLister.List(labels.Everything())
	if err != nil {
		return digests
	}

	active := sets.New[string]()
	for _, pis := range pisList {
		for _, ref := range pis.Spec.PinnedImages {
			if d := imageutils.DigestFromPullspec(string(ref.Name)); d != "" {
				active.Insert(d)
			}
		}
	}

	return sets.New(digests...).Intersection(active).UnsortedList()
}

// Start runs the warming loop, consuming coalesced signals from requestWarm.
// It blocks until stopCh is closed.
func (w *CacheWarmer) Start(stopCh <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-stopCh
		cancel()
	}()

	w.requestWarm()

	klog.Info("Starting PinnedImageSet cache warmer")
	defer klog.Info("Shutting down PinnedImageSet cache warmer")

	for {
		select {
		case <-w.signal:
			if err := w.Warm(ctx); err != nil {
				klog.Warningf("PinnedImageSet cache warming failed: %v", err)
			}
		case <-stopCh:
			return
		}
	}
}

func (w *CacheWarmer) requestWarm() {
	select {
	case w.signal <- struct{}{}:
	default:
	}
}

// Warm performs a single cache warming cycle. It inspects all PIS image labels
// and fetches the ImageStream file from any release payload images found.
func (w *CacheWarmer) Warm(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, osimagestream.ImageInspectionTimeout)
	defer cancel()

	pisList, err := w.pisLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("listing PinnedImageSets: %w", err)
	}

	images := collectPISImages(pisList)
	if len(images) == 0 {
		return nil
	}

	inspector := w.inspectorFactory.ForContext(w.sysCtxFactory)

	results, err := inspector.Inspect(ctx, images...)
	if err != nil {
		return fmt.Errorf("inspecting PIS images: %w", err)
	}

	klog.V(4).Infof("PIS cache warmer: inspected %d images", len(results))

	for _, r := range results {
		if r.Error != nil || r.InspectInfo == nil || !osimagestream.IsReleasePayload(r.InspectInfo.Labels) {
			continue
		}
		// Release payload images contain the ImageStream manifest at a well-known
		// path. Fetching it here warms the file in the inspection cache so that
		// later consumers (e.g. the osimagestream controller) get a cache hit.
		if _, err := inspector.FetchImageFile(ctx, r.Image, releaseImageStreamPath); err != nil {
			klog.Warningf("PIS cache warmer: failed to fetch image-references from %s: %v", r.Image, err)
		}
	}

	return nil
}

func collectPISImages(pisList []*mcfgv1.PinnedImageSet) []string {
	seen := sets.New[string]()
	var images []string
	for _, pis := range pisList {
		for _, ref := range pis.Spec.PinnedImages {
			img := string(ref.Name)
			if seen.Has(img) {
				continue
			}
			seen.Insert(img)
			images = append(images, img)
		}
	}
	return images
}
