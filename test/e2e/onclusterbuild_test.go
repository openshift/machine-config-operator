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

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
)

const (
	layeringEnabledPoolLabel string = "machineconfiguration.openshift.io/layering-enabled"
	osImagePoolAnnotation    string = "machineconfiguration.openshift.io/newestImageEquivalentConfig"
)

func TestOnClusterBuilds(t *testing.T) {
	cs := framework.NewClientSet("")

	canRunTest, err := canRunOnClusterBuildTest(t, cs)
	if !canRunTest {
		t.Skipf("Skipping on-cluster build test: %s", err)
	}

	t.Logf("Generating ephemeral public / private SSH keypair for test. These will be removed after the test is complete.")
	skp, err := generateSSHKeyPair(t.TempDir())
	require.NoError(t, err)

	// Generate the SSH MachineConfig and k8s secret
	publicSSHMC, _ := generateSSHObjects(skp)

	initialWorkerMCP, err := cs.MachineConfigPools().Get(context.TODO(), "worker", metav1.GetOptions{})
	require.NoError(t, err)

	node := helpers.GetRandomNode(t, cs, "worker")

	// Apply the SSH MachineConfig.
	sshUndoFunc := helpers.MakeIdempotent(helpers.ApplyMC(t, cs, publicSSHMC))
	t.Cleanup(func() {
		sshUndoFunc()
		require.NoError(t, helpers.WaitForNodeConfigChange(t, cs, node, initialWorkerMCP.Spec.Configuration.Name))
	})

	t.Logf("Waiting for SSH key MC to roll out to worker pool")
	helpers.WaitForConfigAndPoolComplete(t, cs, "worker", publicSSHMC.Name)

	taggedOSImageURL := "quay.io/zzlotnik/testing:on-cluster-build"

	layeredPoolName := "layered"
	layeredOSImageURLMCName := fmt.Sprintf("%s-custom-os-image", layeredPoolName)

	t.Logf("Creating MachineConfigPool %q", layeredPoolName)
	t.Cleanup(helpers.CreateMCP(t, cs, layeredPoolName))

	t.Logf("Creating MachineConfig %q", layeredOSImageURLMCName)
	layeredMC := helpers.NewMachineConfig(layeredOSImageURLMCName, helpers.MCLabelForRole(layeredPoolName), "", []ign3types.File{})
	t.Cleanup(helpers.ApplyMC(t, cs, layeredMC))

	layeredMCP, err := cs.MachineConfigPools().Get(context.TODO(), layeredPoolName, metav1.GetOptions{})
	require.NoError(t, err)

	// Opt the pool into on-cluster builds
	layeredMCP.Labels[layeringEnabledPoolLabel] = ""
	layeredMCP, err = cs.MachineConfigPools().Update(context.TODO(), layeredMCP, metav1.UpdateOptions{})

	imagePullspec := waitForBuildAndTagImage(t, cs, layeredMCP.Name, taggedOSImageURL)

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
	assertRPMOSTreeBootedIntoContainer(t, cs, node, strings.Split(imagePullspec, "@")[1])

	// To apply a new MachineConfig, we must first pause our pool.
	layeredMCP = setPauseForMachineConfigPool(t, cs, layeredPoolName, true)

	// Now let's add a special file to our new layered MCP.
	newLayeredMC := helpers.NewMachineConfig("layered-hello-world", helpers.MCLabelForRole(layeredPoolName), taggedOSImageURL, []ign3types.File{
		helpers.CreateEncodedIgn3File("/etc/hello-layered-world", "hello layering!", 420),
	})

	t.Logf("Creating MachineConfig %s", newLayeredMC.Name)

	t.Cleanup(helpers.ApplyMC(t, cs, newLayeredMC))

	t.Logf("Waiting for rendered config to be generated")

	// Wait for the MachineConfigPool to pick up our new config.
	_, err = helpers.WaitForRenderedConfig(t, cs, layeredPoolName, newLayeredMC.Name)
	require.NoError(t, err)

	// Now we have to wait for the new image to be built.
	imagePullspec = waitForBuildAndTagImage(t, cs, layeredMCP.Name, taggedOSImageURL)

	// Unpause our pool to allow our new MachineConfig to roll out.
	layeredMCP = setPauseForMachineConfigPool(t, cs, layeredPoolName, false)

	// Wait for the node to finish updating.
	require.NoError(t, helpers.WaitForNodeConfigChange(t, cs, node, layeredMCP.Spec.Configuration.Name))

	// Determine that we've booted into the newest OS image.
	assertRPMOSTreeBootedIntoContainer(t, cs, node, strings.Split(imagePullspec, "@")[1])

	// Assert that this new file is on the node.
	helpers.AssertFileOnNode(t, cs, node, "/etc/hello-layered-world")

	workerMCP, err := cs.MachineConfigPools().Get(context.TODO(), "worker", metav1.GetOptions{})
	require.NoError(t, err)

	t.Logf("Removing label %q to roll node %s back to initial config", nodeRoleLabel, node.Name)

	unlabelFunc()

	t.Logf("Waiting for node to roll back to initial state")
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

	createSSHRecoveryPod(t, cs, node, skp)

	osImageURLConfigMap, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Get(context.TODO(), "machine-config-osimageurl", metav1.GetOptions{})
	require.NoError(t, err)

	require.NoError(t, helpers.WaitForNodeConfigChange(t, cs, node, workerMCP.Spec.Configuration.Name))
	assertRPMOSTreeBootedIntoContainer(t, cs, node, strings.Split(osImageURLConfigMap.Data["baseOSContainerImage"], "@")[1])
}

func setPauseForMachineConfigPool(t *testing.T, cs *framework.ClientSet, poolName string, paused bool) *mcfgv1.MachineConfigPool {
	pool, err := cs.MachineConfigPools().Get(context.TODO(), poolName, metav1.GetOptions{})
	require.NoError(t, err)
	pool.Spec.Paused = paused

	pool, err = cs.MachineConfigPools().Update(context.TODO(), pool, metav1.UpdateOptions{})
	require.NoError(t, err)

	t.Logf("Set MachineConfigPool %s .Spec.Paused to %v", poolName, paused)

	return pool
}

func waitForBuildAndTagImage(t *testing.T, cs *framework.ClientSet, mcpName, taggedImagePullspec string) string {
	t.Logf("Waiting for on-cluster build to complete for MachineConfigPool %s", mcpName)
	fullyQualifiedImagePullspec := waitForBuildPodAndGetImagePullspec(t, cs, mcpName)
	t.Logf("Image build complete, has fully-qualified image pullspec: %q", fullyQualifiedImagePullspec)
	tagImageWithSkopeo(t, cs, fullyQualifiedImagePullspec, taggedImagePullspec)
	return fullyQualifiedImagePullspec
}

func waitForBuildPodAndGetImagePullspec(t *testing.T, cs *framework.ClientSet, mcpName string) string {
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

	t.Logf("Image pullspec changed from %q to %q on MachineConfigPool %s", initialImagePullspec, currentImagePullspec, mcpName)

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
	requiredCommands := []string{
		"oc",
		"skopeo",
		"ssh-keygen",
	}

	for _, cmd := range requiredCommands {
		if _, err := exec.LookPath(cmd); err != nil {
			return false, fmt.Errorf("could not locate required command %q: %w", cmd, err)
		}

		t.Logf("Required binary %q found", cmd)
	}

	ctx := context.Background()

	_, err := cs.AppsV1Interface.Deployments(ctrlcommon.MCONamespace).Get(ctx, "machine-os-builder", metav1.GetOptions{})
	if err != nil {
		return false, err
	}

	onClusterBuildConfigMap, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, "on-cluster-build-config", metav1.GetOptions{})
	if err != nil {
		return false, err
	}

	finalImagePushSecretKey := "finalImagePushSecretName"

	val, ok := onClusterBuildConfigMap.Data[finalImagePushSecretKey]
	if !ok {
		return false, fmt.Errorf("required on-cluster-build-config key %q missing", finalImagePushSecretKey)
	}

	if val == "" {
		return false, fmt.Errorf("required on-cluster-build-config key %q is empty", finalImagePushSecretKey)
	}

	secretName := onClusterBuildConfigMap.Data[finalImagePushSecretKey]
	_, err = cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("could not get secret %q: %w", finalImagePushSecretKey, err)
	}

	secretName = onClusterBuildConfigMap.Data["baseImagePullSecretName"]
	return maybeCloneGlobalPullSecret(ctx, t, cs, secretName)
}

func maybeCloneGlobalPullSecret(ctx context.Context, t *testing.T, cs *framework.ClientSet, secretName string) (bool, error) {
	if secretName != "" {
		secret, err := cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).Get(ctx, secretName, metav1.GetOptions{})
		if err == nil {
			t.Logf("Using preexisting secret %q as the base image pull secret", secret.Name)
			return true, nil
		}

		if !k8serrors.IsNotFound(err) {
			return false, fmt.Errorf("could not get %q: %w", secretName, err)
		}
	}

	// At this point, we've determined that our clone of the global pull secret does not exist and we'll need to clone it.
	t.Logf("Cloning global pull secret (openshift-config/pull-secret) to openshift-machine-config-operator/global-pull-secret-copy")

	globalPullSecret, err := cs.CoreV1Interface.Secrets("openshift-config").Get(ctx, "pull-secret", metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("could not get openshift-config/pull-secret: %w", err)
	}

	cloned := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "global-pull-secret-copy",
			Namespace: ctrlcommon.MCONamespace,
			Labels: map[string]string{
				"on-cluster-build-test": "",
			},
		},
		Data: globalPullSecret.Data,
		Type: globalPullSecret.Type,
	}

	_, err = cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).Create(ctx, cloned, metav1.CreateOptions{})
	if err != nil {
		return false, fmt.Errorf("could not create %q: %w", cloned.Name, err)
	}

	onClusterBuildConfigMap, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, "on-cluster-build-config", metav1.GetOptions{})
	if err != nil {
		return false, err
	}

	t.Logf("Updated on-cluster-build-config to use the cloned global pull secret")
	onClusterBuildConfigMap.Data["baseImagePullSecretName"] = cloned.Name
	_, err = cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Update(ctx, onClusterBuildConfigMap, metav1.UpdateOptions{})
	return err == nil, err
}

// There is probably a better way to do this using native Golang libraries. But
// for the sake of time and simplicity, I'll just shell out to ssh-keygen.
type sshKeyPair struct {
	public  string
	private string
}

func generateSSHKeyPair(dir string) (*sshKeyPair, error) {
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

	ignoreExists := func(err error) error {
		if k8serrors.IsAlreadyExists(err) {
			return nil
		}

		return err
	}

	_, err = cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Create(context.TODO(), configMap, metav1.CreateOptions{})
	require.NoError(t, ignoreExists(err))

	_, err = cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).Create(context.TODO(), privateSSHSecret, metav1.CreateOptions{})
	require.NoError(t, ignoreExists(err))

	p, err := cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace).Create(context.TODO(), pod, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Targetting node %s", node.Name)

	t.Logf("Pod scheduled on %s", p.Spec.NodeName)
}
