// Assisted-by: Claude
package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestNewKubernetesRedactor_ValidConfig(t *testing.T) {
	configs := []ConfigRedact{
		{
			APIVersion: "machineconfiguration.openshift.io/v1",
			Kind:       "MachineConfig",
			Namespaces: []string{"default", "openshift-machine-config-operator"},
			Paths:      []string{"spec.config.storage.files.contents", "spec.config.systemd.units.contents"},
		},
		{
			APIVersion: "machineconfiguration.openshift.io/v1",
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
			APIVersion: "v1",
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
			APIVersion: "machineconfiguration.openshift.io/v1",
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
			APIVersion: "v1",
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
			APIVersion: "v1",
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
			APIVersion: "v2",
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
			APIVersion: "v1",
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
			APIVersion: "v1",
			Kind:       "ConfigMap",
			Metadata:   KubernetesMetadata{Namespace: "default"},
		},
		{
			APIVersion: "v1",
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
		{APIVersion: "v1", Kind: "ConfigMap"},
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
		{APIVersion: "v1", Kind: "ConfigMap"},
		{APIVersion: "v1", Kind: "Secret"},
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
		{APIVersion: "v1", Kind: "ConfigMap"},
	}

	changed, err := redactor.Redact(inputData, k8sResources, yaml.Marshal)

	assert.NoError(t, err)
	assert.False(t, changed)
}

func TestKubernetesRedactor_Redact_ArrayTraversal(t *testing.T) {
	configs := []ConfigRedact{
		{
			Kind:  "MachineConfig",
			Paths: []string{"spec.config.storage.files.*.contents"}, // Path that goes through an array
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
			APIVersion: "machineconfiguration.openshift.io/v1",
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
			APIVersion: "v1",
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

func TestKubernetesRedactor_Redact_UnsupportedArrayIndexInPath(t *testing.T) {
	configs := []ConfigRedact{
		{
			Kind:  "ConfigMap",
			Paths: []string{"data.my-array.1.element"}, // Path with array index in middle
		},
	}
	redactor, err := NewKubernetesRedactor(configs)
	require.NoError(t, err)

	inputData := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"data": map[string]interface{}{
			"my-array": []interface{}{
				map[string]interface{}{
					"element": "first-value-should-not-be-touched",
				},
				map[string]interface{}{
					"element": "second-value-should-be-redacted",
				},
				map[string]interface{}{
					"element": "third-value-should-not-be-touched",
				},
			},
		},
	}

	k8sResources := []KubernetesMetaResource{
		{
			APIVersion: "v1",
			Kind:       "ConfigMap",
			Metadata:   KubernetesMetadata{Namespace: "default"},
		},
	}

	changed, err := redactor.Redact(inputData, k8sResources, yaml.Marshal)

	require.NoError(t, err)
	require.True(t, changed)

	// Verify that ONLY the element at index 1 was redacted
	myArray := inputData["data"].(map[string]interface{})["my-array"].([]interface{})

	// Element 0 should be unchanged
	element0 := myArray[0].(map[string]interface{})
	assert.Equal(t, "first-value-should-not-be-touched", element0["element"],
		"Element at index 0 should not be redacted")

	// Element 1 should be redacted
	element1 := myArray[1].(map[string]interface{})
	redactedInfo, ok := element1["element"].(map[string]interface{})
	require.True(t, ok, "Element at index 1 should be redacted")
	assert.Equal(t, "This field has been redacted", redactedInfo["_REDACTED"])
	assert.Equal(t, len("second-value-should-be-redacted"), redactedInfo["length"])

	// Element 2 should be unchanged
	element2 := myArray[2].(map[string]interface{})
	assert.Equal(t, "third-value-should-not-be-touched", element2["element"],
		"Element at index 2 should not be redacted")
}

func TestKubernetesRedactor_Redact_ArrayIndexOutOfRange(t *testing.T) {
	configs := []ConfigRedact{
		{
			Kind:  "ConfigMap",
			Paths: []string{"data.my-array.5.element"}, // Index 5 is out of range (array has only 3 elements)
		},
	}
	redactor, err := NewKubernetesRedactor(configs)
	require.NoError(t, err)

	inputData := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"data": map[string]interface{}{
			"my-array": []interface{}{
				map[string]interface{}{
					"element": "value-0",
				},
				map[string]interface{}{
					"element": "value-1",
				},
				map[string]interface{}{
					"element": "value-2",
				},
			},
		},
	}

	k8sResources := []KubernetesMetaResource{
		{
			APIVersion: "v1",
			Kind:       "ConfigMap",
			Metadata:   KubernetesMetadata{Namespace: "default"},
		},
	}

	changed, err := redactor.Redact(inputData, k8sResources, yaml.Marshal)

	// Should not error and should return false (no changes made)
	require.NoError(t, err)
	assert.False(t, changed, "Should return false when no redaction occurs due to out-of-range index")

	// Verify all elements remain unchanged
	myArray := inputData["data"].(map[string]interface{})["my-array"].([]interface{})

	for i, expectedValue := range []string{"value-0", "value-1", "value-2"} {
		element := myArray[i].(map[string]interface{})
		assert.Equal(t, expectedValue, element["element"],
			"Element at index %d should remain unchanged when target index is out of range", i)
	}
}

func TestKubernetesRedactor_Redact_InvalidArrayPathElement(t *testing.T) {
	configs := []ConfigRedact{
		{
			Kind:  "ConfigMap",
			Paths: []string{"data.my-array.invalid-key.element"}, // "invalid-key" is not "*" or a number
		},
	}
	redactor, err := NewKubernetesRedactor(configs)
	require.NoError(t, err)

	inputData := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"data": map[string]interface{}{
			"my-array": []interface{}{
				map[string]interface{}{
					"element": "value-0",
				},
				map[string]interface{}{
					"element": "value-1",
				},
			},
		},
	}

	k8sResources := []KubernetesMetaResource{
		{
			APIVersion: "v1",
			Kind:       "ConfigMap",
			Metadata:   KubernetesMetadata{Namespace: "default"},
		},
	}

	changed, err := redactor.Redact(inputData, k8sResources, yaml.Marshal)

	// Should return an error because "invalid-key" is not a valid array path element
	require.Error(t, err)
	assert.False(t, changed, "Should return false when an error occurs")
	assert.Contains(t, err.Error(), "redact path uses array indexing at a path level that is not an array",
		"Error should indicate invalid array path element")
}

func TestKubernetesRedactor_Redact_NonExistentIntermediateKeys(t *testing.T) {
	configs := []ConfigRedact{
		{
			Kind: "ConfigMap",
			Paths: []string{
				"data.existing.password",          // Valid path - should be redacted
				"data.nonexistent.secret",         // Invalid path - intermediate key doesn't exist
				"data.existing.nonexistent.value", // Invalid path - deeper intermediate key doesn't exist
			},
		},
	}
	redactor, err := NewKubernetesRedactor(configs)
	require.NoError(t, err)

	inputData := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"data": map[string]interface{}{
			"existing": map[string]interface{}{
				"password": "secret-password",
				"username": "admin",
			},
			"other": map[string]interface{}{
				"config": "some-config",
			},
		},
	}

	k8sResources := []KubernetesMetaResource{
		{
			APIVersion: "v1",
			Kind:       "ConfigMap",
			Metadata:   KubernetesMetadata{Namespace: "default"},
		},
	}

	changed, err := redactor.Redact(inputData, k8sResources, yaml.Marshal)

	// Should not error and should return true (only the valid path was redacted)
	require.NoError(t, err)
	assert.True(t, changed, "Should return true when at least one path is successfully redacted")

	// Verify that only the existing path was redacted
	existing := inputData["data"].(map[string]interface{})["existing"].(map[string]interface{})

	// The password should be redacted
	redactedPassword, ok := existing["password"].(map[string]interface{})
	require.True(t, ok, "Password should be redacted")
	assert.Equal(t, "This field has been redacted", redactedPassword["_REDACTED"])
	assert.Equal(t, len("secret-password"), redactedPassword["length"])

	// The username should remain unchanged
	assert.Equal(t, "admin", existing["username"], "Username should not be redacted")

	// Other data should remain unchanged
	other := inputData["data"].(map[string]interface{})["other"].(map[string]interface{})
	assert.Equal(t, "some-config", other["config"], "Other data should remain unchanged")

	// Verify the structure hasn't been modified by non-existent paths
	_, hasNonexistent := inputData["data"].(map[string]interface{})["nonexistent"]
	assert.False(t, hasNonexistent, "Non-existent keys should not be created")
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
			APIVersion: "v1",
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

func TestKubernetesRedactor_Redact_LengthMetadata(t *testing.T) {
	testCases := []struct {
		name           string
		inputData      map[string]interface{}
		pathToRedact   string
		expectedLength int
		description    string
	}{
		{
			name: "String primitive",
			inputData: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "Secret",
				"data": map[string]interface{}{
					"password": "secret123",
				},
			},
			pathToRedact:   "data.password",
			expectedLength: 9, // len("secret123")
			description:    "String length should match character count",
		},
		{
			name: "Integer primitive",
			inputData: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"data": map[string]interface{}{
					"port": 8080,
				},
			},
			pathToRedact:   "data.port",
			expectedLength: 4, // len("8080")
			description:    "Integer length should match string representation",
		},
		{
			name: "Boolean primitive",
			inputData: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"data": map[string]interface{}{
					"enabled": true,
				},
			},
			pathToRedact:   "data.enabled",
			expectedLength: 4, // len("true")
			description:    "Boolean length should match string representation",
		},
		{
			name: "Map object",
			inputData: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "Secret",
				"data": map[string]interface{}{
					"config": map[string]interface{}{
						"host": "localhost",
						"port": 5432,
					},
				},
			},
			pathToRedact:   "data.config",
			expectedLength: -1, // Will be calculated based on YAML marshaling
			description:    "Map length should match YAML marshaled representation",
		},
		{
			name: "Array object",
			inputData: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"data": map[string]interface{}{
					"servers": []interface{}{
						"server1.example.com",
						"server2.example.com",
					},
				},
			},
			pathToRedact:   "data.servers",
			expectedLength: -1, // Will be calculated based on YAML marshaling
			description:    "Array length should match YAML marshaled representation",
		},
		{
			name: "Empty string",
			inputData: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "Secret",
				"data": map[string]interface{}{
					"empty": "",
				},
			},
			pathToRedact:   "data.empty",
			expectedLength: 0, // len("")
			description:    "Empty string should have length 0",
		},
		{
			name: "Array of primitives",
			inputData: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"data": map[string]interface{}{
					"keys": []interface{}{
						"key1",
						"key2",
						"key3",
					},
				},
			},
			pathToRedact:   "data.keys",
			expectedLength: -1, // Will be calculated based on YAML marshaling
			description:    "Array of primitives length should match YAML representation",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			configs := []ConfigRedact{
				{
					Kind:  tc.inputData["kind"].(string),
					Paths: []string{tc.pathToRedact},
				},
			}
			redactor, err := NewKubernetesRedactor(configs)
			require.NoError(t, err)

			k8sResources := []KubernetesMetaResource{
				{
					APIVersion: tc.inputData["apiVersion"].(string),
					Kind:       tc.inputData["kind"].(string),
					Metadata:   KubernetesMetadata{},
				},
			}

			// Calculate expected length if not provided (before redaction)
			expectedLength := tc.expectedLength
			if expectedLength == -1 {
				// Get original value and calculate length the same way the redactor does
				pathParts := strings.Split(tc.pathToRedact, ".")
				var originalValue interface{} = tc.inputData
				for _, part := range pathParts {
					originalValue = originalValue.(map[string]interface{})[part]
				}

				var redactSource string
				switch originalValue.(type) {
				case []interface{}, map[string]interface{}:
					rawBytes, err := yaml.Marshal(originalValue)
					require.NoError(t, err)
					redactSource = string(rawBytes)
				case string:
					redactSource = originalValue.(string)
				default:
					redactSource = fmt.Sprintf("%v", originalValue)
				}
				expectedLength = len(redactSource)
			}

			changed, err := redactor.Redact(tc.inputData, k8sResources, yaml.Marshal)

			require.NoError(t, err, "Redaction should not error")
			require.True(t, changed, "Data should be changed after redaction")

			// Navigate to the redacted field
			pathParts := strings.Split(tc.pathToRedact, ".")
			var redactedField interface{} = tc.inputData
			for _, part := range pathParts {
				redactedField = redactedField.(map[string]interface{})[part]
			}

			// Verify redaction structure
			redactInfo, ok := redactedField.(map[string]interface{})
			require.True(t, ok, "Redacted field should be a map")

			redactMsg, ok := redactInfo["_REDACTED"].(string)
			require.True(t, ok, "Should have _REDACTED field")
			assert.Equal(t, "This field has been redacted", redactMsg)

			lengthField, ok := redactInfo["length"]
			require.True(t, ok, "Should have length field")
			actualLength, ok := lengthField.(int)
			require.True(t, ok, "Length should be an integer")

			assert.Equal(t, expectedLength, actualLength,
				"Length field should match expected value: %s", tc.description)
		})
	}
}
