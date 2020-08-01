package daemon

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/containers/image/v5/docker"
	"github.com/containers/image/v5/image"
	"github.com/containers/image/v5/types"
	"github.com/pkg/errors"
)

func retryIfNecessary(ctx context.Context, operation func() error) error {
	err := operation()
	for attempt := 0; err != nil && attempt < numRetriesNetCommands; attempt++ {
		delay := time.Duration(int(math.Pow(2, float64(attempt)))) * time.Second
		fmt.Printf("Warning: failed, retrying in %s ... (%d/%d)", delay, attempt+1, numRetriesNetCommands)
		select {
		case <-time.After(delay):
			break
		case <-ctx.Done():
			return err
		}
		err = operation()
	}
	return err
}

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
func imageInspect(imageName string) (*types.ImageInspectInfo, error) {
	var (
		src        types.ImageSource
		imgInspect *types.ImageInspectInfo
		err        error
	)

	ctx := context.Background()
	sys := &types.SystemContext{AuthFilePath: kubeletAuthFile}

	if err := retryIfNecessary(ctx, func() error {
		src, err = newDockerImageSource(ctx, sys, imageName)
		return err
	}); err != nil {
		return nil, errors.Wrapf(err, "Error parsing image name %q", imageName)
	}

	defer src.Close()

	img, err := image.FromUnparsedImage(ctx, sys, image.UnparsedInstance(src, nil))
	if err != nil {
		return nil, fmt.Errorf("Error parsing manifest for image: %v", err)
	}

	if err := retryIfNecessary(ctx, func() error {
		imgInspect, err = img.Inspect(ctx)
		return err
	}); err != nil {
		return nil, err
	}

	return imgInspect, nil
}
