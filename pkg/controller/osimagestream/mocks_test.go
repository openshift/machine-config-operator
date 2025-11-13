// Assisted-by: Claude
package osimagestream

import (
	"context"
	"errors"

	"github.com/containers/image/v5/types"
	"github.com/openshift/machine-config-operator/pkg/imageutils"
)

type mockImagesInspector struct {
	// Map of image -> ImageInspectInfo for inspection
	inspectData map[string]*types.ImageInspectInfo
	// Map of "image:path" -> file content for fetching files
	fileData map[string][]byte
	// Global error to return for all operations
	err error
	// Legacy: for backwards compatibility with existing tests
	results   []imageutils.BulkInspectResult
	fetchData []byte
	fetchErr  error
}

func (m *mockImagesInspector) Inspect(_ context.Context, images ...string) ([]imageutils.BulkInspectResult, error) {
	if m.err != nil {
		return nil, m.err
	}

	// Legacy path: if results are directly provided, return them
	if m.results != nil {
		return m.results, nil
	}

	// New path: build results from inspectData map
	var results []imageutils.BulkInspectResult
	for _, image := range images {
		result := imageutils.BulkInspectResult{
			Image: image,
		}

		if info, ok := m.inspectData[image]; ok {
			result.InspectInfo = info
		} else {
			result.Error = errors.New("image not found in mock data")
		}

		results = append(results, result)
	}

	return results, nil
}

func (m *mockImagesInspector) FetchImageFile(_ context.Context, image, path string) ([]byte, error) {
	if m.err != nil {
		return nil, m.err
	}

	// Legacy path
	if m.fetchErr != nil {
		return nil, m.fetchErr
	}
	if m.fetchData != nil {
		return m.fetchData, nil
	}

	// New path: look up in fileData map
	key := image + ":" + path
	if data, ok := m.fileData[key]; ok {
		return data, nil
	}

	return nil, errors.New("file not found in mock data for image:path " + key)
}

type mockImageDataExtractor struct {
	data *ImageData
}

func (m *mockImageDataExtractor) GetImageData(_ string, _ map[string]string) *ImageData {
	return m.data
}

type mockImagesInspectorFactory struct {
	inspector ImagesInspector
}

func (f *mockImagesInspectorFactory) ForContext(_ *types.SystemContext) ImagesInspector {
	return f.inspector
}
