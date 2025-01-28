package daemon

import (
	"context"
	"fmt"
	"strings"

	"github.com/containers/common/pkg/retry"
	"github.com/containers/image/v5/image"
	"github.com/containers/image/v5/manifest"
	"github.com/containers/image/v5/transports"
	"github.com/containers/image/v5/types"
	digest "github.com/opencontainers/go-digest"
)

const (
	// Number of retry we want to perform
	cmdRetriesCount = 2
)

func parseImageName(imgName string) (types.ImageReference, error) {
	// Keep this in sync with TransportFromImageName!
	transportName, withinTransport, valid := strings.Cut(imgName, ":")
	if !valid {
		return nil, fmt.Errorf(`invalid image name "%s", expected colon-separated transport:reference`, imgName)
	}
	transport := transports.Get(transportName)
	if transport == nil {
		return nil, fmt.Errorf(`invalid image name "%s", unknown transport "%s"`, imgName, transportName)
	}
	return transport.ParseReference(withinTransport)
}

//nolint:unparam
func DeleteImage(imageName, authFilePath string) error {
	imageName = "docker://" + imageName
	retryOpts := retry.RetryOptions{
		MaxRetry: cmdRetriesCount,
	}

	ctx := context.Background()
	sys := &types.SystemContext{AuthFilePath: authFilePath}

	ref, err := parseImageName(imageName)
	if err != nil {
		return err
	}

	if err := retry.IfNecessary(ctx, func() error {
		return ref.DeleteImage(ctx, sys)
	}, &retryOpts); err != nil {
		return err
	}
	return nil
}

// This function has been inspired from upstream skopeo inspect, see https://github.com/containers/skopeo/blob/master/cmd/skopeo/inspect.go
// We can use skopeo inspect directly once fetching RepoTags becomes optional in skopeo.
// TODO(jkyros): I know we said we eventually wanted to use skopeo inspect directly, but it is really great being able
// to know what the error is by using the libraries directly :)
//
//nolint:unparam
func ImageInspect(imageName, authfile string) (*types.ImageInspectInfo, *digest.Digest, error) {
	var (
		src        types.ImageSource
		imgInspect *types.ImageInspectInfo
		err        error
	)

	retryOpts := retry.RetryOptions{
		MaxRetry: cmdRetriesCount,
	}
	imageName = "docker://" + imageName

	ctx := context.Background()
	authfilePath := ostreeAuthFile
	if authfile != "" {
		authfilePath = authfile
	}
	sys := &types.SystemContext{AuthFilePath: authfilePath}

	ref, err := parseImageName(imageName)
	if err != nil {
		return nil, nil, fmt.Errorf("error parsing image name %q: %w", imageName, err)
	}

	// retry.IfNecessary takes into account whether the error is "retryable"
	// so we don't keep looping on errors that will never resolve
	if err := retry.RetryIfNecessary(ctx, func() error {
		src, err = ref.NewImageSource(ctx, sys)
		return err
	}, &retryOpts); err != nil {
		return nil, nil, fmt.Errorf("error getting image source %q: %w", imageName, err)
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

	img, err := image.FromUnparsedImage(ctx, sys, unparsedInstance)
	if err != nil {
		return nil, nil, fmt.Errorf("error parsing manifest for image %q: %w", imageName, err)
	}

	if err := retry.RetryIfNecessary(ctx, func() error {
		imgInspect, err = img.Inspect(ctx)
		return err
	}, &retryOpts); err != nil {
		return nil, nil, err
	}

	return imgInspect, &digest, nil
}
