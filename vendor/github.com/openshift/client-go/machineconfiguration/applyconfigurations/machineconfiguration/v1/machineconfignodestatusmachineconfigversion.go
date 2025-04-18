// Code generated by applyconfiguration-gen. DO NOT EDIT.

package v1

// MachineConfigNodeStatusMachineConfigVersionApplyConfiguration represents a declarative configuration of the MachineConfigNodeStatusMachineConfigVersion type for use
// with apply.
type MachineConfigNodeStatusMachineConfigVersionApplyConfiguration struct {
	Current *string `json:"current,omitempty"`
	Desired *string `json:"desired,omitempty"`
}

// MachineConfigNodeStatusMachineConfigVersionApplyConfiguration constructs a declarative configuration of the MachineConfigNodeStatusMachineConfigVersion type for use with
// apply.
func MachineConfigNodeStatusMachineConfigVersion() *MachineConfigNodeStatusMachineConfigVersionApplyConfiguration {
	return &MachineConfigNodeStatusMachineConfigVersionApplyConfiguration{}
}

// WithCurrent sets the Current field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Current field is set to the value of the last call.
func (b *MachineConfigNodeStatusMachineConfigVersionApplyConfiguration) WithCurrent(value string) *MachineConfigNodeStatusMachineConfigVersionApplyConfiguration {
	b.Current = &value
	return b
}

// WithDesired sets the Desired field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Desired field is set to the value of the last call.
func (b *MachineConfigNodeStatusMachineConfigVersionApplyConfiguration) WithDesired(value string) *MachineConfigNodeStatusMachineConfigVersionApplyConfiguration {
	b.Desired = &value
	return b
}
