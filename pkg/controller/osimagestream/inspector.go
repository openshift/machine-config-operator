package osimagestream

import (
	"archive/tar"
	"context"
	"strings"

	"github.com/containers/common/pkg/retry"
	"github.com/containers/image/v5/types"
	"github.com/openshift/machine-config-operator/pkg/imageutils"
)

// ImagesInspector provides methods for inspecting container images and extracting their contents.
type ImagesInspector interface {
	// Inspect retrieves metadata for one or more container images.
	Inspect(ctx context.Context, image ...string) ([]imageutils.BulkInspectResult, error)
	// FetchImageFile extracts and returns the contents of a file from a container image.
	FetchImageFile(ctx context.Context, image, path string) ([]byte, error)
}

// ImagesInspectorImpl is the default implementation of ImagesInspector.
type ImagesInspectorImpl struct {
	bulkInspector *imageutils.BulkInspector
	sysCtx        *types.SystemContext
}

// NewImagesInspector creates a new ImagesInspector with the given system context.
func NewImagesInspector(sysCtx *types.SystemContext) *ImagesInspectorImpl {
	return &ImagesInspectorImpl{
		sysCtx: sysCtx,
		bulkInspector: imageutils.NewBulkInspector(&imageutils.BulkInspectorOptions{
			RetryOpts: &retry.RetryOptions{
				MaxRetry: 2,
			},
			Count:     5,
			FailOnErr: false,
		}),
	}
}

// Inspect retrieves metadata for the specified container images.
func (i *ImagesInspectorImpl) Inspect(ctx context.Context, image ...string) ([]imageutils.BulkInspectResult, error) {
	return i.bulkInspector.Inspect(ctx, i.sysCtx, image...)
}

// FetchImageFile extracts and returns the contents of a file from the container image.
func (i *ImagesInspectorImpl) FetchImageFile(ctx context.Context, image, path string) ([]byte, error) {
	targetHeaderPath := strings.TrimLeft(path, "./")
	return imageutils.ReadImageFileContent(ctx, i.sysCtx, image, func(header *tar.Header) bool {
		return targetHeaderPath == strings.TrimLeft(header.Name, "./")
	})
}

// ImagesInspectorFactory creates ImagesInspector instances for different system contexts.
type ImagesInspectorFactory interface {
	// ForContext creates an ImagesInspector for the given system context.
	ForContext(sysCtx *types.SystemContext) ImagesInspector
}

// DefaultImagesInspectorFactory is the production implementation of ImagesInspectorFactory.
type DefaultImagesInspectorFactory struct{}

// ForContext creates an ImagesInspector for the given system context.
func (f *DefaultImagesInspectorFactory) ForContext(sysCtx *types.SystemContext) ImagesInspector {
	return NewImagesInspector(sysCtx)
}
