package node

import (
	"fmt"
	"reflect"
	"testing"

	features "github.com/openshift/api/features"

	apicfgv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	informers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
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

func newNode(name string, currentConfig, desiredConfig string) *corev1.Node {
	nb := helpers.NewNodeBuilder(name)
	nb.WithCurrentConfig(currentConfig)
	nb.WithDesiredConfig(desiredConfig)
	return nb.Node()
}

func newNodeWithLabels(name string, labels map[string]string) *corev1.Node {
	return helpers.NewNodeBuilder(name).WithLabels(labels).Node()
}

func newNodeWithLabel(name string, currentConfig, desiredConfig string, labels map[string]string) *corev1.Node {
	nb := helpers.NewNodeBuilder(name)
	nb.WithCurrentConfig(currentConfig)
	nb.WithDesiredConfig(desiredConfig)
	nb.WithLabels(labels)
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

func TestGetUnavailableMachines(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		nodes   []*corev1.Node
		unavail []string
	}{
		{
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

			unavailableNodes := getUnavailableMachines(test.nodes)
			assertExpectedNodes(t, test.unavail, unavailableNodes)
		})
	}
}

func assertExpectedNodes(t *testing.T, expected []string, actual []*corev1.Node) {
	t.Helper()
	assert.Equal(t, expected, helpers.GetNamesFromNodes(actual))
}

// expectedPoolStatus holds the expected values for assertPoolStatus. Fields that
// are nil / zero/"" are skipped.
type expectedPoolStatus struct {
	machineCount            *int32
	updatedMachineCount     *int32
	readyMachineCount       *int32
	unavailableMachineCount *int32
	degradedMachineCount    *int32
	updated                 corev1.ConditionStatus // "" = don't check
	updating                corev1.ConditionStatus // "" = don't check
	degraded                corev1.ConditionStatus // "" = don't check
	updatingMsg             string                 // "" = don't check
	osImageStreamName       *string                // nil = don't check
}

// assertPoolStatus takes in the expected machine counts and pool status and confirms they match
// what is set in the pool's status.
func assertPoolStatus(t *testing.T, status mcfgv1.MachineConfigPoolStatus, want expectedPoolStatus) {
	t.Helper()
	if want.machineCount != nil {
		assert.Equal(t, *want.machineCount, status.MachineCount, "MachineCount")
	}
	if want.updatedMachineCount != nil {
		assert.Equal(t, *want.updatedMachineCount, status.UpdatedMachineCount, "UpdatedMachineCount")
	}
	if want.readyMachineCount != nil {
		assert.Equal(t, *want.readyMachineCount, status.ReadyMachineCount, "ReadyMachineCount")
	}
	if want.unavailableMachineCount != nil {
		assert.Equal(t, *want.unavailableMachineCount, status.UnavailableMachineCount, "UnavailableMachineCount")
	}
	if want.degradedMachineCount != nil {
		assert.Equal(t, *want.degradedMachineCount, status.DegradedMachineCount, "DegradedMachineCount")
	}
	if want.updated != "" {
		cond := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdated)
		if assert.NotNil(t, cond, "Updated condition not found") {
			assert.Equal(t, want.updated, cond.Status, "Updated condition")
		}
	}
	if want.updating != "" {
		cond := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolUpdating)
		if assert.NotNil(t, cond, "Updating condition not found") {
			assert.Equal(t, want.updating, cond.Status, "Updating condition")
			if want.updatingMsg != "" {
				assert.Equal(t, want.updatingMsg, cond.Message, "Updating condition message")
			}
		}
	}
	if want.degraded != "" {
		cond := apihelpers.GetMachineConfigPoolCondition(status, mcfgv1.MachineConfigPoolDegraded)
		if assert.NotNil(t, cond, "Degraded condition not found") {
			assert.Equal(t, want.degraded, cond.Status, "Degraded condition")
		}
	}
	if want.osImageStreamName != nil {
		assert.Equal(t, *want.osImageStreamName, status.OSImageStream.Name, "OSImageStream.Name")
	}
}

func TestCalculateStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                    string
		nodes                   []*corev1.Node
		currentConfig           string
		paused                  bool
		overrideMosb            bool
		mosb                    *mcfgv1.MachineOSBuild
		osStream                mcfgv1.OSImageStreamReference
		needsOSImageStreamSetup bool
		initialConditions       []mcfgv1.MachineConfigPoolCondition
		verify                  func(mcfgv1.MachineConfigPoolStatus, *testing.T)
	}{
		{
			name: "0 nodes updated, 0 nodes updating, 0 nodes degraded",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			},
			currentConfig: machineConfigV1,
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					machineCount:            new(int32(3)),
					updatedMachineCount:     new(int32(0)),
					readyMachineCount:       new(int32(0)),
					unavailableMachineCount: new(int32(0)),
					degradedMachineCount:    new(int32(0)),
					updated:                 corev1.ConditionFalse,
					updating:                corev1.ConditionTrue,
					degraded:                corev1.ConditionFalse,
				})
			},
		},
		{
			name: "0 nodes updated, 1 node updating, 0 nodes degraded",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			},
			currentConfig: machineConfigV1,
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					machineCount:            new(int32(3)),
					updatedMachineCount:     new(int32(0)),
					readyMachineCount:       new(int32(0)),
					unavailableMachineCount: new(int32(1)),
					degradedMachineCount:    new(int32(0)),
					updated:                 corev1.ConditionFalse,
					updating:                corev1.ConditionTrue,
					degraded:                corev1.ConditionFalse,
				})
			},
		},
		{
			name: "0 nodes updated, 0 nodes updating, 0 nodes degraded, pool paused",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			},
			currentConfig: machineConfigV1,
			paused:        true,
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					machineCount:            new(int32(3)),
					updatedMachineCount:     new(int32(0)),
					readyMachineCount:       new(int32(0)),
					unavailableMachineCount: new(int32(1)),
					degradedMachineCount:    new(int32(0)),
					updated:                 corev1.ConditionFalse,
					updating:                corev1.ConditionFalse,
					degraded:                corev1.ConditionFalse,
				})
			},
		},
		{
			name: "pool paused, mosc exists but no mosb, waiting for build to start",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			},
			currentConfig: machineConfigV1,
			paused:        true,
			overrideMosb:  true,
			mosb:          nil,
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionTrue,
					updatingMsg: "Pool is paused; waiting for a new OS image build to start (mosc: mosc-1)",
				})
			},
		},
		{
			name: "pool paused, mosc but is in initial state",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			},
			currentConfig: machineConfigV1,
			paused:        true,
			overrideMosb:  true,
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV1).WithBuildInitialState().MachineOSBuild(),
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionTrue,
					updatingMsg: "Pool is paused; OS image build has been created but not yet started (mosb: mosb-1)",
				})
			},
		},
		{
			name: "pool paused, build prepared",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			},
			currentConfig: machineConfigV1,
			paused:        true,
			overrideMosb:  true,
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV1).WithBuildPrepared().MachineOSBuild(),
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionTrue,
					updatingMsg: "Pool is paused; OS image build has been prepared but will not rollout (mosb: mosb-1)",
				})
			},
		},
		{
			name: "pool paused, build in progress",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			},
			currentConfig: machineConfigV1,
			paused:        true,
			overrideMosb:  true,
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV1).WithBuildInProgress().MachineOSBuild(),
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionTrue,
					updatingMsg: "Pool is paused; OS image build in progress (mosb: mosb-1)",
				})
			},
		},
		{
			name: "pool paused, build failed",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			},
			currentConfig: machineConfigV1,
			paused:        true,
			overrideMosb:  true,
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV1).WithFailedBuild().MachineOSBuild(),
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionFalse,
					updatingMsg: "Pool is paused; OS image build failed (mosb: mosb-1)",
				})
			},
		},
		{
			name: "pool paused, build interrupted",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			},
			currentConfig: machineConfigV1,
			paused:        true,
			overrideMosb:  true,
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV1).WithInterruptedBuild().MachineOSBuild(),
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionFalse,
					updatingMsg: "Pool is paused; OS image build was interrupted (mosb: mosb-1)",
				})
			},
		},
		{
			name: "pool paused, build succeeded",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			},
			currentConfig: machineConfigV1,
			paused:        true,
			overrideMosb:  true,
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV1).WithSuccessfulBuild().MachineOSBuild(),
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionFalse,
					updatingMsg: "Pool is paused; OS image build completed successfully (mosb: mosb-1)",
				})
			},
		},
		{
			name: "pool not paused, mosc exists but no mosb, waiting for build to start",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			},
			currentConfig: machineConfigV1,
			overrideMosb:  true,
			mosb:          nil,
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionTrue,
					updatingMsg: "Pool is waiting for a new OS image build to start (mosc: mosc-1)",
				})
			},
		},
		{
			name: "pool not paused, build prepared",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			},
			currentConfig: machineConfigV1,
			overrideMosb:  true,
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV1).WithBuildPrepared().MachineOSBuild(),
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionTrue,
					updatingMsg: "Pool is waiting for OS image build to start (mosb: mosb-1)",
				})
			},
		},
		{
			name: "pool not paused, build in progress",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			},
			currentConfig: machineConfigV1,
			overrideMosb:  true,
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV1).WithBuildInProgress().MachineOSBuild(),
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionTrue,
					updatingMsg: "Pool is waiting for OS image build to complete (mosb: mosb-1)",
				})
			},
		},
		{
			name: "pool not paused, build failed",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			},
			currentConfig: machineConfigV1,
			overrideMosb:  true,
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV1).WithFailedBuild().MachineOSBuild(),
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionFalse,
					updatingMsg: "Pool update stopped due to OS image build failure (mosb: mosb-1)",
				})
			},
		},
		{
			name: "pool not paused, build interrupted",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			},
			currentConfig: machineConfigV1,
			overrideMosb:  true,
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV1).WithInterruptedBuild().MachineOSBuild(),
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionFalse,
					updatingMsg: "Pool update stopped due to OS image build being interrupted (mosb: mosb-1)",
				})
			},
		},
		{
			name: "pool not paused, build in initial state",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			},
			currentConfig: machineConfigV1,
			overrideMosb:  true,
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV1).WithBuildInitialState().MachineOSBuild(),
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionTrue,
					updatingMsg: "Pool is waiting for OS image build to start (mosb: mosb-1)",
				})
			},
		},
		{
			name: "pool not paused, build succeeded, nodes still applying",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			},
			currentConfig: machineConfigV1,
			overrideMosb:  true,
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV1).WithSuccessfulBuild().MachineOSBuild(),
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionTrue,
					updatingMsg: "Pool is waiting for nodes to apply OS image (mosb: mosb-1)",
				})
			},
		},
		{
			name: "pool not paused, build succeeded, all nodes updated",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReadyAndDaemonStateAndImageAnnos("node-0", machineConfigV1, machineConfigV1, "registry.host.com/org/repo@sha256:12345", "registry.host.com/org/repo@sha256:12345", daemonconsts.MachineConfigDaemonStateDone, corev1.ConditionTrue),
				helpers.NewNodeWithReadyAndDaemonStateAndImageAnnos("node-1", machineConfigV1, machineConfigV1, "registry.host.com/org/repo@sha256:12345", "registry.host.com/org/repo@sha256:12345", daemonconsts.MachineConfigDaemonStateDone, corev1.ConditionTrue),
				helpers.NewNodeWithReadyAndDaemonStateAndImageAnnos("node-2", machineConfigV1, machineConfigV1, "registry.host.com/org/repo@sha256:12345", "registry.host.com/org/repo@sha256:12345", daemonconsts.MachineConfigDaemonStateDone, corev1.ConditionTrue),
			},
			currentConfig: machineConfigV1,
			overrideMosb:  true,
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV1).WithDigestedImagePushspec("registry.host.com/org/repo@sha256:12345").WithSuccessfulBuild().MachineOSBuild(),
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updated:  corev1.ConditionTrue,
					updating: corev1.ConditionFalse,
				})
			},
		},
		{
			name: "0 nodes updated, 1 node updating, 0 nodes degraded",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionFalse),
				helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			},
			currentConfig: machineConfigV1,
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					machineCount:            new(int32(3)),
					updatedMachineCount:     new(int32(0)),
					readyMachineCount:       new(int32(0)),
					unavailableMachineCount: new(int32(1)),
					degradedMachineCount:    new(int32(0)),
					updated:                 corev1.ConditionFalse,
					updating:                corev1.ConditionTrue,
					degraded:                corev1.ConditionFalse,
				})
			},
		},
		{
			name: "0 nodes updated, 1 node updating, 1 node degraded",
			nodes: []*corev1.Node{
				newNodeWithReadyAndDaemonState("node-0", machineConfigV0, machineConfigV1, corev1.ConditionFalse, daemonconsts.MachineConfigDaemonStateDegraded),
				helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			},
			currentConfig: machineConfigV1,
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					machineCount:            new(int32(3)),
					updatedMachineCount:     new(int32(0)),
					readyMachineCount:       new(int32(0)),
					unavailableMachineCount: new(int32(1)),
					degradedMachineCount:    new(int32(1)),
					updated:                 corev1.ConditionFalse,
					updating:                corev1.ConditionTrue,
					degraded:                corev1.ConditionTrue,
				})
			},
		},
		{
			name: "1 node updated, 1 node updating, 0 nodes degraded",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReady("node-0", machineConfigV1, machineConfigV1, corev1.ConditionFalse),
				helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			},
			currentConfig: machineConfigV1,
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					machineCount:            new(int32(3)),
					updatedMachineCount:     new(int32(1)),
					readyMachineCount:       new(int32(0)),
					unavailableMachineCount: new(int32(1)),
					degradedMachineCount:    new(int32(0)),
					updated:                 corev1.ConditionFalse,
					updating:                corev1.ConditionTrue,
					degraded:                corev1.ConditionFalse,
				})
			},
		},
		{
			name: "1 node updated, 2 nodes updating, 0 nodes degraded",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReady("node-0", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
			},
			currentConfig: machineConfigV1,
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					machineCount:            new(int32(3)),
					updatedMachineCount:     new(int32(1)),
					readyMachineCount:       new(int32(1)),
					unavailableMachineCount: new(int32(2)),
					degradedMachineCount:    new(int32(0)),
					updated:                 corev1.ConditionFalse,
					updating:                corev1.ConditionTrue,
					degraded:                corev1.ConditionFalse,
				})
			},
		},
		{
			name: "3 nodes updated, 0 nodes updating, 0 nodes degraded",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReady("node-0", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-1", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
			},
			currentConfig: machineConfigV1,
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					machineCount:            new(int32(3)),
					updatedMachineCount:     new(int32(3)),
					readyMachineCount:       new(int32(3)),
					unavailableMachineCount: new(int32(0)),
					degradedMachineCount:    new(int32(0)),
					updated:                 corev1.ConditionTrue,
					updating:                corev1.ConditionFalse,
					degraded:                corev1.ConditionFalse,
				})
			},
		},
		{
			name: "OSImageStream is empty when OSImageStream CR does not exist",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			},
			currentConfig: machineConfigV0,
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					machineCount:            new(int32(3)),
					updatedMachineCount:     new(int32(3)),
					readyMachineCount:       new(int32(3)),
					unavailableMachineCount: new(int32(0)),
					updated:                 corev1.ConditionTrue,
					updating:                corev1.ConditionFalse,
					// When OSImageStream CR does not exist, status.OSImageStream should be empty
					osImageStreamName: new(""),
				})
			},
		},
		{
			name: "OSImageStream status populated when pool updated and osImageURL matches stream",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			},
			currentConfig:           machineConfigV0,
			needsOSImageStreamSetup: true,
			initialConditions: []mcfgv1.MachineConfigPoolCondition{
				{Type: mcfgv1.MachineConfigPoolUpdated, Status: corev1.ConditionTrue},
				{Type: mcfgv1.MachineConfigPoolUpdating, Status: corev1.ConditionFalse},
				{Type: mcfgv1.MachineConfigPoolDegraded, Status: corev1.ConditionFalse},
			},
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updated:           corev1.ConditionTrue,
					degraded:          corev1.ConditionFalse,
					osImageStreamName: new("rhel-9"),
				})
			},
		},
		{
			name: "OSImageStream status empty when pool updated but osImageURL doesn't match any stream (override)",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReady("node-0", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-1", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV1, machineConfigV1, corev1.ConditionTrue),
			},
			currentConfig:           machineConfigV1,
			needsOSImageStreamSetup: true,
			initialConditions: []mcfgv1.MachineConfigPoolCondition{
				{Type: mcfgv1.MachineConfigPoolUpdated, Status: corev1.ConditionTrue},
				{Type: mcfgv1.MachineConfigPoolUpdating, Status: corev1.ConditionFalse},
				{Type: mcfgv1.MachineConfigPoolDegraded, Status: corev1.ConditionFalse},
			},
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updated: corev1.ConditionTrue,
					// OSImageStream status should be empty (override scenario)
					osImageStreamName: new(""),
				})
			},
		},
		{
			name: "OSImageStream status empty when pool is updating",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReady("node-0", machineConfigV0, machineConfigV1, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			},
			currentConfig:           machineConfigV1,
			needsOSImageStreamSetup: true,
			// No initialConditions - let calculateStatus set them based on node states
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating: corev1.ConditionTrue,
					updated:  corev1.ConditionFalse,
					// OSImageStream status should be empty when pool is updating
					osImageStreamName: new(""),
				})
			},
		},
		{
			name: "OSImageStream status empty when pool is degraded",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReadyAndDaemonStateAndImageAnnos("node-0", machineConfigV0, machineConfigV0, "", "", daemonconsts.MachineConfigDaemonStateDegraded, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-1", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV0, machineConfigV0, corev1.ConditionTrue),
			},
			currentConfig:           machineConfigV0,
			needsOSImageStreamSetup: true,
			initialConditions: []mcfgv1.MachineConfigPoolCondition{
				{Type: mcfgv1.MachineConfigPoolUpdated, Status: corev1.ConditionFalse},
				{Type: mcfgv1.MachineConfigPoolUpdating, Status: corev1.ConditionFalse},
				{Type: mcfgv1.MachineConfigPoolDegraded, Status: corev1.ConditionTrue},
			},
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					degraded: corev1.ConditionTrue,
					// OSImageStream status should be empty when pool is degraded
					osImageStreamName: new(""),
				})
			},
		},
		{
			name: "OSImageStream status empty when rendered config has empty osImageURL",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReady("node-0", machineConfigV2, machineConfigV2, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-1", machineConfigV2, machineConfigV2, corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", machineConfigV2, machineConfigV2, corev1.ConditionTrue),
			},
			currentConfig:           machineConfigV2,
			needsOSImageStreamSetup: true,
			initialConditions: []mcfgv1.MachineConfigPoolCondition{
				{Type: mcfgv1.MachineConfigPoolUpdated, Status: corev1.ConditionTrue},
				{Type: mcfgv1.MachineConfigPoolUpdating, Status: corev1.ConditionFalse},
				{Type: mcfgv1.MachineConfigPoolDegraded, Status: corev1.ConditionFalse},
			},
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updated: corev1.ConditionTrue,
					// OSImageStream status should be empty when osImageURL is empty
					osImageStreamName: new(""),
				})
			},
		},
		{
			name: "OSImageStream status matches second stream when osImageURL matches it",
			nodes: []*corev1.Node{
				helpers.NewNodeWithReady("node-0", "rendered-rhel10", "rendered-rhel10", corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-1", "rendered-rhel10", "rendered-rhel10", corev1.ConditionTrue),
				helpers.NewNodeWithReady("node-2", "rendered-rhel10", "rendered-rhel10", corev1.ConditionTrue),
			},
			currentConfig:           "rendered-rhel10",
			needsOSImageStreamSetup: true,
			initialConditions: []mcfgv1.MachineConfigPoolCondition{
				{Type: mcfgv1.MachineConfigPoolUpdated, Status: corev1.ConditionTrue},
				{Type: mcfgv1.MachineConfigPoolUpdating, Status: corev1.ConditionFalse},
				{Type: mcfgv1.MachineConfigPoolDegraded, Status: corev1.ConditionFalse},
			},
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updated:           corev1.ConditionTrue,
					osImageStreamName: new("rhel-10"),
				})
			},
		},
	}
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
				Status: mcfgv1.MachineConfigPoolStatus{
					Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
						ObjectReference: corev1.ObjectReference{Name: test.currentConfig},
					},
				},
			}
			f := newFixtureWithFeatureGates(t,
				[]apicfgv1.FeatureGateName{
					features.FeatureGateOSStreams,
				},
				[]apicfgv1.FeatureGateName{},
			)

			var c *Controller
			if test.needsOSImageStreamSetup {
				// Set pool conditions if specified in the test
				if len(test.initialConditions) > 0 {
					pool.Status.Conditions = test.initialConditions
				}

				// Add MachineConfig objects with different osImageURLs for testing
				mc0 := &mcfgv1.MachineConfig{
					ObjectMeta: metav1.ObjectMeta{Name: machineConfigV0},
					Spec: mcfgv1.MachineConfigSpec{
						OSImageURL: "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:rhel9image",
					},
				}
				mc1 := &mcfgv1.MachineConfig{
					ObjectMeta: metav1.ObjectMeta{Name: machineConfigV1},
					Spec: mcfgv1.MachineConfigSpec{
						OSImageURL: "quay.io/custom/custom-image:latest", // Custom URL that doesn't match any stream
					},
				}
				mc2 := &mcfgv1.MachineConfig{
					ObjectMeta: metav1.ObjectMeta{Name: machineConfigV2},
					Spec: mcfgv1.MachineConfigSpec{
						OSImageURL: "", // Empty osImageURL
					},
				}
				mcRhel10 := &mcfgv1.MachineConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "rendered-rhel10"},
					Spec: mcfgv1.MachineConfigSpec{
						OSImageURL: "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:rhel10image",
					},
				}
				// Add OSImageStream CR with available streams
				osImageStream := &mcfgv1.OSImageStream{
					ObjectMeta: metav1.ObjectMeta{
						Name: ctrlcommon.ClusterInstanceNameOSImageStream,
					},
					Status: mcfgv1.OSImageStreamStatus{
						DefaultStream: "rhel-9",
						AvailableStreams: []mcfgv1.OSImageStreamSet{
							{
								Name:    "rhel-9",
								OSImage: "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:rhel9image",
							},
							{
								Name:    "rhel-10",
								OSImage: "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:rhel10image",
							},
						},
					},
				}

				// Add all test objects to the fixture
				f.objects = append(f.objects, mc0, mc1, mc2, mcRhel10, osImageStream)

				c = f.newController()

				// The controller's informers were already created, but we need to add MC and OSImageStream objects
				// to their indexers. We can't access the informer factory from here, so we'll use the client
				// that was already created and manually create new informers to populate the listers.
				tmpInformer := informers.NewSharedInformerFactory(f.client, noResyncPeriodFunc())
				tmpInformer.Machineconfiguration().V1().MachineConfigs().Informer().GetIndexer().Add(mc0)
				tmpInformer.Machineconfiguration().V1().MachineConfigs().Informer().GetIndexer().Add(mc1)
				tmpInformer.Machineconfiguration().V1().MachineConfigs().Informer().GetIndexer().Add(mc2)
				tmpInformer.Machineconfiguration().V1().MachineConfigs().Informer().GetIndexer().Add(mcRhel10)
				tmpInformer.Machineconfiguration().V1().OSImageStreams().Informer().GetIndexer().Add(osImageStream)

				// Replace the controller's listers with the populated ones
				c.mcLister = tmpInformer.Machineconfiguration().V1().MachineConfigs().Lister()
				c.osImageStreamLister = tmpInformer.Machineconfiguration().V1().OSImageStreams().Lister()
			} else {
				c = f.newController()
			}

			var mosc *mcfgv1.MachineOSConfig
			var mosb *mcfgv1.MachineOSBuild
			if test.overrideMosb {
				mosc = helpers.NewMachineOSConfigBuilder("mosc-1").WithCurrentImagePullspec("registry.host.com/org/repo@sha256:12345").MachineOSConfig()
				mosb = test.mosb
			}

			status := c.calculateStatus([]*mcfgv1.MachineConfigNode{}, nil, pool, test.nodes, mosc, mosb)
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
		overrideMosb  bool
		mosb          *mcfgv1.MachineOSBuild
		verify        func(mcfgv1.MachineConfigPoolStatus, *testing.T)
	}{
		{
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
				assertPoolStatus(t, status, expectedPoolStatus{
					machineCount:            new(int32(3)),
					updatedMachineCount:     new(int32(0)),
					readyMachineCount:       new(int32(0)),
					unavailableMachineCount: new(int32(0)),
					degradedMachineCount:    new(int32(0)),
					updated:                 corev1.ConditionFalse,
					updating:                corev1.ConditionTrue,
					degraded:                corev1.ConditionFalse,
				})
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
				assertPoolStatus(t, status, expectedPoolStatus{
					machineCount:            new(int32(3)),
					updatedMachineCount:     new(int32(0)),
					readyMachineCount:       new(int32(0)),
					unavailableMachineCount: new(int32(1)),
					degradedMachineCount:    new(int32(0)),
					updated:                 corev1.ConditionFalse,
					updating:                corev1.ConditionTrue,
					degraded:                corev1.ConditionFalse,
				})
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
				// The default MachineOSBuild created by the test driver (below) has no build
				// conditions set, so it is in its "initial state". A paused pool with a MOSC/MOSB
				// pair whose build is in its initial state is still considered "Updating" so
				// operators can see that a build has been created but has not started yet.
				assertPoolStatus(t, status, expectedPoolStatus{
					machineCount:            new(int32(3)),
					updatedMachineCount:     new(int32(0)),
					readyMachineCount:       new(int32(0)),
					unavailableMachineCount: new(int32(1)),
					degradedMachineCount:    new(int32(0)),
					updated:                 corev1.ConditionFalse,
					updating:                corev1.ConditionTrue,
					degraded:                corev1.ConditionFalse,
				})
			},
		}, {
			name: "pool paused, mosc exists but no mosb, waiting for build to start",
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
			overrideMosb:  true,
			mosb:          nil,
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionTrue,
					updatingMsg: "Pool is paused; waiting for a new OS image build to start (mosc: mosc-1)",
				})
			},
		}, {
			name: "pool paused, mosb exists but is in initial state",
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
			overrideMosb:  true,
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV0).WithBuildInitialState().MachineOSBuild(),
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionTrue,
					updatingMsg: "Pool is paused; OS image build has been created but not yet started (mosb: mosb-1)",
				})
			},
		}, {
			name: "pool paused, build prepared",
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
			overrideMosb:  true,
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV0).WithBuildPrepared().MachineOSBuild(),
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionTrue,
					updatingMsg: "Pool is paused; OS image build has been prepared but will not rollout (mosb: mosb-1)",
				})
			},
		}, {
			name: "pool paused, build in progress",
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
			overrideMosb:  true,
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV0).WithBuildInProgress().MachineOSBuild(),
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionTrue,
					updatingMsg: "Pool is paused; OS image build in progress (mosb: mosb-1)",
				})
			},
		}, {
			name: "pool paused, build failed",
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
			overrideMosb:  true,
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV0).WithFailedBuild().MachineOSBuild(),
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionFalse,
					updatingMsg: "Pool is paused; OS image build failed (mosb: mosb-1)",
				})
			},
		}, {
			name: "pool paused, build interrupted",
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
			overrideMosb:  true,
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV0).WithInterruptedBuild().MachineOSBuild(),
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionFalse,
					updatingMsg: "Pool is paused; OS image build was interrupted (mosb: mosb-1)",
				})
			},
		}, {
			name: "pool paused, build succeeded",
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
			overrideMosb:  true,
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV0).WithSuccessfulBuild().MachineOSBuild(),
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionFalse,
					updatingMsg: "Pool is paused; OS image build completed successfully (mosb: mosb-1)",
				})
			},
		}, {
			name: "pool not paused, mosc exists but no mosb, waiting for build to start",
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
			overrideMosb:  true,
			mosb:          nil,
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionTrue,
					updatingMsg: "Pool is waiting for a new OS image build to start (mosc: mosc-1)",
				})
			},
		}, {
			name: "pool not paused, build in initial state",
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
			overrideMosb:  true,
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV0).WithBuildInitialState().MachineOSBuild(),
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionTrue,
					updatingMsg: "Pool is waiting for OS image build to start (mosb: mosb-1)",
				})
			},
		}, {
			name: "pool not paused, build prepared",
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
			overrideMosb:  true,
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV0).WithBuildPrepared().MachineOSBuild(),
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionTrue,
					updatingMsg: "Pool is waiting for OS image build to start (mosb: mosb-1)",
				})
			},
		}, {
			name: "pool not paused, build in progress",
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
			overrideMosb:  true,
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV0).WithBuildInProgress().MachineOSBuild(),
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionTrue,
					updatingMsg: "Pool is waiting for OS image build to complete (mosb: mosb-1)",
				})
			},
		}, {
			name: "pool not paused, build failed",
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
			overrideMosb:  true,
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV0).WithFailedBuild().MachineOSBuild(),
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionFalse,
					updatingMsg: "Pool update stopped due to OS image build failure (mosb: mosb-1)",
				})
			},
		}, {
			name: "pool not paused, build interrupted",
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
			overrideMosb:  true,
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV0).WithInterruptedBuild().MachineOSBuild(),
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionFalse,
					updatingMsg: "Pool update stopped due to OS image build being interrupted (mosb: mosb-1)",
				})
			},
		}, {
			name: "pool not paused, build succeeded, nodes still applying",
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
			overrideMosb:  true,
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV0).WithSuccessfulBuild().MachineOSBuild(),
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updating:    corev1.ConditionTrue,
					updatingMsg: "Pool is waiting for nodes to apply OS image (mosb: mosb-1)",
				})
			},
		}, {
			name: "pool not paused, build succeeded, all nodes updated",
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
			overrideMosb:  true,
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV1).WithDigestedImagePushspec("registry.host.com/org/repo@sha256:12345").WithSuccessfulBuild().MachineOSBuild(),
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					updated:  corev1.ConditionTrue,
					updating: corev1.ConditionFalse,
				})
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
				assertPoolStatus(t, status, expectedPoolStatus{
					machineCount:            new(int32(3)),
					updatedMachineCount:     new(int32(0)),
					readyMachineCount:       new(int32(0)),
					unavailableMachineCount: new(int32(1)),
					degradedMachineCount:    new(int32(1)),
					updated:                 corev1.ConditionFalse,
					updating:                corev1.ConditionTrue,
					degraded:                corev1.ConditionTrue,
				})
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
			overrideMosb:  true,
			mosb:          helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(machineConfigV1).WithDigestedImagePushspec("registry.host.com/org/repo@sha256:12345").WithSuccessfulBuild().MachineOSBuild(),
			verify: func(status mcfgv1.MachineConfigPoolStatus, t *testing.T) {
				assertPoolStatus(t, status, expectedPoolStatus{
					machineCount:            new(int32(3)),
					updatedMachineCount:     new(int32(3)),
					readyMachineCount:       new(int32(3)),
					unavailableMachineCount: new(int32(0)),
					degradedMachineCount:    new(int32(0)),
					updated:                 corev1.ConditionTrue,
					updating:                corev1.ConditionFalse,
					degraded:                corev1.ConditionFalse,
				})
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
				assertPoolStatus(t, status, expectedPoolStatus{
					machineCount:         new(int32(3)),
					updatedMachineCount:  new(int32(1)),
					readyMachineCount:    new(int32(1)),
					degradedMachineCount: new(int32(0)),
					updated:              corev1.ConditionFalse,
					updating:             corev1.ConditionTrue,
					degraded:             corev1.ConditionFalse,
				})
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			// Create fixture with our ImageModeStatusReporting feature gate handler
			f := newFixtureWithFeatureGates(t,
				[]apicfgv1.FeatureGateName{
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
			var mosb *mcfgv1.MachineOSBuild
			if test.overrideMosb {
				mosb = test.mosb
			} else {
				mosb = helpers.NewMachineOSBuildBuilder("mosb-1").WithDesiredConfig(test.currentConfig).MachineOSBuild()
			}

			c := f.newController()
			status := c.calculateStatus(test.mcns, nil, pool, test.nodes, mosc, mosb)
			test.verify(status, t)
		})
	}
}
