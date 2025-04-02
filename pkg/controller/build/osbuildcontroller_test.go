package build

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakeclientimagev1 "github.com/openshift/client-go/image/clientset/versioned/fake"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	fakeclientmachineconfigv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	fakeclientroutev1 "github.com/openshift/client-go/route/clientset/versioned/fake"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/fixtures"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	testhelpers "github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"

	fakecorev1client "k8s.io/client-go/kubernetes/fake"
)

// TODO: Remove this and deal with the resulting parameter explosion in the test suite.
type clients struct {
	mcfgclient mcfgclientset.Interface
	kubeclient clientset.Interface
}

// This test validates that the OSBuildController does nothing unless
// there is a matching MachineOSConfig for a given MachineConfigPool.
func TestOSBuildControllerDoesNothing(t *testing.T) {

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	_, mcfgclient, _, _, _, _, _ := setupOSBuildControllerForTest(ctx, t)

	// i needs to be set to 2 because rendered-worker-1 already exists.
	for i := 2; i <= 10; i++ {
		insertNewRenderedMachineConfigAndUpdatePool(ctx, t, mcfgclient, "worker", fmt.Sprintf("rendered-worker-%d", i))

		mosbList, err := mcfgclient.MachineconfigurationV1().MachineOSBuilds().List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		assert.Len(t, mosbList.Items, 0)
	}
}

// This test validates that the OSBuildController stops running builds
// when a new MachineOSBuild for a givee MachineOSConfig is created or a new
// rendered MachineConfig is detected on the associated MachineConfigPool.
func TestOSBuildControllerDeletesRunningBuildBeforeStartingANewOne(t *testing.T) {

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	poolName := "worker"

	t.Run("MachineOSConfig change", func(t *testing.T) {

		kubeclient, mcfgclient, _, _, mosc, initialMosb, mcp, kubeassert, _ := setupOSBuildControllerForTestWithRunningBuild(ctx, t, poolName)

		// Now that the build is in the running state, we update the MachineOSConfig.
		apiMosc := testhelpers.SetContainerfileContentsOnMachineOSConfig(ctx, t, mcfgclient, mosc, "FROM configs AS final\nRUN echo 'helloworld' > /etc/helloworld")

		apiMosc, err := mcfgclient.MachineconfigurationV1().MachineOSConfigs().Update(ctx, apiMosc, metav1.UpdateOptions{})
		require.NoError(t, err)

		mosb := buildrequest.NewMachineOSBuildFromAPIOrDie(ctx, kubeclient, apiMosc, mcp)
		buildJobName := utils.GetBuildJobName(mosb)

		// After creating the new MachineOSConfig, a MachineOSBuild should be created.
		kubeassert.MachineOSBuildExists(mosb, "MachineOSBuild not created for MachineOSConfig %s change", mosc.Name)

		// After a new MachineOSBuild is created, a job should be created.
		kubeassert.JobExists(buildJobName, "Build job did not get created for MachineOSConfig %s change", mosc.Name)

		// Set the running status on the job.
		fixtures.SetJobStatus(ctx, t, kubeclient, mosb, fixtures.JobStatus{Active: 1})

		// The MachineOSBuild should be running.
		kubeassert.MachineOSBuildIsRunning(mosb, "Expected the MachineOSBuild %s status to be running", mosb.Name)

		// After the new build starts, the old build should be deleted.
		kubeassert.MachineOSBuildDoesNotExist(initialMosb, "Expected the initial MachineOSBuild %s to be deleted", initialMosb.Name)
		assertBuildObjectsAreDeleted(ctx, t, kubeassert, initialMosb)
		isMachineOSBuildReachedExpectedCount(ctx, t, mcfgclient, mosc, 1)
	})

	t.Run("MachineConfig change", func(t *testing.T) {

		kubeclient, mcfgclient, _, _, mosc, initialMosb, mcp, kubeassert, _ := setupOSBuildControllerForTestWithRunningBuild(ctx, t, poolName)

		apiMCP := insertNewRenderedMachineConfigAndUpdatePool(ctx, t, mcfgclient, mosc.Spec.MachineConfigPool.Name, "rendered-worker-2")

		mosb := buildrequest.NewMachineOSBuildFromAPIOrDie(ctx, kubeclient, mosc, apiMCP)

		buildJobName := utils.GetBuildJobName(mosb)

		// After updating the MachineConfigPool, a new MachineOSBuild should get created.
		kubeassert.MachineOSBuildExists(mosb, "New MachineOSBuild for MachineConfigPool %q update for MachineOSConfig %q never gets created", mcp.Name, mosc.Name)

		// After a new MachineOSBuild is created, a job should be created.
		kubeassert.JobExists(buildJobName, "Build job did not get created for MachineConfigPool %q change", mcp.Name)

		// After the new build starts, the old build should be deleted.
		kubeassert.MachineOSBuildDoesNotExist(initialMosb, "Expected the initial MachineOSBuild %s to be deleted", initialMosb.Name)
		assertBuildObjectsAreDeleted(ctx, t, kubeassert, initialMosb)
		isMachineOSBuildReachedExpectedCount(ctx, t, mcfgclient, mosc, 1)
	})
}

// This test validates that the OSBuildController will not touch old successful
// builds but will still clear running builds before statring a new build for
// the same MachineOSConfig.
func TestOSBuildControllerLeavesSuccessfulBuildAlone(t *testing.T) {

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
	t.Cleanup(cancel)

	poolName := "worker"

	kubeclient, mcfgclient, _, _, firstMosc, firstMosb, mcp, kubeassert := setupOSBuildControllerForTestWithSuccessfulBuild(ctx, t, poolName)

	// Ensures that we have detected the first build.
	isMachineOSBuildReachedExpectedCount(ctx, t, mcfgclient, firstMosc, 1)

	// Creates a MachineOSBuild via a MachineOSConfig change.
	createNewMachineOSBuildViaConfigChange := func(mosc *mcfgv1.MachineOSConfig, containerfileContents string) (*mcfgv1.MachineOSConfig, *mcfgv1.MachineOSBuild) {
		// Modify the MachineOSConfig.
		newMosc := testhelpers.SetContainerfileContentsOnMachineOSConfig(ctx, t, mcfgclient, mosc, containerfileContents)

		// Compute the new MachineOSBuild.
		mosb := buildrequest.NewMachineOSBuildFromAPIOrDie(ctx, kubeclient, newMosc, mcp)

		// Ensure that the MachineOSBuild exists.
		kubeassert.MachineOSBuildExists(mosb)

		// Ensure that the build job exists.
		kubeassert.JobExists(utils.GetBuildJobName(mosb))

		// Set the job status to running.
		fixtures.SetJobStatus(ctx, t, kubeclient, mosb, fixtures.JobStatus{Active: 1})

		// Ensure that the MachineOSBuild gets the running status.
		kubeassert.MachineOSBuildIsRunning(mosb)

		return newMosc, mosb
	}

	// Next, we create the second build which we just leave running.
	secondMosc, secondMosb := createNewMachineOSBuildViaConfigChange(firstMosc, "FROM configs AS final\nRUN echo 'hello' > /etc/hello")

	// Ensure that the build count has increased.
	isMachineOSBuildReachedExpectedCount(ctx, t, mcfgclient, secondMosc, 2)

	// Next, we create the third build.
	thirdMosc, thirdMosb := createNewMachineOSBuildViaConfigChange(firstMosc, "FROM configs AS final\nRUN echo 'helloworld' > /etc/helloworld")
	kubeassert.MachineOSBuildIsRunning(thirdMosb)

	// We ensure that the second build is deleted.
	kubeassert.Now().MachineOSBuildDoesNotExist(secondMosb)
	kubeassert.Now().JobDoesNotExist(utils.GetBuildJobName(secondMosb))

	// We ensure that the first build is still present.
	kubeassert.Now().MachineOSBuildExists(firstMosb)
	kubeassert.Now().MachineOSBuildIsSuccessful(firstMosb)

	// Ensure that the build count has not changed due to the second build being cancelled.
	isMachineOSBuildReachedExpectedCount(ctx, t, mcfgclient, thirdMosc, 2)

	// Set the third build as successful.
	fixtures.SetJobStatus(ctx, t, kubeclient, thirdMosb, fixtures.JobStatus{Succeeded: 1})
	kubeassert.MachineOSBuildIsSuccessful(thirdMosb)
	kubeassert.JobDoesNotExist(utils.GetBuildJobName(thirdMosb))

	// Ensure that the build count has not changed due to the third build completing.
	isMachineOSBuildReachedExpectedCount(ctx, t, mcfgclient, thirdMosc, 2)
}

// This test validates that when a build fails, all of the objects are left
// behind unless someone makes a change to the MachineOSConfig or
// MachineConfigPool.
func TestOSBuildControllerFailure(t *testing.T) {

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	poolName := "worker"

	t.Run("Failed build objects remain", func(t *testing.T) {

		_, _, _, _, _, failedMosb, _, kubeassert := setupOSBuildControllerForTestWithFailedBuild(ctx, t, poolName)

		// Ensure that even after failure, the build objects remain.
		assertBuildObjectsAreCreated(ctx, t, kubeassert, failedMosb)
	})

	t.Run("MachineOSConfig change clears failed build", func(t *testing.T) {

		kubeclient, mcfgclient, _, _, mosc, failedMosb, mcp, kubeassert := setupOSBuildControllerForTestWithFailedBuild(ctx, t, poolName)

		// Modify the MachineOSConfig to start a new build.
		newMosc := testhelpers.SetContainerfileContentsOnMachineOSConfig(ctx, t, mcfgclient, mosc, "FROM configs AS final\nRUN echo 'helloworld' > /etc/helloworld")

		// Compute the new MachineOSBuild.
		newMosb := buildrequest.NewMachineOSBuildFromAPIOrDie(ctx, kubeclient, newMosc, mcp)

		// Ensure that the MachineOSBuild exists.
		kubeassert.MachineOSBuildExists(newMosb)
		// Ensure that the build job exists.
		kubeassert.JobExists(utils.GetBuildJobName(newMosb))
		// Set the job status to running.
		fixtures.SetJobStatus(ctx, t, kubeclient, newMosb, fixtures.JobStatus{Active: 1})
		// Ensure that the MachineOSBuild gets the running status.
		kubeassert.MachineOSBuildIsRunning(newMosb)

		// Ensure that the old build was cleared.
		kubeassert.MachineOSBuildDoesNotExist(failedMosb)
		assertBuildObjectsAreDeleted(ctx, t, kubeassert, failedMosb)
	})

	t.Run("MachineConfig change clears failed build", func(t *testing.T) {

		kubeclient, mcfgclient, _, _, mosc, failedMosb, mcp, kubeassert := setupOSBuildControllerForTestWithFailedBuild(ctx, t, poolName)

		apiMCP := insertNewRenderedMachineConfigAndUpdatePool(ctx, t, mcfgclient, mosc.Spec.MachineConfigPool.Name, "rendered-worker-2")

		mosb := buildrequest.NewMachineOSBuildFromAPIOrDie(ctx, kubeclient, mosc, apiMCP)
		buildJobName := utils.GetBuildJobName(mosb)
		// After updating the MachineConfigPool, a new MachineOSBuild should get created.
		kubeassert.MachineOSBuildExists(mosb, "New MachineOSBuild for MachineConfigPool %q update for MachineOSConfig %q never gets created", mcp.Name, mosc.Name)
		// After a new MachineOSBuild is created, a job should be created.
		kubeassert.JobExists(buildJobName, "Build job did not get created for MachineConfigPool %q change", mcp.Name)

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

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*25)
	t.Cleanup(cancel)

	poolName := "worker"

	getConfigNameForPool := func(num int) string {
		return fmt.Sprintf("rendered-%s-%d", poolName, num)
	}

	t.Run("MachineOSConfig changes creates a new MachineOSBuild", func(t *testing.T) {

		kubeclient, mcfgclient, _, _, mosc, _, _, kubeassert := setupOSBuildControllerForTestWithSuccessfulBuild(ctx, t, poolName)

		// Update the BuildInputs section on the MachineOSConfig and verify that a
		// new MachineOSBuild is produced from it. We'll do this 10 times.
		for i := 0; i <= 5; i++ {
			apiMosc := testhelpers.SetContainerfileContentsOnMachineOSConfig(ctx, t, mcfgclient, mosc, "FROM configs AS final"+fmt.Sprintf("%d", i))

			apiMCP, err := mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(ctx, apiMosc.Spec.MachineConfigPool.Name, metav1.GetOptions{})
			require.NoError(t, err)

			mosb := buildrequest.NewMachineOSBuildFromAPIOrDie(ctx, kubeclient, apiMosc, apiMCP)
			buildJobName := utils.GetBuildJobName(mosb)
			// After creating the new MachineOSConfig, a MachineOSBuild should be created.
			kubeassert.MachineOSBuildExists(mosb, "MachineOSBuild not created for MachineOSConfig %s change, iteration %d", mosc.Name, i)

			assertBuildObjectsAreCreated(ctx, t, kubeassert, mosb)
			// After a new MachineOSBuild is created, a job should be created.
			kubeassert.JobExists(buildJobName, "Build job did not get created for MachineOSConfig %s change", mosc.Name)
			// Set the successful status on the job.
			fixtures.SetJobStatus(ctx, t, kubeclient, mosb, fixtures.JobStatus{Succeeded: 1})
			// The MachineOSBuild should be successful.
			kubeassert.MachineOSBuildIsSuccessful(mosb, "Expected the MachineOSBuild %s status to be successful", mosb.Name)
			// And the build job should be deleted.
			assertBuildObjectsAreDeleted(ctx, t, kubeassert, mosb)
			kubeassert.JobDoesNotExist(buildJobName, "Expected the build job %s to be deleted", buildJobName)

			// Ensure that the MachineOSBuild count increases with each successful build.
			isMachineOSBuildReachedExpectedCount(ctx, t, mcfgclient, apiMosc, i+2)
		}

		// Now, we delete the MachineOSConfig and we expect that all
		// MachineOSBuilds that were created from it are also deleted.
		err := mcfgclient.MachineconfigurationV1().MachineOSConfigs().Delete(ctx, mosc.Name, metav1.DeleteOptions{})
		require.NoError(t, err)

		isMachineOSBuildReachedExpectedCount(ctx, t, mcfgclient, mosc, 0)
	})

	t.Run("MachineConfig changes creates a new MachineOSBuild", func(t *testing.T) {

		kubeclient, mcfgclient, _, _, mosc, _, mcp, kubeassert := setupOSBuildControllerForTestWithSuccessfulBuild(ctx, t, poolName)

		// Update the rendered MachineConfig on the MachineConfigPool and verify that a new MachineOSBuild is produced. We'll do this 10 times.
		for i := 0; i <= 5; i++ {
			apiMosc, err := mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(ctx, mosc.Name, metav1.GetOptions{})
			require.NoError(t, err)

			apiMCP := insertNewRenderedMachineConfigAndUpdatePool(ctx, t, mcfgclient, mosc.Spec.MachineConfigPool.Name, getConfigNameForPool(i+2))

			mosb := buildrequest.NewMachineOSBuildFromAPIOrDie(ctx, kubeclient, apiMosc, apiMCP)
			buildJobName := utils.GetBuildJobName(mosb)
			// After updating the MachineConfigPool, a new MachineOSBuild should get created.
			kubeassert.MachineOSBuildExists(mosb, "New MachineOSBuild for MachineConfigPool %q update for MachineOSConfig %q never gets created", mcp.Name, mosc.Name)
			// After a new MachineOSBuild is created, a job should be created.
			kubeassert.JobExists(buildJobName, "Build job did not get created for MachineConfigPool %q change", mcp.Name)
			// Set the successful status on the job.
			fixtures.SetJobStatus(ctx, t, kubeclient, mosb, fixtures.JobStatus{Succeeded: 1})
			// The MachineOSBuild should be successful.
			kubeassert.MachineOSBuildIsSuccessful(mosb, "Expected the MachineOSBuild %s status to be successful", mosb.Name)
			// And the build job should be deleted.
			kubeassert.JobDoesNotExist(buildJobName, "Expected the build job %s to be deleted", buildJobName)

			// Ensure that the MachineOSBuild count increases with each successful build.
			isMachineOSBuildReachedExpectedCount(ctx, t, mcfgclient, apiMosc, i+2)
		}

		// Now, we delete the MachineOSConfig and we expect that all
		// MachineOSBuilds that were created from it are also deleted.
		err := mcfgclient.MachineconfigurationV1().MachineOSConfigs().Delete(ctx, mosc.Name, metav1.DeleteOptions{})
		require.NoError(t, err)

		isMachineOSBuildReachedExpectedCount(ctx, t, mcfgclient, mosc, 0)
	})
}

func TestOSBuildControllerBuildFailedDoesNotCascade(t *testing.T) {

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	poolName := "worker"
	faultyMC := "rendered-undesiredFaultyMC"

	// Create a MOSC to enable OCL and let it produce a new MOSB in Running State
	_, mcfgclient, _, _, mosc, mosb, mcp, _, ctrl := setupOSBuildControllerForTestWithRunningBuild(ctx, t, poolName)
	assertMachineOSConfigGetsCurrentBuildAnnotation(ctx, t, mcfgclient, mosc, mosb)

	found := func(item *mcfgv1.MachineOSBuild, list []mcfgv1.MachineOSBuild) bool {
		for _, m := range list {
			if m.Name == item.Name {
				return true
			}
		}
		return false
	}

	mosbList, err := mcfgclient.MachineconfigurationV1().MachineOSBuilds().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	if !found(mosb, mosbList.Items) {
		t.Errorf("Expected %v to be in the list %v", mosb.Name, mosbList.Items)
	}

	// This faultyMC represents an older Machine config that passed through API validation checks but if a MOSB (name oldMOSB) were to be built, it would fail to start a job. Hence over here a MC is added but the MCP is not targetting this MCP.
	insertNewRenderedMachineConfig(ctx, t, mcfgclient, poolName, faultyMC)
	now := metav1.Now()
	oldMosb := &mcfgv1.MachineOSBuild{
		TypeMeta: metav1.TypeMeta{
			Kind:       "MachineOSBuild",
			APIVersion: "machineconfiguration.openshift.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "undesiredAndForgottenMOSB",
			Labels: map[string]string{
				constants.TargetMachineConfigPoolLabelKey: mcp.Name,
				constants.RenderedMachineConfigLabelKey:   faultyMC,
				constants.MachineOSConfigNameLabelKey:     mosc.Name,
			},
		},
		Spec: mcfgv1.MachineOSBuildSpec{
			RenderedImagePushSpec: "randRef",
			MachineConfig: mcfgv1.MachineConfigReference{
				Name: faultyMC,
			},
			MachineOSConfig: mcfgv1.MachineOSConfigReference{
				Name: mosc.Name,
			},
		},
		Status: mcfgv1.MachineOSBuildStatus{
			BuildStart: &now,
		},
	}

	// Enqueue another old and un-targeted MOSB to the osbuildcontroller
	_, err = mcfgclient.MachineconfigurationV1().MachineOSBuilds().Create(ctx, oldMosb, metav1.CreateOptions{})
	require.NoError(t, err)
	ctrl.buildReconciler.AddMachineOSBuild(ctx, oldMosb)

	// Assert that the original MOSB which is derived from the current rendered MC that the MCP targets is still building and untouched
	mosbList, err = mcfgclient.MachineconfigurationV1().MachineOSBuilds().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	if !found(mosb, mosbList.Items) {
		t.Errorf("Expected %v to be in the list %v", mosb.Name, mosbList.Items)
	}
}

// This scenario tests the case where the controller restarts and a
// MachineConfig change occurs while a build is already running, while it is
// shutdown. To simulate that, this test shuts down the OSBuildController after
// the initial job gets created, then it rolls a new MachineConfig, then
// finally, it starts OSBuildController again and waits for it to reconcile.
func TestOSBuildControllerReconcilesMachineConfigPoolsAfterRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	poolName := "worker"

	// Gets an OSBuildController with a running job.
	ctrlCtx, ctrlCtxCancel := context.WithCancel(ctx)
	t.Cleanup(ctrlCtxCancel)
	kubeclient, mcfgclient, imageclient, routeclient, mosc, firstMosb, mcp, kubeassert, _ := setupOSBuildControllerForTestWithRunningBuild(ctrlCtx, t, poolName)

	// Stop the OSBuildController.
	ctrlCtxCancel()

	// Create a MachineConfigPool change.
	mcp = insertNewRenderedMachineConfigAndUpdatePool(ctx, t, mcfgclient, poolName, "rendered-worker-2")

	// Get the name of the second MachineOSBuild object.
	secondMosb := buildrequest.NewMachineOSBuildFromAPIOrDie(ctx, kubeclient, mosc, mcp)

	// Ensure that everything still exists.
	kubeassert = kubeassert.Eventually().WithContext(ctx)
	kubeassert.MachineOSBuildExists(firstMosb)
	kubeassert.JobExists(utils.GetBuildJobName(firstMosb))

	// Start OSBuildController (really, get a new instance backed by the same
	// fakeclients as used above).
	_, stop := startController(ctx, t, kubeclient, mcfgclient, imageclient, routeclient)
	t.Cleanup(stop)

	// Assert that the second MachineOSBuild and its job gets created.
	kubeassert.MachineOSBuildExists(secondMosb)
	kubeassert.JobExists(utils.GetBuildJobName(secondMosb))

	// Assert that the first MachineOSBuild goes away.
	kubeassert.MachineOSBuildDoesNotExist(firstMosb)
	kubeassert.JobDoesNotExist(utils.GetBuildJobName(firstMosb))
}

// This scenario tests the case where the controller restarts and a running job
// completes before the reconcilation loop can run. To simulate that, this test
// performs all of the setup steps and creates a successful Job before starting
// the controller.
func TestOSBuildControllerReconcilesJobsAfterRestart(t *testing.T) {
	mainCtx, mainCancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(mainCancel)

	testCases := []struct {
		name       string
		jobStatus  fixtures.JobStatus
		conditions []metav1.Condition
		assertions func(*testhelpers.Assertions, *mcfgv1.MachineOSBuild)
	}{
		{
			name:       "Empty MOSB conditions -> Running",
			jobStatus:  fixtures.JobStatus{Active: 1},
			conditions: []metav1.Condition{},
			assertions: func(kubeassert *testhelpers.Assertions, mosb *mcfgv1.MachineOSBuild) {
				kubeassert.MachineOSBuildIsRunning(mosb)
				kubeassert.JobExists(utils.GetBuildJobName(mosb))
			},
		},
		{
			name:       "Initial MOSB -> Running",
			jobStatus:  fixtures.JobStatus{Active: 1},
			conditions: apihelpers.MachineOSBuildInitialConditions(),
			assertions: func(kubeassert *testhelpers.Assertions, mosb *mcfgv1.MachineOSBuild) {
				kubeassert.MachineOSBuildIsRunning(mosb)
				kubeassert.JobExists(utils.GetBuildJobName(mosb))
			},
		},
		{
			name:       "Running MOSB -> Succeeded",
			jobStatus:  fixtures.JobStatus{Succeeded: 1},
			conditions: apihelpers.MachineOSBuildRunningConditions(),
			assertions: func(kubeassert *testhelpers.Assertions, mosb *mcfgv1.MachineOSBuild) {
				kubeassert.MachineOSBuildIsSuccessful(mosb)
				kubeassert.JobDoesNotExist(utils.GetBuildJobName(mosb))
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(mainCtx)
			t.Cleanup(cancel)

			poolName := "worker"

			kubeclient, mcfgclient, imageclient, routeclient, lobj, kubeassert := fixtures.GetClientsForTest(t)

			kubeassert = kubeassert.Eventually().WithContext(ctx).WithPollInterval(time.Millisecond)
			mcp := lobj.MachineConfigPool
			mosc := lobj.MachineOSConfig
			mosc.Name = fmt.Sprintf("%s-os-config", poolName)

			_, err := mcfgclient.MachineconfigurationV1().MachineOSConfigs().Create(ctx, mosc, metav1.CreateOptions{})
			require.NoError(t, err)

			mosb := buildrequest.NewMachineOSBuildFromAPIOrDie(ctx, kubeclient, mosc, mcp)
			apiMosb, err := mcfgclient.MachineconfigurationV1().MachineOSBuilds().Create(ctx, mosb, metav1.CreateOptions{})
			require.NoError(t, err)

			// This represents the state of the MachineOSBuild before the build
			// controller comes back up after a restart. A job that is in a terminal
			// state will produce a different set of conditions which these conditions
			// will be compared to.
			apiMosb.Status.Conditions = testCase.conditions

			_, err = mcfgclient.MachineconfigurationV1().MachineOSBuilds().UpdateStatus(ctx, apiMosb, metav1.UpdateOptions{})
			require.NoError(t, err)

			br, err := buildrequest.NewBuildRequestFromAPI(ctx, kubeclient, mcfgclient, apiMosb, mosc)
			require.NoError(t, err)

			buildJob := br.Builder().GetObject().(*batchv1.Job)

			_, err = kubeclient.BatchV1().Jobs(ctrlcommon.MCONamespace).Create(ctx, buildJob, metav1.CreateOptions{})
			require.NoError(t, err)

			fixtures.SetJobStatus(ctx, t, kubeclient, mosb, testCase.jobStatus)

			// Start the build controller
			_, stop := startController(ctx, t, kubeclient, mcfgclient, imageclient, routeclient)
			t.Cleanup(stop)

			kubeassert.MachineOSBuildExists(mosb)
			testCase.assertions(kubeassert, mosb)
		})
	}
}

func assertBuildObjectsAreCreated(ctx context.Context, t *testing.T, kubeassert *testhelpers.Assertions, mosb *mcfgv1.MachineOSBuild) {
	t.Helper()

	kubeassert.JobExists(utils.GetBuildJobName(mosb))
	kubeassert.ConfigMapExists(utils.GetContainerfileConfigMapName(mosb))
	kubeassert.ConfigMapExists(utils.GetMCConfigMapName(mosb))
	kubeassert.SecretExists(utils.GetBasePullSecretName(mosb))
	kubeassert.SecretExists(utils.GetFinalPushSecretName(mosb))
}

func assertBuildObjectsAreDeleted(ctx context.Context, t *testing.T, kubeassert *testhelpers.Assertions, mosb *mcfgv1.MachineOSBuild) {
	t.Helper()

	kubeassert.JobDoesNotExist(utils.GetBuildJobName(mosb))
	kubeassert.ConfigMapDoesNotExist(utils.GetContainerfileConfigMapName(mosb))
	kubeassert.ConfigMapDoesNotExist(utils.GetMCConfigMapName(mosb))
	kubeassert.SecretDoesNotExist(utils.GetBasePullSecretName(mosb))
	kubeassert.SecretDoesNotExist(utils.GetFinalPushSecretName(mosb))
}

// Creates a cancellable child context which is passed into the
// OSBuildController instance. Returns the cancel function for the child
// context as well as the OSBuildController instance. Useful for testing
// scenarios where it might be desirable to start and stop the
// OSBuildController.
func startController(ctx context.Context, t *testing.T, kubeclient *fakecorev1client.Clientset, mcfgclient *fakeclientmachineconfigv1.Clientset, imageclient *fakeclientimagev1.Clientset, routeclient *fakeclientroutev1.Clientset) (*OSBuildController, func()) {
	ctrlCtx, ctrlCtxCancel := context.WithCancel(ctx)

	cfg := Config{
		MaxRetries:  1,
		UpdateDelay: 0,
	}

	ctrl := newOSBuildController(cfg, mcfgclient, kubeclient, imageclient, routeclient)

	// Use a work queue which is tuned for testing.
	ctrl.execQueue = ctrlcommon.NewWrappedQueueForTesting(t)

	go ctrl.Run(ctrlCtx, 5)

	return ctrl, ctrlCtxCancel
}

func setupOSBuildControllerForTest(ctx context.Context, t *testing.T) (*fakecorev1client.Clientset, *fakeclientmachineconfigv1.Clientset, *fakeclientimagev1.Clientset, *fakeclientroutev1.Clientset, *testhelpers.Assertions, *fixtures.ObjectsForTest, *OSBuildController) {
	kubeclient, mcfgclient, imageclient, routeclient, lobj, kubeassert := fixtures.GetClientsForTest(t)

	ctrl, _ := startController(ctx, t, kubeclient, mcfgclient, imageclient, routeclient)

	kubeassert = kubeassert.Eventually().WithContext(ctx).WithPollInterval(time.Millisecond)

	return kubeclient, mcfgclient, imageclient, routeclient, kubeassert, lobj, ctrl
}

func setupOSBuildControllerForTestWithBuild(ctx context.Context, t *testing.T, poolName string) (*fakecorev1client.Clientset, *fakeclientmachineconfigv1.Clientset, *fakeclientimagev1.Clientset, *fakeclientroutev1.Clientset, *mcfgv1.MachineOSConfig, *mcfgv1.MachineOSBuild, *mcfgv1.MachineConfigPool, *testhelpers.Assertions, *OSBuildController) {
	kubeclient, mcfgclient, imageclient, routeclient, kubeassert, lobj, ctrl := setupOSBuildControllerForTest(ctx, t)

	mcp := lobj.MachineConfigPool
	mosc := lobj.MachineOSConfig
	mosc.Name = fmt.Sprintf("%s-os-config", poolName)

	_, err := mcfgclient.MachineconfigurationV1().MachineOSConfigs().Create(ctx, mosc, metav1.CreateOptions{})
	require.NoError(t, err)

	mosb := buildrequest.NewMachineOSBuildFromAPIOrDie(ctx, kubeclient, mosc, mcp)

	return kubeclient, mcfgclient, imageclient, routeclient, mosc, mosb, mcp, kubeassert.WithPollInterval(time.Millisecond * 10).WithContext(ctx).Eventually(), ctrl
}

func setupOSBuildControllerForTestWithRunningBuild(ctx context.Context, t *testing.T, poolName string) (*fakecorev1client.Clientset, *fakeclientmachineconfigv1.Clientset, *fakeclientimagev1.Clientset, *fakeclientroutev1.Clientset, *mcfgv1.MachineOSConfig, *mcfgv1.MachineOSBuild, *mcfgv1.MachineConfigPool, *testhelpers.Assertions, *OSBuildController) {
	t.Helper()

	kubeclient, mcfgclient, imageclient, routeclient, mosc, mosb, mcp, kubeassert, ctrl := setupOSBuildControllerForTestWithBuild(ctx, t, poolName)

	initialBuildJobName := utils.GetBuildJobName(mosb)

	// After creating the new MachineOSConfig, a MachineOSBuild should be created.
	kubeassert.MachineOSBuildExists(mosb, "Initial MachineOSBuild not created for MachineOSConfig %s", mosc.Name)

	// After a new MachineOSBuild is created, a job should be created.
	kubeassert.JobExists(initialBuildJobName, "Initial build job %s did not get created for MachineOSConfig %s", initialBuildJobName, mosc.Name)

	// Set the running status on the job.
	fixtures.SetJobStatus(ctx, t, kubeclient, mosb, fixtures.JobStatus{Active: 1})

	// The MachineOSBuild should be running.
	kubeassert.Eventually().WithContext(ctx).MachineOSBuildIsRunning(mosb, "Expected the MachineOSBuild %s status to be running", mosb.Name)

	return kubeclient, mcfgclient, imageclient, routeclient, mosc, mosb, mcp, kubeassert, ctrl
}

func setupOSBuildControllerForTestWithSuccessfulBuild(ctx context.Context, t *testing.T, poolName string) (*fakecorev1client.Clientset, *fakeclientmachineconfigv1.Clientset, *fakeclientimagev1.Clientset, *fakeclientroutev1.Clientset, *mcfgv1.MachineOSConfig, *mcfgv1.MachineOSBuild, *mcfgv1.MachineConfigPool, *testhelpers.Assertions) {
	t.Helper()

	kubeclient, mcfgclient, imageclient, routeclient, mosc, mosb, mcp, kubeassert, _ := setupOSBuildControllerForTestWithRunningBuild(ctx, t, poolName)

	kubeassert.MachineOSBuildExists(mosb)
	kubeassert.JobExists(utils.GetBuildJobName(mosb))
	fixtures.SetJobStatus(ctx, t, kubeclient, mosb, fixtures.JobStatus{Succeeded: 1})
	kubeassert.MachineOSBuildIsSuccessful(mosb)
	kubeassert.JobDoesNotExist(utils.GetBuildJobName(mosb))

	return kubeclient, mcfgclient, imageclient, routeclient, mosc, mosb, mcp, kubeassert
}

func setupOSBuildControllerForTestWithFailedBuild(ctx context.Context, t *testing.T, poolName string) (*fakecorev1client.Clientset, *fakeclientmachineconfigv1.Clientset, *fakeclientimagev1.Clientset, *fakeclientroutev1.Clientset, *mcfgv1.MachineOSConfig, *mcfgv1.MachineOSBuild, *mcfgv1.MachineConfigPool, *testhelpers.Assertions) {
	t.Helper()

	kubeclient, mcfgclient, imageclient, routeclient, mosc, mosb, mcp, kubeassert, _ := setupOSBuildControllerForTestWithBuild(ctx, t, poolName)

	initialBuildJobName := utils.GetBuildJobName(mosb)

	// After creating the new MachineOSConfig, a MachineOSBuild should be created.
	kubeassert.MachineOSBuildExists(mosb, "Initial MachineOSBuild not created for MachineOSConfig %s", mosc.Name)
	// After a new MachineOSBuild is created, a job should be created.
	kubeassert.JobExists(initialBuildJobName, "Initial build job %s did not get created for MachineOSConfig %s", initialBuildJobName, mosc.Name)
	// Set the running status on the job.
	fixtures.SetJobStatus(ctx, t, kubeclient, mosb, fixtures.JobStatus{Active: 1})
	// The MachineOSBuild should be running.
	kubeassert.MachineOSBuildIsRunning(mosb, "Expected the MachineOSBuild %s status to be running", mosb.Name)

	return kubeclient, mcfgclient, imageclient, routeclient, mosc, mosb, mcp, kubeassert
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

func isMachineOSBuildReachedExpectedCount(ctx context.Context, t *testing.T, mcfgclient mcfgclientset.Interface, mosc *mcfgv1.MachineOSConfig, expected int) {
	t.Helper()

	err := wait.PollImmediateInfiniteWithContext(ctx, time.Millisecond, func(ctx context.Context) (bool, error) {
		mosbList, err := mcfgclient.MachineconfigurationV1().MachineOSBuilds().List(ctx, metav1.ListOptions{
			LabelSelector: utils.MachineOSBuildForPoolSelector(mosc).String(),
		})
		if err != nil {
			return false, err
		}

		return len(mosbList.Items) == expected, nil
	})

	require.NoError(t, err, "MachineOSBuild count did not reach expected value %d", expected)
}

func assertMachineOSConfigGetsBuiltImagePushspec(ctx context.Context, t *testing.T, mcfgclient mcfgclientset.Interface, mosc *mcfgv1.MachineOSConfig, pullspec string) {
	t.Helper()

	var foundMosc *mcfgv1.MachineOSConfig

	err := wait.PollImmediateInfiniteWithContext(ctx, time.Millisecond, func(ctx context.Context) (bool, error) {
		apiMosc, err := mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(ctx, mosc.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		foundMosc = apiMosc

		return string(apiMosc.Status.CurrentImagePullSpec) == pullspec, nil
	})

	require.NoError(t, err)
	require.Equal(t, pullspec, string(foundMosc.Status.CurrentImagePullSpec))
}

func assertMachineOSConfigGetsCurrentBuildAnnotation(ctx context.Context, t *testing.T, mcfgclient mcfgclientset.Interface, mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild) {
	t.Helper()

	err := wait.PollImmediateInfiniteWithContext(ctx, time.Millisecond, func(ctx context.Context) (bool, error) {
		apiMosc, err := mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(ctx, mosc.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		val := apiMosc.Annotations[constants.CurrentMachineOSBuildAnnotationKey]
		return val == mosb.Name, nil
	})

	require.NoError(t, err)
}
