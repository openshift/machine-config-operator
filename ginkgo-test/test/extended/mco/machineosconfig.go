package mco

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util"
	logger "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/logext"
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

// CreateMachineOSConfig creates a MOSC resource using the information provided in the arguments
func CreateMachineOSConfig(oc *exutil.CLI, name, pool, currentImagePullSecret, baseImagePullSecret, renderedImagePushSecret, pushSpec string, containerFile []ContainerFile) (*MachineOSConfig, error) {
	var (
		containerFilesString = "[]"
	)
	logger.Infof("Creating MachineOSConfig %s in pool %s with pullSecret %s pushSecret %s and pushSpec %s", name, pool, baseImagePullSecret, renderedImagePushSecret, pushSpec)
	newMOSC := NewMachineOSConfig(oc, name)

	if len(containerFile) > 0 {
		containerFilesBytes, err := json.Marshal(containerFile)
		if err != nil {
			return newMOSC, err
		}
		containerFilesString = string(containerFilesBytes)
	}
	logger.Infof("Using custom Containerfile %s", containerFilesString)

	err := NewMCOTemplate(oc, "generic-machine-os-config.yaml").Create("-p", "NAME="+name, "POOL="+pool, "CURRENTIMAGEPULLSECRET="+currentImagePullSecret, "BASEIMAGEPULLSECRET="+baseImagePullSecret,
		"RENDEREDIMAGEPUSHSECRET="+renderedImagePushSecret, "PUSHSPEC="+pushSpec, "CONTAINERFILE="+containerFilesString)
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

func CreateMachineOSConfigUsingInternalRegistry(oc *exutil.CLI, namespace, name, pool string, containerFile []ContainerFile) (*MachineOSConfig, error) {
	// We use a copy of the cluster's pull secret to pull the images
	pullSecret := NewSecret(oc.AsAdmin(), "openshift-config", "pull-secret")
	baseImagePullSecret, err := CopySecretToMCONamespace(pullSecret, "cloned-pull-secret-"+exutil.GetRandomString())
	if err != nil {
		return NewMachineOSConfig(oc, name), err
	}

	// We use the builder SA secret in the namespace to push the images to the internal registry
	renderedImagePushSecret, err := CreateInternalRegistrySecretFromSA(oc, "builder", namespace, "cloned-push-secret"+exutil.GetRandomString(), MachineConfigNamespace)
	if err != nil {
		return NewMachineOSConfig(oc, name), err
	}
	if !renderedImagePushSecret.Exists() {
		return NewMachineOSConfig(oc, name), fmt.Errorf("Rendered image push secret does not exist: %s", renderedImagePushSecret)
	}

	// We use the default SA secret in MCO to pull the current image from the internal registry
	saDefault := NewNamespacedResource(oc, "sa", namespace, "default")
	currentImagePullSecret := NewSecret(oc, namespace, saDefault.GetOrFail(`{.secrets[0].name}`))
	if !currentImagePullSecret.Exists() {
		return NewMachineOSConfig(oc, name), fmt.Errorf("Current image pull secret does not exist: %s", currentImagePullSecret)
	}

	if namespace != MachineConfigNamespace { // If the secret is not in MCO, we copy it there
		currentImagePullSecret, err = CopySecretToMCONamespace(currentImagePullSecret, currentImagePullSecret.GetName()+"-testcloned")
		if err != nil {
			return NewMachineOSConfig(oc, name), err
		}
	}

	// We use a push spec stored in the internal registry in the MCO namespace. We use a different image for every pool
	pushSpec := fmt.Sprintf("%s/%s/ocb-%s-image:latest", InternalRegistrySvcURL, namespace, pool)

	return CreateMachineOSConfig(oc, name, pool, currentImagePullSecret.GetName(), baseImagePullSecret.GetName(), renderedImagePushSecret.GetName(), pushSpec, containerFile)
}

// GetBaseImagePullSecret returns the pull secret configured in this MOSC
func (mosc MachineOSConfig) GetBaseImagePullSecret() (*Secret, error) {
	pullSecretName, err := mosc.Get(`{.spec.buildInputs.baseImagePullSecret.name}`)
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
	pushSecretName, err := mosc.Get(`{.spec.buildInputs.renderedImagePushSecret.name}`)
	if err != nil {
		return nil, err
	}
	if pushSecretName == "" {
		logger.Warnf("%s has an empty push secret!! GetRenderedImagePushSecret will return nil", mosc)
		return nil, nil
	}

	return NewSecret(mosc.oc, MachineConfigNamespace, pushSecretName), nil
}

// GetCurrentImagePullSecret returns the pull secret that will be used in the nodes to pull the image when applying it
func (mosc MachineOSConfig) GetCurrentImagePullSecret() (*Secret, error) {
	currentImagePullSecretName, err := mosc.Get(`{.spec.buildOutputs.currentImagePullSecret.name}`)
	if err != nil {
		return nil, err
	}
	if currentImagePullSecretName == "" {
		logger.Warnf("%s has an empty push secret!! GetCurrentImagePullSecret will return nil", mosc)
		return nil, nil
	}

	return NewSecret(mosc.oc, MachineConfigNamespace, currentImagePullSecretName), nil
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

	currentImagePullSecret, err := mosc.GetCurrentImagePullSecret()
	if err != nil {
		logger.Errorf("Error getting %s in %s. We continue cleaning.", currentImagePullSecret, mosc)
	}

	if currentImagePullSecret == nil {
		logger.Infof("Push secret is empty in %s, skipping pull secret cleanup", mosc)
	} else {
		cleanupMOSCSecret(*currentImagePullSecret)
		if err != nil {
			logger.Errorf("An error happened cleaning %s in %s. We continue cleaning.\nErr:%s", currentImagePullSecret, mosc, err)
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
		return nil, fmt.Errorf("Empty MachineConfigPool configured in %s", mosc)
	}

	return NewMachineConfigPool(mosc.oc, poolName), nil
}

// GetMachineOSBuildList returns a list of all MOSB linked to this MOSC
func (mosc MachineOSConfig) GetMachineOSBuildList() ([]MachineOSBuild, error) {
	mosbList := NewMachineOSBuildList(mosc.GetOC())
	mosbList.SetItemsFilter(fmt.Sprintf(`?(@.spec.machineOSConfig.name=="%s")`, mosc.GetName()))
	return mosbList.GetAll()
}

// GetAll returns a []MachineOSConfig list with all existing pinnedimageset sorted by creation timestamp
func (moscl *MachineOSConfigList) GetAll() ([]MachineOSConfig, error) {
	moscl.ResourceList.SortByTimestamp()
	allMOSCResources, err := moscl.ResourceList.GetAll()
	if err != nil {
		return nil, err
	}
	allMOSCs := make([]MachineOSConfig, 0, len(allMOSCResources))

	for _, moscRes := range allMOSCResources {
		allMOSCs = append(allMOSCs, *NewMachineOSConfig(moscl.oc, moscRes.name))
	}

	return allMOSCs, nil
}

// GetAllOrFail returns a []MachineOSConfig list with all existing pinnedimageset sorted by creation time, if any error happens it fails the test
func (moscl *MachineOSConfigList) GetAllOrFail() []MachineOSConfig {
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
		return nil, fmt.Errorf("Error trying to login to internal registry:\nOutput:%s\nError:%s", loginOut, loginErr)
	}

	logger.Infof("Copy the docker.json file to local")
	// Because several MOSCs can be applied at the same time, MCDs can be restarted several times and it can cause a failure in the CopyToLocal method. We retry to mitigate this scenario
	err = Retry(5, 5*time.Second, func() error { return masterNode.CopyToLocal(tmpDockerConfigFile, tmpDockerConfigFile) })
	if err != nil {
		return nil, fmt.Errorf("Error copying the resulting authorization file to local")
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
