package e2e_techpreview_test

import (
	"context"
	"flag"
	"strings"
	"testing"

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
)

const (
	// The MachineConfigPool to create for the tests.
	layeredMCPName string = "layered"

	// The ImageStream name to use for the tests.
	imagestreamName string = "os-image"

	// The name of the global pull secret copy to use for the tests.
	globalPullSecretCloneName string = "global-pull-secret-copy"

	// The custom Dockerfile content to build for the tests.
	cowsayDockerfile string = `FROM quay.io/centos/centos:stream9 AS centos
RUN dnf install -y epel-release
FROM configs AS final
COPY --from=centos /etc/yum.repos.d /etc/yum.repos.d
COPY --from=centos /etc/pki/rpm-gpg/RPM-GPG-KEY-* /etc/pki/rpm-gpg/
RUN sed -i 's/\$stream/9-stream/g' /etc/yum.repos.d/centos*.repo && \
    rpm-ostree install cowsay`
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

	// What node(s) should be targeted for the test.
	targetNodes []*corev1.Node

	// What MachineConfigPool name to use for the test.
	poolName string
}

// Tests tha an on-cluster build can be performed with the Custom Pod Builder.
func TestOnClusterBuildsCustomPodBuilder(t *testing.T) {
	runOnClusterBuildTest(t, onClusterBuildTestOpts{
		imageBuilderType: build.CustomPodImageBuilder,
		poolName:         layeredMCPName,
		customDockerfiles: map[string]string{
			layeredMCPName: cowsayDockerfile,
		},
	})
}

// Tests that an on-cluster build can be performed and that the resulting image
// is rolled out to an opted-in node.
func TestOnClusterBuildRollsOutImage(t *testing.T) {
	imagePullspec := runOnClusterBuildTest(t, onClusterBuildTestOpts{
		imageBuilderType: build.CustomPodImageBuilder,
		poolName:         layeredMCPName,
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
	cs := framework.NewClientSet("")

	t.Logf("Running with ImageBuilder type: %s", testOpts.imageBuilderType)

	mosc := prepareForTest(t, cs, testOpts)

	t.Cleanup(createMachineOSConfig(t, cs, mosc))

	t.Logf("Wait for build to start")
	waitForMachineOSBuildToReachState(t, cs, testOpts.poolName, func(mosb *mcfgv1alpha1.MachineOSBuild, err error) (bool, error) {
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

	t.Logf("Build started! Waiting for completion...")

	var build *mcfgv1alpha1.MachineOSBuild
	waitForMachineOSBuildToReachState(t, cs, testOpts.poolName, func(mosb *mcfgv1alpha1.MachineOSBuild, err error) (bool, error) {
		if err != nil {
			return false, err
		}

		build = mosb

		state := ctrlcommon.NewMachineOSBuildState(mosb)

		if state.IsBuildFailure() {
			t.Logf("MachineOSBuild %q unexpectedly failed", mosb.Name)
			t.Fail()
		}

		return state.IsBuildSuccess(), nil
	})

	t.Logf("MachineOSBuild %q has finished building. Got image: %s", build.Name, build.Status.FinalImagePushspec)

	return build.Status.FinalImagePushspec
}

// Prepares for an on-cluster build test by performing the following:
// - Gets the Docker Builder secret name from the MCO namespace.
// - Creates the imagestream to use for the test.
// - Clones the global pull secret into the MCO namespace.
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

	t.Cleanup(makeIdempotentAndRegister(t, helpers.CreateMCP(t, cs, testOpts.poolName)))

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
					// TODO: Can this use the global pull secret? Or should this use the
					// push secret name for now?
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
