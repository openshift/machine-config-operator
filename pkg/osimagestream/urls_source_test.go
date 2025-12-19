// Assisted-by: Claude
package osimagestream

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/containers/image/v5/types"
	"github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/imageutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
)

type mockURLsProvider struct {
	urls OSImageTuple
	err  error
}

func (m *mockURLsProvider) GetUrls() (*OSImageTuple, error) {
	return &m.urls, m.err
}

const (
	dummyOSImageName           = "quay.io/openshift/os:latest"
	dummyOSExtensionsImageName = "quay.io/extensions/os:latest"
)

func createDummyTuple() OSImageTuple {
	return OSImageTuple{
		OSImage:           dummyOSImageName,
		OSExtensionsImage: dummyOSExtensionsImageName,
	}
}

func TestStaticURLProvider_GetUrls(t *testing.T) {
	tests := []struct {
		name     string
		tuple    OSImageTuple
		expected OSImageTuple
	}{
		{
			name:     "returns provided tuple",
			tuple:    createDummyTuple(),
			expected: createDummyTuple(),
		},
		{
			name: "handles empty tuple",
			tuple: OSImageTuple{
				OSImage:           "",
				OSExtensionsImage: "",
			},
			expected: OSImageTuple{
				OSImage:           "",
				OSExtensionsImage: "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := NewStaticURLProvider(tt.tuple)
			result, err := provider.GetUrls()

			require.NoError(t, err)
			assert.Equal(t, &tt.expected, result)
		})
	}
}

func TestOSImagesURLStreamSource_FetchStreams_Success(t *testing.T) {
	ctx := context.Background()

	urlsProvider := &mockURLsProvider{
		urls: createDummyTuple(),
	}
	imagesInspector := &mockImagesInspector{
		results: []imageutils.BulkInspectResult{
			{
				Image: dummyOSImageName,
				InspectInfo: &types.ImageInspectInfo{
					Labels: map[string]string{
						"io.openshift.os.streamclass": "rhel-coreos",
						"ostree.linux":                "present",
					},
				},
				Error: nil,
			},
			{
				Image: dummyOSExtensionsImageName,
				InspectInfo: &types.ImageInspectInfo{
					Labels: map[string]string{
						"io.openshift.os.streamclass": "rhel-coreos",
					},
				},
				Error: nil,
			},
		},
	}

	streams, err := NewOSImagesURLStreamSource(urlsProvider, NewImageStreamExtractor(), imagesInspector).
		FetchStreams(ctx)

	require.NoError(t, err)
	require.NotNil(t, streams)
	require.Len(t, streams, 1)
	assert.Equal(t, "rhel-coreos", streams[0].Name)
	assert.NotEmpty(t, streams[0].OSImage)
	assert.NotEmpty(t, streams[0].OSExtensionsImage)
}

func TestOSImagesURLStreamSource_FetchStreams_URLProviderError(t *testing.T) {
	ctx := context.Background()

	urlsProvider := &mockURLsProvider{
		err: errors.New("failed to get URLs"),
	}

	imagesInspector := &mockImagesInspector{}
	extractor := &mockImageDataExtractor{}
	source := NewOSImagesURLStreamSource(urlsProvider, extractor, imagesInspector)

	streams, err := source.FetchStreams(ctx)

	require.Error(t, err)
	assert.Nil(t, streams)
	assert.Contains(t, err.Error(), "failed to get URLs")
}

func TestOSImagesURLStreamSource_FetchStreams_InspectorError(t *testing.T) {
	ctx := context.Background()

	streams, err := NewOSImagesURLStreamSource(
		&mockURLsProvider{urls: createDummyTuple()},
		&mockImageDataExtractor{},
		&mockImagesInspector{
			err: errors.New("inspection failed"),
		},
	).FetchStreams(ctx)
	require.Error(t, err)
	assert.ErrorContains(t, err, "inspection failed")

	assert.Nil(t, streams)
}

func TestOSImagesURLStreamSource_FetchStreams_MissingImages(t *testing.T) {
	ctx := context.Background()

	urlsProvider := &mockURLsProvider{urls: createDummyTuple()}
	tests := []struct {
		name    string
		results []imageutils.BulkInspectResult
		errMsg  string
	}{
		{
			name:    "no results",
			results: []imageutils.BulkInspectResult{},
			errMsg:  fmt.Sprintf("no inspection result for image %s", dummyOSImageName),
		},
		{
			name: "only OS image",
			results: []imageutils.BulkInspectResult{
				{
					Image: dummyOSImageName,
					InspectInfo: &types.ImageInspectInfo{
						Labels: map[string]string{},
					},
				},
			},
			errMsg: fmt.Sprintf("no inspection result for image %s", dummyOSExtensionsImageName),
		},
		{
			name: "only extensions image",
			results: []imageutils.BulkInspectResult{
				{
					Image: dummyOSExtensionsImageName,
					InspectInfo: &types.ImageInspectInfo{
						Labels: map[string]string{},
					},
				},
			},
			errMsg: fmt.Sprintf("no inspection result for image %s", dummyOSImageName),
		},
		{
			name: "wrong images",
			results: []imageutils.BulkInspectResult{
				{
					Image: "quay.io/wrong/image1:latest",
					InspectInfo: &types.ImageInspectInfo{
						Labels: map[string]string{},
					},
				},
				{
					Image: "quay.io/wrong/image2:latest",
					InspectInfo: &types.ImageInspectInfo{
						Labels: map[string]string{},
					},
				},
			},
			errMsg: fmt.Sprintf("no inspection result for image %s", dummyOSImageName),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			streams, err := NewOSImagesURLStreamSource(
				urlsProvider,
				&mockImageDataExtractor{},
				&mockImagesInspector{
					results: tt.results,
				},
			).FetchStreams(ctx)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errMsg)
			assert.Nil(t, streams)
		})
	}
}

func TestOSImagesURLStreamSource_FetchStreams_InspectionResultError(t *testing.T) {
	ctx := context.Background()

	urlsProvider := &mockURLsProvider{urls: createDummyTuple()}
	tests := []struct {
		name    string
		results []imageutils.BulkInspectResult
		errMsg  string
	}{
		{
			name: "OS image has error",
			results: []imageutils.BulkInspectResult{
				{
					Image: dummyOSImageName,
					Error: errors.New("failed to inspect OS image"),
				},
				{
					Image: dummyOSExtensionsImageName,
					InspectInfo: &types.ImageInspectInfo{
						Labels: map[string]string{},
					},
				},
			},
			errMsg: "error inspecting OS image",
		},
		{
			name: "extensions image has error",
			results: []imageutils.BulkInspectResult{
				{
					Image: dummyOSImageName,
					InspectInfo: &types.ImageInspectInfo{
						Labels: map[string]string{},
					},
				},
				{
					Image: dummyOSExtensionsImageName,
					Error: errors.New("failed to inspect extensions image"),
				},
			},
			errMsg: "error inspecting OS extensions image",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := NewOSImagesURLStreamSource(
				urlsProvider,
				&mockImageDataExtractor{},
				&mockImagesInspector{
					results: tt.results,
				},
			)
			streams, err := source.FetchStreams(ctx)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errMsg)
			assert.Nil(t, streams)
		})
	}
}

// Tests for ConfigMapURLProvider

func TestConfigMapURLProvider_GetUrls_Success(t *testing.T) {
	cm := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      common.MachineConfigOSImageURLConfigMapName,
			Namespace: common.MCONamespace,
		},
		Data: map[string]string{
			"baseOSContainerImage":           dummyOSImageName,
			"baseOSExtensionsContainerImage": dummyOSExtensionsImageName,
			"osImageURL":                     "https://example.com/os",
			"releaseVersion":                 "4.15.0",
		},
	}

	informerFactory := informers.NewSharedInformerFactory(fake.NewSimpleClientset(&cm), 0)
	cmInformer := informerFactory.Core().V1().ConfigMaps()
	require.NoError(t, cmInformer.Informer().GetStore().Add(&cm))

	provider := NewConfigMapURLProviders(cmInformer.Lister())
	result, err := provider.GetUrls()

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, dummyOSImageName, result.OSImage)
	assert.Equal(t, dummyOSExtensionsImageName, result.OSExtensionsImage)
}

func TestConfigMapURLProvider_GetUrls_ConfigMapNotFound(t *testing.T) {
	// Create a fake client with no ConfigMap
	client := fake.NewSimpleClientset()
	informerFactory := informers.NewSharedInformerFactory(client, 0)
	cmInformer := informerFactory.Core().V1().ConfigMaps()

	provider := NewConfigMapURLProviders(cmInformer.Lister())
	result, err := provider.GetUrls()

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "could not get ConfigMap")
}

func TestConfigMapURLProvider_GetUrls_InvalidConfigMap(t *testing.T) {
	tests := []struct {
		name      string
		configMap *corev1.ConfigMap
	}{
		{
			name: "missing baseOSContainerImage",
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      common.MachineConfigOSImageURLConfigMapName,
					Namespace: common.MCONamespace,
				},
				Data: map[string]string{
					"baseOSExtensionsContainerImage": "quay.io/openshift/rhcos-extensions:latest",
					"osImageURL":                     "https://example.com/os",
					"releaseVersion":                 "4.15.0",
				},
			},
		},
		{
			name: "missing baseOSExtensionsContainerImage",
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      common.MachineConfigOSImageURLConfigMapName,
					Namespace: common.MCONamespace,
				},
				Data: map[string]string{
					"baseOSContainerImage": "quay.io/openshift/rhcos:latest",
					"osImageURL":           "https://example.com/os",
					"releaseVersion":       "4.15.0",
				},
			},
		},
		{
			name: "empty data",
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      common.MachineConfigOSImageURLConfigMapName,
					Namespace: common.MCONamespace,
				},
				Data: map[string]string{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset(tt.configMap)
			informerFactory := informers.NewSharedInformerFactory(client, 0)
			cmInformer := informerFactory.Core().V1().ConfigMaps()

			// Add the configmap to the informer's store
			err := cmInformer.Informer().GetStore().Add(tt.configMap)
			require.NoError(t, err)

			provider := NewConfigMapURLProviders(cmInformer.Lister())
			result, err := provider.GetUrls()

			require.Error(t, err)
			assert.Nil(t, result)
		})
	}
}
