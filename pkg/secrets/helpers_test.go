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

func TestDockerConfigJSONDecoder(t *testing.T) {
	legacySecret := `{"registry.hostname.com":{"username":"user","password":"s3kr1t","auth":"s00pers3kr1t","email":"user@hostname.com"}}`
	newSecret := `{"auths":` + legacySecret + `}`

	testCases := []struct {
		name        string
		input       []byte
		expected    *dockerConfigJSONDecoder
		expectError bool
	}{
		// Nil and Empty Input
		{
			name:        "nil bytes",
			input:       nil,
			expectError: true,
		},
		{
			name:        "zero-length byte slice",
			input:       []byte{},
			expectError: true,
		},
		{
			name:        "JSON null literal",
			input:       []byte("null"),
			expectError: true,
		},
		{
			name:        "JSON null with whitespace",
			input:       []byte("  null  "),
			expectError: true,
		},
		// Valid Empty Object
		{
			name:  "empty JSON object",
			input: []byte("{}"),
			expected: &dockerConfigJSONDecoder{
				imageRegistrySecretImpl: imageRegistrySecretImpl{
					cfg:           DockerConfigJSON{Auths: nil},
					isLegacyStyle: false,
				},
			},
		},
		// Invalid JSON
		{
			name:        "malformed JSON - unclosed brace",
			input:       []byte(`{"auths":`),
			expectError: true,
		},
		{
			name:        "malformed JSON - invalid syntax",
			input:       []byte(`{"auths": invalid}`),
			expectError: true,
		},
		{
			name:        "invalid JSON - array instead of object",
			input:       []byte(`["not", "an", "object"]`),
			expectError: true,
		},
		// Unknown Fields
		{
			name:        "unknown field in DockerConfigJSON format",
			input:       []byte(`{"auths":{},"unknownField":"value"}`),
			expectError: true,
		},
		{
			name:        "unknown field in DockerConfig format",
			input:       []byte(`{"registry.hostname.com":{"username":"user","unknownField":"value"}}`),
			expectError: true,
		},
		{
			name:        "unknown field in DockerConfigEntry",
			input:       []byte(`{"auths":{"registry.hostname.com":{"username":"user","unknownField":"value"}}}`),
			expectError: true,
		},
		// Valid DockerConfigJSON Format
		{
			name:  "DockerConfigJSON with empty auths",
			input: []byte(`{"auths":{}}`),
			expected: &dockerConfigJSONDecoder{
				imageRegistrySecretImpl: imageRegistrySecretImpl{
					cfg: DockerConfigJSON{
						Auths: DockerConfig{},
					},
					isLegacyStyle: false,
				},
			},
		},
		{
			name:  "DockerConfigJSON with single registry",
			input: []byte(newSecret),
			expected: &dockerConfigJSONDecoder{
				imageRegistrySecretImpl: imageRegistrySecretImpl{
					cfg: DockerConfigJSON{
						Auths: DockerConfig{
							"registry.hostname.com": {
								Username: "user",
								Password: "s3kr1t",
								Auth:     "s00pers3kr1t",
								Email:    "user@hostname.com",
							},
						},
					},
					isLegacyStyle: false,
				},
			},
		},
		{
			name: "DockerConfigJSON with multiple registries",
			input: []byte(`{"auths":{
				"registry1.com":{"username":"user1","password":"pass1","auth":"auth1","email":"user1@example.com"},
				"registry2.com":{"username":"user2","password":"pass2","auth":"auth2","email":"user2@example.com"}
			}}`),
			expected: &dockerConfigJSONDecoder{
				imageRegistrySecretImpl: imageRegistrySecretImpl{
					cfg: DockerConfigJSON{
						Auths: DockerConfig{
							"registry1.com": {
								Username: "user1",
								Password: "pass1",
								Auth:     "auth1",
								Email:    "user1@example.com",
							},
							"registry2.com": {
								Username: "user2",
								Password: "pass2",
								Auth:     "auth2",
								Email:    "user2@example.com",
							},
						},
					},
					isLegacyStyle: false,
				},
			},
		},
		{
			name:  "DockerConfigJSON with minimal fields",
			input: []byte(`{"auths":{"registry.example.com":{"auth":"dXNlcjpwYXNz"}}}`),
			expected: &dockerConfigJSONDecoder{
				imageRegistrySecretImpl: imageRegistrySecretImpl{
					cfg: DockerConfigJSON{
						Auths: DockerConfig{
							"registry.example.com": {
								Auth: "dXNlcjpwYXNz",
							},
						},
					},
					isLegacyStyle: false,
				},
			},
		},
		{
			name:  "DockerConfigJSON credHelpers are ignored",
			input: []byte(`{"auths":{"registry.example.com":{"auth":"dXNlcjpwYXNz"}}, "credHelpers": {"cred-helper-1": "helper"}}`),
			expected: &dockerConfigJSONDecoder{
				imageRegistrySecretImpl: imageRegistrySecretImpl{
					cfg: DockerConfigJSON{
						Auths: DockerConfig{
							"registry.example.com": {
								Auth: "dXNlcjpwYXNz",
							},
						},
					},
					isLegacyStyle: false,
				},
			},
		},
		// Valid DockerConfig Format
		{
			name:  "DockerConfig with single registry",
			input: []byte(legacySecret),
			expected: &dockerConfigJSONDecoder{
				imageRegistrySecretImpl: imageRegistrySecretImpl{
					cfg: DockerConfigJSON{
						Auths: DockerConfig{
							"registry.hostname.com": {
								Username: "user",
								Password: "s3kr1t",
								Auth:     "s00pers3kr1t",
								Email:    "user@hostname.com",
							},
						},
					},
					isLegacyStyle: true,
				},
			},
		},
		{
			name: "DockerConfig with multiple registries",
			input: []byte(`{
				"registry1.com":{"username":"user1","password":"pass1","auth":"auth1","email":"user1@example.com"},
				"registry2.com":{"username":"user2","password":"pass2","auth":"auth2","email":"user2@example.com"}
			}`),
			expected: &dockerConfigJSONDecoder{
				imageRegistrySecretImpl: imageRegistrySecretImpl{
					cfg: DockerConfigJSON{
						Auths: DockerConfig{
							"registry1.com": {
								Username: "user1",
								Password: "pass1",
								Auth:     "auth1",
								Email:    "user1@example.com",
							},
							"registry2.com": {
								Username: "user2",
								Password: "pass2",
								Auth:     "auth2",
								Email:    "user2@example.com",
							},
						},
					},
					isLegacyStyle: true,
				},
			},
		},
		{
			name:  "DockerConfig with minimal fields",
			input: []byte(`{"registry.example.com":{"auth":"dXNlcjpwYXNz"}}`),
			expected: &dockerConfigJSONDecoder{
				imageRegistrySecretImpl: imageRegistrySecretImpl{
					cfg: DockerConfigJSON{
						Auths: DockerConfig{
							"registry.example.com": {
								Auth: "dXNlcjpwYXNz",
							},
						},
					},
					isLegacyStyle: true,
				},
			},
		},
		// Malformed Structure
		{
			name:        "invalid DockerConfigEntry structure (string instead of object)",
			input:       []byte(`{"registry.hostname.com":"invalid-string-value"}`),
			expectError: true,
		},
		{
			name:        "invalid auth value type in legacy format",
			input:       []byte(`{"registry.hostname.com":{"auth":123}}`),
			expectError: true,
		},
		{
			name:        "auths field with invalid value type",
			input:       []byte(`{"auths":"invalid-string"}`),
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			d := &dockerConfigJSONDecoder{}
			err := d.UnmarshalJSON(tc.input)

			if tc.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tc.expected.isLegacyStyle, d.isLegacyStyle)
			assert.Equal(t, tc.expected.cfg.Auths, d.cfg.Auths)
		})
	}
}
