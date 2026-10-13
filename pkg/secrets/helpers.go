package secrets

import (
	"bytes"
	"encoding/json"
	"errors"
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

// errNotDockerConfigJSON is returned when the input doesn't contain
// DockerConfigJSON-specific keys, indicating a legacy DockerConfig format.
var errNotDockerConfigJSON = errors.New("not DockerConfigJSON format")

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
		d.cfg = newDockerConfigJSON()
		return nil
	}

	// Try to decode as DockerConfigJSON
	err := d.decodeDockerConfigJSON(in)
	if err == nil {
		return nil
	}

	// If it's not DockerConfigJSON format, try legacy DockerConfig format
	if errors.Is(err, errNotDockerConfigJSON) {
		if err := d.decodeDockerConfig(in); err != nil {
			return fmt.Errorf("decoding DockerConfig / DockerConfigJSON: %w", err)
		}
		return nil
	}

	// Error indicates malformed DockerConfigJSON, don't fall back
	return err
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
	// Private version of the DockerConfigJSON struct with CredHelpers and CredsStore fields.
	// This prevents unknown field errors from occurring if these fields are present.
	// However, these fields will be ignored. Strict decoding is necessary because we need
	// to distinguish between the DockerConfigJSON and DockerConfig schemas.
	type withCredHelpers struct {
		DockerConfigJSON
		CredHelpers json.RawMessage `json:"credHelpers"`
		CredsStore  json.RawMessage `json:"credsStore"`
	}

	dcJSON := withCredHelpers{}
	err := strictJSONDecode(in, &dcJSON)

	// If strict decode succeeded, we have DockerConfigJSON format
	if err == nil {
		if len(dcJSON.CredHelpers) != 0 {
			klog.Warning("Found credHelpers in DockerConfigJSON, ignoring")
		}
		if len(dcJSON.CredsStore) != 0 {
			klog.Warning("Found credsStore in DockerConfigJSON, ignoring")
		}
		d.isLegacyStyle = false
		d.cfg.Auths = dcJSON.Auths
		return nil
	}

	// Strict decode as DockerConfigJSON failed. Check if input has DockerConfigJSON-specific
	// keys - if so, the user intended DockerConfigJSON format but it's malformed,
	// so we should NOT fall back to legacy format.
	if len(dcJSON.Auths) > 0 || len(dcJSON.CredHelpers) > 0 || len(dcJSON.CredsStore) > 0 {
		return fmt.Errorf("input has DockerConfigJSON-specific keys but failed to decode as DockerConfigJSON: %w", err)
	}

	// No DockerConfigJSON keys found, return sentinel to indicate legacy format should be tried
	return errNotDockerConfigJSON
}

// Adds additional strictness to the unmarshalling process by disallowing
// unknown fields.
func strictJSONDecode(in []byte, target interface{}) error {
	decoder := json.NewDecoder(bytes.NewReader(in))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}
