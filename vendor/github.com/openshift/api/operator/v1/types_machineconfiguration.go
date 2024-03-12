package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:resource:path=machineconfigurations,scope=Cluster
// +kubebuilder:subresource:status
// +openshift:api-approved.openshift.io=https://github.com/openshift/api/pull/1453
// +openshift:file-pattern=0000_80_machine-config-operator_01_configMARKERS.crd.yaml

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

	// TODO(jkyros): This is where we put our knobs and dials

	// managedBootImages allows configuration for the management of boot images for machine
	// resources within the cluster. This configuration allows users to select resources that should
	// be updated to the latest boot images during cluster upgrades, ensuring that new machines
	// always boot with the current cluster version's boot image. When omitted, no boot images
	// will be updated.
	// +openshift:enable:FeatureGate=ManagedBootImages
	// +optional
	ManagedBootImages ManagedBootImages `json:"managedBootImages"`

	// nodeDisruptionPolicy allows an admin to set granular node disruption actions for
	// MachineConfig-based updates, such as drains, service reloads, etc. Specifying this will allow
	// for less downtime when doing small configuration updates to the cluster. This configuration
	// has no effect on cluster upgrades which will still incur node disruption where required.
	// +openshift:enable:FeatureGate=NodeDisruptionPolicy
	// +optional
	NodeDisruptionPolicy NodeDisruptionPolicyConfig `json:"nodeDisruptionPolicy"`
}

type MachineConfigurationStatus struct {
	// observedGeneration is the last generation change you've dealt with
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions is a list of conditions and their status
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// nodeDisruptionPolicyStatus status reflects what the latest cluster-validated policies are,
	// and will be used by the Machine Config Daemon during future node updates.
	// +openshift:enable:FeatureGate=NodeDisruptionPolicy
	// +optional
	NodeDisruptionPolicyStatus NodeDisruptionPolicyStatus `json:"nodeDisruptionPolicyStatus"`
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

type ManagedBootImages struct {
	// machineManagers can be used to register machine management resources for boot image updates. The Machine Config Operator
	// will watch for changes to this list. Only one entry is permitted per type of machine management resource.
	// +optional
	// +listType=map
	// +listMapKey=resource
	// +listMapKey=apiGroup
	MachineManagers []MachineManager `json:"machineManagers"`
}

// MachineManager describes a target machine resource that is registered for boot image updates. It stores identifying information
// such as the resource type and the API Group of the resource. It also provides granular control via the selection field.
type MachineManager struct {
	// resource is the machine management resource's type.
	// The only current valid value is machinesets.
	// machinesets means that the machine manager will only register resources of the kind MachineSet.
	// +kubebuilder:validation:Required
	Resource MachineManagerMachineSetsResourceType `json:"resource"`

	// apiGroup is name of the APIGroup that the machine management resource belongs to.
	// The only current valid value is machine.openshift.io.
	// machine.openshift.io means that the machine manager will only register resources that belong to OpenShift machine API group.
	// +kubebuilder:validation:Required
	APIGroup MachineManagerMachineSetsAPIGroupType `json:"apiGroup"`

	// selection allows granular control of the machine management resources that will be registered for boot image updates.
	// +kubebuilder:validation:Required
	Selection MachineManagerSelector `json:"selection"`
}

// +kubebuilder:validation:XValidation:rule="has(self.mode) && self.mode == 'Partial' ?  has(self.partial) : !has(self.partial)",message="Partial is required when type is partial, and forbidden otherwise"
// +union
type MachineManagerSelector struct {
	// mode determines how machine managers will be selected for updates.
	// Valid values are All and Partial.
	// All means that every resource matched by the machine manager will be updated.
	// Partial requires specified selector(s) and allows customisation of which resources matched by the machine manager will be updated.
	// +unionDiscriminator
	// +kubebuilder:validation:Required
	Mode MachineManagerSelectorMode `json:"mode"`

	// partial provides label selector(s) that can be used to match machine management resources.
	// Only permitted when mode is set to "Partial".
	// +optional
	Partial *PartialSelector `json:"partial,omitempty"`
}

// PartialSelector provides label selector(s) that can be used to match machine management resources.
type PartialSelector struct {
	// machineResourceSelector is a label selector that can be used to select machine resources like MachineSets.
	// +kubebuilder:validation:Required
	MachineResourceSelector *metav1.LabelSelector `json:"machineResourceSelector,omitempty"`
}

// MachineManagerSelectorMode is a string enum used in the MachineManagerSelector union discriminator.
// +kubebuilder:validation:Enum:="All";"Partial"
type MachineManagerSelectorMode string

const (
	// All represents a configuration mode that registers all resources specified by the parent MachineManager for boot image updates.
	All MachineManagerSelectorMode = "All"

	// Partial represents a configuration mode that will register resources specified by the parent MachineManager only
	// if they match with the label selector.
	Partial MachineManagerSelectorMode = "Partial"
)

// MachineManagerManagedResourceType is a string enum used in the MachineManager type to describe the resource
// type to be registered.
// +kubebuilder:validation:Enum:="machinesets"
type MachineManagerMachineSetsResourceType string

const (
	// MachineSets represent the MachineSet resource type, which manage a group of machines and belong to the Openshift machine API group.
	MachineSets MachineManagerMachineSetsResourceType = "machinesets"
)

// MachineManagerManagedAPIGroupType is a string enum used in in the MachineManager type to describe the APIGroup
// of the resource type being registered.
// +kubebuilder:validation:Enum:="machine.openshift.io"
type MachineManagerMachineSetsAPIGroupType string

const (
	// MachineAPI represent the traditional MAPI Group that a machineset may belong to.
	// This feature only supports MAPI machinesets at this time.
	MachineAPI MachineManagerMachineSetsAPIGroupType = "machine.openshift.io"
)

type NodeDisruptionPolicyStatus struct {
	// clusterPolicies is a merge of cluster default and user provided node disruption policies.
	// +optional
	ClusterPolicies NodeDisruptionPolicyClusterStatus `json:"clusterPolicies"`
}

// NodeDisruptionPolicyConfig is the overall spec definition for files/units/sshkeys
type NodeDisruptionPolicyConfig struct {
	// files is a list of MachineConfig file definitions and actions to take to changes on those paths
	// +optional
	// +listType=map
	// +listMapKey=path
	// +kubebuilder:validation:MaxItems=50
	Files []NodeDisruptionPolicyFile `json:"files"`
	// units is a list MachineConfig unit definitions and actions to take on changes to those services
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=50
	Units []NodeDisruptionPolicyUnit `json:"units"`
	// sshkey maps to the ignition.sshkeys field in the MachineConfig object, definition an action for this
	// will apply to all sshkey changes in the cluster
	// +optional
	SSHKey NodeDisruptionPolicySSHKey `json:"sshkey"`
}

// NodeDisruptionPolicyClusterStatus is the type for the status object, rendered by the controller as a
// merge of cluster defaults and user provided policies
type NodeDisruptionPolicyClusterStatus struct {
	// files is a list of MachineConfig file definitions and actions to take to changes on those paths
	// +optional
	// +listType=map
	// +listMapKey=path
	// +kubebuilder:validation:MaxItems=100
	Files []NodeDisruptionPolicyFile `json:"files,omitempty"`
	// units is a list MachineConfig unit definitions and actions to take on changes to those services
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=100
	Units []NodeDisruptionPolicyUnit `json:"units,omitempty"`
	// sshkey is the overall sshkey MachineConfig definition
	// +optional
	SSHKey NodeDisruptionPolicySSHKey `json:"sshkey,omitempty"`
}

// NodeDisruptionPolicyFile is a file entry and corresponding actions to take
type NodeDisruptionPolicyFile struct {
	// path is the file path to a file on disk managed through a MachineConfig.
	// Actions specified will be applied when changes to the file at the path
	// configured in this field.
	// +kubebuilder:validation:Required
	Path string `json:"path"`
	// actions represents the series of commands to be executed on changes to the file at
	// corresponding file path. This is an atomic list, which will be validated by
	// the MachineConfigOperator, with any conflicts reflecting as an error in the
	// status. If validation is successful, the actions will be applied in the order
	// they are set in the list. If there are other incoming changes to other MachineConfig
	// entries in the same update that require a reboot, the reboot will supercede these actions.
	// +kubebuilder:validation:Required
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=10
	// +kubebuilder:validation:XValidation:rule="self.exists(x, x.type=='Reboot') ? size(self) == 1 : true", message="Reboot action can only be specified standalone, as it will override any other actions"
	// +kubebuilder:validation:XValidation:rule="self.exists(x, x.type=='None') ? size(self) == 1 : true", message="None action can only be specified standalone, as it will override any other actions"
	Actions []NodeDisruptionPolicyAction `json:"actions"`
}

// NodeDisruptionPolicyUnit is a systemd unit name and corresponding actions to take
type NodeDisruptionPolicyUnit struct {
	// name represents the service name of a systemd service managed through a MachineConfig
	// Actions specified will be applied for changes to the named service
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self.endsWith('.service') || self.endsWith('.socket') || self.endsWith('.device') || self.endsWith('.mount') || self.endsWith('.automount') || self.endsWith('.swap') || self.endsWith('.target') || self.endsWith('.path') || self.endsWith('.timer') || self.endsWith('.snapshot') || self.endsWith('.slice') || self.endsWith('.scope')",message="Invalid service name schema, needs to be in the format of ${NAME}.${SERVICETYPE}"
	Name string `json:"name"`
	// actions represents the series of commands to be executed on changes to the unit
	// defined by the unit name. This is an atomic list, which will be validated by
	// the MachineConfigOperator, with any conflicts reflecting as an error in the
	// status. If validation is successful, the actions will be applied in the order
	// they are set in the list. If there are other incoming changes to other MachineConfig
	// entries in the same update that require a reboot, the reboot will supercede these actions.
	// +kubebuilder:validation:Required
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=10
	// +kubebuilder:validation:XValidation:rule="self.exists(x, x.type=='Reboot') ? size(self) == 1 : true", message="Reboot action can only be specified standalone, as it will override any other actions"
	// +kubebuilder:validation:XValidation:rule="self.exists(x, x.type=='None') ? size(self) == 1 : true", message="None action can only be specified standalone, as it will override any other actions"
	Actions []NodeDisruptionPolicyAction `json:"actions"`
}

// NodeDisruptionPolicySSHKey is actions to take for any SSHKey change
type NodeDisruptionPolicySSHKey struct {
	// actions represents the series of commands to be executed on changes to any sshkey changes
	// through MachineConfig objects. This is an atomic list, which will be validated by
	// the MachineConfigOperator, with any conflicts reflecting as an error in the
	// status. If validation is successful, the actions will be applied in the order
	// they are set in the list. If there are other incoming changes to other MachineConfig
	// entries in the same update that require a reboot, the reboot will supercede these actions.
	// +kubebuilder:validation:Required
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=10
	// +kubebuilder:validation:XValidation:rule="self.exists(x, x.type=='Reboot') ? size(self) == 1 : true", message="Reboot action can only be specified standalone, as it will override any other actions"
	// +kubebuilder:validation:XValidation:rule="self.exists(x, x.type=='None') ? size(self) == 1 : true", message="None action can only be specified standalone, as it will override any other actions"
	Actions []NodeDisruptionPolicyAction `json:"actions"`
}

// +kubebuilder:validation:XValidation:rule="has(self.type) && self.type == 'Reload' ? has(self.reload) : !has(self.reload)",message="Reload is required when type is reload, and forbidden otherwise"
// +kubebuilder:validation:XValidation:rule="has(self.type) && self.type == 'Restart' ? has(self.restart) : !has(self.restart)",message="Restart is required when type is restart, and forbidden otherwise"
// +union
type NodeDisruptionPolicyAction struct {
	// type represents the commands that will be carried out if this NodeDisruptionPolicyActionType is executed
	// Valid value(s): Reboot, Drain, Reload, Restart, DaemonReload, None, Special
	// reload/restart requires a corresponding service target specified in the reload/restart field.
	// Other values require no further configuration
	// +unionDiscriminator
	// +kubebuilder:validation:Required
	Type NodeDisruptionPolicyActionType `json:"type"`
	// reload specifies the service to reload, only valid if type is reload
	// +optional
	Reload *ReloadService `json:"reload,omitempty"`
	// restart specifies the service to restart, only valid if type is restart
	// +optional
	Restart *RestartService `json:"restart,omitempty"`
}

// ReloadService allows the user to specify the services to be reloaded
type ReloadService struct {
	// serviceName is the full name (e.g. crio.service) of the service to be reloaded
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self.endsWith('.service') || self.endsWith('.socket') || self.endsWith('.device') || self.endsWith('.mount') || self.endsWith('.automount') || self.endsWith('.swap') || self.endsWith('.target') || self.endsWith('.path') || self.endsWith('.timer') || self.endsWith('.snapshot') || self.endsWith('.slice') || self.endsWith('.scope')",message="Invalid service name schema, needs to be in the format of ${NAME}.${SERVICETYPE}"
	ServiceName string `json:"serviceName,omitempty"`
}

// RestartService allows the user to specify the services to be restarted
type RestartService struct {
	// serviceName is the full name (e.g. crio.service) of the service to be restarted
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self.endsWith('.service') || self.endsWith('.socket') || self.endsWith('.device') || self.endsWith('.mount') || self.endsWith('.automount') || self.endsWith('.swap') || self.endsWith('.target') || self.endsWith('.path') || self.endsWith('.timer') || self.endsWith('.snapshot') || self.endsWith('.slice') || self.endsWith('.scope')",message="Invalid service name schema, needs to be in the format of ${NAME}.${SERVICETYPE}"
	ServiceName string `json:"serviceName,omitempty"`
}

// NodeDisruptionPolicyActionType is a string enum used in a NodeDisruptionPolicyAction object. They describe an action to be performed.
// +kubebuilder:validation:Enum:="Reboot";"Drain";"Reload";"Restart";"DaemonReload";"None";"Special"
type NodeDisruptionPolicyActionType string

const (
	// Reboot represents an action that will cause nodes to be rebooted. This is the default action by the MCO
	// if a reboot policy is not found for a change/update being performed by the MCO.
	Reboot NodeDisruptionPolicyActionType = "Reboot"

	// Drain represents an action that will cause nodes to be drained of their workloads.
	Drain NodeDisruptionPolicyActionType = "Drain"

	// Reload represents an action that will cause nodes to reload the service described by the Target field.
	Reload NodeDisruptionPolicyActionType = "Reload"

	// Restart represents an action that will cause nodes to restart the service described by the Target field.
	Restart NodeDisruptionPolicyActionType = "Restart"

	// DaemonReload represents an action that TBD
	DaemonReload NodeDisruptionPolicyActionType = "DaemonReload"

	// None represents an action that no handling is required by the MCO.
	None NodeDisruptionPolicyActionType = "None"

	// Special represents an action that is internal to the MCO, and is not allowed in user defined NodeDisruption policies.
	Special NodeDisruptionPolicyActionType = "Special"
)
