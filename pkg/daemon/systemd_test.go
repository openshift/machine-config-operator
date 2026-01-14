package daemon

import (
	"context"
	"reflect"
	"testing"
)

func TestNormalizeUnitNames(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "units without suffix get .service appended",
			input:    []string{"crio", "kubelet"},
			expected: []string{"crio.service", "kubelet.service"},
		},
		{
			name:     "units with valid suffixes are preserved",
			input:    []string{"crio.service", "docker.socket", "fstrim.timer"},
			expected: []string{"crio.service", "docker.socket", "fstrim.timer"},
		},
		{
			name:     "mixed units",
			input:    []string{"crio", "docker.socket", "kubelet.service"},
			expected: []string{"crio.service", "docker.socket", "kubelet.service"},
		},
		{
			name:     "empty slice",
			input:    []string{},
			expected: []string{},
		},
		{
			name:     "unit with dots but no valid suffix",
			input:    []string{"my.custom.name"},
			expected: []string{"my.custom.name.service"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NormalizeSystemdUnitNames(tt.input...)
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("NormalizeSystemdUnitNames(%v) = %v, expected %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestMockPresetPolicy(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name            string
		initialEnabled  bool
		presetPolicy    *bool
		expectedEnabled bool
	}{
		{
			name:            "preset disables unit by default when no policy is set",
			initialEnabled:  true,
			presetPolicy:    nil,
			expectedEnabled: false,
		},
		{
			name:            "preset enables unit when policy is set to true",
			initialEnabled:  false,
			presetPolicy:    boolPtr(true),
			expectedEnabled: true,
		},
		{
			name:            "preset disables unit when policy is set to false",
			initialEnabled:  true,
			presetPolicy:    boolPtr(false),
			expectedEnabled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unit := newMockUnitState("test.service")
			unit.enabled = tt.initialEnabled

			conn := newMockSystemdConnection(map[string]*mockUnitState{
				"test.service": unit,
			})

			if tt.presetPolicy != nil {
				conn.SetPresetPolicy("test.service", *tt.presetPolicy)
			}

			if err := conn.Preset(ctx, "test.service"); err != nil {
				t.Fatalf("Preset() returned unexpected error: %v", err)
			}

			if unit.enabled != tt.expectedEnabled {
				t.Errorf("after Preset(), unit.enabled = %v, expected %v", unit.enabled, tt.expectedEnabled)
			}
		})
	}
}

func boolPtr(b bool) *bool { return &b }
