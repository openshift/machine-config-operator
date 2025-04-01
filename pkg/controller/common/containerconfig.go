package common

import (
	"errors"
	"fmt"
	"strings"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/version"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func GenerateContainerConfig(baseMC *mcfgv1.MachineConfig, cc *mcfgv1.ControllerConfig, mcpName string) (*mcfgv1.MachineConfig, error) {

	if err := ValidateRenderedConfig(baseMC); err != nil {
		return nil, fmt.Errorf("base MachineConfig %q must be a rendered config: %w", baseMC.Name, err)
	}

	controllerRef := metav1.GetControllerOf(baseMC)
	if err := validateMachineConfigOwnerKind(baseMC, "MachineConfigPool"); err != nil {
		return nil, fmt.Errorf("base MachineConfig %q controller reference has invalid kind %q", baseMC.Name, controllerRef.Kind)
	}

	poolName := controllerRef.Name

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
                "version": "3.5.0" 
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

	// Initialize labels
	containerMC.Labels = map[string]string{
		MachineConfigRoleLabel: poolName,
	}

	// Validate before returning
	if err := ValidateMachineConfig(containerMC.Spec); err != nil {
		return nil, fmt.Errorf("invalid container config: %w", err)
	}

	return containerMC, nil
}

func ValidateContainerConfig(mc *mcfgv1.MachineConfig) error {
	return errors.Join(validateMachineConfigNamePrefix(mc, "container-config"), validateMachineConfigOwnerKind(mc, "MachineOSConfig"))
}

func ValidateRenderedConfig(mc *mcfgv1.MachineConfig) error {
	// Capture individual validation results
	prefixErr := validateMachineConfigNamePrefix(mc, "rendered")
	ownerErr := validateMachineConfigOwnerKind(mc, "MachineConfigPool")

	// Add context to the combined error
	if prefixErr != nil || ownerErr != nil {
		return fmt.Errorf("rendered MachineConfig validation failed for %s. Name prefix valid: %v, Owner kind valid: %v",
			mc.Name,
			prefixErr == nil,
			ownerErr == nil)
	}

	return nil
}

func validateMachineConfigOwnerKind(mc *mcfgv1.MachineConfig, expectedKind string) error {
	controllerRef := metav1.GetControllerOf(mc)
	if controllerRef == nil {
		return fmt.Errorf("MachineConfig %s has no controller ref", mc.Name)
	}

	if controllerRef.Kind != expectedKind {
		return fmt.Errorf("MachineConfig %s controller kind is %s, expected %s", mc.Name, controllerRef.Kind, expectedKind)
	}

	return nil
}

func validateMachineConfigNamePrefix(mc *mcfgv1.MachineConfig, prefix string) error {
	if !strings.HasPrefix(mc.Name, prefix) {
		return fmt.Errorf("expected MachineConfig %s to have %q as a prefix", mc.Name, prefix)
	}

	return nil
}
