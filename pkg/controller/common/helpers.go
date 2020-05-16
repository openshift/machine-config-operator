package common

import (
	"fmt"
	"io/ioutil"
	"reflect"
	"sort"

	"github.com/clarketm/json"
	fcctbase "github.com/coreos/fcct/base/v0_1"
	ignconverter "github.com/coreos/ign-converter"
	ign2error "github.com/coreos/ignition/config/shared/errors"
	ign "github.com/coreos/ignition/config/v2_2"
	ign2types "github.com/coreos/ignition/config/v2_2/types"
	validate2 "github.com/coreos/ignition/config/validate"
	ign3 "github.com/coreos/ignition/v2/config/v3_0"
	ign3types "github.com/coreos/ignition/v2/config/v3_0/types"
	validate3 "github.com/coreos/ignition/v2/config/validate"
	"github.com/ghodss/yaml"
	"github.com/golang/glog"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/runtime"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
)

// MergeMachineConfigs combines multiple machineconfig objects into one object.
// It sorts all the configs in increasing order of their name.
// It uses the Ignition config from first object as base and appends all the rest.
// Kernel arguments are concatenated.
// It uses only the OSImageURL provided by the CVO and ignores any MC provided OSImageURL.
func MergeMachineConfigs(configs []*mcfgv1.MachineConfig, osImageURL string) (*mcfgv1.MachineConfig, error) {
	if len(configs) == 0 {
		return nil, nil
	}
	sort.Slice(configs, func(i, j int) bool { return configs[i].Name < configs[j].Name })

	var fips, ok bool
	var kernelType string
	var outIgn ign2types.Config

	if configs[0].Spec.Config.Raw == nil {
		outIgn = ign2types.Config{}
	} else {
		parsedIgn, err := IgnParseWrapper(configs[0].Spec.Config.Raw)
		if err != nil {
			return nil, err
		}
		switch parsedIgnValue := parsedIgn.(type) {
		case ign3types.Config:
			convertedIgn, err := convertIgnition3to2(parsedIgnValue)
			if err != nil {
				return nil, err
			}
			outIgn = convertedIgn
		default:
			outIgn, ok = parsedIgn.(ign2types.Config)
			if !ok {
				return nil, errors.Errorf("something unexpected happened when parsing: %v", ok)
			}
		}
	}

	for idx := 1; idx < len(configs); idx++ {
		// if any of the config has FIPS enabled, it'll be set
		if configs[idx].Spec.FIPS {
			fips = true
		}

		var appendIgn ign2types.Config
		if configs[idx].Spec.Config.Raw == nil {
			appendIgn = ign2types.Config{}
		} else {
			parsedIgn, err := IgnParseWrapper(configs[idx].Spec.Config.Raw)
			if err != nil {
				return nil, err
			}
			switch parsedIgnValue := parsedIgn.(type) {
			case ign3types.Config:
				convertedIgn, err := convertIgnition3to2(parsedIgnValue)
				if err != nil {
					return nil, err
				}
				appendIgn = convertedIgn
			default:
				appendIgn, ok = parsedIgn.(ign2types.Config)
				if !ok {
					return nil, errors.Errorf("something unexpected happened when parsing: %v", ok)
				}
			}
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

// NewIgnConfig returns an empty ignition config with version set as latest version
func NewIgnConfig() ign2types.Config {
	return ign2types.Config{
		Ignition: ign2types.Ignition{
			Version: ign2types.MaxVersion.String(),
		},
	}
}

// WriteTerminationError writes to the Kubernetes termination log.
func WriteTerminationError(err error) {
	msg := err.Error()
	ioutil.WriteFile("/dev/termination-log", []byte(msg), 0644)
	glog.Fatal(msg)
}

// convertIgnition3to2 takes an igntion v3 config and returns a v2 config
func convertIgnition3to2(ignconfig ign3types.Config) (ign2types.Config, error) {
	converted2, err := ignconverter.Translate3to2(ignconfig)
	if err != nil {
		return converted2, errors.Errorf("unable to convert Ignition V3 config to V2: %v", err)
	}
	glog.V(4).Infof("Successfully translated ignition V3 config to ignition V2 config: %v", converted2)
	return converted2, nil
}

// ValidateIgnition wraps the underlying Ignition V2/V3 validation, but explicitly supports
// a completely empty Ignition config as valid.  This is because we
// want to allow MachineConfig objects which just have e.g. KernelArguments
// set, but no Ignition config.
// Returns nil if the config is valid (per above) or an error containing a Report otherwise.
func ValidateIgnition(ignconfig interface{}) error {
	switch cfg := ignconfig.(type) {
	case ign2types.Config:
		if reflect.DeepEqual(ign2types.Config{}, cfg) {
			return nil
		}
		if report := validate2.ValidateWithoutSource(reflect.ValueOf(cfg)); report.IsFatal() {
			return errors.Errorf("invalid ignition V2 config found: %v", report)
		}
		return nil
	case ign3types.Config:
		if reflect.DeepEqual(ign3types.Config{}, cfg) {
			return nil
		}
		if report := validate3.ValidateWithContext(cfg, nil); report.IsFatal() {
			return errors.Errorf("invalid ignition V3 config found: %v", report)
		}
		return nil
	default:
		return errors.Errorf("unrecognized ignition type")
	}
}

// ValidateMachineConfig validates that given MachineConfig Spec is valid.
func ValidateMachineConfig(cfg mcfgv1.MachineConfigSpec) error {
	if !(cfg.KernelType == "" || cfg.KernelType == KernelTypeDefault || cfg.KernelType == KernelTypeRealtime) {
		return errors.Errorf("kernelType=%s is invalid", cfg.KernelType)
	}

	if cfg.Config.Raw != nil {
		ignCfg, err := IgnParseWrapper(cfg.Config.Raw)
		if err != nil {
			return err
		}
		if err := ValidateIgnition(ignCfg); err != nil {
			return err
		}
	}
	return nil
}

// IgnParseWrapper parses rawIgn for both V2 and V3 ignition configs and returns
// a V2 or V3 Config or an error. This wrapper is necessary since V2 and V3 use different parsers.
func IgnParseWrapper(rawIgn []byte) (ignconfig interface{}, err error) {
	ignCfg, rpt, err := ign.Parse(rawIgn)

	// this is an ignv2cfg that was successfully parsed
	if err == nil {
		return ignCfg, nil
	}
	if err.Error() == ign2error.ErrUnknownVersion.Error() {
		// check to see if this is an ignv3cfg
		ignCfgV3, rptV3, errV3 := ign3.Parse(rawIgn)
		if errV3 == nil {
			return ignCfgV3, nil
		}
		return ign2types.Config{}, errors.Errorf("parsing Ignition config failed with error: %v\nReport: %v", errV3, rptV3)
	}
	return ign2types.Config{}, errors.Errorf("parsing Ignition config failed with error: %v\nReport: %v", err, rpt)
}

// TranspileCoreOSConfigToIgn transpiles Fedora CoreOS config to ignition
// internally it transpiles to Ign spec v3 config and translates to spec v2
func TranspileCoreOSConfigToIgn(files, units []string) (*ign2types.Config, error) {
	var ctCfg fcctbase.Config
	overwrite := true
	// Convert data to Ignition resources
	for _, d := range files {
		f := new(fcctbase.File)
		if err := yaml.Unmarshal([]byte(d), f); err != nil {
			return nil, fmt.Errorf("failed to unmarshal file into struct: %v", err)
		}
		f.Overwrite = &overwrite

		// Add the file to the config
		ctCfg.Storage.Files = append(ctCfg.Storage.Files, *f)
	}

	for _, d := range units {
		u := new(fcctbase.Unit)
		if err := yaml.Unmarshal([]byte(d), u); err != nil {
			return nil, fmt.Errorf("failed to unmarshal systemd unit into struct: %v", err)
		}

		// Add the unit to the config
		ctCfg.Systemd.Units = append(ctCfg.Systemd.Units, *u)
	}

	ign3Cfg, tSet, err := ctCfg.ToIgn3_0()
	if err != nil {
		return nil, fmt.Errorf("failed to transpile config to Ignition config %s\nTranslation set: %v", err, tSet)
	}

	convertedIgnCfgV2, errV3 := convertIgnition3to2(ign3Cfg)
	if errV3 != nil {
		return nil, errors.Errorf("converting Ignition spec v3 config to v2 failed with error: %v", errV3)
	}

	return &convertedIgnCfgV2, nil
}
