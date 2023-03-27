package e2e_shared_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
)

// This test asserts that the following MachineConfig cases will not cause the node to reboot:
// - If the core user password changes.
// - If the core user SSH keys change.
type noRebootTest struct{}

func (n *noRebootTest) Setup(t *testing.T) {}

func (n *noRebootTest) Teardown(t *testing.T) {}

func (n *noRebootTest) Run(t *testing.T) {
	cs := framework.NewClientSet("")

	node := helpers.GetRandomNode(t, cs, "worker")

	testCases := helpers.MachineConfigTestCases{
		testSSHNoReboot(t, cs, node),
		testPasswordNoReboot(t, cs, node),
	}

	testCases.Run(t, cs, node, "test-noreboot")
}

// Sets up the core user password test case.
func testPasswordNoReboot(t *testing.T, cs *framework.ClientSet, node corev1.Node) helpers.MachineConfigTestCase {
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

	return helpers.MachineConfigTestCase{
		Name:           "Password",
		MC:             mc,
		ApplyAssert:    applyAssert,
		RollbackAssert: rollbackAssert,
	}
}

// Sets up the core user SSH keus test case.
func testSSHNoReboot(t *testing.T, cs *framework.ClientSet, node corev1.Node) helpers.MachineConfigTestCase {
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

	return helpers.MachineConfigTestCase{
		Name:           "SSH key",
		MC:             getSSHMachineConfig(mcName, "infra", sshKeyContent),
		ApplyAssert:    applyAssert,
		RollbackAssert: rollbackAssert,
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
