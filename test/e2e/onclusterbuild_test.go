package e2e

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"

	"github.com/containers/image/v5/pkg/docker/config"
	imageTypes "github.com/containers/image/v5/types"

	imagev1 "github.com/openshift/api/image/v1"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
)

const (
	layeringEnabledPoolLabel string = "machineconfiguration.openshift.io/layering-enabled"
	osImagePoolAnnotation    string = "machineconfiguration.openshift.io/newestImageEquivalentConfig"
	workerPoolName           string = "worker"
	layeredPoolName          string = "layered"
	layeredOSImageURLMCName  string = layeredPoolName + "-layered-custom-os-image"
)

func setupOnClusterBuildConfig(t *testing.T, cs *framework.ClientSet) string {
	finalImagePullspec := createImageStream(t, cs, &imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ctrlcommon.MCONamespace,
			Name:      "os-image",
		},
		Spec: imagev1.ImageStreamSpec{
			LookupPolicy: imagev1.ImageLookupPolicy{
				Local: false,
			},
		},
	})

	clonedSecretName := "global-pull-secret-copy"
	cloneSecret(t, cs,
		&corev1.ObjectReference{
			Namespace: "openshift-config",
			Name:      "pull-secret",
		},
		&corev1.ObjectReference{
			Namespace: ctrlcommon.MCONamespace,
			Name:      clonedSecretName,
		},
	)

	secrets, err := cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)

	builderSecretName := ""
	for _, secret := range secrets.Items {
		if strings.HasPrefix(secret.Name, "builder-dockercfg") {
			builderSecretName = secret.Name
			break
		}
	}

	onClusterBuildConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "on-cluster-build-config",
			Namespace: ctrlcommon.MCONamespace,
		},
		Data: map[string]string{
			"baseImagePullSecretName":  clonedSecretName,
			"finalImagePullspec":       finalImagePullspec,
			"finalImagePushSecretName": builderSecretName,
			"imageBuilderType":         "openshift-image-builder",
		},
	}

	createConfigMap(t, cs, onClusterBuildConfigMap)

	return finalImagePullspec
}

func TestOnClusterBuilds(t *testing.T) {
	cs := framework.NewClientSet("")

	canRunTest, err := canRunOnClusterBuildTest(t, cs)
	if !canRunTest {
		t.Skipf("Skipping on-cluster build test: %s", err)
	}

	n, err := cs.CoreV1Interface.Nodes().Get(context.TODO(), "ip-10-0-139-51.ec2.internal", metav1.GetOptions{})
	require.NoError(t, err)

	node := *n

	// Get the initial worker MachineConfigPool state before we begin our test so we have something to roll back to.
	initialWorkerMCP, err := cs.MachineConfigPools().Get(context.TODO(), "worker", metav1.GetOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, helpers.WaitForNodeConfigChange(t, cs, node, initialWorkerMCP.Spec.Configuration.Name))
	})

	t.Cleanup(addDefaultServiceAccountCredsToGlobalPullSecretsToEnableImagePulls(t, cs))

	finalImagePullspec := setupOnClusterBuildConfig(t, cs)

	t.Logf("Opting pool %q into layering", layeredPoolName)
	unlabelFunc := optIntoLayeredPools(t, cs, node, finalImagePullspec)

	// Now let's add a special file to our new layered MCP.
	newLayeredMC := helpers.NewMachineConfig("layered-hello-world", helpers.MCLabelForRole(layeredPoolName), "", []ign3types.File{
		helpers.CreateEncodedIgn3File("/etc/hello-layered-world", "hello layering!", 420),
	})

	fullyQualifiedImagePullspec := rollOutNewLayeredMachineConfig(t, cs, layeredPoolName, newLayeredMC)

	// Determine that we've booted into the newest OS image.
	assertRPMOSTreeBootedIntoContainer(t, cs, node, strings.Split(fullyQualifiedImagePullspec, "@")[1])

	// Assert that this new file is on the node.
	helpers.AssertFileOnNode(t, cs, node, "/etc/hello-layered-world")

	unlabelFunc()

	t.Logf("Waiting for node to roll back to initial state")
	rollbackUsingSSHBastionPod(t, cs, node)
	// createSSHRecoveryPod(t, cs, node, skp)

	osImageURLConfigMap, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Get(context.TODO(), "machine-config-osimageurl", metav1.GetOptions{})
	require.NoError(t, err)

	require.NoError(t, helpers.WaitForNodeConfigChange(t, cs, node, initialWorkerMCP.Spec.Configuration.Name))
	assertRPMOSTreeBootedIntoContainer(t, cs, node, strings.Split(osImageURLConfigMap.Data["baseOSContainerImage"], "@")[1])
}

func rollbackUsingSSHBastionPod(t *testing.T, cs *framework.ClientSet, node corev1.Node) {
	// Because of how things currently work, we need to use an SSH bastion pod
	// to roll the node back to its original state. But first, we should wait for
	// the Kubelet to become unready. This means that the config has been applied and the node has rebooted, but has not reconnected back to the kubelet
	require.NoError(t, wait.PollImmediate(time.Second*2, time.Minute*10, func() (bool, error) {
		n, err := cs.CoreV1Interface.Nodes().Get(context.TODO(), node.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		for _, condition := range n.Status.Conditions {
			if condition.Message == "Kubelet stopped posting node status." {
				return true, nil
			}
		}

		return false, nil
	}))

	t.Logf("Node %s has disconnected from the cluster", node.Name)

	cmd := exec.Command("/Users/zzlotnik/Repos/oc-oneliners/rebootstrap-node.sh", node.Name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Run())
}

func optIntoLayeredPools(t *testing.T, cs *framework.ClientSet, node corev1.Node, finalImagePullspec string) func() {
	t.Logf("Creating MachineConfigPool %q", layeredPoolName)
	t.Cleanup(helpers.CreateMCP(t, cs, layeredPoolName))

	t.Logf("Creating MachineConfig %q", layeredOSImageURLMCName)
	layeredMC := helpers.NewMachineConfig(layeredOSImageURLMCName, helpers.MCLabelForRole(layeredPoolName), finalImagePullspec, []ign3types.File{})
	t.Cleanup(helpers.ApplyMC(t, cs, layeredMC))

	layeredMCP, err := cs.MachineConfigPools().Get(context.TODO(), layeredPoolName, metav1.GetOptions{})
	require.NoError(t, err)

	// Opt the pool into on-cluster builds
	layeredMCP.Labels[layeringEnabledPoolLabel] = ""
	layeredMCP, err = cs.MachineConfigPools().Update(context.TODO(), layeredMCP, metav1.UpdateOptions{})

	fullyQualifiedImagePullspec := waitForBuildPodCompleteToUpdateOverrideMC(t, cs, layeredPoolName)

	// Label our target node to roll out the new layered image.
	nodeRoleLabel := fmt.Sprintf("node-role.kubernetes.io/%s", layeredPoolName)
	t.Logf("Adding label %q to node %s", nodeRoleLabel, node.Name)

	// Apply the label so the node picks up the layered image.
	unlabelFunc := helpers.MakeIdempotent(helpers.LabelNode(t, cs, node, nodeRoleLabel))
	t.Cleanup(unlabelFunc)

	layeredMCP, err = cs.MachineConfigPools().Get(context.TODO(), layeredPoolName, metav1.GetOptions{})
	require.NoError(t, err)

	t.Logf("Waiting for node %s to change config", node.Name)

	require.NoError(t, helpers.WaitForNodeConfigChange(t, cs, node, layeredMCP.Spec.Configuration.Name))

	// Determine that we've booted into the new OS image.
	assertRPMOSTreeBootedIntoContainer(t, cs, node, strings.Split(fullyQualifiedImagePullspec, "@")[1])

	return helpers.MakeIdempotent(func() {
		t.Logf("Removing label %q to roll node %s back to initial config", nodeRoleLabel, node.Name)
		unlabelFunc()
	})
}

func setPauseForMachineConfigPool(t *testing.T, cs *framework.ClientSet, poolName string, paused bool) func() {
	undoFunc := helpers.MakeIdempotent(func() {
		if paused {
			paused = false
		} else {
			paused = true
		}

		setPauseForMachineConfigPool(t, cs, poolName, paused)
	})

	require.NoError(t, retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		pool, err := cs.MachineConfigPools().Get(context.TODO(), poolName, metav1.GetOptions{})
		if err != nil {
			return err
		}

		if pool.Spec.Paused == paused {
			t.Logf("MachineConfigPool %s .Spec.Paused already set to %v, no-op", poolName, paused)
			return nil
		}

		pool.Spec.Paused = paused

		_, err = cs.MachineConfigPools().Update(context.TODO(), pool, metav1.UpdateOptions{})
		return err
	}))

	t.Logf("Set MachineConfigPool %s .Spec.Paused to %v", poolName, paused)
	return undoFunc
}

func rollOutNewLayeredMachineConfig(t *testing.T, cs *framework.ClientSet, mcpName string, mc *mcfgv1.MachineConfig) string {
	unpause := setPauseForMachineConfigPool(t, cs, mcpName, true)

	t.Logf("Creating MachineConfig %s", mc.Name)

	t.Cleanup(helpers.ApplyMC(t, cs, mc))

	t.Logf("Waiting for rendered config to be generated")

	// Wait for the MachineConfigPool to pick up our new config.
	_, err := helpers.WaitForRenderedConfig(t, cs, mcpName, mc.Name)
	require.NoError(t, err)

	// Now we have to wait for the new image to be built.
	t.Logf("Waiting for on-cluster build to complete for MachineConfigPool %s", mcpName)
	fullyQualifiedImagePullspec := waitForBuildPodCompleteToUpdateOverrideMC(t, cs, mcpName)

	// Unpause our pool to allow our new MachineConfig to roll out.
	unpause()

	// Wait for the node to finish updating.
	require.NoError(t, helpers.WaitForPoolComplete(t, cs, mcpName, helpers.GetMcName(t, cs, mcpName)))

	return fullyQualifiedImagePullspec
}

func updateOverrideMachineConfig(t *testing.T, cs *framework.ClientSet, fullyQualifiedImagePullspec string) {
	// Set the osImageURL override MachineConfig to point to the new image
	// pullspec. Right now, we must do this because the MCD has no concept of a
	// layering-enabled pool nor how to roll out an image for a layering-enabled
	// pool.
	osImageOverrideMCP, err := cs.MachineConfigs().Get(context.TODO(), layeredOSImageURLMCName, metav1.GetOptions{})
	require.NoError(t, err)
	osImageOverrideMCP.Spec.OSImageURL = fullyQualifiedImagePullspec
	_, err = cs.MachineConfigs().Update(context.TODO(), osImageOverrideMCP, metav1.UpdateOptions{})
	require.NoError(t, err)
	t.Logf("Updated osImageURL override MachineConfig %q to point to %q", layeredOSImageURLMCName, fullyQualifiedImagePullspec)
}

func waitForBuildPodCompleteToUpdateOverrideMC(t *testing.T, cs *framework.ClientSet, mcpName string) string {
	mcp, err := cs.MachineConfigPools().Get(context.TODO(), mcpName, metav1.GetOptions{})
	require.NoError(t, err)
	t.Logf("Waiting for on-cluster build to complete for MachineConfigPool %s, config %s", mcpName, mcp.Spec.Configuration.Name)

	fullyQualifiedImagePullspec := waitForBuildPodComplete(t, cs, mcpName)

	updateOverrideMachineConfig(t, cs, fullyQualifiedImagePullspec)

	// Unfortunately, the above MachineConfig change triggers another build that
	// we have to wait for even though the image produced from this MachineConfig
	// change is not the image we're planning to roll out. Right now, we have to
	// wait for this build because there is the possibility that it could get
	// scheduled on the node we're targeting for the update. If we interrupt the
	// build, the MachineConfigPool gets degraded with BuildFailed and right now,
	// we don't have a good way to undo that state right now.
	t.Logf("Waiting for override MachineConfig build to occur. In the future, this will not be necessary...")
	waitForBuildPodComplete(t, cs, mcpName)

	return fullyQualifiedImagePullspec
}

func waitForBuildPodComplete(t *testing.T, cs *framework.ClientSet, mcpName string) string {
	// Wait for the machine-os-builder pod to start.
	err := wait.PollImmediate(time.Second, time.Minute, func() (bool, error) {
		mobPods, err := cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{
			LabelSelector: "k8s-app=machine-os-builder",
		})
		if err != nil {
			return false, err
		}

		if len(mobPods.Items) != 0 && mobPods.Items[0].Status.Phase == corev1.PodRunning {
			t.Logf("machine-os-builder (%s) pod running", mobPods.Items[0].Name)
			return true, nil
		}

		return false, nil
	})

	require.NoError(t, err)

	builderPodName := ""
	pollForCustomBuildPod := func() (bool, error) {
		builderPods, err := cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{
			LabelSelector: "machineconfiguration.openshift.io/buildPod=",
		})

		if err != nil {
			return false, err
		}

		if len(builderPods.Items) != 0 {
			builderPodName = builderPods.Items[0].Name
			t.Logf("Custom Pod Builder (%s) pod running", builderPodName)
			return true, nil
		}

		return false, nil
	}

	pollForImageBuildPod := func() (bool, error) {
		builds, err := cs.BuildV1Interface.Builds(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{
			LabelSelector: "machineconfiguration.openshift.io/buildPod=",
		})

		if err != nil {
			return false, err
		}

		if len(builds.Items) != 0 {
			builderPodName = builds.Items[0].Annotations["openshift.io/build.pod-name"]
			t.Logf("OpenShift Image Builder (%s) pod running", builderPodName)
			return true, nil
		}

		return false, nil
	}

	// Wait for the builder pod to start.
	err = wait.PollImmediate(time.Second, time.Minute, func() (bool, error) {
		buildPodRunning, err := pollForCustomBuildPod()
		if err != nil {
			return false, err
		}

		imageBuildRunning, err := pollForImageBuildPod()
		if err != nil {
			return false, err
		}

		return buildPodRunning || imageBuildRunning, nil
	})
	require.NoError(t, err)

	layeredMCP, err := cs.MachineConfigPools().Get(context.TODO(), mcpName, metav1.GetOptions{})
	require.NoError(t, err)

	initialImagePullspec, ok := layeredMCP.Annotations[osImagePoolAnnotation]
	if !ok {
		initialImagePullspec = ""
	}

	// Wait for the image pullspec to change.
	currentImagePullspec := ""
	err = wait.PollImmediate(time.Second*2, time.Minute*10, func() (bool, error) {
		mcp, err := cs.MachineConfigPools().Get(context.TODO(), mcpName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		val, ok := mcp.Annotations[osImagePoolAnnotation]
		// If we don't have an initial image pullspec, wait until we do.
		if initialImagePullspec == "" {
			if ok && val != "" {
				currentImagePullspec = val
				return true, nil
			}
		}

		// If we have an initial image pullspec, wait until the current one is
		// different from the initial one.
		if initialImagePullspec != "" {
			if ok && val != initialImagePullspec {
				currentImagePullspec = val
				return true, nil
			}
		}

		for _, condition := range mcp.Status.Conditions {
			if condition.Type == "BuildFailed" && condition.Status == "True" {
				cmd := exec.Command("oc", "logs", "pod/"+builderPodName, "-n", ctrlcommon.MCONamespace)
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				require.NoError(t, cmd.Run())
				return false, fmt.Errorf("build failure")
			}
		}

		return false, nil
	})
	require.NoError(t, err)

	if initialImagePullspec == "" {
		t.Logf("MachineConfigPool %s image pullspec now set to %q", mcpName, currentImagePullspec)
	} else {
		t.Logf("Image pullspec changed from %q to %q on MachineConfigPool %s", initialImagePullspec, currentImagePullspec, mcpName)
	}

	return currentImagePullspec
}

func assertRPMOSTreeNotBootedIntoContainer(t *testing.T, cs *framework.ClientSet, node corev1.Node, unexpectedImagePullspec string) {
	status := helpers.ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "rpm-ostree", "status")
	assert.NotContains(t, status, unexpectedImagePullspec)
}

func assertRPMOSTreeBootedIntoContainer(t *testing.T, cs *framework.ClientSet, node corev1.Node, expectedImagePullspec string) {
	status := helpers.ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "rpm-ostree", "status")
	assert.Contains(t, status, expectedImagePullspec)
}

func tagImageWithSkopeo(t *testing.T, cs *framework.ClientSet, fullyQualifiedImagePullspec, taggedImagePullspec string) {
	t.Logf("Tagging fully-qualified image pullspec %q to %q with Skopeo", fullyQualifiedImagePullspec, taggedImagePullspec)
	onClusterBuildConfigMap, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Get(context.TODO(), "on-cluster-build-config", metav1.GetOptions{})
	require.NoError(t, err)

	finalImagePushSecret, err := cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).Get(context.TODO(), onClusterBuildConfigMap.Data["finalImagePushSecretName"], metav1.GetOptions{})
	require.NoError(t, err)

	tmpDir := t.TempDir()
	pullSecretPath := filepath.Join(tmpDir, "config.json")
	defer os.RemoveAll(pullSecretPath)

	require.NoError(t, ioutil.WriteFile(pullSecretPath, []byte(finalImagePushSecret.Data[".dockerconfigjson"]), 0644))
	args := []string{
		"copy",
		fmt.Sprintf("--src-authfile=%s", pullSecretPath),
		fmt.Sprintf("--dest-authfile=%s", pullSecretPath),
		fmt.Sprintf("docker://%s", fullyQualifiedImagePullspec),
		fmt.Sprintf("docker://%s", taggedImagePullspec),
	}

	cmd := exec.Command("skopeo", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	t.Logf("$ %s", cmd)
	require.NoError(t, cmd.Run())
}

func canRunOnClusterBuildTest(t *testing.T, cs *framework.ClientSet) (bool, error) {
	_, err := cs.AppsV1Interface.Deployments(ctrlcommon.MCONamespace).Get(context.TODO(), "machine-os-builder", metav1.GetOptions{})
	if err != nil {
		return false, err
	}

	return true, nil
}

// There is probably a better way to do this using native Golang libraries. But
// for the sake of time and simplicity, I'll just shell out to ssh-keygen.
type sshKeyPair struct {
	public  string
	private string
}

func generateSSHKeyPair(t *testing.T, cs *framework.ClientSet, node corev1.Node) *sshKeyPair {
	_, err := exec.LookPath("ssh-keygen")
	if err == nil {
		skp, genErr := generateSSHKeyPairLocally(t.TempDir())
		require.NoError(t, genErr)
		return skp
	}

	return generateSSHKeyPairOnNode(t, cs, node)
}

func generateSSHKeyPairOnNode(t *testing.T, cs *framework.ClientSet, node corev1.Node) *sshKeyPair {
	t.Logf("Generating ephemeral public / private SSH keypair for test, using %s", node.Name)

	tempDir := strings.TrimSpace(helpers.ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "mktemp", "-d"))
	defer func() {
		helpers.ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "rm", "-rf", tempDir)
	}()

	keyfile := filepath.Join(tempDir, "keyfile")

	helpers.ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "ssh-keygen", "-t", "ed25519", "-f", keyfile, "-q", "-N", "", "-C", "MCO e2e")
	skp := &sshKeyPair{
		private: helpers.ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "cat", keyfile),
		public:  helpers.ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "ssh-keygen", "-f", keyfile, "-y"),
	}

	return skp
}

func generateSSHKeyPairLocally(dir string) (*sshKeyPair, error) {
	keyPath := filepath.Join(dir, "keyfile")
	defer os.RemoveAll(dir)

	// Write the private key to disk for now.
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-f", keyPath, "-q", "-N", "", "-C", "MCO e2e")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, err
	}

	// Get the public key from the private key by outputting it to stdout.
	cmd = exec.Command("ssh-keygen", "-f", keyPath, "-y")
	buf := bytes.NewBuffer([]byte{})
	cmd.Stdout = buf
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return nil, err
	}

	// Read the private key file
	privateKey, err := ioutil.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}

	return &sshKeyPair{
		public:  buf.String(),
		private: string(privateKey),
	}, nil
}

func generateSSHObjects(skp *sshKeyPair) (*mcfgv1.MachineConfig, *corev1.Secret) {
	sshIgnConfig := ctrlcommon.NewIgnConfig()
	sshIgnConfig.Passwd.Users = []ign3types.PasswdUser{
		{
			Name:              "core",
			SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{ign3types.SSHAuthorizedKey(skp.public)},
		},
	}

	mc := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "ssh-public-recovery-key",
			Labels: helpers.MCLabelForRole("worker"),
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: helpers.MarshalOrDie(sshIgnConfig),
			},
		},
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ssh-private-recovery-key",
			Namespace: ctrlcommon.MCONamespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"privatekey": []byte(skp.private),
		},
	}

	return mc, secret
}

//go:embed ssh_recovery_script.sh
var sshRecoveryScript string

//go:embed recovery_pod_entrypoint.sh
var recoveryPodEntrypoint string

func createSSHRecoveryPod(t *testing.T, cs *framework.ClientSet, node corev1.Node, skp *sshKeyPair) {
	// Because of how things currently work, we need to create an SSH bastion pod
	// to roll the node back to its original state. But first, we should wait for
	// the Kubelet to become unready.
	require.NoError(t, wait.PollImmediate(time.Second*2, time.Minute*10, func() (bool, error) {
		n, err := cs.CoreV1Interface.Nodes().Get(context.TODO(), node.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		for _, condition := range n.Status.Conditions {
			if condition.Message == "Kubelet stopped posting node status." {
				return true, nil
			}
		}

		return false, nil
	}))

	mcoImages, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Get(context.TODO(), "machine-config-operator-images", metav1.GetOptions{})
	require.NoError(t, err)

	out := map[string]string{}
	require.NoError(t, json.Unmarshal([]byte(mcoImages.Data["images.json"]), &out))

	mcoImagePullspec := out["machineConfigOperator"]

	osImages, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Get(context.TODO(), "machine-config-osimageurl", metav1.GetOptions{})
	require.NoError(t, err)

	// Use this because we know it has an SSH client available.
	osImagePullspec := osImages.Data["baseOSContainerImage"]

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node-recovery-configmap",
			Namespace: ctrlcommon.MCONamespace,
		},
		Data: map[string]string{
			"recover.sh": sshRecoveryScript,
			"config": `Host *
	StrictHostKeyChecking no
  IdentityFile ~/.ssh/id_ed25519`,
		},
	}

	_, privateSSHSecret := generateSSHObjects(skp)

	var configMapMode int32 = 0500
	var secretMode int32 = 384

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node-recovery-pod",
			Namespace: ctrlcommon.MCONamespace,
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:  "ssh-recovery",
					Image: osImagePullspec,
					Env: []corev1.EnvVar{
						{
							Name:  "MCO_IMAGE_PULLSPEC",
							Value: mcoImagePullspec,
						},
						{
							Name:  "TARGET_HOST",
							Value: node.Name,
						},
					},
					Command: []string{
						"/bin/bash",
						"-c",
						recoveryPodEntrypoint,
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "ssh-private-key",
							MountPath: "/tmp/key",
						},
						{
							Name:      "scripts",
							MountPath: "/tmp/scripts",
						},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "ssh-private-key",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							DefaultMode: &secretMode,
							SecretName:  privateSSHSecret.Name,
							Items: []corev1.KeyToPath{
								{
									Key:  "privatekey",
									Path: "id_ed25519",
								},
							},
						},
					},
				},
				{
					Name: "scripts",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							DefaultMode: &configMapMode,
							LocalObjectReference: corev1.LocalObjectReference{
								Name: configMap.Name,
							},
						},
					},
				},
			},
		},
	}

	createConfigMap(t, cs, configMap)
	createSecret(t, cs, privateSSHSecret)

	p, err := cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace).Create(context.TODO(), pod, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Targetting node %s", node.Name)

	t.Logf("Pod scheduled on %s", p.Spec.NodeName)

	t.Cleanup(func() {
		require.NoError(t, ignoreNotFound(cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace).Delete(context.TODO(), pod.Name, metav1.DeleteOptions{})))
		t.Logf("Pod %q deleted", pod.Name)
	})
}

func ignoreExists(err error) error {
	if k8serrors.IsAlreadyExists(err) {
		return nil
	}

	return err
}

func ignoreNotFound(err error) error {
	if k8serrors.IsNotFound(err) {
		return nil
	}

	return err
}

func addDefaultServiceAccountCredsToGlobalPullSecretsToEnableImagePulls(t *testing.T, cs *framework.ClientSet) func() {
	mcoSecrets, err := cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)

	var secret *corev1.Secret = nil
	for _, mcoSecret := range mcoSecrets.Items {
		if strings.Contains(mcoSecret.Name, "default-dockercfg") {
			mcoSecret := mcoSecret
			secret = &mcoSecret
			break
		}
	}

	global, err := cs.CoreV1Interface.Secrets("openshift-config").Get(context.TODO(), "pull-secret", metav1.GetOptions{})
	require.NoError(t, err)

	originalGlobal := global.DeepCopy()

	globalCfg, err := parseK8sPullSecretIntoDockerAuthConfig(global)
	require.NoError(t, err)

	mcoCfg, err := parseK8sPullSecretIntoDockerAuthConfig(secret)
	require.NoError(t, err)

	for key, val := range mcoCfg {
		globalCfg[key] = val
	}

	tmpDir := t.TempDir()

	sysCtx := &imageTypes.SystemContext{
		AuthFilePath: filepath.Join(tmpDir, "final-config.json"),
	}
	defer os.RemoveAll(tmpDir)

	for key, val := range globalCfg {
		_, err := config.SetCredentials(sysCtx, key, val.Username, val.Password)
		require.NoError(t, err)
	}

	final, err := os.ReadFile(sysCtx.AuthFilePath)
	require.NoError(t, err)

	global.Data[corev1.DockerConfigJsonKey] = final
	_, err = cs.CoreV1Interface.Secrets("openshift-config").Update(context.TODO(), global, metav1.UpdateOptions{})
	require.NoError(t, err)

	t.Logf("Added pull secret for service account %q to openshift-config/pull-secret for the duration of the test", secret.Name)

	undoFunc := func() {
		require.NoError(t, retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			global, err := cs.CoreV1Interface.Secrets("openshift-config").Get(context.TODO(), "pull-secret", metav1.GetOptions{})
			if err != nil {
				return err
			}

			global.Data = originalGlobal.Data
			_, err = cs.CoreV1Interface.Secrets("openshift-config").Update(context.TODO(), global, metav1.UpdateOptions{})
			return err
		}))

		t.Logf("Removed pull secret for service account %q from openshift-config/pull-secret", secret.Name)
	}

	return helpers.MakeIdempotent(undoFunc)
}

func parseK8sPullSecretIntoDockerAuthConfig(secret *corev1.Secret) (map[string]imageTypes.DockerAuthConfig, error) {
	tmpDir, err := os.MkdirTemp("", "mco.e2e")
	if err != nil {
		return nil, err
	}

	defer os.RemoveAll(tmpDir)

	secretPath := filepath.Join(tmpDir, "pull-secret.json")

	sysCtx := imageTypes.SystemContext{}

	secretKey := ""
	if secret.Type == corev1.SecretTypeDockerConfigJson {
		secretKey = string(corev1.DockerConfigJsonKey)
		sysCtx.AuthFilePath = secretPath
	} else if secret.Type == corev1.SecretTypeDockercfg {
		secretKey = string(corev1.DockerConfigKey)
		sysCtx.LegacyFormatAuthFilePath = secretPath
	} else {
		return nil, fmt.Errorf("unsupported secret type %s", secret.Type)
	}

	pullSecretBytes, ok := secret.Data[secretKey]
	if !ok {
		return nil, fmt.Errorf("could not find key %s", secretKey)
	}

	if err := os.WriteFile(secretPath, pullSecretBytes, 0644); err != nil {
		return nil, err
	}

	cfg, err := config.GetAllCredentials(&sysCtx)
	if err != nil {
		return nil, err
	}

	return cfg, nil
}

func createImageStream(t *testing.T, cs *framework.ClientSet, is *imagev1.ImageStream) string {
	updated, err := cs.ImageV1Interface.ImageStreams(is.Namespace).Create(context.TODO(), is, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Created ImageStream %q, has pullspec %q", is.Name, updated.Status.DockerImageRepository)

	t.Cleanup(func() {
		require.NoError(t, cs.ImageV1Interface.ImageStreams(is.Namespace).Delete(context.TODO(), is.Name, metav1.DeleteOptions{}))
		t.Logf("Deleted ImageStream %q", is.Name)
	})

	return updated.Status.DockerImageRepository
}

func createConfigMap(t *testing.T, cs *framework.ClientSet, cm *corev1.ConfigMap) {
	_, err := cs.CoreV1Interface.ConfigMaps(cm.Namespace).Create(context.TODO(), cm, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Created ConfigMap %q", cm.Name)

	t.Cleanup(func() {
		require.NoError(t, cs.CoreV1Interface.ConfigMaps(cm.Namespace).Delete(context.TODO(), cm.Name, metav1.DeleteOptions{}))
		t.Logf("Deleted ConfigMap %q", cm.Name)

	})
}

func createSecret(t *testing.T, cs *framework.ClientSet, secret *corev1.Secret) {
	_, err := cs.CoreV1Interface.Secrets(secret.Namespace).Create(context.TODO(), secret, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Logf("Created Secret %q", secret.Name)

	t.Cleanup(func() {
		require.NoError(t, cs.CoreV1Interface.Secrets(secret.Namespace).Delete(context.TODO(), secret.Name, metav1.DeleteOptions{}))
		t.Logf("Deleted Secret %q", secret.Name)
	})
}

func cloneSecret(t *testing.T, cs *framework.ClientSet, src, dst *corev1.ObjectReference) {
	srcSecret, err := cs.CoreV1Interface.Secrets(src.Namespace).Get(context.TODO(), src.Name, metav1.GetOptions{})
	require.NoError(t, err)

	createSecret(t, cs, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dst.Name,
			Namespace: dst.Namespace,
		},
		Type: srcSecret.Type,
		Data: srcSecret.Data,
	})
}
