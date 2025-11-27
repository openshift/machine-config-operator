package extended

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

// Secret struct encapsulates the functionalities regarding ocp secrets
type Secret struct {
	Resource
}

// NewSecret creates a Secret struct
func NewSecret(oc *exutil.CLI, namespace, name string) *Secret {
	return &Secret{Resource: *NewNamespacedResource(oc, "secret", namespace, name)}
}

// ExtractToDir extracts the secret's content to a given directory
func (s Secret) ExtractToDir(directory string) error {
	err := s.oc.WithoutNamespace().Run("extract").Args(s.GetKind()+"/"+s.GetName(), "-n", s.GetNamespace(), "--to", directory).Execute()
	if err != nil {
		return err
	}

	return nil
}

// Extract extracts the secret's content to a random directory in the testcase's output directory
func (s Secret) Extract() (string, error) {
	layout := "2006_01_02T15-04-05Z.000"

	directory := filepath.Join(e2e.TestContext.OutputDir, fmt.Sprintf("%s-%s-secret-%s", s.GetNamespace(), s.GetName(), time.Now().Format(layout)))
	os.MkdirAll(directory, os.ModePerm)
	return directory, s.ExtractToDir(directory)
}

// GetDataValue gets the value stored in the secret's key
func (s Secret) GetDataValue(key string) (string, error) {
	templateArg := fmt.Sprintf(`--template={{index .data "%s" | base64decode}}`, key)
	return s.oc.AsAdmin().WithoutNamespace().Run("get").Args(s.GetKind(), s.GetName(), "-n", s.GetNamespace(), templateArg).Output()
}

// GetDecodedDataMap returns the secret's data as a map with decoded values
func (s Secret) GetDecodedDataMap() (map[string]string, error) {
	data := map[string]string{}
	dataJSON, err := s.Get(`{.data}`)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal([]byte(dataJSON), &data); err != nil {
		return nil, err
	}

	for k, vb64 := range data {
		v, err := base64.StdEncoding.DecodeString(vb64)
		if err != nil {
			logger.Errorf("The certiifcate provided in the kubeconfig is not base64 encoded")
			return nil, err
		}
		// Replace the original encoded value with the decoded value
		data[k] = string(v)
	}

	return data, nil
}

// SetDataValue sets a key/value to store in the secret
func (s Secret) SetDataValue(key, value string) error {
	// silently set the value so that we don't print the secret in the logs leaking sensible information
	s.oc.NotShowInfo()
	defer s.oc.SetShowInfo()
	logger.Debugf("Secret %s -n %s. Setting value: %s=%s", s.GetName(), s.GetNamespace(), key, value)
	// command example: oc secret pull-secret -n openshift-config set data .dockerconfigjson={}
	return s.oc.AsAdmin().WithoutNamespace().Run("set").Args("data", s.GetKind(), s.GetName(),
		"-n", s.GetNamespace(),
		fmt.Sprintf("%s=%s", key, value)).Execute()
}

// GetPullSecret returns the cluster's pull secret
func GetPullSecret(oc *exutil.CLI) *Secret {
	return NewSecret(oc.AsAdmin(), "openshift-config", "pull-secret")
}
