package imageutils

import (
	"context"
	"fmt"
	"strings"

	"github.com/containers/common/pkg/retry"
	"github.com/containers/image/v5/docker"
	"github.com/containers/image/v5/types"
)

// GetImageSourceFromReference creates an ImageSource from an ImageReference with retry logic.
func GetImageSourceFromReference(ctx context.Context, sysCtx *types.SystemContext, ref types.ImageReference, retryOpts *retry.RetryOptions) (types.ImageSource, error) {
	var src types.ImageSource
	var err error
	if err := retry.RetryIfNecessary(ctx, func() error {
		src, err = ref.NewImageSource(ctx, sysCtx)
		return err
	}, retryOpts); err != nil {
		return nil, fmt.Errorf("error getting image source for %s: %w", ref.StringWithinTransport(), err)
	}
	return src, nil
}

// ParseImageName parses an image name into a docker transport ImageReference.
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
