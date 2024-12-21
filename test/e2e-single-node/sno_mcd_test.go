package e2e_sno_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	e2eShared "github.com/openshift/machine-config-operator/test/e2e-shared-tests"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	kubeErrs "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"

	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func TestKernelArguments(t *testing.T) {
	cs := framework.NewClientSet("")

	// Get initial MachineConfig used by the master pool so that we can rollback to it later on
	mcp, err := cs.MachineConfigPools().Get(context.TODO(), "master", metav1.GetOptions{})
	require.Nil(t, err)
	oldMasterRenderedConfig := mcp.Status.Configuration.Name

	// create kargs MC
	kargsMC := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("kargs-%s", uuid.NewUUID()),
			Labels: helpers.MCLabelForRole("master"),
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
			},
			KernelArguments: []string{"foo=bar", "foo=baz", " baz=test bar=hello world"},
		},
	}

	_, err = cs.MachineConfigs().Create(context.TODO(), kargsMC, metav1.CreateOptions{})
	require.Nil(t, err)
	t.Logf("Created %s", kargsMC.Name)
	renderedConfig, err := helpers.WaitForRenderedConfig(t, cs, "master", kargsMC.Name)
	require.Nil(t, err)
	err = waitForSingleNodePoolComplete(t, cs, "master", renderedConfig)
	require.Nil(t, err)

	node := helpers.GetSingleNodeByRole(t, cs, "master")
	assert.Equal(t, node.Annotations[constants.CurrentMachineConfigAnnotationKey], renderedConfig)
	assert.Equal(t, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)
	kargs := helpers.ExecCmdOnNode(t, cs, node, "cat", "/rootfs/proc/cmdline")
	expectedKernelArgs := []string{"foo=bar", "foo=baz", "baz=test", "bar=hello world"}
	for _, v := range expectedKernelArgs {
		if !strings.Contains(kargs, v) {
			t.Fatalf("Missing %q in kargs: %q", v, kargs)
		}
	}
	t.Logf("Node %s has expected kargs", node.Name)

	// cleanup - delete karg mc and rollback
	if err := cs.MachineConfigs().Delete(context.TODO(), kargsMC.Name, metav1.DeleteOptions{}); err != nil {
		t.Error(err)
	}
	t.Logf("Deleted MachineConfig %s", kargsMC.Name)
	err = waitForSingleNodePoolComplete(t, cs, "master", oldMasterRenderedConfig)
	require.Nil(t, err)

	node = helpers.GetSingleNodeByRole(t, cs, "master")
	assert.Equal(t, node.Annotations[constants.CurrentMachineConfigAnnotationKey], oldMasterRenderedConfig)
	assert.Equal(t, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)
	kargs = helpers.ExecCmdOnNode(t, cs, node, "cat", "/rootfs/proc/cmdline")

	for _, v := range expectedKernelArgs {
		if strings.Contains(kargs, v) {
			t.Fatalf("Node %s did not rollback successfully", node.Name)
		}
	}
	t.Logf("Node %s has successfully rolled back", node.Name)

}

func TestKernelType(t *testing.T) {
	cs := framework.NewClientSet("")

	// Get initial MachineConfig used by the master pool so that we can rollback to it later on
	mcp, err := cs.MachineConfigPools().Get(context.TODO(), "master", metav1.GetOptions{})
	require.Nil(t, err)
	oldMasterRenderedConfig := mcp.Status.Configuration.Name

	// create kernel type MC and roll out
	kernelType := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("kerneltype-%s", uuid.NewUUID()),
			Labels: helpers.MCLabelForRole("master"),
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
	renderedConfig, err := helpers.WaitForRenderedConfig(t, cs, "master", kernelType.Name)
	require.Nil(t, err)
	err = waitForSingleNodePoolComplete(t, cs, "master", renderedConfig)
	require.Nil(t, err)

	node := helpers.GetSingleNodeByRole(t, cs, "master")
	assert.Equal(t, node.Annotations[constants.CurrentMachineConfigAnnotationKey], renderedConfig)
	assert.Equal(t, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)

	kernelInfo := helpers.ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "rpm", "-qa", "kernel-rt-core")
	if !strings.Contains(kernelInfo, "kernel-rt-core") {
		t.Fatalf("Node %s doesn't have expected kernel", node.Name)
	}
	t.Logf("Node %s has expected kernel", node.Name)

	// Delete the applied kerneltype MachineConfig to make sure rollback works fine
	if err := cs.MachineConfigs().Delete(context.TODO(), kernelType.Name, metav1.DeleteOptions{}); err != nil {
		t.Error(err)
	}

	t.Logf("Deleted MachineConfig %s", kernelType.Name)

	// Wait for the mcp to rollback to previous config
	err = waitForSingleNodePoolComplete(t, cs, "master", oldMasterRenderedConfig)
	require.Nil(t, err)

	node = helpers.GetSingleNodeByRole(t, cs, "master")
	assert.Equal(t, node.Annotations[constants.CurrentMachineConfigAnnotationKey], oldMasterRenderedConfig)
	assert.Equal(t, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)

	kernelInfo = helpers.ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "rpm", "-qa", "kernel-rt-core")
	if strings.Contains(kernelInfo, "kernel-rt-core") {
		t.Fatalf("Node %s did not rollback successfully", node.Name)
	}
	t.Logf("Node %s has successfully rolled back", node.Name)
}

func TestExtensions(t *testing.T) {
	cs := framework.NewClientSet("")

	// Get initial MachineConfig used by the master pool so that we can rollback to it later on
	mcp, err := cs.MachineConfigPools().Get(context.TODO(), "master", metav1.GetOptions{})
	require.Nil(t, err)
	oldMasterRenderedConfig := mcp.Status.Configuration.Name

	// Apply extensions
	extensions := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("extensions-%s", uuid.NewUUID()),
			Labels: helpers.MCLabelForRole("master"),
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
			},
			Extensions: []string{"wasm", "ipsec", "usbguard", "kernel-devel", "kerberos", "sysstat", "lvm2-lockd", "sanlock"},
		},
	}

	_, err = cs.MachineConfigs().Create(context.TODO(), extensions, metav1.CreateOptions{})
	require.Nil(t, err)
	t.Logf("Created %s", extensions.Name)
	renderedConfig, err := helpers.WaitForRenderedConfig(t, cs, "master", extensions.Name)
	require.Nil(t, err)
	err = waitForSingleNodePoolComplete(t, cs, "master", renderedConfig)
	require.Nil(t, err)

	node := helpers.GetSingleNodeByRole(t, cs, "master")
	assert.Equal(t, node.Annotations[constants.CurrentMachineConfigAnnotationKey], renderedConfig)
	assert.Equal(t, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)

	installedPackages := helpers.ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "rpm", "-q", "crun-wasm", "libreswan", "usbguard", "kernel-devel", "kernel-headers", "krb5-workstation", "libkadm5", "sysstat", "lvm2-lockd", "sanlock")
	expectedPackages := []string{"crun-wasm", "libreswan", "usbguard", "kernel-devel", "kernel-headers", "krb5-workstation", "libkadm5", "sysstat", "lvm2-lockd", "sanlock"}
	for _, v := range expectedPackages {
		if !strings.Contains(installedPackages, v) {
			t.Fatalf("Node %s doesn't have expected extensions", node.Name)
		}
	}

	t.Logf("Node %s has expected extensions installed", node.Name)

	// Delete the applied kerneltype MachineConfig to make sure rollback works fine
	if err := cs.MachineConfigs().Delete(context.TODO(), extensions.Name, metav1.DeleteOptions{}); err != nil {
		t.Error(err)
	}

	t.Logf("Deleted MachineConfig %s", extensions.Name)

	// Wait for the mcp to rollback to previous config
	err = waitForSingleNodePoolComplete(t, cs, "master", oldMasterRenderedConfig)
	require.Nil(t, err)

	node = helpers.GetSingleNodeByRole(t, cs, "master")
	assert.Equal(t, node.Annotations[constants.CurrentMachineConfigAnnotationKey], oldMasterRenderedConfig)
	assert.Equal(t, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)

	installedPackages = helpers.ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "rpm", "-qa", "crun-wasm", "libreswan", "usbguard", "kernel-devel", "kernel-headers", "krb5-workstation", "libkadm5", "sysstat", "lvm2-lockd", "sanlock")
	for _, v := range expectedPackages {
		if strings.Contains(installedPackages, v) {
			t.Fatalf("Node %s did not rollback successfully", node.Name)
		}
	}

	t.Logf("Node %s has successfully rolled back", node.Name)
}

// TODO: Can this test be moved to e2e-shared since it is very similar to the one in e2e/mcd_test.go?
func TestNoReboot(t *testing.T) {
	cs := framework.NewClientSet("")

	// Get initial MachineConfig used by the master pool so that we can rollback to it later on
	mcp, err := cs.MachineConfigPools().Get(context.TODO(), "master", metav1.GetOptions{})
	require.Nil(t, err)
	oldMasterRenderedConfig := mcp.Status.Configuration.Name

	node := helpers.GetSingleNodeByRole(t, cs, "master")
	output := helpers.ExecCmdOnNode(t, cs, node, "cat", "/rootfs/proc/uptime")
	oldTime := strings.Split(output, " ")[0]
	t.Logf("Node %s initial uptime: %s", node.Name, oldTime)

	sshKeyContent := "test adding authorized key without node reboot"

	nodeOS := helpers.GetOSReleaseForNode(t, cs, node).OS

	sshPaths := helpers.GetSSHPaths(nodeOS)

	t.Logf("Expecting SSH keys to be in %s", sshPaths.Expected)

	if sshPaths.Expected == constants.RHCOS9SSHKeyPath {
		// Write an SSH key to the old location on the node because the update process should remove this file.
		t.Logf("Writing SSH key to %s to ensure that it will be removed later", sshPaths.NotExpected)
		bashCmd := fmt.Sprintf("printf '%s' > %s", sshKeyContent, filepath.Join("/rootfs", sshPaths.NotExpected))
		helpers.ExecCmdOnNode(t, cs, node, "/bin/bash", "-c", bashCmd)
	}

	// Delete the expected SSH keys directory to ensure that the directories are
	// (re)created correctly by the MCD. This targets the upgrade case where that
	// directory may not previously exist.
	helpers.ExecCmdOnNode(t, cs, node, "rm", "-rf", filepath.Join("/rootfs", filepath.Dir(sshPaths.Expected)))

	// Adding authorized key for user core
	testIgnConfig := ctrlcommon.NewIgnConfig()
	testSSHKey := ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{ign3types.SSHAuthorizedKey(sshKeyContent)}}
	testIgnConfig.Passwd.Users = append(testIgnConfig.Passwd.Users, testSSHKey)

	addAuthorizedKey := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("authorized-key-%s", uuid.NewUUID()),
			Labels: helpers.MCLabelForRole("master"),
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
	renderedConfig, err := helpers.WaitForRenderedConfig(t, cs, "master", addAuthorizedKey.Name)
	require.Nil(t, err)
	err = waitForSingleNodePoolComplete(t, cs, "master", renderedConfig)
	require.Nil(t, err)

	// Re-fetch the master node for updated annotations
	node = helpers.GetSingleNodeByRole(t, cs, "master")

	assert.Equal(t, node.Annotations[constants.CurrentMachineConfigAnnotationKey], renderedConfig)
	assert.Equal(t, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)

	t.Logf("Expecting SSH keys to be in %s", sshPaths.Expected)

	helpers.AssertFileOnNode(t, cs, node, sshPaths.Expected)
	helpers.AssertFileNotOnNode(t, cs, node, sshPaths.NotExpected)

	foundSSHKey := helpers.ExecCmdOnNode(t, cs, node, "cat", filepath.Join("/rootfs", sshPaths.Expected))
	if !strings.Contains(foundSSHKey, sshKeyContent) {
		t.Fatalf("updated ssh keys not found in authorized_keys, got %s", foundSSHKey)
	}
	t.Logf("Node %s has SSH key", node.Name)

	output = helpers.ExecCmdOnNode(t, cs, node, "cat", "/rootfs/proc/uptime")
	newTime := strings.Split(output, " ")[0]

	// To ensure we didn't reboot, new uptime should be greater than old uptime
	uptimeOld, err := strconv.ParseFloat(oldTime, 64)
	require.Nil(t, err)

	uptimeNew, err := strconv.ParseFloat(newTime, 64)
	require.Nil(t, err)

	if uptimeOld > uptimeNew {
		t.Fatalf("Node %s rebooted uptime decreased from %f to %f", node.Name, uptimeOld, uptimeNew)
	}

	t.Logf("Node %s didn't reboot as expected, uptime increased from %f to %f ", node.Name, uptimeOld, uptimeNew)

	// Delete the applied authorized key MachineConfig to make sure rollback works fine without node reboot
	if err := cs.MachineConfigs().Delete(context.TODO(), addAuthorizedKey.Name, metav1.DeleteOptions{}); err != nil {
		t.Error(err)
	}

	t.Logf("Deleted MachineConfig %s", addAuthorizedKey.Name)

	// Wait for the mcp to rollback to previous config
	if err := waitForSingleNodePoolComplete(t, cs, "master", oldMasterRenderedConfig); err != nil {
		t.Fatal(err)
	}

	// Re-fetch the master node for updated annotations
	node = helpers.GetSingleNodeByRole(t, cs, "master")

	assert.Equal(t, node.Annotations[constants.CurrentMachineConfigAnnotationKey], oldMasterRenderedConfig)
	assert.Equal(t, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)

	foundSSHKey = helpers.ExecCmdOnNode(t, cs, node, "cat", filepath.Join("/rootfs", sshPaths.Expected))
	if strings.Contains(foundSSHKey, sshKeyContent) {
		t.Fatalf("Node %s did not rollback successfully", node.Name)
	}

	helpers.AssertFileOnNode(t, cs, node, sshPaths.Expected)
	helpers.AssertFileNotOnNode(t, cs, node, sshPaths.NotExpected)

	t.Logf("Node %s has successfully rolled back", node.Name)

	// Ensure that node didn't reboot during rollback
	output = helpers.ExecCmdOnNode(t, cs, node, "cat", "/rootfs/proc/uptime")
	newTime = strings.Split(output, " ")[0]

	uptimeNew, err = strconv.ParseFloat(newTime, 64)
	require.Nil(t, err)

	if uptimeOld > uptimeNew {
		t.Fatalf("Node %s rebooted during rollback, uptime decreased from %f to %f", node.Name, uptimeOld, uptimeNew)
	}

	t.Logf("Node %s didn't reboot as expected during rollback, uptime increased from %f to %f ", node.Name, uptimeOld, uptimeNew)
}

func TestRunShared(t *testing.T) {
	mcpName := "master"

	cs := framework.NewClientSet("")

	cleanupFuncs := helpers.NewCleanupFuncs()

	oldWorkerMCName := helpers.GetMcName(t, cs, mcpName)

	configOpts := e2eShared.ConfigDriftTestOpts{
		MCPName:       mcpName,
		ClientSet:     cs,
		SkipForcefile: true,
		SetupFunc: func(mc *mcfgv1.MachineConfig) {
			// Apply our MachineConfig and store the returned cleanup func
			cleanupFuncs.Add(helpers.ApplyMC(t, cs, mc))

			// Wait for the config to be rendered
			renderedConfigName, err := helpers.WaitForRenderedConfig(t, cs, mcpName, mc.Name)
			require.Nil(t, err)

			// Wait for the config to be rolled out
			require.Nil(t, waitForSingleNodePoolComplete(t, cs, mcpName, renderedConfigName))

			cleanupFuncs.Add(func() {
				// Wait for the pool to catch up
				require.Nil(t, waitForSingleNodePoolComplete(t, cs, mcpName, oldWorkerMCName))
			})
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

func waitForSingleNodePoolComplete(t *testing.T, cs *framework.ClientSet, pool, target string) error {
	var lastErr error
	startTime := time.Now()
	if err := wait.Poll(2*time.Second, 20*time.Minute, func() (bool, error) {
		err := helpers.WaitForPoolComplete(t, cs, pool, target)
		if err != nil {
			lastErr = err
			return false, nil
		}
		return true, nil
	}); err != nil {
		errs := kubeErrs.NewAggregate([]error{lastErr, err})
		if err == wait.ErrWaitTimeout {
			return fmt.Errorf("pool %s is still not updated, waited %v: %w", pool, time.Since(startTime), errs)
		} else {
			return fmt.Errorf("unknown error occurred: %w", errs)
		}
	}

	return nil
}
