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
	// Number of retry we want to perform
	cmdRetriesCount = 2
)

// An object that an inspect a container image.
type ImageInspector interface {
	ImageInspect(context.Context, *types.SystemContext, string) (*types.ImageInspectInfo, *digest.Digest, error)
}

// An object that can delete a container image.
type ImageDeleter interface {
	DeleteImage(context.Context, *types.SystemContext, string) error
}

// An object that can both inspect and delete a container image.
type ImageInspectorDeleter interface {
	ImageInspector
	ImageDeleter
}

// The real image inspector implementation which is wired up with the appropriate functions.
type imageInspectorImpl struct{}

// Constructs a real ImageInspectorDeleter.
func NewImageInspectorDeleter() ImageInspectorDeleter {
	return &imageInspectorImpl{}
}

// Uses the provided system context to inspect the provided image pullspec.
func (i *imageInspectorImpl) ImageInspect(ctx context.Context, sysCtx *types.SystemContext, image string) (*types.ImageInspectInfo, *digest.Digest, error) {
	return imageInspect(ctx, sysCtx, image)
}

// Uses the provided system context to delete the provided image pullspec.
func (i *imageInspectorImpl) DeleteImage(ctx context.Context, sysCtx *types.SystemContext, image string) error {
	return deleteImage(ctx, sysCtx, image)
}

// Parses the image name into an ImageReference.
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

// This function has been inspired from upstream skopeo inspect, see https://github.com/containers/skopeo/blob/master/cmd/skopeo/inspect.go
// We can use skopeo inspect directly once fetching RepoTags becomes optional in skopeo.
// TODO(jkyros): I know we said we eventually wanted to use skopeo inspect directly, but it is really great being able
// to know what the error is by using the libraries directly :)
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
