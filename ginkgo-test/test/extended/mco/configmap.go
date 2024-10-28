package mco

import (
	"fmt"

	"encoding/json"

	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util"
	logger "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/logext"
)

// ConfigMap struct encapsulates the functionalities regarding ocp configmaps
type ConfigMap struct {
	Resource
}

// ConfigMapList handles list of ConfigMap
type ConfigMapList struct {
	ResourceList
}

// NewConfigMap creates a Secret struct
func NewConfigMap(oc *exutil.CLI, namespace, name string) *ConfigMap {
	return &ConfigMap{Resource: *NewNamespacedResource(oc, "ConfigMap", namespace, name)}
}

// NewConfigMapList creates a Secret struct
func NewConfigMapList(oc *exutil.CLI, namespace string) *ConfigMapList {
	return &ConfigMapList{ResourceList: *NewNamespacedResourceList(oc, "ConfigMap", namespace)}
}

// HasKey returns if a key is present in "data"
func (cm *ConfigMap) HasKey(key string) (string, bool, error) {
	dataMap, err := cm.GetDataMap()

	if err != nil {
		return "", false, err
	}

	data, ok := dataMap[key]
	if !ok {
		return "", false, nil
	}

	return data, true, nil
}

// GetDataValue return the value of a key stored in "data".
func (cm *ConfigMap) GetDataValue(key string) (string, error) {
	// We cant use the "resource.Get" method, because exutil.client will trim the output, removing spaces and newlines that could be important in a configuration.
	dataMap, err := cm.GetDataMap()

	if err != nil {
		return "", err
	}

	data, ok := dataMap[key]
	if !ok {
		return "", fmt.Errorf("Key %s does not exist in the .data in Configmap -n %s %s",
			key, cm.GetNamespace(), cm.GetName())
	}

	return data, nil

}

// GetDataMap returns the valus in the .data field as a map[string][string]
func (cm *ConfigMap) GetDataMap() (map[string]string, error) {
	data := map[string]string{}
	dataJSON, err := cm.Get(`{.data}`)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal([]byte(dataJSON), &data); err != nil {
		return nil, err
	}

	return data, nil
}

// GetDataValueOrFail return the value of a key stored in "data" and fails the test if the value cannot be retreived. If the "key" does not exist, it returns an empty string but does not fail
func (cm *ConfigMap) GetDataValueOrFail(key string) string {
	value, err := cm.GetDataValue(key)
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(),
		"Could get the value for key %s in configmap -n %s %s",
		key, cm.GetNamespace(), cm.GetName())

	return value
}

// SetData update the configmap with the given values. Same as "oc set data cm/..."
func (cm *ConfigMap) SetData(arg string, args ...string) error {
	params := []string{"data"}
	params = append(params, cm.Resource.getCommonParams()...)
	params = append(params, arg)
	if len(args) > 0 {
		params = append(params, args...)
	}

	return cm.Resource.oc.WithoutNamespace().Run("set").Args(params...).Execute()
}

// RemoveDataKey removes a key from the configmap data values
func (cm *ConfigMap) RemoveDataKey(key string) error {
	return cm.Patch("json", `[{"op": "remove", "path": "/data/`+key+`"}]`)
}

// CreateConfigMapWithRandomCert creates a configmap that stores a random CA in it
func CreateConfigMapWithRandomCert(oc *exutil.CLI, cmNamespace, cmName, certKey string) (*ConfigMap, error) {
	_, caPath, err := createCA(createTmpDir(), certKey)
	if err != nil {
		return nil, err
	}

	err = oc.WithoutNamespace().Run("create").Args("cm", "-n", cmNamespace, cmName, "--from-file", caPath).Execute()

	if err != nil {
		return nil, err
	}

	return NewConfigMap(oc, cmNamespace, cmName), nil
}

// GetCloudProviderConfigMap will return the CloudProviderConfigMap or nil if it is not defined in the infrastructure resource
func GetCloudProviderConfigMap(oc *exutil.CLI) *ConfigMap {
	infra := NewResource(oc, "infrastructure", "cluster")
	cmName := infra.GetOrFail(`{.spec.cloudConfig.name}`)

	if cmName == "" {
		logger.Infof("CloudProviderConfig ConfigMap is not defined in the infrastructure resource: %s", infra.PrettyString())
		return nil
	}

	return NewConfigMap(oc, "openshift-config", cmName)
}

// GetAll returns a []ConfigMap list with all existing pinnedimageset sorted by creation timestamp
func (cml *ConfigMapList) GetAll() ([]ConfigMap, error) {
	cml.ResourceList.SortByTimestamp()
	allResources, err := cml.ResourceList.GetAll()
	if err != nil {
		return nil, err
	}
	all := make([]ConfigMap, 0, len(allResources))

	for _, res := range allResources {
		all = append(all, *NewConfigMap(cml.oc, res.namespace, res.name))
	}

	return all, nil
}

// GetAllOrFail returns a []ConfigMap list with all existing pinnedimageset sorted by creation time, if any error happens it fails the test
func (cml *ConfigMapList) GetAllOrFail() []ConfigMap {
	all, err := cml.GetAll()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error getting the list of existing ConfigMap in the cluster")
	return all
}
