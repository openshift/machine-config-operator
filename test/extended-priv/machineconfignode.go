package extended

import (
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
)

// MachineConfigNode resource type declaration
type MachineConfigNode struct {
	Resource
}

// NewMachineConfigNode constructor to get MCN resource
func NewMachineConfigNode(oc *exutil.CLI, node string) *MachineConfigNode {
	return &MachineConfigNode{Resource: *NewResource(oc, "machineconfignode", node)}
}

// IsPinnedImageSetsDegraded returns true if the PinnedImageSetsDegraded condition is true
func (mcn *MachineConfigNode) IsPinnedImageSetsDegraded() bool {
	return mcn.IsConditionStatusTrue("PinnedImageSetsDegraded")
}

// IsPinnedImageSetsProgressing returns true if the PinnedImageSetsProgressing condition is true
func (mcn *MachineConfigNode) IsPinnedImageSetsProgressing() bool {
	return mcn.IsConditionStatusTrue("PinnedImageSetsProgressing")
}

// GetPinnedImageSetLastFailedError returns the last failed generation error for pinned image sets
func (mcn *MachineConfigNode) GetPinnedImageSetLastFailedError() string {
	return mcn.GetOrFail(`{.status.pinnedImageSets[*].lastFailedGenerationError}`)
}
