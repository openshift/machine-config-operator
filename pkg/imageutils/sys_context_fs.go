package imageutils

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/secrets"
	corev1 "k8s.io/api/core/v1"
)

// Holds several paths and proxy information needed to create an appropriate
// SysContext. Where possible, we try to use the paths as-is to avoid requiring
// additional logic.
type SysContextPaths struct {
	AdditionalTrustBundles []string
	CertDir                string
	PerHostCertDir         string
	Proxy                  *configv1.ProxyStatus
	PullSecret             string
	RegistryConfig         string
}

// Creates a temporary ControllerConfig which is used to build the SysContext
// object.
func (s *SysContextPaths) newControllerConfig() (*mcfgv1.ControllerConfig, error) {
	cfg := &mcfgv1.ControllerConfig{}

	if s.Proxy != nil {
		cfg.Spec.Proxy = s.Proxy
	}

	// If we have additional trust bundles, then we need to merge its contents
	// with the per-host certificates.
	if s.PerHostCertDir != "" && len(s.AdditionalTrustBundles) > 0 {
		perHostCerts, err := s.loadPerHostCertificates()
		if err != nil {
			return nil, fmt.Errorf("could not load certificates: %w", err)
		}

		tb, err := newTrustBundleLoader().loadAll(s.AdditionalTrustBundles)
		if err != nil {
			return nil, fmt.Errorf("could not load additional trust bundles: %w", err)
		}

		cfg.Spec.AdditionalTrustBundle = tb
		cfg.Spec.ImageRegistryBundleData = perHostCerts
	}

	return cfg, nil
}

// Reads the pull secret from disk. Since we don't know whot format it will be
// in, we will process it into the format we need and use it later via our temp
// directory.
func (s *SysContextPaths) loadPullSecret() (*corev1.Secret, error) {
	sb, err := os.ReadFile(s.PullSecret)
	if err != nil {
		return nil, fmt.Errorf("could not read pull secret: %w", err)
	}

	rs, err := secrets.NewImageRegistrySecret(sb)
	if err != nil {
		return nil, fmt.Errorf("could not parse pull secret: %w", err)
	}

	secret, err := rs.K8sSecret(corev1.SecretTypeDockerConfigJson)
	if err != nil {
		return nil, fmt.Errorf("could not convert pull secret: %w", err)
	}

	return secret, nil
}

// Loads per-host certificates into an ImageRegistryBundle.
func (s *SysContextPaths) loadPerHostCertificates() ([]mcfgv1.ImageRegistryBundle, error) {
	certs := []mcfgv1.ImageRegistryBundle{}

	err := filepath.Walk(s.PerHostCertDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return err
		}

		fileBytes, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		certs = append(certs, mcfgv1.ImageRegistryBundle{Data: fileBytes, File: filepath.Base(path)})

		return nil
	})

	return certs, err
}

// Creates a SysContext instance from the supplied paths and configuration.
// This uses the SysContextBuilder to merge the additional trust bundles with
// the certificates and does most of its work in a tempdir.
func NewSysContextFromFilesystem(opts SysContextPaths) (*SysContext, error) {
	sysCtxBuilder := NewSysContextBuilder()

	if opts.PullSecret != "" {
		secret, err := opts.loadPullSecret()
		if err != nil {
			return nil, fmt.Errorf("could not load image pull secret: %w", err)
		}

		sysCtxBuilder = sysCtxBuilder.WithSecret(secret)
	}

	ctrlCfg, err := opts.newControllerConfig()
	if err != nil {
		return nil, fmt.Errorf("could not create new controllerconfig: %w", err)
	}

	sysCtxBuilder = sysCtxBuilder.WithControllerConfig(ctrlCfg)

	sysCtx, err := sysCtxBuilder.Build()
	if err != nil {
		return nil, err
	}

	sysCtx.SysContext.SystemRegistriesConfPath = opts.RegistryConfig

	// If we have no additional trust bundle or the file is empty, then we should
	// use the provided paths as-is since we did not merge the trust hundle with
	// the per-host certificates.
	if len(opts.AdditionalTrustBundles) == 0 || len(ctrlCfg.Spec.AdditionalTrustBundle) == 0 {
		sysCtx.SysContext.DockerCertPath = opts.CertDir
		sysCtx.SysContext.DockerPerHostCertDirPath = opts.PerHostCertDir
	}

	return sysCtx, nil
}

// Loads all of the trust bundles given, traversing directories as needed.
type trustBundleLoader struct{}

func newTrustBundleLoader() *trustBundleLoader {
	return &trustBundleLoader{}
}

// loadAll loads and merges trust bundles from multiple file or directory paths.
func (t *trustBundleLoader) loadAll(paths []string) ([]byte, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	var mergedBundle []byte

	for _, path := range paths {
		data, err := t.loadTrustBundleFromPath(path)
		if err != nil {
			return nil, err
		}

		if len(data) > 0 {
			mergedBundle = append(mergedBundle, data...)
			mergedBundle = append(mergedBundle, '\n')
		}
	}

	return mergedBundle, nil
}

// loadTrustBundleFromPath loads certificate data from a single file or directory path.
func (t *trustBundleLoader) loadTrustBundleFromPath(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("could not stat path %q: %w", path, err)
	}

	if info.IsDir() {
		return t.loadTrustBundleFromDirectory(path)
	}

	// It's a file - read it directly
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read trust bundle file %q: %w", path, err)
	}

	return data, nil
}

// loadTrustBundleFromDirectory reads all certificate files from a directory (non-recursively).
func (t *trustBundleLoader) loadTrustBundleFromDirectory(dirPath string) ([]byte, error) {
	var certBundle []byte

	err := filepath.Walk(dirPath, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip the root directory itself
		if path == dirPath {
			return nil
		}

		// Skip subdirectories (non-recursive, like loadPerHostCertificates)
		if info.IsDir() {
			return filepath.SkipDir
		}

		// Read the certificate file
		fileBytes, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("could not read file %q: %w", path, err)
		}

		// Append to bundle
		certBundle = append(certBundle, fileBytes...)
		certBundle = append(certBundle, '\n')

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("could not walk directory %q: %w", dirPath, err)
	}

	return certBundle, nil
}
