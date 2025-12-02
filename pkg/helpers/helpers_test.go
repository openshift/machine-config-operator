// Assisted-by: Claude
package helpers

import (
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGetPoolByName(t *testing.T) {
	masterPool := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "master"},
	}

	workerPool := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
	}

	customPool := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "custom"},
	}

	tests := []struct {
		name     string
		pools    []*mcfgv1.MachineConfigPool
		poolName string
		expected *mcfgv1.MachineConfigPool
	}{
		{
			name:     "find master pool",
			pools:    []*mcfgv1.MachineConfigPool{masterPool, workerPool, customPool},
			poolName: "master",
			expected: masterPool,
		},
		{
			name:     "find worker pool",
			pools:    []*mcfgv1.MachineConfigPool{masterPool, workerPool, customPool},
			poolName: "worker",
			expected: workerPool,
		},
		{
			name:     "find custom pool",
			pools:    []*mcfgv1.MachineConfigPool{masterPool, workerPool, customPool},
			poolName: "custom",
			expected: customPool,
		},
		{
			name:     "pool not found",
			pools:    []*mcfgv1.MachineConfigPool{masterPool, workerPool},
			poolName: "non-existent",
			expected: nil,
		},
		{
			name:     "empty pools list",
			pools:    []*mcfgv1.MachineConfigPool{},
			poolName: "master",
			expected: nil,
		},
		{
			name:     "nil pools list",
			pools:    nil,
			poolName: "master",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetPoolByName(tt.pools, tt.poolName)
			assert.Equal(t, tt.expected, result)
		})
	}
}
