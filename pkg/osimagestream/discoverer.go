package osimagestream

import (
	"context"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"k8s.io/klog/v2"
)

// StreamDiscoverer inspects a list of container images and discovers
// which ones are OS or extensions images by reading their OCI labels,
// grouping matching pairs into OS image stream sets.
type StreamDiscoverer struct {
	imageInspector       ImagesInspector
	imageStreamExtractor ImageDataExtractor
}

// NewStreamDiscoverer creates a new StreamDiscoverer with the given dependencies.
func NewStreamDiscoverer(imageInspector ImagesInspector, imageStreamExtractor ImageDataExtractor) *StreamDiscoverer {
	return &StreamDiscoverer{
		imageInspector:       imageInspector,
		imageStreamExtractor: imageStreamExtractor,
	}
}

// Discover inspects the given container images and returns the OS image stream
// sets extracted from their labels. Images that fail inspection or lack stream
// labels are logged and skipped.
func (d *StreamDiscoverer) Discover(ctx context.Context, images []string) ([]*mcfgv1.OSImageStreamSet, error) {
	if len(images) == 0 {
		return []*mcfgv1.OSImageStreamSet{}, nil
	}

	inspectionResults, err := d.imageInspector.Inspect(ctx, images...)
	if err != nil {
		return nil, err
	}

	var imagesData []*ImageData
	for _, result := range inspectionResults {
		if result.Error != nil {
			klog.Errorf(
				"error inspecting image for OSImageStream discovery. %s image is ignored. %v",
				result.Image,
				result.Error,
			)
			continue
		}
		imageData := d.imageStreamExtractor.GetImageData(result.Image, result.InspectInfo.Labels)
		if imageData == nil {
			klog.V(4).Infof("image %s does not contain stream labels. Image discarded.", result.Image)
			continue
		}
		imagesData = append(imagesData, imageData)
	}

	return GroupOSContainerImageMetadataToStream(imagesData), nil
}
