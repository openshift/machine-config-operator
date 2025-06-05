package extended

import (
	exutil "github.com/openshift/machine-config-operator/test/extended/util"
)

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
func (mcnl *MachineConfigNodeList) GetAll() ([]MachineConfigNode, error) {
	resources, err := mcnl.ResourceList.GetAll()
	if err != nil {
		return nil, err
	}

	allMCNs := make([]MachineConfigNode, 0, len(resources))
	for _, mcn := range resources {
		allMCNs = append(allMCNs, *NewMachineConfigNode(mcnl.oc, mcn.GetName()))
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
func (mcn *MachineConfigNode) GetPool() string {
	return mcn.GetOrFail(`{.spec.pool.name}`)
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

// GetAppliedFilesAndOS get condition status of `AppliedFilesAndOS`
func (mcn *MachineConfigNode) GetAppliedFilesAndOS() string {
	return mcn.GetConditionStatusByType("AppliedFilesAndOS")
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
