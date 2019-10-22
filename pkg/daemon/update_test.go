package daemon

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	igntypes "github.com/coreos/ignition/v2/config/v3_0/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
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
		RunPivotReturns: []error{
			// First run will return no error
			nil,
			// Second rrun will return our expected error
			expectedError},
	}

	// Create a Daemon instance with mocked clients
	d := Daemon{
		mock:              true,
		name:              "nodeName",
		OperatingSystem:   machineConfigDaemonOSRHCOS,
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
	if err := d.updateOS(mcfg); err != nil {
		t.Errorf("Expected no error. Got %s.", err)
	}
	// Second call should return an error
	if err := d.updateOS(differentMcfg); err == expectedError {
		t.Error("Expected an error. Got none.")
	}
}

// TestReconcilable attempts to verify the conditions in which configs would and would not be
// reconcilable. Welcome to the longest unittest you've ever read.
func TestReconcilable(t *testing.T) {
	// oldConfig is the current config of the fake system
	oldConfig := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			Config: ctrlcommon.NewIgnConfig(),
		},
	}

	// newConfig is the config that is being requested to apply to the system
	newConfig := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			Config: ctrlcommon.NewIgnConfig(),
		},
	}

	// Verify Ignition version mismatch react as expected
	newConfig.Spec.Config.Ignition.Version = "2.0.0"
	_, isReconcilable := Reconcilable(oldConfig, newConfig)
	checkIrreconcilableResults(t, "Ignition", isReconcilable)
	//reset to proper Ignition version
	newConfig.Spec.Config.Ignition.Version = igntypes.MaxVersion.String()
	_, isReconcilable = Reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "Ignition", isReconcilable)

	// Verify Disk changes react as expected
	oldConfig.Spec.Config.Storage.Disks = []igntypes.Disk{
		igntypes.Disk{
			Device: "/one",
		},
	}

	_, isReconcilable = Reconcilable(oldConfig, newConfig)
	checkIrreconcilableResults(t, "Disk", isReconcilable)

	// Match storage disks
	newConfig.Spec.Config.Storage.Disks = oldConfig.Spec.Config.Storage.Disks
	_, isReconcilable = Reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "Disk", isReconcilable)

	// Verify Filesystems changes react as expected
	oldFSPath := "/foo/bar"
	oldConfig.Spec.Config.Storage.Filesystems = []igntypes.Filesystem{
		igntypes.Filesystem{
			Path: &oldFSPath,
		},
	}

	_, isReconcilable = Reconcilable(oldConfig, newConfig)
	checkIrreconcilableResults(t, "Filesystem", isReconcilable)

	// Match Storage filesystems
	newConfig.Spec.Config.Storage.Filesystems = oldConfig.Spec.Config.Storage.Filesystems
	_, isReconcilable = Reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "Filesystem", isReconcilable)

	// Verify Raid changes react as expected
	oldConfig.Spec.Config.Storage.Raid = []igntypes.Raid{
		igntypes.Raid{
			Name:  "data",
			Level: "stripe",
		},
	}

	_, isReconcilable = Reconcilable(oldConfig, newConfig)
	checkIrreconcilableResults(t, "Raid", isReconcilable)

	// Match storage raid
	newConfig.Spec.Config.Storage.Raid = oldConfig.Spec.Config.Storage.Raid
	_, isReconcilable = Reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "Raid", isReconcilable)

	// Verify Passwd Groups changes unsupported
	oldConfig = &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			Config: ctrlcommon.NewIgnConfig(),
		},
	}
	newConfig = &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			Config: ctrlcommon.NewIgnConfig(),
		},
	}

	_, isReconcilable = Reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "PasswdGroups", isReconcilable)

	tempGroup := igntypes.PasswdGroup{}
	tempGroup.Name = "testGroup"
	newConfig.Spec.Config.Passwd.Groups = []igntypes.PasswdGroup{tempGroup}

	_, isReconcilable = Reconcilable(oldConfig, newConfig)
	checkIrreconcilableResults(t, "PasswdGroups", isReconcilable)
}

func TestMachineConfigDiff(t *testing.T) {
	oldConfig := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "oldconfig"},
		Spec: mcfgv1.MachineConfigSpec{
			Config: ctrlcommon.NewIgnConfig(),
		},
	}
	newConfig := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "newconfig"},
		Spec: mcfgv1.MachineConfigSpec{
			Config: ctrlcommon.NewIgnConfig(),
		},
	}
	diff := NewMachineConfigDiff(oldConfig, newConfig)
	assert.True(t, diff.IsEmpty())

	newConfig.Spec.OSImageURL = "quay.io/example/foo@sha256:b5bb9d8014a0f9b1d61e21e796d78dccdf1352f23cd32812f4850b878ae4944c"
	diff = NewMachineConfigDiff(oldConfig, newConfig)
	assert.False(t, diff.IsEmpty())
	assert.True(t, diff.osUpdate)

	emptyMc := canonicalizeEmptyMC(nil)
	otherEmptyMc := canonicalizeEmptyMC(nil)
	emptyMc.Spec.KernelArguments = nil
	otherEmptyMc.Spec.KernelArguments = []string{}
	diff = NewMachineConfigDiff(emptyMc, otherEmptyMc)
	assert.True(t, diff.IsEmpty())
}

func newTestIgnitionFile(i uint) igntypes.File {
	mode := 0644
	source := fmt.Sprintf("data:,config%d", i)
	return igntypes.File{Node: igntypes.Node{Path: fmt.Sprintf("/etc/config%d", i)},
		FileEmbedded1: igntypes.FileEmbedded1{Contents: igntypes.FileContents{Source: &source}, Mode: &mode}}
}

func newMachineConfigFromFiles(files []igntypes.File) *mcfgv1.MachineConfig {
	newConfig := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			Config: ctrlcommon.NewIgnConfig(),
		},
	}
	newConfig.Spec.Config.Storage.Files = files
	return newConfig
}

func TestReconcilableDiff(t *testing.T) {
	var oldFiles []igntypes.File
	nOldFiles := uint(10)
	for i := uint(0); i < nOldFiles; i++ {
		oldFiles = append(oldFiles, newTestIgnitionFile(uint(i)))
	}
	oldConfig := newMachineConfigFromFiles(oldFiles)
	newConfig := newMachineConfigFromFiles(append(oldFiles, newTestIgnitionFile(nOldFiles+1)))

	diff, err := Reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "add file", err)
	assert.Equal(t, diff.osUpdate, false)
	assert.Equal(t, diff.passwd, false)
	assert.Equal(t, diff.units, false)
	assert.Equal(t, diff.files, true)

	newConfig = newMachineConfigFromFiles(nil)
	diff, err = Reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "remove all files", err)
	assert.Equal(t, diff.osUpdate, false)
	assert.Equal(t, diff.passwd, false)
	assert.Equal(t, diff.units, false)
	assert.Equal(t, diff.files, true)

	newConfig = newMachineConfigFromFiles(oldFiles)
	newConfig.Spec.OSImageURL = "example.com/machine-os-content:new"
	diff, err = Reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "os update", err)
	assert.Equal(t, diff.osUpdate, true)
	assert.Equal(t, diff.passwd, false)
	assert.Equal(t, diff.units, false)
	assert.Equal(t, diff.files, false)
}

func TestKernelAguments(t *testing.T) {
	oldMcfg := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			Config:          ctrlcommon.NewIgnConfig(),
			KernelArguments: []string{"nosmt", "foo", "baz=test"},
		},
	}
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
			args:      append([]string{"baz=othertest"}, oldMcfg.Spec.KernelArguments...),
			deletions: []string{},
			additions: []string{"baz=othertest"},
		},
	}
	rand.Seed(time.Now().UnixNano())
	for idx, test := range tests {
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			newMcfg := &mcfgv1.MachineConfig{
				Spec: mcfgv1.MachineConfigSpec{
					Config:          ctrlcommon.NewIgnConfig(),
					KernelArguments: test.args,
				},
			}
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
	oldMcfg := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			Config: ctrlcommon.NewIgnConfig(),
		},
	}

	tempUser1 := igntypes.PasswdUser{Name: "core", SSHAuthorizedKeys: []igntypes.SSHAuthorizedKey{"5678", "abc"}}
	newMcfg := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			Config: ctrlcommon.NewIgnConfig(),
		},
	}
	newMcfg.Spec.Config.Passwd.Users = []igntypes.PasswdUser{tempUser1}

	_, errMsg := Reconcilable(oldMcfg, newMcfg)
	checkReconcilableResults(t, "SSH", errMsg)

	// 	Check that updating User with User that is not core is not supported
	tempUser2 := igntypes.PasswdUser{Name: "core", SSHAuthorizedKeys: []igntypes.SSHAuthorizedKey{"1234"}}
	oldMcfg.Spec.Config.Passwd.Users = append(oldMcfg.Spec.Config.Passwd.Users, tempUser2)
	tempUser3 := igntypes.PasswdUser{Name: "another user", SSHAuthorizedKeys: []igntypes.SSHAuthorizedKey{"5678"}}
	newMcfg.Spec.Config.Passwd.Users[0] = tempUser3

	_, errMsg = Reconcilable(oldMcfg, newMcfg)
	checkIrreconcilableResults(t, "SSH", errMsg)

	// check that we cannot make updates if any other Passwd.User field is changed.
	homeDir := "somedir"
	tempUser4 := igntypes.PasswdUser{Name: "core", SSHAuthorizedKeys: []igntypes.SSHAuthorizedKey{"5678"}, HomeDir: &homeDir}
	newMcfg.Spec.Config.Passwd.Users[0] = tempUser4

	_, errMsg = Reconcilable(oldMcfg, newMcfg)
	checkIrreconcilableResults(t, "SSH", errMsg)

	// check that we cannot add a user or have len(Passwd.Users)> 1
	tempUser5 := igntypes.PasswdUser{Name: "some user", SSHAuthorizedKeys: []igntypes.SSHAuthorizedKey{"5678"}}
	newMcfg.Spec.Config.Passwd.Users = append(newMcfg.Spec.Config.Passwd.Users, tempUser5)

	_, errMsg = Reconcilable(oldMcfg, newMcfg)
	checkIrreconcilableResults(t, "SSH", errMsg)

	// check that user is not attempting to remove the only sshkey from core user
	tempUser6 := igntypes.PasswdUser{Name: "core", SSHAuthorizedKeys: []igntypes.SSHAuthorizedKey{}}
	newMcfg.Spec.Config.Passwd.Users[0] = tempUser6
	newMcfg.Spec.Config.Passwd.Users = newMcfg.Spec.Config.Passwd.Users[:len(newMcfg.Spec.Config.Passwd.Users)-1]

	_, errMsg = Reconcilable(oldMcfg, newMcfg)
	checkIrreconcilableResults(t, "SSH", errMsg)

	//check that empty Users does not generate error/degrade node
	newMcfg.Spec.Config.Passwd.Users = nil

	_, errMsg = Reconcilable(oldMcfg, newMcfg)
	checkReconcilableResults(t, "SSH", errMsg)
}

func TestUpdateSSHKeys(t *testing.T) {
	// expectedError is the error we will use when expecting an error to return
	expectedError := fmt.Errorf("broken")
	// testClient is the NodeUpdaterClient mock instance that will front
	// calls to update the host.
	testClient := RpmOstreeClientMock{
		GetBootedOSImageURLReturns: []GetBootedOSImageURLReturn{},
		RunPivotReturns: []error{
			// First run will return no error
			nil,
			// Second rrun will return our expected error
			expectedError},
	}
	// Create a Daemon instance with mocked clients
	d := Daemon{
		mock:              true,
		name:              "nodeName",
		OperatingSystem:   machineConfigDaemonOSRHCOS,
		NodeUpdaterClient: testClient,
		kubeClient:        k8sfake.NewSimpleClientset(),
		bootedOSImageURL:  "test",
	}
	// Set up machineconfigs that are identical except for SSH keys
	tempUser := igntypes.PasswdUser{Name: "core", SSHAuthorizedKeys: []igntypes.SSHAuthorizedKey{"1234", "4567"}}
	newMcfg := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			Config: ctrlcommon.NewIgnConfig(),
		},
	}
	newMcfg.Spec.Config.Passwd.Users = []igntypes.PasswdUser{tempUser}

	err := d.updateSSHKeys(newMcfg.Spec.Config.Passwd.Users)
	if err != nil {
		t.Errorf("Expected no error. Got %s.", err)

	}

	// if Users is empty, nothing should happen and no error should ever be generated
	newMcfg2 := &mcfgv1.MachineConfig{}
	err = d.updateSSHKeys(newMcfg2.Spec.Config.Passwd.Users)
	if err != nil {
		t.Errorf("Expected no error. Got: %s", err)
	}
}

// This test should fail until Ignition validation enabled.
// Ignition validation does not permit writing files to relative paths.
func TestInvalidIgnConfig(t *testing.T) {
	oldIgnConfig := ctrlcommon.NewIgnConfig()
	oldMcfg := &mcfgv1.MachineConfig{Spec: mcfgv1.MachineConfigSpec{Config: oldIgnConfig}}

	// create file to write that contains an impermissable relative path
	source := "data:,hello%20world%0A"
	tempFileContents := igntypes.FileContents{Source: &source}
	tempMode := 420
	newIgnConfig := ctrlcommon.NewIgnConfig()
	newIgnFile := igntypes.File{
		Node:          igntypes.Node{Path: "home/core/test"},
		FileEmbedded1: igntypes.FileEmbedded1{Contents: tempFileContents, Mode: &tempMode},
	}
	newIgnConfig.Storage.Files = append(newIgnConfig.Storage.Files, newIgnFile)
	newMcfg := &mcfgv1.MachineConfig{Spec: mcfgv1.MachineConfigSpec{Config: newIgnConfig}}
	_, err := Reconcilable(oldMcfg, newMcfg)
	// assert.NotNil(t, err, "Expected error. Relative Paths should fail general ignition validation")

	newMcfg.Spec.Config.Storage.Files[0].Node.Path = "/home/core/test"
	diff, err := Reconcilable(oldMcfg, newMcfg)
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
