package internalreleaseimage

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
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
			name:        "empty password returns error",
			pullSecret:  basePullSecret,
			password:    "",
			baseDomain:  "example.com",
			expectError: true,
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
			result, changed, err := MergeIRIAuthIntoPullSecret([]byte(tt.pullSecret), tt.password, tt.baseDomain)

			if tt.expectError {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)

			if !tt.expectChanged {
				assert.False(t, changed, "pull secret should not be marked changed")
				assert.Equal(t, tt.pullSecret, string(result), "pull secret should not change")
				return
			}
			assert.True(t, changed, "pull secret should be marked changed")

			// Verify the IRI auth entry was added
			var dockerConfig map[string]interface{}
			err = json.Unmarshal(result, &dockerConfig)
			assert.NoError(t, err)

			auths := dockerConfig["auths"].(map[string]interface{})
			iriEntry, ok := auths[tt.verifyAuthHost].(map[string]interface{})
			assert.True(t, ok, "IRI auth entry should be present for %s", tt.verifyAuthHost)

			expectedAuth := base64.StdEncoding.EncodeToString([]byte("openshift:" + tt.password))
			assert.Equal(t, expectedAuth, iriEntry["auth"])

			// Verify localhost entry is also present for master nodes where registries.conf
			// mirror rules use localhost:22625 instead of api-int.
			localEntry, ok := auths[fmt.Sprintf("localhost:%d", IRIRegistryPort)].(map[string]interface{})
			assert.True(t, ok, "IRI auth entry should be present for localhost:%d", IRIRegistryPort)
			assert.Equal(t, expectedAuth, localEntry["auth"])

			// Verify original entries are preserved
			_, hasQuay := auths["quay.io"]
			assert.True(t, hasQuay, "original quay.io entry should be preserved")
		})
	}
}

// pullSecretWithIRIAuth creates a pull secret JSON that already contains an IRI auth entry.
func pullSecretWithIRIAuth(baseDomain string, password string) string {
	authValue := base64.StdEncoding.EncodeToString([]byte("openshift:" + password))
	apiIntHost := fmt.Sprintf("api-int.%s:%d", baseDomain, IRIRegistryPort)
	localHost := fmt.Sprintf("localhost:%d", IRIRegistryPort)
	dockerConfig := map[string]interface{}{
		"auths": map[string]interface{}{
			"quay.io": map[string]interface{}{
				"auth": "dGVzdDp0ZXN0",
			},
			apiIntHost: map[string]interface{}{
				"auth": authValue,
			},
			localHost: map[string]interface{}{
				"auth": authValue,
			},
		},
	}
	b, _ := json.Marshal(dockerConfig)
	return string(b)
}
