package osimagestream

import (
	"fmt"

	v1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/helpers"
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

// TryGetOSImageStreamSetByName retrieves an OSImageStreamSet by name, returning nil if not found.
func TryGetOSImageStreamSetByName(osImageStream *v1alpha1.OSImageStream, name string) *v1alpha1.OSImageStreamSet {
	stream, _ := GetOSImageStreamSetByName(osImageStream, name)
	return stream
}

// TryGetOSImageStreamFromPoolListByPoolName retrieves an OSImageStreamSet for a given pool name,
// returning nil if the pool or stream is not found. For custom pools (non-master, non-arbiter),
// falls back to the worker pool if the custom pool is not found.
func TryGetOSImageStreamFromPoolListByPoolName(osImageStream *v1alpha1.OSImageStream, pools []*v1.MachineConfigPool, poolName string) *v1alpha1.OSImageStreamSet {
	targetPool := helpers.GetPoolByName(pools, poolName)
	if targetPool == nil && (poolName != common.MachineConfigPoolMaster && poolName != common.MachineConfigPoolArbiter) {
		targetPool = helpers.GetPoolByName(pools, common.MachineConfigPoolWorker)
	}
	if targetPool == nil {
		return nil
	}

	return TryGetOSImageStreamSetByName(osImageStream, targetPool.Spec.OSImageStream.Name)
}
