package osimagestream

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/containers/common/pkg/retry"
	"github.com/containers/image/v5/types"
	apicfgv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	apioperatorsv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/imageutils"
	corev1 "k8s.io/api/core/v1"
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

// InspectStreamClass inspects a container image and returns its OS stream class
// (e.g. "rhel-9", "rhel-10") from the image's labels. Returns ("", nil) when
// the image has no stream class label.
// TODO(OCP 5.3): Remove when runc is removed.
func InspectStreamClass(ctx context.Context, sysCtx *types.SystemContext, imageURL string) (string, error) {
	inspector := NewImagesInspector(sysCtx)
	results, err := inspector.Inspect(ctx, imageURL)
	if err != nil {
		return "", fmt.Errorf("failed to inspect OS image: %w", err)
	}
	if len(results) == 0 {
		return "", fmt.Errorf("no inspection result for OS image")
	}
	if results[0].Error != nil {
		return "", fmt.Errorf("failed to inspect OS image: %w", results[0].Error)
	}
	if results[0].InspectInfo == nil {
		return "", nil
	}

	extractor := NewImageStreamExtractor()
	imageData := extractor.GetImageData(imageURL, results[0].InspectInfo.Labels)
	if imageData == nil {
		return "", nil
	}
	return imageData.Stream, nil
}

// InspectStreamClassWithMirrors builds an image system context with registry mirror
// rules applied, then inspects the container image to determine its OS stream class.
// TODO(OCP 5.3): Remove when runc is removed.
func InspectStreamClassWithMirrors(
	ctx context.Context,
	secret *corev1.Secret,
	cc *mcfgv1.ControllerConfig,
	imgCfg *apicfgv1.Image,
	icspRules []*apioperatorsv1alpha1.ImageContentSourcePolicy,
	idmsRules []*apicfgv1.ImageDigestMirrorSet,
	itmsRules []*apicfgv1.ImageTagMirrorSet,
	imageURL string,
) (string, error) {
	builder := imageutils.NewSysContextBuilder().
		WithSecret(secret).
		WithControllerConfig(cc)

	registriesConfig, err := imageutils.GenerateRegistriesConfig(imgCfg, icspRules, idmsRules, itmsRules)
	if err != nil {
		return "", fmt.Errorf("failed to generate registries config: %w", err)
	}
	if registriesConfig != nil {
		builder.WithRegistriesConfig(registriesConfig)
	}

	sysCtx, err := builder.Build()
	if err != nil {
		return "", fmt.Errorf("failed to build image system context: %w", err)
	}
	defer sysCtx.Cleanup()

	return InspectStreamClass(ctx, sysCtx.SysContext, imageURL)
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
