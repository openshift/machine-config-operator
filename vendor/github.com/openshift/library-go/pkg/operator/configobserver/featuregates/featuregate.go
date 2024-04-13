package featuregates

import (
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

// FeatureGate indicates whether a given feature is enabled or not
// This interface is heavily influenced by k8s.io/component-base, but not exactly compatible.
type FeatureGate interface {
	// Enabled returns true if the key is enabled.
	Enabled(key configv1.FeatureGateName) bool
	// KnownFeatures returns a slice of strings describing the FeatureGate's known features.
	KnownFeatures() []configv1.FeatureGateName
}

type featureGate struct {
	enabled  sets.Set[configv1.FeatureGateName]
	disabled sets.Set[configv1.FeatureGateName]
}

func NewFeatureGate(enabled, disabled []configv1.FeatureGateName) FeatureGate {
	return &featureGate{
		enabled:  sets.New[configv1.FeatureGateName](enabled...),
		disabled: sets.New[configv1.FeatureGateName](disabled...),
	}
}

func (f *featureGate) Enabled(key configv1.FeatureGateName) bool {
	if f.enabled.Has(key) {
		return true
	}
	if f.disabled.Has(key) {
		return false
	}

	panic(fmt.Errorf("feature %q is not registered in FeatureGates %v", key, f.KnownFeatures()))
}

func (f *featureGate) KnownFeatures() []configv1.FeatureGateName {
	allKnown := sets.New[string]()
	allKnown.Insert(FeatureGateNamesToStrings(f.enabled.UnsortedList())...)
	allKnown.Insert(FeatureGateNamesToStrings(f.disabled.UnsortedList())...)

	return StringsToFeatureGateNames(sets.List(allKnown))
}
