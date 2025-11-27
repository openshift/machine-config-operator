package extended

import (
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
