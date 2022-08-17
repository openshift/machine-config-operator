package version

import (
	"fmt"
)

var (
	// Raw is the string representation of the version. This will be replaced
	// with the calculated version at build time.
	Raw = "v0.0.0-was-not-built-properly"

	// Hash is the git hash we've built the MCO with
	Hash = "was-not-built-properly"

	// String is the human-friendly representation of the version.
	String = fmt.Sprintf("MachineConfigOperator %s", Raw)

	// FCOS is a setting to enable Fedora CoreOS-only modifications
	FCOS = false

	// SCOS is a setting to enable CentOS Stream CoreOS-only modifications
	SCOS = false
)

// IsFCOS returns true if Fedora CoreOS-only modifications are enabled
func IsFCOS() bool {
	return FCOS
}

// IsSCOS returns true if CentOS Stream CoreOS-only modifications are enabled
func IsSCOS() bool {
	return SCOS
}
