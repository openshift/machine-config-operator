package osimagestream

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/containers/common/pkg/retry"
	"github.com/containers/image/v5/pkg/blobinfocache/none"
	"github.com/containers/image/v5/types"
	imagev1 "github.com/openshift/api/image/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/image"
)

const releaseImageStreamLocation = "/release-manifests/image-references"

type ImageStreamProvider interface {
	ReadImageStream(ctx context.Context) (*imagev1.ImageStream, error)
}

type ImageStreamProviderResource struct {
	imageStream *imagev1.ImageStream
}

func NewImageStreamProviderResource(imageStream *imagev1.ImageStream) *ImageStreamProviderResource {
	return &ImageStreamProviderResource{imageStream: imageStream}
}

func (i *ImageStreamProviderResource) ReadImageStream(_ context.Context) (*imagev1.ImageStream, error) {
	return i.imageStream, nil
}

type ImageStreamProviderNetwork struct {
	sysCtx    *types.SystemContext
	imageName string
}

func NewImageStreamProviderNetwork(sysCtx *types.SystemContext, imageName string) *ImageStreamProviderNetwork {
	return &ImageStreamProviderNetwork{sysCtx: sysCtx, imageName: imageName}
}

func (i *ImageStreamProviderNetwork) ReadImageStream(ctx context.Context) (*imagev1.ImageStream, error) {
	imageStreamBytes, err := fetchImageReferences(ctx, i.sysCtx, i.imageName)
	if err != nil {
		return nil, err
	}

	imageStream, err := image.ReadImageStreamV1O(imageStreamBytes)
	if err != nil {
		return nil, fmt.Errorf("error reading image stream from %s image: %v", i.imageName, err)
	}
	return imageStream, nil
}

func fetchImageReferences(ctx context.Context, sysCtx *types.SystemContext, imageName string) ([]byte, error) {
	ref, err := image.ParseImageName(imageName)
	if err != nil {
		return nil, err
	}
	img, err := ref.NewImage(ctx, sysCtx)
	if err != nil {
		return nil, err
	}
	defer img.Close()

	src, err := image.GetImageSource(ctx, sysCtx, imageName, &retry.Options{MaxRetry: 2})
	if err != nil {
		return nil, err
	}
	defer src.Close()

	layerInfos := img.LayerInfos()

	// Start searching from the back
	slices.Reverse(layerInfos)
	for _, info := range layerInfos {
		content, err := searchLayerForFile(ctx, src, info, releaseImageStreamLocation)
		if err != nil {
			return nil, err
		}
		if content != nil {
			return content, nil
		}
	}
	return nil, fmt.Errorf("%s file not found in %s image", releaseImageStreamLocation, imageName)
}

func searchLayerForFile(ctx context.Context, imgSrc types.ImageSource, blobInfo types.BlobInfo, targetFile string) ([]byte, error) {
	layerStream, _, err := imgSrc.GetBlob(ctx, blobInfo, none.NoCache)
	if err != nil {
		return nil, err
	}
	defer layerStream.Close()

	gzr, err := gzip.NewReader(layerStream)
	if err != nil {
		return nil, err
	}
	defer gzr.Close()

	// Search tar
	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		// Normalize and match
		if header.Name == targetFile || header.Name == "."+targetFile || "./"+header.Name == targetFile || strings.TrimLeft(targetFile, "/") == header.Name {
			content, err := io.ReadAll(tr)
			return content, err
		}
	}
	return nil, nil
}
