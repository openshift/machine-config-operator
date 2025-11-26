package osimagestream

import (
	"maps"
	"slices"

	"github.com/openshift/api/machineconfiguration/v1alpha1"
	"k8s.io/klog/v2"
)

const (
	// coreOSLabelStreamClass is the container image label that identifies the OS stream name.
	coreOSLabelStreamClass = "io.openshift.os.streamclass"
	// coreOSLabelDiscriminator is the container image label that distinguishes OS images from Extensions images.
	coreOSLabelDiscriminator = "ostree.linux"
)

// ImageType indicates whether a container image is an OS image or an extensions image.
type ImageType int

const (
	// ImageTypeOS represents a base operating system image.
	ImageTypeOS = iota
	// ImageTypeExtensions represents an OS extensions image.
	ImageTypeExtensions
)

// ImageDataExtractor extracts metadata from container image labels to determine
// the image type and associated OS stream.
type ImageDataExtractor interface {
	// GetImageData analyzes container image labels and returns structured metadata,
	// or nil if the image is not an OS or extensions image.
	GetImageData(image string, labels map[string]string) *ImageData
}

// ImageData contains metadata extracted from a container image.
type ImageData struct {
	Image  string    // Container image reference
	Type   ImageType // Image type (OS or extensions)
	Stream string    // OS stream name
}

// ImageStreamExtractorImpl implements ImageDataExtractor using container image labels
// to identify and classify OS and extensions images.
type ImageStreamExtractorImpl struct {
	imagesInspector      ImagesInspector
	streamLabels         []string
	osImageDiscriminator string
}

// NewImageStreamExtractor creates a new ImageDataExtractor configured to recognize
// CoreOS images using standard image labels.
func NewImageStreamExtractor() ImageDataExtractor {
	// The type is thought to allow future extra label addition for
	// i.e. Allow a customer to bring their own images with their own labels (defining a selector)
	return &ImageStreamExtractorImpl{
		streamLabels:         []string{coreOSLabelStreamClass},
		osImageDiscriminator: coreOSLabelDiscriminator,
	}
}

// GetImageData analyzes container image labels to extract OS stream metadata.
// Returns nil if the image does not have the required stream label.
// Distinguishes between OS and extensions images based on the presence of the ostree discriminator label.
func (e *ImageStreamExtractorImpl) GetImageData(image string, labels map[string]string) *ImageData {
	imageData := &ImageData{
		Image:  image,
		Stream: findLabelValue(labels, e.streamLabels...),
	}
	if imageData.Stream == "" {
		// Not an OS/extensions image
		return nil
	}

	if findLabelValue(labels, e.osImageDiscriminator) != "" {
		imageData.Type = ImageTypeOS
	} else {
		imageData.Type = ImageTypeExtensions
	}

	return imageData
}

// findLabelValue searches for the first matching key in the map and returns its value.
// Returns an empty string if none of the keys are found.
func findLabelValue(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return ""
}

// GroupOSContainerImageMetadataToStream groups OS and extensions images by stream name.
// Combines pairs of OS and extensions images that share the same stream name into OSImageStreamSet objects.
// If multiple images are found for the same stream and type, the last one wins.
func GroupOSContainerImageMetadataToStream(imagesMetadata []*ImageData) []*v1alpha1.OSImageStreamSet {
	streamMaps := make(map[string]*v1alpha1.OSImageStreamSet)
	for _, imageMetadata := range imagesMetadata {
		streamURLSet, exists := streamMaps[imageMetadata.Stream]
		if !exists {
			streamMaps[imageMetadata.Stream] = NewOSImageStreamURLSetFromImageMetadata(imageMetadata)
			continue
		}

		// The stream already exists. Maybe it has not both urls yet
		imageName := v1alpha1.ImageDigestFormat(imageMetadata.Image)
		if imageMetadata.Type == ImageTypeOS {
			if streamURLSet.OSImage != "" && streamURLSet.OSImage != imageName {
				// Looks like we have a conflict. Log it and override
				klog.V(4).Infof("multiple OS images for the same %s stream. Previous one was %s. Overriding with %s", streamURLSet.Name, streamURLSet.OSImage, imageName)
			}
			streamURLSet.OSImage = imageName
		} else {
			if streamURLSet.OSExtensionsImage != "" && streamURLSet.OSExtensionsImage != imageName {
				// Looks like we have a conflict. Log it and override
				klog.V(4).Infof("multiple OS Extensions images for the same %s stream. Previous one was %s. Overriding with %s", streamURLSet.Name, streamURLSet.OSImage, imageName)
			}
			streamURLSet.OSExtensionsImage = imageName
		}
	}
	return slices.Collect(maps.Values(streamMaps))
}

// NewOSImageStreamURLSetFromImageMetadata creates an OSImageStreamSet from image metadata.
// Populates either the OSImage or OSExtensionsImage field based on the image type.
func NewOSImageStreamURLSetFromImageMetadata(imageMetadata *ImageData) *v1alpha1.OSImageStreamSet {
	urlSet := &v1alpha1.OSImageStreamSet{
		Name: imageMetadata.Stream,
	}
	imageName := v1alpha1.ImageDigestFormat(imageMetadata.Image)
	if imageMetadata.Type == ImageTypeOS {
		urlSet.OSImage = imageName
	} else {
		urlSet.OSExtensionsImage = imageName
	}
	return urlSet
}
