package common

import (
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/version"
	"k8s.io/apimachinery/pkg/runtime"
)

func GenerateContainerConfig(baseMC *mcfgv1.MachineConfig, cc *mcfgv1.ControllerConfig) (*mcfgv1.MachineConfig, error) {
	// Create deep copy to avoid mutating input
	containerMC := baseMC.DeepCopy()

	// Empty ignition config
	containerMC.Spec.Config = runtime.RawExtension{Raw: []byte("{}")}

	// Generate deterministic name
	hash, err := HashMachineConfigSpec(containerMC.Spec)
	if err != nil {
		return nil, fmt.Errorf("error hashing MachineConfigSpec: %w", err)
	}
	containerMC.Name = fmt.Sprintf("container-config-%s", hash)

	// Initialize annotations map if nil
	if containerMC.Annotations == nil {
		containerMC.Annotations = make(map[string]string)
	}

	// Set standard annotations
	containerMC.Annotations[ContainerBuildAnnotationKey] = "true"
	containerMC.Annotations[GeneratedByControllerVersionAnnotationKey] = version.Hash
	containerMC.Annotations[ReleaseImageVersionAnnotationKey] = cc.Annotations[ReleaseImageVersionAnnotationKey]

	// Validate before returning
	if err := ValidateMachineConfig(containerMC.Spec); err != nil {
		return nil, fmt.Errorf("invalid container config: %w", err)
	}

	return containerMC, nil
}
