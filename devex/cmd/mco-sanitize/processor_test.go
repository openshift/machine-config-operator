package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockRedactor for testing
type mockRedactor struct {
	shouldRedact bool
	shouldError  bool
	errorMsg     string
}

func (m *mockRedactor) Redact(_ interface{}, _ []KubernetesMetaResource, _ contentMarshaler) (bool, error) {
	if m.shouldError {
		return false, &mockError{m.errorMsg}
	}
	return m.shouldRedact, nil
}

type mockError struct {
	msg string
}

func (e *mockError) Error() string {
	return e.msg
}

func TestFileProcessor_Process_ValidYAML(t *testing.T) {
	// Create temp directory
	tempDir := t.TempDir()

	// Create test YAML file
	testFile := filepath.Join(tempDir, "test.yaml")
	yamlContent := `apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  name: test-config
  namespace: default
spec:
  config:
    storage:
      files:
        - contents: "sensitive data"
`
	require.NoError(t, os.WriteFile(testFile, []byte(yamlContent), 0644))

	// Test with redactor that returns true (content changed)
	redactor := &mockRedactor{shouldRedact: true}
	processor := NewFileProcessor(redactor)

	changed, err := processor.Process(context.Background(), testFile)
	assert.NoError(t, err)
	assert.True(t, changed, "Process() should return true when redactor makes changes")

	// Verify file still exists and is readable
	assert.FileExists(t, testFile)
}

func TestFileProcessor_Process_ValidJSON(t *testing.T) {
	tempDir := t.TempDir()

	testFile := filepath.Join(tempDir, "test.json")
	jsonContent := `{
  "apiVersion": "machineconfiguration.openshift.io/v1",
  "kind": "MachineConfig",
  "metadata": {
    "name": "test-config",
    "namespace": "default"
  },
  "spec": {
    "config": {
      "storage": {
        "files": [
          {"contents": "sensitive data"}
        ]
      }
    }
  }
}`
	require.NoError(t, os.WriteFile(testFile, []byte(jsonContent), 0644))

	redactor := &mockRedactor{shouldRedact: true}
	processor := NewFileProcessor(redactor)

	changed, err := processor.Process(context.Background(), testFile)
	assert.NoError(t, err)
	assert.True(t, changed, "Process() should return true when redactor makes changes")
}

func TestFileProcessor_Process_NoChanges(t *testing.T) {
	tempDir := t.TempDir()

	testFile := filepath.Join(tempDir, "test.yaml")
	yamlContent := `apiVersion: v1
kind: ConfigMap
metadata:
  name: test
data:
  key: value
`
	require.NoError(t, os.WriteFile(testFile, []byte(yamlContent), 0644))

	// Redactor returns false (no changes)
	redactor := &mockRedactor{shouldRedact: false}
	processor := NewFileProcessor(redactor)

	changed, err := processor.Process(context.Background(), testFile)
	assert.NoError(t, err)
	assert.False(t, changed, "Process() should return false when redactor makes no changes")
}

func TestFileProcessor_Process_RedactorError(t *testing.T) {
	tempDir := t.TempDir()

	testFile := filepath.Join(tempDir, "test.yaml")
	yamlContent := `apiVersion: v1
kind: ConfigMap
metadata:
  name: test
`
	require.NoError(t, os.WriteFile(testFile, []byte(yamlContent), 0644))

	redactor := &mockRedactor{shouldError: true, errorMsg: "redaction failed"}
	processor := NewFileProcessor(redactor)

	_, err := processor.Process(context.Background(), testFile)
	assert.Error(t, err)
	assert.EqualError(t, err, "redaction failed")
}

func TestFileProcessor_Process_InvalidFile(t *testing.T) {
	processor := NewFileProcessor(&mockRedactor{})

	// Test non-existent file
	changed, err := processor.Process(context.Background(), "/non/existent/file.yaml")
	assert.NoError(t, err, "Process() should handle non-existent files gracefully")
	assert.False(t, changed, "Process() should return false for non-existent files")
}

func TestFileProcessor_Process_UnsupportedExtension(t *testing.T) {
	tempDir := t.TempDir()

	testFile := filepath.Join(tempDir, "test.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("some text"), 0644))

	processor := NewFileProcessor(&mockRedactor{shouldRedact: true})

	changed, err := processor.Process(context.Background(), testFile)
	assert.NoError(t, err, "Process() should handle unsupported files gracefully")
	assert.False(t, changed, "Process() should return false for unsupported file types")
}

func TestFileProcessor_Process_CorruptYAML(t *testing.T) {
	tempDir := t.TempDir()

	testFile := filepath.Join(tempDir, "test.yaml")
	corruptYAML := `apiVersion: v1
kind: ConfigMap
metadata:
  name: test
  invalid: [unclosed bracket
`
	require.NoError(t, os.WriteFile(testFile, []byte(corruptYAML), 0644))

	processor := NewFileProcessor(&mockRedactor{shouldRedact: true})

	changed, err := processor.Process(context.Background(), testFile)
	assert.NoError(t, err, "Process() should handle corrupt files gracefully")
	assert.False(t, changed, "Process() should return false for corrupt files")
}

func TestFileProcessor_Process_ListResource(t *testing.T) {
	tempDir := t.TempDir()

	testFile := filepath.Join(tempDir, "list.yaml")
	// Create a List resource with multiple items
	yamlContent := `apiVersion: v1
kind: ConfigMapList
items:
- apiVersion: v1
  kind: ConfigMap
  metadata:
    name: test-config-1
    namespace: default
  data:
    key1: value1
- apiVersion: v1
  kind: ConfigMap
  metadata:
    name: test-config-2
    namespace: default
  data:
    key2: value2
`
	require.NoError(t, os.WriteFile(testFile, []byte(yamlContent), 0644))

	// Test with redactor that returns true (content changed)
	redactor := &mockRedactor{shouldRedact: true}
	processor := NewFileProcessor(redactor)

	changed, err := processor.Process(context.Background(), testFile)
	assert.NoError(t, err)
	assert.True(t, changed, "Process() should return true when redactor processes List resources")

	// Verify file still exists
	assert.FileExists(t, testFile)
}

func TestFileProcessor_Process_ListResourceNoChanges(t *testing.T) {
	tempDir := t.TempDir()

	testFile := filepath.Join(tempDir, "list.json")
	// Create a JSON List resource
	jsonContent := `{
  "apiVersion": "v1",
  "kind": "SecretList",
  "items": [
    {
      "apiVersion": "v1",
      "kind": "Secret",
      "metadata": {
        "name": "secret-1",
        "namespace": "default"
      },
      "data": {
        "key": "dmFsdWU="
      }
    },
    {
      "apiVersion": "v1",
      "kind": "Secret",
      "metadata": {
        "name": "secret-2",
        "namespace": "kube-system"
      },
      "data": {
        "token": "dG9rZW4="
      }
    }
  ]
}`
	require.NoError(t, os.WriteFile(testFile, []byte(jsonContent), 0644))

	// Test with redactor that returns false (no changes needed)
	redactor := &mockRedactor{shouldRedact: false}
	processor := NewFileProcessor(redactor)

	changed, err := processor.Process(context.Background(), testFile)
	assert.NoError(t, err)
	assert.False(t, changed, "Process() should return false when redactor makes no changes to List resources")
}

func TestFileProcessor_Process_ListResourceMalformed(t *testing.T) {
	tempDir := t.TempDir()

	testFile := filepath.Join(tempDir, "malformed-list.yaml")
	// Create a List resource that will fail during KubernetesMetaListInterface unmarshaling
	yamlContent := `apiVersion: v1
kind: ConfigMapList
items:
  - this is not a valid kubernetes resource structure
  - neither is this: [invalid, yaml, structure
  - completely: broken
`
	require.NoError(t, os.WriteFile(testFile, []byte(yamlContent), 0644))

	// Even with a redactor that would normally make changes
	redactor := &mockRedactor{shouldRedact: true}
	processor := NewFileProcessor(redactor)

	changed, err := processor.Process(context.Background(), testFile)
	// Should handle malformed List structure gracefully
	assert.NoError(t, err, "Process() should handle malformed List structure gracefully")
	// The processor should return false for malformed content since it can't be properly processed
	assert.False(t, changed, "Process() should return false for malformed List resources")
}
