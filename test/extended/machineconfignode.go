// TODO (MCO-1960): Deduplicate these functions with the helpers defined in /extended-priv/machineconfignode.go.
package extended

import (
	"context"
	"fmt"
	"time"

	o "github.com/onsi/gomega"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	machineconfigclient "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

// `ValidateMCNForNode` validates the MCN of a provided node by checking the following:
//   - Check that `mcn.Spec.Pool.Name` matches provided `poolName`
//   - Check that `mcn.Name` matches the node name
//   - Check that `mcn.Spec.ConfigVersion.Desired` matches the node desired config version
//   - Check that `nmcn.Status.ConfigVersion.Current` matches the node current config version
//   - Check that `mcn.Status.ConfigVersion.Desired` matches the node desired config version
//   - Check that `mcn.Spec.ConfigImage.DesiredImage` matches the node desired image
//   - Check that `nmcn.Status.ConfigImage.CurrentImage` matches the node current image
//   - Check that `mcn.Status.ConfigImage.DesiredImage` matches the node desired image
func ValidateMCNForNode(oc *exutil.CLI, machineConfigClient *machineconfigclient.Clientset, nodeName, poolName string) error {
	// Get updated node
	node, nodeErr := oc.AsAdmin().KubeClient().CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
	if nodeErr != nil {
		logger.Errorf("Could not get node `%v`", nodeName)
		return nodeErr
	}

	// Get node's desired and current config versions and images
	nodeCurrentConfig := node.Annotations[constants.CurrentMachineConfigAnnotationKey]
	nodeDesiredConfig := node.Annotations[constants.DesiredMachineConfigAnnotationKey]
	nodeCurrentImage := mcfgv1.ImageDigestFormat(node.Annotations[constants.CurrentImageAnnotationKey])
	nodeDesiredImage := mcfgv1.ImageDigestFormat(node.Annotations[constants.DesiredImageAnnotationKey])

	// Get node's MCN
	logger.Infof("Getting MCN for node `%v`.", nodeName)
	mcn, mcnErr := machineConfigClient.MachineconfigurationV1().MachineConfigNodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
	if mcnErr != nil {
		logger.Errorf("Could not get MCN for node `%v`", nodeName)
		return mcnErr
	}

	// Check MCN pool name value for default MCPs
	logger.Infof("Checking MCN pool name for node `%v` matches pool association `%v`.", nodeName, poolName)
	if mcn.Spec.Pool.Name != poolName {
		logger.Errorf("MCN pool name `%v` does not match node MCP association `%v`.", mcn.Spec.Pool.Name, poolName)
		return fmt.Errorf("the MCN pool name does not match expected node MCP association")
	}

	// Check MCN name matches node name
	logger.Infof("Checking MCN name matches node name `%v`.", nodeName)
	if mcn.Name != nodeName {
		logger.Errorf("MCN name `%v` does not match node name `%v`.", mcn.Name, nodeName)
		return fmt.Errorf("the MCN name does not match node name")
	}

	// Check desired config version in MCN spec matches desired config on node
	logger.Infof("Checking node `%v` desired config version `%v` matches desired config version in MCN spec.", nodeName, nodeDesiredConfig)
	if mcn.Spec.ConfigVersion.Desired != nodeDesiredConfig {
		logger.Errorf("MCN spec desired config version `%v` does not match node desired config version `%v`.", mcn.Spec.ConfigVersion.Desired, nodeDesiredConfig)
		return fmt.Errorf("MCN spec desired config version does not match node desired config version")
	}

	// Check current config version in MCN status matches current config on node
	logger.Infof("Checking node `%v` current config version `%v` matches current version in MCN status.", nodeName, nodeCurrentConfig)
	if mcn.Status.ConfigVersion.Current != nodeCurrentConfig {
		logger.Infof("MCN status current config version `%v` does not match node current config version `%v`.", mcn.Status.ConfigVersion.Current, nodeCurrentConfig)
		return fmt.Errorf("MCN status current config version does not match node current config version")
	}

	// Check desired config version in MCN status matches desired config on node
	logger.Infof("Checking node `%v` desired config version `%v` matches desired version in MCN status.", nodeName, nodeDesiredConfig)
	if mcn.Status.ConfigVersion.Desired != nodeDesiredConfig {
		logger.Infof("MCN status desired config version `%v` does not match node desired config version `%v`.", mcn.Status.ConfigVersion.Desired, nodeDesiredConfig)
		return fmt.Errorf("MCN status desired config version does not match node desired config version")
	}

	// Check desired image in MCN spec matches desired image on node
	logger.Infof("Checking node `%v` desired image `%v` matches desired image in MCN spec.", nodeName, nodeDesiredImage)
	if mcn.Spec.ConfigImage.DesiredImage != nodeDesiredImage {
		logger.Errorf("MCN spec desired image `%v` does not match node desired image `%v`.", mcn.Spec.ConfigImage.DesiredImage, nodeDesiredImage)
		return fmt.Errorf("MCN spec desired image does not match node desired image")
	}

	// Check current image in MCN status matches current image on node
	logger.Infof("Checking node `%v` current image `%v` matches current image in MCN status.", nodeName, nodeCurrentImage)
	if mcn.Status.ConfigImage.CurrentImage != nodeCurrentImage {
		logger.Infof("MCN status current image `%v` does not match node current image `%v`.", mcn.Status.ConfigImage.CurrentImage, nodeCurrentImage)
		return fmt.Errorf("MCN status current image does not match node current image")
	}

	// Check desired image in MCN status matches desired image on node
	logger.Infof("Checking node `%v` desired image `%v` matches desired image in MCN status.", nodeName, nodeDesiredImage)
	if mcn.Status.ConfigImage.DesiredImage != nodeDesiredImage {
		logger.Infof("MCN status desired image `%v` does not match node desired image `%v`.", mcn.Status.ConfigImage.DesiredImage, nodeDesiredImage)
		return fmt.Errorf("MCN status desired image does not match node desired image")
	}

	return nil
}

// `getMCNCondition` returns the queried condition or nil if the condition does not exist
func getMCNCondition(mcn *mcfgv1.MachineConfigNode, conditionType mcfgv1.StateProgress) *metav1.Condition {
	// Loop through conditions and return the status of the desired condition type
	conditions := mcn.Status.Conditions
	for _, condition := range conditions {
		if condition.Type == string(conditionType) {
			return &condition
		}
	}
	return nil
}

// `getMCNConditionStatus` returns the status of the desired condition type for MCN, or
// an empty string if the condition does not exist
func getMCNConditionStatus(mcn *mcfgv1.MachineConfigNode, conditionType mcfgv1.StateProgress) metav1.ConditionStatus {
	// Loop through conditions and return the status of the desired condition type
	condition := getMCNCondition(mcn, conditionType)
	if condition == nil {
		return ""
	}

	logger.Infof("MCN '%s' %s condition status is %s", mcn.Name, conditionType, condition.Status)
	return condition.Status
}

// `checkMCNConditionStatus` checks that an MCN condition matches the desired status (ex.
// confirm "Updated" is "False")
func checkMCNConditionStatus(mcn *mcfgv1.MachineConfigNode, conditionType mcfgv1.StateProgress, status metav1.ConditionStatus) bool {
	conditionStatus := getMCNConditionStatus(mcn, conditionType)
	if conditionStatus != status && conditionType == mcfgv1.MachineConfigNodeResumed {
		condition := getMCNCondition(mcn, conditionType)
		logger.Infof("LastTransitionTime: %v, Message: %v, ObservedGeneration: %v, Reason: %v, Status: %v, Type: %v", condition.LastTransitionTime, condition.Message, condition.ObservedGeneration, condition.Reason, condition.Status, condition.Type)
	}
	return conditionStatus == status
}

// `WaitForMCNConditionStatus` waits up to a specified timeout for the desired MCN condition to
// match the desired status (ex. wait until "Updated" is "False"). If the desired condition is
// "Unknown," the function will also return true if the condition is "True," which ensures that we
// do not fail when an update progresses quickly through the intermediary "Unknown" phase.
func waitForMCNConditionStatus(machineConfigClient *machineconfigclient.Clientset, mcnName string, conditionType mcfgv1.StateProgress, status metav1.ConditionStatus,
	timeout time.Duration, interval time.Duration) (bool, error) {

	conditionMet := false
	var conditionErr error
	var workerNodeMCN *mcfgv1.MachineConfigNode
	if err := wait.PollUntilContextTimeout(context.TODO(), interval, timeout, true, func(_ context.Context) (bool, error) {
		logger.Infof("Waiting for MCN '%v' %v condition to be %v.", mcnName, conditionType, status)

		workerNodeMCN, conditionErr = machineConfigClient.MachineconfigurationV1().MachineConfigNodes().Get(context.TODO(), mcnName, metav1.GetOptions{})
		// Record if an error occurs when getting the MCN resource
		if conditionErr != nil {
			logger.Infof("Error getting MCN for node '%v': %v", mcnName, conditionErr)
			return false, nil
		}

		// Check if the MCN status is as desired
		conditionMet = checkMCNConditionStatus(workerNodeMCN, conditionType, status)
		// If the condition was not met and we are expecting it may have transitioned quickly
		// trough the "Unknown" phase, check if the condition has flipped to `True`.
		if !conditionMet && status == metav1.ConditionUnknown {
			conditionMet = checkMCNConditionStatus(workerNodeMCN, conditionType, metav1.ConditionTrue)
			logger.Infof("MCN '%v' %v condition was %v, missed transition through %v.", mcnName, conditionType, metav1.ConditionTrue, status)
		}
		return conditionMet, nil
	}); err != nil {
		logger.Infof("The desired MCN condition was never met: %v", err)
		// Handle the situation where there were errors getting the MCN resource
		if conditionErr != nil {
			logger.Infof("An error occurred waiting for MCN '%v' %v condition to be %v: %v", mcnName, conditionType, status, conditionErr)
			return conditionMet, fmt.Errorf("MCN '%v' %v condition was not %v: %v", mcnName, conditionType, status, conditionErr)
		}
		// Handle case when no errors occur grabbing the MCN, but we time out waiting for the condition to be in the desired state
		logger.Infof("A timeout occurred waiting for MCN '%v' %v condition was not %v.", mcnName, conditionType, status)
		return conditionMet, nil
	}

	return conditionMet, conditionErr
}

// `ValidateTransitionThroughConditions` validates the condition trasnitions in the MCN
// during a node update. Note that some conditions are passed through quickly in a node update, so
// the test can "miss" catching the phases. For test stability, if we fail to catch an "Unknown"
// status, a warning will be logged instead of erroring out the test.
func ValidateTransitionThroughConditions(machineConfigClient *machineconfigclient.Clientset, updatingNodeName string, isRebootless, isImageMode bool) {
	// Get the start time of the update
	updateStartTime := metav1.Now()

	// Set the expected wait time for the MCN condition of a working node to flip to Updated=False.
	// For image-based updates, this condition flip can take a while since an image needs to build
	// and push before the nodes uppdate, but we want to make sure non-image updates do not take as
	// long for the condition to flip (that would mean something is wrong and would waste time).
	updatingWaitTime := 1 * time.Minute
	updatingWaitInterval := 1 * time.Second
	if isImageMode {
		updatingWaitTime = 15 * time.Minute
		updatingWaitInterval = 5 * time.Second
	}

	// Test the condition transitions.
	logger.Infof("Waiting for Updated=False")
	conditionMet, err := waitForMCNConditionStatus(machineConfigClient, updatingNodeName, mcfgv1.MachineConfigNodeUpdated, metav1.ConditionFalse, updatingWaitTime, updatingWaitInterval)
	o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("Error occurred while waiting for Updated=False: %v", err))
	o.Expect(conditionMet).To(o.BeTrue(), "Error, could not detect Updated=False.")

	logger.Infof("Waiting for UpdatePrepared=True")
	conditionMet, err = waitForMCNConditionStatus(machineConfigClient, updatingNodeName, mcfgv1.MachineConfigNodeUpdatePrepared, metav1.ConditionTrue, 1*time.Minute, 1*time.Second)
	o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("Error occurred while waiting for UpdatePrepared=True: %v", err))
	o.Expect(conditionMet).To(o.BeTrue(), "Error, could not detect UpdatePrepared=True.")

	logger.Infof("Waiting for UpdateExecuted=Unknown")
	conditionMet, err = waitForMCNConditionStatus(machineConfigClient, updatingNodeName, mcfgv1.MachineConfigNodeUpdateExecuted, metav1.ConditionUnknown, 30*time.Second, 1*time.Second)
	o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("Error occurred while waiting for UpdateExecuted=Unknown: %v", err))
	o.Expect(conditionMet).To(o.BeTrue(), "Error, could not detect UpdateExecuted=Unknown.")

	// On standard, non-rebootless, update, check that node transitions through "Cordoned" and "Drained" phases
	if !isRebootless {
		logger.Infof("Waiting for Cordoned=True")
		conditionMet, err = waitForMCNConditionStatus(machineConfigClient, updatingNodeName, mcfgv1.MachineConfigNodeUpdateCordoned, metav1.ConditionTrue, 30*time.Second, 1*time.Second)
		o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("Error occurred while waiting for Cordoned=True: %v", err))
		o.Expect(conditionMet).To(o.BeTrue(), "Error, could not detect Cordoned=True.")

		logger.Infof("Waiting for Drained=Unknown")
		conditionMet, err = waitForMCNConditionStatus(machineConfigClient, updatingNodeName, mcfgv1.MachineConfigNodeUpdateDrained, metav1.ConditionUnknown, 15*time.Second, 1*time.Second)
		o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("Error occurred while waiting for Drained=Unknown: %v", err))
		o.Expect(conditionMet).To(o.BeTrue(), "Error, could not detect Drained=Unknown.")

		logger.Infof("Waiting for Drained=True")
		conditionMet, err = waitForMCNConditionStatus(machineConfigClient, updatingNodeName, mcfgv1.MachineConfigNodeUpdateDrained, metav1.ConditionTrue, 4*time.Minute, 1*time.Second)
		o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("Error occurred while waiting for Drained=True: %v", err))
		o.Expect(conditionMet).To(o.BeTrue(), "Error, could not detect Drained=True.")
	}

	logger.Infof("Waiting for AppliedFilesAndOS=Unknown")
	conditionMet, err = waitForMCNConditionStatus(machineConfigClient, updatingNodeName, mcfgv1.MachineConfigNodeUpdateFilesAndOS, metav1.ConditionUnknown, 30*time.Second, 1*time.Second)
	o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("Error occurred while waiting for AppliedFilesAndOS=Unknown: %v", err))
	o.Expect(conditionMet).To(o.BeTrue(), "Error, could not detect AppliedFilesAndOS=Unknown.")

	// On image mode update, check that node transitions through the "ImagePulledFromRegistry" phase
	if isImageMode {
		logger.Infof("Waiting for ImagePulledFromRegistry=Unknown")
		conditionMet, err = waitForMCNConditionStatus(machineConfigClient, updatingNodeName, mcfgv1.MachineConfigNodeImagePulledFromRegistry, metav1.ConditionUnknown, 30*time.Second, 1*time.Second)
		o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("Error occurred while waiting for ImagePulledFromRegistry=Unknown: %v", err))
		o.Expect(conditionMet).To(o.BeTrue(), "Error, could not detect ImagePulledFromRegistry=Unknown.")
	}

	logger.Infof("Waiting for AppliedFilesAndOS=True")
	conditionMet, err = waitForMCNConditionStatus(machineConfigClient, updatingNodeName, mcfgv1.MachineConfigNodeUpdateFilesAndOS, metav1.ConditionTrue, 3*time.Minute, 1*time.Second)
	o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("Error occurred while waiting for AppliedFilesAndOS=True: %v", err))
	o.Expect(conditionMet).To(o.BeTrue(), "Error, could not detect AppliedFilesAndOS=True.")

	logger.Infof("Waiting for UpdateExecuted=True")
	conditionMet, err = waitForMCNConditionStatus(machineConfigClient, updatingNodeName, mcfgv1.MachineConfigNodeUpdateExecuted, metav1.ConditionTrue, 20*time.Second, 1*time.Second)
	o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("Error occurred while waiting for UpdateExecuted=True: %v", err))
	o.Expect(conditionMet).To(o.BeTrue(), "Error, could not detect UpdateExecuted=True.")

	// On image mode update, check that node transitions through the "ImagePulledFromRegistry" phase
	if isImageMode {
		logger.Infof("Waiting for ImagePulledFromRegistry=True")
		conditionMet, err = waitForMCNConditionStatus(machineConfigClient, updatingNodeName, mcfgv1.MachineConfigNodeImagePulledFromRegistry, metav1.ConditionTrue, 1*time.Minute, 1*time.Second)
		o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("Error occurred while waiting for ImagePulledFromRegistry=True: %v", err))
		o.Expect(conditionMet).To(o.BeTrue(), "Error, could not detect ImagePulledFromRegistry=True.")
	}

	// On rebootless update, check that node transitions through "UpdatePostActionComplete" phase
	if isRebootless {
		logger.Infof("Waiting for UpdatePostActionComplete=True")
		conditionMet, err = waitForMCNConditionStatus(machineConfigClient, updatingNodeName, mcfgv1.MachineConfigNodeUpdatePostActionComplete, metav1.ConditionTrue, 1*time.Minute, 1*time.Second)
		o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("Error occurred while waiting for UpdatePostActionComplete=True: %v", err))
		o.Expect(conditionMet).To(o.BeTrue(), "Error, could not detect UpdatePostActionComplete=True.")
	} else { // On standard, non-rebootless, update, check that node transitions through "RebootedNode" phase
		logger.Infof("Waiting for RebootedNode=Unknown")
		conditionMet, err = waitForMCNConditionStatus(machineConfigClient, updatingNodeName, mcfgv1.MachineConfigNodeUpdateRebooted, metav1.ConditionUnknown, 15*time.Second, 1*time.Second)
		o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("Error occurred while waiting for RebootedNode=Unknown: %v", err))
		o.Expect(conditionMet).To(o.BeTrue(), "Error, could not detect RebootedNode=Unknown.")

		logger.Infof("Waiting for RebootedNode=True")
		conditionMet, err = waitForMCNConditionStatus(machineConfigClient, updatingNodeName, mcfgv1.MachineConfigNodeUpdateRebooted, metav1.ConditionTrue, 10*time.Minute, 1*time.Second)
		o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("Error occurred while waiting for RebootedNode=True: %v", err))
		o.Expect(conditionMet).To(o.BeTrue(), "Error, could not detect RebootedNode=True.")
	}

	// The final steps of the update happen quickly, so sometimes we can miss the final condition
	// transitions. If we do, we will not error out, but record that the condition was missed.
	logger.Infof("Waiting for Resumed=True")
	conditionMet, err = waitForMCNConditionStatus(machineConfigClient, updatingNodeName, mcfgv1.MachineConfigNodeResumed, metav1.ConditionTrue, 5*time.Second, 1*time.Second)
	o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("Error occurred while waiting for Resumed=True: %v", err))
	if !conditionMet {
		logger.Infof("Warning, could not detect Resumed=True.")
	}
	logger.Infof("Waiting for UpdateComplete=True")
	conditionMet, err = waitForMCNConditionStatus(machineConfigClient, updatingNodeName, mcfgv1.MachineConfigNodeUpdateComplete, metav1.ConditionTrue, 10*time.Second, 1*time.Second)
	o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("Error occurred while waiting for UpdateComplete=True: %v", err))
	if !conditionMet {
		logger.Infof("Warning, could not detect UpdateComplete=True.")
	}
	logger.Infof("Waiting for Uncordoned=True")
	conditionMet, err = waitForMCNConditionStatus(machineConfigClient, updatingNodeName, mcfgv1.MachineConfigNodeUpdateUncordoned, metav1.ConditionTrue, 10*time.Second, 1*time.Second)
	o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("Error occurred while waiting for UpdateComplete=True: %v", err))
	if !conditionMet {
		logger.Infof("Warning, could not detect UpdateComplete=True.")
	}

	logger.Infof("Waiting for Updated=True")
	conditionMet, err = waitForMCNConditionStatus(machineConfigClient, updatingNodeName, mcfgv1.MachineConfigNodeUpdated, metav1.ConditionTrue, 1*time.Minute, 1*time.Second)
	o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("Error occurred while waiting for Updated=True: %v", err))
	o.Expect(conditionMet).To(o.BeTrue(), "Error, could not detect Updated=True.")

	// When the update is not an image mode update, we need to check that the
	// "ImagePulledFromRegistry" condition did not transition during the updates
	if !isImageMode {
		mcn, mcnErr := machineConfigClient.MachineconfigurationV1().MachineConfigNodes().Get(context.TODO(), updatingNodeName, metav1.GetOptions{})
		o.Expect(mcnErr).NotTo(o.HaveOccurred(), fmt.Sprintf("Error occurred while trying to get MCN for node `%v`: %v", updatingNodeName, mcnErr))

		for _, condition := range mcn.Status.Conditions {
			if condition.Type == string(mcfgv1.MachineConfigNodeImagePulledFromRegistry) {
				logger.Infof("Checking that %v was not updated during this update.", condition.Type)
				o.Expect(condition.LastTransitionTime.Before(&updateStartTime)).To(o.BeTrue(), "Expected last transition time of %v condition to be before %v.", condition.Type, updateStartTime)
			}
		}
	}
}

// `ConfirmUpdatedMCNStatus` confirms that an MCN is in a fully updated state, which requires:
//  1. "Updated" = True
//  2. All other conditions = False
func ConfirmUpdatedMCNStatus(clientSet *machineconfigclient.Clientset, mcnName string) bool {
	// Get MCN
	workerNodeMCN, workerErr := clientSet.MachineconfigurationV1().MachineConfigNodes().Get(context.TODO(), mcnName, metav1.GetOptions{})
	o.Expect(workerErr).NotTo(o.HaveOccurred())

	// Loop through conditions and return the status of the desired condition type
	conditions := workerNodeMCN.Status.Conditions
	for _, condition := range conditions {
		if condition.Type == string(mcfgv1.MachineConfigNodeUpdated) && condition.Status != metav1.ConditionTrue {
			logger.Infof("Node '%s' update is not complete; 'Updated' condition status is '%v'", mcnName, condition.Status)
			return false
		} else if condition.Type != string(mcfgv1.MachineConfigNodeUpdated) && condition.Status != metav1.ConditionFalse {
			logger.Infof("Node '%s' is updated but MCN is invalid; '%v' codition status is '%v'", mcnName, condition.Type, condition.Status)
			return false
		}
	}

	logger.Infof("Node '%s' update is complete and corresponding MCN is valid.", mcnName)
	return true
}
