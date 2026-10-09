package build

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/containers/image/v5/types"
	"github.com/opencontainers/go-digest"
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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	fakeclock "k8s.io/utils/clock/testing"

	fakecorev1client "k8s.io/client-go/kubernetes/fake"
)

// Provides a fake imagepruner implementation. Currently, we are not validating
// that this is called under certain scenarios because doing so would be very
// difficult with the test suite in its current form.
type fakeImagePruner struct{}

func (f *fakeImagePruner) InspectImage(ctx context.Context, _ string, _ *corev1.Secret, _ *mcfgv1.ControllerConfig) (*types.ImageInspectInfo, *digest.Digest, error) {
	return &types.ImageInspectInfo{}, nil, nil
}

func (f *fakeImagePruner) DeleteImage(ctx context.Context, _ string, _ *corev1.Secret, _ *mcfgv1.ControllerConfig) error {
	return nil
}

// discardEventRecorder keeps controller events synchronous and side-effect
// free. The production broadcaster writes Events to the kube client from its
// own goroutine, which would otherwise be another mutation source to join.
type discardEventRecorder struct{}

func (*discardEventRecorder) Event(k8sruntime.Object, string, string, string) {}

func (*discardEventRecorder) Eventf(k8sruntime.Object, string, string, string, ...interface{}) {}

func (*discardEventRecorder) AnnotatedEventf(k8sruntime.Object, map[string]string, string, string, string, ...interface{}) {
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
	poolName := "worker"

	t.Run("MachineOSConfig change", func(t *testing.T) {
		// Each subtest gets its own timeout derived from context.Background()
		// rather than a single deadline shared across subtests. A shared deadline
		// lets a slow subtest consume the whole budget and cascade into later
		// subtests failing immediately at controller startup ("test controller
		// did not start"). The timeout is only a ceiling, so it never slows a
		// passing run.
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
		t.Cleanup(cancel)

		kubeclient, mcfgclient, imageclient, routeclient, mosc, initialMosb, mcp, kubeassert, lobj, ctrl := setupOSBuildControllerForTestWithRunningBuild(ctx, t, poolName)

		// Now that the build is in the running state, we update the MachineOSConfig.
		apiMosc := testhelpers.SetContainerfileContentsOnMachineOSConfig(ctx, t, mcfgclient, mosc, "FROM configs AS final\nRUN echo 'helloworld' > /etc/helloworld")

		apiMosc, err := mcfgclient.MachineconfigurationV1().MachineOSConfigs().Update(ctx, apiMosc, metav1.UpdateOptions{})
		require.NoError(t, err)

		mosb := buildrequest.NewMachineOSBuildOrDie(buildrequest.MachineOSBuildOpts{
			MachineConfig:     lobj.RenderedMachineConfig,
			MachineOSConfig:   apiMosc,
			MachineConfigPool: mcp,
		})

		buildJobName := utils.GetBuildJobName(mosb)

		// After creating the new MachineOSConfig, a MachineOSBuild should be created.
		kubeassert.MachineOSBuildExists(mosb, "MachineOSBuild not created for MachineOSConfig %s change", mosc.Name)

		// After a new MachineOSBuild is created, a job should be created.
		kubeassert.JobExists(buildJobName, "Build job did not get created for MachineOSConfig %s change", mosc.Name)
		assertMachineOSBuildPrepared(ctx, t, mcfgclient, mosb)

		// The returned controller is managed by t.Cleanup from the restart helper,
		// so the restarted controller is intentionally not captured here.
		_ = setJobStatusAcrossControllerRestart(ctx, t, kubeclient, mcfgclient, imageclient, routeclient, ctrl, mosb, fixtures.JobStatus{Active: 1})

		// The MachineOSBuild should be running.
		kubeassert.MachineOSBuildIsRunning(mosb, "Expected the MachineOSBuild %s status to be running", mosb.Name)

		// After the new build starts, the old build should be deleted.
		kubeassert.MachineOSBuildDoesNotExist(initialMosb, "Expected the initial MachineOSBuild %s to be deleted", initialMosb.Name)
		assertBuildObjectsAreDeleted(t, kubeassert, initialMosb)
		isMachineOSBuildReachedExpectedCount(ctx, t, mcfgclient, mosc, 1)
	})

	t.Run("MachineConfig change", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
		t.Cleanup(cancel)

		_, mcfgclient, _, _, mosc, initialMosb, mcp, kubeassert, _, _ := setupOSBuildControllerForTestWithRunningBuild(ctx, t, poolName)

		apiMCP, apiMC := insertNewRenderedMachineConfigAndUpdatePool(ctx, t, mcfgclient, mosc.Spec.MachineConfigPool.Name, "rendered-worker-2")

		mosb := buildrequest.NewMachineOSBuildOrDie(buildrequest.MachineOSBuildOpts{
			MachineConfig:     apiMC,
			MachineOSConfig:   mosc,
			MachineConfigPool: apiMCP,
		})

		buildJobName := utils.GetBuildJobName(mosb)

		// After updating the MachineConfigPool, a new MachineOSBuild should get created.
		kubeassert.MachineOSBuildExists(mosb, "New MachineOSBuild for MachineConfigPool %q update for MachineOSConfig %q never gets created", mcp.Name, mosc.Name)

		// After a new MachineOSBuild is created, a job should be created.
		kubeassert.JobExists(buildJobName, "Build job did not get created for MachineConfigPool %q change", mcp.Name)

		// After the new build starts, the old build should be deleted.
		kubeassert.MachineOSBuildDoesNotExist(initialMosb, "Expected the initial MachineOSBuild %s to be deleted", initialMosb.Name)
		assertBuildObjectsAreDeleted(t, kubeassert, initialMosb)
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

	kubeclient, mcfgclient, imageclient, routeclient, firstMosc, firstMosb, mcp, lobj, kubeassert, ctrl := setupOSBuildControllerForTestWithSuccessfulBuild(ctx, t, poolName)

	// Ensures that we have detected the first build.
	isMachineOSBuildReachedExpectedCount(ctx, t, mcfgclient, firstMosc, 1)

	// Creates a MachineOSBuild via a MachineOSConfig change.
	createNewMachineOSBuildViaConfigChange := func(mosc *mcfgv1.MachineOSConfig, containerfileContents string) (*mcfgv1.MachineOSConfig, *mcfgv1.MachineOSBuild) {
		// Modify the MachineOSConfig.
		newMosc := testhelpers.SetContainerfileContentsOnMachineOSConfig(ctx, t, mcfgclient, mosc, containerfileContents)

		// Compute the new MachineOSBuild.
		mosb := buildrequest.NewMachineOSBuildOrDie(buildrequest.MachineOSBuildOpts{
			MachineConfig:     lobj.RenderedMachineConfig,
			MachineOSConfig:   newMosc,
			MachineConfigPool: mcp,
		})

		// Ensure that the MachineOSBuild exists.
		kubeassert.MachineOSBuildExists(mosb)

		// Ensure that the build job exists.
		kubeassert.JobExists(utils.GetBuildJobName(mosb))
		assertMachineOSBuildPrepared(ctx, t, mcfgclient, mosb)

		ctrl = setJobStatusAcrossControllerRestart(ctx, t, kubeclient, mcfgclient, imageclient, routeclient, ctrl, mosb, fixtures.JobStatus{Active: 1})

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

	// Set the third build as successful across another joined phase boundary.
	ctrl = setJobStatusAcrossControllerRestart(ctx, t, kubeclient, mcfgclient, imageclient, routeclient, ctrl, thirdMosb, fixtures.JobStatus{Succeeded: 1})
	kubeassert.MachineOSBuildIsSuccessful(thirdMosb)
	kubeassert.JobDoesNotExist(utils.GetBuildJobName(thirdMosb))
	assertMachineOSConfigReferencesMachineOSBuild(ctx, t, mcfgclient, thirdMosc, thirdMosb)
	kubeassert.Now().MachineOSBuildIsSuccessful(firstMosb)
	kubeassert.Now().MachineOSBuildIsSuccessful(thirdMosb)
	kubeassert.Now().JobDoesNotExist(utils.GetBuildJobName(thirdMosb))

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
		_, _, _, _, _, failedMosb, _, kubeassert, _, _ := setupOSBuildControllerForTestWithFailedBuild(ctx, t, poolName)

		// Ensure that even after failure, the build objects remain.
		assertBuildObjectsAreCreated(t, kubeassert, failedMosb)
	})

	t.Run("MachineOSConfig change clears failed build", func(t *testing.T) {
		kubeclient, mcfgclient, imageclient, routeclient, mosc, failedMosb, mcp, kubeassert, lobj, ctrl := setupOSBuildControllerForTestWithFailedBuild(ctx, t, poolName)

		// Modify the MachineOSConfig to start a new build.
		newMosc := testhelpers.SetContainerfileContentsOnMachineOSConfig(ctx, t, mcfgclient, mosc, "FROM configs AS final\nRUN echo 'helloworld' > /etc/helloworld")

		// Compute the new MachineOSBuild.
		newMosb := buildrequest.NewMachineOSBuildOrDie(buildrequest.MachineOSBuildOpts{
			MachineConfig:     lobj.RenderedMachineConfig,
			MachineOSConfig:   newMosc,
			MachineConfigPool: mcp,
		})

		// Ensure that the MachineOSBuild exists.
		kubeassert.MachineOSBuildExists(newMosb)
		// Ensure that the build job exists.
		kubeassert.JobExists(utils.GetBuildJobName(newMosb))
		assertMachineOSBuildPrepared(ctx, t, mcfgclient, newMosb)
		// Set the job status to running. The restarted controller is managed by
		// t.Cleanup from the restart helper, so it is intentionally not captured.
		_ = setJobStatusAcrossControllerRestart(ctx, t, kubeclient, mcfgclient, imageclient, routeclient, ctrl, newMosb, fixtures.JobStatus{Active: 1})
		// Ensure that the MachineOSBuild gets the running status.
		kubeassert.MachineOSBuildIsRunning(newMosb)

		// Ensure that the old build was cleared.
		kubeassert.MachineOSBuildDoesNotExist(failedMosb)
		assertBuildObjectsAreDeleted(t, kubeassert, failedMosb)
	})

	t.Run("MachineConfig change clears failed build", func(t *testing.T) {
		_, mcfgclient, _, _, mosc, failedMosb, mcp, kubeassert, _, _ := setupOSBuildControllerForTestWithFailedBuild(ctx, t, poolName)

		apiMCP, apiMC := insertNewRenderedMachineConfigAndUpdatePool(ctx, t, mcfgclient, mosc.Spec.MachineConfigPool.Name, "rendered-worker-2")

		mosb := buildrequest.NewMachineOSBuildOrDie(buildrequest.MachineOSBuildOpts{
			MachineConfig:     apiMC,
			MachineOSConfig:   mosc,
			MachineConfigPool: apiMCP,
		})

		buildJobName := utils.GetBuildJobName(mosb)
		// After updating the MachineConfigPool, a new MachineOSBuild should get created.
		kubeassert.MachineOSBuildExists(mosb, "New MachineOSBuild for MachineConfigPool %q update for MachineOSConfig %q never gets created", mcp.Name, mosc.Name)
		// After a new MachineOSBuild is created, a job should be created.
		kubeassert.JobExists(buildJobName, "Build job did not get created for MachineConfigPool %q change", mcp.Name)

		// Ensure that the old build was cleared.
		kubeassert.MachineOSBuildDoesNotExist(failedMosb)
		assertBuildObjectsAreDeleted(t, kubeassert, failedMosb)
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
	poolName := "worker"
	getConfigNameForPool := func(num int) string {
		return fmt.Sprintf("rendered-%s-%d", poolName, num)
	}

	t.Run("MachineOSConfig changes creates a new MachineOSBuild", func(t *testing.T) {
		// Per-subtest timeout from context.Background() so subtests do not share a
		// single deadline; a slow subtest must not starve the next one. The timeout
		// is only a ceiling, so it never slows a passing run.
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*25)
		t.Cleanup(cancel)

		kubeclient, mcfgclient, imageclient, routeclient, mosc, initialMosb, _, lobj, kubeassert, ctrl := setupOSBuildControllerForTestWithSuccessfulBuild(ctx, t, poolName)
		builds := []*mcfgv1.MachineOSBuild{initialMosb}

		// Exercise repeated transitions on the same fake clients. Each Job status
		// mutation is separated from informer callbacks and queue workers by a
		// fully joined controller lifecycle boundary.
		for i := 0; i <= 5; i++ {
			apiMosc := testhelpers.SetContainerfileContentsOnMachineOSConfig(ctx, t, mcfgclient, mosc, fmt.Sprintf("FROM configs AS final%d", i))
			apiMCP, err := mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(ctx, apiMosc.Spec.MachineConfigPool.Name, metav1.GetOptions{})
			require.NoError(t, err)

			mosb := buildrequest.NewMachineOSBuildOrDie(buildrequest.MachineOSBuildOpts{
				MachineConfig:     lobj.RenderedMachineConfig,
				MachineOSConfig:   apiMosc,
				MachineConfigPool: apiMCP,
			})
			builds = append(builds, mosb)

			buildJobName := utils.GetBuildJobName(mosb)
			kubeassert.MachineOSBuildExists(mosb, "MachineOSBuild not created for MachineOSConfig %s change, iteration %d", mosc.Name, i)
			assertBuildObjectsAreCreated(t, kubeassert, mosb)
			kubeassert.JobExists(buildJobName, "Build job did not get created for MachineOSConfig %s change", mosc.Name)
			assertMachineOSBuildPrepared(ctx, t, mcfgclient, mosb)

			ctrl = setJobStatusAcrossControllerRestart(ctx, t, kubeclient, mcfgclient, imageclient, routeclient, ctrl, mosb, fixtures.JobStatus{Succeeded: 1})
			kubeassert.MachineOSBuildIsSuccessful(mosb, "Expected the MachineOSBuild %s status to be successful", mosb.Name)
			assertBuildObjectsAreDeleted(t, kubeassert, mosb)
			assertMachineOSConfigReferencesMachineOSBuild(ctx, t, mcfgclient, apiMosc, mosb)
			isMachineOSBuildReachedExpectedCount(ctx, t, mcfgclient, apiMosc, i+2)
			mosc = apiMosc
		}

		// Delete the same MachineOSConfig and prove that every accumulated stale
		// build from the transitions above is cascade-deleted.
		err := mcfgclient.MachineconfigurationV1().MachineOSConfigs().Delete(ctx, mosc.Name, metav1.DeleteOptions{})
		require.NoError(t, err)
		isMachineOSBuildReachedExpectedCount(ctx, t, mcfgclient, mosc, 0)
		for _, mosb := range builds {
			kubeassert.MachineOSBuildDoesNotExist(mosb)
		}
	})

	t.Run("MachineConfig changes creates a new MachineOSBuild", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*25)
		t.Cleanup(cancel)

		kubeclient, mcfgclient, imageclient, routeclient, mosc, initialMosb, mcp, _, kubeassert, ctrl := setupOSBuildControllerForTestWithSuccessfulBuild(ctx, t, poolName)
		builds := []*mcfgv1.MachineOSBuild{initialMosb}

		for i := 0; i <= 5; i++ {
			apiMosc, err := mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(ctx, mosc.Name, metav1.GetOptions{})
			require.NoError(t, err)

			apiMCP, apiMC := insertNewRenderedMachineConfigAndUpdatePool(ctx, t, mcfgclient, mosc.Spec.MachineConfigPool.Name, getConfigNameForPool(i+2))
			mosb := buildrequest.NewMachineOSBuildOrDie(buildrequest.MachineOSBuildOpts{
				MachineConfig:     apiMC,
				MachineOSConfig:   apiMosc,
				MachineConfigPool: apiMCP,
			})
			builds = append(builds, mosb)

			buildJobName := utils.GetBuildJobName(mosb)
			kubeassert.MachineOSBuildExists(mosb, "New MachineOSBuild for MachineConfigPool %q update for MachineOSConfig %q never gets created, iteration %d", mcp.Name, mosc.Name, i)
			kubeassert.JobExists(buildJobName, "Build job did not get created for MachineConfigPool %q change", mcp.Name)
			assertMachineOSBuildPrepared(ctx, t, mcfgclient, mosb)

			ctrl = setJobStatusAcrossControllerRestart(ctx, t, kubeclient, mcfgclient, imageclient, routeclient, ctrl, mosb, fixtures.JobStatus{Succeeded: 1})
			kubeassert.MachineOSBuildIsSuccessful(mosb, "Expected the MachineOSBuild %s status to be successful", mosb.Name)
			kubeassert.JobDoesNotExist(buildJobName, "Expected the build job %s to be deleted", buildJobName)
			assertMachineOSConfigReferencesMachineOSBuild(ctx, t, mcfgclient, apiMosc, mosb)
			isMachineOSBuildReachedExpectedCount(ctx, t, mcfgclient, apiMosc, i+2)
		}

		err := mcfgclient.MachineconfigurationV1().MachineOSConfigs().Delete(ctx, mosc.Name, metav1.DeleteOptions{})
		require.NoError(t, err)
		isMachineOSBuildReachedExpectedCount(ctx, t, mcfgclient, mosc, 0)
		for _, mosb := range builds {
			kubeassert.MachineOSBuildDoesNotExist(mosb)
		}
	})
}

func TestOSBuildControllerBuildFailedDoesNotCascade(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	poolName := "worker"
	faultyMC := "rendered-undesiredFaultyMC"

	// Create a MOSC to enable OCL and let it produce a new MOSB in Running State
	_, mcfgclient, _, _, mosc, mosb, mcp, _, _, ctrl := setupOSBuildControllerForTestWithRunningBuild(ctx, t, poolName)
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
	insertNewRenderedMachineConfig(ctx, t, mcfgclient, poolName, faultyMC, fixtures.OSImageURL)
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
	kubeclient, mcfgclient, imageclient, routeclient, mosc, firstMosb, _, kubeassert, _, ctrl := setupOSBuildControllerForTestWithRunningBuild(ctrlCtx, t, poolName)

	// Stop the OSBuildController and join its callbacks before changing the
	// shared fake clients.
	require.NoError(t, ctrl.stop())

	// Create a MachineConfigPool change.
	apiMCP, apiMC := insertNewRenderedMachineConfigAndUpdatePool(ctx, t, mcfgclient, poolName, "rendered-worker-2")

	// Get the name of the second MachineOSBuild object.
	secondMosb := buildrequest.NewMachineOSBuildOrDie(buildrequest.MachineOSBuildOpts{
		MachineConfig:     apiMC,
		MachineOSConfig:   mosc,
		MachineConfigPool: apiMCP,
	})

	// Ensure that everything still exists.
	kubeassert = kubeassert.Eventually().WithContext(ctx)
	kubeassert.MachineOSBuildExists(firstMosb)
	kubeassert.JobExists(utils.GetBuildJobName(firstMosb))

	// Start OSBuildController (really, get a new instance backed by the same
	// fakeclients as used above).
	startController(ctx, t, kubeclient, mcfgclient, imageclient, routeclient)

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

			mosb := buildrequest.NewMachineOSBuildOrDie(buildrequest.MachineOSBuildOpts{
				MachineConfig:     lobj.RenderedMachineConfig,
				MachineOSConfig:   mosc,
				MachineConfigPool: mcp,
			})

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
			startController(ctx, t, kubeclient, mcfgclient, imageclient, routeclient)

			kubeassert.MachineOSBuildExists(mosb)
			testCase.assertions(kubeassert, mosb)
		})
	}
}

func assertBuildObjectsAreCreated(t *testing.T, kubeassert *testhelpers.Assertions, mosb *mcfgv1.MachineOSBuild) {
	t.Helper()

	kubeassert.JobExists(utils.GetBuildJobName(mosb))
	kubeassert.ConfigMapExists(utils.GetContainerfileConfigMapName(mosb))
	kubeassert.ConfigMapExists(utils.GetMCConfigMapName(mosb))
	kubeassert.SecretExists(utils.GetBasePullSecretName(mosb))
	kubeassert.SecretExists(utils.GetFinalPushSecretName(mosb))
}

func assertBuildObjectsAreDeleted(t *testing.T, kubeassert *testhelpers.Assertions, mosb *mcfgv1.MachineOSBuild) {
	t.Helper()

	kubeassert.JobDoesNotExist(utils.GetBuildJobName(mosb))
	kubeassert.ConfigMapDoesNotExist(utils.GetContainerfileConfigMapName(mosb))
	kubeassert.ConfigMapDoesNotExist(utils.GetMCConfigMapName(mosb))
	kubeassert.SecretDoesNotExist(utils.GetBasePullSecretName(mosb))
	kubeassert.SecretDoesNotExist(utils.GetFinalPushSecretName(mosb))
}

func TestControllerStopJoinsInformerCallbacks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	kubeclient, mcfgclient, imageclient, routeclient, _, _ := fixtures.GetClientsForTest(t)
	firstCallbackStarted := make(chan struct{})
	secondCallbackObserved := make(chan struct{})
	secondCallbackBuffered := make(chan struct{})
	secondCallbackStarted := make(chan struct{})
	firstCallbackDone := make(chan struct{})
	secondCallbackDone := make(chan struct{})
	releaseFirstCallback := make(chan struct{})
	releaseSecondCallback := make(chan struct{})
	callbackErr := make(chan error, 2)
	var releaseFirstOnce sync.Once
	var releaseSecondOnce sync.Once
	releaseFirst := func() {
		releaseFirstOnce.Do(func() { close(releaseFirstCallback) })
	}
	releaseSecond := func() {
		releaseSecondOnce.Do(func() { close(releaseSecondCallback) })
	}
	releaseAll := func() {
		releaseFirst()
		releaseSecond()
	}
	t.Cleanup(releaseAll)

	ctrl := startControllerWithOptions(ctx, t, kubeclient, mcfgclient, imageclient, routeclient, testControllerOptions{
		releaseOnStopTimeout: releaseAll,
		beforeRun: func(ctrl *OSBuildController) {
			_, err := ctrl.machineConfigInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {
					mc := obj.(*mcfgv1.MachineConfig)
					var callbackDone chan struct{}
					switch mc.Name {
					case "lifecycle-probe-first":
						close(firstCallbackStarted)
						select {
						case <-releaseFirstCallback:
						case <-ctx.Done():
							return
						}
						callbackDone = firstCallbackDone
					case "lifecycle-probe-second":
						close(secondCallbackStarted)
						select {
						case <-releaseSecondCallback:
						case <-ctx.Done():
							return
						}
						callbackDone = secondCallbackDone
					default:
						return
					}

					_, err := kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Create(context.Background(), &corev1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{Name: mc.Name},
					}, metav1.CreateOptions{})
					callbackErr <- err
					close(callbackDone)
				},
			})
			require.NoError(t, err)

			// A later observer event is used as a distribution fence below. Once
			// that event reaches this listener, distribution of the preceding
			// second event has completed for every processorListener.
			_, err = ctrl.machineConfigInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {
					switch obj.(*mcfgv1.MachineConfig).Name {
					case "lifecycle-probe-second":
						close(secondCallbackObserved)
					case "lifecycle-probe-distribution-fence":
						close(secondCallbackBuffered)
					}
				},
			})
			require.NoError(t, err)
		},
	})

	_, err := mcfgclient.MachineconfigurationV1().MachineConfigs().Create(ctx, &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "lifecycle-probe-first"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	select {
	case <-firstCallbackStarted:
	case <-ctx.Done():
		require.NoError(t, ctx.Err(), "first informer callback did not start")
	}

	_, err = mcfgclient.MachineconfigurationV1().MachineConfigs().Create(ctx, &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "lifecycle-probe-second"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	select {
	case <-secondCallbackObserved:
	case <-ctx.Done():
		require.NoError(t, ctx.Err(), "observer did not receive the second informer event")
	}

	// The reflector cannot process this fence until it has finished distributing
	// the second event. Receiving the fence therefore proves the blocked
	// listener accepted the second event into processorListener's pending buffer.
	_, err = mcfgclient.MachineconfigurationV1().MachineConfigs().Create(ctx, &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "lifecycle-probe-distribution-fence"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	select {
	case <-secondCallbackBuffered:
	case <-ctx.Done():
		require.NoError(t, ctx.Err(), "second informer callback was not buffered before shutdown")
	}

	stopDone := make(chan error, 1)
	go func() { stopDone <- ctrl.stop() }()

	select {
	case <-ctrl.runDone:
	case err := <-stopDone:
		t.Fatalf("controller stop returned before the informer callback completed: %v", err)
	case <-ctx.Done():
		require.NoError(t, ctx.Err(), "controller Run did not stop")
	}

	select {
	case err := <-stopDone:
		t.Fatalf("controller stop returned before joining its informer callback: %v", err)
	default:
	}

	releaseFirst()
	select {
	case <-firstCallbackDone:
	case <-ctx.Done():
		require.NoError(t, ctx.Err(), "first informer callback did not finish")
	}
	callbacksCompleted := 1
	select {
	case <-secondCallbackStarted:
		// Delivery won the race with listener shutdown. Shutdown must now join
		// this callback just as it joined the callback that was already running.
		select {
		case err := <-stopDone:
			t.Fatalf("controller stop returned before joining the buffered informer callback: %v", err)
		default:
		}
		releaseSecond()
		select {
		case err := <-stopDone:
			require.NoError(t, err)
		case <-ctx.Done():
			require.NoError(t, ctx.Err(), "controller did not stop after releasing the buffered callback")
		}
		select {
		case <-secondCallbackDone:
		case <-ctx.Done():
			require.NoError(t, ctx.Err(), "buffered informer callback did not finish")
		}
		callbacksCompleted++
	case err := <-stopDone:
		// Listener shutdown won the race and discarded the buffered event. A
		// stopped listener cannot deliver it later or mutate the old client.
		require.NoError(t, err)
	case <-ctx.Done():
		require.NoError(t, ctx.Err(), "controller did not join or discard the buffered informer callback")
	}

	releaseSecond()
	select {
	case <-secondCallbackStarted:
		if callbacksCompleted == 1 {
			t.Fatal("buffered informer callback started after controller stop returned")
		}
	default:
	}
	for i := 0; i < callbacksCompleted; i++ {
		select {
		case err := <-callbackErr:
			require.NoError(t, err)
		case <-ctx.Done():
			require.NoError(t, ctx.Err(), "informer callback did not report its result")
		}
	}
	_, err = kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, "lifecycle-probe-first", metav1.GetOptions{})
	require.NoError(t, err)
	_, err = kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, "lifecycle-probe-second", metav1.GetOptions{})
	if callbacksCompleted == 2 {
		require.NoError(t, err)
	} else {
		require.True(t, apierrors.IsNotFound(err), "discarded callback unexpectedly mutated the old client: %v", err)
	}
}

func TestControllerStopTimeoutJoinsLifecycleWaiter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	kubeclient, mcfgclient, imageclient, routeclient, _, _ := fixtures.GetClientsForTest(t)
	callbackStarted := make(chan struct{})
	callbackDone := make(chan struct{})
	releaseCallback := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseCallback) })
	}
	t.Cleanup(release)

	ctrl := startControllerWithOptions(ctx, t, kubeclient, mcfgclient, imageclient, routeclient, testControllerOptions{
		stopTimeout:          time.Nanosecond,
		releaseOnStopTimeout: release,
		expectStopError:      true,
		beforeRun: func(ctrl *OSBuildController) {
			_, err := ctrl.machineConfigInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {
					if obj.(*mcfgv1.MachineConfig).Name != "lifecycle-timeout-probe" {
						return
					}
					close(callbackStarted)
					select {
					case <-releaseCallback:
					case <-ctx.Done():
						return
					}
					close(callbackDone)
				},
			})
			require.NoError(t, err)
		},
	})

	_, err := mcfgclient.MachineconfigurationV1().MachineConfigs().Create(ctx, &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "lifecycle-timeout-probe"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	select {
	case <-callbackStarted:
	case <-ctx.Done():
		require.NoError(t, ctx.Err(), "timeout probe callback did not start")
	}

	require.ErrorContains(t, ctrl.stop(), "test controller lifecycle did not stop")
	select {
	case <-callbackDone:
		// Timeout recovery released the callback and joined the lifecycle waiter
		// before stop returned its diagnostic error.
	default:
		t.Fatal("stop returned from its timeout path before joining the lifecycle waiter")
	}
}

func TestControllerStopTimeoutWithoutReleaseHookJoinsWorkers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	kubeclient, mcfgclient, imageclient, routeclient, _, _ := fixtures.GetClientsForTest(t)
	ctrl := startControllerWithOptions(ctx, t, kubeclient, mcfgclient, imageclient, routeclient, testControllerOptions{
		stopTimeout:     time.Nanosecond,
		expectStopError: true,
	})

	workerStarted := make(chan struct{})
	workerDone := make(chan struct{})
	ctrl.execQueue.EnqueueWithName("bounded default timeout blocker", func() error {
		close(workerStarted)
		defer close(workerDone)
		timer := time.NewTimer(10 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
		}
		return nil
	})
	select {
	case <-workerStarted:
	case <-ctx.Done():
		require.NoError(t, ctx.Err(), "default timeout worker did not start")
	}

	require.ErrorContains(t, ctrl.stop(), "test controller lifecycle did not stop")
	select {
	case <-workerDone:
		// Even without a release hook, stop did not return until the worker and
		// the lifecycle owner had both completed.
	default:
		t.Fatal("default timeout path returned before joining its worker")
	}
	select {
	case <-ctrl.runDone:
	default:
		t.Fatal("default timeout path returned before controller Run completed")
	}
}

func TestControllerStopDiscardsDelayedRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	kubeclient, mcfgclient, imageclient, routeclient, _, _ := fixtures.GetClientsForTest(t)
	retryDelay := time.Hour
	queueClock := fakeclock.NewFakeClock(time.Now())
	queue, rateLimited := ctrlcommon.NewWrappedQueueWithClockForTesting(t, queueClock, retryDelay)

	workersStarted := make(chan struct{}, testControllerWorkers)
	releaseWorkers := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseWorkers) })
	}
	t.Cleanup(release)
	ctrl := startControllerWithOptions(ctx, t, kubeclient, mcfgclient, imageclient, routeclient, testControllerOptions{
		queue:                queue,
		releaseOnStopTimeout: release,
	})

	for i := 0; i < testControllerWorkers-1; i++ {
		ctrl.execQueue.EnqueueWithName("test delayed retry blocker", func() error {
			workersStarted <- struct{}{}
			select {
			case <-releaseWorkers:
			case <-ctx.Done():
			}
			return nil
		})
	}
	for i := 0; i < testControllerWorkers-1; i++ {
		select {
		case <-workersStarted:
		case <-ctx.Done():
			require.NoError(t, ctx.Err(), "worker blocker %d did not start", i+1)
		}
	}

	var retryCalls atomic.Int32
	firstRetryCall := make(chan struct{})
	reentered := make(chan struct{}, 1)
	ctrl.execQueue.EnqueueWithName("test delayed retry", func() error {
		if retryCalls.Add(1) == 1 {
			close(firstRetryCall)
		} else {
			reentered <- struct{}{}
		}
		return errors.New("schedule a delayed retry")
	})
	select {
	case <-firstRetryCall:
	case <-ctx.Done():
		require.NoError(t, ctx.Err(), "retrying callback did not run")
	}
	select {
	case <-rateLimited:
		// This is the synchronization point proving handleErr used
		// AddRateLimited rather than the exhausted-retry AddAfter branch.
	case <-ctx.Done():
		require.NoError(t, ctx.Err(), "callback did not schedule a rate-limited retry")
	}

	markerStarted := make(chan struct{})
	ctrl.execQueue.EnqueueWithName("test delayed retry marker", func() error {
		close(markerStarted)
		select {
		case <-releaseWorkers:
		case <-ctx.Done():
		}
		return nil
	})
	select {
	case <-markerStarted:
	case <-ctx.Done():
		require.NoError(t, ctx.Err(), "retry scheduling marker did not start")
	}

	stopDone := make(chan error, 1)
	go func() { stopDone <- ctrl.stop() }()
	select {
	case <-ctrl.runDone:
		// Run closes only after queue shutdown has rejected delayed additions.
	case err := <-stopDone:
		t.Fatalf("controller stop returned before Run completed: %v", err)
	case <-ctx.Done():
		require.NoError(t, ctx.Err(), "controller Run did not stop")
	}

	// The rate-limited item was accepted by the delaying queue before the marker
	// started. Advance its injected clock only after Run has shut the queue down;
	// if the delayed item is promoted after that boundary, one of the held
	// production-equivalent workers will execute it when released below.
	queueClock.Step(retryDelay)
	release()
	select {
	case err := <-stopDone:
		require.NoError(t, err)
	case <-ctx.Done():
		require.NoError(t, ctx.Err(), "controller did not stop after releasing queue workers")
	}
	require.Equal(t, int32(1), retryCalls.Load())
	select {
	case <-reentered:
		t.Fatal("delayed retry reentered after the lifecycle boundary")
	default:
	}
}

// testOSBuildController owns the complete test lifecycle. A stopped instance
// has no informer listener, delayed retry, worker, or event sink that can
// mutate its fake clients.
type testOSBuildController struct {
	*OSBuildController
	stop    func() error
	runDone <-chan struct{}
}

const testControllerWorkers = 4

type testControllerOptions struct {
	queue                *ctrlcommon.WrappedQueue
	beforeRun            func(*OSBuildController)
	stopTimeout          time.Duration
	releaseOnStopTimeout func()
	expectStopError      bool
}

func startController(ctx context.Context, t *testing.T, kubeclient *fakecorev1client.Clientset, mcfgclient *fakeclientmachineconfigv1.Clientset, imageclient *fakeclientimagev1.Clientset, routeclient *fakeclientroutev1.Clientset) *testOSBuildController {
	return startControllerWithOptions(ctx, t, kubeclient, mcfgclient, imageclient, routeclient, testControllerOptions{})
}

func setJobStatusAcrossControllerRestart(
	ctx context.Context,
	t *testing.T,
	kubeclient *fakecorev1client.Clientset,
	mcfgclient *fakeclientmachineconfigv1.Clientset,
	imageclient *fakeclientimagev1.Clientset,
	routeclient *fakeclientroutev1.Clientset,
	ctrl *testOSBuildController,
	mosb *mcfgv1.MachineOSBuild,
	status fixtures.JobStatus,
) *testOSBuildController {
	t.Helper()

	require.NoError(t, ctrl.stop())
	fixtures.SetJobStatus(ctx, t, kubeclient, mosb, status)
	return startController(ctx, t, kubeclient, mcfgclient, imageclient, routeclient)
}

func startControllerWithOptions(ctx context.Context, t *testing.T, kubeclient *fakecorev1client.Clientset, mcfgclient *fakeclientmachineconfigv1.Clientset, imageclient *fakeclientimagev1.Clientset, routeclient *fakeclientroutev1.Clientset, opts testControllerOptions) *testOSBuildController {
	ctrlCtx, ctrlCtxCancel := context.WithCancel(ctx)
	if opts.stopTimeout == 0 {
		opts.stopTimeout = 5 * time.Second
	}

	cfg := Config{
		MaxRetries:           1,
		UpdateDelay:          0,
		MaxShutdownDelay:     time.Millisecond,
		ShutdownPollInterval: time.Nanosecond,
	}

	testRecorder := &discardEventRecorder{}
	ctrl := newOSBuildControllerWithEventRecorder(cfg, mcfgclient, kubeclient, imageclient, routeclient, &fakeImagePruner{}, testRecorder)
	if opts.queue == nil {
		opts.queue = ctrlcommon.NewWrappedQueueForTesting(t)
	}
	ctrl.execQueue = opts.queue

	if opts.beforeRun != nil {
		opts.beforeRun(ctrl)
	}

	runDone := make(chan struct{})
	runStarted := make(chan struct{})
	ctrl.runStarted = runStarted
	testCtrl := &testOSBuildController{
		OSBuildController: ctrl,
		runDone:           runDone,
	}

	go func() {
		defer close(runDone)
		// WrappedQueue.Start counts from zero, so passing three starts the same
		// four workers used by production.
		ctrl.Run(ctrlCtx, testControllerWorkers-1)
	}()
	select {
	case <-runStarted:
	case <-ctx.Done():
		require.NoError(t, ctx.Err(), "test controller did not start")
	}

	var stopOnce sync.Once
	var stopErr error
	stopFunc := func() error {
		stopOnce.Do(func() {
			ctrlCtxCancel()

			// The caller owns the lifecycle join. A watchdog only reports the
			// deadline and releases any deliberate test obstruction; it is joined
			// before stop returns. Consequently no timeout path can abandon an
			// informer/worker waiter in a background goroutine.
			joined := make(chan struct{})
			timedOut := make(chan bool, 1)
			shutdownTimer := time.NewTimer(opts.stopTimeout)
			go func() {
				select {
				case <-joined:
					// If the timer already fired, the deadline was missed even when
					// both select cases became ready before this goroutine ran.
					timedOut <- !shutdownTimer.Stop()
				case <-shutdownTimer.C:
					if opts.releaseOnStopTimeout != nil {
						opts.releaseOnStopTimeout()
					}
					timedOut <- true
				}
			}()

			<-runDone
			var lifecycleErr error
			for _, startable := range ctrl.informers.toStart {
				factory, ok := startable.(interface{ Shutdown() })
				if !ok {
					lifecycleErr = errors.Join(lifecycleErr, fmt.Errorf("informer factory %T has no shutdown join", startable))
					continue
				}
				factory.Shutdown()
			}
			ctrl.execQueue.WaitForWorkers()
			close(joined)
			if <-timedOut {
				stopErr = errors.Join(fmt.Errorf("test controller lifecycle did not stop: %w", context.DeadlineExceeded), lifecycleErr)
				return
			}
			stopErr = lifecycleErr
		})
		return stopErr
	}
	testCtrl.stop = stopFunc

	t.Cleanup(func() {
		if opts.expectStopError {
			require.Error(t, testCtrl.stop())
			return
		}
		require.NoError(t, testCtrl.stop())
	})

	return testCtrl
}

func setupOSBuildControllerForTest(ctx context.Context, t *testing.T) (*fakecorev1client.Clientset, *fakeclientmachineconfigv1.Clientset, *fakeclientimagev1.Clientset, *fakeclientroutev1.Clientset, *testhelpers.Assertions, *fixtures.ObjectsForTest, *testOSBuildController) {
	kubeclient, mcfgclient, imageclient, routeclient, lobj, kubeassert := fixtures.GetClientsForTest(t)

	ctrl := startController(ctx, t, kubeclient, mcfgclient, imageclient, routeclient)

	kubeassert = kubeassert.Eventually().WithContext(ctx).WithPollInterval(time.Millisecond)

	return kubeclient, mcfgclient, imageclient, routeclient, kubeassert, lobj, ctrl
}

func setupOSBuildControllerForTestWithBuild(ctx context.Context, t *testing.T, poolName string) (*fakecorev1client.Clientset, *fakeclientmachineconfigv1.Clientset, *fakeclientimagev1.Clientset, *fakeclientroutev1.Clientset, *mcfgv1.MachineOSConfig, *mcfgv1.MachineOSBuild, *mcfgv1.MachineConfigPool, *testhelpers.Assertions, *fixtures.ObjectsForTest, *testOSBuildController) {
	kubeclient, mcfgclient, imageclient, routeclient, kubeassert, lobj, ctrl := setupOSBuildControllerForTest(ctx, t)

	mcp := lobj.MachineConfigPool
	mosc := lobj.MachineOSConfig
	mosc.Name = fmt.Sprintf("%s-os-config", poolName)

	_, err := mcfgclient.MachineconfigurationV1().MachineOSConfigs().Create(ctx, mosc, metav1.CreateOptions{})
	require.NoError(t, err)

	mosb := buildrequest.NewMachineOSBuildOrDie(buildrequest.MachineOSBuildOpts{
		MachineConfig:     lobj.RenderedMachineConfig,
		MachineOSConfig:   mosc,
		MachineConfigPool: mcp,
	})

	return kubeclient, mcfgclient, imageclient, routeclient, mosc, mosb, mcp, kubeassert.WithPollInterval(time.Millisecond * 10).WithContext(ctx).Eventually(), lobj, ctrl
}

func setupOSBuildControllerForTestWithRunningBuild(ctx context.Context, t *testing.T, poolName string) (*fakecorev1client.Clientset, *fakeclientmachineconfigv1.Clientset, *fakeclientimagev1.Clientset, *fakeclientroutev1.Clientset, *mcfgv1.MachineOSConfig, *mcfgv1.MachineOSBuild, *mcfgv1.MachineConfigPool, *testhelpers.Assertions, *fixtures.ObjectsForTest, *testOSBuildController) {
	t.Helper()

	kubeclient, mcfgclient, imageclient, routeclient, mosc, mosb, mcp, kubeassert, lobj, ctrl := setupOSBuildControllerForTestWithBuild(ctx, t, poolName)
	initialBuildJobName := utils.GetBuildJobName(mosb)

	// After creating the new MachineOSConfig, a MachineOSBuild should be created.
	kubeassert.MachineOSBuildExists(mosb, "Initial MachineOSBuild not created for MachineOSConfig %s", mosc.Name)

	// After a new MachineOSBuild is created, a job should be created.
	kubeassert.JobExists(initialBuildJobName, "Initial build job %s did not get created for MachineOSConfig %s", initialBuildJobName, mosc.Name)
	assertMachineOSBuildPrepared(ctx, t, mcfgclient, mosb)

	ctrl = setJobStatusAcrossControllerRestart(ctx, t, kubeclient, mcfgclient, imageclient, routeclient, ctrl, mosb, fixtures.JobStatus{Active: 1})
	// The MachineOSBuild should be running.
	kubeassert.Eventually().WithContext(ctx).MachineOSBuildIsRunning(mosb, "Expected the MachineOSBuild %s status to be running", mosb.Name)

	return kubeclient, mcfgclient, imageclient, routeclient, mosc, mosb, mcp, kubeassert, lobj, ctrl
}

func setupOSBuildControllerForTestWithSuccessfulBuild(ctx context.Context, t *testing.T, poolName string) (*fakecorev1client.Clientset, *fakeclientmachineconfigv1.Clientset, *fakeclientimagev1.Clientset, *fakeclientroutev1.Clientset, *mcfgv1.MachineOSConfig, *mcfgv1.MachineOSBuild, *mcfgv1.MachineConfigPool, *fixtures.ObjectsForTest, *testhelpers.Assertions, *testOSBuildController) {
	t.Helper()

	kubeclient, mcfgclient, imageclient, routeclient, mosc, mosb, mcp, kubeassert, lobj, ctrl := setupOSBuildControllerForTestWithRunningBuild(ctx, t, poolName)
	kubeassert.MachineOSBuildExists(mosb)
	kubeassert.JobExists(utils.GetBuildJobName(mosb))
	ctrl = setJobStatusAcrossControllerRestart(ctx, t, kubeclient, mcfgclient, imageclient, routeclient, ctrl, mosb, fixtures.JobStatus{Succeeded: 1})
	kubeassert.MachineOSBuildIsSuccessful(mosb)
	kubeassert.JobDoesNotExist(utils.GetBuildJobName(mosb))
	assertMachineOSConfigReferencesMachineOSBuild(ctx, t, mcfgclient, mosc, mosb)

	return kubeclient, mcfgclient, imageclient, routeclient, mosc, mosb, mcp, lobj, kubeassert, ctrl
}

func setupOSBuildControllerForTestWithFailedBuild(ctx context.Context, t *testing.T, poolName string) (*fakecorev1client.Clientset, *fakeclientmachineconfigv1.Clientset, *fakeclientimagev1.Clientset, *fakeclientroutev1.Clientset, *mcfgv1.MachineOSConfig, *mcfgv1.MachineOSBuild, *mcfgv1.MachineConfigPool, *testhelpers.Assertions, *fixtures.ObjectsForTest, *testOSBuildController) {
	t.Helper()

	kubeclient, mcfgclient, imageclient, routeclient, mosc, mosb, mcp, kubeassert, lobj, ctrl := setupOSBuildControllerForTestWithBuild(ctx, t, poolName)

	initialBuildJobName := utils.GetBuildJobName(mosb)

	// After creating the new MachineOSConfig, a MachineOSBuild should be created.
	kubeassert.MachineOSBuildExists(mosb, "Initial MachineOSBuild not created for MachineOSConfig %s", mosc.Name)
	// After a new MachineOSBuild is created, a job should be created.
	kubeassert.JobExists(initialBuildJobName, "Initial build job %s did not get created for MachineOSConfig %s", initialBuildJobName, mosc.Name)
	assertMachineOSBuildPrepared(ctx, t, mcfgclient, mosb)
	// Set the running status on the job.
	ctrl = setJobStatusAcrossControllerRestart(ctx, t, kubeclient, mcfgclient, imageclient, routeclient, ctrl, mosb, fixtures.JobStatus{Active: 1})
	// The MachineOSBuild should be running.
	kubeassert.MachineOSBuildIsRunning(mosb, "Expected the MachineOSBuild %s status to be running", mosb.Name)

	return kubeclient, mcfgclient, imageclient, routeclient, mosc, mosb, mcp, kubeassert, lobj, ctrl
}

func insertNewRenderedMachineConfigAndUpdatePool(ctx context.Context, t *testing.T, mcfgclient mcfgclientset.Interface, poolName, renderedName string) (*mcfgv1.MachineConfigPool, *mcfgv1.MachineConfig) {
	mcp, err := mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(ctx, poolName, metav1.GetOptions{})
	require.NoError(t, err)

	mc := insertNewRenderedMachineConfig(ctx, t, mcfgclient, poolName, renderedName, fixtures.OSImageURL)

	mcp.Spec.Configuration.Name = renderedName

	mcp, err = mcfgclient.MachineconfigurationV1().MachineConfigPools().Update(ctx, mcp, metav1.UpdateOptions{})
	require.NoError(t, err)

	return mcp, mc
}

func insertNewRenderedMachineConfig(ctx context.Context, t *testing.T, mcfgclient mcfgclientset.Interface, poolName, renderedName string, osImageURL string) *mcfgv1.MachineConfig {
	mc := fixtures.NewObjectsForTest(poolName).RenderedMachineConfig
	mc.Name = renderedName
	mc.Spec.OSImageURL = osImageURL

	apiMC, err := mcfgclient.MachineconfigurationV1().MachineConfigs().Create(ctx, mc, metav1.CreateOptions{})
	require.NoError(t, err)

	return apiMC
}

func insertNewRenderedMachineConfigWithoutImageChangeAndUpdatePool(ctx context.Context, t *testing.T, mcfgclient mcfgclientset.Interface, poolName, renderedName string) (*mcfgv1.MachineConfigPool, *mcfgv1.MachineConfig) {
	mcp, err := mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(ctx, poolName, metav1.GetOptions{})
	require.NoError(t, err)

	mc := insertNewRenderedMachineConfigWithoutImageChange(ctx, t, mcfgclient, poolName, renderedName)

	mcp.Spec.Configuration.Name = renderedName

	mcp, err = mcfgclient.MachineconfigurationV1().MachineConfigPools().Update(ctx, mcp, metav1.UpdateOptions{})
	require.NoError(t, err)

	return mcp, mc
}

func insertNewRenderedMachineConfigWithoutImageChange(ctx context.Context, t *testing.T, mcfgclient mcfgclientset.Interface, poolName, renderedName string) *mcfgv1.MachineConfig {
	mc := fixtures.NewObjectsForTest(poolName).RenderedMachineConfig
	mc.Name = renderedName

	apiMC, err := mcfgclient.MachineconfigurationV1().MachineConfigs().Create(ctx, mc, metav1.CreateOptions{})
	require.NoError(t, err)

	return apiMC
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

// Wait until the Job add callback has recorded its initial observation. A Job
// existing in the fake client does not prove that its informer callback has
// run, and changing the Job status before this point lets the stale add event
// overwrite a later Running or Succeeded observation.
func assertMachineOSBuildPrepared(ctx context.Context, t *testing.T, mcfgclient mcfgclientset.Interface, mosb *mcfgv1.MachineOSBuild) {
	t.Helper()

	err := wait.PollImmediateInfiniteWithContext(ctx, time.Millisecond, func(ctx context.Context) (bool, error) {
		apiMosb, err := mcfgclient.MachineconfigurationV1().MachineOSBuilds().Get(ctx, mosb.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		return apihelpers.IsMachineOSBuildConditionTrue(apiMosb.Status.Conditions, mcfgv1.MachineOSBuildPrepared), nil
	})

	require.NoError(t, err, "MachineOSBuild %s was not prepared before its Job status changed", mosb.Name)
}

// Waits until the completed MachineOSBuild and its parent MachineOSConfig
// agree on the current build and image. Observing only the terminal build
// status is insufficient because the controller propagates the image and
// reference to the MachineOSConfig in a subsequent callback.
func assertMachineOSConfigReferencesMachineOSBuild(ctx context.Context, t *testing.T, mcfgclient mcfgclientset.Interface, mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild) {
	t.Helper()

	err := wait.PollImmediateInfiniteWithContext(ctx, time.Millisecond, func(ctx context.Context) (bool, error) {
		apiMosc, err := mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(ctx, mosc.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		apiMosb, err := mcfgclient.MachineconfigurationV1().MachineOSBuilds().Get(ctx, mosb.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		return apiMosc.Annotations[constants.CurrentMachineOSBuildAnnotationKey] == apiMosb.Name &&
			apiMosc.Status.MachineOSBuild != nil &&
			apiMosc.Status.MachineOSBuild.Name == apiMosb.Name &&
			apiMosb.Status.DigestedImagePushSpec != "" &&
			apiMosc.Status.CurrentImagePullSpec == apiMosb.Status.DigestedImagePushSpec, nil
	})

	require.NoError(t, err, "MachineOSConfig %s did not reference completed MachineOSBuild %s", mosc.Name, mosb.Name)
}

// Test that when the MCP’s rendered-MC name changes but the two MCs only differ
// by on-cluster layering, no Build Job is created.
func TestOSBuildControllerSkipsBuildForLayerOnlyChanges(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	poolName := "worker"

	_, mcfgclient, _, _, mosc, firstMosb, mcp, _, kubeassert, _ := setupOSBuildControllerForTestWithSuccessfulBuild(ctx, t, poolName)
	isMachineOSBuildReachedExpectedCount(ctx, t, mcfgclient, mosc, 1)

	assertMachineOSConfigGetsCurrentBuildAnnotation(ctx, t, mcfgclient, mosc, firstMosb)

	isMachineOSBuildReachedExpectedCount(ctx, t, mcfgclient, mosc, 1)

	insertNewRenderedMachineConfigWithoutImageChangeAndUpdatePool(ctx, t, mcfgclient, mcp.Name, "rendered-worker-layer-only")
	isMachineOSBuildReachedExpectedCount(ctx, t, mcfgclient, mosc, 2)

	mosbList, err := mcfgclient.MachineconfigurationV1().
		MachineOSBuilds().
		List(ctx, metav1.ListOptions{LabelSelector: utils.MachineOSBuildForPoolSelector(mosc).String()})
	require.NoError(t, err)
	require.Len(t, mosbList.Items, 2, "expected a new MOSB to be created for layering-only change")
	assert.Equal(t, firstMosb.Name, mosbList.Items[0].Name, "first MOSB should remain unchanged")

	layerOnlyMosb := mosbList.Items[1]

	jobName := utils.GetBuildJobName(&layerOnlyMosb)

	// Reaching the reused successful state proves reconciliation completed;
	// only then assert that no build Job was created.
	kubeassert.MachineOSBuildIsSuccessful(&layerOnlyMosb)
	assertMachineOSConfigReferencesMachineOSBuild(ctx, t, mcfgclient, mosc, &layerOnlyMosb)
	kubeassert.Now().JobDoesNotExist(jobName, "layering-only MOSB should not spawn a build Job")
}
