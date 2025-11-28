package containerruntimeconfig

import (
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/api/machineconfiguration/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/controller/osimagestream"
	"github.com/openshift/machine-config-operator/pkg/version"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"
)

// RunContainerRuntimeBootstrap generates ignition configs at bootstrap
func RunContainerRuntimeBootstrap(templateDir string, crconfigs []*mcfgv1.ContainerRuntimeConfig, controllerConfig *mcfgv1.ControllerConfig, mcpPools []*mcfgv1.MachineConfigPool, osImageStream *v1alpha1.OSImageStream) ([]*mcfgv1.MachineConfig, error) {
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
			originalStorageIgn, _, _, err := generateOriginalContainerRuntimeConfigs(
				templateDir,
				controllerConfig,
				role,
				osimagestream.TryGetOSImageStreamFromPoolListByPoolName(osImageStream, mcpPools, pool.Name),
			)
			if err != nil {
				return nil, fmt.Errorf("could not generate origin ContainerRuntime Configs: %w", err)
			}

			var configFileList []generatedConfigFile
			ctrcfg := cfg.Spec.ContainerRuntimeConfig
			if ctrcfg.OverlaySize != nil && !ctrcfg.OverlaySize.IsZero() {
				storageTOML, err := mergeConfigChanges(originalStorageIgn, cfg, updateStorageConfig)
				if err != nil {
					klog.V(2).Infoln(cfg, err, "error merging user changes to storage.conf: %v", err)
				} else {
					configFileList = append(configFileList, generatedConfigFile{filePath: storageConfigPath, data: storageTOML})
				}
			}
			// Create the cri-o drop-in files
			if ctrcfg.LogLevel != "" || ctrcfg.PidsLimit != nil || (ctrcfg.LogSizeMax != nil && !ctrcfg.LogSizeMax.IsZero()) || ctrcfg.DefaultRuntime != mcfgv1.ContainerRuntimeDefaultRuntimeEmpty {
				crioFileConfigs := createCRIODropinFiles(cfg)
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
