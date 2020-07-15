package e2e_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"


	ign3types "github.com/coreos/ignition/v2/config/v3_1/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/e2e/framework"
	"github.com/openshift/machine-config-operator/test/helpers"


)

// Test case for https://github.com/openshift/machine-config-operator/issues/358
func TestMCDToken(t *testing.T) {
	cs := framework.NewClientSet("")

	listOptions := metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{"k8s-app": "machine-config-daemon"}).String(),
	}

	mcdList, err := cs.Pods("openshift-machine-config-operator").List(context.TODO(), listOptions)
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

	// TODO: bring this back to 10
	for i := 0; i < 3; i++ {
		startTime := time.Now()
		mcadd := createMCToAddFile("add-a-file", fmt.Sprintf("/etc/mytestconf%d", i), "test")

		// create the dummy MC now
		_, err := cs.MachineConfigs().Create(context.TODO(), mcadd, metav1.CreateOptions{})
		if err != nil {
			t.Errorf("failed to create machine config %v", err)
		}

		t.Logf("Created %s", mcadd.Name)
		renderedConfig, err := waitForRenderedConfig(t, cs, "worker", mcadd.Name)
		require.Nil(t, err)
		err = waitForPoolComplete(t, cs, "worker", renderedConfig)
		require.Nil(t, err)
		nodes, err := getNodesByRole(cs, "worker")
		require.Nil(t, err)
		for _, node := range nodes {
			assert.Equal(t, renderedConfig, node.Annotations[constants.CurrentMachineConfigAnnotationKey])
			assert.Equal(t, constants.MachineConfigDaemonStateDone, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey])
		}
		t.Logf("All nodes updated with %s (%s elapsed)", mcadd.Name, time.Since(startTime))
	}
}

func TestKernelArguments(t *testing.T) {
	cs := framework.NewClientSet("")

	// Create infra pool to roll out MC changes
	unlabelFunc := labelRandomNodeFromPool(t, cs, "worker", "node-role.kubernetes.io/infra")
	createMCP(t, cs, "infra")

	// create old mc to have something to verify we successfully rolled back
	oldInfraConfig := createMC("old-infra", "infra")
	_, err := cs.MachineConfigs().Create(context.TODO(), oldInfraConfig, metav1.CreateOptions{})
	require.Nil(t, err)
	oldInfraRenderedConfig, err := waitForRenderedConfig(t, cs, "infra", oldInfraConfig.Name)
	err = waitForPoolComplete(t, cs, "infra", oldInfraRenderedConfig)
	require.Nil(t, err)

	// create kargs MC
	kargsMC := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("kargs-%s", uuid.NewUUID()),
			Labels: mcLabelForRole("infra"),
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
			},
			KernelArguments: []string{"nosmt", "foo=bar", "foo=baz", " baz=test bar=hello world"},
		},
	}

	_, err = cs.MachineConfigs().Create(context.TODO(), kargsMC, metav1.CreateOptions{})
	require.Nil(t, err)
	t.Logf("Created %s", kargsMC.Name)
	renderedConfig, err := waitForRenderedConfig(t, cs, "infra", kargsMC.Name)
	require.Nil(t, err)
	err = waitForPoolComplete(t, cs, "infra", renderedConfig)
	require.Nil(t, err)

	// Re-fetch the infra node for updated annotations
	infraNode := getSingleNodeByRole(t, cs, "infra")
	assert.Equal(t, infraNode.Annotations[constants.CurrentMachineConfigAnnotationKey], renderedConfig)
	assert.Equal(t, infraNode.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)
	kargs := execCmdOnNode(t, cs, infraNode, "cat", "/rootfs/proc/cmdline")
	expectedKernelArgs := []string{"nosmt", "foo=bar", "foo=baz", "baz=test", "bar=hello world"}
	for _, v := range expectedKernelArgs {
		if !strings.Contains(kargs, v) {
			t.Fatalf("Missing %q in kargs: %q", v, kargs)
		}
	}
	t.Logf("Node %s has expected kargs", infraNode.Name)

	// cleanup - delete karg mc and rollback
	if err := cs.MachineConfigs().Delete(context.TODO(), kargsMC.Name, metav1.DeleteOptions{}); err != nil {
		t.Error(err)
	}
	t.Logf("Deleted MachineConfig %s", kargsMC.Name)
	err = waitForPoolComplete(t, cs, "infra", oldInfraRenderedConfig)
	require.Nil(t, err)

	unlabelFunc()

	workerMCP, err := cs.MachineConfigPools().Get(context.TODO(), "worker", metav1.GetOptions{})
	require.Nil(t, err)
	if err := wait.Poll(2*time.Second, 5*time.Minute, func() (bool, error) {
		node, err := cs.Nodes().Get(context.TODO(), infraNode.Name, metav1.GetOptions{})
		require.Nil(t, err)
		if node.Annotations[constants.DesiredMachineConfigAnnotationKey] != workerMCP.Spec.Configuration.Name {
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Errorf("infra node hasn't moved back to worker config: %v", err)
	}
	err = waitForPoolComplete(t, cs, "infra", oldInfraRenderedConfig)
	require.Nil(t, err)
}

func TestKernelType(t *testing.T) {
	cs := framework.NewClientSet("")

	unlabelFunc := labelRandomNodeFromPool(t, cs, "worker", "node-role.kubernetes.io/infra")

	oldInfraRenderedConfig := getMcName(t, cs, "infra")

	// create kernel type MC and roll out
	kernelType := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("kerneltype-%s", uuid.NewUUID()),
			Labels: mcLabelForRole("infra"),
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
			},
			KernelType: "realtime",
		},
	}

	_, err := cs.MachineConfigs().Create(context.TODO(), kernelType, metav1.CreateOptions{})
	require.Nil(t, err)
	t.Logf("Created %s", kernelType.Name)
	renderedConfig, err := waitForRenderedConfig(t, cs, "infra", kernelType.Name)
	require.Nil(t, err)
	if err := waitForPoolComplete(t, cs, "infra", renderedConfig); err != nil {
		t.Fatal(err)
	}
	infraNode := getSingleNodeByRole(t, cs, "infra")

	assert.Equal(t, infraNode.Annotations[constants.CurrentMachineConfigAnnotationKey], renderedConfig)
	assert.Equal(t, infraNode.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)

	kernelInfo := execCmdOnNode(t, cs, infraNode, "uname", "-a")
	if !strings.Contains(kernelInfo, "PREEMPT RT") {
		t.Fatalf("Node %s doesn't have expected kernel", infraNode.Name)
	}
	t.Logf("Node %s has expected kernel", infraNode.Name)

	// Delete the applied kerneltype MachineConfig to make sure rollback works fine
	if err := cs.MachineConfigs().Delete(context.TODO(), kernelType.Name, metav1.DeleteOptions{}); err != nil {
		t.Error(err)
	}

	t.Logf("Deleted MachineConfig %s", kernelType.Name)

	// Wait for the mcp to rollback to previous config
	if err := waitForPoolComplete(t, cs, "infra", oldInfraRenderedConfig); err != nil {
		t.Fatal(err)
	}

	// Re-fetch the infra node for updated annotations
	infraNode = getSingleNodeByRole(t, cs, "infra")

	assert.Equal(t, infraNode.Annotations[constants.CurrentMachineConfigAnnotationKey], oldInfraRenderedConfig)
	assert.Equal(t, infraNode.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)
	kernelInfo = execCmdOnNode(t, cs, infraNode, "uname", "-a")
	if strings.Contains(kernelInfo, "PREEMPT RT") {
		t.Fatalf("Node %s did not rollback successfully", infraNode.Name)
	}
	t.Logf("Node %s has successfully rolled back", infraNode.Name)

	unlabelFunc()

	workerMCP, err := cs.MachineConfigPools().Get(context.TODO(), "worker", metav1.GetOptions{})
	require.Nil(t, err)
	if err := wait.Poll(2*time.Second, 5*time.Minute, func() (bool, error) {
		node, err := cs.Nodes().Get(context.TODO(), infraNode.Name, metav1.GetOptions{})
		require.Nil(t, err)
		if node.Annotations[constants.DesiredMachineConfigAnnotationKey] != workerMCP.Spec.Configuration.Name {
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Errorf("infra node hasn't moved back to worker config: %v", err)
	}
	err = waitForPoolComplete(t, cs, "infra", oldInfraRenderedConfig)
	require.Nil(t, err)

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
		ign3types.Disk{
			Device: "/one",
		},
	}
	rawIgnCfg := helpers.MarshalOrDie(ignCfg)
	mcadd.Spec.Config.Raw = rawIgnCfg

	workerOldMc := getMcName(t, cs, "worker")

	// create the dummy MC now
	_, err = cs.MachineConfigs().Create(context.TODO(), mcadd, metav1.CreateOptions{})
	if err != nil {
		t.Errorf("failed to create machine config %v", err)
	}

	renderedConfig, err := waitForRenderedConfig(t, cs, "worker", mcadd.Name)
	require.Nil(t, err)

	// verify that one node picked the above up
	if err := wait.Poll(2*time.Second, 5*time.Minute, func() (bool, error) {
		nodes, err := getNodesByRole(cs, "worker")
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
	if err := waitForPoolComplete(t, cs, "worker", workerOldMc); err != nil {
		t.Fatal(err)
	}

	visited := make(map[string]bool)
	if err := wait.Poll(2*time.Second, 30*time.Minute, func() (bool, error) {
		nodes, err := getNodesByRole(cs, "worker")
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

// Test that deleting a MC that changes a file does not completely delete the file
// entirely but rather restores it to its original state.
func TestDontDeleteRPMFiles(t *testing.T) {
	cs := framework.NewClientSet("")

	unlabelFunc := labelRandomNodeFromPool(t, cs, "worker", "node-role.kubernetes.io/infra")
	//createMCP(t, cs, "infra")

	// create old mc to have something to verify we successfully rolled back
	// oldInfraConfig := createMC("old-infra-rpm", "infra")
	// _, err := cs.MachineConfigs().Create(context.TODO(), oldInfraConfig, metav1.CreateOptions{})
	// require.Nil(t, err)
	// oldInfraRenderedConfig, err := waitForRenderedConfig(t, cs, "infra", oldInfraConfig.Name)
	// err = waitForPoolComplete(t, cs, "infra", oldInfraRenderedConfig)
	// require.Nil(t, err)
	oldInfraRenderedConfig := getMcName(t, cs, "infra")

	mcHostFile := createMCToAddFileForRole("modify-host-file", "infra", "/etc/motd", "mco-test")

	// create the dummy MC now
	_, err := cs.MachineConfigs().Create(context.TODO(), mcHostFile, metav1.CreateOptions{})
	if err != nil {
		t.Errorf("failed to create machine config %v", err)
	}

	renderedConfig, err := waitForRenderedConfig(t, cs, "infra", mcHostFile.Name)
	require.Nil(t, err)
	err = waitForPoolComplete(t, cs, "infra", renderedConfig)
	require.Nil(t, err)

	// now delete the bad MC and watch the nodes reconciling as expected
	if err := cs.MachineConfigs().Delete(context.TODO(), mcHostFile.Name, metav1.DeleteOptions{}); err != nil {
		t.Error(err)
	}

	// wait for the mcp to go back to previous config
	err = waitForPoolComplete(t, cs, "infra", oldInfraRenderedConfig)
	require.Nil(t, err)

	infraNode := getSingleNodeByRole(t, cs, "infra")

	assert.Equal(t, infraNode.Annotations[constants.CurrentMachineConfigAnnotationKey], oldInfraRenderedConfig)
	assert.Equal(t, infraNode.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)

	found := execCmdOnNode(t, cs, infraNode, "cat", "/rootfs/etc/motd")
	if strings.Contains(found, "mco-test") {
		t.Fatalf("updated file doesn't contain expected data, got %s", found)
	}

	unlabelFunc()

	workerMCP, err := cs.MachineConfigPools().Get(context.TODO(), "worker", metav1.GetOptions{})
	require.Nil(t, err)
	if err := wait.Poll(2*time.Second, 5*time.Minute, func() (bool, error) {
		node, err := cs.Nodes().Get(context.TODO(), infraNode.Name, metav1.GetOptions{})
		require.Nil(t, err)
		if node.Annotations[constants.DesiredMachineConfigAnnotationKey] != workerMCP.Spec.Configuration.Name {
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Errorf("infra node hasn't moved back to worker config: %v", err)
	}
	err = waitForPoolComplete(t, cs, "infra", oldInfraRenderedConfig)
	require.Nil(t, err)

}

func TestIgn3Cfg(t *testing.T) {
	cs := framework.NewClientSet("")

	unlabelFunc := labelRandomNodeFromPool(t, cs, "worker", "node-role.kubernetes.io/infra")

	//createMCP(t, cs, "infra")

	// create a dummy MC with an sshKey for user Core
	mcName := fmt.Sprintf("99-ign3cfg-infra-%s", uuid.NewUUID())
	mcadd := &mcfgv1.MachineConfig{}
	mcadd.ObjectMeta = metav1.ObjectMeta{
		Name:   mcName,
		Labels: mcLabelForRole("infra"),
	}
	// create a new MC that adds a valid user & ssh key
	testIgn3Config := ign3types.Config{}
	tempUser := ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"1234_test_ign3"}}
	testIgn3Config.Passwd.Users = append(testIgn3Config.Passwd.Users, tempUser)
	testIgn3Config.Ignition.Version = "3.1.0"
	mode := 420
	testfiledata := "data:,test-ign3-stuff"
	tempFile := ign3types.File{Node: ign3types.Node{Path: "/etc/testfileconfig"},
		FileEmbedded1: ign3types.FileEmbedded1{Contents: ign3types.Resource{Source: &testfiledata}, Mode: &mode}}
	testIgn3Config.Storage.Files = append(testIgn3Config.Storage.Files, tempFile)
	rawIgnConfig := helpers.MarshalOrDie(testIgn3Config)
	mcadd.Spec.Config.Raw = rawIgnConfig

	_, err := cs.MachineConfigs().Create(context.TODO(), mcadd, metav1.CreateOptions{})
	require.Nil(t, err, "failed to create MC")
	t.Logf("Created %s", mcadd.Name)

	// grab the latest worker- MC
	renderedConfig, err := waitForRenderedConfig(t, cs, "infra", mcadd.Name)
	require.Nil(t, err)
	err = waitForPoolComplete(t, cs, "infra", renderedConfig)
	require.Nil(t, err)

	infraNode := getSingleNodeByRole(t, cs, "infra")

	assert.Equal(t, infraNode.Annotations[constants.CurrentMachineConfigAnnotationKey], renderedConfig)
	assert.Equal(t, infraNode.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)

	foundSSH := execCmdOnNode(t, cs, infraNode, "grep", "1234_test_ign3", "/rootfs/home/core/.ssh/authorized_keys")
	if !strings.Contains(foundSSH, "1234_test_ign3") {
		t.Fatalf("updated ssh keys not found in authorized_keys, got %s", foundSSH)
	}
	t.Logf("Node %s has SSH key", infraNode.Name)

	foundFile := execCmdOnNode(t, cs, infraNode, "cat", "/rootfs/etc/testfileconfig")
	if !strings.Contains(foundFile, "test-ign3-stuff") {
		t.Fatalf("updated file doesn't contain expected data, got %s", foundFile)
	}
	t.Logf("Node %s has file", infraNode.Name)

	unlabelFunc()

	workerMCP, err := cs.MachineConfigPools().Get(context.TODO(), "worker", metav1.GetOptions{})
	require.Nil(t, err)
	if err := wait.Poll(2*time.Second, 5*time.Minute, func() (bool, error) {
		node, err := cs.Nodes().Get(context.TODO(), infraNode.Name, metav1.GetOptions{})
		require.Nil(t, err)
		if node.Annotations[constants.DesiredMachineConfigAnnotationKey] != workerMCP.Spec.Configuration.Name {
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Errorf("infra node hasn't moved back to worker config: %v", err)
	}
	err = waitForPoolComplete(t, cs, "infra", renderedConfig)
	require.Nil(t, err)

}
