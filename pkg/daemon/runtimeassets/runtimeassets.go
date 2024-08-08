package runtimeassets

import (
	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
)

type RuntimeAsset interface {
	Ignition() (*ign3types.Config, error)
}
