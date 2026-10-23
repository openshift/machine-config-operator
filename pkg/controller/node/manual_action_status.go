package node

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/upgrademonitor"
)

const manualActionStatusPollInterval = time.Minute

// enqueueManualAction schedules a per-node reconciliation of MCN conditions
// that describe node actions observed outside an MCO-managed update.
func (ctrl *Controller) enqueueManualAction(nodeName string) {
	ctrl.enqueueManualActionAfter(nodeName, ctrl.updateDelay)
}

func (ctrl *Controller) enqueueManualActionAfter(nodeName string, delay time.Duration) {
	if nodeName == "" || ctrl.manualActionQueue == nil {
		return
	}
	ctrl.manualActionQueue.AddAfter(nodeName, delay)
}

func manualActionNodeChanged(oldNode, curNode *corev1.Node) bool {
	if oldNode.Spec.Unschedulable != curNode.Spec.Unschedulable || oldNode.Status.NodeInfo.BootID != curNode.Status.NodeInfo.BootID {
		return true
	}

	for _, key := range []string{
		constants.CurrentMachineConfigAnnotationKey,
		constants.DesiredMachineConfigAnnotationKey,
		constants.CurrentImageAnnotationKey,
		constants.DesiredImageAnnotationKey,
		constants.MachineConfigDaemonStateAnnotationKey,
		constants.DesiredDrainerAnnotationKey,
		constants.LastAppliedDrainerAnnotationKey,
		constants.MachineConfigDaemonPostConfigAction,
	} {
		oldValue, oldOK := oldNode.Annotations[key]
		newValue, newOK := curNode.Annotations[key]
		if oldOK != newOK || oldValue != newValue {
			return true
		}
	}

	return false
}

func (ctrl *Controller) manualActionWorker() {
	for ctrl.processNextManualAction() {
	}
}

func (ctrl *Controller) processNextManualAction() bool {
	nodeName, quit := ctrl.manualActionQueue.Get()
	if quit {
		return false
	}
	defer ctrl.manualActionQueue.Done(nodeName)

	if err := ctrl.syncManualActionStatus(nodeName); err != nil {
		if ctrl.manualActionQueue.NumRequeues(nodeName) < maxRetries {
			klog.V(2).Infof("Error reconciling manual MCN action status for node %q: %v", nodeName, err)
			ctrl.manualActionQueue.AddRateLimited(nodeName)
			return true
		}

		klog.Errorf("Dropping manual MCN action status for node %q after retries: %v", nodeName, err)
		ctrl.manualActionQueue.Forget(nodeName)
		ctrl.manualActionQueue.AddAfter(nodeName, time.Minute)
		return true
	}

	ctrl.manualActionQueue.Forget(nodeName)
	return true
}

func (ctrl *Controller) syncManualActionStatus(nodeName string) error {
	node, err := ctrl.nodeLister.Get(nodeName)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !isNodeManaged(node) {
		return nil
	}

	mcn, err := ctrl.client.MachineconfigurationV1().MachineConfigNodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// The MCN add event will enqueue this node again once the resource exists.
		return nil
	}
	if err != nil {
		return err
	}

	bootID := node.Status.NodeInfo.BootID
	lastBootID := node.Annotations[constants.MachineConfigDaemonLastObservedBootIDAnnotationKey]
	bootIDChanged := bootID != "" && lastBootID != "" && bootID != lastBootID
	manualActionAllowed := !isMCOUpdateInProgress(node, mcn)

	if manualActionAllowed {
		drainablePodCount := 0
		if node.Spec.Unschedulable {
			pods, err := ctrl.kubeClient.CoreV1().Pods(metav1.NamespaceAll).List(context.TODO(), metav1.ListOptions{
				FieldSelector: fields.OneTermEqualSelector("spec.nodeName", nodeName).String(),
			})
			if err != nil {
				return fmt.Errorf("listing pods while reconciling manual actions for node %q: %w", nodeName, err)
			}
			drainablePods := make([]*corev1.Pod, 0, len(pods.Items))
			for i := range pods.Items {
				drainablePods = append(drainablePods, &pods.Items[i])
			}
			drainablePodCount = countDrainBlockingPods(nodeName, drainablePods)
		}
		conditions := manualActionConditions(node, mcn, drainablePodCount, bootIDChanged, bootID)
		rebootedConditionPersisted, err := upgrademonitor.UpdateMachineConfigNodeActionConditions(ctrl.client, nodeName, conditions...)
		if err != nil {
			return err
		}
		if bootIDChanged && rebootedConditionPersisted {
			// Re-read both objects immediately before consuming the boot change.
			// Updated=True alone is insufficient: the desired config or image may
			// have changed while the action condition was being written. The action
			// update must also confirm Rebooted=True was persisted before advancing.
			shouldAdvance, err := ctrl.shouldAdvanceBootIDMarker(nodeName, bootID, lastBootID)
			if err != nil {
				return err
			}
			if shouldAdvance {
				if err := ctrl.recordLastObservedBootID(nodeName, bootID, lastBootID); err != nil {
					return err
				}
				lastBootID = bootID
			}
		}
		if node.Spec.Unschedulable {
			// Pod changes do not generate node events. Poll only cordoned,
			// converged nodes so the Drained condition follows pod eviction.
			ctrl.enqueueManualActionAfter(nodeName, manualActionStatusPollInterval)
		}
	}

	// Seed the current boot ID on first observation. For a node mid-update, do
	// not consume a changed boot ID here; the daemon records its own update
	// reboot, or the post-update reconciliation will report an otherwise missed
	// reboot before advancing this marker.
	if bootID != "" && lastBootID == "" {
		if err := ctrl.recordLastObservedBootID(nodeName, bootID, lastBootID); err != nil {
			return err
		}
	}

	return nil
}

func (ctrl *Controller) shouldAdvanceBootIDMarker(nodeName, observedBootID, expectedLastBootID string) (bool, error) {
	node, err := ctrl.kubeClient.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if node.Status.NodeInfo.BootID != observedBootID ||
		node.Annotations[constants.MachineConfigDaemonLastObservedBootIDAnnotationKey] != expectedLastBootID {
		return false, nil
	}

	mcn, err := ctrl.client.MachineconfigurationV1().MachineConfigNodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	return conditionStatus(mcn.Status.Conditions, string(mcfgv1.MachineConfigNodeUpdateRebooted)) == metav1.ConditionTrue &&
		!isMCOUpdateInProgress(node, mcn), nil
}

func isMCOUpdateInProgress(node *corev1.Node, mcn *mcfgv1.MachineConfigNode) bool {
	if !machineConfigNodeUpdated(mcn) {
		return true
	}
	if mcn.Status.ObservedGeneration < mcn.Generation {
		return true
	}
	if mcn.Status.ConfigVersion == nil {
		return true
	}

	desiredConfig := node.Annotations[constants.DesiredMachineConfigAnnotationKey]
	if desiredConfig == "" || node.Annotations[constants.CurrentMachineConfigAnnotationKey] != desiredConfig ||
		mcn.Spec.ConfigVersion.Desired != desiredConfig || mcn.Status.ConfigVersion.Desired != desiredConfig {
		return true
	}
	desiredImage := node.Annotations[constants.DesiredImageAnnotationKey]
	if node.Annotations[constants.CurrentImageAnnotationKey] != desiredImage {
		return true
	}
	specDesiredImage := string(mcn.Spec.ConfigImage.DesiredImage)
	statusDesiredImage := string(mcn.Status.ConfigImage.DesiredImage)
	if specDesiredImage != "" && (specDesiredImage != desiredImage || statusDesiredImage != specDesiredImage) {
		return true
	}
	if statusDesiredImage != "" && statusDesiredImage != desiredImage {
		return true
	}
	if node.Annotations[constants.MachineConfigDaemonStateAnnotationKey] == constants.MachineConfigDaemonStateWorking {
		return true
	}
	if node.Annotations[constants.DesiredDrainerAnnotationKey] != node.Annotations[constants.LastAppliedDrainerAnnotationKey] {
		return true
	}
	return node.Annotations[constants.MachineConfigDaemonPostConfigAction] == constants.MachineConfigDaemonStateRebooting
}

func machineConfigNodeUpdated(mcn *mcfgv1.MachineConfigNode) bool {
	for _, condition := range mcn.Status.Conditions {
		if condition.Type == string(mcfgv1.MachineConfigNodeUpdated) {
			return condition.Status == metav1.ConditionTrue
		}
	}
	return false
}

func manualActionConditions(node *corev1.Node, mcn *mcfgv1.MachineConfigNode, drainablePodCount int, bootIDChanged bool, bootID string) []metav1.Condition {
	conditions := []metav1.Condition{}

	cordonedStatus := metav1.ConditionFalse
	cordonedReason := "NodeSchedulable"
	cordonedMessage := "Node is currently schedulable."
	if node.Spec.Unschedulable {
		cordonedStatus = metav1.ConditionTrue
		cordonedReason = "NodeUnschedulable"
		cordonedMessage = "Node is currently unschedulable."
	}
	conditions = append(conditions, actionCondition(mcfgv1.MachineConfigNodeUpdateCordoned, cordonedStatus, cordonedReason, cordonedMessage))

	drainedStatus := metav1.ConditionFalse
	drainedReason := "NodeNotCordoned"
	drainedMessage := "Node is not cordoned."
	if node.Spec.Unschedulable {
		if drainablePodCount == 0 {
			drainedStatus = metav1.ConditionTrue
			drainedReason = "NoDrainablePods"
			drainedMessage = "No drainable workload pods remain on the cordoned node."
		} else {
			drainedReason = "DrainablePodsRemain"
			drainedMessage = fmt.Sprintf("%d drainable workload pods remain on the cordoned node.", drainablePodCount)
		}
	}
	conditions = append(conditions, actionCondition(mcfgv1.MachineConfigNodeUpdateDrained, drainedStatus, drainedReason, drainedMessage))

	uncordonedCondition := conditionForType(mcn.Status.Conditions, string(mcfgv1.MachineConfigNodeUpdateUncordoned))
	switch {
	case node.Spec.Unschedulable:
		conditions = append(conditions, actionCondition(mcfgv1.MachineConfigNodeUpdateUncordoned, metav1.ConditionFalse, "NodeCordoned", "Node is currently cordoned."))
	case conditionStatus(mcn.Status.Conditions, string(mcfgv1.MachineConfigNodeUpdateCordoned)) == metav1.ConditionTrue:
		conditions = append(conditions, actionCondition(mcfgv1.MachineConfigNodeUpdateUncordoned, metav1.ConditionTrue, "NodeUncordoned", "Node became schedulable after previously being cordoned."))
	case uncordonedCondition == nil:
		conditions = append(conditions, actionCondition(mcfgv1.MachineConfigNodeUpdateUncordoned, metav1.ConditionFalse, "NotYetOccurred", "No uncordon transition has been observed."))
	}

	rebootedCondition := conditionForType(mcn.Status.Conditions, string(mcfgv1.MachineConfigNodeUpdateRebooted))
	if bootIDChanged {
		conditions = append(conditions, actionCondition(mcfgv1.MachineConfigNodeUpdateRebooted, metav1.ConditionTrue, "BootIDChanged", fmt.Sprintf("Node reboot observed from a changed boot ID (%s).", bootID)))
	} else if rebootedCondition == nil {
		conditions = append(conditions, actionCondition(mcfgv1.MachineConfigNodeUpdateRebooted, metav1.ConditionFalse, "NotYetOccurred", "No reboot has been observed by a boot ID change."))
	}

	return conditions
}

func actionCondition(conditionType mcfgv1.StateProgress, status metav1.ConditionStatus, reason, message string) metav1.Condition {
	return metav1.Condition{
		Type:               string(conditionType),
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}
}

func conditionForType(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

func conditionStatus(conditions []metav1.Condition, conditionType string) metav1.ConditionStatus {
	if condition := conditionForType(conditions, conditionType); condition != nil {
		return condition.Status
	}
	return ""
}

func countDrainBlockingPods(nodeName string, pods []*corev1.Pod) int {
	count := 0
	for _, pod := range pods {
		if pod.Spec.NodeName == nodeName && isDrainBlockingPod(pod) {
			count++
		}
	}
	return count
}

func isDrainBlockingPod(pod *corev1.Pod) bool {
	if pod == nil || pod.Spec.NodeName == "" || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return false
	}
	if _, isMirrorPod := pod.Annotations[corev1.MirrorPodAnnotationKey]; isMirrorPod {
		return false
	}
	for _, owner := range pod.OwnerReferences {
		if owner.Kind == "DaemonSet" {
			return false
		}
	}
	return true
}

func (ctrl *Controller) recordLastObservedBootID(nodeName, bootID, expectedLastBootID string) error {
	node, err := ctrl.kubeClient.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting node %q before recording its last observed boot ID: %w", nodeName, err)
	}

	markerKey := constants.MachineConfigDaemonLastObservedBootIDAnnotationKey
	lastBootID, hasLastBootID := node.Annotations[markerKey]
	if node.Status.NodeInfo.BootID != bootID || lastBootID == bootID {
		// A newer Node boot will enqueue another reconciliation, or another
		// worker has already recorded this boot ID.
		return nil
	}
	if lastBootID != expectedLastBootID {
		return fmt.Errorf("last observed boot ID for node %q changed from expected %q to %q before recording %q", nodeName, expectedLastBootID, lastBootID, bootID)
	}

	type jsonPatchOperation struct {
		Op    string      `json:"op"`
		Path  string      `json:"path"`
		Value interface{} `json:"value"`
	}
	operations := []jsonPatchOperation{}
	if node.ResourceVersion != "" {
		operations = append(operations, jsonPatchOperation{
			Op: "test", Path: "/metadata/resourceVersion", Value: node.ResourceVersion,
		})
	}
	operations = append(operations, jsonPatchOperation{
		Op: "test", Path: "/status/nodeInfo/bootID", Value: bootID,
	})
	if hasLastBootID {
		operations = append(operations, jsonPatchOperation{
			Op: "test", Path: "/metadata/annotations/" + escapeJSONPointerToken(markerKey), Value: expectedLastBootID,
		})
	}
	if len(node.Annotations) == 0 {
		operations = append(operations, jsonPatchOperation{
			Op:    "add",
			Path:  "/metadata/annotations",
			Value: map[string]string{markerKey: bootID},
		})
	} else {
		operations = append(operations, jsonPatchOperation{
			Op:    "add",
			Path:  "/metadata/annotations/" + escapeJSONPointerToken(markerKey),
			Value: bootID,
		})
	}

	patch, err := json.Marshal(operations)
	if err != nil {
		return fmt.Errorf("building last observed boot ID patch for node %q: %w", nodeName, err)
	}
	_, err = ctrl.kubeClient.CoreV1().Nodes().Patch(context.TODO(), nodeName, types.JSONPatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("recording last observed boot ID for node %q: %w", nodeName, err)
	}
	return nil
}

func escapeJSONPointerToken(token string) string {
	token = strings.ReplaceAll(token, "~", "~0")
	return strings.ReplaceAll(token, "/", "~1")
}
