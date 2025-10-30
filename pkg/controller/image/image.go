package image

import (
	"context"
	"fmt"
	"strings"

	"github.com/containers/common/pkg/retry"
	"github.com/containers/image/v5/docker"
	image_v5 "github.com/containers/image/v5/image"
	"github.com/containers/image/v5/manifest"
	"github.com/containers/image/v5/types"
	"github.com/opencontainers/go-digest"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
)

var (
	imagesScheme = runtime.NewScheme()
	imagesCodecs = serializer.NewCodecFactory(imagesScheme)
)

func GetImageSource(ctx context.Context, sysCtx *types.SystemContext, imageName string, retryOpts *retry.RetryOptions) (types.ImageSource, error) {
	ref, err := ParseImageName(imageName)
	if err != nil {
		return nil, fmt.Errorf("error parsing image name %q: %w", imageName, err)
	}

	var src types.ImageSource
	// retry.IfNecessary takes into account whether the error is "retryable"
	// so we don't keep looping on errors that will never resolve
	if err := retry.RetryIfNecessary(ctx, func() error {
		src, err = ref.NewImageSource(ctx, sysCtx)
		return err
	}, retryOpts); err != nil {
		return nil, NewErrImage(imageName, fmt.Errorf("error getting image source: %w", err))
	}
	return src, nil
}

// ParseImageName parses the image name string into an ImageReference,
// handling various prefix formats like "docker://" and ensuring a standard format.
func ParseImageName(imgName string) (types.ImageReference, error) {
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

// imageInspect inspects the specified image, retrieving its ImageInspectInfo and digest.
// This function has been inspired by upstream skopeo inspect.
// It includes retry logic for image source creation and manifest retrieval.
// TODO(jkyros): Revisit direct skopeo inspect usage, but direct library calls are beneficial for error context.
//
//nolint:unparam
func ImageInspect(ctx context.Context, sysCtx *types.SystemContext, imageName string, retryOpts *retry.RetryOptions) (*types.ImageInspectInfo, *digest.Digest, error) {
	var (
		imgInspect *types.ImageInspectInfo
		err        error
	)

	src, err := GetImageSource(ctx, sysCtx, imageName, retryOpts)
	if err != nil {
		return nil, nil, err
	}
	defer src.Close()

	var rawManifest []byte
	unparsedInstance := image_v5.UnparsedInstance(src, nil)
	if err := retry.IfNecessary(ctx, func() error {
		rawManifest, _, err = unparsedInstance.Manifest(ctx)
		return err
	}, retryOpts); err != nil {
		return nil, nil, fmt.Errorf("error retrieving manifest for image: %w", err)
	}

	// get the digest here because it's not part of the image inspection
	digest, err := manifest.Digest(rawManifest)
	if err != nil {
		return nil, nil, fmt.Errorf("error retrieving image digest: %q: %w", imageName, err)
	}

	defer src.Close()

	img, err := image_v5.FromUnparsedImage(ctx, sysCtx, unparsedInstance)
	if err != nil {
		return nil, nil, NewErrImage(imageName, fmt.Errorf("error parsing manifest for image: %w", err))
	}

	if err := retry.RetryIfNecessary(ctx, func() error {
		imgInspect, err = img.Inspect(ctx)
		return err
	}, retryOpts); err != nil {
		return nil, nil, NewErrImage(imageName, err)
	}

	return imgInspect, &digest, nil
}
