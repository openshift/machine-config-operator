package e2e_2of2

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openshift/machine-config-operator/internal/clients"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/osimagestream"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
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

// TestOSImageStreamInheritance tests that custom MCPs inherit osImageStream from the worker pool
func TestOSImageStreamInheritance(t *testing.T) {
	cs := framework.NewClientSet("")
	ctx := context.TODO()

	// Check if OSImageStream feature is enabled
	osImageStream, err := cs.OSImageStreams().Get(ctx, ctrlcommon.ClusterInstanceNameOSImageStream, metav1.GetOptions{})
	if err != nil {
		t.Skipf("OSImageStream feature not enabled, skipping: %v", err)
		return
	}

	// Ensure we have at least one available stream
	require.NotEmpty(t, osImageStream.Status.AvailableStreams, "No available streams found in OSImageStream")

	// Get worker pool to check its current osImageStream
	workerPool, err := cs.MachineConfigPools().Get(ctx, "worker", metav1.GetOptions{})
	require.NoError(t, err)

	// Get the effective stream name for worker (explicit or default)
	workerStreamName := workerPool.Spec.OSImageStream.Name
	if workerStreamName == "" {
		workerStreamName = osImageStream.Status.DefaultStream
	}

	t.Logf("Worker pool using osImageStream: %q", workerStreamName)

	// Get worker's rendered MC to find osImageURL
	workerMC, err := cs.MachineConfigs().Get(ctx, workerPool.Status.Configuration.Name, metav1.GetOptions{})
	require.NoError(t, err)
	workerOSImageURL := workerMC.Spec.OSImageURL
	t.Logf("Worker pool osImageURL: %s", workerOSImageURL)

	// Create a custom MCP without osImageStream set
	customPoolName := "osstream-inherit-test"
	node := helpers.GetRandomNode(t, cs, "worker")

	t.Logf("Creating custom pool %q without explicit osImageStream", customPoolName)
	cleanupFunc := helpers.CreatePoolWithNode(t, cs, customPoolName, node)
	defer cleanupFunc()

	// Get the custom pool's rendered MC
	customMCName := helpers.GetMcName(t, cs, customPoolName)
	customMC, err := cs.MachineConfigs().Get(ctx, customMCName, metav1.GetOptions{})
	require.NoError(t, err)

	// Verify the custom pool inherited the worker's osImageURL
	assert.Equal(t, workerOSImageURL, customMC.Spec.OSImageURL,
		"Custom pool should inherit osImageURL from worker pool")

	t.Logf("✓ Custom pool %q successfully inherited osImageURL from worker: %s", customPoolName, customMC.Spec.OSImageURL)
}

// TestOSImageStreamExplicitOverride tests that custom MCPs can override the worker's osImageStream
func TestOSImageStreamExplicitOverride(t *testing.T) {
	cs := framework.NewClientSet("")
	ctx := context.TODO()

	// Check if OSImageStream feature is enabled
	osImageStream, err := cs.OSImageStreams().Get(ctx, ctrlcommon.ClusterInstanceNameOSImageStream, metav1.GetOptions{})
	if err != nil {
		t.Skipf("OSImageStream feature not enabled, skipping: %v", err)
		return
	}

	// Ensure we have at least 2 available streams to test override
	if len(osImageStream.Status.AvailableStreams) < 2 {
		t.Skipf("Need at least 2 available streams for override test, found %d", len(osImageStream.Status.AvailableStreams))
		return
	}

	// Get worker pool's stream
	workerPool, err := cs.MachineConfigPools().Get(ctx, "worker", metav1.GetOptions{})
	require.NoError(t, err)

	workerStreamName := workerPool.Spec.OSImageStream.Name
	if workerStreamName == "" {
		workerStreamName = osImageStream.Status.DefaultStream
	}

	// Find a different stream to use for override
	var overrideStreamName string
	for _, stream := range osImageStream.Status.AvailableStreams {
		if stream.Name != workerStreamName {
			overrideStreamName = stream.Name
			break
		}
	}

	if overrideStreamName == "" {
		t.Skip("Could not find a different stream for override test")
		return
	}

	t.Logf("Worker pool using stream: %q, will override custom pool with: %q", workerStreamName, overrideStreamName)

	// Create a custom MCP with explicit osImageStream
	customPoolName := "osstream-override-test"
	node := helpers.GetRandomNode(t, cs, "worker")

	// Create the pool first
	t.Logf("Creating custom pool %q", customPoolName)
	unlabelFunc := helpers.LabelNode(t, cs, node, helpers.MCPNameToRole(customPoolName))
	deleteMCPFunc := helpers.CreateMCP(t, cs, customPoolName)

	// Set the osImageStream on the custom pool
	customPool, err := cs.MachineConfigPools().Get(ctx, customPoolName, metav1.GetOptions{})
	require.NoError(t, err)
	customPool.Spec.OSImageStream.Name = overrideStreamName
	_, err = cs.MachineConfigPools().Update(ctx, customPool, metav1.UpdateOptions{})
	require.NoError(t, err)

	t.Logf("Set custom pool osImageStream to: %q", overrideStreamName)

	// Wait for the pool to complete rollout
	helpers.WaitForConfigAndPoolComplete(t, cs, customPoolName, "00-worker")
	customMCName := helpers.GetMcName(t, cs, customPoolName)
	require.NoError(t, helpers.WaitForNodeConfigChange(t, cs, node, customMCName))

	// Cleanup function
	cleanupFunc := helpers.MakeIdempotent(func() {
		t.Logf("Cleaning up custom pool %q", customPoolName)
		unlabelFunc()
		workerMC := helpers.GetMcName(t, cs, "worker")
		require.NoError(t, helpers.WaitForPoolComplete(t, cs, "worker", workerMC))
		require.NoError(t, helpers.WaitForNodeConfigChange(t, cs, node, workerMC))
		deleteMCPFunc()
	})
	defer cleanupFunc()

	// Get the custom pool's rendered MC
	customMC, err := cs.MachineConfigs().Get(ctx, customMCName, metav1.GetOptions{})
	require.NoError(t, err)

	// Get the worker's rendered MC for comparison
	workerMC, err := cs.MachineConfigs().Get(ctx, workerPool.Status.Configuration.Name, metav1.GetOptions{})
	require.NoError(t, err)

	// Verify the custom pool's osImageURL is different from worker's
	assert.NotEqual(t, workerMC.Spec.OSImageURL, customMC.Spec.OSImageURL,
		"Custom pool with explicit osImageStream should have different osImageURL than worker")

	t.Logf("✓ Custom pool successfully overrode worker's osImageStream")
	t.Logf("  Worker osImageURL: %s", workerMC.Spec.OSImageURL)
	t.Logf("  Custom osImageURL: %s", customMC.Spec.OSImageURL)
}

// TestOSImageStreamDynamicInheritance tests that custom MCPs update when worker's osImageStream changes
func TestOSImageStreamDynamicInheritance(t *testing.T) {
	cs := framework.NewClientSet("")
	ctx := context.TODO()

	// Check if OSImageStream feature is enabled
	osImageStream, err := cs.OSImageStreams().Get(ctx, ctrlcommon.ClusterInstanceNameOSImageStream, metav1.GetOptions{})
	if err != nil {
		t.Skipf("OSImageStream feature not enabled, skipping: %v", err)
		return
	}

	// Ensure we have at least 2 available streams
	if len(osImageStream.Status.AvailableStreams) < 2 {
		t.Skipf("Need at least 2 available streams for dynamic test, found %d", len(osImageStream.Status.AvailableStreams))
		return
	}

	// Get worker pool's current stream
	workerPool, err := cs.MachineConfigPools().Get(ctx, "worker", metav1.GetOptions{})
	require.NoError(t, err)

	originalWorkerStreamName := workerPool.Spec.OSImageStream.Name
	if originalWorkerStreamName == "" {
		originalWorkerStreamName = osImageStream.Status.DefaultStream
	}

	// Find a different stream to switch to
	var newStreamName string
	for _, stream := range osImageStream.Status.AvailableStreams {
		if stream.Name != originalWorkerStreamName {
			newStreamName = stream.Name
			break
		}
	}

	if newStreamName == "" {
		t.Skip("Could not find a different stream for dynamic test")
		return
	}

	t.Logf("Worker currently using stream: %q, will change to: %q", originalWorkerStreamName, newStreamName)

	// Create a custom MCP without osImageStream set
	customPoolName := "osstream-dynamic-test"
	node := helpers.GetRandomNode(t, cs, "worker")

	t.Logf("Creating custom pool %q without explicit osImageStream", customPoolName)
	cleanupFunc := helpers.CreatePoolWithNode(t, cs, customPoolName, node)
	defer cleanupFunc()

	// Get initial osImageURL of custom pool
	customMCName1 := helpers.GetMcName(t, cs, customPoolName)
	customMC1, err := cs.MachineConfigs().Get(ctx, customMCName1, metav1.GetOptions{})
	require.NoError(t, err)
	initialOSImageURL := customMC1.Spec.OSImageURL

	t.Logf("Initial custom pool osImageURL: %s", initialOSImageURL)

	// Change worker pool's osImageStream
	t.Logf("Changing worker pool osImageStream to: %q", newStreamName)
	workerPool, err = cs.MachineConfigPools().Get(ctx, "worker", metav1.GetOptions{})
	require.NoError(t, err)
	workerPool.Spec.OSImageStream.Name = newStreamName
	_, err = cs.MachineConfigPools().Update(ctx, workerPool, metav1.UpdateOptions{})
	require.NoError(t, err)

	// Wait for custom pool to reconcile - a new rendered config should be generated
	t.Logf("Waiting for custom pool to reconcile and generate new rendered config...")
	var customMCName2 string
	var newOSImageURL string

	// Poll until a new rendered MC for this pool exists with a different name
	// We check the MachineConfig list rather than pool status because the pool status
	// is only updated after the node completes the update
	err = wait.PollUntilContextTimeout(ctx, 2*time.Second, 5*time.Minute, false, func(ctx context.Context) (bool, error) {
		// List all MachineConfigs with the pool name prefix
		mcList, err := cs.MachineConfigs().List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}

		// Look for a rendered config for this pool that's different from the initial one
		prefix := "rendered-" + customPoolName + "-"
		for _, mc := range mcList.Items {
			if strings.HasPrefix(mc.Name, prefix) && mc.Name != customMCName1 {
				// Found a new rendered config!
				customMCName2 = mc.Name
				newOSImageURL = mc.Spec.OSImageURL
				t.Logf("Custom pool new rendered config generated: %s", customMCName2)
				return true, nil
			}
		}
		return false, nil
	})
	require.NoError(t, err, "Timed out waiting for custom pool to generate new rendered config")

	// Verify the custom pool's osImageURL changed
	assert.NotEqual(t, initialOSImageURL, newOSImageURL,
		"Custom pool should update osImageURL when worker's osImageStream changes")

	t.Logf("✓ Custom pool dynamically inherited new osImageStream from worker")
	t.Logf("  Initial osImageURL: %s", initialOSImageURL)
	t.Logf("  New osImageURL:     %s", newOSImageURL)

	// Restore worker pool's original osImageStream
	t.Logf("Restoring worker pool osImageStream to: %q", originalWorkerStreamName)
	workerPool, err = cs.MachineConfigPools().Get(ctx, "worker", metav1.GetOptions{})
	require.NoError(t, err)
	workerPool.Spec.OSImageStream.Name = originalWorkerStreamName
	_, err = cs.MachineConfigPools().Update(ctx, workerPool, metav1.UpdateOptions{})
	require.NoError(t, err)

	// Wait for custom pool to reconcile back to original stream
	t.Logf("Waiting for custom pool to reconcile back to original stream...")
	err = wait.PollUntilContextTimeout(ctx, 2*time.Second, 5*time.Minute, false, func(ctx context.Context) (bool, error) {
		currentMCName := helpers.GetMcName(t, cs, customPoolName)
		// Wait until it changes back to a different config (should match original stream's config)
		if currentMCName != customMCName2 {
			t.Logf("Custom pool rendered config changed back: %s -> %s", customMCName2, currentMCName)
			return true, nil
		}
		return false, nil
	})
	require.NoError(t, err, "Timed out waiting for custom pool to restore to original stream")

	t.Logf("✓ Restored worker pool to original osImageStream")
}
