package e2e_2of2

import (
	"context"
	"testing"

	"github.com/openshift/machine-config-operator/internal/clients"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/controller/osimagestream"
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

	image, err := osimagestream.GetReleasePayloadImage(ctrlctx.ConfigInformerFactory.Config().V1().ClusterVersions().Lister())
	assert.NoError(t, err)
	assert.NotEmpty(t, image)

	cs := framework.NewClientSet("")
	assert.Equal(t, getCVOImage(t, cs), image)

	sysContext := setupSysContext(t, cs)
	defer func() {
		require.NoError(t, sysContext.Cleanup())
	}()

	isNetProvider := osimagestream.NewImageStreamProviderNetwork(osimagestream.NewImagesInspector(sysContext.SysContext), image)
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
