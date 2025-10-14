package extended

import (
	exutil "github.com/openshift/origin/test/extended/util"
)

// MachineConfigNode resource type declaration
type MachineConfigNode struct {
	Resource
}

// NewMachineConfigNode constructor to get MCN resource
func NewMachineConfigNode(oc *exutil.CLI, node string) *MachineConfigNode {
	return &MachineConfigNode{Resource: *NewResource(oc, "machineconfignode", node)}
}
