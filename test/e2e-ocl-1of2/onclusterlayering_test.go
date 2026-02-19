package e2e_ocl_1of2_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"

	"github.com/openshift/machine-config-operator/pkg/daemon/runtimeassets"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	ocltesthelper "github.com/openshift/machine-config-operator/test/e2e-ocl-shared"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
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

// Tests that an on-cluster build can be performed with the Custom Pod Builder.
func TestOnClusterLayering(t *testing.T) {
	_, firstMosb := runOnClusterLayeringTest(t, onClusterLayeringTestOpts{
		poolName: layeredMCPName,
		customDockerfiles: map[string]string{
			layeredMCPName: ocltesthelper.CowsayDockerfile,
		},
	})

	assert.NotEqual(t, string(firstMosb.UID), "")

	// Test rebuild annotation works
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	t.Logf("Applying rebuild annotation (%q) to MachineOSConfig (%q) to cause a rebuild", constants.RebuildMachineOSConfigAnnotationKey, layeredMCPName)

	cs := framework.NewClientSet("")

	mosc, err := cs.MachineconfigurationV1Interface.MachineOSConfigs().Get(ctx, layeredMCPName, metav1.GetOptions{})
	require.NoError(t, err)

	helpers.SetRebuildAnnotationOnMachineOSConfig(ctx, t, cs.GetMcfgclient(), mosc)

	// Use the UID of the previous MOSB to ensure it is deleted as the rebuild will trigger a MOSB with the same name
	t.Logf("Waiting for the previous MachineOSBuild with UID %q to be deleted", firstMosb.UID)
	waitForMOSBToBeDeleted(t, cs, firstMosb)

	// Wait for the build to start
	secondMosb := waitForBuildToStartForPoolAndConfig(t, cs, layeredMCPName, mosc.Name)
	assert.NotEqual(t, firstMosb.UID, secondMosb.UID)
}

// Tests that an on-cluster build can be performed and that the resulting image
// is rolled out to an opted-in node.
func TestOnClusterBuildRollsOutImage(t *testing.T) {
	requiredKernelType := ctrlcommon.KernelTypeRealtime
	if goruntime.GOARCH == "arm64" {
		requiredKernelType = ctrlcommon.KernelType64kPages
	}

	imagePullspec, _ := runOnClusterLayeringTest(t, onClusterLayeringTestOpts{
		poolName: layeredMCPName,
		customDockerfiles: map[string]string{
			layeredMCPName: ocltesthelper.CowsayDockerfile,
		},
		machineConfigs: []*mcfgv1.MachineConfig{
			ocltesthelper.NewMachineConfigWithKernelType(fmt.Sprintf("%s-kernel-machineconfig", requiredKernelType), layeredMCPName, requiredKernelType),
		},
	})

	cs := framework.NewClientSet("")
	node := helpers.GetRandomNode(t, cs, "worker")

	unlabelFunc := ocltesthelper.MakeIdempotentAndRegisterAlwaysRun(t, helpers.LabelNode(t, cs, node, helpers.MCPNameToRole(layeredMCPName)))
	helpers.WaitForNodeImageChange(t, cs, node, imagePullspec)

	helpers.AssertNodeBootedIntoImage(t, cs, node, imagePullspec)
	t.Logf("Node %s is booted into image %q", node.Name, imagePullspec)
	t.Log(helpers.ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "cowsay", "Moo!"))

	// Check that the booted image has the requested kernel
	foundKernel := helpers.ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "uname", "-r")
	t.Logf("Node %s running kernel: %s", node.Name, foundKernel)
	if !ocltesthelper.CompareKernelType(t, foundKernel, requiredKernelType) {
		t.Fatalf("Kernel type requested %s, got %s", requiredKernelType, foundKernel)
	}

	unlabelFunc()

	assertNodeRevertsToNonLayered(t, cs, node)

	// Check that the reverted image has the default kernel.
	requiredKernelType = ctrlcommon.KernelTypeDefault
	foundKernel = helpers.ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "uname", "-r")
	t.Logf("Node %s running kernel: %s", node.Name, foundKernel)
	if !ocltesthelper.CompareKernelType(t, foundKernel, requiredKernelType) {
		t.Fatalf("Kernel type requested %s, got %s", requiredKernelType, foundKernel)
	}
}

func TestMissingImageIsRebuilt(t *testing.T) {
	cs := framework.NewClientSet("")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kubeassert := helpers.AssertClientSet(t, cs).WithContext(ctx)

	firstImagePullspec, firstMOSB := runOnClusterLayeringTest(t, onClusterLayeringTestOpts{
		poolName: layeredMCPName,
		customDockerfiles: map[string]string{
			layeredMCPName: ocltesthelper.CowsayDockerfile,
		},
	})

	moscName := layeredMCPName
	t.Logf("Waiting for MachineOSConfig %q to have a new pullspec", moscName)
	waitForMOSCToGetNewPullspec(ctx, t, cs, moscName, firstImagePullspec)

	// Create a MC to create another MOSB
	testMC := ocltesthelper.NewMachineConfigTriggersImageRebuild(mcNameUsbguard, layeredMCPName, []string{"usbguard"})
	t.Logf("Creating MachineConfig %q", testMC.Name)
	firstMC, err := cs.MachineConfigs().Create(ctx, testMC, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Logf("Created MachineConfig %q", firstMC.Name)
	kubeassert.MachineConfigExists(firstMC)

	// Wait for the build to start
	t.Logf("Waiting for 2nd build to start...")
	secondMOSBName := waitForMOSCToUpdateCurrentMOSB(ctx, t, cs, moscName, firstMOSB.Name)
	secondMOSB, err := cs.GetMcfgclient().MachineconfigurationV1().MachineOSBuilds().Get(ctx, secondMOSBName, metav1.GetOptions{})
	require.NoError(t, err)
	secondMOSB = waitForBuildToStart(t, cs, secondMOSB)
	t.Logf("MachineOSBuild %q has started", secondMOSB.Name)
	assertBuildJobIsAsExpected(t, cs, secondMOSB)

	// Wait for the build to finish
	t.Logf("Waiting for 2nd build completion...")
	secondFinishedBuild := waitForBuildToComplete(t, cs, secondMOSB)
	secondImagePullspec := string(secondFinishedBuild.Status.DigestedImagePushSpec)
	t.Logf("MachineOSBuild %q has completed and produced image: %s", secondFinishedBuild.Name, secondImagePullspec)
	waitForMOSCToGetNewPullspec(ctx, t, cs, moscName, secondImagePullspec)

	// Delete the first image- simulating image deletion
	t.Logf("Deleting image %q", firstImagePullspec)
	istName := fmt.Sprintf("os-image:%s", firstMOSB.Name)
	err = cs.ImageStreamTags(ctrlcommon.MCONamespace).Delete(ctx, istName, metav1.DeleteOptions{})
	require.NoError(t, err)
	kubeassert.ImageDoesNotExist(istName)
	t.Logf("Deleted image %q", firstImagePullspec)

	// Delete the first MC
	t.Logf("Deleting MachineConfig %q to retrigger build", firstMC.Name)
	err = cs.MachineConfigs().Delete(ctx, firstMC.Name, metav1.DeleteOptions{})
	require.NoError(t, err)
	kubeassert.MachineConfigDoesNotExist(firstMC)
	t.Logf("Deleted MachineConfig %q", firstMC.Name)

	// Wait for the build to start
	t.Logf("Waiting for 3rd build (rebuild of image1) to start...")
	thirdMOSBName := waitForMOSCToUpdateCurrentMOSB(ctx, t, cs, moscName, secondMOSB.Name)
	thirdMOSB, err := cs.GetMcfgclient().MachineconfigurationV1().MachineOSBuilds().Get(ctx, thirdMOSBName, metav1.GetOptions{})
	require.NoError(t, err)
	thirdMOSB = waitForBuildToStart(t, cs, thirdMOSB)
	t.Logf("MachineOSBuild %q has started (rebuild of image1)", thirdMOSB.Name)
	assertBuildJobIsAsExpected(t, cs, thirdMOSB)

	// Wait for the build to finish
	t.Logf("Waiting for 3rd build completion...")
	thirdFinishedBuild := waitForBuildToComplete(t, cs, thirdMOSB)
	thirdImagePullspec := string(thirdFinishedBuild.Status.DigestedImagePushSpec)
	t.Logf("MachineOSBuild %q has completed and produced image: %s", thirdFinishedBuild.Name, thirdImagePullspec)
	waitForMOSCToGetNewPullspec(ctx, t, cs, moscName, thirdImagePullspec)

	// Apply the MC again
	t.Logf("Re‐applying the same MachineConfig %q to confirm no new build for image2", testMC.Name)
	secondMC, err := cs.MachineConfigs().Create(ctx, testMC, metav1.CreateOptions{})
	require.NoError(t, err)
	kubeassert.MachineConfigExists(secondMC)
	t.Logf("Created MachineConfig %q", secondMC.Name)

	// waitForMOSCToGetNewPullspec(ctx, t, cs, moscName, secondImagePullspec)

	t.Logf("Waiting for recycled USBGuard MOSB %q to finish (or to prove there is none)", secondMOSB.Name)
	secondMOSB, err = cs.GetMcfgclient().MachineconfigurationV1().MachineOSBuilds().Get(ctx, secondMOSB.Name, metav1.GetOptions{})
	require.NoError(t, err)
	secondMOSB = waitForBuildToComplete(t, cs, secondMOSB)
	t.Logf("MOSB %q is now complete (reused image)", secondMOSB.Name)

	t.Logf("Deleting MachineOSBuild %q (MOSB3) to test pruning of image1", thirdMOSB.Name)
	err = cs.MachineconfigurationV1Interface.MachineOSBuilds().Delete(ctx, thirdMOSB.Name, metav1.DeleteOptions{})
	require.NoError(t, err)
	kubeassert.MachineOSBuildDoesNotExist(thirdMOSB)
	t.Logf("Deleted MachineOSBuild %q", thirdMOSB.Name)

	deletedIst := fmt.Sprintf("os-image:%s", thirdMOSBName)
	kubeassert.ImageDoesNotExist(deletedIst)
	t.Logf("ImageStreamTag %q has been pruned", deletedIst)

	t.Logf("Deleting MachineConfig %q for cleanup", secondMC.Name)
	err = cs.MachineConfigs().Delete(ctx, secondMC.Name, metav1.DeleteOptions{})
	require.NoError(t, err)
	kubeassert.MachineConfigDoesNotExist(secondMC)
	t.Logf("Deleted MachineConfig %q", secondMC.Name)
}

// This test extracts the /etc/yum.repos.d and /etc/pki/rpm-gpg content from a
// Centos Stream 9 image and injects them into the MCO namespace. It then
// performs a build with the expectation that these artifacts will be used,
// simulating a build where someone has added this content; usually a Red Hat
// Satellite user.
func TestYumReposBuilds(t *testing.T) {
	// Skipping this test as it is having a package conflict issue unrelated to MCO
	t.Skip()
	runOnClusterLayeringTest(t, onClusterLayeringTestOpts{
		poolName: layeredMCPName,
		customDockerfiles: map[string]string{
			layeredMCPName: ocltesthelper.YumReposDockerfile,
		},
		useYumRepos: true,
	})
}

// Then performs an on-cluster layering build which should consume the
// etc-pki-entitlement certificates.
func TestEntitledBuilds(t *testing.T) {
	ocltesthelper.SkipOnOKD(t)

	runOnClusterLayeringTest(t, onClusterLayeringTestOpts{
		poolName: layeredMCPName,
		customDockerfiles: map[string]string{
			layeredMCPName: ocltesthelper.EntitledDockerfile,
		},
		entitlementRequired: true,
	})
}

// This test verifies that if a change is made to a given MachineOSConfig, that
// any in-progress builds are terminated and that only the latest change is
// being built.
func TestMachineOSConfigChangeRestartsBuild(t *testing.T) {
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

	mcp, err := cs.MachineconfigurationV1Interface.MachineConfigPools().Get(ctx, layeredMCPName, metav1.GetOptions{})
	require.NoError(t, err)

	firstMosb := buildrequest.NewMachineOSBuildFromAPIOrDie(ctx, cs.GetKubeclient(), mosc, mcp)

	// First, we get a MachineOSBuild started as usual.
	waitForBuildToStart(t, cs, firstMosb)

	// Next, we update the Containerfile.
	t.Logf("Initial build has started, updating Containerfile...")

	apiMosc := helpers.SetContainerfileContentsOnMachineOSConfig(ctx, t, cs.GetMcfgclient(), mosc, "FROM configs AS final\nRUN echo 'hello' > /etc/hello")

	moscChangeMosb := buildrequest.NewMachineOSBuildFromAPIOrDie(ctx, cs.GetKubeclient(), apiMosc, mcp)

	kubeassert := helpers.AssertClientSet(t, cs).WithContext(ctx)

	assertBuildObjectsAreCreated(t, kubeassert, firstMosb)

	t.Logf("Containerfile is updated, waiting for new build %s to start", moscChangeMosb.Name)

	// Wait for the second build to start.
	waitForBuildToStart(t, cs, moscChangeMosb)

	t.Logf("Waiting for initial MachineOSBuild %s to be deleted", firstMosb.Name)
	// Wait for the first build to be deleted.
	waitForBuildToBeDeleted(t, cs, firstMosb)

	// Ensure that the second build still exists.
	_, err = cs.MachineconfigurationV1Interface.MachineOSBuilds().Get(context.TODO(), moscChangeMosb.Name, metav1.GetOptions{})
	require.NoError(t, err)
}

// This test verifies that a change to the MachineConfigPool, such as the
// presence of a new rendered MachineConfig, will halt the currently running
// build, replacing it with a new build instead.
func TestMachineConfigPoolChangeRestartsBuild(t *testing.T) {
	cs := framework.NewClientSet("")

	mosc := prepareForOnClusterLayeringTest(t, cs, onClusterLayeringTestOpts{
		poolName: layeredMCPName,
		customDockerfiles: map[string]string{
			layeredMCPName: ocltesthelper.CowsayDockerfile,
		},
	})

	createMachineOSConfig(t, cs, mosc)

	// Wait for the first build to start.
	firstMosb := waitForBuildToStartForPoolAndConfig(t, cs, layeredMCPName, mosc.Name)

	// Once the first build has started, we create a new MachineConfig, wait for
	// the rendered config to appear, then we check that a new MachineOSBuild has
	// started for that new MachineConfig.
	mcName := "new-machineconfig"
	mc := ocltesthelper.NewMachineConfigTriggersImageRebuild(mcName, layeredMCPName, []string{"usbguard"})
	applyMC(t, cs, mc)

	_, err := helpers.WaitForRenderedConfig(t, cs, layeredMCPName, mcName)
	require.NoError(t, err)

	// We wait for the first build to be deleted.
	waitForBuildToBeDeleted(t, cs, firstMosb)

	// Next, we wait for the new build to be started.
	secondMosb := waitForBuildToStartForPoolAndConfig(t, cs, layeredMCPName, mosc.Name)

	_, err = cs.MachineconfigurationV1Interface.MachineOSBuilds().Get(context.TODO(), secondMosb.Name, metav1.GetOptions{})
	require.NoError(t, err)
}

// This test verifies that if the rebuild annotation is added to a given MachineOSConfig, that
// the build is restarted
func TestRebuildAnnotationRestartsBuild(t *testing.T) {
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

	// First, we get a MachineOSBuild started as usual.
	firstMosb := waitForBuildToStartForPoolAndConfig(t, cs, layeredMCPName, mosc.Name)

	assert.NotEqual(t, string(firstMosb.UID), "")

	kubeassert := helpers.AssertClientSet(t, cs).WithContext(ctx)
	assertBuildObjectsAreCreated(t, kubeassert, firstMosb)

	firstJob, err := cs.BatchV1Interface.Jobs(ctrlcommon.MCONamespace).Get(ctx, utils.GetBuildJobName(firstMosb), metav1.GetOptions{})
	require.NoError(t, err)

	pod, err := ocltesthelper.GetPodFromJob(ctx, cs, utils.GetBuildJobName(firstMosb))
	require.NoError(t, err)
	t.Logf("Initial build has started, delete the job to interrupt the build...")
	// Delete the builder
	bgDeletion := metav1.DeletePropagationBackground
	err = cs.BatchV1Interface.Jobs(ctrlcommon.MCONamespace).Delete(ctx, utils.GetBuildJobName(firstMosb), metav1.DeleteOptions{PropagationPolicy: &bgDeletion})
	require.NoError(t, err)

	// Wait for the build to be interrupted.
	waitForBuildToBeInterrupted(t, cs, firstMosb)

	// Wait for the job and pod to be deleted.
	kubeassert.Eventually().JobDoesNotExist(utils.GetBuildJobName(firstMosb))
	kubeassert.Eventually().PodDoesNotExist(pod.Name)

	t.Logf("Add rebuild annotation to the MOSC...")
	helpers.SetRebuildAnnotationOnMachineOSConfig(ctx, t, cs.GetMcfgclient(), mosc)

	// Wait for the MOSB to be deleted
	t.Logf("Waiting for MachineOSBuild with UID %s to be deleted", firstMosb.UID)
	waitForMOSBToBeDeleted(t, cs, firstMosb)

	t.Logf("Annotation is updated, waiting for new build %s to start", firstMosb.Name)
	// Wait for the build to start.
	secondMosb := waitForBuildToStart(t, cs, firstMosb)

	secondJob, err := cs.BatchV1Interface.Jobs(ctrlcommon.MCONamespace).Get(ctx, utils.GetBuildJobName(secondMosb), metav1.GetOptions{})
	require.NoError(t, err)

	// Ensure that the names are the same, but that the first and second
	// MachineOSBuilds have different UIDs.
	assert.Equal(t, firstMosb.Name, secondMosb.Name)
	assert.NotEqual(t, firstMosb.UID, secondMosb.UID)

	// Ensure that the build jobs have also changed.
	assert.Equal(t, firstJob.Name, secondJob.Name)
	assert.NotEqual(t, firstJob.UID, secondJob.UID)
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

	// Assert that the build job has certain properties and configuration.
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

// Waits for the build to start and returns the started MachineOSBuild object.
func waitForBuildToStartForPoolAndConfig(t *testing.T, cs *framework.ClientSet, poolName, moscName string) *mcfgv1.MachineOSBuild {
	t.Helper()

	var mosbName string

	require.NoError(t, wait.PollImmediate(2*time.Second, 3*time.Minute, func() (bool, error) {
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

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute*5)
	defer cancel()

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
	err := wait.PollImmediate(time.Second, time.Minute*5, func() (bool, error) {
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

// Validates that the build job is configured correctly. In this case,
// "correctly" means that it has the correct container images. Future
// assertions could include things like ensuring that the proper volume mounts
// are present, etc.
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
	require.NoError(t, wait.PollImmediate(1*time.Second, 5*time.Minute, func() (bool, error) {
		mosc, err := cs.MachineconfigurationV1Interface.MachineOSConfigs().Get(ctx, moscName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		return mosc.Status.CurrentImagePullSpec != "" && string(mosc.Status.CurrentImagePullSpec) == pullspec, nil
	}))
}

func waitForMOSCToUpdateCurrentMOSB(ctx context.Context, t *testing.T, cs *framework.ClientSet, moscName, mosbName string) string {
	var currentMOSB string
	require.NoError(t, wait.PollImmediate(1*time.Second, 5*time.Minute, func() (bool, error) {
		mosc, err := cs.MachineconfigurationV1Interface.MachineOSConfigs().Get(ctx, moscName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		currentMOSB = mosc.GetAnnotations()[constants.CurrentMachineOSBuildAnnotationKey]
		return currentMOSB != mosbName, nil

	}))
	return currentMOSB
}
