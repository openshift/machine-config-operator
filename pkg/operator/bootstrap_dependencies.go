package operator

import (
	"errors"
	"fmt"
	"os"

	configv1 "github.com/openshift/api/config/v1"
	configscheme "github.com/openshift/client-go/config/clientset/versioned/scheme"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
)

// BootstrapDependenciesFiles contains file paths to all the dependency files required for bootstrap.
type BootstrapDependenciesFiles struct {
	AdditionalTrustBundle  string
	Proxy                  string
	ClusterConfig          string
	Infrastructure         string
	Network                string
	DNS                    string
	CloudConfig            string
	CloudProviderCA        string
	MCSCAFile              string
	KubeAPIServerServingCA string
	PullSecret             string
}

// Validate ensures all required files are set and all provided file paths exist.
func (b BootstrapDependenciesFiles) Validate() error {
	files := []struct {
		name     string
		path     string
		required bool
	}{
		{"Proxy", b.Proxy, true},
		{"ClusterConfig", b.ClusterConfig, true},
		{"MCSCAFile", b.MCSCAFile, true},
		{"DNS", b.DNS, true},
		{"Network", b.Network, true},
		{"Infrastructure", b.Infrastructure, true},
		{"PullSecret", b.PullSecret, true},
		{"KubeAPIServerServingCA", b.KubeAPIServerServingCA, false},
		{"CloudProviderCA", b.CloudProviderCA, false},
		{"AdditionalTrustBundle", b.AdditionalTrustBundle, false},
		{"CloudConfig", b.CloudConfig, false},
	}
	for _, file := range files {
		if err := b.validateFile(file.name, file.path, file.required); err != nil {
			return err
		}
	}
	return nil
}

// validateFile checks if a file path is set (when required) and if the file exists.
func (b BootstrapDependenciesFiles) validateFile(name, path string, required bool) error {
	if required && path == "" {
		return fmt.Errorf("required file %s not set", name)
	} else if !required && path == "" {
		return nil
	}
	_, err := os.Stat(path)
	if err == nil {
		return nil
	} else if !os.IsNotExist(err) || required {
		// If required the file should exist. If not required, the file path may
		// be set but, it may not exist
		return fmt.Errorf("cannot read %s file. Error: %w", path, err)
	}
	return nil
}

// BootstrapDependencies contains all parsed and loaded dependencies required for MCO bootstrap.
type BootstrapDependencies struct {
	Files          BootstrapDependenciesFiles
	Proxy          *configv1.Proxy
	Infrastructure *configv1.Infrastructure
	Network        *configv1.Network
	DNS            *configv1.DNS
	PullSecret     string
	ClusterConfig  string
	CloudConfig    string

	CloudProviderCA        string
	KubeAPIServerServingCA string
	MCSCA                  string
	AdditionalTrustBundle  string
}

// NewBootstrapDependencies creates a new BootstrapDependencies instance by validating and loading
// all dependency files from the provided file paths.
func NewBootstrapDependencies(dependencyFiles BootstrapDependenciesFiles) (*BootstrapDependencies, error) {
	dependencies := &BootstrapDependencies{
		Files: dependencyFiles,
	}

	if err := dependencies.validate(); err != nil {
		return nil, err
	}

	if err := dependencies.fillDependencies(); err != nil {
		return nil, err
	}

	return dependencies, nil
}

// validate ensures all input file paths are valid.
func (b *BootstrapDependencies) validate() error {
	// Ensure the inputs are valid
	if err := b.Files.Validate(); err != nil {
		return err
	}

	return nil
}

// fillDependencies loads and parses all dependency data from the validated file paths.
func (b *BootstrapDependencies) fillDependencies() error {
	if err := b.fillCRs(); err != nil {
		return err
	}
	if err := b.fillCAs(); err != nil {
		return err
	}
	if err := b.fillPullSecret(); err != nil {
		return err
	}
	if err := b.fillClusterConfig(); err != nil {
		return err
	}
	if err := b.fillCloudConfigData(); err != nil {
		return err
	}

	return nil
}

// fillCRs loads all OpenShift config custom resources (Proxy, Infrastructure, Network, DNS).
func (b *BootstrapDependencies) fillCRs() error {
	var err error
	if b.Proxy, err = readConfigCR[*configv1.Proxy](b.Files.Proxy); err != nil {
		return fmt.Errorf("failed to read proxy config: %w", err)
	}
	if b.Infrastructure, err = readConfigCR[*configv1.Infrastructure](b.Files.Infrastructure); err != nil {
		return fmt.Errorf("failed to read infrastructure config: %w", err)
	}
	if b.Network, err = readConfigCR[*configv1.Network](b.Files.Network); err != nil {
		return fmt.Errorf("failed to read network config: %w", err)
	}
	if b.DNS, err = readConfigCR[*configv1.DNS](b.Files.DNS); err != nil {
		return fmt.Errorf("failed to read DNS config: %w", err)
	}
	return nil
}

// readCR reads and decodes a Kubernetes API object from a file using the provided decoder.
func readCR[T runtime.Object](path string, decoder runtime.Decoder) (T, error) {
	var zero T
	content, err := os.ReadFile(path)
	if err != nil {
		return zero, fmt.Errorf("failed to read %T file %s: %w", zero, path, err)
	}

	obj, err := runtime.Decode(decoder, content)
	if err != nil {
		return zero, fmt.Errorf("failed to decode %T file %s: %w", zero, path, err)
	}

	result, ok := obj.(T)
	if !ok {
		return zero, fmt.Errorf("expected %T but got %T", zero, obj)
	}
	return result, nil
}

// readConfigCR reads and decodes an OpenShift config API custom resource from a file.
func readConfigCR[T runtime.Object](path string) (T, error) {
	return readCR[T](path, configscheme.Codecs.UniversalDecoder(configv1.SchemeGroupVersion))
}

// readCoreCR reads and decodes a Kubernetes core API object from a file.
func readCoreCR[T runtime.Object](path string) (T, error) {
	return readCR[T](path, scheme.Codecs.UniversalDecoder(corev1.SchemeGroupVersion))
}

// readString reads the entire contents of a file and returns it as a string.
func readString(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

// fillCAs loads all certificate authority data.
func (b *BootstrapDependencies) fillCAs() error {
	var err error
	if b.MCSCA, err = readString(b.Files.MCSCAFile); err != nil {
		return fmt.Errorf("failed to read MCS CA file %s: %w", b.Files.MCSCAFile, err)
	}

	if b.Files.KubeAPIServerServingCA != "" {
		// This file is usually always passed, but it may not exist and that's acceptable
		b.KubeAPIServerServingCA, err = readString(b.Files.KubeAPIServerServingCA)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("failed to read KubeAPI CA file %s: %w", b.Files.KubeAPIServerServingCA, err)
		}
	}

	if b.Files.CloudProviderCA != "" {
		// This file is usually always passed, but it may not exist and that's acceptable
		b.CloudProviderCA, err = readString(b.Files.CloudProviderCA)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("failed to read Cloud Provider CA file %s: %w", b.Files.CloudProviderCA, err)
		}
	}

	if b.Files.AdditionalTrustBundle != "" {
		// This file is usually always passed, but it may not exist and that's acceptable
		additionalTrustBundleCM, err := readCoreCR[*corev1.ConfigMap](b.Files.AdditionalTrustBundle)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("failed to read Additional Trust Bundle file %s: %w", b.Files.AdditionalTrustBundle, err)
		}
		if additionalTrustBundleCM != nil {
			b.AdditionalTrustBundle = additionalTrustBundleCM.Data["ca-bundle.crt"]
		}
	}

	return nil
}

// fillPullSecret loads the pull secret data.
func (b *BootstrapDependencies) fillPullSecret() error {
	var err error
	if b.PullSecret, err = readString(b.Files.PullSecret); err != nil {
		return fmt.Errorf("failed to read PullSecret file %s: %w", b.Files.PullSecret, err)
	}
	return nil
}

// fillCloudConfigData loads cloud provider configuration when required by infrastructure.
func (b *BootstrapDependencies) fillCloudConfigData() error {
	if b.Infrastructure.Spec.CloudConfig.Name == "" {
		return nil
	}
	if b.Files.CloudConfig == "" {
		return errors.New("cloud-config file not provided but required by infrastructure")
	}

	cm, err := readCoreCR[*corev1.ConfigMap](b.Files.CloudConfig)
	if err != nil {
		return fmt.Errorf("failed to read cloud-config file %s: %w", b.Files.CloudConfig, err)
	}

	cloudConf, ok := cm.Data["cloud.conf"]
	if !ok {
		cloudConf = cm.Data[b.Infrastructure.Spec.CloudConfig.Key]
	}

	if cloudConf == "" {
		return fmt.Errorf("cloud-config CR doesn't have 'cloud.conf' or %s keys", b.Infrastructure.Spec.CloudConfig.Key)
	}

	b.CloudConfig = cloudConf
	return nil
}

// fillClusterConfig loads the cluster configuration data.
func (b *BootstrapDependencies) fillClusterConfig() error {
	var err error
	if b.ClusterConfig, err = readString(b.Files.ClusterConfig); err != nil {
		return fmt.Errorf("failed to read ClusterConfig file %s: %w", b.Files.ClusterConfig, err)
	}
	return nil
}
