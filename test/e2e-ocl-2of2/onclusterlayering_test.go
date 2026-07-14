package e2e_ocl_2of2_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"

	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/imagebuilder"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/runtimeassets"
	ocltesthelper "github.com/openshift/machine-config-operator/test/e2e-ocl-shared"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// The MachineConfigPool to create for the tests.
	layeredMCPName string = "layered"

	// The MachineConfig names to create for the tests.
	mcNameUsbguard string = "inspect-usbguard"
)

var skipCleanupAlways bool
var skipCleanupOnlyAfterFailure bool

func init() {
	// Skips running the cleanup functions. Useful for debugging tests.
	flag.BoolVar(&skipCleanupAlways, "skip-cleanup", false, "Skips running cleanups regardless of outcome")
	// Skips running the cleanup function only when the test fails.
	flag.BoolVar(&skipCleanupOnlyAfterFailure, "skip-cleanup-on-failure", false, "Skips running cleanups only after failure")
}

// Holds elements common for each on-cluster build tests.
type onClusterLayeringTestOpts struct {
	// Which image builder type to use for the test.
	imageBuilderType mcfgv1.MachineOSImageBuilderType

	// The custom Dockerfiles to use for the test. This is a map of MachineConfigPool name to Dockerfile content.
	customDockerfiles map[string]string

	// What node should be targeted for the test.
	targetNode *corev1.Node

	// What MachineConfigPool name to use for the test.
	poolName string

	// Use RHEL entitlements
	entitlementRequired bool

	// Inject YUM repo information from a Centos 9 stream container
	useYumRepos bool

	// Apply the following MachineConfigs before beginning the build.
	machineConfigs []*mcfgv1.MachineConfig
}

func TestOnClusterLayeringOnOKD(t *testing.T) {
	ocltesthelper.SkipOnOCP(t)

	runOnClusterLayeringTest(t, onClusterLayeringTestOpts{
		poolName: layeredMCPName,
		customDockerfiles: map[string]string{
			layeredMCPName: ocltesthelper.OkdFcosDockerfile,
		},
	})
}

// This test starts a build that it then forces to fail by deleting the build
// pods until the job itself fails. After failure, it edits the
// MachineOSConfig with the expectation that the failed build and its  will be
// deleted and a new build will start in its place.
func TestGracefulBuildFailureRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cs := framework.NewClientSet("")

	mosc := prepareForOnClusterLayeringTest(t, cs, onClusterLayeringTestOpts{
		poolName: layeredMCPName,
		customDockerfiles: map[string]string{
			layeredMCPName: ocltesthelper.CowsayDockerfile,
		},
	})

	createMachineOSConfig(t, cs, mosc)

	// Wait for the build to start.
	firstMosb := waitForBuildToStartForPoolAndConfig(t, cs, layeredMCPName, mosc.Name)

	t.Logf("Waiting for MachineOSBuild %s to fail", firstMosb.Name)

	// Repeatedly delete the build pod until the job fails to cause a failure.
	// Otherwise, it takes a very long time for the job to actually fail.
	require.NoError(t, ocltesthelper.ForceMachineOSBuildToFail(ctx, t, cs, firstMosb))

	// Wait for the build to fail.
	kubeassert := helpers.AssertClientSet(t, cs).WithContext(ctx)
	kubeassert.Eventually().MachineOSBuildIsFailure(firstMosb)

	// Clear the overridden image pullspec.
	apiMosc, err := cs.MachineconfigurationV1Interface.MachineOSConfigs().Get(ctx, mosc.Name, metav1.GetOptions{})
	require.NoError(t, err)

	apiMosc.Spec.Containerfile = []mcfgv1.MachineOSContainerfile{}

	updatedMosc, err := cs.MachineconfigurationV1Interface.MachineOSConfigs().Update(ctx, apiMosc, metav1.UpdateOptions{})
	require.NoError(t, err)

	mcp, err := cs.MachineconfigurationV1Interface.MachineConfigPools().Get(ctx, layeredMCPName, metav1.GetOptions{})
	require.NoError(t, err)

	// Compute the new MachineOSBuild image name.
	moscChangeMosb := buildrequest.NewMachineOSBuildFromAPIOrDie(ctx, cs.GetKubeclient(), updatedMosc, mcp)

	// Wait for the second build to start.
	secondMosb := waitForBuildToStart(t, cs, moscChangeMosb)

	// Ensure that the first build is eventually cleaned up.
	kubeassert.Eventually().MachineOSBuildDoesNotExist(firstMosb)
	assertBuildObjectsAreDeleted(t, kubeassert.Eventually(), firstMosb)

	// Ensure that the second build is still running.
	kubeassert.MachineOSBuildExists(secondMosb)
	assertBuildObjectsAreCreated(t, kubeassert, secondMosb)
}

// This test validates that when a running builder is deleted, the
// MachineOSBuild associated with it goes into an interrupted status.
func TestDeletedBuilderInterruptsMachineOSBuild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cs := framework.NewClientSet("")

	poolName := layeredMCPName

	mosc := prepareForOnClusterLayeringTest(t, cs, onClusterLayeringTestOpts{
		poolName: poolName,
		customDockerfiles: map[string]string{
			layeredMCPName: ocltesthelper.CowsayDockerfile,
		},
	})

	// Create our MachineOSConfig and ensure that it is deleted after the test is
	// finished.
	createMachineOSConfig(t, cs, mosc)

	// Wait for the build to start
	startedBuild := waitForBuildToStartForPoolAndConfig(t, cs, poolName, mosc.Name)
	t.Logf("MachineOSBuild %q has started", startedBuild.Name)

	pod, err := ocltesthelper.GetPodFromJob(ctx, cs, utils.GetBuildJobName(startedBuild))
	require.NoError(t, err)

	// Delete the builder
	bgDeletion := metav1.DeletePropagationBackground
	err = cs.BatchV1Interface.Jobs(ctrlcommon.MCONamespace).Delete(ctx, utils.GetBuildJobName(startedBuild), metav1.DeleteOptions{PropagationPolicy: &bgDeletion})
	require.NoError(t, err)

	// Wait for the build to be interrupted.
	kubeassert := helpers.AssertClientSet(t, cs).WithContext(ctx).Eventually()
	waitForBuildToBeInterrupted(t, cs, startedBuild)
	// Ensure that the pod and job are deleted
	kubeassert.Eventually().JobDoesNotExist(utils.GetBuildJobName(startedBuild))
	kubeassert.Eventually().PodDoesNotExist(pod.Name)
}

// This test validates that when a running build pod is deleted, the
// Job associated with the MachineOSBuild creates a new pod and the
// MachineOSBuild still reports its state as building.
func TestDeletedPodDoesNotInterruptMachineOSBuild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cs := framework.NewClientSet("")

	poolName := layeredMCPName

	mosc := prepareForOnClusterLayeringTest(t, cs, onClusterLayeringTestOpts{
		poolName: poolName,
		customDockerfiles: map[string]string{
			layeredMCPName: ocltesthelper.CowsayDockerfile,
		},
	})

	// Create our MachineOSConfig and ensure that it is deleted after the test is
	// finished.
	createMachineOSConfig(t, cs, mosc)

	// Wait for the build to start
	startedBuild := waitForBuildToStartForPoolAndConfig(t, cs, poolName, mosc.Name)
	t.Logf("MachineOSBuild %q has started", startedBuild.Name)

	// Get the pod created by the build Job
	pod, err := ocltesthelper.GetPodFromJob(ctx, cs, utils.GetBuildJobName(startedBuild))
	require.NoError(t, err)

	// Delete the pod
	err = cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
	require.NoError(t, err)

	// Wait a few seconds to ensure that a new pod is created
	time.Sleep(time.Second * 5)

	// Ensure the build is still running
	kubeassert := helpers.AssertClientSet(t, cs).WithContext(ctx).Eventually()
	kubeassert.MachineOSBuildIsRunning(startedBuild)

	// Check that a new pod was created
	podNew, err := ocltesthelper.GetPodFromJob(ctx, cs, utils.GetBuildJobName(startedBuild))
	require.NoError(t, err)
	assert.NotEqual(t, podNew, pod)
}

func TestSSHKeyAndPasswordForOSBuilder(t *testing.T) {
	t.Skip()

	cs := framework.NewClientSet("")

	// label random node from pool, get the node
	unlabelFunc := helpers.LabelRandomNodeFromPool(t, cs, "worker", "node-role.kubernetes.io/layered")
	osNode := helpers.GetSingleNodeByRole(t, cs, layeredMCPName)

	// prepare for on cluster build test
	prepareForOnClusterLayeringTest(t, cs, onClusterLayeringTestOpts{
		poolName:          layeredMCPName,
		customDockerfiles: map[string]string{},
	})

	// Set up Ignition config with the desired SSH key and password
	testIgnConfig := ctrlcommon.NewIgnConfig()
	sshKeyContent := "testsshkey11"
	passwordHash := "testpassword11"

	// retreive initial etc/shadow contents
	initialEtcShadowContents := helpers.ExecCmdOnNode(t, cs, osNode, "grep", "^core:", "/rootfs/etc/shadow")

	testIgnConfig.Passwd.Users = []ign3types.PasswdUser{
		{
			Name:              "core",
			SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{ign3types.SSHAuthorizedKey(sshKeyContent)},
			PasswordHash:      &passwordHash,
		},
	}

	testConfig := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "99-test-ssh-and-password",
			Labels: helpers.MCLabelForRole(layeredMCPName),
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: helpers.MarshalOrDie(testIgnConfig),
			},
		},
	}

	helpers.SetMetadataOnObject(t, testConfig)

	// Create the MachineConfig and wait for the configuration to be applied
	mcCleanupFunc := applyMC(t, cs, testConfig)

	// wait for rendered config to finish creating
	renderedConfig, err := helpers.WaitForRenderedConfig(t, cs, layeredMCPName, testConfig.Name)
	require.Nil(t, err)
	t.Logf("Finished rendering config")

	// wait for mcp to complete updating
	err = helpers.WaitForPoolComplete(t, cs, layeredMCPName, renderedConfig)
	require.Nil(t, err)
	t.Logf("Pool completed updating")

	// Validate the SSH key and password
	osNode = helpers.GetSingleNodeByRole(t, cs, layeredMCPName) // Re-fetch node with updated configurations

	foundSSHKey := helpers.ExecCmdOnNode(t, cs, osNode, "cat", "/rootfs/home/core/.ssh/authorized_keys.d/ignition")
	if !strings.Contains(foundSSHKey, sshKeyContent) {
		t.Fatalf("updated ssh key not found, got %s", foundSSHKey)
	}
	t.Logf("updated ssh hash found, got %s", foundSSHKey)

	currentEtcShadowContents := helpers.ExecCmdOnNode(t, cs, osNode, "grep", "^core:", "/rootfs/etc/shadow")
	if currentEtcShadowContents == initialEtcShadowContents {
		t.Fatalf("updated password hash not found in /etc/shadow, got %s", currentEtcShadowContents)
	}
	t.Logf("updated password hash found in /etc/shadow, got %s", currentEtcShadowContents)

	t.Logf("Node %s has correct SSH Key and Password Hash", osNode.Name)

	// Clean-up: Delete the applied MachineConfig and ensure configurations are rolled back

	t.Cleanup(func() {
		unlabelFunc()
		mcCleanupFunc()
	})
}

// This test starts a build and then immediately scales down the
// machine-os-builder deployment until the underlying build job has completed.
// The rationale behind this test is so that if the machine-os-builder pod gets
// rescheduled onto a different node while a build is occurring that the
// MachineOSBuild object will eventually be reconciled, even if the build
// completed during the rescheduling operation.
func TestControllerEventuallyReconciles(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cs := framework.NewClientSet("")

	poolName := layeredMCPName

	mosc := prepareForOnClusterLayeringTest(t, cs, onClusterLayeringTestOpts{
		poolName: poolName,
		customDockerfiles: map[string]string{
			layeredMCPName: ocltesthelper.CowsayDockerfile,
		},
	})

	mcp, err := cs.MachineconfigurationV1Interface.MachineConfigPools().Get(ctx, poolName, metav1.GetOptions{})
	require.NoError(t, err)

	createMachineOSConfig(t, cs, mosc)

	mosb := buildrequest.NewMachineOSBuildFromAPIOrDie(ctx, cs.GetKubeclient(), mosc, mcp)

	// Wait for the MachineOSBuild to exist.
	kubeassert := helpers.AssertClientSet(t, cs).WithContext(ctx).Eventually()
	kubeassert.MachineOSBuildExists(mosb)
	jobName, err := ocltesthelper.GetJobForMOSB(ctx, cs, mosb)
	require.NoError(t, err)
	kubeassert.JobExists(jobName)
	assertBuildObjectsAreCreated(t, kubeassert, mosb)

	t.Logf("MachineOSBuild %q exists, stopping machine-os-builder", mosb.Name)

	// As soon as the MachineOSBuild exists, scale down the machine-os-builder
	// deployment and any other deployments which may inadvertantly cause its
	// replica count to increase. This is done to simulate the machine-os-builder
	// pod being scheduled onto a different node.
	restoreDeployments := ocltesthelper.ScaleDownDeployments(t, cs)

	// Wait for the job to start running.
	waitForJobToReachMOSBCondition(ctx, t, cs, jobName, mcfgv1.MachineOSBuilding)

	t.Logf("Job %s has started running, starting machine-os-builder", jobName)

	// Restore the deployments.
	restoreDeployments()

	// Ensure that the MachineOSBuild object eventually gets updated.
	kubeassert.MachineOSBuildIsRunning(mosb)

	t.Logf("MachineOSBuild %s is now running, stopping machine-os-builder", mosb.Name)

	// Stop the deployments again.
	restoreDeployments = ocltesthelper.ScaleDownDeployments(t, cs)

	// Wait for the job to complete.
	waitForJobToReachMOSBCondition(ctx, t, cs, jobName, mcfgv1.MachineOSBuildSucceeded)

	t.Logf("Job %q finished, starting machine-os-builder", jobName)

	// Restore the deployments again.
	restoreDeployments()

	// At this point, the machine-os-builder is running, so we wait for the build
	// itself to complete and be updated.
	mosb = waitForBuildToComplete(t, cs, mosb)

	// Wait until the MachineOSConfig gets the digested pullspec from the MachineOSBuild.
	require.NoError(t, wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		mosc, err := cs.MachineconfigurationV1Interface.MachineOSConfigs().Get(ctx, mosc.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		return mosc.Status.CurrentImagePullSpec != "" && mosc.Status.CurrentImagePullSpec == mosb.Status.DigestedImagePushSpec, nil
	}))
}

// TestImageBuildDegradedOnFailureAndClearedOnBuildStart tests that the
// ImageBuildDegraded condition is set to True when a MachineOSBuild fails, and
// is set to False when a MachineOSBuild is started after a previous failure.
// Previously, this test waited until the build was completed before verifying
// that the state was no longer degraded.
func TestImageBuildDegradedOnFailureAndClearedOnBuildStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cs := framework.NewClientSet("")

	mosc := prepareForOnClusterLayeringTest(t, cs, onClusterLayeringTestOpts{
		poolName: layeredMCPName,
		customDockerfiles: map[string]string{
			layeredMCPName: ocltesthelper.CowsayDockerfile,
		},
	})

	// First, add a bad containerfile to cause a build failure. However, we will
	// actually delete the build pod to force the failure to happen faster.
	t.Logf("Adding a bad containerfile for MachineOSConfig %s to cause a build failure", mosc.Name)
	mosc.Spec.Containerfile = ocltesthelper.GetBadContainerFileForFailureTest()

	createMachineOSConfig(t, cs, mosc)

	// Wait for the build to start and fail
	firstMosb := waitForBuildToStartForPoolAndConfig(t, cs, layeredMCPName, mosc.Name)
	t.Logf("Waiting for MachineOSBuild %s to fail", firstMosb.Name)

	// Force the build to fail faster by repeatedly deleting the build pods until
	// the job reflects a failure status.
	require.NoError(t, ocltesthelper.ForceMachineOSBuildToFail(ctx, t, cs, firstMosb))

	kubeassert := helpers.AssertClientSet(t, cs).WithContext(ctx)
	kubeassert.Eventually().MachineOSBuildIsFailure(firstMosb)

	// Wait for and verify ImageBuildDegraded condition is set to True
	degradedCondition := waitForImageBuildDegradedCondition(ctx, t, cs, layeredMCPName, corev1.ConditionTrue)
	require.NotNil(t, degradedCondition, "ImageBuildDegraded condition should be present")
	assert.Equal(t, string(mcfgv1.MachineConfigPoolBuildFailed), degradedCondition.Reason, "ImageBuildDegraded reason should be BuildFailed")
	assert.Contains(t, degradedCondition.Message, fmt.Sprintf("Failed to build OS image for pool %s", layeredMCPName), "ImageBuildDegraded message should contain pool name")
	assert.Contains(t, degradedCondition.Message, firstMosb.Name, "ImageBuildDegraded message should contain MachineOSBuild name")

	t.Logf("ImageBuildDegraded condition correctly set to True with message: %s", degradedCondition.Message)

	// Now fix the MachineOSConfig with a good containerfile
	apiMosc, err := cs.MachineconfigurationV1Interface.MachineOSConfigs().Get(ctx, mosc.Name, metav1.GetOptions{})
	require.NoError(t, err)

	apiMosc.Spec.Containerfile = []mcfgv1.MachineOSContainerfile{
		{
			ContainerfileArch: mcfgv1.NoArch,
			Content:           ocltesthelper.CowsayDockerfile,
		},
	}

	updatedMosc, err := cs.MachineconfigurationV1Interface.MachineOSConfigs().Update(ctx, apiMosc, metav1.UpdateOptions{})
	require.NoError(t, err)

	t.Logf("Fixed containerfile, waiting for new build to start")

	mcp, err := cs.MachineconfigurationV1Interface.MachineConfigPools().Get(ctx, layeredMCPName, metav1.GetOptions{})
	require.NoError(t, err)

	// Compute the new MachineOSBuild name
	moscChangeMosb := buildrequest.NewMachineOSBuildFromAPIOrDie(ctx, cs.GetKubeclient(), updatedMosc, mcp)

	// Wait for the second build to start
	secondMosb := waitForBuildToStart(t, cs, moscChangeMosb)
	t.Logf("Second build started successfully: %s", secondMosb.Name)

	// Wait for and verify ImageBuildDegraded condition is False after the new build starts.
	// The condition should be cleared when the build starts.
	degradedCondition = waitForImageBuildDegradedCondition(ctx, t, cs, layeredMCPName, corev1.ConditionFalse)
	require.NotNil(t, degradedCondition, "ImageBuildDegraded condition should still be present")
	assert.Equal(t, string(mcfgv1.MachineConfigPoolBuilding), degradedCondition.Reason, "ImageBuildDegraded reason should be Building")
	t.Logf("ImageBuildDegraded condition correctly cleared to False when build started with message: %s", degradedCondition.Message)

	// Wait for the second build to complete successfully
	finishedBuild := waitForBuildToComplete(t, cs, secondMosb)
	t.Logf("Second build completed successfully: %s", finishedBuild.Name)

	// Wait for the MachineOSConfig to get the new pullspec, which indicates full reconciliation
	waitForMOSCToGetNewPullspec(ctx, t, cs, mosc.Name, string(finishedBuild.Status.DigestedImagePushSpec))

	// Wait for and verify ImageBuildDegraded condition is False with reason BuildSucceeded
	degradedCondition = waitForImageBuildDegradedCondition(ctx, t, cs, layeredMCPName, corev1.ConditionFalse)
	require.NotNil(t, degradedCondition, "ImageBuildDegraded condition should still be present")
	assert.Equal(t, string(mcfgv1.MachineConfigPoolBuildSuccess), degradedCondition.Reason, "ImageBuildDegraded reason should be BuildSuccess")
	t.Logf("ImageBuildDegraded condition correctly set to False when build succeeded with message: %s", degradedCondition.Message)

}

func assertNodeRevertsToNonLayered(t *testing.T, cs *framework.ClientSet, node corev1.Node) {
	workerMCName := helpers.GetMcName(t, cs, "worker")
	workerMC, err := cs.MachineConfigs().Get(context.TODO(), workerMCName, metav1.GetOptions{})
	require.NoError(t, err)

	helpers.WaitForNodeConfigAndImageChange(t, cs, node, workerMCName, "")

	helpers.AssertNodeBootedIntoImage(t, cs, node, workerMC.Spec.OSImageURL)
	t.Logf("Node %s has reverted to OS image %q", node.Name, workerMC.Spec.OSImageURL)

	helpers.AssertFileNotOnNode(t, cs, node, filepath.Join("/etc/systemd/system", runtimeassets.RevertServiceName))
	helpers.AssertFileNotOnNode(t, cs, node, runtimeassets.RevertServiceMachineConfigFile)
}

func assertBuildObjectsAreCreated(t *testing.T, kubeassert *helpers.Assertions, mosb *mcfgv1.MachineOSBuild) {
	t.Helper()

	kubeassert.JobExists(utils.GetBuildJobName(mosb))
	kubeassert.ConfigMapExists(utils.GetContainerfileConfigMapName(mosb))
	kubeassert.ConfigMapExists(utils.GetMCConfigMapName(mosb))
	kubeassert.ConfigMapExists(utils.GetEtcPolicyConfigMapName(mosb))
	kubeassert.ConfigMapExists(utils.GetEtcRegistriesConfigMapName(mosb))
	kubeassert.SecretExists(utils.GetBasePullSecretName(mosb))
	kubeassert.SecretExists(utils.GetFinalPushSecretName(mosb))

	// Check that ownerReferences are set as well
	kubeassert.ConfigMapHasOwnerSet(utils.GetContainerfileConfigMapName(mosb))
	kubeassert.ConfigMapHasOwnerSet(utils.GetMCConfigMapName(mosb))
	kubeassert.ConfigMapHasOwnerSet(utils.GetEtcPolicyConfigMapName(mosb))
	kubeassert.ConfigMapHasOwnerSet(utils.GetEtcRegistriesConfigMapName(mosb))
	kubeassert.SecretHasOwnerSet(utils.GetBasePullSecretName(mosb))
	kubeassert.SecretHasOwnerSet(utils.GetFinalPushSecretName(mosb))
}

func assertBuildObjectsAreDeleted(t *testing.T, kubeassert *helpers.Assertions, mosb *mcfgv1.MachineOSBuild) {
	t.Helper()

	kubeassert.JobDoesNotExist(utils.GetBuildJobName(mosb))
	kubeassert.ConfigMapDoesNotExist(utils.GetContainerfileConfigMapName(mosb))
	kubeassert.ConfigMapDoesNotExist(utils.GetMCConfigMapName(mosb))
	kubeassert.ConfigMapDoesNotExist(utils.GetEtcPolicyConfigMapName(mosb))
	kubeassert.ConfigMapDoesNotExist(utils.GetEtcRegistriesConfigMapName(mosb))
	kubeassert.SecretDoesNotExist(utils.GetBasePullSecretName(mosb))
	kubeassert.SecretDoesNotExist(utils.GetFinalPushSecretName(mosb))
}

// Sets up and performs an on-cluster build for a given set of parameters.
// Returns the built image pullspec for later consumption.
func runOnClusterLayeringTest(t *testing.T, testOpts onClusterLayeringTestOpts) (string, *mcfgv1.MachineOSBuild) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cs := framework.NewClientSet("")

	imageBuilder := testOpts.imageBuilderType
	if testOpts.imageBuilderType == "" {
		imageBuilder = mcfgv1.JobBuilder
	}

	t.Logf("Running with ImageBuilder type: %s", imageBuilder)

	mosc := prepareForOnClusterLayeringTest(t, cs, testOpts)

	// Create our MachineOSConfig.
	createMachineOSConfig(t, cs, mosc)

	// Create a child context for the machine-os-builder pod log streamer. We
	// create it here because we want the cancellation to run before the
	// MachineOSConfig object is removed.
	mobPodStreamerCtx, mobPodStreamerCancel := context.WithCancel(ctx)
	t.Cleanup(mobPodStreamerCancel)

	// Wait for the build to start
	startedBuild := waitForBuildToStartForPoolAndConfig(t, cs, testOpts.poolName, mosc.Name)
	t.Logf("MachineOSBuild %q has started", startedBuild.Name)

	assertBuildJobIsAsExpected(t, cs, startedBuild)

	t.Logf("Waiting for build completion...")

	// Create a child context for the build pod log streamer. This is so we can
	// cancel it independently of the parent context or the context for the
	// machine-os-build pod watcher (which has its own separate context).
	buildPodStreamerCtx, buildPodStreamerCancel := context.WithCancel(ctx)

	// We wire this to both t.Cleanup() as well as defer because we want to
	// cancel this context either at the end of this function or when the test
	// fails, whichever comes first.
	buildPodWatcherShutdown := ocltesthelper.MakeIdempotentAndRegisterAlwaysRun(t, buildPodStreamerCancel)
	defer buildPodWatcherShutdown()

	dirPath := ocltesthelper.GetBuildArtifactDir(t)

	podLogsDirPath := filepath.Join(dirPath, "pod-logs")
	require.NoError(t, os.MkdirAll(podLogsDirPath, 0o755))

	// In the event of a test failure, we want to dump all of the build artifacts
	// to files for easy reference later.
	t.Cleanup(func() {
		if t.Failed() {
			ocltesthelper.WriteBuildArtifactsToFiles(t, cs)
		}
	})

	// The pod log collection blocks the main Goroutine since we follow the logs
	// for each container in the build pod. So they must run in a separate
	// Goroutine so that the rest of the test can continue.
	go func() {
		err := ocltesthelper.StreamBuildPodLogsToFile(buildPodStreamerCtx, t, cs, startedBuild, podLogsDirPath)
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Logf("Warning: failed to stream build pod logs: %v", err)
		}
	}()

	// We also want to collect logs from the machine-os-builder pod since they
	// can provide a valuable window in how / why a test failed. As mentioned
	// above, we need to run this in a separate Goroutine so that the test is not
	// blocked.
	go func() {
		err := ocltesthelper.StreamMachineOSBuilderPodLogsToFile(mobPodStreamerCtx, t, cs, podLogsDirPath)
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Logf("Warning: failed to stream machine-os-builder pod logs: %v", err)
		}
	}()

	// Wait for the build to complete.
	finishedBuild := waitForBuildToComplete(t, cs, startedBuild)

	t.Logf("MachineOSBuild %q has completed and produced image: %s", finishedBuild.Name, finishedBuild.Status.DigestedImagePushSpec)

	require.NoError(t, archiveBuildPodLogs(t, podLogsDirPath))

	return string(finishedBuild.Status.DigestedImagePushSpec), startedBuild
}

// Waits for the build to start and returns the started MachineOSBuild object.
func waitForBuildToStartForPoolAndConfig(t *testing.T, cs *framework.ClientSet, poolName, moscName string) *mcfgv1.MachineOSBuild {
	t.Helper()

	var mosbName string

	require.NoError(t, wait.PollUntilContextTimeout(context.Background(), 2*time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		// Get the name for the MachineOSBuild based upon the MachineConfigPool and MachineOSConfig state.
		name, err := ocltesthelper.GetMachineOSBuildNameForPool(cs, poolName, moscName)
		if err != nil {
			return false, nil
		}

		mosbName = name
		return true, nil
	}))

	// Create a "dummy" MachineOSBuild object with just the name field set so
	// that waitForMachineOSBuildToReachState() can use it.
	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: mosbName,
		},
	}

	return waitForBuildToStart(t, cs, mosb)
}

func assertBuildJobIsAsExpected(t *testing.T, cs *framework.ClientSet, mosb *mcfgv1.MachineOSBuild) {
	t.Helper()

	osImageURLConfig, err := ctrlcommon.GetOSImageURLConfig(context.TODO(), cs.GetKubeclient())
	require.NoError(t, err)

	mcoImages, err := ctrlcommon.GetImagesConfig(context.TODO(), cs.GetKubeclient())
	require.NoError(t, err)

	buildPod, err := ocltesthelper.GetPodFromJob(context.TODO(), cs, mosb.Status.Builder.Job.Name)
	require.NoError(t, err)

	assertContainerIsUsingExpectedImage := func(c corev1.Container, containerName, expectedImage string) {
		if c.Name == containerName {
			assert.Equal(t, c.Image, expectedImage)
		}
	}

	for _, container := range buildPod.Spec.Containers {
		assertContainerIsUsingExpectedImage(container, "image-build", mcoImages.MachineConfigOperator)
		assertContainerIsUsingExpectedImage(container, "wait-for-done", osImageURLConfig.BaseOSContainerImage)
	}
}

// Waits for a MachineOSBuild to start building.
func waitForBuildToStart(t *testing.T, cs *framework.ClientSet, build *mcfgv1.MachineOSBuild) *mcfgv1.MachineOSBuild {
	t.Helper()

	t.Logf("Waiting for MachineOSBuild %s to start", build.Name)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute*5)
	defer cancel()

	start := time.Now()

	kubeassert := helpers.AssertClientSet(t, cs).WithContext(ctx)
	kubeassert.Eventually().MachineOSBuildExists(build)
	t.Logf("MachineOSBuild %s created after %s", build.Name, time.Since(start))
	kubeassert.Eventually().MachineOSBuildIsRunning(build)
	t.Logf("MachineOSBuild %s running after %s", build.Name, time.Since(start))

	// Get the job for the MOSB created by comparing the job UID with the MOSB annotation
	buildJobName, err := ocltesthelper.GetJobForMOSB(ctx, cs, build)
	require.NoError(t, err)
	kubeassert.Eventually().JobExists(buildJobName)
	t.Logf("Build job %s created after %s", buildJobName, time.Since(start))
	// Get the pod created by the job
	buildPod, err := ocltesthelper.GetPodFromJob(ctx, cs, buildJobName)
	require.NoError(t, err)
	kubeassert.Eventually().PodIsRunning(buildPod.Name)
	t.Logf("Build pod %s running after %s", buildPod.Name, time.Since(start))
	kubeassert.Eventually().PodHasOwnerSet(buildPod.Name)
	t.Logf("Build pod %s has owner set after %s", buildPod.Name, time.Since(start))

	mosb, err := cs.MachineconfigurationV1Interface.MachineOSBuilds().Get(ctx, build.Name, metav1.GetOptions{})
	require.NoError(t, err)

	assertBuildObjectsAreCreated(t, kubeassert.Eventually(), mosb)
	t.Logf("Build objects created after %s", time.Since(start))

	return mosb
}

// Waits for a MachineOSBuild with a specific UID to be deleted.
func waitForMOSBToBeDeleted(t *testing.T, cs *framework.ClientSet, mosb *mcfgv1.MachineOSBuild) {
	t.Helper()

	start := time.Now()

	// If the given MachineOSBuild does not have a UID, e.g., from the
	// NewMachineOSBuildFromAPIOrDie() helper, then we query the API server to
	// find it.
	if mosb.UID == "" {
		t.Logf("No UID provided for MachineOSBuild %s, querying API for UID", mosb.Name)
		// Get the MOSB from the API to get the UID
		apiMosb, err := cs.MachineconfigurationV1Interface.MachineOSBuilds().Get(context.Background(), mosb.Name, metav1.GetOptions{})
		require.NoError(t, err)

		if k8serrors.IsNotFound(err) {
			t.Logf("MachineOSBuild %s is not found, must have already been deleted", mosb.Name)
			return
		}

		require.NoError(t, err)

		mosb = apiMosb
	}

	mosbName := mosb.Name
	mosbUID := mosb.UID

	t.Logf("Waiting for MachineOSBuild %s with UID %s to be deleted", mosbName, mosbUID)

	// Assert does not adequately handle the case where the object is deleted.
	// See https://issues.redhat.com/browse/OCPBUGS-63048 for details.
	err := wait.PollUntilContextTimeout(context.Background(), time.Second, time.Minute*5, true, func(ctx context.Context) (bool, error) {
		mosbs, err := cs.MachineconfigurationV1Interface.MachineOSBuilds().List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}

		for _, mosb := range mosbs.Items {
			// If we find a MachineOSBuild with the same name and UID, then we know
			// it has not been deleted yet.
			if mosb.Name == mosbName && mosb.UID == mosbUID {
				return false, nil
			}
		}

		return true, nil
	})

	t.Logf("MachineOSBuild %s with UID %s deleted after %s", mosb.Name, mosb.UID, time.Since(start))

	require.NoError(t, err, "MachineOSBuild %s with UID %s not deleted after %s", mosb.Name, mosb.UID, time.Since(start))
}

// Waits for a MachineOSBuild to be deleted. This is different than
// waitForMOSBToBeDeleted since it then asserts that all of the objects
// associated with the MOSB are deleted.
func waitForBuildToBeDeleted(t *testing.T, cs *framework.ClientSet, build *mcfgv1.MachineOSBuild) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute*5)
	defer cancel()

	kubeassert := helpers.AssertClientSet(t, cs).WithContext(ctx)

	t.Logf("Waiting for MachineOSBuild %s to be deleted", build.Name)

	start := time.Now()

	waitForMOSBToBeDeleted(t, cs, build)

	assertBuildObjectsAreDeleted(t, kubeassert.Eventually(), build)
	t.Logf("Build objects deleted after %s", time.Since(start))
}

// Waits for the given MachineOSBuild to complete and returns the completed
// MachineOSBuild object.
func waitForBuildToComplete(t *testing.T, cs *framework.ClientSet, startedBuild *mcfgv1.MachineOSBuild) *mcfgv1.MachineOSBuild {
	t.Helper()

	t.Logf("Waiting for MachineOSBuild %s to complete", startedBuild.Name)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute*20)
	defer cancel()

	start := time.Now()

	kubeassert := helpers.AssertClientSet(t, cs).WithContext(ctx)
	kubeassert.Eventually().MachineOSBuildIsSuccessful(startedBuild)
	t.Logf("MachineOSBuild %s successful after %s", startedBuild.Name, time.Since(start))
	assertBuildObjectsAreDeleted(t, kubeassert.Eventually(), startedBuild)
	t.Logf("Build objects deleted after %s", time.Since(start))

	mosb, err := cs.MachineconfigurationV1Interface.MachineOSBuilds().Get(ctx, startedBuild.Name, metav1.GetOptions{})
	require.NoError(t, err)

	return mosb
}

func waitForBuildToBeInterrupted(t *testing.T, cs *framework.ClientSet, startedBuild *mcfgv1.MachineOSBuild) *mcfgv1.MachineOSBuild {
	t.Helper()

	t.Logf("Waiting for MachineOSBuild %s to be interrupted", startedBuild.Name)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute*5)
	defer cancel()

	start := time.Now()

	kubeassert := helpers.AssertClientSet(t, cs).WithContext(ctx)
	kubeassert.Eventually().MachineOSBuildIsInterrupted(startedBuild)
	t.Logf("MachineOSBuild %s interrupted after %s", startedBuild.Name, time.Since(start))

	mosb, err := cs.MachineconfigurationV1Interface.MachineOSBuilds().Get(ctx, startedBuild.Name, metav1.GetOptions{})
	require.NoError(t, err)

	return mosb
}

// Prepares for an on-cluster build test by performing the following:
// - Gets the Docker Builder secret name from the MCO namespace.
// - Creates the imagestream to use for the test.
// - Clones the global pull secret into the MCO namespace.
// - If requested, clones the RHEL entitlement secret into the MCO namespace.
// - Creates the on-cluster-build-config ConfigMap.
// - Creates the target MachineConfigPool and waits for it to get a rendered config.
// - Creates the on-cluster-build-custom-dockerfile ConfigMap.
//
// Each of the object creation steps registers an idempotent cleanup function
// that will delete the object at the end of the test.
//
// Returns a MachineOSConfig object for the caller to create to begin the build
// process.
func prepareForOnClusterLayeringTest(t *testing.T, cs *framework.ClientSet, testOpts onClusterLayeringTestOpts) *mcfgv1.MachineOSConfig {
	// If the test requires RHEL entitlements, ensure they are present
	// in the test cluster. If not found, the test is skipped.
	if testOpts.entitlementRequired {
		ocltesthelper.SkipIfEntitlementNotPresent(t, cs)
	}

	// If the test requires /etc/yum.repos.d and /etc/pki/rpm-gpg, pull a Centos
	// Stream 9 container image and populate them from there. This is intended to
	// emulate the Red Hat Satellite enablement process, but does not actually
	// require any Red Hat Satellite creds to work.
	if testOpts.useYumRepos {
		ocltesthelper.InjectYumRepos(t, cs, skipCleanupAlways, skipCleanupOnlyAfterFailure)
	}

	// Register ephemeral object cleanup function.
	ocltesthelper.MakeIdempotentAndRegister(t, skipCleanupAlways, skipCleanupOnlyAfterFailure, func() {
		ocltesthelper.CleanupEphemeralBuildObjects(t, cs)
	})

	imagestreamObjMeta := metav1.ObjectMeta{
		Name: "os-image",
	}

	pushSecretName, finalPullspec, _ := ocltesthelper.SetupImageStream(t, cs, imagestreamObjMeta, skipCleanupAlways, skipCleanupOnlyAfterFailure)

	if testOpts.targetNode != nil {
		ocltesthelper.MakeIdempotentAndRegister(t, skipCleanupAlways, skipCleanupOnlyAfterFailure, helpers.CreatePoolWithNode(t, cs, testOpts.poolName, *testOpts.targetNode))
	} else {
		ocltesthelper.MakeIdempotentAndRegister(t, skipCleanupAlways, skipCleanupOnlyAfterFailure, helpers.CreateMCP(t, cs, testOpts.poolName))
	}

	mcNames := []string{"00-worker"}
	if len(testOpts.machineConfigs) > 0 {
		for _, mc := range testOpts.machineConfigs {
			ocltesthelper.MakeIdempotentAndRegister(t, skipCleanupAlways, skipCleanupOnlyAfterFailure, helpers.ApplyMC(t, cs, mc))
			mcNames = append(mcNames, mc.Name)
		}
	}

	_, err := helpers.WaitForRenderedConfigs(t, cs, testOpts.poolName, mcNames...)
	require.NoError(t, err)

	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: testOpts.poolName,
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{
				Name: testOpts.poolName,
			},
			RenderedImagePushSecret: mcfgv1.ImageSecretObjectReference{
				Name: pushSecretName,
			},
			RenderedImagePushSpec: mcfgv1.ImageTagFormat(finalPullspec),
			ImageBuilder: mcfgv1.MachineOSImageBuilder{
				ImageBuilderType: mcfgv1.JobBuilder,
			},
			Containerfile: []mcfgv1.MachineOSContainerfile{
				{
					ContainerfileArch: mcfgv1.NoArch,
					Content:           testOpts.customDockerfiles[testOpts.poolName],
				},
			},
		},
	}

	helpers.SetMetadataOnObject(t, mosc)

	return mosc
}

func createMachineOSConfig(t *testing.T, cs *framework.ClientSet, mosc *mcfgv1.MachineOSConfig) {
	ocltesthelper.CreateMachineOSConfig(t, cs, mosc, skipCleanupAlways, skipCleanupOnlyAfterFailure)
}

func applyMC(t *testing.T, cs *framework.ClientSet, mc *mcfgv1.MachineConfig) func() {
	return ocltesthelper.ApplyMC(t, cs, mc, skipCleanupAlways, skipCleanupOnlyAfterFailure)
}

func waitForMOSCToGetNewPullspec(ctx context.Context, t *testing.T, cs *framework.ClientSet, moscName, pullspec string) {
	require.NoError(t, wait.PollUntilContextTimeout(ctx, 1*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		mosc, err := cs.MachineconfigurationV1Interface.MachineOSConfigs().Get(ctx, moscName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		return mosc.Status.CurrentImagePullSpec != "" && string(mosc.Status.CurrentImagePullSpec) == pullspec, nil
	}))
}

func waitForMOSCToUpdateCurrentMOSB(ctx context.Context, t *testing.T, cs *framework.ClientSet, moscName, mosbName string) string {
	var currentMOSB string
	require.NoError(t, wait.PollUntilContextTimeout(ctx, 1*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		mosc, err := cs.MachineconfigurationV1Interface.MachineOSConfigs().Get(ctx, moscName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		currentMOSB = mosc.GetAnnotations()[constants.CurrentMachineOSBuildAnnotationKey]
		return currentMOSB != "" && currentMOSB != mosbName, nil

	}))
	return currentMOSB
}

// Waits for a job object to be mapped to a given MachineOSBuild state. Will always fail the test if the job reaches a failed state unexpectedly.
func waitForJobToReachMOSBCondition(ctx context.Context, t *testing.T, cs *framework.ClientSet, jobName string, expectedCondition mcfgv1.BuildProgress) {
	waitForJobToReachCondition(ctx, t, cs, jobName, func(job *batchv1.Job) (bool, error) {
		buildprogress, _ := imagebuilder.MapJobStatusToBuildStatus(job)
		if buildprogress == mcfgv1.MachineOSBuildFailed && expectedCondition != mcfgv1.MachineOSBuildFailed {
			return false, fmt.Errorf("job %q failed unexpectedly", jobName)
		}

		return expectedCondition == buildprogress, nil
	})
}

// Waits for a job object to reach a given state.
func waitForJobToReachCondition(ctx context.Context, t *testing.T, cs *framework.ClientSet, jobName string, condFunc func(*batchv1.Job) (bool, error)) {
	require.NoError(t, wait.PollUntilContextTimeout(ctx, 1*time.Second, 20*time.Minute, true, func(ctx context.Context) (bool, error) {
		job, err := cs.BatchV1Interface.Jobs(ctrlcommon.MCONamespace).Get(ctx, jobName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		return condFunc(job)
	}))
}

// waitForImageBuildDegradedCondition waits for the ImageBuildDegraded condition to reach the expected state
func waitForImageBuildDegradedCondition(ctx context.Context, t *testing.T, cs *framework.ClientSet, poolName string, expectedStatus corev1.ConditionStatus) *mcfgv1.MachineConfigPoolCondition {
	t.Helper()

	var condition *mcfgv1.MachineConfigPoolCondition
	require.NoError(t, wait.PollUntilContextTimeout(ctx, 1*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		mcp, err := cs.MachineconfigurationV1Interface.MachineConfigPools().Get(ctx, poolName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		condition = apihelpers.GetMachineConfigPoolCondition(mcp.Status, mcfgv1.MachineConfigPoolImageBuildDegraded)
		if condition == nil {
			return false, nil
		}

		return condition.Status == expectedStatus, nil
	}))

	return condition
}

func archiveBuildPodLogs(t *testing.T, podLogsDirPath string) error {
	archiveName := fmt.Sprintf("%s-pod-logs.tar.gz", helpers.SanitizeTestName(t))

	archive, err := helpers.NewArtifactArchive(t, archiveName)
	if err != nil {
		return err
	}

	cmd := exec.Command("mv", podLogsDirPath, archive.StagingDir())
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Log(string(output))
		return err
	}

	return archive.WriteArchive()
}

