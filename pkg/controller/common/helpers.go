package common

import (
	"context"
	"fmt"
	"io/ioutil"
	"reflect"
	"sort"

	"github.com/clarketm/json"
	fcctbase "github.com/coreos/fcct/base/v0_1"
	"github.com/coreos/ign-converter/translate/v23tov30"
	"github.com/coreos/ign-converter/translate/v31tov22"
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
	kerr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	mcfgclientset "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
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
	sort.SliceStable(configs, func(i, j int) bool { return configs[i].Name < configs[j].Name })

	var fips bool
	var kernelType string
	var outIgn ign3types.Config
	var err error

	if configs[0].Spec.Config.Raw == nil {
		outIgn = ign3types.Config{
			Ignition: ign3types.Ignition{
				Version: ign3types.MaxVersion.String(),
			},
		}
	} else {
		outIgn, err = ParseAndConvertConfig(configs[0].Spec.Config.Raw)
		if err != nil {
			return nil, err
		}
	}

	for idx := 1; idx < len(configs); idx++ {
		if configs[idx].Spec.Config.Raw != nil {
			mergedIgn, err := ParseAndConvertConfig(configs[idx].Spec.Config.Raw)
			if err != nil {
				return nil, err
			}
			outIgn = ign3.Merge(outIgn, mergedIgn)
		}
	}
	rawOutIgn, err := json.Marshal(outIgn)
	if err != nil {
		return nil, err
	}

	// sets the KernelType if specified in any of the MachineConfig
	// Setting kerneType to realtime in any of MachineConfig takes priority
	// also if any of the config has FIPS enabled, it'll be set
	for _, cfg := range configs {
		if cfg.Spec.FIPS {
			fips = true
		}
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

	extensions := []string{}
	for _, cfg := range configs {
		extensions = append(extensions, cfg.Spec.Extensions...)
	}

	// Ensure that kernel-devel extension is applied only with default kernel.
	if kernelType != KernelTypeDefault {
		if InSlice("kernel-devel", extensions) {
			return nil, fmt.Errorf("installing kernel-devel extension is not supported with kernelType: %s", kernelType)
		}
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
			Extensions: extensions,
		},
	}, nil
}

// NewIgnConfig returns an empty ignition config with version set as latest version
func NewIgnConfig() ign3types.Config {
	return ign3types.Config{
		Ignition: ign3types.Ignition{
			Version: ign3types.MaxVersion.String(),
		},
	}
}

// WriteTerminationError writes to the Kubernetes termination log.
func WriteTerminationError(err error) {
	msg := err.Error()
	ioutil.WriteFile("/dev/termination-log", []byte(msg), 0644)
	glog.Fatal(msg)
}

// ConvertRawExtIgnitionToV3 ensures that the Ignition config in
// the RawExtension is spec v3.1, or translates to it.
func ConvertRawExtIgnitionToV3(inRawExtIgn *runtime.RawExtension) (runtime.RawExtension, error) {
	// This function is only used by the MCServer so we don't need to consider v3.0
	_, rptV3, errV3 := ign3.Parse(inRawExtIgn.Raw)
	if errV3 == nil && !rptV3.IsFatal() {
		// The rawExt is already on V3.1, no need to translate
		return *inRawExtIgn, nil
	}

	ignCfg, rpt, err := ign2.Parse(inRawExtIgn.Raw)
	if err != nil || rpt.IsFatal() {
		return runtime.RawExtension{}, errors.Errorf("parsing Ignition config spec v2.2 failed with error: %v\nReport: %v", err, rpt)
	}
	converted3, err := convertIgnition2to3(ignCfg)
	if err != nil {
		return runtime.RawExtension{}, errors.Errorf("failed to convert config from spec v2.2 to v3.1: %v", err)
	}

	outIgnV3, err := json.Marshal(converted3)
	if err != nil {
		return runtime.RawExtension{}, errors.Errorf("failed to marshal converted config: %v", err)
	}

	outRawExt := runtime.RawExtension{}
	outRawExt.Raw = outIgnV3

	return outRawExt, nil
}

// ConvertRawExtIgnitionToV2 ensures that the Ignition config in
// the RawExtension is spec v2.2, or translates to it.
func ConvertRawExtIgnitionToV2(inRawExtIgn *runtime.RawExtension) (runtime.RawExtension, error) {
	ignCfg, rpt, err := ign3.Parse(inRawExtIgn.Raw)
	if err != nil || rpt.IsFatal() {
		return runtime.RawExtension{}, errors.Errorf("parsing Ignition config spec v3.1 failed with error: %v\nReport: %v", err, rpt)
	}

	converted2, err := convertIgnition3to2(ignCfg)
	if err != nil {
		return runtime.RawExtension{}, errors.Errorf("failed to convert config from spec v3.1 to v2.2: %v", err)
	}

	outIgnV2, err := json.Marshal(converted2)
	if err != nil {
		return runtime.RawExtension{}, errors.Errorf("failed to marshal converted config: %v", err)
	}

	outRawExt := runtime.RawExtension{}
	outRawExt.Raw = outIgnV2

	return outRawExt, nil
}

// convertIgnition2to3 takes an ignition spec v2.2 config and returns a v3.1 config
func convertIgnition2to3(ign2config ign2types.Config) (ign3types.Config, error) {
	// only support writing to root file system
	fsMap := map[string]string{
		"root": "/",
	}

	// Workaround to get v2.3 as input for converter
	ign2_3config := ign2_3.Translate(ign2config)
	ign3_0config, err := v23tov30.Translate(ign2_3config, fsMap)
	if err != nil {
		return ign3types.Config{}, errors.Errorf("unable to convert Ignition spec v2 config to v3: %v", err)
	}
	// Workaround to get a v3.1 config as output
	converted3 := translate3.Translate(ign3_0config)

	glog.V(4).Infof("Successfully translated Ignition spec v2 config to Ignition spec v3 config: %v", converted3)
	return converted3, nil
}

// convertIgnition3to2 takes an ignition spec v3.1 config and returns a v2.2 config
func convertIgnition3to2(ign3config ign3types.Config) (ign2types.Config, error) {
	converted2, err := v31tov22.Translate(ign3config)
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

// InSlice search for an element in slice and return true if found, otherwise return false
func InSlice(elem string, slice []string) bool {
	for _, k := range slice {
		if k == elem {
			return true
		}
	}
	return false
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
	ignCfgV3_1, rptV3_1, errV3_1 := ign3.Parse(rawIgn)
	if errV3_1 == nil && !rptV3_1.IsFatal() {
		return ignCfgV3_1, nil
	}
	// unlike spec v2 parsers, v3 parsers aren't chained by default so we need to try parsing as spec v3.0 as well
	if errV3_1.Error() == ign3error.ErrUnknownVersion.Error() {
		ignCfgV3_0, rptV3_0, errV3_0 := ign3_0.Parse(rawIgn)
		if errV3_0 == nil && !rptV3_0.IsFatal() {
			return translate3.Translate(ignCfgV3_0), nil
		}

		if errV3_0.Error() == ign3error.ErrUnknownVersion.Error() {
			ignCfgV2, rptV2, errV2 := ign2.Parse(rawIgn)
			if errV2 == nil && !rptV2.IsFatal() {
				return ignCfgV2, nil
			}

			// If the error is still UnknownVersion it's not a 3.1/3.0 or 2.x config, thus unsupported
			if errV2.Error() == ign2error.ErrUnknownVersion.Error() {
				return ign3types.Config{}, errors.Errorf("parsing Ignition config failed: unknown version. Supported spec versions: 2.2, 3.0, 3.1")
			}
			return ign3types.Config{}, errors.Errorf("parsing Ignition spec v2 failed with error: %v\nReport: %v", errV2, rptV2)
		}
		return ign3types.Config{}, errors.Errorf("parsing Ignition config spec v3.0 failed with error: %v\nReport: %v", errV3_0, rptV3_0)
	}
	return ign3types.Config{}, errors.Errorf("parsing Ignition config spec v3.1 failed with error: %v\nReport: %v", errV3_1, rptV3_1)
}

// ParseAndConvertConfig parses rawIgn for both V2 and V3 ignition configs and returns
// a V3 or an error.
func ParseAndConvertConfig(rawIgn []byte) (ign3types.Config, error) {
	ignconfigi, err := IgnParseWrapper(rawIgn)
	if err != nil {
		return ign3types.Config{}, errors.Wrapf(err, "failed to parse Ignition config")
	}

	switch typedConfig := ignconfigi.(type) {
	case ign3types.Config:
		return ignconfigi.(ign3types.Config), nil
	case ign2types.Config:
		ignconfv2, err := removeIgnDuplicateFilesUnitsUsers(ignconfigi.(ign2types.Config))
		if err != nil {
			return ign3types.Config{}, err
		}
		convertedIgnV3, err := convertIgnition2to3(ignconfv2)
		if err != nil {
			return ign3types.Config{}, errors.Wrapf(err, "failed to convert Ignition config spec v2 to v3")
		}
		return convertedIgnV3, nil
	default:
		return ign3types.Config{}, errors.Errorf("unexpected type for ignition config: %v", typedConfig)
	}
}

// Function to remove duplicated files/units/users from a V2 MC, since the translator
// (and ignition spec V3) does not allow for duplicated entries in one MC.
// This should really not change the actual final behaviour, since it keeps
// ordering into consideration and has contents from the highest alphanumeric
// MC's final version of a file.
// Note:
// Append is not considered since we do not allow for appending
// Units have one exception: dropins are concat'ed

func removeIgnDuplicateFilesUnitsUsers(ignConfig ign2types.Config) (ign2types.Config, error) {

	files := ignConfig.Storage.Files
	units := ignConfig.Systemd.Units
	users := ignConfig.Passwd.Users

	filePathMap := map[string]bool{}
	var outFiles []ign2types.File
	for i := len(files) - 1; i >= 0; i-- {
		// We do not actually support to other filesystems so we make the assumption that there is only 1 here
		path := files[i].Path
		if _, isDup := filePathMap[path]; isDup {
			continue
		}
		outFiles = append(outFiles, files[i])
		filePathMap[path] = true
	}

	unitNameMap := map[string]bool{}
	var outUnits []ign2types.Unit
	for i := len(units) - 1; i >= 0; i-- {
		unitName := units[i].Name
		if _, isDup := unitNameMap[unitName]; isDup {
			// this is a duplicated unit by name, so let's check for the dropins and append them
			if len(units[i].Dropins) > 0 {
				for j := range outUnits {
					if outUnits[j].Name == unitName {
						// outUnits[j] is the highest priority entry with this unit name
						// now loop over the new unit's dropins and append it if the name
						// isn't duplicated in the existing unit's dropins
						for _, newDropin := range units[i].Dropins {
							hasExistingDropin := false
							for _, existingDropins := range outUnits[j].Dropins {
								if existingDropins.Name == newDropin.Name {
									hasExistingDropin = true
									break
								}
							}
							if !hasExistingDropin {
								outUnits[j].Dropins = append(outUnits[j].Dropins, newDropin)
							}
						}
						continue
					}
				}
				glog.V(2).Infof("Found duplicate unit %v, appending dropin section", unitName)
			}
			continue
		}
		outUnits = append(outUnits, units[i])
		unitNameMap[unitName] = true
	}

	// Concat sshkey sections into the newest passwdUser in the list
	// We make the assumption that there is only one user: core
	// since that is the only supported user by design.
	// It's technically possible, though, to have created another user
	// during install time configs, since we only check the validity of
	// the passwd section if it was changed. Explicitly error in that case.
	if len(users) > 0 {
		outUser := users[len(users)-1]
		if outUser.Name != "core" {
			return ignConfig, errors.Errorf("unexpected user with name: %v. Only core user is supported.", outUser.Name)
		}
		for i := len(users) - 2; i >= 0; i-- {
			if users[i].Name != "core" {
				return ignConfig, errors.Errorf("unexpected user with name: %v. Only core user is supported.", users[i].Name)
			}
			for j := range users[i].SSHAuthorizedKeys {
				outUser.SSHAuthorizedKeys = append(outUser.SSHAuthorizedKeys, users[i].SSHAuthorizedKeys[j])
			}
		}
		ignConfig.Passwd.Users = []ign2types.PasswdUser{outUser}
	}

	// outFiles and outUnits should now have all duplication removed
	ignConfig.Storage.Files = outFiles
	ignConfig.Systemd.Units = outUnits

	return ignConfig, nil
}

// TranspileCoreOSConfigToIgn transpiles Fedora CoreOS config to ignition
// internally it transpiles to Ign spec v3 config
func TranspileCoreOSConfigToIgn(files, units []string) (*ign3types.Config, error) {
	overwrite := true
	outConfig := ign3types.Config{}
	// Convert data to Ignition resources
	for _, d := range files {
		f := new(fcctbase.File)
		if err := yaml.Unmarshal([]byte(d), f); err != nil {
			return nil, fmt.Errorf("failed to unmarshal file into struct: %v", err)
		}
		f.Overwrite = &overwrite

		// Add the file to the config
		var ctCfg fcctbase.Config
		ctCfg.Storage.Files = append(ctCfg.Storage.Files, *f)
		ign3_0config, tSet, err := ctCfg.ToIgn3_0()
		if err != nil {
			return nil, fmt.Errorf("failed to transpile config to Ignition config %s\nTranslation set: %v", err, tSet)
		}
		ign3_1config := translate3.Translate(ign3_0config)
		outConfig = ign3.Merge(outConfig, ign3_1config)
	}

	for _, d := range units {
		u := new(fcctbase.Unit)
		if err := yaml.Unmarshal([]byte(d), u); err != nil {
			return nil, fmt.Errorf("failed to unmarshal systemd unit into struct: %v", err)
		}

		// Add the unit to the config
		var ctCfg fcctbase.Config
		ctCfg.Systemd.Units = append(ctCfg.Systemd.Units, *u)
		ign3_0config, tSet, err := ctCfg.ToIgn3_0()
		if err != nil {
			return nil, fmt.Errorf("failed to transpile config to Ignition config %s\nTranslation set: %v", err, tSet)
		}
		ign3_1config := translate3.Translate(ign3_0config)
		outConfig = ign3.Merge(outConfig, ign3_1config)
	}

	return &outConfig, nil
}

// MachineConfigFromIgnConfig creates a MachineConfig with the provided Ignition config
func MachineConfigFromIgnConfig(role, name string, ignCfg interface{}) (*mcfgv1.MachineConfig, error) {
	rawIgnCfg, err := json.Marshal(ignCfg)
	if err != nil {
		return nil, fmt.Errorf("error marshalling Ignition config: %v", err)
	}
	return MachineConfigFromRawIgnConfig(role, name, rawIgnCfg)
}

// MachineConfigFromRawIgnConfig creates a MachineConfig with the provided raw Ignition config
func MachineConfigFromRawIgnConfig(role, name string, rawIgnCfg []byte) (*mcfgv1.MachineConfig, error) {
	labels := map[string]string{
		mcfgv1.MachineConfigRoleLabelKey: role,
	}
	return &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Labels: labels,
			Name:   name,
		},
		Spec: mcfgv1.MachineConfigSpec{
			OSImageURL: "",
			Config: runtime.RawExtension{
				Raw: rawIgnCfg,
			},
		},
	}, nil
}

// GetManagedKey returns the managed key for sub-controllers, handling any migration needed
func GetManagedKey(pool *mcfgv1.MachineConfigPool, client mcfgclientset.Interface, prefix, suffix, deprecatedKey string) (string, error) {
	managedKey := fmt.Sprintf("%s-%s-generated-%s", prefix, pool.Name, suffix)
	// if we don't have a client, we're installing brand new, and we don't need to adjust for backward compatibility
	if client == nil {
		return managedKey, nil
	}
	if _, err := client.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), managedKey, metav1.GetOptions{}); err == nil {
		return managedKey, nil
	}
	old, err := client.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), deprecatedKey, metav1.GetOptions{})
	if err != nil && !kerr.IsNotFound(err) {
		return "", fmt.Errorf("could not get MachineConfig %q: %v", deprecatedKey, err)
	}
	// this means no previous CR config were here, so we can start fresh
	if kerr.IsNotFound(err) {
		return managedKey, nil
	}
	// if we're here, we'll grab the old CR config, dupe it and patch its name
	mc, err := MachineConfigFromRawIgnConfig(pool.Name, managedKey, old.Spec.Config.Raw)
	if err != nil {
		return "", err
	}
	_, err = client.MachineconfigurationV1().MachineConfigs().Create(context.TODO(), mc, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}
	err = client.MachineconfigurationV1().MachineConfigs().Delete(context.TODO(), deprecatedKey, metav1.DeleteOptions{})
	return managedKey, err
}
