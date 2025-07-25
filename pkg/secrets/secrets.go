// This file contains several functions for working with Docker image pull
// configs. Instead of maintaining our own implementation here, we should
// instead use the implementations found under:
//
// - https://github.com/containers/image/blob/main/pkg/docker/config/config.go
// - https://github.com/kubernetes/kubernetes/blob/master/pkg/credentialprovider/config.go
//
// These more official implementations are more aware of edgecases than our
// naive implementation here.
package secrets

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/kubectl/pkg/scheme"
)

// RegistrySecret is a type constraint that defines the various forms of image registry secrets
// that can be wrapped by the ImageRegistrySecret interface.
type RegistrySecret interface {
	*corev1.Secret | corev1.Secret | DockerConfigJSON | DockerConfig | []byte
}

// NewImageRegistrySecret is a constructor which accepts a value conforming to the RegistrySecret
// type constraint and instantiates an ImageRegistrySecret from it by calling the appropriate
// internal function.
func NewImageRegistrySecret[T RegistrySecret](in T) (ImageRegistrySecret, error) {
	return newImageRegistrySecretFromAny(in)
}

// newImageRegistrySecretFromAny is a private constructor that accepts a broader range of types
// than the public RegistrySecret type constraint allows. It dispatches to the correct
// instantiation function based on the input's concrete type.
func newImageRegistrySecretFromAny(in any) (ImageRegistrySecret, error) {
	switch val := in.(type) {
	case ImageRegistrySecret:
		return newImageRegistrySecretFromDockerConfigJSON(val.DockerConfigJSON()), nil
	case corev1.Secret:
		return newImageRegistrySecretFromK8sSecret(&val)
	case *corev1.Secret:
		return newImageRegistrySecretFromK8sSecret(val)
	case DockerConfigJSON:
		return newImageRegistrySecretFromDockerConfigJSON(val), nil
	case DockerConfig:
		return newImageRegistrySecretFromDockerConfig(val), nil
	case []byte:
		return newImageRegistrySecretFromBytes(val)
	default:
		return nil, fmt.Errorf("unknown type %T", val)
	}
}

// DockerConfigJSON represents the ~/.docker/config.json file information.
// This is the newer-style format which maps to `kubernetes.io/dockerconfigjson` secret type.
type DockerConfigJSON struct {
	Auths DockerConfig `json:"auths,omitempty"`
}

// newDockerConfigJSON creates and returns a new, empty DockerConfigJSON struct.
func newDockerConfigJSON() DockerConfigJSON {
	return DockerConfigJSON{
		Auths: newDockerConfig(),
	}
}

// newDockerConfig creates and returns a new, empty DockerConfig map.
func newDockerConfig() DockerConfig {
	return DockerConfig{}
}

// DockerConfig represents the config file used by the docker CLI.
// This config specifies the credentials that should be used when pulling images
// from specific image repositories.
//
// This is the legacy-style DockerConfig which maps to `kubernetes.io/dockercfg` secret type.
type DockerConfig map[string]DockerConfigEntry

// DockerConfigEntry wraps a single docker registry configuration entry.
type DockerConfigEntry struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Email    string `json:"email,omitempty"`
	Auth     string `json:"auth,omitempty"`
}

// ImageRegistrySecret is an interface intended to be a thin wrapper around image registry pull secrets.
// It provides methods to convert secrets between various formats (e.g., DockerConfigJSON, DockerConfig)
// and to/from Kubernetes Secret objects.
type ImageRegistrySecret interface {
	// K8sSecret converts the internal representation of the secret into a Kubernetes Secret object
	// of the specified corev1.SecretType. It automatically sets the correct data keys.
	// Note: The returned corev1.Secret does not have a name or any other metadata associated with it.
	// It is the responsibility of the caller to ensure those values are present, if needed.
	K8sSecret(corev1.SecretType) (*corev1.Secret, error)
	// DockerConfigJSON returns the secret's content as a DockerConfigJSON struct,
	// which is the modern representation.
	DockerConfigJSON() DockerConfigJSON
	// DockerConfig returns the secret's content as a DockerConfig map,
	// which is the legacy representation.
	DockerConfig() DockerConfig
	// JSONBytes returns a JSON byte-slice representation of the secret,
	// formatted according to the specified corev1.SecretType.
	JSONBytes(corev1.SecretType) ([]byte, error)
	// IsLegacyStyle returns true if the secret was originally in the legacy
	// format when the ImageRegistrySecret struct was instantiated.
	IsLegacyStyle() bool
	// Equal determines if another ImageRegistrySecret instance is semantically equal
	// to the current instance by comparing their underlying DockerConfigJSON data.
	Equal(ImageRegistrySecret) bool
}

// imageRegistrySecretImpl holds the internal implementation details for the ImageRegistrySecret interface.
type imageRegistrySecretImpl struct {
	cfg           DockerConfigJSON
	isLegacyStyle bool
}

// newImageRegistrySecretFromBytes reads a byte array and attempts to guess its format
// (e.g., Kubernetes secret, dockerconfigjson, or dockercfg) and decodes it into
// an ImageRegistrySecret instance.
func newImageRegistrySecretFromBytes(in []byte) (ImageRegistrySecret, error) {
	k8sSecret, err := decodeKubeSecretFromBytes(in)
	if err == nil {
		return newImageRegistrySecretFromK8sSecret(k8sSecret)
	}

	if isRuntimeError(err, runtime.IsMissingKind) {
		return newImageRegistrySecretFromDockerConfigBytes(in)
	}

	return nil, err
}

// newImageRegistrySecretFromK8sSecret accepts a Kubernetes secret object and attempts
// to decode the appropriate data field(s) into an ImageRegistrySecret.
func newImageRegistrySecretFromK8sSecret(secret *corev1.Secret) (ImageRegistrySecret, error) {
	if err := ValidateKubernetesImageRegistrySecret(secret); err != nil {
		return nil, err
	}

	for secretType, secretKey := range getSecretTypeMap() {
		if secretType == secret.Type {
			return newImageRegistrySecretFromDockerConfigBytes(secret.Data[secretKey])
		}
	}

	return nil, fmt.Errorf("invalid secret type %q and / or invalid secret map key(s) for secret %s", secret.Type, secret.Name)
}

// newImageRegistrySecretFromDockerConfigJSON accepts a DockerConfigJSON input,
// makes a deep copy of it, and returns a new ImageRegistrySecret instance.
func newImageRegistrySecretFromDockerConfigJSON(dcj DockerConfigJSON) ImageRegistrySecret {
	cfg := newDockerConfigJSON()

	for key, val := range dcj.Auths {
		cfg.Auths[key] = val
	}

	return &imageRegistrySecretImpl{cfg: cfg}
}

// newImageRegistrySecretFromDockerConfig accepts a DockerConfig input,
// makes a deep copy of it, and returns a new ImageRegistrySecret instance,
// marked as being of legacy style.
func newImageRegistrySecretFromDockerConfig(dc DockerConfig) ImageRegistrySecret {
	cfg := newDockerConfigJSON()

	for key, val := range dc {
		cfg.Auths[key] = val
	}

	return &imageRegistrySecretImpl{cfg: cfg, isLegacyStyle: true}
}

// newImageRegistrySecretFromDockerConfigBytes is a private function that attempts
// to decode a given byte slice into either a DockerConfigJSON or a DockerConfig
// structure, and then returns an ImageRegistrySecret. It prioritizes DockerConfigJSON.
func newImageRegistrySecretFromDockerConfigBytes(in []byte) (ImageRegistrySecret, error) {
	if in == nil || len(in) == 0 {
		return nil, fmt.Errorf("empty dockerconfig bytes")
	}

	errs := []error{}

	cfg, err := decodeDockerConfigJSONBytes(in)
	if err == nil {
		return &imageRegistrySecretImpl{cfg: *cfg, isLegacyStyle: false}, nil
	}

	errs = append(errs, err)

	auths, err := decodeDockercfgBytes(in)
	if err == nil {
		return &imageRegistrySecretImpl{cfg: DockerConfigJSON{Auths: *auths}, isLegacyStyle: true}, nil
	}

	errs = append(errs, err)

	return nil, fmt.Errorf("input bytes not dockerconfigjson or dockercfg secret(s): %w", errors.Join(errs...))
}

// decodeDockerConfigJSONBytes decodes a byte slice into a DockerConfigJSON struct.
func decodeDockerConfigJSONBytes(in []byte) (*DockerConfigJSON, error) {
	cfg := &DockerConfigJSON{}
	err := decodeDockerConfigBytes(in, &cfg)
	if err != nil {
		return nil, fmt.Errorf("could not decode dockerconfigjson bytes: %w", err)
	}

	return cfg, nil
}

// decodeDockercfgBytes decodes a byte slice into a DockerConfig struct.
func decodeDockercfgBytes(in []byte) (*DockerConfig, error) {
	cfg := &DockerConfig{}
	err := decodeDockerConfigBytes(in, &cfg)
	if err != nil {
		return nil, fmt.Errorf("could not decode dockercfg bytes: %w", err)
	}

	return cfg, nil
}

// decodeDockerConfigBytes creates a JSON decoder instance with strict settings
// and attempts to decode the input bytes into the provided target interface.
func decodeDockerConfigBytes(in []byte, target interface{}) error {
	decoder := json.NewDecoder(bytes.NewReader(in))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

// K8sSecret converts the internal representation of the secret into a format
// acceptable for insertion into the Kubernetes API server. Given the secret type,
// it automatically converts the secret to the appropriate type and sets the correct keys.
//
// NOTE: The returned corev1.Secret does not have a name or any other metadata
// associated with it. It is the responsibility of the caller to ensure those
// values are present, if needed.
func (i *imageRegistrySecretImpl) K8sSecret(secretType corev1.SecretType) (*corev1.Secret, error) {
	secretBytes, key, err := i.getSecretBytesForSecretType(secretType)
	if err != nil {
		return nil, err
	}

	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Secret",
		},
		Type: secretType,
		Data: map[string][]byte{
			key: secretBytes,
		},
	}, nil
}

// getSecretBytesForSecretType marshals the internal secret representation into
// a byte slice and returns the appropriate key for insertion into the
// Kubernetes secret's data map, based on the provided secretType.
func (i *imageRegistrySecretImpl) getSecretBytesForSecretType(secretType corev1.SecretType) ([]byte, string, error) {
	if secretType == corev1.SecretTypeDockerConfigJson {
		bytes, err := json.Marshal(i.DockerConfigJSON())
		return bytes, corev1.DockerConfigJsonKey, err
	}

	if secretType == corev1.SecretTypeDockercfg {
		bytes, err := json.Marshal(i.DockerConfig())
		return bytes, corev1.DockerConfigKey, err
	}

	return nil, "", fmt.Errorf("invalid secret type %s, expected '%s' or '%s'", secretType, corev1.SecretTypeDockercfg, corev1.SecretTypeDockerConfigJson)
}

// DockerConfigJSON returns the DockerConfigJSON which is the modern representation of this secret.
func (i *imageRegistrySecretImpl) DockerConfigJSON() DockerConfigJSON {
	return i.cfg
}

// DockerConfig returns the DockerConfig, which is the legacy representation of this secret.
func (i *imageRegistrySecretImpl) DockerConfig() DockerConfig {
	return i.cfg.Auths
}

// JSONBytes gets a JSON byte-slice representation of the secret based on the specified secretType.
func (i *imageRegistrySecretImpl) JSONBytes(secretType corev1.SecretType) ([]byte, error) {
	bytes, _, err := i.getSecretBytesForSecretType(secretType)
	return bytes, err
}

// IsLegacyStyle returns whether the secret was originally in the legacy format
// when the struct was instantiated.
func (i *imageRegistrySecretImpl) IsLegacyStyle() bool {
	return i.isLegacyStyle
}

// Equal determines if an ImageRegistrySecret instance is equal to the current instance.
// It compares the `Auths` map of the underlying DockerConfigJSON.
func (i *imageRegistrySecretImpl) Equal(is2 ImageRegistrySecret) bool {
	dcj1 := i.DockerConfigJSON()
	dcj2 := is2.DockerConfigJSON()

	if len(dcj1.Auths) != len(dcj2.Auths) {
		return false
	}

	for key, val := range dcj1.Auths {
		if dcj2.Auths[key] != val {
			return false
		}
	}

	return true
}

// decodeKubeSecretFromBytes attempts to decode the input bytes into a Kubernetes
// Secret object using the runtime decoder.
func decodeKubeSecretFromBytes(data []byte) (*corev1.Secret, error) {
	decode := scheme.Codecs.UniversalDeserializer().Decode

	secretObj, _, err := decode(data, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("could not decode object: %w", err)
	}

	secret, ok := secretObj.(*corev1.Secret)
	if !ok {
		return nil, fmt.Errorf("invalid object type %T, wanted corev1.Secret", secretObj)
	}

	return secret, nil
}

// isRuntimeError unwraps all errors and calls the provided check function at each level
// in order to determine whether the error is matched by the check function.
// It returns true if the error matches at any level of unwrapping.
func isRuntimeError(err error, checkFunc func(err error) bool) bool {
	if err == nil {
		return false
	}

	if checkFunc(err) {
		return true
	}

	unwrapped := errors.Unwrap(err)

	for {
		if unwrapped == nil {
			return false
		}

		if checkFunc(unwrapped) {
			return true
		}

		unwrapped = errors.Unwrap(unwrapped)
	}
}

// k8sSecretErr holds values for a Kubernetes secret-related error.
type k8sSecretErr struct {
	name      string
	namespace string
	msg       string
}

// newK8sSecretErr instantiates a new k8sSecretErr given the secret itself and an error message.
func newK8sSecretErr(secret *corev1.Secret, msg string) error {
	return &k8sSecretErr{
		name:      secret.Name,
		namespace: secret.Namespace,
		msg:       msg,
	}
}

// Error implements the error interface for k8sSecretErr, providing a formatted error message.
func (k *k8sSecretErr) Error() string {
	name := "<unnamed>"
	if k.name != "" {
		name = k.name
	}

	namespace := "<unnamespaced>"
	if k.namespace != "" {
		namespace = k.namespace
	}

	return fmt.Sprintf("secret '%s/%s' %s", name, namespace, k.msg)
}
