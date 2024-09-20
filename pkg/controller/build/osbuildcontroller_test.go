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
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/fixtures"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	testhelpers "github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
)

// TODO: Remove this and deal with the resulting parameter explosion in the test suite.
type clients struct {
	mcfgclient mcfgclientset.Interface
	kubeclient clientset.Interface
}

// This test validates that the OSBuildController does nothing unless
// there is a matching MachineOSConfig for a given MachineConfigPool.
func TestOSBuildControllerDoesNothing(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	clients, _, _ := setupOSBuildControllerForTest(ctx, t)

	// i needs to be set to 2 because rendered-worker-1 already exists.
	for i := 2; i <= 10; i++ {
		insertNewRenderedMachineConfigAndUpdatePool(ctx, t, clients.mcfgclient, "worker", fmt.Sprintf("rendered-worker-%d", i))

		mosbList, err := clients.mcfgclient.MachineconfigurationV1alpha1().MachineOSBuilds().List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		assert.Len(t, mosbList.Items, 0)
	}
}

// This test validates that the OSBuildController stops running builds
// when a new MachineOSBuild for a givee MachineOSConfig is created or a new
// rendered MachineConfig is detected on the associated MachineConfigPool.
func TestOSBuildControllerDeletesRunningBuildBeforeStartingANewOne(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	poolName := "worker"

	t.Run("MachineOSConfig change", func(t *testing.T) {
		t.Parallel()

		clients, mosc, initialMosb, mcp, kubeassert := setupOSBuildControllerForTestWithRunningBuild(ctx, t, poolName)

		// Now that the build is in the running state, we update the MachineOSConfig.
		apiMosc := testhelpers.SetContainerfileContentsOnMachineOSConfig(ctx, t, clients.mcfgclient, mosc, "FROM configs AS final\nRUN echo 'helloworld' > /etc/helloworld")

		apiMosc, err := clients.mcfgclient.MachineconfigurationV1alpha1().MachineOSConfigs().Update(ctx, apiMosc, metav1.UpdateOptions{})
		require.NoError(t, err)

		mosb := buildrequest.NewMachineOSBuildFromAPIOrDie(ctx, clients.kubeclient, apiMosc, mcp)
		buildPodName := utils.GetBuildPodName(mosb)

		// After creating the new MachineOSConfig, a MachineOSBuild should be created.
		kubeassert.MachineOSBuildExists(mosb, "MachineOSBuild not created for MachineOSConfig %s change", mosc.Name)

		// After a new MachineOSBuild is created, a pod should be created.
		kubeassert.PodExists(buildPodName, "Build pod did not get created for MachineOSConfig %s change", mosc.Name)

		// Set the running status on the pod.
		fixtures.SetPodPhase(ctx, t, clients.kubeclient, mosb, corev1.PodRunning)

		// The MachineOSBuild should be running.
		kubeassert.MachineOSBuildIsRunning(mosb, "Expected the MachineOSBuild %s status to be running", mosb.Name)

		// After the new build starts, the old build should be deleted.
		kubeassert.MachineOSBuildDoesNotExist(initialMosb, "Expected the initial MachineOSBuild %s to be deleted", initialMosb.Name)
		assertBuildObjectsAreDeleted(ctx, t, kubeassert, initialMosb)
		isMachineOSBuildReachedExpectedCount(ctx, t, clients.mcfgclient, mosc, 1)
	})

	t.Run("MachineConfig change", func(t *testing.T) {
		t.Parallel()

		clients, mosc, initialMosb, mcp, kubeassert := setupOSBuildControllerForTestWithRunningBuild(ctx, t, poolName)

		apiMCP := insertNewRenderedMachineConfigAndUpdatePool(ctx, t, clients.mcfgclient, mosc.Spec.MachineConfigPool.Name, "rendered-worker-2")

		mosb := buildrequest.NewMachineOSBuildFromAPIOrDie(ctx, clients.kubeclient, mosc, apiMCP)

		buildPodName := utils.GetBuildPodName(mosb)

		// After updating the MachineConfigPool, a new MachineOSBuild should get created.
		kubeassert.MachineOSBuildExists(mosb, "New MachineOSBuild for MachineConfigPool %q update for MachineOSConfig %q never gets created", mcp.Name, mosc.Name)

		// After a new MachineOSBuild is created, a pod should be created.
		kubeassert.PodExists(buildPodName, "Build pod did not get created for MachineConfigPool %q change", mcp.Name)

		// After the new build starts, the old build should be deleted.
		kubeassert.MachineOSBuildDoesNotExist(initialMosb, "Expected the initial MachineOSBuild %s to be deleted", initialMosb.Name)
		assertBuildObjectsAreDeleted(ctx, t, kubeassert, initialMosb)
		isMachineOSBuildReachedExpectedCount(ctx, t, clients.mcfgclient, mosc, 1)
	})
}

// This test validates that the OSBuildController will not touch old successful
// builds but will still clear running builds before statring a new build for
// the same MachineOSConfig.
func TestOSBuildControllerLeavesSuccessfulBuildAlone(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	poolName := "worker"

	clients, firstMosc, firstMosb, mcp, kubeassert := setupOSBuildControllerForTestWithSuccessfulBuild(ctx, t, poolName)

	// Ensures that we have detected the first build.
	isMachineOSBuildReachedExpectedCount(ctx, t, clients.mcfgclient, firstMosc, 1)

	// Creates a MachineOSBuild via a MachineOSConfig change.
	createNewMachineOSBuildViaConfigChange := func(mosc *mcfgv1alpha1.MachineOSConfig, containerfileContents string) (*mcfgv1alpha1.MachineOSConfig, *mcfgv1alpha1.MachineOSBuild) {
		// Modify the MachineOSConfig.
		newMosc := testhelpers.SetContainerfileContentsOnMachineOSConfig(ctx, t, clients.mcfgclient, mosc, containerfileContents)

		// Compute the new MachineOSBuild.
		mosb := buildrequest.NewMachineOSBuildFromAPIOrDie(ctx, clients.kubeclient, newMosc, mcp)

		// Ensure that the MachineOSBuild exists.
		kubeassert.MachineOSBuildExists(mosb)

		// Ensure that the build pod exists.
		kubeassert.PodExists(utils.GetBuildPodName(mosb))

		// Set the pod phase to running.
		fixtures.SetPodPhase(ctx, t, clients.kubeclient, mosb, corev1.PodRunning)

		// Ensure that the MachineOSBuild gets the running status.
		kubeassert.MachineOSBuildIsRunning(mosb)

		return newMosc, mosb
	}

	// Next, we create the second build which we just leave running.
	secondMosc, secondMosb := createNewMachineOSBuildViaConfigChange(firstMosc, "FROM configs AS final\nRUN echo 'hello' > /etc/hello")

	// Ensure that the build count has increased.
	isMachineOSBuildReachedExpectedCount(ctx, t, clients.mcfgclient, secondMosc, 2)

	// Next, we create the third build.
	thirdMosc, thirdMosb := createNewMachineOSBuildViaConfigChange(firstMosc, "FROM configs AS final\nRUN echo 'helloworld' > /etc/helloworld")
	kubeassert.MachineOSBuildIsRunning(thirdMosb)

	// We ensure that the second build is deleted.
	kubeassert.Now().MachineOSBuildDoesNotExist(secondMosb)
	kubeassert.Now().PodDoesNotExist(utils.GetBuildPodName(secondMosb))

	// We ensure that the first build is still present.
	kubeassert.Now().MachineOSBuildExists(firstMosb)
	kubeassert.Now().MachineOSBuildIsSuccessful(firstMosb)

	// Ensure that the build count has not changed due to the second build being cancelled.
	isMachineOSBuildReachedExpectedCount(ctx, t, clients.mcfgclient, thirdMosc, 2)

	// Set the third build as successful.
	fixtures.SetPodPhase(ctx, t, clients.kubeclient, thirdMosb, corev1.PodSucceeded)
	kubeassert.MachineOSBuildIsSuccessful(thirdMosb)
	kubeassert.PodDoesNotExist(utils.GetBuildPodName(thirdMosb))

	// Ensure that the build count has not changed due to the third build completing.
	isMachineOSBuildReachedExpectedCount(ctx, t, clients.mcfgclient, thirdMosc, 2)
}

// This test validates that when a build fails, all of the objects are left
// behind unless someone makes a change to the MachineOSConfig or
// MachineConfigPool.
func TestOSBuildControllerFailure(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	poolName := "worker"

	t.Run("Failed build objects remain", func(t *testing.T) {
		t.Parallel()

		_, _, failedMosb, _, kubeassert := setupOSBuildControllerForTestWithFailedBuild(ctx, t, poolName)

		// Ensure that even after failure, the build objects remain.
		assertBuildObjectsAreCreated(ctx, t, kubeassert, failedMosb)
	})

	t.Run("MachineOSConfig change clears failed build", func(t *testing.T) {
		t.Parallel()

		clients, mosc, failedMosb, mcp, kubeassert := setupOSBuildControllerForTestWithFailedBuild(ctx, t, poolName)

		// Modify the MachineOSConfig to start a new build.
		newMosc := testhelpers.SetContainerfileContentsOnMachineOSConfig(ctx, t, clients.mcfgclient, mosc, "FROM configs AS final\nRUN echo 'helloworld' > /etc/helloworld")

		// Compute the new MachineOSBuild.
		newMosb := buildrequest.NewMachineOSBuildFromAPIOrDie(ctx, clients.kubeclient, newMosc, mcp)

		// Ensure that the MachineOSBuild exists.
		kubeassert.MachineOSBuildExists(newMosb)
		// Ensure that the build pod exists.
		kubeassert.PodExists(utils.GetBuildPodName(newMosb))
		// Set the pod phase to running.
		fixtures.SetPodPhase(ctx, t, clients.kubeclient, newMosb, corev1.PodRunning)
		// Ensure that the MachineOSBuild gets the running status.
		kubeassert.MachineOSBuildIsRunning(newMosb)

		// Ensure that the old build was cleared.
		kubeassert.MachineOSBuildDoesNotExist(failedMosb)
		assertBuildObjectsAreDeleted(ctx, t, kubeassert, failedMosb)
	})

	t.Run("MachineConfig change clears failed build", func(t *testing.T) {
		t.Parallel()

		clients, mosc, failedMosb, mcp, kubeassert := setupOSBuildControllerForTestWithFailedBuild(ctx, t, poolName)

		apiMCP := insertNewRenderedMachineConfigAndUpdatePool(ctx, t, clients.mcfgclient, mosc.Spec.MachineConfigPool.Name, "rendered-worker-2")

		mosb := buildrequest.NewMachineOSBuildFromAPIOrDie(ctx, clients.kubeclient, mosc, apiMCP)
		buildPodName := utils.GetBuildPodName(mosb)
		// After updating the MachineConfigPool, a new MachineOSBuild should get created.
		kubeassert.MachineOSBuildExists(mosb, "New MachineOSBuild for MachineConfigPool %q update for MachineOSConfig %q never gets created", mcp.Name, mosc.Name)
		// After a new MachineOSBuild is created, a pod should be created.
		kubeassert.PodExists(buildPodName, "Build pod did not get created for MachineConfigPool %q change", mcp.Name)

		// Ensure that the old build was cleared.
		kubeassert.MachineOSBuildDoesNotExist(failedMosb)
		assertBuildObjectsAreDeleted(ctx, t, kubeassert, failedMosb)
	})
}

// This test validates that the OSBuildController does the following:
// 1. Creates a new MachineOSBuild for a given MachineOSConfig whenever the
// MachineOSConfig is updated.
// 2. Creates a new MachineOSbuild for a given MachineOSConfig whenever the
// MachineConfigPool is changed.
// 3. Removes all MachineOSBuilds associated with a given MachineOSConfig
// whenever the MachineOSConfig itself is deleted.
func TestOSBuildController(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	poolName := "worker"

	getConfigNameForPool := func(num int) string {
		return fmt.Sprintf("rendered-%s-%d", poolName, num)
	}

	t.Run("MachineOSConfig changes creates a new MachineOSBuild", func(t *testing.T) {
		t.Parallel()

		clients, mosc, _, _, kubeassert := setupOSBuildControllerForTestWithSuccessfulBuild(ctx, t, poolName)

		// Update the BuildInputs section on the MachineOSConfig and verify that a
		// new MachineOSBuild is produced from it. We'll do this 10 times.
		for i := 0; i <= 10; i++ {
			apiMosc := testhelpers.SetContainerfileContentsOnMachineOSConfig(ctx, t, clients.mcfgclient, mosc, "FROM configs AS final"+fmt.Sprintf("%d", i))

			apiMCP, err := clients.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(ctx, apiMosc.Spec.MachineConfigPool.Name, metav1.GetOptions{})
			require.NoError(t, err)

			mosb := buildrequest.NewMachineOSBuildFromAPIOrDie(ctx, clients.kubeclient, apiMosc, apiMCP)
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

			// Ensure that the MachineOSBuild count increases with each successful build.
			isMachineOSBuildReachedExpectedCount(ctx, t, clients.mcfgclient, apiMosc, i+2)
		}

		// Now, we delete the MachineOSConfig and we expect that all
		// MachineOSBuilds that were created from it are also deleted.
		err := clients.mcfgclient.MachineconfigurationV1alpha1().MachineOSConfigs().Delete(ctx, mosc.Name, metav1.DeleteOptions{})
		require.NoError(t, err)

		isMachineOSBuildReachedExpectedCount(ctx, t, clients.mcfgclient, mosc, 0)
	})

	t.Run("MachineConfig changes creates a new MachineOSBuild", func(t *testing.T) {
		t.Parallel()

		clients, mosc, _, mcp, kubeassert := setupOSBuildControllerForTestWithSuccessfulBuild(ctx, t, poolName)

		// Update the rendered MachineConfig on the MachineConfigPool and verify that a new MachineOSBuild is produced. We'll do this 10 times.
		for i := 0; i <= 10; i++ {
			apiMosc, err := clients.mcfgclient.MachineconfigurationV1alpha1().MachineOSConfigs().Get(ctx, mosc.Name, metav1.GetOptions{})
			require.NoError(t, err)

			apiMCP := insertNewRenderedMachineConfigAndUpdatePool(ctx, t, clients.mcfgclient, mosc.Spec.MachineConfigPool.Name, getConfigNameForPool(i+2))

			mosb := buildrequest.NewMachineOSBuildFromAPIOrDie(ctx, clients.kubeclient, apiMosc, apiMCP)
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

			// Ensure that the MachineOSBuild count increases with each successful build.
			isMachineOSBuildReachedExpectedCount(ctx, t, clients.mcfgclient, apiMosc, i+2)
		}

		// Now, we delete the MachineOSConfig and we expect that all
		// MachineOSBuilds that were created from it are also deleted.
		err := clients.mcfgclient.MachineconfigurationV1alpha1().MachineOSConfigs().Delete(ctx, mosc.Name, metav1.DeleteOptions{})
		require.NoError(t, err)

		isMachineOSBuildReachedExpectedCount(ctx, t, clients.mcfgclient, mosc, 0)
	})
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

func setupOSBuildControllerForTest(ctx context.Context, t *testing.T) (*clients, *testhelpers.Assertions, *fixtures.ObjectsForTest) {
	kubeclient, mcfgclient, lobj, kubeassert := fixtures.GetClientsForTest(t)

	clients := &clients{
		kubeclient: kubeclient,
		mcfgclient: mcfgclient,
	}

	cfg := Config{
		MaxRetries:  1,
		UpdateDelay: 0,
	}

	ctrl := newOSBuildController(cfg, mcfgclient, kubeclient)

	// Use a work queue which is tuned for testing.
	// ctrl.execQueue = ctrlcommon.NewWrappedQueueForTesting(t, "test-mosb-controller")

	go ctrl.Run(ctx, 5)

	kubeassert = kubeassert.Eventually().WithContext(ctx).WithPollInterval(time.Millisecond)

	return clients, kubeassert, lobj
}

func setupOSBuildControllerForTestWithBuild(ctx context.Context, t *testing.T, poolName string) (*clients, *mcfgv1alpha1.MachineOSConfig, *mcfgv1alpha1.MachineOSBuild, *mcfgv1.MachineConfigPool, *testhelpers.Assertions) {
	clients, kubeassert, lobj := setupOSBuildControllerForTest(ctx, t)

	mcp := lobj.MachineConfigPool
	mosc := lobj.MachineOSConfig
	mosc.Name = fmt.Sprintf("%s-os-config", poolName)

	_, err := clients.mcfgclient.MachineconfigurationV1alpha1().MachineOSConfigs().Create(ctx, mosc, metav1.CreateOptions{})
	require.NoError(t, err)

	mosb := buildrequest.NewMachineOSBuildFromAPIOrDie(ctx, clients.kubeclient, mosc, mcp)

	return clients, mosc, mosb, mcp, kubeassert
}

func setupOSBuildControllerForTestWithRunningBuild(ctx context.Context, t *testing.T, poolName string) (*clients, *mcfgv1alpha1.MachineOSConfig, *mcfgv1alpha1.MachineOSBuild, *mcfgv1.MachineConfigPool, *testhelpers.Assertions) {
	t.Helper()

	clients, mosc, mosb, mcp, kubeassert := setupOSBuildControllerForTestWithBuild(ctx, t, poolName)

	initialBuildPodName := utils.GetBuildPodName(mosb)

	// After creating the new MachineOSConfig, a MachineOSBuild should be created.
	kubeassert.MachineOSBuildExists(mosb, "Initial MachineOSBuild not created for MachineOSConfig %s", mosc.Name)

	// After a new MachineOSBuild is created, a pod should be created.
	kubeassert.PodExists(initialBuildPodName, "Initial build pod %s did not get created for MachineOSConfig %s", initialBuildPodName, mosc.Name)

	// Set the running status on the pod.
	fixtures.SetPodPhase(ctx, t, clients.kubeclient, mosb, corev1.PodRunning)

	// The MachineOSBuild should be running.
	kubeassert.MachineOSBuildIsRunning(mosb, "Expected the MachineOSBuild %s status to be running", mosb.Name)

	return clients, mosc, mosb, mcp, kubeassert
}

func setupOSBuildControllerForTestWithSuccessfulBuild(ctx context.Context, t *testing.T, poolName string) (*clients, *mcfgv1alpha1.MachineOSConfig, *mcfgv1alpha1.MachineOSBuild, *mcfgv1.MachineConfigPool, *testhelpers.Assertions) {
	t.Helper()

	clients, mosc, mosb, mcp, kubeassert := setupOSBuildControllerForTestWithBuild(ctx, t, poolName)

	kubeassert.MachineOSBuildExists(mosb)
	kubeassert.PodExists(utils.GetBuildPodName(mosb))
	fixtures.SetPodPhase(ctx, t, clients.kubeclient, mosb, corev1.PodSucceeded)
	kubeassert.MachineOSBuildIsSuccessful(mosb)
	kubeassert.PodDoesNotExist(utils.GetBuildPodName(mosb))

	return clients, mosc, mosb, mcp, kubeassert
}

func setupOSBuildControllerForTestWithFailedBuild(ctx context.Context, t *testing.T, poolName string) (*clients, *mcfgv1alpha1.MachineOSConfig, *mcfgv1alpha1.MachineOSBuild, *mcfgv1.MachineConfigPool, *testhelpers.Assertions) {
	t.Helper()

	clients, mosc, mosb, mcp, kubeassert := setupOSBuildControllerForTestWithBuild(ctx, t, poolName)

	initialBuildPodName := utils.GetBuildPodName(mosb)

	// After creating the new MachineOSConfig, a MachineOSBuild should be created.
	kubeassert.MachineOSBuildExists(mosb, "Initial MachineOSBuild not created for MachineOSConfig %s", mosc.Name)
	// After a new MachineOSBuild is created, a pod should be created.
	kubeassert.PodExists(initialBuildPodName, "Initial build pod %s did not get created for MachineOSConfig %s", initialBuildPodName, mosc.Name)
	// Set the running status on the pod.
	fixtures.SetPodPhase(ctx, t, clients.kubeclient, mosb, corev1.PodRunning)
	// The MachineOSBuild should be running.
	kubeassert.MachineOSBuildIsRunning(mosb, "Expected the MachineOSBuild %s status to be running", mosb.Name)

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
