package common

import (
	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
)

// NewIgnConfig returns an empty ignition config with version set as 2.2.0
func NewIgnConfig() ignv2_2types.Config {
	return ignv2_2types.Config{
		Ignition: ignv2_2types.Ignition{
			Version: "2.2.0",
		},
	}
}
