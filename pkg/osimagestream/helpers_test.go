// Assisted-by: Claude
package osimagestream

import (
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

func TestGetStreamSetsNames(t *testing.T) {
	tests := []struct {
		name     string
		input    []mcfgv1.OSImageStreamSet
		expected []string
	}{
		{
			name:     "empty slice",
			input:    []mcfgv1.OSImageStreamSet{},
			expected: []string{},
		},
		{
			name: "single stream",
			input: []mcfgv1.OSImageStreamSet{
				{Name: "rhel-9"},
			},
			expected: []string{"rhel-9"},
		},
		{
			name: "multiple streams",
			input: []mcfgv1.OSImageStreamSet{
				{Name: "rhel-9"},
				{Name: "rhel-10"},
				{Name: "custom-stream"},
			},
			expected: []string{"rhel-9", "rhel-10", "custom-stream"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetStreamSetsNames(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func getStubOSImageStream() *mcfgv1.OSImageStream {
	return &mcfgv1.OSImageStream{
		Status: mcfgv1.OSImageStreamStatus{
			DefaultStream: "rhel-9",
			AvailableStreams: []mcfgv1.OSImageStreamSet{
				{Name: "rhel-9", OSImage: "image1", OSExtensionsImage: "ext1"},
				{Name: "rhel-10", OSImage: "image2", OSExtensionsImage: "ext2"},
			},
		},
	}
}

func TestGetOSImageStreamSetByName(t *testing.T) {
	tests := []struct {
		name                 string
		osImageStreamFactory func() *mcfgv1.OSImageStream
		streamName           string
		expected             *mcfgv1.OSImageStreamSet
		errorContains        string
		errorCheckFn         func(*testing.T, error)
	}{
		{
			name:                 "find existing stream",
			osImageStreamFactory: getStubOSImageStream,
			streamName:           "rhel-9",
			expected:             &mcfgv1.OSImageStreamSet{Name: "rhel-9", OSImage: "image1", OSExtensionsImage: "ext1"},
		},
		{
			name:                 "find another existing stream",
			osImageStreamFactory: getStubOSImageStream,
			streamName:           "rhel-10",
			expected:             &mcfgv1.OSImageStreamSet{Name: "rhel-10", OSImage: "image2", OSExtensionsImage: "ext2"},
		},
		{
			name:                 "empty name returns default stream",
			osImageStreamFactory: getStubOSImageStream,
			streamName:           "",
			expected:             &mcfgv1.OSImageStreamSet{Name: "rhel-9", OSImage: "image1", OSExtensionsImage: "ext1"},
		},
		{
			name:                 "non-existent stream",
			osImageStreamFactory: getStubOSImageStream,
			streamName:           "non-existent",
			errorContains:        "not found",
			errorCheckFn: func(t *testing.T, err error) {
				assert.True(t, apierrors.IsNotFound(err))
			},
		},
		{
			name:                 "nil osImageStream",
			osImageStreamFactory: nil,
			streamName:           "rhel-9",
			errorContains:        "cannot be nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var osImageStream *mcfgv1.OSImageStream
			if tt.osImageStreamFactory != nil {
				osImageStream = tt.osImageStreamFactory()
			}

			result, err := GetOSImageStreamSetByName(osImageStream, tt.streamName)
			if tt.errorContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
				assert.Nil(t, result)
				if tt.errorCheckFn != nil {
					tt.errorCheckFn(t, err)
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestGetOSImageStreamSpecDefault(t *testing.T) {
	tests := []struct {
		name     string
		input    *mcfgv1.OSImageStream
		expected string
	}{
		{
			name:     "nil OSImageStream",
			input:    nil,
			expected: "",
		},
		{
			name:     "nil Spec",
			input:    &mcfgv1.OSImageStream{},
			expected: "",
		},
		{
			name: "empty DefaultStream",
			input: &mcfgv1.OSImageStream{
				Spec: mcfgv1.OSImageStreamSpec{},
			},
			expected: "",
		},
		{
			name: "DefaultStream set",
			input: &mcfgv1.OSImageStream{
				Spec: mcfgv1.OSImageStreamSpec{DefaultStream: "rhel-10"},
			},
			expected: "rhel-10",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, GetOSImageStreamSpecDefault(tt.input))
		})
	}
}
