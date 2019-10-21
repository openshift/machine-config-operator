package containerruntimeconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/BurntSushi/toml"
	"github.com/containers/image/docker/reference"
	"github.com/containers/image/pkg/sysregistriesv2"
	signature "github.com/containers/image/signature"
	storageconfig "github.com/containers/storage/pkg/config"
	igntypes "github.com/coreos/ignition/v2/config/v3_0/types"
	crioconfig "github.com/cri-o/cri-o/pkg/config"
	apicfgv1 "github.com/openshift/api/config/v1"
	apioperatorsv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/runtime-utils/pkg/registries"
	"github.com/vincent-petithory/dataurl"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	minLogSize           = 8192
	minPidsLimit         = 20
	crioConfigPath       = "/etc/crio/crio.conf"
	storageConfigPath    = "/etc/containers/storage.conf"
	registriesConfigPath = "/etc/containers/registries.conf"
	policyConfigPath     = "/etc/containers/policy.json"
)

var errParsingReference = errors.New("error parsing reference of desired image from cluster version config")

// TOML-friendly explicit tables used for conversions.
type tomlConfigStorage struct {
	Storage struct {
		Driver    string                                `toml:"driver"`
		RunRoot   string                                `toml:"runroot"`
		GraphRoot string                                `toml:"graphroot"`
		Options   struct{ storageconfig.OptionsConfig } `toml:"options"`
	} `toml:"storage"`
}

// tomlConfig is another way of looking at a Config, which is
// TOML-friendly (it has all of the explicit tables). It's just used for
// conversions.
type tomlConfigCRIO struct {
	Crio struct {
		crioconfig.RootConfig
		API     struct{ crioconfig.APIConfig }     `toml:"api"`
		Runtime struct{ crioconfig.RuntimeConfig } `toml:"runtime"`
		Image   struct{ crioconfig.ImageConfig }   `toml:"image"`
		Network struct{ crioconfig.NetworkConfig } `toml:"network"`
	} `toml:"crio"`
}

type updateConfigFunc func(data []byte, internal *mcfgv1.ContainerRuntimeConfiguration) ([]byte, error)

// createNewIgnition takes a map where the key is the path of the file, and the value is the
// new data in the form of a byte array. The function returns the ignition config with the
// updated data.
func createNewIgnition(configs map[string][]byte) igntypes.Config {
	tempIgnConfig := ctrlcommon.NewIgnConfig()
	mode := 0644
	overwrite := true
	// Create ignitions
	for filePath, data := range configs {
		// If the file is not included, the data will be nil so skip over
		if data == nil {
			continue
		}
		configdu := dataurl.New(data, "text/plain")
		configdu.Encoding = dataurl.EncodingASCII
		strConfigdu := configdu.String()
		configTempFile := igntypes.File{
			Node: igntypes.Node{
				Path:       filePath,
				Overwrite: &overwrite,
			},
			FileEmbedded1: igntypes.FileEmbedded1{
				Mode: &mode,
				Contents: igntypes.FileContents{
					Source: &strConfigdu,
				},
			},
		}
		tempIgnConfig.Storage.Files = append(tempIgnConfig.Storage.Files, configTempFile)
	}

	return tempIgnConfig
}

func findStorageConfig(mc *mcfgv1.MachineConfig) (*igntypes.File, error) {
	for _, c := range mc.Spec.Config.Storage.Files {
		if c.Path == storageConfigPath {
			c := c
			return &c, nil
		}
	}
	return nil, fmt.Errorf("could not find Storage Config")
}

func findCRIOConfig(mc *mcfgv1.MachineConfig) (*igntypes.File, error) {
	for _, c := range mc.Spec.Config.Storage.Files {
		if c.Path == crioConfigPath {
			c := c
			return &c, nil
		}
	}
	return nil, fmt.Errorf("could not find CRI-O Config")
}

func findRegistriesConfig(mc *mcfgv1.MachineConfig) (*igntypes.File, error) {
	for _, c := range mc.Spec.Config.Storage.Files {
		if c.Path == registriesConfigPath {
			return &c, nil
		}
	}
	return nil, fmt.Errorf("could not find Registries Config")
}

func findPolicyJSON(mc *mcfgv1.MachineConfig) (*igntypes.File, error) {
	for _, c := range mc.Spec.Config.Storage.Files {
		if c.Path == policyConfigPath {
			return &c, nil
		}
	}
	return nil, fmt.Errorf("could not find Policy JSON")
}

func getManagedKeyCtrCfg(pool *mcfgv1.MachineConfigPool) string {
	return fmt.Sprintf("99-%s-%s-containerruntime", pool.Name, pool.ObjectMeta.UID)
}

func getManagedKeyReg(pool *mcfgv1.MachineConfigPool) string {
	return fmt.Sprintf("99-%s-%s-registries", pool.Name, pool.ObjectMeta.UID)
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
			condition.Message = fmt.Sprintf(format, args[:1]...)
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

	if internal.OverlaySize != (resource.Quantity{}) {
		tomlConf.Storage.Options.Size = internal.OverlaySize.String()
	}

	var newData bytes.Buffer
	encoder := toml.NewEncoder(&newData)
	if err := encoder.Encode(*tomlConf); err != nil {
		return nil, err
	}

	return newData.Bytes(), nil
}

// updateCRIOConfig decodes the data rendered from the template, merges the changes in and encodes it
// back into a TOML format. It returns the bytes of the encoded data
func updateCRIOConfig(data []byte, internal *mcfgv1.ContainerRuntimeConfiguration) ([]byte, error) {
	tomlConf := new(tomlConfigCRIO)
	if _, err := toml.DecodeReader(bytes.NewBuffer(data), tomlConf); err != nil {
		return nil, fmt.Errorf("error decoding crio config: %v", err)
	}

	if internal.PidsLimit > 0 {
		tomlConf.Crio.Runtime.PidsLimit = internal.PidsLimit
	}
	if internal.LogSizeMax != (resource.Quantity{}) {
		tomlConf.Crio.Runtime.LogSizeMax = internal.LogSizeMax.Value()
	}
	if internal.LogLevel != "" {
		tomlConf.Crio.Runtime.LogLevel = internal.LogLevel
	}
	// For some reason, when the crio.conf file is created storage_option is not included
	// in the file. Noticed the same thing for all fields in the struct that are of type []string
	// and are empty. This is a dumb hack for now to ensure that cri-o doesn't blow up when
	// overlaySize in storage.conf is set. This field being empty tells cri-o that it should take
	// all its storage configurations from storage.conf instead of crio.conf
	tomlConf.Crio.StorageOptions = []string{}

	var newData bytes.Buffer
	encoder := toml.NewEncoder(&newData)
	if err := encoder.Encode(*tomlConf); err != nil {
		return nil, err
	}

	return newData.Bytes(), nil
}

func updateRegistriesConfig(data []byte, internalInsecure, internalBlocked []string, icspRules []*apioperatorsv1alpha1.ImageContentSourcePolicy) ([]byte, error) {
	tomlConf := sysregistriesv2.V2RegistriesConf{}
	if _, err := toml.Decode(string(data), &tomlConf); err != nil {
		return nil, fmt.Errorf("error unmarshalling registries config: %v", err)
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
	// Note: this is just for testing, the controller does not call this functio till
	// either allowed or blocked registries are configured
	if internalAllowed == nil && internalBlocked == nil {
		return data, nil
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
	if ctrcfg.PidsLimit > 0 && ctrcfg.PidsLimit < minPidsLimit {
		return fmt.Errorf("invalid PidsLimit %q, cannot be less than 20", ctrcfg.PidsLimit)
	}

	if ctrcfg.LogSizeMax.Value() > 0 && ctrcfg.LogSizeMax.Value() <= minLogSize {
		return fmt.Errorf("invalid LogSizeMax %q, cannot be less than 8kB", ctrcfg.LogSizeMax.String())
	}

	if ctrcfg.LogLevel != "" {
		validLogLevels := map[string]bool{
			"error": true,
			"fatal": true,
			"panic": true,
			"warn":  true,
			"info":  true,
			"debug": true,
		}
		if !validLogLevels[ctrcfg.LogLevel] {
			return fmt.Errorf("invalid LogLevel %q, must be one of error, fatal, panic, warn, info, or debug", ctrcfg.LogLevel)
		}
	}

	return nil
}

// getValidBlockedRegistries gets the blocked registries in the image spec and validates that the user is not adding
// the registry being used by the payload to the list of blocked registries.
// If the user is, we drop that registry and continue with syncing the registries.conf with the other registry options
func getValidBlockedRegistries(clusterVersionStatus *apicfgv1.ClusterVersionStatus, imgSpec *apicfgv1.ImageSpec) ([]string, error) {
	if clusterVersionStatus == nil || imgSpec == nil {
		return nil, nil
	}

	var blockedRegs []string

	// Get the registry being used by the payload from the clusterversion config
	ref, err := reference.ParseNamed(clusterVersionStatus.Desired.Image)
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
