package e2e_techpreview_test

import (
	"context"
	_ "embed"
	"flag"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"

	"github.com/openshift/machine-config-operator/pkg/controller/build"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	// The MachineConfigPool to create for the tests.
	layeredMCPName string = "layered"

	// The ImageStream name to use for the tests.
	imagestreamName string = "os-image"

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

var skipCleanup bool

func init() {
	// Skips running the cleanup functions. Useful for debugging tests.
	flag.BoolVar(&skipCleanup, "skip-cleanup", false, "Skips running the cleanup functions")
}

// Holds elements common for each on-cluster build tests.
type onClusterBuildTestOpts struct {
	// Which image builder type to use for the test.
	imageBuilderType build.ImageBuilderType

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
}

func TestOnClusterBuildsOnOKD(t *testing.T) {
	skipOnOCP(t)

	runOnClusterBuildTest(t, onClusterBuildTestOpts{
		poolName: layeredMCPName,
		customDockerfiles: map[string]string{
			layeredMCPName: okdFcosDockerfile,
		},
	})
}

// Tests tha an on-cluster build can be performed with the Custom Pod Builder.
func TestOnClusterBuildsCustomPodBuilder(t *testing.T) {
	runOnClusterBuildTest(t, onClusterBuildTestOpts{
		poolName: layeredMCPName,
		customDockerfiles: map[string]string{
			layeredMCPName: cowsayDockerfile,
		},
	})
}

// Tests that an on-cluster build can be performed and that the resulting image
// is rolled out to an opted-in node.
func TestOnClusterBuildRollsOutImage(t *testing.T) {
	imagePullspec := runOnClusterBuildTest(t, onClusterBuildTestOpts{
		poolName: layeredMCPName,
		customDockerfiles: map[string]string{
			layeredMCPName: cowsayDockerfile,
		},
	})

	cs := framework.NewClientSet("")
	node := helpers.GetRandomNode(t, cs, "worker")

	t.Cleanup(makeIdempotentAndRegister(t, func() {
		helpers.DeleteNodeAndMachine(t, cs, node)
	}))

	helpers.LabelNode(t, cs, node, helpers.MCPNameToRole(layeredMCPName))
	helpers.WaitForNodeImageChange(t, cs, node, imagePullspec)

	t.Log(helpers.ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "cowsay", "Moo!"))
}

// This test extracts the /etc/yum.repos.d and /etc/pki/rpm-gpg content from a
// Centos Stream 9 image and injects them into the MCO namespace. It then
// performs a build with the expectation that these artifacts will be used,
// simulating a build where someone has added this content; usually a Red Hat
// Satellite user.
func TestYumReposBuilds(t *testing.T) {
	runOnClusterBuildTest(t, onClusterBuildTestOpts{
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

	runOnClusterBuildTest(t, onClusterBuildTestOpts{
		poolName: layeredMCPName,
		customDockerfiles: map[string]string{
			layeredMCPName: entitledDockerfile,
		},
		useEtcPkiEntitlement: true,
	})
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
	t.Cleanup(createSecret(t, cs, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: secretName,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: []byte(`{"auths": {"registry.hostname.com": {"username": "user", "password": "password"}}}`),
		},
	}))

	// Select a random node that we will opt into the layered pool. Note: If
	// successful, this node will never actually get the new image because we
	// will have halted the build process before that happens.
	node := helpers.GetRandomNode(t, cs, "worker")

	// Set up all of the objects needed for the build, including getting (but not
	// yet applying) the MachineOSConfig.
	mosc := prepareForTest(t, cs, onClusterBuildTestOpts{
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
	t.Cleanup(createMachineOSConfig(t, cs, mosc))

	// Wait for the build to start. We don't need to wait for it to complete
	// since we're mostly concerned about whether the MCD on the node gets our
	// dummy secret.
	waitForBuildToStart(t, cs, layeredMCPName)

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

		return podHasExpectedSecretVolume(mcdPod, secretName) &&
			podHasExpectedSecretVolumeMount(mcdPod, layeredMCPName, secretName), nil
	})

	require.NoError(t, err)

	t.Logf("MCD pod has secret volume mount for %s", secretName)

	// Wait for the MCD pod to write the dummy secret to the nodes' filesystem
	// and validate that our dummy hostname is in there along with all of the
	// ones in the ControllerConfig.
	err = wait.PollImmediate(1*time.Second, 5*time.Minute, func() (bool, error) {
		cfg, err := cs.ControllerConfigs().Get(context.TODO(), "machine-config-controller", metav1.GetOptions{})
		require.NoError(t, err)

		imagePullCfg, err := ctrlcommon.ToDockerConfigJSON(cfg.Spec.InternalRegistryPullSecret)
		require.NoError(t, err)

		hostnames := []string{
			"registry.hostname.com",
		}

		for key := range imagePullCfg.Auths {
			hostnames = append(hostnames, key)
		}

		filename := "/etc/mco/internal-registry-pull-secret.json"

		contents := helpers.ExecCmdOnNode(t, cs, node, "cat", filepath.Join("/rootfs", filename))

		for _, hostname := range hostnames {
			if !strings.Contains(contents, hostname) {
				return false, nil
			}
		}

		t.Logf("All hostnames %v found in %q on node %q", hostnames, filename, node.Name)
		return true, nil
	})

	require.NoError(t, err)

	t.Logf("Node filesystem %s got secret from MachineOSConfig", node.Name)
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

// Sets up and performs an on-cluster build for a given set of parameters.
// Returns the built image pullspec for later consumption.
func runOnClusterBuildTest(t *testing.T, testOpts onClusterBuildTestOpts) string {
	ctx, cancel := context.WithCancel(context.Background())
	cancel = makeIdempotentAndRegister(t, cancel)

	cs := framework.NewClientSet("")

	imageBuilder := testOpts.imageBuilderType
	if testOpts.imageBuilderType == "" {
		imageBuilder = build.CustomPodImageBuilder
	}

	t.Logf("Running with ImageBuilder type: %s", imageBuilder)

	mosc := prepareForTest(t, cs, testOpts)

	t.Cleanup(createMachineOSConfig(t, cs, mosc))

	// Create a child context for the machine-os-builder pod log streamer. We
	// create it here because we want the cancellation to run before the
	// MachineOSConfig object is removed.
	mobPodStreamerCtx, mobPodStreamerCancel := context.WithCancel(ctx)
	t.Cleanup(mobPodStreamerCancel)

	waitForBuildToStart(t, cs, testOpts.poolName)

	t.Logf("Waiting for build completion...")

	// Create a child context for the build pod log streamer. This is so we can
	// cancel it independently of the parent context or the context for the
	// machine-os-build pod watcher (which has its own separate context).
	buildPodStreamerCtx, buildPodStreamerCancel := context.WithCancel(ctx)

	// We wire this to both t.Cleanup() as well as defer because we want to
	// cancel this context either at the end of this function or when the test
	// fails, whichever comes first.
	buildPodWatcherShutdown := makeIdempotentAndRegister(t, buildPodStreamerCancel)
	defer buildPodWatcherShutdown()

	t.Cleanup(func() {
		if t.Failed() {
			writeBuildArtifactsToFiles(t, cs, testOpts.poolName)
		}
	})

	// The pod log collection blocks the main Goroutine since we follow the logs
	// for each container in the build pod. So they must run in a separate
	// Goroutine so that the rest of the test can continue.
	go func() {
		pool, err := cs.MachineconfigurationV1Interface.MachineConfigPools().Get(buildPodStreamerCtx, testOpts.poolName, metav1.GetOptions{})
		require.NoError(t, err)
		err = streamBuildPodLogsToFile(buildPodStreamerCtx, t, cs, pool)
		require.NoError(t, err, "expected no error, got %s", err)
	}()

	// We also want to collect logs from the machine-os-builder pod since they
	// can provide a valuable window in how / why a test failed. As mentioned
	// above, we need to run this in a separate Goroutine so that the test is not
	// blocked.
	go func() {
		err := streamMachineOSBuilderPodLogsToFile(mobPodStreamerCtx, t, cs)
		require.NoError(t, err, "expected no error, got: %s", err)
	}()

	var build *mcfgv1alpha1.MachineOSBuild
	waitForMachineOSBuildToReachState(t, cs, testOpts.poolName, func(mosb *mcfgv1alpha1.MachineOSBuild, err error) (bool, error) {
		if err != nil {
			return false, err
		}

		build = mosb

		state := ctrlcommon.NewMachineOSBuildState(mosb)

		if state.IsBuildFailure() {
			t.Fatalf("MachineOSBuild %q unexpectedly failed", mosb.Name)
		}

		return state.IsBuildSuccess(), nil
	})

	t.Logf("MachineOSBuild %q has finished building. Got image: %s", build.Name, build.Status.FinalImagePushspec)

	return build.Status.FinalImagePushspec
}

func waitForBuildToStart(t *testing.T, cs *framework.ClientSet, poolName string) {
	t.Helper()

	t.Logf("Wait for build to start")

	start := time.Now()

	waitForMachineOSBuildToReachState(t, cs, poolName, func(mosb *mcfgv1alpha1.MachineOSBuild, err error) (bool, error) {
		// If we had any errors retrieving the build, (other than it not existing yet), stop here.
		if err != nil && !k8serrors.IsNotFound(err) {
			return false, err
		}

		// The build object has not been created yet, requeue and try again.
		if mosb == nil && k8serrors.IsNotFound(err) {
			t.Logf("MachineOSBuild does not exist yet, retrying...")
			return false, nil
		}

		// At this point, the build object exists and we want to ensure that it is running.
		return ctrlcommon.NewMachineOSBuildState(mosb).IsBuilding(), nil
	})

	t.Logf("Build started in %s!", time.Since(start))
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
func prepareForTest(t *testing.T, cs *framework.ClientSet, testOpts onClusterBuildTestOpts) *mcfgv1alpha1.MachineOSConfig {
	// If the test requires RHEL entitlements, clone them from
	// "etc-pki-entitlement" in the "openshift-config-managed" namespace.
	if testOpts.useEtcPkiEntitlement {
		t.Cleanup(copyEntitlementCerts(t, cs))
	}

	// If the test requires /etc/yum.repos.d and /etc/pki/rpm-gpg, pull a Centos
	// Stream 9 container image and populate them from there. This is intended to
	// emulate the Red Hat Satellite enablement process, but does not actually
	// require any Red Hat Satellite creds to work.
	if testOpts.useYumRepos {
		t.Cleanup(injectYumRepos(t, cs))
	}

	pushSecretName, err := getBuilderPushSecretName(cs)
	require.NoError(t, err)

	// Register ephemeral object cleanup function.
	t.Cleanup(func() {
		cleanupEphemeralBuildObjects(t, cs)
	})

	imagestreamName := "os-image"
	t.Cleanup(createImagestream(t, cs, imagestreamName))

	t.Cleanup(copyGlobalPullSecret(t, cs))

	finalPullspec, err := getImagestreamPullspec(cs, imagestreamName)
	require.NoError(t, err)

	if testOpts.targetNode != nil {
		t.Cleanup(makeIdempotentAndRegister(t, helpers.CreatePoolWithNode(t, cs, testOpts.poolName, *testOpts.targetNode)))
	} else {
		t.Cleanup(makeIdempotentAndRegister(t, helpers.CreateMCP(t, cs, testOpts.poolName)))
	}

	_, err = helpers.WaitForRenderedConfig(t, cs, testOpts.poolName, "00-worker")
	require.NoError(t, err)

	return &mcfgv1alpha1.MachineOSConfig{
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
}

func TestSSHKeyAndPasswordForOSBuilder(t *testing.T) {
	t.Skip()

	cs := framework.NewClientSet("")

	// label random node from pool, get the node
	unlabelFunc := helpers.LabelRandomNodeFromPool(t, cs, "worker", "node-role.kubernetes.io/layered")
	osNode := helpers.GetSingleNodeByRole(t, cs, layeredMCPName)

	// prepare for on cluster build test
	prepareForTest(t, cs, onClusterBuildTestOpts{
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

	// Create the MachineConfig and wait for the configuration to be applied
	_, err := cs.MachineConfigs().Create(context.TODO(), testConfig, metav1.CreateOptions{})
	require.Nil(t, err, "failed to create MC")
	t.Logf("Created %s", testConfig.Name)

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
		if err := cs.MachineConfigs().Delete(context.TODO(), testConfig.Name, metav1.DeleteOptions{}); err != nil {
			t.Error(err)
		}
		// delete()
		t.Logf("Deleted MachineConfig %s", testConfig.Name)
	})
}
