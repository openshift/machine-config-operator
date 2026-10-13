package imageutils

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"slices"

	"github.com/containers/common/pkg/retry"
	"github.com/containers/image/v5/pkg/blobinfocache/none"
	"github.com/containers/image/v5/types"
)

// ReadImageFileContentFn is a predicate function that returns true when
// the tar header matches the desired file to extract from the image layer.
type ReadImageFileContentFn func(*tar.Header) bool

// ReadImageFileContent searches for and extracts a file from a container image.
// It iterates through the image layers (starting from the last) and uses matcherFn
// to identify the target file. When found, the file content is returned as a byte slice.
// If no matching file is found, (nil, nil) is returned.
func ReadImageFileContent(ctx context.Context, sysCtx *types.SystemContext, imageName string, matcherFn ReadImageFileContentFn, retryOpts *retry.Options) (content []byte, err error) {
	ref, err := ParseImageName(imageName)
	if err != nil {
		return nil, err
	}
	img, err := ref.NewImage(ctx, sysCtx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := img.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	retries := retryOpts
	if retries == nil {
		retries = &retry.Options{MaxRetry: 2}
	}
	src, err := GetImageSourceFromReference(ctx, sysCtx, ref, retries)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := src.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	layerInfos := img.LayerInfos()

	// Small optimization: Usually user defined content is
	// at the very end layers so start searching backwards
	// may result in finding the file sooner.
	slices.Reverse(layerInfos)
	for _, info := range layerInfos {
		if content, err = searchLayerForFile(ctx, src, info, matcherFn); err != nil || content != nil {
			return content, err
		}
	}
	return nil, nil

}

func searchLayerForFile(ctx context.Context, imgSrc types.ImageSource, blobInfo types.BlobInfo, matcherFn ReadImageFileContentFn) (content []byte, err error) {
	layerStream, _, err := imgSrc.GetBlob(ctx, blobInfo, none.NoCache)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := layerStream.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	// The layer content is just a gzip tar file. Create both readers
	gzr, err := gzip.NewReader(layerStream)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := gzr.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	// Open the tar and search for the target file till
	// we find it or no more files are present in the tar
	tr := tar.NewReader(gzr)
	for {
		var header *tar.Header
		header, err = tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if matcherFn(header) {
			content, err = io.ReadAll(tr)
			return content, err
		}
	}

	// The target file wasn't found
	return nil, nil
}
