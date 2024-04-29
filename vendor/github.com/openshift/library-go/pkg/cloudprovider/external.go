package cloudprovider

import (
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	features "github.com/openshift/api/features"
)

var (
	// ExternalCloudProviderFeature is the name of the external cloud provider feature gate.
	// This is used to flag to operators that the cluster should be using the external cloud-controller-manager
	// rather than the in-tree cloud controller loops.
	ExternalCloudProviderFeature = features.FeatureGateExternalCloudProvider

	// ExternalCloudProviderFeatureAzure is the name of the external cloud provider feature gate for Azure.
	ExternalCloudProviderFeatureAzure = features.FeatureGateExternalCloudProviderAzure

	// ExternalCloudProviderFeatureGCP is the name of the external cloud provider feature gate for GCP.
	ExternalCloudProviderFeatureGCP = features.FeatureGateExternalCloudProviderGCP

	// ExternalCloudProviderFeatureExternal is the name of the external cloud provider feature gate for External platform.
	ExternalCloudProviderFeatureExternal = features.FeatureGateExternalCloudProviderExternal
)

// IsCloudProviderExternal is used to check whether external cloud provider settings should be used in a component.
// It checks whether the ExternalCloudProvider feature gate is enabled and whether the ExternalCloudProvider feature
// has been implemented for the platform.
func IsCloudProviderExternal(platformStatus *configv1.PlatformStatus) (bool, error) {
	if platformStatus == nil {
		return false, fmt.Errorf("platformStatus is required")
	}
	switch platformStatus.Type {
	case configv1.AlibabaCloudPlatformType,
		configv1.AWSPlatformType,
		configv1.AzurePlatformType,
		configv1.GCPPlatformType,
		configv1.IBMCloudPlatformType,
		configv1.KubevirtPlatformType,
		configv1.NutanixPlatformType,
		configv1.OpenStackPlatformType,
		configv1.PowerVSPlatformType,
		configv1.VSpherePlatformType:
		return true, nil
	case configv1.ExternalPlatformType:
		return isExternalPlatformCCMEnabled(platformStatus)
	default:
		// Platforms that do not have external cloud providers implemented
		return false, nil
	}
}

func isExternalPlatformCCMEnabled(platformStatus *configv1.PlatformStatus) (bool, error) {
	if platformStatus == nil || platformStatus.External == nil {
		return false, nil
	}

	if platformStatus.External.CloudControllerManager.State == configv1.CloudControllerManagerExternal {
		return true, nil
	}

	return false, nil
}
