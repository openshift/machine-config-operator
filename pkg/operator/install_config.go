package operator

import (
	"errors"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

// InstallConfig is a simplified version of the installer's InstallConfig type,
// containing only the fields needed by the MCO.
type InstallConfig struct {

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
