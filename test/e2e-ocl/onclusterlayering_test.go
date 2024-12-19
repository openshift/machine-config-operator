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

	corev1 "k8s.io/api/core/v1"

	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"

	"github.com/openshift/machine-config-operator/pkg/daemon/runtimeassets"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
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
	imageBuilderType mcfgv1alpha1.MachineOSImageBuilderType

	// The custom Dockerfiles to use for the test. This is a map of MachineConfigPool name to Dockerfile content.
	customDockerfiles map[string]string

	// What node should be targeted for the test.
	targetNode *corev1.Node

	// What MachineConfigPool name to use for the test.
	poolName string

	// Use RHEL entitlements
	useEtcPkiEntitlement bool

	// Inject YUM repo information from a Centos 9 stream container
	useYumRepos bool

	// Add Extensions for testing
	useExtensions bool
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
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	runOnClusterLayeringTest(t, onClusterLayeringTestOpts{
		poolName: layeredMCPName,
		customDockerfiles: map[string]string{
			layeredMCPName: cowsayDockerfile,
		},
	})

	t.Logf("Applying rebuild annotation (%q) to MachineOSConfig (%q) to cause a rebuild", constants.RebuildMachineOSConfigAnnotationKey, layeredMCPName)

	cs := framework.NewClientSet("")

	mosc, err := cs.MachineconfigurationV1alpha1Interface.MachineOSConfigs().Get(ctx, layeredMCPName, metav1.GetOptions{})
	require.NoError(t, err)

	mosc.Annotations[constants.RebuildMachineOSConfigAnnotationKey] = ""

	_, err = cs.MachineconfigurationV1alpha1Interface.MachineOSConfigs().Update(ctx, mosc, metav1.UpdateOptions{})
	require.NoError(t, err)

	waitForBuildToStartForPoolAndConfig(t, cs, layeredMCPName, layeredMCPName)
}

// Tests that an on-cluster build can be performed and that the resulting image
// is rolled out to an opted-in node.
func TestOnClusterBuildRollsOutImageWithExtensionsInstalled(t *testing.T) {
	imagePullspec := runOnClusterLayeringTest(t, onClusterLayeringTestOpts{
		poolName: layeredMCPName,
		customDockerfiles: map[string]string{
			layeredMCPName: cowsayDockerfile,
		},
		useExtensions: true,
	})

	cs := framework.NewClientSet("")
	node := helpers.GetRandomNode(t, cs, "worker")

	unlabelFunc := makeIdempotentAndRegisterAlwaysRun(t, helpers.LabelNode(t, cs, node, helpers.MCPNameToRole(layeredMCPName)))
	helpers.WaitForNodeImageChange(t, cs, node, imagePullspec)

	helpers.AssertNodeBootedIntoImage(t, cs, node, imagePullspec)
	t.Logf("Node %s is booted into image %q", node.Name, imagePullspec)
	assertExtensionInstalledOnNode(t, cs, node, true)

	t.Log(helpers.ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "cowsay", "Moo!"))

	unlabelFunc()

	assertNodeRevertsToNonLayered(t, cs, node)
	assertExtensionInstalledOnNode(t, cs, node, false)
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

func assertExtensionInstalledOnNode(t *testing.T, cs *framework.ClientSet, node corev1.Node, shouldExist bool) {
	foundPkg, err := helpers.ExecCmdOnNodeWithError(cs, node, "chroot", "/rootfs", "rpm", "-q", "usbguard")
	if shouldExist {
		require.NoError(t, err, "usbguard extension not found")
		if strings.Contains(foundPkg, "package usbguard is not installed") {
			t.Fatalf("usbguard package not installed on node %s, got %s", node.Name, foundPkg)
		}
		t.Logf("usbguard extension installed, got %s", foundPkg)
	} else {
		if !strings.Contains(foundPkg, "package usbguard is not installed") {
			t.Fatalf("usbguard package is installed on node %s, got %s", node.Name, foundPkg)
		}
		t.Logf("usbguard extension not installed as expected, got %s", foundPkg)
	}
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

// Clones the etc-pki-entitlement certificate from the openshift-config-managed
// namespace into the MCO namespace. Then performs an on-cluster layering build
// which should consume the entitlement certificates.
func TestEntitledBuilds(t *testing.T) {
	skipOnOKD(t)

	runOnClusterLayeringTest(t, onClusterLayeringTestOpts{
		poolName: layeredMCPName,
		customDockerfiles: map[string]string{
			layeredMCPName: entitledDockerfile,
		},
		useEtcPkiEntitlement: true,
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
	_, err = cs.MachineconfigurationV1alpha1Interface.MachineOSBuilds().Get(context.TODO(), moscChangeMosb.Name, metav1.GetOptions{})
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

	_, err = cs.MachineconfigurationV1alpha1Interface.MachineOSBuilds().Get(context.TODO(), secondMosb.Name, metav1.GetOptions{})
	require.NoError(t, err)
}

// This test starts a build with an image that is known to fail because it does
// not have the necessary binaries within it. After failure, it edits the
// MachineOSConfig with the expectation that the failed build and its objects
// will be deleted and a new build will start in its place.
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

	// Override the base OS container image to pull an invalid (and smaller)
	// image that should produce a failure faster.
	pullspec, err := getImagePullspecForFailureTest(ctx, cs)
	require.NoError(t, err)

	t.Logf("Overriding BaseImagePullspec for MachineOSConfig %s with %q to cause a build failure", mosc.Name, pullspec)

	mosc.Spec.BuildInputs.BaseOSImagePullspec = pullspec

	createMachineOSConfig(t, cs, mosc)

	// Wait for the build to start.
	firstMosb := waitForBuildToStartForPoolAndConfig(t, cs, layeredMCPName, mosc.Name)

	t.Logf("Waiting for MachineOSBuild %s to fail", firstMosb.Name)

	// Wait for the build to fail.
	kubeassert := helpers.AssertClientSet(t, cs).WithContext(ctx)
	kubeassert.Eventually().MachineOSBuildIsFailure(firstMosb)

	// Clear the overridden image pullspec.
	apiMosc, err := cs.MachineconfigurationV1alpha1Interface.MachineOSConfigs().Get(ctx, mosc.Name, metav1.GetOptions{})
	require.NoError(t, err)

	apiMosc.Spec.BuildInputs.BaseOSImagePullspec = ""

	updated, err := cs.MachineconfigurationV1alpha1Interface.MachineOSConfigs().Update(ctx, apiMosc, metav1.UpdateOptions{})
	require.NoError(t, err)

	t.Logf("Cleared BaseImagePullspec to use default value")

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
	err := cs.BatchV1Interface.Jobs(ctrlcommon.MCONamespace).Delete(ctx, utils.GetBuildName(startedBuild), metav1.DeleteOptions{})
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
	pod, err := getPodFromJob(ctx, cs, utils.GetBuildName(startedBuild))
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
	podNew, err := getPodFromJob(ctx, cs, utils.GetBuildName(startedBuild))
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

	firstJob, err := cs.BatchV1Interface.Jobs(ctrlcommon.MCONamespace).Get(ctx, utils.GetBuildName(firstMosb), metav1.GetOptions{})
	require.NoError(t, err)

	// Delete the MachineOSBuild.
	err = cs.MachineconfigurationV1alpha1Interface.MachineOSBuilds().Delete(ctx, firstMosb.Name, metav1.DeleteOptions{})
	require.NoError(t, err)

	t.Logf("MachineOSBuild %q deleted", firstMosb.Name)

	// Wait a few seconds for the MachineOSBuild deletion to complete.
	time.Sleep(time.Second * 5)
	// Ensure that the Job is deleted as this might take some time
	kubeassert := helpers.AssertClientSet(t, cs).WithContext(ctx).Eventually()
	kubeassert.Eventually().JobDoesNotExist(firstJob.Name)

	// Wait for a new MachineOSBuild to start in its place.
	secondMosb := waitForBuildToStartForPoolAndConfig(t, cs, poolName, mosc.Name)

	secondJob, err := cs.BatchV1Interface.Jobs(ctrlcommon.MCONamespace).Get(ctx, utils.GetBuildName(secondMosb), metav1.GetOptions{})
	require.NoError(t, err)

	assert.Equal(t, firstMosb.Name, secondMosb.Name)
	assert.NotEqual(t, firstMosb.UID, secondMosb.UID)

	assert.Equal(t, firstJob.Name, secondJob.Name)
	assert.NotEqual(t, firstJob.UID, secondJob.UID)
}

func assertBuildObjectsAreCreated(t *testing.T, kubeassert *helpers.Assertions, mosb *mcfgv1alpha1.MachineOSBuild) {
	t.Helper()

	kubeassert.JobExists(utils.GetBuildName(mosb))
	kubeassert.ConfigMapExists(utils.GetContainerfileConfigMapName(mosb))
	kubeassert.ConfigMapExists(utils.GetMCConfigMapName(mosb))
	kubeassert.SecretExists(utils.GetBasePullSecretName(mosb))
	kubeassert.SecretExists(utils.GetFinalPushSecretName(mosb))
}

func assertBuildObjectsAreDeleted(t *testing.T, kubeassert *helpers.Assertions, mosb *mcfgv1alpha1.MachineOSBuild) {
	t.Helper()

	kubeassert.JobDoesNotExist(utils.GetBuildName(mosb))
	kubeassert.ConfigMapDoesNotExist(utils.GetContainerfileConfigMapName(mosb))
	kubeassert.ConfigMapDoesNotExist(utils.GetMCConfigMapName(mosb))
	kubeassert.SecretDoesNotExist(utils.GetBasePullSecretName(mosb))
	kubeassert.SecretDoesNotExist(utils.GetFinalPushSecretName(mosb))
}

// This test asserts that any secrets attached to the MachineOSConfig are made
// available to the MCD and get written to the node. Note: In this test, the
// built image should not make it to the node before we've verified that the
// MCD has performed the desired actions.
func TestMCDGetsMachineOSConfigSecrets(t *testing.T) {
	cs := framework.NewClientSet("")

	secretName := "mosc-image-pull-secret"

	// Create a dummy secret with a known hostname which will be assigned to the
	// MachineOSConfig. This secret does not actually have to work for right now;
	// we just need to make sure it lands on the node.
	createSecret(t, cs, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: ctrlcommon.MCONamespace,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: []byte(`{"auths": {"registry.hostname.com": {"username": "user", "password": "password"}}}`),
		},
	})

	// Select a random node that we will opt into the layered pool. Note: If
	// successful, this node will never actually get the new image because we
	// will have halted the build process before that happens.
	node := helpers.GetRandomNode(t, cs, "worker")

	// Set up all of the objects needed for the build, including getting (but not
	// yet applying) the MachineOSConfig.
	mosc := prepareForOnClusterLayeringTest(t, cs, onClusterLayeringTestOpts{
		poolName: layeredMCPName,
		customDockerfiles: map[string]string{
			layeredMCPName: yumReposDockerfile,
		},
		targetNode:  &node,
		useYumRepos: true,
	})

	// Assign the secret name to the MachineOSConfig.
	mosc.Spec.BuildOutputs.CurrentImagePullSecret.Name = secretName

	// Create the MachineOSConfig which will start the build process.
	createMachineOSConfig(t, cs, mosc)

	// Wait for the build to start. We don't need to wait for it to complete
	// since this test is primarily concerned about whether the MCD on the node
	// gets our dummy secret or not. In the future, we should use a real secret
	// and validate that the node can push and pull the image from it. We can
	// simulate that by using an imagestream that lives in a different namespace.
	waitForBuildToStartForPoolAndConfig(t, cs, layeredMCPName, mosc.Name)

	// Verifies that the MCD pod gets the appropriate secret volume and volume mount.
	err := wait.PollImmediate(1*time.Second, 5*time.Minute, func() (bool, error) {
		mcdPod, err := helpers.MCDForNode(cs, &node)

		// If we can ignore this error, it means that the MCD is not ready yet, so
		// return false here and try again later.
		if err != nil && canIgnoreMCDForNodeError(err) {
			return false, nil
		}

		if err != nil {
			return false, err
		}

		// Return true when the following conditions are met:
		// 1. The MCD pod has the expected secret volume.
		// 2. The MCD pod has the expected secret volume mount.
		// 3. All of the containers within the MCD pod are ready and running.
		return podHasExpectedSecretVolume(mcdPod, secretName) &&
			podHasExpectedSecretVolumeMount(mcdPod, layeredMCPName, secretName) &&
			isMcdPodRunning(mcdPod), nil
	})

	require.NoError(t, err)

	t.Logf("MCD pod is running and has secret volume mount for %s", secretName)

	// Get the internal image registry hostnames that we will ensure are present
	// on the target node.
	internalRegistryHostnames, err := getInternalRegistryHostnamesFromControllerConfig(cs)
	require.NoError(t, err)

	// Adds the dummy hostname to the expected internal registry hostname list.
	// At this point, the ControllerConfig may already have our dummy
	// hostname, but there is no harm if it is present in this list twice.
	internalRegistryHostnames = append(internalRegistryHostnames, "registry.hostname.com")

	// Wait for the MCD pod to write the dummy secret to the nodes' filesystem
	// and validate that our dummy hostname is in there along with all of the
	// ones from the ControllerConfig.
	err = wait.PollImmediate(1*time.Second, 5*time.Minute, func() (bool, error) {
		filename := "/etc/mco/internal-registry-pull-secret.json"

		contents := helpers.ExecCmdOnNode(t, cs, node, "cat", filepath.Join("/rootfs", filename))

		for _, hostname := range internalRegistryHostnames {
			if !strings.Contains(contents, hostname) {
				return false, nil
			}
		}

		t.Logf("All hostnames %v found in %q on node %q", internalRegistryHostnames, filename, node.Name)
		return true, nil
	})

	require.NoError(t, err)

	t.Logf("Node filesystem %s got secret from MachineOSConfig", node.Name)
}

// Returns a list of the hostnames provided by the InternalRegistryPullSecret
// field on the ControllerConfig object.
func getInternalRegistryHostnamesFromControllerConfig(cs *framework.ClientSet) ([]string, error) {
	cfg, err := cs.ControllerConfigs().Get(context.TODO(), "machine-config-controller", metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	imagePullCfg, err := ctrlcommon.ToDockerConfigJSON(cfg.Spec.InternalRegistryPullSecret)
	if err != nil {
		return nil, err
	}

	hostnames := []string{}

	for key := range imagePullCfg.Auths {
		hostnames = append(hostnames, key)
	}

	return hostnames, nil
}

// Determines if we can ignore the error returned by helpers.MCDForNode(). This
// checks for a very specific condition that is encountered by this test. In
// order for the MCD to get the secret from the MachineOSConfig, it must be
// restarted. While it is restarting, it is possible that the node will
// temporarily have two MCD pods associated with it; one is being created while
// the other is being terminated.
//
// The helpers.MCDForNode() function cannot
// distinguish between those scenarios, which is fine. But for the purposes of
// this test, we should ignore that specific error because it means that the
// MCD pod is not ready yet.
//
// Finally, it is worth noting that this is not the best way to compare errors,
// but its acceptable for our purposes here.
func canIgnoreMCDForNodeError(err error) bool {
	return strings.Contains(err.Error(), "too many") &&
		strings.Contains(err.Error(), "MCDs for node")
}

func podHasExpectedSecretVolume(pod *corev1.Pod, secretName string) bool {
	for _, volume := range pod.Spec.Volumes {
		if volume.Secret != nil && volume.Secret.SecretName == secretName {
			return true
		}
	}

	return false
}

func podHasExpectedSecretVolumeMount(pod *corev1.Pod, poolName, secretName string) bool {
	for _, container := range pod.Spec.Containers {
		for _, volumeMount := range container.VolumeMounts {
			if volumeMount.Name == secretName && volumeMount.MountPath == filepath.Join("/run/secrets/os-image-pull-secrets", poolName) {
				return true
			}
		}
	}

	return false
}

// Determines whether a given MCD pod is running. Returns true only once all of
// the container statuses are in a running state.
func isMcdPodRunning(pod *corev1.Pod) bool {
	for _, containerStatus := range pod.Status.ContainerStatuses {
		if containerStatus.Ready != true {
			return false
		}

		if containerStatus.Started == nil {
			return false
		}

		if *containerStatus.Started != true {
			return false
		}

		if containerStatus.State.Running == nil {
			return false
		}
	}

	return true
}

// Sets up and performs an on-cluster build for a given set of parameters.
// Returns the built image pullspec for later consumption.
func runOnClusterLayeringTest(t *testing.T, testOpts onClusterLayeringTestOpts) string {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cs := framework.NewClientSet("")

	imageBuilder := testOpts.imageBuilderType
	if testOpts.imageBuilderType == "" {
		imageBuilder = mcfgv1alpha1.PodBuilder
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

	t.Logf("MachineOSBuild %q has completed and produced image: %s", finishedBuild.Name, finishedBuild.Status.FinalImagePushspec)

	require.NoError(t, archiveBuildPodLogs(t, podLogsDirPath))

	return finishedBuild.Status.FinalImagePushspec
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
func waitForBuildToStartForPoolAndConfig(t *testing.T, cs *framework.ClientSet, poolName, moscName string) *mcfgv1alpha1.MachineOSBuild {
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
	mosb := &mcfgv1alpha1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: mosbName,
		},
	}

	return waitForBuildToStart(t, cs, mosb)
}

// Waits for a MachineOSBuild to start building.
func waitForBuildToStart(t *testing.T, cs *framework.ClientSet, build *mcfgv1alpha1.MachineOSBuild) *mcfgv1alpha1.MachineOSBuild {
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
	kubeassert.Eventually().JobExists(utils.GetBuildName(build))
	t.Logf("Build job %s created after %s", utils.GetBuildName(build), time.Since(start))
	// Get the pod created by the job
	buildPod, err := getPodFromJob(context.TODO(), cs, utils.GetBuildName(build))
	require.NoError(t, err)
	kubeassert.Eventually().PodIsRunning(buildPod.Name)
	t.Logf("Build pod %s running after %s", buildPod.Name, time.Since(start))

	mosb, err := cs.MachineconfigurationV1alpha1Interface.MachineOSBuilds().Get(ctx, build.Name, metav1.GetOptions{})
	require.NoError(t, err)

	assertBuildObjectsAreCreated(t, kubeassert.Eventually(), mosb)
	t.Logf("Build objects created after %s", time.Since(start))

	return mosb
}

// Waits for a MachineOSBuild to be deleted.
func waitForBuildToBeDeleted(t *testing.T, cs *framework.ClientSet, build *mcfgv1alpha1.MachineOSBuild) {
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
func waitForBuildToComplete(t *testing.T, cs *framework.ClientSet, startedBuild *mcfgv1alpha1.MachineOSBuild) *mcfgv1alpha1.MachineOSBuild {
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

	mosb, err := cs.MachineconfigurationV1alpha1Interface.MachineOSBuilds().Get(ctx, startedBuild.Name, metav1.GetOptions{})
	require.NoError(t, err)

	return mosb
}

// Validates that the build job is configured correctly. In this case,
// "correctly" means that it has the correct container images. Future
// assertions could include things like ensuring that the proper volume mounts
// are present, etc.
func assertBuildJobIsAsExpected(t *testing.T, cs *framework.ClientSet, mosb *mcfgv1alpha1.MachineOSBuild) {
	t.Helper()

	osImageURLConfig, err := ctrlcommon.GetOSImageURLConfig(context.TODO(), cs.GetKubeclient())
	require.NoError(t, err)

	mcoImages, err := ctrlcommon.GetImagesConfig(context.TODO(), cs.GetKubeclient())
	require.NoError(t, err)

	buildPod, err := getPodFromJob(context.TODO(), cs, mosb.Status.BuilderReference.PodImageBuilder.Name)
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
func prepareForOnClusterLayeringTest(t *testing.T, cs *framework.ClientSet, testOpts onClusterLayeringTestOpts) *mcfgv1alpha1.MachineOSConfig {
	// If the test requires RHEL entitlements, clone them from
	// "etc-pki-entitlement" in the "openshift-config-managed" namespace.
	if testOpts.useEtcPkiEntitlement {
		copyEntitlementCerts(t, cs)
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
		Name:      "os-image",
		Namespace: strings.ToLower(t.Name()),
	}

	pushSecretName, finalPullspec, _ := setupImageStream(t, cs, imagestreamObjMeta)

	copyGlobalPullSecret(t, cs)

	if testOpts.targetNode != nil {
		makeIdempotentAndRegister(t, helpers.CreatePoolWithNode(t, cs, testOpts.poolName, *testOpts.targetNode))
	} else {
		makeIdempotentAndRegister(t, helpers.CreateMCP(t, cs, testOpts.poolName))
	}

	if testOpts.useExtensions {
		extensionsMC := &mcfgv1.MachineConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "99-extensions",
				Labels: helpers.MCLabelForRole(testOpts.poolName),
			},
			Spec: mcfgv1.MachineConfigSpec{
				Config: runtime.RawExtension{
					Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
				},
				Extensions: []string{"usbguard"},
			},
		}

		helpers.SetMetadataOnObject(t, extensionsMC)
		// Apply the extensions MC
		applyMC(t, cs, extensionsMC)

		// Wait for rendered config to finish creating
		renderedConfig, err := helpers.WaitForRenderedConfig(t, cs, testOpts.poolName, extensionsMC.Name)
		require.NoError(t, err)
		t.Logf("Finished rendering config %s", renderedConfig)
	}

	_, err := helpers.WaitForRenderedConfig(t, cs, testOpts.poolName, "00-worker")
	require.NoError(t, err)

	mosc := &mcfgv1alpha1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: testOpts.poolName,
		},
		Spec: mcfgv1alpha1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1alpha1.MachineConfigPoolReference{
				Name: testOpts.poolName,
			},
			BuildInputs: mcfgv1alpha1.BuildInputs{
				BaseImagePullSecret: mcfgv1alpha1.ImageSecretObjectReference{
					Name: globalPullSecretCloneName,
				},
				RenderedImagePushSecret: mcfgv1alpha1.ImageSecretObjectReference{
					Name: pushSecretName,
				},
				RenderedImagePushspec: finalPullspec,
				ImageBuilder: &mcfgv1alpha1.MachineOSImageBuilder{
					ImageBuilderType: mcfgv1alpha1.PodBuilder,
				},
				Containerfile: []mcfgv1alpha1.MachineOSContainerfile{
					{
						ContainerfileArch: mcfgv1alpha1.NoArch,
						Content:           testOpts.customDockerfiles[testOpts.poolName],
					},
				},
			},
			BuildOutputs: mcfgv1alpha1.BuildOutputs{
				CurrentImagePullSecret: mcfgv1alpha1.ImageSecretObjectReference{
					Name: pushSecretName,
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
