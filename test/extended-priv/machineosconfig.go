package extended

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"github.com/tidwall/sjson"
)

type ContainerFile struct {
	ContainerfileArch string `json:"containerfileArch,omitempty"` // TODO: CURRENTLY MCO DOES NOT SUPPORT DIFFERENT ARCHITECTURES, BUT IT WILL
	Content           string `json:"content"`
}

// MachineOSConfig resource type declaration
type MachineOSConfig struct {
	Resource
}

// MachineOSConfigList handles list of MachineOSConfig
type MachineOSConfigList struct {
	ResourceList
}

// MachineOSConfig constructor to get MachineOSConfig resource
func NewMachineOSConfig(oc *exutil.CLI, name string) *MachineOSConfig {
	return &MachineOSConfig{Resource: *NewResource(oc, "machineosconfig", name)}
}

// NewMachineOSConfigList construct a new MachineOSConfig list struct to handle all existing MachineOSConfig
func NewMachineOSConfigList(oc *exutil.CLI) *MachineOSConfigList {
	return &MachineOSConfigList{*NewResourceList(oc, "machineosconfig")}
}

// MOSCCreateOption configures optional behavior for CreateMOSC.
type MOSCCreateOption func(*moscCreateConfig)

type moscCreateConfig struct {
	namespace         string
	containerFiles    []ContainerFile
	defaultPullSecret bool
	expireImage       bool
	useInternal       bool
	useExternal       bool
}

// WithContainerFiles configures the MOSC to use the given containerfiles.
func WithContainerFiles(files []ContainerFile) MOSCCreateOption {
	return func(c *moscCreateConfig) { c.containerFiles = files }
}

// WithDefaultPullSecret configures the MOSC to inherit the global pull secret instead of specifying one.
func WithDefaultPullSecret() MOSCCreateOption {
	return func(c *moscCreateConfig) { c.defaultPullSecret = true }
}

// WithNoImageExpiration configures the MOSC to not add expiration labels to images in external registries.
func WithNoImageExpiration() MOSCCreateOption {
	return func(c *moscCreateConfig) { c.expireImage = false }
}

// WithMOSCNamespace configures a custom namespace for storing the OS image (defaults to MachineConfigNamespace).
func WithMOSCNamespace(ns string) MOSCCreateOption {
	return func(c *moscCreateConfig) { c.namespace = ns }
}

// WithMOSCInternalRegistry forces the MOSC to use the cluster's internal registry.
func WithMOSCInternalRegistry() MOSCCreateOption {
	return func(c *moscCreateConfig) { c.useInternal = true }
}

// WithMOSCExternalRegistry forces the MOSC to use the external quay registry.
func WithMOSCExternalRegistry() MOSCCreateOption {
	return func(c *moscCreateConfig) { c.useExternal = true }
}

// CreateMOSC creates a MachineOSConfig resource using auto-detected or explicitly configured registry.
// By default it auto-selects internal registry if available, otherwise external, with image expiration enabled
// and an explicit pull secret configured.
func CreateMOSC(oc *exutil.CLI, name, pool string, opts ...MOSCCreateOption) (*MachineOSConfig, error) {
	cfg := &moscCreateConfig{
		namespace:   MachineConfigNamespace,
		expireImage: true,
	}
	for _, o := range opts {
		o(cfg)
	}

	if cfg.useInternal {
		return createMOSCUsingInternalRegistry(oc, cfg.namespace, name, pool, cfg.containerFiles, cfg.defaultPullSecret)
	}
	if cfg.useExternal {
		return createMOSCUsingExternalRegistry(oc, name, pool, cfg.containerFiles, cfg.defaultPullSecret, cfg.expireImage)
	}

	if CanUseInternalRegistryToStoreOSImage(oc) {
		return createMOSCUsingInternalRegistry(oc, cfg.namespace, name, pool, cfg.containerFiles, cfg.defaultPullSecret)
	}
	return createMOSCUsingExternalRegistry(oc, name, pool, cfg.containerFiles, cfg.defaultPullSecret, cfg.expireImage)
}

// CreateMachineOSConfig creates a MOSC resource with explicit pull/push secrets and push spec.
// Use this only when you need full control over the MOSC parameters (e.g. testing misconfigured MOSCs).
func CreateMachineOSConfig(oc *exutil.CLI, moscAndMcpName, baseImagePullSecret, renderedImagePushSecret, pushSpec string, containerFile []ContainerFile) (*MachineOSConfig, error) {
	return createMachineOSConfig(oc, moscAndMcpName, &baseImagePullSecret, renderedImagePushSecret, pushSpec, containerFile)
}

func createMachineOSConfigWithDefaultPullSecret(oc *exutil.CLI, moscAndMcpName, renderedImagePushSecret, pushSpec string, containerFile []ContainerFile) (*MachineOSConfig, error) {
	return createMachineOSConfig(oc, moscAndMcpName, nil, renderedImagePushSecret, pushSpec, containerFile)
}

func createMachineOSConfig(oc *exutil.CLI, moscAndMcpName string, baseImagePullSecret *string, renderedImagePushSecret, pushSpec string, containerFile []ContainerFile) (*MachineOSConfig, error) {
	var (
		containerFilesString = "[]"
		moscTemplateFile     = "generic-machine-os-config-nobaseimagepull.yaml"
		parameters           = []string{"-p", "NAME=" + moscAndMcpName, "POOL=" + moscAndMcpName,
			"RENDEREDIMAGEPUSHSECRET=" + renderedImagePushSecret, "PUSHSPEC=" + pushSpec}
	)
	if baseImagePullSecret != nil {
		moscTemplateFile = "generic-machine-os-config.yaml"
		parameters = append(parameters, "BASEIMAGEPULLSECRET="+*baseImagePullSecret)
		logger.Infof("Creating MachineOSConfig %s in pool %s with pullSecret %s pushSecret %s and pushSpec %s", moscAndMcpName, moscAndMcpName, *baseImagePullSecret, renderedImagePushSecret, pushSpec)
	} else {
		logger.Infof("Creating MachineOSConfig %s in pool %s with default pullSecret pushSecret %s and pushSpec %s", moscAndMcpName, moscAndMcpName, renderedImagePushSecret, pushSpec)
	}
	newMOSC := NewMachineOSConfig(oc, moscAndMcpName)

	if len(containerFile) > 0 {
		containerFilesBytes, err := json.Marshal(containerFile)
		if err != nil {
			return newMOSC, err
		}
		containerFilesString = string(containerFilesBytes)
	}

	parameters = append(parameters, "CONTAINERFILE="+containerFilesString)
	logger.Infof("Using custom Containerfile %s", containerFilesString)

	err := NewMCOTemplate(oc, moscTemplateFile).Create(parameters...)
	return newMOSC, err
}

// CopySecretToMCONamespace copy a secret to the MCO namespace and removes the "ownerReferences" and the "annotations" in this secret
func CopySecretToMCONamespace(secret *Secret, newName string) (*Secret, error) {

	mcoResource, err := CloneResource(secret, newName, MachineConfigNamespace,
		func(resString string) (string, error) {
			newResString, err := sjson.Delete(resString, "metadata.ownerReferences")
			if err != nil {
				return "", err
			}
			newResString, err = sjson.Delete(newResString, "metadata.annotations")
			if err != nil {
				return "", err
			}
			return newResString, nil
		})

	if err != nil {
		return nil, err
	}

	return &Secret{Resource: *mcoResource}, nil
}

func createMOSCUsingInternalRegistry(oc *exutil.CLI, namespace, name, pool string, containerFile []ContainerFile, defaultPullSecret bool) (*MachineOSConfig, error) {
	renderedImagePushSecret, err := CreateInternalRegistrySecretFromSA(oc, "builder", namespace, "cloned-push-secret"+exutil.GetRandomString(), MachineConfigNamespace)
	if err != nil {
		return NewMachineOSConfig(oc, name), err
	}
	if !renderedImagePushSecret.Exists() {
		return NewMachineOSConfig(oc, name), fmt.Errorf("rendered image push secret does not exist: %s", renderedImagePushSecret)
	}

	if namespace != MachineConfigNamespace {
		namespacedPullSecret, err := CreateInternalRegistrySecretFromSA(oc, "default", namespace, "cloned-currentpull-secret"+exutil.GetRandomString(), namespace)
		if err != nil {
			return NewMachineOSConfig(oc, name), err
		}
		if !namespacedPullSecret.Exists() {
			return NewMachineOSConfig(oc, name), fmt.Errorf("current image pull secret does not exist: %s", namespacedPullSecret)
		}

		namespacedDockerConfig, err := namespacedPullSecret.GetDataValue(".dockerconfigjson")
		if err != nil {
			return NewMachineOSConfig(oc, name), fmt.Errorf("could not extract dockerConfig from the namespaced pull secret")
		}

		pullSecret := GetPullSecret(oc.AsAdmin())
		pullSecretDockerConfig, err := pullSecret.GetDataValue(".dockerconfigjson")
		if err != nil {
			return NewMachineOSConfig(oc, name), fmt.Errorf("could not extract dockerConfig from the cluster's pull secret")
		}

		mergedDockerConfig, err := MergeDockerConfigs(pullSecretDockerConfig, namespacedDockerConfig)
		if err != nil {
			return NewMachineOSConfig(oc, name), fmt.Errorf("could not merge the namespaced pull-secret dockerconfig and the cluster's pull-secret docker config")
		}

		err = pullSecret.SetDataValue(".dockerconfigjson", mergedDockerConfig)
		if err != nil {
			return NewMachineOSConfig(oc, name), fmt.Errorf("could not configure the cluster's pull-secret with the merged dockerconfig")
		}

		logger.Infof("Waiting for the secret to be updated in all pools")
		NewMachineConfigPoolList(oc.AsAdmin()).waitForComplete()
	}

	pushSpec := fmt.Sprintf("%s/%s/ocb-%s-image:latest", InternalRegistrySvcURL, namespace, pool)

	if !defaultPullSecret {
		pullSecret := NewSecret(oc.AsAdmin(), "openshift-config", "pull-secret")
		baseImagePullSecret, err := CopySecretToMCONamespace(pullSecret, "cloned-basepull-secret-"+exutil.GetRandomString())
		if err != nil {
			return NewMachineOSConfig(oc, name), err
		}
		return CreateMachineOSConfig(oc, name, baseImagePullSecret.GetName(), renderedImagePushSecret.GetName(), pushSpec, containerFile)
	}

	return createMachineOSConfigWithDefaultPullSecret(oc, name, renderedImagePushSecret.GetName(), pushSpec, containerFile)
}

func createMOSCUsingExternalRegistry(oc *exutil.CLI, name, pool string, containerFile []ContainerFile, defaultPullSecret, expireImage bool) (*MachineOSConfig, error) {
	pullSecret := NewSecret(oc.AsAdmin(), "openshift-config", "pull-secret")
	copyPullSecret, err := CopySecretToMCONamespace(pullSecret, "cloned-pull-secret-"+exutil.GetRandomString())
	if err != nil {
		return NewMachineOSConfig(oc, name), err
	}

	clusterName, err := exutil.GetInfraID(oc)
	if err != nil {
		return NewMachineOSConfig(oc, name), err
	}

	pushSpec := fmt.Sprintf("%s:ocb-%s-%s", DefaultLayeringQuayRepository, pool, clusterName)

	configuredContainerFile := []ContainerFile{}
	if expireImage {
		if len(containerFile) == 0 {
			configuredContainerFile = append(configuredContainerFile, ContainerFile{Content: ExpirationDockerfileLabel})
		} else {
			for _, cf := range containerFile {
				configuredContainerFile = append(configuredContainerFile, ContainerFile{Content: cf.Content + "\n" + ExpirationDockerfileLabel, ContainerfileArch: cf.ContainerfileArch})
			}
		}
	} else {
		configuredContainerFile = containerFile
	}

	if defaultPullSecret {
		return createMachineOSConfigWithDefaultPullSecret(oc, name, copyPullSecret.GetName(), pushSpec, configuredContainerFile)
	}

	return CreateMachineOSConfig(oc, name, copyPullSecret.GetName(), copyPullSecret.GetName(), pushSpec, configuredContainerFile)
}

// GetBaseImagePullSecret returns the pull secret configured in this MOSC
func (mosc MachineOSConfig) GetBaseImagePullSecret() (*Secret, error) {
	pullSecretName, err := mosc.Get(`{.spec.baseImagePullSecret.name}`)
	if err != nil {
		return nil, err
	}
	if pullSecretName == "" {
		logger.Warnf("%s has an empty pull secret!! GetBaseImagePullSecret will return nil", mosc)
		return nil, nil
	}

	return NewSecret(mosc.oc, MachineConfigNamespace, pullSecretName), nil
}

// GetRenderedImagePushSecret returns the push secret configured in this MOSC
func (mosc MachineOSConfig) GetRenderedImagePushSecret() (*Secret, error) {
	pushSecretName, err := mosc.Get(`{.spec.renderedImagePushSecret.name}`)
	if err != nil {
		return nil, err
	}
	if pushSecretName == "" {
		logger.Warnf("%s has an empty push secret!! GetRenderedImagePushSecret will return nil", mosc)
		return nil, nil
	}

	return NewSecret(mosc.oc, MachineConfigNamespace, pushSecretName), nil
}

// CleanupAndDelete removes the secrets in the MachineOSConfig resource and the removes the MachoneOSConfig resource itself
func (mosc MachineOSConfig) CleanupAndDelete() error {
	if !mosc.Exists() {
		logger.Infof("%s does not exist. No need to delete it", mosc)
		return nil
	}

	baseImagePullSecret, err := mosc.GetBaseImagePullSecret()
	if err != nil {
		logger.Errorf("Error getting %s in %s. We continue cleaning.", baseImagePullSecret, mosc)
	}
	if baseImagePullSecret == nil {
		logger.Infof("Pull secret is empty in %s, skipping pull secret cleanup", mosc)
	} else {
		err := cleanupMOSCSecret(*baseImagePullSecret)
		if err != nil {
			logger.Errorf("An error happened cleaning %s in %s. We continue cleaning.\nErr:%s", baseImagePullSecret, mosc, err)
		}
	}

	renderedImagePushSecret, err := mosc.GetRenderedImagePushSecret()
	if err != nil {
		logger.Errorf("Error getting %s in %s. We continue cleaning.", renderedImagePushSecret, mosc)
	}

	if renderedImagePushSecret == nil {
		logger.Infof("Push secret is empty in %s, skipping pull secret cleanup", mosc)
	} else {
		cleanupMOSCSecret(*renderedImagePushSecret)
		if err != nil {
			logger.Errorf("An error happened cleaning %s in %s. We continue cleaning.\nErr:%s", renderedImagePushSecret, mosc, err)
		}
	}

	return mosc.Delete()
}

// GetMachineConfigPool returns the MachineConfigPool for this MOSC
func (mosc MachineOSConfig) GetMachineConfigPool() (*MachineConfigPool, error) {
	poolName, err := mosc.Get(`{.spec.machineConfigPool.name}`)
	if err != nil {
		return nil, err
	}
	if poolName == "" {
		logger.Errorf("Empty MachineConfigPool configured in %s", mosc)
		return nil, fmt.Errorf("empty MachineConfigPool configured in %s", mosc)
	}

	return NewMachineConfigPool(mosc.oc, poolName), nil
}

// GetMachineOSBuildList returns a list of all MOSB linked to this MOSC
func (mosc MachineOSConfig) GetMachineOSBuildList() ([]*MachineOSBuild, error) {
	mosbList := NewMachineOSBuildList(mosc.GetOC())
	mosbList.SetItemsFilter(fmt.Sprintf(`?(@.spec.machineOSConfig.name=="%s")`, mosc.GetName()))
	return mosbList.GetAll()
}

// GetCurrentMachineOSBuild returns the MOSB resource that is currently used by this MOSC
func (mosc MachineOSConfig) GetCurrentMachineOSBuild() (*MachineOSBuild, error) {
	mosbName, err := mosc.GetAnnotation("machineconfiguration.openshift.io/current-machine-os-build")
	if err != nil {
		return nil, err
	}

	if mosbName == "" {
		return nil, fmt.Errorf("cannot find the current MOSB for %s. Annotation empty", mosc)
	}

	return NewMachineOSBuild(mosc.GetOC(), mosbName), nil
}

// SetRenderedImagePushspec patches the MOSC resource in order to configure a new renderedImagePushspec
func (mosc MachineOSConfig) SetRenderedImagePushspec(rips string) error {
	escaped, err := json.Marshal(rips)
	if err != nil {
		return err
	}
	return mosc.Patch("json", `[{"op":"replace","path":"/spec/renderedImagePushSpec","value":`+string(escaped)+`}]`)
}

// GetRenderedImagePushspec returns the current valude of renderedImagePushspec
func (mosc MachineOSConfig) GetRenderedImagePushspec() (string, error) {
	return mosc.Get(`{.spec.renderedImagePushSpec}`)
}

// SetContainerfiles sets the container files used by this MOSC
func (mosc MachineOSConfig) SetContainerfiles(containerFiles []ContainerFile) error {
	containerFilesBytes, err := json.Marshal(containerFiles)
	if err != nil {
		return err
	}
	containerFilesString := string(containerFilesBytes)

	return mosc.Patch("json", `[{"op": "replace", "path": "/spec/containerFile", "value":  `+containerFilesString+`}]`)
}

// RemoveContainerfiles removes the container files configured in this MOSC
func (mosc MachineOSConfig) RemoveContainerfiles() error {
	return mosc.SetContainerfiles([]ContainerFile{})
}

// GetStatusCurrentImagePullSpec returns the current image pull spec that is applied and reported in the status
func (mosc MachineOSConfig) GetStatusCurrentImagePullSpec() (string, error) {
	return mosc.Get(`{.status.currentImagePullSpec}`)
}

// Rebuild forces a rebuild of the current image
func (mosc MachineOSConfig) Rebuild() error {
	return mosc.Patch("json", `[{"op": "add", "path": "/metadata/annotations/machineconfiguration.openshift.io~1rebuild", "value":""}]`)
}

// GetAll returns a []*MachineOSConfig list with all existing pinnedimageset sorted by creation timestamp
func (moscl *MachineOSConfigList) GetAll() ([]*MachineOSConfig, error) {
	moscl.ResourceList.SortByTimestamp()
	allMOSCResources, err := moscl.ResourceList.GetAll()
	if err != nil {
		return nil, err
	}
	allMOSCs := make([]*MachineOSConfig, 0, len(allMOSCResources))

	for _, moscRes := range allMOSCResources {
		allMOSCs = append(allMOSCs, NewMachineOSConfig(moscl.oc, moscRes.name))
	}

	return allMOSCs, nil
}

// GetAllOrFail returns a []*MachineOSConfig list with all existing pinnedimageset sorted by creation time, if any error happens it fails the test
func (moscl *MachineOSConfigList) GetAllOrFail() []*MachineOSConfig {
	moscs, err := moscl.GetAll()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error getting the list of existing MachineOSConfig in the cluster")
	return moscs
}

// cleanupMOSCSecret helper function to clean the secrets configured in MachineOSConfig resources
func cleanupMOSCSecret(secret Secret) error {

	originalSecretName, isCanonical := strings.CutSuffix(secret.GetName(), "-canonical")
	if isCanonical {
		originalSecret := NewSecret(secret.GetOC(), secret.GetNamespace(), originalSecretName)
		err := cleanupMOSCSecret(*originalSecret)
		if err != nil {
			logger.Errorf("Errors removing the oringal secre of canonical %s: %s", secret, err)
			return err
		}
	}

	if !secret.Exists() {
		logger.Infof("%s does not exist. Not need to delete it.", secret)
		return nil
	}

	hasOwner, err := secret.HasOwner()
	if err != nil {
		logger.Errorf("There was an error looking for the owner of %s. We will not delete it.\nErr:%s", secret, err)
		return err
	}

	if hasOwner {
		logger.Infof("%s is owned by other resources, skipping deletion", secret)
		return nil
	}

	return secret.Delete()
}

// CreateInternalRegistrySecretFromSA creates a secret containing the credentials to logint to the internal registry as a given service account
func CreateInternalRegistrySecretFromSA(oc *exutil.CLI, saName, saNamespace, secretName, secretNamespace string) (*Secret, error) {
	var (
		tmpDockerConfigFile = generateTmpFile(oc, "tmp-config.json")
		masterNode          = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster).GetNodesOrFail()[0]
	)
	logger.Infof("Create a new secret with the credentials to login to the internal registry using a SA")
	saToken, err := oc.Run("create").Args("-n", saNamespace, "token", saName, "--duration=72h").Output()
	if err != nil {
		logger.Errorf("Error getting token for SA %s", saName)
		return nil, err
	}
	logger.Debugf("SA TOKEN: %s", saToken)

	logger.Infof("Use a master node to login to the internal registry using the new token")
	loginOut, loginErr := masterNode.DebugNodeWithChroot("podman", "login", InternalRegistrySvcURL, "-u", saName, "-p", saToken, "--authfile", tmpDockerConfigFile)
	if loginErr != nil {
		return nil, fmt.Errorf("error trying to login to internal registry:\nOutput:%s\nError:%s", loginOut, loginErr)
	}

	logger.Infof("Copy the docker.json file to local")
	// Because several MOSCs can be applied at the same time, MCDs can be restarted several times and it can cause a failure in the CopyToLocal method. We retry to mitigate this scenario
	err = Retry(5, 5*time.Second, func() error { return masterNode.CopyToLocal(tmpDockerConfigFile, tmpDockerConfigFile) })
	if err != nil {
		return nil, fmt.Errorf("error copying the resulting authorization file to local")
	}

	logger.Infof("OK!")

	logger.Infof("Create the secret with the credentials to push images to the internal registry")
	err = oc.Run("create").Args("secret", "-n", secretNamespace, "docker-registry", secretName, "--from-file=.dockerconfigjson="+tmpDockerConfigFile).Execute()
	if err != nil {
		return nil, err
	}
	logger.Infof("OK!")

	return NewSecret(oc, secretNamespace, secretName), nil
}

// CanUseInternalRegistryToStoreOSImage returns true if the osImage can be stored in the internal registry in this cluster
func CanUseInternalRegistryToStoreOSImage(oc *exutil.CLI) bool {
	var (
		registryConfig = NewResource(oc, "configs.imageregistry.operator.openshift.io", "cluster")
	)

	if !IsCapabilityEnabled(oc.AsAdmin(), "ImageRegistry") {
		logger.Infof("ImageRegistry capability is not enabled. Cannot use the internal registry to store the osImage")
		return false
	}

	// if the configured storage is "emptyDir" we cannot use the internal registry to store the osImage
	// because when the image registry pods are evicted, all images are deleted
	storageConfig := registryConfig.GetOrFail(`{.spec.storage.emptyDir}`)

	return storageConfig == ""
}

func SkipTestIfCannotUseInternalRegistry(oc *exutil.CLI) {
	if !CanUseInternalRegistryToStoreOSImage(oc) {
		g.Skip("The internal registry cannot be used to store the osImage in this cluster. Skipping test case")
	}
}

// IsUsingInternalRegistry checks if the current image is from the internal registry
func (mosc MachineOSConfig) IsUsingInternalRegistry() (bool, error) {
	currentImage, err := mosc.GetStatusCurrentImagePullSpec()
	if err != nil {
		return false, err
	}
	return strings.Contains(currentImage, InternalRegistrySvcURL), nil
}
