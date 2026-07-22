package osimagestream

import (
	"context"
	"github.com/containers/image/v5/types"
	"github.com/openshift/machine-config-operator/pkg/imageutils"
	"k8s.io/klog/v2"
)

// CachedImagesInspector wraps an ImagesInspector and serves label lookups
// from an InspectionCache when the image pullspec contains a digest.
// Cache misses are delegated to the underlying inspector and written back.
type CachedImagesInspector struct {
	delegate ImagesInspector
	cache    imageutils.InspectionCache
}

// NewCachedImagesInspector creates a CachedImagesInspector decorator.
func NewCachedImagesInspector(delegate ImagesInspector, cache imageutils.InspectionCache) *CachedImagesInspector {
	return &CachedImagesInspector{delegate: delegate, cache: cache}
}

func (c *CachedImagesInspector) Inspect(ctx context.Context, images ...string) ([]imageutils.BulkInspectResult, error) {
	results := make([]imageutils.BulkInspectResult, len(images))
	indices := make(map[string][]int, len(images))
	for i, img := range images {
		indices[img] = append(indices[img], i)
	}

	var misses []string
	for img := range indices {
		digest := imageutils.DigestFromPullspec(img)
		if digest == "" {
			misses = append(misses, img)
			continue
		}

		entry := c.cache.Get(digest)
		if entry == nil {
			misses = append(misses, img)
			continue
		}

		klog.V(4).Infof("cache hit for digest %s", digest)
		hit := imageutils.BulkInspectResult{
			Image:       img,
			InspectInfo: &types.ImageInspectInfo{Labels: entry.Labels},
		}
		for _, idx := range indices[img] {
			results[idx] = hit
		}
	}

	if len(misses) == 0 {
		return results, nil
	}

	fetched, err := c.delegate.Inspect(ctx, misses...)
	if err != nil {
		for _, img := range misses {
			for _, idx := range indices[img] {
				results[idx] = imageutils.BulkInspectResult{Image: img, Error: err}
			}
		}
		return results, nil
	}

	for _, r := range fetched {
		for _, idx := range indices[r.Image] {
			results[idx] = r
		}
		if r.Error != nil || r.InspectInfo == nil {
			continue
		}
		digest := imageutils.DigestFromPullspec(r.Image)
		if digest == "" {
			continue
		}
		if err := c.cache.Put(digest, &imageutils.InspectionCacheEntry{Labels: r.InspectInfo.Labels}); err != nil {
			klog.Warningf("failed to cache inspection result for digest %s: %v", digest, err)
		}
	}

	return results, nil
}

func (c *CachedImagesInspector) FetchImageFile(ctx context.Context, image, filePath string) ([]byte, error) {
	digest := imageutils.DigestFromPullspec(image)
	if digest != "" {
		if entry := c.cache.Get(digest); entry != nil && entry.Files != nil {
			if data, ok := entry.Files[filePath]; ok {
				klog.V(4).Infof("cache hit for file %s in digest %s", filePath, digest)
				return data, nil
			}
		}
	}

	data, err := c.delegate.FetchImageFile(ctx, image, filePath)
	if err != nil {
		return nil, err
	}

	if digest != "" {
		if putErr := c.cache.Put(digest, &imageutils.InspectionCacheEntry{
			Files: map[string][]byte{filePath: data},
		}); putErr != nil {
			klog.Warningf("failed to cache file %s for digest %s: %v", filePath, digest, putErr)
		}
	}

	return data, nil
}

// CachedImagesInspectorFactory wraps an ImagesInspectorFactory and injects
// an InspectionCache into every inspector it creates.
type CachedImagesInspectorFactory struct {
	delegate ImagesInspectorFactory
	cache    imageutils.InspectionCache
}

// NewCachedImagesInspectorFactory creates a factory that decorates inspectors with caching.
func NewCachedImagesInspectorFactory(delegate ImagesInspectorFactory, cache imageutils.InspectionCache) *CachedImagesInspectorFactory {
	return &CachedImagesInspectorFactory{delegate: delegate, cache: cache}
}

func (f *CachedImagesInspectorFactory) ForContext(sysCtxFactory imageutils.SysContextFactory) ImagesInspector {
	return NewCachedImagesInspector(f.delegate.ForContext(sysCtxFactory), f.cache)
}
