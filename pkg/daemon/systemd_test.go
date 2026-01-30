package daemon

import (
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
