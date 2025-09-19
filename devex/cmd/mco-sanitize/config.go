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

type ConfigRedact struct {
	ApiVersion string   `yaml:"apiVersion"`
	Kind       string   `yaml:"kind"`
	Namespaces []string `yaml:"namespaces"`

	// The list of paths (dot separated words) to redact in the output, only used with
	// the type Kubernetes redactor.
	Paths []string `yaml:"paths,omitempty"`
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
