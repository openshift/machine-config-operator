package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Feature holds cluster-wide information about feature gates.  The canonical name is `cluster`
//
// Compatibility level 1: Stable within a major release for a minimum of 12 months or 3 minor releases (whichever is longer).
// +openshift:compatibility-gen:level=1
// +openshift:api-approved.openshift.io=https://github.com/openshift/api/pull/470
// +openshift:file-pattern=cvoRunLevel=0000_10,operatorName=config-operator,operatorOrdering=01
// +kubebuilder:object:root=true
// +kubebuilder:resource:path=featuregates,scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:metadata:annotations=release.openshift.io/bootstrap-required=true
type FeatureGate struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is the standard object's metadata.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#metadata
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec holds user settable values for configuration
	// +required
	// +kubebuilder:validation:XValidation:rule="has(oldSelf.featureSet) ? has(self.featureSet) : true",message=".spec.featureSet cannot be removed"
	Spec FeatureGateSpec `json:"spec"`
	// status holds observed values from the cluster. They may not be overridden.
	// +optional
	Status FeatureGateStatus `json:"status"`
}

type FeatureSet string

var (
	// Default feature set that allows upgrades.
	Default FeatureSet = ""

	// TechPreviewNoUpgrade turns on tech preview features that are not part of the normal supported platform. Turning
	// this feature set on CANNOT BE UNDONE and PREVENTS UPGRADES.
	TechPreviewNoUpgrade FeatureSet = "TechPreviewNoUpgrade"

	// DevPreviewNoUpgrade turns on dev preview features that are not part of the normal supported platform. Turning
	// this feature set on CANNOT BE UNDONE and PREVENTS UPGRADES.
	DevPreviewNoUpgrade FeatureSet = "DevPreviewNoUpgrade"

	// CustomNoUpgrade allows the enabling or disabling of any feature. Turning this feature set on IS NOT SUPPORTED, CANNOT BE UNDONE, and PREVENTS UPGRADES.
	// Because of its nature, this setting cannot be validated.  If you have any typos or accidentally apply invalid combinations
	// your cluster may fail in an unrecoverable way.
	CustomNoUpgrade FeatureSet = "CustomNoUpgrade"

	// AllFixedFeatureSets are the featuresets that have known featuregates.  Custom doesn't for instance.  LatencySensitive is dead
	AllFixedFeatureSets = []FeatureSet{Default, TechPreviewNoUpgrade, DevPreviewNoUpgrade}
)

type FeatureGateSpec struct {
	FeatureGateSelection `json:",inline"`
}

// +union
type FeatureGateSelection struct {
	// featureSet changes the list of features in the cluster.  The default is empty.  Be very careful adjusting this setting.
	// Turning on or off features may cause irreversible changes in your cluster which cannot be undone.
	// +unionDiscriminator
	// +optional
	// +kubebuilder:validation:Enum=CustomNoUpgrade;DevPreviewNoUpgrade;TechPreviewNoUpgrade;""
	// +kubebuilder:validation:XValidation:rule="oldSelf == 'CustomNoUpgrade' ? self == 'CustomNoUpgrade' : true",message="CustomNoUpgrade may not be changed"
	// +kubebuilder:validation:XValidation:rule="oldSelf == 'TechPreviewNoUpgrade' ? self == 'TechPreviewNoUpgrade' : true",message="TechPreviewNoUpgrade may not be changed"
	// +kubebuilder:validation:XValidation:rule="oldSelf == 'DevPreviewNoUpgrade' ? self == 'DevPreviewNoUpgrade' : true",message="DevPreviewNoUpgrade may not be changed"
	FeatureSet FeatureSet `json:"featureSet,omitempty"`

	// customNoUpgrade allows the enabling or disabling of any feature. Turning this feature set on IS NOT SUPPORTED, CANNOT BE UNDONE, and PREVENTS UPGRADES.
	// Because of its nature, this setting cannot be validated.  If you have any typos or accidentally apply invalid combinations
	// your cluster may fail in an unrecoverable way.  featureSet must equal "CustomNoUpgrade" must be set to use this field.
	// +optional
	// +nullable
	CustomNoUpgrade *CustomFeatureGates `json:"customNoUpgrade,omitempty"`
}

type CustomFeatureGates struct {
	// enabled is a list of all feature gates that you want to force on
	// +optional
	Enabled []FeatureGateName `json:"enabled,omitempty"`
	// disabled is a list of all feature gates that you want to force off
	// +optional
	Disabled []FeatureGateName `json:"disabled,omitempty"`
}

// FeatureGateName is a string to enforce patterns on the name of a FeatureGate
// +kubebuilder:validation:Pattern=`^([A-Za-z0-9-]+\.)*[A-Za-z0-9-]+\.?$`
type FeatureGateName string

type FeatureGateStatus struct {
	// conditions represent the observations of the current state.
	// Known .status.conditions.type are: "DeterminationDegraded"
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// featureGates contains a list of enabled and disabled featureGates that are keyed by payloadVersion.
	// Operators other than the CVO and cluster-config-operator, must read the .status.featureGates, locate
	// the version they are managing, find the enabled/disabled featuregates and make the operand and operator match.
	// The enabled/disabled values for a particular version may change during the life of the cluster as various
	// .spec.featureSet values are selected.
	// Operators may choose to restart their processes to pick up these changes, but remembering past enable/disable
	// lists is beyond the scope of this API and is the responsibility of individual operators.
	// Only featureGates with .version in the ClusterVersion.status will be present in this list.
	// +listType=map
	// +listMapKey=version
	FeatureGates []FeatureGateDetails `json:"featureGates"`
}

type FeatureGateDetails struct {
	// version matches the version provided by the ClusterVersion and in the ClusterOperator.Status.Versions field.
	// +required
	Version string `json:"version"`
	// enabled is a list of all feature gates that are enabled in the cluster for the named version.
	// +optional
	Enabled []FeatureGateAttributes `json:"enabled"`
	// disabled is a list of all feature gates that are disabled in the cluster for the named version.
	// +optional
	Disabled []FeatureGateAttributes `json:"disabled"`
	// renderedMinimumComponentVersions are the component versions that the feature gate list of this status were rendered from.
	// Currently, the only supported component is Kubelet, and setting the MinimumComponentVersion.Component to "Kubelet" will mean
	// feature set was rendered given the minimumKubeletVersion in the nodes.config object was lower than or equal to the given MinimumComponentVersion.Version
	// +kubebuilder:validation:MaxItems:=1
	// +listType=map
	// +listMapKey=component
	// +openshift:enable:FeatureGate=MinimumKubeletVersion
	// +optional
	RenderedMinimumComponentVersions []MinimumComponentVersion `json:"renderedMinimumComponentVersions,omitempty"`
}

type FeatureGateAttributes struct {
	// name is the name of the FeatureGate.
	// +required
	Name FeatureGateName `json:"name"`

	// requiredMinimumComponentVersions is a list of component/version pairs that declares the is the lowest version the given
	// component may be in this cluster to have this feature turned on in the Default featureset.
	// Currently, the only supported component is Kubelet, and setting the MinimumComponentVersion.Component to "Kubelet" will mean
	// this feature will be added to the Default set if the minimumKubeletVersion in the nodes.config object is lower than
	// or equal to the given MinimumComponentVersion.Version
	// +kubebuilder:validation:MaxItems:=1
	// +listType=map
	// +listMapKey=component
	// +openshift:enable:FeatureGate=MinimumKubeletVersion
	// +optional
	RequiredMinimumComponentVersions []MinimumComponentVersion `json:"requiredMinimumComponentVersions,omitempty"`

	// possible (probable?) future additions include
	// 1. support level (Stable, ServiceDeliveryOnly, TechPreview, DevPreview)
	// 2. description
}

// MinimumComponentVersion is a pair of Component and Version that specifies the required minimum Version of the given Component
// to enable this feature.
type MinimumComponentVersion struct {
	// component is the entity whose version must be above a certain version.
	// The only valid value is Kubelet
	// +required
	Component MinimumComponent `json:"component"`
	// version is the minimum version the given component may be in this cluster.
	// version must be in semver format (x.y.z) and must consist only of numbers and periods (.).
	// Note: this is the version of the component, not Openshift. For instance, when Component is "Kubelet", it is a required version of the Kubelet (i.e: kubernetes version, like 1.32.0),
	// not the corresponding Openshift version (4.19.0)
	// +kubebuilder:validation:XValidation:rule="self.matches('^[0-9]*.[0-9]*.[0-9]*$')",message="minmumKubeletVersion must be in a semver compatible format of x.y.z, or empty"
	// +kubebuilder:validation:MaxLength:=8
	// +required
	Version string `json:"version"`
}

// MinimumComponent is a type defining a component that can have a minimum version declared.
// Currently, the only supported value is "Kubelet".
// +kubebuilder:validation:Enum:=Kubelet
type MinimumComponent string

// MinimumComponentKubelet can be used to define the required minimum version for kubelets.
// It will be compared against the "minimumKubeletVersion" field in the nodes.config.openshift.io object.
var MinimumComponentKubelet MinimumComponent = "Kubelet"

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Compatibility level 1: Stable within a major release for a minimum of 12 months or 3 minor releases (whichever is longer).
// +openshift:compatibility-gen:level=1
type FeatureGateList struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is the standard list's metadata.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#metadata
	metav1.ListMeta `json:"metadata"`

	Items []FeatureGate `json:"items"`
}
