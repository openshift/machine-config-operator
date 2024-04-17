package build

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestValidateImagePullspecHasDigest(t *testing.T) {
	cm := getOSImageURLConfigMap()

	validPullspecs := []string{
		cm.Data[baseOSContainerImageConfigKey],
		cm.Data[baseOSExtensionsContainerImageConfigKey],
	}

	for _, pullspec := range validPullspecs {
		assert.NoError(t, validateImageHasDigestedPullspec(pullspec))
	}

	invalidPullspecs := []string{
		expectedImagePullspecWithTag,
	}

	for _, pullspec := range invalidPullspecs {
		assert.Error(t, validateImageHasDigestedPullspec(pullspec))
	}
}

// Tests that a given image pullspec with a tag and SHA is correctly substituted.
func TestParseImagePullspec(t *testing.T) {
	t.Parallel()

	out, err := ParseImagePullspec(expectedImagePullspecWithTag, expectedImageSHA)
	assert.NoError(t, err)
	assert.Equal(t, expectedImagePullspecWithSHA, out)
}

// Tests that pull secrets are canonicalized. In other words, converted from
// the legacy-style pull secret to the new-style secret.
func TestCanonicalizePullSecret(t *testing.T) {
	t.Parallel()

	legacySecret := `{"registry.hostname.com": {"username": "user", "password": "s3kr1t", "auth": "s00pers3kr1t", "email": "user@hostname.com"}}`

	newSecret := `{"auths":` + legacySecret + `}`

	testCases := []struct {
		name            string
		inputSecret     *corev1.Secret
		expectCanonical bool
		expectError     bool
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
			expectCanonical: false,
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
			expectCanonical: false,
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
			expectCanonical: true,
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
			expectCanonical: true,
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

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			out, err := canonicalizePullSecret(testCase.inputSecret)
			if testCase.expectError {
				assert.Error(t, err)
				return
			} else {
				assert.NoError(t, err)
			}

			if testCase.expectCanonical {
				assert.Contains(t, out.Name, "canonical")
			}

			for _, val := range out.Data {
				assert.JSONEq(t, newSecret, string(val))
			}
		})
	}
}
