package common

import (
	igntypes "github.com/coreos/ignition/config/v3_0/types"
)

// NewIgnConfig returns an empty ignition config with version set as
// igntypes.MaxVersion.String() currently: 3.0.0
func NewIgnConfig() igntypes.Config {
	return igntypes.Config{
		Ignition: igntypes.Ignition{
			Version: igntypes.MaxVersion.String(),
		},
	}
}

// StrFromStrPtr returns string value from string pointer or empty string if nil
func StrFromStrPtr(strptr *string) string {
	if strptr == nil {
		return ""
	}
	return *strptr
}
