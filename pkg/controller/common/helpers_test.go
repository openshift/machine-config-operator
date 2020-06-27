package common

import (
	"testing"

	ign2types "github.com/coreos/ignition/config/v2_2/types"
	ign3types "github.com/coreos/ignition/v2/config/v3_1/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	testIgn3Config.Ignition.Version = "3.1.0"
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

func TestConvertIgnition3to2(t *testing.T) {

	// Make a new Ign3 config
	testIgn3Config := ign3types.Config{}
	tempUser := ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"5678", "abc"}}
	testIgn3Config.Passwd.Users = []ign3types.PasswdUser{tempUser}
	testIgn3Config.Ignition.Version = "3.1.0"
	isValid := ValidateIgnition(testIgn3Config)
	require.Nil(t, isValid)

	convertedIgn, err := convertIgnition3to2(testIgn3Config)
	require.Nil(t, err)
	assert.IsType(t, ign2types.Config{}, convertedIgn)
	isValid2 := ValidateIgnition(convertedIgn)
	require.Nil(t, isValid2)
}

func TestIgnParseWrapper(t *testing.T) {
	// Make a new Ign3.1 config
	testIgn3Config := ign3types.Config{}
	tempUser := ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"5678", "abc"}}
	testIgn3Config.Passwd.Users = []ign3types.PasswdUser{tempUser}
	testIgn3Config.Ignition.Version = "3.1.0"

	// Make a Ign2 comp config
	testIgn2Config := NewIgnConfig()
	tempUser2 := ign2types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign2types.SSHAuthorizedKey{"5678", "abc"}}
	testIgn2Config.Passwd.Users = []ign2types.PasswdUser{tempUser2}

	// turn v2.2 config into a raw []byte
	rawIgn := helpers.MarshalOrDie(testIgn2Config)
	// check that it was parsed successfully
	convertedIgn, err := IgnParseWrapper(rawIgn)
	require.Nil(t, err)
	assert.Equal(t, testIgn2Config, convertedIgn)

	// turn v3.1 config into a raw []byte
	rawIgn = helpers.MarshalOrDie(testIgn3Config)
	// check that it was parsed successfully
	convertedIgn, err = IgnParseWrapper(rawIgn)
	require.Nil(t, err)
	assert.Equal(t, testIgn2Config, convertedIgn)

	// Make a valid Ign 3.0 cfg
	testIgn3Config.Ignition.Version = "3.0.0"
	// turn it into a raw []byte
	rawIgn = helpers.MarshalOrDie(testIgn3Config)
	// check that it was parsed successfully
	convertedIgn, err = IgnParseWrapper(rawIgn)
	require.Nil(t, err)
	assert.Equal(t, testIgn2Config, convertedIgn)

	// Make a bad Ign3 cfg
	testIgn3Config.Ignition.Version = "21.0.0"
	rawIgn = helpers.MarshalOrDie(testIgn3Config)
	// check that it failed since this is an invalid cfg
	convertedIgn, err = IgnParseWrapper(rawIgn)
	require.NotNil(t, err)
	assert.Equal(t, ign2types.Config{}, convertedIgn)
}
