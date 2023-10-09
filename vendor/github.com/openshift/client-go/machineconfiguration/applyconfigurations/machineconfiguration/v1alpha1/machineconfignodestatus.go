// Code generated by applyconfiguration-gen. DO NOT EDIT.

package v1alpha1

import (
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MachineConfigNodeStatusApplyConfiguration represents an declarative configuration of the MachineConfigNodeStatus type for use
// with apply.
type MachineConfigNodeStatusApplyConfiguration struct {
	Conditions         []v1.Condition                          `json:"conditions,omitempty"`
	MostRecentError    *string                                 `json:"mostRecentError,omitempty"`
	ObservedGeneration *int64                                  `json:"observedGeneration,omitempty"`
	ConfigVersion      *MachineConfigVersionApplyConfiguration `json:"configVersion,omitempty"`
}

// MachineConfigNodeStatusApplyConfiguration constructs an declarative configuration of the MachineConfigNodeStatus type for use with
// apply.
func MachineConfigNodeStatus() *MachineConfigNodeStatusApplyConfiguration {
	return &MachineConfigNodeStatusApplyConfiguration{}
}

// WithConditions adds the given value to the Conditions field in the declarative configuration
// and returns the receiver, so that objects can be build by chaining "With" function invocations.
// If called multiple times, values provided by each call will be appended to the Conditions field.
func (b *MachineConfigNodeStatusApplyConfiguration) WithConditions(values ...v1.Condition) *MachineConfigNodeStatusApplyConfiguration {
	for i := range values {
		b.Conditions = append(b.Conditions, values[i])
	}
	return b
}

// WithMostRecentError sets the MostRecentError field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the MostRecentError field is set to the value of the last call.
func (b *MachineConfigNodeStatusApplyConfiguration) WithMostRecentError(value string) *MachineConfigNodeStatusApplyConfiguration {
	b.MostRecentError = &value
	return b
}

// WithObservedGeneration sets the ObservedGeneration field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the ObservedGeneration field is set to the value of the last call.
func (b *MachineConfigNodeStatusApplyConfiguration) WithObservedGeneration(value int64) *MachineConfigNodeStatusApplyConfiguration {
	b.ObservedGeneration = &value
	return b
}

// WithConfigVersion sets the ConfigVersion field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the ConfigVersion field is set to the value of the last call.
func (b *MachineConfigNodeStatusApplyConfiguration) WithConfigVersion(value *MachineConfigVersionApplyConfiguration) *MachineConfigNodeStatusApplyConfiguration {
	b.ConfigVersion = value
	return b
}
