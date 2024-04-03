package e2e_techpreview_test

import (
	"context"
	_ "embed"
	"flag"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/retry"

	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"

	"github.com/openshift/machine-config-operator/pkg/controller/build"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	// The MachineConfigPool to create for the tests.
	layeredMCPName string = "layered"

	// The ImageStream name to use for the tests.
	imagestreamName string = "os-image"

	// The name of the global pull secret copy to use for the tests.
	globalPullSecretCloneName string = "global-pull-secret-copy"
)

var skipCleanup bool

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
)

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

	// What node(s) should be targeted for the test.
	targetNodes []*corev1.Node

	// What MachineConfigPool name to use for the test.
	poolName string

	// Use RHEL entitlements
	useEtcPkiEntitlement bool

	// Inject YUM repo information from a Centos 9 stream container
	useYumRepos bool
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

// Performs the same build as above, but deploys the built image to a node on
// that cluster and attempts to run the binary installed in the process (in
// this case, buildah).
func TestEntitledBuildsRollsOutImage(t *testing.T) {
	skipOnOKD(t)

	imagePullspec := runOnClusterBuildTest(t, onClusterBuildTestOpts{
		poolName: layeredMCPName,
		customDockerfiles: map[string]string{
			layeredMCPName: entitledDockerfile,
		},
		useEtcPkiEntitlement: true,
	})

	cs := framework.NewClientSet("")
	node := helpers.GetRandomNode(t, cs, "worker")
	t.Cleanup(makeIdempotentAndRegister(t, func() {
		helpers.DeleteNodeAndMachine(t, cs, node)
	}))
	helpers.LabelNode(t, cs, node, helpers.MCPNameToRole(layeredMCPName))
	helpers.WaitForNodeImageChange(t, cs, node, imagePullspec)

	t.Log(helpers.ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "buildah", "--help"))
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

// Sets up and performs an on-cluster build for a given set of parameters.
// Returns the built image pullspec for later consumption.
func runOnClusterBuildTest(t *testing.T, testOpts onClusterBuildTestOpts) string {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cs := framework.NewClientSet("")

	t.Logf("Running with ImageBuilder type: %s", testOpts.imageBuilderType)

	// Create all of the objects needed to set up our test.
	prepareForTest(t, cs, testOpts)

	// Opt the test MachineConfigPool into layering to signal the build to begin.
	optPoolIntoLayering(t, cs, testOpts.poolName)

	t.Logf("Wait for build to start")
	var pool *mcfgv1.MachineConfigPool
	waitForPoolToReachState(t, cs, testOpts.poolName, func(mcp *mcfgv1.MachineConfigPool) bool {
		pool = mcp
		return ctrlcommon.NewLayeredPoolState(mcp).IsBuilding()
	})

	t.Logf("Build started! Waiting for completion...")

	// The pod log collection blocks the main Goroutine since we follow the logs
	// for each container in the build pod. So they must run in a separate
	// Goroutine so that the rest of the test can continue.
	go func() {
		require.NoError(t, streamBuildPodLogsToFile(ctx, t, cs, pool))
	}()

	imagePullspec := ""
	waitForPoolToReachState(t, cs, testOpts.poolName, func(mcp *mcfgv1.MachineConfigPool) bool {
		lps := ctrlcommon.NewLayeredPoolState(mcp)
		if lps.HasOSImage() && lps.IsBuildSuccess() {
			imagePullspec = lps.GetOSImage()
			return true
		}

		if lps.IsBuildFailure() {
			require.NoError(t, writeBuildArtifactsToFiles(t, cs, pool))
			t.Fatalf("Build unexpectedly failed.")
		}

		return false
	})

	t.Logf("MachineConfigPool %q has finished building. Got image: %s", testOpts.poolName, imagePullspec)

	return imagePullspec
}

// Adds the layeringEnabled label to the target MachineConfigPool and registers
// / returns a function to unlabel it.
func optPoolIntoLayering(t *testing.T, cs *framework.ClientSet, pool string) func() {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		mcp, err := cs.MachineconfigurationV1Interface.MachineConfigPools().Get(context.TODO(), pool, metav1.GetOptions{})
		require.NoError(t, err)

		if mcp.Labels == nil {
			mcp.Labels = map[string]string{}
		}

		mcp.Labels[ctrlcommon.LayeringEnabledPoolLabel] = ""

		_, err = cs.MachineconfigurationV1Interface.MachineConfigPools().Update(context.TODO(), mcp, metav1.UpdateOptions{})
		if err == nil {
			t.Logf("Added label %q to MachineConfigPool %s to opt into layering", ctrlcommon.LayeringEnabledPoolLabel, pool)
		}
		return err
	})

	require.NoError(t, err)

	return makeIdempotentAndRegister(t, func() {
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			mcp, err := cs.MachineconfigurationV1Interface.MachineConfigPools().Get(context.TODO(), pool, metav1.GetOptions{})
			require.NoError(t, err)

			delete(mcp.Labels, ctrlcommon.LayeringEnabledPoolLabel)

			_, err = cs.MachineconfigurationV1Interface.MachineConfigPools().Update(context.TODO(), mcp, metav1.UpdateOptions{})
			if err == nil {
				t.Logf("Removed label %q to MachineConfigPool %s to opt out of layering", ctrlcommon.LayeringEnabledPoolLabel, pool)
			}
			return err
		})

		require.NoError(t, err)
	})
}

// Prepares for an on-cluster build test by performing the following:
// - Gets the Docker Builder secret name from the MCO namespace.
// - Creates the imagestream to use for the test.
// - Clones the global pull secret into the MCO namespace.
// - If requrested, clones the RHEL entitlement secret into the MCO namespace.
// - Creates the on-cluster-build-config ConfigMap.
// - Creates the target MachineConfigPool and waits for it to get a rendered config.
// - Creates the on-cluster-build-custom-dockerfile ConfigMap.
//
// Each of the object creation steps registers an idempotent cleanup function
// that will delete the object at the end of the test.
func prepareForTest(t *testing.T, cs *framework.ClientSet, testOpts onClusterBuildTestOpts) {
	pushSecretName, err := getBuilderPushSecretName(cs)
	require.NoError(t, err)

	// If the test requires RHEL entitlements, clone them from
	// "etc-pki-entitlement" in the "openshift-config-managed" namespace.
	// If we want to use RHEL entit
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

	// Creates an imagestream to push the built OS image to. This is so that the
	// test may be self-contained within the test cluster.
	imagestreamName := "os-image"
	t.Cleanup(createImagestream(t, cs, imagestreamName))

	// Default to the custom pod builder image builder type.
	if testOpts.imageBuilderType == "" {
		testOpts.imageBuilderType = build.CustomPodImageBuilder
	}

	// Copy the global pull secret into the MCO namespace.
	t.Cleanup(copyGlobalPullSecret(t, cs))

	// Get the final image pullspec from the imagestream that we just created.
	finalPullspec, err := getImagestreamPullspec(cs, imagestreamName)
	require.NoError(t, err)

	// Set up the on-cluster-build-config ConfigMap.
	cmCleanup := createConfigMap(t, cs, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      build.OnClusterBuildConfigMapName,
			Namespace: ctrlcommon.MCONamespace,
		},
		Data: map[string]string{
			build.BaseImagePullSecretNameConfigKey:  globalPullSecretCloneName,
			build.FinalImagePushSecretNameConfigKey: pushSecretName,
			build.FinalImagePullspecConfigKey:       finalPullspec,
			build.ImageBuilderTypeConfigMapKey:      string(testOpts.imageBuilderType),
		},
	})

	t.Cleanup(cmCleanup)

	// Create the MachineConfigPool that we intend to target for the test.
	t.Cleanup(makeIdempotentAndRegister(t, helpers.CreateMCP(t, cs, testOpts.poolName)))

	// Create the on-cluster-build-custom-dockerfile ConfigMap.
	t.Cleanup(createCustomDockerfileConfigMap(t, cs, testOpts.customDockerfiles))

	// Wait for our targeted MachineConfigPool to get a base MachineConfig.
	_, err = helpers.WaitForRenderedConfig(t, cs, testOpts.poolName, "00-worker")
	require.NoError(t, err)
}

func TestSSHKeyAndPasswordForOSBuilder(t *testing.T) {
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
