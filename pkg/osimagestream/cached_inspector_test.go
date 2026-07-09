package osimagestream

import (
	"context"
	"errors"
	"testing"

	"github.com/containers/image/v5/types"
	"github.com/openshift/machine-config-operator/pkg/imageutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	digestedImage1 = "quay.io/openshift/os@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestedImage2 = "quay.io/openshift/ext@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	digest1        = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digest2        = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	taggedImage    = "quay.io/openshift/os:latest"
)

type fakeCache struct {
	data map[string]*imageutils.InspectionCacheEntry
	puts []string
}

func newFakeCache() *fakeCache {
	return &fakeCache{data: make(map[string]*imageutils.InspectionCacheEntry)}
}

func (c *fakeCache) Get(digest string) *imageutils.InspectionCacheEntry {
	return c.data[digest]
}

func (c *fakeCache) Put(digest string, entry *imageutils.InspectionCacheEntry) error {
	c.data[digest] = entry
	c.puts = append(c.puts, digest)
	return nil
}

func TestCachedImagesInspector_AllCacheHits(t *testing.T) {
	cache := newFakeCache()
	cache.data[digest1] = &imageutils.InspectionCacheEntry{Labels: map[string]string{"os": "rhel9"}}
	cache.data[digest2] = &imageutils.InspectionCacheEntry{Labels: map[string]string{"ext": "true"}}

	delegate := &mockImagesInspector{err: errors.New("should not be called")}
	inspector := NewCachedImagesInspector(delegate, cache)

	results, err := inspector.Inspect(context.Background(), digestedImage1, digestedImage2)
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, "rhel9", results[0].InspectInfo.Labels["os"])
	assert.Equal(t, "true", results[1].InspectInfo.Labels["ext"])
}

func TestCachedImagesInspector_AllCacheMisses(t *testing.T) {
	cache := newFakeCache()
	delegate := &mockImagesInspector{
		inspectData: map[string]*types.ImageInspectInfo{
			digestedImage1: {Labels: map[string]string{"os": "rhel9"}},
			digestedImage2: {Labels: map[string]string{"ext": "true"}},
		},
	}
	inspector := NewCachedImagesInspector(delegate, cache)

	results, err := inspector.Inspect(context.Background(), digestedImage1, digestedImage2)
	require.NoError(t, err)
	require.Len(t, results, 2)

	assert.Equal(t, "rhel9", results[0].InspectInfo.Labels["os"])
	assert.Equal(t, "true", results[1].InspectInfo.Labels["ext"])
	assert.Contains(t, cache.puts, digest1)
	assert.Contains(t, cache.puts, digest2)
}

func TestCachedImagesInspector_MixedHitsAndMisses(t *testing.T) {
	cache := newFakeCache()
	cache.data[digest1] = &imageutils.InspectionCacheEntry{Labels: map[string]string{"os": "rhel9"}}

	delegate := &mockImagesInspector{
		inspectData: map[string]*types.ImageInspectInfo{
			digestedImage2: {Labels: map[string]string{"ext": "true"}},
		},
	}
	inspector := NewCachedImagesInspector(delegate, cache)

	results, err := inspector.Inspect(context.Background(), digestedImage1, digestedImage2)
	require.NoError(t, err)
	require.Len(t, results, 2)

	assert.Equal(t, "rhel9", results[0].InspectInfo.Labels["os"])
	assert.Equal(t, "true", results[1].InspectInfo.Labels["ext"])
	assert.Equal(t, []string{digest2}, cache.puts)
}

func TestCachedImagesInspector_DuplicateInputs(t *testing.T) {
	cache := newFakeCache()
	delegate := &mockImagesInspector{
		inspectData: map[string]*types.ImageInspectInfo{
			digestedImage1: {Labels: map[string]string{"os": "rhel9"}},
		},
	}
	inspector := NewCachedImagesInspector(delegate, cache)

	results, err := inspector.Inspect(context.Background(), digestedImage1, digestedImage2, digestedImage1)
	require.NoError(t, err)
	require.Len(t, results, 3)
	assert.Equal(t, "rhel9", results[0].InspectInfo.Labels["os"])
	assert.Error(t, results[1].Error)
	assert.Equal(t, "rhel9", results[2].InspectInfo.Labels["os"])
}

func TestCachedImagesInspector_TaggedImageBypassesCache(t *testing.T) {
	cache := newFakeCache()
	delegate := &mockImagesInspector{
		inspectData: map[string]*types.ImageInspectInfo{
			taggedImage: {Labels: map[string]string{"os": "rhel9"}},
		},
	}
	inspector := NewCachedImagesInspector(delegate, cache)

	results, err := inspector.Inspect(context.Background(), taggedImage)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "rhel9", results[0].InspectInfo.Labels["os"])
	assert.Empty(t, cache.puts)
}

func TestCachedImagesInspector_DelegateError(t *testing.T) {
	cache := newFakeCache()
	delegate := &mockImagesInspector{err: errors.New("network failure")}
	inspector := NewCachedImagesInspector(delegate, cache)

	results, err := inspector.Inspect(context.Background(), digestedImage1)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.ErrorContains(t, results[0].Error, "network failure")
}

func TestCachedImagesInspector_DelegateErrorPreservesCacheHits(t *testing.T) {
	cache := newFakeCache()
	cache.data[digest1] = &imageutils.InspectionCacheEntry{Labels: map[string]string{"os": "rhel9"}}

	delegate := &mockImagesInspector{err: errors.New("network failure")}
	inspector := NewCachedImagesInspector(delegate, cache)

	results, err := inspector.Inspect(context.Background(), digestedImage1, digestedImage2)
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, "rhel9", results[0].InspectInfo.Labels["os"])
	assert.ErrorContains(t, results[1].Error, "network failure")
}

func TestCachedImagesInspector_PerImageErrorNotCached(t *testing.T) {
	cache := newFakeCache()
	delegate := &mockImagesInspector{
		inspectData: map[string]*types.ImageInspectInfo{},
	}
	inspector := NewCachedImagesInspector(delegate, cache)

	results, err := inspector.Inspect(context.Background(), digestedImage1)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Error(t, results[0].Error)
	assert.Empty(t, cache.puts)
}

func TestCachedImagesInspector_FetchImageFileCachesResult(t *testing.T) {
	cache := newFakeCache()
	delegate := &mockImagesInspector{
		fileData: map[string][]byte{
			digestedImage1 + ":/etc/os-release": []byte("ID=rhel"),
		},
	}
	inspector := NewCachedImagesInspector(delegate, cache)

	data, err := inspector.FetchImageFile(context.Background(), digestedImage1, "/etc/os-release")
	require.NoError(t, err)
	assert.Equal(t, []byte("ID=rhel"), data)
	assert.Contains(t, cache.puts, digest1)

	// Second call should hit cache, not delegate
	delegate.err = errors.New("should not be called")
	data, err = inspector.FetchImageFile(context.Background(), digestedImage1, "/etc/os-release")
	require.NoError(t, err)
	assert.Equal(t, []byte("ID=rhel"), data)
}

func TestCachedImagesInspector_FetchImageFileTaggedBypassesCache(t *testing.T) {
	cache := newFakeCache()
	delegate := &mockImagesInspector{
		fileData: map[string][]byte{
			taggedImage + ":/etc/os-release": []byte("ID=rhel"),
		},
	}
	inspector := NewCachedImagesInspector(delegate, cache)

	data, err := inspector.FetchImageFile(context.Background(), taggedImage, "/etc/os-release")
	require.NoError(t, err)
	assert.Equal(t, []byte("ID=rhel"), data)
	assert.Empty(t, cache.puts)
}

func TestCachedImagesInspectorFactory(t *testing.T) {
	cache := newFakeCache()
	cache.data[digest1] = &imageutils.InspectionCacheEntry{Labels: map[string]string{"os": "rhel9"}}

	delegate := &mockImagesInspectorFactory{
		inspector: &mockImagesInspector{err: errors.New("should not be called")},
	}
	factory := NewCachedImagesInspectorFactory(delegate, cache)
	inspector := factory.ForContext(nil)

	results, err := inspector.Inspect(context.Background(), digestedImage1)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "rhel9", results[0].InspectInfo.Labels["os"])
}
