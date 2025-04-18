// Code generated by applyconfiguration-gen. DO NOT EDIT.

package v1

import (
	operatorv1 "github.com/openshift/api/operator/v1"
)

// AdditionalRoutingCapabilitiesApplyConfiguration represents a declarative configuration of the AdditionalRoutingCapabilities type for use
// with apply.
type AdditionalRoutingCapabilitiesApplyConfiguration struct {
	Providers []operatorv1.RoutingCapabilitiesProvider `json:"providers,omitempty"`
}

// AdditionalRoutingCapabilitiesApplyConfiguration constructs a declarative configuration of the AdditionalRoutingCapabilities type for use with
// apply.
func AdditionalRoutingCapabilities() *AdditionalRoutingCapabilitiesApplyConfiguration {
	return &AdditionalRoutingCapabilitiesApplyConfiguration{}
}

// WithProviders adds the given value to the Providers field in the declarative configuration
// and returns the receiver, so that objects can be build by chaining "With" function invocations.
// If called multiple times, values provided by each call will be appended to the Providers field.
func (b *AdditionalRoutingCapabilitiesApplyConfiguration) WithProviders(values ...operatorv1.RoutingCapabilitiesProvider) *AdditionalRoutingCapabilitiesApplyConfiguration {
	for i := range values {
		b.Providers = append(b.Providers, values[i])
	}
	return b
}
