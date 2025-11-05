package extended

import (
	"fmt"

	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
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
func (mosb MachineOSBuild) GetMachineOSConfig() (*MachineOSConfig, error) {
	moscName, err := mosb.Get(`{.spec.machineOSConfig.name}`)
	if moscName == "" {
		return nil, fmt.Errorf("Could not get the MachineOSCOnfig name from %s", mosb)
	}

	return NewMachineOSConfig(mosb.GetOC(), moscName), err
}

// GetStatusDigestedImagePullSpec resturns the image pull spec resulting from building this machineosbuild
func (mosb MachineOSBuild) GetStatusDigestedImagePullSpec() (string, error) {
	return mosb.Get(`{.status.digestedImagePushSpec}`)
}

// GetMachineConfigName returns the name of the MC that this MOSB is using to build the image
func (mosb MachineOSBuild) GetMachineConfigName() (string, error) {
	return mosb.Get(`{.spec.machineConfig.name}`)
}

// GetJob returns the pod used to build this build
func (mosb MachineOSBuild) GetJob() (*Job, error) {
	jobName, err := mosb.Get(`{.status.builder.job.name}`)
	if err != nil {
		return nil, err
	}
	jobNamespace, err := mosb.Get(`{.status.builder.job.namespace}`)
	if err != nil {
		return nil, err
	}

	if jobName == "" {
		return nil, fmt.Errorf("Could not get the job name from %s", mosb)
	}

	if jobNamespace == "" {
		return nil, fmt.Errorf("Could not get the job namespace from %s", mosb)
	}

	return NewJob(mosb.GetOC(), jobNamespace, jobName), nil
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
