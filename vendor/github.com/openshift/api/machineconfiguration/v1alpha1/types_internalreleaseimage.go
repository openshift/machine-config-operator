package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:resource:path=internalreleaseimages,scope=Cluster
// +kubebuilder:subresource:status
// +openshift:api-approved.openshift.io=https://github.com/openshift/api/pull/2510
// +openshift:file-pattern=cvoRunLevel=0000_80,operatorName=machine-config,operatorOrdering=01
// +openshift:enable:FeatureGate=NoRegistryClusterOperations
// +kubebuilder:metadata:labels=openshift.io/operator-managed=
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'cluster'",message="internalreleaseimage is a singleton, .metadata.name must be 'cluster'"

// InternalReleaseImage is used to keep track and manage a set
// of release bundles (OCP and OLM operators images) that are stored
// into the control planes nodes.
//
// Compatibility level 4: No compatibility is provided, the API can change at any point for any reason. These capabilities should not be used by applications needing long term support.
// +openshift:compatibility-gen:level=4
type InternalReleaseImage struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is the standard object's metadata.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec describes the configuration of this internal release image.
	// +required
	Spec InternalReleaseImageSpec `json:"spec,omitzero"`

	// status describes the last observed state of this internal release image.
	// +optional
	Status *InternalReleaseImageStatus `json:"status,omitempty,omitzero"`
}

// InternalReleaseImageSpec defines the desired state of a InternalReleaseImage.
type InternalReleaseImageSpec struct {
	// releases is a list of release bundle identifiers that the user wants to
	// add/remove to/from the control plane nodes.
	// Entries must be unique, keyed on the name field.
	// This field can contain between 1 and 5 entries.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=5
	// +listType=map
	// +listMapKey=name
	// +required
	Releases []InternalReleaseImageRef `json:"releases,omitempty"`
}

// InternalReleaseImageRef is used to provide a simple reference for a release
// bundle. Currently it contains only the name field.
type InternalReleaseImageRef struct {
	// name indicates the desired release bundle identifier. This field is required and must be between 1 and 64 characters long.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Name string `json:"name,omitempty"`
}

// InternalReleaseImageStatus describes the current state of a InternalReleaseImage.
type InternalReleaseImageStatus struct {
	// releases is a list of the release bundles currently owned and managed by the
	// cluster, indicating that their images can be safely pulled by any cluster entity
	// requiring them.
	// Entries must be unique, keyed on the name field.
	// This field can contain between 1 and 5 entries.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=5
	// +optional
	Releases []InternalReleaseImageStatusRef `json:"releases,omitempty"`
}

type InternalReleaseImageStatusRef struct {
	// conditions represent the observations of an internal release image current state. Valid types are:
	// Mounted, Installing, Available, Removing and Degraded.
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=5
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// name indicates the desired release bundle identifier. This field is required and must be between 1 and 64 characters long.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +required
	Name string `json:"name,omitempty"`
	// image is an OCP release image referenced by digest.
	// The format of the image pull spec is: host[:port][/namespace]/name@sha256:<digest>,
	// where the digest must be 64 characters long, and consist only of lowercase hexadecimal characters, a-f and 0-9.
	// The length of the whole spec must be between 0 to 447 characters.
	// The field is optional, and it will be provided after a release will be successfully installed.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=447
	// +kubebuilder:validation:XValidation:rule=`self == '' || (self.split('@').size() == 2 && self.split('@')[1].matches('^sha256:[a-f0-9]{64}$'))`,message="the OCI Image reference must end with a valid '@sha256:<digest>' suffix, where '<digest>' is 64 characters long"
	// +kubebuilder:validation:XValidation:rule=`self == '' || (self.split('@')[0].matches('^([a-zA-Z0-9-]+\\.)+[a-zA-Z0-9-]+(:[0-9]{2,5})?/([a-zA-Z0-9-_]{0,61}/)?[a-zA-Z0-9-_.]*?$'))`,message="the OCI Image name should follow the host[:port][/namespace]/name format, resembling a valid URL without the scheme"
	// +optional
	Image string `json:"image,omitempty"`
}

// InternalReleaseImageConditionType is each possible state for each possible MachineConfigNodeStatusInternalReleaseImageRef
// conditions type.
// +enum
type InternalReleaseImageConditionType string

const (
	// InternalReleaseImageConditionTypeMounted describes a new release, not yet installed, that has been discovered when an ISO has been attached to
	// one of the control plane nodes
	InternalReleaseImageConditionTypeMounted InternalReleaseImageConditionType = "Mounted"
	// InternalReleaseImageConditionTypeInstalling describes a new release that is getting installed in the cluster. Due the size of the data
	// transfered, the operation could take several minutes. The condition will remain in such state until all the control plane nodes will
	// complete the installing operation
	InternalReleaseImageConditionTypeInstalling InternalReleaseImageConditionType = "Installing"
	// InternalReleaseImageConditionTypeAvailable describes a release that has been successfully installed in the cluster, ready to be consumed. This
	// means that the release has been successfully installed on all the control plane nodes
	InternalReleaseImageConditionTypeAvailable InternalReleaseImageConditionType = "Available"
	// InternalReleaseImageConditionTypeRemoving describes an existing release that is getting removed from the cluster. The condition will remain in such
	// state until all the control plane nodes will complete the removal operation
	InternalReleaseImageConditionTypeRemoving InternalReleaseImageConditionType = "Removing"
	// InternalReleaseImageConditionTypeDegraded describes a failure, happened in one or more control plane nodes, for the current release
	InternalReleaseImageConditionTypeDegraded InternalReleaseImageConditionType = "Degraded"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// InternalReleaseImageList is a list of InternalReleaseImage resources
//
// Compatibility level 4: No compatibility is provided, the API can change at any point for any reason. These capabilities should not be used by applications needing long term support.
// +openshift:compatibility-gen:level=4
type InternalReleaseImageList struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is the standard list's metadata.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#metadata
	metav1.ListMeta `json:"metadata"`

	Items []InternalReleaseImage `json:"items"`
}
