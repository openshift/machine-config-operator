package osimagestream

import "github.com/openshift/api/machineconfiguration/v1alpha1"

func GetStreamSetsNames(streamSet []v1alpha1.OSImageStreamSet) []string {
	streams := make([]string, 0)
	for _, stream := range streamSet {
		streams = append(streams, stream.Name)
	}
	return streams
}
