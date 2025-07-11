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

// Type constraint that defines the various forms of image registry secrets
// that we can wrap.
type RegistrySecret interface {
	*corev1.Secret | corev1.Secret | DockerConfigJSON | DockerConfig | []byte
}

// Constructor which accepts the type constraint and calls the appropriate
// function to instantiate an ImageRegistrySecret from it.
func NewImageRegistrySecret[T RegistrySecret](in T) (ImageRegistrySecret, error) {
	return newImageRegistrySecretFromAny(in)
}

// Private constructor that accepts more types than the type constraint allows.
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

// This file contains several functions for working with Docker image pull
// configs. Instead of maintaining our own implementation here, we should
// instead use the implementations found under:
//
// - https://github.com/containers/image/blob/main/pkg/docker/config/config.go
// - https://github.com/kubernetes/kubernetes/blob/master/pkg/credentialprovider/config.go
//
// These more official implementations are more aware of edgecases than our
// naive implementation here.

// DockerConfigJSON represents ~/.docker/config.json file info
// This is the newer-style which maps to kubernetes.io/dockerconfigjson
type DockerConfigJSON struct {
	Auths DockerConfig `json:"auths,omitempty"`
}

func newDockerConfigJSON() DockerConfigJSON {
	return DockerConfigJSON{
		Auths: newDockerConfig(),
	}
}

func newDockerConfig() DockerConfig {
	return DockerConfig{}
}

// DockerConfig represents the config file used by the docker CLI.
// This config that represents the credentials that should be used
// when pulling images from specific image repositories.
//
// This is the legacy-style DockerConfig which maps to kubernetes.io/dockercfg
type DockerConfig map[string]DockerConfigEntry

// DockerConfigEntry wraps a docker config as a entry
type DockerConfigEntry struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Email    string `json:"email,omitempty"`
	Auth     string `json:"auth,omitempty"`
}

// This is intended to be a thin wrapper around image registry pull secrets.
// Specifically, it is intended to convert secrets from one form to another in
// addition to converting to / from K8s secrets.
type ImageRegistrySecret interface {
	K8sSecret(corev1.SecretType) (*corev1.Secret, error)
	DockerConfigJSON() DockerConfigJSON
	DockerConfig() DockerConfig
	JSONBytes(corev1.SecretType) ([]byte, error)
	IsLegacyStyle() bool
	Equal(ImageRegistrySecret) bool
}

// Holds the implementation for ImageRegistrySecret
type imageRegistrySecretImpl struct {
	cfg           DockerConfigJSON
	isLegacyStyle bool
}

// Reads in a byte array and attempts to guess whether the inputted byte array
// is a Kubernetes secret of the appropriate type, a dockerconfig or
// dockerconfigjson secret, etc. and attempts to decode it into an
// ImageRegistrySecret instance.
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

// Accepts a Kubernetes secret and attempts to decode the correct field(s).
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

// Accepts a DockerConfigJSON input, makes a copy of it, and returns an ImageRegistrySecret.
func newImageRegistrySecretFromDockerConfigJSON(dcj DockerConfigJSON) ImageRegistrySecret {
	cfg := newDockerConfigJSON()

	for key, val := range dcj.Auths {
		cfg.Auths[key] = val
	}

	return &imageRegistrySecretImpl{cfg: cfg}
}

// Accepts a DockerConfig input, makes a copy of it, and returns an ImageRegistrySecret.
func newImageRegistrySecretFromDockerConfig(dc DockerConfig) ImageRegistrySecret {
	cfg := newDockerConfigJSON()

	for key, val := range dc {
		cfg.Auths[key] = val
	}

	return &imageRegistrySecretImpl{cfg: cfg, isLegacyStyle: true}
}

// Private function that attempts to decode a given DockerConfig into an ImageRegistrySecret.
func newImageRegistrySecretFromDockerConfigBytes(in []byte) (ImageRegistrySecret, error) {
	if in == nil || len(in) == 0 {
		return nil, fmt.Errorf("dockerconfig empty")
	}

	decoder := json.NewDecoder(bytes.NewReader(in))
	decoder.DisallowUnknownFields()

	cfg := DockerConfigJSON{}
	err := decoder.Decode(&cfg)
	if err == nil && len(cfg.Auths) > 0 {
		// If there were no errors and we have Auths, that means this was a
		// new-style pull secret.
		return &imageRegistrySecretImpl{cfg: cfg, isLegacyStyle: false}, nil
	}

	decoder = json.NewDecoder(bytes.NewReader(in))
	decoder.DisallowUnknownFields()

	err = decoder.Decode(&cfg.Auths)
	if err == nil && len(cfg.Auths) > 0 {
		// If there were no errors and we have auths, that means this was a legacy-style pull secret.
		return &imageRegistrySecretImpl{cfg: cfg, isLegacyStyle: true}, nil
	}

	return nil, fmt.Errorf("could not decode docker config: %w", err)
}

// Converts the internal representation of the secret into a format acceptable
// for insertion into the Kubernetes API server. Given the secret type, it will
// automatically convert the secret to the appropriate type and set the correct
// keys.
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

// Gets the marshaled secret as well as the correct key type for insertion into the secret bytes map.
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

// Returns the DockerConfigJSON which is the modern representation of this secret.
func (i *imageRegistrySecretImpl) DockerConfigJSON() DockerConfigJSON {
	return i.cfg
}

// Returns the DockerConfig, which is the legacy representation of this secret.
func (i *imageRegistrySecretImpl) DockerConfig() DockerConfig {
	return i.cfg.Auths
}

// Gets a JSON byte-slice representation of the secret.
func (i *imageRegistrySecretImpl) JSONBytes(secretType corev1.SecretType) ([]byte, error) {
	bytes, _, err := i.getSecretBytesForSecretType(secretType)
	return bytes, err
}

// Returns whether the secret was originally in the legacy format when the struct was instantiated.
func (i *imageRegistrySecretImpl) IsLegacyStyle() bool {
	return i.isLegacyStyle
}

// Determines if an ImageRegistrySecret instance is equal
// to the current instance.
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

// Attempts to decode the input bytes into a Kubernetes Secret object using the
// runtime decoder.
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

// The runtime.IsMissingKind() helper function does not unwrap errors. This
// will unwrap all errors and call the provided check function at each level in
// order to determine whether the error is matched by the check function.
// Returns true if the error matches.
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

// Holds values for a secret error type.
type k8sSecretErr struct {
	name      string
	namespace string
	msg       string
}

// Instantiates a new k8sSecretErr given the secret itself
func newK8sSecretErr(secret *corev1.Secret, msg string) error {
	return &k8sSecretErr{
		name:      secret.Name,
		namespace: secret.Namespace,
		msg:       msg,
	}
}

// Implements the Error() interface.
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
