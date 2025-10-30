package osimagestream

import (
	"context"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/containers/image/v5/types"
	imagev1 "github.com/openshift/api/image/v1"
	"github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/imagepruner"
)

type OSImageUrlType string

const (
	coreOSLabelStream           = "io.coreos.oscontainerimage.osstream"
	coreOSExtensionsLabelStream = "io.coreos.osextensionscontainerimage.osstream"
	OSImageUrlTypeOS            = OSImageUrlType("OS")
	OSImageUrlTypeExtensions    = OSImageUrlType("Extensions")
)

var (
	imageTagRegxpr = regexp.MustCompile(`^(rhel[\w.+-]*|stream)-coreos[\w.+-]*(-extensions[\w.+-]*)?$`)
)

type OSContainerLabelMetadata struct {
	ImageUrl  string
	ImageType OSImageUrlType
	Stream    string
}

func NewOSContainerLabelMetadataFromLabels(image string, labels map[string]string) *OSContainerLabelMetadata {
	if osContainerStream, ok := labels[coreOSLabelStream]; ok && osContainerStream != "" {
		return &OSContainerLabelMetadata{
			ImageUrl:  image,
			ImageType: OSImageUrlTypeOS,
			Stream:    osContainerStream,
		}
	}
	if osExtensionsContainerStream, ok := labels[coreOSExtensionsLabelStream]; ok && osExtensionsContainerStream != "" {
		return &OSContainerLabelMetadata{
			ImageUrl:  image,
			ImageType: OSImageUrlTypeExtensions,
			Stream:    osExtensionsContainerStream,
		}
	}
	return nil
}

func FetchImagesLabels(ctx context.Context, sys *types.SystemContext, images []string) []OSContainerLabelMetadata {
	imageLabels := make([]OSContainerLabelMetadata, 0)
	if len(images) == 0 {
		return imageLabels
	}

	inspector := imagepruner.NewImageInspectorDeleter()

	type labelsInspectionResult struct {
		imageLabels map[string]string
		url         string
		err         error
	}
	results := make(chan labelsInspectionResult, len(images))
	rateLimiterChannel := make(chan struct{}, 5)
	for _, image := range images {
		go func(img string) {
			select {
			case rateLimiterChannel <- struct{}{}:
				defer func() { <-rateLimiterChannel }()

				info, _, err := inspector.ImageInspect(ctx, sys, img)
				if err != nil {
					results <- labelsInspectionResult{err: err}
					return
				}
				results <- labelsInspectionResult{imageLabels: info.Labels, url: img}
			case <-ctx.Done():
				results <- labelsInspectionResult{err: ctx.Err()}
			}
		}(image)
	}

	for range images {
		res := <-results
		if res.err != nil {
			// Best effort, do not return! Just discard the stream
			// TODO Log
			continue
		}
		containerLabelMetadata := NewOSContainerLabelMetadataFromLabels(res.url, res.imageLabels)
		if containerLabelMetadata != nil {
			// TODO Debug log the nil case
			imageLabels = append(imageLabels, *containerLabelMetadata)
		}
	}
	return imageLabels
}

func FilterImageStreamImages(is *imagev1.ImageStream) []string {
	imagesToParse := make([]string, 0)
	for _, tag := range is.Spec.Tags {
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

func GroupOSContainerLabelMetadataToStream(labelMetadatas []OSContainerLabelMetadata) []*v1alpha1.OSImageStreamURLSet {
	streamMaps := make(map[string]*v1alpha1.OSImageStreamURLSet, 0)
	for _, labelMetadata := range labelMetadatas {
		streamUrlSet, exists := streamMaps[labelMetadata.Stream]
		if !exists {
			streamMaps[labelMetadata.Stream] = NewOSImageStreamURLSetFromLabelMetadata(&labelMetadata)
			continue
		}

		// The stream already exits. Maybe it has not both urls yet
		if labelMetadata.ImageType == OSImageUrlTypeOS {
			if streamUrlSet.OSImageUrl != "" && streamUrlSet.OSImageUrl != labelMetadata.ImageUrl {
				// Looks like we have a conflict. Log it and override // todo
			}
			streamUrlSet.OSImageUrl = labelMetadata.ImageUrl
		} else {
			if streamUrlSet.OSExtensionsImageUrl != "" && streamUrlSet.OSExtensionsImageUrl != labelMetadata.ImageUrl {
				// Looks like we have a conflict. Log it and override // todo
			}
			streamUrlSet.OSExtensionsImageUrl = labelMetadata.ImageUrl
		}
	}
	return slices.Collect(maps.Values(streamMaps))
}

func NewOSImageStreamURLSetFromLabelMetadata(metadata *OSContainerLabelMetadata) *v1alpha1.OSImageStreamURLSet {
	urlSet := &v1alpha1.OSImageStreamURLSet{
		Name: metadata.Stream,
	}
	if metadata.ImageType == OSImageUrlTypeOS {
		urlSet.OSImageUrl = metadata.ImageUrl
	} else {
		urlSet.OSExtensionsImageUrl = metadata.ImageUrl
	}
	return urlSet
}

type OSImageStreamParser struct {
	imageStreamProvider ImageStreamProvider
	sysCtx              *types.SystemContext
}

func NewOSImageStreamParser(sysCtx *types.SystemContext, imageStreamProvider ImageStreamProvider) *OSImageStreamParser {
	return &OSImageStreamParser{sysCtx: sysCtx, imageStreamProvider: imageStreamProvider}
}

func (r *OSImageStreamParser) FetchStreams(ctx context.Context) ([]*v1alpha1.OSImageStreamURLSet, error) {
	imageStream, err := r.imageStreamProvider.ReadImageStream(ctx)
	if err != nil {
		return nil, err
	}

	// Filter out the tags to get only the one we consider
	// related to OS/Extensions
	osImagesDigests := FilterImageStreamImages(imageStream)

	// Get the labels of each OS image
	osContainerMetadatas := FetchImagesLabels(ctx, r.sysCtx, osImagesDigests)
	return GroupOSContainerLabelMetadataToStream(osContainerMetadatas), nil
}
