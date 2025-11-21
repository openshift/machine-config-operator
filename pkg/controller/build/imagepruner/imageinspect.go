package imagepruner

import (
	"context"
	"errors"
	"fmt"

	"github.com/containers/common/pkg/retry"
	"github.com/containers/image/v5/types"
	digest "github.com/opencontainers/go-digest"
	"github.com/openshift/machine-config-operator/pkg/imageutils"
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

// deleteImage attempts to delete the specified image with retries,
// using the provided context and system context.
//
//nolint:unparam
func deleteImage(ctx context.Context, sysCtx *types.SystemContext, imageName string) error {
	retryOpts := retry.RetryOptions{
		MaxRetry: cmdRetriesCount,
	}

	ref, err := imageutils.ParseImageName(imageName)
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
func imageInspect(ctx context.Context, sysCtx *types.SystemContext, imageName string) (inspectInfo *types.ImageInspectInfo, digest *digest.Digest, err error) {
	retryOpts := retry.RetryOptions{
		MaxRetry: cmdRetriesCount,
	}
	image, imgSource, err := imageutils.GetImage(ctx, sysCtx, imageName, &retryOpts)
	if err != nil {
		return nil, nil, newErrImage(imageName, fmt.Errorf("error fetching underlying image: %w", err))
	}
	defer func() {
		if imgSourceErr := imgSource.Close(); imgSourceErr != nil {
			err = errors.Join(err, imgSourceErr)
		}
	}()

	inspectInfo, err = imageutils.GetInspectInfoFromImage(ctx, image, &retryOpts)
	if err != nil {
		return nil, nil, newErrImage(imageName, fmt.Errorf("error inspecting image: %w", err))
	}

	imageDigest, err := imageutils.GetDigestFromImage(ctx, image, &retryOpts)
	if err != nil {
		return nil, nil, newErrImage(imageName, fmt.Errorf("error fetching image digest: %w", err))
	}
	return inspectInfo, &imageDigest, nil
}
