package common

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func newIRIRegistryCredentialsSecret(password string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      InternalReleaseImageAuthSecretName,
			Namespace: MCONamespace,
		},
		Data: map[string][]byte{
			"password": []byte(password),
		},
	}
}

func cconfigWithDNS(baseDomain string) *mcfgv1.ControllerConfig {
	return &mcfgv1.ControllerConfig{
		Spec: mcfgv1.ControllerConfigSpec{
			DNS: &configv1.DNS{
				Spec: configv1.DNSSpec{BaseDomain: baseDomain},
			},
		},
	}
}

func TestMergeIRIRegistryCredentials(t *testing.T) {
	basePullSecret := `{"auths":{"quay.io":{"auth":"dGVzdDp0ZXN0"}}}`

	tests := []struct {
		name           string
		pullSecret     string
		secret         *corev1.Secret
		cconfig        *mcfgv1.ControllerConfig
		expectUnchanged bool
		expectError    bool
		verifyAuthHost string
	}{
		{
			name:           "adds IRI auth entry",
			pullSecret:     basePullSecret,
			secret:         newIRIRegistryCredentialsSecret("testpassword"),
			cconfig:        cconfigWithDNS("example.com"),
			verifyAuthHost: "api-int.example.com:22625",
		},
		{
			name:            "nil secret returns pull secret unchanged",
			pullSecret:      basePullSecret,
			secret:          nil,
			cconfig:         cconfigWithDNS("example.com"),
			expectUnchanged: true,
		},
		{
			name:            "nil DNS returns pull secret unchanged",
			pullSecret:      basePullSecret,
			secret:          newIRIRegistryCredentialsSecret("testpassword"),
			cconfig:         &mcfgv1.ControllerConfig{},
			expectUnchanged: true,
		},
		{
			name:        "empty password returns error",
			pullSecret:  basePullSecret,
			secret:      newIRIRegistryCredentialsSecret(""),
			cconfig:     cconfigWithDNS("example.com"),
			expectError: true,
		},
		{
			name:            "already up-to-date returns unchanged",
			pullSecret:      pullSecretWithIRIRegistryCredentials("example.com", "testpassword"),
			secret:          newIRIRegistryCredentialsSecret("testpassword"),
			cconfig:         cconfigWithDNS("example.com"),
			expectUnchanged: true,
		},
		{
			name:           "updates stale entry",
			pullSecret:     pullSecretWithIRIRegistryCredentials("example.com", "oldpassword"),
			secret:         newIRIRegistryCredentialsSecret("newpassword"),
			cconfig:        cconfigWithDNS("example.com"),
			verifyAuthHost: "api-int.example.com:22625",
		},
		{
			name:        "invalid JSON returns error",
			pullSecret:  "not-json",
			secret:      newIRIRegistryCredentialsSecret("testpassword"),
			cconfig:     cconfigWithDNS("example.com"),
			expectError: true,
		},
		{
			name:        `missing "auths" field returns error`,
			pullSecret:  `{"registry":"quay.io"}`,
			secret:      newIRIRegistryCredentialsSecret("testpassword"),
			cconfig:     cconfigWithDNS("example.com"),
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := MergeIRIRegistryCredentials([]byte(tt.pullSecret), tt.secret, tt.cconfig)

			if tt.expectError {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)

			if tt.expectUnchanged {
				assert.Equal(t, tt.pullSecret, string(result), "pull secret should not change")
				return
			}

			// Verify the IRI auth entries were added.
			var dockerConfig map[string]interface{}
			err = json.Unmarshal(result, &dockerConfig)
			assert.NoError(t, err)

			auths := dockerConfig["auths"].(map[string]interface{})
			password := string(tt.secret.Data["password"])
			expectedAuth := base64.StdEncoding.EncodeToString([]byte("openshift:" + password))

			iriEntry, ok := auths[tt.verifyAuthHost].(map[string]interface{})
			assert.True(t, ok, "IRI auth entry should be present for %s", tt.verifyAuthHost)
			assert.Equal(t, expectedAuth, iriEntry["auth"])

			// Verify localhost entry is also present for master nodes.
			localEntry, ok := auths[fmt.Sprintf("localhost:%d", IRIRegistryPort)].(map[string]interface{})
			assert.True(t, ok, "IRI auth entry should be present for localhost:%d", IRIRegistryPort)
			assert.Equal(t, expectedAuth, localEntry["auth"])

			_, hasQuay := auths["quay.io"]
			assert.True(t, hasQuay, "original quay.io entry should be preserved")
		})
	}
}

// pullSecretWithIRIRegistryCredentials creates a pull secret JSON that already contains an IRI auth entry.
func pullSecretWithIRIRegistryCredentials(baseDomain string, password string) string {
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
