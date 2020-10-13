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
	ign2 "github.com/coreos/ignition/config/v2_2"
	ign2types "github.com/coreos/ignition/config/v2_2/types"
	ign2_3 "github.com/coreos/ignition/config/v2_3"
	validate2 "github.com/coreos/ignition/config/validate"
	ign3error "github.com/coreos/ignition/v2/config/shared/errors"
	ign3_0 "github.com/coreos/ignition/v2/config/v3_0"
	ign3 "github.com/coreos/ignition/v2/config/v3_1"
	translate3 "github.com/coreos/ignition/v2/config/v3_1/translate"
	ign3types "github.com/coreos/ignition/v2/config/v3_1/types"
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

	var fips bool
	var kernelType string
	var outIgn ign2types.Config
	var err error

	if configs[0].Spec.Config.Raw == nil {
		outIgn = ign2types.Config{}
	} else {
		outIgn, err = ParseAndConvertConfig(configs[0].Spec.Config.Raw)
		if err != nil {
			return nil, err
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
			appendIgn, err = ParseAndConvertConfig(configs[idx].Spec.Config.Raw)
			if err != nil {
				return nil, err
			}
		}
		outIgn = ign2.Append(outIgn, appendIgn)
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

func replaceOrAppend(files []ign2types.File, file ign2types.File) []ign2types.File {
	for i, f := range files {
		if f.Node.Path == file.Node.Path {
			files[i] = file
			return files
		}
	}
	return append(files, file)
}

// ConvertIgnition2to3 takes an ignition spec v2 config and returns a v3 config
func ConvertIgnition2to3(ign2config ign2types.Config) (ign3types.Config, error) {
	// only support writing to root file system
	fsMap := map[string]string{
		"root": "/",
	}

	// Ensure storage has no duplicate files in Storage.Files
	// This is a valid situation in Ign2, but not allowed in Ign3
	files := []ign2types.File{}
	for _, f := range ign2config.Storage.Files {
		files = replaceOrAppend(files, f)
	}
	ign2config.Storage.Files = files

	// Workaround to get v2.3 as input for converter
	ign2_3config := ign2_3.Translate(ign2config)
	ign3_0config, err := ignconverter.Translate(ign2_3config, fsMap)
	if err != nil {
		return ign3types.Config{}, errors.Errorf("unable to convert Ignition spec v2 config to v3: %v", err)
	}
	// Workaround to get a v3.1 config
	converted3 := translate3.Translate(ign3_0config)

	glog.V(4).Infof("Successfully translated Ignition spec v2 config to Ignition spec v3 config: %v", converted3)
	return converted3, nil
}

// ConvertIgnition3to2 takes an ignition spec v3 config and returns a v2 config
func ConvertIgnition3to2(ign3config ign3types.Config) (ign2types.Config, error) {
	// Bad hack to convert spec 3.1 config to spec 3.0
	// ign-convert doesn't currently support spec v3.1
	// TODO(lorbus)
	ign3config.Ignition.Version = "3.0.0"
	raw3_0, err := json.Marshal(ign3config)
	if err != nil {
		return ign2types.Config{}, fmt.Errorf("SHOULD NEVER HAPPEN: failed to marshal Ignition spec v3.0 config %s", err)
	}
	ign3_0config, rep, err := ign3_0.Parse(raw3_0)
	if rep.IsFatal() || err != nil {
		return ign2types.Config{}, fmt.Errorf("SHOULD NEVER HAPPEN: failed to parse Ignition spec v3.1 to v3.0 config: %s\nreport: %v", err, rep)
	}

	converted2, err := ignconverter.Translate3to2(ign3_0config)
	if err != nil {
		return ign2types.Config{}, errors.Errorf("unable to convert Ignition spec v3 config to v2: %v", err)
	}

	glog.V(4).Infof("Successfully translated Ignition spec v3 config to Ignition spec v2 config: %v", converted2)
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
func IgnParseWrapper(rawIgn []byte) (interface{}, error) {
	ignCfg, rpt, err := ign2.Parse(rawIgn)
	if err == nil && !rpt.IsFatal() {
		// this is an ignv2cfg that was successfully parsed
		return ignCfg, nil
	}
	if err.Error() == ign2error.ErrUnknownVersion.Error() {
		// check to see if this is an ign v3.1 config
		ignCfgV3, rptV3, errV3 := ign3.Parse(rawIgn)
		if errV3 == nil && !rptV3.IsFatal() {
			return ignCfgV3, nil
		}
		// unlike spec v2 parsers, v3 parsers aren't chained by default so we need to try parsing as spec v3.0 as well
		if errV3.Error() == ign3error.ErrUnknownVersion.Error() {
			ignCfgV3_0, rptV3_0, errV3_0 := ign3_0.Parse(rawIgn)
			if errV3_0 == nil && !rptV3_0.IsFatal() {
				return translate3.Translate(ignCfgV3_0), nil
			}

			return ign2types.Config{}, errors.Errorf("parsing Ignition config spec v3.0 failed with error: %v\nReport: %v", errV3_0, rptV3_0)
		}

		return ign2types.Config{}, errors.Errorf("parsing Ignition config spec v3.1 failed with error: %v\nReport: %v", errV3, rptV3)
	}

	return ign2types.Config{}, errors.Errorf("parsing Ignition config spec v2 failed with error: %v\nReport: %v", err, rpt)
}

// ParseAndConvertConfig parses rawIgn for both V2 and V3 ignition configs and returns
// a V2 or an error.
func ParseAndConvertConfig(rawIgn []byte) (ign2types.Config, error) {
	ignconfigi, err := IgnParseWrapper(rawIgn)
	if err != nil {
		return ign2types.Config{}, errors.Wrapf(err, "failed to parse Ignition config")
	}

	switch typedConfig := ignconfigi.(type) {
	case ign2types.Config:
		return ignconfigi.(ign2types.Config), nil
	case ign3types.Config:
		convertedIgnV2, err := ConvertIgnition3to2(ignconfigi.(ign3types.Config))
		if err != nil {
			return ign2types.Config{}, errors.Wrapf(err, "failed to convert Ignition config spec v3 to v2")
		}
		return convertedIgnV2, nil
	default:
		return ign2types.Config{}, errors.Errorf("unexpected type for ignition config: %v", typedConfig)
	}
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

	ign3_0config, tSet, err := ctCfg.ToIgn3_0()
	if err != nil {
		return nil, fmt.Errorf("failed to transpile config to Ignition config %s\nTranslation set: %v", err, tSet)
	}

	// Workaround to get a v3.1 config
	ign3config := translate3.Translate(ign3_0config)

	converted2, errV3 := ConvertIgnition3to2(ign3config)
	if errV3 != nil {
		return nil, errors.Errorf("converting Ignition spec v3 config to v2 failed with error: %v", errV3)
	}

	return &converted2, nil
}

// InSlice search for an element in slice and return true if found, otherwise return false
func InSlice(elem string, slice []string) bool {
	for _, k := range slice {
		if k == elem {
			return true
		}
	}
	return false
}
