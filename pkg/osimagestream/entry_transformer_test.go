package osimagestream

import (
	"bytes"
	"testing"

	imagev1 "github.com/openshift/api/image/v1"
	corev1 "k8s.io/api/core/v1"
	"github.com/openshift/client-go/image/clientset/versioned/scheme"
	"github.com/openshift/machine-config-operator/pkg/imageutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
)

func encodeImageStream(t *testing.T, is *imagev1.ImageStream) []byte {
	t.Helper()
	encoder := scheme.Codecs.EncoderForVersion(
		json.NewSerializerWithOptions(json.DefaultMetaFactory, scheme.Scheme, scheme.Scheme,
			json.SerializerOptions{Yaml: false}),
		imagev1.SchemeGroupVersion,
	)
	var buf bytes.Buffer
	require.NoError(t, encoder.Encode(is, &buf))
	return buf.Bytes()
}

func TestImageStreamFileTransformer_FiltersUnneededTags(t *testing.T) {
	is := &imagev1.ImageStream{
		Spec: imagev1.ImageStreamSpec{
			Tags: []imagev1.TagReference{
				{
					Name: "rhel-coreos",
					From: &corev1.ObjectReference{Kind: "DockerImage", Name: "quay.io/os@sha256:aaa"},
				},
				{
					Name: "rhel-coreos-extensions",
					From: &corev1.ObjectReference{Kind: "DockerImage", Name: "quay.io/ext@sha256:bbb"},
				},
				{
					Name: "machine-config-operator",
					From: &corev1.ObjectReference{Kind: "DockerImage", Name: "quay.io/mco@sha256:ccc"},
				},
				{
					Name: "etcd",
					From: &corev1.ObjectReference{Kind: "DockerImage", Name: "quay.io/etcd@sha256:ddd"},
				},
				{
					Name: "kube-apiserver",
					From: &corev1.ObjectReference{Kind: "DockerImage", Name: "quay.io/kas@sha256:eee"},
				},
			},
		},
	}

	entry := &imageutils.InspectionCacheEntry{
		Labels: map[string]string{"io.openshift.release": "5.0.0"},
		Files:  map[string][]byte{releaseImageStreamLocation: encodeImageStream(t, is)},
	}

	transformer := NewImageStreamFileTransformer()
	result := transformer("sha256:release", entry)
	require.NotNil(t, result)

	resultIS := decodeImageStream(t, result.Files[releaseImageStreamLocation])
	tagNames := make([]string, 0, len(resultIS.Spec.Tags))
	for _, tag := range resultIS.Spec.Tags {
		tagNames = append(tagNames, tag.Name)
	}

	assert.Contains(t, tagNames, "rhel-coreos")
	assert.Contains(t, tagNames, "rhel-coreos-extensions")
	assert.NotContains(t, tagNames, "etcd")
	assert.NotContains(t, tagNames, "kube-apiserver")
	assert.NotContains(t, tagNames, "machine-config-operator")
}

func TestImageStreamFileTransformer_AnnotationMatch(t *testing.T) {
	is := &imagev1.ImageStream{
		Spec: imagev1.ImageStreamSpec{
			Tags: []imagev1.TagReference{
				{
					Name:        "custom-os",
					From:        &corev1.ObjectReference{Kind: "DockerImage", Name: "quay.io/custom@sha256:aaa"},
					Annotations: map[string]string{"io.openshift.build.source-location": "https://github.com/openshift/os"},
				},
				{
					Name: "unrelated",
					From: &corev1.ObjectReference{Kind: "DockerImage", Name: "quay.io/other@sha256:bbb"},
				},
			},
		},
	}

	entry := &imageutils.InspectionCacheEntry{
		Files: map[string][]byte{releaseImageStreamLocation: encodeImageStream(t, is)},
	}

	transformer := NewImageStreamFileTransformer()
	result := transformer("sha256:x", entry)
	require.NotNil(t, result)

	resultIS := decodeImageStream(t, result.Files[releaseImageStreamLocation])
	require.Len(t, resultIS.Spec.Tags, 1)
	assert.Equal(t, "custom-os", resultIS.Spec.Tags[0].Name)
}

func TestImageStreamFileTransformer_NonImageStreamFilePassesThrough(t *testing.T) {
	entry := &imageutils.InspectionCacheEntry{
		Labels: map[string]string{"k": "v"},
		Files:  map[string][]byte{"/other/path": []byte("untouched")},
	}

	transformer := NewImageStreamFileTransformer()
	result := transformer("sha256:x", entry)
	require.NotNil(t, result)
	assert.Equal(t, []byte("untouched"), result.Files["/other/path"])
}

func TestImageStreamFileTransformer_DoesNotMutateInput(t *testing.T) {
	is := &imagev1.ImageStream{
		Spec: imagev1.ImageStreamSpec{
			Tags: []imagev1.TagReference{
				{Name: "rhel-coreos", From: &corev1.ObjectReference{Kind: "DockerImage", Name: "quay.io/os@sha256:aaa"}},
				{Name: "etcd", From: &corev1.ObjectReference{Kind: "DockerImage", Name: "quay.io/etcd@sha256:bbb"}},
			},
		},
	}
	originalData := encodeImageStream(t, is)

	entry := &imageutils.InspectionCacheEntry{
		Files: map[string][]byte{releaseImageStreamLocation: originalData},
	}

	transformer := NewImageStreamFileTransformer()
	_ = transformer("sha256:x", entry)

	assert.Equal(t, originalData, entry.Files[releaseImageStreamLocation])
}

func decodeImageStream(t *testing.T, data []byte) *imagev1.ImageStream {
	t.Helper()
	serializer := json.NewSerializerWithOptions(json.DefaultMetaFactory, scheme.Scheme, scheme.Scheme,
		json.SerializerOptions{Yaml: false})
	obj, _, err := serializer.Decode(data, nil, nil)
	require.NoError(t, err)
	is, ok := obj.(*imagev1.ImageStream)
	require.True(t, ok)
	return is
}
