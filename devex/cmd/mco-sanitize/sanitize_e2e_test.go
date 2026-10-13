package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitize_E2E_GoldenFiles(t *testing.T) {
	testCases := []struct {
		name        string
		config      *Config // Direct config object
		inputFiles  []string
		goldenFiles []string
	}{
		{
			name:   "default-config",
			config: nil, // Use default config
			inputFiles: []string{
				"testdata/input/machineconfig.yaml",
				"testdata/input/controllerconfig.yaml",
				"testdata/input/configmap.yaml", // Should not be redacted
			},
			goldenFiles: []string{
				"testdata/expected/default-config/machineconfig.yaml",
				"testdata/expected/default-config/controllerconfig.yaml",
				"testdata/expected/default-config/configmap.yaml",
			},
		},
		{
			name: "custom-config-secrets",
			config: &Config{
				Redact: []ConfigRedact{
					{
						APIVersion: "v1",
						Kind:       "Secret",
						Paths:      []string{"data"},
					},
				},
			},
			inputFiles: []string{
				"testdata/input/secret.yaml",
				"testdata/input/configmap.yaml",
			},
			goldenFiles: []string{
				"testdata/expected/custom-config-secrets/secret.yaml",
				"testdata/expected/custom-config-secrets/configmap.yaml",
			},
		},
		{
			name: "json-files",
			config: &Config{
				Redact: []ConfigRedact{
					{
						APIVersion: "v1",
						Kind:       "Secret",
						Paths:      []string{"data"},
					},
				},
			},
			inputFiles: []string{
				"testdata/input/secret.json",
			},
			goldenFiles: []string{
				"testdata/expected/json-files/secret.json",
			},
		},
		{
			name: "array-primitive-redaction",
			config: &Config{
				Redact: []ConfigRedact{
					{
						APIVersion: "v1",
						Kind:       "ConfigMap",
						Paths: []string{
							"data.apiKeys",
							"data.passwords",
							"data.databases.1",
							"data.servers.1",
						},
					},
				},
			},
			inputFiles: []string{
				"testdata/input/configmap-with-primitive-arrays.yaml",
			},
			goldenFiles: []string{
				"testdata/expected/array-primitive-redaction/configmap-with-primitive-arrays.yaml",
			},
		},
		{
			name:   "mixed-file-types",
			config: nil, // Use default config
			inputFiles: []string{
				"testdata/input/machineconfig.yaml",
				"testdata/input/application.log",
				"testdata/input/backup.tar.gz",
				"testdata/input/readme.txt",
				"testdata/input/script.sh",
				"testdata/input/binary-file.bin",
			},
			goldenFiles: []string{
				"testdata/expected/mixed-file-types/machineconfig.yaml",
				"testdata/expected/mixed-file-types/application.log",
				"testdata/expected/mixed-file-types/backup.tar.gz",
				"testdata/expected/mixed-file-types/readme.txt",
				"testdata/expected/mixed-file-types/script.sh",
				"testdata/expected/mixed-file-types/binary-file.bin",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()

			// Copy input files to temp directory
			for _, inputFile := range tc.inputFiles {
				inputContent, err := os.ReadFile(inputFile)
				require.NoError(t, err, "Failed to read input file %s", inputFile)

				filename := filepath.Base(inputFile)
				tempInputFile := filepath.Join(tempDir, filename)
				err = os.WriteFile(tempInputFile, inputContent, 0644)
				require.NoError(t, err, "Failed to write temp input file %s", tempInputFile)
			}

			// Determine config to use
			var config *Config
			if tc.config != nil {
				config = tc.config
			} else {
				// Use default config
				defaultConfig, err := BuildDefaultConfig()
				require.NoError(t, err, "Failed to get default config for test case %s", tc.name)
				config = defaultConfig
			}

			// Run sanitize
			ctx := context.Background()
			err := sanitize(ctx, tempDir, 2, config)
			require.NoError(t, err, "Sanitize failed for test case %s", tc.name)

			// Compare outputs with golden files or update them if UPDATE_GOLDEN=true
			updateGolden := os.Getenv("UPDATE_GOLDEN") == "true"

			for i, goldenFile := range tc.goldenFiles {
				filename := filepath.Base(tc.inputFiles[i])
				actualFile := filepath.Join(tempDir, filename)
				actualContent, err := os.ReadFile(actualFile)
				require.NoError(t, err, "Failed to read actual output file %s", actualFile)

				if updateGolden {
					// Create directory for golden file if it doesn't exist
					goldenDir := filepath.Dir(goldenFile)
					err = os.MkdirAll(goldenDir, 0755)
					require.NoError(t, err, "Failed to create golden file directory %s", goldenDir)

					// Write the actual content as the new golden file
					err = os.WriteFile(goldenFile, actualContent, 0644)
					require.NoError(t, err, "Failed to write golden file %s", goldenFile)

					t.Logf("Updated golden file: %s", goldenFile)
				} else {
					// Normal comparison mode
					expectedContent, err := os.ReadFile(goldenFile)
					require.NoError(t, err, "Failed to read golden file %s", goldenFile)

					// Use string comparison for text files, byte comparison for binary files
					match, err := regexp.MatchString(`\.(yaml|yml|json|log|txt|sh)$`, filename)
					assert.NoError(t, err, "Failed to file extension %s", goldenFile)
					if match {
						assert.Equal(t, string(expectedContent), string(actualContent),
							"Text content mismatch for file %s in test case %s", filename, tc.name)
					} else {
						assert.Equal(t, expectedContent, actualContent,
							"Binary content mismatch for file %s in test case %s", filename, tc.name)
					}
				}
			}
		})
	}
}

func TestSanitize_E2E_ErrorCases(t *testing.T) {
	testCases := []struct {
		name          string
		setupFunc     func(t *testing.T) (inputDir string, config *Config)
		expectError   bool
		errorContains string
	}{
		{
			name: "nonexistent-directory",
			setupFunc: func(t *testing.T) (string, *Config) {
				defaultConfig, err := BuildDefaultConfig()
				require.NoError(t, err)
				return "/nonexistent/path", defaultConfig
			},
			expectError: true,
		},
		{
			name: "empty-redact-config",
			setupFunc: func(t *testing.T) (string, *Config) {
				tempDir := t.TempDir()

				// Create a secret file
				secretContent := `apiVersion: v1
kind: Secret
data:
  password: "secret-password"`

				secretFile := filepath.Join(tempDir, "secret.yaml")
				err := os.WriteFile(secretFile, []byte(secretContent), 0644)
				require.NoError(t, err)

				// Create empty config
				emptyConfig := &Config{Redact: []ConfigRedact{}}
				return tempDir, emptyConfig
			},
			expectError: false, // Should complete with warning
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			inputDir, config := tc.setupFunc(t)

			ctx := context.Background()
			err := sanitize(ctx, inputDir, 1, config)

			if tc.expectError {
				assert.Error(t, err, "Expected error for test case %s", tc.name)
				if tc.errorContains != "" {
					assert.Contains(t, err.Error(), tc.errorContains,
						"Error should contain %s for test case %s", tc.errorContains, tc.name)
				}
			} else {
				assert.NoError(t, err, "Should not error for test case %s", tc.name)
			}
		})
	}
}
