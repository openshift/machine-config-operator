package osimagestream

import (
	"context"
	"fmt"

	"github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/imageutils"
	corelisterv1 "k8s.io/client-go/listers/core/v1"
)

// OSImageTuple represents a pair of container images for OS and extensions.
type OSImageTuple struct {
	OSImage           string
	OSExtensionsImage string
}

// OSImagesURLStreamSource fetches OS image stream metadata from specific container image URLs.
type OSImagesURLStreamSource struct {
	urlsProvider         URLsProvider
	imageStreamExtractor ImageDataExtractor
	imageInspector       ImagesInspector
}

// NewOSImagesURLStreamSource creates a new OSImagesURLStreamSource with the provided dependencies.
func NewOSImagesURLStreamSource(urlsProvider URLsProvider, imageStreamExtractor ImageDataExtractor, imageInspector ImagesInspector) *OSImagesURLStreamSource {
	return &OSImagesURLStreamSource{
		urlsProvider:         urlsProvider,
		imageStreamExtractor: imageStreamExtractor,
		imageInspector:       imageInspector,
	}
}

// FetchStreams retrieves and inspects OS and extensions images from the configured URLs,
// returning the extracted OS image stream metadata.
func (s *OSImagesURLStreamSource) FetchStreams(ctx context.Context) ([]*v1alpha1.OSImageStreamSet, error) {
	urls, err := s.urlsProvider.GetUrls()
	if err != nil {
		return nil, err
	}
	inspectionResults, err := s.imageInspector.Inspect(ctx, urls.OSImage, urls.OSExtensionsImage)
	if err != nil {
		return nil, err
	}

	// Validate we got results for both required images
	osInspectResult, err := getInspectResultByImageName(inspectionResults, urls.OSImage)
	if err != nil {
		return nil, fmt.Errorf("error inspecting OS image %w", err)
	}
	osExtensionsInspectResult, err := getInspectResultByImageName(inspectionResults, urls.OSExtensionsImage)
	if err != nil {
		return nil, fmt.Errorf("error inspecting OS extensions image %w", err)
	}

	return GroupOSContainerImageMetadataToStream([]*ImageData{
		s.imageStreamExtractor.GetImageData(osInspectResult.Image, osInspectResult.InspectInfo.Labels),
		s.imageStreamExtractor.GetImageData(osExtensionsInspectResult.Image, osExtensionsInspectResult.InspectInfo.Labels),
	}), nil
}

// getInspectResultByImageName finds the inspection result for a specific image name
// and returns an error if the inspection failed or no result was found.
func getInspectResultByImageName(inspectResults []imageutils.BulkInspectResult, imageName string) (*imageutils.BulkInspectResult, error) {
	for _, result := range inspectResults {
		if imageName == result.Image {
			if result.Error != nil {
				return nil, result.Error
			}
			return &result, nil
		}
	}
	return nil, fmt.Errorf("no inspection result for image %s", imageName)
}

// URLsProvider provides OS and extensions container image URLs.
type URLsProvider interface {
	GetUrls() (*OSImageTuple, error)
}

// ConfigMapURLProvider retrieves OS image URLs from a ConfigMap.
type ConfigMapURLProvider struct {
	cmLister corelisterv1.ConfigMapLister
}

// NewConfigMapURLProviders creates a new ConfigMapURLProvider with the given ConfigMap lister.
func NewConfigMapURLProviders(cmLister corelisterv1.ConfigMapLister) *ConfigMapURLProvider {
	return &ConfigMapURLProvider{cmLister: cmLister}

}

// GetUrls retrieves the OS and extensions image URLs from the machine-config-osimageurl ConfigMap.
func (s *ConfigMapURLProvider) GetUrls() (*OSImageTuple, error) {
	cm, err := s.cmLister.ConfigMaps(common.MCONamespace).Get(common.MachineConfigOSImageURLConfigMapName)
	if err != nil {
		return nil, fmt.Errorf("could not get ConfigMap %s: %w", common.MachineConfigOSImageURLConfigMapName, err)
	}
	osUrls, err := common.ParseOSImageURLConfigMap(cm)
	if err != nil {
		return nil, fmt.Errorf("failed to parse OS Image URLs ConfigMap: %w", err)
	}
	return &OSImageTuple{OSImage: osUrls.BaseOSContainerImage, OSExtensionsImage: osUrls.BaseOSExtensionsContainerImage}, nil
}

// StaticURLProvider returns a fixed set of OS image URLs configured at initialization.
type StaticURLProvider struct {
	osImageTuple OSImageTuple
}

// NewStaticURLProvider creates a new StaticURLProvider with the given OS image tuple.
func NewStaticURLProvider(osImageTuple OSImageTuple) *StaticURLProvider {
	return &StaticURLProvider{osImageTuple: osImageTuple}

}

// GetUrls returns the statically configured OS and extensions image URLs.
func (s *StaticURLProvider) GetUrls() (*OSImageTuple, error) {
	return &s.osImageTuple, nil
}
