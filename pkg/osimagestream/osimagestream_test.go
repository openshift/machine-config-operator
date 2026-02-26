// Assisted-by: Claude
package osimagestream

import (
	"context"
	"errors"
	"testing"

	"github.com/containers/image/v5/types"
	imagev1 "github.com/openshift/api/image/v1"
	"github.com/openshift/api/machineconfiguration/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
)

// mockStreamSource is a test implementation of StreamSource
type mockStreamSource struct {
	streams []*v1alpha1.OSImageStreamSet
	err     error
}

func (m *mockStreamSource) FetchStreams(_ context.Context) ([]*v1alpha1.OSImageStreamSet, error) {
	return m.streams, m.err
}

func TestCollect(t *testing.T) {
	tests := []struct {
		name           string
		sources        []StreamSource
		expectedCount  int
		validateStreams func(t *testing.T, streams []v1alpha1.OSImageStreamSet)
	}{
		{
			name: "no streams found",
			sources: []StreamSource{
				&mockStreamSource{
					streams: []*v1alpha1.OSImageStreamSet{},
				},
			},
			expectedCount: 0,
		},
		{
			name: "all sources fail",
			sources: []StreamSource{
				&mockStreamSource{
					err: errors.New("fetch failed"),
				},
			},
			expectedCount: 0,
		},
		{
			name: "multiple sources - streams merged",
			sources: []StreamSource{
				&mockStreamSource{
					streams: []*v1alpha1.OSImageStreamSet{
						{Name: "rhel-9", OSImage: "image1", OSExtensionsImage: "ext1"},
					},
				},
				&mockStreamSource{
					streams: []*v1alpha1.OSImageStreamSet{
						{Name: "rhel-10", OSImage: "image2", OSExtensionsImage: "ext2"},
					},
				},
			},
			expectedCount: 2,
			validateStreams: func(t *testing.T, streams []v1alpha1.OSImageStreamSet) {
				streamNames := make(map[string]bool)
				for _, stream := range streams {
					streamNames[stream.Name] = true
				}
				assert.True(t, streamNames["rhel-9"])
				assert.True(t, streamNames["rhel-10"])
			},
		},
		{
			name: "duplicate streams - last one wins",
			sources: []StreamSource{
				&mockStreamSource{
					streams: []*v1alpha1.OSImageStreamSet{
						{Name: "rhel-9", OSImage: "original-image", OSExtensionsImage: "original-ext"},
					},
				},
				&mockStreamSource{
					streams: []*v1alpha1.OSImageStreamSet{
						{Name: "rhel-9", OSImage: "overridden-image", OSExtensionsImage: "overridden-ext"},
					},
				},
			},
			expectedCount: 1,
			validateStreams: func(t *testing.T, streams []v1alpha1.OSImageStreamSet) {
				require.Len(t, streams, 1)
				assert.Equal(t, "rhel-9", streams[0].Name)
				assert.Equal(t, v1alpha1.ImageDigestFormat("overridden-image"), streams[0].OSImage)
				assert.Equal(t, v1alpha1.ImageDigestFormat("overridden-ext"), streams[0].OSExtensionsImage)
			},
		},
		{
			name: "source with error - continues with other sources",
			sources: []StreamSource{
				&mockStreamSource{
					err: errors.New("fetch failed"),
				},
				&mockStreamSource{
					streams: []*v1alpha1.OSImageStreamSet{
						{Name: "rhel-9", OSImage: "image1", OSExtensionsImage: "ext1"},
					},
				},
			},
			expectedCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := collect(context.Background(), tt.sources)
			assert.Len(t, result, tt.expectedCount)
			if tt.validateStreams != nil {
				tt.validateStreams(t, result)
			}
		})
	}
}

func TestGetDefaultStreamSet(t *testing.T) {
	tests := []struct {
		name                   string
		streams                []v1alpha1.OSImageStreamSet
		builtinDefault         string
		requestedDefaultStream string
		expectedDefault        string
		errorContains          string
	}{
		{
			name: "requested default exists",
			streams: []v1alpha1.OSImageStreamSet{
				{Name: "rhel-9", OSImage: "image1"},
				{Name: "rhel-10", OSImage: "image2"},
			},
			builtinDefault:         "rhel-9",
			requestedDefaultStream: "rhel-10",
			expectedDefault:        "rhel-10",
		},
		{
			name: "requested default does not exist",
			streams: []v1alpha1.OSImageStreamSet{
				{Name: "rhel-9", OSImage: "image1"},
			},
			builtinDefault:         "rhel-9",
			requestedDefaultStream: "rhel-10",
			errorContains:          "could not find the requested rhel-10 default stream",
		},
		{
			name: "no requested default falls back to builtin",
			streams: []v1alpha1.OSImageStreamSet{
				{Name: "rhel-9", OSImage: "image1"},
			},
			builtinDefault:  "rhel-9",
			expectedDefault: "rhel-9",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := getDefaultStreamSet(tt.streams, tt.builtinDefault, tt.requestedDefaultStream)
			if tt.errorContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expectedDefault, result)
		})
	}
}

func TestDefaultStreamSourceFactory_Create_RuntimeBothSources(t *testing.T) {
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "machine-config-osimageurl",
			Namespace: "openshift-machine-config-operator",
		},
		Data: map[string]string{
			"baseOSContainerImage":           "quay.io/openshift/os-cm@sha256:111",
			"baseOSExtensionsContainerImage": "quay.io/openshift/ext-cm@sha256:222",
			"osImageURL":                     "",
			"releaseVersion":                 "4.21.0",
		},
	}

	// Create fake client and informer
	fakeClient := fake.NewSimpleClientset(configMap)
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 0)
	cmInformer := informerFactory.Core().V1().ConfigMaps()
	cmInformer.Informer().GetIndexer().Add(configMap)

	// Valid ImageStream manifest that will be fetched from the release image
	imageStreamManifest := `
apiVersion: image.openshift.io/v1
kind: ImageStream
metadata:
  name: release-images
spec:
  tags:
  - name: rhel-coreos
    from:
      kind: DockerImage
      name: quay.io/openshift/os-net@sha256:333
  - name: rhel-coreos-extensions
    from:
      kind: DockerImage
      name: quay.io/openshift/ext-net@sha256:444
`

	inspector := &mockImagesInspector{
		inspectData: map[string]*types.ImageInspectInfo{
			// ConfigMap source images
			"quay.io/openshift/os-cm@sha256:111": {
				Labels: map[string]string{
					"io.openshift.os.streamclass": "rhel-9",
					"ostree.linux":                "present",
				},
			},
			"quay.io/openshift/ext-cm@sha256:222": {
				Labels: map[string]string{
					"io.openshift.os.streamclass": "rhel-9",
				},
			},
			// Network source images
			"quay.io/openshift/os-net@sha256:333": {
				Labels: map[string]string{
					"io.openshift.os.streamclass": "rhel-9",
					"ostree.linux":                "present",
				},
			},
			"quay.io/openshift/ext-net@sha256:444": {
				Labels: map[string]string{
					"io.openshift.os.streamclass": "rhel-9",
				},
			},
		},
		fileData: map[string][]byte{
			"quay.io/openshift/release:4.16:/release-manifests/image-references": []byte(imageStreamManifest),
		},
	}

	inspectorFactory := &mockImagesInspectorFactory{inspector: inspector}
	factory := NewDefaultStreamSourceFactory(inspectorFactory)

	ctx := context.Background()
	sysCtx := &types.SystemContext{}

	result, err := factory.Create(ctx, sysCtx, CreateOptions{
		ReleaseImage:    "quay.io/openshift/release:4.16",
		ConfigMapLister: cmInformer.Lister(),
	})

	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "cluster", result.Name)
	assert.Equal(t, "rhel-9", result.Status.DefaultStream)
	assert.NotEmpty(t, result.Status.AvailableStreams)
	// Should have stream from both sources (they have same name so should be merged)
	assert.Len(t, result.Status.AvailableStreams, 1)
	assert.Equal(t, "rhel-9", result.Annotations[ctrlcommon.BuiltinDefaultStreamAnnotationKey])
}

func TestDefaultStreamSourceFactory_Create_RuntimeConfigMapOnly(t *testing.T) {
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "machine-config-osimageurl",
			Namespace: "openshift-machine-config-operator",
		},
		Data: map[string]string{
			"baseOSContainerImage":           "quay.io/openshift/os@sha256:abc123",
			"baseOSExtensionsContainerImage": "quay.io/openshift/ext@sha256:def456",
			"osImageURL":                     "",
			"releaseVersion":                 "4.21.0",
		},
	}

	// Create fake client and informer
	fakeClient := fake.NewSimpleClientset(configMap)
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 0)
	cmInformer := informerFactory.Core().V1().ConfigMaps()
	cmInformer.Informer().GetIndexer().Add(configMap)

	imageStreamManifest := `
apiVersion: image.openshift.io/v1
kind: ImageStream
metadata:
  name: release-images
spec:
  tags:
  - name: rhel-coreos
    from:
      kind: DockerImage
      name: quay.io/openshift/os@sha256:abc123
  - name: rhel-coreos-extensions
    from:
      kind: DockerImage
      name: quay.io/openshift/ext@sha256:def456
`

	inspector := &mockImagesInspector{
		inspectData: map[string]*types.ImageInspectInfo{
			"quay.io/openshift/os@sha256:abc123": {
				Labels: map[string]string{
					"io.openshift.os.streamclass": "rhel-9",
					"ostree.linux":                "present",
				},
			},
			"quay.io/openshift/ext@sha256:def456": {
				Labels: map[string]string{
					"io.openshift.os.streamclass": "rhel-9",
				},
			},
		},
		fileData: map[string][]byte{
			"quay.io/openshift/release:4.16:/release-manifests/image-references": []byte(imageStreamManifest),
		},
	}

	inspectorFactory := &mockImagesInspectorFactory{inspector: inspector}
	factory := NewDefaultStreamSourceFactory(inspectorFactory)

	ctx := context.Background()
	sysCtx := &types.SystemContext{}

	result, err := factory.Create(ctx, sysCtx, CreateOptions{
		ReleaseImage:    "quay.io/openshift/release:4.16",
		ConfigMapLister: cmInformer.Lister(),
		ExistingOSImageStream: &v1alpha1.OSImageStream{
			Spec: &v1alpha1.OSImageStreamSpec{DefaultStream: "rhel-9"},
		},
	})

	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "cluster", result.Name)
	assert.Equal(t, "rhel-9", result.Status.DefaultStream)
	assert.NotEmpty(t, result.Status.AvailableStreams)
	assert.Equal(t, "rhel-9", result.Annotations[ctrlcommon.BuiltinDefaultStreamAnnotationKey])
}

func TestDefaultStreamSourceFactory_Create_RuntimeBothSourcesFail(t *testing.T) {
	// Create empty fake client with no ConfigMaps
	fakeClient := fake.NewSimpleClientset()
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 0)
	cmInformer := informerFactory.Core().V1().ConfigMaps()

	inspector := &mockImagesInspector{
		inspectData: map[string]*types.ImageInspectInfo{},
	}

	inspectorFactory := &mockImagesInspectorFactory{inspector: inspector}
	factory := NewDefaultStreamSourceFactory(inspectorFactory)

	ctx := context.Background()
	sysCtx := &types.SystemContext{}

	result, err := factory.Create(ctx, sysCtx, CreateOptions{
		ReleaseImage:    "quay.io/openshift/release:4.16",
		ConfigMapLister: cmInformer.Lister(),
	})

	require.Error(t, err)
	assert.Nil(t, result)
	assert.ErrorIs(t, err, ErrorNoOSImageStreamAvailable)
}

func TestDefaultStreamSourceFactory_Create_RuntimeMultipleStreams(t *testing.T) {
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "machine-config-osimageurl",
			Namespace: "openshift-machine-config-operator",
		},
		Data: map[string]string{
			"baseOSContainerImage":           "quay.io/openshift/os@sha256:abc123",
			"baseOSExtensionsContainerImage": "quay.io/openshift/ext@sha256:def456",
			"osImageURL":                     "",
			"releaseVersion":                 "4.21.0",
		},
	}

	// Create fake client and informer
	fakeClient := fake.NewSimpleClientset(configMap)
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 0)
	cmInformer := informerFactory.Core().V1().ConfigMaps()
	cmInformer.Informer().GetIndexer().Add(configMap)

	// ImageStream manifest with multiple streams, including the default rhel-coreos tag
	imageStreamManifest := `
apiVersion: image.openshift.io/v1
kind: ImageStream
metadata:
  name: release-images
spec:
  tags:
  - name: rhel-coreos
    from:
      kind: DockerImage
      name: quay.io/openshift/os@sha256:abc123
  - name: rhel-10-coreos
    from:
      kind: DockerImage
      name: quay.io/openshift/os-10@sha256:aaa111
  - name: rhel-10-coreos-extensions
    from:
      kind: DockerImage
      name: quay.io/openshift/ext-10@sha256:bbb222
`

	inspector := &mockImagesInspector{
		inspectData: map[string]*types.ImageInspectInfo{
			// ConfigMap source images (rhel-9)
			"quay.io/openshift/os@sha256:abc123": {
				Labels: map[string]string{
					"io.openshift.os.streamclass": "rhel-9",
					"ostree.linux":                "present",
				},
			},
			"quay.io/openshift/ext@sha256:def456": {
				Labels: map[string]string{
					"io.openshift.os.streamclass": "rhel-9",
				},
			},
			// Network source images (rhel-10)
			"quay.io/openshift/os-10@sha256:aaa111": {
				Labels: map[string]string{
					"io.openshift.os.streamclass": "rhel-10",
					"ostree.linux":                "present",
				},
			},
			"quay.io/openshift/ext-10@sha256:bbb222": {
				Labels: map[string]string{
					"io.openshift.os.streamclass": "rhel-10",
				},
			},
		},
		fileData: map[string][]byte{
			"quay.io/openshift/release:4.16:/release-manifests/image-references": []byte(imageStreamManifest),
		},
	}

	inspectorFactory := &mockImagesInspectorFactory{inspector: inspector}
	factory := NewDefaultStreamSourceFactory(inspectorFactory)

	ctx := context.Background()
	sysCtx := &types.SystemContext{}

	result, err := factory.Create(ctx, sysCtx, CreateOptions{
		ReleaseImage:    "quay.io/openshift/release:4.16",
		ConfigMapLister: cmInformer.Lister(),
	})

	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "cluster", result.Name)
	assert.Equal(t, "rhel-9", result.Status.DefaultStream)
	require.Len(t, result.Status.AvailableStreams, 2)

	streamNames := []string{result.Status.AvailableStreams[0].Name, result.Status.AvailableStreams[1].Name}
	assert.ElementsMatch(t, []string{"rhel-9", "rhel-10"}, streamNames)
	assert.Equal(t, "rhel-9", result.Annotations[ctrlcommon.BuiltinDefaultStreamAnnotationKey])
}

func TestDefaultStreamSourceFactory_Create_BootstrapMultipleStreams(t *testing.T) {
	imageStream := &imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "machine-os-images",
			Namespace: "openshift-machine-config-operator",
		},
		Spec: imagev1.ImageStreamSpec{
			Tags: []imagev1.TagReference{
				{
					Name: "rhel-coreos",
					From: &corev1.ObjectReference{
						Kind: "DockerImage",
						Name: "quay.io/openshift/os-9@sha256:aaa111",
					},
				},
				{
					Name: "rhel-9-coreos",
					From: &corev1.ObjectReference{
						Kind: "DockerImage",
						Name: "quay.io/openshift/os-9@sha256:aaa111",
					},
				},
				{
					Name: "rhel-9-coreos-extensions",
					From: &corev1.ObjectReference{
						Kind: "DockerImage",
						Name: "quay.io/openshift/ext-9@sha256:bbb222",
					},
				},
				{
					Name: "rhel-10-coreos",
					From: &corev1.ObjectReference{
						Kind: "DockerImage",
						Name: "quay.io/openshift/os-10@sha256:ccc333",
					},
				},
				{
					Name: "rhel-10-coreos-extensions",
					From: &corev1.ObjectReference{
						Kind: "DockerImage",
						Name: "quay.io/openshift/ext-10@sha256:ddd444",
					},
				},
			},
		},
	}

	inspector := &mockImagesInspector{
		inspectData: map[string]*types.ImageInspectInfo{
			"quay.io/openshift/os-9@sha256:aaa111": {
				Labels: map[string]string{
					"io.openshift.os.streamclass": "rhel-9",
					"ostree.linux":                "present",
				},
			},
			"quay.io/openshift/ext-9@sha256:bbb222": {
				Labels: map[string]string{
					"io.openshift.os.streamclass": "rhel-9",
				},
			},
			"quay.io/openshift/os-10@sha256:ccc333": {
				Labels: map[string]string{
					"io.openshift.os.streamclass": "rhel-10",
					"ostree.linux":                "present",
				},
			},
			"quay.io/openshift/ext-10@sha256:ddd444": {
				Labels: map[string]string{
					"io.openshift.os.streamclass": "rhel-10",
				},
			},
		},
	}

	inspectorFactory := &mockImagesInspectorFactory{inspector: inspector}
	factory := NewDefaultStreamSourceFactory(inspectorFactory)

	ctx := context.Background()
	sysCtx := &types.SystemContext{}

	result, err := factory.Create(ctx, sysCtx, CreateOptions{
		ReleaseImageStream: imageStream,
	})

	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "cluster", result.Name)
	assert.NotEmpty(t, result.Status.DefaultStream)
	require.Len(t, result.Status.AvailableStreams, 2)

	streamNames := []string{result.Status.AvailableStreams[0].Name, result.Status.AvailableStreams[1].Name}
	assert.Contains(t, streamNames, "rhel-9")
	assert.Contains(t, streamNames, "rhel-10")
	assert.Equal(t, "rhel-9", result.Annotations[ctrlcommon.BuiltinDefaultStreamAnnotationKey])
}

func TestDefaultStreamSourceFactory_Create_BootstrapCliImagesOnly(t *testing.T) {
	cliImages := &OSImageTuple{
		OSImage:           "quay.io/openshift/os@sha256:abc123",
		OSExtensionsImage: "quay.io/openshift/ext@sha256:def456",
	}

	imageStream := &imagev1.ImageStream{
		Spec: imagev1.ImageStreamSpec{
			Tags: []imagev1.TagReference{
				{
					Name: "rhel-coreos",
					From: &corev1.ObjectReference{
						Kind: "DockerImage",
						Name: "quay.io/openshift/os@sha256:abc123",
					},
				},
			},
		},
	}

	inspector := &mockImagesInspector{
		inspectData: map[string]*types.ImageInspectInfo{
			"quay.io/openshift/os@sha256:abc123": {
				Labels: map[string]string{
					"io.openshift.os.streamclass": "rhel-9",
					"ostree.linux":                "present",
				},
			},
			"quay.io/openshift/ext@sha256:def456": {
				Labels: map[string]string{
					"io.openshift.os.streamclass": "rhel-9",
				},
			},
		},
	}

	inspectorFactory := &mockImagesInspectorFactory{inspector: inspector}
	factory := NewDefaultStreamSourceFactory(inspectorFactory)

	ctx := context.Background()
	sysCtx := &types.SystemContext{}

	result, err := factory.Create(ctx, sysCtx, CreateOptions{
		CliImages:          cliImages,
		ReleaseImageStream: imageStream,
		ExistingOSImageStream: &v1alpha1.OSImageStream{
			Spec: &v1alpha1.OSImageStreamSpec{DefaultStream: "rhel-9"},
		},
	})

	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "cluster", result.Name)
	assert.Equal(t, "rhel-9", result.Status.DefaultStream)
	require.NotEmpty(t, result.Status.AvailableStreams)
	assert.Equal(t, "rhel-9", result.Annotations[ctrlcommon.BuiltinDefaultStreamAnnotationKey])
	assert.Equal(t, "rhel-9", result.Spec.DefaultStream)
}

func TestDefaultStreamSourceFactory_Create_PreservesExistingSpec(t *testing.T) {
	imageStream := &imagev1.ImageStream{
		Spec: imagev1.ImageStreamSpec{
			Tags: []imagev1.TagReference{
				{
					Name: "rhel-coreos",
					From: &corev1.ObjectReference{
						Kind: "DockerImage",
						Name: "quay.io/openshift/os@sha256:abc123",
					},
				},
				{
					Name: "rhel-9-coreos",
					From: &corev1.ObjectReference{
						Kind: "DockerImage",
						Name: "quay.io/openshift/os@sha256:abc123",
					},
				},
				{
					Name: "rhel-9-coreos-extensions",
					From: &corev1.ObjectReference{
						Kind: "DockerImage",
						Name: "quay.io/openshift/ext@sha256:def456",
					},
				},
			},
		},
	}

	inspector := &mockImagesInspector{
		inspectData: map[string]*types.ImageInspectInfo{
			"quay.io/openshift/os@sha256:abc123": {
				Labels: map[string]string{
					"io.openshift.os.streamclass": "rhel-9",
					"ostree.linux":                "present",
				},
			},
			"quay.io/openshift/ext@sha256:def456": {
				Labels: map[string]string{
					"io.openshift.os.streamclass": "rhel-9",
				},
			},
		},
	}

	inspectorFactory := &mockImagesInspectorFactory{inspector: inspector}
	factory := NewDefaultStreamSourceFactory(inspectorFactory)

	ctx := context.Background()
	sysCtx := &types.SystemContext{}

	existing := &v1alpha1.OSImageStream{
		Spec: &v1alpha1.OSImageStreamSpec{
			DefaultStream: "rhel-9",
		},
	}

	result, err := factory.Create(ctx, sysCtx, CreateOptions{
		ReleaseImageStream:    imageStream,
		ExistingOSImageStream: existing,
	})

	require.NoError(t, err)
	assert.Equal(t, "rhel-9", result.Spec.DefaultStream)
	// Ensure the spec is a copy, not the same pointer
	assert.NotSame(t, existing.Spec, result.Spec)
}

func TestDefaultStreamSourceFactory_Create_BootstrapImageStreamOnly(t *testing.T) {
	imageStream := &imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "machine-os-images",
			Namespace: "openshift-machine-config-operator",
		},
		Spec: imagev1.ImageStreamSpec{
			Tags: []imagev1.TagReference{
				{
					Name: "rhel-coreos",
					From: &corev1.ObjectReference{
						Kind: "DockerImage",
						Name: "quay.io/openshift/os@sha256:abc123",
					},
				},
				{
					Name: "rhel-9-coreos",
					From: &corev1.ObjectReference{
						Kind: "DockerImage",
						Name: "quay.io/openshift/os@sha256:abc123",
					},
				},
				{
					Name: "rhel-9-coreos-extensions",
					From: &corev1.ObjectReference{
						Kind: "DockerImage",
						Name: "quay.io/openshift/ext@sha256:def456",
					},
				},
			},
		},
	}

	inspector := &mockImagesInspector{
		inspectData: map[string]*types.ImageInspectInfo{
			"quay.io/openshift/os@sha256:abc123": {
				Labels: map[string]string{
					"io.openshift.os.streamclass": "rhel-9",
					"ostree.linux":                "present",
				},
			},
			"quay.io/openshift/ext@sha256:def456": {
				Labels: map[string]string{
					"io.openshift.os.streamclass": "rhel-9",
				},
			},
		},
	}

	inspectorFactory := &mockImagesInspectorFactory{inspector: inspector}
	factory := NewDefaultStreamSourceFactory(inspectorFactory)

	ctx := context.Background()
	sysCtx := &types.SystemContext{}

	result, err := factory.Create(ctx, sysCtx, CreateOptions{
		ReleaseImageStream: imageStream,
	})

	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "cluster", result.Name)
	assert.NotEmpty(t, result.Status.DefaultStream)
}

func TestDefaultStreamSourceFactory_Create_BootstrapBothSources(t *testing.T) {
	cliImages := &OSImageTuple{
		OSImage:           "quay.io/openshift/os-cli@sha256:111",
		OSExtensionsImage: "quay.io/openshift/ext-cli@sha256:222",
	}

	imageStream := &imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "machine-os-images",
			Namespace: "openshift-machine-config-operator",
		},
		Spec: imagev1.ImageStreamSpec{
			Tags: []imagev1.TagReference{
				{
					Name: "rhel-coreos",
					From: &corev1.ObjectReference{
						Kind: "DockerImage",
						Name: "quay.io/openshift/os-stream@sha256:333",
					},
				},
				{
					Name: "rhel-9-coreos",
					From: &corev1.ObjectReference{
						Kind: "DockerImage",
						Name: "quay.io/openshift/os-stream@sha256:333",
					},
				},
				{
					Name: "rhel-9-coreos-extensions",
					From: &corev1.ObjectReference{
						Kind: "DockerImage",
						Name: "quay.io/openshift/ext-stream@sha256:444",
					},
				},
			},
		},
	}

	inspector := &mockImagesInspector{
		inspectData: map[string]*types.ImageInspectInfo{
			"quay.io/openshift/os-cli@sha256:111": {
				Labels: map[string]string{
					"io.openshift.os.streamclass": "rhel-9",
					"ostree.linux":                "present",
				},
			},
			"quay.io/openshift/ext-cli@sha256:222": {
				Labels: map[string]string{
					"io.openshift.os.streamclass": "rhel-9",
				},
			},
			"quay.io/openshift/os-stream@sha256:333": {
				Labels: map[string]string{
					"io.openshift.os.streamclass": "rhel-9",
					"ostree.linux":                "present",
				},
			},
			"quay.io/openshift/ext-stream@sha256:444": {
				Labels: map[string]string{
					"io.openshift.os.streamclass": "rhel-9",
				},
			},
		},
	}

	inspectorFactory := &mockImagesInspectorFactory{inspector: inspector}
	factory := NewDefaultStreamSourceFactory(inspectorFactory)

	ctx := context.Background()
	sysCtx := &types.SystemContext{}

	result, err := factory.Create(ctx, sysCtx, CreateOptions{
		ReleaseImageStream: imageStream,
		CliImages:          cliImages,
	})

	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "cluster", result.Name)
	assert.NotEmpty(t, result.Status.DefaultStream)
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

func TestDefaultStreamSourceFactory_Create_BootstrapNoSources(t *testing.T) {
	inspector := &mockImagesInspector{
		inspectData: map[string]*types.ImageInspectInfo{},
	}

	inspectorFactory := &mockImagesInspectorFactory{inspector: inspector}
	factory := NewDefaultStreamSourceFactory(inspectorFactory)

	ctx := context.Background()
	sysCtx := &types.SystemContext{}

	result, err := factory.Create(ctx, sysCtx, CreateOptions{})

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "one of ReleaseImageStream or ReleaseImage must be specified")
}
