package cloudprovider

import (
	"fmt"

	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"

	configv1 "github.com/openshift/api/config/v1"
)

var (
	// ExternalCloudProviderFeature is the name of the external cloud provider feature gate.
	// This is used to flag to operators that the cluster should be using the external cloud-controller-manager
	// rather than the in-tree cloud controller loops.
	ExternalCloudProviderFeature = configv1.FeatureGateExternalCloudProvider

	// ExternalCloudProviderFeatureAzure is the name of the external cloud provider feature gate for Azure.
	ExternalCloudProviderFeatureAzure = configv1.FeatureGateExternalCloudProviderAzure

	// ExternalCloudProviderFeatureGCP is the name of the external cloud provider feature gate for GCP.
	ExternalCloudProviderFeatureGCP = configv1.FeatureGateExternalCloudProviderGCP

	// ExternalCloudProviderFeatureExternal is the name of the external cloud provider feature gate for External platform.
	ExternalCloudProviderFeatureExternal = configv1.FeatureGateExternalCloudProviderExternal
)

// IsCloudProviderExternal is used to check whether external cloud provider settings should be used in a component.
// It checks whether the ExternalCloudProvider feature gate is enabled and whether the ExternalCloudProvider feature
// has been implemented for the platform.
func IsCloudProviderExternal(platformStatus *configv1.PlatformStatus, featureGateAccessor featuregates.FeatureGateAccess) (bool, error) {
	if !featureGateAccessor.AreInitialFeatureGatesObserved() {
		return false, fmt.Errorf("featureGates have not been read yet")
	}
	if platformStatus == nil {
		return false, fmt.Errorf("platformStatus is required")
	}
	switch platformStatus.Type {
	case configv1.GCPPlatformType:
		// Platforms that are external based on feature gate presence
		return isExternalFeatureGateEnabled(featureGateAccessor, ExternalCloudProviderFeature, ExternalCloudProviderFeatureGCP)
	case configv1.AzurePlatformType:
		if isAzureStackHub(platformStatus) {
			return true, nil
		}
		return isExternalFeatureGateEnabled(featureGateAccessor, ExternalCloudProviderFeature, ExternalCloudProviderFeatureAzure)
	case configv1.AlibabaCloudPlatformType,
		configv1.AWSPlatformType,
		configv1.IBMCloudPlatformType,
		configv1.KubevirtPlatformType,
		configv1.NutanixPlatformType,
		configv1.OpenStackPlatformType,
		configv1.PowerVSPlatformType,
		configv1.VSpherePlatformType:
		return true, nil
	case configv1.ExternalPlatformType:
		return isExternalPlatformCCMEnabled(platformStatus, featureGateAccessor)
	default:
		// Platforms that do not have external cloud providers implemented
		return false, nil
	}
}

func isAzureStackHub(platformStatus *configv1.PlatformStatus) bool {
	return platformStatus.Azure != nil && platformStatus.Azure.CloudName == configv1.AzureStackCloud
}

func isExternalPlatformCCMEnabled(platformStatus *configv1.PlatformStatus, featureGateAccessor featuregates.FeatureGateAccess) (bool, error) {
	featureEnabled, err := isExternalFeatureGateEnabled(featureGateAccessor, ExternalCloudProviderFeature, ExternalCloudProviderFeatureExternal)
	if err != nil || !featureEnabled {
		return featureEnabled, err
	}

	if platformStatus == nil || platformStatus.External == nil {
		return false, nil
	}

	if platformStatus.External.CloudControllerManager.State == configv1.CloudControllerManagerExternal {
		return true, nil
	}

	return false, nil
}

// isExternalFeatureGateEnabled determines whether the ExternalCloudProvider feature gate is present in the current
// feature set.
func isExternalFeatureGateEnabled(featureGateAccess featuregates.FeatureGateAccess, featureGateNames ...configv1.FeatureGateName) (bool, error) {
	featureGates, err := featureGateAccess.CurrentFeatureGates()
	if err != nil {
		return false, fmt.Errorf("unable to read current featuregates: %w", err)
	}

	// If any of the desired feature gates are enabled, then the external cloud provider should be used.
	for _, featureGateName := range featureGateNames {
		if featureGates.Enabled(featureGateName) {
			return true, nil
		}
	}

	// No explicit opinion on the feature gate, assume it's not enabled.
	return false, nil
}
