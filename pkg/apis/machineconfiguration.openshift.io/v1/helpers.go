package v1

import (
	"sort"

	ignv2_2 "github.com/coreos/ignition/config/v2_2"
)

// MergeMachineConfigs combines multiple machineconfig objects into one object.
// It sorts all the configs in increasing order of their name.
// It uses the Ign config from first object as base and appends all the rest.
// It only uses the OSImageURL from first object and ignores it from rest.
func MergeMachineConfigs(configs []*MachineConfig) *MachineConfig {
	if len(configs) == 0 {
		return nil
	}
	sort.Slice(configs, func(i, j int) bool { return configs[i].Name < configs[j].Name })

	outOSImageURL := configs[0].Spec.OSImageURL
	outIgn := configs[0].Spec.Config
	for idx := 1; idx < len(configs); idx++ {
		outIgn = ignv2_2.Append(outIgn, configs[idx].Spec.Config)
	}

	return &MachineConfig{
		Spec: MachineConfigSpec{
			OSImageURL: outOSImageURL,
			Config:     outIgn,
		},
	}
}
