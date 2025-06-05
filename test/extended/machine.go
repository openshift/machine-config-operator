package extended

import (
	"fmt"

	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended/util"
	logger "github.com/openshift/machine-config-operator/test/extended/util/logext"
)

// Machine struct to handle Machine resources
type Machine struct {
	Resource
}

// MachineList struct to handle lists of Machine resources
type MachineList struct {
	ResourceList
}

// NewMachine constructs a new Machine struct
func NewMachine(oc *exutil.CLI, namespace, name string) *Machine {
	return &Machine{*NewNamespacedResource(oc, MachineFullName, namespace, name)}
}

// GetNode returns the node created by this machine
func (m Machine) GetNode() (*Node, error) {
	nodeList := NewNodeList(m.oc)
	nodeList.SetItemsFilter(`?(@.metadata.annotations.machine\.openshift\.io/machine=="openshift-machine-api/` + m.GetName() + `")`)
	nodes, nErr := nodeList.GetAll()
	if nErr != nil {
		return nil, nErr
	}
	numNodes := len(nodes)
	if numNodes > 1 {
		return nil, fmt.Errorf("More than one nodes linked to this Machine. Machine: %s. Num nodes:%d",
			m.GetName(), numNodes)
	}

	if numNodes == 0 {
		return nil, fmt.Errorf("No node linked to this Machine. Machine: %s", m.GetName())
	}

	return &(nodes[0]), nil
}

// GetNodeOrFail, call GetNode, fail the test if any error occurred
func (m Machine) GetNodeOrFail() *Node {
	node, err := m.GetNode()
	o.Expect(err).NotTo(o.HaveOccurred(), "Get node from machine %s failed", m.GetName())
	return node
}

// GetPhase get phase of the machine
func (m Machine) GetPhase() string {
	phase := m.GetOrFail(`{.status.phase}`)
	logger.Infof("machine %s phase is %s", m.GetName(), phase)
	return phase
}

// NewMachineList constructs a new MachineList struct to handle all existing Machines
func NewMachineList(oc *exutil.CLI, namespace string) *MachineList {
	return &MachineList{*NewNamespacedResourceList(oc, MachineFullName, namespace)}
}

// GetAll returns a []Machine slice with all existing nodes
func (ml MachineList) GetAll() ([]Machine, error) {
	allMResources, err := ml.ResourceList.GetAll()
	if err != nil {
		return nil, err
	}
	allMs := make([]Machine, 0, len(allMResources))

	for _, mRes := range allMResources {
		allMs = append(allMs, *NewMachine(ml.oc, mRes.GetNamespace(), mRes.GetName()))
	}

	return allMs, nil
}
