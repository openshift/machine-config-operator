package common

import (
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
)

const (
	machineConfigV0 string = "rendered-machineconfig-v1"
	machineConfigV1 string = "rendered-machineconfig-v2"
	imageV0         string = "registry.host.com/org/repo:tag-1"
	imageV1         string = "registry.host.com/org/repo:tag-2"
)

func newNode(current, desired string) *corev1.Node {
	return helpers.NewNodeBuilder("").WithCurrentConfig(current).WithDesiredConfig(desired).Node()
}

func newLayeredNode(currentConfig, desiredConfig, currentImage, desiredImage string) *corev1.Node {
	nb := helpers.NewNodeBuilder("")
	nb.WithCurrentConfig(currentConfig).WithDesiredConfig(desiredConfig)
	nb.WithCurrentImage(currentImage).WithDesiredImage(desiredImage)
	nb.WithNodeReady()
	return nb.Node()
}

func newMachineConfigPool(currentConfig string) *mcfgv1.MachineConfigPool {
	return helpers.NewMachineConfigPoolBuilder("").WithMachineConfig(currentConfig).MachineConfigPool()
}

func newLayeredMachineConfigPool(currentConfig string) *mcfgv1.MachineConfigPool {
	return helpers.NewMachineConfigPoolBuilder("").WithMachineConfig(currentConfig).WithLayeringEnabled().MachineConfigPool()
}

func newLayeredMachineConfigPoolWithImage(currentConfig, currentImage string) *mcfgv1.MachineConfigPool {
	return helpers.NewMachineConfigPoolBuilder("").WithMachineConfig(currentConfig).WithImage(currentImage).MachineConfigPool()
}

func TestLayeredNodeState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		node *corev1.Node
		pool *mcfgv1.MachineConfigPool

		isDoneAt             bool
		isUnavailable        bool
		isDesiredEqualToPool bool
		layered              bool
	}{
		{
			name:                 "Updated non-layered node",
			node:                 newNode(machineConfigV0, machineConfigV0),
			pool:                 newMachineConfigPool(machineConfigV0),
			isDesiredEqualToPool: true,
			isDoneAt:             true,
			layered:              false,
		},
		{
			name:     "Out-of-date non-layered node",
			node:     newNode(machineConfigV0, machineConfigV0),
			pool:     newMachineConfigPool(machineConfigV1),
			isDoneAt: false,
			layered:  false,
		},
		{
			name:                 "Fully transitioned layered node",
			node:                 newLayeredNode(machineConfigV0, machineConfigV0, imageV0, imageV0),
			pool:                 newLayeredMachineConfigPoolWithImage(machineConfigV0, imageV0),
			isDesiredEqualToPool: true,
			isDoneAt:             true,
			layered:              true,
		},
		{
			name:    "Layered node changes image only",
			node:    newLayeredNode(machineConfigV0, machineConfigV0, imageV0, imageV0),
			pool:    newLayeredMachineConfigPoolWithImage(machineConfigV0, imageV1),
			layered: true,
		},
		{
			name:                 "Layered node changes machineconfigs and image",
			node:                 newLayeredNode(machineConfigV0, machineConfigV1, imageV0, imageV1),
			pool:                 newLayeredMachineConfigPoolWithImage(machineConfigV1, imageV1),
			isUnavailable:        true,
			isDesiredEqualToPool: true,
			layered:              true,
		},
		{
			name:    "Out-of-date layered image",
			node:    newLayeredNode(machineConfigV1, machineConfigV1, imageV0, imageV0),
			pool:    newLayeredMachineConfigPoolWithImage(machineConfigV1, imageV1),
			layered: true,
		},
		{
			name:                 "layered node machineconfig outdated",
			node:                 newLayeredNode(machineConfigV0, machineConfigV1, imageV1, imageV1),
			pool:                 newLayeredMachineConfigPoolWithImage(machineConfigV1, imageV1),
			isUnavailable:        true,
			isDesiredEqualToPool: true,
			layered:              true,
		},
		{
			name: "Node becoming layered should be unavailable",
			node: helpers.NewNodeBuilder("").
				WithEqualConfigs(machineConfigV0).
				WithDesiredImage(imageV0).
				WithMCDState(daemonconsts.MachineConfigDaemonStateWorking).
				WithNodeReady().
				Node(),
			pool:                 newLayeredMachineConfigPoolWithImage(machineConfigV0, imageV0),
			isUnavailable:        true,
			isDesiredEqualToPool: true,
			layered:              true,
		},
		{
			name: "Node becoming layered should be unavailable even if the MCD hasn't started yet",
			node: helpers.NewNodeBuilder("").
				WithEqualConfigs(machineConfigV0).
				WithDesiredImage(imageV0).
				WithMCDState(daemonconsts.MachineConfigDaemonStateDone).
				WithNodeReady().
				Node(),
			pool:                 newLayeredMachineConfigPoolWithImage(machineConfigV0, imageV0),
			isUnavailable:        true,
			isDesiredEqualToPool: true,
			layered:              true,
		},
		{
			name: "Node changing configs should be unavailable",
			node: helpers.NewNodeBuilder("").
				WithConfigs(machineConfigV0, machineConfigV1).
				WithMCDState(daemonconsts.MachineConfigDaemonStateWorking).
				WithNodeReady().
				Node(),
			pool:          newLayeredMachineConfigPool(machineConfigV0),
			isUnavailable: true,
			layered:       true,
		},
		{
			name: "Node changing configs should be unavailable even if the MCD hasn't started yet",
			node: helpers.NewNodeBuilder("").
				WithConfigs(machineConfigV0, machineConfigV1).
				WithMCDState(daemonconsts.MachineConfigDaemonStateDone).
				WithNodeReady().
				Node(),
			pool:          newLayeredMachineConfigPool(machineConfigV0),
			isUnavailable: true,
			layered:       true,
		},
		{
			name: "Node changing images should be unavailable",
			node: helpers.NewNodeBuilder("").
				WithConfigs(machineConfigV0, machineConfigV0).
				WithImages(imageV0, imageV1).
				WithMCDState(daemonconsts.MachineConfigDaemonStateWorking).
				WithNodeReady().
				Node(),
			pool:                 newLayeredMachineConfigPoolWithImage(machineConfigV0, imageV1),
			isUnavailable:        true,
			isDesiredEqualToPool: true,
			layered:              true,
		},
		{
			name: "Node changing images should be unavailable even if the MCD hasn't started yet",
			node: helpers.NewNodeBuilder("").
				WithConfigs(machineConfigV0, machineConfigV0).
				WithImages(imageV0, imageV1).
				WithMCDState(daemonconsts.MachineConfigDaemonStateDone).
				WithNodeReady().
				Node(),
			pool:                 newLayeredMachineConfigPoolWithImage(machineConfigV0, imageV1),
			isUnavailable:        true,
			isDesiredEqualToPool: true,
			layered:              true,
		},
		{
			name: "Degraded node should be unavailable",
			node: helpers.NewNodeBuilder("").
				WithEqualConfigs(machineConfigV0).
				WithMCDState(daemonconsts.MachineConfigDaemonStateDegraded).
				WithNodeReady().
				Node(),
			pool:                 newLayeredMachineConfigPool(machineConfigV0),
			isUnavailable:        true,
			isDesiredEqualToPool: true,
			layered:              true,
		},
		{
			name: "Degraded layered node should be unavailable",
			node: helpers.NewNodeBuilder("").
				WithEqualConfigs(machineConfigV0).
				WithEqualImages(imageV0).
				WithMCDState(daemonconsts.MachineConfigDaemonStateDegraded).
				WithNodeReady().
				Node(),
			pool:                 newLayeredMachineConfigPoolWithImage(machineConfigV0, imageV0),
			isUnavailable:        true,
			isDesiredEqualToPool: true,
			layered:              true,
		},
		{
			name: "Degraded layered node should be unavailable while transitioning images",
			node: helpers.NewNodeBuilder("").
				WithCurrentImage(imageV0).
				WithDesiredImage(imageV1).
				WithMCDState(daemonconsts.MachineConfigDaemonStateDegraded).
				WithNodeReady().
				Node(),
			pool:          newLayeredMachineConfigPoolWithImage(machineConfigV0, imageV1),
			isUnavailable: true,
			layered:       true,
		},
		{
			name: "Rebooting node should be unavailable",
			node: helpers.NewNodeBuilder("").
				WithEqualConfigs(machineConfigV0).
				WithMCDState(daemonconsts.MachineConfigDaemonStateRebooting).
				WithNodeReady().
				Node(),
			pool:                 newLayeredMachineConfigPool(machineConfigV0),
			isUnavailable:        true,
			isDesiredEqualToPool: true,
			layered:              true,
		},
		{
			name: "Rebooting layered node should be unavailable",
			node: helpers.NewNodeBuilder("").
				WithEqualConfigs(machineConfigV0).
				WithEqualImages(imageV0).
				WithMCDState(daemonconsts.MachineConfigDaemonStateRebooting).
				WithNodeReady().
				Node(),
			pool:                 newLayeredMachineConfigPoolWithImage(machineConfigV0, imageV0),
			isUnavailable:        true,
			isDesiredEqualToPool: true,
			layered:              true,
		},
		{
			name: "Unready node should be unavailable",
			node: helpers.NewNodeBuilder("").
				WithEqualConfigs(machineConfigV0).
				WithEqualImages(imageV0).
				WithMCDState(daemonconsts.MachineConfigDaemonStateRebooting).
				WithNodeNotReady().
				Node(),
			pool:                 newLayeredMachineConfigPoolWithImage(machineConfigV0, imageV0),
			isUnavailable:        true,
			isDesiredEqualToPool: true,
			layered:              true,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			lns := NewLayeredNodeState(test.node)

			if test.pool != nil {
				assert.Equal(t, test.isDoneAt, lns.IsDoneAt(test.pool, test.layered), "IsDoneAt()")
				assert.Equal(t, test.isDesiredEqualToPool, lns.IsDesiredEqualToPool(test.pool, test.layered), "IsDesiredEqualToPool()")
				assert.Equal(t, test.isUnavailable, lns.IsUnavailable(test.pool, test.layered), "IsUnavailable()")
			}

			if t.Failed() {
				helpers.DumpNodesAndPools(t, []*corev1.Node{test.node}, []*mcfgv1.MachineConfigPool{test.pool})
			}
		})
	}
}

func TestLayeredNodeStateIsMutated(t *testing.T) {
	tests := []struct {
		name                  string
		pool                  *mcfgv1.MachineConfigPool
		node                  *corev1.Node
		expectedImage         string
		expectedMachineConfig string
		layered               bool
	}{
		{
			name:    "layered node loses desired image because pool is not layered",
			pool:    newMachineConfigPool(machineConfigV0),
			node:    newLayeredNode(machineConfigV0, machineConfigV0, imageV0, imageV0),
			layered: true,
		},
		{
			name: "layered node loses desired image because pool has no image",
			pool: newLayeredMachineConfigPool(machineConfigV0),
			node: newLayeredNode(machineConfigV0, machineConfigV0, imageV0, imageV0),
		},
		{
			name:    "layered node loses desired image because pool has no image and MachineConfig changes",
			pool:    newLayeredMachineConfigPool(machineConfigV1),
			node:    newLayeredNode(machineConfigV0, machineConfigV0, imageV0, imageV0),
			layered: true,
		},
		{
			name:          "unlayered node becomes layered because pool is layered",
			pool:          newLayeredMachineConfigPoolWithImage(machineConfigV0, imageV0),
			node:          newNode(machineConfigV0, machineConfigV0),
			expectedImage: imageV0,
			layered:       false,
		},
		{
			name:    "unlayered node MachineConfig changes",
			pool:    newMachineConfigPool(machineConfigV1),
			node:    newNode(machineConfigV0, machineConfigV0),
			layered: false,
		},
		{
			name:          "layered node image changes",
			pool:          newLayeredMachineConfigPoolWithImage(machineConfigV0, imageV1),
			node:          newLayeredNode(machineConfigV0, machineConfigV0, imageV0, imageV0),
			expectedImage: imageV1,
			layered:       true,
		},
		{
			name:                  "layered node image and MachineConfig changes",
			pool:                  newLayeredMachineConfigPoolWithImage(machineConfigV1, imageV1),
			node:                  newLayeredNode(machineConfigV0, machineConfigV0, imageV0, imageV0),
			expectedImage:         imageV1,
			expectedMachineConfig: machineConfigV1,
			layered:               true,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			lns := NewLayeredNodeState(test.node)
			lns.SetDesiredStateFromPool(test.layered, test.pool)

			updatedNode := lns.Node()

			if test.expectedImage == "" {
				assert.NotContains(t, updatedNode.Annotations, daemonconsts.DesiredImageAnnotationKey)
			} else {
				assert.Equal(t, test.expectedImage, updatedNode.Annotations[daemonconsts.DesiredImageAnnotationKey])
			}

			assert.Equal(t, test.pool.Spec.Configuration.Name, updatedNode.Annotations[daemonconsts.DesiredMachineConfigAnnotationKey])

			// Ensure that the original node and updated node are not the same object
			// nor that they have the same value.
			assert.NotEqual(t, test.node, updatedNode)
			assert.True(t, test.node != updatedNode)
		})
	}
}
