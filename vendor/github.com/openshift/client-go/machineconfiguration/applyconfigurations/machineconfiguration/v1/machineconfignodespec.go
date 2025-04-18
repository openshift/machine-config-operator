// Code generated by applyconfiguration-gen. DO NOT EDIT.

package v1

// MachineConfigNodeSpecApplyConfiguration represents a declarative configuration of the MachineConfigNodeSpec type for use
// with apply.
type MachineConfigNodeSpecApplyConfiguration struct {
	Node          *MCOObjectReferenceApplyConfiguration                        `json:"node,omitempty"`
	Pool          *MCOObjectReferenceApplyConfiguration                        `json:"pool,omitempty"`
	ConfigVersion *MachineConfigNodeSpecMachineConfigVersionApplyConfiguration `json:"configVersion,omitempty"`
}

// MachineConfigNodeSpecApplyConfiguration constructs a declarative configuration of the MachineConfigNodeSpec type for use with
// apply.
func MachineConfigNodeSpec() *MachineConfigNodeSpecApplyConfiguration {
	return &MachineConfigNodeSpecApplyConfiguration{}
}

// WithNode sets the Node field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Node field is set to the value of the last call.
func (b *MachineConfigNodeSpecApplyConfiguration) WithNode(value *MCOObjectReferenceApplyConfiguration) *MachineConfigNodeSpecApplyConfiguration {
	b.Node = value
	return b
}

// WithPool sets the Pool field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Pool field is set to the value of the last call.
func (b *MachineConfigNodeSpecApplyConfiguration) WithPool(value *MCOObjectReferenceApplyConfiguration) *MachineConfigNodeSpecApplyConfiguration {
	b.Pool = value
	return b
}

// WithConfigVersion sets the ConfigVersion field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the ConfigVersion field is set to the value of the last call.
func (b *MachineConfigNodeSpecApplyConfiguration) WithConfigVersion(value *MachineConfigNodeSpecMachineConfigVersionApplyConfiguration) *MachineConfigNodeSpecApplyConfiguration {
	b.ConfigVersion = value
	return b
}
