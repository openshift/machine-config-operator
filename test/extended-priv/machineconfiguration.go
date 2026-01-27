package extended

import (
	"strings"

	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

// MachineConfiguration struct is used to handle MachineConfiguration resources in OCP
type MachineConfiguration struct {
	Resource
}

// GetMachineConfiguration returns the "cluster" MachineConfiguration resource. It is the only MachineConfiguration resource that can be used
func GetMachineConfiguration(oc *exutil.CLI) *MachineConfiguration {
	return &MachineConfiguration{Resource: *NewResource(oc, "machineconfiguration", "cluster")}
}

// RemoveManagedBootImagesConfig removes the ManagedBootImagesConfig from the MachineConfig resource. It returns a function that can be used to restore the original config and an error.
func (mc MachineConfiguration) RemoveManagedBootImagesConfig() error {
	logger.Infof("Removing .spec.managedBootImages from %s", mc)
	managedBootImages, err := mc.Get(`{.spec.managedBootImages}`)
	if err != nil {
		return err
	}
	if managedBootImages == "" {
		logger.Infof(".spec.managedBootImages does not exist. No need to remove it")
		return nil
	}
	return mc.Patch("json", `[{ "op": "remove", "path": "/spec/managedBootImages"}]`)
}

// SetAllManagedBootImagesConfig configures MachineConfiguration so that all machinesets are updated if necessary
func (mc MachineConfiguration) SetAllManagedBootImagesConfig(resource string) error {
	return mc.Patch("merge", `{"spec":{"managedBootImages":{"machineManagers":[{"resource": "`+resource+`","apiGroup": "machine.openshift.io","selection": {"mode": "All"}}]}}}`)
}

// SetPartialManagedBootImagesConfig  configures MachineConfiguration so that only the machinesets with the given label are updated if necessary
func (mc MachineConfiguration) SetPartialManagedBootImagesConfig(resource, label, value string) error {

	if label == "" && value == "" {
		return mc.Patch("merge", `{"spec":{"managedBootImages":{"machineManagers":[{"resource":"`+resource+`","apiGroup":"machine.openshift.io","selection":{"mode":"Partial","partial":{"machineResourceSelector":{"matchLabels":{}}}}}]}}}`)
	}

	return mc.Patch("merge", `{"spec":{"managedBootImages":{"machineManagers":[{"resource":"`+resource+`","apiGroup":"machine.openshift.io","selection":{"mode":"Partial","partial":{"machineResourceSelector":{"matchLabels":{"`+label+`":"`+value+`"}}}}}]}}}`)
}

// SetNoneManagedBootImagesConfig configures MachineConfiguration so that no machinesets are updated
func (mc MachineConfiguration) SetNoneManagedBootImagesConfig(resource string) error {
	return mc.Patch("merge", `{"spec":{"managedBootImages":{"machineManagers":[{"resource": "`+resource+`","apiGroup": "machine.openshift.io","selection": {"mode": "None"}}]}}}`)
}

// EnableIrreconcilableValidationOverrides enables irreconcilableValidationOverrides for storage
func (mc MachineConfiguration) EnableIrreconcilableValidationOverrides() error {
	return mc.Patch("merge", `{"spec":{"irreconcilableValidationOverrides":{"storage":["Disks","Raid","FileSystems"]}}}`)
}

// GetManagedBootImagesStatus returns the entire .status.managedBootImagesStatus field
func (mc MachineConfiguration) GetManagedBootImagesStatus() (string, error) {
	return mc.Get(`{.status.managedBootImagesStatus}`)
}

// GetManagedBootImagesStatusForResource returns the status for a specific resource type
func (mc MachineConfiguration) GetManagedBootImagesStatusForResource(resource string) (string, error) {
	return mc.Get(`{.status.managedBootImagesStatus.machineManagers[?(@.resource=="` + resource + `")]}`)
}

// GetManagedBootImagesModeForResource returns the selection mode for a specific resource type
func (mc MachineConfiguration) GetManagedBootImagesModeForResource(resource string) (string, error) {
	return mc.Get(`{.status.managedBootImagesStatus.machineManagers[?(@.resource=="` + resource + `")].selection.mode}`)
}

// GetAllManagedBootImagesResources returns all resource types configured in the status as a slice
func (mc MachineConfiguration) GetAllManagedBootImagesResources() ([]string, error) {
	result, err := mc.Get(`{.status.managedBootImagesStatus.machineManagers[*].resource}`)
	if err != nil {
		return nil, err
	}
	if result == "" {
		return []string{}, nil
	}
	return strings.Fields(result), nil
}

// SetManualSkew configures bootImageSkewEnforcement to Manual mode with the specified mode type and version.
// mode should be "RHCOSVersion" or "OCPVersion", version is the corresponding version string.
func (mc MachineConfiguration) SetManualSkew(mode, version string) error {
	logger.Infof("Setting .spec.bootImageSkewEnforcement to Manual mode (%s: %s) on %s", mode, version, mc)
	var versionField string
	switch mode {
	case "RHCOSVersion":
		versionField = `"rhcosVersion":"` + version + `"`
	case "OCPVersion":
		versionField = `"ocpVersion":"` + version + `"`
	default:
		versionField = `"rhcosVersion":"` + version + `"`
	}
	return mc.Patch("merge", `{"spec":{"bootImageSkewEnforcement":{"mode":"Manual","manual":{"mode":"`+mode+`",`+versionField+`}}}}`)
}

// SetNoneSkew configures bootImageSkewEnforcement to None mode, effectively disabling skew enforcement
func (mc MachineConfiguration) SetNoneSkew() error {
	logger.Infof("Setting .spec.bootImageSkewEnforcement to None mode on %s", mc)
	return mc.Patch("merge", `{"spec":{"bootImageSkewEnforcement":{"mode":"None"}}}`)
}

// RemoveSkew removes the bootImageSkewEnforcement config from MachineConfiguration
func (mc MachineConfiguration) RemoveSkew() error {
	logger.Infof("Removing .spec.bootImageSkewEnforcement from %s", mc)
	skewConfig, err := mc.Get(`{.spec.bootImageSkewEnforcement}`)
	if err != nil {
		return err
	}
	if skewConfig == "" {
		logger.Infof(".spec.bootImageSkewEnforcement does not exist. No need to remove it")
		return nil
	}
	return mc.Patch("json", `[{ "op": "remove", "path": "/spec/bootImageSkewEnforcement"}]`)
}
