// Assisted-by: Claude
package osimagestream

import (
	"testing"

	v1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGetStreamSetsNames(t *testing.T) {
	tests := []struct {
		name     string
		input    []v1alpha1.OSImageStreamSet
		expected []string
	}{
		{
			name:     "empty slice",
			input:    []v1alpha1.OSImageStreamSet{},
			expected: []string{},
		},
		{
			name: "single stream",
			input: []v1alpha1.OSImageStreamSet{
				{Name: "rhel-9"},
			},
			expected: []string{"rhel-9"},
		},
		{
			name: "multiple streams",
			input: []v1alpha1.OSImageStreamSet{
				{Name: "rhel-9"},
				{Name: "rhel-10"},
				{Name: "custom-stream"},
			},
			expected: []string{"rhel-9", "rhel-10", "custom-stream"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetStreamSetsNames(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetOSImageStreamSetByName(t *testing.T) {
	osImageStream := &v1alpha1.OSImageStream{
		Status: v1alpha1.OSImageStreamStatus{
			DefaultStream: "rhel-9",
			AvailableStreams: []v1alpha1.OSImageStreamSet{
				{Name: "rhel-9", OSImage: "image1", OSExtensionsImage: "ext1"},
				{Name: "rhel-10", OSImage: "image2", OSExtensionsImage: "ext2"},
			},
		},
	}

	tests := []struct {
		name          string
		osImageStream *v1alpha1.OSImageStream
		streamName    string
		expected      *v1alpha1.OSImageStreamSet
		errorContains string
	}{
		{
			name:          "find existing stream",
			osImageStream: osImageStream,
			streamName:    "rhel-9",
			expected:      &v1alpha1.OSImageStreamSet{Name: "rhel-9", OSImage: "image1", OSExtensionsImage: "ext1"},
		},
		{
			name:          "find another existing stream",
			osImageStream: osImageStream,
			streamName:    "rhel-10",
			expected:      &v1alpha1.OSImageStreamSet{Name: "rhel-10", OSImage: "image2", OSExtensionsImage: "ext2"},
		},
		{
			name:          "empty name returns default stream",
			osImageStream: osImageStream,
			streamName:    "",
			expected:      &v1alpha1.OSImageStreamSet{Name: "rhel-9", OSImage: "image1", OSExtensionsImage: "ext1"},
		},
		{
			name:          "non-existent stream",
			osImageStream: osImageStream,
			streamName:    "non-existent",
			errorContains: "does not exist",
		},
		{
			name:          "nil osImageStream",
			osImageStream: nil,
			streamName:    "rhel-9",
			errorContains: "cannot be nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := GetOSImageStreamSetByName(tt.osImageStream, tt.streamName)
			if tt.errorContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestTryGetOSImageStreamSetByName(t *testing.T) {
	osImageStream := &v1alpha1.OSImageStream{
		Status: v1alpha1.OSImageStreamStatus{
			DefaultStream: "rhel-9",
			AvailableStreams: []v1alpha1.OSImageStreamSet{
				{Name: "rhel-9", OSImage: "image1", OSExtensionsImage: "ext1"},
				{Name: "rhel-10", OSImage: "image2", OSExtensionsImage: "ext2"},
			},
		},
	}

	tests := []struct {
		name          string
		osImageStream *v1alpha1.OSImageStream
		streamName    string
		expected      *v1alpha1.OSImageStreamSet
	}{
		{
			name:          "find existing stream",
			osImageStream: osImageStream,
			streamName:    "rhel-9",
			expected:      &v1alpha1.OSImageStreamSet{Name: "rhel-9", OSImage: "image1", OSExtensionsImage: "ext1"},
		},
		{
			name:          "non-existent stream returns nil",
			osImageStream: osImageStream,
			streamName:    "non-existent",
			expected:      nil,
		},
		{
			name:          "nil osImageStream returns nil",
			osImageStream: nil,
			streamName:    "rhel-9",
			expected:      nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := TryGetOSImageStreamSetByName(tt.osImageStream, tt.streamName)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTryGetOSImageStreamFromPoolListByPoolName(t *testing.T) {
	osImageStream := &v1alpha1.OSImageStream{
		Status: v1alpha1.OSImageStreamStatus{
			DefaultStream: "stream-master",
			AvailableStreams: []v1alpha1.OSImageStreamSet{
				{Name: "stream-master", OSImage: "image1", OSExtensionsImage: "ext1"},
				{Name: "stream-worker", OSImage: "image2", OSExtensionsImage: "ext2"},
				{Name: "stream-arbiter", OSImage: "image3", OSExtensionsImage: "ext3"},
				{Name: "stream-custom", OSImage: "image4", OSExtensionsImage: "ext4"},
			},
		},
	}

	masterPool := &v1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: common.MachineConfigPoolMaster},
		Spec: v1.MachineConfigPoolSpec{
			OSImageStream: v1.OSImageStreamReference{Name: "stream-master"},
		},
	}

	workerPool := &v1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: common.MachineConfigPoolWorker},
		Spec: v1.MachineConfigPoolSpec{
			OSImageStream: v1.OSImageStreamReference{Name: "stream-worker"},
		},
	}

	arbiterPool := &v1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: common.MachineConfigPoolArbiter},
		Spec: v1.MachineConfigPoolSpec{
			OSImageStream: v1.OSImageStreamReference{Name: "stream-arbiter"},
		},
	}

	customPool := &v1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "custom"},
		Spec: v1.MachineConfigPoolSpec{
			OSImageStream: v1.OSImageStreamReference{Name: "stream-custom"},
		},
	}

	tests := []struct {
		name          string
		osImageStream *v1alpha1.OSImageStream
		pools         []*v1.MachineConfigPool
		poolName      string
		expected      *v1alpha1.OSImageStreamSet
	}{
		{
			name:          "find stream for master pool",
			osImageStream: osImageStream,
			pools:         []*v1.MachineConfigPool{masterPool, workerPool},
			poolName:      common.MachineConfigPoolMaster,
			expected:      &v1alpha1.OSImageStreamSet{Name: "stream-master", OSImage: "image1", OSExtensionsImage: "ext1"},
		},
		{
			name:          "find stream for worker pool",
			osImageStream: osImageStream,
			pools:         []*v1.MachineConfigPool{masterPool, workerPool},
			poolName:      common.MachineConfigPoolWorker,
			expected:      &v1alpha1.OSImageStreamSet{Name: "stream-worker", OSImage: "image2", OSExtensionsImage: "ext2"},
		},
		{
			name:          "find stream for arbiter pool",
			osImageStream: osImageStream,
			pools:         []*v1.MachineConfigPool{masterPool, workerPool, arbiterPool},
			poolName:      common.MachineConfigPoolArbiter,
			expected:      &v1alpha1.OSImageStreamSet{Name: "stream-arbiter", OSImage: "image3", OSExtensionsImage: "ext3"},
		},
		{
			name:          "find stream for custom pool",
			osImageStream: osImageStream,
			pools:         []*v1.MachineConfigPool{masterPool, workerPool, customPool},
			poolName:      "custom",
			expected:      &v1alpha1.OSImageStreamSet{Name: "stream-custom", OSImage: "image4", OSExtensionsImage: "ext4"},
		},
		{
			name:          "custom pool not found - fallback to worker",
			osImageStream: osImageStream,
			pools:         []*v1.MachineConfigPool{masterPool, workerPool},
			poolName:      "non-existent-custom",
			expected:      &v1alpha1.OSImageStreamSet{Name: "stream-worker", OSImage: "image2", OSExtensionsImage: "ext2"},
		},
		{
			name:          "master pool not found - no fallback",
			osImageStream: osImageStream,
			pools:         []*v1.MachineConfigPool{workerPool},
			poolName:      common.MachineConfigPoolMaster,
			expected:      nil,
		},
		{
			name:          "arbiter pool not found - no fallback",
			osImageStream: osImageStream,
			pools:         []*v1.MachineConfigPool{masterPool, workerPool},
			poolName:      common.MachineConfigPoolArbiter,
			expected:      nil,
		},
		{
			name:          "worker pool not found - no fallback",
			osImageStream: osImageStream,
			pools:         []*v1.MachineConfigPool{masterPool},
			poolName:      common.MachineConfigPoolWorker,
			expected:      nil,
		},
		{
			name:          "pool found but stream not in osImageStream",
			osImageStream: osImageStream,
			pools: []*v1.MachineConfigPool{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "custom"},
					Spec: v1.MachineConfigPoolSpec{
						OSImageStream: v1.OSImageStreamReference{Name: "non-existent-stream"},
					},
				},
				workerPool,
			},
			poolName: "custom",
			expected: nil,
		},
		{
			name:          "nil osImageStream",
			osImageStream: nil,
			pools:         []*v1.MachineConfigPool{masterPool, workerPool},
			poolName:      common.MachineConfigPoolMaster,
			expected:      nil,
		},
		{
			name:          "empty pools list",
			osImageStream: osImageStream,
			pools:         []*v1.MachineConfigPool{},
			poolName:      common.MachineConfigPoolMaster,
			expected:      nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := TryGetOSImageStreamFromPoolListByPoolName(tt.osImageStream, tt.pools, tt.poolName)
			assert.Equal(t, tt.expected, result)
		})
	}
}
