package e2e_ocl_test

import (
	"context"
	_ "embed"
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

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"

	"github.com/openshift/machine-config-operator/pkg/daemon/runtimeassets"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/imagebuilder"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	// The MachineConfigPool to create for the tests.
	layeredMCPName string = "layered"

	// The name of the global pull secret copy to use for the tests.
	globalPullSecretCloneName string = "global-pull-secret-copy"
)

var (
	// Provides a Containerfile that installs cowsayusing the Centos Stream 9
	// EPEL repository to do so without requiring any entitlements.
	//go:embed Containerfile.cowsay
	cowsayDockerfile string

	// Provides a Containerfile that installs Buildah from the default RHCOS RPM
	// repositories. If the installation succeeds, the entitlement certificate is
	// working.
	//go:embed Containerfile.entitled
	entitledDockerfile string

	// Provides a Containerfile that works similarly to the cowsay Dockerfile
	// with the exception that the /etc/yum.repos.d and /etc/pki/rpm-gpg key
	// content is mounted into the build context by the BuildController.
	//go:embed Containerfile.yum-repos-d
	yumReposDockerfile string

	//go:embed Containerfile.okd-fcos
	okdFcosDockerfile string
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
}

func TestOnClusterLayeringOnOKD(t *testing.T) {
	skipOnOCP(t)

	runOnClusterLayeringTest(t, onClusterLayeringTestOpts{
		poolName: layeredMCPName,
		customDockerfiles: map[string]string{
			layeredMCPName: okdFcosDockerfile,
		},
	})
}

// Tests that an on-cluster build can be performed with the Custom Pod Builder.
func TestOnClusterLayering(t *testing.T) {

	runOnClusterLayeringTest(t, onClusterLayeringTestOpts{
		poolName: layeredMCPName,
		customDockerfiles: map[string]string{
			layeredMCPName: cowsayDockerfile,
		},
	})

	/* Removing this portion of this test - update when https://issues.redhat.com/browse/OCPBUGS-46421 fixed.

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	t.Logf("Applying rebuild annotation (%q) to MachineOSConfig (%q) to cause a rebuild", constants.RebuildMachineOSConfigAnnotationKey, layeredMCPName)

	cs := framework.NewClientSet("")

	mosc, err := cs.MachineconfigurationV1Interface.MachineOSConfigs().Get(ctx, layeredMCPName, metav1.GetOptions{})
	require.NoError(t, err)

	mosc.Annotations[constants.RebuildMachineOSConfigAnnotationKey] = ""

	_, err = cs.MachineconfigurationV1Interface.MachineOSConfigs().Update(ctx, mosc, metav1.UpdateOptions{})
	require.NoError(t, err)

	waitForBuildToStartForPoolAndConfig(t, cs, layeredMCPName, layeredMCPName)
	*/
}

// Tests that an on-cluster build can be performed and that the resulting image
// is rolled out to an opted-in node.
func TestOnClusterBuildRollsOutImage(t *testing.T) {
	imagePullspec := runOnClusterLayeringTest(t, onClusterLayeringTestOpts{
		poolName: layeredMCPName,
		customDockerfiles: map[string]string{
			layeredMCPName: cowsayDockerfile,
		},
	})

	cs := framework.NewClientSet("")
	node := helpers.GetRandomNode(t, cs, "worker")

	unlabelFunc := makeIdempotentAndRegisterAlwaysRun(t, helpers.LabelNode(t, cs, node, helpers.MCPNameToRole(layeredMCPName)))
	helpers.WaitForNodeImageChange(t, cs, node, imagePullspec)

	helpers.AssertNodeBootedIntoImage(t, cs, node, imagePullspec)
	t.Logf("Node %s is booted into image %q", node.Name, imagePullspec)

	t.Log(helpers.ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "cowsay", "Moo!"))

	unlabelFunc()

	assertNodeRevertsToNonLayered(t, cs, node)
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

// This test extracts the /etc/yum.repos.d and /etc/pki/rpm-gpg content from a
// Centos Stream 9 image and injects them into the MCO namespace. It then
// performs a build with the expectation that these artifacts will be used,
// simulating a build where someone has added this content; usually a Red Hat
// Satellite user.
func TestYumReposBuilds(t *testing.T) {
	runOnClusterLayeringTest(t, onClusterLayeringTestOpts{
		poolName: layeredMCPName,
		customDockerfiles: map[string]string{
			layeredMCPName: yumReposDockerfile,
		},
		useYumRepos: true,
	})
}

// Then performs an on-cluster layering build which should consume the
// etc-pki-entitlement certificates.
func TestEntitledBuilds(t *testing.T) {
	skipOnOKD(t)

	runOnClusterLayeringTest(t, onClusterLayeringTestOpts{
		poolName: layeredMCPName,
		customDockerfiles: map[string]string{
			layeredMCPName: entitledDockerfile,
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
			layeredMCPName: cowsayDockerfile,
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
			layeredMCPName: cowsayDockerfile,
		},
	})

	createMachineOSConfig(t, cs, mosc)

	// Wait for the first build to start.
	firstMosb := waitForBuildToStartForPoolAndConfig(t, cs, layeredMCPName, mosc.Name)

	// Once the first build has started, we create a new MachineConfig, wait for
	// the rendered config to appear, then we check that a new MachineOSBuild has
	// started for that new MachineConfig.
	mcName := "new-machineconfig"
	mc := newMachineConfig(mcName, layeredMCPName)
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

// This test starts a build with an image that is known to fail because it uses
// an invalid containerfile. After failure, it edits the  MachineOSConfig
// with the expectation that the failed build and its  will be deleted and a new
// build will start in its place.
func TestGracefulBuildFailureRecovery(t *testing.T) {

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cs := framework.NewClientSet("")

	mosc := prepareForOnClusterLayeringTest(t, cs, onClusterLayeringTestOpts{
		poolName: layeredMCPName,
		customDockerfiles: map[string]string{
			layeredMCPName: cowsayDockerfile,
		},
	})

	// Add a bad containerfile so that we can cause a build failure
	t.Logf("Adding a bad containerfile for MachineOSConfig %s to cause a build failure", mosc.Name)

	mosc.Spec.Containerfile = getBadContainerFileForFailureTest()

	createMachineOSConfig(t, cs, mosc)

	// Wait for the build to start.
	firstMosb := waitForBuildToStartForPoolAndConfig(t, cs, layeredMCPName, mosc.Name)

	t.Logf("Waiting for MachineOSBuild %s to fail", firstMosb.Name)

	// Wait for the build to fail.
	kubeassert := helpers.AssertClientSet(t, cs).WithContext(ctx)
	kubeassert.Eventually().MachineOSBuildIsFailure(firstMosb)

	// Clear the overridden image pullspec.
	apiMosc, err := cs.MachineconfigurationV1Interface.MachineOSConfigs().Get(ctx, mosc.Name, metav1.GetOptions{})
	require.NoError(t, err)

	apiMosc.Spec.Containerfile = []mcfgv1.MachineOSContainerfile{}

	updated, err := cs.MachineconfigurationV1Interface.MachineOSConfigs().Update(ctx, apiMosc, metav1.UpdateOptions{})
	require.NoError(t, err)

	t.Logf("Cleared out bad containerfile")

	mcp, err := cs.MachineconfigurationV1Interface.MachineConfigPools().Get(ctx, layeredMCPName, metav1.GetOptions{})
	require.NoError(t, err)

	// Compute the new MachineOSBuild image name.
	moscChangeMosb := buildrequest.NewMachineOSBuildFromAPIOrDie(ctx, cs.GetKubeclient(), updated, mcp)

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
			layeredMCPName: cowsayDockerfile,
		},
	})

	// Create our MachineOSConfig and ensure that it is deleted after the test is
	// finished.
	createMachineOSConfig(t, cs, mosc)

	// Wait for the build to start
	startedBuild := waitForBuildToStartForPoolAndConfig(t, cs, poolName, mosc.Name)
	t.Logf("MachineOSBuild %q has started", startedBuild.Name)

	// Delete the builder
	err := cs.BatchV1Interface.Jobs(ctrlcommon.MCONamespace).Delete(ctx, utils.GetBuildJobName(startedBuild), metav1.DeleteOptions{})
	require.NoError(t, err)

	// Wait for the build to be interrupted.
	kubeassert := helpers.AssertClientSet(t, cs).WithContext(ctx).Eventually()
	kubeassert.MachineOSBuildIsInterrupted(startedBuild)
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
			layeredMCPName: cowsayDockerfile,
		},
	})

	// Create our MachineOSConfig and ensure that it is deleted after the test is
	// finished.
	createMachineOSConfig(t, cs, mosc)

	// Wait for the build to start
	startedBuild := waitForBuildToStartForPoolAndConfig(t, cs, poolName, mosc.Name)
	t.Logf("MachineOSBuild %q has started", startedBuild.Name)

	// Get the pod created by the build Job
	pod, err := getPodFromJob(ctx, cs, utils.GetBuildJobName(startedBuild))
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
	podNew, err := getPodFromJob(ctx, cs, utils.GetBuildJobName(startedBuild))
	require.NoError(t, err)
	assert.NotEqual(t, podNew, pod)
}

// This test validates that when a running MachineOSBuild is deleted that it will be recreated.
func TestDeletedTransientMachineOSBuildIsRecreated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cs := framework.NewClientSet("")

	poolName := layeredMCPName

	mosc := prepareForOnClusterLayeringTest(t, cs, onClusterLayeringTestOpts{
		poolName: poolName,
		customDockerfiles: map[string]string{
			layeredMCPName: cowsayDockerfile,
		},
	})

	// Create our MachineOSConfig and ensure that it is deleted after the test is
	// finished.
	createMachineOSConfig(t, cs, mosc)

	// Wait for the build to start
	firstMosb := waitForBuildToStartForPoolAndConfig(t, cs, poolName, mosc.Name)

	firstJob, err := cs.BatchV1Interface.Jobs(ctrlcommon.MCONamespace).Get(ctx, utils.GetBuildJobName(firstMosb), metav1.GetOptions{})
	require.NoError(t, err)

	// Delete the MachineOSBuild.
	err = cs.MachineconfigurationV1Interface.MachineOSBuilds().Delete(ctx, firstMosb.Name, metav1.DeleteOptions{})
	require.NoError(t, err)

	t.Logf("MachineOSBuild %q deleted", firstMosb.Name)

	// Wait a few seconds for the MachineOSBuild deletion to complete.
	time.Sleep(time.Second * 5)
	// Ensure that the Job is deleted as this might take some time
	kubeassert := helpers.AssertClientSet(t, cs).WithContext(ctx).Eventually()
	kubeassert.Eventually().JobDoesNotExist(firstJob.Name)

	// Wait for a new MachineOSBuild to start in its place.
	secondMosb := waitForBuildToStartForPoolAndConfig(t, cs, poolName, mosc.Name)

	secondJob, err := cs.BatchV1Interface.Jobs(ctrlcommon.MCONamespace).Get(ctx, utils.GetBuildJobName(secondMosb), metav1.GetOptions{})
	require.NoError(t, err)

	assert.Equal(t, firstMosb.Name, secondMosb.Name)
	assert.NotEqual(t, firstMosb.UID, secondMosb.UID)

	assert.Equal(t, firstJob.Name, secondJob.Name)
	assert.NotEqual(t, firstJob.UID, secondJob.UID)
}

func assertBuildObjectsAreCreated(t *testing.T, kubeassert *helpers.Assertions, mosb *mcfgv1.MachineOSBuild) {
	t.Helper()

	kubeassert.JobExists(utils.GetBuildJobName(mosb))
	kubeassert.ConfigMapExists(utils.GetContainerfileConfigMapName(mosb))
	kubeassert.ConfigMapExists(utils.GetMCConfigMapName(mosb))
	kubeassert.SecretExists(utils.GetBasePullSecretName(mosb))
	kubeassert.SecretExists(utils.GetFinalPushSecretName(mosb))
}

func assertBuildObjectsAreDeleted(t *testing.T, kubeassert *helpers.Assertions, mosb *mcfgv1.MachineOSBuild) {
	t.Helper()

	kubeassert.JobDoesNotExist(utils.GetBuildJobName(mosb))
	kubeassert.ConfigMapDoesNotExist(utils.GetContainerfileConfigMapName(mosb))
	kubeassert.ConfigMapDoesNotExist(utils.GetMCConfigMapName(mosb))
	kubeassert.SecretDoesNotExist(utils.GetBasePullSecretName(mosb))
	kubeassert.SecretDoesNotExist(utils.GetFinalPushSecretName(mosb))
}

// Sets up and performs an on-cluster build for a given set of parameters.
// Returns the built image pullspec for later consumption.
func runOnClusterLayeringTest(t *testing.T, testOpts onClusterLayeringTestOpts) string {
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
	buildPodWatcherShutdown := makeIdempotentAndRegisterAlwaysRun(t, buildPodStreamerCancel)
	defer buildPodWatcherShutdown()

	dirPath, err := helpers.GetBuildArtifactDir(t)
	require.NoError(t, err)

	podLogsDirPath := filepath.Join(dirPath, "pod-logs")
	require.NoError(t, os.MkdirAll(podLogsDirPath, 0o755))

	// In the event of a test failure, we want to dump all of the build artifacts
	// to files for easy reference later.
	t.Cleanup(func() {
		if t.Failed() {
			writeBuildArtifactsToFiles(t, cs, testOpts.poolName)
		}
	})

	// The pod log collection blocks the main Goroutine since we follow the logs
	// for each container in the build pod. So they must run in a separate
	// Goroutine so that the rest of the test can continue.
	go func() {
		err := streamBuildPodLogsToFile(buildPodStreamerCtx, t, cs, startedBuild, podLogsDirPath)
		require.NoError(t, err, "expected no error, got %s", err)
	}()

	// We also want to collect logs from the machine-os-builder pod since they
	// can provide a valuable window in how / why a test failed. As mentioned
	// above, we need to run this in a separate Goroutine so that the test is not
	// blocked.
	go func() {
		err := streamMachineOSBuilderPodLogsToFile(mobPodStreamerCtx, t, cs, podLogsDirPath)
		require.NoError(t, err, "expected no error, got: %s", err)
	}()

	// Wait for the build to complete.
	finishedBuild := waitForBuildToComplete(t, cs, startedBuild)

	t.Logf("MachineOSBuild %q has completed and produced image: %s", finishedBuild.Name, finishedBuild.Status.DigestedImagePushSpec)

	require.NoError(t, archiveBuildPodLogs(t, podLogsDirPath))

	return string(finishedBuild.Status.DigestedImagePushSpec)
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
		t.Logf(string(output))
		return err
	}

	return archive.WriteArchive()
}

// Waits for the build to start and returns the started MachineOSBuild object.
func waitForBuildToStartForPoolAndConfig(t *testing.T, cs *framework.ClientSet, poolName, moscName string) *mcfgv1.MachineOSBuild {
	t.Helper()

	var mosbName string

	require.NoError(t, wait.PollImmediate(time.Second, time.Minute, func() (bool, error) {
		// Get the name for the MachineOSBuild based upon the MachineConfigPool and MachineOSConfig state.
		name, err := getMachineOSBuildNameForPool(cs, poolName, moscName)
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
	// The Job reports running before the pod is fully up and running, so the mosb ends up in building status
	// however, since we are streaming container logs we might hit a race where the container has not started yet
	// so add a check to ensure that the pod is up an running also
	kubeassert.Eventually().JobExists(utils.GetBuildJobName(build))
	t.Logf("Build job %s created after %s", utils.GetBuildJobName(build), time.Since(start))
	// Get the pod created by the job
	buildPod, err := getPodFromJob(context.TODO(), cs, utils.GetBuildJobName(build))
	require.NoError(t, err)
	kubeassert.Eventually().PodIsRunning(buildPod.Name)
	t.Logf("Build pod %s running after %s", buildPod.Name, time.Since(start))

	mosb, err := cs.MachineconfigurationV1Interface.MachineOSBuilds().Get(ctx, build.Name, metav1.GetOptions{})
	require.NoError(t, err)

	assertBuildObjectsAreCreated(t, kubeassert.Eventually(), mosb)
	t.Logf("Build objects created after %s", time.Since(start))

	return mosb
}

// Waits for a MachineOSBuild to be deleted.
func waitForBuildToBeDeleted(t *testing.T, cs *framework.ClientSet, build *mcfgv1.MachineOSBuild) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute*5)
	defer cancel()

	t.Logf("Waiting for MachineOSBuild %s to be deleted", build.Name)

	start := time.Now()
	kubeassert := helpers.AssertClientSet(t, cs).WithContext(ctx)
	kubeassert.Eventually().MachineOSBuildDoesNotExist(build)
	t.Logf("MachineOSBuild %s deleted after %s", build.Name, time.Since(start))

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
	kubeassert.Eventually().MachineOSBuildIsSuccessful(startedBuild) //foo
	t.Logf("MachineOSBuild %s successful after %s", startedBuild.Name, time.Since(start))
	assertBuildObjectsAreDeleted(t, kubeassert.Eventually(), startedBuild)
	t.Logf("Build objects deleted after %s", time.Since(start))

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

	buildPod, err := getPodFromJob(context.TODO(), cs, mosb.Status.Builder.Job.Name)
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
		skipIfEntitlementNotPresent(t, cs)
	}

	// If the test requires /etc/yum.repos.d and /etc/pki/rpm-gpg, pull a Centos
	// Stream 9 container image and populate them from there. This is intended to
	// emulate the Red Hat Satellite enablement process, but does not actually
	// require any Red Hat Satellite creds to work.
	if testOpts.useYumRepos {
		injectYumRepos(t, cs)
	}

	// Register ephemeral object cleanup function.
	makeIdempotentAndRegister(t, func() {
		cleanupEphemeralBuildObjects(t, cs)
	})

	imagestreamObjMeta := metav1.ObjectMeta{
		Name: "os-image",
	}

	pushSecretName, finalPullspec, _ := setupImageStream(t, cs, imagestreamObjMeta)

	if testOpts.targetNode != nil {
		makeIdempotentAndRegister(t, helpers.CreatePoolWithNode(t, cs, testOpts.poolName, *testOpts.targetNode))
	} else {
		makeIdempotentAndRegister(t, helpers.CreateMCP(t, cs, testOpts.poolName))
	}

	_, err := helpers.WaitForRenderedConfig(t, cs, testOpts.poolName, "00-worker")
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
			layeredMCPName: cowsayDockerfile,
		},
	})

	mcp, err := cs.MachineconfigurationV1Interface.MachineConfigPools().Get(ctx, poolName, metav1.GetOptions{})
	require.NoError(t, err)

	mosb := buildrequest.NewMachineOSBuildFromAPIOrDie(ctx, cs.GetKubeclient(), mosc, mcp)

	jobName := utils.GetBuildJobName(mosb)

	createMachineOSConfig(t, cs, mosc)

	// Wait for the MachineOSBuild to exist.
	kubeassert := helpers.AssertClientSet(t, cs).WithContext(ctx).Eventually()
	kubeassert.MachineOSBuildExists(mosb)
	kubeassert.JobExists(utils.GetBuildJobName(mosb))

	t.Logf("MachineOSBuild %q exists, stopping machine-os-builder", mosb.Name)

	// As soon as the MachineOSBuild exists, scale down the machine-os-builder
	// deployment and any other deployments which may inadvertantly cause its
	// replica count to increase. This is done to simulate the machine-os-builder
	// pod being scheduled onto a different node.
	restoreDeployments := scaleDownDeployments(t, cs)

	// Wait for the job to start running.
	waitForJobToReachMOSBCondition(ctx, t, cs, utils.GetBuildJobName(mosb), mcfgv1.MachineOSBuilding)

	t.Logf("Job %s has started running, starting machine-os-builder", jobName)

	// Restore the deployments.
	restoreDeployments()

	// Ensure that the MachineOSBuild object eventually gets updated.
	kubeassert.MachineOSBuildIsRunning(mosb)

	t.Logf("MachineOSBuild %s is now running, stopping machine-os-builder", mosb.Name)

	// Stop the deployments again.
	restoreDeployments = scaleDownDeployments(t, cs)

	// Wait for the job to complete.
	waitForJobToReachMOSBCondition(ctx, t, cs, utils.GetBuildJobName(mosb), mcfgv1.MachineOSBuildSucceeded)

	t.Logf("Job %q finished, starting machine-os-builder", jobName)

	// Restore the deployments again.
	restoreDeployments()

	// At this point, the machine-os-builder is running, so we wait for the build
	// itself to complete and be updated.
	mosb = waitForBuildToComplete(t, cs, mosb)

	// Wait until the MachineOSConfig gets the digested pullspec from the MachineOSBuild.
	require.NoError(t, wait.PollImmediate(1*time.Second, 5*time.Minute, func() (bool, error) {
		mosc, err := cs.MachineconfigurationV1Interface.MachineOSConfigs().Get(ctx, mosc.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		return mosc.Status.CurrentImagePullSpec != "" && mosc.Status.CurrentImagePullSpec == mosb.Status.DigestedImagePushSpec, nil
	}))
}

// Waits for a job object to reach a given state.
// TOOD: Add this to the Asserts helper struct.
func waitForJobToReachCondition(ctx context.Context, t *testing.T, cs *framework.ClientSet, jobName string, condFunc func(*batchv1.Job) (bool, error)) {
	require.NoError(t, wait.PollImmediate(1*time.Second, 10*time.Minute, func() (bool, error) {
		job, err := cs.BatchV1Interface.Jobs(ctrlcommon.MCONamespace).Get(ctx, jobName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		return condFunc(job)
	}))
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
