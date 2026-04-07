// Assisted-by: Claude
package osimagestream

import (
	"context"
	"fmt"
	"testing"

	"github.com/containers/image/v5/types"
	imagev1 "github.com/openshift/api/image/v1"
	"github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sversion "k8s.io/apimachinery/pkg/util/version"
	corelisterv1 "k8s.io/client-go/listers/core/v1"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
)

// testImageDef defines an OS image pair for test setup.
type testImageDef struct {
	streamClass string
	osImage     string
	extImage    string
}

// newTestImageStream builds an ImageStream from a version name and image definitions.
// Each def produces the standard tagged pair (e.g. rhel-9-coreos, rhel-9-coreos-extensions).
func newTestImageStream(version string, defs ...testImageDef) *imagev1.ImageStream {
	var tags []imagev1.TagReference
	for _, d := range defs {
		tags = append(tags,
			imagev1.TagReference{
				Name: d.streamClass + "-coreos",
				From: &corev1.ObjectReference{Kind: "DockerImage", Name: d.osImage},
			},
			imagev1.TagReference{
				Name: d.streamClass + "-coreos-extensions",
				From: &corev1.ObjectReference{Kind: "DockerImage", Name: d.extImage},
			},
		)
	}
	return &imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{Name: version},
		Spec:       imagev1.ImageStreamSpec{Tags: tags},
	}
}

// newTestInspectData builds mock image inspection data from image definitions.
func newTestInspectData(defs ...testImageDef) map[string]*types.ImageInspectInfo {
	data := make(map[string]*types.ImageInspectInfo)
	for _, d := range defs {
		data[d.osImage] = &types.ImageInspectInfo{
			Labels: map[string]string{
				testCoreOSLabelStreamClass: d.streamClass,
				testCoreOSLabelBootc:       testCoreOSLabelBootcValueTrue,
			},
		}
		data[d.extImage] = &types.ImageInspectInfo{
			Labels: map[string]string{
				testCoreOSLabelStreamClass: d.streamClass,
				testCoreOSLabelExtension:   testCoreOSLabelExtensionValue,
			},
		}
	}
	return data
}

// newTestFactory creates a factory with the given inspect data and optional file data for network release image fetching.
func newTestFactory(inspectData map[string]*types.ImageInspectInfo, fileData map[string][]byte) *DefaultStreamSourceFactory {
	inspector := &mockImagesInspector{inspectData: inspectData, fileData: fileData}
	return NewDefaultStreamSourceFactory(&mockImagesInspectorFactory{inspector: inspector})
}

// newTestConfigMapLister creates a ConfigMap lister with an osimageurl ConfigMap containing the given image refs.
func newTestConfigMapLister(t *testing.T, osImage, extImage string) corelisterv1.ConfigMapLister {
	t.Helper()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "machine-config-osimageurl",
			Namespace: "openshift-machine-config-operator",
		},
		Data: map[string]string{
			"baseOSContainerImage":           osImage,
			"baseOSExtensionsContainerImage": extImage,
			"osImageURL":                     "",
			"releaseVersion":                 "4.21.0",
		},
	}
	fakeClient := fake.NewSimpleClientset(cm)
	factory := informers.NewSharedInformerFactory(fakeClient, 0)
	cmInformer := factory.Core().V1().ConfigMaps()
	require.NoError(t, cmInformer.Informer().GetIndexer().Add(cm))
	return cmInformer.Lister()
}

// newTestEmptyConfigMapLister creates a ConfigMap lister with no ConfigMaps.
func newTestEmptyConfigMapLister() corelisterv1.ConfigMapLister {
	fakeClient := fake.NewSimpleClientset()
	factory := informers.NewSharedInformerFactory(fakeClient, 0)
	return factory.Core().V1().ConfigMaps().Lister()
}

// newTestImageStreamManifest generates a YAML ImageStream manifest for use as release image file data.
func newTestImageStreamManifest(version string, defs ...testImageDef) []byte {
	manifest := fmt.Sprintf(`apiVersion: image.openshift.io/v1
kind: ImageStream
metadata:
  name: %s
spec:
  tags:`, version)
	for _, d := range defs {
		manifest += fmt.Sprintf(`
  - name: %s-coreos
    from:
      kind: DockerImage
      name: %s
  - name: %s-coreos-extensions
    from:
      kind: DockerImage
      name: %s`, d.streamClass, d.osImage, d.streamClass, d.extImage)
	}
	return []byte(manifest)
}

var (
	// testInstallVersion simulates a 4.x cluster for tests that need the builtin default to be rhel-9.
	testInstallVersion = k8sversion.MustParseGeneric("4.22.0")

	testStreamDefRHEL9 = testImageDef{
		streamClass: "rhel-9",
		osImage:     "quay.io/openshift/os-9@sha256:aaa111",
		extImage:    "quay.io/openshift/ext-9@sha256:bbb222",
	}
	testStreamDefRHEL10 = testImageDef{
		streamClass: "rhel-10",
		osImage:     "quay.io/openshift/os-10@sha256:ccc333",
		extImage:    "quay.io/openshift/ext-10@sha256:ddd444",
	}
	// Alternate image refs for ConfigMap sources (to distinguish from ImageStream sources).
	testStreamDefRHEL9ConfigMap = testImageDef{
		streamClass: "rhel-9",
		osImage:     "quay.io/openshift/os-cm@sha256:111",
		extImage:    "quay.io/openshift/ext-cm@sha256:222",
	}
)

func TestDefaultStreamSourceFactory_Create_RuntimeBothSources(t *testing.T) {
	cmLister := newTestConfigMapLister(t, testStreamDefRHEL9ConfigMap.osImage, testStreamDefRHEL9ConfigMap.extImage)

	// Network release image contains a different rhel-9 stream
	rhel9Net := testImageDef{streamClass: "rhel-9", osImage: "quay.io/openshift/os-net@sha256:333", extImage: "quay.io/openshift/ext-net@sha256:444"}
	manifest := newTestImageStreamManifest("4.22.0", rhel9Net)

	inspectData := newTestInspectData(testStreamDefRHEL9ConfigMap, rhel9Net)
	factory := newTestFactory(inspectData, map[string][]byte{
		"quay.io/openshift/release:4.16:/release-manifests/image-references": manifest,
	})

	result, err := factory.Create(context.Background(), &types.SystemContext{}, CreateOptions{
		ReleaseImage:    "quay.io/openshift/release:4.16",
		ConfigMapLister: cmLister,
		InstallVersion:  testInstallVersion,
	})

	require.NoError(t, err)
	assert.Equal(t, "cluster", result.Name)
	assert.Equal(t, "rhel-9", result.Status.DefaultStream)
	assert.Len(t, result.Status.AvailableStreams, 1)
}

func TestDefaultStreamSourceFactory_Create_RuntimeConfigMapOnly(t *testing.T) {
	cmLister := newTestConfigMapLister(t, testStreamDefRHEL9.osImage, testStreamDefRHEL9.extImage)

	manifest := newTestImageStreamManifest("4.22.0", testStreamDefRHEL9)
	inspectData := newTestInspectData(testStreamDefRHEL9)
	factory := newTestFactory(inspectData, map[string][]byte{
		"quay.io/openshift/release:4.16:/release-manifests/image-references": manifest,
	})

	result, err := factory.Create(context.Background(), &types.SystemContext{}, CreateOptions{
		ReleaseImage:    "quay.io/openshift/release:4.16",
		ConfigMapLister: cmLister,
		InstallVersion:  testInstallVersion,
		ExistingOSImageStream: &v1alpha1.OSImageStream{
			Spec: &v1alpha1.OSImageStreamSpec{DefaultStream: "rhel-9"},
		},
	})

	require.NoError(t, err)
	assert.Equal(t, "cluster", result.Name)
	assert.Equal(t, "rhel-9", result.Status.DefaultStream)
	assert.NotEmpty(t, result.Status.AvailableStreams)
}

func TestDefaultStreamSourceFactory_Create_RuntimeBothSourcesFail(t *testing.T) {
	cmLister := newTestEmptyConfigMapLister()
	factory := newTestFactory(map[string]*types.ImageInspectInfo{}, nil)

	result, err := factory.Create(context.Background(), &types.SystemContext{}, CreateOptions{
		ReleaseImage:    "quay.io/openshift/release:4.16",
		ConfigMapLister: cmLister,
	})

	require.Error(t, err)
	assert.Nil(t, result)
	assert.ErrorIs(t, err, ErrorNoOSImageStreamAvailable)
}

func TestDefaultStreamSourceFactory_Create_RuntimeMultipleStreams(t *testing.T) {
	cmLister := newTestConfigMapLister(t, testStreamDefRHEL9.osImage, testStreamDefRHEL9.extImage)

	manifest := newTestImageStreamManifest("4.22.0", testStreamDefRHEL9, testStreamDefRHEL10)
	inspectData := newTestInspectData(testStreamDefRHEL9, testStreamDefRHEL10)
	factory := newTestFactory(inspectData, map[string][]byte{
		"quay.io/openshift/release:4.16:/release-manifests/image-references": manifest,
	})

	result, err := factory.Create(context.Background(), &types.SystemContext{}, CreateOptions{
		ReleaseImage:    "quay.io/openshift/release:4.16",
		ConfigMapLister: cmLister,
		InstallVersion:  testInstallVersion,
	})

	require.NoError(t, err)
	assert.Equal(t, "cluster", result.Name)
	assert.Equal(t, "rhel-9", result.Status.DefaultStream)
	require.Len(t, result.Status.AvailableStreams, 2)
	assert.ElementsMatch(t, []string{"rhel-9", "rhel-10"}, GetStreamSetsNames(result.Status.AvailableStreams))
}

func TestDefaultStreamSourceFactory_Create_BootstrapMultipleStreams(t *testing.T) {
	imageStream := newTestImageStream("4.22.0", testStreamDefRHEL9, testStreamDefRHEL10)
	// Also add the legacy rhel-coreos alias pointing to the rhel-9 image
	imageStream.Spec.Tags = append(imageStream.Spec.Tags, imagev1.TagReference{
		Name: "rhel-coreos",
		From: &corev1.ObjectReference{Kind: "DockerImage", Name: testStreamDefRHEL9.osImage},
	})

	factory := newTestFactory(newTestInspectData(testStreamDefRHEL9, testStreamDefRHEL10), nil)

	result, err := factory.Create(context.Background(), &types.SystemContext{}, CreateOptions{
		ReleaseImageStream: imageStream,
		InstallVersion:     testInstallVersion,
	})

	require.NoError(t, err)
	assert.Equal(t, "cluster", result.Name)
	assert.NotEmpty(t, result.Status.DefaultStream)
	require.Len(t, result.Status.AvailableStreams, 2)
	assert.ElementsMatch(t, []string{"rhel-9", "rhel-10"}, GetStreamSetsNames(result.Status.AvailableStreams))
}

func TestDefaultStreamSourceFactory_Create_UserDefaultOverride(t *testing.T) {
	imageStream := newTestImageStream("4.22.0", testStreamDefRHEL9, testStreamDefRHEL10)
	// Add legacy alias
	imageStream.Spec.Tags = append(imageStream.Spec.Tags, imagev1.TagReference{
		Name: "rhel-coreos",
		From: &corev1.ObjectReference{Kind: "DockerImage", Name: testStreamDefRHEL9.osImage},
	})
	factory := newTestFactory(newTestInspectData(testStreamDefRHEL9, testStreamDefRHEL10), nil)

	result, err := factory.Create(context.Background(), &types.SystemContext{}, CreateOptions{
		ReleaseImageStream: imageStream,
		InstallVersion:     testInstallVersion,
		ExistingOSImageStream: &v1alpha1.OSImageStream{
			Spec: &v1alpha1.OSImageStreamSpec{DefaultStream: "rhel-10"},
		},
	})

	require.NoError(t, err)
	assert.Equal(t, "rhel-10", result.Status.DefaultStream, "status should reflect the user override")
	assert.Equal(t, "rhel-10", result.Spec.DefaultStream, "spec should carry the user override")
}

func TestDefaultStreamSourceFactory_Create_BootstrapCliImagesOnly(t *testing.T) {
	imageStream := newTestImageStream("4.22.0", testStreamDefRHEL9)
	// Add legacy alias
	imageStream.Spec.Tags = append(imageStream.Spec.Tags, imagev1.TagReference{
		Name: "rhel-coreos",
		From: &corev1.ObjectReference{Kind: "DockerImage", Name: testStreamDefRHEL9.osImage},
	})
	factory := newTestFactory(newTestInspectData(testStreamDefRHEL9), nil)

	result, err := factory.Create(context.Background(), &types.SystemContext{}, CreateOptions{
		CliImages:          &OSImageTuple{OSImage: testStreamDefRHEL9.osImage, OSExtensionsImage: testStreamDefRHEL9.extImage},
		ReleaseImageStream: imageStream,
		InstallVersion:     testInstallVersion,
		ExistingOSImageStream: &v1alpha1.OSImageStream{
			Spec: &v1alpha1.OSImageStreamSpec{DefaultStream: "rhel-9"},
		},
	})

	require.NoError(t, err)
	assert.Equal(t, "cluster", result.Name)
	assert.Equal(t, "rhel-9", result.Status.DefaultStream)
	require.NotEmpty(t, result.Status.AvailableStreams)
	assert.Equal(t, "rhel-9", result.Spec.DefaultStream)
}

func TestDefaultStreamSourceFactory_Create_PreservesExistingSpec(t *testing.T) {
	imageStream := newTestImageStream("4.22.0", testStreamDefRHEL9)
	// Add legacy alias
	imageStream.Spec.Tags = append(imageStream.Spec.Tags, imagev1.TagReference{
		Name: "rhel-coreos",
		From: &corev1.ObjectReference{Kind: "DockerImage", Name: testStreamDefRHEL9.osImage},
	})
	factory := newTestFactory(newTestInspectData(testStreamDefRHEL9), nil)

	existing := &v1alpha1.OSImageStream{
		Spec: &v1alpha1.OSImageStreamSpec{DefaultStream: "rhel-9"},
	}

	result, err := factory.Create(context.Background(), &types.SystemContext{}, CreateOptions{
		ReleaseImageStream:    imageStream,
		InstallVersion:        testInstallVersion,
		ExistingOSImageStream: existing,
	})

	require.NoError(t, err)
	assert.Equal(t, "rhel-9", result.Spec.DefaultStream)
	assert.NotSame(t, existing.Spec, result.Spec)
}

func TestDefaultStreamSourceFactory_Create_BootstrapImageStreamOnly(t *testing.T) {
	imageStream := newTestImageStream("4.22.0", testStreamDefRHEL9)
	// Add legacy alias
	imageStream.Spec.Tags = append(imageStream.Spec.Tags, imagev1.TagReference{
		Name: "rhel-coreos",
		From: &corev1.ObjectReference{Kind: "DockerImage", Name: testStreamDefRHEL9.osImage},
	})
	factory := newTestFactory(newTestInspectData(testStreamDefRHEL9), nil)

	result, err := factory.Create(context.Background(), &types.SystemContext{}, CreateOptions{
		ReleaseImageStream: imageStream,
		InstallVersion:     testInstallVersion,
	})

	require.NoError(t, err)
	assert.Equal(t, "cluster", result.Name)
	assert.NotEmpty(t, result.Status.DefaultStream)
}

func TestDefaultStreamSourceFactory_Create_BootstrapBothSources(t *testing.T) {
	rhel9CLI := testImageDef{streamClass: "rhel-9", osImage: "quay.io/openshift/os-cli@sha256:111", extImage: "quay.io/openshift/ext-cli@sha256:222"}
	rhel9Stream := testImageDef{streamClass: "rhel-9", osImage: "quay.io/openshift/os-stream@sha256:333", extImage: "quay.io/openshift/ext-stream@sha256:444"}

	imageStream := newTestImageStream("4.22.0", rhel9Stream)
	// Add legacy alias
	imageStream.Spec.Tags = append(imageStream.Spec.Tags, imagev1.TagReference{
		Name: "rhel-coreos",
		From: &corev1.ObjectReference{Kind: "DockerImage", Name: rhel9Stream.osImage},
	})
	factory := newTestFactory(newTestInspectData(rhel9CLI, rhel9Stream), nil)

	result, err := factory.Create(context.Background(), &types.SystemContext{}, CreateOptions{
		ReleaseImageStream: imageStream,
		CliImages:          &OSImageTuple{OSImage: rhel9CLI.osImage, OSExtensionsImage: rhel9CLI.extImage},
		InstallVersion:     testInstallVersion,
	})

	require.NoError(t, err)
	assert.Equal(t, "cluster", result.Name)
	assert.NotEmpty(t, result.Status.DefaultStream)
}

func TestDefaultStreamSourceFactory_Create_InstallVersionTakesPriority(t *testing.T) {
	// ImageStream is version 5.0.0, but InstallVersion is 4.22.0
	// The builtin default should be "rhel-9" because InstallVersion takes priority
	imageStream := newTestImageStream("5.0.0", testStreamDefRHEL9, testStreamDefRHEL10)
	factory := newTestFactory(newTestInspectData(testStreamDefRHEL9, testStreamDefRHEL10), nil)

	result, err := factory.Create(context.Background(), &types.SystemContext{}, CreateOptions{
		ReleaseImageStream: imageStream,
		InstallVersion:     k8sversion.MustParseGeneric("4.22.0"),
	})

	require.NoError(t, err)
	assert.Equal(t, "rhel-9", result.Status.DefaultStream)
}

func TestDefaultStreamSourceFactory_Create_DuplicateStreamsLastSourceWins(t *testing.T) {
	rhel9Net := testImageDef{streamClass: "rhel-9", osImage: "quay.io/openshift/os-net@sha256:333", extImage: "quay.io/openshift/ext-net@sha256:444"}
	cmLister := newTestConfigMapLister(t, testStreamDefRHEL9ConfigMap.osImage, testStreamDefRHEL9ConfigMap.extImage)

	manifest := newTestImageStreamManifest("4.22.0", rhel9Net)
	inspectData := newTestInspectData(testStreamDefRHEL9ConfigMap, rhel9Net)
	factory := newTestFactory(inspectData, map[string][]byte{
		"quay.io/openshift/release:4.16:/release-manifests/image-references": manifest,
	})

	result, err := factory.Create(context.Background(), &types.SystemContext{}, CreateOptions{
		ReleaseImage:    "quay.io/openshift/release:4.16",
		ConfigMapLister: cmLister,
		InstallVersion:  testInstallVersion,
	})

	require.NoError(t, err)
	require.Len(t, result.Status.AvailableStreams, 1)
	// The ImageStream source is appended last, so its images should override the ConfigMap ones
	assert.Equal(t, v1alpha1.ImageDigestFormat(rhel9Net.osImage), result.Status.AvailableStreams[0].OSImage)
	assert.Equal(t, v1alpha1.ImageDigestFormat(rhel9Net.extImage), result.Status.AvailableStreams[0].OSExtensionsImage)
}

func TestDefaultStreamSourceFactory_Create_PartialSourceFailure(t *testing.T) {
	// ConfigMap source will fail (no ConfigMap in the lister), but ImageStream source succeeds
	imageStream := newTestImageStream("4.22.0", testStreamDefRHEL9)
	// Add legacy alias
	imageStream.Spec.Tags = append(imageStream.Spec.Tags, imagev1.TagReference{
		Name: "rhel-coreos",
		From: &corev1.ObjectReference{Kind: "DockerImage", Name: testStreamDefRHEL9.osImage},
	})
	factory := newTestFactory(newTestInspectData(testStreamDefRHEL9), nil)

	result, err := factory.Create(context.Background(), &types.SystemContext{}, CreateOptions{
		ReleaseImageStream: imageStream,
		ConfigMapLister:    newTestEmptyConfigMapLister(),
		InstallVersion:     testInstallVersion,
	})

	require.NoError(t, err)
	require.Len(t, result.Status.AvailableStreams, 1)
	assert.Equal(t, "rhel-9", result.Status.DefaultStream)
}

func TestDefaultStreamSourceFactory_Create_InvalidUserDefault(t *testing.T) {
	imageStream := newTestImageStream("4.22.0", testStreamDefRHEL9)
	// Add legacy alias
	imageStream.Spec.Tags = append(imageStream.Spec.Tags, imagev1.TagReference{
		Name: "rhel-coreos",
		From: &corev1.ObjectReference{Kind: "DockerImage", Name: testStreamDefRHEL9.osImage},
	})
	factory := newTestFactory(newTestInspectData(testStreamDefRHEL9), nil)

	_, err := factory.Create(context.Background(), &types.SystemContext{}, CreateOptions{
		ReleaseImageStream: imageStream,
		InstallVersion:     testInstallVersion,
		ExistingOSImageStream: &v1alpha1.OSImageStream{
			Spec: &v1alpha1.OSImageStreamSpec{DefaultStream: "rhel-99"},
		},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not find the requested rhel-99 default stream")
}

func TestDefaultStreamSourceFactory_Create_StatusDefaultPreserved(t *testing.T) {
	imageStream := newTestImageStream("4.22.0", testStreamDefRHEL9, testStreamDefRHEL10)
	factory := newTestFactory(newTestInspectData(testStreamDefRHEL9, testStreamDefRHEL10), nil)

	// Existing CR has rhel-10 as status default but no spec override.
	// The builtin default for 4.x is rhel-9, but the existing status should be preserved.
	result, err := factory.Create(context.Background(), &types.SystemContext{}, CreateOptions{
		ReleaseImageStream: imageStream,
		InstallVersion:     testInstallVersion,
		ExistingOSImageStream: &v1alpha1.OSImageStream{
			Status: v1alpha1.OSImageStreamStatus{DefaultStream: "rhel-10"},
		},
	})

	require.NoError(t, err)
	assert.Equal(t, "rhel-10", result.Status.DefaultStream, "existing status default should be preserved when no spec override is set")
}

func TestDefaultStreamSourceFactory_Create_FallsBackToBuildVersion(t *testing.T) {
	// No InstallVersion provided, the code should fall back to version.ReleaseVersion.
	imageStream := newTestImageStream("4.22.0", testStreamDefRHEL9, testStreamDefRHEL10)
	factory := newTestFactory(newTestInspectData(testStreamDefRHEL9, testStreamDefRHEL10), nil)

	originalReleaseVersion := version.ReleaseVersion
	version.ReleaseVersion = "4.18.0"
	defer func() { version.ReleaseVersion = originalReleaseVersion }()

	result, err := factory.Create(context.Background(), &types.SystemContext{}, CreateOptions{
		ReleaseImageStream: imageStream,
	})

	require.NoError(t, err)
	assert.Equal(t, "rhel-9", result.Status.DefaultStream, "should fall back to build version 4.18.0 and pick rhel-9")
}

func TestDefaultStreamSourceFactory_Create_BootstrapNoSources(t *testing.T) {
	factory := newTestFactory(map[string]*types.ImageInspectInfo{}, nil)

	result, err := factory.Create(context.Background(), &types.SystemContext{}, CreateOptions{})

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "one of ReleaseImageStream or ReleaseImage must be specified")
}
