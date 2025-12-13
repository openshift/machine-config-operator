package secrets

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/yaml"
)

func TestImageRegistrySecretFromBytes(t *testing.T) {
	dockerconfigJSON := []byte(`{"auths":{"registry.hostname.com":{"username":"user","email":"user@hostname.com","auth":"s00pers3kr1t"}}}`)
	dockercfgJSON := []byte(`{"registry.hostname.com":{"username":"user","email":"user@hostname.com","auth":"s00pers3kr1t"}}`)

	k8sDockerConfigJsonSecret := `apiVersion: v1
data:
  .dockerconfigjson: eyJhdXRocyI6eyJyZWdpc3RyeS5ob3N0bmFtZS5jb20iOnsidXNlcm5hbWUiOiJ1c2VyIiwiZW1haWwiOiJ1c2VyQGhvc3RuYW1lLmNvbSIsImF1dGgiOiJzMDBwZXJzM2tyMXQifX19
kind: Secret
metadata:
  name: secret
  # This is needed
  creationTimestamp:
type: kubernetes.io/dockerconfigjson`

	k8sDockerConfigYAMLBytes := []byte(k8sDockerConfigJsonSecret)

	k8sDockercfgSecret := `apiVersion: v1
data:
  .dockercfg: eyJyZWdpc3RyeS5ob3N0bmFtZS5jb20iOnsidXNlcm5hbWUiOiJ1c2VyIiwiZW1haWwiOiJ1c2VyQGhvc3RuYW1lLmNvbSIsImF1dGgiOiJzMDBwZXJzM2tyMXQifX0=
kind: Secret
metadata:
  name: secret
  # This is needed
  creationTimestamp:
type: kubernetes.io/dockercfg`

	k8sDockerConfigJSONBytes, err := yaml.YAMLToJSON(k8sDockerConfigYAMLBytes)
	require.NoError(t, err)

	k8sDockercfgYAMLBytes := []byte(k8sDockercfgSecret)

	k8sDockercfgJSONBytes, err := yaml.YAMLToJSON(k8sDockercfgYAMLBytes)
	require.NoError(t, err)

	mismatchedK8sDockerConfigJSONSecret := `apiVersion: v1
data:
  .dockerconfigjson: eyJyZWdpc3RyeS5ob3N0bmFtZS5jb20iOnsidXNlcm5hbWUiOiJ1c2VyIiwiZW1haWwiOiJ1c2VyQGhvc3RuYW1lLmNvbSIsImF1dGgiOiJzMDBwZXJzM2tyMXQifX0=
kind: Secret
metadata:
  name: secret
  # This is needed
  creationTimestamp:
type: kubernetes.io/dockerconfigjson`

	mismatchedK8sDockercfgSecret := `apiVersion: v1
data:
  .dockercfg: eyJhdXRocyI6eyJyZWdpc3RyeS5ob3N0bmFtZS5jb20iOnsidXNlcm5hbWUiOiJ1c2VyIiwiZW1haWwiOiJ1c2VyQGhvc3RuYW1lLmNvbSIsImF1dGgiOiJzMDBwZXJzM2tyMXQifX19
kind: Secret
metadata:
  name: secret
  # This is needed
  creationTimestamp:
type: kubernetes.io/dockercfg`

	emptyK8sDockerConfigJSON := `apiVersion: v1
data:
  .dockercfg:
kind: Secret
metadata:
  name: secret
  # This is needed
  creationTimestamp:
type: kubernetes.io/dockercfg`

	emptyBase64K8sDockerConfigJSON := `apiVersion: v1
data:
  .dockercfg: Cg==
kind: Secret
metadata:
  name: secret
  # This is needed
  creationTimestamp:
type: kubernetes.io/dockercfg`

	cfg := DockerConfigJSON{
		Auths: map[string]DockerConfigEntry{
			"registry.hostname.com": DockerConfigEntry{
				Username: "user",
				Email:    "user@hostname.com",
				Auth:     "s00pers3kr1t",
			},
		},
	}

	testCases := []struct {
		name          string
		bytes         []byte
		isLegacyStyle bool
		expected      *DockerConfigJSON
		errExpected   bool
	}{
		{
			name:  "From YAML K8s DockerconfigJSON secret bytes base64",
			bytes: k8sDockerConfigYAMLBytes,
		},
		{
			name:          "From YAML K8s Dockercfg secret bytes base64",
			bytes:         k8sDockercfgYAMLBytes,
			isLegacyStyle: true,
		},
		{
			name:          "Mismatched K8s DockerConfigJSON Secret",
			bytes:         []byte(mismatchedK8sDockerConfigJSONSecret),
			isLegacyStyle: true,
		},
		{
			name:  "Mismatched K8s Dockercfg Secret",
			bytes: []byte(mismatchedK8sDockercfgSecret),
		},
		{
			name:        "Empty K8s DockerConfigJSON",
			bytes:       []byte(emptyK8sDockerConfigJSON),
			errExpected: true,
		},
		{
			name:        "Empty Base64 K8s DockerConfigJSON",
			bytes:       []byte(emptyBase64K8sDockerConfigJSON),
			errExpected: true,
		},
		{
			name:  "From JSON K8s DockerconfigJSON secret bytes base64",
			bytes: k8sDockerConfigJSONBytes,
		},
		{
			name:          "From JSON K8s Dockercfg secret bytes base64",
			bytes:         k8sDockercfgJSONBytes,
			isLegacyStyle: true,
		},
		{
			name:  "From dockerconfigjson bytes",
			bytes: dockerconfigJSON,
		},
		{
			name:          "From dockercfg bytes",
			bytes:         dockercfgJSON,
			isLegacyStyle: true,
		},
		{
			name:        "Empty bytes",
			bytes:       []byte(``),
			errExpected: true,
		},
		{
			name:        "Nil bytes",
			bytes:       nil,
			errExpected: true,
		},
		{
			name:        "Malformed JSON bytes",
			bytes:       []byte(`{`),
			errExpected: true,
		},
		{
			name:        "Invalid keys in JSON bytes",
			bytes:       []byte(`{"key":"value"}`),
			errExpected: true,
		},
		{
			name:        "Invalid auths keys in JSON bytes",
			bytes:       []byte(`{"auths":{"key":"value"}}`),
			errExpected: true,
		},
		{
			name:     "Empty auths in JSON bytes",
			bytes:    []byte(`{"auths":{}}`),
			expected: &DockerConfigJSON{Auths: DockerConfig{}},
		},
		{
			name:     "Empty JSON object without auths key",
			bytes:    []byte(`{}`),
			expected: &DockerConfigJSON{Auths: nil},
		},
		{
			name:        "JSON null literal",
			bytes:       []byte(`null`),
			errExpected: true,
		},
		{
			name:        "Invalid K8s object bytes",
			bytes:       []byte(`{"kind":"ConfigMap","apiVersion":"v1","metadata":{"name":"configmap"},"data":{"key":"value"}}`),
			errExpected: true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			is, err := NewImageRegistrySecret[[]byte](testCase.bytes)
			if testCase.errExpected {
				assert.Error(t, err)
				assert.Nil(t, is)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, is)
				assert.Equal(t, testCase.isLegacyStyle, is.IsLegacyStyle())
				if testCase.expected != nil {
					assert.Equal(t, *testCase.expected, is.DockerConfigJSON())
				} else {
					assert.Equal(t, cfg, is.DockerConfigJSON())
				}
			}
		})
	}
}

func TestEquality(t *testing.T) {
	dockerconfigJSON := []byte(`{"auths":{"registry.hostname.com":{"username":"user","email":"user@hostname.com","auth":"s00pers3kr1t"}}}`)
	dockercfgJSON := []byte(`{"registry.hostname.com":{"username":"user","email":"user@hostname.com","auth":"s00pers3kr1t"}}`)

	k8sDockerConfigJsonSecret := `apiVersion: v1
data:
  .dockerconfigjson: eyJhdXRocyI6eyJyZWdpc3RyeS5ob3N0bmFtZS5jb20iOnsidXNlcm5hbWUiOiJ1c2VyIiwiZW1haWwiOiJ1c2VyQGhvc3RuYW1lLmNvbSIsImF1dGgiOiJzMDBwZXJzM2tyMXQifX19
kind: Secret
metadata:
  name: secret
  # This is needed
  creationTimestamp:
type: kubernetes.io/dockerconfigjson`

	k8sDockerConfigYAMLBytes := []byte(k8sDockerConfigJsonSecret)

	k8sDockercfgSecret := `apiVersion: v1
data:
  .dockercfg: eyJyZWdpc3RyeS5ob3N0bmFtZS5jb20iOnsidXNlcm5hbWUiOiJ1c2VyIiwiZW1haWwiOiJ1c2VyQGhvc3RuYW1lLmNvbSIsImF1dGgiOiJzMDBwZXJzM2tyMXQifX0=
kind: Secret
metadata:
  name: secret
  # This is needed
  creationTimestamp:
type: kubernetes.io/dockercfg`

	k8sDockercfgYAMLBytes := []byte(k8sDockercfgSecret)

	unequalDockerconfigJSON := []byte(`{"auths":{"other-registry.hostname.com":{"username":"user","email":"user@hostname.com","auth":"s00pers3kr1t"}}}`)

	is1, err := NewImageRegistrySecret(dockerconfigJSON)
	assert.NoError(t, err)

	is2, err := NewImageRegistrySecret(dockercfgJSON)
	assert.NoError(t, err)

	is3, err := NewImageRegistrySecret(k8sDockerConfigYAMLBytes)
	assert.NoError(t, err)

	is4, err := NewImageRegistrySecret(k8sDockercfgYAMLBytes)
	assert.NoError(t, err)

	is5, err := NewImageRegistrySecret(unequalDockerconfigJSON)
	assert.NoError(t, err)

	assert.True(t, is1.Equal(is2))
	assert.True(t, is2.Equal(is3))
	assert.True(t, is3.Equal(is4))
	assert.False(t, is1.Equal(is5))
}

func TestImageRegistrySecretFromK8s(t *testing.T) {
	dockerconfigJSON := []byte(`{"auths":{"registry.hostname.com":{"username":"user","email":"user@hostname.com","auth":"s00pers3kr1t"}}}`)
	dockercfgJSON := []byte(`{"registry.hostname.com":{"username":"user","email":"user@hostname.com","auth":"s00pers3kr1t"}}`)

	testCases := []struct {
		name          string
		secret        *corev1.Secret
		isLegacyStyle bool
		errExpected   bool
	}{
		{
			name: "From K8s DockerConfigJSON secret",
			secret: &corev1.Secret{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "Secret",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "secret",
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{
					corev1.DockerConfigJsonKey: dockerconfigJSON,
				},
			},
		},
		{
			name: "From K8s Dockercfg secret",
			secret: &corev1.Secret{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "Secret",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "secret",
				},
				Type: corev1.SecretTypeDockercfg,
				Data: map[string][]byte{
					corev1.DockerConfigKey: dockercfgJSON,
				},
			},
			isLegacyStyle: true,
		},
		{
			name: "From K8s secret missing secret type",
			secret: &corev1.Secret{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "Secret",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "secret",
				},
				Data: map[string][]byte{
					corev1.DockerConfigKey: dockercfgJSON,
				},
			},
			errExpected: true,
		},
		{
			name:        "Empty secret",
			secret:      &corev1.Secret{},
			errExpected: true,
		},
		{
			name: "Empty DockerConfigJSON secret",
			secret: &corev1.Secret{
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{corev1.DockerConfigJsonKey: []byte{}},
			},
			errExpected: true,
		},
		{
			name: "Empty Dockercfg secret",
			secret: &corev1.Secret{
				Type: corev1.SecretTypeDockercfg,
				Data: map[string][]byte{corev1.DockerConfigKey: []byte{}},
			},
			errExpected: true,
		},
		{
			name: "Mismatched DockerConfigJSON secret",
			secret: &corev1.Secret{
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{corev1.DockerConfigKey: dockercfgJSON},
			},
			errExpected: true,
		},
		{
			name: "Mismatched Dockercfg secret",
			secret: &corev1.Secret{
				Type: corev1.SecretTypeDockercfg,
				Data: map[string][]byte{corev1.DockerConfigJsonKey: dockerconfigJSON},
			},
			errExpected: true,
		},
		{
			name: "Invalid secret type",
			secret: &corev1.Secret{
				Type: "i'm a secret",
				Data: map[string][]byte{corev1.DockerConfigJsonKey: dockerconfigJSON},
			},
			errExpected: true,
		},
		{
			name: "Multiple secret keys for type",
			secret: &corev1.Secret{
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{
					corev1.DockerConfigJsonKey: dockerconfigJSON,
					corev1.DockerConfigKey:     dockercfgJSON,
				},
			},
			errExpected: true,
		},
	}

	cfg := DockerConfigJSON{
		Auths: map[string]DockerConfigEntry{
			"registry.hostname.com": DockerConfigEntry{
				Username: "user",
				Email:    "user@hostname.com",
				Auth:     "s00pers3kr1t",
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			is, err := NewImageRegistrySecret[*corev1.Secret](testCase.secret)
			if testCase.errExpected {
				assert.Error(t, err)
				assert.Nil(t, is)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, is)
				assert.Equal(t, testCase.isLegacyStyle, is.IsLegacyStyle())
				assert.Equal(t, cfg, is.DockerConfigJSON())
			}
		})
	}
}

func TestImageRegistrySecretConversion(t *testing.T) {
	dockerconfigJSON := []byte(`{"auths":{"registry.hostname.com":{"username":"user","email":"user@hostname.com","auth":"s00pers3kr1t"}}}`)
	dockercfgJSON := []byte(`{"registry.hostname.com":{"username":"user","email":"user@hostname.com","auth":"s00pers3kr1t"}}`)

	cfg := DockerConfigJSON{
		Auths: map[string]DockerConfigEntry{
			"registry.hostname.com": DockerConfigEntry{
				Username: "user",
				Email:    "user@hostname.com",
				Auth:     "s00pers3kr1t",
			},
		},
	}

	is, err := NewImageRegistrySecret(cfg)
	assert.NoError(t, err)

	assert.Equal(t, cfg.Auths, is.DockerConfig())
	assert.Equal(t, cfg, is.DockerConfigJSON())

	k8sSecret, err := is.K8sSecret(corev1.SecretTypeDockerConfigJson)
	assert.NoError(t, err)
	assert.Equal(t, corev1.SecretTypeDockerConfigJson, k8sSecret.Type)
	assert.Contains(t, k8sSecret.Data, corev1.DockerConfigJsonKey)
	assert.JSONEq(t, string(k8sSecret.Data[corev1.DockerConfigJsonKey]), string(dockerconfigJSON))

	k8sSecret, err = is.K8sSecret(corev1.SecretTypeDockercfg)
	assert.NoError(t, err)
	assert.Equal(t, corev1.SecretTypeDockercfg, k8sSecret.Type)
	assert.Contains(t, k8sSecret.Data, corev1.DockerConfigKey)
	assert.JSONEq(t, string(k8sSecret.Data[corev1.DockerConfigKey]), string(dockercfgJSON))

	bytes, err := is.JSONBytes(corev1.SecretTypeDockercfg)
	assert.NoError(t, err)
	assert.JSONEq(t, string(dockercfgJSON), string(bytes))

	bytes, err = is.JSONBytes(corev1.SecretTypeDockerConfigJson)
	assert.NoError(t, err)
	assert.JSONEq(t, string(dockerconfigJSON), string(bytes))
}

func TestMarshalling(t *testing.T) {
	expected := []byte(`{"auths":{"registry.hostname.com":{"username":"user","email":"user@hostname.com","auth":"s00pers3kr1t"}}}`)

	outBytes, err := json.Marshal(DockerConfigJSON{
		Auths: map[string]DockerConfigEntry{
			"registry.hostname.com": DockerConfigEntry{
				Username: "user",
				Email:    "user@hostname.com",
				Auth:     "s00pers3kr1t",
			},
		},
	})

	require.NoError(t, err)
	assert.Equal(t, outBytes, expected)
}
