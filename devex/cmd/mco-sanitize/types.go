package main

type contentMarshaler func(interface{}) ([]byte, error)
type contentUnmarshaller func([]byte, interface{}) error

type KubernetesMetaListInterface struct {
	Items []map[string]interface{} `yaml:"items,omitempty" json:"items,omitempty"`
}

type KubernetesMetadata struct {
	Namespace string `yaml:"namespace" json:"namespace"`
}

type KubernetesMetaResource struct {
	ApiVersion string                   `yaml:"apiVersion" json:"apiVersion"`
	Kind       string                   `yaml:"kind" json:"kind"`
	Metadata   KubernetesMetadata       `yaml:"metadata,omitempty" json:"metadata,omitempty"`
	Items      []KubernetesMetaResource `yaml:"items,omitempty" json:"items,omitempty"`
}
