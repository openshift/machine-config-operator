package secrets

import (
	"bytes"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

// NormalizeDockerConfigJSONSecret reads in a RegistrySecret and converts it
// to the DockerConfigJSON Kubernetes secret format (type `kubernetes.io/dockerconfigjson`).
func NormalizeDockerConfigJSONSecret[T RegistrySecret](in T) (*corev1.Secret, error) {
	is, err := NewImageRegistrySecret[T](in)
	if err != nil {
		return nil, err
	}

	return is.K8sSecret(corev1.SecretTypeDockerConfigJson)
}

// NormalizeDockercfgSecret reads in a RegistrySecret and converts it
// to the Dockercfg Kubernetes secret format (type `kubernetes.io/dockercfg`).
func NormalizeDockercfgSecret[T RegistrySecret](in T) (*corev1.Secret, error) {
	is, err := NewImageRegistrySecret[T](in)
	if err != nil {
		return nil, err
	}

	return is.K8sSecret(corev1.SecretTypeDockercfg)
}

// ValidateKubernetesImageRegistrySecret validates that a Kubernetes image pull secret
// conforms to the expected structure and types (`kubernetes.io/dockerconfigjson`
// or `kubernetes.io/dockercfg`). This function is exported for reuse.
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

// getSecretTypeMap returns a mapping of Kubernetes secret types to their expected
// corresponding data map keys (e.g., `kubernetes.io/dockerconfigjson` maps to `.dockerconfigjson`).
func getSecretTypeMap() map[corev1.SecretType]string {
	return map[corev1.SecretType]string{
		corev1.SecretTypeDockerConfigJson: corev1.DockerConfigJsonKey,
		corev1.SecretTypeDockercfg:        corev1.DockerConfigKey,
	}
}

// Wraps imageRegistrySecretImpl and implements UnmarshalJSON so that
// progressive JSON decoding may be used.
type dockerConfigJSONDecoder struct {
	imageRegistrySecretImpl
}

// Custom unmarshal method that supports both old-style and new-style
// DockerConfig unmarshalling.
func (d *dockerConfigJSONDecoder) UnmarshalJSON(in []byte) error {
	// Check if we have a nil or zero-length payload.
	if in == nil || len(in) == 0 {
		return fmt.Errorf("empty dockerconfig bytes")
	}

	// Check if the input is just the JSON null literal
	if bytes.TrimSpace(in) != nil && string(bytes.TrimSpace(in)) == "null" {
		return fmt.Errorf("dockerconfig bytes contain JSON null")
	}

	// Next, determine if the JSON payload is valid, but empty (e.g., `{}`).
	empty := map[string]interface{}{}

	if err := json.Unmarshal(in, &empty); err != nil {
		return err
	}

	if len(empty) == 0 {
		d.cfg.Auths = nil
		return nil
	}

	// The JSON payload is not empty, but we don't know what fields are present.
	// So we decode into a private struct with an Auths field.
	type cfg struct {
		Auths json.RawMessage `json:"auths"`
	}

	c := &cfg{}
	if err := json.Unmarshal(in, c); err != nil {
		return fmt.Errorf("could not unmarshal top-level: %w", err)
	}

	// We determine whether this is a DockerConfigJSON or a DockerConfig by
	// looking at the length of the Auths field. These are decoded with extra
	// strictness.
	if len(c.Auths) > 0 {
		// If there is an Auths field, we decode into a DockerConfigJSON instance.
		dcJSON := DockerConfigJSON{}
		if err := strictJSONDecode(in, &dcJSON); err != nil {
			return fmt.Errorf("could not decode DockerConfigJSON: %w", err)
		}

		d.isLegacyStyle = false
		d.cfg.Auths = dcJSON.Auths
		return nil
	}

	// If there is no auths field, we try decoding into a DockerConfig.
	dc := DockerConfig{}
	if err := strictJSONDecode(in, &dc); err != nil {
		return fmt.Errorf("could not decode DockerConfig: %w", err)
	}

	d.cfg.Auths = dc
	d.isLegacyStyle = true
	return nil
}

// Adds additional strictness to the unmarshalling process by disallowing
// unknown fields.
func strictJSONDecode(in []byte, target interface{}) error {
	decoder := json.NewDecoder(bytes.NewReader(in))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}
