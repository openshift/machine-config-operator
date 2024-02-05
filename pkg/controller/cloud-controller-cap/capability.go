package cloudcontrollercap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	configv1 "github.com/openshift/api/config/v1"
	openshiftconfigclientset "github.com/openshift/client-go/config/clientset/versioned"
	configscheme "github.com/openshift/client-go/config/clientset/versioned/scheme"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/yaml"
	kscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

const installConfigFilename = "cluster-config.yaml"

const defaultManifestsDir = "/etc/mcc/bootstrap"

type config struct {
	manifestDir     string
	configClientSet openshiftconfigclientset.Interface
	restConfig      *rest.Config
}

type Option func(c *config)

func WithManifestDir(dir string) Option {
	return func(c *config) {
		c.manifestDir = dir
	}
}

func WithConfigClientSet(cl openshiftconfigclientset.Interface) Option {
	return func(c *config) {
		c.configClientSet = cl
	}
}

func WithRestConfig(cfg *rest.Config) Option {
	return func(c *config) {
		c.restConfig = cfg
	}
}

// IsCloudControllerCapDisabled checks if CloudController capability disabled
// through querying kubernetes api for clusterversion or reading capabilities
// from the install config
func IsCloudControllerCapDisabled(opts ...Option) (bool, error) {
	conf := config{
		manifestDir: defaultManifestsDir,
	}
	for _, opt := range opts {
		opt(&conf)
	}

	// check if running in hypershift and return true because
	// hypershift does not support capabilities
	external, err := isTopologyExternal(conf.manifestDir)
	if err != nil {
		klog.Infoln("failed to check if topology is external", err)
	} else if external {
		return false, nil
	}

	disabled, err := checkClusterVersionForCCMODisabled(conf)
	if err != nil {
		klog.Errorln("failed to check if CloudController disabled via clusterversion, trying install config", err)
	} else {
		return disabled, err
	}

	disabled, err = checkInstallConfigForCCMODisabled(conf)
	if err != nil {
		klog.Errorln("failed to check if CloudController disabled via install config", err)
	}
	return disabled, err
}

func isTopologyExternal(manifestDir string) (bool, error) {
	if manifestDir == "" {
		manifestDir = "/assets/manifests/config"
	} else {
		// for hypershift we need to change dir
		manifestDir, _ = filepath.Split(manifestDir)
		manifestDir = filepath.Join(manifestDir, "config")
	}

	f, err := os.ReadFile(filepath.Join(manifestDir, "cluster-infrastructure-02-config.yaml"))
	if err != nil {
		return false, fmt.Errorf("failed to read cluster config, %w", err)
	}

	obj, err := runtime.Decode(configscheme.Codecs.UniversalDecoder(configv1.GroupVersion), f)
	if err != nil {
		return false, fmt.Errorf("failed to decode infrastructure config, %w", err)
	}

	infra, ok := obj.(*configv1.Infrastructure)
	if !ok {
		return false, errors.New("unable to read infrastructure config ")
	}

	return infra.Status.ControlPlaneTopology == configv1.ExternalTopologyMode, nil
}

func checkClusterVersionForCCMODisabled(cfg config) (bool, error) {
	if cfg.configClientSet == nil {
		if cfg.restConfig == nil {
			restCfg, err := rest.InClusterConfig()
			if err != nil {
				return false, fmt.Errorf("failed to get rest config from in cluster data: %w", err)
			}
			cfg.restConfig = restCfg
		}
		cl, err := openshiftconfigclientset.NewForConfig(cfg.restConfig)
		if err != nil {
			return false, fmt.Errorf("failed to create clientset from rest config, %w", err)
		}
		cfg.configClientSet = cl
	}

	cv, err := cfg.configClientSet.ConfigV1().ClusterVersions().Get(context.Background(), "version", metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to get clusterversion, %w", err)
	}

	if !sets.New[configv1.ClusterVersionCapability](cv.Status.Capabilities.KnownCapabilities...).
		Has(configv1.ClusterVersionCapabilityCloudControllerManager) {
		return false, nil
	}

	if sets.New[configv1.ClusterVersionCapability](cv.Status.Capabilities.EnabledCapabilities...).
		Has(configv1.ClusterVersionCapabilityCloudControllerManager) {
		return false, nil
	}

	return true, nil
}

// checkInstallConfigForCCMODisabled reads install config from the filesystem
// and checks if CloudController capability disabled
func checkInstallConfigForCCMODisabled(cfg config) (bool, error) {
	f, err := os.ReadFile(filepath.Join(cfg.manifestDir, installConfigFilename))
	if err != nil {
		return false, fmt.Errorf("failed to read cluster config, %w", err)
	}

	obji, err := runtime.Decode(kscheme.Codecs.UniversalDecoder(corev1.SchemeGroupVersion), f)
	if err != nil {
		return false, fmt.Errorf("failed to decode configmap with install config, %w", err)
	}

	s, ok := obji.(*corev1.ConfigMap)
	if !ok {
		return false, fmt.Errorf("invalid format of configmap with install config")
	}

	installConfRaw, ok := s.Data["install-config"]
	if !ok {
		return false, fmt.Errorf("install config is not found in configmap, %v", s.Data)
	}

	var caps = &struct {
		Capabilities *configv1.ClusterVersionCapabilitiesSpec `json:"capabilities"`
	}{}

	err = yaml.Unmarshal([]byte(installConfRaw), caps)
	if err != nil {
		return false, fmt.Errorf("failed to recode install config, %w", err)
	}

	baselineCapSet := configv1.ClusterVersionCapabilitySetCurrent
	if caps.Capabilities != nil && caps.Capabilities.BaselineCapabilitySet != "" {
		baselineCapSet = caps.Capabilities.BaselineCapabilitySet
	}

	enabledCaps := sets.New[configv1.ClusterVersionCapability](configv1.ClusterVersionCapabilitySets[baselineCapSet]...)
	if caps.Capabilities != nil && caps.Capabilities.AdditionalEnabledCapabilities != nil {
		enabledCaps.Insert(caps.Capabilities.AdditionalEnabledCapabilities...)
	}
	return !enabledCaps.Has(configv1.ClusterVersionCapabilityCloudControllerManager), nil
}
