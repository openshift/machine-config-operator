package imageutils

import (
	"path/filepath"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNewSysContextFromControllerConfig(t *testing.T) {
	legacySecret := `{"registry.hostname.com": {"username": "user", "password": "s3kr1t", "auth": "s00pers3kr1t", "email": "user@hostname.com"}}`
	newSecret := `{"auths":` + legacySecret + `}`

	testCert := []byte(`-----BEGIN CERTIFICATE-----
MIICljCCAX4CCQCKz8Vz4VR5+jANBgkqhkiG9w0BAQsFADANMQswCQYDVQQGEwJV
UzAeFw0yMDAxMDEwMDAwMDBaFw0zMDAxMDEwMDAwMDBaMA0xCzAJBgNVBAYTAlVT
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAuOSW8w==
-----END CERTIFICATE-----`)

	testCases := []struct {
		name   string
		secret *corev1.Secret
	}{
		{
			name: "DockerConfigJSON",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pull-secret",
					Namespace: "test-namespace",
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{
					corev1.DockerConfigJsonKey: []byte(newSecret),
				},
			},
		},
		{
			name: "Dockercfg",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pull-secret",
					Namespace: "test-namespace",
				},
				Type: corev1.SecretTypeDockercfg,
				Data: map[string][]byte{
					corev1.DockerConfigKey: []byte(legacySecret),
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cc := &mcfgv1.ControllerConfig{
				Spec: mcfgv1.ControllerConfigSpec{
					ImageRegistryBundleData: []mcfgv1.ImageRegistryBundle{
						{
							File: "registry.hostname.com",
							Data: testCert,
						},
					},
				},
			}

			sysCtx, err := NewSysContextFromControllerConfig(tc.secret, cc)
			require.NoError(t, err, "NewSysContextFromControllerConfig should not fail")
			require.NotNil(t, sysCtx, "SysContext wrapper should not be nil")
			require.NotNil(t, sysCtx.SysContext, "Underlying SystemContext should not be nil")

			// Check that temporal directory was created
			assert.NotEmpty(t, sysCtx.temporalDir, "Temporal directory should not be empty")
			assert.DirExists(t, sysCtx.temporalDir, "Temporal directory should exist")

			// Check that AuthFilePath was set and file exists
			assert.NotEmpty(t, sysCtx.SysContext.AuthFilePath, "AuthFilePath should not be empty")
			assert.FileExists(t, sysCtx.SysContext.AuthFilePath, "AuthFile should exist")
			assert.Contains(t, sysCtx.SysContext.AuthFilePath, "authfile.json", "AuthFile should be named authfile.json")

			// Check that DockerPerHostCertDirPath was set and directory exists
			assert.NotEmpty(t, sysCtx.SysContext.DockerPerHostCertDirPath, "DockerPerHostCertDirPath should not be empty")
			assert.DirExists(t, sysCtx.SysContext.DockerPerHostCertDirPath, "Certs directory should exist")

			// Check that cert file was created
			certFile := filepath.Join(sysCtx.SysContext.DockerPerHostCertDirPath, "registry.hostname.com", "ca.crt")
			assert.FileExists(t, certFile, "Cert file should exist")

			// Proxy should be nil when not configured
			assert.Nil(t, sysCtx.SysContext.DockerProxyURL, "DockerProxyURL should be nil when no proxy is configured")

			// Cleanup
			err = sysCtx.Cleanup()
			require.NoError(t, err, "Cleanup should not fail")

			// Verify cleanup removed the entire temporal directory
			assert.NoDirExists(t, sysCtx.temporalDir, "Temporal directory should be removed after cleanup")
		})
	}
}

func TestNewSysContextFromControllerConfigWithProxy(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pull-secret",
			Namespace: "test-namespace",
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: []byte(`{"auths":{"registry.hostname.com": {"username": "user", "password": "s3kr1t"}}}`),
		},
	}

	testCases := []struct {
		name           string
		httpProxy      string
		httpsProxy     string
		expectedScheme string
		expectedHost   string
	}{
		{
			name:           "HTTPS proxy only",
			httpsProxy:     "proxy.example.com:3128",
			expectedScheme: "https",
			expectedHost:   "proxy.example.com:3128",
		},
		{
			name:           "HTTP proxy only",
			httpProxy:      "proxy.example.com:8080",
			expectedScheme: "http",
			expectedHost:   "proxy.example.com:8080",
		},
		{
			name:           "Both proxies - HTTPS preferred",
			httpProxy:      "http-proxy.example.com:8080",
			httpsProxy:     "https-proxy.example.com:3128",
			expectedScheme: "https",
			expectedHost:   "https-proxy.example.com:3128",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cc := &mcfgv1.ControllerConfig{
				Spec: mcfgv1.ControllerConfigSpec{
					Proxy: &configv1.ProxyStatus{
						HTTPProxy:  tc.httpProxy,
						HTTPSProxy: tc.httpsProxy,
					},
				},
			}

			sysCtx, err := NewSysContextFromControllerConfig(secret, cc)
			require.NoError(t, err, "NewSysContextFromControllerConfig should not fail")
			require.NotNil(t, sysCtx, "SysContext wrapper should not be nil")
			require.NotNil(t, sysCtx.SysContext, "Underlying SystemContext should not be nil")

			// Check that proxy was set correctly
			require.NotNil(t, sysCtx.SysContext.DockerProxyURL, "DockerProxyURL should not be nil")
			assert.Equal(t, tc.expectedScheme, sysCtx.SysContext.DockerProxyURL.Scheme, "Proxy scheme should match")
			assert.Equal(t, tc.expectedHost, sysCtx.SysContext.DockerProxyURL.Host, "Proxy host should match")

			// Cleanup
			err = sysCtx.Cleanup()
			require.NoError(t, err, "Cleanup should not fail")
		})
	}
}
