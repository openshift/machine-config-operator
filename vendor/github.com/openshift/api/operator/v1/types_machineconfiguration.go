package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MachineConfiguration provides information to configure an operator to manage Machine Configuration.
//
// Compatibility level 1: Stable within a major release for a minimum of 12 months or 3 minor releases (whichever is longer).
// +openshift:compatibility-gen:level=1
type MachineConfiguration struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is the standard object's metadata.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#metadata
	metav1.ObjectMeta `json:"metadata"`

	// spec is the specification of the desired behavior of the Machine Config Operator
	// +kubebuilder:validation:Required
	Spec MachineConfigurationSpec `json:"spec"`

	// status is the most recently observed status of the Machine Config Operator
	// +optional
	Status MachineConfigurationStatus `json:"status"`
}

type MachineConfigurationSpec struct {
	StaticPodOperatorSpec `json:",inline"`

	// Mode describes if we are talking about this object in cluster or during bootstrap
	// +kubebuilder:validation:Required
	// +required
	Mode MCOOperationMode `json:"mode"`
}

type MachineConfigurationComponent struct {
	// name represents the full name of this component
	Name string `json:"name"`
	// conditions is the most recent state reporting for each component
	// +listType=map
	// +listMapKey=type
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions" patchStrategy:"merge" patchMergeKey:"type"`
}

type MachineConfigurationStatus struct {
	StaticPodOperatorStatus `json:",inline"`
	// daemon describes the most recent progression of the MCD pods
	// +kubebuilder:validation:Required
	// +required
	Daemon MachineConfigurationComponent `json:"daemon"`
	// controller describes the most recent progression of the MCC pods
	// +kubebuilder:validation:Required
	// +required
	Controller MachineConfigurationComponent `json:"controller"`
	// operator describes the most recent progression of the MCO pod
	// +kubebuilder:validation:Required
	// +required
	Operator MachineConfigurationComponent `json:"operator"`
	// mostRecentError is populated if the State reports an error.
	MostRecentError string `json:"mostRecentError"`
	// health reports the overall status of the MCO given its Progress
	Health MachineConfigOperatorHealthEnum `json:"health"`
}

type MachineConfigOperatorHealthEnum string

const (
	// healthy describes an operator that is functioning properly
	Healthy MachineConfigOperatorHealthEnum = "Healthy"
	// unknown describes  an operator  who's health is unknown
	Unknown MachineConfigOperatorHealthEnum = "Unknown"
	// unhealthy describes an operator  that is not functioning properly
	UnHealthy MachineConfigOperatorHealthEnum = "Unhealthy"
)

// StateProgress is each possible state for the components of the MCO
type StateProgress string

const (
	// OperatorSync describes the overall process of syncing the operator regularly
	OperatorSync StateProgress = "OperatorSync"
	// OperatorSyncRenderConfig describes a machine that is creating or syncing its render config
	OperatorSyncRenderConfig StateProgress = "OperatorSyncRenderConfig"
	// OperatorSyncCustomResourceDefinitions describes the process of applying and verifying the CRDs related to the MCO
	OperatorSyncCustomResourceDefinitions StateProgress = "OperatorSyncCustomResourceDefinitions"
	// OperatorSyncConfigMaps describes the process of generating new data for and applying the configmaps the MCO manages
	OperatorSyncConfigMaps StateProgress = "OperatorSyncConfigmaps"
	// OperatorSyncMCP describes a machine that is syncing or applying its MachineConigPools
	OperatorSyncMCP StateProgress = "OperatorSyncMCP"
	// OperatorSyncMCD describes a machine that is syncing or applying Daemon related files.
	OperatorSyncMCD StateProgress = "OperatorSyncMCD"
	// OperatorSyncMCC describes a machine that is sycing or applying Controller related files
	OperatorSyncMCC StateProgress = "OperatorSyncMCC"
	// OperatorSyncMCS describes a machine that is syncing or applying server related files
	OperatorSyncMCS StateProgress = "OperatorSyncMCS"
	// OperatorSyncMCPRequired describes a machine in the process of ensuring and applying required MachineConfigPools
	OperatorSyncMCPRequired StateProgress = "OperatorSyncMCPRequired"
	// OperatorSyncKubeletConfig describes a machine that is syncing its KubeletConfig
	OperatorSyncKubeletConfig StateProgress = "OperatorSyncKubeletConfig"
	// MCCSync describes the process of syncing the MCC regularly
	MCCSync StateProgress = "SyncMCC"
	// MCCSyncMachineConfigPool describes the process of modifying a machineconfigpool in the MCC
	MCCSyncMachineConfigPool StateProgress = "SyncMCCMachineConfigPool"
	// MCCSyncRenderedMachineConfigs describes the process of generating and applying new machineconfigs
	MCCSyncRenderedMachineConfigs StateProgress = "SyncMCCRenderedMachineConfigs"
	// MCCSyncGeneratedKubeletConfig describes the process of generating and applying new kubelet machineconfigs
	MCCSyncGeneratedKubeletConfigs StateProgress = "SyncMCCGeneratedKubeletConfigs"
	// MCCSyncControllerConfig describes the process of filling out and applying a new controller config
	MCCSyncControllerConfig StateProgress = "SyncMCCControllerConfig"
	// MCCSyncContainerRuntimeConfig describes the process of generating and applying the container runtime machineconfigs
	MCCSyncContainerRuntimeConfig StateProgress = "SyncMCCContainerRuntimeConfig"
	// MCDSync describes the process of syncing the MCD regularly
	MCDSync StateProgress = "SyncMCD"
	// MCDSyncChangingStateAndReason indicates when the MCD changes the state on the node.
	MCDSyncChangingStateAndReason StateProgress = "SyncMCDChangingStateAndReason"
	// MCDSyncTriggeringUpdate indicates when the MCD tells the node it needs to update.
	MCDSyncTriggeringUpdate StateProgress = "SyncMCDTriggeringUpdate"
	// MetricSync describes the process of updating metrics and their related options
	MetricsSync StateProgress = "SyncMetrics"
	// BootstrapProgression describes processes occuring during the bootstrapping process
	BootstrapProgression StateProgress = "BootstrapProgression"
	// MachineStateErroed describes a machine that has errored during its proccess
	MachineStateErrored StateProgress = "Errored"
)

type MCOOperationMode string

const (
	// Bootstrap
	Bootstrap string = "bootstrap"
	// InCluster
	InCluster string = "inCluster"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MachineConfigurationList is a collection of items
//
// Compatibility level 1: Stable within a major release for a minimum of 12 months or 3 minor releases (whichever is longer).
// +openshift:compatibility-gen:level=1
type MachineConfigurationList struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is the standard list's metadata.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#metadata
	metav1.ListMeta `json:"metadata"`

	// Items contains the items
	Items []MachineConfiguration `json:"items"`
}
