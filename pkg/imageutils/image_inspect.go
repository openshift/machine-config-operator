package imageutils

import (
	"context"
	"errors"
	"fmt"

	"github.com/containers/common/pkg/retry"
	"github.com/containers/image/v5/image"
	"github.com/containers/image/v5/manifest"
	"github.com/containers/image/v5/types"
	"github.com/opencontainers/go-digest"
)

// GetImage retrieves a container image by name using the provided system context.
// The returned ImageSource must be closed by the caller.
func GetImage(ctx context.Context, sysCtx *types.SystemContext, imageName string, retryOpts *retry.RetryOptions) (types.Image, types.ImageSource, error) {
	ref, err := ParseImageName(imageName)
	if err != nil {
		return nil, nil, fmt.Errorf("error parsing image name %q: %w", imageName, err)
	}

	source, err := GetImageSourceFromReference(ctx, sysCtx, ref, retryOpts)
	if err != nil {
		return nil, nil, fmt.Errorf("error getting image source for %s: %w", imageName, err)
	}

	img, err := image.FromUnparsedImage(ctx, sysCtx, image.UnparsedInstance(source, nil))
	if err != nil {
		return nil, nil, errors.Join(err, source.Close())
	}

	return img, source, nil
}

// GetDigestFromImage computes and returns the manifest digest for the given image.
// It includes retry logic to handle transient network errors.
func GetDigestFromImage(ctx context.Context, image types.Image, retryOpts *retry.RetryOptions) (digest.Digest, error) {
	var (
		rawManifest []byte
		err         error
	)
	if err = retry.IfNecessary(ctx, func() error {
		rawManifest, _, err = image.Manifest(ctx)
		return err
	}, retryOpts); err != nil {
		return "", fmt.Errorf("error retrieving manifest for image: %w", err)
	}
	return manifest.Digest(rawManifest)
}

// GetInspectInfoFromImage retrieves detailed inspection information for the given image.
// It includes retry logic to handle transient network errors.
func GetInspectInfoFromImage(ctx context.Context, image types.Image, retryOpts *retry.RetryOptions) (*types.ImageInspectInfo, error) {
	var (
		imgInspect *types.ImageInspectInfo
		err        error
	)
	return imgInspect, retry.IfNecessary(ctx, func() error {
		imgInspect, err = image.Inspect(ctx)
		return err
	}, retryOpts)
}

// BulkInspectResult represents the result of inspecting a single image in a bulk operation.
// It contains either the inspection information or an error if the inspection failed.
type BulkInspectResult struct {
	Image       string
	InspectInfo *types.ImageInspectInfo
	Error       error
}

// BulkInspectorOptions configures the behavior of a BulkInspector.
// RetryOpts specifies retry behavior for transient network errors.
// FailOnErr determines whether to return immediately on the first error (true)
// or to continue inspecting all images and collect all results (false).
// Count limits the number of concurrent image inspections. If Count is 0 or
// negative, no limit is applied and all images are inspected concurrently.
type BulkInspectorOptions struct {
	RetryOpts *retry.RetryOptions
	FailOnErr bool
	Count     int
}

// BulkInspector performs concurrent image inspections with optional rate limiting
// and configurable error handling.
type BulkInspector struct {
	retryOpts *retry.RetryOptions
	failOnErr bool
	count     int
}

// NewBulkInspector creates a new BulkInspector with the provided options.
// If opts is nil or RetryOpts is nil, sensible defaults are applied:
// - RetryOpts.MaxRetry defaults to 2
// - FailOnErr defaults to false
// - Count defaults to 0 (unlimited concurrency)
func NewBulkInspector(opts *BulkInspectorOptions) *BulkInspector {
	if opts == nil {
		opts = &BulkInspectorOptions{}
	}
	if opts.RetryOpts == nil {
		opts.RetryOpts = &retry.RetryOptions{MaxRetry: 2}
	}
	return &BulkInspector{
		retryOpts: opts.RetryOpts,
		failOnErr: opts.FailOnErr,
		count:     opts.Count,
	}
}

// Inspect concurrently inspects multiple container images and returns their inspection results.
// The inspection is performed with optional rate limiting based on the Count configuration.
// If FailOnErr is true, the method returns immediately upon encountering the first error.
// Otherwise, it inspects all images and returns results for all of them, with errors
// recorded in individual BulkInspectResult entries.
// Results are returned in completion order, not input order. Use the Image field to
// correlate results with the input image names.
func (i *BulkInspector) Inspect(ctx context.Context, sysCtx *types.SystemContext, images ...string) ([]BulkInspectResult, error) {
	imagesCount := len(images)
	if imagesCount == 0 {
		return []BulkInspectResult{}, nil
	}

	results := make(chan BulkInspectResult, imagesCount)
	var rateLimiterChannel chan struct{}
	if i.count > 0 {
		rateLimiterChannel = make(chan struct{}, min(i.count, imagesCount))
	} else {
		// No rate limiting - use a channel that never blocks
		rateLimiterChannel = make(chan struct{}, imagesCount)
	}

	childContext, cancel := context.WithCancel(ctx)
	// The deferred cancellation of the context will kill the tasks when exiting
	// Useful in case of error
	defer cancel()
	for _, imageName := range images {
		go func(img string) {
			select {
			case rateLimiterChannel <- struct{}{}:
				defer func() { <-rateLimiterChannel }()

				inspectInfo, err := i.inspectImage(childContext, sysCtx, img)
				results <- BulkInspectResult{Image: img, InspectInfo: inspectInfo, Error: err}
			case <-childContext.Done():
				results <- BulkInspectResult{Error: childContext.Err(), Image: img, InspectInfo: nil}
			}
		}(imageName)
	}

	inspectInfos := make([]BulkInspectResult, imagesCount)
	for idx := range imagesCount {
		res := <-results
		if res.Error != nil && i.failOnErr {
			return nil, res.Error
		}
		inspectInfos[idx] = res
	}
	return inspectInfos, nil
}

func (i *BulkInspector) inspectImage(ctx context.Context, sysCtx *types.SystemContext, image string) (inspectInfo *types.ImageInspectInfo, err error) {
	img, imgSource, err := GetImage(ctx, sysCtx, image, i.retryOpts)
	if err != nil {
		return nil, err
	}
	defer func() {
		if imgSourceErr := imgSource.Close(); imgSourceErr != nil {
			err = errors.Join(err, imgSourceErr)
		}
	}()
	return GetInspectInfoFromImage(ctx, img, i.retryOpts)
}
