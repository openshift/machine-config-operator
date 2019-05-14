package daemon

import (
	"fmt"
	"os/exec"
	"testing"

	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	d := Daemon{
		name:              "nodeName",
		OperatingSystem:   machineConfigDaemonOSRHCOS,
		NodeUpdaterClient: nil,
		kubeClient:        nil,
		bootedOSImageURL:  "test",
	}

	// oldConfig is the current config of the fake system
	oldConfig := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			Config: ignv2_2types.Config{
				Ignition: ignv2_2types.Ignition{
					Version: "2.0.0",
				},
			},
		},
	}

	// newConfig is the config that is being requested to apply to the system
	newConfig := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			Config: ignv2_2types.Config{
				Ignition: ignv2_2types.Ignition{
					Version: "2.2.0",
				},
			},
		},
	}

	// Verify Ignition version changes react as expected
	_, isReconcilable := d.reconcilable(oldConfig, newConfig)
	checkIrreconcilableResults(t, "Ignition", isReconcilable)

	// Match ignition versions
	oldConfig.Spec.Config.Ignition.Version = "2.2.0"
	_, isReconcilable = d.reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "Ignition", isReconcilable)

	// Verify Networkd unit changes react as expected
	oldConfig.Spec.Config.Networkd = ignv2_2types.Networkd{}
	newConfig.Spec.Config.Networkd = ignv2_2types.Networkd{
		Units: []ignv2_2types.Networkdunit{
			ignv2_2types.Networkdunit{
				Name: "test.network",
			},
		},
	}
	_, isReconcilable = d.reconcilable(oldConfig, newConfig)
	checkIrreconcilableResults(t, "Networkd", isReconcilable)

	// Match Networkd
	oldConfig.Spec.Config.Networkd = newConfig.Spec.Config.Networkd

	_, isReconcilable = d.reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "Networkd", isReconcilable)

	// Verify Disk changes react as expected
	oldConfig.Spec.Config.Storage.Disks = []ignv2_2types.Disk{
		ignv2_2types.Disk{
			Device: "/one",
		},
	}

	_, isReconcilable = d.reconcilable(oldConfig, newConfig)
	checkIrreconcilableResults(t, "Disk", isReconcilable)

	// Match storage disks
	newConfig.Spec.Config.Storage.Disks = oldConfig.Spec.Config.Storage.Disks
	_, isReconcilable = d.reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "Disk", isReconcilable)

	// Verify Filesystems changes react as expected
	oldFSPath := "/foo/bar"
	oldConfig.Spec.Config.Storage.Filesystems = []ignv2_2types.Filesystem{
		ignv2_2types.Filesystem{
			Name: "user",
			Path: &oldFSPath,
		},
	}

	_, isReconcilable = d.reconcilable(oldConfig, newConfig)
	checkIrreconcilableResults(t, "Filesystem", isReconcilable)

	// Match Storage filesystems
	newConfig.Spec.Config.Storage.Filesystems = oldConfig.Spec.Config.Storage.Filesystems
	_, isReconcilable = d.reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "Filesystem", isReconcilable)

	// Verify Raid changes react as expected
	oldConfig.Spec.Config.Storage.Raid = []ignv2_2types.Raid{
		ignv2_2types.Raid{
			Name:  "data",
			Level: "stripe",
		},
	}

	_, isReconcilable = d.reconcilable(oldConfig, newConfig)
	checkIrreconcilableResults(t, "Raid", isReconcilable)

	// Match storage raid
	newConfig.Spec.Config.Storage.Raid = oldConfig.Spec.Config.Storage.Raid
	_, isReconcilable = d.reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "Raid", isReconcilable)

	// Verify Passwd Groups changes unsupported
	oldConfig = &mcfgv1.MachineConfig{}
	tempGroup := ignv2_2types.PasswdGroup{Name: "testGroup"}
	newMcfg := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			Config: ignv2_2types.Config{
				Passwd: ignv2_2types.Passwd{
					Groups: []ignv2_2types.PasswdGroup{tempGroup},
				},
			},
		},
	}
	_, isReconcilable = d.reconcilable(oldConfig, newMcfg)
	checkIrreconcilableResults(t, "PasswdGroups", isReconcilable)
}

func newTestIgnitionFile(i uint) ignv2_2types.File {
	mode := 0644
	return ignv2_2types.File{Node: ignv2_2types.Node{Path: fmt.Sprintf("/etc/config%d", i), Filesystem: "root"},
		FileEmbedded1: ignv2_2types.FileEmbedded1{Contents: ignv2_2types.FileContents{Source: fmt.Sprintf("data:,config%d", i)}, Mode: &mode}}
}

func newMachineConfigFromFiles(files []ignv2_2types.File) *mcfgv1.MachineConfig {
	return &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			Config: ignv2_2types.Config{
				Ignition: ignv2_2types.Ignition{
					Version: "2.2.0",
				},
				Storage: ignv2_2types.Storage{
					Files: files,
				},
			},
		},
	}
}

func TestReconcilableDiff(t *testing.T) {
	d := Daemon{
		name:              "nodeName",
		OperatingSystem:   machineConfigDaemonOSRHCOS,
		NodeUpdaterClient: nil,
		kubeClient:        nil,
		bootedOSImageURL:  "test",
	}
	var oldFiles []ignv2_2types.File
	nOldFiles := uint(10)
	for i := uint(0); i < nOldFiles; i++ {
		oldFiles = append(oldFiles, newTestIgnitionFile(uint(i)))
	}
	oldConfig := newMachineConfigFromFiles(oldFiles)
	newConfig := newMachineConfigFromFiles(append(oldFiles, newTestIgnitionFile(nOldFiles + 1)))

	diff, err := d.reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "add file", err)
	assert.Equal(t, diff.osUpdate, false)
	assert.Equal(t, diff.passwd, false)
	assert.Equal(t, diff.units, false)
	assert.Equal(t, diff.files, true)

	newConfig = newMachineConfigFromFiles(nil)
	diff, err = d.reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "remove all files", err)
	assert.Equal(t, diff.osUpdate, false)
	assert.Equal(t, diff.passwd, false)
	assert.Equal(t, diff.units, false)
	assert.Equal(t, diff.files, true)

	newConfig = newMachineConfigFromFiles(oldFiles)
	newConfig.Spec.OSImageURL = "example.com/machine-os-content:new"
	diff, err = d.reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "os update", err)
	assert.Equal(t, diff.osUpdate, true)
	assert.Equal(t, diff.passwd, false)
	assert.Equal(t, diff.units, false)
	assert.Equal(t, diff.files, false)
}

func TestReconcilableSSH(t *testing.T) {
	// expectedError is the error we will use when expecting an error to return
	expectedError := fmt.Errorf("broken")

	// testClient is the NodeUpdaterClient mock instance that will front
	// calls to update the host.
	testClient := RpmOstreeClientMock{
		GetBootedOSImageURLReturns: []GetBootedOSImageURLReturn{},
		RunPivotReturns: []error{
			// First run will return no error
			nil,
			// Second run will return our expected error
			expectedError},
	}

	// Create a Daemon instance with mocked clients
	d := Daemon{
		name:              "nodeName",
		OperatingSystem:   machineConfigDaemonOSRHCOS,
		NodeUpdaterClient: testClient,
		kubeClient:        k8sfake.NewSimpleClientset(),
		bootedOSImageURL:  "test",
	}

	// Check that updating SSH Key of user core supported
	//tempUser1 := ignv2_2types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ignv2_2types.SSHAuthorizedKey{"1234"}}
	oldMcfg := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			Config: ignv2_2types.Config{
				Ignition: ignv2_2types.Ignition{
					Version: "2.2.0",
				},
			},
		},
	}
	tempUser1 := ignv2_2types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ignv2_2types.SSHAuthorizedKey{"5678", "abc"}}
	newMcfg := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			Config: ignv2_2types.Config{
				Ignition: ignv2_2types.Ignition{
					Version: "2.2.0",
				},
				Passwd: ignv2_2types.Passwd{
					Users: []ignv2_2types.PasswdUser{tempUser1},
				},
			},
		},
	}

	_, errMsg := d.reconcilable(oldMcfg, newMcfg)
	checkReconcilableResults(t, "SSH", errMsg)

	// 	Check that updating User with User that is not core is not supported
	tempUser2 := ignv2_2types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ignv2_2types.SSHAuthorizedKey{"1234"}}
	oldMcfg.Spec.Config.Passwd.Users = append(oldMcfg.Spec.Config.Passwd.Users, tempUser2)
	tempUser3 := ignv2_2types.PasswdUser{Name: "another user", SSHAuthorizedKeys: []ignv2_2types.SSHAuthorizedKey{"5678"}}
	newMcfg.Spec.Config.Passwd.Users[0] = tempUser3

	_, errMsg = d.reconcilable(oldMcfg, newMcfg)
	checkIrreconcilableResults(t, "SSH", errMsg)

	// check that we cannot make updates if any other Passwd.User field is changed.
	tempUser4 := ignv2_2types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ignv2_2types.SSHAuthorizedKey{"5678"}, HomeDir: "somedir"}
	newMcfg.Spec.Config.Passwd.Users[0] = tempUser4

	_, errMsg = d.reconcilable(oldMcfg, newMcfg)
	checkIrreconcilableResults(t, "SSH", errMsg)

	// check that we cannot add a user or have len(Passwd.Users)> 1
	tempUser5 := ignv2_2types.PasswdUser{Name: "some user", SSHAuthorizedKeys: []ignv2_2types.SSHAuthorizedKey{"5678"}}
	newMcfg.Spec.Config.Passwd.Users = append(newMcfg.Spec.Config.Passwd.Users, tempUser5)

	_, errMsg = d.reconcilable(oldMcfg, newMcfg)
	checkIrreconcilableResults(t, "SSH", errMsg)

	// check that user is not attempting to remove the only sshkey from core user
	tempUser6 := ignv2_2types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ignv2_2types.SSHAuthorizedKey{}}
	newMcfg.Spec.Config.Passwd.Users[0] = tempUser6
	newMcfg.Spec.Config.Passwd.Users = newMcfg.Spec.Config.Passwd.Users[:len(newMcfg.Spec.Config.Passwd.Users)-1]

	_, errMsg = d.reconcilable(oldMcfg, newMcfg)
	checkIrreconcilableResults(t, "SSH", errMsg)

	//check that empty Users does not generate error/degrade node
	newMcfg.Spec.Config.Passwd.Users = nil

	_, errMsg = d.reconcilable(oldMcfg, newMcfg)
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
		name:              "nodeName",
		OperatingSystem:   machineConfigDaemonOSRHCOS,
		NodeUpdaterClient: testClient,
		kubeClient:        k8sfake.NewSimpleClientset(),
		bootedOSImageURL:  "test",
	}
	// Set up machineconfigs that are identical except for SSH keys
	tempUser := ignv2_2types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ignv2_2types.SSHAuthorizedKey{"1234", "4567"}}

	newMcfg := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			Config: ignv2_2types.Config{
				Passwd: ignv2_2types.Passwd{
					Users: []ignv2_2types.PasswdUser{tempUser},
				},
			},
		},
	}

	d.atomicSSHKeysWriter = func(user ignv2_2types.PasswdUser, keys string) error { return nil }

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
		name:              "nodeName",
		OperatingSystem:   machineConfigDaemonOSRHCOS,
		NodeUpdaterClient: testClient,
		kubeClient:        k8sfake.NewSimpleClientset(),
		bootedOSImageURL:  "test",
	}

	oldMcfg := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			Config: ignv2_2types.Config{
				Ignition: ignv2_2types.Ignition{
					Version: "2.2.0",
				},
			},
		},
	}
	// create file to write that contains an impermissable relative path
	tempFileContents := ignv2_2types.FileContents{Source: "data:,hello%20world%0A"}
	tempMode := 420
	newMcfg := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			Config: ignv2_2types.Config{
				Ignition: ignv2_2types.Ignition{
					Version: "2.2.0",
				},
				Storage: ignv2_2types.Storage{
					Files: []ignv2_2types.File{
						{Node: ignv2_2types.Node{Path: "home/core/test", Filesystem: "root"},
							FileEmbedded1: ignv2_2types.FileEmbedded1{Contents: tempFileContents, Mode: &tempMode}},
					},
				},
			},
		},
	}
	_, err := d.reconcilable(oldMcfg, newMcfg)
	assert.NotNil(t, err, "Expected error. Relative Paths should fail general ignition validation")

	newMcfg.Spec.Config.Storage.Files[0].Node.Path = "/home/core/test"
	diff, err := d.reconcilable(oldMcfg, newMcfg)
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

func TestSkipReboot(t *testing.T) {
	// skipReboot is only honored with onceFrom != ""
	d := &Daemon{
		onceFrom:   "test",
		skipReboot: true,
	}
	require.Nil(t, d.reboot("", 0, nil))

	// skipReboot in normal cluster run is just a no-op and we reboot anyway
	d = &Daemon{
		onceFrom:   "",
		skipReboot: true,
	}
	require.NotNil(t, d.reboot("", 0, exec.Command("true")))
}
