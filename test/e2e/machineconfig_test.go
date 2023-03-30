package e2e_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
)

func TestRunMachineConfigTestCases(t *testing.T) {
	t.Parallel()

	cs := framework.NewClientSet("")

	t.Run("", func(t *testing.T) {
		t.Parallel()

		n, releaseFunc, err := nodeLeaser.GetNode(t, cs)
		require.NoError(t, err)
		t.Cleanup(releaseFunc)

		testCases := machineConfigTestCases{
			// The kernel type test must be run separately from the extensions test.
			testKernelType(cs),
		}

		testCases.run(t, cs, *n, "mc-testcase-1")
	})

	t.Run("", func(t *testing.T) {
		t.Parallel()

		n, releaseFunc, err := nodeLeaser.GetNode(t, cs)
		require.NoError(t, err)
		t.Cleanup(releaseFunc)

		testCases := machineConfigTestCases{
			testKernelArguments(cs),
			testDontDeleteRPMFiles(cs),
		}

		testCases.run(t, cs, *n, "mc-testcase-2")
	})

	t.Run("", func(t *testing.T) {
		t.Parallel()

		n, releaseFunc, err := nodeLeaser.GetNode(t, cs)
		require.NoError(t, err)
		t.Cleanup(releaseFunc)

		testCases := machineConfigTestCases{
			testIgn3Config(cs),
			testExtensions(t, cs),
		}

		testCases.run(t, cs, *n, "mc-testcase-3")
	})
}

func TestNoReboot(t *testing.T) {
	t.Parallel()
	cs := framework.NewClientSet("")

	n, releaseFunc, err := nodeLeaser.GetNode(t, cs)
	require.NoError(t, err)
	t.Cleanup(releaseFunc)

	node := *n

	testCases := machineConfigTestCases{
		testSSHNoReboot(t, cs, node),
		testPasswordNoReboot(t, cs, node),
	}

	testCases.run(t, cs, node, "test-noreboot")
}

func testDontDeleteRPMFiles(cs *framework.ClientSet) machineConfigTestCase {
	expectedContents := "mco-test"

	motdPath := "/etc/motd"

	return machineConfigTestCase{
		name: "Don't Delete RPM Files", // This name doesn't make sense for what this tests...
		mc:   createMCToAddFileForRole("modify-host-file", "infra", motdPath, expectedContents),
		applyAssert: func(t *testing.T, node corev1.Node) {
			helpers.AssertFileOnNodeContains(t, cs, node, motdPath, expectedContents)
		},
		rollbackAssert: func(t *testing.T, node corev1.Node) {
			helpers.AssertFileOnNodeNotContains(t, cs, node, motdPath, expectedContents)
			helpers.AssertFileOnNodeNotEmpty(t, cs, node, motdPath)
		},
	}
}

func testExtensions(t *testing.T, cs *framework.ClientSet) machineConfigTestCase {
	var expectedPackages []string

	isOKD, err := helpers.IsOKDCluster(cs)
	require.NoError(t, err)

	if isOKD {
		// OKD does not support grouped extensions yet, so installing kernel-devel will not also pull in kernel-headers
		// "sandboxed-containers" extension is not available on OKD
		// "kerberos" extension is not available on OKD
		expectedPackages = []string{"usbguard", "kernel-devel"}
	} else {
		expectedPackages = []string{"usbguard", "kernel-devel", "kernel-headers", "kata-containers", "krb5-workstation", "libkadm5"}
	}

	mc := &mcfgv1.MachineConfig{
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

	applyAssert := func(t *testing.T, node corev1.Node) {
		args := append([]string{"chroot", "/rootfs", "rpm", "-q"}, expectedPackages...)
		installedPackages := helpers.ExecCmdOnNode(t, cs, node, args...)

		for _, v := range expectedPackages {
			if !strings.Contains(installedPackages, v) {
				t.Fatalf("Node %s doesn't have expected extensions", node.Name)
			}
		}

		t.Logf("Node %s has expected extensions installed", node.Name)
	}

	rollbackAssert := func(t *testing.T, node corev1.Node) {
		args := append([]string{"chroot", "/rootfs", "rpm", "-qa"}, expectedPackages...)
		installedPackages := helpers.ExecCmdOnNode(t, cs, node, args...)

		for _, v := range expectedPackages {
			if strings.Contains(installedPackages, v) {
				t.Fatalf("Node %s did not rollback successfully", node.Name)
			}
		}

		t.Logf("Node %s has successfully rolled back", node.Name)
	}

	return machineConfigTestCase{
		mc:             mc,
		name:           "Extensions",
		applyAssert:    applyAssert,
		rollbackAssert: rollbackAssert,
	}
}

func testKernelArguments(cs *framework.ClientSet) machineConfigTestCase {
	kernelArgs := []string{"nosmt", "foo=bar", "foo=baz", " baz=test bar=hello world"}

	mc := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("kargs-%s", uuid.NewUUID()),
			Labels: helpers.MCLabelForRole("infra"),
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
			},
			KernelArguments: kernelArgs,
		},
	}

	applyAssert := func(t *testing.T, node corev1.Node) {
		kargs := helpers.ExecCmdOnNode(t, cs, node, "cat", "/rootfs/proc/cmdline")
		for _, v := range kernelArgs {
			if !strings.Contains(kargs, v) {
				t.Fatalf("Missing %q in kargs: %q", v, kargs)
			}
		}
		t.Logf("Node %s has expected kargs", node.Name)
	}

	rollbackAssert := func(t *testing.T, node corev1.Node) {
		kargs := helpers.ExecCmdOnNode(t, cs, node, "cat", "/rootfs/proc/cmdline")
		for _, v := range kernelArgs {
			if strings.Contains(kargs, v) {
				t.Fatalf("Found unexpected kernel arg %q", v)
			}
		}
		t.Logf("Node %s has no unexpected kargs", node.Name)
	}

	return machineConfigTestCase{
		name:           "Kernel Arguments",
		mc:             mc,
		applyAssert:    applyAssert,
		rollbackAssert: rollbackAssert,
	}
}

func testKernelType(cs *framework.ClientSet) machineConfigTestCase {
	mc := &mcfgv1.MachineConfig{
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

	applyAssert := func(t *testing.T, node corev1.Node) {
		kernelInfo := helpers.ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "rpm", "-qa", "kernel-rt-core")
		if !strings.Contains(kernelInfo, "kernel-rt-core") {
			t.Fatalf("Node %s doesn't have expected kernel", node.Name)
		}
		t.Logf("Node %s has expected kernel", node.Name)
	}

	rollbackAssert := func(t *testing.T, node corev1.Node) {
		kernelInfo := helpers.ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "rpm", "-qa", "kernel-rt-core")
		if strings.Contains(kernelInfo, "kernel-rt-core") {
			t.Fatalf("Node %s did not rollback successfully", node.Name)
		}
		t.Logf("Node %s has successfully rolled back", node.Name)
	}

	return machineConfigTestCase{
		mc:             mc,
		name:           "Kernel Type",
		applyAssert:    applyAssert,
		rollbackAssert: rollbackAssert,
		skipOnOKD:      true,
	}
}

func testIgn3Config(cs *framework.ClientSet) machineConfigTestCase {
	sshContents := "1234_test_ign3"
	fileContents := "test-ign3-stuff"

	// create a dummy MC with an sshKey for user Core
	mcName := fmt.Sprintf("99-ign3cfg-infra-%s", uuid.NewUUID())
	mcadd := &mcfgv1.MachineConfig{}
	mcadd.ObjectMeta = metav1.ObjectMeta{
		Name:   mcName,
		Labels: helpers.MCLabelForRole("infra"),
	}
	// create a new MC that adds a valid user & ssh key
	testIgn3Config := ign3types.Config{}
	tempUser := ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{ign3types.SSHAuthorizedKey(sshContents)}}
	testIgn3Config.Passwd.Users = append(testIgn3Config.Passwd.Users, tempUser)
	testIgn3Config.Ignition.Version = "3.2.0"
	mode := 420
	testfiledata := fmt.Sprintf("data:,%s", fileContents)
	tempFile := ign3types.File{Node: ign3types.Node{Path: "/etc/testfileconfig"},
		FileEmbedded1: ign3types.FileEmbedded1{Contents: ign3types.Resource{Source: &testfiledata}, Mode: &mode}}
	testIgn3Config.Storage.Files = append(testIgn3Config.Storage.Files, tempFile)
	rawIgnConfig := helpers.MarshalOrDie(testIgn3Config)
	mcadd.Spec.Config.Raw = rawIgnConfig

	applyAssert := func(t *testing.T, node corev1.Node) {
		sshPaths := helpers.GetSSHPaths(helpers.GetOSReleaseForNode(t, cs, node).OS)

		foundSSH := helpers.ExecCmdOnNode(t, cs, node, "cat", filepath.Join("/rootfs", sshPaths.Expected))
		if !strings.Contains(foundSSH, sshContents) {
			t.Fatalf("updated ssh keys not found in authorized_keys, got %s", foundSSH)
		}
		t.Logf("Node %s has SSH key", node.Name)

		foundFile := helpers.ExecCmdOnNode(t, cs, node, "cat", "/rootfs/etc/testfileconfig")
		if !strings.Contains(foundFile, fileContents) {
			t.Fatalf("updated file doesn't contain expected data, got %s", foundFile)
		}
		t.Logf("Node %s has file", node.Name)
	}

	rollbackAssert := func(t *testing.T, node corev1.Node) {
		sshPaths := helpers.GetSSHPaths(helpers.GetOSReleaseForNode(t, cs, node).OS)

		foundSSH := helpers.ExecCmdOnNode(t, cs, node, "cat", filepath.Join("/rootfs", sshPaths.Expected))
		if strings.Contains(foundSSH, sshContents) {
			t.Fatalf("unexpected ssh key %q found in authorized_keys, got %s", sshContents, foundSSH)
		}
		t.Logf("Node %s does not have SSH key %q (this is expected)", node.Name, sshContents)

		helpers.AssertFileNotOnNode(t, cs, node, filepath.Join("/rootfs", "/etc/testfileconfig"))
	}

	return machineConfigTestCase{
		name:           "Ign3Config", // Not sure why we even need this test since it seems to be covered otherwise
		applyAssert:    applyAssert,
		rollbackAssert: rollbackAssert,
		mc:             mcadd,
	}
}

func testPasswordNoReboot(t *testing.T, cs *framework.ClientSet, node corev1.Node) machineConfigTestCase {
	testIgnConfig := ctrlcommon.NewIgnConfig()
	testPasswdHash := "testpass"

	testIgnConfig.Passwd.Users = []ign3types.PasswdUser{
		{
			Name:         "core",
			PasswordHash: &testPasswdHash,
		},
	}

	mc := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("user-password-%s", uuid.NewUUID()),
			Labels: helpers.MCLabelForRole("infra"),
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: helpers.MarshalOrDie(testIgnConfig),
			},
		},
	}

	// Get the initial node uptime
	initialUptime := helpers.GetNodeUptime(t, cs, node)
	t.Logf("Node %s initial uptime: %f", node.Name, initialUptime)
	initialEtcShadowContents := helpers.ExecCmdOnNode(t, cs, node, "grep", "^core:", "/rootfs/etc/shadow")

	applyAssert := func(t *testing.T, node corev1.Node) {
		currentEtcShadowContents := helpers.ExecCmdOnNode(t, cs, node, "grep", "^core:", "/rootfs/etc/shadow")

		if currentEtcShadowContents == initialEtcShadowContents {
			t.Fatalf("updated password hash not found in etc/shadow, got %s", currentEtcShadowContents)
		}

		t.Logf("Node %s has Password Hash", node.Name)
	}

	rollbackAssert := func(t *testing.T, node corev1.Node) {
		rollbackEtcShadowContents := helpers.ExecCmdOnNode(t, cs, node, "grep", "^core:", "/rootfs/etc/shadow")
		assert.Equal(t, initialEtcShadowContents, rollbackEtcShadowContents)

		// Ensure that node didn't reboot during rollback
		helpers.AssertNodeNotReboot(t, cs, node, initialUptime)
	}

	return machineConfigTestCase{
		name:           "Password",
		mc:             mc,
		applyAssert:    applyAssert,
		rollbackAssert: rollbackAssert,
	}
}

func testSSHNoReboot(t *testing.T, cs *framework.ClientSet, node corev1.Node) machineConfigTestCase {
	sshKeyContent := "ssh-rsa 12345"

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
	// directory may not previously exist. Note: This will need to be revisited
	// once Config Drift Monitor is aware of SSH keys.
	helpers.ExecCmdOnNode(t, cs, node, "rm", "-rf", filepath.Join("/rootfs", filepath.Dir(sshPaths.Expected)))

	// Get the initial uptime for future comparison.
	initialUptime := helpers.GetNodeUptime(t, cs, node)
	t.Logf("Node %s initial uptime: %f", node.Name, initialUptime)

	applyAssert := func(t *testing.T, node corev1.Node) {
		helpers.AssertFileOnNode(t, cs, node, sshPaths.Expected)
		helpers.AssertFileNotOnNode(t, cs, node, sshPaths.NotExpected)

		foundSSHKey := helpers.ExecCmdOnNode(t, cs, node, "cat", filepath.Join("/rootfs", sshPaths.Expected))
		if !strings.Contains(foundSSHKey, sshKeyContent) {
			t.Fatalf("updated ssh keys not found in authorized_keys, got %s", foundSSHKey)
		}
		t.Logf("Node %s has SSH key", node.Name)

		assertExpectedPerms(t, cs, node, "/home/core/.ssh", []string{constants.CoreUserName, constants.CoreGroupName, "700"})

		if sshPaths.Expected == constants.RHCOS9SSHKeyPath {
			// /home/core/.ssh/authorized_keys.d
			assertExpectedPerms(t, cs, node, filepath.Dir(constants.RHCOS9SSHKeyPath), []string{constants.CoreUserName, constants.CoreGroupName, "700"})
		}

		assertExpectedPerms(t, cs, node, sshPaths.Expected, []string{constants.CoreUserName, constants.CoreGroupName, "600"})
	}

	rollbackAssert := func(t *testing.T, node corev1.Node) {
		foundSSHKey := helpers.ExecCmdOnNode(t, cs, node, "cat", filepath.Join("/rootfs", sshPaths.Expected))
		if strings.Contains(foundSSHKey, sshKeyContent) {
			t.Fatalf("Node %s did not rollback successfully", node.Name)
		}

		helpers.AssertFileOnNode(t, cs, node, sshPaths.Expected)
		helpers.AssertFileNotOnNode(t, cs, node, sshPaths.NotExpected)

		t.Logf("Node %s has successfully rolled back", node.Name)

		// Ensure that node didn't reboot during rollback
		helpers.AssertNodeNotReboot(t, cs, node, initialUptime)
	}

	mcName := fmt.Sprintf("authorized-key-%s", uuid.NewUUID())

	return machineConfigTestCase{
		name:           "SSH key",
		mc:             getSSHMachineConfig(mcName, "infra", sshKeyContent),
		applyAssert:    applyAssert,
		rollbackAssert: rollbackAssert,
	}
}

// Checks that a file or directory on a given node matches the expected
// permissions in the form of [username, groupname, octal file permissions].
func assertExpectedPerms(t *testing.T, cs *framework.ClientSet, node corev1.Node, path string, expectedPerms []string) {
	t.Helper()

	actualPerms := strings.Split(strings.TrimSuffix(helpers.ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "stat", "--format=%U %G %a", path), "\n"), " ")
	assert.Equal(t, expectedPerms, actualPerms, "expected %s to have perms %v, got: %v", path, expectedPerms, actualPerms)
}

func getSSHMachineConfig(mcName, mcpName, sshKeyContent string) *mcfgv1.MachineConfig {
	// Adding authorized key for user core
	testIgnConfig := ctrlcommon.NewIgnConfig()

	testIgnConfig.Passwd.Users = []ign3types.PasswdUser{
		{
			Name:              "core",
			SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{ign3types.SSHAuthorizedKey(sshKeyContent)},
		},
	}

	return &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   mcName,
			Labels: helpers.MCLabelForRole(mcpName),
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: helpers.MarshalOrDie(testIgnConfig),
			},
		},
	}
}
