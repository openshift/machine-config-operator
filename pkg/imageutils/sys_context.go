package imageutils

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/containers/image/v5/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/secrets"
	corev1 "k8s.io/api/core/v1"
)

// SysContext wraps types.SystemContext and manages cleanup of temporary files.
type SysContext struct {
	SysContext  *types.SystemContext
	temporalDir string
}

// NewSysContextFromControllerConfig creates a SysContext with authentication and certificates
// from the provided secret and ControllerConfig. Caller must call Cleanup() method to remove temporary files.
func NewSysContextFromControllerConfig(secret *corev1.Secret, cc *mcfgv1.ControllerConfig) (*SysContext, error) {
	temporalDir, err := os.MkdirTemp("", "syscontext-temporal-dir")
	if err != nil {
		return nil, fmt.Errorf("could not create SysContext temp dir: %w", err)
	}
	sysContext := &SysContext{
		temporalDir: temporalDir,
	}

	certsDir, err := sysContext.buildCertsFromControllerConfig(cc)
	if err != nil {
		return nil, fmt.Errorf("could not prepare certs: %w", err)
	}

	authfilePath, err := sysContext.buildAuthFileFromSecret(secret)
	if err != nil {
		return nil, fmt.Errorf("could not get authfile path for secret %s: %w", secret.Name, err)
	}
	sysContext.SysContext = &types.SystemContext{
		AuthFilePath:             authfilePath,
		DockerPerHostCertDirPath: certsDir,
	}
	return sysContext, nil
}

// Cleanup removes all temporary files and directories created by NewSysContextFromControllerConfig.
func (s *SysContext) Cleanup() error {
	if err := os.RemoveAll(s.temporalDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("could not clean up SysContext temporal directory %s: %w", s.temporalDir, err)
	}
	return nil
}

// buildCertsFromControllerConfig writes image registry certificates from the ControllerConfig to the temporal directory.
func (s *SysContext) buildCertsFromControllerConfig(cc *mcfgv1.ControllerConfig) (string, error) {
	certsDir := filepath.Join(s.temporalDir, "certs-dir")
	for _, irb := range cc.Spec.ImageRegistryBundleData {
		if err := writeCertFromImageRegistryBundle(certsDir, irb); err != nil {
			return "", fmt.Errorf("could not write image registry bundle from ImageRegistryBundleData: %w", err)
		}
	}

	for _, irb := range cc.Spec.ImageRegistryBundleUserData {
		if err := writeCertFromImageRegistryBundle(certsDir, irb); err != nil {
			return "", fmt.Errorf("could not write image registry bundle from ImageRegistryBundleUserData: %w", err)
		}
	}
	return certsDir, nil
}

// writeCertFromImageRegistryBundle writes a certificate from an image registry bundle
// to the specified certificates directory. Path traversal attempts (..) are sanitized to colons.
func writeCertFromImageRegistryBundle(certsDir string, irb mcfgv1.ImageRegistryBundle) error {
	caFile := strings.ReplaceAll(irb.File, "..", ":")

	certDir := filepath.Join(certsDir, caFile)

	if err := os.MkdirAll(certDir, 0o755); err != nil {
		return fmt.Errorf("could not create cert dir %q: %w", certDir, err)
	}

	certFile := filepath.Join(certDir, "ca.crt")

	if err := os.WriteFile(certFile, irb.Data, 0o644); err != nil {
		return fmt.Errorf("could not write cert file %q: %w", certFile, err)
	}

	return nil
}

// buildAuthFileFromSecret writes the Docker secret as authfile.json to the temporal directory.
func (s *SysContext) buildAuthFileFromSecret(secret *corev1.Secret) (string, error) {
	authfilePath := filepath.Join(s.temporalDir, "authfile.json")

	is, err := secrets.NewImageRegistrySecret(secret)
	if err != nil {
		return "", fmt.Errorf("could not create an ImageRegistrySecret for '%s/%s': %w", secret.Namespace, secret.Name, err)
	}

	secretBytes, err := is.JSONBytes(corev1.SecretTypeDockerConfigJson)
	if err != nil {
		return "", fmt.Errorf("could not normalize secret '%s/%s' to %s: %w", secret.Namespace, secret.Name, corev1.SecretTypeDockerConfigJson, err)
	}

	if err := os.WriteFile(authfilePath, secretBytes, 0o644); err != nil {
		return "", fmt.Errorf("could not write temp authfile %q for secret %q: %w", authfilePath, secret.Name, err)
	}

	return authfilePath, nil
}
