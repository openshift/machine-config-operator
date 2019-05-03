package node

import (
	"fmt"
	"strings"

	"github.com/golang/glog"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (ctrl *Controller) syncStatusOnly(pool *mcfgv1.MachineConfigPool) error {
	selector, err := metav1.LabelSelectorAsSelector(pool.Spec.NodeSelector)
	if err != nil {
		return err
	}
	nodes, err := ctrl.nodeLister.List(selector)
	if err != nil {
		return err
	}

	newStatus := calculateStatus(pool, nodes)
	if equality.Semantic.DeepEqual(pool.Status, newStatus) {
		return nil
	}

	newPool := pool
	newPool.Status = newStatus
	_, err = ctrl.client.MachineconfigurationV1().MachineConfigPools().UpdateStatus(newPool)
	return err
}

func calculateStatus(pool *mcfgv1.MachineConfigPool, nodes []*corev1.Node) mcfgv1.MachineConfigPoolStatus {
	machineCount := int32(len(nodes))

	updatedMachines := getUpdatedMachines(pool.Status.Configuration.Name, nodes)
	updatedMachineCount := int32(len(updatedMachines))

	readyMachines := getReadyMachines(pool.Status.Configuration.Name, nodes)
	readyMachineCount := int32(len(readyMachines))

	unavailableMachines := getUnavailableMachines(pool.Status.Configuration.Name, nodes)
	unavailableMachineCount := int32(len(unavailableMachines))

	degradedMachines := getDegradedMachines(pool.Status.Configuration.Name, nodes)
	degradedReasons := []string{}
	for _, n := range degradedMachines {
		reason, ok := n.Annotations[daemonconsts.MachineConfigDaemonReasonAnnotationKey]
		if ok && reason != "" {
			degradedReasons = append(degradedReasons, fmt.Sprintf("Node %s is reporting: %q", n.Name, reason))
		}
	}
	degradedMachineCount := int32(len(degradedMachines))

	status := mcfgv1.MachineConfigPoolStatus{
		ObservedGeneration:      pool.Generation,
		MachineCount:            machineCount,
		UpdatedMachineCount:     updatedMachineCount,
		ReadyMachineCount:       readyMachineCount,
		UnavailableMachineCount: unavailableMachineCount,
		DegradedMachineCount:    degradedMachineCount,
	}

	status.Configuration = pool.Status.Configuration

	conditions := pool.Status.Conditions
	for i := range conditions {
		status.Conditions = append(status.Conditions, conditions[i])
	}

	if updatedMachineCount == machineCount &&
		readyMachineCount == machineCount &&
		unavailableMachineCount == 0 {
		//TODO: update api to only have one condition regarding status of update.
		supdated := mcfgv1.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolUpdated, corev1.ConditionTrue, fmt.Sprintf("All nodes are updated with %s", pool.Status.Configuration.Name), "")
		mcfgv1.SetMachineConfigPoolCondition(&status, *supdated)
		supdating := mcfgv1.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolUpdating, corev1.ConditionFalse, "", "")
		mcfgv1.SetMachineConfigPoolCondition(&status, *supdating)
	} else {
		supdated := mcfgv1.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolUpdated, corev1.ConditionFalse, "", "")
		mcfgv1.SetMachineConfigPoolCondition(&status, *supdated)
		supdating := mcfgv1.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolUpdating, corev1.ConditionTrue, fmt.Sprintf("All nodes are updating to %s", pool.Status.Configuration.Name), "")
		mcfgv1.SetMachineConfigPoolCondition(&status, *supdating)
	}

	var nodeDegraded bool
	if degradedMachineCount > 0 {
		nodeDegraded = true
		sdegraded := mcfgv1.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolNodeDegraded, corev1.ConditionTrue, fmt.Sprintf("%d nodes are reporting degraded status on sync", len(degradedMachines)), strings.Join(degradedReasons, ", "))
		mcfgv1.SetMachineConfigPoolCondition(&status, *sdegraded)
	} else {
		sdegraded := mcfgv1.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolNodeDegraded, corev1.ConditionFalse, "", "")
		mcfgv1.SetMachineConfigPoolCondition(&status, *sdegraded)
	}

	// here we now set the MCP Degraded field, the node_controller is the one making the call right now
	// but we might have a dedicated controller or control loop somewhere else that understands how to
	// set Degraded. For now, the node_controller understand NodeDegraded & RenderDegraded = Degraded.
	renderDegraded := mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolRenderDegraded)
	if nodeDegraded || renderDegraded {
		sdegraded := mcfgv1.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolDegraded, corev1.ConditionTrue, "", "")
		mcfgv1.SetMachineConfigPoolCondition(&status, *sdegraded)
	} else {
		sdegraded := mcfgv1.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolDegraded, corev1.ConditionFalse, "", "")
		mcfgv1.SetMachineConfigPoolCondition(&status, *sdegraded)
	}

	return status
}

func getUpdatedMachines(currentConfig string, nodes []*corev1.Node) []*corev1.Node {
	var updated []*corev1.Node
	for _, node := range nodes {
		if node.Annotations == nil {
			continue
		}
		cconfig, ok := node.Annotations[daemonconsts.CurrentMachineConfigAnnotationKey]
		if !ok || cconfig == "" {
			continue
		}
		dconfig, ok := node.Annotations[daemonconsts.DesiredMachineConfigAnnotationKey]
		if !ok || cconfig == "" {
			continue
		}

		dstate, ok := node.Annotations[daemonconsts.MachineConfigDaemonStateAnnotationKey]
		if !ok || dstate == "" {
			continue
		}

		if cconfig == currentConfig && dconfig == currentConfig && dstate == daemonconsts.MachineConfigDaemonStateDone {
			updated = append(updated, node)
		}
	}
	return updated
}

func getReadyMachines(currentConfig string, nodes []*corev1.Node) []*corev1.Node {
	updated := getUpdatedMachines(currentConfig, nodes)
	var ready []*corev1.Node
	for _, node := range updated {
		if isNodeReady(node) {
			ready = append(ready, node)
		}
	}
	return ready
}

func isNodeReady(node *corev1.Node) bool {
	for i := range node.Status.Conditions {
		cond := &node.Status.Conditions[i]
		// We consider the node for scheduling only when its:
		// - NodeReady condition status is ConditionTrue,
		// - NodeOutOfDisk condition status is ConditionFalse,
		// - NodeNetworkUnavailable condition status is ConditionFalse.
		if cond.Type == corev1.NodeReady && cond.Status != corev1.ConditionTrue {
			glog.Infof("Node %s is reporting NotReady", node.Name)
			return false
		}
		if cond.Type == corev1.NodeOutOfDisk && cond.Status != corev1.ConditionFalse {
			glog.Infof("Node %s is reporting OutOfDisk", node.Name)
			return false
		}
		if cond.Type == corev1.NodeNetworkUnavailable && cond.Status != corev1.ConditionFalse {
			glog.Infof("Node %s is reporting NetworkUnavailable", node.Name)
			return false
		}
	}
	// Ignore nodes that are marked unschedulable
	if node.Spec.Unschedulable {
		glog.Infof("Node %s is reporting Unschedulable", node.Name)
		return false
	}
	return true
}

// TODO(runcom): drop currentConfig arg
func getUnavailableMachines(currentConfig string, nodes []*corev1.Node) []*corev1.Node {
	var unavail []*corev1.Node
	for _, node := range nodes {
		if node.Annotations == nil {
			continue
		}
		dconfig, ok := node.Annotations[daemonconsts.DesiredMachineConfigAnnotationKey]
		if !ok || dconfig == "" {
			continue
		}
		cconfig, ok := node.Annotations[daemonconsts.CurrentMachineConfigAnnotationKey]
		if !ok || cconfig == "" {
			continue
		}
		dstate, ok := node.Annotations[daemonconsts.MachineConfigDaemonStateAnnotationKey]
		if !ok || dstate == "" {
			continue
		}

		nodeNotReady := !isNodeReady(node)
		// we want to be able to roll back if a bad MC caused an unreconcilable state
		if (dconfig != cconfig || nodeNotReady) && dstate != daemonconsts.MachineConfigDaemonStateUnreconcilable {
			unavail = append(unavail, node)
			glog.V(2).Infof("Node %s unavailable: different configs (desired: %s, current %s) or node not ready %v", node.Name, dconfig, cconfig, nodeNotReady)
		}
	}
	return unavail
}

func getDegradedMachines(currentConfig string, nodes []*corev1.Node) []*corev1.Node {
	var degraded []*corev1.Node
	for _, node := range nodes {
		if node.Annotations == nil {
			continue
		}
		dconfig, ok := node.Annotations[daemonconsts.DesiredMachineConfigAnnotationKey]
		if !ok || dconfig == "" {
			continue
		}
		dstate, ok := node.Annotations[daemonconsts.MachineConfigDaemonStateAnnotationKey]
		if !ok || dstate == "" {
			continue
		}

		if dconfig == currentConfig &&
			(dstate == daemonconsts.MachineConfigDaemonStateDegraded || dstate == daemonconsts.MachineConfigDaemonStateUnreconcilable) {
			degraded = append(degraded, node)
		}
	}
	return degraded
}
