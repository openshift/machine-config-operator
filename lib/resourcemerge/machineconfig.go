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

	if existing.Spec.NodeSelector == nil {
		*modified = true
		existing.Spec.NodeSelector = required.Spec.NodeSelector
	}
	if !equality.Semantic.DeepEqual(existing.Spec.NodeSelector, required.Spec.NodeSelector) {
		*modified = true
		existing.Spec.NodeSelector = required.Spec.NodeSelector
	}
}

func ensureMachineConfigSpec(modified *bool, existing *mcfgv1.MachineConfigSpec, required mcfgv1.MachineConfigSpec) {
	setStringIfSet(modified, &existing.OSImageURL, required.OSImageURL)
	setStringIfSet(modified, &existing.KernelType, required.KernelType)

	if !equality.Semantic.DeepEqual(existing.KernelArguments, required.KernelArguments) {
		*modified = true
		(*existing).KernelArguments = required.KernelArguments
	}
	if !equality.Semantic.DeepEqual(existing.Config, required.Config) {
		*modified = true
		(*existing).Config = required.Config
	}
	if existing.FIPS != required.FIPS {
		*modified = true
		(*existing).FIPS = required.FIPS
	}
	if !equality.Semantic.DeepEqual(existing.Extensions, required.Extensions) {
		*modified = true
		(*existing).Extensions = required.Extensions
	}
}

func ensureControllerConfigSpec(modified *bool, existing *mcfgv1.ControllerConfigSpec, required mcfgv1.ControllerConfigSpec) {
	setStringIfSet(modified, &existing.ClusterDNSIP, required.ClusterDNSIP)
	setStringIfSet(modified, &existing.CloudProviderConfig, required.CloudProviderConfig)
	setStringIfSet(modified, &existing.Platform, required.Platform)
	setStringIfSet(modified, &existing.EtcdDiscoveryDomain, required.EtcdDiscoveryDomain)
	setStringIfSet(modified, &existing.OSImageURL, required.OSImageURL)
	setStringIfSet(modified, &existing.NetworkType, required.NetworkType)

	setBytesIfSet(modified, &existing.AdditionalTrustBundle, required.AdditionalTrustBundle)
	setBytesIfSet(modified, &existing.RootCAData, required.RootCAData)
	setBytesIfSet(modified, &existing.KubeAPIServerServingCAData, required.KubeAPIServerServingCAData)
	setBytesIfSet(modified, &existing.CloudProviderCAData, required.CloudProviderCAData)

	if required.Infra != nil && !equality.Semantic.DeepEqual(existing.Infra, required.Infra) {
		*modified = true
		existing.Infra = required.Infra
	}

	if existing.Infra.Status.PlatformStatus != nil && required.Infra.Status.PlatformStatus != nil {
		if !equality.Semantic.DeepEqual(existing.Infra.Status.PlatformStatus.Type, required.Infra.Status.PlatformStatus.Type) {
			*modified = true
			existing.Infra.Status.PlatformStatus.Type = required.Infra.Status.PlatformStatus.Type
		}
	}

	if !equality.Semantic.DeepEqual(existing.Proxy, required.Proxy) {
		*modified = true
		existing.Proxy = required.Proxy
	}

	if required.PullSecret != nil && !equality.Semantic.DeepEqual(existing.PullSecret, required.PullSecret) {
		existing.PullSecret = required.PullSecret
		*modified = true
	}

	if existing.DNS != nil && required.DNS != nil && !equality.Semantic.DeepEqual(existing.DNS, required.DNS) {
		*modified = true
		existing.DNS = required.DNS
	}

	mergeMap(modified, &existing.Images, required.Images)
}
