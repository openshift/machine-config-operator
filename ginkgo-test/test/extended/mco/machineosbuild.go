package mco

import (
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util"
)

// MachineOSBuild resource type declaration
type MachineOSBuild struct {
	Resource
}

// MachineOSBuildList handles list of MachineOSBuild
type MachineOSBuildList struct {
	ResourceList
}

// MachineOSBuild constructor to get MachineOSBuild resource
func NewMachineOSBuild(oc *exutil.CLI, name string) *MachineOSBuild {
	return &MachineOSBuild{Resource: *NewResource(oc, "machineosbuild", name)}
}

// NewMachineOSBuildList construct a new MachineOSBuild list struct to handle all existing MachineOSBuild
func NewMachineOSBuildList(oc *exutil.CLI) *MachineOSBuildList {
	return &MachineOSBuildList{*NewResourceList(oc, "machineosbuild")}
}

// GetMachineOSConfig returns the MachineOSCOnfig resource linked to this MOSB
func (mosb MachineOSBuild) GetMachineOSConfig() (string, error) {
	return mosb.Get(`{.spec.machineOSConfig}`)
}

// GetPod returns the pod used to build this build
func (mosb MachineOSBuild) GetPod() (*Resource, error) {
	podName, err := mosb.Get(`{.status.builderReference.buildPod.name}`)
	if err != nil {
		return nil, err
	}
	podNamespace, err := mosb.Get(`{.status.builderReference.buildPod.namespace}`)
	if err != nil {
		return nil, err
	}

	return NewNamespacedResource(mosb.oc, "pod", podNamespace, podName), nil
}

// GetAll returns a []MachineOSBuild list with all existing pinnedimageset sorted by creation timestamp
func (mosbl *MachineOSBuildList) GetAll() ([]MachineOSBuild, error) {
	mosbl.ResourceList.SortByTimestamp()
	allResources, err := mosbl.ResourceList.GetAll()
	if err != nil {
		return nil, err
	}
	all := make([]MachineOSBuild, 0, len(allResources))

	for _, res := range allResources {
		all = append(all, *NewMachineOSBuild(mosbl.oc, res.name))
	}

	return all, nil
}

// GetAllOrFail returns a []MachineOSBuild list with all existing pinnedimageset sorted by creation time, if any error happens it fails the test
func (mosbl *MachineOSBuildList) GetAllOrFail() []MachineOSBuild {
	all, err := mosbl.GetAll()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error getting the list of existing MachineOSBuild in the cluster")
	return all
}
