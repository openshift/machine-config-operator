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
