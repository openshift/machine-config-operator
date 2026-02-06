package imageutils

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/containers/image/v5/pkg/sysregistriesv2"
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

// SysContextBuilder provides a fluent API for constructing SysContext instances.
type SysContextBuilder struct {
	secret           *corev1.Secret
	controllerConfig *mcfgv1.ControllerConfig
	registriesConfig *sysregistriesv2.V2RegistriesConf
}

// NewSysContextBuilder creates a new SysContextBuilder for building SysContext instances.
func NewSysContextBuilder() *SysContextBuilder {
	return &SysContextBuilder{}
}

// WithSecret adds authentication from a Kubernetes Secret to the SysContext.
func (b *SysContextBuilder) WithSecret(secret *corev1.Secret) *SysContextBuilder {
	b.secret = secret
	return b
}

// WithControllerConfig adds certificates and proxy settings from ControllerConfig to the SysContext.
func (b *SysContextBuilder) WithControllerConfig(cc *mcfgv1.ControllerConfig) *SysContextBuilder {
	b.controllerConfig = cc
	return b
}

// WithRegistriesConfig adds custom container registry configuration to the SysContext.
// The registries config will be written as a TOML file and used for registry lookups,
// mirrors, and pull policies.
func (b *SysContextBuilder) WithRegistriesConfig(registriesConfig *sysregistriesv2.V2RegistriesConf) *SysContextBuilder {
	b.registriesConfig = registriesConfig
	return b
}

// Build constructs the SysContext based on the configured options.
// Caller must call Cleanup() method to remove temporary files if any were created.
func (b *SysContextBuilder) Build() (*SysContext, error) {
	sysContext := &SysContext{
		SysContext: &types.SystemContext{},
	}

	// Only create temp dir if needed (for auth file, certs, or registries config)

	needsTempDir := b.secret != nil || b.hasCerts() || b.registriesConfig != nil
	if needsTempDir {
		temporalDir, err := os.MkdirTemp("", "syscontext-temporal-dir")
		if err != nil {
			return nil, fmt.Errorf("could not create SysContext temp dir: %w", err)
		}
		sysContext.temporalDir = temporalDir
	}

	if err := b.buildCerts(sysContext); err != nil {
		return nil, fmt.Errorf("could not build CA certs: %w", err)
	}

	if err := b.buildProxy(sysContext); err != nil {
		return nil, fmt.Errorf("could not build proxy: %w", err)
	}

	if err := b.buildAuth(sysContext); err != nil {
		return nil, fmt.Errorf("could not build auth: %w", err)
	}

	if err := b.buildRegistries(sysContext); err != nil {
		return nil, fmt.Errorf("could not build registries: %w", err)
	}

	return sysContext, nil
}

func (b *SysContextBuilder) hasCerts() bool {
	return b.controllerConfig != nil &&
		(len(b.controllerConfig.Spec.ImageRegistryBundleData) > 0 ||
			len(b.controllerConfig.Spec.ImageRegistryBundleUserData) > 0 ||
			len(b.controllerConfig.Spec.AdditionalTrustBundle) > 0)
}

// buildAuth configures authentication by writing the Docker secret as authfile.json
// to the temporal directory and setting AuthFilePath in the SystemContext.
// Returns early if no secret was configured via WithSecret.
func (b *SysContextBuilder) buildAuth(sysContext *SysContext) error {
	if b.secret == nil {
		return nil
	}

	authfilePath := filepath.Join(sysContext.temporalDir, "authfile.json")

	is, err := secrets.NewImageRegistrySecret(b.secret)
	if err != nil {
		return fmt.Errorf("could not create an ImageRegistrySecret for '%s/%s': %w", b.secret.Namespace, b.secret.Name, err)
	}

	secretBytes, err := is.JSONBytes(corev1.SecretTypeDockerConfigJson)
	if err != nil {
		return fmt.Errorf("could not normalize secret '%s/%s' to %s: %w", b.secret.Namespace, b.secret.Name, corev1.SecretTypeDockerConfigJson, err)
	}

	if err := os.WriteFile(authfilePath, secretBytes, 0o644); err != nil {
		return fmt.Errorf("could not write temp authfile %q for secret %q: %w", authfilePath, b.secret.Name, err)
	}

	sysContext.SysContext.AuthFilePath = authfilePath
	return nil
}

// buildRegistries configures custom container registries by encoding the V2RegistriesConf
// to TOML format, writing it to registries.conf in the temporal directory, and setting
// SystemRegistriesConfPath. Returns early if no registries config was provided.
func (b *SysContextBuilder) buildRegistries(sysContext *SysContext) error {
	if b.registriesConfig == nil {
		return nil
	}

	var data bytes.Buffer
	if err := toml.NewEncoder(&data).Encode(b.registriesConfig); err != nil {
		return fmt.Errorf("could not encode registry config to TOML: %w", err)
	}

	registriesFilePath := filepath.Join(sysContext.temporalDir, "registries.conf")

	if err := os.WriteFile(registriesFilePath, data.Bytes(), 0o644); err != nil {
		return fmt.Errorf("could not write registries.conf file %q: %w", registriesFilePath, err)
	}

	sysContext.SysContext.SystemRegistriesConfPath = registriesFilePath
	return nil
}

// buildProxy configures the Docker proxy URL from ControllerConfig.Spec.Proxy.
// Prioritizes HTTPS proxy over HTTP proxy when both are configured.
// Returns early if no controller config was provided or no proxy is configured.
func (b *SysContextBuilder) buildProxy(sysContext *SysContext) error {
	if b.controllerConfig == nil {
		return nil
	}
	// TODO: Improve when containers-libs is used with https://github.com/containers/container-libs/pull/583
	// proxy settings

	var proxyRawURL string
	//nolint:gocritic // if-else chain is clearer than switch for this proxy priority logic
	if b.controllerConfig.Spec.Proxy != nil && b.controllerConfig.Spec.Proxy.HTTPSProxy != "" {
		proxyRawURL = b.controllerConfig.Spec.Proxy.HTTPSProxy
	} else if b.controllerConfig.Spec.Proxy != nil && b.controllerConfig.Spec.Proxy.HTTPProxy != "" {
		proxyRawURL = b.controllerConfig.Spec.Proxy.HTTPProxy
	} else {
		// No proxy configured
		return nil
	}

	proxyURL, err := url.Parse(proxyRawURL)
	if err != nil {
		return fmt.Errorf("could not get proxy URL: %w", err)
	}

	sysContext.SysContext.DockerProxyURL = proxyURL
	return nil
}

// buildCerts writes image registry certificates from ControllerConfig to the temporal directory
// and configures DockerPerHostCertDirPath. Processes both ImageRegistryBundleData and
// ImageRegistryBundleUserData. Returns early if no certificates are configured.
func (b *SysContextBuilder) buildCerts(sysContext *SysContext) error {
	if !b.hasCerts() {
		return nil
	}

	certsDir := filepath.Join(sysContext.temporalDir, "certs-dir")
	if err := os.MkdirAll(certsDir, 0o755); err != nil {
		return fmt.Errorf("could not create cert dir %q: %w", certsDir, err)
	}

	for _, irb := range b.controllerConfig.Spec.ImageRegistryBundleData {
		if err := writeCertFromImageRegistryBundle(certsDir, irb); err != nil {
			return fmt.Errorf("could not write image registry bundle from ImageRegistryBundleData: %w", err)
		}
	}

	for _, irb := range b.controllerConfig.Spec.ImageRegistryBundleUserData {
		if err := writeCertFromImageRegistryBundle(certsDir, irb); err != nil {
			return fmt.Errorf("could not write image registry bundle from ImageRegistryBundleUserData: %w", err)
		}
	}

	// TODO: This mix of CAs is not ideal. Tracking in the following Jira
	// https://issues.redhat.com/browse/MCO-2061
	// container-libs doesn't support a mix of per-host and common CA certs, so, if
	// a common CA bundle is given by AdditionalTrustBundle we need to create a temporal bundle
	// that concatenates all the bundles into a single file and pass that to the lib.
	// We loose the ability to isolate CAs per registry till the fix in the library lands
	if len(b.controllerConfig.Spec.AdditionalTrustBundle) > 0 {
		var certBundle bytes.Buffer

		for _, irb := range b.controllerConfig.Spec.ImageRegistryBundleData {
			certBundle.Write(irb.Data)
			certBundle.WriteString("\n")
		}
		for _, irb := range b.controllerConfig.Spec.ImageRegistryBundleUserData {
			certBundle.Write(irb.Data)
			certBundle.WriteString("\n")
		}

		// Append AdditionalTrustBundle
		if len(b.controllerConfig.Spec.AdditionalTrustBundle) > 0 {
			certBundle.Write(b.controllerConfig.Spec.AdditionalTrustBundle)
			certBundle.WriteString("\n")
		}

		// Write merged bundle to file
		bundlePath := filepath.Join(certsDir, "ca-bundle.crt")
		if err := os.WriteFile(bundlePath, certBundle.Bytes(), 0o644); err != nil {
			return fmt.Errorf("could not write CA bundle: %w", err)
		}

		// TODO This one takes precedence over DockerPerHostCertDirPath
		// To be removed alongside the above TODO
		sysContext.SysContext.DockerCertPath = certsDir
	}

	sysContext.SysContext.DockerPerHostCertDirPath = certsDir
	return nil
}

// Cleanup removes all temporary files and directories created during SysContext construction.
// Safe to call even if no temporary directory was created.
func (s *SysContext) Cleanup() error {
	if err := os.RemoveAll(s.temporalDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("could not clean up SysContext temporal directory %s: %w", s.temporalDir, err)
	}
	return nil
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
