package cloudprovider

import (
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

const (
	// ExternalCloudProviderFeature is the name of the external cloud provider feature gate.
	// This is used to flag to operators that the cluster should be using the external cloud-controller-manager
	// rather than the in-tree cloud controller loops.
	ExternalCloudProviderFeature = "ExternalCloudProvider"
)

// IsCloudProviderExternal is used to check whether external cloud provider settings should be used in a component.
// It checks whether the ExternalCloudProvider feature gate is enabled and whether the ExternalCloudProvider feature
// has been implemented for the platform.
func IsCloudProviderExternal(platformType configv1.PlatformType, featureGate *configv1.FeatureGate) (bool, error) {
	switch platformType {
	case configv1.AWSPlatformType,
		configv1.OpenStackPlatformType:
		// Platforms that are external based on feature gate presence
		return isExternalFeatureGateEnabled(featureGate)
	default:
		// Platforms that do not have external cloud providers implemented
		return false, nil
	}
}

// isExternalFeatureGateEnabled determines whether the ExternalCloudProvider feature gate is present in the current
// feature set.
func isExternalFeatureGateEnabled(featureGate *configv1.FeatureGate) (bool, error) {
	if featureGate == nil {
		// If no featureGate is present, then the user hasn't opted in to the external cloud controllers
		return false, nil
	}
	featureSet, ok := configv1.FeatureSets[featureGate.Spec.FeatureSet]
	if !ok {
		return false, fmt.Errorf(".spec.featureSet %q not found", featureGate.Spec.FeatureSet)
	}

	enabledFeatureGates := sets.NewString(featureSet.Enabled...)
	disabledFeatureGates := sets.NewString(featureSet.Disabled...)
	// CustomNoUpgrade will override the deafult enabled feature gates.
	if featureGate.Spec.FeatureSet == configv1.CustomNoUpgrade && featureGate.Spec.CustomNoUpgrade != nil {
		enabledFeatureGates = sets.NewString(featureGate.Spec.CustomNoUpgrade.Enabled...)
		disabledFeatureGates = sets.NewString(featureGate.Spec.CustomNoUpgrade.Disabled...)
	}

	return !disabledFeatureGates.Has(ExternalCloudProviderFeature) && enabledFeatureGates.Has(ExternalCloudProviderFeature), nil
}
