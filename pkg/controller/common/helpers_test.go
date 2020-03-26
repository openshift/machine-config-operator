package common

import (
	"testing"

	"github.com/clarketm/json"
	ignTypes "github.com/coreos/ignition/config/v2_2/types"
	ignTypesV3 "github.com/coreos/ignition/v2/config/v3_0/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestValidateIgnitionV2(t *testing.T) {
	// Test that an empty ignition config returns nil
	testIgnConfig := ignTypes.Config{}
	isValid := ValidateIgnitionV2(testIgnConfig)
	require.Nil(t, isValid)

	// Test that an invalid ignition config returns and error
	tempUser1 := ignTypes.PasswdUser{Name: "core", SSHAuthorizedKeys: []ignTypes.SSHAuthorizedKey{"5678", "abc"}}
	testIgnConfig.Passwd.Users = []ignTypes.PasswdUser{tempUser1}
	isValid = ValidateIgnitionV2(testIgnConfig)
	require.NotNil(t, isValid)

	// Test that a valid ignition config returns nil
	testIgnConfig.Ignition.Version = "2.0.0"
	isValid = ValidateIgnitionV2(testIgnConfig)
	require.Nil(t, isValid)
}

func TestValidateMachineConfigV3(t *testing.T) {
	// Test that an empty ignition config returns nil
	testIgnConfig1 := ignTypesV3.Config{}

	testRawIgnConfig1, err := json.Marshal(testIgnConfig1)
	require.Nil(t, err)

	testMCSpec1 := mcfgv1.MachineConfigSpec{
		Config: runtime.RawExtension{
			Raw: testRawIgnConfig1,
		},
	}

	isValid := ValidateMachineConfigV3(testMCSpec1)
	require.Nil(t, isValid)

	// Test that an invalid ignition config (no version set, not empty) returns an error
	testIgnConfig2 := ignTypesV3.Config{}
	tempUser1 := ignTypesV3.PasswdUser{Name: "core", SSHAuthorizedKeys: []ignTypesV3.SSHAuthorizedKey{"5678", "abc"}}
	testIgnConfig2.Passwd.Users = []ignTypesV3.PasswdUser{tempUser1}
	testRawIgnConfig2, err := json.Marshal(testIgnConfig2)
	require.Nil(t, err)

	testMCSpec2 := mcfgv1.MachineConfigSpec{
		Config: runtime.RawExtension{
			Raw: testRawIgnConfig2,
		},
	}

	isValid = ValidateMachineConfigV3(testMCSpec2)
	require.NotNil(t, isValid)

	// Test that a valid ignition config returns nil
	testIgnConfig3 := ignTypesV3.Config{}
	testIgnConfig3.Ignition.Version = ignTypesV3.MaxVersion.String()
	testRawIgnConfig3, err := json.Marshal(testIgnConfig3)
	require.Nil(t, err)

	testMCSpec3 := mcfgv1.MachineConfigSpec{
		Config: runtime.RawExtension{
			Raw: testRawIgnConfig3,
		},
	}

	isValid = ValidateMachineConfigV3(testMCSpec3)
	require.Nil(t, isValid)
}
