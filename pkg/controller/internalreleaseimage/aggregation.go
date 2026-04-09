package internalreleaseimage

import (
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"
)

// aggregateMCNIRIStatus aggregates the IRI status from all MachineConfigNodes
// and returns the cluster-wide status for each release bundle.
//
// This function implements the pattern used by the Node Controller for aggregating
// MCN conditions into MachineConfigPool status.
func (ctrl *Controller) aggregateMCNIRIStatus(iri *mcfgv1alpha1.InternalReleaseImage) ([]mcfgv1alpha1.InternalReleaseImageBundleStatus, error) {
	// Get all MachineConfigNodes
	mcns, err := ctrl.mcnLister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("failed to list MachineConfigNodes: %w", err)
	}

	// Filter to only master nodes (IRI only runs on control plane)
	masterMCNs := filterMasterMCNs(mcns)

	if len(masterMCNs) == 0 {
		klog.V(2).Info("No master MachineConfigNodes found, skipping IRI status aggregation")
		return nil, nil
	}

	klog.V(4).Infof("Aggregating IRI status from %d master nodes", len(masterMCNs))

	// Build aggregated status for each release in the IRI spec
	aggregatedReleases := []mcfgv1alpha1.InternalReleaseImageBundleStatus{}

	for _, specRelease := range iri.Spec.Releases {
		releaseName := specRelease.Name
		klog.V(4).Infof("Aggregating status for release %s", releaseName)

		// Find existing status for this release to preserve timestamps
		var existingStatus *mcfgv1alpha1.InternalReleaseImageBundleStatus
		for i := range iri.Status.Releases {
			if iri.Status.Releases[i].Name == releaseName {
				existingStatus = &iri.Status.Releases[i]
				break
			}
		}

		// Aggregate status for this release across all nodes
		aggregated := aggregateReleaseStatus(releaseName, masterMCNs, existingStatus)
		aggregatedReleases = append(aggregatedReleases, aggregated)
	}

	return aggregatedReleases, nil
}

// filterMasterMCNs returns only MachineConfigNodes that belong to the master pool.
// IRI registry only runs on control plane nodes.
func filterMasterMCNs(mcns []*mcfgv1.MachineConfigNode) []*mcfgv1.MachineConfigNode {
	var masterMCNs []*mcfgv1.MachineConfigNode
	for _, mcn := range mcns {
		// Check if MCN belongs to master pool
		if mcn.Spec.Pool.Name == "master" {
			masterMCNs = append(masterMCNs, mcn)
		}
	}
	return masterMCNs
}

// aggregateReleaseStatus aggregates the status of a single release across all nodes.
// existingStatus is used to preserve LastTransitionTime for unchanged conditions.
func aggregateReleaseStatus(releaseName string, mcns []*mcfgv1.MachineConfigNode, existingStatus *mcfgv1alpha1.InternalReleaseImageBundleStatus) mcfgv1alpha1.InternalReleaseImageBundleStatus {
	totalNodes := len(mcns)

	// Initialize counters for each condition type
	counters := map[mcfgv1alpha1.InternalReleaseImageConditionType]int{
		mcfgv1alpha1.InternalReleaseImageConditionTypeAvailable:   0,
		mcfgv1alpha1.InternalReleaseImageConditionTypeDegraded:    0,
		mcfgv1alpha1.InternalReleaseImageConditionTypeInstalling:  0,
		mcfgv1alpha1.InternalReleaseImageConditionTypeRemoving:    0,
		mcfgv1alpha1.InternalReleaseImageConditionTypeMounted:     0,
	}

	// Track degraded nodes for detailed messages
	var degradedNodes []string
	var installingNodes []string
	var removingNodes []string

	// Track the image pullspec (should be same across all nodes)
	var imagePullspec string

	// Iterate through all MCNs and count condition states
	for _, mcn := range mcns {
		// Find this release in the MCN status
		releaseStatus := findReleaseInMCN(mcn, releaseName)
		if releaseStatus == nil {
			// Node doesn't have status for this release yet
			klog.V(4).Infof("Node %s has no status for release %s", mcn.Name, releaseName)
			continue
		}

		// Store the image pullspec (first non-empty one we find)
		if imagePullspec == "" && releaseStatus.Image != "" {
			imagePullspec = releaseStatus.Image
		}

		// Count condition states
		for _, cond := range releaseStatus.Conditions {
			condType := mcfgv1alpha1.InternalReleaseImageConditionType(cond.Type)

			if cond.Status == metav1.ConditionTrue {
				counters[condType]++

				// Track which nodes are in non-Available states
				switch condType {
				case mcfgv1alpha1.InternalReleaseImageConditionTypeDegraded:
					degradedNodes = append(degradedNodes, mcn.Name)
				case mcfgv1alpha1.InternalReleaseImageConditionTypeInstalling:
					installingNodes = append(installingNodes, mcn.Name)
				case mcfgv1alpha1.InternalReleaseImageConditionTypeRemoving:
					removingNodes = append(removingNodes, mcn.Name)
				}
			}
		}
	}

	// Get existing conditions to preserve timestamps
	var existingConditions []metav1.Condition
	if existingStatus != nil {
		existingConditions = existingStatus.Conditions
	}

	// Build the aggregated conditions based on counters
	conditions := buildAggregatedConditions(releaseName, totalNodes, counters, degradedNodes, installingNodes, removingNodes, existingConditions)

	return mcfgv1alpha1.InternalReleaseImageBundleStatus{
		Name:       releaseName,
		Image:      imagePullspec,
		Conditions: conditions,
	}
}

// findReleaseInMCN finds a specific release in the MCN's IRI status.
func findReleaseInMCN(mcn *mcfgv1.MachineConfigNode, releaseName string) *mcfgv1.MachineConfigNodeStatusInternalReleaseImageRef {
	for _, release := range mcn.Status.InternalReleaseImage.Releases {
		if release.Name == releaseName {
			return &release
		}
	}
	return nil
}

// buildAggregatedConditions creates the condition list for the aggregated release status.
//
// Aggregation rules:
// - Available: True if ALL nodes report Available=True
// - Degraded: True if ANY node reports Degraded=True
// - Installing: True if ANY node reports Installing=True
// - Removing: True if ANY node reports Removing=True
// - Mounted: True if ANY node reports Mounted=True
func buildAggregatedConditions(
	releaseName string,
	totalNodes int,
	counters map[mcfgv1alpha1.InternalReleaseImageConditionType]int,
	degradedNodes, installingNodes, removingNodes []string,
	existingConditions []metav1.Condition,
) []metav1.Condition {

	now := metav1.Now()
	conditions := []metav1.Condition{}

	// Helper to preserve LastTransitionTime if condition content hasn't changed
	preserveTimestamp := func(newCond *metav1.Condition) {
		for _, existing := range existingConditions {
			if existing.Type == newCond.Type &&
				existing.Status == newCond.Status &&
				existing.Reason == newCond.Reason &&
				existing.Message == newCond.Message {
				// Content unchanged, preserve timestamp
				newCond.LastTransitionTime = existing.LastTransitionTime
				return
			}
		}
		// Content changed or new condition, use current time
		newCond.LastTransitionTime = now
	}

	// Available Condition
	availableCount := counters[mcfgv1alpha1.InternalReleaseImageConditionTypeAvailable]
	availableCondition := metav1.Condition{
		Type: string(mcfgv1alpha1.InternalReleaseImageConditionTypeAvailable),
	}

	if availableCount == totalNodes && totalNodes > 0 {
		availableCondition.Status = metav1.ConditionTrue
		availableCondition.Reason = "AllNodesAvailable"
		availableCondition.Message = fmt.Sprintf("Release %s is available on all %d nodes", releaseName, totalNodes)
	} else {
		availableCondition.Status = metav1.ConditionFalse
		availableCondition.Reason = "NotAllNodesAvailable"
		availableCondition.Message = fmt.Sprintf("Release %s is available on %d/%d nodes", releaseName, availableCount, totalNodes)
	}
	preserveTimestamp(&availableCondition)
	conditions = append(conditions, availableCondition)

	// Degraded Condition
	degradedCount := counters[mcfgv1alpha1.InternalReleaseImageConditionTypeDegraded]
	degradedCondition := metav1.Condition{
		Type: string(mcfgv1alpha1.InternalReleaseImageConditionTypeDegraded),
	}

	if degradedCount > 0 {
		degradedCondition.Status = metav1.ConditionTrue
		degradedCondition.Reason = "NodesReportDegraded"
		degradedCondition.Message = fmt.Sprintf("Release %s is degraded on %d/%d nodes: %v",
			releaseName, degradedCount, totalNodes, degradedNodes)
	} else {
		degradedCondition.Status = metav1.ConditionFalse
		degradedCondition.Reason = "NoNodesDegraded"
		degradedCondition.Message = fmt.Sprintf("Release %s is not degraded on any nodes", releaseName)
	}
	preserveTimestamp(&degradedCondition)
	conditions = append(conditions, degradedCondition)

	// Installing Condition
	installingCount := counters[mcfgv1alpha1.InternalReleaseImageConditionTypeInstalling]
	if installingCount > 0 {
		installingCondition := metav1.Condition{
			Type:    string(mcfgv1alpha1.InternalReleaseImageConditionTypeInstalling),
			Status:  metav1.ConditionTrue,
			Reason:  "InstallationInProgress",
			Message: fmt.Sprintf("Release %s is installing on %d/%d nodes: %v",
				releaseName, installingCount, totalNodes, installingNodes),
		}
		preserveTimestamp(&installingCondition)
		conditions = append(conditions, installingCondition)
	}

	// Removing Condition
	removingCount := counters[mcfgv1alpha1.InternalReleaseImageConditionTypeRemoving]
	if removingCount > 0 {
		removingCondition := metav1.Condition{
			Type:    string(mcfgv1alpha1.InternalReleaseImageConditionTypeRemoving),
			Status:  metav1.ConditionTrue,
			Reason:  "RemovalInProgress",
			Message: fmt.Sprintf("Release %s is being removed from %d/%d nodes: %v",
				releaseName, removingCount, totalNodes, removingNodes),
		}
		preserveTimestamp(&removingCondition)
		conditions = append(conditions, removingCondition)
	}

	// Mounted Condition
	mountedCount := counters[mcfgv1alpha1.InternalReleaseImageConditionTypeMounted]
	if mountedCount > 0 {
		mountedCondition := metav1.Condition{
			Type:    string(mcfgv1alpha1.InternalReleaseImageConditionTypeMounted),
			Status:  metav1.ConditionTrue,
			Reason:  "ISODetected",
			Message: fmt.Sprintf("Release %s ISO is mounted on %d/%d nodes",
				releaseName, mountedCount, totalNodes),
		}
		preserveTimestamp(&mountedCondition)
		conditions = append(conditions, mountedCondition)
	}

	return conditions
}
