package osimagestream

import (
	"archive/tar"
	"context"
	"errors"
	"net"
	"strings"

	"github.com/containers/common/pkg/retry"
	"github.com/containers/image/v5/types"
	"github.com/openshift/machine-config-operator/pkg/imageutils"
)

// isNetworkErrorRetryable checks if an error is a network-related error that should be retried.
// This handles DNS and timeout errors that may occur during cluster bootstrap.
func isNetworkErrorRetryable(err error) bool {
	if err == nil {
		return false
	}

	// Check for net.DNSError (DNS lookup failures)
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}

	// Check for timeout errors (net.Error with Timeout() == true)
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}

// newImageInspectionRetryOptions creates retry options suitable for image inspection operations.
// It includes special handling for DNS and network errors that may occur during cluster bootstrap.
func newImageInspectionRetryOptions() *retry.Options {
	return &retry.Options{
		MaxRetry: 50,
		IsErrorRetryable: func(err error) bool {
			// Use the default retry logic first
			if retry.IsErrorRetryable(err) {
				return true
			}
			// Additionally check for network errors that may be wrapped in ways
			// that don't match the default retry logic's type assertions
			return isNetworkErrorRetryable(err)
		},
	}
}

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
			RetryOpts: newImageInspectionRetryOptions(),
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
	}, newImageInspectionRetryOptions())
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
