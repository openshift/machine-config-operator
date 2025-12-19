// Assisted-by: Claude
package osimagestream

import (
	"context"
	"errors"
	"testing"

	"github.com/containers/image/v5/types"
	imagev1 "github.com/openshift/api/image/v1"
	"github.com/openshift/machine-config-operator/pkg/imageutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	testImageStreamName      = "test-stream"
	testImageStreamNamespace = "openshift"

	testRHELCoreosImage           = "quay.io/openshift/rhel-coreos:9.4"
	testRHELCoreosExtensionsImage = "quay.io/openshift/rhel-coreos-extensions:9.4"

	testRHELCoreosTagName    = "rhel-coreos-9.4"
	testRHELCoreosExtTagName = "rhel-coreos-extensions-9.4"

	labelStreamClass = "io.openshift.os.streamclass"
	labelOSTreeLinux = "ostree.linux"

	labelValuePresent = "present"

	streamNameRHELCoreos = "rhel-coreos"
)

type mockImageStreamProvider struct {
	imageStream *imagev1.ImageStream
	err         error
}

func (m *mockImageStreamProvider) ReadImageStream(_ context.Context) (*imagev1.ImageStream, error) {
	return m.imageStream, m.err
}

func createTestImageStream() *imagev1.ImageStream {
	return &imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testImageStreamName,
			Namespace: testImageStreamNamespace,
		},
		Spec: imagev1.ImageStreamSpec{
			Tags: []imagev1.TagReference{
				{
					Name: testRHELCoreosTagName,
					From: &corev1.ObjectReference{
						Kind: "DockerImage",
						Name: testRHELCoreosImage,
					},
				},
				{
					Name: testRHELCoreosExtTagName,
					From: &corev1.ObjectReference{
						Kind: "DockerImage",
						Name: testRHELCoreosExtensionsImage,
					},
				},
			},
		},
	}
}

func TestImageStreamStreamSource_FetchStreams(t *testing.T) {
	tests := []struct {
		name                string
		providerImageStream *imagev1.ImageStream
		providerErr         error
		inspectorResults    []imageutils.BulkInspectResult
		inspectorErr        error
		expectedStreamNames []string
		errorContains       string
	}{
		{
			name:                "success with OS and extensions",
			providerImageStream: createTestImageStream(),
			inspectorResults: []imageutils.BulkInspectResult{
				{
					Image: testRHELCoreosImage,
					InspectInfo: &types.ImageInspectInfo{
						Labels: map[string]string{
							labelStreamClass: streamNameRHELCoreos,
							labelOSTreeLinux: labelValuePresent,
						},
					},
				},
				{
					Image: testRHELCoreosExtensionsImage,
					InspectInfo: &types.ImageInspectInfo{
						Labels: map[string]string{
							labelStreamClass: streamNameRHELCoreos,
						},
					},
				},
			},
			expectedStreamNames: []string{streamNameRHELCoreos},
		},
		{
			name:          "imagestream provider error",
			providerErr:   errors.New("failed to read imagestream"),
			errorContains: "failed to read imagestream",
		},
		{
			name:                "images inspector error",
			providerImageStream: createTestImageStream(),
			inspectorErr:        errors.New("inspection failed"),
			errorContains:       "inspection failed",
		},
		{
			name: "annotation-based filtering",
			providerImageStream: &imagev1.ImageStream{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testImageStreamName,
					Namespace: testImageStreamNamespace,
				},
				Spec: imagev1.ImageStreamSpec{
					Tags: []imagev1.TagReference{
						{
							Name: "custom-os",
							From: &corev1.ObjectReference{
								Kind: "DockerImage",
								Name: "quay.io/openshift/custom-os:latest",
							},
							Annotations: map[string]string{
								"io.openshift.build.source-location": "https://github.com/openshift/os",
							},
						},
						{
							Name: "custom-os-extensions",
							From: &corev1.ObjectReference{
								Kind: "DockerImage",
								Name: "quay.io/openshift/custom-os-extensions:latest",
							},
							Annotations: map[string]string{
								"io.openshift.build.source-location": "https://github.com/openshift/os",
							},
						},
					},
				},
			},
			inspectorResults: []imageutils.BulkInspectResult{
				{
					Image: "quay.io/openshift/custom-os:latest",
					InspectInfo: &types.ImageInspectInfo{
						Labels: map[string]string{
							labelStreamClass: "custom-stream",
						},
					},
				},
				{
					Image: "quay.io/openshift/custom-os-extensions:latest",
					InspectInfo: &types.ImageInspectInfo{
						Labels: map[string]string{
							labelStreamClass: "custom-stream",
							labelOSTreeLinux: labelValuePresent,
						},
					},
				},
			},
			expectedStreamNames: []string{"custom-stream"},
		},
		{
			name: "skips non-DockerImage tags",
			providerImageStream: &imagev1.ImageStream{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testImageStreamName,
					Namespace: testImageStreamNamespace,
				},
				Spec: imagev1.ImageStreamSpec{
					Tags: []imagev1.TagReference{
						{
							Name: "rhel-coreos",
							From: &corev1.ObjectReference{
								Kind: "ImageStreamTag",
								Name: "other:latest",
							},
						},
						{
							Name: "stream-coreos",
							From: nil,
						},
					},
				},
			},
			inspectorResults:    []imageutils.BulkInspectResult{},
			expectedStreamNames: []string{},
		},
		{
			name:                "skips individual image inspection errors",
			providerImageStream: createTestImageStream(),
			inspectorResults: []imageutils.BulkInspectResult{
				{
					Image: "invalid-image",
					Error: errors.New("failed to inspect"),
				},
				{
					Image: testRHELCoreosImage,
					InspectInfo: &types.ImageInspectInfo{
						Labels: map[string]string{
							labelStreamClass: streamNameRHELCoreos,
						},
					},
				},
				{
					Image: testRHELCoreosExtensionsImage,
					InspectInfo: &types.ImageInspectInfo{
						Labels: map[string]string{
							labelStreamClass: streamNameRHELCoreos,
							labelOSTreeLinux: labelValuePresent,
						},
					},
				},
			},
			expectedStreamNames: []string{streamNameRHELCoreos},
		},
		{
			name: "empty imagestream",
			providerImageStream: &imagev1.ImageStream{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testImageStreamName,
					Namespace: testImageStreamNamespace,
				},
				Spec: imagev1.ImageStreamSpec{
					Tags: []imagev1.TagReference{},
				},
			},
			inspectorResults:    []imageutils.BulkInspectResult{},
			expectedStreamNames: []string{},
		},
		{
			name: "multiple streams",
			providerImageStream: &imagev1.ImageStream{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testImageStreamName,
					Namespace: testImageStreamNamespace,
				},
				Spec: imagev1.ImageStreamSpec{
					Tags: []imagev1.TagReference{
						{
							Name: testRHELCoreosTagName,
							From: &corev1.ObjectReference{
								Kind: "DockerImage",
								Name: testRHELCoreosImage,
							},
						},
						{
							Name: "stream-coreos-10-extensions",
							From: &corev1.ObjectReference{
								Kind: "DockerImage",
								Name: "quay.io/openshift/stream-coreos-extensions:10.0",
							},
						},
					},
				},
			},
			inspectorResults: []imageutils.BulkInspectResult{
				{
					Image: testRHELCoreosImage,
					InspectInfo: &types.ImageInspectInfo{
						Labels: map[string]string{
							labelStreamClass: streamNameRHELCoreos,
						},
					},
				},
				{
					Image: testRHELCoreosExtensionsImage,
					InspectInfo: &types.ImageInspectInfo{
						Labels: map[string]string{
							labelStreamClass: streamNameRHELCoreos,
							labelOSTreeLinux: labelValuePresent,
						},
					},
				},
				{
					Image: "quay.io/openshift/stream-coreos:10.0",
					InspectInfo: &types.ImageInspectInfo{
						Labels: map[string]string{
							labelStreamClass: "stream-coreos",
						},
					},
				},
				{
					Image: "quay.io/openshift/stream-coreos-extensions:10.0",
					InspectInfo: &types.ImageInspectInfo{
						Labels: map[string]string{
							labelStreamClass: "stream-coreos",
							labelOSTreeLinux: labelValuePresent,
						},
					},
				},
			},
			expectedStreamNames: []string{streamNameRHELCoreos, "stream-coreos"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			streams, err := NewImageStreamStreamSource(
				&mockImagesInspector{
					results: tt.inspectorResults,
					err:     tt.inspectorErr,
				}, &mockImageStreamProvider{
					imageStream: tt.providerImageStream,
					err:         tt.providerErr,
				}, NewImageStreamExtractor()).
				FetchStreams(ctx)

			if tt.errorContains != "" {
				require.Error(t, err)
				assert.Nil(t, streams)
				assert.Contains(t, err.Error(), tt.errorContains)
				return
			}

			require.NoError(t, err)
			require.Len(t, streams, len(tt.expectedStreamNames))

			// Verify all expected stream names are present (order-independent)
			actualNames := make(map[string]bool)
			for _, stream := range streams {
				actualNames[stream.Name] = true
			}
			for _, expectedName := range tt.expectedStreamNames {
				assert.True(t, actualNames[expectedName], "expected stream %s not found", expectedName)
			}
		})
	}
}
