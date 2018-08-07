package resourcemerge

import (
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"k8s.io/apimachinery/pkg/api/equality"
)

// EnsureMachineConfig ensures that the existing matches the required.
// modified is set to true when existing had to be updated with required.
func EnsureMachineConfig(modified *bool, existing *mcfgv1.MachineConfig, required mcfgv1.MachineConfig) {
	EnsureObjectMeta(modified, &existing.ObjectMeta, required.ObjectMeta)
	ensureMachineConfigSpec(modified, &existing.Spec, required.Spec)
}

// EnsureControllerConfig ensures that the existing matches the required.
// modified is set to true when existing had to be updated with required.
func EnsureControllerConfig(modified *bool, existing *mcfgv1.ControllerConfig, required mcfgv1.ControllerConfig) {
	EnsureObjectMeta(modified, &existing.ObjectMeta, required.ObjectMeta)
	ensureControllerConfigSpec(modified, &existing.Spec, required.Spec)
}

// EnsureMachineConfigPool ensures that the existing matches the required.
// modified is set to true when existing had to be updated with required.
func EnsureMachineConfigPool(modified *bool, existing *mcfgv1.MachineConfigPool, required mcfgv1.MachineConfigPool) {
	EnsureObjectMeta(modified, &existing.ObjectMeta, required.ObjectMeta)

	if existing.Spec.MachineConfigSelector == nil {
		*modified = true
		existing.Spec.MachineConfigSelector = required.Spec.MachineConfigSelector
	}
	if !equality.Semantic.DeepEqual(existing.Spec.MachineConfigSelector, required.Spec.MachineConfigSelector) {
		*modified = true
		existing.Spec.MachineConfigSelector = required.Spec.MachineConfigSelector
	}

	if existing.Spec.MachineSelector == nil {
		*modified = true
		existing.Spec.MachineSelector = required.Spec.MachineSelector
	}
	if !equality.Semantic.DeepEqual(existing.Spec.MachineSelector, required.Spec.MachineSelector) {
		*modified = true
		existing.Spec.MachineSelector = required.Spec.MachineSelector
	}
}

func ensureMachineConfigSpec(modified *bool, existing *mcfgv1.MachineConfigSpec, required mcfgv1.MachineConfigSpec) {
	setStringIfSet(modified, &existing.OSImageURL, required.OSImageURL)
	if !equality.Semantic.DeepEqual(existing.Config, required.Config) {
		*modified = true
		(*existing).Config = required.Config
	}
}

func ensureControllerConfigSpec(modified *bool, existing *mcfgv1.ControllerConfigSpec, required mcfgv1.ControllerConfigSpec) {
	setStringIfSet(modified, &existing.ClusterDNSIP, required.ClusterDNSIP)
	setStringIfSet(modified, &existing.CloudProviderConfig, required.CloudProviderConfig)
	setStringIfSet(modified, &existing.ClusterName, required.ClusterName)
	setStringIfSet(modified, &existing.Platform, required.Platform)
	setStringIfSet(modified, &existing.BaseDomain, required.BaseDomain)
	setStringIfSet(modified, &existing.Platform, required.Platform)
}
