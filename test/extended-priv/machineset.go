package extended

import (
	"fmt"

	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// MachineSet struct to handle MachineSet resources
type MachineSet struct {
	Resource
}

// MachineSetList struct to handle lists of MachineSet resources
type MachineSetList struct {
	ResourceList
}

// NewMachineSet constructs a new MachineSet struct
func NewMachineSet(oc *exutil.CLI, namespace, name string) *MachineSet {
	return &MachineSet{*NewNamespacedResource(oc, MachineSetFullName, namespace, name)}
}

// NewMachineSetList constructs a new MachineSetList struct
func NewMachineSetList(oc *exutil.CLI, namespace string) *MachineSetList {
	return &MachineSetList{*NewNamespacedResourceList(oc, MachineSetFullName, namespace)}
}

// GetAll returns a []MachineSet list with all existing MachineSets
func (msl *MachineSetList) GetAll() ([]MachineSet, error) {
	allMachineSetResources, err := msl.ResourceList.GetAll()
	if err != nil {
		return nil, err
	}
	allMachineSets := make([]MachineSet, 0, len(allMachineSetResources))

	for _, msRes := range allMachineSetResources {
		allMachineSets = append(allMachineSets, *NewMachineSet(msl.oc, msRes.GetNamespace(), msRes.GetName()))
	}

	return allMachineSets, nil
}

// convertUserDataToNewVersion converts the provided userData ignition config into the provided version format
//
//nolint:unparam // newIgnitionVersion is always "2.2.0", but kept for flexibility
func convertUserDataToNewVersion(userData, newIgnitionVersion string) (string, error) {
	var err error

	currentIgnitionVersionResult := gjson.Get(userData, "ignition.version")
	if !currentIgnitionVersionResult.Exists() || currentIgnitionVersionResult.String() == "" {
		logger.Debugf("Could not get ignition version from ignition userData: %s", userData)
		return "", fmt.Errorf("Could not get ignition version from ignition userData. Enable debug GINKGO_TEST_ENABLE_DEBUG_LOG to get more info")
	}
	currentIgnitionVersion := currentIgnitionVersionResult.String()

	if CompareVersions(currentIgnitionVersion, "==", newIgnitionVersion) {
		logger.Infof("Current ignition version %s is the same as the new ignition version %s. No need to manipulate the userData info",
			currentIgnitionVersion, newIgnitionVersion)
	} else {
		if CompareVersions(newIgnitionVersion, "<", "3.0.0") {
			logger.Infof("New ignition version is %s, we need to adapt the userData ignition config to 2.0 config", newIgnitionVersion)
			userData, err = ConvertUserDataIgnition3ToIgnition2(userData)
			if err != nil {
				return "", err
			}
		}
		logger.Infof("Replace ignition version '%s' with  version '%s'", currentIgnitionVersion, newIgnitionVersion)
		userData, err = sjson.Set(userData, "ignition.version", newIgnitionVersion)
		if err != nil {
			return "", err
		}
	}

	return userData, nil
}

// ConvertUserDataIgnition3ToIgnition2 transforms an ignitionV3 userdata configuration into an ignitionV2 userdata configuration
// IMPORTANT: If the ignition config includes storage or systemd they will be deleted. Don't expect this configuration to actually work
//
//	The resulting 2.2.0 ignition config is only for testing purposes. If it is needed to actually transform the ignition version to
//	2.2.0 then this function needs to be modified.
func ConvertUserDataIgnition3ToIgnition2(ignition3 string) (string, error) {
	var (
		ignition2 = ignition3
		err       error
	)
	logger.Infof("Replace the 'merge' action with the 'append' action")
	merge := gjson.Get(ignition3, "ignition.config.merge")
	if !merge.Exists() {
		logger.Debugf("Could not find the 'merge' information in the ignition3 ignition config: %s", ignition3)
		return "", fmt.Errorf("Could not find the 'merge' information in the ignition3 ignition config. Enable debug GINKGO_TEST_ENABLE_DEBUG_LOG to get more info")
	}
	ignition2, err = sjson.SetRaw(ignition2, "ignition.config.append", merge.String())
	if err != nil {
		return "", err
	}

	logger.Infof("Delete ignition.config.merge field")
	ignition2, err = sjson.Delete(ignition2, "ignition.config.merge")
	if err != nil {
		return "", err
	}

	logger.Infof("Delete storage field to create stub ignition config")
	ignition2, err = sjson.Delete(ignition2, "storage")
	if err != nil {
		return "", err
	}

	logger.Infof("Delete systemd field to create stub ignition config")
	ignition2, err = sjson.Delete(ignition2, "systemd")
	if err != nil {
		return "", err
	}

	return ignition2, nil
}
