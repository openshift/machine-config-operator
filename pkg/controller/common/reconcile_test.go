package common

import (
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	opv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/helpers"
)

// TestReconcilable attempts to verify the conditions in which configs would and would not be
// reconcilable. Welcome to the longest unittest you've ever read.
func TestIsRenderedConfigReconcilable(t *testing.T) {
	oldIgnCfg := NewIgnConfig()
	// oldConfig is the current config of the fake system
	oldConfig := helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
	newIgnCfg := NewIgnConfig()

	// Set improper version
	newIgnCfg.Ignition.Version = "4.0.0"

	// newConfig is the config that is being requested to apply to the system
	newConfig := helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	// Verify Ignition version mismatch react as expected
	isReconcilable := IsRenderedConfigReconcilable(oldConfig, newConfig, nil)
	checkIrreconcilableResults(t, "Ignition", isReconcilable)
	//reset to proper Ignition version
	newIgnCfg.Ignition.Version = ign3types.MaxVersion.String()
	newConfig = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	isReconcilable = IsRenderedConfigReconcilable(oldConfig, newConfig, nil)
	checkReconcilableResults(t, "Ignition", isReconcilable)

	// Verify Disk changes react as expected
	oldIgnCfg.Storage.Disks = []ign3types.Disk{
		{
			Device: "/one",
		},
	}
	oldConfig = helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
	isReconcilable = IsRenderedConfigReconcilable(oldConfig, newConfig, nil)
	checkIrreconcilableResults(t, "Disk", isReconcilable)

	// Verify Disk changes are allowed if an override is given
	isReconcilable = IsRenderedConfigReconcilable(
		oldConfig,
		newConfig,
		&opv1.IrreconcilableValidationOverrides{
			Storage: []opv1.IrreconcilableValidationOverridesStorage{
				opv1.IrreconcilableValidationOverridesStorageDisks,
			},
		},
	)
	checkReconcilableResults(t, "Disk", isReconcilable)

	// Match storage disks
	newIgnCfg.Storage.Disks = oldIgnCfg.Storage.Disks
	newConfig = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	isReconcilable = IsRenderedConfigReconcilable(oldConfig, newConfig, nil)
	checkReconcilableResults(t, "Disk", isReconcilable)

	// Verify Filesystems changes react as expected
	oldIgnCfg.Storage.Filesystems = []ign3types.Filesystem{
		{
			Device: "/dev/sda1",
			Format: helpers.StrToPtr("ext4"),
			Path:   helpers.StrToPtr("/foo/bar"),
		},
	}
	oldConfig = helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
	isReconcilable = IsRenderedConfigReconcilable(oldConfig, newConfig, nil)
	checkIrreconcilableResults(t, "Filesystem", isReconcilable)

	// Verify Filesystems changes are allowed if an override is given
	isReconcilable = IsRenderedConfigReconcilable(
		oldConfig,
		newConfig,
		&opv1.IrreconcilableValidationOverrides{
			Storage: []opv1.IrreconcilableValidationOverridesStorage{
				opv1.IrreconcilableValidationOverridesStorageFileSystems,
			},
		},
	)
	checkReconcilableResults(t, "Filesystem", isReconcilable)

	// Match Storage filesystems
	newIgnCfg.Storage.Filesystems = oldIgnCfg.Storage.Filesystems
	newConfig = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	isReconcilable = IsRenderedConfigReconcilable(oldConfig, newConfig, nil)
	checkReconcilableResults(t, "Filesystem", isReconcilable)

	// Verify Raid changes react as expected
	var stripe = "stripe"
	oldIgnCfg.Storage.Raid = []ign3types.Raid{
		{
			Name:    "data",
			Level:   &stripe,
			Devices: []ign3types.Device{"/dev/vda", "/dev/vdb"},
		},
	}
	oldConfig = helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
	isReconcilable = IsRenderedConfigReconcilable(oldConfig, newConfig, nil)
	checkIrreconcilableResults(t, "Raid", isReconcilable)

	// Verify Raid changes are allowed if an override is given
	isReconcilable = IsRenderedConfigReconcilable(
		oldConfig,
		newConfig,
		&opv1.IrreconcilableValidationOverrides{
			Storage: []opv1.IrreconcilableValidationOverridesStorage{
				opv1.IrreconcilableValidationOverridesStorageRaid,
			},
		},
	)
	checkReconcilableResults(t, "Raid", isReconcilable)

	// Match storage raid
	newIgnCfg.Storage.Raid = oldIgnCfg.Storage.Raid
	newConfig = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	isReconcilable = IsRenderedConfigReconcilable(oldConfig, newConfig, nil)
	checkReconcilableResults(t, "Raid", isReconcilable)

	// Verify Passwd Groups changes unsupported
	oldIgnCfg = NewIgnConfig()
	oldConfig = helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
	newIgnCfg = NewIgnConfig()
	newConfig = helpers.CreateMachineConfigFromIgnition(newIgnCfg)

	isReconcilable = IsRenderedConfigReconcilable(oldConfig, newConfig, nil)
	checkReconcilableResults(t, "PasswdGroups", isReconcilable)

	tempGroup := ign3types.PasswdGroup{}
	tempGroup.Name = "testGroup"
	newIgnCfg.Passwd.Groups = []ign3types.PasswdGroup{tempGroup}
	newConfig = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	isReconcilable = IsRenderedConfigReconcilable(oldConfig, newConfig, nil)
	checkIrreconcilableResults(t, "PasswdGroups", isReconcilable)

	// Verify Ignition kernelArguments changes unsupported
	oldIgnCfg = NewIgnConfig()
	oldConfig = helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
	newIgnCfg = NewIgnConfig()
	newIgnCfg.KernelArguments.ShouldExist = []ign3types.KernelArgument{"foo=bar"}
	newIgnCfg.KernelArguments.ShouldNotExist = []ign3types.KernelArgument{"baz=foo"}

	newConfig = helpers.CreateMachineConfigFromIgnition(newIgnCfg)

	isReconcilable = IsRenderedConfigReconcilable(oldConfig, newConfig, nil)
	checkIrreconcilableResults(t, "KernelArguments", isReconcilable)

	// Verify Tang changes are supported (even though we don't do anything with them yet)
	oldIgnCfg = NewIgnConfig()
	oldIgnCfg.Storage.Luks = []ign3types.Luks{
		{
			Clevis: ign3types.Clevis{
				Custom: ign3types.ClevisCustom{},
				Tang: []ign3types.Tang{
					{
						URL:           "https://tang.example.com",
						Advertisement: helpers.StrToPtr(`{"payload": "...", "protected": "...", "signature": "..."}`),
						Thumbprint:    helpers.StrToPtr("TREPLACE-THIS-WITH-YOUR-TANG-THUMBPRINT"),
					},
				},
			},
			Device: helpers.StrToPtr("/dev/sdb"),
			Name:   "luks-tang",
		},
	}
	oldConfig = helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
	newIgnCfg = NewIgnConfig()
	newConfig = helpers.CreateMachineConfigFromIgnition(newIgnCfg)

	isReconcilable = IsRenderedConfigReconcilable(oldConfig, newConfig, nil)
	checkReconcilableResults(t, "LuksClevisTang", isReconcilable)

	// FIPS changes are not reconcilable
	oldConfig = helpers.CreateMachineConfigFromIgnition(NewIgnConfig())
	newIgnCfg = NewIgnConfig()
	newConfig = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	newConfig.Spec.FIPS = true

	isReconcilable = IsRenderedConfigReconcilable(oldConfig, newConfig, nil)
	checkIrreconcilableResults(t, "FIPS", isReconcilable)

	// Cannot use MachineConfigs to create the forcefile.
	oldConfig = helpers.CreateMachineConfigFromIgnition(NewIgnConfig())
	newIgnCfg = NewIgnConfig()
	newIgnCfg.Storage.Files = []ign3types.File{
		helpers.CreateUncompressedIgn3File(constants.MachineConfigDaemonForceFile, "", 644),
	}

	checkIrreconcilableResults(t, "forcefile", isReconcilable)
}

func TestReconcilableSSH(t *testing.T) {
	// Check that updating SSH Key of user core supported
	oldIgnCfg := NewIgnConfig()
	oldMcfg := helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
	tempUser1 := ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"5678", "abc"}}
	newIgnCfg := NewIgnConfig()
	newIgnCfg.Passwd.Users = []ign3types.PasswdUser{tempUser1}
	newMcfg := helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	errMsg := IsRenderedConfigReconcilable(oldMcfg, newMcfg, nil)
	checkReconcilableResults(t, "SSH", errMsg)

	// 	Check that updating User with User that is not core is not supported
	tempUser2 := ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"1234"}}
	oldIgnCfg.Passwd.Users = append(oldIgnCfg.Passwd.Users, tempUser2)
	oldMcfg = helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
	tempUser3 := ign3types.PasswdUser{Name: "another user", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"5678"}}
	newIgnCfg.Passwd.Users[0] = tempUser3
	newMcfg = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	errMsg = IsRenderedConfigReconcilable(oldMcfg, newMcfg, nil)
	checkIrreconcilableResults(t, "SSH", errMsg)

	// check that we cannot make updates if any other Passwd.User field is changed.
	tempUser4 := ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"5678"}, HomeDir: helpers.StrToPtr("somedir")}
	newIgnCfg.Passwd.Users[0] = tempUser4
	newMcfg = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	errMsg = IsRenderedConfigReconcilable(oldMcfg, newMcfg, nil)
	checkIrreconcilableResults(t, "SSH", errMsg)

	// check that we cannot add a user or have len(Passwd.Users)> 1
	tempUser5 := ign3types.PasswdUser{Name: "some user", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"5678"}}
	newIgnCfg.Passwd.Users = append(newIgnCfg.Passwd.Users, tempUser5)
	newMcfg = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	errMsg = IsRenderedConfigReconcilable(oldMcfg, newMcfg, nil)
	checkIrreconcilableResults(t, "SSH", errMsg)

	// check that user is not attempting to remove the only sshkey from core user
	tempUser6 := ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{}}
	newIgnCfg.Passwd.Users[0] = tempUser6
	newIgnCfg.Passwd.Users = newIgnCfg.Passwd.Users[:len(newIgnCfg.Passwd.Users)-1]
	newMcfg = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	errMsg = IsRenderedConfigReconcilable(oldMcfg, newMcfg, nil)
	checkIrreconcilableResults(t, "SSH", errMsg)

	//check that empty Users does not cause panic
	newIgnCfg.Passwd.Users = nil
	newMcfg = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	errMsg = IsRenderedConfigReconcilable(oldMcfg, newMcfg, nil)
	checkReconcilableResults(t, "SSH", errMsg)
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

	t.Logf("Received error: %s", reconcilableError)
}
