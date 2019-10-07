package node

import (
	"fmt"
	"reflect"
	"testing"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
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

func newNode(name string, currentConfig, desiredConfig string) *corev1.Node {
	var annos map[string]string
	if currentConfig != "" || desiredConfig != "" {
		var state string
		if currentConfig == desiredConfig {
			state = daemonconsts.MachineConfigDaemonStateDone
		} else {
			state = daemonconsts.MachineConfigDaemonStateWorking
		}
		annos = map[string]string{}
		annos[daemonconsts.CurrentMachineConfigAnnotationKey] = currentConfig
		annos[daemonconsts.DesiredMachineConfigAnnotationKey] = desiredConfig
		annos[daemonconsts.MachineConfigDaemonStateAnnotationKey] = state
	}
	return newNodeWithAnnotations(name, annos)
}

func newNodeWithLabels(name string, labels map[string]string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
	}
}

func newNodeWithAnnotations(name string, annotations map[string]string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: annotations,
		},
	}
}

func newNodeWithLabel(name string, currentConfig, desiredConfig string, labels map[string]string) *corev1.Node {
	node := newNode(name, currentConfig, desiredConfig)
	if node.Labels == nil {
		node.Labels = map[string]string{}
	}
	for k, v := range labels {
		node.Labels[k] = v
	}
	return node
}

func newNodeWithReady(name string, currentConfig, desiredConfig string, status corev1.ConditionStatus) *corev1.Node {
	node := newNode(name, currentConfig, desiredConfig)
	node.Status = corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: status}}}
	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}
	return node
}

func newNodeWithReadyAndDaemonState(name string, currentConfig, desiredConfig string, status corev1.ConditionStatus, dstate string) *corev1.Node {
	node := newNode(name, currentConfig, desiredConfig)
	node.Status = corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: status}}}
	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}
	node.Annotations[daemonconsts.MachineConfigDaemonStateAnnotationKey] = dstate
	return node
}

func TestGetUpdatedMachines(t *testing.T) {
	tests := []struct {
		nodes         []*corev1.Node
		currentConfig string
		updated       []*corev1.Node
	}{{
		// no nodes
		nodes:         []*corev1.Node{},
		currentConfig: "v1",
		updated:       nil,
	}, {
		// 1 node updated, 1 updating, 1 not acted upon
		nodes: []*corev1.Node{
			newNode("node-0", "v0", "v0"),
			newNode("node-1", "v1", "v1"),
			newNode("node-2", "v0", "v1"),
		},
		currentConfig: "v1",
		updated:       []*corev1.Node{newNode("node-1", "v1", "v1")},
	}, {
		// 2 node updated, 1 updating
		nodes: []*corev1.Node{
			newNode("node-0", "v0", "v1"),
			newNode("node-1", "v1", "v1"),
			newNode("node-2", "v1", "v1"),
		},
		currentConfig: "v1",
		updated:       []*corev1.Node{newNode("node-1", "v1", "v1"), newNode("node-2", "v1", "v1")},
	}, {
		// 2 node updated, 1 updating, but one updated node is NotReady.
		nodes: []*corev1.Node{
			newNode("node-0", "v0", "v1"),
			newNode("node-1", "v1", "v1"),
			newNodeWithReady("node-2", "v1", "v1", corev1.ConditionFalse),
		},
		currentConfig: "v1",
		updated:       []*corev1.Node{newNode("node-1", "v1", "v1"), newNodeWithReady("node-2", "v1", "v1", corev1.ConditionFalse)},
	}}

	for idx, test := range tests {
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			updated := getUpdatedMachines(test.currentConfig, test.nodes)
			if !reflect.DeepEqual(updated, test.updated) {
				t.Fatalf("mismatch expected: %v got %v", test.updated, updated)
			}
		})
	}
}

func TestGetReadyMachines(t *testing.T) {
	tests := []struct {
		nodes         []*corev1.Node
		currentConfig string
		ready         []*corev1.Node
	}{{
		// no nodes
		nodes:         []*corev1.Node{},
		currentConfig: "v1",
		ready:         nil,
	}, {
		// 1 node updated, 1 updating, 1 not acted upon
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", "v0", "v0", corev1.ConditionTrue),
			newNodeWithReady("node-1", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-2", "v0", "v1", corev1.ConditionFalse),
		},
		currentConfig: "v1",
		ready:         []*corev1.Node{newNodeWithReady("node-1", "v1", "v1", corev1.ConditionTrue)},
	}, {
		// 2 node updated, 1 updating
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", "v0", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-1", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-2", "v1", "v1", corev1.ConditionFalse),
		},
		currentConfig: "v1",
		ready:         []*corev1.Node{newNodeWithReady("node-1", "v1", "v1", corev1.ConditionTrue)},
	}, {
		// 2 node updated, 1 updating
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", "v0", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-1", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-2", "v1", "v1", corev1.ConditionTrue),
		},
		currentConfig: "v1",
		ready:         []*corev1.Node{newNodeWithReady("node-1", "v1", "v1", corev1.ConditionTrue), newNodeWithReady("node-2", "v1", "v1", corev1.ConditionTrue)},
	}, {
		// 2 node updated, 1 updating, but one updated node is NotReady.
		nodes: []*corev1.Node{
			newNode("node-0", "v0", "v1"),
			newNode("node-1", "v1", "v1"),
			newNodeWithReady("node-2", "v1", "v1", corev1.ConditionFalse),
		},
		currentConfig: "v1",
		ready:         []*corev1.Node{newNode("node-1", "v1", "v1")},
	}}

	for idx, test := range tests {
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			ready := getReadyMachines(test.currentConfig, test.nodes)
			if !reflect.DeepEqual(ready, test.ready) {
				t.Fatalf("mismatch expected: %v got %v", test.ready, ready)
			}
		})
	}
}

func TestGetUnavailableMachines(t *testing.T) {
	tests := []struct {
		nodes         []*corev1.Node
		currentConfig string
		unavail       []string
	}{{
		// no nodes
		nodes:         []*corev1.Node{},
		currentConfig: "v1",
		unavail:       nil,
	}, {
		// 1 in progress
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", "v0", "v0", corev1.ConditionTrue),
			newNodeWithReady("node-1", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-2", "v0", "v1", corev1.ConditionTrue),
		},
		currentConfig: "v1",
		unavail:       []string{"node-2"},
	}, {
		// 1 unavail, 1 in progress
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", "v0", "v0", corev1.ConditionTrue),
			newNodeWithReady("node-1", "v1", "v1", corev1.ConditionFalse),
			newNodeWithReady("node-2", "v0", "v1", corev1.ConditionTrue),
		},
		currentConfig: "v1",
		unavail:       []string{"node-1", "node-2"},
	}, {
		// 1 node updated, 1 updating, 1 updating but not v2 and is ready
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", "v0", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-1", "v2", "v2", corev1.ConditionTrue),
			newNodeWithReady("node-2", "v0", "v2", corev1.ConditionTrue),
		},
		currentConfig: "v2",
		unavail:       []string{"node-0", "node-2"},
	}, {
		// 1 node updated, 1 updating, 1 updating but not v2 and is not ready
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", "v0", "v1", corev1.ConditionFalse),
			newNodeWithReady("node-1", "v2", "v2", corev1.ConditionTrue),
			newNodeWithReady("node-2", "v0", "v2", corev1.ConditionTrue),
		},
		currentConfig: "v2",
		unavail:       []string{"node-0", "node-2"},
	}, {
		// 2 node updated, 1 updating
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", "v0", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-1", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-2", "v1", "v1", corev1.ConditionFalse),
		},
		currentConfig: "v1",
		unavail:       []string{"node-0", "node-2"},
	}, {
		// 2 node updated, 1 updating, but one updated node is NotReady.
		nodes: []*corev1.Node{
			newNode("node-0", "v0", "v1"),
			newNode("node-1", "v1", "v1"),
			newNodeWithReady("node-2", "v1", "v1", corev1.ConditionFalse),
		},
		currentConfig: "v1",
		unavail:       []string{"node-0", "node-2"},
	}}

	for idx, test := range tests {
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			fmt.Printf("Starting case %d\n", idx)
			unavail := getUnavailableMachines(test.nodes)
			var unavailNames []string
			for _, node := range unavail {
				unavailNames = append(unavailNames, node.Name)
			}
			if !reflect.DeepEqual(unavailNames, test.unavail) {
				t.Fatalf("mismatch expected: %v got %v", test.unavail, unavailNames)
			}
		})
	}
}

func TestCalculateStatus(t *testing.T) {
	tests := []struct {
		nodes         []*corev1.Node
		currentConfig string

		verify func(mcfgv1.MachineConfigPoolStatus, *testing.T)
	}{{
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", "v0", "v0", corev1.ConditionTrue),
			newNodeWithReady("node-1", "v0", "v0", corev1.ConditionTrue),
			newNodeWithReady("node-2", "v0", "v0", corev1.ConditionTrue),
		},
		currentConfig: "v1",
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

			condupdated := mcfgv1.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}

			condupdating := mcfgv1.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
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
			newNodeWithReady("node-0", "v0", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-1", "v0", "v0", corev1.ConditionTrue),
			newNodeWithReady("node-2", "v0", "v0", corev1.ConditionTrue),
		},
		currentConfig: "v1",
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

			condupdated := mcfgv1.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}

			condupdating := mcfgv1.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
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
			newNodeWithReady("node-0", "v0", "v1", corev1.ConditionFalse),
			newNodeWithReady("node-1", "v0", "v0", corev1.ConditionTrue),
			newNodeWithReady("node-2", "v0", "v0", corev1.ConditionTrue),
		},
		currentConfig: "v1",
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

			condupdated := mcfgv1.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}

			condupdating := mcfgv1.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
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
			newNodeWithReadyAndDaemonState("node-0", "v0", "v1", corev1.ConditionFalse, daemonconsts.MachineConfigDaemonStateDegraded),
			newNodeWithReady("node-1", "v0", "v0", corev1.ConditionTrue),
			newNodeWithReady("node-2", "v0", "v0", corev1.ConditionTrue),
		},
		currentConfig: "v1",
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

			condupdated := mcfgv1.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}

			condupdating := mcfgv1.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
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
			newNodeWithReady("node-0", "v1", "v1", corev1.ConditionFalse),
			newNodeWithReady("node-1", "v0", "v0", corev1.ConditionTrue),
			newNodeWithReady("node-2", "v0", "v0", corev1.ConditionTrue),
		},
		currentConfig: "v1",
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

			condupdated := mcfgv1.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}

			condupdating := mcfgv1.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
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
			newNodeWithReady("node-0", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-1", "v0", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-2", "v0", "v1", corev1.ConditionTrue),
		},
		currentConfig: "v1",
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

			condupdated := mcfgv1.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}

			condupdating := mcfgv1.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
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
			newNodeWithReady("node-0", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-1", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-2", "v1", "v1", corev1.ConditionTrue),
		},
		currentConfig: "v1",
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

			condupdated := mcfgv1.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
			if condupdated == nil {
				t.Fatal("updated condition not found")
			}

			condupdating := mcfgv1.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
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
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			pool := &mcfgv1.MachineConfigPool{
				Spec: mcfgv1.MachineConfigPoolSpec{
					Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{ObjectReference: corev1.ObjectReference{Name: test.currentConfig}},
				},
			}
			status := calculateStatus(pool, test.nodes)
			test.verify(status, t)
		})
	}
}
