package main

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

const McoMustGatherSanitizerConfigEncodedEnvVar = "MCO_MUST_GATHER_SANITIZER_CFG"

//go:embed data/default-config.yaml
var defaultConfigRaw []byte

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

type Config struct {
	Redact []ConfigRedact `yaml:"redact,omitempty"`
}

func NewConfigFromEnv() (*Config, error) {
	content := os.Getenv(McoMustGatherSanitizerConfigEncodedEnvVar)
	if content == "" {
		return nil, nil
	}

	var rawConfig []byte
	var err error
	_, err = os.Stat(content)
	if err != nil {
		rawConfig, err = base64.StdEncoding.DecodeString(content)
	} else {
		rawConfig, err = os.ReadFile(content)
	}
	if rawConfig == nil || err != nil {
		return nil, fmt.Errorf("the given config env var %s is not neither a valid path nor bas64 encoded config", McoMustGatherSanitizerConfigEncodedEnvVar)
	}

	var config Config
	if err := yaml.Unmarshal(rawConfig, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

func BuildDefaultConfig() (*Config, error) {

	var config Config
	if err := yaml.Unmarshal(defaultConfigRaw, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

func GetConfig() (*Config, error) {
	config, err := NewConfigFromEnv()
	if err != nil {
		return nil, err
	}
	if config == nil {
		config, err = BuildDefaultConfig()
	}
	if err != nil {
		return nil, err
	}
	return config, nil
}
