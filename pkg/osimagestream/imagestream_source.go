package osimagestream

import (
	"context"
	"regexp"
	"strings"

	imagev1 "github.com/openshift/api/image/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
)

var (
	// imageTagRegxpr matches ImageStream tag names that are considered OS or extensions images.
	// Matches patterns like "rhel-coreos", "stream-coreos", "rhel-coreos-extensions", etc.
	imageTagRegxpr = regexp.MustCompile(`^(rhel|stream)[a-zA-Z0-9.-]*-coreos[a-zA-Z0-9.-]*(-extensions[a-zA-Z0-9.-]*)?$`)
)

// ImageStreamStreamSource fetches OS image stream metadata from an OpenShift ImageStream resource.
type ImageStreamStreamSource struct {
	discoverer          *StreamDiscoverer
	imageStreamProvider ImageStreamProvider
}

// NewImageStreamStreamSource creates a new ImageStreamStreamSource with the provided dependencies.
func NewImageStreamStreamSource(discoverer *StreamDiscoverer, imageStreamProvider ImageStreamProvider) *ImageStreamStreamSource {
	return &ImageStreamStreamSource{discoverer: discoverer, imageStreamProvider: imageStreamProvider}
}

// FetchStreams retrieves an ImageStream, filters relevant OS image tags,
// and delegates discovery to the StreamDiscoverer.
func (r *ImageStreamStreamSource) FetchStreams(ctx context.Context) ([]*mcfgv1.OSImageStreamSet, error) {
	imageStream, err := r.imageStreamProvider.ReadImageStream(ctx)
	if err != nil {
		return nil, err
	}
	osImagesDigests := r.filterImageTag(imageStream)
	return r.discoverer.Discover(ctx, osImagesDigests)
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
