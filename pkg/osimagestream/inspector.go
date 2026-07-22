package osimagestream

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/containers/common/pkg/retry"
	"github.com/openshift/machine-config-operator/pkg/imageutils"
	"k8s.io/klog/v2"
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
// It lazily creates a SysContext on each operation via the provided factory.
type ImagesInspectorImpl struct {
	bulkInspector *imageutils.BulkInspector
	sysCtxFactory imageutils.SysContextFactory
}

// NewImagesInspector creates a new ImagesInspector with the given SysContext factory.
func NewImagesInspector(sysCtxFactory imageutils.SysContextFactory) *ImagesInspectorImpl {
	return &ImagesInspectorImpl{
		sysCtxFactory: sysCtxFactory,
		bulkInspector: imageutils.NewBulkInspector(&imageutils.BulkInspectorOptions{
			RetryOpts: newImageInspectionRetryOptions(),
			Count:     5,
			FailOnErr: false,
		}),
	}
}

// Inspect retrieves metadata for the specified container images.
func (i *ImagesInspectorImpl) Inspect(ctx context.Context, image ...string) ([]imageutils.BulkInspectResult, error) {
	sysCtx, err := i.sysCtxFactory()
	if err != nil {
		return nil, fmt.Errorf("creating system context for image inspection: %w", err)
	}
	defer func() {
		if cleanupErr := sysCtx.Cleanup(); cleanupErr != nil {
			klog.Warningf("could not clean up system context: %v", cleanupErr)
		}
	}()
	return i.bulkInspector.Inspect(ctx, sysCtx.SysContext, image...)
}

// FetchImageFile extracts and returns the contents of a file from the container image.
func (i *ImagesInspectorImpl) FetchImageFile(ctx context.Context, image, path string) ([]byte, error) {
	sysCtx, err := i.sysCtxFactory()
	if err != nil {
		return nil, fmt.Errorf("creating system context for image file fetch: %w", err)
	}
	defer func() {
		if cleanupErr := sysCtx.Cleanup(); cleanupErr != nil {
			klog.Warningf("could not clean up system context: %v", cleanupErr)
		}
	}()
	targetHeaderPath := strings.TrimLeft(path, "./")
	return imageutils.ReadImageFileContent(ctx, sysCtx.SysContext, image, func(header *tar.Header) bool {
		return targetHeaderPath == strings.TrimLeft(header.Name, "./")
	}, newImageInspectionRetryOptions())
}

// ImagesInspectorFactory creates ImagesInspector instances.
type ImagesInspectorFactory interface {
	ForContext(sysCtxFactory imageutils.SysContextFactory) ImagesInspector
}

// DefaultImagesInspectorFactory is the production implementation of ImagesInspectorFactory.
type DefaultImagesInspectorFactory struct{}

// ForContext creates an ImagesInspector with the given SysContext factory.
func (f *DefaultImagesInspectorFactory) ForContext(sysCtxFactory imageutils.SysContextFactory) ImagesInspector {
	return NewImagesInspector(sysCtxFactory)
}
