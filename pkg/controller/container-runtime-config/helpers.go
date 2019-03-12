package containerruntimeconfig

import (
	"bytes"
	"fmt"
	"reflect"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/BurntSushi/toml"
	"github.com/containers/image/pkg/sysregistriesv2"
	storageconfig "github.com/containers/storage/pkg/config"
	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
	crioconfig "github.com/kubernetes-sigs/cri-o/pkg/config"
	apicfgv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/vincent-petithory/dataurl"

	"k8s.io/api/core/v1"
)

const (
	minLogSize           = 8192
	minPidsLimit         = 20
	crioConfigPath       = "/etc/crio/crio.conf"
	storageConfigPath    = "/etc/containers/storage.conf"
	registriesConfigPath = "/etc/containers/registries.conf"
)

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

type tomlConfigRegistries struct {
	Registries []sysregistriesv2.Registry `toml:"registry"`
	// backwards compatability to sysregistries v1
	sysregistriesv2.V1TOMLConfig `toml:"registries"`
}

type updateConfig func(data []byte, internal *mcfgv1.ContainerRuntimeConfiguration) ([]byte, error)

func createNewCtrRuntimeConfigIgnition(storageTOMLConfig, crioTOMLConfig []byte) ignv2_2types.Config {
	tempIgnConfig := ctrlcommon.NewIgnConfig()
	mode := 0644
	// Create storage.conf ignition
	if storageTOMLConfig != nil {
		storagedu := dataurl.New(storageTOMLConfig, "text/plain")
		storagedu.Encoding = dataurl.EncodingASCII
		storageTempFile := ignv2_2types.File{
			Node: ignv2_2types.Node{
				Filesystem: "root",
				Path:       storageConfigPath,
			},
			FileEmbedded1: ignv2_2types.FileEmbedded1{
				Mode: &mode,
				Contents: ignv2_2types.FileContents{
					Source: storagedu.String(),
				},
			},
		}
		tempIgnConfig.Storage.Files = append(tempIgnConfig.Storage.Files, storageTempFile)
	}

	// Create CRIO ignition
	if crioTOMLConfig != nil {
		criodu := dataurl.New(crioTOMLConfig, "text/plain")
		criodu.Encoding = dataurl.EncodingASCII
		crioTempFile := ignv2_2types.File{
			Node: ignv2_2types.Node{
				Filesystem: "root",
				Path:       crioConfigPath,
			},
			FileEmbedded1: ignv2_2types.FileEmbedded1{
				Mode: &mode,
				Contents: ignv2_2types.FileContents{
					Source: criodu.String(),
				},
			},
		}
		tempIgnConfig.Storage.Files = append(tempIgnConfig.Storage.Files, crioTempFile)
	}

	return tempIgnConfig
}

func createNewRegistriesConfigIgnition(registriesTOMLConfig []byte) ignv2_2types.Config {
	tempIgnConfig := ctrlcommon.NewIgnConfig()
	mode := 0644
	// Create Registries ignition
	if registriesTOMLConfig != nil {
		regdu := dataurl.New(registriesTOMLConfig, "text/plain")
		regdu.Encoding = dataurl.EncodingASCII
		regTempFile := ignv2_2types.File{
			Node: ignv2_2types.Node{
				Filesystem: "root",
				Path:       registriesConfigPath,
			},
			FileEmbedded1: ignv2_2types.FileEmbedded1{
				Mode: &mode,
				Contents: ignv2_2types.FileContents{
					Source: regdu.String(),
				},
			},
		}
		tempIgnConfig.Storage.Files = append(tempIgnConfig.Storage.Files, regTempFile)
	}
	return tempIgnConfig
}

func findStorageConfig(mc *mcfgv1.MachineConfig) (*ignv2_2types.File, error) {
	for _, c := range mc.Spec.Config.Storage.Files {
		if c.Path == storageConfigPath {
			return &c, nil
		}
	}
	return nil, fmt.Errorf("could not find Storage Config")
}

func findCRIOConfig(mc *mcfgv1.MachineConfig) (*ignv2_2types.File, error) {
	for _, c := range mc.Spec.Config.Storage.Files {
		if c.Path == crioConfigPath {
			return &c, nil
		}
	}
	return nil, fmt.Errorf("could not find CRI-O Config")
}

func findRegistriesConfig(mc *mcfgv1.MachineConfig) (*ignv2_2types.File, error) {
	for _, c := range mc.Spec.Config.Storage.Files {
		if c.Path == registriesConfigPath {
			return &c, nil
		}
	}
	return nil, fmt.Errorf("could not find Registries Config")
}

func getManagedKeyCtrCfg(pool *mcfgv1.MachineConfigPool, config *mcfgv1.ContainerRuntimeConfig) string {
	return fmt.Sprintf("99-%s-%s-containerruntime", pool.Name, pool.ObjectMeta.UID)
}

func getManagedKeyReg(pool *mcfgv1.MachineConfigPool, config *apicfgv1.Image) string {
	return fmt.Sprintf("99-%s-%s-registries", pool.Name, pool.ObjectMeta.UID)
}

func wrapErrorWithCondition(err error, args ...interface{}) mcfgv1.ContainerRuntimeConfigCondition {
	var condition *mcfgv1.ContainerRuntimeConfigCondition
	if err != nil {
		condition = mcfgv1.NewContainerRuntimeConfigCondition(
			mcfgv1.ContainerRuntimeConfigFailure,
			v1.ConditionFalse,
			fmt.Sprintf("Error: %v", err),
		)
	} else {
		condition = mcfgv1.NewContainerRuntimeConfigCondition(
			mcfgv1.ContainerRuntimeConfigSuccess,
			v1.ConditionTrue,
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

func updateRegistriesConfig(data []byte, internal apicfgv1.ImageSpec) ([]byte, error) {
	tomlConf := new(tomlConfigRegistries)
	if _, err := toml.Decode(string(data), tomlConf); err != nil {
		return nil, fmt.Errorf("error unmarshalling registries config: %v", err)
	}

	if internal.RegistrySources.InsecureRegistries != nil {
		tomlConf.Insecure = sysregistriesv2.V1TOMLregistries{Registries: internal.RegistrySources.InsecureRegistries}
	}
	if internal.RegistrySources.BlockedRegistries != nil {
		tomlConf.Block = sysregistriesv2.V1TOMLregistries{Registries: internal.RegistrySources.BlockedRegistries}
	}

	var newData bytes.Buffer
	encoder := toml.NewEncoder(&newData)
	if err := encoder.Encode(*tomlConf); err != nil {
		return nil, err
	}

	return newData.Bytes(), nil
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
			"debug": true,
		}
		if !validLogLevels[ctrcfg.LogLevel] {
			return fmt.Errorf("invalid LogLevel %q, must be one of error, fatal, panic, warn, or debug", ctrcfg.LogLevel)
		}
	}

	return nil
}
