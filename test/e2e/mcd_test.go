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

	_ "embed"

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
			Extensions: []string{"wasm", "ipsec", "usbguard", "kerberos", "kernel-devel", "sandboxed-containers", "sysstat"},
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
		installedPackages = helpers.ExecCmdOnNode(t, cs, infraNode, "chroot", "/rootfs", "rpm", "-q", "crun-wasm", "libreswan", "usbguard", "kernel-devel", "sysstat")
		// "kerberos" extension is not available on OKD
		expectedPackages = []string{"libreswan", "usbguard", "kernel-devel"}
	} else {
		installedPackages = helpers.ExecCmdOnNode(t, cs, infraNode, "chroot", "/rootfs", "rpm", "-q", "crun-wasm", "libreswan", "usbguard", "kernel-devel", "kernel-headers", "kata-containers", "krb5-workstation", "libkadm5", "sysstat")
		expectedPackages = []string{"crun-wasm", "libreswan", "usbguard", "kernel-devel", "kernel-headers", "kata-containers", "krb5-workstation", "libkadm5", "sysstat"}

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

//go:embed assets/x86_embeddedrpm.txt
var base64EncodedRPM string

func TestInstallRPMAndCheckMCDMetrics(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute*20)
	t.Cleanup(cancel)

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

	filePath := "/var/tmp/aeskeyfind.rpm"
	mode := 0644
	base64EncodedRPM = "data:text/plain;base64,7avu2wMAAAAAAWFlc2tleWZpbmQtMS4wLTE2LmVsOQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABAAUAAAAAAAAAAAAAAAAAAAAAjq3oAQAAAAAAAAAJAAAQlAAAAD4AAAAHAAAQhAAAABAAAAEMAAAABwAAAAAAAAI2AAABDQAAAAYAAAI2AAAAAQAAAREAAAAGAAACXwAAAAEAAAPoAAAABAAAAqAAAAABAAAD6gAAAAcAAAKkAAACNgAAA+wAAAAHAAAE2gAAABAAAAPvAAAABAAABOwAAAABAAAD8AAAAAcAAATwAAALlIkCMwQAAQgAHRYhBP+K0TRFlxBuzoE7kYo4cr8yKEZ8BQJk+SE6AAoJEIo4cr8yKEZ8nEQP/2lK+v3hvmTy6Nbj7JLUYMIQM0DN3d4jtNCxZzS11ABken0P5q8kA4qXrzpuQFmF14TXVaKJxwwXqu+pH9j989HchWoHktwGDmLVhV83+iWw9WyDzbnBx9Tis4VOcjhZmFOqYm2I1yNHsGoxI+JGHGRPMCwnTo/5fIkVDn4hRZKf4Ol1AST9ASBIwFoE0cHR4t1GrIOl3LQhffUdE3u/W94Q7iI4TGDBeSY5ne0E/aTaiuJ4wXMA/0n3o6h5tCdO8CiGviO0q/fwi7iqRbIM6Gvk2s6cv5CKaFQZHEx+iFkge5/WHTq9tZsU9++2DOeMdQrWCdejZEhuAMKXZ98MHeKKKIEw9dTQNwTjgEAixzC88mxfJwWUxnskqWSsTd+kY2tFcEoKz0A85gAW58rjPzxlcZ5ogGiwEgS1aQH7N8LXtqtVna4H0fn4heDmm5e939ydQTXuPoe1yHaoBMKMWtH1zjkO1MdtePQH3KbfuQ0LDsRbgyGOIUEJAD1G1eRFHCHXzAdbEe/PZE38n/jmJZ4a5/8jTo7IuJJRnGBSQvJl8Vp6R2jF2i4/2woACUHXOC+MJPoAXJ6fG11C5Zm/10yyY6t5ELOuntUW7bDbzdUijYpkkD3UWDE1xOx6c6wf1C9ao0cdMsuVweoCAaylMruEZAI7Ou+mt7oo/Ki+tmGkMGFjM2JmNjk0NjcwNmRkNGYxNGM5MjEzOTkwYTMwODgzYzcxNTc0YQBhYjU3N2EwNTBkOGMzMTZlYmRlNTYxZGY5MzQzMGQ3YzJhMGY5YTUzNzQ2NGE1MDkyMjYxMjcyMzM1MzFiOWY1AAAAM+uJAjMEAAEIAB0WIQT/itE0RZcQbs6BO5GKOHK/MihGfAUCZPkhOgAKCRCKOHK/MihGfGCfD/9+yUCVfa94Qe7asG+KVlR/gOWUBZftTPiw0k0l1ezaRl4ttkRBUQQlJJxTZlwKyaQckAlXc9R6KGEiFVxeZpKRhFj4Xa1Vjj4s8HS1Cstfvqwm6aAXj2cgtHGzA76fjQVVhttxr2XARkZ5GJPdvg2hsOmrW8Dxbbv6UTvixRpCAVr3UOriWb8bVqOKYwvl/hID+7/7SyIxU641+EQG+7gZMhuivKqOunLryt2T7g7vphPcino2dNcwevPvEduwbDA6WJMiVmKedrxuG+YwxuuefHd88KfM/t+KGwMFNxMk1bXm9IT5btNdwHAy8MhXiofqzORoJF92rVpX9k9fff/kUHRDUyKlp0DUad39C34hR7B5Y9QDLGVROg8M0A9fq/WgruNTv8JY3mMuOfjE32S41+NTtVvK7KQTwRw4p0tEuvniSypHkuM8LxQ5lAgyGCEZC7CW9sR7PVen+8G3C4kjPvsZzqFAT5IXzC0v4FPq0Uw/FXJGoP18u6OtIgXYuLN7G4SzFLefYB+vfa4q2iPzz41+rmR2V5Uyl9hEGb6oGvhZOPg8OJ/T/j48xuoKjI8nieA+j0Cy5EUbAl80bVbxIH0Xb+iJg3z52RBGXAqVAPl8BmbK47wgBpIYlSF1IpqEFLUi9WHWMQLPK4kdNZAVGfOKrCIIt4TQACj4Iegyd+ValDx0OjuXdCZ0mGFZw28AAAAAUewAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA+AAAAB////3AAAAAQAAAAAI6t6AEAAAAAAAAAPgAAD40AAAA/AAAABwAAD30AAAAQAAAAZAAAAAgAAAAAAAAAAQAAA+gAAAAGAAAAAgAAAAEAAAPpAAAABgAAAA0AAAABAAAD6gAAAAYAAAARAAAAAQAAA+wAAAAJAAAAGAAAAAEAAAPtAAAACQAAAFcAAAABAAAD7gAAAAQAAAJIAAAAAQAAA+8AAAAGAAACTAAAAAEAAAPxAAAABAAAAnQAAAABAAAD8gAAAAYAAAJ4AAAAAQAAA/MAAAAGAAAChwAAAAEAAAP2AAAABgAAApYAAAABAAAD9wAAAAYAAAKaAAAAAQAAA/gAAAAJAAACqQAAAAEAAAP8AAAABgAAArUAAAABAAAD/QAAAAYAAALhAAAAAQAAA/4AAAAGAAAC5wAAAAEAAAQEAAAABAAAAvAAAAAJAAAEBgAAAAMAAAMUAAAACQAABAkAAAADAAADJgAAAAkAAAQKAAAABAAAAzgAAAAJAAAECwAAAAgAAANcAAAACQAABAwAAAAIAAAEZQAAAAkAAAQNAAAABAAABIwAAAAJAAAEDwAAAAgAAASwAAAACQAABBAAAAAIAAAE3QAAAAkAAAQUAAAABgAABQoAAAABAAAEFQAAAAQAAAUoAAAACQAABBcAAAAIAAAFTAAAAAIAAAQYAAAABAAABWwAAAALAAAEGQAAAAgAAAWYAAAACwAABBoAAAAIAAAGsQAAAAsAAAQoAAAABgAABtcAAAABAAAEOAAAAAQAAAbgAAAABQAABDkAAAAIAAAG9AAAAAUAAAQ6AAAACAAACCAAAAAFAAAERwAAAAQAAAlcAAAACQAABEgAAAAEAAAJgAAAAAkAAARJAAAACAAACaQAAAAJAAAEWAAAAAQAAAmwAAAAAgAABFkAAAAIAAAJuAAAAAIAAARcAAAABAAACdAAAAAJAAAEXQAAAAgAAAn0AAAACQAABF4AAAAIAAAKaAAAAAkAAARiAAAABgAACxwAAAABAAAEZAAAAAYAAAybAAAAAQAABGUAAAAGAAAMoAAAAAEAAARmAAAABgAADKUAAAABAAAEbAAAAAYAAAyoAAAAAQAABHQAAAAEAAAMwAAAAAkAAAR1AAAABAAADOQAAAAJAAAEdgAAAAgAAA0IAAAABQAABHcAAAAEAAAOXAAAAAkAAAR4AAAABAAADoAAAAAJAAAEeQAAAAQAAA6kAAAABwAAE5MAAAAEAAAOwAAAAAEAABOUAAAABgAADsQAAAABAAATxgAAAAYAAA7uAAAAAQAAE+QAAAAIAAAO9AAAAAEAABPlAAAABAAADzgAAAABAAAT6QAAAAgAAA88AAAAAUMAYWVza2V5ZmluZAAxLjAAMTYuZWw5AExvY2F0ZSAxMjgtYml0IGFuZCAyNTYtYml0IEFFUyBrZXlzIGluIGEgY2FwdHVyZWQgbWVtb3J5IGltYWdlAFRoaXMgcHJvZ3JhbSBpbGx1c3RyYXRlcyBhdXRvbWF0aWMgdGVjaG5pcXVlcyBmb3IgbG9jYXRpbmcgMTI4LWJpdCBhbmQKMjU2LWJpdCBBRVMga2V5cyBpbiBhIGNhcHR1cmVkIG1lbW9yeSBpbWFnZS4KClRoZSBwcm9ncmFtIHVzZXMgdmFyaW91cyBhbGdvcml0aG1zIGFuZCBhbHNvIHBlcmZvcm1zIGEgc2ltcGxlIGVudHJvcHkKdGVzdCB0byBmaWx0ZXIgb3V0IGJsb2NrcyB0aGF0IGFyZSBub3Qga2V5cy4gSXQgY291bnRzIHRoZSBudW1iZXIgb2YKcmVwZWF0ZWQgYnl0ZXMgYW5kIHNraXBzIGJsb2NrcyB0aGF0IGhhdmUgdG9vIG1hbnkgcmVwZWF0cy4KClRoaXMgbWV0aG9kIHdvcmtzIGV2ZW4gaWYgc2V2ZXJhbCBiaXRzIG9mIHRoZSBrZXkgc2NoZWR1bGUgaGF2ZSBiZWVuCmNvcnJ1cHRlZCBkdWUgdG8gbWVtb3J5IGRlY2F5LgoKVGhpcyBwYWNrYWdlIGlzIHVzZWZ1bCB0byBzZXZlcmFsIGFjdGl2aXRpZXMsIGFzIGZvcmVuc2ljcyBpbnZlc3RpZ2F0aW9ucy4AAAAAZPkd9WJ1aWxkdm0teDg2LTI0LmlhZDIuZmVkb3JhcHJvamVjdC5vcmcAAAAAAExVRmVkb3JhIFByb2plY3QARmVkb3JhIFByb2plY3QAQlNEAEZlZG9yYSBQcm9qZWN0AFVuc3BlY2lmaWVkAGh0dHBzOi8vY2l0cC5wcmluY2V0b24uZWR1L291ci13b3JrL21lbW9yeS8AbGludXgAeDg2XzY0AAAAAAA8oAAAAAAAAAAAAAAAHgAAAAAAAAaxAAAAAAAABh4AAALIge1B7UHtof9B7YGkQe2BpIGkAAAAAAAAAAAAAAAAAAAAAAAAZPkd9WT5HfZk+R32ZPkd9mT5HfZIgDWqZPkd9kiANapk+R1mMmVjMjRhMmY3NzgwYWQ0OTIwNDA3ZDAwYmQ1NjYyYzQ2YmRkZmYyYWRiOWEwYWQ4YmI0NTc2ZTJiYTgxNWRlMgAAAAAANzUyNjhmYjI4OWYxNTUzZjQ0OWVhY2UwMTdhMzZlNDQ0ODRkYzA3YWEwNjQ4NDlmNzBmZGM5YzY2ZTYzMDhkMQAAMzUzNmI2NWY1OGJmMTIzZTY2OTk4MTFlMWM3ZmY2MDNhYjM0Yjc2OTMxZTQ0YTI5MGViYjZjNWJmYWE5ZmI2MABmNmZlMDg5YTQ3MTJmNmM2MGIzOTY0ZTliMGE1ZjlmNTg2MmZmY2U2ZGEwZmNkMTAzMmIxOWI4ZmUxMTU5YjE3AAAAAC4uLy4uLy4uLy4uL3Vzci9iaW4vYWVza2V5ZmluZAAAAAAAAAAAAAAAABAAAAAQAAAAEAAAAAAAAAAAAgAAAAAAAACAAAAAAnJvb3QAcm9vdAByb290AHJvb3QAcm9vdAByb290AHJvb3QAcm9vdAByb290AHJvb3QAcm9vdAByb290AHJvb3QAcm9vdAByb290AHJvb3QAcm9vdAByb290AGFlc2tleWZpbmQtMS4wLTE2LmVsOS5zcmMucnBtAP///////////////////////////////////////////////2Flc2tleWZpbmQAYWVza2V5ZmluZCh4ODYtNjQpAAAAAABAAAAAQAAAAEAAAABAAAAAQAAAAEAAAQAACgEAAAoBAAAKAQAACgAAQABsaWJjLnNvLjYoKSg2NGJpdCkAbGliYy5zby42KEdMSUJDXzIuMi41KSg2NGJpdCkAbGliYy5zby42KEdMSUJDXzIuMy40KSg2NGJpdCkAbGliYy5zby42KEdMSUJDXzIuMzMpKDY0Yml0KQBsaWJjLnNvLjYoR0xJQkNfMi4zNCkoNjRiaXQpAGxpYmMuc28uNihHTElCQ18yLjQpKDY0Yml0KQBycG1saWIoQ29tcHJlc3NlZEZpbGVOYW1lcykAcnBtbGliKEZpbGVEaWdlc3RzKQBycG1saWIoUGF5bG9hZEZpbGVzSGF2ZVByZWZpeCkAcnBtbGliKFBheWxvYWRJc1pzdGQpAHJ0bGQoR05VX0hBU0gpAAAAAAAAADMuMC40LTEANC42LjAtMQA0LjAtMQA1LjQuMTgtMQAANC4xNi4xLjMAZPm7QGS30EBjx99AYtfuQGHn/UBTYW11ZWwgSGVucmlxdWUgPHNhbXVlbG9waEBkZWJpYW4ub3JnPiAtIDEuMC0xNgBGZWRvcmEgUmVsZWFzZSBFbmdpbmVlcmluZyA8cmVsZW5nQGZlZG9yYXByb2plY3Qub3JnPiAtIDEuMC0xNQBGZWRvcmEgUmVsZWFzZSBFbmdpbmVlcmluZyA8cmVsZW5nQGZlZG9yYXByb2plY3Qub3JnPiAtIDEuMC0xNABGZWRvcmEgUmVsZWFzZSBFbmdpbmVlcmluZyA8cmVsZW5nQGZlZG9yYXByb2plY3Qub3JnPiAtIDEuMC0xMwBGZWRvcmEgUmVsZWFzZSBFbmdpbmVlcmluZyA8cmVsZW5nQGZlZG9yYXByb2plY3Qub3JnPiAtIDEuMC0xMgAtIHN5bmMgd2l0aCB0aGUgYnVnZml4IHBhdGNoZXMgd2l0aCBEZWJpYW4ALSBSZWJ1aWx0IGZvciBodHRwczovL2ZlZG9yYXByb2plY3Qub3JnL3dpa2kvRmVkb3JhXzM5X01hc3NfUmVidWlsZAAtIFJlYnVpbHQgZm9yIGh0dHBzOi8vZmVkb3JhcHJvamVjdC5vcmcvd2lraS9GZWRvcmFfMzhfTWFzc19SZWJ1aWxkAC0gUmVidWlsdCBmb3IgaHR0cHM6Ly9mZWRvcmFwcm9qZWN0Lm9yZy93aWtpL0ZlZG9yYV8zN19NYXNzX1JlYnVpbGQALSBSZWJ1aWx0IGZvciBodHRwczovL2ZlZG9yYXByb2plY3Qub3JnL3dpa2kvRmVkb3JhXzM2X01hc3NfUmVidWlsZAAAAAAAAQAAAAEAAAABAAAAAQAAAAEAAAABAAAAAQAAAAEAAAABAAAAAQAAAAIAAAADAAAABAAAAAUAAAAGAAAABwAAAAgAAAAJAAAAAAAAAAAAAAAAAAAACAAAAAgxLjAtMTYuZWw5ADEuMC0xNi5lbDkAAAAAAAAAAAAAAQAAAAIAAAADAAAABAAAAAUAAAAGAAAABwAAAAhhZXNrZXlmaW5kAC5idWlsZC1pZAAxOABlNjlmY2NmMDI4MzhjNGZhZjRkOWE1MTcyYmNhNGY1YTAyM2JjMwBhZXNrZXlmaW5kAFJFQURNRQBhZXNrZXlmaW5kAExJQ0VOU0UAYWVza2V5ZmluZC4xLmd6AC91c3IvYmluLwAvdXNyL2xpYi8AL3Vzci9saWIvLmJ1aWxkLWlkLwAvdXNyL2xpYi8uYnVpbGQtaWQvMTgvAC91c3Ivc2hhcmUvZG9jLwAvdXNyL3NoYXJlL2RvYy9hZXNrZXlmaW5kLwAvdXNyL3NoYXJlL2xpY2Vuc2VzLwAvdXNyL3NoYXJlL2xpY2Vuc2VzL2Flc2tleWZpbmQvAC91c3Ivc2hhcmUvbWFuL21hbjEvAC1PMiAtZmx0bz1hdXRvIC1mZmF0LWx0by1vYmplY3RzIC1mZXhjZXB0aW9ucyAtZyAtZ3JlY29yZC1nY2Mtc3dpdGNoZXMgLXBpcGUgLVdhbGwgLVdlcnJvcj1mb3JtYXQtc2VjdXJpdHkgLVdwLC1EX0ZPUlRJRllfU09VUkNFPTIgLVdwLC1EX0dMSUJDWFhfQVNTRVJUSU9OUyAtc3BlY3M9L3Vzci9saWIvcnBtL3JlZGhhdC9yZWRoYXQtaGFyZGVuZWQtY2MxIC1mc3RhY2stcHJvdGVjdG9yLXN0cm9uZyAtc3BlY3M9L3Vzci9saWIvcnBtL3JlZGhhdC9yZWRoYXQtYW5ub2Jpbi1jYzEgIC1tNjQgLW1hcmNoPXg4Ni02NC12MiAtbXR1bmU9Z2VuZXJpYyAtZmFzeW5jaHJvbm91cy11bndpbmQtdGFibGVzIC1mc3RhY2stY2xhc2gtcHJvdGVjdGlvbiAtZmNmLXByb3RlY3Rpb24AY3BpbwB6c3RkADE5AHg4Nl82NC1yZWRoYXQtbGludXgtZ251AAAAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABAAAAAQAAAAIAAAABAAAAAwAAAAEAAAADAAAABEVMRiA2NC1iaXQgTFNCIHBpZSBleGVjdXRhYmxlLCB4ODYtNjQsIHZlcnNpb24gMSAoU1lTViksIGR5bmFtaWNhbGx5IGxpbmtlZCwgaW50ZXJwcmV0ZXIgL2xpYjY0L2xkLWxpbnV4LXg4Ni02NC5zby4yLCBCdWlsZElEW3NoYTFdPTE4ZTY5ZmNjZjAyODM4YzRmYWY0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"

	mc := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "95-worker-install-aeskeyfind",
			Labels: map[string]string{
				"machineconfiguration.openshift.io/role": "worker",
			},
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: helpers.MarshalOrDie(ign3types.Config{
					Ignition: ign3types.Ignition{
						Version: ign3types.MaxVersion.String(),
					},
					Storage: ign3types.Storage{
						Files: []ign3types.File{
							helpers.CreateIgn3File(filePath, base64EncodedRPM, mode),
						},
					},
				},
				),
			},
		},
	}

	t.Log("Selecting a random worker node to check the installation")
	node := helpers.GetRandomNode(t, cs, "worker")
	poolName := "test-mcp"
	t.Logf("Selected node: %s", node.Name)

	cleanupFunc := helpers.CreatePoolAndApplyMCToNode(t, cs, poolName, node, mc)
	defer cleanupFunc()

	t.Log("Waiting for the configuration to be applied to the custom MCP")
	helpers.WaitForConfigAndPoolComplete(t, cs, poolName, mc.Name)

	uninstallRpmFunc := helpers.MakeIdempotent(func() {
		t.Log("Starting cleanup: Uninstalling the RPM")

		err := helpers.WaitForNodeReady(t, cs, node)
		if err != nil {
			t.Logf("Node %s is not ready: %v.", node.Name, err)
			return
		}

		uninstallCmd := []string{
			"chroot", "/rootfs", "rpm-ostree", "uninstall", "aeskeyfind",
		}

		_, err = helpers.ExecCmdOnNodeWithError(cs, node, uninstallCmd...)
		if err != nil {
			t.Logf("Failed to uninstall RPM package on node %s: %v", node.Name, err)
			return
		}
		t.Log("RPM package uninstalled successfully")

		t.Logf("Rebooting node %s to apply the layered package", node.Name)

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
			t.Logf("Node %s is not ready: %v.", node.Name, err)
			return
		}
		t.Logf("Node %s rebooted successfully and RPM package is removed", node.Name)
	})

	// Register the uninstall function for cleanup
	t.Cleanup(uninstallRpmFunc)

	t.Log("Waiting for the configuration to be applied to the pool")
	helpers.WaitForConfigAndPoolComplete(t, cs, "worker", mc.Name)
	t.Log("Configuration applied successfully")

	unsupportedPackage := "/rootfs/var/tmp/aeskeyfind.rpm"

	t.Logf("Checking if the unsupported package exists on node %s", node.Name)
	fileCheckCmd := []string{
		"ls", "-l", "/rootfs/var/tmp", unsupportedPackage,
	}
	_, err := helpers.ExecCmdOnNodeWithError(cs, node, fileCheckCmd...)
	require.NoError(t, err, "Unsupported package %s not found on node %s: %v", unsupportedPackage, node.Name, err)
	t.Logf("Unsupported package %s found on node %s", unsupportedPackage, node.Name)

	installCmd := []string{
		"chroot", "/rootfs", "rpm-ostree", "install", "/var/tmp/aeskeyfind.rpm",
	}

	t.Logf("Executing rpm-ostree install command on node %s", node.Name)
	out, err := helpers.ExecCmdOnNodeWithError(cs, node, installCmd...)
	require.NoError(t, err, "Failed to install unsupported package on node %s: %v", node.Name, err)
	t.Logf("Output from rpm-ostree install: %s", out)

	t.Logf("Rebooting node %s to apply the layered package", node.Name)

	rebootCmd := []string{
		"chroot", "/rootfs", "sudo", "systemctl", "reboot",
	}

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
