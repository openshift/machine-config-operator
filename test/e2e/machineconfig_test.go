package e2e_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
)

func testDontDeleteRPMFiles(cs *framework.ClientSet) helpers.MachineConfigTestCase {
	expectedContents := "mco-test"

	motdPath := "/etc/motd"

	return helpers.MachineConfigTestCase{
		Name: "Don't Delete RPM Files", // This name doesn't make sense for what this tests...
		MC:   createMCToAddFileForRole("modify-host-file", "infra", motdPath, expectedContents),
		ApplyAssert: func(t *testing.T, node corev1.Node) {
			helpers.AssertFileOnNodeContains(t, cs, node, motdPath, expectedContents)
		},
		RollbackAssert: func(t *testing.T, node corev1.Node) {
			helpers.AssertFileOnNodeNotContains(t, cs, node, motdPath, expectedContents)
			helpers.AssertFileOnNodeNotEmpty(t, cs, node, motdPath)
		},
	}
}

func testIgn3Config(cs *framework.ClientSet) helpers.MachineConfigTestCase {
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

	return helpers.MachineConfigTestCase{
		Name:           "Ign3Config", // Not sure why we even need this test since it seems to be covered otherwise
		ApplyAssert:    applyAssert,
		RollbackAssert: rollbackAssert,
		MC:             mcadd,
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
