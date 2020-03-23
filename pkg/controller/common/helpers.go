package common

import (
	"fmt"
	"io/ioutil"
	"reflect"
	"sort"

	"github.com/clarketm/json"
	ign "github.com/coreos/ignition/config/v2_2"
	ignTypes "github.com/coreos/ignition/config/v2_2/types"
	validate "github.com/coreos/ignition/config/validate"
	ignConfigV3 "github.com/coreos/ignition/v2/config"
	ignV3 "github.com/coreos/ignition/v2/config/v3_1_experimental"
	ignTypesV3 "github.com/coreos/ignition/v2/config/v3_1_experimental/types"
	"github.com/golang/glog"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	errors "github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/runtime"
)

// MergeMachineConfigsV2 combines multiple machineconfig objects into one object.
// It sorts all the configs in increasing order of their name.
// It uses the Ignition config from first object as base and appends all the rest.
// Kernel arguments are concatenated.
// It uses only the OSImageURL provided by the CVO and ignores any MC provided OSImageURL.
func MergeMachineConfigsV2(configs []*mcfgv1.MachineConfig, osImageURL string) (*mcfgv1.MachineConfig, error) {
	if len(configs) == 0 {
		return nil, nil
	}
	sort.Slice(configs, func(i, j int) bool { return configs[i].Name < configs[j].Name })

	var fips bool
	var kernelType string

	outIgn, report, err := ign.Parse(configs[0].Spec.Config.Raw)
	if err != nil {
		return nil, errors.Errorf("MergeMachineConfigsV2: parsing Ignition config 0 failed with error: %v\nReport: %v", err, report)
	}

	for idx := 1; idx < len(configs); idx++ {
		// if any of the config has FIPS enabled, it'll be set
		if configs[idx].Spec.FIPS {
			fips = true
		}
		appendIgn, report, err := ign.Parse(configs[idx].Spec.Config.Raw)
		if err != nil {
			return nil, errors.Errorf("MergeMachineConfigsV2: parsing appendix Ignition config %d failed with error: %v\nReport: %v", idx, err, report)
		}
		outIgn = ign.Append(outIgn, appendIgn)
	}
	rawOutIgn, err := json.Marshal(outIgn)
	if err != nil {
		return nil, err
	}

	// sets the KernelType if specified in any of the MachineConfig
	// Setting kerneType to realtime in any of MachineConfig takes priority
	for _, cfg := range configs {
		if cfg.Spec.KernelType == KernelTypeRealtime {
			kernelType = cfg.Spec.KernelType
			break
		} else if kernelType == KernelTypeDefault {
			kernelType = cfg.Spec.KernelType
		}
	}

	// If no MC sets kerneType, then set it to 'default' since that's what it is using
	if kernelType == "" {
		kernelType = KernelTypeDefault
	}

	kargs := []string{}
	for _, cfg := range configs {
		kargs = append(kargs, cfg.Spec.KernelArguments...)
	}

	return &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			OSImageURL:      osImageURL,
			KernelArguments: kargs,
			Config: runtime.RawExtension{
				Raw: rawOutIgn,
			},
			FIPS:       fips,
			KernelType: kernelType,
		},
	}, nil
}

// MergeMachineConfigsV3 combines multiple machineconfig objects into one object.
// It sorts all the configs in increasing order of their name.
// It uses the Ignition config from first object as base and appends all the rest.
// Kernel arguments are concatenated.
// It uses only the OSImageURL provided by the CVO and ignores any MC provided OSImageURL.
func MergeMachineConfigsV3(configs []*mcfgv1.MachineConfig, osImageURL string) (*mcfgv1.MachineConfig, error) {
	if len(configs) == 0 {
		return nil, nil
	}
	sort.Slice(configs, func(i, j int) bool { return configs[i].Name < configs[j].Name })

	var fips bool
	var kernelType string

	internalIgn, report, err := ignConfigV3.Parse(configs[0].Spec.Config.Raw)
	if err != nil {
		return nil, errors.Errorf("MergeMachineConfigsV3: parsing Ignition config 0 failed with error: %v\nReport: %v", err, report)
	}

	for idx := 1; idx < len(configs); idx++ {
		// if any of the config has FIPS enabled, it'll be set
		if configs[idx].Spec.FIPS {
			fips = true
		}
		updateIgn, report, err := ignConfigV3.Parse(configs[idx].Spec.Config.Raw)
		if err != nil {
			return nil, errors.Errorf("MergeMachineConfigsV3: parsing Ignition config %d failed with error: %v\nReport: %v", idx, err, report)
		}
		internalIgn = ignV3.Merge(internalIgn, updateIgn)
	}
	outIgnRaw, err := json.Marshal(&internalIgn)
	if err != nil {
		return nil, err
	}

	// sets the KernelType if specified in any of the MachineConfig
	// Setting kerneType to realtime in any of MachineConfig takes priority
	for _, cfg := range configs {
		if cfg.Spec.KernelType == KernelTypeRealtime {
			kernelType = cfg.Spec.KernelType
			break
		} else if kernelType == KernelTypeDefault {
			kernelType = cfg.Spec.KernelType
		}
	}

	// If no MC sets kerneType, then set it to 'default' since that's what it is using
	if kernelType == "" {
		kernelType = KernelTypeDefault
	}

	kargs := []string{}
	for _, cfg := range configs {
		kargs = append(kargs, cfg.Spec.KernelArguments...)
	}

	return &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			OSImageURL:      osImageURL,
			KernelArguments: kargs,
			Config: runtime.RawExtension{
				Raw: outIgnRaw,
			},
			FIPS:       fips,
			KernelType: kernelType,
		},
	}, nil
}

// NewIgnConfigSpecV2 returns an empty ignition config with version set as latest version
func NewIgnConfigSpecV2() ignTypes.Config {
	return ignTypes.Config{
		Ignition: ignTypes.Ignition{
			Version: ignTypes.MaxVersion.String(),
		},
	}
}

// NewIgnConfigSpecV3 returns an empty ignition config with version set as latest version
func NewIgnConfigSpecV3() ignTypesV3.Config {
	return ignTypesV3.Config{
		Ignition: ignTypesV3.Ignition{
			Version: ignTypesV3.MaxVersion.String(),
		},
	}
}

// WriteTerminationError writes to the Kubernetes termination log.
func WriteTerminationError(err error) {
	msg := err.Error()
	ioutil.WriteFile("/dev/termination-log", []byte(msg), 0644)
	glog.Fatal(msg)
}

// ValidateIgnitionV2 wraps the underlying Ignition validation, but explicitly supports
// a completely empty Ignition config as valid.  This is because we
// want to allow MachineConfig objects which just have e.g. KernelArguments
// set, but no Ignition config.
// Returns nil if the config is valid (per above) or an error containing a Report otherwise.
func ValidateIgnitionV2(cfg ignTypes.Config) error {
	// only validate if Ignition Config is not empty
	if reflect.DeepEqual(ignTypes.Config{}, cfg) {
		return nil
	}
	if report := validate.ValidateWithoutSource(reflect.ValueOf(cfg)); report.IsFatal() {
		return errors.Errorf("invalid Ignition config found: %v", report)
	}
	return nil
}

// ValidateMachineConfigV2 validates that given MachineConfig Spec is valid.
func ValidateMachineConfigV2(cfg mcfgv1.MachineConfigSpec) error {
	if !(cfg.KernelType == "" || cfg.KernelType == KernelTypeDefault || cfg.KernelType == KernelTypeRealtime) {
		return errors.Errorf("kernelType=%s is invalid", cfg.KernelType)
	}
	ignCfg, report, err := ign.Parse(cfg.Config.Raw)
	if err != nil {
		return errors.Errorf("ValidateMachineConfigV2: parsing Ignition config failed with error: %v\nReport: %v", err, report)
	}
	if err := ValidateIgnitionV2(ignCfg); err != nil {
		return err
	}
	return nil
}

// ValidateMachineConfigV3 validates that given MachineConfig Spec is valid.
func ValidateMachineConfigV3(cfg mcfgv1.MachineConfigSpec) error {
	if !(cfg.KernelType == "" || cfg.KernelType == KernelTypeDefault || cfg.KernelType == KernelTypeRealtime) {
		return errors.Errorf("kernelType=%s is invalid", cfg.KernelType)
	}
	// only validate Ignition Config if it is not empty
	rawEmptyIgn, err := json.Marshal(ignTypesV3.Config{})
	if err != nil {
		return fmt.Errorf("SHOULD NOT HAPPEN error marshalling empty Ignition Config: %v", err)
	}
	if reflect.DeepEqual(rawEmptyIgn, cfg.Config.Raw) {
		return nil
	}

	_, report, err := ignConfigV3.Parse(cfg.Config.Raw)
	if err != nil {
		return errors.Errorf("ValidateMachineConfigV3: parsing Ignition config failed with error: %v\nReport: %v", err, report)
	}

	return nil
}
