package osimagestream

import (
	"context"
	"fmt"
	"sync"

	imagev1 "github.com/openshift/api/image/v1"
	"github.com/openshift/client-go/image/clientset/versioned/scheme"
	"k8s.io/apimachinery/pkg/runtime"
)

const releaseImageStreamLocation = "/release-manifests/image-references"

// ImageStreamProvider provides access to an ImageStream resource.
type ImageStreamProvider interface {
	ReadImageStream(ctx context.Context) (*imagev1.ImageStream, error)
}

// ImageStreamProviderResource provides an ImageStream from a direct in-memory resource.
type ImageStreamProviderResource struct {
	imageStream *imagev1.ImageStream
}

// NewImageStreamProviderResource creates a new ImageStreamProviderResource with the given ImageStream.
func NewImageStreamProviderResource(imageStream *imagev1.ImageStream) *ImageStreamProviderResource {
	return &ImageStreamProviderResource{imageStream: imageStream}
}

// ReadImageStream returns the ImageStream provided at initialization.
func (i *ImageStreamProviderResource) ReadImageStream(_ context.Context) (*imagev1.ImageStream, error) {
	return i.imageStream, nil
}

// ImageStreamProviderNetwork fetches an ImageStream from a container image over the network.
// It extracts the ImageStream manifest from the /release-manifests/image-references file
// within the specified container image.
type ImageStreamProviderNetwork struct {
	imagesInspector ImagesInspector
	imageName       string
	cacheLock       sync.Mutex
	imageStream     *imagev1.ImageStream
}

// NewImageStreamProviderNetwork creates a new ImageStreamProviderNetwork that will fetch
// the ImageStream from the specified container image.
func NewImageStreamProviderNetwork(imagesInspector ImagesInspector, imageName string) *ImageStreamProviderNetwork {
	return &ImageStreamProviderNetwork{imagesInspector: imagesInspector, imageName: imageName}
}

// ReadImageStream fetches the ImageStream manifest from the container image and decodes it.
// The result is cached on success; failures are retried on subsequent calls.
func (i *ImageStreamProviderNetwork) ReadImageStream(ctx context.Context) (*imagev1.ImageStream, error) {
	i.cacheLock.Lock()
	defer i.cacheLock.Unlock()
	if i.imageStream != nil {
		return i.imageStream, nil
	}
	is, err := i.fetchImageStream(ctx)
	if err != nil {
		return nil, err
	}
	i.imageStream = is
	return is, nil
}

func (i *ImageStreamProviderNetwork) fetchImageStream(ctx context.Context) (*imagev1.ImageStream, error) {
	imageStreamBytes, err := i.imagesInspector.FetchImageFile(ctx, i.imageName, releaseImageStreamLocation)
	if err != nil {
		return nil, err
	}
	if len(imageStreamBytes) == 0 {
		return nil, fmt.Errorf("no ImageStream found for %s", i.imageName)
	}

	requiredObj, err := runtime.Decode(scheme.Codecs.UniversalDecoder(imagev1.SchemeGroupVersion), imageStreamBytes)
	if err != nil {
		return nil, fmt.Errorf("image %s contains an invalid manifest", i.imageName)
	}

	is, ok := requiredObj.(*imagev1.ImageStream)
	if !ok {
		return nil, fmt.Errorf("image %s contains unexpected ImageStream content", i.imageName)
	}
	return is, nil
}
