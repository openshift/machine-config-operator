package common

import (
	"testing"

	igntypes "gopkg.in/coreos/ignition.v0/config/v2_2/types"
	"github.com/stretchr/testify/require"
)

func TestValidateIgnition(t *testing.T) {
	// Test that an empty ignition config returns nil
	testIgnConfig := igntypes.Config{}
	isValid := ValidateIgnition(testIgnConfig)
	require.Nil(t, isValid)

	// Test that an invalid ignition config returns and error
	tempUser1 := igntypes.PasswdUser{Name: "core", SSHAuthorizedKeys: []igntypes.SSHAuthorizedKey{"5678", "abc"}}
	testIgnConfig.Passwd.Users = []igntypes.PasswdUser{tempUser1}
	isValid = ValidateIgnition(testIgnConfig)
	require.NotNil(t, isValid)

	// Test that a valid ignition config returns nil
	testIgnConfig.Ignition.Version = "2.0.0"
	isValid = ValidateIgnition(testIgnConfig)
	require.Nil(t, isValid)
}
