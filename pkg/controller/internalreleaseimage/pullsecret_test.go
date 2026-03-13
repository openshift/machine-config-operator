package internalreleaseimage

import (
	"encoding/base64"
	"encoding/json"
	"strings"
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

func TestExtractIRICredentialsFromPullSecret(t *testing.T) {
	tests := []struct {
		name             string
		pullSecret       string
		baseDomain       string
		expectedUsername string
		expectedPassword string
	}{
		{
			name:             "extracts credentials from valid entry",
			pullSecret:       pullSecretWithIRIAuth("example.com", "mypassword"),
			baseDomain:       "example.com",
			expectedUsername: "openshift",
			expectedPassword: "mypassword",
		},
		{
			name:             "extracts credentials with generation username",
			pullSecret:       pullSecretWithIRIAuthAndUsername("example.com", "openshift2", "mypassword"),
			baseDomain:       "example.com",
			expectedUsername: "openshift2",
			expectedPassword: "mypassword",
		},
		{
			name:             "returns empty for missing IRI entry",
			pullSecret:       `{"auths":{"quay.io":{"auth":"dGVzdDp0ZXN0"}}}`,
			baseDomain:       "example.com",
			expectedUsername: "",
			expectedPassword: "",
		},
		{
			name:             "returns empty for wrong domain",
			pullSecret:       pullSecretWithIRIAuth("other.com", "mypassword"),
			baseDomain:       "example.com",
			expectedUsername: "",
			expectedPassword: "",
		},
		{
			name:             "returns empty for invalid JSON",
			pullSecret:       "not-json",
			baseDomain:       "example.com",
			expectedUsername: "",
			expectedPassword: "",
		},
		{
			name:             "returns empty for missing auths",
			pullSecret:       `{"registry":"quay.io"}`,
			baseDomain:       "example.com",
			expectedUsername: "",
			expectedPassword: "",
		},
		{
			name:             "returns empty for invalid base64",
			pullSecret:       `{"auths":{"api-int.example.com:22625":{"auth":"!!!invalid"}}}`,
			baseDomain:       "example.com",
			expectedUsername: "",
			expectedPassword: "",
		},
		{
			name:             "returns empty for auth without colon",
			pullSecret:       `{"auths":{"api-int.example.com:22625":{"auth":"bm9jb2xvbg=="}}}`,
			baseDomain:       "example.com",
			expectedUsername: "",
			expectedPassword: "",
		},
		{
			name:             "handles password containing colon",
			pullSecret:       pullSecretWithIRIAuth("example.com", "pass:word"),
			baseDomain:       "example.com",
			expectedUsername: "openshift",
			expectedPassword: "pass:word",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			username, password := ExtractIRICredentialsFromPullSecret([]byte(tt.pullSecret), tt.baseDomain)
			assert.Equal(t, tt.expectedUsername, username)
			assert.Equal(t, tt.expectedPassword, password)
		})
	}
}

func TestNextIRIUsername(t *testing.T) {
	tests := []struct {
		current  string
		expected string
	}{
		{"openshift", "openshift1"},
		{"openshift1", "openshift2"},
		{"openshift2", "openshift3"},
		{"openshift99", "openshift100"},
		{"unknown", "openshift1"},
	}

	for _, tt := range tests {
		t.Run(tt.current+"->"+tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, NextIRIUsername(tt.current))
		})
	}
}

func TestHtpasswdHasValidEntry(t *testing.T) {
	entry, err := GenerateHtpasswdEntry("openshift", "mypassword")
	assert.NoError(t, err)

	dual, err := GenerateDualHtpasswd("openshift", "oldpass", "openshift1", "newpass")
	assert.NoError(t, err)

	tests := []struct {
		name     string
		htpasswd string
		username string
		password string
		expected bool
	}{
		{"valid single entry", entry, "openshift", "mypassword", true},
		{"wrong password", entry, "openshift", "wrongpassword", false},
		{"wrong username", entry, "other", "mypassword", false},
		{"dual entry matches first", dual, "openshift", "oldpass", true},
		{"dual entry matches second", dual, "openshift1", "newpass", true},
		{"dual entry wrong password", dual, "openshift1", "oldpass", false},
		{"empty htpasswd", "", "openshift", "mypassword", false},
		{"malformed line", "nocolon", "openshift", "mypassword", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, HtpasswdHasValidEntry(tt.htpasswd, tt.username, tt.password))
		})
	}
}

func TestGenerateHtpasswdEntry(t *testing.T) {
	entry, err := GenerateHtpasswdEntry("openshift", "testpassword")
	assert.NoError(t, err)
	assert.True(t, strings.HasPrefix(entry, "openshift:$2"), "should start with username and bcrypt prefix")
	assert.True(t, strings.HasSuffix(entry, "\n"), "should end with newline")
}

func TestGenerateDualHtpasswd(t *testing.T) {
	dual, err := GenerateDualHtpasswd("openshift", "oldpass", "openshift1", "newpass")
	assert.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(dual), "\n")
	assert.Equal(t, 2, len(lines), "should have two htpasswd entries")
	assert.True(t, strings.HasPrefix(lines[0], "openshift:$2"), "first entry should be current username")
	assert.True(t, strings.HasPrefix(lines[1], "openshift1:$2"), "second entry should be new username")
}

// pullSecretWithIRIAuth creates a pull secret JSON with an IRI auth entry using
// the default "openshift" username (generation 0).
func pullSecretWithIRIAuth(baseDomain string, password string) string {
	return pullSecretWithIRIAuthAndUsername(baseDomain, "openshift", password)
}

// pullSecretWithIRIAuthAndUsername creates a pull secret JSON with an IRI auth
// entry using the specified username.
func pullSecretWithIRIAuthAndUsername(baseDomain string, username string, password string) string {
	authValue := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
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
