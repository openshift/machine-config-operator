package cloudprovider

import (
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
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
