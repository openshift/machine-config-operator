package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MachineConfigState describes the health of the Machines on the system
// Compatibility level 4: No compatibility is provided, the API can change at any point for any reason. These capabilities should not be used by applications needing long term support.
// +openshift:compatibility-gen:level=4
type MachineConfigState struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:Required
	Spec MachineConfigStateSpec `json:"spec"`
	// +optional
	Status MachineConfigStateStatus `json:"status"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MachineConfigStateList describes all of the MachinesStates on the system
//
// Compatibility level 4: No compatibility is provided, the API can change at any point for any reason. These capabilities should not be used by applications needing long term support.
// +openshift:compatibility-gen:level=4
type MachineConfigStateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []MachineConfigState `json:"items"`
}

// MachineConfigStateSpec describes the type of State we are managing
type MachineConfigStateSpec struct {
	// configuration describes the object reference information of a MachineConfigState
	Config MachineConfigStateConfig `json:"configuration"`
}

// MachineConfigStateStatus holds the reported information on a particular MachineConfigState
type MachineConfigStateStatus struct {
	corev1.ObjectReference `json:",inline"`
	// conditions represents the latest available observations of current state.
	// +listType=atomic
	// +optional
	Conditions []ProgressionCondition `json:"conditions"`
	// mostRecentError is populated if the State reports an error.
	MostRecentError string `json:"mostRecentError"`
	// health reports the overall status of this MachineConfigState Given its Progress
	Health MachineConfigStateHealthEnumeration `json:"health"`
	// observedGeneration represents the generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// configVersion holds the current and desired config versions
	ConfigVersion MachineConfigVersion `json:"configVersion"`
}

// ConfigVersion holds the current and desired config versions
type MachineConfigVersion struct {
	// Current is the current MachineConfig a node is using
	Current string `json:"current"`
	// Desired is the MachineConfig the node wants to upgrade to.
	Desired string `json:"desired"`
}

type MachineConfigStateHealthEnumeration string

const (
	// healthy describes a node that is functioning properly
	Healthy MachineConfigStateHealthEnumeration = "Healthy"
	// unknown describes a node who's health is unknown
	Unknown MachineConfigStateHealthEnumeration = "Unknown"
	// unhealthy describes a node that is not functioning properly
	UnHealthy MachineConfigStateHealthEnumeration = "Unhealthy"
)

// MachineConfigStateConfig describes the configuration of a MachineConfigState
type MachineConfigStateConfig struct {
	corev1.ObjectReference `json:",inline"`

	// nodeRef contains references to the node for this MachineConfigState
	NodeRef corev1.ObjectReference `json:"nodeRef"`

	// pool is the MachineConfigPool this MachineConfigState is describing
	Pool string `json:"pool"`
}

// StateProgress is each possible state for each possible MachineConfigStateType
// UpgradeProgression Kind will only use the "MachinConfigPoolUpdate..." types for example
type StateProgress string

const (
	// MachineConfigPoolUpdatePreparing describes a machine that is preparing in the daemon to trigger an update
	MachineConfigPoolUpdatePreparing StateProgress = "UpdatePreparing"
	// MachineConfigPoolUpdateInProgress describes a machine that is in progress of updating
	MachineConfigPoolUpdateInProgress StateProgress = "UpdateInProgress"
	// MachineConfigPoolUpdatePostAction describes a machine that is executing its post update action
	MachineConfigPoolUpdatePostAction StateProgress = "UpdatePostAction"
	// MachineConfigPoolUpdateCompleting describes a machine that is in the process of resuming normal processes
	MachineConfigPoolUpdateCompleting StateProgress = "UpdateCompleting"
	// MachineConfigPoolUpdateComplete describes a machine that has a matching desired and current config after executing an update
	MachineConfigPoolUpdateComplete StateProgress = "Updated"
	// MachineConfigPoolUpdateResuming describes a machine that is in the process of resuming normal processes
	MachineConfigPoolResuming StateProgress = "Resuming"
	// MachineConfigPoolReady describes a machine that has a matching desired and current config
	MachineConfigPoolReady StateProgress = "Ready"
	// MachineConfigStateErrored describes when a machine had run into an issue
	MachineConfigStateErrored StateProgress = "Errored"
)

// ProgressionCondition is the base struct that contains all information about an event reported from an MCO component
type ProgressionCondition struct {
	// state describes what is happening with this object
	State StateProgress `json:"state"`
	// phase is the general action occuring
	Phase string `json:"phase"`
	// reason is a more detailed description of the phase
	Reason string `json:"reason"`
	// time is the timestamp of this event
	Time metav1.Time `json:"time"`
}
