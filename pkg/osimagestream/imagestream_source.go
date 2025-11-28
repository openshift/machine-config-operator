package osimagestream

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	imagev1 "github.com/openshift/api/image/v1"
	"github.com/openshift/api/machineconfiguration/v1alpha1"
	"k8s.io/klog/v2"
)

var (
	// imageTagRegxpr matches ImageStream tag names that are considered OS or extensions images.
	// Matches patterns like "rhel-coreos", "stream-coreos", "rhel-coreos-extensions", etc.
	imageTagRegxpr = regexp.MustCompile(`^(rhel|stream)[a-zA-Z0-9.-]*-coreos[a-zA-Z0-9.-]*(-extensions[a-zA-Z0-9.-]*)?$`)
)

// ImageStreamStreamSource fetches OS image stream metadata from an OpenShift ImageStream resource.
type ImageStreamStreamSource struct {
	imageStreamExtractor ImageDataExtractor
	imageInspector       ImagesInspector
	imageStreamProvider  ImageStreamProvider
}

// NewImageStreamStreamSource creates a new ImageStreamStreamSource with the provided dependencies.
func NewImageStreamStreamSource(imageInspector ImagesInspector, imageStreamProvider ImageStreamProvider, imageStreamExtractor ImageDataExtractor) *ImageStreamStreamSource {
	return &ImageStreamStreamSource{imageInspector: imageInspector, imageStreamProvider: imageStreamProvider, imageStreamExtractor: imageStreamExtractor}
}

// FetchStreams retrieves an ImageStream, filters and inspects relevant OS images,
// and returns the extracted OS image stream metadata.
func (r *ImageStreamStreamSource) FetchStreams(ctx context.Context) ([]*v1alpha1.OSImageStreamSet, error) {
	imageStream, err := r.imageStreamProvider.ReadImageStream(ctx)
	if err != nil {
		return nil, err
	}

	// Filter out the tags to get only the one we consider
	// related to OS/Extensions
	osImagesDigests := r.filterImageTag(imageStream)

	// Get the labels of each OS image
	imagesData, err := r.fetchImagesData(ctx, osImagesDigests)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch ImageStream images data: %w", err)
	}
	return GroupOSContainerImageMetadataToStream(imagesData), nil
}

// fetchImagesData inspects the provided container images and extracts their metadata.
// Images that fail inspection are logged and skipped rather than causing the entire operation to fail.
func (r *ImageStreamStreamSource) fetchImagesData(ctx context.Context, images []string) ([]*ImageData, error) {
	inspectionResults, err := r.imageInspector.Inspect(ctx, images...)
	if err != nil {
		return nil, err
	}
	results := make([]*ImageData, 0)
	for _, inspectionResult := range inspectionResults {
		if inspectionResult.Error != nil {
			klog.Errorf(
				"error inspecting ImageStream Image for OSImageStream generation. %s image will is ignored. %v",
				inspectionResult.Image,
				inspectionResult.Error,
			)
			continue
		}
		imageData := r.imageStreamExtractor.GetImageData(inspectionResult.Image, inspectionResult.InspectInfo.Labels)
		// If imageData is nil: Not a CoreOS versions with the streams labels in place
		if imageData == nil {
			klog.V(4).Infof("image %s does not contain stream labels. Image discarded.", inspectionResult.Image)
			continue
		}
		results = append(results, imageData)
	}
	return results, nil
}

// filterImageTag returns container image references from ImageStream tags that are
// either annotated with the OpenShift OS build source or match the OS/extensions tag pattern.
func (r *ImageStreamStreamSource) filterImageTag(imageStream *imagev1.ImageStream) []string {
	imagesToParse := make([]string, 0)
	for _, tag := range imageStream.Spec.Tags {
		if tag.From == nil || tag.From.Kind != "DockerImage" {
			continue
		}
		if tag.Annotations != nil {
			if source, ok := tag.Annotations["io.openshift.build.source-location"]; ok && strings.Contains(source, "github.com/openshift/os") {
				imagesToParse = append(imagesToParse, tag.From.Name)
				continue
			}
		}
		if imageTagRegxpr.MatchString(tag.Name) {
			imagesToParse = append(imagesToParse, tag.From.Name)
		}
	}
	return imagesToParse
}
