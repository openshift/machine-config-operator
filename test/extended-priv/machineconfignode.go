package extended

import (
	"context"

	o "github.com/onsi/gomega"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	machineconfigclient "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ConfirmUpdatedMCNStatus checks for Updated=True and no active update phase.
// Resumed=True means normal operations have resumed; manually observed actions
// such as Cordoned=True can also coexist with an updated node.
func ConfirmUpdatedMCNStatus(clientSet *machineconfigclient.Clientset, mcnName string) bool {
	mcn, err := clientSet.MachineconfigurationV1().MachineConfigNodes().Get(context.TODO(), mcnName, metav1.GetOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())

	updated := false
	for _, condition := range mcn.Status.Conditions {
		if condition.Type == string(mcfgv1.MachineConfigNodeUpdated) {
			updated = condition.Status == metav1.ConditionTrue
			continue
		}
		if condition.Status != metav1.ConditionFalse && !isCompletedMCNCondition(condition) {
			logger.Infof("Node '%s' has active or unknown condition '%s' with status '%s'", mcnName, condition.Type, condition.Status)
			return false
		}
	}
	return updated
}

// isCompletedMCNCondition identifies true conditions that do not indicate an active update.
func isCompletedMCNCondition(condition metav1.Condition) bool {
	return condition.Status == metav1.ConditionTrue &&
		(condition.Type == string(mcfgv1.MachineConfigNodeResumed) || isObservedNodeActionCondition(condition))
}

// isObservedNodeActionCondition distinguishes manual node actions from update phases.
func isObservedNodeActionCondition(condition metav1.Condition) bool {
	if condition.Status != metav1.ConditionTrue {
		return false
	}
	switch mcfgv1.StateProgress(condition.Type) {
	case mcfgv1.MachineConfigNodeUpdateCordoned:
		return condition.Reason == "NodeUnschedulable"
	case mcfgv1.MachineConfigNodeUpdateDrained:
		return condition.Reason == "NoDrainablePods"
	case mcfgv1.MachineConfigNodeUpdateUncordoned:
		return condition.Reason == "NodeUncordoned"
	case mcfgv1.MachineConfigNodeUpdateRebooted:
		return condition.Reason == "BootIDChanged"
	default:
		return false
	}
}

// MachineConfigNode resource type declaration
type MachineConfigNode struct {
	Resource
}

// MachineConfigNodeList resource type declaration
type MachineConfigNodeList struct {
	ResourceList
}

// NewMachineConfigNode constructor to get MCN resource
func NewMachineConfigNode(oc *exutil.CLI, node string) *MachineConfigNode {
	return &MachineConfigNode{Resource: *NewResource(oc, "machineconfignode", node)}
}

// NewMachineConfigNodeList constructor to get MCN list
func NewMachineConfigNodeList(oc *exutil.CLI) *MachineConfigNodeList {
	return &MachineConfigNodeList{ResourceList: *NewResourceList(oc, "machineconfignodes")}
}

// GetAll get list of MachineConfigNode
func (mcnl *MachineConfigNodeList) GetAll() ([]*MachineConfigNode, error) {
	resources, err := mcnl.ResourceList.GetAll()
	if err != nil {
		return nil, err
	}

	allMCNs := make([]*MachineConfigNode, 0, len(resources))
	for _, mcn := range resources {
		allMCNs = append(allMCNs, NewMachineConfigNode(mcnl.oc, mcn.GetName()))
	}

	return allMCNs, nil
}

// GetDesiredMachineConfigOfSpec get value of `.spec.configVersion.desired`
func (mcn *MachineConfigNode) GetDesiredMachineConfigOfSpec() string {
	return mcn.GetOrFail(`{.spec.configVersion.desired}`)
}

// GetDesiredMachineConfigOfStatus get value of `.status.configVersion.desired`
func (mcn *MachineConfigNode) GetDesiredMachineConfigOfStatus() string {
	return mcn.GetOrFail(`{.status.configVersion.desired}`)
}

// GetCurrentMachineConfigOfStatus get value of `.status.configVersion.current`
func (mcn *MachineConfigNode) GetCurrentMachineConfigOfStatus() string {
	return mcn.GetOrFail(`{.status.configVersion.current}`)
}

// GetPool get value of `.spec.pool.name`
func (mcn *MachineConfigNode) GetPoolName() (string, error) {
	return mcn.Get(`{.spec.pool.name}`)
}

// GetNode get value of `.spec.node.name`
func (mcn *MachineConfigNode) GetNode() string {
	return mcn.GetOrFail(`{.spec.node.name}`)
}

// GetUpdated get condition status of `Updated`
func (mcn *MachineConfigNode) GetUpdated() string {
	return mcn.GetConditionStatusByType("Updated")
}

// GetUpdatePrepared get condition status of `UpdatePrepared`
func (mcn *MachineConfigNode) GetUpdatePrepared() string {
	return mcn.GetConditionStatusByType("UpdatePrepared")
}

// GetUpdateExecuted get condition status of `UpdateExecuted`
func (mcn *MachineConfigNode) GetUpdateExecuted() string {
	return mcn.GetConditionStatusByType("UpdateExecuted")
}

// GetUpdatePostActionComplete get condition status of `UpdatePostActionComplete`
func (mcn *MachineConfigNode) GetUpdatePostActionComplete() string {
	return mcn.GetConditionStatusByType("UpdatePostActionComplete")
}

// GetUpdateComplete get condition status of `UpdateComplete`
func (mcn *MachineConfigNode) GetUpdateComplete() string {
	return mcn.GetConditionStatusByType("UpdateComplete")
}

// GetResumed get condition status of `Resumed`
func (mcn *MachineConfigNode) GetResumed() string {
	return mcn.GetConditionStatusByType("Resumed")
}

// GetUpdateCompatible get condition status of `UpdateCompatible`
func (mcn *MachineConfigNode) GetUpdateCompatible() string {
	return mcn.GetConditionStatusByType("UpdateCompatible")
}

// GetAppliedFiles get condition status of `AppliedFiles`
func (mcn *MachineConfigNode) GetAppliedFiles() string {
	return mcn.GetConditionStatusByType("AppliedFiles")
}

// GetAppliedConditionStatus returns the status of the appropriate Applied condition.
// It checks for AppliedFilesAndOS first (non-TP clusters), then falls back to AppliedFiles (TP clusters)
func (mcn *MachineConfigNode) GetAppliedConditionStatus() string {
	// Try AppliedFilesAndOS first (non-TechPreview clusters)
	status, err := mcn.Get(`{.status.conditions[?(@.type=="AppliedFilesAndOS")].status}`)
	if err == nil && status != "" {
		return status
	}
	// Fall back to AppliedFiles (TechPreview clusters)
	return mcn.GetAppliedFiles()
}

// GetCordoned get condition status of `Cordoned`
func (mcn *MachineConfigNode) GetCordoned() string {
	return mcn.GetConditionStatusByType("Cordoned")
}

// GetUncordoned get condition status of `Uncordoned`
func (mcn *MachineConfigNode) GetUncordoned() string {
	return mcn.GetConditionStatusByType("Uncordoned")
}

// GetDrained get condition status of `Drained`
func (mcn *MachineConfigNode) GetDrained() string {
	return mcn.GetConditionStatusByType("Drained")
}

// GetRebootedNode get condition status of `RebootedNode`
func (mcn *MachineConfigNode) GetRebootedNode() string {
	return mcn.GetConditionStatusByType("RebootedNode")
}

// GetReloadedCRIO get condition status of `ReloadedCRIO`
func (mcn *MachineConfigNode) GetReloadedCRIO() string {
	return mcn.GetConditionStatusByType("ReloadedCRIO")
}

func (mcn *MachineConfigNode) IsPinnedImageSetsDegraded() bool {
	return mcn.IsConditionStatusTrue("PinnedImageSetsDegraded")
}

func (mcn *MachineConfigNode) IsPinnedImageSetsProgressing() bool {
	return mcn.IsConditionStatusTrue("PinnedImageSetsProgressing")
}

// GetDesiredMachineConfigOfSpec get value of `.spec.configVersion.desired`
func (mcn *MachineConfigNode) GetPinnedImageSetLastFailedError() string {
	return mcn.GetOrFail(`{.status.pinnedImageSets[*].lastFailedGenerationError}`)
}

// GetDesiredImage get value of `.spec.configImage.desiredImage`
func (mcn *MachineConfigNode) GetSpecDesiredImage() (string, error) {
	return mcn.Get(`{.spec.configImage.desiredImage}`)
}

// GetStatusDesiredImage get value of `.status.configImage.desiredImage`
func (mcn *MachineConfigNode) GetStatusDesiredImage() (string, error) {
	return mcn.Get(`{.status.configImage.desiredImage}`)
}

// GetStatusCurrentImage get value of `.status.configImage.currentImage`
func (mcn *MachineConfigNode) GetStatusCurrentImage() (string, error) {
	return mcn.Get(`{.status.configImage.currentImage}`)
}
