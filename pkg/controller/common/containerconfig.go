package common

import (
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/version"
	"k8s.io/apimachinery/pkg/runtime"
)

func GenerateContainerConfig(baseMC *mcfgv1.MachineConfig, cc *mcfgv1.ControllerConfig, mcpName string) (*mcfgv1.MachineConfig, error) {
	// containerMC := &mcfgv1.MachineConfig{
	// 	Spec: baseMC.Spec,
	// }

	containerMC := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			OSImageURL:      baseMC.Spec.OSImageURL,
			KernelType:      baseMC.Spec.KernelType,
			Extensions:      baseMC.Spec.Extensions,
			KernelArguments: baseMC.Spec.KernelArguments,
		},
	}

	// Empty ignition config- testing this because we need a valid version and it fails with
	// saying the config uses a unsupported spec version
	containerMC.Spec.Config = runtime.RawExtension{
		Raw: []byte(`{
            "ignition": { 
                "version": "3.4.0" 
            }
        }`),
	}

	// Generate deterministic name
	hash, err := HashMachineConfigSpec(containerMC.Spec)
	if err != nil {
		return nil, fmt.Errorf("error hashing MachineConfigSpec: %w", err)
	}
	containerMC.Name = fmt.Sprintf("container-config-%s-%s", mcpName, hash)

	// Initialize annotations with all values at once
	containerMC.Annotations = map[string]string{
		ContainerBuildAnnotationKey:               "true",
		GeneratedByControllerVersionAnnotationKey: version.Hash,
		ReleaseImageVersionAnnotationKey:          cc.Annotations[ReleaseImageVersionAnnotationKey],
	}

	// Validate before returning
	if err := ValidateMachineConfig(containerMC.Spec); err != nil {
		return nil, fmt.Errorf("invalid container config: %w", err)
	}

	return containerMC, nil
}
