package e2e_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	e2eShared "github.com/openshift/machine-config-operator/test/e2e-shared-tests"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
)

// Test case for https://github.com/openshift/machine-config-operator/issues/358
func TestMCDToken(t *testing.T) {
	t.Parallel()
	cs := framework.NewClientSet("")

	listOptions := metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{"k8s-app": "machine-config-daemon"}).String(),
	}

	mcdList, err := cs.Pods(ctrlcommon.MCONamespace).List(context.TODO(), listOptions)
	require.Nil(t, err)

	for _, pod := range mcdList.Items {
		res, err := cs.Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
			Container: "machine-config-daemon",
		}).DoRaw(context.TODO())
		require.Nil(t, err)
		for _, line := range strings.Split(string(res), "\n") {
			if strings.Contains(line, "Unable to rotate token") {
				t.Fatalf("found token rotation failure message: %s", line)
			}
		}
	}
}

func TestMCDeployed(t *testing.T) {
	cs := framework.NewClientSet("")

	mcp, err := cs.MachineconfigurationV1Interface.MachineConfigPools().List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)

	for _, mcp := range mcp.Items {
		if mcp.Status.MachineCount == 0 {
			continue
		}

		poolName := mcp.Name
		t.Run(poolName, func(t *testing.T) {
			t.Parallel()
			testMCDeployedToPool(t, poolName)
		})
	}
}

func testMCDeployedToPool(t *testing.T, poolName string) {
	cs := framework.NewClientSet("")

	sshKeyContents := "ssh-rsa 123"

	startTime := time.Now()
	mcName := fmt.Sprintf("%s-custom-ssh-key", poolName)
	mcadd := getSSHMachineConfig(mcName, poolName, sshKeyContents)

	initialMCName := helpers.GetMcName(t, cs, poolName)

	undoFunc := helpers.MakeIdempotent(helpers.ApplyMC(t, cs, mcadd))
	t.Cleanup(undoFunc)

	t.Logf("Created %s", mcadd.Name)
	renderedConfig, err := helpers.WaitForRenderedConfig(t, cs, poolName, mcadd.Name)
	require.Nil(t, err)
	err = helpers.WaitForPoolComplete(t, cs, poolName, renderedConfig)
	require.Nil(t, err)
	nodes, err := helpers.GetNodesByRole(cs, poolName)
	require.Nil(t, err)
	for _, node := range nodes {
		assert.Equal(t, renderedConfig, node.Annotations[constants.CurrentMachineConfigAnnotationKey])
		assert.Equal(t, constants.MachineConfigDaemonStateDone, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey])
		osrelease := helpers.GetOSReleaseForNode(t, cs, node)
		sshPaths := helpers.GetSSHPaths(osrelease.OS)
		contents := helpers.ExecCmdOnNode(t, cs, node, "cat", filepath.Join("/rootfs", sshPaths.Expected))
		assert.Contains(t, contents, sshKeyContents)
	}

	t.Logf("All nodes updated with %s (%s elapsed)", mcadd.Name, time.Since(startTime))

	t.Logf("Rolling back nodes to %s", initialMCName)

	undoFunc()

	startTime = time.Now()

	err = helpers.WaitForPoolComplete(t, cs, poolName, initialMCName)
	require.Nil(t, err)
	nodes, err = helpers.GetNodesByRole(cs, poolName)
	require.Nil(t, err)

	for _, node := range nodes {
		assert.Equal(t, initialMCName, node.Annotations[constants.CurrentMachineConfigAnnotationKey])
		assert.Equal(t, constants.MachineConfigDaemonStateDone, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey])
		osrelease := helpers.GetOSReleaseForNode(t, cs, node)
		sshPaths := helpers.GetSSHPaths(osrelease.OS)
		contents := helpers.ExecCmdOnNode(t, cs, node, "cat", filepath.Join("/rootfs", sshPaths.Expected))
		assert.NotContains(t, contents, sshKeyContents)
	}

	t.Logf("All nodes rolled back to %s (%s elapsed)", initialMCName, time.Since(startTime))
}

func TestRunShared(t *testing.T) {
	t.Parallel()

	cs := framework.NewClientSet("")

	mcpName := "test-shared"

	node, releaseFunc, err := nodeLeaser.GetNodeWithReleaseFunc(t)
	require.NoError(t, err)
	t.Cleanup(releaseFunc)

	configOpts := e2eShared.ConfigDriftTestOpts{
		ClientSet: cs,
		MCPName:   mcpName,
		Node:      *node,
		SetupFunc: func(mc *mcfgv1.MachineConfig) {
			t.Cleanup(helpers.CreatePoolAndApplyMCToNode(t, cs, mcpName, *node, []*mcfgv1.MachineConfig{mc}))
		},
		TeardownFunc: func() {},
	}

	sharedOpts := e2eShared.SharedTestOpts{
		ConfigDriftTestOpts: configOpts,
	}

	e2eShared.Run(t, sharedOpts)
}

func TestPoolDegradedOnFailToRender(t *testing.T) {
	cs := framework.NewClientSet("")

	mcadd := createMCToAddFile("add-a-file", "/etc/mytestconfs", "test")
	ignCfg, err := ctrlcommon.ParseAndConvertConfig(mcadd.Spec.Config.Raw)
	require.Nil(t, err, "failed to parse ignition config")
	ignCfg.Ignition.Version = "" // invalid, won't render
	rawIgnCfg := helpers.MarshalOrDie(ignCfg)
	mcadd.Spec.Config.Raw = rawIgnCfg

	// create the dummy MC now
	_, err = cs.MachineConfigs().Create(context.TODO(), mcadd, metav1.CreateOptions{})
	require.Nil(t, err, "failed to create machine config")

	// verify the pool goes degraded
	if err := wait.PollImmediate(2*time.Second, 5*time.Minute, func() (bool, error) {
		mcp, err := cs.MachineConfigPools().Get(context.TODO(), "worker", metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if mcfgv1.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolDegraded) {
			return true, nil
		}
		return false, nil
	}); err != nil {
		t.Errorf("machine config pool never switched to Degraded on failure to render: %v", err)
	}

	// now delete the bad MC and watch pool flipping back to not degraded
	if err := cs.MachineConfigs().Delete(context.TODO(), mcadd.Name, metav1.DeleteOptions{}); err != nil {
		t.Error(err)
	}

	// wait for the mcp to go back to previous config
	if err := wait.PollImmediate(2*time.Second, 5*time.Minute, func() (bool, error) {
		mcp, err := cs.MachineConfigPools().Get(context.TODO(), "worker", metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if mcfgv1.IsMachineConfigPoolConditionFalse(mcp.Status.Conditions, mcfgv1.MachineConfigPoolRenderDegraded) {
			return true, nil
		}
		return false, nil
	}); err != nil {
		t.Errorf("machine config pool never switched back to Degraded=False: %v", err)
	}
}

func TestReconcileAfterBadMC(t *testing.T) {
	cs := framework.NewClientSet("")

	// create a MC that contains a valid ignition config but is not reconcilable
	mcadd := createMCToAddFile("add-a-file", "/etc/mytestconfs", "test")
	ignCfg, err := ctrlcommon.ParseAndConvertConfig(mcadd.Spec.Config.Raw)
	require.Nil(t, err, "failed to parse ignition config")
	ignCfg.Storage.Disks = []ign3types.Disk{
		{
			Device: "/one",
		},
	}
	rawIgnCfg := helpers.MarshalOrDie(ignCfg)
	mcadd.Spec.Config.Raw = rawIgnCfg

	workerOldMc := helpers.GetMcName(t, cs, "worker")

	// create the dummy MC now
	_, err = cs.MachineConfigs().Create(context.TODO(), mcadd, metav1.CreateOptions{})
	if err != nil {
		t.Errorf("failed to create machine config %v", err)
	}

	renderedConfig, err := helpers.WaitForRenderedConfig(t, cs, "worker", mcadd.Name)
	require.Nil(t, err)

	// verify that one node picked the above up
	if err := wait.Poll(2*time.Second, 5*time.Minute, func() (bool, error) {
		nodes, err := helpers.GetNodesByRole(cs, "worker")
		if err != nil {
			return false, err
		}
		for _, node := range nodes {
			if node.Annotations[constants.DesiredMachineConfigAnnotationKey] == renderedConfig &&
				node.Annotations[constants.MachineConfigDaemonStateAnnotationKey] != constants.MachineConfigDaemonStateDone {
				// just check that we have the annotation here, w/o strings checking anything that can flip fast causing flakes
				if node.Annotations[constants.MachineConfigDaemonReasonAnnotationKey] != "" {
					return true, nil
				}
			}
		}
		return false, nil
	}); err != nil {
		t.Errorf("machine config hasn't been picked by any MCD: %v", err)
	}

	// verify that we got indeed an unavailable machine in the pool
	if err := wait.Poll(2*time.Second, 5*time.Minute, func() (bool, error) {
		mcp, err := cs.MachineConfigPools().Get(context.TODO(), "worker", metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if mcfgv1.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolNodeDegraded) && mcp.Status.DegradedMachineCount >= 1 {
			return true, nil
		}
		return false, nil
	}); err != nil {
		t.Errorf("worker pool isn't reporting degraded with a bad MC: %v", err)
	}

	// now delete the bad MC and watch the nodes reconciling as expected
	if err := cs.MachineConfigs().Delete(context.TODO(), mcadd.Name, metav1.DeleteOptions{}); err != nil {
		t.Error(err)
	}

	// wait for the mcp to go back to previous config
	if err := helpers.WaitForPoolComplete(t, cs, "worker", workerOldMc); err != nil {
		t.Fatal(err)
	}

	visited := make(map[string]bool)
	if err := wait.Poll(2*time.Second, 30*time.Minute, func() (bool, error) {
		nodes, err := helpers.GetNodesByRole(cs, "worker")
		if err != nil {
			return false, err
		}
		mcp, err := cs.MachineConfigPools().Get(context.TODO(), "worker", metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		for _, node := range nodes {
			if node.Annotations[constants.CurrentMachineConfigAnnotationKey] == workerOldMc &&
				node.Annotations[constants.DesiredMachineConfigAnnotationKey] == workerOldMc &&
				node.Annotations[constants.MachineConfigDaemonStateAnnotationKey] == constants.MachineConfigDaemonStateDone {
				visited[node.Name] = true
				if len(visited) == len(nodes) {
					if mcp.Status.UnavailableMachineCount == 0 && mcp.Status.ReadyMachineCount == int32(len(nodes)) &&
						mcp.Status.UpdatedMachineCount == int32(len(nodes)) {
						return true, nil
					}
				}
				continue
			}
		}
		return false, nil
	}); err != nil {
		t.Errorf("machine config didn't roll back on any worker: %v", err)
	}
}

// Test case for correct certificate rotation, even if a pool is paused
func TestMCDRotatesCertsOnPausedPool(t *testing.T) {
	var testPool = "master"

	cs := framework.NewClientSet("")

	// Get the machine config pool
	mcp, err := cs.MachineConfigPools().Get(context.TODO(), testPool, metav1.GetOptions{})
	require.Nil(t, err)

	// Update our copy of the pool
	newMcp := mcp.DeepCopy()
	newMcp.Spec.Paused = true

	t.Logf("Pausing pool")

	// Update the pool to be paused
	_, err = cs.MachineConfigPools().Update(context.TODO(), newMcp, metav1.UpdateOptions{})
	require.Nil(t, err)
	t.Logf("Paused")

	// Rotate the certificates
	t.Logf("Patching certificate")
	err = helpers.ForceKubeApiserverCertificateRotation(cs)
	require.Nil(t, err)
	t.Logf("Patched")

	// Verify the pool is paused and the config is pending but not rolling out
	t.Logf("Waiting for rendered config to get stuck behind pause")
	err = helpers.WaitForPausedConfig(t, cs, testPool)
	require.Nil(t, err)
	t.Logf("Certificate stuck behind paused (as expected)")

	// Get the latest certificate from the configmap
	inClusterCert, err := helpers.GetKubeletCABundleFromConfigmap(cs)
	require.Nil(t, err)

	// Get the on-disk state for the cert
	nodes, err := helpers.GetNodesByRole(cs, testPool)
	selectedNode := nodes[0]
	controllerConfig, err := cs.ControllerConfigs().Get(context.TODO(), "machine-config-controller", metav1.GetOptions{})
	require.Nil(t, err)
	err = helpers.WaitForMCDToSyncCert(t, cs, selectedNode, controllerConfig.ResourceVersion)
	require.Nil(t, err)
	require.NotEmpty(t, nodes)
	onDiskCert := helpers.ExecCmdOnNode(t, cs, selectedNode, "cat", "/rootfs/etc/kubernetes/kubelet-ca.crt")

	assert.Equal(t, inClusterCert, onDiskCert)
	t.Logf("The cert was properly rotated behind pause\n")

	// Set the pool back to unpaused
	mcp2, err := cs.MachineConfigPools().Get(context.TODO(), testPool, metav1.GetOptions{})
	require.Nil(t, err)

	newMcp2 := mcp2.DeepCopy()
	newMcp2.Spec.Paused = false

	t.Logf("Unpausing pool\n")
	_, err = cs.MachineConfigPools().Update(context.TODO(), newMcp2, metav1.UpdateOptions{})
	require.Nil(t, err)
	t.Logf("Waiting for config to sync after unpause...")

	// Wait for the pools to settle again, and see that we ran into no errors due to the cert rotation
	err = helpers.WaitForPoolCompleteAny(t, cs, testPool)
	require.Nil(t, err)
}

func createMCToAddFileForRole(name, role, filename, data string) *mcfgv1.MachineConfig {
	mcadd := helpers.CreateMC(fmt.Sprintf("%s-%s", name, uuid.NewUUID()), role)

	ignConfig := ctrlcommon.NewIgnConfig()
	ignFile := helpers.CreateIgn3File(filename, "data:,"+data, 420)
	ignConfig.Storage.Files = append(ignConfig.Storage.Files, ignFile)
	rawIgnConfig := helpers.MarshalOrDie(ignConfig)
	mcadd.Spec.Config.Raw = rawIgnConfig
	return mcadd
}

func createMCToAddFile(name, filename, data string) *mcfgv1.MachineConfig {
	return createMCToAddFileForRole(name, "worker", filename, data)
}
