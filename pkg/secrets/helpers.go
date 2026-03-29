package secrets

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
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

// Wraps imageRegistrySecretImpl and implements the Unmarshaller interface for
// better control over unmarshalling.
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

	if bytes.Equal(in, []byte(strings.TrimSpace(`{}`))) {
		d.cfg.Auths = nil
		return nil
	}

	// Attempt to decode into DockerConfigJSON first. It is likely that we will
	// get an unknown field error. This error can be ignored provided that
	// decoding into a DockerConfig does not produce any errors.
	if err := d.decodeDockerConfigJSON(in); err == nil {
		return nil
	}

	if err := d.decodeDockerConfig(in); err != nil {
		return fmt.Errorf("decoding DockerConfig / DockerConfigJSON: %w", err)
	}

	return nil
}

// Strictly decodes bytes into a DockerConfig object.
func (d *dockerConfigJSONDecoder) decodeDockerConfig(in []byte) error {
	dc := DockerConfig{}
	if err := strictJSONDecode(in, &dc); err != nil {
		return fmt.Errorf("could not decode DockerConfig: %w", err)
	}

	d.cfg.Auths = dc
	d.isLegacyStyle = true
	return nil
}

// Strictly decodes JSON bytes into a DockerConfigJSON object.
func (d *dockerConfigJSONDecoder) decodeDockerConfigJSON(in []byte) error {
	// Private version of the DockerConfigJSON struct with a CredHelpers field.
	// This prevents an unknown field error from occurring if this field is
	// present. However, this field will be ignored by everything else here.
	// Strict decoding is necessary because we need to distinguish between the
	// DockerConfigJSON and DockerConfig schemas.
	type withCredHelpers struct {
		DockerConfigJSON
		CredHelpers json.RawMessage `json:"credHelpers"`
	}

	dcJSON := withCredHelpers{}

	if err := strictJSONDecode(in, &dcJSON); err != nil {
		return fmt.Errorf("could not decode DockerConfigJSON: %w", err)
	}

	if len(dcJSON.CredHelpers) != 0 {
		klog.Warning("Found credHelpers in DockerConfigJSON, ignoring")
	}

	d.isLegacyStyle = false
	d.cfg.Auths = dcJSON.Auths
	return nil
}

// Adds additional strictness to the unmarshalling process by disallowing
// unknown fields.
func strictJSONDecode(in []byte, target interface{}) error {
	decoder := json.NewDecoder(bytes.NewReader(in))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}
