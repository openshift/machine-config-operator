package secrets

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

// Reads in a RegistrySecret and converts it to the DockerConfigJSON Kubernetes secret format.
func NormalizeDockerConfigJSONSecret[T RegistrySecret](in T) (*corev1.Secret, error) {
	is, err := NewImageRegistrySecret[T](in)
	if err != nil {
		return nil, err
	}

	return is.K8sSecret(corev1.SecretTypeDockerConfigJson)
}

// Reads in a RegistrySecret and converts it to the Dockercfg Kubernetes secret format.
func NormalizeDockercfgSecret[T RegistrySecret](in T) (*corev1.Secret, error) {
	is, err := NewImageRegistrySecret[T](in)
	if err != nil {
		return nil, err
	}

	return is.K8sSecret(corev1.SecretTypeDockercfg)
}

// Validates that a Kubernetes image pull secret is as we expect it to be. This
// is exported for easy reuse.
func ValidateKubernetesImageRegistrySecret(secret *corev1.Secret) error {
	if secret == nil {
		return fmt.Errorf("secret is nil")
	}

	if secret.Data == nil || len(secret.Data) == 0 {
		return newK8sSecretErr(secret, "has no data")
	}

	if secret.Type == "" {
		return newK8sSecretErr(secret, "type field not set")
	}

	secretTypes := getSecretTypeMap()

	key, ok := secretTypes[secret.Type]
	if !ok {
		return newK8sSecretErr(secret, fmt.Sprintf("invalid secret key type %s, expected '%s' or '%s'", secret.Type, corev1.SecretTypeDockercfg, corev1.SecretTypeDockerConfigJson))
	}

	val, ok := secret.Data[key]
	if !ok {
		return newK8sSecretErr(secret, fmt.Sprintf("type is '%s' but required key '%s' is missing", secret.Type, key))
	}

	if len(val) == 0 {
		return newK8sSecretErr(secret, fmt.Sprintf("type is '%s' but key '%s' has an empty value", secret.Type, key))
	}

	for secretType, secretKey := range secretTypes {
		if secret.Type != secretType && secretKey != key {
			if _, ok := secret.Data[secretKey]; ok {
				return newK8sSecretErr(secret, fmt.Sprintf("type is '%s' but also contains unexpected key '%s'", secret.Type, secretKey))
			}
		}
	}

	return nil
}

// Gets a mapping of the Kubernetes secret type to its expected map key.
func getSecretTypeMap() map[corev1.SecretType]string {
	return map[corev1.SecretType]string{
		corev1.SecretTypeDockerConfigJson: corev1.DockerConfigJsonKey,
		corev1.SecretTypeDockercfg:        corev1.DockerConfigKey,
	}
}
