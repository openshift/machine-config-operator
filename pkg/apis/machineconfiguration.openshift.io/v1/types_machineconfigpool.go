package v1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MachineConfigPool describes a pool of MachineConfigs.
type MachineConfigPool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec MachineConfigPoolSpec `json:"spec"`
	// +optional
	Status MachineConfigPoolStatus `json:"status"`
}

// MachineConfigPoolSpec is the spec for MachineConfigPool resource.
type MachineConfigPoolSpec struct {
	// machineConfigSelector specifies a label selector for MachineConfigs.
	// Refer https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/ on how label and selectors work.
	MachineConfigSelector *metav1.LabelSelector `json:"machineConfigSelector,omitempty"`

	// nodeSelector specifies a label selector for Machines
	NodeSelector *metav1.LabelSelector `json:"nodeSelector,omitempty"`

	// paused specifies whether or not changes to this machine config pool should be stopped.
	// This includes generating new desiredMachineConfig and update of machines.
	Paused bool `json:"paused"`

	// maxUnavailable specifies the percentage or constant number of machines that can be updating at any given time.
	// default is 1.
	MaxUnavailable *intstr.IntOrString `json:"maxUnavailable,omitempty"`

	// The targeted MachineConfig object for the machine config pool.
	Configuration MachineConfigPoolStatusConfiguration `json:"configuration"`
}

// MachineConfigPoolStatus is the status for MachineConfigPool resource.
type MachineConfigPoolStatus struct {
	// observedGeneration represents the generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// configuration represents the current MachineConfig object for the machine config pool.
	Configuration MachineConfigPoolStatusConfiguration `json:"configuration"`

	// machineCount represents the total number of machines in the machine config pool.
	MachineCount int32 `json:"machineCount"`

	// updatedMachineCount represents the total number of machines targeted by the pool that have the CurrentMachineConfig as their config.
	UpdatedMachineCount int32 `json:"updatedMachineCount"`

	// readyMachineCount represents the total number of ready machines targeted by the pool.
	ReadyMachineCount int32 `json:"readyMachineCount"`

	// unavailableMachineCount represents the total number of unavailable (non-ready) machines targeted by the pool.
	// A node is marked unavailable if it is in updating state or NodeReady condition is false.
	UnavailableMachineCount int32 `json:"unavailableMachineCount"`

	// degradedMachineCount represents the total number of machines marked degraded (or unreconcilable).
	// A node is marked degraded if applying a configuration failed..
	DegradedMachineCount int32 `json:"degradedMachineCount"`

	// conditions represents the latest available observations of current state.
	// +optional
	Conditions []MachineConfigPoolCondition `json:"conditions"`
}

// MachineConfigPoolStatusConfiguration stores the current configuration for the pool, and
// optionally also stores the list of MachineConfig objects used to generate the configuration.
type MachineConfigPoolStatusConfiguration struct {
	corev1.ObjectReference `json:",inline"`

	// source is the list of MachineConfig objects that were used to generate the single MachineConfig object specified in `content`.
	// +optional
	Source []corev1.ObjectReference `json:"source,omitempty"`
}

// MachineConfigPoolCondition contains condition information for an MachineConfigPool.
type MachineConfigPoolCondition struct {
	// type of the condition, currently ('Done', 'Updating', 'Failed').
	Type MachineConfigPoolConditionType `json:"type"`

	// status of the condition, one of ('True', 'False', 'Unknown').
	Status corev1.ConditionStatus `json:"status"`

	// lastTransitionTime is the timestamp corresponding to the last status
	// change of this condition.
	// +nullable
	LastTransitionTime metav1.Time `json:"lastTransitionTime"`

	// reason is a brief machine readable explanation for the condition's last
	// transition.
	Reason string `json:"reason"`

	// message is a human readable description of the details of the last
	// transition, complementing reason.
	Message string `json:"message"`
}

// MachineConfigPoolConditionType valid conditions of a MachineConfigPool
type MachineConfigPoolConditionType string

const (
	// MachineConfigPoolUpdated means MachineConfigPool is updated completely.
	// When the all the machines in the pool are updated to the correct machine config.
	MachineConfigPoolUpdated MachineConfigPoolConditionType = "Updated"

	// MachineConfigPoolUpdating means MachineConfigPool is updating.
	// When at least one of machine is not either not updated or is in the process of updating
	// to the desired machine config.
	MachineConfigPoolUpdating MachineConfigPoolConditionType = "Updating"

	// MachineConfigPoolNodeDegraded means the update for one of the machine is not progressing
	MachineConfigPoolNodeDegraded MachineConfigPoolConditionType = "NodeDegraded"

	// MachineConfigPoolRenderDegraded means the rendered configuration for the pool cannot be generated because of an error
	MachineConfigPoolRenderDegraded MachineConfigPoolConditionType = "RenderDegraded"

	// MachineConfigPoolDegraded is the overall status of the pool based, today, on whether we fail with NodeDegraded or RenderDegraded
	MachineConfigPoolDegraded MachineConfigPoolConditionType = "Degraded"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MachineConfigPoolList is a list of MachineConfigPool resources
type MachineConfigPoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []MachineConfigPool `json:"items"`
}
