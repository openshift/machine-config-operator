package common

import (
	"testing"

	"github.com/clarketm/json"
	ign2types "github.com/coreos/ignition/config/v2_2/types"
	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/test/helpers"
)

func TestValidateIgnition(t *testing.T) {
	// Test that an empty ignition config returns nil
	testIgn2Config := ign2types.Config{}
	isValid := ValidateIgnition(testIgn2Config)
	require.Nil(t, isValid)

	// Test that an invalid ignition config returns and error
	tempUser1 := ign2types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign2types.SSHAuthorizedKey{"5678", "abc"}}
	testIgn2Config.Passwd.Users = []ign2types.PasswdUser{tempUser1}
	isValid = ValidateIgnition(testIgn2Config)
	require.NotNil(t, isValid)

	// Test that a valid ignition config returns nil
	testIgn2Config.Ignition.Version = "2.0.0"
	isValid = ValidateIgnition(testIgn2Config)
	require.Nil(t, isValid)

	// Test that an empty ignition config returns nil
	testIgn3Config := ign3types.Config{}
	isValid2 := ValidateIgnition(testIgn3Config)
	require.Nil(t, isValid2)

	// Test that an invalid ignition config returns and error
	tempUser2 := ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"5678", "abc"}}
	testIgn3Config.Passwd.Users = []ign3types.PasswdUser{tempUser2}
	isValid2 = ValidateIgnition(testIgn3Config)
	require.NotNil(t, isValid2)

	// Test that a valid ignition config returns nil
	testIgn3Config.Ignition.Version = "3.2.0"
	mode := 420
	testfiledata := "data:,greatconfigstuff"
	tempFile := ign3types.File{Node: ign3types.Node{Path: "/etc/testfileconfig"},
		FileEmbedded1: ign3types.FileEmbedded1{Contents: ign3types.Resource{Source: &testfiledata}, Mode: &mode}}
	testIgn3Config.Storage.Files = append(testIgn3Config.Storage.Files, tempFile)
	isValid2 = ValidateIgnition(testIgn3Config)
	require.Nil(t, isValid2)

	// Test that an invalid typed config will fail
	testInvalid := "test"
	isValid3 := ValidateIgnition(testInvalid)
	require.NotNil(t, isValid3)
}

func TestConvertIgnition2to3(t *testing.T) {
	// Make a new Ign spec v2 config
	testIgn2Config := ign2types.Config{}
	tempUser := ign2types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign2types.SSHAuthorizedKey{"5678", "abc"}}
	testIgn2Config.Passwd.Users = []ign2types.PasswdUser{tempUser}
	testIgn2Config.Ignition.Version = "2.2.0"
	isValid := ValidateIgnition(testIgn2Config)
	require.Nil(t, isValid)

	convertedIgn, err := convertIgnition2to3(testIgn2Config)
	require.Nil(t, err)
	assert.IsType(t, ign3types.Config{}, convertedIgn)
	isValid3 := ValidateIgnition(convertedIgn)
	require.Nil(t, isValid3)
}

func TestConvertIgnition3to2(t *testing.T) {
	// Make a new Ign3 config
	testIgn3Config := ign3types.Config{}
	tempUser := ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"5678", "abc"}}
	testIgn3Config.Passwd.Users = []ign3types.PasswdUser{tempUser}
	testIgn3Config.Ignition.Version = "3.2.0"
	isValid := ValidateIgnition(testIgn3Config)
	require.Nil(t, isValid)

	convertedIgn, err := convertIgnition3to2(testIgn3Config)
	require.Nil(t, err)
	assert.IsType(t, ign2types.Config{}, convertedIgn)
	isValid2 := ValidateIgnition(convertedIgn)
	require.Nil(t, isValid2)
}

func TestParseAndConvert(t *testing.T) {
	// Make a new Ign3.2 config
	testIgn3Config := ign3types.Config{}
	tempUser := ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"5678", "abc"}}
	testIgn3Config.Passwd.Users = []ign3types.PasswdUser{tempUser}
	testIgn3Config.Ignition.Version = "3.2.0"

	// Make a Ign2 comp config
	testIgn2Config := ign2types.Config{}
	tempUser2 := ign2types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign2types.SSHAuthorizedKey{"5678", "abc"}}
	testIgn2Config.Passwd.Users = []ign2types.PasswdUser{tempUser2}
	testIgn2Config.Ignition.Version = "2.2.0"

	// turn v2.2 config into a raw []byte
	rawIgn := helpers.MarshalOrDie(testIgn2Config)
	// check that it was parsed successfully
	convertedIgn, err := ParseAndConvertConfig(rawIgn)
	require.Nil(t, err)
	assert.Equal(t, testIgn3Config, convertedIgn)

	// turn v3.1 config into a raw []byte
	rawIgn = helpers.MarshalOrDie(testIgn3Config)
	// check that it was parsed successfully
	convertedIgn, err = ParseAndConvertConfig(rawIgn)
	require.Nil(t, err)
	assert.Equal(t, testIgn3Config, convertedIgn)

	// Make a valid Ign 3.2 cfg
	testIgn3Config.Ignition.Version = "3.2.0"
	// turn it into a raw []byte
	rawIgn = helpers.MarshalOrDie(testIgn3Config)
	// check that it was parsed successfully
	convertedIgn, err = ParseAndConvertConfig(rawIgn)
	require.Nil(t, err)
	assert.Equal(t, testIgn3Config, convertedIgn)

	// Make a valid Ign 3.1 cfg
	testIgn3Config.Ignition.Version = "3.1.0"
	// turn it into a raw []byte
	rawIgn = helpers.MarshalOrDie(testIgn3Config)
	// check that it was parsed successfully back to 3.2
	convertedIgn, err = ParseAndConvertConfig(rawIgn)
	require.Nil(t, err)
	testIgn3Config.Ignition.Version = "3.2.0"
	assert.Equal(t, testIgn3Config, convertedIgn)

	// Make a valid Ign 3.0 cfg
	testIgn3Config.Ignition.Version = "3.0.0"
	// turn it into a raw []byte
	rawIgn = helpers.MarshalOrDie(testIgn3Config)
	// check that it was parsed successfully back to 3.2
	convertedIgn, err = ParseAndConvertConfig(rawIgn)
	require.Nil(t, err)
	testIgn3Config.Ignition.Version = "3.2.0"
	assert.Equal(t, testIgn3Config, convertedIgn)

	// Make a bad Ign3 cfg
	testIgn3Config.Ignition.Version = "21.0.0"
	rawIgn = helpers.MarshalOrDie(testIgn3Config)
	// check that it failed since this is an invalid cfg
	convertedIgn, err = ParseAndConvertConfig(rawIgn)
	require.NotNil(t, err)
	assert.Equal(t, ign3types.Config{}, convertedIgn)
}

func TestMergeMachineConfigs(t *testing.T) {
	// variable setup
	osImageURL := "testURL"
	fips := true
	kargs := []string{"testKarg"}
	extensions := []string{"testExtensions"}

	// Test that a singular base config that sets FIPS also sets other defaults correctly
	machineConfigFIPS := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			FIPS: fips,
		},
	}
	inMachineConfigs := []*mcfgv1.MachineConfig{machineConfigFIPS}
	mergedMachineConfig, err := MergeMachineConfigs(inMachineConfigs, osImageURL)
	require.Nil(t, err)

	// check that the outgoing config does have the version string set,
	// despite not having a MC with an ignition conf
	outIgn := ign3types.Config{
		Ignition: ign3types.Ignition{
			Version: ign3types.MaxVersion.String(),
		},
	}
	rawOutIgn, err := json.Marshal(outIgn)
	require.Nil(t, err)
	expectedMachineConfig := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			OSImageURL:      osImageURL,
			KernelArguments: []string{},
			Config: runtime.RawExtension{
				Raw: rawOutIgn,
			},
			FIPS:       fips,
			KernelType: KernelTypeDefault,
			Extensions: []string{},
		},
	}
	assert.Equal(t, *mergedMachineConfig, *expectedMachineConfig)

	// Test that all other configs can also be set properly

	// osImageURL should be set from the passed variable, make sure that
	// setting it via a MachineConfig doesn't do anything
	machineConfigOSImageURL := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			OSImageURL: "badURL",
		},
	}
	machineConfigKernelArgs := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			KernelArguments: kargs,
		},
	}
	machineConfigKernelType := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			KernelType: KernelTypeRealtime,
		},
	}
	machineConfigExtensions := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			Extensions: extensions,
		},
	}
	outIgn = ign3types.Config{
		Ignition: ign3types.Ignition{
			Version: ign3types.MaxVersion.String(),
		},
		Passwd: ign3types.Passwd{
			Users: []ign3types.PasswdUser{
				ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"1234"}},
			},
		},
	}
	rawOutIgn, err = json.Marshal(outIgn)
	machineConfigIgn := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: rawOutIgn,
			},
		},
	}

	// Now merge all of the above
	inMachineConfigs = []*mcfgv1.MachineConfig{
		machineConfigFIPS,
		machineConfigOSImageURL,
		machineConfigKernelArgs,
		machineConfigKernelType,
		machineConfigExtensions,
		machineConfigIgn,
	}
	mergedMachineConfig, err = MergeMachineConfigs(inMachineConfigs, osImageURL)
	require.Nil(t, err)

	expectedMachineConfig = &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			OSImageURL:      osImageURL,
			KernelArguments: kargs,
			Config: runtime.RawExtension{
				Raw: rawOutIgn,
			},
			FIPS:       true,
			KernelType: KernelTypeRealtime,
			Extensions: extensions,
		},
	}
	assert.Equal(t, *mergedMachineConfig, *expectedMachineConfig)

}

func TestRemoveIgnDuplicateFilesAndUnits(t *testing.T) {
	mode := 420
	testDataOld := "data:,old"
	testDataNew := "data:,new"
	testIgn2Config := ign2types.Config{}

	// file test, add a duplicate file and see if the newest one is preserved
	fileOld := ign2types.File{
		Node: ign2types.Node{
			Filesystem: "root", Path: "/etc/testfileconfig",
		},
		FileEmbedded1: ign2types.FileEmbedded1{
			Contents: ign2types.FileContents{
				Source: testDataOld,
			},
			Mode: &mode,
		},
	}
	testIgn2Config.Storage.Files = append(testIgn2Config.Storage.Files, fileOld)

	fileNew := ign2types.File{
		Node: ign2types.Node{
			Filesystem: "root", Path: "/etc/testfileconfig",
		},
		FileEmbedded1: ign2types.FileEmbedded1{
			Contents: ign2types.FileContents{
				Source: testDataNew,
			},
			Mode: &mode,
		},
	}
	testIgn2Config.Storage.Files = append(testIgn2Config.Storage.Files, fileNew)

	// unit test, add three units and three dropins with the same name as follows:
	// unitOne:
	//    contents: old
	//    dropin:
	//        name: one
	//        contents: old
	// unitTwo:
	//    dropin:
	//        name: one
	//        contents: new
	// unitThree:
	//    contents: new
	//    dropin:
	//        name: two
	//        contents: new
	// Which should result in:
	// unitFinal:
	//    contents: new
	//    dropin:
	//      - name: one
	//        contents: new
	//      - name: two
	//        contents: new
	//
	unitName := "testUnit"
	dropinNameOne := "one"
	dropinNameTwo := "two"
	dropinOne := ign2types.SystemdDropin{
		Contents: testDataOld,
		Name:     dropinNameOne,
	}
	dropinTwo := ign2types.SystemdDropin{
		Contents: testDataNew,
		Name:     dropinNameOne,
	}
	dropinThree := ign2types.SystemdDropin{
		Contents: testDataNew,
		Name:     dropinNameTwo,
	}

	unitOne := ign2types.Unit{
		Contents: testDataOld,
		Name:     unitName,
	}
	unitOne.Dropins = append(unitOne.Dropins, dropinOne)
	testIgn2Config.Systemd.Units = append(testIgn2Config.Systemd.Units, unitOne)

	unitTwo := ign2types.Unit{
		Name: unitName,
	}
	unitTwo.Dropins = append(unitTwo.Dropins, dropinTwo)
	testIgn2Config.Systemd.Units = append(testIgn2Config.Systemd.Units, unitTwo)

	unitThree := ign2types.Unit{
		Contents: testDataNew,
		Name:     unitName,
	}
	unitThree.Dropins = append(unitThree.Dropins, dropinThree)
	testIgn2Config.Systemd.Units = append(testIgn2Config.Systemd.Units, unitThree)

	convertedIgn2Config, err := removeIgnDuplicateFilesUnitsUsers(testIgn2Config)
	require.Nil(t, err)

	expectedIgn2Config := ign2types.Config{}
	expectedIgn2Config.Storage.Files = append(expectedIgn2Config.Storage.Files, fileNew)
	unitExpected := ign2types.Unit{
		Contents: testDataNew,
		Name:     unitName,
	}
	unitExpected.Dropins = append(unitExpected.Dropins, dropinThree)
	unitExpected.Dropins = append(unitExpected.Dropins, dropinTwo)
	expectedIgn2Config.Systemd.Units = append(expectedIgn2Config.Systemd.Units, unitExpected)

	assert.Equal(t, expectedIgn2Config, convertedIgn2Config)
}
