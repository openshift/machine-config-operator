package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MachineConfigNode describes the health of the Machines on the system
// Compatibility level 4: No compatibility is provided, the API can change at any point for any reason. These capabilities should not be used by applications needing long term support.
// +openshift:compatibility-gen:level=4
type MachineConfigNode struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec describes the configuration of this specific machineconfignode
	// +kubebuilder:validation:Required
	Spec MachineConfigNodeSpec `json:"spec"`

	// status describes the last observed state of this machineconfignode
	// +kubebuilder:validation:Optional
	// +optional
	Status MachineConfigNodeStatus `json:"status"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MachineConfigNodeList describes all of the MachinesStates on the system
//
// Compatibility level 4: No compatibility is provided, the API can change at any point for any reason. These capabilities should not be used by applications needing long term support.
// +openshift:compatibility-gen:level=4
type MachineConfigNodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []MachineConfigNode `json:"items"`
}

// MCOObjectReference holds information about an object the MCO either owns
// or modifies in some way
type MCOObjectReference struct {
	// kind describes the type of object thsi reference points to
	// +kubebuilder:validation:Required
	// +required
	Kind string `json:"kind"`
	// name describes what this object is called
	// +kubebuilder:validation:Required
	// +required
	Name string `json:"name"`
	// apiVersion details the group and version of this resource.
	// +kubebuilder:validation:Required
	// +required
	APIVersion string `json:"apiVersion"`
}

// MachineConfigNodeSpec describes the type of State we are managing
type MachineConfigNodeSpec struct {
	// nodeRef contains references to the node for this MachineConfigNode
	// +optional
	NodeRef MCOObjectReference `json:"nodeRef"`

	// pool is the MachineConfigPool this MachineConfigNode is describing
	// +optional
	Pool MCOObjectReference `json:"pool"`
}

// MachineConfigNodeStatus holds the reported information on a particular MachineConfigNode
type MachineConfigNodeStatus struct {
	// conditions represent the observations of a MachineConfigState's current state.
	// Known .status.conditions.type are: "UpdatePreparing", "UpdateInProgress", "UpdatePostAction", "UpdateCompleting", "Resuming", "Updated", "Ready", "Errored", "ComparingMCs", "DrainingNode", "ApplyingFilesAndOS", "CordoningNode", "RebootingNode", and "ReloadingCRIO"
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
	// mostRecentError is populated if the State reports an error.
	// +optional
	MostRecentError string `json:"mostRecentError"`
	// observedGeneration represents the generation observed by the controller.
	// +kubebuilder:validation:Required
	// +required
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// configVersion holds the current and desired config versions for the node targeted by this MachineConfigNode resource.
	// The current version represents the current machine config for the node and is updated after a successful update.
	// The desired version represents the machine config the node will attempt to update to when it next has the chance.
	// +kubebuilder:validation:Required
	// +required
	ConfigVersion MachineConfigVersion `json:"configVersion"`
}

// ConfigVersion holds the current and desired config versions
type MachineConfigVersion struct {
	// Current is the current MachineConfig a node is using
	// Must be a lowercase RFC-1123 hostname (https://tools.ietf.org/html/rfc1123)
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^([a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])(\.([a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9\-]{0,61}[a-zA-Z0-9]))*$`
	// +kubebuilder:validation:Required
	// +required
	Current string `json:"current"`
	// Desired is the MachineConfig the node wants to upgrade to.
	// Must be a lowercase RFC-1123 hostname (https://tools.ietf.org/html/rfc1123)
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^([a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])(\.([a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9\-]{0,61}[a-zA-Z0-9]))*$`
	// +kubebuilder:validation:Required
	// +required
	Desired string `json:"desired"`
}

// StateProgress is each possible state for each possible MachineConfigNodeType
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
	// MachineConfigNodeErrored describes when a machine had run into an issue
	MachineConfigNodeErrored StateProgress = "Errored"
	// MachineConfigPoolUpdateComparingMC describes the part of the preparing phase where the mco decides whether it can update
	MachineConfigPoolUpdateComparingMC StateProgress = "ComparingMCs"
	// MachineConfigPoolUpdateDraining describes the part of the inprogress phase where the node drains
	MachineConfigPoolUpdateDraining StateProgress = "DrainingNode"
	// MachineConfigPoolUpdateFilesAndOS describes the part of the inprogress phase where the nodes file and OS config change
	MachineConfigPoolUpdateFilesAndOS StateProgress = "ApplyingFilesAndOS"
	// MachineConfigPoolUpdateCordoning describes the part of the completing phase where the node cordons
	MachineConfigPoolUpdateCordoning StateProgress = "CordoningNode"
	// MachineConfigPoolUpdateRebooting describes the part of the post action phase where the node reboots itself
	MachineConfigPoolUpdateRebooting StateProgress = "RebootingNode"
	// MachineConfigPoolUpdateReloading describes the part of the post action phase where the node reloads its CRIO service
	MachineConfigPoolUpdateReloading StateProgress = "ReloadingCRIO"
)
