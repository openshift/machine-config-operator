package e2e_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	ign "github.com/coreos/ignition/config/v2_4"
	igntypes "github.com/coreos/ignition/config/v2_4/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/e2e/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"
)

// Test case for https://github.com/openshift/machine-config-operator/issues/358
func TestMCDToken(t *testing.T) {
	cs := framework.NewClientSet("")

	listOptions := metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{"k8s-app": "machine-config-daemon"}).String(),
	}

	mcdList, err := cs.Pods("openshift-machine-config-operator").List(listOptions)
	require.Nil(t, err)

	for _, pod := range mcdList.Items {
		res, err := cs.Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
			Container: "machine-config-daemon",
		}).DoRaw()
		require.Nil(t, err)
		for _, line := range strings.Split(string(res), "\n") {
			if strings.Contains(line, "Unable to rotate token") {
				t.Fatalf("found token rotation failure message: %s", line)
			}
		}
	}
}

func mcLabelForRole(role string) map[string]string {
	mcLabels := make(map[string]string)
	mcLabels[mcfgv1.MachineConfigRoleLabelKey] = role
	return mcLabels
}

func mcLabelForWorkers() map[string]string {
	return mcLabelForRole("worker")
}

func createIgnFile(path, content, fs string, mode int) igntypes.File {
	return igntypes.File{
		FileEmbedded1: igntypes.FileEmbedded1{
			Contents: igntypes.FileContents{
				Source: content,
			},
			Mode: &mode,
		},
		Node: igntypes.Node{
			Filesystem: fs,
			Path:       path,
			User: &igntypes.NodeUser{
				Name: "root",
			},
		},
	}
}

func createMCToAddFileForRole(name, role, filename, data, fs string) *mcfgv1.MachineConfig {
	mcadd := createMC(fmt.Sprintf("%s-%s", name, uuid.NewUUID()), role)

	ignConfig := ctrlcommon.NewIgnConfig()
	ignFile := createIgnFile(filename, "data:,"+data, fs, 420)
	ignConfig.Storage.Files = append(ignConfig.Storage.Files, ignFile)
	rawIgnConfig := helpers.MarshalOrDie(ignConfig)
	mcadd.Spec.Config.Raw = rawIgnConfig
	return mcadd
}

func createMCToAddFile(name, filename, data, fs string) *mcfgv1.MachineConfig {
	return createMCToAddFileForRole(name, "worker", filename, data, fs)
}

func TestMCDeployed(t *testing.T) {
	cs := framework.NewClientSet("")

	// TODO: bring this back to 10
	for i := 0; i < 3; i++ {
		startTime := time.Now()
		mcadd := createMCToAddFile("add-a-file", fmt.Sprintf("/etc/mytestconf%d", i), "test", "root")

		// create the dummy MC now
		_, err := cs.MachineConfigs().Create(mcadd)
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

func mcdForNode(cs *framework.ClientSet, node *corev1.Node) (*corev1.Pod, error) {
	// find the MCD pod that has spec.nodeNAME = node.Name and get its name:
	listOptions := metav1.ListOptions{
		FieldSelector: fields.SelectorFromSet(fields.Set{"spec.nodeName": node.Name}).String(),
	}
	listOptions.LabelSelector = labels.SelectorFromSet(labels.Set{"k8s-app": "machine-config-daemon"}).String()

	mcdList, err := cs.Pods("openshift-machine-config-operator").List(listOptions)
	if err != nil {
		return nil, err
	}
	if len(mcdList.Items) != 1 {
		if len(mcdList.Items) == 0 {
			return nil, fmt.Errorf("Failed to find MCD for node %s", node.Name)
		}
		return nil, fmt.Errorf("Too many (%d) MCDs for node %s", len(mcdList.Items), node.Name)
	}
	return &mcdList.Items[0], nil
}

func TestUpdateSSH(t *testing.T) {
	cs := framework.NewClientSet("")

	// create a dummy MC with an sshKey for user Core
	mcName := fmt.Sprintf("sshkeys-worker-%s", uuid.NewUUID())
	mcadd := &mcfgv1.MachineConfig{}
	mcadd.ObjectMeta = metav1.ObjectMeta{
		Name:   mcName,
		Labels: mcLabelForWorkers(),
	}
	// create a new MC that adds a valid user & ssh keys
	tempUser := igntypes.PasswdUser{
		Name: "core",
		SSHAuthorizedKeys: []igntypes.SSHAuthorizedKey{
			"1234_test",
			"abc_test",
		},
	}
	ignConfig := ctrlcommon.NewIgnConfig()
	ignConfig.Passwd.Users = append(ignConfig.Passwd.Users, tempUser)
	rawIgnConfig := helpers.MarshalOrDie(ignConfig)
	mcadd.Spec.Config.Raw = rawIgnConfig

	_, err := cs.MachineConfigs().Create(mcadd)
	require.Nil(t, err, "failed to create MC")
	t.Logf("Created %s", mcadd.Name)

	// grab the latest worker- MC
	renderedConfig, err := waitForRenderedConfig(t, cs, "worker", mcadd.Name)
	require.Nil(t, err)
	err = waitForPoolComplete(t, cs, "worker", renderedConfig)
	require.Nil(t, err)
	nodes, err := getNodesByRole(cs, "worker")
	require.Nil(t, err)
	for _, node := range nodes {
		assert.Equal(t, node.Annotations[constants.CurrentMachineConfigAnnotationKey], renderedConfig)
		assert.Equal(t, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)

		// now rsh into that daemon and grep the authorized key file to check if 1234_test was written
		// must do both commands in same shell, combine commands into one exec.Command()
		found := execCmdOnNode(t, cs, node, "grep", "1234_test", "/rootfs/home/core/.ssh/authorized_keys")
		if !strings.Contains(found, "1234_test") {
			t.Fatalf("updated ssh keys not found in authorized_keys, got %s", found)
		}
		t.Logf("Node %s has SSH key", node.Name)
	}
}

func TestKernelArguments(t *testing.T) {
	cs := framework.NewClientSet("")
	kargsMC := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("kargs-%s", uuid.NewUUID()),
			Labels: mcLabelForWorkers(),
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
			},
			KernelArguments: []string{"nosmt", "foo=bar"},
		},
	}

	_, err := cs.MachineConfigs().Create(kargsMC)
	require.Nil(t, err)
	t.Logf("Created %s", kargsMC.Name)
	renderedConfig, err := waitForRenderedConfig(t, cs, "worker", kargsMC.Name)
	require.Nil(t, err)
	if err := waitForPoolComplete(t, cs, "worker", renderedConfig); err != nil {
		t.Fatal(err)
	}
	nodes, err := getNodesByRole(cs, "worker")
	require.Nil(t, err)
	for _, node := range nodes {
		assert.Equal(t, node.Annotations[constants.CurrentMachineConfigAnnotationKey], renderedConfig)
		assert.Equal(t, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)
		kargs := execCmdOnNode(t, cs, node, "cat", "/rootfs/proc/cmdline")
		for _, v := range kargsMC.Spec.KernelArguments {
			if !strings.Contains(kargs, v) {
				t.Fatalf("Missing '%s' in kargs", v)
			}
		}
		t.Logf("Node %s has expected kargs", node.Name)
	}
}

func TestKernelType(t *testing.T) {
	cs := framework.NewClientSet("")
	// Get initial MachineConfig used by the worker pool so that we can rollback to it later on
	mcp, err := cs.MachineConfigPools().Get("worker", metav1.GetOptions{})
	require.Nil(t, err)
	workerOldMC := mcp.Status.Configuration.Name

	kernelType := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("kerneltype-%s", uuid.NewUUID()),
			Labels: mcLabelForWorkers(),
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
			},
			KernelType: "realtime",
		},
	}

	_, err = cs.MachineConfigs().Create(kernelType)
	require.Nil(t, err)
	t.Logf("Created %s", kernelType.Name)
	renderedConfig, err := waitForRenderedConfig(t, cs, "worker", kernelType.Name)
	require.Nil(t, err)
	if err := waitForPoolComplete(t, cs, "worker", renderedConfig); err != nil {
		t.Fatal(err)
	}
	nodes, err := getNodesByRole(cs, "worker")
	require.Nil(t, err)
	for _, node := range nodes {
		assert.Equal(t, node.Annotations[constants.CurrentMachineConfigAnnotationKey], renderedConfig)
		assert.Equal(t, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)

		kernelInfo := execCmdOnNode(t, cs, node, "uname", "-a")
		if !strings.Contains(kernelInfo, "PREEMPT RT") {
			t.Fatalf("Node %s doesn't have expected kernel", node.Name)
		}
		t.Logf("Node %s has expected kernel", node.Name)
	}

	// Delete the applied kerneltype MachineConfig to make sure rollback works fine
	if err := cs.MachineConfigs().Delete(kernelType.Name, &metav1.DeleteOptions{}); err != nil {
		t.Error(err)
	}

	t.Logf("Deleted MachineConfig %s", kernelType.Name)

	// Wait for the mcp to rollback to previous config
	if err := waitForPoolComplete(t, cs, "worker", workerOldMC); err != nil {
		t.Fatal(err)
	}

	// Re-fetch the worker nodes for updated annotations
	nodes, err = getNodesByRole(cs, "worker")
	require.Nil(t, err)
	for _, node := range nodes {
		assert.Equal(t, node.Annotations[constants.CurrentMachineConfigAnnotationKey], workerOldMC)
		assert.Equal(t, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)
		kernelInfo := execCmdOnNode(t, cs, node, "uname", "-a")
		if strings.Contains(kernelInfo, "PREEMPT RT") {
			t.Fatalf("Node %s did not rollback successfully", node.Name)
		}
		t.Logf("Node %s has successfully rolled back", node.Name)
	}

}

func TestPoolDegradedOnFailToRender(t *testing.T) {
	cs := framework.NewClientSet("")

	mcadd := createMCToAddFile("add-a-file", "/etc/mytestconfs", "test", "root")
	ignCfg, _, err := ign.Parse(mcadd.Spec.Config.Raw)
	require.Nil(t, err, "failed to parse ignition config")
	ignCfg.Ignition.Version = "" // invalid, won't render
	rawIgnCfg := helpers.MarshalOrDie(ignCfg)
	mcadd.Spec.Config.Raw = rawIgnCfg

	// create the dummy MC now
	_, err = cs.MachineConfigs().Create(mcadd)
	require.Nil(t, err, "failed to create machine config")

	// verify the pool goes degraded
	if err := wait.PollImmediate(2*time.Second, 5*time.Minute, func() (bool, error) {
		mcp, err := cs.MachineConfigPools().Get("worker", metav1.GetOptions{})
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
	if err := cs.MachineConfigs().Delete(mcadd.Name, &metav1.DeleteOptions{}); err != nil {
		t.Error(err)
	}

	// wait for the mcp to go back to previous config
	if err := wait.PollImmediate(2*time.Second, 5*time.Minute, func() (bool, error) {
		mcp, err := cs.MachineConfigPools().Get("worker", metav1.GetOptions{})
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
	mcadd := createMCToAddFile("add-a-file", "/etc/mytestconfs", "test", "root")
	ignCfg, _, err := ign.Parse(mcadd.Spec.Config.Raw)
	require.Nil(t, err, "failed to parse ignition config")
	ignCfg.Networkd = igntypes.Networkd{
		Units: []igntypes.Networkdunit{
			{
				Name:     "test.network",
				Contents: "test contents",
			},
		},
	}
	rawIgnCfg := helpers.MarshalOrDie(ignCfg)
	mcadd.Spec.Config.Raw = rawIgnCfg

	workerOldMc := getMcName(t, cs, "worker")

	// create the dummy MC now
	_, err = cs.MachineConfigs().Create(mcadd)
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
		mcp, err := cs.MachineConfigPools().Get("worker", metav1.GetOptions{})
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
	if err := cs.MachineConfigs().Delete(mcadd.Name, &metav1.DeleteOptions{}); err != nil {
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
		mcp, err := cs.MachineConfigPools().Get("worker", metav1.GetOptions{})
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

func TestDontDeleteRPMFiles(t *testing.T) {
	cs := framework.NewClientSet("")

	mcHostFile := createMCToAddFile("modify-host-file", "/etc/motd", "mco-test", "root")

	workerOldMc := getMcName(t, cs, "worker")

	// create the dummy MC now
	_, err := cs.MachineConfigs().Create(mcHostFile)
	if err != nil {
		t.Errorf("failed to create machine config %v", err)
	}

	renderedConfig, err := waitForRenderedConfig(t, cs, "worker", mcHostFile.Name)
	require.Nil(t, err)

	// wait for the mcp to go back to previous config
	if err := waitForPoolComplete(t, cs, "worker", renderedConfig); err != nil {
		t.Fatal(err)
	}

	// now delete the bad MC and watch the nodes reconciling as expected
	if err := cs.MachineConfigs().Delete(mcHostFile.Name, &metav1.DeleteOptions{}); err != nil {
		t.Error(err)
	}

	// wait for the mcp to go back to previous config
	if err := waitForPoolComplete(t, cs, "worker", workerOldMc); err != nil {
		t.Fatal(err)
	}

	nodes, err := getNodesByRole(cs, "worker")
	require.Nil(t, err)

	for _, node := range nodes {
		assert.Equal(t, node.Annotations[constants.CurrentMachineConfigAnnotationKey], workerOldMc)
		assert.Equal(t, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)

		found := execCmdOnNode(t, cs, node, "cat", "/rootfs/etc/motd")
		if strings.Contains(found, "mco-test") {
			t.Fatalf("updated file doesn't contain expected data, got %s", found)
		}
	}
}

func TestCustomPool(t *testing.T) {
	cs := framework.NewClientSet("")

	unlabelFunc := labelRandomNodeFromPool(t, cs, "worker", "node-role.kubernetes.io/infra")

	createMCP(t, cs, "infra")

	infraMC := createMCToAddFileForRole("infra-host-file", "infra", "/etc/mco-custom-pool", "mco-custom-pool", "root")
	_, err := cs.MachineConfigs().Create(infraMC)
	require.Nil(t, err)
	renderedConfig, err := waitForRenderedConfig(t, cs, "infra", infraMC.Name)
	require.Nil(t, err)
	err = waitForPoolComplete(t, cs, "infra", renderedConfig)
	require.Nil(t, err)

	infraNode := getSingleNodeByRole(t, cs, "infra")

	assert.Equal(t, infraNode.Annotations[constants.CurrentMachineConfigAnnotationKey], renderedConfig)
	assert.Equal(t, infraNode.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)
	out := execCmdOnNode(t, cs, infraNode, "cat", "/rootfs/etc/mco-custom-pool")
	if !strings.Contains(out, "mco-custom-pool") {
		t.Fatalf("Unexpected infra MC content on node %s: %s", infraNode.Name, out)
	}
	t.Logf("Node %s has expected infra MC content", infraNode.Name)

	unlabelFunc()

	workerMCP, err := cs.MachineConfigPools().Get("worker", metav1.GetOptions{})
	require.Nil(t, err)
	if err := wait.Poll(2*time.Second, 5*time.Minute, func() (bool, error) {
		node, err := cs.Nodes().Get(infraNode.Name, metav1.GetOptions{})
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
