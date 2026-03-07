// Assisted-by: Claude
package osimagestream

import (
	"testing"

	"github.com/openshift/api/machineconfiguration/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGetStreamSetsNames(t *testing.T) {
	tests := []struct {
		name     string
		input    []v1alpha1.OSImageStreamSet
		expected []string
	}{
		{
			name:     "empty slice",
			input:    []v1alpha1.OSImageStreamSet{},
			expected: []string{},
		},
		{
			name: "single stream",
			input: []v1alpha1.OSImageStreamSet{
				{Name: "rhel-9"},
			},
			expected: []string{"rhel-9"},
		},
		{
			name: "multiple streams",
			input: []v1alpha1.OSImageStreamSet{
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

func getStubOSImageStream() *v1alpha1.OSImageStream {
	return &v1alpha1.OSImageStream{
		Status: v1alpha1.OSImageStreamStatus{
			DefaultStream: "rhel-9",
			AvailableStreams: []v1alpha1.OSImageStreamSet{
				{Name: "rhel-9", OSImage: "image1", OSExtensionsImage: "ext1"},
				{Name: "rhel-10", OSImage: "image2", OSExtensionsImage: "ext2"},
			},
		},
	}
}

func TestGetOSImageStreamSetByName(t *testing.T) {
	tests := []struct {
		name                 string
		osImageStreamFactory func() *v1alpha1.OSImageStream
		streamName           string
		expected             *v1alpha1.OSImageStreamSet
		errorContains        string
		errorCheckFn         func(*testing.T, error)
	}{
		{
			name:                 "find existing stream",
			osImageStreamFactory: getStubOSImageStream,
			streamName:           "rhel-9",
			expected:             &v1alpha1.OSImageStreamSet{Name: "rhel-9", OSImage: "image1", OSExtensionsImage: "ext1"},
		},
		{
			name:                 "find another existing stream",
			osImageStreamFactory: getStubOSImageStream,
			streamName:           "rhel-10",
			expected:             &v1alpha1.OSImageStreamSet{Name: "rhel-10", OSImage: "image2", OSExtensionsImage: "ext2"},
		},
		{
			name:                 "empty name returns default stream",
			osImageStreamFactory: getStubOSImageStream,
			streamName:           "",
			expected:             &v1alpha1.OSImageStreamSet{Name: "rhel-9", OSImage: "image1", OSExtensionsImage: "ext1"},
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
			var osImageStream *v1alpha1.OSImageStream
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

func TestGetBuiltinDefault(t *testing.T) {
	tests := []struct {
		name     string
		input    *v1alpha1.OSImageStream
		expected string
	}{
		{
			name:     "nil OSImageStream",
			input:    nil,
			expected: "",
		},
		{
			name: "no annotation",
			input: &v1alpha1.OSImageStream{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
			},
			expected: "",
		},
		{
			name: "annotation present",
			input: &v1alpha1.OSImageStream{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						ctrlcommon.BuiltinDefaultStreamAnnotationKey: "rhel-9",
					},
				},
			},
			expected: "rhel-9",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, GetBuiltinDefault(tt.input))
		})
	}
}

func TestGetOSImageStreamSpecDefault(t *testing.T) {
	tests := []struct {
		name     string
		input    *v1alpha1.OSImageStream
		expected string
	}{
		{
			name:     "nil OSImageStream",
			input:    nil,
			expected: "",
		},
		{
			name:     "nil Spec",
			input:    &v1alpha1.OSImageStream{},
			expected: "",
		},
		{
			name: "empty DefaultStream",
			input: &v1alpha1.OSImageStream{
				Spec: &v1alpha1.OSImageStreamSpec{},
			},
			expected: "",
		},
		{
			name: "DefaultStream set",
			input: &v1alpha1.OSImageStream{
				Spec: &v1alpha1.OSImageStreamSpec{DefaultStream: "rhel-10"},
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
