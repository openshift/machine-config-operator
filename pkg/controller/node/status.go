package node

import (
	"context"
	"fmt"
	"strings"

	"github.com/openshift/machine-config-operator/pkg/osimagestream"
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
	for _, node := range nodes {
		ms, err := ctrl.client.MachineconfigurationV1().MachineConfigNodes().Get(context.TODO(), node.Name, metav1.GetOptions{})
		if err != nil {
			klog.Errorf("Could not find our MachineConfigNode for node. %s: %v", node.Name, err)
			continue
		}
		machineConfigStates = append(machineConfigStates, ms)
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

// `calculateStatus` calculates the MachineConfigPoolStatus object for the desired MCP
//
//nolint:gocyclo,gosec
func (ctrl *Controller) calculateStatus(mcns []*mcfgv1.MachineConfigNode, cconfig *mcfgv1.ControllerConfig, pool *mcfgv1.MachineConfigPool, nodes []*corev1.Node, mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild) mcfgv1.MachineConfigPoolStatus {
	// Get the `CertExpiry` details for the MCP status
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

	// Get total machine count and initialize pool synchronizer for MCP
	totalMachineCount := int32(len(nodes))
	poolSynchronizer := newPoolSynchronizer(totalMachineCount)

	// Determine if pool is layered and get enabled feature gates
	isLayeredPool := ctrl.isLayeredPool(mosc, mosb)
	imageModeReportingIsEnabled := ctrl.fgHandler.Enabled(features.FeatureGateImageModeStatusReporting)

	// Update the number of degraded and updated machines from conditions in the MCNs for the nodes
	// in the associated MCP.
	var degradedMachines, updatedMachines []*corev1.Node
	degradedReasons := []string{}
	pinnedImageSetsDegraded := false
	for _, mcn := range mcns {
		// If the MCN Conditions list is empty, the MCN is not ready and cannot be used to determine the MCP status
		if len(mcn.Status.Conditions) == 0 {
			klog.Infof("MCN %s is not ready yet; no conditions exist.", mcn.Name)
			break
		}

		// Get the node associated with the MCN
		var ourNode *corev1.Node
		for _, node := range nodes {
			if mcn.Name == node.Name {
				ourNode = node
				break
			}
		}
		if ourNode == nil {
			klog.Errorf("Could not find specified node for MCN %s", mcn.Name)
			break
		}

		// Update the PIS reference in the PoolSynchronizer object
		if isPinnedImageSetsUpdated(mcn) {
			poolSynchronizer.SetUpdated(mcfgv1.PinnedImageSets)
		}

		// Loop through the MCN conditions to determine if the associated node is updating, updated, or degraded
		for _, cond := range mcn.Status.Conditions {
			// Handle the case when the node is degraded
			if mcfgv1.StateProgress(cond.Type) == mcfgv1.MachineConfigNodeNodeDegraded && cond.Status == metav1.ConditionTrue {
				degradedMachines = append(degradedMachines, ourNode)
				// populate the degradedReasons from the MachineConfigNodeNodeDegraded condition
				degradedReasons = append(degradedReasons, fmt.Sprintf("Node %s is reporting: %q", ourNode.Name, cond.Message))
				// break since the node cannot be considered updated in addition to degraded
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

			// If the ImageModeStatusReporting feature gate is enabled, the updated machine count
			// in the MCP status should be populated from MCN conditions
			if imageModeReportingIsEnabled {
				// A node is considered "updated" when the following are true:
				// 	- The desired and current config versions (for non-image layering updates) and
				// 	  the desired and current images (for image layering updates) are equal, which
				// 	  is only met in the MCN when the `Updated` status is `True`.
				// 	- The MCN's desired config version matches the MCP's desired config version and
				// 	  the config version in the MOSB (for image layering updates) and the MCN's
				// 	  desired image matches the image in the MOSC . Note that this check is
				// 	  required to ensure no regressions occur in migrating to the MCN driven MCP
				// 	  updates, since "Updated" in the MCN does not flip from "True" until a node
				// 	  starts updating.
				if mcfgv1.StateProgress(cond.Type) == mcfgv1.MachineConfigNodeUpdated && cond.Status == metav1.ConditionTrue && ctrlcommon.IsMachineUpdatedMCN(mcn, pool, mosc, mosb, isLayeredPool) {
					updatedMachines = append(updatedMachines, ourNode)
					// break since the node cannot be considered degraded in addition to updated
					break
				}
			}
		}
	}
	// Get number of degraded and updated machines as determined by the MCN conditions
	degradedMachineCount := int32(len(degradedMachines))
	updatedMachineCount := int32(len(updatedMachines))

	// Get the number of ready and unavailable machines from the node and MCP properties.
	machinesByState := ctrlcommon.GetMachinesByState(pool, nodes, mosc, mosb, isLayeredPool)
	readyMachineCount := int32(len(machinesByState.Ready))
	unavailableMachineCount := int32(len(machinesByState.Unavailable))

	// When the ImageModeStatusReporting feature gate is not enabled, get the number of updated and
	// degraded machines from the node and MCP properties.
	if !imageModeReportingIsEnabled {
		updatedMachineCount = int32(len(machinesByState.Updated))
		degradedMachineCount = int32(len(machinesByState.Degraded))
	}

	// Create degrade message by aggregating degraded reasons from all degraded machines
	for _, n := range degradedMachines {
		reason, ok := n.Annotations[daemonconsts.MachineConfigDaemonReasonAnnotationKey]
		if ok && reason != "" {
			degradedReasons = append(degradedReasons, fmt.Sprintf("Node %s is reporting: %q", n.Name, reason))
		}
	}

	// Update MCP status with machine counts
	status := mcfgv1.MachineConfigPoolStatus{
		ObservedGeneration:      pool.Generation,
		MachineCount:            totalMachineCount,
		UpdatedMachineCount:     updatedMachineCount,
		ReadyMachineCount:       readyMachineCount,
		UnavailableMachineCount: unavailableMachineCount,
		DegradedMachineCount:    degradedMachineCount,
		CertExpirys:             certExpirys,
	}

	// Update synchronizer status for pinned image sets
	syncStatus := poolSynchronizer.GetStatus(mcfgv1.PinnedImageSets)
	status.PoolSynchronizersStatus = []mcfgv1.PoolSynchronizerStatus{
		{
			PoolSynchronizerType:    mcfgv1.PinnedImageSets,
			MachineCount:            syncStatus.MachineCount,
			UpdatedMachineCount:     syncStatus.UpdatedMachineCount,
			ReadyMachineCount:       int64(readyMachineCount),
			UnavailableMachineCount: int64(unavailableMachineCount),
			AvailableMachineCount:   int64(totalMachineCount - unavailableMachineCount),
		},
	}

	// Update MCP status configuation & conditions
	status.Configuration = pool.Status.Configuration
	conditions := pool.Status.Conditions
	status.Conditions = append(status.Conditions, conditions...)

	// Determine if all machines are updated and
	// 	- If all machines are updated, set "Updated" condition to true and "Updating" condition to false
	// 	- If all machines not updated, set "Updated" condition to false and "Updating" condition to false
	// 	  if the pool is paused or true if the pool is not paused and the PIS is not degraded
	allUpdated := updatedMachineCount == totalMachineCount &&
		readyMachineCount == totalMachineCount &&
		unavailableMachineCount == 0 &&
		!isLayeredPoolBuilding(isLayeredPool, mosc, mosb)
	if allUpdated {
		// When the pool is fully updated & the `OSStreams` FeatureGate is enabled, set the
		// `OSImageStream` reference in the MCP status to be consistent to what is defined in the
		// MCP spec
		if osimagestream.IsFeatureEnabled(ctrl.fgHandler) {
			status.OSImageStream = pool.Spec.OSImageStream
		}

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

	// If the MachineOSConfig is not nil, image mode is enabled and the MCP status should be handled
	// according to its and the MachineOSBuild's statuses
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
				// MOSC exists but MOSB doesn't exist yet -> change MCP to updating
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

	// Update degrade condition
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

// getUnavailableMachines returns the set of nodes which are
// either marked unscheduleable, or have a MCD actively working.
// If the MCD is actively working (or hasn't started) then the
// node *may* go unschedulable in the future, so we don't want to
// potentially start another node update exceeding our maxUnavailable.
// Somewhat the opposite of getReadyNodes().
func getUnavailableMachines(nodes []*corev1.Node) []*corev1.Node {
	var unavail []*corev1.Node
	for _, node := range nodes {
		lns := ctrlcommon.NewLayeredNodeState(node)
		if lns.IsUnavailableForUpdate() {
			unavail = append(unavail, node)
		}
	}
	return unavail
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

// isLayeredPoolBuilding checks if a layered pool has an active build or failed build that would
// make nodes not truly "updated" even if they have the current machine config
func isLayeredPoolBuilding(isLayeredPool bool, mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild) bool {
	if !isLayeredPool || mosc == nil || mosb == nil {
		return false
	}

	mosbState := ctrlcommon.NewMachineOSBuildState(mosb)

	// Check if there's an active build (building or prepared)
	if mosbState.IsBuilding() || mosbState.IsBuildPrepared() {
		return true
	}

	// Check if there's a failed build - this means the update attempt failed
	// so nodes should not be considered "updated"
	if mosbState.IsBuildFailure() {
		return true
	}

	// Check if there's a successful build that nodes haven't applied yet
	// This happens when a build completes but the MOSC status hasn't been updated
	// or nodes haven't picked up the new image yet
	if mosbState.IsBuildSuccess() && mosb.Status.DigestedImagePushSpec != "" {
		// If the successful build's image differs from what MOSC thinks is current,
		// then nodes are not truly updated to the latest successful build
		return string(mosb.Status.DigestedImagePushSpec) != string(mosc.Status.CurrentImagePullSpec)
	}

	return false
}
