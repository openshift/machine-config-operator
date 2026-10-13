package imageutils

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testDockerConfigJSON = `{"auths":{"registry.hostname.com": {"username": "user", "password": "s3kr1t", "auth": "s00pers3kr1t", "email": "user@hostname.com"}}}`

	testDockerConfig = `{"registry.hostname.com": {"username": "user", "password": "s3kr1t", "auth": "s00pers3kr1t", "email": "user@hostname.com"}}`

	testCert1 = `-----BEGIN CERTIFICATE-----
MIICljCCAX4CCQCKz8Vz4VR5+jANBgkqhkiG9w0BAQsFADANMQswCQYDVQQGEwJV
UzAeFw0yMDAxMDEwMDAwMDBaFw0zMDAxMDEwMDAwMDBaMA0xCzAJBgNVBAYTAlVT
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAuOSW8w==
-----END CERTIFICATE-----`

	testCert2 = `-----BEGIN CERTIFICATE-----
MIIDXTCCAkWgAwIBAgIJANXKLQzOJAoiMA0GCSqGSIb3DQEBCwUAMEUxCzAJBgNV
BAYTAkFVMRMwEQYDVQQIDApTb21lLVN0YXRlMSEwHwYDVQQKDBhJbnRlcm5ldCBX
aWRnaXRzIFB0eSBMdGQwHhcNMjAwMTAxMDAwMDAwWhcNMzAwMTAxMDAwMDAwWjBF
MQswCQYDVQQGEwJBVTETMBEGA1UECAwKU29tZS1TdGF0ZTEhMB8GA1UECgwYSW50
-----END CERTIFICATE-----`

	testRegistriesConf = `unqualified-search-registries = ["registry.access.redhat.com"]`
)

func TestNewSysContextFromFilesystem_Success(t *testing.T) {
	testCases := []struct {
		name             string
		setupPaths       func(t *testing.T, tmpDir string) SysContextPaths
		expectTempDir    bool
		expectAuthFile   bool
		expectCerts      bool
		expectProxy      bool
		expectRegistries bool
	}{
		{
			name: "Empty SysContextPaths",
			setupPaths: func(t *testing.T, tmpDir string) SysContextPaths {
				return SysContextPaths{}
			},
			expectTempDir: false,
		},
		{
			name: "Only PullSecret",
			setupPaths: func(t *testing.T, tmpDir string) SysContextPaths {
				return SysContextPaths{
					PullSecret: createPullSecretFile(t, tmpDir, testDockerConfigJSON),
				}
			},
			expectTempDir:  true,
			expectAuthFile: true,
		},
		{
			name: "Only AdditionalTrustBundle",
			setupPaths: func(t *testing.T, tmpDir string) SysContextPaths {
				return SysContextPaths{
					AdditionalTrustBundles: []string{createAdditionalTrustBundleFile(t, tmpDir, testCert1)},
				}
			},
			expectTempDir: true, // Temp dir created when AdditionalTrustBundle is processed
		},
		{
			name: "Only PerHostCertificates",
			setupPaths: func(t *testing.T, tmpDir string) SysContextPaths {
				certs := map[string]string{
					"registry1.example.com.crt": testCert1,
				}
				return SysContextPaths{
					PerHostCertDir: createPerHostCertificatesDir(t, tmpDir, certs),
				}
			},
			expectTempDir: true, // Temp dir created when PerHostCertificates is processed
			expectCerts:   true,
		},
		{
			name: "Only Proxy",
			setupPaths: func(t *testing.T, tmpDir string) SysContextPaths {
				return SysContextPaths{
					Proxy: &configv1.ProxyStatus{
						HTTPSProxy: "https://proxy.example.com:3128",
					},
				}
			},
			expectTempDir: false,
			expectProxy:   true,
		},
		{
			name: "Only RegistryConfig",
			setupPaths: func(t *testing.T, tmpDir string) SysContextPaths {
				return SysContextPaths{
					RegistryConfig: createRegistryConfigFile(t, tmpDir),
				}
			},
			expectTempDir:    false,
			expectRegistries: true,
		},
		{
			name: "All fields populated",
			setupPaths: func(t *testing.T, tmpDir string) SysContextPaths {
				certs := map[string]string{
					"registry1.example.com.crt": testCert1,
				}
				return SysContextPaths{
					PullSecret:             createPullSecretFile(t, tmpDir, testDockerConfigJSON),
					AdditionalTrustBundles: []string{createAdditionalTrustBundleFile(t, tmpDir, testCert2)},
					PerHostCertDir:         createPerHostCertificatesDir(t, tmpDir, certs),
					Proxy: &configv1.ProxyStatus{
						HTTPSProxy: "https://proxy.example.com:3128",
					},
					RegistryConfig: createRegistryConfigFile(t, tmpDir),
				}
			},
			expectTempDir:    true,
			expectAuthFile:   true,
			expectCerts:      true,
			expectProxy:      true,
			expectRegistries: true,
		},
		{
			name: "PullSecret + Proxy",
			setupPaths: func(t *testing.T, tmpDir string) SysContextPaths {
				return SysContextPaths{
					PullSecret: createPullSecretFile(t, tmpDir, testDockerConfigJSON),
					Proxy: &configv1.ProxyStatus{
						HTTPProxy: "http://proxy.example.com:8080",
					},
				}
			},
			expectTempDir:  true,
			expectAuthFile: true,
			expectProxy:    true,
		},
		{
			name: "AdditionalTrustBundle + PerHostCertificates (triggers merging)",
			setupPaths: func(t *testing.T, tmpDir string) SysContextPaths {
				certs := map[string]string{
					"registry1.example.com.crt": testCert1,
				}
				return SysContextPaths{
					AdditionalTrustBundles: []string{createAdditionalTrustBundleFile(t, tmpDir, testCert2)},
					PerHostCertDir:         createPerHostCertificatesDir(t, tmpDir, certs),
				}
			},
			expectTempDir: true,
			expectCerts:   true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			paths := tc.setupPaths(t, tmpDir)

			sysCtx, err := NewSysContextFromFilesystem(paths)
			require.NoError(t, err, "NewSysContextFromFilesystem should not fail")
			require.NotNil(t, sysCtx, "SysContext wrapper should not be nil")
			require.NotNil(t, sysCtx.SysContext, "Underlying SystemContext should not be nil")

			// Check temp dir expectations
			if tc.expectTempDir {
				assert.NotEmpty(t, sysCtx.temporalDir, "Temporal directory should not be empty")
				assert.DirExists(t, sysCtx.temporalDir, "Temporal directory should exist")
			} else {
				assert.Empty(t, sysCtx.temporalDir, "Temporal directory should be empty")
			}

			// Check auth file expectations
			if tc.expectAuthFile {
				assert.NotEmpty(t, sysCtx.SysContext.AuthFilePath, "AuthFilePath should not be empty")
				assert.FileExists(t, sysCtx.SysContext.AuthFilePath, "AuthFile should exist")
			}

			// Check certs expectations
			if tc.expectCerts {
				// Either DockerPerHostCertDirPath or DockerCertPath should be set
				hasPerHostCerts := sysCtx.SysContext.DockerPerHostCertDirPath != ""
				hasCertPath := sysCtx.SysContext.DockerCertPath != ""
				assert.True(t, hasPerHostCerts || hasCertPath, "Either DockerPerHostCertDirPath or DockerCertPath should be set")
			}

			// Check proxy expectations
			if tc.expectProxy {
				assert.NotNil(t, sysCtx.SysContext.DockerProxyURL, "DockerProxyURL should not be nil")
			} else {
				assert.Nil(t, sysCtx.SysContext.DockerProxyURL, "DockerProxyURL should be nil")
			}

			// Check registry config expectations
			if tc.expectRegistries {
				assert.NotEmpty(t, sysCtx.SysContext.SystemRegistriesConfPath, "SystemRegistriesConfPath should not be empty")
				assert.Equal(t, paths.RegistryConfig, sysCtx.SysContext.SystemRegistriesConfPath, "SystemRegistriesConfPath should match provided path")
			}

			// Cleanup
			err = sysCtx.Cleanup()
			require.NoError(t, err, "Cleanup should not fail")

			// Verify cleanup removed temp directory if it existed
			if tc.expectTempDir {
				assert.NoDirExists(t, sysCtx.temporalDir, "Temporal directory should be removed after cleanup")
			}
		})
	}
}

func TestNewSysContextFromFilesystem_CertificateMerging(t *testing.T) {
	testCases := []struct {
		name                   string
		setupPaths             func(t *testing.T, tmpDir string) SysContextPaths
		expectMerged           bool
		expectDockerCertPath   bool
		expectPerHostCertPath  bool
		expectMergedBundleFile bool
	}{
		{
			name: "Both AdditionalTrustBundle AND PerHostCertificates present - expect merged",
			setupPaths: func(t *testing.T, tmpDir string) SysContextPaths {
				certs := map[string]string{
					"registry1.example.com.crt": testCert1,
					"registry2.example.com.crt": testCert2,
				}
				return SysContextPaths{
					AdditionalTrustBundles: []string{createAdditionalTrustBundleFile(t, tmpDir, testCert1)},
					PerHostCertDir:         createPerHostCertificatesDir(t, tmpDir, certs),
				}
			},
			expectMerged:           true,
			expectDockerCertPath:   true,
			expectPerHostCertPath:  true,
			expectMergedBundleFile: true,
		},
		{
			name: "Only AdditionalTrustBundle (no PerHostCertificates) - no merge",
			setupPaths: func(t *testing.T, tmpDir string) SysContextPaths {
				return SysContextPaths{
					AdditionalTrustBundles: []string{createAdditionalTrustBundleFile(t, tmpDir, testCert1)},
					CertDir:                filepath.Join(tmpDir, "certs"),
				}
			},
			expectMerged:          false,
			expectDockerCertPath:  true,
			expectPerHostCertPath: true, // DockerPerHostCertDirPath is always set when certs are processed
		},
		{
			name: "Only PerHostCertificates (no AdditionalTrustBundle) - no merge",
			setupPaths: func(t *testing.T, tmpDir string) SysContextPaths {
				certs := map[string]string{
					"registry1.example.com.crt": testCert1,
				}
				return SysContextPaths{
					PerHostCertDir: createPerHostCertificatesDir(t, tmpDir, certs),
				}
			},
			expectMerged:          false,
			expectDockerCertPath:  false,
			expectPerHostCertPath: true,
		},
		{
			name: "Empty AdditionalTrustBundle file with PerHostCertificates - no merge",
			setupPaths: func(t *testing.T, tmpDir string) SysContextPaths {
				certs := map[string]string{
					"registry1.example.com.crt": testCert1,
				}
				return SysContextPaths{
					AdditionalTrustBundles: []string{createAdditionalTrustBundleFile(t, tmpDir, "")},
					PerHostCertDir:         createPerHostCertificatesDir(t, tmpDir, certs),
				}
			},
			expectMerged:          false,
			expectDockerCertPath:  false,
			expectPerHostCertPath: true,
		},
		{
			name: "Multiple per-host certificates with AdditionalTrustBundle - merged",
			setupPaths: func(t *testing.T, tmpDir string) SysContextPaths {
				certs := map[string]string{
					"registry1.example.com.crt": testCert1,
					"registry2.example.com.crt": testCert2,
					"registry3.example.com.crt": testCert1,
				}
				return SysContextPaths{
					AdditionalTrustBundles: []string{createAdditionalTrustBundleFile(t, tmpDir, testCert2)},
					PerHostCertDir:         createPerHostCertificatesDir(t, tmpDir, certs),
				}
			},
			expectMerged:           true,
			expectDockerCertPath:   true,
			expectPerHostCertPath:  true,
			expectMergedBundleFile: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			paths := tc.setupPaths(t, tmpDir)

			sysCtx, err := NewSysContextFromFilesystem(paths)
			require.NoError(t, err, "NewSysContextFromFilesystem should not fail")
			require.NotNil(t, sysCtx, "SysContext wrapper should not be nil")

			// Check DockerCertPath
			if tc.expectDockerCertPath {
				assert.NotEmpty(t, sysCtx.SysContext.DockerCertPath, "DockerCertPath should not be empty")
				if tc.expectMerged {
					assert.DirExists(t, sysCtx.SysContext.DockerCertPath, "DockerCertPath should point to a directory when merged")
				}
			} else {
				if paths.CertDir != "" {
					assert.Equal(t, paths.CertDir, sysCtx.SysContext.DockerCertPath, "DockerCertPath should match provided Certificates path")
				} else {
					assert.Empty(t, sysCtx.SysContext.DockerCertPath, "DockerCertPath should be empty")
				}
			}

			// Check DockerPerHostCertDirPath
			if tc.expectPerHostCertPath {
				assert.NotEmpty(t, sysCtx.SysContext.DockerPerHostCertDirPath, "DockerPerHostCertDirPath should not be empty")
				if tc.expectMerged {
					assert.DirExists(t, sysCtx.SysContext.DockerPerHostCertDirPath, "DockerPerHostCertDirPath should exist when merged")
				} else {
					// When not merged, check if original path was provided
					if paths.PerHostCertDir != "" {
						assert.Equal(t, paths.PerHostCertDir, sysCtx.SysContext.DockerPerHostCertDirPath, "DockerPerHostCertDirPath should match provided path")
					} else {
						// If no original path provided, temp path is used when AdditionalTrustBundle is present
						assert.DirExists(t, sysCtx.SysContext.DockerPerHostCertDirPath, "DockerPerHostCertDirPath should exist")
					}
				}
			} else {
				if paths.PerHostCertDir == "" {
					assert.Empty(t, sysCtx.SysContext.DockerPerHostCertDirPath, "DockerPerHostCertDirPath should be empty")
				}
			}

			// Check for merged bundle file
			if tc.expectMergedBundleFile {
				mergedBundlePath := filepath.Join(sysCtx.SysContext.DockerCertPath, "ca-bundle.crt")
				assert.FileExists(t, mergedBundlePath, "Merged bundle file should exist")

				// Read and verify merged content contains certificates
				content, err := os.ReadFile(mergedBundlePath)
				require.NoError(t, err, "Should be able to read merged bundle file")
				assert.NotEmpty(t, content, "Merged bundle should not be empty")
				assert.Contains(t, string(content), "BEGIN CERTIFICATE", "Merged bundle should contain certificates")
			}

			// Cleanup
			err = sysCtx.Cleanup()
			require.NoError(t, err, "Cleanup should not fail")
		})
	}
}

func TestNewSysContextFromFilesystem_ErrorCases(t *testing.T) {
	testCases := []struct {
		name        string
		setupPaths  func(t *testing.T, tmpDir string) SysContextPaths
		expectError string
	}{
		{
			name: "Non-existent pull secret file",
			setupPaths: func(t *testing.T, tmpDir string) SysContextPaths {
				return SysContextPaths{
					PullSecret: filepath.Join(tmpDir, "nonexistent-pull-secret.json"),
				}
			},
			expectError: "could not load image pull secret",
		},
		{
			name: "Invalid JSON in pull secret file",
			setupPaths: func(t *testing.T, tmpDir string) SysContextPaths {
				secretPath := filepath.Join(tmpDir, "invalid-pull-secret.json")
				err := os.WriteFile(secretPath, []byte(`{invalid json`), 0644)
				require.NoError(t, err)
				return SysContextPaths{
					PullSecret: secretPath,
				}
			},
			expectError: "could not load image pull secret",
		},
		{
			name: "Empty pull secret file",
			setupPaths: func(t *testing.T, tmpDir string) SysContextPaths {
				secretPath := filepath.Join(tmpDir, "empty-pull-secret.json")
				err := os.WriteFile(secretPath, []byte(""), 0644)
				require.NoError(t, err)
				return SysContextPaths{
					PullSecret: secretPath,
				}
			},
			expectError: "could not load image pull secret",
		},
		{
			name: "Pull secret with null value",
			setupPaths: func(t *testing.T, tmpDir string) SysContextPaths {
				secretPath := filepath.Join(tmpDir, "null-pull-secret.json")
				err := os.WriteFile(secretPath, []byte(`null`), 0644)
				require.NoError(t, err)
				return SysContextPaths{
					PullSecret: secretPath,
				}
			},
			expectError: "could not load image pull secret",
		},
		{
			name: "Non-existent AdditionalTrustBundle file",
			setupPaths: func(t *testing.T, tmpDir string) SysContextPaths {
				return SysContextPaths{
					AdditionalTrustBundles: []string{filepath.Join(tmpDir, "nonexistent-trust-bundle.pem")},
				}
			},
			expectError: "could not create new controllerconfig",
		},
		{
			name: "Non-existent AdditionalTrustBundle file with PerHostCertificates",
			setupPaths: func(t *testing.T, tmpDir string) SysContextPaths {
				certs := map[string]string{
					"registry1.example.com.crt": testCert1,
				}
				return SysContextPaths{
					AdditionalTrustBundles: []string{filepath.Join(tmpDir, "nonexistent-trust-bundle.pem")},
					PerHostCertDir:         createPerHostCertificatesDir(t, tmpDir, certs),
				}
			},
			expectError: "could not create new controllerconfig",
		},
		{
			name: "Non-existent PerHostCertificates directory",
			setupPaths: func(t *testing.T, tmpDir string) SysContextPaths {
				return SysContextPaths{
					PerHostCertDir: filepath.Join(tmpDir, "nonexistent-certs-dir"),
				}
			},
			expectError: "could not create new controllerconfig",
		},
		{
			name: "Non-existent PerHostCertificates directory with AdditionalTrustBundle",
			setupPaths: func(t *testing.T, tmpDir string) SysContextPaths {
				return SysContextPaths{
					AdditionalTrustBundles: []string{createAdditionalTrustBundleFile(t, tmpDir, testCert1)},
					PerHostCertDir:         filepath.Join(tmpDir, "nonexistent-certs-dir"),
				}
			},
			expectError: "could not create new controllerconfig",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			paths := tc.setupPaths(t, tmpDir)

			sysCtx, err := NewSysContextFromFilesystem(paths)
			require.Error(t, err, "NewSysContextFromFilesystem should fail")
			assert.Contains(t, err.Error(), tc.expectError, "Error message should contain expected text")
			assert.Nil(t, sysCtx, "SysContext should be nil on error")
		})
	}
}

func TestNewSysContextFromFilesystem_PullSecretFormats(t *testing.T) {
	testCases := []struct {
		name         string
		secretFormat string
	}{
		{
			name:         "DockerConfigJSON format (modern)",
			secretFormat: testDockerConfigJSON,
		},
		{
			name:         "DockerConfig format (legacy)",
			secretFormat: testDockerConfig,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			paths := SysContextPaths{
				PullSecret: createPullSecretFile(t, tmpDir, tc.secretFormat),
			}

			sysCtx, err := NewSysContextFromFilesystem(paths)
			require.NoError(t, err, "NewSysContextFromFilesystem should not fail")
			require.NotNil(t, sysCtx, "SysContext wrapper should not be nil")

			// Auth file should be valid and exist
			assert.NotEmpty(t, sysCtx.SysContext.AuthFilePath, "AuthFilePath should not be empty")
			assert.FileExists(t, sysCtx.SysContext.AuthFilePath, "AuthFile should exist")

			// Read and verify auth file is valid JSON with "auths" key
			authContent, err := os.ReadFile(sysCtx.SysContext.AuthFilePath)
			require.NoError(t, err, "Should be able to read auth file")

			var authData map[string]interface{}
			err = json.Unmarshal(authContent, &authData)
			require.NoError(t, err, "Auth file should be valid JSON")
			assert.Contains(t, authData, "auths", "Auth file should have normalized 'auths' key")

			// Cleanup
			err = sysCtx.Cleanup()
			require.NoError(t, err, "Cleanup should not fail")
		})
	}
}

func TestNewSysContextFromFilesystem_Cleanup(t *testing.T) {
	testCases := []struct {
		name       string
		setupPaths func(t *testing.T, tmpDir string) SysContextPaths
	}{
		{
			name: "Cleanup with pull secret",
			setupPaths: func(t *testing.T, tmpDir string) SysContextPaths {
				return SysContextPaths{
					PullSecret: createPullSecretFile(t, tmpDir, testDockerConfigJSON),
				}
			},
		},
		{
			name: "Cleanup with merged certificates",
			setupPaths: func(t *testing.T, tmpDir string) SysContextPaths {
				certs := map[string]string{
					"registry1.example.com.crt": testCert1,
				}
				return SysContextPaths{
					AdditionalTrustBundles: []string{createAdditionalTrustBundleFile(t, tmpDir, testCert2)},
					PerHostCertDir:         createPerHostCertificatesDir(t, tmpDir, certs),
				}
			},
		},
		{
			name: "Cleanup with all configurations",
			setupPaths: func(t *testing.T, tmpDir string) SysContextPaths {
				certs := map[string]string{
					"registry1.example.com.crt": testCert1,
				}
				return SysContextPaths{
					PullSecret:             createPullSecretFile(t, tmpDir, testDockerConfigJSON),
					AdditionalTrustBundles: []string{createAdditionalTrustBundleFile(t, tmpDir, testCert2)},
					PerHostCertDir:         createPerHostCertificatesDir(t, tmpDir, certs),
					Proxy: &configv1.ProxyStatus{
						HTTPSProxy: "https://proxy.example.com:3128",
					},
					RegistryConfig: createRegistryConfigFile(t, tmpDir),
				}
			},
		},
		{
			name: "Cleanup with no temp directory",
			setupPaths: func(t *testing.T, tmpDir string) SysContextPaths {
				return SysContextPaths{
					Proxy: &configv1.ProxyStatus{
						HTTPSProxy: "https://proxy.example.com:3128",
					},
				}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			paths := tc.setupPaths(t, tmpDir)

			sysCtx, err := NewSysContextFromFilesystem(paths)
			require.NoError(t, err, "NewSysContextFromFilesystem should not fail")

			tempDir := sysCtx.temporalDir

			// First cleanup
			err = sysCtx.Cleanup()
			require.NoError(t, err, "First cleanup should not fail")

			// Verify temp directory is removed if it existed
			if tempDir != "" {
				assert.NoDirExists(t, tempDir, "Temp directory should be removed after cleanup")
			}

			// Second cleanup (idempotent - should not error)
			err = sysCtx.Cleanup()
			require.NoError(t, err, "Second cleanup should not fail (idempotent)")
		})
	}
}

func TestNewSysContextFromFilesystem_EdgeCases(t *testing.T) {
	t.Run("Empty PerHostCertificates directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		emptyDir := filepath.Join(tmpDir, "empty-certs")
		err := os.MkdirAll(emptyDir, 0755)
		require.NoError(t, err)

		paths := SysContextPaths{
			PerHostCertDir: emptyDir,
		}

		sysCtx, err := NewSysContextFromFilesystem(paths)
		require.NoError(t, err, "Should succeed with empty directory")
		require.NotNil(t, sysCtx, "SysContext should not be nil")
		assert.Equal(t, emptyDir, sysCtx.SysContext.DockerPerHostCertDirPath, "DockerPerHostCertDirPath should match provided path")

		err = sysCtx.Cleanup()
		require.NoError(t, err, "Cleanup should not fail")
	})

	t.Run("Nested certificate directory structure", func(t *testing.T) {
		tmpDir := t.TempDir()
		certs := map[string]string{
			"subdir1/registry1.example.com.crt": testCert1,
			"subdir1/registry2.example.com.crt": testCert2,
			"subdir2/registry3.example.com.crt": testCert1,
		}
		certsDir := createPerHostCertificatesDir(t, tmpDir, certs)

		paths := SysContextPaths{
			AdditionalTrustBundles: []string{createAdditionalTrustBundleFile(t, tmpDir, testCert2)},
			PerHostCertDir:         certsDir,
		}

		sysCtx, err := NewSysContextFromFilesystem(paths)
		require.NoError(t, err, "Should handle nested directories")
		require.NotNil(t, sysCtx, "SysContext should not be nil")

		// Verify per-host cert dir is set and exists
		assert.NotEmpty(t, sysCtx.SysContext.DockerPerHostCertDirPath, "DockerPerHostCertDirPath should be set")
		assert.DirExists(t, sysCtx.SysContext.DockerPerHostCertDirPath, "DockerPerHostCertDirPath should exist")

		err = sysCtx.Cleanup()
		require.NoError(t, err, "Cleanup should not fail")
	})

	t.Run("Both HTTP and HTTPS proxy set - HTTPS should take precedence", func(t *testing.T) {
		paths := SysContextPaths{
			Proxy: &configv1.ProxyStatus{
				HTTPProxy:  "http://http-proxy.example.com:8080",
				HTTPSProxy: "https://https-proxy.example.com:3128",
			},
		}

		sysCtx, err := NewSysContextFromFilesystem(paths)
		require.NoError(t, err, "Should not fail")
		require.NotNil(t, sysCtx, "SysContext should not be nil")
		require.NotNil(t, sysCtx.SysContext.DockerProxyURL, "DockerProxyURL should not be nil")

		assert.Equal(t, "https", sysCtx.SysContext.DockerProxyURL.Scheme, "HTTPS proxy should take precedence")
		assert.Equal(t, "https-proxy.example.com:3128", sysCtx.SysContext.DockerProxyURL.Host, "Proxy host should match HTTPS proxy")

		err = sysCtx.Cleanup()
		require.NoError(t, err, "Cleanup should not fail")
	})

	t.Run("Registry config path verification", func(t *testing.T) {
		tmpDir := t.TempDir()
		registryConfigPath := createRegistryConfigFile(t, tmpDir)

		paths := SysContextPaths{
			RegistryConfig: registryConfigPath,
		}

		sysCtx, err := NewSysContextFromFilesystem(paths)
		require.NoError(t, err, "Should not fail")
		require.NotNil(t, sysCtx, "SysContext should not be nil")

		assert.Equal(t, registryConfigPath, sysCtx.SysContext.SystemRegistriesConfPath, "SystemRegistriesConfPath should match provided path")

		err = sysCtx.Cleanup()
		require.NoError(t, err, "Cleanup should not fail")
	})

	t.Run("Certificate loading filters non-CA files and preserves relative paths", func(t *testing.T) {
		tmpDir := t.TempDir()
		certsDir := filepath.Join(tmpDir, "per-host-certs")
		err := os.MkdirAll(certsDir, 0755)
		require.NoError(t, err)

		// Create a mix of CA files and non-CA files
		files := map[string]string{
			"registry1.example.com.crt":         testCert1, // Should be included
			"registry2.example.com.pem":         testCert2, // Should be included
			"subdir/registry3.example.com.cert": testCert1, // Should be included (nested)
			"README.txt":                        "not a certificate", // Should be filtered out
			"config.json":                       `{"key": "value"}`,  // Should be filtered out
			".hidden":                           "hidden file",       // Should be filtered out
		}

		for filename, content := range files {
			certPath := filepath.Join(certsDir, filename)
			certSubdir := filepath.Dir(certPath)
			if certSubdir != certsDir {
				err := os.MkdirAll(certSubdir, 0755)
				require.NoError(t, err)
			}
			err := os.WriteFile(certPath, []byte(content), 0644)
			require.NoError(t, err)
		}

		paths := SysContextPaths{
			PerHostCertDir:         certsDir,
			AdditionalTrustBundles: []string{createAdditionalTrustBundleFile(t, tmpDir, testCert1)},
		}

		sysCtx, err := NewSysContextFromFilesystem(paths)
		require.NoError(t, err, "Should handle mixed file types")
		require.NotNil(t, sysCtx, "SysContext should not be nil")

		// Verify the per-host cert directory was processed
		assert.NotEmpty(t, sysCtx.SysContext.DockerPerHostCertDirPath, "DockerPerHostCertDirPath should be set")

		// Verify that only CA files were processed and relative paths were preserved
		// We can verify this by checking the created directory structure
		perHostDir := sysCtx.SysContext.DockerPerHostCertDirPath

		// Should have created directories for each CA file with relative paths
		assert.DirExists(t, filepath.Join(perHostDir, "registry1.example.com.crt"), "Should have created dir for .crt file")
		assert.DirExists(t, filepath.Join(perHostDir, "registry2.example.com.pem"), "Should have created dir for .pem file")
		assert.DirExists(t, filepath.Join(perHostDir, "subdir", "registry3.example.com.cert"), "Should have created nested dir for .cert file")

		// Should NOT have created directories for non-CA files
		assert.NoDirExists(t, filepath.Join(perHostDir, "README.txt"), "Should not have created dir for .txt file")
		assert.NoDirExists(t, filepath.Join(perHostDir, "config.json"), "Should not have created dir for .json file")
		assert.NoDirExists(t, filepath.Join(perHostDir, ".hidden"), "Should not have created dir for hidden file")

		err = sysCtx.Cleanup()
		require.NoError(t, err, "Cleanup should not fail")
	})
}

func createPullSecretFile(t *testing.T, dir string, content string) string {
	t.Helper()
	secretPath := filepath.Join(dir, "pull-secret.json")
	err := os.WriteFile(secretPath, []byte(content), 0644)
	require.NoError(t, err, "Failed to create pull secret file")
	return secretPath
}

func createAdditionalTrustBundleFile(t *testing.T, dir string, content string) string {
	t.Helper()
	bundlePath := filepath.Join(dir, "additional-trust-bundle.pem")
	err := os.WriteFile(bundlePath, []byte(content), 0644)
	require.NoError(t, err, "Failed to create additional trust bundle file")
	return bundlePath
}

func createPerHostCertificatesDir(t *testing.T, dir string, certs map[string]string) string {
	t.Helper()
	certsDir := filepath.Join(dir, "per-host-certs")
	err := os.MkdirAll(certsDir, 0755)
	require.NoError(t, err, "Failed to create per-host certificates directory")

	for filename, content := range certs {
		certPath := filepath.Join(certsDir, filename)
		// Handle nested directories
		certDir := filepath.Dir(certPath)
		if certDir != certsDir {
			err := os.MkdirAll(certDir, 0755)
			require.NoError(t, err, "Failed to create nested certificate directory")
		}
		err := os.WriteFile(certPath, []byte(content), 0644)
		require.NoError(t, err, "Failed to create certificate file")
	}

	return certsDir
}

func createRegistryConfigFile(t *testing.T, dir string) string {
	t.Helper()
	configPath := filepath.Join(dir, "registries.conf")
	err := os.WriteFile(configPath, []byte(testRegistriesConf), 0644)
	require.NoError(t, err, "Failed to create registry config file")
	return configPath
}
