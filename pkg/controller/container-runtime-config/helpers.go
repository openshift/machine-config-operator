package containerruntimeconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/containers/image/v5/docker/reference"
	"github.com/containers/image/v5/pkg/sysregistriesv2"
	signature "github.com/containers/image/v5/signature"
	storageconfig "github.com/containers/storage/pkg/config"
	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	"github.com/golang/glog"
	apicfgv1 "github.com/openshift/api/config/v1"
	apioperatorsv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	"github.com/openshift/runtime-utils/pkg/registries"
	"github.com/vincent-petithory/dataurl"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	mcfgclientset "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
)

const (
	minLogSize              = 8192
	minPidsLimit            = 20
	storageConfigPath       = "/etc/containers/storage.conf"
	registriesConfigPath    = "/etc/containers/registries.conf"
	searchRegDropInFilePath = "/etc/containers/registries.conf.d/01-image-searchRegistries.conf"
	policyConfigPath        = "/etc/containers/policy.json"
	// CRIODropInFilePathLogLevel is the path at which changes to the crio config for log-level
	// will be dropped in this is exported so that we can use it in the e2e-tests
	CRIODropInFilePathLogLevel   = "/etc/crio/crio.conf.d/01-ctrcfg-logLevel"
	crioDropInFilePathPidsLimit  = "/etc/crio/crio.conf.d/01-ctrcfg-pidsLimit"
	crioDropInFilePathLogSizeMax = "/etc/crio/crio.conf.d/01-ctrcfg-logSizeMax"
)

var errParsingReference = errors.New("error parsing reference of release image")

// TOML-friendly explicit tables used for conversions.
type tomlConfigStorage struct {
	Storage struct {
		Driver    string                                `toml:"driver"`
		RunRoot   string                                `toml:"runroot"`
		GraphRoot string                                `toml:"graphroot"`
		Options   struct{ storageconfig.OptionsConfig } `toml:"options"`
	} `toml:"storage"`
}

// tomlConfigCRIOLogLevel is used for conversions when log-level is changed
// TOML-friendly (it has all of the explicit tables). It's just used for
// conversions.
// This is only for when the log-level field is changed. We have separated it out into 3 different toml structs
// and 3 different functions because the toml encoder does not ignore a value that was not set - it will set it as
// empty. So if we just change field A and not B, it sets B to empty which is usually different from the default it
// was set to.
type tomlConfigCRIOLogLevel struct {
	Crio struct {
		Runtime struct {
			LogLevel string `toml:"log_level,omitempty"`
		} `toml:"runtime"`
	} `toml:"crio"`
}

// tomlConfigCRIOPidsLimit is used for conversions when pids-limit is changed
// TOML-friendly (it has all of the explicit tables). It's just used for
// conversions.
type tomlConfigCRIOPidsLimit struct {
	Crio struct {
		Runtime struct {
			PidsLimit int64 `toml:"pids_limit,omitempty"`
		} `toml:"runtime"`
	} `toml:"crio"`
}

// tomlConfigCRIOLogSizeMax is used for conversions when log-size-max is changed
// TOML-friendly (it has all of the explicit tables). It's just used for
// conversions.
type tomlConfigCRIOLogSizeMax struct {
	Crio struct {
		Runtime struct {
			LogSizeMax int64 `toml:"log_size_max,omitempty"`
		} `toml:"runtime"`
	} `toml:"crio"`
}

// generatedConfigFile is a struct that holds the filepath and data of the various configs
// Using a struct array ensures that the order of the ignition files always stay the same
// ensuring that double MCs are not created due to a change in the order
type generatedConfigFile struct {
	filePath string
	data     []byte
}

type updateConfigFunc func(data []byte, internal *mcfgv1.ContainerRuntimeConfiguration) ([]byte, error)

// createNewIgnition takes a map where the key is the path of the file, and the value is the
// new data in the form of a byte array. The function returns the ignition config with the
// updated data.
func createNewIgnition(configs []generatedConfigFile) ign3types.Config {
	tempIgnConfig := ctrlcommon.NewIgnConfig()
	mode := 0644
	overwrite := true
	// Create ignitions
	for _, ignConf := range configs {
		// If the file is not included, the data will be nil so skip over
		if ignConf.data == nil {
			continue
		}
		configdu := dataurl.New(ignConf.data, "text/plain")
		configdu.Encoding = dataurl.EncodingASCII
		configduStr := configdu.String()
		configTempFile := ign3types.File{
			Node: ign3types.Node{
				Path:      ignConf.filePath,
				Overwrite: &overwrite,
			},
			FileEmbedded1: ign3types.FileEmbedded1{
				Mode: &mode,
				Contents: ign3types.Resource{
					Source: &(configduStr),
				},
			},
		}
		tempIgnConfig.Storage.Files = append(tempIgnConfig.Storage.Files, configTempFile)
	}

	return tempIgnConfig
}

func findStorageConfig(mc *mcfgv1.MachineConfig) (*ign3types.File, error) {
	ignCfg, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	if err != nil {
		return nil, fmt.Errorf("parsing Storage Ignition config failed with error: %v", err)
	}
	for _, c := range ignCfg.Storage.Files {
		if c.Path == storageConfigPath {
			c := c
			return &c, nil
		}
	}
	return nil, fmt.Errorf("could not find Storage Config")
}

func findRegistriesConfig(mc *mcfgv1.MachineConfig) (*ign3types.File, error) {
	ignCfg, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	if err != nil {
		return nil, fmt.Errorf("parsing Registries Ignition config failed with error: %v", err)
	}
	for _, c := range ignCfg.Storage.Files {
		if c.Path == registriesConfigPath {
			return &c, nil
		}
	}
	return nil, fmt.Errorf("could not find Registries Config")
}

func findPolicyJSON(mc *mcfgv1.MachineConfig) (*ign3types.File, error) {
	ignCfg, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	if err != nil {
		return nil, fmt.Errorf("parsing Policy JSON Ignition config failed with error: %v", err)
	}
	for _, c := range ignCfg.Storage.Files {
		if c.Path == policyConfigPath {
			return &c, nil
		}
	}
	return nil, fmt.Errorf("could not find Policy JSON")
}

// Deprecated: use getManagedKeyCtrCfg
func getManagedKeyCtrCfgDeprecated(pool *mcfgv1.MachineConfigPool) string {
	return fmt.Sprintf("99-%s-%s-containerruntime", pool.Name, pool.ObjectMeta.UID)
}

// nolint: dupl
func getManagedKeyCtrCfg(pool *mcfgv1.MachineConfigPool, client mcfgclientset.Interface, cfg *mcfgv1.ContainerRuntimeConfig) (string, error) {
	// Get all the ctrcfg CRs
	ctrcfgListAll, err := client.MachineconfigurationV1().ContainerRuntimeConfigs().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("error listing container runtime configs: %v", err)
	}
	// If there is no ctrcfg in the list, return the default MC name with no suffix
	if ctrcfgListAll == nil || len(ctrcfgListAll.Items) == 0 {
		return ctrlcommon.GetManagedKey(pool, client, "99", "containerruntime", getManagedKeyCtrCfgDeprecated(pool))
	}

	var ctrcfgList []mcfgv1.ContainerRuntimeConfig
	for _, ctrcfg := range ctrcfgListAll.Items {
		selector, err := metav1.LabelSelectorAsSelector(ctrcfg.Spec.MachineConfigPoolSelector)
		if err != nil {
			return "", fmt.Errorf("invalid label selector: %v", err)
		}
		if selector.Empty() || !selector.Matches(labels.Set(pool.Labels)) {
			continue
		}
		ctrcfgList = append(ctrcfgList, ctrcfg)
	}

	for _, ctrcfg := range ctrcfgList {
		if ctrcfg.Name != cfg.Name {
			continue
		}
		val, ok := ctrcfg.GetAnnotations()[ctrlcommon.MCNameSuffixAnnotationKey]
		// If we find a matching ctrcfg and it is the only one in the list, then return the default MC name with no suffix
		if !ok && len(ctrcfgList) < 2 {
			return ctrlcommon.GetManagedKey(pool, client, "99", "containerruntime", getManagedKeyCtrCfgDeprecated(pool))
		}
		// Otherwise if an MC name suffix exists, append it to the default MC name and return that as this ctrcfg exists and
		// we are probably doing an update action on it
		if val != "" {
			return fmt.Sprintf("99-%s-generated-containerruntime-%s", pool.Name, val), nil
		}
	}

	// If we are here, this means that a new ctrcfg was created, so we have to calculate the suffix value for its MC name
	suffixNum := 0
	// Go through the list of ctrcfg objects created and get the max suffix value currently created
	for _, item := range ctrcfgList {
		val, ok := item.GetAnnotations()[ctrlcommon.MCNameSuffixAnnotationKey]
		if ok {
			// Convert the suffix value to int so we can look through the list and grab the max suffix created so far
			intVal, err := strconv.Atoi(val)
			if err != nil {
				return "", fmt.Errorf("error converting %s to int: %v", val, err)
			}
			if intVal > suffixNum {
				suffixNum = intVal
			}
		}
	}
	// The max suffix value that we can go till with this logic is 9 - this means that a user can create up to 10 different ctrcfg CRs.
	// However, if there is a ctrcfg-1 mapping to mc-1 and ctrcfg-2 mapping to mc-2 and the user deletes ctrcfg-1, it will delete mc-1 but
	// then if the user creates a ctrcfg-new it will map to mc-3. This is what we want as the latest ctrcfg created should be higher in priority
	// so that those changes can be rolled out to the nodes. But users will have to be mindful of how many ctrcfg CRs they create. Don't think
	// anyone should ever have the need to create 10 when they can simply update an existing ctrcfg unless it is to apply to another pool.
	if suffixNum+1 > ctrlcommon.MaxMCNameSuffix {
		return "", fmt.Errorf("max number of supported ctrcfgs (10) has been reached. Please delete old ctrcfgs before retrying")
	}
	// Return the default MC name with the suffixNum+1 value appended to it
	return fmt.Sprintf("99-%s-generated-containerruntime-%s", pool.Name, strconv.Itoa(suffixNum+1)), nil
}

// Deprecated: use getManagedKeyReg
func getManagedKeyRegDeprecated(pool *mcfgv1.MachineConfigPool) string {
	return fmt.Sprintf("99-%s-%s-registries", pool.Name, pool.ObjectMeta.UID)
}

func getManagedKeyReg(pool *mcfgv1.MachineConfigPool, client mcfgclientset.Interface) (string, error) {
	return ctrlcommon.GetManagedKey(pool, client, "99", "registries", getManagedKeyRegDeprecated(pool))
}

func wrapErrorWithCondition(err error, args ...interface{}) mcfgv1.ContainerRuntimeConfigCondition {
	var condition *mcfgv1.ContainerRuntimeConfigCondition
	if err != nil {
		condition = mcfgv1.NewContainerRuntimeConfigCondition(
			mcfgv1.ContainerRuntimeConfigFailure,
			corev1.ConditionFalse,
			fmt.Sprintf("Error: %v", err),
		)
	} else {
		condition = mcfgv1.NewContainerRuntimeConfigCondition(
			mcfgv1.ContainerRuntimeConfigSuccess,
			corev1.ConditionTrue,
			"Success",
		)
	}
	if len(args) > 0 {
		format, ok := args[0].(string)
		if ok {
			condition.Message = fmt.Sprintf(format, args[1:]...)
		}
	}
	return *condition
}

// updateStorageConfig decodes the data rendered from the template, merges the changes in and encodes it
// back into a TOML format. It returns the bytes of the encoded data
func updateStorageConfig(data []byte, internal *mcfgv1.ContainerRuntimeConfiguration) ([]byte, error) {
	tomlConf := new(tomlConfigStorage)
	if _, err := toml.DecodeReader(bytes.NewBuffer(data), tomlConf); err != nil {
		return nil, fmt.Errorf("error decoding crio config: %v", err)
	}

	if internal.OverlaySize.Value() < 0 {
		return nil, fmt.Errorf("invalid overlaySize config %q: the overlaySize should be larger than 0", internal.OverlaySize.String())
	}

	if internal.OverlaySize.Value() != 0 {
		tomlConf.Storage.Options.Size = internal.OverlaySize.String()
	}

	var newData bytes.Buffer
	encoder := toml.NewEncoder(&newData)
	if err := encoder.Encode(*tomlConf); err != nil {
		return nil, err
	}

	return newData.Bytes(), nil
}

func addTOMLgeneratedConfigFile(configFileList []generatedConfigFile, path string, tomlConf interface{}) ([]generatedConfigFile, error) {
	var newData bytes.Buffer
	encoder := toml.NewEncoder(&newData)
	if err := encoder.Encode(tomlConf); err != nil {
		return nil, fmt.Errorf("error encoding toml for CRIO drop-in files: %v", err)
	}
	configFileList = append(configFileList, generatedConfigFile{filePath: path, data: newData.Bytes()})
	return configFileList, nil
}

// createCRIODropinFiles gets the data from the CRD and creates the respective drop in file in /etc/crio/crio.conf.d
// We create different drop-in files for each CRI-O field that can be changed by the ctrcfg CR
// this ensures that we don't have to rely on hard coded defaults that might cause problems
// in future if something in cri-o or the templates used by the MCO changes
func createCRIODropinFiles(cfg *mcfgv1.ContainerRuntimeConfig) []generatedConfigFile {
	var (
		generatedConfigFileList []generatedConfigFile
		err                     error
	)
	ctrcfg := cfg.Spec.ContainerRuntimeConfig
	if ctrcfg.LogLevel != "" {
		tomlConf := tomlConfigCRIOLogLevel{}
		tomlConf.Crio.Runtime.LogLevel = ctrcfg.LogLevel
		generatedConfigFileList, err = addTOMLgeneratedConfigFile(generatedConfigFileList, CRIODropInFilePathLogLevel, tomlConf)
		if err != nil {
			glog.V(2).Infoln(cfg, err, "error updating user changes for log-level to crio.conf.d: %v", err)
		}
	}
	if ctrcfg.PidsLimit != nil {
		tomlConf := tomlConfigCRIOPidsLimit{}
		tomlConf.Crio.Runtime.PidsLimit = *ctrcfg.PidsLimit
		generatedConfigFileList, err = addTOMLgeneratedConfigFile(generatedConfigFileList, crioDropInFilePathPidsLimit, tomlConf)
		if err != nil {
			glog.V(2).Infoln(cfg, err, "error updating user changes for pids-limit to crio.conf.d: %v", err)
		}
	}
	if ctrcfg.LogSizeMax.Value() != 0 {
		tomlConf := tomlConfigCRIOLogSizeMax{}
		tomlConf.Crio.Runtime.LogSizeMax = ctrcfg.LogSizeMax.Value()
		generatedConfigFileList, err = addTOMLgeneratedConfigFile(generatedConfigFileList, crioDropInFilePathLogSizeMax, tomlConf)
		if err != nil {
			glog.V(2).Infoln(cfg, err, "error updating user changes for log-size-max to crio.conf.d: %v", err)
		}
	}
	return generatedConfigFileList
}

// updateSearchRegistriesConfig gets the ContainerRuntimeSearchRegistries data from the Image CRD
// and creates a drop-in file for it at /etc/containers/registries.conf.d
func updateSearchRegistriesConfig(searchRegs []string) []generatedConfigFile {
	var (
		generatedConfigFileList []generatedConfigFile
		err                     error
	)
	tomlConf := sysregistriesv2.V2RegistriesConf{}
	tomlConf.UnqualifiedSearchRegistries = searchRegs
	generatedConfigFileList, err = addTOMLgeneratedConfigFile(generatedConfigFileList, searchRegDropInFilePath, tomlConf)
	if err != nil {
		glog.Warningln("error updating user changes for containerRuntimeSearchRegistries to registries.conf.d: ", err)
	}
	return generatedConfigFileList
}

func updateRegistriesConfig(data []byte, internalInsecure, internalBlocked []string, icspRules []*apioperatorsv1alpha1.ImageContentSourcePolicy) ([]byte, error) {
	tomlConf := sysregistriesv2.V2RegistriesConf{}
	if _, err := toml.Decode(string(data), &tomlConf); err != nil {
		return nil, fmt.Errorf("error unmarshalling registries config: %v", err)
	}

	if err := validateRegistriesConfScopes(internalInsecure, internalBlocked, []string{}, icspRules); err != nil {
		return nil, err
	}

	if err := registries.EditRegistriesConfig(&tomlConf, internalInsecure, internalBlocked, icspRules); err != nil {
		return nil, err
	}

	var newData bytes.Buffer
	encoder := toml.NewEncoder(&newData)
	if err := encoder.Encode(tomlConf); err != nil {
		return nil, err
	}
	return newData.Bytes(), nil
}

// updatePolicyJSON decodes the data rendered from the template, merges the changes in and encodes it
// back into a JSON format. It returns the bytes of the encoded data
// It also returns an error if both allowed and blocked registries are set
// WARNING: This can not safely edit policy files with arbitrary complexity, especially files which include signedBy
// requirements. It expects the input policy to be generated by templates in this project.
func updatePolicyJSON(data []byte, internalBlocked, internalAllowed []string) ([]byte, error) {
	if len(internalAllowed) != 0 && len(internalBlocked) != 0 {
		return nil, fmt.Errorf("invalid images config: only one of AllowedRegistries or BlockedRegistries may be specified")
	}
	// Return original data if neither allowed or blocked registries are configured
	// Note: this is just for testing, the controller does not call this function till
	// either allowed or blocked registries are configured
	if internalAllowed == nil && internalBlocked == nil {
		return data, nil
	}

	if err := validateRegistriesConfScopes([]string{}, internalBlocked, internalAllowed, nil); err != nil {
		return nil, err
	}

	policyObj := &signature.Policy{}
	decoder := json.NewDecoder(bytes.NewBuffer(data))
	err := decoder.Decode(policyObj)
	if err != nil {
		return nil, fmt.Errorf("error decoding policy json: %v", err)
	}
	transportScopes := make(signature.PolicyTransportScopes)
	if len(internalAllowed) > 0 {
		policyObj.Default = signature.PolicyRequirements{
			signature.NewPRReject(),
		}
		for _, reg := range internalAllowed {
			transportScopes[reg] = signature.PolicyRequirements{
				signature.NewPRInsecureAcceptAnything(),
			}
		}
	}
	if len(internalBlocked) > 0 {
		policyObj.Default = signature.PolicyRequirements{
			signature.NewPRInsecureAcceptAnything(),
		}
		for _, reg := range internalBlocked {
			transportScopes[reg] = signature.PolicyRequirements{
				signature.NewPRReject(),
			}
		}
	}

	policyObj.Transports["atomic"] = transportScopes
	policyObj.Transports["docker"] = transportScopes

	policyJSON, err := json.Marshal(policyObj)
	if err != nil {
		return nil, err
	}
	return policyJSON, nil
}

// validateUserContainerRuntimeConfig ensures that the values set by the user are valid
func validateUserContainerRuntimeConfig(cfg *mcfgv1.ContainerRuntimeConfig) error {
	if cfg.Spec.ContainerRuntimeConfig == nil {
		return nil
	}
	ctrcfgValues := reflect.ValueOf(*cfg.Spec.ContainerRuntimeConfig)
	if !ctrcfgValues.IsValid() {
		return fmt.Errorf("containerRuntimeConfig is not valid")
	}

	ctrcfg := cfg.Spec.ContainerRuntimeConfig
	if ctrcfg.PidsLimit != nil && *ctrcfg.PidsLimit != 0 && *ctrcfg.PidsLimit < minPidsLimit {
		return fmt.Errorf("invalid PidsLimit %v", *ctrcfg.PidsLimit)
	}

	if ctrcfg.LogSizeMax.Value() > 0 && ctrcfg.LogSizeMax.Value() <= minLogSize {
		return fmt.Errorf("invalid LogSizeMax %q, cannot be less than 8kB", ctrcfg.LogSizeMax.String())
	}

	if ctrcfg.OverlaySize.Value() < 0 {
		return fmt.Errorf("invalid overlaySize %q, cannot be less than 0", ctrcfg.OverlaySize.String())
	}

	if ctrcfg.LogLevel != "" {
		validLogLevels := map[string]bool{
			"error": true,
			"fatal": true,
			"panic": true,
			"warn":  true,
			"info":  true,
			"debug": true,
			"trace": true,
		}
		if !validLogLevels[ctrcfg.LogLevel] {
			return fmt.Errorf("invalid LogLevel %q, must be one of error, fatal, panic, warn, info, debug, or trace", ctrcfg.LogLevel)
		}
	}

	return nil
}

// getValidBlockedRegistries gets the blocked registries in the image spec and validates that the user is not adding
// the registry being used by the payload to the list of blocked registries.
// If the user is, we drop that registry and continue with syncing the registries.conf with the other registry options
func getValidBlockedRegistries(releaseImage string, imgSpec *apicfgv1.ImageSpec) ([]string, error) {
	if imgSpec == nil {
		return nil, nil
	}

	var blockedRegs []string

	// Get the registry being used by the payload from the releaseImage
	ref, err := reference.ParseNamed(releaseImage)
	if err != nil {
		return nil, errParsingReference
	}
	payloadReg := reference.Domain(ref)
	for i, reg := range imgSpec.RegistrySources.BlockedRegistries {
		// if there is a match, return all the blocked registries except the one that matched and return an error as well
		if reg == payloadReg {
			blockedRegs = append(blockedRegs, imgSpec.RegistrySources.BlockedRegistries[i+1:]...)
			return blockedRegs, fmt.Errorf("error adding %q to blocked registries, cannot block the registry being used by the payload", payloadReg)
		}
		// Was not a match to the registry being used by the payload, so add to valid blocked registries
		blockedRegs = append(blockedRegs, reg)
	}
	return blockedRegs, nil
}

func validateRegistriesConfScopes(insecure, blocked, allowed []string, icspRules []*apioperatorsv1alpha1.ImageContentSourcePolicy) error {
	for _, scope := range insecure {
		if !registries.IsValidRegistriesConfScope(scope) {
			return fmt.Errorf("invalid entry for insecure registries %q", scope)
		}
	}

	for _, scope := range blocked {
		if !registries.IsValidRegistriesConfScope(scope) {
			return fmt.Errorf("invalid entry for blocked registries %q", scope)
		}
	}

	for _, scope := range allowed {
		if !registries.IsValidRegistriesConfScope(scope) {
			return fmt.Errorf("invalid entry for allowed registries %q", scope)
		}
	}

	for _, icsp := range icspRules {
		for _, mirrorSet := range icsp.Spec.RepositoryDigestMirrors {
			if mirrorSet.Source == "" {
				return fmt.Errorf("invalid empty entry for source configuration")
			}
			if strings.Contains(mirrorSet.Source, "*") {
				return fmt.Errorf("wildcard entries are not supported with mirror configuration %q", mirrorSet.Source)
			}
			for _, mirror := range mirrorSet.Mirrors {
				if mirror == "" {
					return fmt.Errorf("invalid empty entry for mirror configuration")
				}
				if strings.Contains(mirror, "*") {
					return fmt.Errorf("wildcard entries are not supported with mirror configuration %q", mirror)
				}
			}
		}

	}
	return nil
}
