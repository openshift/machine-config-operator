// Code generated by applyconfiguration-gen. DO NOT EDIT.

package v1

import (
	apimachineconfigurationv1 "github.com/openshift/api/machineconfiguration/v1"
	internal "github.com/openshift/client-go/machineconfiguration/applyconfigurations/internal"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	managedfields "k8s.io/apimachinery/pkg/util/managedfields"
	v1 "k8s.io/client-go/applyconfigurations/meta/v1"
)

// MachineStateApplyConfiguration represents an declarative configuration of the MachineState type for use
// with apply.
type MachineStateApplyConfiguration struct {
	v1.TypeMetaApplyConfiguration    `json:",inline"`
	*v1.ObjectMetaApplyConfiguration `json:"metadata,omitempty"`
	Spec                             *MachineStateSpecApplyConfiguration   `json:"spec,omitempty"`
	Status                           *MachineStateStatusApplyConfiguration `json:"status,omitempty"`
}

// MachineState constructs an declarative configuration of the MachineState type for use with
// apply.
func MachineState(name string) *MachineStateApplyConfiguration {
	b := &MachineStateApplyConfiguration{}
	b.WithName(name)
	b.WithKind("MachineState")
	b.WithAPIVersion("machineconfiguration.openshift.io/v1")
	return b
}

// ExtractMachineState extracts the applied configuration owned by fieldManager from
// machineState. If no managedFields are found in machineState for fieldManager, a
// MachineStateApplyConfiguration is returned with only the Name, Namespace (if applicable),
// APIVersion and Kind populated. It is possible that no managed fields were found for because other
// field managers have taken ownership of all the fields previously owned by fieldManager, or because
// the fieldManager never owned fields any fields.
// machineState must be a unmodified MachineState API object that was retrieved from the Kubernetes API.
// ExtractMachineState provides a way to perform a extract/modify-in-place/apply workflow.
// Note that an extracted apply configuration will contain fewer fields than what the fieldManager previously
// applied if another fieldManager has updated or force applied any of the previously applied fields.
// Experimental!
func ExtractMachineState(machineState *apimachineconfigurationv1.MachineState, fieldManager string) (*MachineStateApplyConfiguration, error) {
	return extractMachineState(machineState, fieldManager, "")
}

// ExtractMachineStateStatus is the same as ExtractMachineState except
// that it extracts the status subresource applied configuration.
// Experimental!
func ExtractMachineStateStatus(machineState *apimachineconfigurationv1.MachineState, fieldManager string) (*MachineStateApplyConfiguration, error) {
	return extractMachineState(machineState, fieldManager, "status")
}

func extractMachineState(machineState *apimachineconfigurationv1.MachineState, fieldManager string, subresource string) (*MachineStateApplyConfiguration, error) {
	b := &MachineStateApplyConfiguration{}
	err := managedfields.ExtractInto(machineState, internal.Parser().Type("com.github.openshift.api.machineconfiguration.v1.MachineState"), fieldManager, b, subresource)
	if err != nil {
		return nil, err
	}
	b.WithName(machineState.Name)

	b.WithKind("MachineState")
	b.WithAPIVersion("machineconfiguration.openshift.io/v1")
	return b, nil
}

// WithKind sets the Kind field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Kind field is set to the value of the last call.
func (b *MachineStateApplyConfiguration) WithKind(value string) *MachineStateApplyConfiguration {
	b.Kind = &value
	return b
}

// WithAPIVersion sets the APIVersion field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the APIVersion field is set to the value of the last call.
func (b *MachineStateApplyConfiguration) WithAPIVersion(value string) *MachineStateApplyConfiguration {
	b.APIVersion = &value
	return b
}

// WithName sets the Name field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Name field is set to the value of the last call.
func (b *MachineStateApplyConfiguration) WithName(value string) *MachineStateApplyConfiguration {
	b.ensureObjectMetaApplyConfigurationExists()
	b.Name = &value
	return b
}

// WithGenerateName sets the GenerateName field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the GenerateName field is set to the value of the last call.
func (b *MachineStateApplyConfiguration) WithGenerateName(value string) *MachineStateApplyConfiguration {
	b.ensureObjectMetaApplyConfigurationExists()
	b.GenerateName = &value
	return b
}

// WithNamespace sets the Namespace field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Namespace field is set to the value of the last call.
func (b *MachineStateApplyConfiguration) WithNamespace(value string) *MachineStateApplyConfiguration {
	b.ensureObjectMetaApplyConfigurationExists()
	b.Namespace = &value
	return b
}

// WithUID sets the UID field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the UID field is set to the value of the last call.
func (b *MachineStateApplyConfiguration) WithUID(value types.UID) *MachineStateApplyConfiguration {
	b.ensureObjectMetaApplyConfigurationExists()
	b.UID = &value
	return b
}

// WithResourceVersion sets the ResourceVersion field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the ResourceVersion field is set to the value of the last call.
func (b *MachineStateApplyConfiguration) WithResourceVersion(value string) *MachineStateApplyConfiguration {
	b.ensureObjectMetaApplyConfigurationExists()
	b.ResourceVersion = &value
	return b
}

// WithGeneration sets the Generation field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Generation field is set to the value of the last call.
func (b *MachineStateApplyConfiguration) WithGeneration(value int64) *MachineStateApplyConfiguration {
	b.ensureObjectMetaApplyConfigurationExists()
	b.Generation = &value
	return b
}

// WithCreationTimestamp sets the CreationTimestamp field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the CreationTimestamp field is set to the value of the last call.
func (b *MachineStateApplyConfiguration) WithCreationTimestamp(value metav1.Time) *MachineStateApplyConfiguration {
	b.ensureObjectMetaApplyConfigurationExists()
	b.CreationTimestamp = &value
	return b
}

// WithDeletionTimestamp sets the DeletionTimestamp field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the DeletionTimestamp field is set to the value of the last call.
func (b *MachineStateApplyConfiguration) WithDeletionTimestamp(value metav1.Time) *MachineStateApplyConfiguration {
	b.ensureObjectMetaApplyConfigurationExists()
	b.DeletionTimestamp = &value
	return b
}

// WithDeletionGracePeriodSeconds sets the DeletionGracePeriodSeconds field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the DeletionGracePeriodSeconds field is set to the value of the last call.
func (b *MachineStateApplyConfiguration) WithDeletionGracePeriodSeconds(value int64) *MachineStateApplyConfiguration {
	b.ensureObjectMetaApplyConfigurationExists()
	b.DeletionGracePeriodSeconds = &value
	return b
}

// WithLabels puts the entries into the Labels field in the declarative configuration
// and returns the receiver, so that objects can be build by chaining "With" function invocations.
// If called multiple times, the entries provided by each call will be put on the Labels field,
// overwriting an existing map entries in Labels field with the same key.
func (b *MachineStateApplyConfiguration) WithLabels(entries map[string]string) *MachineStateApplyConfiguration {
	b.ensureObjectMetaApplyConfigurationExists()
	if b.Labels == nil && len(entries) > 0 {
		b.Labels = make(map[string]string, len(entries))
	}
	for k, v := range entries {
		b.Labels[k] = v
	}
	return b
}

// WithAnnotations puts the entries into the Annotations field in the declarative configuration
// and returns the receiver, so that objects can be build by chaining "With" function invocations.
// If called multiple times, the entries provided by each call will be put on the Annotations field,
// overwriting an existing map entries in Annotations field with the same key.
func (b *MachineStateApplyConfiguration) WithAnnotations(entries map[string]string) *MachineStateApplyConfiguration {
	b.ensureObjectMetaApplyConfigurationExists()
	if b.Annotations == nil && len(entries) > 0 {
		b.Annotations = make(map[string]string, len(entries))
	}
	for k, v := range entries {
		b.Annotations[k] = v
	}
	return b
}

// WithOwnerReferences adds the given value to the OwnerReferences field in the declarative configuration
// and returns the receiver, so that objects can be build by chaining "With" function invocations.
// If called multiple times, values provided by each call will be appended to the OwnerReferences field.
func (b *MachineStateApplyConfiguration) WithOwnerReferences(values ...*v1.OwnerReferenceApplyConfiguration) *MachineStateApplyConfiguration {
	b.ensureObjectMetaApplyConfigurationExists()
	for i := range values {
		if values[i] == nil {
			panic("nil value passed to WithOwnerReferences")
		}
		b.OwnerReferences = append(b.OwnerReferences, *values[i])
	}
	return b
}

// WithFinalizers adds the given value to the Finalizers field in the declarative configuration
// and returns the receiver, so that objects can be build by chaining "With" function invocations.
// If called multiple times, values provided by each call will be appended to the Finalizers field.
func (b *MachineStateApplyConfiguration) WithFinalizers(values ...string) *MachineStateApplyConfiguration {
	b.ensureObjectMetaApplyConfigurationExists()
	for i := range values {
		b.Finalizers = append(b.Finalizers, values[i])
	}
	return b
}

func (b *MachineStateApplyConfiguration) ensureObjectMetaApplyConfigurationExists() {
	if b.ObjectMetaApplyConfiguration == nil {
		b.ObjectMetaApplyConfiguration = &v1.ObjectMetaApplyConfiguration{}
	}
}

// WithSpec sets the Spec field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Spec field is set to the value of the last call.
func (b *MachineStateApplyConfiguration) WithSpec(value *MachineStateSpecApplyConfiguration) *MachineStateApplyConfiguration {
	b.Spec = value
	return b
}

// WithStatus sets the Status field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Status field is set to the value of the last call.
func (b *MachineStateApplyConfiguration) WithStatus(value *MachineStateStatusApplyConfiguration) *MachineStateApplyConfiguration {
	b.Status = value
	return b
}
