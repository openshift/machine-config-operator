package e2e_2of2

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openshift/machine-config-operator/internal/clients"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/imageutils"
	"github.com/openshift/machine-config-operator/pkg/osimagestream"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func getCVOImage(t *testing.T, cs *framework.ClientSet) string {
	t.Helper()

	clusterVersion, err := cs.ClusterVersions().Get(context.TODO(), "version", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, clusterVersion)

	image := clusterVersion.Status.Desired.Image
	assert.NotEmpty(t, image)
	return image
}

func TestImageStreamProviderCVO(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cb, err := clients.NewBuilder("")
	ctrlctx := ctrlcommon.CreateControllerContext(ctx, cb)
	ctrlctx.ConfigInformerFactory.Start(ctrlctx.Stop)

	// Wait for the informer cache to sync before querying the lister
	ctrlctx.ConfigInformerFactory.WaitForCacheSync(ctx.Done())

	clusterVersion, err := osimagestream.GetClusterVersion(ctrlctx.ConfigInformerFactory.Config().V1().ClusterVersions().Lister())
	assert.NoError(t, err)
	assert.NotNil(t, clusterVersion)

	image, err := osimagestream.GetReleasePayloadImage(clusterVersion)
	assert.NoError(t, err)
	assert.NotEmpty(t, image)

	cs := framework.NewClientSet("")
	assert.Equal(t, getCVOImage(t, cs), image)

	sysContext := setupSysContext(t, cs)
	sysCtxFactory := func() (*imageutils.SysContext, error) { return sysContext, nil }

	isNetProvider := osimagestream.NewImageStreamProviderNetwork(osimagestream.NewImagesInspector(sysCtxFactory), image)
	imageStream, err := isNetProvider.ReadImageStream(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, imageStream)
	assert.NotEmpty(t, imageStream.Spec.Tags)
}

func TestConfigMapUrlProvider(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cb, err := clients.NewBuilder("")
	require.NoError(t, err)

	ctrlctx := ctrlcommon.CreateControllerContext(ctx, cb)

	// Access the informer and force it to be created BEFORE Start()
	// This registers it in the factory's internal map
	cmInformer := ctrlctx.KubeNamespacedInformerFactory.Core().V1().ConfigMaps()
	_ = cmInformer.Informer()

	ctrlctx.KubeNamespacedInformerFactory.Start(ctrlctx.Stop)

	// Wait for the informer cache to sync before querying the lister
	ctrlctx.KubeNamespacedInformerFactory.WaitForCacheSync(ctx.Done())

	configMapUrlProvider := osimagestream.NewConfigMapURLProviders(cmInformer.Lister())
	urls, err := configMapUrlProvider.GetUrls()
	require.NoError(t, err)
	require.NotNil(t, urls)

	// Validate the URLs are populated
	assert.NotEmpty(t, urls.OSImage)
	assert.NotEmpty(t, urls.OSExtensionsImage)

	cs := framework.NewClientSet("")
	configMap, err := cs.ConfigMaps("openshift-machine-config-operator").
		Get(ctx, "machine-config-osimageurl", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, configMap)
	assert.NotEmpty(t, configMap.Data)
	baseOSContainerImage, ok := configMap.Data["baseOSContainerImage"]
	assert.True(t, ok)
	assert.Equal(t, baseOSContainerImage, urls.OSImage)
	baseOSExtensionsContainerImage, ok := configMap.Data["baseOSExtensionsContainerImage"]
	assert.True(t, ok)
	assert.Equal(t, baseOSExtensionsContainerImage, urls.OSExtensionsImage)
}

// TestCachedInspectorFactory validates the CachedImagesInspectorFactory stack
// (cache + real image inspector) wired the same way as production. It inspects
// a real cluster image to populate the cache, then verifies a second inspect
// with a broken SysContextFactory is served entirely from cache.
func TestCachedInspectorFactory(t *testing.T) {
	cs := framework.NewClientSet("")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	image := getCVOImage(t, cs)
	digest := imageutils.DigestFromPullspec(image)
	require.NotEmpty(t, digest, "CVO release image must be digested")

	cachePath := filepath.Join(t.TempDir(), "test-cache.json")
	cache := imageutils.NewFileInspectionCache(cachePath, 48*time.Hour)
	factory := osimagestream.NewCachedImagesInspectorFactory(
		&osimagestream.DefaultImagesInspectorFactory{},
		cache,
	)

	// First inspect: cache miss, hits the real registry
	sysContext := setupSysContext(t, cs)
	sysCtxFactory := func() (*imageutils.SysContext, error) { return sysContext, nil }
	inspector := factory.ForContext(sysCtxFactory)

	results, err := inspector.Inspect(ctx, image)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NoError(t, results[0].Error)
	require.NotNil(t, results[0].InspectInfo)
	require.NotEmpty(t, results[0].InspectInfo.Labels)

	entry := cache.Get(digest)
	require.NotNil(t, entry, "cache must be populated after first inspect")
	assert.Equal(t, results[0].InspectInfo.Labels, entry.Labels)

	_, err = os.Stat(cachePath)
	require.NoError(t, err, "cache file must exist on disk")

	// Second inspect: uses a broken factory that errors if called, proving
	// the result is served from cache without touching the registry.
	broken := func() (*imageutils.SysContext, error) {
		return nil, errors.New("must not be called — cache should serve this")
	}
	inspector2 := factory.ForContext(broken)

	results2, err := inspector2.Inspect(ctx, image)
	require.NoError(t, err)
	require.Len(t, results2, 1)
	require.NoError(t, results2[0].Error)
	assert.Equal(t, results[0].InspectInfo.Labels, results2[0].InspectInfo.Labels)
}
