package build

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/controller/build/fixtures"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	testhelpers "github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

// This test validates that the BuildController does nothing unless
// there is a matching MachineOSConfig for a given MachineConfigPool.
func TestBuildControllerDoesNothing(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	clients, _, _ := setupBuildControllerForTest(ctx, t)

	// i needs to be set to 2 because rendered-worker-1 already exists.
	for i := 2; i <= 10; i++ {
		insertNewRenderedMachineConfigAndUpdatePool(ctx, t, clients.mcfgclient, "worker", fmt.Sprintf("rendered-worker-%d", i))

		mosbList, err := clients.mcfgclient.MachineconfigurationV1alpha1().MachineOSBuilds().List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		assert.Len(t, mosbList.Items, 0)
	}
}

// This test validates that the BuildController stops running builds
// when a new MachineOSBuild for a givee MachineOSConfig is created or a new
// rendered MachineConfig is detected on the associated MachineConfigPool.
func TestBuildControllerDeletesRunningBuildBeforeStartingANewOne(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	poolName := "worker"

	clients, mosc, initialMosb, mcp, kubeassert := setupBuildControllerForTestWithBuild(ctx, t, poolName)

	initialBuildPodName := utils.GetBuildPodName(initialMosb)

	// After creating the new MachineOSConfig, a MachineOSBuild should be created.
	kubeassert.MachineOSBuildExists(initialMosb, "Initial MachineOSBuild not created for MachineOSConfig %s", mosc.Name)
	// After a new MachineOSBuild is created, a pod should be created.
	kubeassert.PodExists(initialBuildPodName, "Initial build pod %s did not get created for MachineOSConfig %s", initialBuildPodName, mosc.Name)
	// Set the running status on the pod.
	fixtures.SetPodPhase(ctx, t, clients.kubeclient, initialMosb, corev1.PodRunning)
	// The MachineOSBuild should be running.
	kubeassert.MachineOSBuildIsRunning(initialMosb, "Expected the MachineOSBuild %s status to be running", initialMosb.Name)

	// Now that the build is in the running state, we update the MachineOSConfig.
	apiMosc := testhelpers.SetContainerfileContentsOnMachineOSConfig(ctx, t, clients.mcfgclient, mosc, "FROM configs AS final\nRUN echo 'helloworld' > /etc/helloworld")

	apiMosc, err := clients.mcfgclient.MachineconfigurationV1alpha1().MachineOSConfigs().Update(ctx, apiMosc, metav1.UpdateOptions{})
	require.NoError(t, err)

	mosb := utils.NewMachineOSBuildFromAPIOrDie(ctx, clients.kubeclient, apiMosc, mcp)
	buildPodName := utils.GetBuildPodName(mosb)

	// After creating the new MachineOSConfig, a MachineOSBuild should be created.
	kubeassert.MachineOSBuildExists(mosb, "MachineOSBuild not created for MachineOSConfig %s change", mosc.Name)
	// After a new MachineOSBuild is created, a pod should be created.
	kubeassert.PodExists(buildPodName, "Build pod did not get created for MachineOSConfig %s change", mosc.Name)
	// Set the running status on the pod.
	fixtures.SetPodPhase(ctx, t, clients.kubeclient, mosb, corev1.PodRunning)
	// The MachineOSBuild should be running.
	kubeassert.MachineOSBuildIsRunning(mosb, "Expected the MachineOSBuild %s status to be running", mosb.Name)

	// AFter the new build starts, the old build should be deleted.
	kubeassert.MachineOSBuildDoesNotExist(initialMosb, "Expected the initial MachineOSBuild %s to be deleted", initialMosb.Name)
	kubeassert.PodDoesNotExist(initialBuildPodName, "Expected the initial build pod %s to be deleted", initialBuildPodName)
}

// This test validates that the BuildController will not touch old successful builds.
func TestBuildControllerLeavesSuccessfulBuildAlone(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	poolName := "worker"

	clients, firstMosc, firstMosb, mcp, kubeassert := setupBuildControllerForTestWithBuild(ctx, t, poolName)
	kubeassert.MachineOSBuildExists(firstMosb)
	kubeassert.PodExists(utils.GetBuildPodName(firstMosb))
	fixtures.SetPodPhase(ctx, t, clients.kubeclient, firstMosb, corev1.PodSucceeded)
	kubeassert.MachineOSBuildIsSuccessful(firstMosb)

	// Ensures that we have detected the first build.
	isMachineOSBuildReachedExpectedCount(ctx, t, clients.mcfgclient, firstMosc, 1)

	// Next, we create the second build which we just leave running.
	secondMosc := testhelpers.SetContainerfileContentsOnMachineOSConfig(ctx, t, clients.mcfgclient, firstMosc, "FROM configs AS final\nRUN echo 'hello' > /etc/hello")
	secondMosb := utils.NewMachineOSBuildFromAPIOrDie(ctx, clients.kubeclient, secondMosc, mcp)
	kubeassert.MachineOSBuildExists(secondMosb)
	kubeassert.PodExists(utils.GetBuildPodName(secondMosb))
	fixtures.SetPodPhase(ctx, t, clients.kubeclient, secondMosb, corev1.PodRunning)

	// Ensure that the build count has increased.
	isMachineOSBuildReachedExpectedCount(ctx, t, clients.mcfgclient, secondMosc, 2)

	// Next, we create the third build.
	thirdMosc := testhelpers.SetContainerfileContentsOnMachineOSConfig(ctx, t, clients.mcfgclient, secondMosc, "FROM configs AS final\nRUN echo 'helloworld' > /etc/helloworld")
	thirdMosb := utils.NewMachineOSBuildFromAPIOrDie(ctx, clients.kubeclient, thirdMosc, mcp)
	kubeassert.MachineOSBuildExists(thirdMosb)
	kubeassert.PodExists(utils.GetBuildPodName(thirdMosb))
	fixtures.SetPodPhase(ctx, t, clients.kubeclient, thirdMosb, corev1.PodRunning)

	// We ensure that the second build is deleted.
	kubeassert.MachineOSBuildDoesNotExist(secondMosb)
	kubeassert.PodDoesNotExist(utils.GetBuildPodName(secondMosb))

	// Ensure that the build count has not changed due to the second build being cancelled..
	isMachineOSBuildReachedExpectedCount(ctx, t, clients.mcfgclient, thirdMosc, 2)
}

// This test validates that when a build fails, all of the objects are left
// behind unless someone makes a change to the MachineOSConfig or
// MachineConfigPool.
func TestBuildControllerFailure(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	poolName := "worker"

	clients, _, mosb, _, kubeassert := setupBuildControllerForTestWithBuild(ctx, t, poolName)
	kubeassert.MachineOSBuildExists(mosb)
	kubeassert.PodExists(utils.GetBuildPodName(mosb))
	fixtures.SetPodPhase(ctx, t, clients.kubeclient, mosb, corev1.PodFailed)
	kubeassert.MachineOSBuildIsFailure(mosb)

	kubeassert.PodExists(utils.GetBuildPodName(mosb))
	kubeassert.ConfigMapExists(utils.GetContainerfileConfigMapName(mosb))
	kubeassert.ConfigMapExists(utils.GetMCConfigMapName(mosb))
	kubeassert.SecretExists(utils.GetBasePullSecretName(mosb))
	kubeassert.SecretExists(utils.GetFinalPushSecretName(mosb))
}

// This test validates that the BuildController does the following:
// 1. Creates a new MachineOSBuild for a given MachineOSConfig whenever the
// MachineOSConfig is updated.
// 2. Creates a new MachineOSbuild for a given MachineOSConfig whenever the
// MachineConfigPool is changed.
// 3. Removes all MachineOSBuilds associated with a given MachineOSConfig
// whenever the MachineOSConfig itself is deleted.
func TestBuildController(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	poolName := "worker"

	clients, mosc, _, mcp, kubeassert := setupBuildControllerForTestWithBuild(ctx, t, poolName)

	getConfigNameForPool := func(num int) string {
		return fmt.Sprintf("rendered-%s-%d", poolName, num)
	}

	mosb := utils.NewMachineOSBuildFromAPIOrDie(ctx, clients.kubeclient, mosc, mcp)

	buildPodName := utils.GetBuildPodName(mosb)
	// After creating the new MachineOSConfig, a MachineOSBuild should be created.
	kubeassert.MachineOSBuildExists(mosb, "Initial MachineOSBuild %s not created for MachineOSConfig %s", mosb.Name, mosc.Name)
	// After a new MachineOSBuild is created, a pod should be created.
	kubeassert.PodExists(buildPodName, "Initial build pod %s did not get created for MachineOSConfig %s", buildPodName, mosc.Name)
	// Set the successful status on the pod.
	fixtures.SetPodPhase(ctx, t, clients.kubeclient, mosb, corev1.PodSucceeded)
	// The MachineOSBuild should be successful.
	kubeassert.MachineOSBuildIsSuccessful(mosb, "Expected the MachineOSBuild %s status to be successful", mosb.Name)
	// And the build pod should be deleted.
	kubeassert.PodDoesNotExist(buildPodName, "Expected the build pod %s to be deleted", buildPodName)

	// Next, update the BuildInputs section on the MachineOSConfig and verify
	// that a new MachineOSBuild is produced from it. We'll do this 10 times.
	for i := 0; i <= 10; i++ {
		apiMosc := testhelpers.SetContainerfileContentsOnMachineOSConfig(ctx, t, clients.mcfgclient, mosc, "FROM configs AS final"+fmt.Sprintf("%d", i))

		apiMCP, err := clients.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(ctx, apiMosc.Spec.MachineConfigPool.Name, metav1.GetOptions{})
		require.NoError(t, err)

		mosb := utils.NewMachineOSBuildFromAPIOrDie(ctx, clients.kubeclient, apiMosc, apiMCP)
		buildPodName := utils.GetBuildPodName(mosb)
		// After creating the new MachineOSConfig, a MachineOSBuild should be created.
		kubeassert.MachineOSBuildExists(mosb, "MachineOSBuild not created for MachineOSConfig %s change", mosc.Name)

		assertBuildObjectsAreCreated(ctx, t, kubeassert, mosb)
		// After a new MachineOSBuild is created, a pod should be created.
		kubeassert.PodExists(buildPodName, "Build pod did not get created for MachineOSConfig %s change", mosc.Name)
		// Set the successful status on the pod.
		fixtures.SetPodPhase(ctx, t, clients.kubeclient, mosb, corev1.PodSucceeded)
		// The MachineOSBuild should be successful.
		kubeassert.MachineOSBuildIsSuccessful(mosb, "Expected the MachineOSBuild %s status to be successful", mosb.Name)
		// And the build pod should be deleted.

		assertBuildObjectsAreDeleted(ctx, t, kubeassert, mosb)
		kubeassert.PodDoesNotExist(buildPodName, "Expected the build pod %s to be deleted", buildPodName)
	}

	// Next, update the rendered MachineConfig on the MachineConfigPool and verify that a new MachineOSBuild is produced. We'll do this 10 times.
	for i := 0; i <= 10; i++ {
		apiMosc, err := clients.mcfgclient.MachineconfigurationV1alpha1().MachineOSConfigs().Get(ctx, mosc.Name, metav1.GetOptions{})
		require.NoError(t, err)

		apiMCP := insertNewRenderedMachineConfigAndUpdatePool(ctx, t, clients.mcfgclient, mosc.Spec.MachineConfigPool.Name, getConfigNameForPool(i+2))

		mosb := utils.NewMachineOSBuildFromAPIOrDie(ctx, clients.kubeclient, apiMosc, apiMCP)
		buildPodName := utils.GetBuildPodName(mosb)
		// After updating the MachineConfigPool, a new MachineOSBuild should get created.
		kubeassert.MachineOSBuildExists(mosb, "New MachineOSBuild for MachineConfigPool %q update for MachineOSConfig %q never gets created", mcp.Name, mosc.Name)
		// After a new MachineOSBuild is created, a pod should be created.
		kubeassert.PodExists(buildPodName, "Build pod did not get created for MachineConfigPool %q change", mcp.Name)
		// Set the successful status on the pod.
		fixtures.SetPodPhase(ctx, t, clients.kubeclient, mosb, corev1.PodSucceeded)
		// The MachineOSBuild should be successful.
		kubeassert.MachineOSBuildIsSuccessful(mosb, "Expected the MachineOSBuild %s status to be successful", mosb.Name)
		// And the build pod should be deleted.
		kubeassert.PodDoesNotExist(buildPodName, "Expected the build pod %s to be deleted", buildPodName)
	}

	// Now, we delete the MachineOSConfig and we expect that all
	// MachineOSBuilds that were created from it are also deleted.
	err := clients.mcfgclient.MachineconfigurationV1alpha1().MachineOSConfigs().Delete(ctx, mosc.Name, metav1.DeleteOptions{})
	require.NoError(t, err)

	isMachineOSBuildReachedExpectedCount(ctx, t, clients.mcfgclient, mosc, 0)
}

func assertBuildObjectsAreCreated(ctx context.Context, t *testing.T, kubeassert *testhelpers.Assertions, mosb *mcfgv1alpha1.MachineOSBuild) {
	t.Helper()

	kubeassert.PodExists(utils.GetBuildPodName(mosb))
	kubeassert.ConfigMapExists(utils.GetContainerfileConfigMapName(mosb))
	kubeassert.ConfigMapExists(utils.GetMCConfigMapName(mosb))
	kubeassert.SecretExists(utils.GetBasePullSecretName(mosb))
	kubeassert.SecretExists(utils.GetFinalPushSecretName(mosb))
}

func assertBuildObjectsAreDeleted(ctx context.Context, t *testing.T, kubeassert *testhelpers.Assertions, mosb *mcfgv1alpha1.MachineOSBuild) {
	t.Helper()

	kubeassert.PodDoesNotExist(utils.GetBuildPodName(mosb))
	kubeassert.ConfigMapDoesNotExist(utils.GetContainerfileConfigMapName(mosb))
	kubeassert.ConfigMapDoesNotExist(utils.GetMCConfigMapName(mosb))
	kubeassert.SecretDoesNotExist(utils.GetBasePullSecretName(mosb))
	kubeassert.SecretDoesNotExist(utils.GetFinalPushSecretName(mosb))
}

func setupBuildControllerForTest(ctx context.Context, t *testing.T) (*Clients, *testhelpers.Assertions, *fixtures.LayeredObjectsForTest) {
	kubeclient, mcfgclient, lobj := fixtures.GetClientsForTest()

	clients := &Clients{
		kubeclient: kubeclient,
		mcfgclient: mcfgclient,
	}

	ctrl := NewBuildController(BuildControllerConfig{
		MaxRetries:  1,
		UpdateDelay: time.Millisecond * 5,
	}, clients)

	// Use our own work queue specifically for testing.
	ctrl.execQueue = ctrlcommon.NewWrappedQueueForTesting(t, "test-mosb-controller")

	go ctrl.Run(ctx, 5)

	kubeassert := testhelpers.Assert(t, clients.kubeclient, clients.mcfgclient).WithContext(ctx).Eventually().WithPollInterval(time.Millisecond)

	return clients, kubeassert, lobj
}

func setupBuildControllerForTestWithBuild(ctx context.Context, t *testing.T, poolName string) (*Clients, *mcfgv1alpha1.MachineOSConfig, *mcfgv1alpha1.MachineOSBuild, *mcfgv1.MachineConfigPool, *testhelpers.Assertions) {
	clients, kubeassert, lobj := setupBuildControllerForTest(ctx, t)

	mcp := lobj.MachineConfigPool
	mosc := lobj.MachineOSConfig
	mosc.Name = fmt.Sprintf("%s-os-config", poolName)

	_, err := clients.mcfgclient.MachineconfigurationV1alpha1().MachineOSConfigs().Create(ctx, mosc, metav1.CreateOptions{})
	require.NoError(t, err)

	mosb := utils.NewMachineOSBuildFromAPIOrDie(ctx, clients.kubeclient, mosc, mcp)

	return clients, mosc, mosb, mcp, kubeassert
}

func insertNewRenderedMachineConfigAndUpdatePool(ctx context.Context, t *testing.T, mcfgclient mcfgclientset.Interface, poolName, renderedName string) *mcfgv1.MachineConfigPool {
	mcp, err := mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(ctx, poolName, metav1.GetOptions{})
	require.NoError(t, err)

	insertNewRenderedMachineConfig(ctx, t, mcfgclient, poolName, renderedName)

	mcp.Spec.Configuration.Name = renderedName

	mcp, err = mcfgclient.MachineconfigurationV1().MachineConfigPools().Update(ctx, mcp, metav1.UpdateOptions{})
	require.NoError(t, err)

	return mcp
}

func insertNewRenderedMachineConfig(ctx context.Context, t *testing.T, mcfgclient mcfgclientset.Interface, poolName, renderedName string) {
	filename := filepath.Join("/etc", poolName, renderedName)

	file := ctrlcommon.NewIgnFile(filename, renderedName)
	mc := testhelpers.NewMachineConfig(
		renderedName,
		map[string]string{
			ctrlcommon.GeneratedByControllerVersionAnnotationKey: "version-number",
			"machineconfiguration.openshift.io/role":             poolName,
		},
		"",
		[]ign3types.File{file})
	_, err := mcfgclient.MachineconfigurationV1().MachineConfigs().Create(ctx, mc, metav1.CreateOptions{})
	require.NoError(t, err)
}

func isMachineOSBuildReachedExpectedCount(ctx context.Context, t *testing.T, mcfgclient mcfgclientset.Interface, mosc *mcfgv1alpha1.MachineOSConfig, expected int) {
	t.Helper()

	err := wait.PollImmediateInfiniteWithContext(ctx, time.Millisecond, func(ctx context.Context) (bool, error) {
		mosbList, err := mcfgclient.MachineconfigurationV1alpha1().MachineOSBuilds().List(ctx, metav1.ListOptions{
			LabelSelector: utils.MachineOSBuildForPoolSelector(mosc).String(),
		})
		if err != nil {
			return false, err
		}

		return len(mosbList.Items) == expected, nil
	})

	require.NoError(t, err, "MachineOSBuild count did not reach expected value %d", expected)
}
