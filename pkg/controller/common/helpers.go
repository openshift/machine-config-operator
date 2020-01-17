package common

import (
	"io/ioutil"
	"reflect"

	igntypes "github.com/coreos/ignition/config/v2_2/types"
	validate "github.com/coreos/ignition/config/validate"
	"github.com/golang/glog"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	errors "github.com/pkg/errors"
)

// NewIgnConfig returns an empty ignition config with version set as latest version
func NewIgnConfig() igntypes.Config {
	return igntypes.Config{
		Ignition: igntypes.Ignition{
			Version: igntypes.MaxVersion.String(),
		},
	}
}

// WriteTerminationError writes to the Kubernetes termination log.
func WriteTerminationError(err error) {
	msg := err.Error()
	ioutil.WriteFile("/dev/termination-log", []byte(msg), 0644)
	glog.Fatal(msg)
}

// ValidateIgnition wraps the underlying Ignition validation, but explicitly supports
// a completely empty Ignition config as valid.  This is because we
// want to allow MachineConfig objects which just have e.g. KernelArguments
// set, but no Ignition config.
// Returns nil if the config is valid (per above) or an error containing a Report otherwise.
func ValidateIgnition(cfg igntypes.Config) error {
	// only validate if Ignition Config is not empty
	if reflect.DeepEqual(igntypes.Config{}, cfg) {
		return nil
	}
	if report := validate.ValidateWithoutSource(reflect.ValueOf(cfg)); report.IsFatal() {
		return errors.Errorf("invalid Ignition config found: %v", report)
	}
	return nil
}

// ValidateMachineConfig validates that given MachineConfig Spec is valid.
func ValidateMachineConfig(cfg mcfgv1.MachineConfigSpec) error {

	if !(cfg.KernelType == "" || cfg.KernelType == "default" || cfg.KernelType == "realtime") {
		return errors.Errorf("kernelType=%s is invalid", cfg.KernelType)
	}
	if err := ValidateIgnition(cfg.Config); err != nil {
		return err
	}
	return nil
}
