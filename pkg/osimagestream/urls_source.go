package osimagestream

import (
	"context"
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/common"
	corelisterv1 "k8s.io/client-go/listers/core/v1"
)

// OSImageTuple represents a pair of container images for OS and extensions.
type OSImageTuple struct {
	OSImage           string
	OSExtensionsImage string
}

// OSImagesURLStreamSource fetches OS image stream metadata from specific container image URLs.
type OSImagesURLStreamSource struct {
	urlsProvider URLsProvider
	discoverer   *StreamDiscoverer
}

// NewOSImagesURLStreamSource creates a new OSImagesURLStreamSource with the provided dependencies.
func NewOSImagesURLStreamSource(urlsProvider URLsProvider, discoverer *StreamDiscoverer) *OSImagesURLStreamSource {
	return &OSImagesURLStreamSource{
		urlsProvider: urlsProvider,
		discoverer:   discoverer,
	}
}

// FetchStreams retrieves OS and extensions image URLs from the configured provider
// and delegates discovery to the StreamDiscoverer.
func (s *OSImagesURLStreamSource) FetchStreams(ctx context.Context) ([]*mcfgv1.OSImageStreamSet, error) {
	urls, err := s.urlsProvider.GetUrls()
	if err != nil {
		return nil, err
	}
	return s.discoverer.Discover(ctx, []string{urls.OSImage, urls.OSExtensionsImage})
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
