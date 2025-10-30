package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// OSImageStream describes a set of streams and associated URLs available
// for the MachineConfigPools to be used as base OS images.
//
// Compatibility level 4: No compatibility is provided, the API can change at any point for any reason. These capabilities should not be used by applications needing long term support.
// +openshift:compatibility-gen:level=4
// +kubebuilder:object:root=true
// +kubebuilder:resource:path=osimagestreams,scope=Cluster
// +kubebuilder:subresource:status
// +openshift:api-approved.openshift.io=https://github.com/openshift/api/pull/2555
// +openshift:file-pattern=cvoRunLevel=0000_80,operatorName=machine-config,operatorOrdering=01
// +openshift:enable:FeatureGate=OSStreams
// +kubebuilder:metadata:labels=openshift.io/operator-managed=
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'cluster'",message="osimagestream is a singleton, .metadata.name must be 'cluster'"
type OSImageStream struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is the standard object's metadata.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec contains the desired OSImageStream config configuration.
	// +required
	Spec *OSImageStreamSpec `json:"spec,omitempty"`

	// status describes the last observed state of this OSImageStream.
	// Populated by the MachineConfigOperator after reading release metadata.
	// When not present, the controller has not yet reconciled this resource.
	// +optional
	Status *OSImageStreamStatus `json:"status,omitempty"`
}

// OSImageStreamStatus describes the current state of a OSImageStream.
// +kubebuilder:validation:XValidation:rule="!has(self.availableStreams) || size(self.availableStreams) == 0 || (has(self.defaultStream) && size(self.defaultStream) != 0)",message="defaultStream must be set when availableStreams is not empty"
// +kubebuilder:validation:XValidation:rule="!has(self.defaultStream) || self.defaultStream in self.availableStreams.map(s, s.name)",message="defaultStream must reference a stream name from availableStreams"
type OSImageStreamStatus struct {
	// availableStreams is a list of the available OS Image Streams
	// available and their associated URLs for both OS and Extensions
	// images.
	//
	// A maximum of 100 streams may be specified.
	// +optional
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=100
	// +listType=map
	// +listMapKey=name
	AvailableStreams []OSImageStreamURLSet `json:"availableStreams,omitempty"`

	// defaultStream is the name of the stream that should be used as the default
	// when no specific stream is requested by a MachineConfigPool.
	// Must reference the name of one of the streams in availableStreams.
	// Must be set when availableStreams is not empty.
	// When not set and availableStreams is empty, controllers should use the default one stated in the release image.
	//
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=70
	// +kubebuilder:validation:XValidation:rule=`self.matches('^[\\w\\.\\-]+$')`,message="The name must consist only of alphanumeric characters, hyphens ('-') and dots ('.')."
	DefaultStream string `json:"defaultStream,omitempty"`
}

// OSImageStreamSpec defines the desired state of a OSImageStream.
type OSImageStreamSpec struct {
}

type OSImageStreamURLSet struct {
	// name is the identifier of the stream.
	//
	// Must not be empty and must not exceed 70 characters in length.
	// Must only contain alphanumeric characters, hyphens ('-'), or dots ('.').
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=70
	// +kubebuilder:validation:XValidation:rule=`self.matches('^[\\w\\.\\-]+$')`,message="The name must consist only of alphanumeric characters, hyphens ('-') and dots ('.')."
	Name string `json:"name,omitempty"`

	// osImageUrl is an OS Image referenced by digest.
	//
	// The format of the URL ref is:
	// host[:port][/namespace]/name@sha256:<digest>
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=447
	// +kubebuilder:validation:XValidation:rule=`self.split('@').size() == 2 && self.split('@')[1].matches('^sha256:[a-f0-9]{64}$')`,message="the OCI Image reference must end with a valid '@sha256:<digest>' suffix, where '<digest>' is 64 characters long"
	// +kubebuilder:validation:XValidation:rule=`self.split('@')[0].matches('^([a-zA-Z0-9-]+\\.)+[a-zA-Z0-9-]+(:[0-9]{2,5})?/([a-zA-Z0-9-_]{0,61}/)?[a-zA-Z0-9-_.]*?$')`,message="the OCI Image name should follow the host[:port][/namespace]/name format, resembling a valid URL without the scheme"
	OSImageUrl string `json:"osImageUrl,omitempty"`

	// osExtensionsImageUrl is an OS Extensions Image referenced by digest.
	//
	// The format of the URL ref is:
	// host[:port][/namespace]/name@sha256:<digest>
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=447
	// +kubebuilder:validation:XValidation:rule=`self.split('@').size() == 2 && self.split('@')[1].matches('^sha256:[a-f0-9]{64}$')`,message="the OCI Image reference must end with a valid '@sha256:<digest>' suffix, where '<digest>' is 64 characters long"
	// +kubebuilder:validation:XValidation:rule=`self.split('@')[0].matches('^([a-zA-Z0-9-]+\\.)+[a-zA-Z0-9-]+(:[0-9]{2,5})?/([a-zA-Z0-9-_]{0,61}/)?[a-zA-Z0-9-_.]*?$')`,message="the OCI Image name should follow the host[:port][/namespace]/name format, resembling a valid URL without the scheme"
	OSExtensionsImageUrl string `json:"osExtensionsImageUrl,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// OSImageStreamList is a list of OSImageStream resources
//
// Compatibility level 4: No compatibility is provided, the API can change at any point for any reason. These capabilities should not be used by applications needing long term support.
// +openshift:compatibility-gen:level=4
type OSImageStreamList struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is the standard list's metadata.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#metadata
	metav1.ListMeta `json:"metadata"`

	Items []OSImageStream `json:"items"`
}
