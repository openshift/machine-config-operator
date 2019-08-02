package containerruntimeconfig

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/containers/image/docker/reference"
	"github.com/containers/image/pkg/sysregistriesv2"
	storageconfig "github.com/containers/storage/pkg/config"
	igntypes "gopkg.in/coreos/ignition.v0/config/v2_2/types"
	crioconfig "github.com/kubernetes-sigs/cri-o/pkg/config"
	apicfgv1 "github.com/openshift/api/config/v1"
	apioperatorsv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
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

func createNewCtrRuntimeConfigIgnition(storageTOMLConfig, crioTOMLConfig []byte) igntypes.Config {
	tempIgnConfig := ctrlcommon.NewIgnConfig()
	mode := 0644
	// Create storage.conf ignition
	if storageTOMLConfig != nil {
		storagedu := dataurl.New(storageTOMLConfig, "text/plain")
		storagedu.Encoding = dataurl.EncodingASCII
		storageTempFile := igntypes.File{
			Node: igntypes.Node{
				Filesystem: "root",
				Path:       storageConfigPath,
			},
			FileEmbedded1: igntypes.FileEmbedded1{
				Mode: &mode,
				Contents: igntypes.FileContents{
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
		crioTempFile := igntypes.File{
			Node: igntypes.Node{
				Filesystem: "root",
				Path:       crioConfigPath,
			},
			FileEmbedded1: igntypes.FileEmbedded1{
				Mode: &mode,
				Contents: igntypes.FileContents{
					Source: criodu.String(),
				},
			},
		}
		tempIgnConfig.Storage.Files = append(tempIgnConfig.Storage.Files, crioTempFile)
	}

	return tempIgnConfig
}

func createNewRegistriesConfigIgnition(registriesTOMLConfig []byte) igntypes.Config {
	tempIgnConfig := ctrlcommon.NewIgnConfig()
	mode := 0644
	// Create Registries ignition
	if registriesTOMLConfig != nil {
		regdu := dataurl.New(registriesTOMLConfig, "text/plain")
		regdu.Encoding = dataurl.EncodingASCII
		regTempFile := igntypes.File{
			Node: igntypes.Node{
				Filesystem: "root",
				Path:       registriesConfigPath,
			},
			FileEmbedded1: igntypes.FileEmbedded1{
				Mode: &mode,
				Contents: igntypes.FileContents{
					Source: regdu.String(),
				},
			},
		}
		tempIgnConfig.Storage.Files = append(tempIgnConfig.Storage.Files, regTempFile)
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

// scopeMatchesRegistry returns true if a scope value (as in sysregistriesv2.Registry.Prefix / sysregistriesv2.Endpoint.Location)
// matches a host[:port] value in reg.
func scopeMatchesRegistry(scope, reg string) bool {
	if reg == scope {
		return true
	}
	if len(scope) > len(reg) {
		return strings.HasPrefix(scope, reg) && scope[len(reg)] == '/'
	}
	return false
}

// mergedMirrorSets processes icspRules and returns a set of RepositoryDigestMirrors, one for each Source value,
// ordered consistently with the preference order of the individual entries (if possible)
// E.g. given mirror sets (B, C) and (A, B), it will combine them into a single (A, B, C) set.
func mergedMirrorSets(icspRules []*apioperatorsv1alpha1.ImageContentSourcePolicy) ([]apioperatorsv1alpha1.RepositoryDigestMirrors, error) {
	disjointSets := map[string]*[]*apioperatorsv1alpha1.RepositoryDigestMirrors{} // Key == Source
	for _, icsp := range icspRules {
		for i := range icsp.Spec.RepositoryDigestMirrors {
			set := &icsp.Spec.RepositoryDigestMirrors[i]
			if len(set.Mirrors) == 0 {
				continue // No mirrors is not really a mirror set.
			}
			ds, ok := disjointSets[set.Source]
			if !ok {
				ds = &[]*apioperatorsv1alpha1.RepositoryDigestMirrors{}
				disjointSets[set.Source] = ds
			}
			*ds = append(*ds, set)
		}
	}

	// Sort the sets of mirrors by Source to ensure deterministic output
	sources := []string{}
	for key := range disjointSets {
		sources = append(sources, key)
	}
	sort.Strings(sources)
	// Convert the sets of mirrors
	res := []apioperatorsv1alpha1.RepositoryDigestMirrors{}
	for _, source := range sources {
		ds := disjointSets[source]
		topoGraph := newTopoGraph()
		for _, set := range *ds {
			for i := 0; i+1 < len(set.Mirrors); i++ {
				topoGraph.AddEdge(set.Mirrors[i], set.Mirrors[i+1])
			}
			sourceInGraph := false
			for _, m := range set.Mirrors {
				if m == source {
					sourceInGraph = true
					break
				}
			}
			if !sourceInGraph {
				// The build of mirrorSets guarantees len(set.Mirrors) > 0.
				topoGraph.AddEdge(set.Mirrors[len(set.Mirrors)-1], source)
			}
			// Every node in topoGraph, including source, is implicitly added by topoGraph.AddEdge (there are no unconnected nodes that we would
			// have to add separately from the edges).
		}
		sortedRepos, err := topoGraph.Sorted()
		if err != nil {
			return nil, err
		}
		if sortedRepos[len(sortedRepos)-1] == source {
			// We don't need to explicitly include source in the list, it will be automatically tried last per the semantics of sysregistriesv2. Mirrors.
			sortedRepos = sortedRepos[:len(sortedRepos)-1]
		}
		res = append(res, apioperatorsv1alpha1.RepositoryDigestMirrors{
			Source:  source,
			Mirrors: sortedRepos,
		})
	}
	return res, nil
}

func updateRegistriesConfig(data []byte, internalInsecure, internalBlocked []string, icspRules []*apioperatorsv1alpha1.ImageContentSourcePolicy) ([]byte, error) {
	tomlConf := sysregistriesv2.V2RegistriesConf{}
	if _, err := toml.Decode(string(data), &tomlConf); err != nil {
		return nil, fmt.Errorf("error unmarshalling registries config: %v", err)
	}

	// getRegistryEntry returns a pointer to a modifiable Registry object corresponding to scope,
	// creating it if necessary.
	// NOTE: We never generate entries with Prefix != Location, so everything in updateRegistriesConfig
	// only checks Location.
	// NOTE: The pointer is valid only until the next getRegistryEntry call.
	getRegistryEntry := func(scope string) *sysregistriesv2.Registry {
		for i := range tomlConf.Registries {
			if tomlConf.Registries[i].Location == scope {
				return &tomlConf.Registries[i]
			}
		}
		tomlConf.Registries = append(tomlConf.Registries, sysregistriesv2.Registry{
			Endpoint: sysregistriesv2.Endpoint{Location: scope},
		})
		return &tomlConf.Registries[len(tomlConf.Registries)-1]
	}

	mirrorSets, err := mergedMirrorSets(icspRules)
	if err != nil {
		return nil, err
	}
	for _, mirrorSet := range mirrorSets {
		reg := getRegistryEntry(mirrorSet.Source)
		reg.MirrorByDigestOnly = true
		for _, mirror := range mirrorSet.Mirrors {
			reg.Mirrors = append(reg.Mirrors, sysregistriesv2.Endpoint{Location: mirror})
		}
	}

	// internalInsecure and internalBlocked are lists of registries; now that mirrors can be configured at a namespace/repo level,
	// configuration at the namespace/repo level would shadow the registry-level entries; so, propagate the insecure/blocked
	// flags to the child namespaces as well.
	for _, insecureReg := range internalInsecure {
		reg := getRegistryEntry(insecureReg)
		reg.Insecure = true
		for i := range tomlConf.Registries {
			reg := &tomlConf.Registries[i]
			if scopeMatchesRegistry(reg.Location, insecureReg) {
				reg.Insecure = true
			}
			for j := range reg.Mirrors {
				mirror := &reg.Mirrors[j]
				if scopeMatchesRegistry(mirror.Location, insecureReg) {
					mirror.Insecure = true
				}
			}
		}
	}
	for _, blockedReg := range internalBlocked {
		reg := getRegistryEntry(blockedReg)
		reg.Blocked = true
		for i := range tomlConf.Registries {
			reg := &tomlConf.Registries[i]
			if scopeMatchesRegistry(reg.Location, blockedReg) {
				reg.Blocked = true
			}
		}
	}

	var newData bytes.Buffer
	encoder := toml.NewEncoder(&newData)
	if err := encoder.Encode(tomlConf); err != nil {
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
			"info":  true,
			"debug": true,
		}
		if !validLogLevels[ctrcfg.LogLevel] {
			return fmt.Errorf("invalid LogLevel %q, must be one of error, fatal, panic, warn, info, or debug", ctrcfg.LogLevel)
		}
	}

	return nil
}

// getValidRegistries gets the insecure and blocked registries in the image spec and validates that the user is not adding
// the registry being used by the payload to the list of blocked registries.
// If the user is, we drop that registry and continue with syncing the registries.conf with the other registry options
func getValidRegistries(clusterVersionStatus *apicfgv1.ClusterVersionStatus, imgSpec *apicfgv1.ImageSpec) ([]string, []string, error) {
	if clusterVersionStatus == nil || imgSpec == nil {
		return nil, nil, nil
	}

	var blockedRegs []string
	// Copy the insecure registries from the spec
	insecureRegs := imgSpec.RegistrySources.InsecureRegistries

	// Get the registry being used by the payload from the clusterversion config
	ref, err := reference.ParseNamed(clusterVersionStatus.Desired.Image)
	if err != nil {
		return nil, nil, errParsingReference
	}
	payloadReg := reference.Domain(ref)
	for i, reg := range imgSpec.RegistrySources.BlockedRegistries {
		// if there is a match, return all the blocked registries except the one that matched and return an error as well
		if reg == payloadReg {
			blockedRegs = append(blockedRegs, imgSpec.RegistrySources.BlockedRegistries[i+1:]...)
			return insecureRegs, blockedRegs, fmt.Errorf("error adding %q to blocked registries, cannot block the registry being used by the payload", payloadReg)
		}
		// Was not a match to the registry being used by the payload, so add to valid blocked registries
		blockedRegs = append(blockedRegs, reg)
	}
	return insecureRegs, blockedRegs, nil
}
