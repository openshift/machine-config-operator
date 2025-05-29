package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestBuildDefaultConfig(t *testing.T) {
	config, err := BuildDefaultConfig()

	require.NoError(t, err)
	require.NotNil(t, config)
	assert.NotEmpty(t, config.Redact, "Default config should have redaction rules")

	// Verify structure of default config
	for _, redact := range config.Redact {
		assert.NotEmpty(t, redact.APIVersion, "APIVersion should not be empty")
		assert.NotEmpty(t, redact.Kind, "Kind should not be empty")
		assert.NotEmpty(t, redact.Paths, "Paths should not be empty")
	}
}

func TestNewConfigFromEnv_EmptyEnv(t *testing.T) {
	// Ensure env var is not set
	originalEnv := os.Getenv(McoMustGatherSanitizerConfigEncodedEnvVar)
	defer os.Setenv(McoMustGatherSanitizerConfigEncodedEnvVar, originalEnv)
	os.Unsetenv(McoMustGatherSanitizerConfigEncodedEnvVar)

	config, err := NewConfigFromEnv()

	assert.NoError(t, err)
	assert.Nil(t, config, "Should return nil when env var is not set")
}

func TestNewConfigFromEnv_Base64EncodedConfig(t *testing.T) {
	// Create test config
	testConfig := Config{
		Redact: []ConfigRedact{
			{
				APIVersion: "v1",
				Kind:       "Secret",
				Paths:      []string{"data"},
			},
		},
	}

	configBytes, err := yaml.Marshal(testConfig)
	require.NoError(t, err)
	encodedConfig := base64.StdEncoding.EncodeToString(configBytes)

	// Set env var
	originalEnv := os.Getenv(McoMustGatherSanitizerConfigEncodedEnvVar)
	defer os.Setenv(McoMustGatherSanitizerConfigEncodedEnvVar, originalEnv)
	os.Setenv(McoMustGatherSanitizerConfigEncodedEnvVar, encodedConfig)

	config, err := NewConfigFromEnv()

	require.NoError(t, err)
	require.NotNil(t, config)
	assert.Len(t, config.Redact, 1)
	assert.Equal(t, "v1", config.Redact[0].APIVersion)
	assert.Equal(t, "Secret", config.Redact[0].Kind)
	assert.Equal(t, []string{"data"}, config.Redact[0].Paths)
}

func TestNewConfigFromEnv_FilePath(t *testing.T) {
	// Create temporary config file
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "test-config.yaml")

	testConfig := Config{
		Redact: []ConfigRedact{
			{
				APIVersion: "v1",
				Kind:       "ConfigMap",
				Namespaces: []string{"kube-system"},
				Paths:      []string{"data.sensitive"},
			},
		},
	}

	configBytes, err := yaml.Marshal(testConfig)
	require.NoError(t, err)
	err = os.WriteFile(configFile, configBytes, 0644)
	require.NoError(t, err)

	// Set env var to file path
	originalEnv := os.Getenv(McoMustGatherSanitizerConfigEncodedEnvVar)
	defer os.Setenv(McoMustGatherSanitizerConfigEncodedEnvVar, originalEnv)
	os.Setenv(McoMustGatherSanitizerConfigEncodedEnvVar, configFile)

	config, err := NewConfigFromEnv()

	require.NoError(t, err)
	require.NotNil(t, config)
	assert.Len(t, config.Redact, 1)
	assert.Equal(t, "v1", config.Redact[0].APIVersion)
	assert.Equal(t, "ConfigMap", config.Redact[0].Kind)
	assert.Equal(t, []string{"kube-system"}, config.Redact[0].Namespaces)
	assert.Equal(t, []string{"data.sensitive"}, config.Redact[0].Paths)
}

func TestNewConfigFromEnv_NonexistentFile(t *testing.T) {
	// Set env var to nonexistent file path
	originalEnv := os.Getenv(McoMustGatherSanitizerConfigEncodedEnvVar)
	defer os.Setenv(McoMustGatherSanitizerConfigEncodedEnvVar, originalEnv)
	os.Setenv(McoMustGatherSanitizerConfigEncodedEnvVar, "/nonexistent/path/config.yaml")

	config, err := NewConfigFromEnv()

	assert.Error(t, err)
	assert.Nil(t, config)
	assert.Contains(t, err.Error(), "is not neither a valid path nor bas64 encoded config")
}

func TestNewConfigFromEnv_InvalidBase64(t *testing.T) {
	// Set env var to invalid base64
	originalEnv := os.Getenv(McoMustGatherSanitizerConfigEncodedEnvVar)
	defer os.Setenv(McoMustGatherSanitizerConfigEncodedEnvVar, originalEnv)
	os.Setenv(McoMustGatherSanitizerConfigEncodedEnvVar, "invalid-base64-content!!!")

	config, err := NewConfigFromEnv()

	assert.Error(t, err)
	assert.Nil(t, config)
	assert.Contains(t, err.Error(), "is not neither a valid path nor bas64 encoded config")
}

func TestNewConfigFromEnv_InvalidYAML(t *testing.T) {
	// Create invalid YAML and encode it
	invalidYAML := "invalid: yaml: content: ["
	encodedConfig := base64.StdEncoding.EncodeToString([]byte(invalidYAML))

	// Set env var
	originalEnv := os.Getenv(McoMustGatherSanitizerConfigEncodedEnvVar)
	defer os.Setenv(McoMustGatherSanitizerConfigEncodedEnvVar, originalEnv)
	os.Setenv(McoMustGatherSanitizerConfigEncodedEnvVar, encodedConfig)

	config, err := NewConfigFromEnv()

	assert.Error(t, err)
	assert.Nil(t, config)
}

func TestGetConfig_EnvVarSet(t *testing.T) {
	// Create test config
	testConfig := Config{
		Redact: []ConfigRedact{
			{
				APIVersion: "v1",
				Kind:       "Secret",
				Paths:      []string{"data.password"},
			},
		},
	}

	configBytes, err := yaml.Marshal(testConfig)
	require.NoError(t, err)
	encodedConfig := base64.StdEncoding.EncodeToString(configBytes)

	// Set env var
	originalEnv := os.Getenv(McoMustGatherSanitizerConfigEncodedEnvVar)
	defer os.Setenv(McoMustGatherSanitizerConfigEncodedEnvVar, originalEnv)
	os.Setenv(McoMustGatherSanitizerConfigEncodedEnvVar, encodedConfig)

	config, err := GetConfig()

	require.NoError(t, err)
	require.NotNil(t, config)
	assert.Len(t, config.Redact, 1)
	assert.Equal(t, "Secret", config.Redact[0].Kind)
}

func TestGetConfig_EnvVarNotSet(t *testing.T) {
	// Ensure env var is not set
	originalEnv := os.Getenv(McoMustGatherSanitizerConfigEncodedEnvVar)
	defer os.Setenv(McoMustGatherSanitizerConfigEncodedEnvVar, originalEnv)
	os.Unsetenv(McoMustGatherSanitizerConfigEncodedEnvVar)

	config, err := GetConfig()

	require.NoError(t, err)
	require.NotNil(t, config)
	// Should return default config
	assert.NotEmpty(t, config.Redact, "Should return default config with redaction rules")
}

func TestGetConfig_EnvVarError(t *testing.T) {
	// Set env var to invalid content
	originalEnv := os.Getenv(McoMustGatherSanitizerConfigEncodedEnvVar)
	defer os.Setenv(McoMustGatherSanitizerConfigEncodedEnvVar, originalEnv)
	os.Setenv(McoMustGatherSanitizerConfigEncodedEnvVar, "invalid-content")

	config, err := GetConfig()

	assert.Error(t, err)
	assert.Nil(t, config)
}

func TestConfigRedact_YAMLStructure(t *testing.T) {
	// Test YAML marshaling/unmarshaling of ConfigRedact
	original := ConfigRedact{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Namespaces: []string{"default", "kube-system"},
		Paths:      []string{"spec.template.spec.containers.0.env", "metadata.annotations"},
	}

	yamlBytes, err := yaml.Marshal(original)
	require.NoError(t, err)

	var unmarshaled ConfigRedact
	err = yaml.Unmarshal(yamlBytes, &unmarshaled)
	require.NoError(t, err)

	assert.Equal(t, original.APIVersion, unmarshaled.APIVersion)
	assert.Equal(t, original.Kind, unmarshaled.Kind)
	assert.Equal(t, original.Namespaces, unmarshaled.Namespaces)
	assert.Equal(t, original.Paths, unmarshaled.Paths)
}

func TestConfig_YAMLStructure(t *testing.T) {
	// Test YAML marshaling/unmarshaling of Config
	original := Config{
		Redact: []ConfigRedact{
			{
				APIVersion: "v1",
				Kind:       "Secret",
				Paths:      []string{"data"},
			},
			{
				APIVersion: "v1",
				Kind:       "ConfigMap",
				Namespaces: []string{"default"},
				Paths:      []string{"data.sensitive"},
			},
		},
	}

	yamlBytes, err := yaml.Marshal(original)
	require.NoError(t, err)

	var unmarshaled Config
	err = yaml.Unmarshal(yamlBytes, &unmarshaled)
	require.NoError(t, err)

	assert.Len(t, unmarshaled.Redact, 2)
	assert.Equal(t, original.Redact[0].Kind, unmarshaled.Redact[0].Kind)
	assert.Equal(t, original.Redact[1].Namespaces, unmarshaled.Redact[1].Namespaces)
}

func TestConfigRedact_OptionalFields(t *testing.T) {
	// Test that optional fields are properly handled
	configYAML := `
redact:
  - apiVersion: v1
    kind: Secret
    paths:
      - data
  - apiVersion: v1
    kind: ConfigMap
    namespaces:
      - default
    paths:
      - data.sensitive
`

	var config Config
	err := yaml.Unmarshal([]byte(configYAML), &config)
	require.NoError(t, err)

	assert.Len(t, config.Redact, 2)

	// First config has no namespaces (should be nil/empty)
	assert.Empty(t, config.Redact[0].Namespaces)
	assert.Equal(t, []string{"data"}, config.Redact[0].Paths)

	// Second config has namespaces
	assert.Equal(t, []string{"default"}, config.Redact[1].Namespaces)
	assert.Equal(t, []string{"data.sensitive"}, config.Redact[1].Paths)
}
