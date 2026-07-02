package secrets

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNormalizeFuncs(t *testing.T) {
	legacySecret := `{"registry.hostname.com":{"username":"user","password":"s3kr1t","auth":"s00pers3kr1t","email":"user@hostname.com"}}`

	newSecret := `{"auths":` + legacySecret + `}`

	testCases := []struct {
		name        string
		inputSecret *corev1.Secret
		expectError bool
	}{
		{
			name: "new-style secret dockerconfigjson",
			inputSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pull-secret",
				},
				Data: map[string][]byte{
					corev1.DockerConfigJsonKey: []byte(newSecret),
				},
				Type: corev1.SecretTypeDockerConfigJson,
			},
		},
		{
			name: "new-style secret dockercfg",
			inputSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pull-secret",
				},
				Data: map[string][]byte{
					corev1.DockerConfigKey: []byte(newSecret),
				},
				Type: corev1.SecretTypeDockercfg,
			},
		},
		{
			name: "legacy secret dockercfg",
			inputSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pull-secret",
				},
				Data: map[string][]byte{
					corev1.DockerConfigKey: []byte(legacySecret),
				},
				Type: corev1.SecretTypeDockercfg,
			},
		},
		{
			name: "legacy secret dockerconfigjson",
			inputSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pull-secret",
				},
				Data: map[string][]byte{
					corev1.DockerConfigJsonKey: []byte(legacySecret),
				},
				Type: corev1.SecretTypeDockerConfigJson,
			},
		},
		{
			name: "empty secret",
			inputSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pull-secret",
				},
				Data: map[string][]byte{
					corev1.DockerConfigKey: {},
				},
				Type: corev1.SecretTypeDockercfg,
			},
			expectError: true,
		},
		{
			name: "unknown key secret",
			inputSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pull-secret",
				},
				Data: map[string][]byte{
					"unknown-key": []byte(newSecret),
				},
			},
			expectError: true,
		},
		{
			name: "unknown secret type",
			inputSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pull-secret",
				},
				Data: map[string][]byte{
					corev1.DockerConfigKey: []byte(newSecret),
				},
				Type: corev1.SecretTypeOpaque,
			},
			expectError: true,
		},
	}

	t.Run("Dockercfg Bytes", func(t *testing.T) {
		normalized, err := NormalizeDockerConfigJSONSecret([]byte(legacySecret))
		assert.NoError(t, err)

		_, ok := normalized.Data[corev1.DockerConfigJsonKey]
		assert.True(t, ok)

		assert.Equal(t, normalized.Type, corev1.SecretTypeDockerConfigJson)
		assert.JSONEq(t, string(newSecret), string(normalized.Data[corev1.DockerConfigJsonKey]))
	})

	t.Run("DockerConfigJSON Bytes", func(t *testing.T) {
		normalized, err := NormalizeDockercfgSecret([]byte(newSecret))
		assert.NoError(t, err)

		_, ok := normalized.Data[corev1.DockerConfigKey]
		assert.True(t, ok)

		assert.Equal(t, normalized.Type, corev1.SecretTypeDockercfg)
		assert.JSONEq(t, string(legacySecret), string(normalized.Data[corev1.DockerConfigKey]))
	})

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Run("DockerConfigJSON", func(t *testing.T) {
				normalized, err := NormalizeDockerConfigJSONSecret(testCase.inputSecret)
				if testCase.expectError {
					assert.Error(t, err)
					return
				}

				_, ok := normalized.Data[corev1.DockerConfigJsonKey]
				assert.True(t, ok)

				assert.Equal(t, normalized.Type, corev1.SecretTypeDockerConfigJson)
				assert.JSONEq(t, string(newSecret), string(normalized.Data[corev1.DockerConfigJsonKey]))
			})

			t.Run("Dockercfg", func(t *testing.T) {
				normalized, err := NormalizeDockercfgSecret(testCase.inputSecret)
				if testCase.expectError {
					assert.Error(t, err)
					return
				}

				_, ok := normalized.Data[corev1.DockerConfigKey]
				assert.True(t, ok)

				assert.Equal(t, normalized.Type, corev1.SecretTypeDockercfg)
				assert.JSONEq(t, string(legacySecret), string(normalized.Data[corev1.DockerConfigKey]))
			})
		})
	}
}
