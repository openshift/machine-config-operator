package resourcemerge

import (
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"

	"github.com/openshift/library-go/pkg/operator/resource/resourcemerge"
	"k8s.io/apimachinery/pkg/api/equality"
)

// EnsureMachineConfig ensures that the existing matches the required.
// modified is set to true when existing had to be updated with required.
func EnsureMachineConfigNode(modified *bool, existing *mcfgv1.MachineConfigNode, required mcfgv1.MachineConfigNode) {
	resourcemerge.EnsureObjectMeta(modified, &existing.ObjectMeta, required.ObjectMeta)
	ensureMachineConfigNodeSpec(modified, &existing.Spec, required.Spec)
}

// EnsureMachineConfig ensures that the existing matches the required.
// modified is set to true when existing had to be updated with required.
func EnsureMachineConfig(modified *bool, existing *mcfgv1.MachineConfig, required mcfgv1.MachineConfig) {
	resourcemerge.EnsureObjectMeta(modified, &existing.ObjectMeta, required.ObjectMeta)
	ensureMachineConfigSpec(modified, &existing.Spec, required.Spec)
}

// EnsureControllerConfig ensures that the existing matches the required.
// modified is set to true when existing had to be updated with required.
func EnsureControllerConfig(modified *bool, existing *mcfgv1.ControllerConfig, required mcfgv1.ControllerConfig) {
	resourcemerge.EnsureObjectMeta(modified, &existing.ObjectMeta, required.ObjectMeta)
	ensureControllerConfigSpec(modified, &existing.Spec, required.Spec)
}

// EnsureMachineConfigPool ensures that the existing matches the required.
// modified is set to true when existing had to be updated with required.
func EnsureMachineConfigPool(modified *bool, existing *mcfgv1.MachineConfigPool, required mcfgv1.MachineConfigPool) {
	resourcemerge.EnsureObjectMeta(modified, &existing.ObjectMeta, required.ObjectMeta)

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

func ensureMachineConfigNodeSpec(modified *bool, existing *mcfgv1.MachineConfigNodeSpec, required mcfgv1.MachineConfigNodeSpec) {
	if !equality.Semantic.DeepEqual(existing.Node, required.Node) {
		*modified = true
		(*existing).Node = required.Node
	}
	if !equality.Semantic.DeepEqual(existing.Pool, required.Pool) {
		*modified = true
		(*existing).Pool = required.Pool
	}
}
func ensureMachineConfigSpec(modified *bool, existing *mcfgv1.MachineConfigSpec, required mcfgv1.MachineConfigSpec) {
	resourcemerge.SetStringIfSet(modified, &existing.OSImageURL, required.OSImageURL)
	resourcemerge.SetStringIfSet(modified, &existing.KernelType, required.KernelType)
	resourcemerge.SetStringIfSet(modified, &existing.BaseOSExtensionsContainerImage, required.BaseOSExtensionsContainerImage)

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
	resourcemerge.SetStringIfSet(modified, &existing.ClusterDNSIP, required.ClusterDNSIP)
	resourcemerge.SetStringIfSet(modified, &existing.CloudProviderConfig, required.CloudProviderConfig)
	resourcemerge.SetStringIfSet(modified, &existing.Platform, required.Platform)
	resourcemerge.SetStringIfSet(modified, &existing.EtcdDiscoveryDomain, required.EtcdDiscoveryDomain)
	resourcemerge.SetStringIfSet(modified, &existing.OSImageURL, required.OSImageURL)
	resourcemerge.SetStringIfSet(modified, &existing.BaseOSContainerImage, required.BaseOSContainerImage)
	resourcemerge.SetStringIfSet(modified, &existing.BaseOSExtensionsContainerImage, required.BaseOSExtensionsContainerImage)
	resourcemerge.SetStringIfSet(modified, &existing.NetworkType, required.NetworkType)

	setBytesIfSet(modified, &existing.InternalRegistryPullSecret, required.InternalRegistryPullSecret)
	setBytesIfSet(modified, &existing.AdditionalTrustBundle, required.AdditionalTrustBundle)
	setBytesIfSet(modified, &existing.RootCAData, required.RootCAData)
	setBytesIfSet(modified, &existing.KubeAPIServerServingCAData, required.KubeAPIServerServingCAData)
	setBytesIfSet(modified, &existing.CloudProviderCAData, required.CloudProviderCAData)

	setIPFamiliesIfSet(modified, &existing.IPFamilies, required.IPFamilies)

	if required.ImageRegistryBundleData != nil && !equality.Semantic.DeepEqual(existing.ImageRegistryBundleData, required.ImageRegistryBundleData) {
		*modified = true
		existing.ImageRegistryBundleData = required.ImageRegistryBundleData
	}

	if required.ImageRegistryBundleUserData != nil && !equality.Semantic.DeepEqual(existing.ImageRegistryBundleUserData, required.ImageRegistryBundleUserData) {
		*modified = true
		existing.ImageRegistryBundleUserData = required.ImageRegistryBundleUserData
	}
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

	if required.DNS != nil && !equality.Semantic.DeepEqual(existing.DNS, required.DNS) {
		*modified = true
		existing.DNS = required.DNS
	}

	if !equality.Semantic.DeepEqual(existing.Network, required.Network) {
		*modified = true
		existing.Network = required.Network
	}

	resourcemerge.MergeMap(modified, &existing.Images, required.Images)
}
