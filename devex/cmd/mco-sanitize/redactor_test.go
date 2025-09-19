package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestNewKubernetesRedactor_ValidConfig(t *testing.T) {
	configs := []ConfigRedact{
		{
			ApiVersion: "machineconfiguration.openshift.io/v1",
			Kind:       "MachineConfig",
			Namespaces: []string{"default", "openshift-machine-config-operator"},
			Paths:      []string{"spec.config.storage.files.contents", "spec.config.systemd.units.contents"},
		},
		{
			ApiVersion: "machineconfiguration.openshift.io/v1",
			Kind:       "ControllerConfig",
			Paths:      []string{"spec.internalRegistryPullSecret", "spec.kubeAPIServerServingCAData"},
		},
	}

	redactor, err := NewKubernetesRedactor(configs)

	assert.NoError(t, err)
	assert.NotNil(t, redactor)
}

func TestNewKubernetesRedactor_NilConfig(t *testing.T) {
	redactor, err := NewKubernetesRedactor(nil)

	assert.Error(t, err)
	assert.Nil(t, redactor)
	assert.EqualError(t, err, "invalid redact configuration")
}

func TestNewKubernetesRedactor_EmptyKind(t *testing.T) {
	configs := []ConfigRedact{
		{
			ApiVersion: "v1",
			Kind:       "", // Empty kind should cause error
			Paths:      []string{"data.secret"},
		},
	}

	redactor, err := NewKubernetesRedactor(configs)

	assert.Error(t, err)
	assert.Nil(t, redactor)
	assert.EqualError(t, err, "invalid redact configuration. Empty kind")
}

func TestNewKubernetesRedactor_EmptySlice(t *testing.T) {
	configs := []ConfigRedact{}

	redactor, err := NewKubernetesRedactor(configs)

	assert.NoError(t, err)
	assert.NotNil(t, redactor)
}

func TestKubernetesRedactor_Redact_EmptyResources(t *testing.T) {
	configs := []ConfigRedact{
		{Kind: "MachineConfig", Paths: []string{"spec.config"}},
	}
	redactor, err := NewKubernetesRedactor(configs)
	require.NoError(t, err)

	inputData := map[string]interface{}{
		"apiVersion": "machineconfiguration.openshift.io/v1",
		"kind":       "MachineConfig",
		"spec":       map[string]interface{}{"config": "sensitive"},
	}

	changed, err := redactor.Redact(inputData, []KubernetesMetaResource{}, yaml.Marshal)

	assert.NoError(t, err)
	assert.False(t, changed)
}

func TestKubernetesRedactor_Redact_SingleResource_Match(t *testing.T) {
	configs := []ConfigRedact{
		{
			Kind:  "MachineConfig",
			Paths: []string{"spec.config"},
		},
	}
	redactor, err := NewKubernetesRedactor(configs)
	require.NoError(t, err)

	inputData := map[string]interface{}{
		"apiVersion": "machineconfiguration.openshift.io/v1",
		"kind":       "MachineConfig",
		"metadata": map[string]interface{}{
			"name":      "test-config",
			"namespace": "default",
		},
		"spec": map[string]interface{}{
			"config": "sensitive configuration",
		},
	}

	k8sResources := []KubernetesMetaResource{
		{
			ApiVersion: "machineconfiguration.openshift.io/v1",
			Kind:       "MachineConfig",
			Metadata:   KubernetesMetadata{Namespace: "default"},
		},
	}

	changed, err := redactor.Redact(inputData, k8sResources, yaml.Marshal)

	assert.NoError(t, err)
	assert.True(t, changed)

	// Verify the content was actually redacted
	spec := inputData["spec"].(map[string]interface{})
	redactedInfo, ok := spec["config"].(map[string]interface{})["_REDACTED"]
	assert.True(t, ok)
	assert.Equal(t, "This field has been redacted", redactedInfo)
}

func TestKubernetesRedactor_Redact_SingleResource_NoMatch(t *testing.T) {
	configs := []ConfigRedact{
		{
			Kind:  "Secret", // Different kind
			Paths: []string{"data.password"},
		},
	}
	redactor, err := NewKubernetesRedactor(configs)
	require.NoError(t, err)

	inputData := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap", // Different kind, shouldn't match
		"data": map[string]interface{}{
			"config": "not sensitive",
		},
	}

	k8sResources := []KubernetesMetaResource{
		{
			ApiVersion: "v1",
			Kind:       "ConfigMap",
			Metadata:   KubernetesMetadata{Namespace: "default"},
		},
	}

	changed, err := redactor.Redact(inputData, k8sResources, yaml.Marshal)

	assert.NoError(t, err)
	assert.False(t, changed)
}

func TestKubernetesRedactor_Redact_ApiVersionMismatch(t *testing.T) {
	configs := []ConfigRedact{
		{
			ApiVersion: "v1",
			Kind:       "ConfigMap",
			Paths:      []string{"data.secret"},
		},
	}
	redactor, err := NewKubernetesRedactor(configs)
	require.NoError(t, err)

	inputData := map[string]interface{}{
		"apiVersion": "v2", // Different API version
		"kind":       "ConfigMap",
		"data": map[string]interface{}{
			"secret": "sensitive",
		},
	}

	k8sResources := []KubernetesMetaResource{
		{
			ApiVersion: "v2",
			Kind:       "ConfigMap",
			Metadata:   KubernetesMetadata{Namespace: "default"},
		},
	}

	changed, err := redactor.Redact(inputData, k8sResources, yaml.Marshal)

	assert.NoError(t, err)
	assert.False(t, changed)
}

func TestKubernetesRedactor_Redact_NamespaceFilter(t *testing.T) {
	configs := []ConfigRedact{
		{
			Kind:       "Secret",
			Namespaces: []string{"openshift-machine-config-operator"}, // Only specific namespace
			Paths:      []string{"data.token"},
		},
	}
	redactor, err := NewKubernetesRedactor(configs)
	require.NoError(t, err)

	inputData := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Secret",
		"data": map[string]interface{}{
			"token": "sensitive-token",
		},
	}

	// Test with non-matching namespace
	k8sResources := []KubernetesMetaResource{
		{
			ApiVersion: "v1",
			Kind:       "Secret",
			Metadata:   KubernetesMetadata{Namespace: "default"}, // Different namespace
		},
	}

	changed, err := redactor.Redact(inputData, k8sResources, yaml.Marshal)
	assert.NoError(t, err)
	assert.False(t, changed, "Should not redact resource in non-matching namespace")

	// Test with matching namespace
	k8sResources[0].Metadata.Namespace = "openshift-machine-config-operator"
	changed, err = redactor.Redact(inputData, k8sResources, yaml.Marshal)
	assert.NoError(t, err)
	assert.True(t, changed, "Should redact resource in matching namespace")
}

func TestKubernetesRedactor_Redact_ListResources(t *testing.T) {
	configs := []ConfigRedact{
		{
			Kind:  "ConfigMap",
			Paths: []string{"data"},
		},
	}
	redactor, err := NewKubernetesRedactor(configs)
	require.NoError(t, err)

	inputData := []map[string]interface{}{
		{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"data": map[string]interface{}{
				"config": "sensitive config 1",
			},
		},
		{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"data": map[string]interface{}{
				"config": "sensitive config 2",
			},
		},
	}

	k8sResources := []KubernetesMetaResource{
		{
			ApiVersion: "v1",
			Kind:       "ConfigMap",
			Metadata:   KubernetesMetadata{Namespace: "default"},
		},
		{
			ApiVersion: "v1",
			Kind:       "ConfigMap",
			Metadata:   KubernetesMetadata{Namespace: "default"},
		},
	}

	changed, err := redactor.Redact(inputData, k8sResources, json.Marshal)

	assert.NoError(t, err)
	assert.True(t, changed)

	// Verify both items were redacted
	for i, item := range inputData {
		redactedInfo, ok := item["data"].(map[string]interface{})["_REDACTED"]
		assert.True(t, ok, "Item %d should be redacted", i)
		assert.Equal(t, "This field has been redacted", redactedInfo)
	}
}

func TestKubernetesRedactor_Redact_ListResources_LengthMismatch(t *testing.T) {
	configs := []ConfigRedact{
		{Kind: "ConfigMap", Paths: []string{"data.config"}},
	}
	redactor, err := NewKubernetesRedactor(configs)
	require.NoError(t, err)

	inputData := []map[string]interface{}{
		{"apiVersion": "v1", "kind": "ConfigMap"},
		{"apiVersion": "v1", "kind": "ConfigMap"},
	}

	// Mismatched lengths
	k8sResources := []KubernetesMetaResource{
		{ApiVersion: "v1", Kind: "ConfigMap"},
	}

	changed, err := redactor.Redact(inputData, k8sResources, json.Marshal)

	assert.Error(t, err)
	assert.False(t, changed)
	assert.Contains(t, err.Error(), "mismatch between input data length")
}

func TestKubernetesRedactor_Redact_ListResources_PartialMatch(t *testing.T) {
	configs := []ConfigRedact{
		{
			Kind:  "ConfigMap", // Only ConfigMaps will be redacted
			Paths: []string{"data"},
		},
	}
	redactor, err := NewKubernetesRedactor(configs)
	require.NoError(t, err)

	inputData := []map[string]interface{}{
		{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"data": map[string]interface{}{
				"secret": "redact this",
			},
		},
		{
			"apiVersion": "v1",
			"kind":       "Secret", // Different kind, won't be redacted
			"data": map[string]interface{}{
				"secret": "don't redact this",
			},
		},
	}

	k8sResources := []KubernetesMetaResource{
		{ApiVersion: "v1", Kind: "ConfigMap"},
		{ApiVersion: "v1", Kind: "Secret"},
	}

	changed, err := redactor.Redact(inputData, k8sResources, yaml.Marshal)

	assert.NoError(t, err)
	assert.True(t, changed, "Should return true since at least one item was redacted")

	// Verify only the first item was redacted
	_, configMapRedacted := inputData[0]["data"].(map[string]interface{})["_REDACTED"]
	assert.True(t, configMapRedacted, "ConfigMap should be redacted")

	secretData, secretExists := inputData[1]["data"].(map[string]interface{})["secret"]
	assert.True(t, secretExists, "Secret data should still exist")
	assert.Equal(t, "don't redact this", secretData, "Secret should not be redacted")
}

func TestKubernetesRedactor_Redact_UnsupportedInputType(t *testing.T) {
	configs := []ConfigRedact{
		{Kind: "ConfigMap", Paths: []string{"data.config"}},
	}
	redactor, err := NewKubernetesRedactor(configs)
	require.NoError(t, err)

	// Unsupported input type (string instead of map or slice)
	inputData := "this is not a supported input type"

	k8sResources := []KubernetesMetaResource{
		{ApiVersion: "v1", Kind: "ConfigMap"},
	}

	changed, err := redactor.Redact(inputData, k8sResources, yaml.Marshal)

	assert.NoError(t, err)
	assert.False(t, changed)
}

func TestKubernetesRedactor_Redact_ArrayTraversal(t *testing.T) {
	configs := []ConfigRedact{
		{
			Kind:  "MachineConfig",
			Paths: []string{"spec.config.storage.files.contents"}, // Path that goes through an array
		},
	}
	redactor, err := NewKubernetesRedactor(configs)
	require.NoError(t, err)

	inputData := map[string]interface{}{
		"apiVersion": "machineconfiguration.openshift.io/v1",
		"kind":       "MachineConfig",
		"metadata": map[string]interface{}{
			"name":      "test-config",
			"namespace": "default",
		},
		"spec": map[string]interface{}{
			"config": map[string]interface{}{
				"storage": map[string]interface{}{
					"files": []interface{}{ // Array of files
						map[string]interface{}{
							"path":     "/etc/test1.conf",
							"contents": "sensitive file content 1",
						},
						map[string]interface{}{
							"path":     "/etc/test2.conf",
							"contents": "sensitive file content 2",
						},
						map[string]interface{}{
							"path":     "/etc/test3.conf",
							"contents": "sensitive file content 3",
						},
					},
				},
			},
		},
	}

	k8sResources := []KubernetesMetaResource{
		{
			ApiVersion: "machineconfiguration.openshift.io/v1",
			Kind:       "MachineConfig",
			Metadata:   KubernetesMetadata{Namespace: "default"},
		},
	}

	changed, err := redactor.Redact(inputData, k8sResources, yaml.Marshal)

	assert.NoError(t, err)
	assert.True(t, changed)

	// Verify that all files in the array had their contents redacted
	files := inputData["spec"].(map[string]interface{})["config"].(map[string]interface{})["storage"].(map[string]interface{})["files"].([]interface{})

	for i, file := range files {
		fileMap := file.(map[string]interface{})

		// Check that contents was redacted
		redactedInfo, ok := fileMap["contents"].(map[string]interface{})["_REDACTED"]
		assert.True(t, ok, "File %d contents should be redacted", i)
		assert.Equal(t, "This field has been redacted", redactedInfo)

		// Check that path field is unchanged
		assert.Contains(t, fileMap["path"], "/etc/test", "Path should remain unchanged for file %d", i)
	}
}

func TestKubernetesRedactor_Redact_ArrayIndexPaths(t *testing.T) {
	configs := []ConfigRedact{
		{
			Kind:  "ConfigMap",
			Paths: []string{"data.items.0", "data.items.2"}, // Target specific array elements by index
		},
	}
	redactor, err := NewKubernetesRedactor(configs)
	require.NoError(t, err)

	inputData := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]interface{}{
			"name":      "test-config",
			"namespace": "default",
		},
		"data": map[string]interface{}{
			"items": []interface{}{
				"first-item-sensitive", // Index 0 - should be redacted
				"second-item-safe",     // Index 1 - should not be redacted
				"third-item-sensitive", // Index 2 - should be redacted
				"fourth-item-safe",     // Index 3 - should not be redacted
			},
		},
	}

	k8sResources := []KubernetesMetaResource{
		{
			ApiVersion: "v1",
			Kind:       "ConfigMap",
			Metadata:   KubernetesMetadata{Namespace: "default"},
		},
	}

	changed, err := redactor.Redact(inputData, k8sResources, yaml.Marshal)

	assert.NoError(t, err)
	assert.True(t, changed)

	// Check that specific array elements were redacted
	items := inputData["data"].(map[string]interface{})["items"].([]interface{})

	// Element 0 should be redacted
	redactedInfo0, ok0 := items[0].(map[string]interface{})["_REDACTED"]
	assert.True(t, ok0, "Element 0 should be redacted")
	assert.Equal(t, "This field has been redacted", redactedInfo0)

	// Element 1 should remain unchanged
	assert.Equal(t, "second-item-safe", items[1], "Element 1 should not be redacted")

	// Element 2 should be redacted
	redactedInfo2, ok2 := items[2].(map[string]interface{})["_REDACTED"]
	assert.True(t, ok2, "Element 2 should be redacted")
	assert.Equal(t, "This field has been redacted", redactedInfo2)

	// Element 3 should remain unchanged
	assert.Equal(t, "fourth-item-safe", items[3], "Element 3 should not be redacted")
}

func TestKubernetesRedactor_Redact_RootPath(t *testing.T) {
	configs := []ConfigRedact{
		{
			Kind:  "ConfigMap",
			Paths: []string{"data"}, // Root-level path
		},
	}
	redactor, err := NewKubernetesRedactor(configs)
	require.NoError(t, err)

	inputData := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]interface{}{
			"name":      "test-config",
			"namespace": "default",
		},
		"data": map[string]interface{}{
			"config":   "sensitive configuration",
			"password": "secret-password",
			"token":    "access-token",
		},
	}

	k8sResources := []KubernetesMetaResource{
		{
			ApiVersion: "v1",
			Kind:       "ConfigMap",
			Metadata:   KubernetesMetadata{Namespace: "default"},
		},
	}

	changed, err := redactor.Redact(inputData, k8sResources, yaml.Marshal)

	assert.NoError(t, err)
	assert.True(t, changed)

	// Verify the entire "data" field was redacted
	redactedInfo, ok := inputData["data"].(map[string]interface{})["_REDACTED"]
	assert.True(t, ok, "Data field should be redacted")
	assert.Equal(t, "This field has been redacted", redactedInfo)

	// Verify that metadata was not affected
	metadata := inputData["metadata"].(map[string]interface{})
	assert.Equal(t, "test-config", metadata["name"])
	assert.Equal(t, "default", metadata["namespace"])
}
