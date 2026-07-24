package pinnedimageset

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/containers/image/v5/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakemco "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	mcfginformers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
	"github.com/openshift/machine-config-operator/pkg/imageutils"
	"github.com/openshift/machine-config-operator/pkg/osimagestream"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const (
	testOSImage       = "registry.example.com/os@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testReleaseImage  = "registry.example.com/release@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testOperatorImage = "registry.example.com/operator@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

type fakeInspector struct {
	inspectData map[string]*types.ImageInspectInfo
	fileData    map[string][]byte
}

func (f *fakeInspector) Inspect(_ context.Context, images ...string) ([]imageutils.BulkInspectResult, error) {
	var results []imageutils.BulkInspectResult
	for _, img := range images {
		r := imageutils.BulkInspectResult{Image: img}
		if info, ok := f.inspectData[img]; ok {
			r.InspectInfo = info
		}
		results = append(results, r)
	}
	return results, nil
}

func (f *fakeInspector) FetchImageFile(_ context.Context, image, path string) ([]byte, error) {
	if data, ok := f.fileData[image+":"+path]; ok {
		return data, nil
	}
	return nil, nil
}

type fakeInspectorFactory struct {
	inspector osimagestream.ImagesInspector
}

func (f *fakeInspectorFactory) ForContext(_ imageutils.SysContextFactory) osimagestream.ImagesInspector {
	return f.inspector
}

func noopSysCtxFactory() (*imageutils.SysContext, error) {
	return &imageutils.SysContext{}, nil
}

// TestCacheWarmerWarmsOnPISChange verifies that the cache warmer, running inside
// the PIS controller, reacts to PinnedImageSet create and update events by
// inspecting all referenced images and caching their labels. For images
// identified as release payloads (io.openshift.release label), it also fetches
// and caches the ImageStream file at /release-manifests/image-references.
func TestCacheWarmerWarmsOnPISChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cache := imageutils.NewFileInspectionCache(
		filepath.Join(t.TempDir(), "cache.json"), 48*time.Hour)

	inspector := &fakeInspector{
		inspectData: map[string]*types.ImageInspectInfo{
			testOSImage: {Labels: map[string]string{
				"io.openshift.os.streamclass": "rhel-9",
				"containers.bootc":            "true",
			}},
			testReleaseImage: {Labels: map[string]string{
				"io.openshift.release": "5.0.0",
			}},
			testOperatorImage: {Labels: map[string]string{
				"io.openshift.release.operator": "true",
			}},
		},
		fileData: map[string][]byte{
			testReleaseImage + ":" + releaseImageStreamPath: []byte("fake-imagestream"),
		},
	}

	cachedFactory := osimagestream.NewCachedImagesInspectorFactory(
		&fakeInspectorFactory{inspector: inspector},
		cache,
	)

	pool := helpers.NewMachineConfigPool("master", masterPoolSelector, helpers.MasterSelector, "")
	fakeMCOClient := fakemco.NewSimpleClientset(pool)
	fakeClient := fake.NewSimpleClientset()

	sharedInformers := mcfginformers.NewSharedInformerFactory(fakeMCOClient, 0)
	pisInformer := sharedInformers.Machineconfiguration().V1().PinnedImageSets()
	mcpInformer := sharedInformers.Machineconfiguration().V1().MachineConfigPools()

	warmer := NewCacheWarmer(pisInformer.Lister(), cachedFactory, noopSysCtxFactory)
	ctrl := New(pisInformer, mcpInformer, fakeClient, fakeMCOClient, warmer)

	sharedInformers.Start(ctx.Done())
	sharedInformers.WaitForCacheSync(ctx.Done())

	go ctrl.Run(2, ctx.Done())

	pis := &mcfgv1.PinnedImageSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "test-pis",
			Labels: map[string]string{"machineconfiguration.openshift.io/role": "master"},
		},
		Spec: mcfgv1.PinnedImageSetSpec{
			PinnedImages: []mcfgv1.PinnedImageRef{
				{Name: mcfgv1.ImageDigestFormat(testOSImage)},
				{Name: mcfgv1.ImageDigestFormat(testReleaseImage)},
				{Name: mcfgv1.ImageDigestFormat(testOperatorImage)},
			},
		},
	}
	_, err := fakeMCOClient.MachineconfigurationV1().PinnedImageSets().Create(ctx, pis, metav1.CreateOptions{})
	require.NoError(t, err)

	osDigest := imageutils.DigestFromPullspec(testOSImage)
	releaseDigest := imageutils.DigestFromPullspec(testReleaseImage)
	operatorDigest := imageutils.DigestFromPullspec(testOperatorImage)

	require.Eventually(t, func() bool {
		releaseEntry := cache.Get(releaseDigest)
		return releaseEntry != nil &&
			releaseEntry.Files != nil &&
			len(releaseEntry.Files[releaseImageStreamPath]) > 0
	}, 5*time.Second, 100*time.Millisecond)

	osEntry := cache.Get(osDigest)
	require.NotNil(t, osEntry)
	assert.Equal(t, "rhel-9", osEntry.Labels["io.openshift.os.streamclass"])

	releaseEntry := cache.Get(releaseDigest)
	require.NotNil(t, releaseEntry)
	assert.Equal(t, "5.0.0", releaseEntry.Labels["io.openshift.release"])
	assert.Equal(t, []byte("fake-imagestream"), releaseEntry.Files[releaseImageStreamPath])

	operatorEntry := cache.Get(operatorDigest)
	require.NotNil(t, operatorEntry)
	assert.Equal(t, "true", operatorEntry.Labels["io.openshift.release.operator"])

	// Update the PIS adding a new image and verify it gets warmed too
	newImage := "registry.example.com/new@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	inspector.inspectData[newImage] = &types.ImageInspectInfo{
		Labels: map[string]string{"some-label": "some-value"},
	}

	pis.Spec.PinnedImages = append(pis.Spec.PinnedImages, mcfgv1.PinnedImageRef{
		Name: mcfgv1.ImageDigestFormat(newImage),
	})
	_, err = fakeMCOClient.MachineconfigurationV1().PinnedImageSets().Update(ctx, pis, metav1.UpdateOptions{})
	require.NoError(t, err)

	newDigest := imageutils.DigestFromPullspec(newImage)
	require.Eventually(t, func() bool {
		return cache.Get(newDigest) != nil
	}, 5*time.Second, 100*time.Millisecond)

	newEntry := cache.Get(newDigest)
	assert.Equal(t, "some-value", newEntry.Labels["some-label"])
}
