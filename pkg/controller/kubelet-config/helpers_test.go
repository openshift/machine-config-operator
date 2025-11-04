package kubeletconfig

import (
	"strings"
	"testing"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

func TestCreateNewKubeletDynamicSystemReservedIgnition(t *testing.T) {
	tests := []struct {
		name                string
		autoSystemReserved  *bool
		expectedAutoSizing  string
		expectedMemory      string
		expectedCPU         string
		expectedEphemeral   string
	}{
		{
			name:                "Auto sizing should be true if autoSystemReserved is passed as nil",
			autoSystemReserved:  nil,
			expectedAutoSizing:  "NODE_SIZING_ENABLED=true",
			expectedMemory:      "SYSTEM_RESERVED_MEMORY=1Gi",
			expectedCPU:         "SYSTEM_RESERVED_CPU=500m",
			expectedEphemeral:   "SYSTEM_RESERVED_ES=1Gi",
		},
		{
			name:                "Auto sizing should be false if autoSystemReserved is passed as false",
			autoSystemReserved:  boolPtr(false),
			expectedAutoSizing:  "NODE_SIZING_ENABLED=false",
			expectedMemory:      "SYSTEM_RESERVED_MEMORY=1Gi",
			expectedCPU:         "SYSTEM_RESERVED_CPU=500m",
			expectedEphemeral:   "SYSTEM_RESERVED_ES=1Gi",
		},
		{
			name:                "Auto sizing should be true if autoSystemReserved is passed as true",
			autoSystemReserved:  boolPtr(true),
			expectedAutoSizing:  "NODE_SIZING_ENABLED=true",
			expectedMemory:      "SYSTEM_RESERVED_MEMORY=1Gi",
			expectedCPU:         "SYSTEM_RESERVED_CPU=500m",
			expectedEphemeral:   "SYSTEM_RESERVED_ES=1Gi",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Call the function with nil for userDefinedSystemReserved as requested
			result := createNewKubeletDynamicSystemReservedIgnition(tt.autoSystemReserved, nil)

			// Verify the file path is correct
			expectedPath := "/etc/node-sizing-enabled.env"
			if result.Path != expectedPath {
				t.Errorf("Expected path %s, got %s", expectedPath, result.Path)
			}

			// Decode the file contents
			contents, err := ctrlcommon.DecodeIgnitionFileContents(result.Contents.Source, result.Contents.Compression)
			if err != nil {
				t.Fatalf("Failed to decode ignition file contents: %v", err)
			}

			contentsStr := string(contents)

			// Verify each expected line is in the contents
			if !strings.Contains(contentsStr, tt.expectedAutoSizing) {
				t.Errorf("Expected contents to contain %q, but got: %s", tt.expectedAutoSizing, contentsStr)
			}

			if !strings.Contains(contentsStr, tt.expectedMemory) {
				t.Errorf("Expected contents to contain %q, but got: %s", tt.expectedMemory, contentsStr)
			}

			if !strings.Contains(contentsStr, tt.expectedCPU) {
				t.Errorf("Expected contents to contain %q, but got: %s", tt.expectedCPU, contentsStr)
			}

			if !strings.Contains(contentsStr, tt.expectedEphemeral) {
				t.Errorf("Expected contents to contain %q, but got: %s", tt.expectedEphemeral, contentsStr)
			}
		})
	}
}

// boolPtr is a helper function to get a pointer to a bool value
func boolPtr(b bool) *bool {
	return &b
}
