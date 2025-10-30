package osimagestream

import (
	"context"

	"github.com/openshift/api/machineconfiguration/v1alpha1"
)

type CliOSImageStreamParser struct {
	osImageUrl           string
	osExtensionsImageUrl string
}

func NewCliOSImageStreamParser(osImageUrl string, osExtensionsImageUrl string) *CliOSImageStreamParser {
	return &CliOSImageStreamParser{
		osImageUrl:           osImageUrl,
		osExtensionsImageUrl: osExtensionsImageUrl,
	}
}

func (c *CliOSImageStreamParser) FetchStreams(_ context.Context) ([]*v1alpha1.OSImageStreamURLSet, error) {
	return []*v1alpha1.OSImageStreamURLSet{
		{
			Name:                 GetDefaultStreamName(),
			OSExtensionsImageUrl: c.osExtensionsImageUrl,
			OSImageUrl:           c.osImageUrl,
		},
	}, nil
}
