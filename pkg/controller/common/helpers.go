package common

import (
	igntypes "github.com/coreos/ignition/config/v2_2/types"
)

// NewIgnConfig returns an empty ignition config with version set as latest version
func NewIgnConfig() igntypes.Config {
	return igntypes.Config{
		Ignition: igntypes.Ignition{
			Version: igntypes.MaxVersion.String(),
		},
	}
}
