package main

// contentMarshaler is a function type that converts an interface{} value to its byte representation.
// Abstraction of JSON/YAML marshaller.
type contentMarshaler func(interface{}) ([]byte, error)

// contentUnmarshaller is a function type that a byte representation to an interface{}.
// Abstraction of JSON/YAML unmarshaller.
type contentUnmarshaller func([]byte, interface{}) error

// KubernetesMetaListInterface represents a generic Kubernetes List resource structure.
// It contains a collection of items that can be any type of Kubernetes resource.
type KubernetesMetaListInterface struct {
	Items []map[string]interface{} `yaml:"items,omitempty" json:"items,omitempty"`
}

// KubernetesMetadata represents the metadata section of a Kubernetes resource.
// It contains essential identification information for the resource.
type KubernetesMetadata struct {
	// Namespace specifies the namespace where the Kubernetes resource is located.
	// For cluster-scoped resources, this field may be empty.
	Namespace string `yaml:"namespace" json:"namespace"`
}

// KubernetesMetaResource represents the minimal metadata structure of a Kubernetes resource
// required for redaction operations. It contains only the fields necessary to identify
// and process resources during sanitization.
type KubernetesMetaResource struct {
	// APIVersion specifies the API version of the Kubernetes resource.
	APIVersion string `yaml:"apiVersion" json:"apiVersion"`

	// Kind specifies the type of Kubernetes resource.
	Kind string `yaml:"kind" json:"kind"`

	// Metadata contains the resource's metadata information, primarily the namespace.
	Metadata KubernetesMetadata `yaml:"metadata,omitempty" json:"metadata,omitempty"`

	// Items is used when the resource represents a List type.
	// It contains the individual resources within the list that need to be processed.
	Items []KubernetesMetaResource `yaml:"items,omitempty" json:"items,omitempty"`
}
