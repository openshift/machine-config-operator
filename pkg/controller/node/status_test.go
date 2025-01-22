package node

import (
	"fmt"
	"reflect"
	"testing"

	features "github.com/openshift/api/features"
	mcfgalphav1 "github.com/openshift/api/machineconfiguration/v1alpha1"

	apicfgv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIsNodeReady(t *testing.T) {
	nodeList := &corev1.NodeList{
		Items: []corev1.Node{
			// node1 considered
			{ObjectMeta: metav1.ObjectMeta{Name: "node1"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}},
			// node2 ignored - node not Ready
			{ObjectMeta: metav1.ObjectMeta{Name: "node2"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}}},
			// node3 ignored - node out of disk
			{ObjectMeta: metav1.ObjectMeta{Name: "node3"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue}}}},
			// node4 considered
			{ObjectMeta: metav1.ObjectMeta{Name: "node4"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeDiskPressure, Status: corev1.ConditionFalse}}}},

			// node5 ignored - node out of disk
			{ObjectMeta: metav1.ObjectMeta{Name: "node5"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}, {Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue}}}},
			// node6 considered
			{ObjectMeta: metav1.ObjectMeta{Name: "node6"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}, {Type: corev1.NodeDiskPressure, Status: corev1.ConditionFalse}}}},
			// node7 ignored - node out of disk, node not Ready
			{ObjectMeta: metav1.ObjectMeta{Name: "node7"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}, {Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue}}}},
			// node8 ignored - node not Ready
			{ObjectMeta: metav1.ObjectMeta{Name: "node8"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}, {Type: corev1.NodeDiskPressure, Status: corev1.ConditionFalse}}}},

			// node9 ignored - node unschedulable
			{ObjectMeta: metav1.ObjectMeta{Name: "node9"}, Spec: corev1.NodeSpec{Unschedulable: true}},
			// node10 considered
			{ObjectMeta: metav1.ObjectMeta{Name: "node10"}, Spec: corev1.NodeSpec{Unschedulable: false}},
			// node11 considered
			{ObjectMeta: metav1.ObjectMeta{Name: "node11"}},
		},
	}

	nodeNames := []string{}
	for _, node := range nodeList.Items {
		if isNodeReady(&node) {
			nodeNames = append(nodeNames, node.Name)
		}
	}
	expectedNodes := []string{"node1", "node4", "node6", "node10", "node11"}
	if !reflect.DeepEqual(expectedNodes, nodeNames) {
		t.Errorf("expected: %v, got %v", expectedNodes, nodeNames)
	}
}

func newLayeredNode(name string, currentConfig, desiredConfig, currentImage, desiredImage string) *corev1.Node {
	nb := helpers.NewNodeBuilder(name)
	nb.WithCurrentConfig(currentConfig)
	nb.WithDesiredConfig(desiredConfig)
	nb.WithCurrentImage(currentImage)
	nb.WithDesiredImage(desiredImage)
	return nb.Node()
}

func newNode(name string, currentConfig, desiredConfig string) *corev1.Node {
	nb := helpers.NewNodeBuilder(name)
	nb.WithCurrentConfig(currentConfig)
	nb.WithDesiredConfig(desiredConfig)
	return nb.Node()
}

func newNodeWithLabels(name string, labels map[string]string) *corev1.Node {
	return helpers.NewNodeBuilder(name).WithLabels(labels).Node()
}

func newNodeWithAnnotations(name string, annotations map[string]string) *corev1.Node {
	return helpers.NewNodeBuilder(name).WithAnnotations(annotations).Node()
}

func newLayeredNodeWithLabel(name string, currentConfig, desiredConfig, currentImage, desiredImage string, labels map[string]string) *corev1.Node {
	nb := helpers.NewNodeBuilder(name)
	nb.WithCurrentConfig(currentConfig)
	nb.WithDesiredConfig(desiredConfig)
	nb.WithCurrentImage(currentImage)
	nb.WithDesiredImage(desiredImage)
	nb.WithLabels(labels)
	return nb.Node()
}

func newNodeWithLabel(name string, currentConfig, desiredConfig string, labels map[string]string) *corev1.Node {
	nb := helpers.NewNodeBuilder(name)
	nb.WithCurrentConfig(currentConfig)
	nb.WithDesiredConfig(desiredConfig)
	nb.WithLabels(labels)
	return nb.Node()
}

func newLayeredNodeWithReady(name string, currentConfig, desiredConfig, currentImage, desiredImage string, status corev1.ConditionStatus) *corev1.Node {
	nb := helpers.NewNodeBuilder(name)
	nb.WithCurrentConfig(currentConfig)
	nb.WithDesiredConfig(desiredConfig)
	nb.WithCurrentImage(currentImage)
	nb.WithDesiredImage(desiredImage)
	nb.WithStatus(corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: status}}})
	return nb.Node()
}

func newNodeWithReady(name string, currentConfig, desiredConfig string, status corev1.ConditionStatus) *corev1.Node {
	nb := helpers.NewNodeBuilder(name)
	nb.WithCurrentConfig(currentConfig)
	nb.WithDesiredConfig(desiredConfig)
	nb.WithStatus(corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: status}}})
	return nb.Node()
}

func newNodeWithDaemonState(name string, currentConfig, desiredConfig, dstate string) *corev1.Node {
	nb := helpers.NewNodeBuilder(name)
	nb.WithConfigs(currentConfig, desiredConfig)
	nb.WithMCDState(dstate)
	return nb.Node()
}

func newNodeWithReadyAndDaemonState(name string, currentConfig, desiredConfig string, status corev1.ConditionStatus, dstate string) *corev1.Node {
	nb := helpers.NewNodeBuilder(name)
	nb.WithCurrentConfig(currentConfig)
	nb.WithDesiredConfig(desiredConfig)
	nb.WithStatus(corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: status}}})
	nb.WithMCDState(dstate)
	return nb.Node()
}

func TestGetUpdatedMachines(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		nodes         []*corev1.Node
		currentConfig string
		currentImage  string
		updated       []*corev1.Node
		layeredPool   bool
	}{{
		name:          "no nodes",
		nodes:         []*corev1.Node{},
		currentConfig: machineConfigV1,
		updated:       nil,
	}, {
		name: "1 node updated, 1 updating, 1 not acted upon",
		nodes: []*corev1.Node{
			newNode("node-0", machineConfigV0, machineConfigV0),
			newNode("node-1", machineConfigV1, machineConfigV1),
			newNode("node-2", machineConfigV0, machineConfigV1),
		},
		currentConfig: machineConfigV1,
		updated:       []*corev1.Node{newNode("node-1", machineConfigV1, machineConfigV1)},
	}, {
		name: "2 node updated, 1 updating",
		nodes: []*corev1.Node{
			newNode("node-0", machineConfigV0, machineConfigV1),
			newNode("node-1", machineConfigV1, machineConfigV1),
			newNode("node-2", machineConfigV1, machineConfigV1),
		},
		currentConfig: machineConfigV1,
		updated:       []*corev1.Node{newNode("node-1", machineConfigV1, machineConfigV1), newNode("node-2", machineConfigV1, machineConfigV1)},
	}, {
		name: "2 node updated, 1 updating, but one updated node is NotReady",
		nodes: []*corev1.Node{
			newNode("node-0", machineConfigV0, machineConfigV1),
			newNode("node-1", machineConfigV1, machineConfigV1),
			newNodeWithReady("node-2", machineConfigV1, machineConfigV1, corev1.ConditionFalse),
		},
		currentConfig: machineConfigV1,
		updated:       []*corev1.Node{newNode("node-1", machineConfigV1, machineConfigV1), newNodeWithReady("node-2", machineConfigV1, machineConfigV1, corev1.ConditionFalse)},
	}, {
		name: "1 layered node updated, 1 updating, 1 not acted upon",
		nodes: []*corev1.Node{
			newLayeredNode("node-0", machineConfigV0, machineConfigV0, imageV0, imageV0),
			newLayeredNode("node-1", machineConfigV1, machineConfigV1, imageV1, imageV1),
			newLayeredNode("node-2", machineConfigV0, machineConfigV1, imageV0, imageV1),
		},
		currentConfig: machineConfigV1,
		currentImage:  imageV1,
		updated: []*corev1.Node{
			newLayeredNode("node-1", machineConfigV1, machineConfigV1, imageV1, imageV1),
		},
	}, {
		name: "2 layered nodes updated, 1 updating MachineConfig",
		nodes: []*corev1.Node{
			newLayeredNode("node-0", machineConfigV0, machineConfigV1, imageV1, imageV1),
			newLayeredNode("node-1", machineConfigV1, machineConfigV1, imageV1, imageV1),
			newLayeredNode("node-2", machineConfigV1, machineConfigV1, imageV1, imageV1),
		},
		currentConfig: machineConfigV1,
		currentImage:  imageV1,
		updated: []*corev1.Node{
			newLayeredNode("node-1", machineConfigV1, machineConfigV1, imageV1, imageV1),
			newLayeredNode("node-2", machineConfigV1, machineConfigV1, imageV1, imageV1),
		},
	}, {
		name: "2 layered nodes updated, 1 updating image",
		nodes: []*corev1.Node{
			newLayeredNode("node-0", machineConfigV1, machineConfigV1, imageV0, imageV1),
			newLayeredNode("node-1", machineConfigV1, machineConfigV1, imageV1, imageV1),
			newLayeredNode("node-2", machineConfigV1, machineConfigV1, imageV1, imageV1),
		},
		currentConfig: machineConfigV1,
		currentImage:  imageV1,
		updated: []*corev1.Node{
			newLayeredNode("node-1", machineConfigV1, machineConfigV1, imageV1, imageV1),
			newLayeredNode("node-2", machineConfigV1, machineConfigV1, imageV1, imageV1),
		},
	}, {
		name: "2 layered nodes updated, 1 updating, but one updated node is NotReady",
		nodes: []*corev1.Node{
			newLayeredNode("node-0", machineConfigV0, machineConfigV1, imageV0, imageV1),
			newLayeredNode("node-1", machineConfigV1, machineConfigV1, imageV1, imageV1),
			newLayeredNodeWithReady("node-2", machineConfigV1, machineConfigV1, imageV1, imageV1, corev1.ConditionFalse),
		},
		currentConfig: machineConfigV1,
		currentImage:  imageV1,
		updated: []*corev1.Node{
			newLayeredNode("node-1", machineConfigV1, machineConfigV1, imageV1, imageV1),
			newLayeredNodeWithReady("node-2", machineConfigV1, machineConfigV1, imageV1, imageV1, corev1.ConditionFalse),
		},
	}, {
		name: "Layered pool with unlayered nodes",
		nodes: []*corev1.Node{
			newLayeredNode("node-0", machineConfigV1, machineConfigV1, imageV1, imageV1),
			newLayeredNode("node-1", machineConfigV1, machineConfigV1, imageV1, imageV1),
			newLayeredNodeWithReady("node-2", machineConfigV1, machineConfigV1, imageV1, imageV1, corev1.ConditionTrue),
			newNode("node-3", machineConfigV0, machineConfigV0),
		},
		currentConfig: machineConfigV1,
		currentImage:  imageV1,
		updated: []*corev1.Node{
			newLayeredNode("node-0", machineConfigV1, machineConfigV1, imageV1, imageV1),
			newLayeredNode("node-1", machineConfigV1, machineConfigV1, imageV1, imageV1),
			newLayeredNodeWithReady("node-2", machineConfigV1, machineConfigV1, imageV1, imageV1, corev1.ConditionTrue),
		},
	}, {
		name: "Unlayered pool with 1 layered node",
		nodes: []*corev1.Node{
			newNode("node-0", machineConfigV1, machineConfigV1),
			newNode("node-1", machineConfigV1, machineConfigV1),
			newNodeWithReady("node-2", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
			newLayeredNode("node-3", machineConfigV0, machineConfigV0, imageV1, imageV1),
		},
		currentConfig: machineConfigV1,
		updated: []*corev1.Node{
			newNode("node-0", machineConfigV1, machineConfigV1),
			newNode("node-1", machineConfigV1, machineConfigV1),
			newNodeWithReady("node-2", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
		},
	},
	}

	for _, test := range tests {
		test := test

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			poolBuilder := helpers.NewMachineConfigPoolBuilder("").WithMachineConfig(test.currentConfig)
			if test.layeredPool {
				poolBuilder.WithLayeringEnabled()
			}

			if test.currentImage != "" {
				poolBuilder.WithImage(test.currentImage)
			}

			pool := poolBuilder.MachineConfigPool()

			updated := getUpdatedMachines(pool, test.nodes, nil, nil, test.layeredPool)
			assertExpectedNodes(t, getNamesFromNodes(test.updated), updated)

			// This is a much tighter assertion than the one I added. Not sure if
			// it's strictly required or not, so I'll leave it for now.
			if !reflect.DeepEqual(updated, test.updated) {
				t.Fatalf("mismatch expected: %v got %v", test.updated, updated)
			}

			if t.Failed() {
				helpers.DumpNodesAndPools(t, test.nodes, []*mcfgv1.MachineConfigPool{pool})
			}
		})
	}
}

func TestGetReadyMachines(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		nodes         []*corev1.Node
		currentConfig string
		currentImage  string
		ready         []*corev1.Node
		layered       bool
	}{{
		name:          "no nodes",
		nodes:         []*corev1.Node{},
		currentConfig: machineConfigV1,
		ready:         nil,
	}, {
		name: "1 node updated, 1 updating, 1 not acted upon",
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			newNodeWithReady("node-1", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
			newNodeWithReady("node-2", machineConfigV0, machineConfigV1, corev1.ConditionFalse),
		},
		currentConfig: machineConfigV1,
		ready:         []*corev1.Node{newNodeWithReady("node-1", machineConfigV1, machineConfigV1, corev1.ConditionTrue)},
	}, {
		name: "2 node updated, 1 updating",
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
			newNodeWithReady("node-1", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
			newNodeWithReady("node-2", machineConfigV1, machineConfigV1, corev1.ConditionFalse),
		},
		currentConfig: machineConfigV1,
		ready:         []*corev1.Node{newNodeWithReady("node-1", machineConfigV1, machineConfigV1, corev1.ConditionTrue)},
	}, {
		name: "2 node updated, 1 updating",
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
			newNodeWithReady("node-1", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
			newNodeWithReady("node-2", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
		},
		currentConfig: machineConfigV1,
		ready:         []*corev1.Node{newNodeWithReady("node-1", machineConfigV1, machineConfigV1, corev1.ConditionTrue), newNodeWithReady("node-2", machineConfigV1, machineConfigV1, corev1.ConditionTrue)},
	}, {
		name: "2 node updated, 1 updating, but one updated node is NotReady",
		nodes: []*corev1.Node{
			newNode("node-0", machineConfigV0, machineConfigV1),
			newNode("node-1", machineConfigV1, machineConfigV1),
			newNodeWithReady("node-2", machineConfigV1, machineConfigV1, corev1.ConditionFalse),
		},
		currentConfig: machineConfigV1,
		ready:         []*corev1.Node{newNode("node-1", machineConfigV1, machineConfigV1)},
	}, {
		name: "2 layered nodes updated, 1 updating, but one updated node is NotReady",
		nodes: []*corev1.Node{
			newLayeredNode("node-0", machineConfigV0, machineConfigV1, imageV0, imageV1),
			newLayeredNode("node-1", machineConfigV1, machineConfigV1, imageV1, imageV1),
			newLayeredNodeWithReady("node-2", machineConfigV1, machineConfigV1, imageV1, imageV1, corev1.ConditionFalse),
		},
		currentConfig: machineConfigV1,
		currentImage:  imageV1,
		ready:         []*corev1.Node{newLayeredNode("node-1", machineConfigV1, machineConfigV1, imageV1, imageV1)},
		layered:       true,
	}, {
		name: "2 layered nodes updated, one node has layering mismatch",
		nodes: []*corev1.Node{
			newLayeredNode("node-0", machineConfigV0, machineConfigV1, imageV0, imageV1),
			newLayeredNode("node-1", machineConfigV1, machineConfigV1, imageV1, imageV1),
			newNode("node-2", machineConfigV0, machineConfigV0),
		},
		currentConfig: machineConfigV1,
		currentImage:  imageV1,
		ready: []*corev1.Node{
			newLayeredNode("node-1", machineConfigV1, machineConfigV1, imageV1, imageV1),
		},
		layered: true,
	},
		{
			name: "2 nodes updated, one node has layering mismatch",
			nodes: []*corev1.Node{
				newLayeredNode("node-0", machineConfigV0, machineConfigV1, imageV0, imageV1),
				newNode("node-1", machineConfigV1, machineConfigV1),
				newNode("node-2", machineConfigV0, machineConfigV0),
				newLayeredNode("node-3", machineConfigV1, machineConfigV1, imageV1, imageV1),
			},
			currentConfig: machineConfigV1,
			ready: []*corev1.Node{
				newNode("node-1", machineConfigV1, machineConfigV1),
			},
			layered: true,
		},
	}

	for _, test := range tests {
		test := test

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			// If we declare a current image for our pool, the pool must be layered.
			poolBuilder := helpers.NewMachineConfigPoolBuilder("").WithMachineConfig(test.currentConfig)
			if test.currentImage != "" {
				poolBuilder.WithImage(test.currentImage)
			}

			pool := poolBuilder.MachineConfigPool()

			ready := getReadyMachines(pool, test.nodes, nil, nil, test.layered)
			if !reflect.DeepEqual(ready, test.ready) {
				t.Fatalf("mismatch expected: %v got %v", test.ready, ready)
			}
		})
	}
}

func (ctrl *Controller) TestGetUnavailableMachines(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                 string
		nodes                []*corev1.Node
		unavail              []string
		layeredPool          bool
		layeredPoolWithImage bool
	}{{
		name:    "no nodes",
		nodes:   []*corev1.Node{},
		unavail: nil,
	}, {
		name: "1 in progress",
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			newNodeWithReady("node-1", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
			newNodeWithReady("node-2", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
		},
		unavail: []string{"node-2"},
	}, {
		name: "1 unavail, 1 in progress",
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			newNodeWithReady("node-1", machineConfigV1, machineConfigV1, corev1.ConditionFalse),
			newNodeWithReady("node-2", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
		},
		unavail: []string{"node-1", "node-2"},
	}, {
		name: "1 node updated, 1 updating, 1 updating but not v2 and is ready",
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
			newNodeWithReady("node-1", machineConfigV2, machineConfigV2, corev1.ConditionTrue),
			newNodeWithReady("node-2", machineConfigV0, machineConfigV2, corev1.ConditionTrue),
		},
		unavail: []string{"node-0", "node-2"},
	}, {
		name: "1 node updated, 1 updating, 1 updating but not v2 and is not ready",
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionFalse),
			newNodeWithReady("node-1", machineConfigV2, machineConfigV2, corev1.ConditionTrue),
			newNodeWithReady("node-2", machineConfigV0, machineConfigV2, corev1.ConditionTrue),
		},
		unavail: []string{"node-0", "node-2"},
	}, {
		name: "2 node updated, 1 updating",
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
			newNodeWithReady("node-1", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
			newNodeWithReady("node-2", machineConfigV1, machineConfigV1, corev1.ConditionFalse),
		},
		unavail: []string{"node-0", "node-2"},
	}, {
		name: "2 node updated, 1 updating, but one updated node is NotReady",
		nodes: []*corev1.Node{
			newNode("node-0", machineConfigV0, machineConfigV1),
			newNode("node-1", machineConfigV1, machineConfigV1),
			newNodeWithReady("node-2", machineConfigV1, machineConfigV1, corev1.ConditionFalse),
		},
		unavail: []string{"node-0", "node-2"},
	}, {
		name: "2 node updated, 1 updating, but one updated node is NotReady",
		nodes: []*corev1.Node{
			newNode("node-0", machineConfigV0, machineConfigV1),
			newNode("node-1", machineConfigV1, machineConfigV1),
			newNodeWithReady("node-2", machineConfigV1, machineConfigV1, corev1.ConditionFalse),
		},
		unavail: []string{"node-0", "node-2"},
	}, {
		name: "1 layered node updated, 1 updating, but one updated node is NotReady",
		nodes: []*corev1.Node{
			helpers.NewNodeBuilder("node-0").WithConfigs(machineConfigV0, machineConfigV1).WithImages(imageV0, imageV1).Node(),
			helpers.NewNodeBuilder("node-1").WithEqualConfigsAndImages(machineConfigV1, imageV1).Node(),
			helpers.NewNodeBuilder("node-2").WithEqualConfigsAndImages(machineConfigV1, imageV1).WithNodeNotReady().Node(),
		},
		unavail:              []string{"node-0", "node-2"},
		layeredPoolWithImage: true,
	}, {
		name: "Mismatched unlayered node and layered pool with image available",
		nodes: []*corev1.Node{
			helpers.NewNodeBuilder("node-0").WithConfigs(machineConfigV0, machineConfigV1).WithImages(imageV0, imageV1).Node(),
			helpers.NewNodeBuilder("node-1").WithEqualConfigsAndImages(machineConfigV1, imageV1).Node(),
			helpers.NewNodeBuilder("node-2").WithEqualConfigsAndImages(machineConfigV1, imageV1).WithNodeNotReady().Node(),
			helpers.NewNodeBuilder("node-3").WithEqualConfigs(machineConfigV0).WithNodeNotReady().Node(),
			helpers.NewNodeBuilder("node-4").WithEqualConfigs(machineConfigV0).WithNodeReady().Node(),
		},
		unavail:              []string{"node-0", "node-2", "node-3"},
		layeredPoolWithImage: true,
	}, {
		name: "Mismatched unlayered node and layered pool with image unavailable",
		nodes: []*corev1.Node{
			helpers.NewNodeBuilder("node-0").WithConfigs(machineConfigV0, machineConfigV1).WithImages(imageV0, imageV1).Node(),
			helpers.NewNodeBuilder("node-1").WithEqualConfigsAndImages(machineConfigV1, imageV1).Node(),
			helpers.NewNodeBuilder("node-2").WithEqualConfigsAndImages(machineConfigV1, imageV1).WithNodeNotReady().Node(),
			helpers.NewNodeBuilder("node-3").WithEqualConfigs(machineConfigV0).WithNodeNotReady().Node(),
			helpers.NewNodeBuilder("node-4").WithEqualConfigsAndImages(machineConfigV0, imageV1).WithNodeReady().Node(),
		},
		unavail:     []string{"node-0", "node-2", "node-3"},
		layeredPool: true,
	}, {
		name: "Mismatched layered node and unlayered pool",
		nodes: []*corev1.Node{
			helpers.NewNodeBuilder("node-0").WithConfigs(machineConfigV0, machineConfigV1).Node(),
			helpers.NewNodeBuilder("node-1").WithEqualConfigs(machineConfigV1).Node(),
			helpers.NewNodeBuilder("node-2").WithEqualConfigs(machineConfigV1).WithEqualImages(imageV1).WithNodeNotReady().Node(),
			helpers.NewNodeBuilder("node-3").WithEqualConfigs(machineConfigV0).WithEqualImages(imageV1).WithNodeNotReady().Node(),
			helpers.NewNodeBuilder("node-4").WithEqualConfigs(machineConfigV0).WithEqualImages(imageV1).WithNodeReady().Node(),
		},
		unavail: []string{"node-0", "node-2", "node-3"},
	}, {
		// Targets https://issues.redhat.com/browse/OCPBUGS-24705.
		name: "nodes working toward layered should not be considered available",
		nodes: []*corev1.Node{
			// Need to set WithNodeReady() on all nodes to avoid short-circuiting.
			helpers.NewNodeBuilder("node-0").
				WithEqualConfigs(machineConfigV0).
				WithNodeReady().
				Node(),
			helpers.NewNodeBuilder("node-1").
				WithEqualConfigs(machineConfigV0).
				WithNodeReady().
				Node(),
			helpers.NewNodeBuilder("node-2").
				WithEqualConfigs(machineConfigV0).
				WithDesiredImage(imageV1).
				WithMCDState(daemonconsts.MachineConfigDaemonStateWorking).
				WithNodeReady().
				Node(),
			helpers.NewNodeBuilder("node-3").
				WithEqualConfigs(machineConfigV0).
				WithDesiredImage(imageV1).WithCurrentImage("").
				WithNodeReady().
				Node(),
		},
		layeredPoolWithImage: true,
		unavail:              []string{"node-2", "node-3"},
	}, {
		// Targets https://issues.redhat.com/browse/OCPBUGS-24705.
		name: "nodes with desiredImage annotation that have not yet started working should not be considered available",
		nodes: []*corev1.Node{
			// Need to set WithNodeReady() on all nodes to avoid short-circuiting.
			helpers.NewNodeBuilder("node-0").
				WithEqualConfigs(machineConfigV0).
				WithDesiredImage(imageV0).WithCurrentImage(imageV0).
				WithMCDState(daemonconsts.MachineConfigDaemonStateDone).
				WithNodeReady().
				Node(),
			helpers.NewNodeBuilder("node-1").
				WithEqualConfigs(machineConfigV0).
				WithDesiredImage(imageV0).WithCurrentImage(imageV0).
				WithMCDState(daemonconsts.MachineConfigDaemonStateDone).
				WithNodeReady().
				Node(),
			helpers.NewNodeBuilder("node-2").
				WithEqualConfigs(machineConfigV0).
				WithDesiredImage(imageV1).
				WithMCDState(daemonconsts.MachineConfigDaemonStateDone).
				WithNodeReady().
				Node(),
			helpers.NewNodeBuilder("node-3").
				WithEqualConfigs(machineConfigV0).
				WithDesiredImage(imageV1).WithCurrentImage(imageV0).
				WithMCDState(daemonconsts.MachineConfigDaemonStateDone).
				WithNodeReady().
				Node(),
		},
		layeredPool:          true,
		layeredPoolWithImage: true,
		unavail:              []string{"node-2", "node-3"},
	},
	}

	for _, test := range tests {
		test := test

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			pb := helpers.NewMachineConfigPoolBuilder("")

			if test.layeredPool {
				pb.WithLayeringEnabled()
			}

			if test.layeredPoolWithImage {
				pb.WithLayeringEnabled()
				pb.WithImage(imageV1)
			}
			// pool := pb.MachineConfigPool()

			unavailableNodes := ctrl.getUnavailableMachines(test.nodes, test.layeredPool)
			assertExpectedNodes(t, test.unavail, unavailableNodes)
		})
	}
}

func assertExpectedNodes(t *testing.T, expected []string, actual []*corev1.Node) {
	t.Helper()
	assert.Equal(t, expected, getNamesFromNodes(actual))
}

func TestCalculateStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		nodes         []*corev1.Node
		currentConfig string
		paused        bool

		verify func(mcfgv1.MachineConfigPoolStatus, *testing.T)
	}{{
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			newNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			newNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
		},
		currentConfig: machineConfigV1,
		verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
			if got, want := status.MachineCount, int32(3); got != want {
				t.Fatalf("mismatch MachineCount: got %d want: %d", got, want)
			}

			if got, want := status.UpdatedMachineCount, int32(0); got != want {
				t.Fatalf("mismatch UpdatedMachineCount: got %d want: %d", got, want)
			}

			if got, want := status.ReadyMachineCount, int32(0); got != want {
				t.Fatalf("mismatch ReadyMachineCount: got %d want: %d", got, want)
			}

			if got, want := status.UnavailableMachineCount, int32(0); got != want {
				t.Fatalf("mismatch UnavailableMachineCount: got %d want: %d", got, want)
			}

			condupdated := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}

			condupdating := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
			if condupdating == nil {
				t.Fatal("updated condition not found")
			}

			if got, want := condupdated.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdated.Status: got %s want: %s", got, want)
			}

			if got, want := condupdating.Status, corev1.ConditionTrue; got != want {
				t.Fatalf("mismatch condupdating.Status: got %s want: %s", got, want)
			}
		},
	}, {
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
			newNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			newNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
		},
		currentConfig: machineConfigV1,
		verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
			if got, want := status.MachineCount, int32(3); got != want {
				t.Fatalf("mismatch MachineCount: got %d want: %d", got, want)
			}

			if got, want := status.UpdatedMachineCount, int32(0); got != want {
				t.Fatalf("mismatch UpdatedMachineCount: got %d want: %d", got, want)
			}

			if got, want := status.ReadyMachineCount, int32(0); got != want {
				t.Fatalf("mismatch ReadyMachineCount: got %d want: %d", got, want)
			}

			if got, want := status.UnavailableMachineCount, int32(1); got != want {
				t.Fatalf("mismatch UnavailableMachineCount: got %d want: %d", got, want)
			}

			condupdated := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}

			condupdating := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
			if condupdating == nil {
				t.Fatal("updated condition not found")
			}

			if got, want := condupdated.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdated.Status: got %s want: %s", got, want)
			}

			if got, want := condupdating.Status, corev1.ConditionTrue; got != want {
				t.Fatalf("mismatch condupdating.Status: got %s want: %s", got, want)
			}
		},
	}, {
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
			newNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			newNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
		},
		currentConfig: machineConfigV1,
		paused:        true,
		verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
			condupdated := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}
			if got, want := condupdated.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdated.Status: got %s want: %s", got, want)
			}

			condupdating := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
			if condupdating == nil {
				t.Fatal("updated condition not found")
			}
			if got, want := condupdating.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdating.Status: got %s want: %s", got, want)
			}
		},
	}, {
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionFalse),
			newNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			newNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
		},
		currentConfig: machineConfigV1,
		verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
			if got, want := status.MachineCount, int32(3); got != want {
				t.Fatalf("mismatch MachineCount: got %d want: %d", got, want)
			}

			if got, want := status.UpdatedMachineCount, int32(0); got != want {
				t.Fatalf("mismatch UpdatedMachineCount: got %d want: %d", got, want)
			}

			if got, want := status.ReadyMachineCount, int32(0); got != want {
				t.Fatalf("mismatch ReadyMachineCount: got %d want: %d", got, want)
			}

			if got, want := status.UnavailableMachineCount, int32(1); got != want {
				t.Fatalf("mismatch UnavailableMachineCount: got %d want: %d", got, want)
			}

			condupdated := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}

			condupdating := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
			if condupdating == nil {
				t.Fatal("updated condition not found")
			}

			if got, want := condupdated.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdated.Status: got %s want: %s", got, want)
			}

			if got, want := condupdating.Status, corev1.ConditionTrue; got != want {
				t.Fatalf("mismatch condupdating.Status: got %s want: %s", got, want)
			}
		},
	}, {
		nodes: []*corev1.Node{
			newNodeWithReadyAndDaemonState("node-0", machineConfigV0, machineConfigV1, corev1.ConditionFalse, daemonconsts.MachineConfigDaemonStateDegraded),
			newNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			newNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
		},
		currentConfig: machineConfigV1,
		verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
			if got, want := status.MachineCount, int32(3); got != want {
				t.Fatalf("mismatch MachineCount: got %d want: %d", got, want)
			}

			if got, want := status.UpdatedMachineCount, int32(0); got != want {
				t.Fatalf("mismatch UpdatedMachineCount: got %d want: %d", got, want)
			}

			if got, want := status.ReadyMachineCount, int32(0); got != want {
				t.Fatalf("mismatch ReadyMachineCount: got %d want: %d", got, want)
			}

			if got, want := status.UnavailableMachineCount, int32(1); got != want {
				t.Fatalf("mismatch UnavailableMachineCount: got %d want: %d", got, want)
			}

			condupdated := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}

			condupdating := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
			if condupdating == nil {
				t.Fatal("updated condition not found")
			}

			if got, want := condupdated.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdated.Status: got %s want: %s", got, want)
			}

			if got, want := condupdating.Status, corev1.ConditionTrue; got != want {
				t.Fatalf("mismatch condupdating.Status: got %s want: %s", got, want)
			}
		},
	}, {
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", machineConfigV1, machineConfigV1, corev1.ConditionFalse),
			newNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			newNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
		},
		currentConfig: machineConfigV1,
		verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
			if got, want := status.MachineCount, int32(3); got != want {
				t.Fatalf("mismatch MachineCount: got %d want: %d", got, want)
			}

			if got, want := status.UpdatedMachineCount, int32(1); got != want {
				t.Fatalf("mismatch UpdatedMachineCount: got %d want: %d", got, want)
			}

			if got, want := status.ReadyMachineCount, int32(0); got != want {
				t.Fatalf("mismatch ReadyMachineCount: got %d want: %d", got, want)
			}

			if got, want := status.UnavailableMachineCount, int32(1); got != want {
				t.Fatalf("mismatch UnavailableMachineCount: got %d want: %d", got, want)
			}

			condupdated := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}

			condupdating := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
			if condupdating == nil {
				t.Fatal("updated condition not found")
			}

			if got, want := condupdated.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdated.Status: got %s want: %s", got, want)
			}

			if got, want := condupdating.Status, corev1.ConditionTrue; got != want {
				t.Fatalf("mismatch condupdating.Status: got %s want: %s", got, want)
			}
		},
	}, {
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
			newNodeWithReady("node-1", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
			newNodeWithReady("node-2", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
		},
		currentConfig: machineConfigV1,
		verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
			if got, want := status.MachineCount, int32(3); got != want {
				t.Fatalf("mismatch MachineCount: got %d want: %d", got, want)
			}

			if got, want := status.UpdatedMachineCount, int32(1); got != want {
				t.Fatalf("mismatch UpdatedMachineCount: got %d want: %d", got, want)
			}

			if got, want := status.ReadyMachineCount, int32(1); got != want {
				t.Fatalf("mismatch ReadyMachineCount: got %d want: %d", got, want)
			}

			if got, want := status.UnavailableMachineCount, int32(2); got != want {
				t.Fatalf("mismatch UnavailableMachineCount: got %d want: %d", got, want)
			}

			condupdated := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}

			condupdating := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
			if condupdating == nil {
				t.Fatal("updated condition not found")
			}

			if got, want := condupdated.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdated.Status: got %s want: %s", got, want)
			}

			if got, want := condupdating.Status, corev1.ConditionTrue; got != want {
				t.Fatalf("mismatch condupdating.Status: got %s want: %s", got, want)
			}
		},
	}, {
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
			newNodeWithReady("node-1", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
			newNodeWithReady("node-2", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
		},
		currentConfig: machineConfigV1,
		verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
			if got, want := status.MachineCount, int32(3); got != want {
				t.Fatalf("mismatch MachineCount: got %d want: %d", got, want)
			}

			if got, want := status.UpdatedMachineCount, int32(3); got != want {
				t.Fatalf("mismatch UpdatedMachineCount: got %d want: %d", got, want)
			}

			if got, want := status.ReadyMachineCount, int32(3); got != want {
				t.Fatalf("mismatch ReadyMachineCount: got %d want: %d", got, want)
			}

			if got, want := status.UnavailableMachineCount, int32(0); got != want {
				t.Fatalf("mismatch UnavailableMachineCount: got %d want: %d", got, want)
			}

			condupdated := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}

			condupdating := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
			if condupdating == nil {
				t.Fatal("updated condition not found")
			}

			if got, want := condupdated.Status, corev1.ConditionTrue; got != want {
				t.Fatalf("mismatch condupdated.Status: got %s want: %s", got, want)
			}

			if got, want := condupdating.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdating.Status: got %s want: %s", got, want)
			}
		},
	}}
	for idx, test := range tests {
		idx := idx
		test := test
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			t.Parallel()
			pool := &mcfgv1.MachineConfigPool{
				Spec: mcfgv1.MachineConfigPoolSpec{
					Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{ObjectReference: corev1.ObjectReference{Name: test.currentConfig}},
					Paused:        test.paused,
				},
			}
			fgAccess := featuregates.NewHardcodedFeatureGateAccess(
				[]apicfgv1.FeatureGateName{
					features.FeatureGateMachineConfigNodes,
					features.FeatureGatePinnedImages,
				},
				[]apicfgv1.FeatureGateName{},
			)
			fg, err := fgAccess.CurrentFeatureGates()
			if err != nil {
				t.Fatal(err)
			}
			f := newFixture(t)
			c := f.newController()
			status := c.calculateStatus(fg, []*mcfgalphav1.MachineConfigNode{}, nil, pool, test.nodes, nil, nil)
			test.verify(status, t)
		})
	}
}
