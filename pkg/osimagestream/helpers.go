package osimagestream

import (
	"fmt"

	"github.com/openshift/api/features"
	"github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/version"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
)

// GetStreamSetsNames extracts the names from a slice of OSImageStreamSets.
func GetStreamSetsNames(streamSet []v1alpha1.OSImageStreamSet) []string {
	streams := make([]string, 0)
	for _, stream := range streamSet {
		streams = append(streams, stream.Name)
	}
	return streams
}

// GetOSImageStreamSetByName retrieves an OSImageStreamSet by name from an OSImageStream.
// If name is empty, the default stream is returned. Returns an error if the stream is not found.
func GetOSImageStreamSetByName(osImageStream *v1alpha1.OSImageStream, name string) (*v1alpha1.OSImageStreamSet, error) {
	if osImageStream == nil {
		return nil, fmt.Errorf("requested OSImageStreamSet %s does not exist. OSImageStream cannot be nil", name)
	}
	if name == "" {
		name = osImageStream.Status.DefaultStream
	}

	for _, stream := range osImageStream.Status.AvailableStreams {
		if stream.Name == name {
			return &stream, nil
		}
	}

	return nil, k8serrors.NewNotFound(v1alpha1.GroupVersion.WithResource("osimagestreams").GroupResource(), name)
}

// IsFeatureEnabled checks if the OSImageStream feature is enabled.
// Returns true only if the FeatureGateOSStreams is enabled and the cluster is not running SCOS or FCOS.
func IsFeatureEnabled(fgHandler common.FeatureGatesHandler) bool {
	return fgHandler.Enabled(features.FeatureGateOSStreams) && !version.IsSCOS() && !version.IsFCOS()
}
