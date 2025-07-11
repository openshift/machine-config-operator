package common

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/openshift/machine-config-operator/pkg/secrets"
)

// All of the functions within this file should eventually be replaced by the
// secrets module at their respective call-sites. However, the unit tests
// provided alongside these functions provide a valuable check to ensure that
// the secrets code works as intended as well as allows the secrets code to be
// adopted in the future.

// Merges kubernetes.io/dockercfg type secrets into a JSON map.
// Returns an error on failure to marshal the incoming secret.
func MergeDockerConfigstoJSONMap(secretRaw []byte, auths map[string]secrets.DockerConfigEntry) error {
	merger := secrets.NewSecretMerger()

	if err := merger.Insert(secrets.DockerConfig(auths)); err != nil {
		return err
	}

	if err := merger.Insert(secretRaw); err != nil {
		return err
	}

	is := merger.ImageRegistrySecret()

	for key, val := range is.DockerConfigJSON().Auths {
		auths[key] = val
	}

	return nil
}

// Converts a kubernetes.io/dockerconfigjson type secret to a
// kubernetes.io/dockercfg type secret. Returns an error on failure
// if the incoming secret is not formatted correctly.
func ConvertSecretTodockercfg(secretBytes []byte) ([]byte, error) {
	bytes, _, err := convertToSecretType(secretBytes, corev1.SecretTypeDockercfg)
	return bytes, err
}

// Converts a legacy Docker pull secret into a more modern representation.
// Essentially, it converts {"registry.hostname.com": {"username": "user"...}}
// into {"auths": {"registry.hostname.com": {"username": "user"...}}}. If it
// encounters a pull secret already in this configuration, it will return the
// input secret as-is. Returns either the supplied data or the newly-configured
// representation of said data, a boolean to indicate whether it was converted,
// and any errors resulting from the conversion process.
func ConvertSecretToDockerconfigJSON(secretBytes []byte) ([]byte, bool, error) {
	return convertToSecretType(secretBytes, corev1.SecretTypeDockerConfigJson)
}

// Converts a provided secret into a kubernetes.io/dockerconfigjson secret then
// unmarshals it into the appropriate data structure. This means it will handle
// both legacy and current-style Docker configs.
func ToDockerConfigJSON(secretBytes []byte) (*secrets.DockerConfigJSON, error) {
	is, err := secrets.NewImageRegistrySecret(secretBytes)
	if err != nil {
		return nil, err
	}

	dcj := is.DockerConfigJSON()
	return &dcj, nil
}

func convertToSecretType(secretBytes []byte, secretType corev1.SecretType) ([]byte, bool, error) {
	is, err := secrets.NewImageRegistrySecret(secretBytes)
	if err != nil {
		return nil, false, err
	}

	bytes, err := is.JSONBytes(secretType)
	return bytes, is.IsLegacyStyle(), err
}
