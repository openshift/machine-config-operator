package internalreleaseimage

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMergeIRIAuthIntoPullSecret(t *testing.T) {
	basePullSecret := `{"auths":{"quay.io":{"auth":"dGVzdDp0ZXN0"}}}`

	tests := []struct {
		name           string
		pullSecret     string
		password       string
		baseDomain     string
		expectChanged  bool
		expectError    bool
		verifyAuthHost string
	}{
		{
			name:           "adds IRI auth entry",
			pullSecret:     basePullSecret,
			password:       "testpassword",
			baseDomain:     "example.com",
			expectChanged:  true,
			verifyAuthHost: "api-int.example.com:22625",
		},
		{
			name:          "empty password returns unchanged",
			pullSecret:    basePullSecret,
			password:      "",
			baseDomain:    "example.com",
			expectChanged: false,
		},
		{
			name:          "already up-to-date returns unchanged",
			pullSecret:    pullSecretWithIRIAuth("example.com", "testpassword"),
			password:      "testpassword",
			baseDomain:    "example.com",
			expectChanged: false,
		},
		{
			name:           "updates stale entry",
			pullSecret:     pullSecretWithIRIAuth("example.com", "oldpassword"),
			password:       "newpassword",
			baseDomain:     "example.com",
			expectChanged:  true,
			verifyAuthHost: "api-int.example.com:22625",
		},
		{
			name:        "invalid JSON returns error",
			pullSecret:  "not-json",
			password:    "testpassword",
			baseDomain:  "example.com",
			expectError: true,
		},
		{
			name:        "missing auths field returns error",
			pullSecret:  `{"registry":"quay.io"}`,
			password:    "testpassword",
			baseDomain:  "example.com",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := MergeIRIAuthIntoPullSecret([]byte(tt.pullSecret), tt.password, tt.baseDomain)

			if tt.expectError {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)

			if !tt.expectChanged {
				assert.Equal(t, tt.pullSecret, string(result), "pull secret should not change")
				return
			}

			// Verify the IRI auth entry was added
			var dockerConfig map[string]interface{}
			err = json.Unmarshal(result, &dockerConfig)
			assert.NoError(t, err)

			auths := dockerConfig["auths"].(map[string]interface{})
			iriEntry, ok := auths[tt.verifyAuthHost].(map[string]interface{})
			assert.True(t, ok, "IRI auth entry should be present")

			expectedAuth := base64.StdEncoding.EncodeToString([]byte("openshift:" + tt.password))
			assert.Equal(t, expectedAuth, iriEntry["auth"])

			// Verify original entries are preserved
			_, hasQuay := auths["quay.io"]
			assert.True(t, hasQuay, "original quay.io entry should be preserved")
		})
	}
}

func TestGenerateHtpasswdEntry(t *testing.T) {
	entry, err := GenerateHtpasswdEntry("openshift", "testpassword")
	assert.NoError(t, err)
	assert.True(t, HtpasswdMatchesPassword(entry, "openshift", "testpassword"))
	assert.False(t, HtpasswdMatchesPassword(entry, "openshift", "wrongpassword"))
}

func TestHtpasswdMatchesPassword(t *testing.T) {
	entry, err := GenerateHtpasswdEntry("openshift", "mypassword")
	assert.NoError(t, err)

	assert.True(t, HtpasswdMatchesPassword(entry, "openshift", "mypassword"), "correct credentials should match")
	assert.False(t, HtpasswdMatchesPassword(entry, "openshift", "wrongpassword"), "wrong password should not match")
	assert.False(t, HtpasswdMatchesPassword(entry, "otheruser", "mypassword"), "wrong username should not match")
	assert.False(t, HtpasswdMatchesPassword("", "openshift", "mypassword"), "empty htpasswd should not match")
}

// pullSecretWithIRIAuth creates a pull secret JSON that already contains an IRI auth entry.
func pullSecretWithIRIAuth(baseDomain string, password string) string {
	authValue := base64.StdEncoding.EncodeToString([]byte("openshift:" + password))
	host := "api-int." + baseDomain + ":22625"
	dockerConfig := map[string]interface{}{
		"auths": map[string]interface{}{
			"quay.io": map[string]interface{}{
				"auth": "dGVzdDp0ZXN0",
			},
			host: map[string]interface{}{
				"auth": authValue,
			},
		},
	}
	b, _ := json.Marshal(dockerConfig)
	return string(b)
}
