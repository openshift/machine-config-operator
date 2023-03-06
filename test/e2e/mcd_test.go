package e2e_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"

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

	ctx := context.Background()

	startTime := time.Now()
	mcadd := createMCToAddFile("add-a-file", "/etc/mytestconf", "test")

	// create the dummy MC now
	_, err := cs.MachineConfigs().Create(ctx, mcadd, metav1.CreateOptions{})
	if err != nil {
		t.Errorf("failed to create machine config %v", err)
	}

	t.Logf("Created %s", mcadd.Name)
	renderedConfig, err := helpers.WaitForRenderedConfig(t, cs, "worker", mcadd.Name)
	require.Nil(t, err)
	err = helpers.WaitForPoolComplete(t, cs, "worker", renderedConfig)
	require.Nil(t, err)
	nodes, err := helpers.GetNodesByRole(cs, "worker")
	require.Nil(t, err)
	for _, node := range nodes {
		assert.Equal(t, renderedConfig, node.Annotations[constants.CurrentMachineConfigAnnotationKey])
		assert.Equal(t, constants.MachineConfigDaemonStateDone, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey])
	}
	t.Logf("All nodes updated with %s (%s elapsed)", mcadd.Name, time.Since(startTime))
}

func TestRunShared(t *testing.T) {
	mcpName := "infra"

	cleanupFuncs := helpers.NewCleanupFuncs()
	cs := framework.NewClientSet("")

	configOpts := e2eShared.ConfigDriftTestOpts{
		ClientSet: cs,
		MCPName:   mcpName,
		SetupFunc: func(mc *mcfgv1.MachineConfig) {
			cleanupFuncs.Add(helpers.CreatePoolAndApplyMC(t, cs, mcpName, mc))
		},
		TeardownFunc: func() {
			cleanupFuncs.Run()
		},
	}

	sharedOpts := e2eShared.SharedTestOpts{
		ConfigDriftTestOpts: configOpts,
	}

	e2eShared.Run(t, sharedOpts)
}

func TestKernelArguments(t *testing.T) {
	cs := framework.NewClientSet("")

	// Create infra pool to roll out MC changes
	unlabelFunc := helpers.LabelRandomNodeFromPool(t, cs, "worker", "node-role.kubernetes.io/infra")
	helpers.CreateMCP(t, cs, "infra")

	// create old mc to have something to verify we successfully rolled back
	oldInfraConfig := helpers.CreateMC("old-infra", "infra")
	_, err := cs.MachineConfigs().Create(context.TODO(), oldInfraConfig, metav1.CreateOptions{})
	require.Nil(t, err)
	oldInfraRenderedConfig, err := helpers.WaitForRenderedConfig(t, cs, "infra", oldInfraConfig.Name)
	err = helpers.WaitForPoolComplete(t, cs, "infra", oldInfraRenderedConfig)
	require.Nil(t, err)

	// create kargs MC
	kargsMC := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("kargs-%s", uuid.NewUUID()),
			Labels: helpers.MCLabelForRole("infra"),
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
	renderedConfig, err := helpers.WaitForRenderedConfig(t, cs, "infra", kargsMC.Name)
	require.Nil(t, err)
	err = helpers.WaitForPoolComplete(t, cs, "infra", renderedConfig)
	require.Nil(t, err)

	// Re-fetch the infra node for updated annotations
	infraNode := helpers.GetSingleNodeByRole(t, cs, "infra")
	assert.Equal(t, infraNode.Annotations[constants.CurrentMachineConfigAnnotationKey], renderedConfig)
	assert.Equal(t, infraNode.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)
	kargs := helpers.ExecCmdOnNode(t, cs, infraNode, "cat", "/rootfs/proc/cmdline")
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
	err = helpers.WaitForPoolComplete(t, cs, "infra", oldInfraRenderedConfig)
	require.Nil(t, err)

	unlabelFunc()

	workerMCP, err := cs.MachineConfigPools().Get(context.TODO(), "worker", metav1.GetOptions{})
	require.Nil(t, err)
	if err := wait.Poll(2*time.Second, 5*time.Minute, func() (bool, error) {
		node, err := cs.CoreV1Interface.Nodes().Get(context.TODO(), infraNode.Name, metav1.GetOptions{})
		require.Nil(t, err)
		if node.Annotations[constants.DesiredMachineConfigAnnotationKey] != workerMCP.Spec.Configuration.Name {
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Errorf("infra node hasn't moved back to worker config: %v", err)
	}
	err = helpers.WaitForPoolComplete(t, cs, "infra", oldInfraRenderedConfig)
	require.Nil(t, err)
}

func TestKernelType(t *testing.T) {
	cs := framework.NewClientSet("")

	isOKD, err := helpers.IsOKDCluster(cs)
	require.Nil(t, err)
	if isOKD {
		t.Skip("skipping test on OKD")
	}

	unlabelFunc := helpers.LabelRandomNodeFromPool(t, cs, "worker", "node-role.kubernetes.io/infra")

	oldInfraRenderedConfig := helpers.GetMcName(t, cs, "infra")

	// create kernel type MC and roll out
	kernelType := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("kerneltype-%s", uuid.NewUUID()),
			Labels: helpers.MCLabelForRole("infra"),
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
			},
			KernelType: "realtime",
		},
	}

	_, err = cs.MachineConfigs().Create(context.TODO(), kernelType, metav1.CreateOptions{})
	require.Nil(t, err)
	t.Logf("Created %s", kernelType.Name)
	renderedConfig, err := helpers.WaitForRenderedConfig(t, cs, "infra", kernelType.Name)
	require.Nil(t, err)
	if err := helpers.WaitForPoolComplete(t, cs, "infra", renderedConfig); err != nil {
		t.Fatal(err)
	}
	infraNode := helpers.GetSingleNodeByRole(t, cs, "infra")

	assert.Equal(t, infraNode.Annotations[constants.CurrentMachineConfigAnnotationKey], renderedConfig)
	assert.Equal(t, infraNode.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)

	kernelInfo := helpers.ExecCmdOnNode(t, cs, infraNode, "chroot", "/rootfs", "rpm", "-qa", "kernel-rt-core")
	if !strings.Contains(kernelInfo, "kernel-rt-core") {
		t.Fatalf("Node %s doesn't have expected kernel", infraNode.Name)
	}
	t.Logf("Node %s has expected kernel", infraNode.Name)

	// Delete the applied kerneltype MachineConfig to make sure rollback works fine
	if err := cs.MachineConfigs().Delete(context.TODO(), kernelType.Name, metav1.DeleteOptions{}); err != nil {
		t.Error(err)
	}

	t.Logf("Deleted MachineConfig %s", kernelType.Name)

	// Wait for the mcp to rollback to previous config
	if err := helpers.WaitForPoolComplete(t, cs, "infra", oldInfraRenderedConfig); err != nil {
		t.Fatal(err)
	}

	// Re-fetch the infra node for updated annotations
	infraNode = helpers.GetSingleNodeByRole(t, cs, "infra")

	assert.Equal(t, infraNode.Annotations[constants.CurrentMachineConfigAnnotationKey], oldInfraRenderedConfig)
	assert.Equal(t, infraNode.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)
	kernelInfo = helpers.ExecCmdOnNode(t, cs, infraNode, "chroot", "/rootfs", "rpm", "-qa", "kernel-rt-core")
	if strings.Contains(kernelInfo, "kernel-rt-core") {
		t.Fatalf("Node %s did not rollback successfully", infraNode.Name)
	}
	t.Logf("Node %s has successfully rolled back", infraNode.Name)

	unlabelFunc()

	workerMCP, err := cs.MachineConfigPools().Get(context.TODO(), "worker", metav1.GetOptions{})
	require.Nil(t, err)
	if err := wait.Poll(2*time.Second, 5*time.Minute, func() (bool, error) {
		node, err := cs.CoreV1Interface.Nodes().Get(context.TODO(), infraNode.Name, metav1.GetOptions{})
		require.Nil(t, err)
		if node.Annotations[constants.DesiredMachineConfigAnnotationKey] != workerMCP.Spec.Configuration.Name {
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Errorf("infra node hasn't moved back to worker config: %v", err)
	}
	err = helpers.WaitForPoolComplete(t, cs, "infra", oldInfraRenderedConfig)
	require.Nil(t, err)

}

func TestExtensions(t *testing.T) {
	cs := framework.NewClientSet("")

	unlabelFunc := helpers.LabelRandomNodeFromPool(t, cs, "worker", "node-role.kubernetes.io/infra")

	oldInfraRenderedConfig := helpers.GetMcName(t, cs, "infra")

	// Apply extensions
	extensions := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("extensions-%s", uuid.NewUUID()),
			Labels: helpers.MCLabelForRole("infra"),
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
			},
			Extensions: []string{"usbguard", "kerberos", "kernel-devel", "sandboxed-containers"},
		},
	}

	_, err := cs.MachineConfigs().Create(context.TODO(), extensions, metav1.CreateOptions{})
	require.Nil(t, err)
	t.Logf("Created %s", extensions.Name)
	renderedConfig, err := helpers.WaitForRenderedConfig(t, cs, "infra", extensions.Name)
	require.Nil(t, err)
	if err := helpers.WaitForPoolComplete(t, cs, "infra", renderedConfig); err != nil {
		t.Fatal(err)
	}
	infraNode := helpers.GetSingleNodeByRole(t, cs, "infra")

	assert.Equal(t, infraNode.Annotations[constants.CurrentMachineConfigAnnotationKey], renderedConfig)
	assert.Equal(t, infraNode.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)

	isOKD, err := helpers.IsOKDCluster(cs)
	require.Nil(t, err)

	var installedPackages string
	var expectedPackages []string
	if isOKD {
		// OKD does not support grouped extensions yet, so installing kernel-devel will not also pull in kernel-headers
		// "sandboxed-containers" extension is not available on OKD
		installedPackages = helpers.ExecCmdOnNode(t, cs, infraNode, "chroot", "/rootfs", "rpm", "-q", "usbguard", "kernel-devel")
		// "kerberos" extension is not available on OKD
		expectedPackages = []string{"usbguard", "kernel-devel"}
	} else {
		installedPackages = helpers.ExecCmdOnNode(t, cs, infraNode, "chroot", "/rootfs", "rpm", "-q", "usbguard", "kernel-devel", "kernel-headers", "kata-containers", "krb5-workstation", "libkadm5")
		expectedPackages = []string{"usbguard", "kernel-devel", "kernel-headers", "kata-containers", "krb5-workstation", "libkadm5"}

	}
	for _, v := range expectedPackages {
		if !strings.Contains(installedPackages, v) {
			t.Fatalf("Node %s doesn't have expected extensions", infraNode.Name)
		}
	}

	t.Logf("Node %s has expected extensions installed", infraNode.Name)

	// Delete the applied kerneltype MachineConfig to make sure rollback works fine
	if err := cs.MachineConfigs().Delete(context.TODO(), extensions.Name, metav1.DeleteOptions{}); err != nil {
		t.Error(err)
	}

	t.Logf("Deleted MachineConfig %s", extensions.Name)

	// Wait for the mcp to rollback to previous config
	if err := helpers.WaitForPoolComplete(t, cs, "infra", oldInfraRenderedConfig); err != nil {
		t.Fatal(err)
	}

	// Re-fetch the infra node for updated annotations
	infraNode = helpers.GetSingleNodeByRole(t, cs, "infra")

	assert.Equal(t, infraNode.Annotations[constants.CurrentMachineConfigAnnotationKey], oldInfraRenderedConfig)
	assert.Equal(t, infraNode.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)

	if isOKD {
		// OKD does not support grouped extensions yet, so installing kernel-devel will not also pull in kernel-headers
		// "sandboxed-containers" extension is not available on OKD
		installedPackages = helpers.ExecCmdOnNode(t, cs, infraNode, "chroot", "/rootfs", "rpm", "-qa", "usbguard", "kernel-devel")
	} else {
		installedPackages = helpers.ExecCmdOnNode(t, cs, infraNode, "chroot", "/rootfs", "rpm", "-qa", "usbguard", "kernel-devel", "kernel-headers", "kata-containers", "krb5-workstation", "libkadm5")
	}
	for _, v := range expectedPackages {
		if strings.Contains(installedPackages, v) {
			t.Fatalf("Node %s did not rollback successfully", infraNode.Name)
		}
	}

	t.Logf("Node %s has successfully rolled back", infraNode.Name)

	unlabelFunc()

	workerMCP, err := cs.MachineConfigPools().Get(context.TODO(), "worker", metav1.GetOptions{})
	require.Nil(t, err)
	if err := wait.Poll(2*time.Second, 5*time.Minute, func() (bool, error) {
		node, err := cs.CoreV1Interface.Nodes().Get(context.TODO(), infraNode.Name, metav1.GetOptions{})
		require.Nil(t, err)
		if node.Annotations[constants.DesiredMachineConfigAnnotationKey] != workerMCP.Spec.Configuration.Name {
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Errorf("infra node hasn't moved back to worker config: %v", err)
	}
	err = helpers.WaitForPoolComplete(t, cs, "infra", oldInfraRenderedConfig)
	require.Nil(t, err)
}

func TestNoReboot(t *testing.T) {
	cs := framework.NewClientSet("")
	unlabelFunc := helpers.LabelRandomNodeFromPool(t, cs, "worker", "node-role.kubernetes.io/infra")
	oldInfraRenderedConfig := helpers.GetMcName(t, cs, "infra")

	infraNode := helpers.GetSingleNodeByRole(t, cs, "infra")

	sshKeyContent := "test adding authorized key without node reboot"

	nodeOS := helpers.GetOSReleaseForNode(t, cs, infraNode).OS

	sshPaths := helpers.GetSSHPaths(nodeOS)

	t.Logf("Expecting SSH keys to be in %s", sshPaths.Expected)

	if sshPaths.Expected == constants.RHCOS9SSHKeyPath {
		// Write an SSH key to the old location on the node because the update process should remove this file.
		t.Logf("Writing SSH key to %s to ensure that it will be removed later", sshPaths.NotExpected)
		bashCmd := fmt.Sprintf("printf '%s' > %s", sshKeyContent, filepath.Join("/rootfs", sshPaths.NotExpected))
		helpers.ExecCmdOnNode(t, cs, infraNode, "/bin/bash", "-c", bashCmd)
	}

	// Delete the expected SSH keys directory to ensure that the directories are
	// (re)created correctly by the MCD. This targets the upgrade case where that
	// directory may not previously exist. Note: This will need to be revisited
	// once Config Drift Monitor is aware of SSH keys.
	helpers.ExecCmdOnNode(t, cs, infraNode, "rm", "-rf", filepath.Join("/rootfs", filepath.Dir(sshPaths.Expected)))

	output := helpers.ExecCmdOnNode(t, cs, infraNode, "cat", "/rootfs/proc/uptime")
	oldTime := strings.Split(output, " ")[0]
	t.Logf("Node %s initial uptime: %s", infraNode.Name, oldTime)
	initialEtcShadowContents := helpers.ExecCmdOnNode(t, cs, infraNode, "grep", "^core:", "/rootfs/etc/shadow")

	// Adding authorized key for user core
	testIgnConfig := ctrlcommon.NewIgnConfig()
	testPasswdHash := "testpass"

	testIgnConfig.Passwd.Users = []ign3types.PasswdUser{
		{
			Name:              "core",
			SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{ign3types.SSHAuthorizedKey(sshKeyContent)},
			PasswordHash:      &testPasswdHash,
		},
	}

	addAuthorizedKey := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("authorized-key-infra-%s", uuid.NewUUID()),
			Labels: helpers.MCLabelForRole("infra"),
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: helpers.MarshalOrDie(testIgnConfig),
			},
		},
	}

	_, err := cs.MachineConfigs().Create(context.TODO(), addAuthorizedKey, metav1.CreateOptions{})
	require.Nil(t, err, "failed to create MC")
	t.Logf("Created %s", addAuthorizedKey.Name)

	// grab the latest worker- MC
	renderedConfig, err := helpers.WaitForRenderedConfig(t, cs, "infra", addAuthorizedKey.Name)
	require.Nil(t, err)
	err = helpers.WaitForPoolComplete(t, cs, "infra", renderedConfig)
	require.Nil(t, err)

	// Re-fetch the infra node for updated annotations
	infraNode = helpers.GetSingleNodeByRole(t, cs, "infra")

	assert.Equal(t, infraNode.Annotations[constants.CurrentMachineConfigAnnotationKey], renderedConfig)
	assert.Equal(t, infraNode.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)

	helpers.AssertFileOnNode(t, cs, infraNode, sshPaths.Expected)
	helpers.AssertFileNotOnNode(t, cs, infraNode, sshPaths.NotExpected)

	foundSSHKey := helpers.ExecCmdOnNode(t, cs, infraNode, "cat", filepath.Join("/rootfs", sshPaths.Expected))
	if !strings.Contains(foundSSHKey, sshKeyContent) {
		t.Fatalf("updated ssh keys not found in authorized_keys, got %s", foundSSHKey)
	}
	t.Logf("Node %s has SSH key", infraNode.Name)

	assertExpectedPerms(t, cs, infraNode, "/home/core/.ssh", []string{constants.CoreUserName, constants.CoreGroupName, "700"})

	if sshPaths.Expected == constants.RHCOS9SSHKeyPath {
		// /home/core/.ssh/authorized_keys.d
		assertExpectedPerms(t, cs, infraNode, filepath.Dir(constants.RHCOS9SSHKeyPath), []string{constants.CoreUserName, constants.CoreGroupName, "700"})
	}

	assertExpectedPerms(t, cs, infraNode, sshPaths.Expected, []string{constants.CoreUserName, constants.CoreGroupName, "600"})

	currentEtcShadowContents := helpers.ExecCmdOnNode(t, cs, infraNode, "grep", "^core:", "/rootfs/etc/shadow")

	if currentEtcShadowContents == initialEtcShadowContents {
		t.Fatalf("updated password hash not found in etc/shadow, got %s", currentEtcShadowContents)
	}

	t.Logf("Node %s has Password Hash", infraNode.Name)

	output = helpers.ExecCmdOnNode(t, cs, infraNode, "cat", "/rootfs/proc/uptime")
	newTime := strings.Split(output, " ")[0]

	// To ensure we didn't reboot, new uptime should be greater than old uptime
	uptimeOld, err := strconv.ParseFloat(oldTime, 64)
	require.Nil(t, err)

	uptimeNew, err := strconv.ParseFloat(newTime, 64)
	require.Nil(t, err)

	if uptimeOld > uptimeNew {
		t.Fatalf("Node %s rebooted uptime decreased from %f to %f", infraNode.Name, uptimeOld, uptimeNew)
	}

	t.Logf("Node %s didn't reboot as expected, uptime increased from %f to %f ", infraNode.Name, uptimeOld, uptimeNew)

	// Delete the applied authorized key MachineConfig to make sure rollback works fine without node reboot
	if err := cs.MachineConfigs().Delete(context.TODO(), addAuthorizedKey.Name, metav1.DeleteOptions{}); err != nil {
		t.Error(err)
	}

	t.Logf("Deleted MachineConfig %s", addAuthorizedKey.Name)

	// Wait for the mcp to rollback to previous config
	if err := helpers.WaitForPoolComplete(t, cs, "infra", oldInfraRenderedConfig); err != nil {
		t.Fatal(err)
	}

	// Re-fetch the infra node for updated annotations
	infraNode = helpers.GetSingleNodeByRole(t, cs, "infra")

	assert.Equal(t, infraNode.Annotations[constants.CurrentMachineConfigAnnotationKey], oldInfraRenderedConfig)
	assert.Equal(t, infraNode.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)

	foundSSHKey = helpers.ExecCmdOnNode(t, cs, infraNode, "cat", filepath.Join("/rootfs", sshPaths.Expected))
	if strings.Contains(foundSSHKey, sshKeyContent) {
		t.Fatalf("Node %s did not rollback successfully", infraNode.Name)
	}

	helpers.AssertFileOnNode(t, cs, infraNode, sshPaths.Expected)
	helpers.AssertFileNotOnNode(t, cs, infraNode, sshPaths.NotExpected)

	t.Logf("Node %s has successfully rolled back", infraNode.Name)

	// Ensure that node didn't reboot during rollback
	output = helpers.ExecCmdOnNode(t, cs, infraNode, "cat", "/rootfs/proc/uptime")
	newTime = strings.Split(output, " ")[0]

	uptimeNew, err = strconv.ParseFloat(newTime, 64)
	require.Nil(t, err)

	if uptimeOld > uptimeNew {
		t.Fatalf("Node %s rebooted during rollback, uptime decreased from %f to %f", infraNode.Name, uptimeOld, uptimeNew)
	}

	t.Logf("Node %s didn't reboot as expected during rollback, uptime increased from %f to %f ", infraNode.Name, uptimeOld, uptimeNew)

	unlabelFunc()

	workerMCP, err := cs.MachineConfigPools().Get(context.TODO(), "worker", metav1.GetOptions{})
	require.Nil(t, err)
	if err := wait.Poll(2*time.Second, 5*time.Minute, func() (bool, error) {
		node, err := cs.CoreV1Interface.Nodes().Get(context.TODO(), infraNode.Name, metav1.GetOptions{})
		require.Nil(t, err)
		if node.Annotations[constants.DesiredMachineConfigAnnotationKey] != workerMCP.Spec.Configuration.Name {
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Errorf("infra node hasn't moved back to worker config: %v", err)
	}
	err = helpers.WaitForPoolComplete(t, cs, "infra", oldInfraRenderedConfig)
	require.Nil(t, err)

	rollbackEtcShadowContents := helpers.ExecCmdOnNode(t, cs, infraNode, "grep", "^core:", "/rootfs/etc/shadow")
	assert.Equal(t, initialEtcShadowContents, rollbackEtcShadowContents)
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

// Test that deleting a MC that changes a file does not completely delete the file
// entirely but rather restores it to its original state.
func TestDontDeleteRPMFiles(t *testing.T) {
	cs := framework.NewClientSet("")

	unlabelFunc := helpers.LabelRandomNodeFromPool(t, cs, "worker", "node-role.kubernetes.io/infra")

	oldInfraRenderedConfig := helpers.GetMcName(t, cs, "infra")

	mcHostFile := createMCToAddFileForRole("modify-host-file", "infra", "/etc/motd", "mco-test")

	// create the dummy MC now
	_, err := cs.MachineConfigs().Create(context.TODO(), mcHostFile, metav1.CreateOptions{})
	if err != nil {
		t.Errorf("failed to create machine config %v", err)
	}

	renderedConfig, err := helpers.WaitForRenderedConfig(t, cs, "infra", mcHostFile.Name)
	require.Nil(t, err)
	err = helpers.WaitForPoolComplete(t, cs, "infra", renderedConfig)
	require.Nil(t, err)

	// now delete the bad MC and watch the nodes reconciling as expected
	if err := cs.MachineConfigs().Delete(context.TODO(), mcHostFile.Name, metav1.DeleteOptions{}); err != nil {
		t.Error(err)
	}

	// wait for the mcp to go back to previous config
	err = helpers.WaitForPoolComplete(t, cs, "infra", oldInfraRenderedConfig)
	require.Nil(t, err)

	infraNode := helpers.GetSingleNodeByRole(t, cs, "infra")

	assert.Equal(t, infraNode.Annotations[constants.CurrentMachineConfigAnnotationKey], oldInfraRenderedConfig)
	assert.Equal(t, infraNode.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)

	found := helpers.ExecCmdOnNode(t, cs, infraNode, "cat", "/rootfs/etc/motd")
	if strings.Contains(found, "mco-test") {
		t.Fatalf("updated file doesn't contain expected data, got %s", found)
	}

	unlabelFunc()

	workerMCP, err := cs.MachineConfigPools().Get(context.TODO(), "worker", metav1.GetOptions{})
	require.Nil(t, err)
	if err := wait.Poll(2*time.Second, 5*time.Minute, func() (bool, error) {
		node, err := cs.CoreV1Interface.Nodes().Get(context.TODO(), infraNode.Name, metav1.GetOptions{})
		require.Nil(t, err)
		if node.Annotations[constants.DesiredMachineConfigAnnotationKey] != workerMCP.Spec.Configuration.Name {
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Errorf("infra node hasn't moved back to worker config: %v", err)
	}
	err = helpers.WaitForPoolComplete(t, cs, "infra", oldInfraRenderedConfig)
	require.Nil(t, err)

}

func TestIgn3Cfg(t *testing.T) {
	cs := framework.NewClientSet("")

	unlabelFunc := helpers.LabelRandomNodeFromPool(t, cs, "worker", "node-role.kubernetes.io/infra")

	// create a dummy MC with an sshKey for user Core
	mcName := fmt.Sprintf("99-ign3cfg-infra-%s", uuid.NewUUID())
	mcadd := &mcfgv1.MachineConfig{}
	mcadd.ObjectMeta = metav1.ObjectMeta{
		Name:   mcName,
		Labels: helpers.MCLabelForRole("infra"),
	}
	// create a new MC that adds a valid user & ssh key
	testIgn3Config := ign3types.Config{}
	tempUser := ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"1234_test_ign3"}}
	testIgn3Config.Passwd.Users = append(testIgn3Config.Passwd.Users, tempUser)
	testIgn3Config.Ignition.Version = "3.2.0"
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
	renderedConfig, err := helpers.WaitForRenderedConfig(t, cs, "infra", mcadd.Name)
	require.Nil(t, err)
	err = helpers.WaitForPoolComplete(t, cs, "infra", renderedConfig)
	require.Nil(t, err)

	infraNode := helpers.GetSingleNodeByRole(t, cs, "infra")

	assert.Equal(t, infraNode.Annotations[constants.CurrentMachineConfigAnnotationKey], renderedConfig)
	assert.Equal(t, infraNode.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)

	sshPaths := helpers.GetSSHPaths(helpers.GetOSReleaseForNode(t, cs, infraNode).OS)

	foundSSH := helpers.ExecCmdOnNode(t, cs, infraNode, "grep", "1234_test_ign3", filepath.Join("/rootfs", sshPaths.Expected))
	if !strings.Contains(foundSSH, "1234_test_ign3") {
		t.Fatalf("updated ssh keys not found in authorized_keys, got %s", foundSSH)
	}
	t.Logf("Node %s has SSH key", infraNode.Name)

	foundFile := helpers.ExecCmdOnNode(t, cs, infraNode, "cat", "/rootfs/etc/testfileconfig")
	if !strings.Contains(foundFile, "test-ign3-stuff") {
		t.Fatalf("updated file doesn't contain expected data, got %s", foundFile)
	}
	t.Logf("Node %s has file", infraNode.Name)

	unlabelFunc()

	workerMCP, err := cs.MachineConfigPools().Get(context.TODO(), "worker", metav1.GetOptions{})
	require.Nil(t, err)
	if err := wait.Poll(2*time.Second, 5*time.Minute, func() (bool, error) {
		node, err := cs.CoreV1Interface.Nodes().Get(context.TODO(), infraNode.Name, metav1.GetOptions{})
		require.Nil(t, err)
		if node.Annotations[constants.DesiredMachineConfigAnnotationKey] != workerMCP.Spec.Configuration.Name {
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Errorf("infra node hasn't moved back to worker config: %v", err)
	}
	err = helpers.WaitForPoolComplete(t, cs, "infra", renderedConfig)
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

// Checks that a file or directory on a given node matches the expected
// permissions in the form of [username, groupname, octal file permissions].
func assertExpectedPerms(t *testing.T, cs *framework.ClientSet, node corev1.Node, path string, expectedPerms []string) {
	t.Helper()

	actualPerms := strings.Split(strings.TrimSuffix(helpers.ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "stat", "--format=%U %G %a", path), "\n"), " ")
	assert.Equal(t, expectedPerms, actualPerms, "expected %s to have perms %v, got: %v", path, expectedPerms, actualPerms)
}
