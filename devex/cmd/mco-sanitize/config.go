package main

// ConfigRedact defines the configuration for redacting specific fields in Kubernetes resources.
// It specifies which resources to target and which fields within those resources should be sanitized.
type ConfigRedact struct {
	// APIVersion specifies the API version of the Kubernetes resource to target.
	// If empty, all API versions of the specified Kind will be redacted.
	APIVersion string `yaml:"apiVersion"`

	// Kind specifies the Kubernetes resource type to target (e.g., "Pod", "Secret", "ConfigMap").
	// This field is required and cannot be empty.
	Kind string `yaml:"kind"`

	// Namespaces specifies a list of namespaces to limit redaction to.
	// If empty or nil, resources from all namespaces will be considered for redaction.
	Namespaces []string `yaml:"namespaces"`

	// Paths contains a list of dot-separated field paths to redact within the targeted resources.
	// For example: "spec.containers.0.env.0.value" or "data.password".
	// Array elements can be referenced by index or all elements will be processed if * is given.
	Paths []string `yaml:"paths"`
}
