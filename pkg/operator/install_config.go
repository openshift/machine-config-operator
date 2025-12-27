package operator

import (
	"errors"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

// MachinePool is a simplified version of the installer's MachinePool type,
// containing only the fields needed by the MCO.
type MachinePool struct {

	// OSImageStream specifies the OS image stream to use for this machine pool.
	// Overrides the global osImageStream from InstallConfig when set.
	//
	// +optional
	OSImageStream string `json:"osImageStream,omitempty"`
}

// InstallConfig is a simplified version of the installer's InstallConfig type,
// containing only the fields needed by the MCO.
type InstallConfig struct {

	// ControlPlane is the configuration for control plane machines.
	// +optional
	ControlPlane *MachinePool `json:"controlPlane,omitempty"`

	// Arbiter is the configuration for arbiter machines.
	// +optional
	Arbiter *MachinePool `json:"arbiter,omitempty"`

	// Compute is the configuration for compute machines.
	// +optional
	Compute []MachinePool `json:"compute,omitempty"`

	// OSImageStream is the default OS image stream for all machine pools.
	// Individual machine pools can override this with their own osImageStream.
	//
	// +optional
	OSImageStream string `json:"osImageStream,omitempty"`
}

func NewInstallConfigFromBytes(data []byte) (*InstallConfig, error) {
	installConfig := &InstallConfig{}
	err := yaml.Unmarshal(data, &installConfig)
	if err != nil {
		return nil, fmt.Errorf("error unmarshalling install config: %v", err)
	}
	return installConfig, nil
}

func NewInstallConfigFromConfigMap(configMap *v1.ConfigMap) (*InstallConfig, error) {
	installConfigData, ok := configMap.Data["install-config"]
	if !ok {
		return nil, errors.New("ConfigMap doesn't have an install-config key")
	}

	installConfig, err := NewInstallConfigFromBytes([]byte(installConfigData))
	if err != nil {
		return nil, fmt.Errorf("failed to load install-config: %w", err)
	}
	return installConfig, nil
}

type InstallOSImageStreams struct {
	ControlPlane string
	Compute      string
	Arbiter      string
}

func (i *InstallConfig) GetInstallOSImageStreams() InstallOSImageStreams {
	streams := InstallOSImageStreams{
		ControlPlane: i.OSImageStream,
		Compute:      i.OSImageStream,
		Arbiter:      i.OSImageStream,
	}
	if i.ControlPlane != nil && i.ControlPlane.OSImageStream != "" {
		streams.ControlPlane = i.ControlPlane.OSImageStream
	}
	if i.Arbiter != nil && i.Arbiter.OSImageStream != "" {
		streams.Arbiter = i.Arbiter.OSImageStream
	}
	for _, compute := range i.Compute {
		if compute.OSImageStream != "" {
			streams.Compute = compute.OSImageStream
			break
		}
	}
	return streams
}
