package imagepruner

import (
	"context"
	"fmt"
	"strings"

	"github.com/containers/common/pkg/retry"
	"github.com/containers/image/v5/docker"
	"github.com/containers/image/v5/image"
	"github.com/containers/image/v5/manifest"
	"github.com/containers/image/v5/types"
	digest "github.com/opencontainers/go-digest"
)

const (
	// cmdRetriesCount defines the number of retries to perform for image operations.
	cmdRetriesCount = 2
)

// ImageInspector defines the interface for inspecting a container image.
type ImageInspector interface {
	// ImageInspect inspects the specified image using the provided system context.
	// It returns image inspection information, its digest, or an error.
	ImageInspect(context.Context, *types.SystemContext, string) (*types.ImageInspectInfo, *digest.Digest, error)
}

// ImageDeleter defines the interface for deleting a container image.
type ImageDeleter interface {
	// DeleteImage deletes the specified image using the provided system context.
	// It returns an error if the deletion fails.
	DeleteImage(context.Context, *types.SystemContext, string) error
}

// ImageInspectorDeleter defines an interface that can both inspect and delete a container image,
// combining the functionalities of ImageInspector and ImageDeleter.
type ImageInspectorDeleter interface {
	ImageInspector
	ImageDeleter
}

// imageInspectorImpl is the real image inspector implementation which is wired up with
// the appropriate functions from the containers/image library.
type imageInspectorImpl struct{}

// NewImageInspectorDeleter constructs and returns a new real ImageInspectorDeleter.
func NewImageInspectorDeleter() ImageInspectorDeleter {
	return &imageInspectorImpl{}
}

// ImageInspect uses the provided system context to inspect the provided image pullspec.
func (i *imageInspectorImpl) ImageInspect(ctx context.Context, sysCtx *types.SystemContext, image string) (*types.ImageInspectInfo, *digest.Digest, error) {
	return imageInspect(ctx, sysCtx, image)
}

// DeleteImage uses the provided system context to delete the provided image pullspec.
func (i *imageInspectorImpl) DeleteImage(ctx context.Context, sysCtx *types.SystemContext, image string) error {
	return deleteImage(ctx, sysCtx, image)
}

// parseImageName parses the image name string into an ImageReference,
// handling various prefix formats like "docker://" and ensuring a standard format.
func parseImageName(imgName string) (types.ImageReference, error) {
	if strings.Contains(imgName, "//") && !strings.HasPrefix(imgName, "docker://") {
		return nil, fmt.Errorf("unknown transport for pullspec %s", imgName)
	}

	if strings.HasPrefix(imgName, "docker://") {
		imgName = strings.ReplaceAll(imgName, "docker://", "//")
	}

	if !strings.HasPrefix(imgName, "//") {
		imgName = "//" + imgName
	}

	return docker.Transport.ParseReference(imgName)
}

// deleteImage attempts to delete the specified image with retries,
// using the provided context and system context.
//
//nolint:unparam
func deleteImage(ctx context.Context, sysCtx *types.SystemContext, imageName string) error {
	retryOpts := retry.RetryOptions{
		MaxRetry: cmdRetriesCount,
	}

	ref, err := parseImageName(imageName)
	if err != nil {
		return err
	}

	if err := retry.IfNecessary(ctx, func() error {
		return ref.DeleteImage(ctx, sysCtx)
	}, &retryOpts); err != nil {
		return newErrImage(imageName, err)
	}
	return nil
}

// imageInspect inspects the specified image, retrieving its ImageInspectInfo and digest.
// This function has been inspired by upstream skopeo inspect.
// It includes retry logic for image source creation and manifest retrieval.
// TODO(jkyros): Revisit direct skopeo inspect usage, but direct library calls are beneficial for error context.
//
//nolint:unparam
func imageInspect(ctx context.Context, sysCtx *types.SystemContext, imageName string) (*types.ImageInspectInfo, *digest.Digest, error) {
	var (
		src        types.ImageSource
		imgInspect *types.ImageInspectInfo
		err        error
	)

	retryOpts := retry.RetryOptions{
		MaxRetry: cmdRetriesCount,
	}

	ref, err := parseImageName(imageName)
	if err != nil {
		return nil, nil, fmt.Errorf("error parsing image name %q: %w", imageName, err)
	}

	// retry.IfNecessary takes into account whether the error is "retryable"
	// so we don't keep looping on errors that will never resolve
	if err := retry.RetryIfNecessary(ctx, func() error {
		src, err = ref.NewImageSource(ctx, sysCtx)
		return err
	}, &retryOpts); err != nil {
		return nil, nil, newErrImage(imageName, fmt.Errorf("error getting image source: %w", err))
	}

	var rawManifest []byte
	unparsedInstance := image.UnparsedInstance(src, nil)
	if err := retry.IfNecessary(ctx, func() error {
		rawManifest, _, err = unparsedInstance.Manifest(ctx)
		return err
	}, &retryOpts); err != nil {
		return nil, nil, fmt.Errorf("error retrieving manifest for image: %w", err)
	}

	// get the digest here because it's not part of the image inspection
	digest, err := manifest.Digest(rawManifest)
	if err != nil {
		return nil, nil, fmt.Errorf("error retrieving image digest: %q: %w", imageName, err)
	}

	defer src.Close()

	img, err := image.FromUnparsedImage(ctx, sysCtx, unparsedInstance)
	if err != nil {
		return nil, nil, newErrImage(imageName, fmt.Errorf("error parsing manifest for image: %w", err))
	}

	if err := retry.RetryIfNecessary(ctx, func() error {
		imgInspect, err = img.Inspect(ctx)
		return err
	}, &retryOpts); err != nil {
		return nil, nil, newErrImage(imageName, err)
	}

	return imgInspect, &digest, nil
}
