package extended

import (
	"fmt"

	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

// CAPIMachine struct to handle CAPI Machine resources
type CAPIMachine struct {
	Resource
}

// CAPIMachineList struct to handle lists of CAPI Machine resources
type CAPIMachineList struct {
	ResourceList
}

// NewCAPIMachine constructs a new CAPIMachine struct
func NewCAPIMachine(oc *exutil.CLI, namespace, name string) *CAPIMachine {
	return &CAPIMachine{*NewNamespacedResource(oc, CAPIMachineFullName, namespace, name)}
}

// NewCAPIMachineList constructs a new CAPIMachineList struct
func NewCAPIMachineList(oc *exutil.CLI, namespace string) *CAPIMachineList {
	return &CAPIMachineList{*NewNamespacedResourceList(oc, CAPIMachineFullName, namespace)}
}

// GetNode returns the node created by this CAPI machine.
// CAPI nodes use the annotation cluster.x-k8s.io/machine=MACHINE_NAME (without namespace prefix).
func (m CAPIMachine) GetNode() (*Node, error) {
	nodeList := NewNodeList(m.oc)
	nodeList.SetItemsFilter(`?(@.metadata.annotations.cluster\.x-k8s\.io/machine=="` + m.GetName() + `")`)
	nodes, nErr := nodeList.GetAll()
	if nErr != nil {
		return nil, nErr
	}
	numNodes := len(nodes)
	if numNodes > 1 {
		return nil, fmt.Errorf("more than one nodes linked to this CAPI Machine. Machine: %s. Num nodes:%d",
			m.GetName(), numNodes)
	}

	if numNodes == 0 {
		return nil, fmt.Errorf("no node linked to this CAPI Machine. Machine: %s", m.GetName())
	}

	return nodes[0], nil
}

// GetPhase returns the phase of the CAPI machine
func (m CAPIMachine) GetPhase() (string, error) {
	phase, err := m.Get(`{.status.phase}`)
	if err != nil {
		return "", err
	}
	logger.Infof("CAPI machine %s phase is %s", m.GetName(), phase)
	return phase, nil
}

// IsRunning returns true if the CAPI machine phase is "Running"
func (m CAPIMachine) IsRunning() (bool, error) {
	phase, err := m.GetPhase()
	if err != nil {
		return false, err
	}
	return phase == "Running", nil
}

// GetAll returns a []*CAPIMachine slice with all existing CAPI machines
func (ml CAPIMachineList) GetAll() ([]*CAPIMachine, error) {
	allMResources, err := ml.ResourceList.GetAll()
	if err != nil {
		return nil, err
	}
	allMs := make([]*CAPIMachine, 0, len(allMResources))

	for _, mRes := range allMResources {
		allMs = append(allMs, NewCAPIMachine(ml.oc, mRes.GetNamespace(), mRes.GetName()))
	}

	return allMs, nil
}
