package node

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	features "github.com/openshift/api/features"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	helpers "github.com/openshift/machine-config-operator/pkg/helpers"
)

// syncStatusOnly for MachineConfigNode
func (ctrl *Controller) syncStatusOnly(pool *mcfgv1.MachineConfigPool) error {
	cc, err := ctrl.ccLister.Get(ctrlcommon.ControllerConfigName)
	if err != nil {
		return err
	}
	nodes, err := ctrl.getNodesForPool(pool)
	if err != nil {
		return err
	}

	machineConfigStates := []*mcfgv1.MachineConfigNode{}
	if ctrl.fgHandler.Enabled(features.FeatureGateMachineConfigNodes) {
		for _, node := range nodes {
			ms, err := ctrl.client.MachineconfigurationV1().MachineConfigNodes().Get(context.TODO(), node.Name, metav1.GetOptions{})
			if err != nil {
				klog.Errorf("Could not find our MachineConfigNode for node. %s: %v", node.Name, err)
				continue
			}
			machineConfigStates = append(machineConfigStates, ms)
		}
	}

	// Get fresh copy of MCP from lister to ensure we have the latest status
	// in case other controllers like build controller updated it recently
	freshPool, err := ctrl.mcpLister.Get(pool.Name)
	if err != nil {
		return fmt.Errorf("could not get fresh MachineConfigPool %q: %w", pool.Name, err)
	}

	mosc, mosb, l, err := ctrl.getConfigAndBuildAndLayeredStatus(freshPool)
	if err != nil {
		return fmt.Errorf("could get MachineOSConfig or MachineOSBuild: %w", err)
	}

	newStatus := ctrl.calculateStatus(machineConfigStates, cc, freshPool, nodes, mosc, mosb)
	if equality.Semantic.DeepEqual(freshPool.Status, newStatus) {
		return nil
	}

	newPool := freshPool.DeepCopy()
	newPool.Status = newStatus
	_, err = ctrl.client.MachineconfigurationV1().MachineConfigPools().UpdateStatus(context.TODO(), newPool, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("could not update MachineConfigPool %q: %w", newPool.Name, err)
	}

	if pool.Spec.Configuration.Name != newPool.Spec.Configuration.Name {
		ctrl.eventRecorder.Eventf(pool, corev1.EventTypeNormal, "Updating", "Pool %s now targeting %s", pool.Name, getPoolUpdateLine(newPool, mosc, l))
	}
	if pool.Status.Configuration.Name != newPool.Status.Configuration.Name {
		ctrl.eventRecorder.Eventf(pool, corev1.EventTypeNormal, "Completed", "Pool %s has completed update to %s", pool.Name, getPoolUpdateLine(newPool, mosc, l))
	}
	return err
}

//nolint:gocyclo,gosec
func (ctrl *Controller) calculateStatus(mcs []*mcfgv1.MachineConfigNode, cconfig *mcfgv1.ControllerConfig, pool *mcfgv1.MachineConfigPool, nodes []*corev1.Node, mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild) mcfgv1.MachineConfigPoolStatus {
	certExpirys := []mcfgv1.CertExpiry{}
	if cconfig != nil {
		for _, cert := range cconfig.Status.ControllerCertificates {
			if cert.BundleFile == "KubeAPIServerServingCAData" {
				certExpirys = append(certExpirys, mcfgv1.CertExpiry{
					Bundle:  cert.BundleFile,
					Subject: cert.Subject,
					Expiry:  cert.NotAfter,
				},
				)
			}
		}
	}
	machineCount := int32(len(nodes))
	poolSynchronizer := newPoolSynchronizer(machineCount)

	isLayeredPool := ctrl.isLayeredPool(mosc, mosb)

	var degradedMachines, readyMachines, updatedMachines, unavailableMachines, updatingMachines []*corev1.Node
	degradedReasons := []string{}
	pisIsEnabled := ctrl.fgHandler.Enabled(features.FeatureGatePinnedImages)
	pinnedImageSetsDegraded := false

	// if we represent updating properly here, we will also represent updating properly in the CO
	// so this solves the cordoning RFE and the upgradeable RFE
	// updating == updatePrepared, updateExecuted, updatedComplete, postAction, cordoning, draining
	// updated == nodeResumed, updated
	// ready == nodeResumed, updated
	// unavailable == draining, cordoned
	// degraded == if the condition.Reason == error
	// this ensures that a MCP only enters Upgradeable==False if the node actually needs to upgrade to the new MC
	for _, state := range mcs {
		var ourNode *corev1.Node
		for _, n := range nodes {
			if state.Name == n.Name {
				ourNode = n
				break
			}
		}
		if ourNode == nil {
			klog.Errorf("Could not find specified node %s", state.Name)
		}
		if len(state.Status.Conditions) == 0 {
			// not ready yet
			break
		}
		if pisIsEnabled {
			if isPinnedImageSetsUpdated(state) {
				poolSynchronizer.SetUpdated(mcfgv1.PinnedImageSets)
			}
		}
		for _, cond := range state.Status.Conditions {
			// populate the degradedReasons from the MachineConfigNodeNodeDegraded condition
			if mcfgv1.StateProgress(cond.Type) == mcfgv1.MachineConfigNodeNodeDegraded && cond.Status == metav1.ConditionTrue {
				degradedMachines = append(degradedMachines, ourNode)
				// Degraded nodes are also unavailable since they are not in a "Done" state
				// and cannot be used for further updates (see IsUnavailableForUpdate)
				unavailableMachines = append(unavailableMachines, ourNode)
				degradedReasons = append(degradedReasons, fmt.Sprintf("Node %s is reporting: %q", ourNode.Name, cond.Message))
				break
			}
			/*
				// TODO (ijanssen): This section of code should be implemented as part of OCPBUGS-57177 after OCPBUGS-32745 is addressed.
				// 	In the current state of the code, the `MachineConfigNodePinnedImageSetsDegraded` condition is wrongly being set to True`
				// 	even when no unintended functionality is occurring, such as many images taking more than 2 minutes to fetch. Thus, it is
				//  not wise to degrade an MCP on a PIS degrade while the PIS degrade is not acting as intended. OCPBUGS-57177 has
				// 	been marked as blocked by OCPBUGS-32745 in Jira, but once the degrade condition is stablilized, this code block should
				// 	cover the fix for OCPBUGS-57177.
					// populate the degradedReasons from the MachineConfigNodePinnedImageSetsDegraded condition
					if pisIsEnabled && mcfgv1.StateProgress(cond.Type) == mcfgv1.MachineConfigNodePinnedImageSetsDegraded && cond.Status == metav1.ConditionTrue {
						degradedMachines = append(degradedMachines, ourNode)
						degradedReasons = append(degradedReasons, fmt.Sprintf("Node %s references an invalid PinnedImageSet. See the node's MachineConfigNode resource for details.", ourNode.Name))
						if mcfgv1.StateProgress(cond.Type) == mcfgv1.MachineConfigNodePinnedImageSetsDegraded {
							pinnedImageSetsDegraded = true
						}
						break
					}
			*/
			/*
				// TODO: (djoshy) Rework this block to use MCN conditions correctly. See: https://issues.redhat.com/browse/MCO-1228

				// Specifically, the main concerns for the following block are:
				// (i) Why are only unknown conditions being evaluated? Shouldn't True/False conditions be used?
				// (ii) Multiple conditions can be unknown at the same time, resulting in certain machines being double counted

				// Some background:
				// The MCN conditions are used to feed MCP statuses if the machine counts add up correctly to the total node count.
				// If this check fails, node conditions are used to determine machine counts. The MCN counts calculated in this block
				// seem incorrect most of the time, so the controller almost always defaults to using the node condition based counts.
				// On occasion, the MCN counts cause a false positive in aformentioned check, resulting in invalid values for the MCP
				// statuses. Commenting out this block will force the controller to always use node condition based counts to feed the
				// MCP status.


				if cond.Status == metav1.ConditionUnknown {
					// This switch case will cause a node to be double counted, maybe use a hash for node count
					switch mcfgv1.StateProgress(cond.Type) {
					case mcfgv1.MachineConfigNodeUpdatePrepared:
						updatingMachines = append(updatedMachines, ourNode) //nolint:gocritic
					case mcfgv1.MachineConfigNodeUpdateExecuted:
						updatingMachines = append(updatingMachines, ourNode)
					case mcfgv1.MachineConfigNodeUpdatePostActionComplete:
						updatingMachines = append(updatingMachines, ourNode)
					case mcfgv1.MachineConfigNodeUpdateComplete:
						updatingMachines = append(updatingMachines, ourNode)
					case mcfgv1.MachineConfigNodeResumed:
						updatingMachines = append(updatedMachines, ourNode) //nolint:gocritic
						readyMachines = append(readyMachines, ourNode)
					// Note (ijanssen): `MachineConfigNodeUpdateCompatible` was removed with MCO-1543. This case will need to be replaced/removed when working on MCO-1228.
					case mcfgv1.MachineConfigNodeUpdateCompatible:
						updatingMachines = append(updatedMachines, ourNode) //nolint:gocritic
					case mcfgv1.MachineConfigNodeUpdateDrained:
						unavailableMachines = append(unavailableMachines, ourNode)
						updatingMachines = append(updatingMachines, ourNode)
					case mcfgv1.MachineConfigNodeUpdateCordoned:
						unavailableMachines = append(unavailableMachines, ourNode)
						updatingMachines = append(updatingMachines, ourNode)
					case mcfgv1.MachineConfigNodeUpdated:
						updatedMachines = append(updatedMachines, ourNode)
						readyMachines = append(readyMachines, ourNode)
					}
				}
			*/
		}
	}
	degradedMachineCount := int32(len(degradedMachines))
	updatedMachineCount := int32(len(updatedMachines))
	unavailableMachineCount := int32(len(unavailableMachines))
	updatingMachineCount := int32(len(updatingMachines))
	readyMachineCount := int32(len(readyMachines))

	// this is # 1 priority, get the upgrade states actually reporting
	if degradedMachineCount+readyMachineCount+unavailableMachineCount+updatingMachineCount != int32(len(nodes)) {

		updatedMachines = getUpdatedMachines(pool, nodes, mosc, mosb, isLayeredPool)
		updatedMachineCount = int32(len(updatedMachines))

		readyMachines = getReadyMachines(pool, nodes, mosc, mosb, isLayeredPool)
		readyMachineCount = int32(len(readyMachines))

		unavailableMachines = getUnavailableMachines(nodes, pool)
		unavailableMachineCount = int32(len(unavailableMachines))

		degradedMachines = getDegradedMachines(nodes)
		degradedMachineCount = int32(len(degradedMachines))
	}

	for _, n := range degradedMachines {
		reason, ok := n.Annotations[daemonconsts.MachineConfigDaemonReasonAnnotationKey]
		if ok && reason != "" {
			degradedReasons = append(degradedReasons, fmt.Sprintf("Node %s is reporting: %q", n.Name, reason))
		}
	}

	status := mcfgv1.MachineConfigPoolStatus{
		ObservedGeneration:      pool.Generation,
		MachineCount:            machineCount,
		UpdatedMachineCount:     updatedMachineCount,
		ReadyMachineCount:       readyMachineCount,
		UnavailableMachineCount: unavailableMachineCount,
		DegradedMachineCount:    degradedMachineCount,
		CertExpirys:             certExpirys,
	}

	// update synchronizer status for pinned image sets
	if pisIsEnabled {
		syncStatus := poolSynchronizer.GetStatus(mcfgv1.PinnedImageSets)
		status.PoolSynchronizersStatus = []mcfgv1.PoolSynchronizerStatus{
			{
				PoolSynchronizerType:    mcfgv1.PinnedImageSets,
				MachineCount:            syncStatus.MachineCount,
				UpdatedMachineCount:     syncStatus.UpdatedMachineCount,
				ReadyMachineCount:       int64(readyMachineCount),
				UnavailableMachineCount: int64(unavailableMachineCount),
				AvailableMachineCount:   int64(machineCount - unavailableMachineCount),
			},
		}
	}

	status.Configuration = pool.Status.Configuration

	conditions := pool.Status.Conditions
	status.Conditions = append(status.Conditions, conditions...)

	allUpdated := updatedMachineCount == machineCount &&
		readyMachineCount == machineCount &&
		unavailableMachineCount == 0

	if allUpdated {
		//TODO: update api to only have one condition regarding status of update.
		updatedMsg := fmt.Sprintf("All nodes are updated with %s", getPoolUpdateLine(pool, mosc, isLayeredPool))
		supdated := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolUpdated, corev1.ConditionTrue, "", updatedMsg)
		apihelpers.SetMachineConfigPoolCondition(&status, *supdated)

		supdating := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolUpdating, corev1.ConditionFalse, "", "")
		apihelpers.SetMachineConfigPoolCondition(&status, *supdating)
		if status.Configuration.Name != pool.Spec.Configuration.Name || !equality.Semantic.DeepEqual(status.Configuration.Source, pool.Spec.Configuration.Source) {
			klog.Infof("Pool %s: %s", pool.Name, updatedMsg)
			status.Configuration = pool.Spec.Configuration
		}
	} else {
		supdated := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolUpdated, corev1.ConditionFalse, "", "")
		apihelpers.SetMachineConfigPoolCondition(&status, *supdated)
		if pool.Spec.Paused {
			supdating := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolUpdating, corev1.ConditionFalse, "", fmt.Sprintf("Pool is paused; will not update to %s", getPoolUpdateLine(pool, mosc, isLayeredPool)))
			apihelpers.SetMachineConfigPoolCondition(&status, *supdating)
		} else if !pinnedImageSetsDegraded { // note that when the PinnedImageSet is degraded, the `Updating` status should not be updated
			supdating := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolUpdating, corev1.ConditionTrue, "", fmt.Sprintf("All nodes are updating to %s", getPoolUpdateLine(pool, mosc, isLayeredPool)))
			apihelpers.SetMachineConfigPoolCondition(&status, *supdating)
		}
	}

	if mosc != nil && !pool.Spec.Paused {
		// Check for non-build degradation before setting Updating status
		// Don't set Updating=True if pool is degraded due to render/node/other issues
		isNonBuildDegraded := apihelpers.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolRenderDegraded) ||
			apihelpers.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolNodeDegraded) ||
			apihelpers.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolPinnedImageSetsDegraded)

		switch {
		case !isNonBuildDegraded:
			switch {
			case mosb == nil:
				// MOSC exists but MOSB doesn't exist yet -> change MCP to update
				updating := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolUpdating, corev1.ConditionTrue, "", fmt.Sprintf("Pool is waiting for a new OS image build to start (mosc: %s)", mosc.Name))
				apihelpers.SetMachineConfigPoolCondition(&status, *updating)
			default:
				// Some cases we have an old MOSB object that still exists, we still update MCP
				mosbState := ctrlcommon.NewMachineOSBuildState(mosb)
				switch {
				case mosbState.IsBuilding() || mosbState.IsBuildPrepared():
					updating := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolUpdating, corev1.ConditionTrue, "", fmt.Sprintf("Pool is waiting for OS image build to complete (mosb: %s)", mosb.Name))
					apihelpers.SetMachineConfigPoolCondition(&status, *updating)
				case mosbState.IsBuildFailure():
					// Clear updating status when build fails
					updating := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolUpdating, corev1.ConditionFalse, "", fmt.Sprintf("Pool update stopped due to OS image build failure (mosb: %s)", mosb.Name))
					apihelpers.SetMachineConfigPoolCondition(&status, *updating)
				}
			}
		default:
			// Pool is degraded due to non-build issues, clear updating status
			updating := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolUpdating, corev1.ConditionFalse, "", "Pool update paused due to degraded condition")
			apihelpers.SetMachineConfigPoolCondition(&status, *updating)
		}
	}

	var nodeDegraded bool
	var nodeDegradedMessage string
	for _, m := range degradedMachines {
		klog.Infof("Degraded Machine: %v and Degraded Reason: %v", m.Name, m.Annotations[daemonconsts.MachineConfigDaemonReasonAnnotationKey])
	}
	if degradedMachineCount > 0 {
		nodeDegraded = true
		sdegraded := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolNodeDegraded, corev1.ConditionTrue, fmt.Sprintf("%d nodes are reporting degraded status on sync", len(degradedMachines)), strings.Join(degradedReasons, ", "))
		nodeDegradedMessage = sdegraded.Message
		apihelpers.SetMachineConfigPoolCondition(&status, *sdegraded)
		if pinnedImageSetsDegraded {
			sdegraded := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolPinnedImageSetsDegraded, corev1.ConditionTrue, "one or more pinned image set is reporting degraded", strings.Join(degradedReasons, ", "))
			apihelpers.SetMachineConfigPoolCondition(&status, *sdegraded)
		}
	} else {
		sdegraded := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolNodeDegraded, corev1.ConditionFalse, "", "")
		apihelpers.SetMachineConfigPoolCondition(&status, *sdegraded)
	}

	// here we now set the MCP Degraded field, the node_controller is the one making the call right now
	// but we might have a dedicated controller or control loop somewhere else that understands how to
	// set Degraded. For now, the node_controller understand NodeDegraded & RenderDegraded & BuildDegraded = Degraded.
	renderDegraded := apihelpers.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolRenderDegraded)
	buildDegraded := apihelpers.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolImageBuildDegraded)

	// Clear BuildDegraded condition in several scenarios to prevent stale degraded state:
	// 1. Active build (building or prepared) - new build started
	// 2. Successful build - build completed successfully
	// 3. MachineOSConfig exists but no MachineOSBuild - new or retry attempt pending
	// 4. No layered pool objects (cleanup scenario) - clear stale BuildDegraded
	switch {
	case mosb != nil:
		mosbState := ctrlcommon.NewMachineOSBuildState(mosb)
		switch {
		case mosbState.IsBuilding() || mosbState.IsBuildPrepared():
			// Active build detected - clear any previous BuildDegraded condition
			buildDegradedClear := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolImageBuildDegraded, corev1.ConditionFalse, string(mcfgv1.MachineConfigPoolBuilding), "New build started")
			apihelpers.SetMachineConfigPoolCondition(&status, *buildDegradedClear)
			// Update local variable for degraded calculation
			buildDegraded = false
		case mosbState.IsBuildSuccess():
			// Successful build detected - clear any previous BuildDegraded condition
			buildDegradedClear := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolImageBuildDegraded, corev1.ConditionFalse, string(mcfgv1.MachineConfigPoolBuildSuccess), "Build completed successfully")
			apihelpers.SetMachineConfigPoolCondition(&status, *buildDegradedClear)
			buildDegraded = false
		}
	case mosc != nil:
		// MachineOSConfig exists but no MachineOSBuild - this indicates a retry attempt
		// Clear any previous BuildDegraded condition to allow the retry
		if apihelpers.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolImageBuildDegraded) {
			buildDegradedClear := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolImageBuildDegraded, corev1.ConditionFalse, string(mcfgv1.MachineConfigPoolBuildPending), "MachineOSConfig updated/created, waiting for MachineOSBuild")
			apihelpers.SetMachineConfigPoolCondition(&status, *buildDegradedClear)
			buildDegraded = false
		}
	default:
		// No layered pool objects exist (both mosb and mosc are nil)
		// This means builds have been cleaned up - clear any stale BuildDegraded condition
		if apihelpers.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolImageBuildDegraded) {
			buildDegradedClear := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolImageBuildDegraded, corev1.ConditionFalse, "NoLayeredObjects", "No layered pool objects found, clearing stale build degraded condition")
			apihelpers.SetMachineConfigPoolCondition(&status, *buildDegradedClear)
			buildDegraded = false
		}
	}

	if nodeDegraded || renderDegraded || buildDegraded || pinnedImageSetsDegraded {
		sdegraded := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolDegraded, corev1.ConditionTrue, "", "")
		if nodeDegraded {
			sdegraded.Message = nodeDegradedMessage
		} else if buildDegraded {
			sdegraded.Message = "Custom OS image build failed"
		}
		apihelpers.SetMachineConfigPoolCondition(&status, *sdegraded)
	} else {
		sdegraded := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolDegraded, corev1.ConditionFalse, "", "")
		apihelpers.SetMachineConfigPoolCondition(&status, *sdegraded)
	}

	return status
}

func getPoolUpdateLine(pool *mcfgv1.MachineConfigPool, mosc *mcfgv1.MachineOSConfig, layered bool) string {
	targetConfig := pool.Spec.Configuration.Name
	mcLine := fmt.Sprintf("MachineConfig %s", targetConfig)

	if !layered {
		return mcLine
	}

	targetImage := mosc.Status.CurrentImagePullSpec
	if string(targetImage) == "" {
		return mcLine
	}

	return fmt.Sprintf("%s / Image %s", mcLine, targetImage)
}

// isNodeManaged checks whether the MCD has ever run on a node
func isNodeManaged(node *corev1.Node) bool {
	if helpers.IsWindows(node) {
		klog.V(4).Infof("Node %v is a windows node so won't be managed by MCO", node.Name)
		return false
	}
	if node.Annotations == nil {
		return false
	}
	cconfig, ok := node.Annotations[daemonconsts.CurrentMachineConfigAnnotationKey]
	if !ok || cconfig == "" {
		return false
	}
	return true
}

// getUpdatedMachines filters the provided nodes to return the nodes whose
// current config matches the desired config, which also matches the target config,
// and the "done" flag is set.
func getUpdatedMachines(pool *mcfgv1.MachineConfigPool, nodes []*corev1.Node, mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild, layered bool) []*corev1.Node {
	var updated []*corev1.Node
	for _, node := range nodes {
		lns := ctrlcommon.NewLayeredNodeState(node)
		if lns.IsDone(pool, layered, mosc, mosb) {
			updated = append(updated, node)
		}
	}
	return updated
}

// getReadyMachines filters the provided nodes to return the nodes
// that are updated and marked ready
func getReadyMachines(pool *mcfgv1.MachineConfigPool, nodes []*corev1.Node, mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild, layered bool) []*corev1.Node {
	updated := getUpdatedMachines(pool, nodes, mosc, mosb, layered)
	var ready []*corev1.Node
	for _, node := range updated {
		lns := ctrlcommon.NewLayeredNodeState(node)
		if lns.IsNodeReady() {
			ready = append(ready, node)
		}
	}
	return ready
}

// getUnavailableMachines returns the set of nodes which are
// either marked unscheduleable, or have a MCD actively working.
// If the MCD is actively working (or hasn't started) then the
// node *may* go unschedulable in the future, so we don't want to
// potentially start another node update exceeding our maxUnavailable.
// Somewhat the opposite of getReadyNodes().
func getUnavailableMachines(nodes []*corev1.Node, pool *mcfgv1.MachineConfigPool) []*corev1.Node {
	var unavail []*corev1.Node
	for _, node := range nodes {
		lns := ctrlcommon.NewLayeredNodeState(node)
		if lns.IsUnavailableForUpdate() {
			unavail = append(unavail, node)
			klog.V(4).Infof("getUnavailableMachines: Found unavailable node %s in pool %s", node.Name, pool.Name)
		}
	}
	klog.V(4).Infof("getUnavailableMachines: Found %d unavailable in pool %s", len(unavail), pool.Name)
	return unavail
}

func getDegradedMachines(nodes []*corev1.Node) []*corev1.Node {
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

		if dstate == daemonconsts.MachineConfigDaemonStateDegraded || dstate == daemonconsts.MachineConfigDaemonStateUnreconcilable {
			degraded = append(degraded, node)
		}
	}
	return degraded
}

func getNamesFromNodes(nodes []*corev1.Node) []string {
	if len(nodes) == 0 {
		return nil
	}

	names := []string{}
	for _, node := range nodes {
		names = append(names, node.Name)
	}

	return names
}

// newPoolSynchronizer creates a new pool synchronizer.
func newPoolSynchronizer(machineCount int32) *poolSynchronizer {
	return &poolSynchronizer{
		synchronizers: map[mcfgv1.PoolSynchronizerType]*mcfgv1.PoolSynchronizerStatus{
			mcfgv1.PinnedImageSets: {
				PoolSynchronizerType: mcfgv1.PinnedImageSets,
				MachineCount:         int64(machineCount),
			},
		},
	}
}

// poolSynchronizer is a helper struct to track the status of multiple synchronizers for a pool.
type poolSynchronizer struct {
	synchronizers map[mcfgv1.PoolSynchronizerType]*mcfgv1.PoolSynchronizerStatus
}

// SetUpdated updates the updated machine count for the given synchronizer type.
func (p *poolSynchronizer) SetUpdated(sType mcfgv1.PoolSynchronizerType) {
	status := p.synchronizers[sType]
	if status == nil {
		return
	}
	status.UpdatedMachineCount++
}

func (p *poolSynchronizer) GetStatus(sType mcfgv1.PoolSynchronizerType) *mcfgv1.PoolSynchronizerStatus {
	return p.synchronizers[sType]
}

// isPinnedImageSetNodeUpdated checks if the pinned image sets are updated for the node.
func isPinnedImageSetsUpdated(mcn *mcfgv1.MachineConfigNode) bool {
	updated := 0
	for _, set := range mcn.Status.PinnedImageSets {
		if set.DesiredGeneration > 0 && set.CurrentGeneration == set.DesiredGeneration {
			updated++
		}
	}
	return updated == len(mcn.Status.PinnedImageSets)
}

// isPinnedImageSetsInProgressForPool checks if the pinned image sets are in progress of reconciling for the pool.
func isPinnedImageSetsInProgressForPool(pool *mcfgv1.MachineConfigPool) bool {
	for _, status := range pool.Status.PoolSynchronizersStatus {
		if status.PoolSynchronizerType == mcfgv1.PinnedImageSets && status.UpdatedMachineCount != status.MachineCount {
			return true
		}
	}
	return false
}
