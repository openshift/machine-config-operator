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

	// component details which part of the MCO this is coming from
	// +kubebuilder:validation:Required
	// +required
	Component MCOComponent `json:"component"`

	// Mode describes if we are talking about this object in cluster or during bootstrap
	Mode MCOOperationMode `json:"mode"`
}

type MachineConfigurationStatus struct {
	StaticPodOperatorStatus `json:",inline"`
	// mostRecentState is the most recent state reporting for each component
	// +listType=map
	// +listMapKey=objectName
	// +patchMergeKey=objectName
	// +patchStrategy=merge
	MostRecentState []ProgressionCondition `json:"mostRecentState" patchStrategy:"merge" patchMergeKey:"objectName"`
	// progressionHistory contains a list of events that have happened on all objects in the MCO
	ProgressionHistory []ProgressionHistory `json:"progressionHistory"`
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

// An OperatorObject is used as the key in the map describing the most recent states
type OperatorObject string

const (
	// MCP describes a MachineConfigPool
	MCP OperatorObject = "MachineConfigPool"
	// KC describes a KubeletConfig object
	KC OperatorObject = "KubeletConfig"
	// MC describes a MachineConfig object
	MC OperatorObject = "MachineConfig"
	// CC describes a ControllerConfig object
	CC OperatorObject = "ControllerConfig"
	// Node describes a Node object
	Node OperatorObject = "Node"
)

// StateProgress is each possible state for the components of the MCO
type StateProgress string

const (
	// OperatorSyncRenderConfig describes a machine that is creating or syncing its render config
	OperatorSyncRenderConfig StateProgress = "OperatorSyncRenderConfig"
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
	MCCSync StateProgress = "SyncController"
	// MCDSync describes the process of syncing the MCD regularly
	MCDSync StateProgress = "SyncDaemon"
	// MetricSync describes the process of updating metrics and their related options
	MetricsSync StateProgress = "SyncMetrics"
	// BootstrapProgression describes processes occuring during the bootstrapping process
	BootstrapProgression StateProgress = "BootstrapProgression"
	// MachineStateErroed describes a machine that has errored during its proccess
	MachineStateErrored StateProgress = "Errored"
)

// MachineStateType describes the Kind of MachineState we are using
type MCOComponent string

const (
	// MCOperator describes behaviors in the Operator's sync functions
	MCOperator MCOComponent = "Operator"
	// MCController describes behaviors in the Controller's sync function
	MCController MCOComponent = "Controller"
	// MCDaemon describes behaviors in the Daemon's sync function
	MCDaemon MCOComponent = "Daemon"
	// MCServer describes behaviors in the Serve's sync function
	MCServer MCOComponent = "Server"
	// Metrics describes changes to metrics on the cluster
	Metrics MCOComponent = "Metrics"
)

type MCOOperationMode string

const (
	// Bootstrap
	Bootstrap string = "bootstrap"
	// InCluster
	InCluster string = "inCluster"
)

// ProgressionCondition is the base struct that contains all information about an event reported from an MCO component
type ProgressionCondition struct {
	// kind describes the type of object for this condition (node, mcp, etc)
	Kind OperatorObject `json:"kind"`
	// state describes what is happening with this object
	State StateProgress `json:"state"`
	// nameName is the object's name
	// +required
	ObjectName string `json:"objectName"`
	// phase is the general action occuring
	Phase string `json:"phase"`
	// reason is a more detailed description of the phase
	Reason string `json:"reason"`
	// time is the timestamp of this event
	Time metav1.Time `json:"time"`
}

// ProgressionHistory contains the history of an object that exists in a progressioncondition
type ProgressionHistory struct {
	// componentAndObject describes the name of the component and type of object we are dealing with
	ComponentAndObject string `json:"componentAndObject"`
	// state describes what is happening with this component
	State StateProgress `json:"state"`
	// objectType describes the type of object
	ObjectType OperatorObject `json:"objectType"`
	// phase is the general action occuring
	Phase string `json:"phase"`
	// reason is a more detailed description of the phase
	Reason string `json:"reason"`
}

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
