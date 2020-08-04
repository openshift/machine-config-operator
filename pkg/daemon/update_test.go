package daemon

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	ign3types "github.com/coreos/ignition/v2/config/v3_1/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

// TestUpdateOS verifies the return errors from attempting to update the OS follow expectations
func TestUpdateOS(t *testing.T) {
	// expectedError is the error we will use when expecting an error to return
	expectedError := fmt.Errorf("broken")

	// testClient is the NodeUpdaterClient mock instance that will front
	// calls to update the host.
	testClient := RpmOstreeClientMock{
		GetBootedOSImageURLReturns: []GetBootedOSImageURLReturn{},
	}

	// Create a Daemon instance with mocked clients
	d := Daemon{
		mock:              true,
		name:              "nodeName",
		OperatingSystem:   MachineConfigDaemonOSRHCOS,
		NodeUpdaterClient: testClient,
		kubeClient:        k8sfake.NewSimpleClientset(),
		bootedOSImageURL:  "test",
	}

	// Set up machineconfigs to pass to updateOS.
	mcfg := &mcfgv1.MachineConfig{}
	// differentMcfg has a different OSImageURL so it will force Daemon.UpdateOS
	// to trigger an update of the operatingsystem (as fronted by our testClient)
	differentMcfg := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			OSImageURL: "somethingDifferent",
		},
	}

	// This should be a no-op
	if err := d.updateOS(mcfg, ""); err != nil {
		t.Errorf("Expected no error. Got %s.", err)
	}
	// Second call should return an error
	if err := d.updateOS(differentMcfg, ""); err == expectedError {
		t.Error("Expected an error. Got none.")
	}
}

// TestReconcilable attempts to verify the conditions in which configs would and would not be
// reconcilable. Welcome to the longest unittest you've ever read.
func TestReconcilable(t *testing.T) {
	oldIgnCfg := ctrlcommon.NewIgnConfig()
	// oldConfig is the current config of the fake system
	oldConfig := helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
	newIgnCfg := ctrlcommon.NewIgnConfig()

	// Set improper version
	newIgnCfg.Ignition.Version = "4.0.0"

	// newConfig is the config that is being requested to apply to the system
	newConfig := helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	// Verify Ignition version mismatch react as expected
	_, isReconcilable := reconcilable(oldConfig, newConfig)
	checkIrreconcilableResults(t, "Ignition", isReconcilable)
	//reset to proper Ignition version
	newIgnCfg.Ignition.Version = ign3types.MaxVersion.String()
	newConfig = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	_, isReconcilable = reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "Ignition", isReconcilable)

	// Verify Disk changes react as expected
	oldIgnCfg.Storage.Disks = []ign3types.Disk{
		ign3types.Disk{
			Device: "/one",
		},
	}
	oldConfig = helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
	_, isReconcilable = reconcilable(oldConfig, newConfig)
	checkIrreconcilableResults(t, "Disk", isReconcilable)

	// Match storage disks
	newIgnCfg.Storage.Disks = oldIgnCfg.Storage.Disks
	newConfig = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	_, isReconcilable = reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "Disk", isReconcilable)

	// Verify Filesystems changes react as expected
	oldIgnCfg.Storage.Filesystems = []ign3types.Filesystem{
		ign3types.Filesystem{
			Device: "/dev/sda1",
			Format: helpers.StrToPtr("ext4"),
			Path:   helpers.StrToPtr("/foo/bar"),
		},
	}
	oldConfig = helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
	_, isReconcilable = reconcilable(oldConfig, newConfig)
	checkIrreconcilableResults(t, "Filesystem", isReconcilable)

	// Match Storage filesystems
	newIgnCfg.Storage.Filesystems = oldIgnCfg.Storage.Filesystems
	newConfig = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	_, isReconcilable = reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "Filesystem", isReconcilable)

	// Verify Raid changes react as expected
	oldIgnCfg.Storage.Raid = []ign3types.Raid{
		ign3types.Raid{
			Name:  "data",
			Level: "stripe",
		},
	}
	oldConfig = helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
	_, isReconcilable = reconcilable(oldConfig, newConfig)
	checkIrreconcilableResults(t, "Raid", isReconcilable)

	// Match storage raid
	newIgnCfg.Storage.Raid = oldIgnCfg.Storage.Raid
	newConfig = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	_, isReconcilable = reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "Raid", isReconcilable)

	// Verify Passwd Groups changes unsupported
	oldIgnCfg = ctrlcommon.NewIgnConfig()
	oldConfig = helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
	newIgnCfg = ctrlcommon.NewIgnConfig()
	newConfig = helpers.CreateMachineConfigFromIgnition(newIgnCfg)

	_, isReconcilable = reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "PasswdGroups", isReconcilable)

	tempGroup := ign3types.PasswdGroup{}
	tempGroup.Name = "testGroup"
	newIgnCfg.Passwd.Groups = []ign3types.PasswdGroup{tempGroup}
	newConfig = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	_, isReconcilable = reconcilable(oldConfig, newConfig)
	checkIrreconcilableResults(t, "PasswdGroups", isReconcilable)
}

func TestMachineConfigDiff(t *testing.T) {
	oldIgnCfg := ctrlcommon.NewIgnConfig()
	oldConfig := helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
	oldConfig.ObjectMeta = metav1.ObjectMeta{Name: "oldconfig"}
	newIgnCfg := ctrlcommon.NewIgnConfig()
	newConfig := helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	newConfig.ObjectMeta = metav1.ObjectMeta{Name: "newconfig"}
	diff, err := newMachineConfigDiff(oldConfig, newConfig)
	assert.Nil(t, err)
	assert.True(t, diff.isEmpty())

	newConfig.Spec.OSImageURL = "quay.io/example/foo@sha256:b5bb9d8014a0f9b1d61e21e796d78dccdf1352f23cd32812f4850b878ae4944c"
	diff, err = newMachineConfigDiff(oldConfig, newConfig)
	assert.Nil(t, err)
	assert.False(t, diff.isEmpty())
	assert.True(t, diff.osUpdate)

	emptyMc := canonicalizeEmptyMC(nil)
	otherEmptyMc := canonicalizeEmptyMC(nil)
	emptyMc.Spec.KernelArguments = nil
	otherEmptyMc.Spec.KernelArguments = []string{}
	diff, err = newMachineConfigDiff(emptyMc, otherEmptyMc)
	assert.Nil(t, err)
	assert.True(t, diff.isEmpty())
}

func newTestIgnitionFile(i uint) ign3types.File {
	mode := 0644
	return ign3types.File{Node: ign3types.Node{Path: fmt.Sprintf("/etc/config%d", i)},
		FileEmbedded1: ign3types.FileEmbedded1{Contents: ign3types.Resource{Source: helpers.StrToPtr(fmt.Sprintf("data:,config%d", i))}, Mode: &mode}}
}

func newMachineConfigFromFiles(files []ign3types.File) *mcfgv1.MachineConfig {
	newIgnCfg := ctrlcommon.NewIgnConfig()
	newIgnCfg.Storage.Files = files
	newConfig := helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	return newConfig
}

func TestReconcilableDiff(t *testing.T) {
	var oldFiles []ign3types.File
	nOldFiles := uint(10)
	for i := uint(0); i < nOldFiles; i++ {
		oldFiles = append(oldFiles, newTestIgnitionFile(uint(i)))
	}
	oldConfig := newMachineConfigFromFiles(oldFiles)
	newConfig := newMachineConfigFromFiles(append(oldFiles, newTestIgnitionFile(nOldFiles+1)))

	diff, err := reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "add file", err)
	assert.Equal(t, diff.osUpdate, false)
	assert.Equal(t, diff.passwd, false)
	assert.Equal(t, diff.units, false)
	assert.Equal(t, diff.files, true)

	newConfig = newMachineConfigFromFiles(nil)
	diff, err = reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "remove all files", err)
	assert.Equal(t, diff.osUpdate, false)
	assert.Equal(t, diff.passwd, false)
	assert.Equal(t, diff.units, false)
	assert.Equal(t, diff.files, true)

	newConfig = newMachineConfigFromFiles(oldFiles)
	newConfig.Spec.OSImageURL = "example.com/machine-os-content:new"
	diff, err = reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "os update", err)
	assert.Equal(t, diff.osUpdate, true)
	assert.Equal(t, diff.passwd, false)
	assert.Equal(t, diff.units, false)
	assert.Equal(t, diff.files, false)
}

func TestKernelAguments(t *testing.T) {
	oldIgnCfg := ctrlcommon.NewIgnConfig()
	oldMcfg := helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
	oldMcfg.Spec.KernelArguments = []string{"nosmt", "foo", "baz=test"}
	tests := []struct {
		args      []string
		deletions []string
		additions []string
	}{
		{
			args:      nil,
			deletions: []string{"nosmt", "foo", "baz=test"},
			additions: []string{},
		},
		{
			args:      oldMcfg.Spec.KernelArguments,
			deletions: []string{},
			additions: []string{},
		},
		{
			args:      append(oldMcfg.Spec.KernelArguments, "hello=world"),
			deletions: []string{},
			additions: []string{"hello=world"},
		},
		{
			args:      []string{"foo", "hello=world"},
			deletions: []string{"nosmt", "baz=test"},
			additions: []string{"hello=world"},
		},
		{
			args:      []string{"foo", "bar=1 hello=world"},
			deletions: []string{"nosmt", "baz=test"},
			additions: []string{"bar=1", "hello=world"},
		},
		{
			args:      []string{"foo", " baz=test bar=\"hello world\""},
			deletions: []string{"nosmt"},
			additions: []string{"bar=\"hello world\""},
		},
		{
			args:      append([]string{"foo=baz", "foo=bar"}, oldMcfg.Spec.KernelArguments...),
			deletions: []string{},
			additions: []string{"foo=baz", "foo=bar"},
		},
		{
			args:      append([]string{"baz=othertest"}, oldMcfg.Spec.KernelArguments...),
			deletions: []string{},
			additions: []string{"baz=othertest"},
		},
	}
	rand.Seed(time.Now().UnixNano())
	for idx, test := range tests {
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			newIgnCfg := ctrlcommon.NewIgnConfig()
			newMcfg := helpers.CreateMachineConfigFromIgnition(newIgnCfg)
			newMcfg.Spec.KernelArguments = test.args
			// Randomize the order of both old/new to sort out ordering issues.
			rand.Shuffle(len(test.args), func(i, j int) {
				test.args[i], test.args[j] = test.args[j], test.args[i]
			})
			oldKargs := oldMcfg.Spec.KernelArguments
			rand.Shuffle(len(oldMcfg.Spec.KernelArguments), func(i, j int) {
				oldKargs[i], oldKargs[j] = oldKargs[j], oldKargs[i]
			})
			res := generateKargsCommand(oldMcfg, newMcfg)
			additionsExpected := make(map[string]bool)
			for _, k := range test.additions {
				additionsExpected[k] = true
			}
			deletionsExpected := make(map[string]bool)
			for _, k := range test.deletions {
				deletionsExpected[k] = true
			}
			for _, a := range res {
				parts := strings.SplitN(a, "=", 2)
				if len(parts) != 2 {
					t.Fatalf("Bad karg %v", a)
				}
				arg := parts[1]
				if parts[0] == "--append" {
					if !additionsExpected[arg] {
						t.Errorf("Unexpected addition %s", arg)
					}
				} else if parts[0] == "--delete" {
					if !deletionsExpected[arg] {
						t.Errorf("Unexpected deletion %s", arg)
					}
				} else {
					t.Fatalf("Bad karg %s", a)
				}
			}
		})
	}
}

func TestReconcilableSSH(t *testing.T) {
	// Check that updating SSH Key of user core supported
	oldIgnCfg := ctrlcommon.NewIgnConfig()
	oldMcfg := helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
	tempUser1 := ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"5678", "abc"}}
	newIgnCfg := ctrlcommon.NewIgnConfig()
	newIgnCfg.Passwd.Users = []ign3types.PasswdUser{tempUser1}
	newMcfg := helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	_, errMsg := reconcilable(oldMcfg, newMcfg)
	checkReconcilableResults(t, "SSH", errMsg)

	// 	Check that updating User with User that is not core is not supported
	tempUser2 := ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"1234"}}
	oldIgnCfg.Passwd.Users = append(oldIgnCfg.Passwd.Users, tempUser2)
	oldMcfg = helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
	tempUser3 := ign3types.PasswdUser{Name: "another user", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"5678"}}
	newIgnCfg.Passwd.Users[0] = tempUser3
	newMcfg = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	_, errMsg = reconcilable(oldMcfg, newMcfg)
	checkIrreconcilableResults(t, "SSH", errMsg)

	// check that we cannot make updates if any other Passwd.User field is changed.
	tempUser4 := ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"5678"}, HomeDir: helpers.StrToPtr("somedir")}
	newIgnCfg.Passwd.Users[0] = tempUser4
	newMcfg = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	_, errMsg = reconcilable(oldMcfg, newMcfg)
	checkIrreconcilableResults(t, "SSH", errMsg)

	// check that we cannot add a user or have len(Passwd.Users)> 1
	tempUser5 := ign3types.PasswdUser{Name: "some user", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"5678"}}
	newIgnCfg.Passwd.Users = append(newIgnCfg.Passwd.Users, tempUser5)
	newMcfg = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	_, errMsg = reconcilable(oldMcfg, newMcfg)
	checkIrreconcilableResults(t, "SSH", errMsg)

	// check that user is not attempting to remove the only sshkey from core user
	tempUser6 := ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{}}
	newIgnCfg.Passwd.Users[0] = tempUser6
	newIgnCfg.Passwd.Users = newIgnCfg.Passwd.Users[:len(newIgnCfg.Passwd.Users)-1]
	newMcfg = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	_, errMsg = reconcilable(oldMcfg, newMcfg)
	checkIrreconcilableResults(t, "SSH", errMsg)

	//check that empty Users does not generate error/degrade node
	newIgnCfg.Passwd.Users = nil
	newMcfg = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	_, errMsg = reconcilable(oldMcfg, newMcfg)
	checkReconcilableResults(t, "SSH", errMsg)
}

func TestUpdateSSHKeys(t *testing.T) {
	// testClient is the NodeUpdaterClient mock instance that will front
	// calls to update the host.
	testClient := RpmOstreeClientMock{
		GetBootedOSImageURLReturns: []GetBootedOSImageURLReturn{},
	}

	// Create a Daemon instance with mocked clients
	d := Daemon{
		mock:              true,
		name:              "nodeName",
		OperatingSystem:   MachineConfigDaemonOSRHCOS,
		NodeUpdaterClient: testClient,
		kubeClient:        k8sfake.NewSimpleClientset(),
		bootedOSImageURL:  "test",
	}
	// Set up machineconfigs that are identical except for SSH keys
	tempUser := ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"1234", "4567"}}
	newIgnCfg := ctrlcommon.NewIgnConfig()
	newIgnCfg.Passwd.Users = []ign3types.PasswdUser{tempUser}
	err := d.updateSSHKeys(newIgnCfg.Passwd.Users)
	if err != nil {
		t.Errorf("Expected no error. Got %s.", err)

	}

	// if Users is empty, nothing should happen and no error should ever be generated
	newIgnCfg2 := ctrlcommon.NewIgnConfig()
	newIgnCfg2.Passwd.Users = []ign3types.PasswdUser{}
	err = d.updateSSHKeys(newIgnCfg2.Passwd.Users)
	if err != nil {
		t.Errorf("Expected no error. Got: %s", err)
	}
}

// This test should fail until Ignition validation enabled.
// Ignition validation does not permit writing files to relative paths.
func TestInvalidIgnConfig(t *testing.T) {
	oldIgnConfig := ctrlcommon.NewIgnConfig()
	oldMcfg := helpers.CreateMachineConfigFromIgnition(oldIgnConfig)

	// create file to write that contains an impermissable relative path
	tempFileContents := ign3types.Resource{Source: helpers.StrToPtr("data:,hello%20world%0A")}
	tempMode := 420
	newIgnConfig := ctrlcommon.NewIgnConfig()
	newIgnFile := ign3types.File{
		Node:          ign3types.Node{Path: "home/core/test"},
		FileEmbedded1: ign3types.FileEmbedded1{Contents: tempFileContents, Mode: &tempMode},
	}
	newIgnConfig.Storage.Files = append(newIgnConfig.Storage.Files, newIgnFile)
	newMcfg := helpers.CreateMachineConfigFromIgnition(newIgnConfig)
	_, err := reconcilable(oldMcfg, newMcfg)
	assert.NotNil(t, err, "Expected error. Relative Paths should fail general ignition validation")

	newIgnConfig.Storage.Files[0].Node.Path = "/home/core/test"
	newMcfg = helpers.CreateMachineConfigFromIgnition(newIgnConfig)
	diff, err := reconcilable(oldMcfg, newMcfg)
	assert.Nil(t, err, "Expected no error. Absolute paths should not fail general ignition validation")
	assert.Equal(t, diff.files, true)
}

// checkReconcilableResults is a shortcut for verifying results that should be reconcilable
func checkReconcilableResults(t *testing.T, key string, reconcilableError error) {
	if reconcilableError != nil {
		t.Errorf("%s values should be reconcilable. Received error: %v", key, reconcilableError)
	}
}

// checkIrreconcilableResults is a shortcut for verifing results that should be irreconcilable
func checkIrreconcilableResults(t *testing.T, key string, reconcilableError error) {
	if reconcilableError == nil {
		t.Errorf("Different %s values should not be reconcilable.", key)
	}
}
