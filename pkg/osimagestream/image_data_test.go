// Assisted-by: Claude
package osimagestream

import (
	"testing"

	"github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImageStreamExtractorImpl_GetImageData_OSImage(t *testing.T) {
	extractor := NewImageStreamExtractor()

	labels := map[string]string{
		"io.openshift.os.streamclass": "rhel-9",
		"ostree.linux":                "present",
	}

	result := extractor.GetImageData("quay.io/openshift/os@sha256:abc123", labels)

	require.NotNil(t, result)
	assert.Equal(t, "quay.io/openshift/os@sha256:abc123", result.Image)
	assert.EqualValues(t, ImageTypeOS, result.Type)
	assert.Equal(t, "rhel-9", result.Stream)
}

func TestImageStreamExtractorImpl_GetImageData_ExtensionsImage(t *testing.T) {
	extractor := NewImageStreamExtractor()

	labels := map[string]string{
		"io.openshift.os.streamclass": "rhel-9",
	}

	result := extractor.GetImageData("quay.io/openshift/ext@sha256:def456", labels)

	require.NotNil(t, result)
	assert.Equal(t, "quay.io/openshift/ext@sha256:def456", result.Image)
	assert.EqualValues(t, ImageTypeExtensions, result.Type)
	assert.Equal(t, "rhel-9", result.Stream)
}

func TestImageStreamExtractorImpl_GetImageData_MissingStreamLabel(t *testing.T) {
	extractor := NewImageStreamExtractor()

	labels := map[string]string{
		"some.other.label": "value",
	}

	result := extractor.GetImageData("quay.io/openshift/random@sha256:xyz789", labels)

	assert.Nil(t, result)
}

func TestImageStreamExtractorImpl_GetImageData_EmptyLabels(t *testing.T) {
	extractor := NewImageStreamExtractor()

	result := extractor.GetImageData("quay.io/openshift/random@sha256:xyz789", map[string]string{})

	assert.Nil(t, result)
}

func TestGroupOSContainerImageMetadataToStream_SingleStream(t *testing.T) {
	imagesMetadata := []*ImageData{
		{
			Image:  "quay.io/openshift/os@sha256:abc123",
			Type:   ImageTypeOS,
			Stream: "rhel-9",
		},
		{
			Image:  "quay.io/openshift/ext@sha256:def456",
			Type:   ImageTypeExtensions,
			Stream: "rhel-9",
		},
	}

	result := GroupOSContainerImageMetadataToStream(imagesMetadata)

	require.Len(t, result, 1)
	assert.Equal(t, "rhel-9", result[0].Name)
	assert.Equal(t, v1alpha1.ImageDigestFormat("quay.io/openshift/os@sha256:abc123"), result[0].OSImage)
	assert.Equal(t, v1alpha1.ImageDigestFormat("quay.io/openshift/ext@sha256:def456"), result[0].OSExtensionsImage)
}

func TestGroupOSContainerImageMetadataToStream_MultipleStreams(t *testing.T) {
	imagesMetadata := []*ImageData{
		{
			Image:  "quay.io/openshift/os-9@sha256:aaa111",
			Type:   ImageTypeOS,
			Stream: "rhel-9",
		},
		{
			Image:  "quay.io/openshift/ext-9@sha256:bbb222",
			Type:   ImageTypeExtensions,
			Stream: "rhel-9",
		},
		{
			Image:  "quay.io/openshift/os-10@sha256:ccc333",
			Type:   ImageTypeOS,
			Stream: "rhel-10",
		},
		{
			Image:  "quay.io/openshift/ext-10@sha256:ddd444",
			Type:   ImageTypeExtensions,
			Stream: "rhel-10",
		},
	}

	result := GroupOSContainerImageMetadataToStream(imagesMetadata)

	require.Len(t, result, 2)

	// Verify both streams are present (order-independent)
	streamMap := make(map[string]*v1alpha1.OSImageStreamSet)
	for _, stream := range result {
		streamMap[stream.Name] = stream
	}

	rhel9 := streamMap["rhel-9"]
	require.NotNil(t, rhel9)
	assert.Equal(t, v1alpha1.ImageDigestFormat("quay.io/openshift/os-9@sha256:aaa111"), rhel9.OSImage)
	assert.Equal(t, v1alpha1.ImageDigestFormat("quay.io/openshift/ext-9@sha256:bbb222"), rhel9.OSExtensionsImage)

	rhel10 := streamMap["rhel-10"]
	require.NotNil(t, rhel10)
	assert.Equal(t, v1alpha1.ImageDigestFormat("quay.io/openshift/os-10@sha256:ccc333"), rhel10.OSImage)
	assert.Equal(t, v1alpha1.ImageDigestFormat("quay.io/openshift/ext-10@sha256:ddd444"), rhel10.OSExtensionsImage)
}

func TestGroupOSContainerImageMetadataToStream_PartialURLs(t *testing.T) {
	// This test ensures that GroupOSContainerImageMetadataToStream only returns
	// OSImageStreamSet that have both URLs

	tests := []struct {
		name      string
		imageData []*ImageData
	}{
		{
			name: "OS only",
			imageData: []*ImageData{
				{
					Image:  "quay.io/openshift/os@sha256:abc123",
					Type:   ImageTypeOS,
					Stream: "rhel-9",
				},
			},
		},
		{
			name: "Extensions only",
			imageData: []*ImageData{
				{
					Image:  "quay.io/openshift/ext@sha256:def456",
					Type:   ImageTypeExtensions,
					Stream: "rhel-9",
				},
			},
		},
		{
			name: "OS Duplicated",
			imageData: []*ImageData{
				{
					Image:  "quay.io/openshift/os-old@sha256:111",
					Type:   ImageTypeOS,
					Stream: "rhel-9",
				},
				{
					Image:  "quay.io/openshift/os-new@sha256:222",
					Type:   ImageTypeOS,
					Stream: "rhel-9",
				},
			},
		},
		{
			name: "Extensions Duplicated",
			imageData: []*ImageData{
				{
					Image:  "quay.io/openshift/ext-old@sha256:333",
					Type:   ImageTypeExtensions,
					Stream: "rhel-9",
				},
				{
					Image:  "quay.io/openshift/ext-new@sha256:444",
					Type:   ImageTypeExtensions,
					Stream: "rhel-9",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GroupOSContainerImageMetadataToStream(tt.imageData)
			require.Empty(t, result, "result should be empty")
		})
	}
}

func TestGroupOSContainerImageMetadataToStream_EmptyInput(t *testing.T) {
	assert.Len(t, GroupOSContainerImageMetadataToStream([]*ImageData{}), 0)
}

func TestNewOSImageStreamURLSetFromImageMetadata_OSImage(t *testing.T) {
	imageMetadata := &ImageData{
		Image:  "quay.io/openshift/os@sha256:abc123",
		Type:   ImageTypeOS,
		Stream: "rhel-9",
	}

	result := NewOSImageStreamURLSetFromImageMetadata(imageMetadata)

	assert.Equal(t, "rhel-9", result.Name)
	assert.Equal(t, v1alpha1.ImageDigestFormat("quay.io/openshift/os@sha256:abc123"), result.OSImage)
	assert.Empty(t, result.OSExtensionsImage)
}

func TestNewOSImageStreamURLSetFromImageMetadata_ExtensionsImage(t *testing.T) {
	imageMetadata := &ImageData{
		Image:  "quay.io/openshift/ext@sha256:def456",
		Type:   ImageTypeExtensions,
		Stream: "rhel-9",
	}

	result := NewOSImageStreamURLSetFromImageMetadata(imageMetadata)

	assert.Equal(t, "rhel-9", result.Name)
	assert.Empty(t, result.OSImage)
	assert.Equal(t, v1alpha1.ImageDigestFormat("quay.io/openshift/ext@sha256:def456"), result.OSExtensionsImage)
}
