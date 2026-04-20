package containerruntimeconfig

import (
	"fmt"

	apicfgv1 "github.com/openshift/api/config/v1"
	features "github.com/openshift/api/features"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/version"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"
)

// RunContainerRuntimeBootstrap generates ignition configs at bootstrap
func RunContainerRuntimeBootstrap(templateDir string, crconfigs []*mcfgv1.ContainerRuntimeConfig, controllerConfig *mcfgv1.ControllerConfig, mcpPools []*mcfgv1.MachineConfigPool, fgHandler ctrlcommon.FeatureGatesHandler) ([]*mcfgv1.MachineConfig, error) {
	var res []*mcfgv1.MachineConfig
	managedKeyExist := make(map[string]bool)
	for _, cfg := range crconfigs {
		if err := validateUserContainerRuntimeConfig(cfg); err != nil {
			return nil, err
		}
		// use selector since label matching part of a ContaineRuntimeConfig is not handled during the bootstrap
		selector, err := metav1.LabelSelectorAsSelector(cfg.Spec.MachineConfigPoolSelector)
		if err != nil {
			return nil, fmt.Errorf("invalid label selector: %w", err)
		}
		for _, pool := range mcpPools {
			// If a pool with a nil or empty selector creeps in, it should match nothing, not everything.
			// skip the pool if no matched label for containerruntime config
			if selector.Empty() || !selector.Matches(labels.Set(pool.Labels)) {
				continue
			}
			role := pool.Name
			// Generate the original ContainerRuntimeConfig
			originalStorageIgn, _, _, err := generateOriginalContainerRuntimeConfigs(templateDir, controllerConfig, role)
			if err != nil {
				return nil, fmt.Errorf("could not generate origin ContainerRuntime Configs: %w", err)
			}

			var configFileList []generatedConfigFile
			ctrcfg := cfg.Spec.ContainerRuntimeConfig
			additionalStorageEnabled := fgHandler != nil && fgHandler.Enabled(features.FeatureGateAdditionalStorageConfig)
			if needsStorageUpdate(ctrcfg, additionalStorageEnabled) {
				storageTOML, err := mergeConfigChanges(originalStorageIgn, cfg, func(data []byte, internal *mcfgv1.ContainerRuntimeConfiguration) ([]byte, error) {
					return updateStorageConfig(data, internal, additionalStorageEnabled)
				})
				if err != nil {
					klog.V(2).Infof("error merging user changes to storage.conf: %v", err)
				} else {
					configFileList = append(configFileList, generatedConfigFile{filePath: storageConfigPath, data: storageTOML})
				}
			}
			// Create the cri-o drop-in files
			if needsCRIODropinUpdate(ctrcfg, additionalStorageEnabled) {
				crioFileConfigs := createCRIODropinFiles(cfg, additionalStorageEnabled)
				configFileList = append(configFileList, crioFileConfigs...)
			}

			ctrRuntimeConfigIgn := createNewIgnition(configFileList)
			managedKey, err := generateBootstrapManagedKeyContainerConfig(pool, managedKeyExist)
			if err != nil {
				return nil, fmt.Errorf("could not marshal container runtime ignition: %w", err)
			}
			// the first managed key value 99-poolname-generated-containerruntime does not have a suffix
			// set "" as suffix annotation to the containerruntime config object
			cfg.SetAnnotations(map[string]string{
				ctrlcommon.MCNameSuffixAnnotationKey: "",
			})
			mc, err := ctrlcommon.MachineConfigFromIgnConfig(role, managedKey, ctrRuntimeConfigIgn)
			if err != nil {
				return nil, fmt.Errorf("could not create MachineConfig from new Ignition config: %w", err)
			}
			mc.SetAnnotations(map[string]string{
				ctrlcommon.GeneratedByControllerVersionAnnotationKey: version.Hash,
			})
			oref := metav1.OwnerReference{
				APIVersion: controllerKind.GroupVersion().String(),
				Kind:       controllerKind.Kind,
			}
			mc.SetOwnerReferences([]metav1.OwnerReference{oref})
			res = append(res, mc)
		}
	}

	return res, nil
}

// RunTLSBootstrap creates one CRI-O TLS MachineConfig per pool at bootstrap.
// This is separate from RunContainerRuntimeBootstrap so that TLS is propagated
// even when there are no ContainerRuntimeConfig CRs.
func RunTLSBootstrap(mcpPools []*mcfgv1.MachineConfigPool, kubeletConfigs []*mcfgv1.KubeletConfig, apiServer *apicfgv1.APIServer) ([]*mcfgv1.MachineConfig, error) {
	// Validate KubeletConfig selectors upfront.
	for _, kc := range kubeletConfigs {
		if kc.Spec.MachineConfigPoolSelector != nil {
			if _, err := metav1.LabelSelectorAsSelector(kc.Spec.MachineConfigPoolSelector); err != nil {
				return nil, fmt.Errorf("invalid MachineConfigPoolSelector in KubeletConfig %s: %w", kc.Name, err)
			}
		}
	}

	// Iterate pools (not kubeletConfigs) because every pool needs a TLS MC,
	// even when no KubeletConfig matches — the APIServer profile is the fallback.
	var res []*mcfgv1.MachineConfig
	for _, pool := range mcpPools {
		tlsMinVersion, tlsCipherSuites := tlsConfigFromKubeletConfigs(kubeletConfigs, pool)
		if tlsMinVersion == "" {
			tlsMinVersion, tlsCipherSuites = ctrlcommon.GetSecurityProfileCiphersFromAPIServer(apiServer)
		}
		tlsDropinFiles, err := createCRIOTLSDropinFile(tlsMinVersion, tlsCipherSuites)
		if err != nil {
			return nil, fmt.Errorf("could not create TLS drop-in for pool %s: %w", pool.Name, err)
		}
		tlsIgn := createNewIgnition(tlsDropinFiles)

		managedKey, err := getManagedKeyTLS(pool)
		if err != nil {
			return nil, fmt.Errorf("could not get TLS managed key for pool %s: %w", pool.Name, err)
		}
		mc, err := ctrlcommon.MachineConfigFromIgnConfig(pool.Name, managedKey, tlsIgn)
		if err != nil {
			return nil, fmt.Errorf("could not create TLS MachineConfig for pool %s: %w", pool.Name, err)
		}
		mc.SetAnnotations(map[string]string{
			ctrlcommon.GeneratedByControllerVersionAnnotationKey: version.Hash,
		})
		res = append(res, mc)
	}
	return res, nil
}

// generateBootstrapManagedKeyContainerConfig generates the machine config name for a CR during bootstrap, returns error
// if there's more than 1 container config for the same pool.

// Note: Only one ContainerConfig manifest per pool is allowed for bootstrap mode for the following reason:
// if you provide multiple per pool, they would overwrite each other and not merge, potentially confusing customers post install;
// we can simplify the logic for the bootstrap generation and avoid some edge cases.
func generateBootstrapManagedKeyContainerConfig(pool *mcfgv1.MachineConfigPool, managedKeyExist map[string]bool) (string, error) {
	if _, ok := managedKeyExist[pool.Name]; ok {
		return "", fmt.Errorf("Error found multiple ContainerConfig targeting MachineConfigPool %v. Please apply only one ContainerConfig manifest for each pool during installation", pool.Name)
	}
	managedKey, err := ctrlcommon.GetManagedKey(pool, nil, "99", "containerruntime", "")
	if err != nil {
		return "", err
	}
	managedKeyExist[pool.Name] = true
	return managedKey, nil
}
