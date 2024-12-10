package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"

	e2eShared "github.com/openshift/machine-config-operator/test/e2e-shared-tests"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	machineclientset "github.com/openshift/client-go/machine/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
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

	delete := helpers.CreateMCP(t, cs, "infra")
	workerOldMc := helpers.GetMcName(t, cs, "worker")

	unlabelFunc := helpers.LabelRandomNodeFromPool(t, cs, "worker", "node-role.kubernetes.io/infra")
	oldInfraConfig := helpers.CreateMC("old-infra", "infra")

	t.Cleanup(func() {
		unlabelFunc()
		// wait for the mcp to go back to previous config
		if err := helpers.WaitForPoolComplete(t, cs, "worker", workerOldMc); err != nil {
			t.Fatal(err)
		}
		delete()
		require.Nil(t, cs.MachineConfigs().Delete(context.TODO(), oldInfraConfig.Name, metav1.DeleteOptions{}))
	})
	// create old mc to have something to verify we successfully rolled back
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

	delete := helpers.CreateMCP(t, cs, "infra")
	workerOldMc := helpers.GetMcName(t, cs, "worker")

	unlabelFunc := helpers.LabelRandomNodeFromPool(t, cs, "worker", "node-role.kubernetes.io/infra")
	oldInfraConfig := helpers.CreateMC("old-infra", "infra")

	t.Cleanup(func() {
		unlabelFunc()
		// wait for the mcp to go back to previous config
		if err := helpers.WaitForPoolComplete(t, cs, "worker", workerOldMc); err != nil {
			t.Fatal(err)
		}
		delete()
		require.Nil(t, cs.MachineConfigs().Delete(context.TODO(), oldInfraConfig.Name, metav1.DeleteOptions{}))

	})

	_, err = cs.MachineConfigs().Create(context.TODO(), oldInfraConfig, metav1.CreateOptions{})
	require.Nil(t, err)
	oldInfraRenderedConfig, err := helpers.WaitForRenderedConfig(t, cs, "infra", oldInfraConfig.Name)
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

	delete := helpers.CreateMCP(t, cs, "infra")
	workerOldMc := helpers.GetMcName(t, cs, "worker")

	unlabelFunc := helpers.LabelRandomNodeFromPool(t, cs, "worker", "node-role.kubernetes.io/infra")
	oldInfraConfig := helpers.CreateMC("old-infra", "infra")

	t.Cleanup(func() {
		unlabelFunc()
		// wait for the mcp to go back to previous config
		if err := helpers.WaitForPoolComplete(t, cs, "worker", workerOldMc); err != nil {
			t.Fatal(err)
		}
		delete()
		require.Nil(t, cs.MachineConfigs().Delete(context.TODO(), oldInfraConfig.Name, metav1.DeleteOptions{}))

	})

	_, err := cs.MachineConfigs().Create(context.TODO(), oldInfraConfig, metav1.CreateOptions{})
	require.Nil(t, err)
	oldInfraRenderedConfig, err := helpers.WaitForRenderedConfig(t, cs, "infra", oldInfraConfig.Name)
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
			Extensions: []string{"wasm", "ipsec", "usbguard", "kerberos", "kernel-devel", "sandboxed-containers", "sysstat", "lvm2-lockd", "sanlock"},
		},
	}

	_, err = cs.MachineConfigs().Create(context.TODO(), extensions, metav1.CreateOptions{})
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
		installedPackages = helpers.ExecCmdOnNode(t, cs, infraNode, "chroot", "/rootfs", "rpm", "-q", "crun-wasm", "libreswan", "usbguard", "kernel-devel", "sysstat", "lvm2-lockd", "sanlock")
		// "kerberos" extension is not available on OKD
		expectedPackages = []string{"libreswan", "usbguard", "kernel-devel"}
	} else {
		installedPackages = helpers.ExecCmdOnNode(t, cs, infraNode, "chroot", "/rootfs", "rpm", "-q", "crun-wasm", "libreswan", "usbguard", "kernel-devel", "kernel-headers", "kata-containers", "krb5-workstation", "libkadm5", "sysstat", "lvm2-lockd", "sanlock")
		expectedPackages = []string{"crun-wasm", "libreswan", "usbguard", "kernel-devel", "kernel-headers", "kata-containers", "krb5-workstation", "libkadm5", "sysstat", "lvm2-lockd", "sanlock"}

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
	delete := helpers.CreateMCP(t, cs, "infra")
	workerOldMc := helpers.GetMcName(t, cs, "worker")

	unlabelFunc := helpers.LabelRandomNodeFromPool(t, cs, "worker", "node-role.kubernetes.io/infra")
	oldInfraConfig := helpers.CreateMC("old-infra", "infra")

	t.Cleanup(func() {
		unlabelFunc()
		// wait for the mcp to go back to previous config
		if err := helpers.WaitForPoolComplete(t, cs, "worker", workerOldMc); err != nil {
			t.Fatal(err)
		}
		delete()
		require.Nil(t, cs.MachineConfigs().Delete(context.TODO(), oldInfraConfig.Name, metav1.DeleteOptions{}))

	})
	_, err := cs.MachineConfigs().Create(context.TODO(), oldInfraConfig, metav1.CreateOptions{})
	require.Nil(t, err)
	oldInfraRenderedConfig, err := helpers.WaitForRenderedConfig(t, cs, "infra", oldInfraConfig.Name)
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

	_, err = cs.MachineConfigs().Create(context.TODO(), addAuthorizedKey, metav1.CreateOptions{})
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
		if apihelpers.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolDegraded) {
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
		if apihelpers.IsMachineConfigPoolConditionFalse(mcp.Status.Conditions, mcfgv1.MachineConfigPoolRenderDegraded) {
			return true, nil
		}
		return false, nil
	}); err != nil {
		t.Errorf("machine config pool never switched back to Degraded=False: %v", err)
	}
}

// Test that deleting a MC that changes a file does not completely delete the file
// entirely but rather restores it to its original state.
func TestDontDeleteRPMFiles(t *testing.T) {
	cs := framework.NewClientSet("")

	delete := helpers.CreateMCP(t, cs, "infra")
	workerOldMc := helpers.GetMcName(t, cs, "worker")

	unlabelFunc := helpers.LabelRandomNodeFromPool(t, cs, "worker", "node-role.kubernetes.io/infra")
	oldInfraConfig := helpers.CreateMC("old-infra", "infra")

	t.Cleanup(func() {
		unlabelFunc()
		// wait for the mcp to go back to previous config
		if err := helpers.WaitForPoolComplete(t, cs, "worker", workerOldMc); err != nil {
			t.Fatal(err)
		}
		delete()
		require.Nil(t, cs.MachineConfigs().Delete(context.TODO(), oldInfraConfig.Name, metav1.DeleteOptions{}))

	})

	_, err := cs.MachineConfigs().Create(context.TODO(), oldInfraConfig, metav1.CreateOptions{})
	require.Nil(t, err)
	oldInfraRenderedConfig, err := helpers.WaitForRenderedConfig(t, cs, "infra", oldInfraConfig.Name)

	mcHostFile := createMCToAddFileForRole("modify-host-file", "infra", "/etc/motd", "mco-test")

	// create the dummy MC now
	_, err = cs.MachineConfigs().Create(context.TODO(), mcHostFile, metav1.CreateOptions{})
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

	delete := helpers.CreateMCP(t, cs, "infra")
	workerOldMc := helpers.GetMcName(t, cs, "worker")

	unlabelFunc := helpers.LabelRandomNodeFromPool(t, cs, "worker", "node-role.kubernetes.io/infra")

	t.Cleanup(func() {
		unlabelFunc()
		// wait for the mcp to go back to previous config
		if err := helpers.WaitForPoolComplete(t, cs, "worker", workerOldMc); err != nil {
			t.Fatal(err)
		}
		delete()
	})
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

// Test case for correct certificate rotation, even if a pool is paused
func TestMCDRotatesCerts(t *testing.T) {
	var testPool = "master"

	cs := framework.NewClientSet("")

	// Rotate the certificates
	controllerConfig, err := cs.ControllerConfigs().Get(context.TODO(), "machine-config-controller", metav1.GetOptions{})
	require.Nil(t, err)

	oldData := ""
	for _, cert := range controllerConfig.Status.ControllerCertificates {
		if cert.BundleFile == "KubeAPIServerServingCAData" {
			oldData = cert.Subject
		}
	}
	t.Logf("Patching certificate")
	err = helpers.ForceKubeApiserverCertificateRotation(cs)
	require.Nil(t, err)
	t.Logf("Patched")

	// Get the on-disk state for the cert
	nodes, err := helpers.GetNodesByRole(cs, testPool)
	require.NotEmpty(t, nodes)
	selectedNode := nodes[0]
	controllerConfig, err = cs.ControllerConfigs().Get(context.TODO(), "machine-config-controller", metav1.GetOptions{})
	require.Nil(t, err)
	err = helpers.WaitForMCDToSyncCert(t, cs, selectedNode, controllerConfig.ResourceVersion)
	require.Nil(t, err)

	// Due to the nature of the cert updates(more info in https://github.com/openshift/machine-config-operator/pull/3718#discussion_r1218282540)
	// attempt to retry on failure after waiting for a few seconds before failing the test
	// To be extra safe, we re-fetch both certs on every try

	if err := wait.PollImmediate(5*time.Second, 15*time.Second, func() (bool, error) {
		inClusterCert, err := helpers.GetKubeletCABundleFromConfigmap(cs)
		require.Nil(t, err)
		onDiskCert := helpers.ExecCmdOnNode(t, cs, selectedNode, "cat", "/rootfs/etc/kubernetes/kubelet-ca.crt")
		return (onDiskCert == inClusterCert), nil
	}); err != nil {
		t.Errorf("Mismatch between on disk cert and in cluster cert: %v", err)
	}

	err = helpers.WaitForCertStatusToChange(t, cs, oldData)
	require.Nil(t, err)
}

// This test provisions a new node by using the MachineSet API to ensure that
// the SSH is present on the node after first boot. This will eventually need
// to migrate to using the Cluster API (CAPI) instead.
func TestFirstBootHasSSHKeys(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cs := framework.NewClientSet("")

	// This client is not part of framework.ClientSet, but we can instantiate it
	// and use the config from our ClientSet.
	machineclient := machineclientset.NewForConfigOrDie(cs.GetRestConfig())

	// Any MachineSets returned by this list will be worker MachineSets. Control
	// plane nodes are wrapped in a separate ControlPlaneMachineSet type with its
	// own client methods.
	machinesets, err := machineclient.MachineV1beta1().MachineSets("openshift-machine-api").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	// Just grab the first MachineSet we get back. It doesn't matter which one we
	// use for this test.
	machineset := machinesets.Items[0]

	// Scale up our MachineSet to add a new node to target for our test.
	newNodes, cleanupFunc := helpers.ScaleMachineSetAndWaitForNodesToBeReady(t, cs, machineset.Name, *machineset.Spec.Replicas+1)
	newNode := newNodes[0]
	t.Cleanup(func() {
		if t.Failed() {
			helpers.CollectDebugInfoFromNode(t, cs, newNode)
		}

		cleanupFunc()
	})

	sshKeyFileExistsOnNode := func(keyPath string) bool {
		_, err := helpers.ExecCmdOnNodeWithError(cs, *newNode, "stat", filepath.Join("/rootfs", keyPath))
		return err == nil
	}

	assertSSHKeyContents := func(keyPath string) {
		// Now that the new node is ready, ensure that the SSH key file is populated.
		out := helpers.ExecCmdOnNode(t, cs, *newNode, "cat", filepath.Join("/rootfs", keyPath))
		t.Logf("Got ssh key file data: %s", out)
		// TODO: Assert that the file contents equals the SSH key field on the
		// MachineConfig. In theory, this may seem easy to do, but in practice it's a
		// bit more involved because the SSH key field on MachineConfigs can accept
		// multiple SSH keys per item with line breaks or single SSH keys
		// one-per-line. For now, we just assert that the field is not empty.
		assert.NotEmpty(t, out, "expected SSH key file %s on %s to contain SSH keys, but it was empty", keyPath, newNode.Name)
	}

	isFound := false
	isFoundRhcos8KeyPath := false
	isFoundRhcos9KeyPath := false

	if sshKeyFileExistsOnNode(constants.RHCOS8SSHKeyPath) {
		assertSSHKeyContents(constants.RHCOS8SSHKeyPath)
		isFound = true
		isFoundRhcos8KeyPath = true
	}

	if sshKeyFileExistsOnNode(constants.RHCOS9SSHKeyPath) {
		assertSSHKeyContents(constants.RHCOS9SSHKeyPath)
		isFound = true
		isFoundRhcos9KeyPath = true
	}

	if isFound {
		t.Logf("SSH keys found on node in RHCOS8 location %v / RHCOS9 location %v", isFoundRhcos8KeyPath, isFoundRhcos9KeyPath)
	} else {
		t.Logf("Neither %s or %s exists on the node", constants.RHCOS8SSHKeyPath, constants.RHCOS9SSHKeyPath)
		t.FailNow()
	}
}

func sshKeyFileExistsOnNode(t *testing.T, cs *framework.ClientSet, node corev1.Node, path string) bool {
	_, err := helpers.ExecCmdOnNodeWithError(cs, node, "stat", filepath.Join("/rootfs", path))
	return err == nil
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

// Tests that changes to the internal image registry pull secret has the
// desired effect on both the ControllerConfig and the individual nodes
// filesystems.
func TestInternalImageRegistryPullSecret(t *testing.T) {
	cs := framework.NewClientSet("")

	hostnames := []string{
		"registry1.hostname.com",
		"registry2.hostname.com",
		"registry3.hostname.com",
	}

	filename := "/etc/mco/internal-registry-pull-secret.json"
	canonicalizedFilename := filepath.Join("/rootfs", filename)

	// For each hostname, we do the following (in parallel to make the test
	// faster and ensure that we don't run into any unforeseen edge-cases):
	//
	// 1. Create a new secret with the sanitized hostname as the secret name.
	// 2. Append the secret name to the machine-os-puller service account.
	// 3. Wait for the ControllerConfig to pick up the new hostname.
	// 4. Wait for each of the nodes to pick up the new hostname.
	// 5. Delete the secret.
	// 6. Remove the secret name from the machine-os-puller service account.
	// 7. Wait for the ControllerConfig to lose the hostname.
	// 8. Wait for each of the nodes to lose the new hostname.
	for _, imageRegistryHostname := range hostnames {
		imageRegistryHostname := imageRegistryHostname
		t.Run(imageRegistryHostname, func(t *testing.T) {
			t.Parallel()

			revertFunc := setupForInternalImageRegistryPullSecretTest(t, cs, imageRegistryHostname)
			t.Cleanup(revertFunc)

			t.Logf("Waiting for ControllerConfig to get hostname %s", imageRegistryHostname)

			helpers.AssertControllerConfigReachesExpectedState(t, cs, func(cc *mcfgv1.ControllerConfig) bool {
				return strings.Contains(string(cc.Spec.InternalRegistryPullSecret), imageRegistryHostname)
			})

			t.Logf("Waiting for all nodes to get hostname %s", imageRegistryHostname)

			helpers.AssertAllNodesReachExpectedState(t, cs, func(node corev1.Node) bool {
				contents := helpers.ExecCmdOnNode(t, cs, node, "cat", canonicalizedFilename)
				return strings.Contains(contents, imageRegistryHostname)
			})

			// Undo the change.
			revertFunc()

			t.Logf("Waiting for ControllerConfig to lose hostname %s", imageRegistryHostname)

			helpers.AssertControllerConfigReachesExpectedState(t, cs, func(cc *mcfgv1.ControllerConfig) bool {
				return !strings.Contains(string(cc.Spec.InternalRegistryPullSecret), imageRegistryHostname)
			})

			t.Logf("Waiting for all nodes to lose hostname %s", imageRegistryHostname)

			helpers.AssertAllNodesReachExpectedState(t, cs, func(node corev1.Node) bool {
				contents := helpers.ExecCmdOnNode(t, cs, node, "cat", canonicalizedFilename)
				return !strings.Contains(contents, imageRegistryHostname)
			})
		})
	}
}

// Creates a secret containing an arbitrary image registry hostname and appends
// it to the machine-os-puller service account. Returns an idempotent function
// which undoes these changes.
func setupForInternalImageRegistryPullSecretTest(t *testing.T, cs *framework.ClientSet, imageRegistryHostname string) func() {
	t.Helper()

	serviceAccountName := "machine-os-puller"
	secretName := strings.ReplaceAll(imageRegistryHostname, ".", "-")

	auths := map[string]ctrlcommon.DockerConfigEntry{
		imageRegistryHostname: ctrlcommon.DockerConfigEntry{
			Username: "user",
			Password: "secret",
			Email:    "user@hostname.com",
			Auth:     "auth",
		},
	}

	authBytes, err := json.Marshal(auths)
	require.NoError(t, err)

	newSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: ctrlcommon.MCONamespace,
		},
		Type: corev1.SecretTypeDockercfg,
		Data: map[string][]byte{
			corev1.DockerConfigKey: authBytes,
		},
	}

	_, err = cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).Create(context.TODO(), newSecret, metav1.CreateOptions{})
	require.NoError(t, err)

	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		sa, err := cs.CoreV1Interface.ServiceAccounts(ctrlcommon.MCONamespace).Get(context.TODO(), serviceAccountName, metav1.GetOptions{})
		if err != nil {
			return err
		}

		sa.ImagePullSecrets = append(sa.ImagePullSecrets, corev1.LocalObjectReference{
			Name: secretName,
		})

		_, err = cs.CoreV1Interface.ServiceAccounts(ctrlcommon.MCONamespace).Update(context.TODO(), sa, metav1.UpdateOptions{})
		return err
	})

	require.NoError(t, err)

	t.Logf("Added secret %s with registry hostname %s to %s serviceaccount", secretName, imageRegistryHostname, serviceAccountName)

	return helpers.MakeIdempotent(func() {
		err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			sa, err := cs.CoreV1Interface.ServiceAccounts(ctrlcommon.MCONamespace).Get(context.TODO(), serviceAccountName, metav1.GetOptions{})
			if err != nil {
				return err
			}

			refs := []corev1.LocalObjectReference{}

			for _, ref := range sa.ImagePullSecrets {
				if ref.Name != secretName {
					refs = append(refs, ref)
				}
			}

			sa.ImagePullSecrets = refs

			_, err = cs.CoreV1Interface.ServiceAccounts(ctrlcommon.MCONamespace).Update(context.TODO(), sa, metav1.UpdateOptions{})
			return err
		})

		require.NoError(t, err)

		require.NoError(t, cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).Delete(context.TODO(), secretName, metav1.DeleteOptions{}))

		t.Logf("Removed secret %s with hostname %s from %s service account", secretName, imageRegistryHostname, serviceAccountName)
	})
}

func TestInstallRPMAndCheckMCDMetrics(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute*20)
	t.Cleanup(cancel)

	// Start a goroutine to check for context timeout
	go func() {
		for {
			select {
			case <-ctx.Done():
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					t.Fatalf("Test deadline exceeded")
				}
				return
			default:
				time.Sleep(time.Second)
			}
		}
	}()

	cs := framework.NewClientSet("")
	t.Log("Starting test: TestInstallRPMAndCheckMCDMetrics")

	// Select a random worker node
	t.Log("Selecting a random worker node to check the installation")
	node := helpers.GetRandomNode(t, cs, "worker")
	t.Logf("Selected node: %s", node.Name)

	// Define cleanup function to uninstall the RPM and reboot the node
	uninstallRpmFunc := helpers.MakeIdempotent(func() {
		t.Log("Starting cleanup: Uninstalling the RPM")

		err := helpers.WaitForNodeReady(t, cs, node)
		if err != nil {
			t.Logf("Node %s is not ready: %v.", node.Name, err)
			return
		}

		uninstallCmd := []string{
			"chroot", "/rootfs", "rpm-ostree", "uninstall", "epel-release",
		}

		_, err = helpers.ExecCmdOnNodeWithError(cs, node, uninstallCmd...)
		if err != nil {
			t.Logf("Failed to uninstall RPM package on node %s: %v", node.Name, err)
			return
		}
		t.Log("RPM package uninstalled successfully")

		t.Logf("Rebooting node %s to apply the changes", node.Name)

		rebootCmd := []string{
			"chroot", "/rootfs", "sudo", "systemctl", "reboot",
		}

		_, err = helpers.ExecCmdOnNodeWithError(cs, node, rebootCmd...)
		if err != nil {
			t.Logf("Failed to reboot node %s: %v", node.Name, err)
			return
		}

		err = helpers.WaitForNodeReady(t, cs, node)
		if err != nil {
			t.Logf("Node %s is not ready after reboot: %v.", node.Name, err)
			return
		}
		t.Logf("Node %s rebooted successfully and RPM package is removed", node.Name)
	})

	// Register the uninstall function for cleanup
	t.Cleanup(uninstallRpmFunc)

	// Download the RPM package on the node
	t.Logf("Downloading the RPM package on node %s", node.Name)
	downloadCmd := []string{
		"chroot", "/rootfs", "curl", "-KL", "https://dl.fedoraproject.org/pub/epel/epel-release-latest-9.noarch.rpm", "-o", "/tmp/epel-release-latest-9.noarch.rpm",
	}

	_, err := helpers.ExecCmdOnNodeWithError(cs, node, downloadCmd...)
	require.NoError(t, err, "Failed to download RPM package on node %s: %v", node.Name, err)
	t.Logf("RPM package downloaded successfully")

	// Reboot the node to apply changes
	t.Logf("Rebooting node %s to apply the changes", node.Name)
	rebootCmd := []string{
		"chroot", "/rootfs", "sudo", "systemctl", "reboot",
	}

	t.Logf("Executing rpm-ostree install command on node %s", node.Name)
	// Install the RPM package
	installCmd := []string{
		"chroot", "/rootfs", "rpm-ostree", "install", "/tmp/epel-release-latest-9.noarch.rpm",
	}

	out, err := helpers.ExecCmdOnNodeWithError(cs, node, installCmd...)
	if err != nil {
		t.Fatalf("Failed to install RPM package on node %s: %v. Output: %s", node.Name, err, out)
	}
	t.Logf("Output from rpm-ostree install: %s", out)

	// Reboot the node to apply the changes
	t.Logf("Rebooting node %s to apply the layered package", node.Name)
	_, err = helpers.ExecCmdOnNodeWithError(cs, node, rebootCmd...)
	require.NoError(t, err, "Failed to reboot node %s: %v", node.Name, err)
	t.Logf("Reboot command issued successfully")
	t.Logf("Waiting for node %s to be ready after reboot", node.Name)
	require.NoError(t, helpers.WaitForNodeReady(t, cs, node))

	// Wait for the MCD to detect the change and update metrics
	t.Logf("Waiting for MCD metrics to reflect the installed package")
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	err = wait.PollUntilContextTimeout(ctx, 30*time.Second, 10*time.Minute, true, func(ctx context.Context) (done bool, err error) {
		cmd := []string{
			"curl", "-s", "http://127.0.0.1:8797/metrics",
		}

		out, err := helpers.ExecCmdOnNodeWithError(cs, node, cmd...)
		if err != nil {
			t.Logf("Error executing curl on node %s: %v", node.Name, err)
			return false, nil
		}

		// Check for the metric
		metricName := "mcd_local_unsupported_packages"
		if !strings.Contains(out, metricName) {
			t.Logf("Metric %s not found in metrics output", metricName)
			return false, nil
		}

		// Parse the metric to make sure it reflects the installed package
		metricLine := findMetricLine(out, metricName)
		if metricLine == "" {
			t.Logf("Metric %s line not found in metrics output", metricName)
			return false, nil
		}

		// Get the metric value
		metricValue, err := parseMetricValue(metricLine)
		if err != nil {
			t.Logf("Failed to parse metric value: %v", err)
			return false, nil
		}

		if metricValue == 1 {
			t.Logf("Metric %s value is 1, test passed", metricName)
			return true, nil
		}

		t.Logf("Metric %s value is %d, expected 1", metricName, metricValue)
		return false, nil
	})

	require.NoError(t, err, "Failed to verify MCD metrics for unsupported packages on node %s", node.Name)

	t.Log("Test completed successfully")
}

// Helper function to find the metric line in the metrics output
func findMetricLine(metricsOutput, metricName string) string {
	lines := strings.Split(metricsOutput, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, metricName) {
			return line
		}
	}
	return ""
}

// Helper function to parse the metric value from the metric line
func parseMetricValue(metricLine string) (int, error) {
	parts := strings.Fields(metricLine)
	if len(parts) < 2 {
		return 0, fmt.Errorf("invalid metric line: %s", metricLine)
	}
	value, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return 0, err
	}
	return value, nil
}
