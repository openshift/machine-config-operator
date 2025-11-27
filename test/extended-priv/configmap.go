package extended

import (
	"encoding/json"
	"fmt"

	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
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

// GetDataValue returns the value of a specific key in the .data field
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

// GetDataValueOrFail returns the value of a specific key in the .data field and fails the test if any error happens
func (cm *ConfigMap) GetDataValueOrFail(key string) string {
	value, err := cm.GetDataValue(key)
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(),
		"Could get the value for key %s in configmap -n %s %s",
		key, cm.GetNamespace(), cm.GetName())

	return value
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
