// All of the functions within this file should eventually be replaced by the
// secrets module at their respective call-sites. However, the unit tests
// provided alongside these functions provide a valuable check to ensure that
// the secrets code works as intended given how these functions are used.
package common

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/openshift/machine-config-operator/pkg/secrets"
)

// MergeDockerConfigstoJSONMap merges kubernetes.io/dockercfg type secrets into a JSON map.
// It takes raw secret bytes and a map of existing DockerConfigEntry objects.
// The function uses a SecretMerger to combine the provided secretRaw and the
// existing auths map, then updates the auths map with the merged content.
// It returns an error if the incoming secret cannot be marshaled or if any
// other merging operation fails.
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

// ConvertSecretTodockercfg converts a kubernetes.io/dockerconfigjson type secret
// to a kubernetes.io/dockercfg type secret.
// It takes a byte slice representing the secret and returns the converted
// byte slice and an error if the conversion fails or if the incoming secret
// is not formatted correctly.
func ConvertSecretTodockercfg(secretBytes []byte) ([]byte, error) {
	bytes, _, err := convertToSecretType(secretBytes, corev1.SecretTypeDockercfg)
	return bytes, err
}

// ConvertSecretToDockerconfigJSON converts a legacy Docker pull secret into a more
// modern representation (kubernetes.io/dockerconfigjson).
// Specifically, it transforms a structure like {"registry.hostname.com": {"username": "user"...}}
// into {"auths": {"registry.hostname.com": {"username": "user"...}}}.
// If the input secret is already in the modern format, it will be returned as-is.
// The function returns the supplied data or the newly-configured representation,
// a boolean indicating whether the conversion occurred, and any errors encountered
// during the conversion process.
func ConvertSecretToDockerconfigJSON(secretBytes []byte) ([]byte, bool, error) {
	return convertToSecretType(secretBytes, corev1.SecretTypeDockerConfigJson)
}

// ToDockerConfigJSON converts a provided secret into a kubernetes.io/dockerconfigjson
// secret and then unmarshals it into the appropriate data structure.
// This function is designed to handle both legacy and current-style Docker configurations.
// It takes a byte slice representing the secret and returns a pointer to a
// secrets.DockerConfigJSON object and an error if the conversion or unmarshaling fails.
func ToDockerConfigJSON(secretBytes []byte) (*secrets.DockerConfigJSON, error) {
	is, err := secrets.NewImageRegistrySecret(secretBytes)
	if err != nil {
		return nil, err
	}

	dcj := is.DockerConfigJSON()
	return &dcj, nil
}

// convertToSecretType is an internal helper function that converts a given secret
// byte slice into a specific Kubernetes secret type (e.g., corev1.SecretTypeDockercfg
// or corev1.SecretTypeDockerConfigJson).
// It takes the raw secret bytes and the target corev1.SecretType.
// It returns the converted byte slice, a boolean indicating if the original secret
// was in a legacy style, and any error encountered during the conversion.
func convertToSecretType(secretBytes []byte, secretType corev1.SecretType) ([]byte, bool, error) {
	is, err := secrets.NewImageRegistrySecret(secretBytes)
	if err != nil {
		return nil, false, err
	}

	bytes, err := is.JSONBytes(secretType)
	return bytes, is.IsLegacyStyle(), err
}
