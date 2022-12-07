package daemon

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

// newDockerImageSource creates an image source for an image reference.
// The caller must call .Close() on the returned ImageSource.
func newDockerImageSource(ctx context.Context, sys *types.SystemContext, name string) (types.ImageSource, error) {
	var imageName string
	if !strings.HasPrefix(name, "//") {
		imageName = "//" + name
	} else {
		imageName = name
	}
	ref, err := docker.ParseReference(imageName)
	if err != nil {
		return nil, err
	}

	return ref.NewImageSource(ctx, sys)
}

// This function has been inspired from upstream skopeo inspect, see https://github.com/containers/skopeo/blob/master/cmd/skopeo/inspect.go
// We can use skopeo inspect directly once fetching RepoTags becomes optional in skopeo.
// TODO(jkyros): I know we said we eventually wanted to use skopeo inspect directly, but it is really great being able
// to know what the error is by using the libraries directly :)
//nolint:unparam
func imageInspect(imageName string) (*types.ImageInspectInfo, *digest.Digest, error) {
	var (
		src        types.ImageSource
		imgInspect *types.ImageInspectInfo
		err        error
	)

	retryOpts := retry.Options{
		MaxRetry: cmdRetriesCount,
	}

	ctx := context.Background()
	sys := &types.SystemContext{AuthFilePath: kubeletAuthFile}

	// retry.IfNecessary takes into account whether the error is "retryable"
	// so we don't keep looping on errors that will never resolve
	if err := retry.IfNecessary(ctx, func() error {
		src, err = newDockerImageSource(ctx, sys, imageName)
		return err
	}, &retryOpts); err != nil {
		return nil, nil, fmt.Errorf("error parsing image name %q: %w", imageName, err)
	}

	var rawManifest []byte
	if err := retry.IfNecessary(ctx, func() error {
		rawManifest, _, err = src.GetManifest(ctx, nil)

		return err
	}, &retryOpts); err != nil {
		return nil, nil, fmt.Errorf("error retrieving image manifest %q: %w", imageName, err)
	}

	// get the digest here because it's not part of the image inspection
	digest, err := manifest.Digest(rawManifest)
	if err != nil {
		return nil, nil, fmt.Errorf("error retrieving image digest: %q: %w", imageName, err)
	}

	defer src.Close()

	img, err := image.FromUnparsedImage(ctx, sys, image.UnparsedInstance(src, nil))
	if err != nil {
		return nil, nil, fmt.Errorf("error parsing manifest for image %q: %w", imageName, err)
	}

	if err := retry.IfNecessary(ctx, func() error {
		imgInspect, err = img.Inspect(ctx)
		return err
	}, &retryOpts); err != nil {
		return nil, nil, err
	}

	return imgInspect, &digest, nil
}
