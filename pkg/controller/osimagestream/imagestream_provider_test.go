// Assisted-by: Claude
package osimagestream

import (
	"context"
	"errors"
	"testing"

	imagev1 "github.com/openshift/api/image/v1"
	"github.com/openshift/client-go/image/clientset/versioned/scheme"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
)

const (
	testReleaseName = "quay.io/openshift/release:4.15.0"
)

func TestImageStreamProviderResource_ReadImageStream(t *testing.T) {
	imageStream := &imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-stream",
			Namespace: "openshift",
		},
	}

	provider := NewImageStreamProviderResource(imageStream)
	result, err := provider.ReadImageStream(context.Background())

	require.NoError(t, err)
	assert.Equal(t, imageStream, result)
}

func TestImageStreamProviderNetwork_ReadImageStream(t *testing.T) {
	validImageStream := &imagev1.ImageStream{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "image.openshift.io/v1",
			Kind:       "ImageStream",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-stream",
			Namespace: "openshift",
		},
		Spec: imagev1.ImageStreamSpec{
			Tags: []imagev1.TagReference{
				{
					Name: "rhel-coreos-9.4",
					From: &corev1.ObjectReference{
						Kind: "DockerImage",
						Name: "quay.io/openshift/rhel-coreos:9.4",
					},
				},
			},
		},
	}

	validImageStreamBytes, err := runtime.Encode(scheme.Codecs.LegacyCodec(imagev1.SchemeGroupVersion), validImageStream)
	require.NoError(t, err)

	wrongTypeObj := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "test",
		},
		Data: map[string]string{
			"key": "value",
		},
	}

	coreScheme := runtime.NewScheme()
	_ = corev1.AddToScheme(coreScheme)
	coreCodecs := serializer.NewCodecFactory(coreScheme)
	wrongTypeBytes, err := runtime.Encode(coreCodecs.LegacyCodec(corev1.SchemeGroupVersion), wrongTypeObj)
	require.NoError(t, err)

	tests := []struct {
		name          string
		fetchData     []byte
		fetchErr      error
		errorContains string
	}{
		{
			name:      "success",
			fetchData: validImageStreamBytes,
		},
		{
			name:          "fetch error",
			fetchErr:      errors.New("network error"),
			errorContains: "network error",
		},
		{
			name:          "empty data",
			fetchData:     []byte{},
			errorContains: "no ImageStream found",
		},
		{
			name:          "invalid manifest",
			fetchData:     []byte("not valid yaml"),
			errorContains: "invalid manifest",
		},
		{
			name:          "wrong type",
			fetchData:     wrongTypeBytes,
			errorContains: "invalid manifest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			inspector := &mockImagesInspector{
				fetchData: tt.fetchData,
				fetchErr:  tt.fetchErr,
			}

			provider := NewImageStreamProviderNetwork(inspector, testReleaseName)
			result, err := provider.ReadImageStream(ctx)

			if tt.errorContains != "" {
				require.Error(t, err)
				assert.Nil(t, result)
				assert.Contains(t, err.Error(), tt.errorContains)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, "ImageStream", result.Kind)
			assert.Equal(t, "test-stream", result.Name)
		})
	}
}
