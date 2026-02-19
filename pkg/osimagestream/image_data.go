package osimagestream

import (
	"maps"
	"slices"
	"strings"

	"github.com/openshift/api/machineconfiguration/v1alpha1"
	"k8s.io/klog/v2"
)

const (
	// coreOSLabelStreamClass is the container image label that identifies the OS stream name.
	coreOSLabelStreamClass = "io.openshift.os.streamclass"

	// coreOSLabelExtension is the container image label that identifies an extensions image.
	coreOSLabelExtension      = "io.openshift.os.extensions"
	coreOSLabelExtensionValue = "true"

	// coreOSLabelBootc is the container image label present on bootc-based OS images.
	coreOSLabelBootc             = "containers.bootc"
	coreOSLabelBootcValueBool    = "true"
	coreOSLabelBootcValueInteger = "1"
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

// ImageLabelMatcher is a function that checks whether a set of container image
// labels matches a specific criterion.
type ImageLabelMatcher func(labels map[string]string) bool

// ImageStreamExtractorImpl implements ImageDataExtractor using container image labels
// to identify and classify OS and extensions images.
type ImageStreamExtractorImpl struct {
	streamLabels            []string
	osImageMatchers         []ImageLabelMatcher
	extensionsImageMatchers []ImageLabelMatcher
}

// NewImageStreamExtractor creates a new ImageDataExtractor configured to recognize
// CoreOS images using standard image labels.
func NewImageStreamExtractor() ImageDataExtractor {
	// The type is thought to allow future extra label addition for
	// i.e. Allow a customer to bring their own images with their own labels (defining a selector)
	return &ImageStreamExtractorImpl{
		streamLabels: []string{coreOSLabelStreamClass},
		osImageMatchers: []ImageLabelMatcher{
			labelEquals(coreOSLabelBootc, coreOSLabelBootcValueBool, coreOSLabelBootcValueInteger),
		},
		extensionsImageMatchers: []ImageLabelMatcher{
			labelEquals(coreOSLabelExtension, coreOSLabelExtensionValue),
		},
	}
}

// GetImageData analyzes container image labels to extract OS stream metadata.
// Returns nil if the image does not have the required stream label or if it
// cannot be classified as either an OS or extensions image.
func (e *ImageStreamExtractorImpl) GetImageData(image string, labels map[string]string) *ImageData {
	imageData := &ImageData{
		Image:  image,
		Stream: findLabelValue(labels, e.streamLabels...),
	}
	if imageData.Stream == "" {
		// Not an OS/extensions image
		return nil
	}

	// Determine the type of image. OS or extensions.
	switch {
	case matchesAny(labels, e.osImageMatchers):
		imageData.Type = ImageTypeOS
	case matchesAny(labels, e.extensionsImageMatchers):
		imageData.Type = ImageTypeExtensions
	default:
		return nil
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
	streamMap := make(map[string]*v1alpha1.OSImageStreamSet)
	for _, imageMetadata := range imagesMetadata {
		streamURLSet, exists := streamMap[imageMetadata.Stream]
		if !exists {
			streamMap[imageMetadata.Stream] = NewOSImageStreamURLSetFromImageMetadata(imageMetadata)
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

	// Remove mapped OSImageStreamSet with only one URL. A valid OSImageStreamSet must
	// point to both images.
	// How can a stream end here?
	//  - An ImageStream with only one of the images tagged
	//  - A proper built ImageStream but one of the images is lacking the stream labels
	//  - A URL set with only one image having the labels
	for _, stream := range streamMap {
		if stream.OSExtensionsImage == "" || stream.OSImage == "" {
			delete(streamMap, stream.Name)
		}
	}

	return slices.Collect(maps.Values(streamMap))
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

// labelEquals returns a matcher that checks whether a label has the expected value (case-insensitive).
func labelEquals(key string, values ...string) ImageLabelMatcher {
	return func(labels map[string]string) bool {
		v, ok := labels[key]
		if !ok {
			return false
		}
		for _, value := range values {
			if strings.EqualFold(v, value) {
				return true
			}
		}
		return false
	}
}

// matchesAny returns true if any of the matchers match the given labels.
func matchesAny(labels map[string]string, matchers []ImageLabelMatcher) bool {
	for _, m := range matchers {
		if m(labels) {
			return true
		}
	}
	return false
}
