package node

import (
	"fmt"
	"reflect"
	"testing"

	features "github.com/openshift/api/features"

	apicfgv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
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
		lns := ctrlcommon.NewLayeredNodeState(&node)
		if lns.IsNodeReady() {
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

func TestGetReadyMachines(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		nodes         []*corev1.Node
		currentConfig string
		currentImage  string
		ready         []*corev1.Node
		layered       bool
		mosc          *mcfgv1.MachineOSConfig
		mosb          *mcfgv1.MachineOSBuild
	}{{
		name:          "no nodes",
		nodes:         []*corev1.Node{},
		currentConfig: machineConfigV1,
		ready:         nil,
	}, {
		name: "1 node updated, 1 updating, 1 not acted upon",
		nodes: []*corev1.Node{
			helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-1", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV1, corev1.ConditionFalse),
		},
		currentConfig: machineConfigV1,
		ready:         []*corev1.Node{helpers.NewNodeWithReady("node-1", machineConfigV1, machineConfigV1, corev1.ConditionTrue)},
	}, {
		name: "2 node updated, 1 updating",
		nodes: []*corev1.Node{
			helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-1", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-2", machineConfigV1, machineConfigV1, corev1.ConditionFalse),
		},
		currentConfig: machineConfigV1,
		ready:         []*corev1.Node{helpers.NewNodeWithReady("node-1", machineConfigV1, machineConfigV1, corev1.ConditionTrue)},
	}, {
		name: "2 node updated, 1 updating",
		nodes: []*corev1.Node{
			helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-1", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-2", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
		},
		currentConfig: machineConfigV1,
		ready:         []*corev1.Node{helpers.NewNodeWithReady("node-1", machineConfigV1, machineConfigV1, corev1.ConditionTrue), helpers.NewNodeWithReady("node-2", machineConfigV1, machineConfigV1, corev1.ConditionTrue)},
	}, {
		name: "2 node updated, 1 updating, but one updated node is NotReady",
		nodes: []*corev1.Node{
			newNode("node-0", machineConfigV0, machineConfigV1),
			newNode("node-1", machineConfigV1, machineConfigV1),
			helpers.NewNodeWithReady("node-2", machineConfigV1, machineConfigV1, corev1.ConditionFalse),
		},
		currentConfig: machineConfigV1,
		ready:         []*corev1.Node{newNode("node-1", machineConfigV1, machineConfigV1)},
	}, {
		name: "2 layered nodes updated, 1 updating, but one updated node is NotReady",
		nodes: []*corev1.Node{
			newLayeredNode("node-0", machineConfigV0, machineConfigV1, imageV0, imageV1),
			newLayeredNode("node-1", machineConfigV1, machineConfigV1, imageV1, imageV1),
			helpers.NewLayeredNodeWithReady("node-2", machineConfigV1, machineConfigV1, imageV1, imageV1, corev1.ConditionFalse),
		},
		currentConfig: machineConfigV1,
		currentImage:  imageV1,
		ready:         []*corev1.Node{newLayeredNode("node-1", machineConfigV1, machineConfigV1, imageV1, imageV1)},
		layered:       true,
		mosc:          helpers.NewMachineOSConfigBuilder("mosc-1").WithCurrentImagePullspec(imageV1).MachineOSConfig(),
		mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV1).MachineOSBuild(),
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
		mosc:    helpers.NewMachineOSConfigBuilder("mosc-1").WithCurrentImagePullspec(imageV1).MachineOSConfig(),
		mosb:    helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV1).MachineOSBuild(),
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
				newLayeredNode("node-3", machineConfigV1, machineConfigV1, imageV1, imageV1),
			},
			layered: true,
			mosc:    helpers.NewMachineOSConfigBuilder("mosc-1").WithCurrentImagePullspec(imageV1).MachineOSConfig(),
			mosb:    helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV1).MachineOSBuild(),
		},
		{
			name: "Layered pool with image not built",
			nodes: []*corev1.Node{
				newNode("node-0", machineConfigV1, machineConfigV1),
				newNode("node-1", machineConfigV1, machineConfigV1),
				newNode("node-2", machineConfigV1, machineConfigV1),
			},
			currentConfig: machineConfigV1,
			ready:         nil,
			layered:       true,
			mosc:          helpers.NewMachineOSConfigBuilder("mosc-1").WithMachineConfigPool("pool-1").MachineOSConfig(),
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV1).MachineOSBuild(),
		},
	}

	for _, test := range tests {
		test := test

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			pool := helpers.NewMachineConfigPoolBuilder("").WithMachineConfig(test.currentConfig).MachineConfigPool()
			ready := getReadyMachines(pool, test.nodes, test.mosc, test.mosb, test.layered)
			if !reflect.DeepEqual(ready, test.ready) {
				t.Fatalf("mismatch expected: %v got %v", test.ready, ready)
			}
		})
	}
}

func TestGetUnavailableMachines(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		nodes   []*corev1.Node
		unavail []string
	}{{
		name:    "no nodes",
		nodes:   []*corev1.Node{},
		unavail: []string{},
	}, {
		name: "1 in progress",
		nodes: []*corev1.Node{
			helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-1", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
		},
		unavail: []string{"node-2"},
	}, {
		name: "1 unavail, 1 in progress",
		nodes: []*corev1.Node{
			helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-1", machineConfigV1, machineConfigV1, corev1.ConditionFalse),
			helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
		},
		unavail: []string{"node-1", "node-2"},
	}, {
		name: "1 node updated, 1 updating, 1 updating but not v2 and is ready",
		nodes: []*corev1.Node{
			helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-1", machineConfigV2, machineConfigV2, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV2, corev1.ConditionTrue),
		},
		unavail: []string{"node-0", "node-2"},
	}, {
		name: "1 node updated, 1 updating, 1 updating but not v2 and is not ready",
		nodes: []*corev1.Node{
			helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionFalse),
			helpers.NewNodeWithReady("node-1", machineConfigV2, machineConfigV2, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV2, corev1.ConditionTrue),
		},
		unavail: []string{"node-0", "node-2"},
	}, {
		name: "2 node updated, 1 updating",
		nodes: []*corev1.Node{
			helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-1", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-2", machineConfigV1, machineConfigV1, corev1.ConditionFalse),
		},
		unavail: []string{"node-0", "node-2"},
	}, {
		name: "2 node updated, 1 updating, but one updated node is NotReady",
		nodes: []*corev1.Node{
			newNode("node-0", machineConfigV0, machineConfigV1),
			newNode("node-1", machineConfigV1, machineConfigV1),
			helpers.NewNodeWithReady("node-2", machineConfigV1, machineConfigV1, corev1.ConditionFalse),
		},
		unavail: []string{"node-0", "node-2"},
	}, {
		name: "2 node updated, 1 updating, but one updated node is NotReady",
		nodes: []*corev1.Node{
			newNode("node-0", machineConfigV0, machineConfigV1),
			newNode("node-1", machineConfigV1, machineConfigV1),
			helpers.NewNodeWithReady("node-2", machineConfigV1, machineConfigV1, corev1.ConditionFalse),
		},
		unavail: []string{"node-0", "node-2"},
	}, {
		name: "1 layered node updated, 1 updating, but one updated node is NotReady",
		nodes: []*corev1.Node{
			helpers.NewNodeBuilder("node-0").WithConfigs(machineConfigV0, machineConfigV1).WithImages(imageV0, imageV1).Node(),
			helpers.NewNodeBuilder("node-1").WithEqualConfigsAndImages(machineConfigV1, imageV1).Node(),
			helpers.NewNodeBuilder("node-2").WithEqualConfigsAndImages(machineConfigV1, imageV1).WithNodeNotReady().Node(),
		},
		unavail: []string{"node-0", "node-2"},
	}, {
		name: "Mismatched unlayered node and layered pool with image available",
		nodes: []*corev1.Node{
			helpers.NewNodeBuilder("node-0").WithConfigs(machineConfigV0, machineConfigV1).WithImages(imageV0, imageV1).Node(),
			helpers.NewNodeBuilder("node-1").WithEqualConfigsAndImages(machineConfigV1, imageV1).Node(),
			helpers.NewNodeBuilder("node-2").WithEqualConfigsAndImages(machineConfigV1, imageV1).WithNodeNotReady().Node(),
			helpers.NewNodeBuilder("node-3").WithEqualConfigs(machineConfigV0).WithNodeNotReady().Node(),
			helpers.NewNodeBuilder("node-4").WithEqualConfigs(machineConfigV0).WithNodeReady().Node(),
		},
		unavail: []string{"node-0", "node-2", "node-3"},
	}, {
		name: "Mismatched unlayered node and layered pool with image unavailable",
		nodes: []*corev1.Node{
			helpers.NewNodeBuilder("node-0").WithConfigs(machineConfigV0, machineConfigV1).WithImages(imageV0, imageV1).Node(),
			helpers.NewNodeBuilder("node-1").WithEqualConfigsAndImages(machineConfigV1, imageV1).Node(),
			helpers.NewNodeBuilder("node-2").WithEqualConfigsAndImages(machineConfigV1, imageV1).WithNodeNotReady().Node(),
			helpers.NewNodeBuilder("node-3").WithEqualConfigs(machineConfigV0).WithNodeNotReady().Node(),
			helpers.NewNodeBuilder("node-4").WithEqualConfigsAndImages(machineConfigV0, imageV1).WithNodeReady().Node(),
		},
		unavail: []string{"node-0", "node-2", "node-3"},
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
		unavail: []string{"node-2", "node-3"},
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
		unavail: []string{"node-2", "node-3"},
	},
	}

	for _, test := range tests {
		test := test

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			pb := helpers.NewMachineConfigPoolBuilder("")

			pool := pb.MachineConfigPool()

			unavailableNodes := getUnavailableMachines(test.nodes, pool)
			assertExpectedNodes(t, test.unavail, unavailableNodes)
		})
	}
}

func assertExpectedNodes(t *testing.T, expected []string, actual []*corev1.Node) {
	t.Helper()
	assert.Equal(t, expected, helpers.GetNamesFromNodes(actual))
}

func TestCalculateStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		nodes         []*corev1.Node
		currentConfig string
		paused        bool
		osStream      mcfgv1.OSImageStreamReference
		verify        func(mcfgv1.MachineConfigPoolStatus, *testing.T)
	}{{
		name: "0 nodes updated, 0 nodes updating, 0 nodes degraded",
		nodes: []*corev1.Node{
			helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
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

			if got, want := status.DegradedMachineCount, int32(0); got != want {
				t.Fatalf("mismatch DegradedMachineCount: got %d want: %d", got, want)
			}

			condupdated := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}

			condupdating := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
			if condupdating == nil {
				t.Fatal("updating condition not found")
			}

			conddegraded := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolDegraded)
			if conddegraded == nil {
				t.Fatal("degraded condition not found")
			}

			if got, want := condupdated.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdated.Status: got %s want: %s", got, want)
			}

			if got, want := condupdating.Status, corev1.ConditionTrue; got != want {
				t.Fatalf("mismatch condupdating.Status: got %s want: %s", got, want)
			}

			if got, want := conddegraded.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch conddegraded.Status: got %s want: %s", got, want)
			}
		},
	}, {
		name: "0 nodes updated, 1 node updating, 0 nodes degraded",
		nodes: []*corev1.Node{
			helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
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

			if got, want := status.DegradedMachineCount, int32(0); got != want {
				t.Fatalf("mismatch DegradedMachineCount: got %d want: %d", got, want)
			}

			condupdated := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}

			condupdating := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
			if condupdating == nil {
				t.Fatal("updating condition not found")
			}

			conddegraded := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolDegraded)
			if condupdating == nil {
				t.Fatal("degraded condition not found")
			}

			if got, want := condupdated.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdated.Status: got %s want: %s", got, want)
			}

			if got, want := condupdating.Status, corev1.ConditionTrue; got != want {
				t.Fatalf("mismatch condupdating.Status: got %s want: %s", got, want)
			}

			if got, want := conddegraded.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch conddegraded.Status: got %s want: %s", got, want)
			}
		},
	}, {
		name: "0 nodes updates, 0 nodes updating, 0 nodes degraded, pool paused",
		nodes: []*corev1.Node{
			helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
		},
		currentConfig: machineConfigV1,
		paused:        true,
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

			if got, want := status.DegradedMachineCount, int32(0); got != want {
				t.Fatalf("mismatch DegradedMachineCount: got %d want: %d", got, want)
			}

			condupdated := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}
			if got, want := condupdated.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdated.Status: got %s want: %s", got, want)
			}

			condupdating := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
			if condupdating == nil {
				t.Fatal("updating condition not found")
			}
			if got, want := condupdating.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdating.Status: got %s want: %s", got, want)
			}

			conddegraded := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolDegraded)
			if conddegraded == nil {
				t.Fatal("degraded condition not found")
			}
			if got, want := conddegraded.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch conddegraded.Status: got %s want: %s", got, want)
			}
		},
	}, {
		name: "0 nodes updated, 1 node updating, 0 nodes degraded",
		nodes: []*corev1.Node{
			helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionFalse),
			helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
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

			if got, want := status.DegradedMachineCount, int32(0); got != want {
				t.Fatalf("mismatch DegradedMachineCount: got %d want: %d", got, want)
			}

			condupdated := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}

			condupdating := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
			if condupdating == nil {
				t.Fatal("updating condition not found")
			}

			conddegraded := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolDegraded)
			if conddegraded == nil {
				t.Fatal("degraded condition not found")
			}

			if got, want := condupdated.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdated.Status: got %s want: %s", got, want)
			}

			if got, want := condupdating.Status, corev1.ConditionTrue; got != want {
				t.Fatalf("mismatch condupdating.Status: got %s want: %s", got, want)
			}

			if got, want := conddegraded.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch conddegraded.Status: got %s want: %s", got, want)
			}
		},
	}, {
		name: "0 nodes updated, 1 node updating, 1 node degraded",
		nodes: []*corev1.Node{
			newNodeWithReadyAndDaemonState("node-0", machineConfigV0, machineConfigV1, corev1.ConditionFalse, daemonconsts.MachineConfigDaemonStateDegraded),
			helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
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

			if got, want := status.DegradedMachineCount, int32(1); got != want {
				t.Fatalf("mismatch DegradedMachineCount: got %d want: %d", got, want)
			}

			condupdated := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}

			condupdating := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
			if condupdating == nil {
				t.Fatal("updating condition not found")
			}

			conddegraded := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolDegraded)
			if conddegraded == nil {
				t.Fatal("updating condition not found")
			}

			if got, want := condupdated.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdated.Status: got %s want: %s", got, want)
			}

			if got, want := condupdating.Status, corev1.ConditionTrue; got != want {
				t.Fatalf("mismatch condupdating.Status: got %s want: %s", got, want)
			}

			if got, want := conddegraded.Status, corev1.ConditionTrue; got != want {
				t.Fatalf("mismatch conddegraded.Status: got %s want: %s", got, want)
			}
		},
	}, {
		name: "1 node updated, 1 node updating, 0 nodes degraded",
		nodes: []*corev1.Node{
			helpers.NewNodeWithReady("node-0", machineConfigV1, machineConfigV1, corev1.ConditionFalse),
			helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
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

			if got, want := status.DegradedMachineCount, int32(0); got != want {
				t.Fatalf("mismatch DegradedMachineCount: got %d want: %d", got, want)
			}

			condupdated := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}

			condupdating := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
			if condupdating == nil {
				t.Fatal("updating condition not found")
			}

			conddegraded := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolDegraded)
			if conddegraded == nil {
				t.Fatal("degraded condition not found")
			}

			if got, want := condupdated.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdated.Status: got %s want: %s", got, want)
			}

			if got, want := condupdating.Status, corev1.ConditionTrue; got != want {
				t.Fatalf("mismatch condupdating.Status: got %s want: %s", got, want)
			}

			if got, want := conddegraded.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch conddegraded.Status: got %s want: %s", got, want)
			}
		},
	}, {
		name: "1 node updated, 2 nodes updating, 0 nodes degraded",
		nodes: []*corev1.Node{
			helpers.NewNodeWithReady("node-0", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
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

			if got, want := status.DegradedMachineCount, int32(0); got != want {
				t.Fatalf("mismatch DegradedMachineCount: got %d want: %d", got, want)
			}

			condupdated := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}

			condupdating := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
			if condupdating == nil {
				t.Fatal("updating condition not found")
			}

			conddegraded := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolDegraded)
			if conddegraded == nil {
				t.Fatal("degraded condition not found")
			}

			if got, want := condupdated.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdated.Status: got %s want: %s", got, want)
			}

			if got, want := condupdating.Status, corev1.ConditionTrue; got != want {
				t.Fatalf("mismatch condupdating.Status: got %s want: %s", got, want)
			}

			if got, want := conddegraded.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch conddegraded.Status: got %s want: %s", got, want)
			}
		},
	}, {
		name: "3 nodes updated, 0 nodes updating, 0 nodes degraded",
		nodes: []*corev1.Node{
			helpers.NewNodeWithReady("node-0", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-1", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-2", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
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

			if got, want := status.DegradedMachineCount, int32(0); got != want {
				t.Fatalf("mismatch DegradedMachineCount: got %d want: %d", got, want)
			}

			condupdated := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}

			condupdating := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
			if condupdating == nil {
				t.Fatal("updating condition not found")
			}

			conddegraded := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolDegraded)
			if conddegraded == nil {
				t.Fatal("degraded condition not found")
			}

			if got, want := condupdated.Status, corev1.ConditionTrue; got != want {
				t.Fatalf("mismatch condupdated.Status: got %s want: %s", got, want)
			}

			if got, want := condupdating.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdating.Status: got %s want: %s", got, want)
			}

			if got, want := conddegraded.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch conddegraded.Status: got %s want: %s", got, want)
			}
		},
	}, {
		name: "all nodes updated, OSStream defined in MCP Spec",
		nodes: []*corev1.Node{
			helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
		},
		currentConfig: machineConfigV0,
		osStream: mcfgv1.OSImageStreamReference{
			Name: "rhel-10",
		},
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
				t.Fatal("updating condition not found")
			}

			if got, want := condupdated.Status, corev1.ConditionTrue; got != want {
				t.Fatalf("mismatch condupdated.Status: got %s want: %s", got, want)
			}

			if got, want := condupdating.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdating.Status: got %s want: %s", got, want)
			}

			statusOSStreamName := status.OSImageStream.Name
			if statusOSStreamName != "rhel-10" {
				t.Fatal("OSImageStreamReference in MCP status not updated correctly")
			}
		},
	}, {
		name: "some nodes still updating, OSStream defined in MCP Spec",
		nodes: []*corev1.Node{
			helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
		},
		currentConfig: machineConfigV1,
		osStream: mcfgv1.OSImageStreamReference{
			Name: "rhel-10",
		},
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
				t.Fatal("updating condition not found")
			}

			if got, want := condupdated.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdated.Status: got %s want: %s", got, want)
			}

			if got, want := condupdating.Status, corev1.ConditionTrue; got != want {
				t.Fatalf("mismatch condupdating.Status: got %s want: %s", got, want)
			}

			statusOSStreamName := status.OSImageStream.Name
			if statusOSStreamName == "rhel-10" {
				t.Fatal("OSImageStreamReference updated in MCP status, but should not be")
			}
		},
	}, {
		name: "all nodes updated, OSStream removed from MCP Spec",
		nodes: []*corev1.Node{
			helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
		},
		currentConfig: machineConfigV0,
		osStream:      mcfgv1.OSImageStreamReference{},
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
				t.Fatal("updating condition not found")
			}

			if got, want := condupdated.Status, corev1.ConditionTrue; got != want {
				t.Fatalf("mismatch condupdated.Status: got %s want: %s", got, want)
			}

			if got, want := condupdating.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdating.Status: got %s want: %s", got, want)
			}

			statusOSStream := status.OSImageStream
			if statusOSStream.Name != "" {
				t.Fatal("OSImageStreamReference in MCP status not cleared correctly")
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
					OSImageStream: test.osStream,
				},
			}
			f := newFixtureWithFeatureGates(t,
				[]apicfgv1.FeatureGateName{
					features.FeatureGateMachineConfigNodes,
					features.FeatureGatePinnedImages,
					features.FeatureGateOSStreams,
				},
				[]apicfgv1.FeatureGateName{},
			)

			c := f.newController()
			status := c.calculateStatus([]*mcfgv1.MachineConfigNode{}, nil, pool, test.nodes, nil, nil)
			test.verify(status, t)
		})
	}
}

// Assisted by: Cursor
// TestCalculateStatusWithImageModeReporting tests the status calculation with ImageModeStatusReporting feature gate enabled
func TestCalculateStatusWithImageModeReporting(t *testing.T) {
	t.Parallel()

	// Create feature gate handler that directly enables ImageModeStatusReporting
	// This simulates a DevPreview environment where this feature gate is available
	fgHandler := ctrlcommon.NewFeatureGatesHardcodedHandler(
		[]apicfgv1.FeatureGateName{
			features.FeatureGateMachineConfigNodes,
			features.FeatureGatePinnedImages,
			features.FeatureGateImageModeStatusReporting, // Enable ImageModeStatusReporting directly
		},
		[]apicfgv1.FeatureGateName{},
	)

	// Verify that ImageModeStatusReporting is enabled
	if !fgHandler.Enabled(features.FeatureGateImageModeStatusReporting) {
		t.Skip("ImageModeStatusReporting could not be enabled")
	}

	tests := []struct {
		name          string
		nodes         []*corev1.Node
		mcns          []*mcfgv1.MachineConfigNode
		currentConfig string
		paused        bool
		verify        func(mcfgv1.MachineConfigPoolStatus, *testing.T)
	}{{
		name: "0 nodes updated, 0 nodes updating, 0 nodes degraded",
		nodes: []*corev1.Node{
			helpers.NewNodeWithReadyAndDaemonStateAndImageAnnos("node-0", machineConfigV1, machineConfigV1, "registry.host.com/org/repo@sha256:12345", "registry.host.com/org/repo@sha256:12345", daemonconsts.MachineConfigDaemonStateDone, corev1.ConditionTrue),
			helpers.NewNodeWithReadyAndDaemonStateAndImageAnnos("node-1", machineConfigV1, machineConfigV1, "registry.host.com/org/repo@sha256:12345", "registry.host.com/org/repo@sha256:12345", daemonconsts.MachineConfigDaemonStateDone, corev1.ConditionTrue),
			helpers.NewNodeWithReadyAndDaemonStateAndImageAnnos("node-2", machineConfigV1, machineConfigV1, "registry.host.com/org/repo@sha256:12345", "registry.host.com/org/repo@sha256:12345", daemonconsts.MachineConfigDaemonStateDone, corev1.ConditionTrue),
		},
		mcns: []*mcfgv1.MachineConfigNode{
			helpers.NewMachineConfigNode("node-0", "worker", machineConfigV1, "registry.host.com/org/repo@sha256:12345", true, false),
			helpers.NewMachineConfigNode("node-1", "worker", machineConfigV1, "registry.host.com/org/repo@sha256:12345", true, false),
			helpers.NewMachineConfigNode("node-2", "worker", machineConfigV1, "registry.host.com/org/repo@sha256:12345", true, false),
		},
		currentConfig: machineConfigV0,
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

			if got, want := status.DegradedMachineCount, int32(0); got != want {
				t.Fatalf("mismatch DegradedMachineCount: got %d want: %d", got, want)
			}

			condupdated := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}

			if got, want := condupdated.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdated.Status: got %s want: %s", got, want)
			}

			condupdating := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
			if condupdating == nil {
				t.Fatal("updating condition not found")
			}

			if got, want := condupdating.Status, corev1.ConditionTrue; got != want {
				t.Fatalf("mismatch condupdating.Status: got %s want: %s", got, want)
			}

			conddegraded := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolDegraded)
			if conddegraded == nil {
				t.Fatal("degraded condition not found")
			}

			if got, want := conddegraded.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch conddegraded.Status: got %s want: %s", got, want)
			}
		},
	}, {
		name: "0 nodes updated, 1 node updating, 0 nodes degraded",
		nodes: []*corev1.Node{
			helpers.NewNodeWithReadyAndDaemonStateAndImageAnnos("node-0", machineConfigV1, machineConfigV0, "registry.host.com/org/repo@sha256:12345", "registry.host.com/org/repo@sha256:12346", daemonconsts.MachineConfigDaemonStateDone, corev1.ConditionTrue),
			helpers.NewNodeWithReadyAndDaemonStateAndImageAnnos("node-1", machineConfigV1, machineConfigV1, "registry.host.com/org/repo@sha256:12345", "registry.host.com/org/repo@sha256:12345", daemonconsts.MachineConfigDaemonStateDone, corev1.ConditionTrue),
			helpers.NewNodeWithReadyAndDaemonStateAndImageAnnos("node-2", machineConfigV1, machineConfigV1, "registry.host.com/org/repo@sha256:12345", "registry.host.com/org/repo@sha256:12345", daemonconsts.MachineConfigDaemonStateDone, corev1.ConditionTrue),
		},
		mcns: []*mcfgv1.MachineConfigNode{
			helpers.NewMachineConfigNode("node-0", "worker", machineConfigV0, "registry.host.com/org/repo@sha256:12345", false, false),
			helpers.NewMachineConfigNode("node-1", "worker", machineConfigV1, "registry.host.com/org/repo@sha256:12345", true, false),
			helpers.NewMachineConfigNode("node-2", "worker", machineConfigV1, "registry.host.com/org/repo@sha256:12345", true, false),
		},
		currentConfig: machineConfigV0,
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

			if got, want := status.DegradedMachineCount, int32(0); got != want {
				t.Fatalf("mismatch DegradedMachineCount: got %d want: %d", got, want)
			}

			condupdated := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}

			if got, want := condupdated.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdated.Status: got %s want: %s", got, want)
			}

			condupdating := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
			if condupdating == nil {
				t.Fatal("updating condition not found")
			}

			if got, want := condupdating.Status, corev1.ConditionTrue; got != want {
				t.Fatalf("mismatch condupdating.Status: got %s want: %s", got, want)
			}

			conddegraded := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolDegraded)
			if conddegraded == nil {
				t.Fatal("degraded condition not found")
			}

			if got, want := conddegraded.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch conddegraded.Status: got %s want: %s", got, want)
			}
		},
	}, {
		name: "0 nodes updates, 0 nodes updating, 0 nodes degraded, pool paused",
		nodes: []*corev1.Node{
			helpers.NewNodeWithReadyAndDaemonStateAndImageAnnos("node-0", machineConfigV1, machineConfigV0, "registry.host.com/org/repo@sha256:12345", "registry.host.com/org/repo@sha256:12346", daemonconsts.MachineConfigDaemonStateDone, corev1.ConditionTrue),
			helpers.NewNodeWithReadyAndDaemonStateAndImageAnnos("node-1", machineConfigV1, machineConfigV1, "registry.host.com/org/repo@sha256:12345", "registry.host.com/org/repo@sha256:12345", daemonconsts.MachineConfigDaemonStateDone, corev1.ConditionTrue),
			helpers.NewNodeWithReadyAndDaemonStateAndImageAnnos("node-2", machineConfigV1, machineConfigV1, "registry.host.com/org/repo@sha256:12345", "registry.host.com/org/repo@sha256:12345", daemonconsts.MachineConfigDaemonStateDone, corev1.ConditionTrue),
		},
		mcns: []*mcfgv1.MachineConfigNode{
			helpers.NewMachineConfigNode("node-0", "worker", machineConfigV0, "registry.host.com/org/repo@sha256:12345", false, false),
			helpers.NewMachineConfigNode("node-1", "worker", machineConfigV1, "registry.host.com/org/repo@sha256:12345", true, false),
			helpers.NewMachineConfigNode("node-2", "worker", machineConfigV1, "registry.host.com/org/repo@sha256:12345", true, false),
		},
		currentConfig: machineConfigV0,
		paused:        true,
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

			if got, want := status.DegradedMachineCount, int32(0); got != want {
				t.Fatalf("mismatch DegradedMachineCount: got %d want: %d", got, want)
			}

			condupdated := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}

			if got, want := condupdated.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdated.Status: got %s want: %s", got, want)
			}

			condupdating := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
			if condupdating == nil {
				t.Fatal("updating condition not found")
			}

			if got, want := condupdating.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdating.Status: got %s want: %s", got, want)
			}

			conddegraded := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolDegraded)
			if conddegraded == nil {
				t.Fatal("degraded condition not found")
			}

			if got, want := conddegraded.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch conddegraded.Status: got %s want: %s", got, want)
			}
		},
	}, {
		name: "0 nodes updated, 1 node updating, 1 node degraded",
		nodes: []*corev1.Node{
			helpers.NewNodeWithReadyAndDaemonStateAndImageAnnos("node-0", machineConfigV1, machineConfigV0, "registry.host.com/org/repo@sha256:12345", "registry.host.com/org/repo@sha256:12346", daemonconsts.MachineConfigDaemonStateDone, corev1.ConditionTrue),
			helpers.NewNodeWithReadyAndDaemonStateAndImageAnnos("node-1", machineConfigV1, machineConfigV1, "registry.host.com/org/repo@sha256:12345", "registry.host.com/org/repo@sha256:12345", daemonconsts.MachineConfigDaemonStateDone, corev1.ConditionTrue),
			helpers.NewNodeWithReadyAndDaemonStateAndImageAnnos("node-2", machineConfigV1, machineConfigV1, "registry.host.com/org/repo@sha256:12345", "registry.host.com/org/repo@sha256:12345", daemonconsts.MachineConfigDaemonStateDone, corev1.ConditionTrue),
		},
		mcns: []*mcfgv1.MachineConfigNode{
			helpers.NewMachineConfigNode("node-0", "worker", machineConfigV0, "registry.host.com/org/repo@sha256:12345", false, true),
			helpers.NewMachineConfigNode("node-1", "worker", machineConfigV1, "registry.host.com/org/repo@sha256:12345", true, false),
			helpers.NewMachineConfigNode("node-2", "worker", machineConfigV1, "registry.host.com/org/repo@sha256:12345", true, false),
		},
		currentConfig: machineConfigV0,
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

			if got, want := status.DegradedMachineCount, int32(1); got != want {
				t.Fatalf("mismatch DegradedMachineCount: got %d want: %d", got, want)
			}

			condupdated := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}

			if got, want := condupdated.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdated.Status: got %s want: %s", got, want)
			}

			condupdating := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
			if condupdating == nil {
				t.Fatal("updating condition not found")
			}

			if got, want := condupdating.Status, corev1.ConditionTrue; got != want {
				t.Fatalf("mismatch condupdating.Status: got %s want: %s", got, want)
			}

			conddegraded := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolDegraded)
			if conddegraded == nil {
				t.Fatal("degraded condition not found")
			}

			if got, want := conddegraded.Status, corev1.ConditionTrue; got != want {
				t.Fatalf("mismatch conddegraded.Status: got %s want: %s", got, want)
			}
		},
	}, {
		name: "3 nodes updated, 0 nodes updating, 0 nodes degraded",
		nodes: []*corev1.Node{
			helpers.NewNodeWithReadyAndDaemonStateAndImageAnnos("node-0", machineConfigV1, machineConfigV1, "registry.host.com/org/repo@sha256:12345", "registry.host.com/org/repo@sha256:12345", daemonconsts.MachineConfigDaemonStateDone, corev1.ConditionTrue),
			helpers.NewNodeWithReadyAndDaemonStateAndImageAnnos("node-1", machineConfigV1, machineConfigV1, "registry.host.com/org/repo@sha256:12345", "registry.host.com/org/repo@sha256:12345", daemonconsts.MachineConfigDaemonStateDone, corev1.ConditionTrue),
			helpers.NewNodeWithReadyAndDaemonStateAndImageAnnos("node-2", machineConfigV1, machineConfigV1, "registry.host.com/org/repo@sha256:12345", "registry.host.com/org/repo@sha256:12345", daemonconsts.MachineConfigDaemonStateDone, corev1.ConditionTrue),
		},
		mcns: []*mcfgv1.MachineConfigNode{
			helpers.NewMachineConfigNode("node-0", "worker", machineConfigV1, "registry.host.com/org/repo@sha256:12345", true, false),
			helpers.NewMachineConfigNode("node-1", "worker", machineConfigV1, "registry.host.com/org/repo@sha256:12345", true, false),
			helpers.NewMachineConfigNode("node-2", "worker", machineConfigV1, "registry.host.com/org/repo@sha256:12345", true, false),
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

			if got, want := status.DegradedMachineCount, int32(0); got != want {
				t.Fatalf("mismatch DegradedMachineCount: got %d want: %d", got, want)
			}

			condupdated := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}

			if got, want := condupdated.Status, corev1.ConditionTrue; got != want {
				t.Fatalf("mismatch condupdated.Status: got %s want: %s", got, want)
			}

			condupdating := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
			if condupdating == nil {
				t.Fatal("updating condition not found")
			}

			if got, want := condupdating.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdating.Status: got %s want: %s", got, want)
			}

			conddegraded := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolDegraded)
			if conddegraded == nil {
				t.Fatal("degraded condition not found")
			}

			if got, want := conddegraded.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch conddegraded.Status: got %s want: %s", got, want)
			}
		},
	}, {
		name: "1 node updated, 2 nodes updating, 0 nodes degraded",
		nodes: []*corev1.Node{
			// Node-0 is updated and ready
			helpers.NewNodeWithReadyAndDaemonStateAndImageAnnos("node-0", machineConfigV1, machineConfigV1, "registry.host.com/org/repo@sha256:12345", "registry.host.com/org/repo@sha256:12345", daemonconsts.MachineConfigDaemonStateDone, corev1.ConditionTrue),
			// Node-1 is not updated yet, targeting new config but not done
			helpers.NewNodeWithReadyAndDaemonStateAndImageAnnos("node-1", machineConfigV0, machineConfigV1, "registry.host.com/org/repo@sha256:old", "registry.host.com/org/repo@sha256:12345", daemonconsts.MachineConfigDaemonStateWorking, corev1.ConditionTrue),
			// Node-2 is not updated yet, targeting new config but not done
			helpers.NewNodeWithReadyAndDaemonStateAndImageAnnos("node-2", machineConfigV0, machineConfigV1, "registry.host.com/org/repo@sha256:old", "registry.host.com/org/repo@sha256:12345", daemonconsts.MachineConfigDaemonStateWorking, corev1.ConditionTrue),
		},
		mcns: []*mcfgv1.MachineConfigNode{
			// Node-0 is updated to machineConfigV1
			helpers.NewMachineConfigNode("node-0", "worker", machineConfigV1, "registry.host.com/org/repo@sha256:12345", true, false),
			// Node-1 is targeting machineConfigV1 but not updated yet
			helpers.NewMachineConfigNode("node-1", "worker", machineConfigV1, "registry.host.com/org/repo@sha256:12345", false, false),
			// Node-2 is targeting machineConfigV1 but not updated yet
			helpers.NewMachineConfigNode("node-2", "worker", machineConfigV1, "registry.host.com/org/repo@sha256:12345", false, false),
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

			if got, want := status.DegradedMachineCount, int32(0); got != want {
				t.Fatalf("mismatch DegradedMachineCount: got %d want: %d", got, want)
			}

			condupdated := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}

			if got, want := condupdated.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch condupdated.Status: got %s want: %s", got, want)
			}

			condupdating := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
			if condupdating == nil {
				t.Fatal("updating condition not found")
			}

			if got, want := condupdating.Status, corev1.ConditionTrue; got != want {
				t.Fatalf("mismatch condupdating.Status: got %s want: %s", got, want)
			}

			conddegraded := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolDegraded)
			if conddegraded == nil {
				t.Fatal("updating condition not found")
			}

			if got, want := conddegraded.Status, corev1.ConditionFalse; got != want {
				t.Fatalf("mismatch conddegraded.Status: got %s want: %s", got, want)
			}
		},
	}}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			// Create fixture with our ImageModeStatusReporting feature gate handler
			f := newFixtureWithFeatureGates(t,
				[]apicfgv1.FeatureGateName{
					features.FeatureGateMachineConfigNodes,
					features.FeatureGatePinnedImages,
					features.FeatureGateImageModeStatusReporting,
				},
				[]apicfgv1.FeatureGateName{},
			)

			pool := &mcfgv1.MachineConfigPool{
				Spec: mcfgv1.MachineConfigPoolSpec{
					Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{ObjectReference: corev1.ObjectReference{Name: test.currentConfig}},
					Paused:        test.paused,
				},
			}

			// For ImageModeStatusReporting tests, we need MachineOSConfig and MachineOSBuild
			// Use the same image that we set in the MCN Status
			mosc := helpers.NewMachineOSConfigBuilder("mosc-1").WithCurrentImagePullspec("registry.host.com/org/repo@sha256:12345").MachineOSConfig()
			mosb := helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(test.currentConfig).MachineOSBuild()

			c := f.newController()
			status := c.calculateStatus(test.mcns, nil, pool, test.nodes, mosc, mosb)
			test.verify(status, t)
		})
	}
}
