package kubeletconfig

import (
	"context"
	"fmt"

	"github.com/clarketm/json"
	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

const (
	// CompressibleMachineConfigNamePrefix is the prefix for compressible machine configs
	CompressibleMachineConfigNamePrefix = "50-%s-compressible-kubelet-override"

	// KubeletConfPath is the path to the kubelet config file
	KubeletConfPath = "/etc/kubernetes/kubelet.conf"
)

// ensureCompressibleMachineConfigs ensures compressible machine configs exist for all pools
// This is called at controller startup to create compressible MCs for all pools
func (ctrl *Controller) ensureCompressibleMachineConfigs() error {
	if err := apihelpers.IsControllerConfigRunningOrCompleted(ctrlcommon.ControllerConfigName, ctrl.ccLister.Get); err != nil {
		// If the ControllerConfig is not running, we will encounter an error when generating the
		// kubeletconfig object.
		klog.V(1).Infof("ControllerConfig not running or completed: %v", err)
		return err
	}

	pools, err := ctrl.mcpLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("could not list machine config pools: %w", err)
	}

	cc, err := ctrl.ccLister.Get(ctrlcommon.ControllerConfigName)
	if err != nil {
		return fmt.Errorf("could not get ControllerConfig: %w", err)
	}

	apiServer, err := ctrl.apiserverLister.Get(ctrlcommon.APIServerInstanceName)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("could not get APIServer: %w", err)
	}

	for _, pool := range pools {
		// Generate the original kubelet config for this pool
		// Kubelet config in templates/master/01-master-kubelet/_base/files/kubelet.yaml has parameeters which need
		// to be resolved
		_, kubeletContents, err := generateOriginalKubeletConfigWithFeatureGates(cc, ctrl.templatesDir, pool.Name, ctrl.fgHandler, apiServer)
		if err != nil {
			klog.Warningf("Failed to generate kubelet config for pool %v: %v", pool.Name, err)
			continue
		}

		if err := ctrl.createCompressibleMachineConfigIfNeeded(pool.Name, kubeletContents); err != nil {
			klog.Warningf("Failed to create compressible machine config for pool %v: %v", pool.Name, err)
			// Don't fail startup if compressible MC creation fails for a pool
			continue
		}
	}

	return nil
}

// createCompressibleMachineConfigIfNeeded creates a compressible machine config if it doesn't exist
// This function is called from the controller after kubelet config is successfully generated
func (ctrl *Controller) createCompressibleMachineConfigIfNeeded(poolName string, kubeletContents []byte) error {
	compressibleKey := fmt.Sprintf(CompressibleMachineConfigNamePrefix, poolName)
	_, err := ctrl.client.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), compressibleKey, metav1.GetOptions{})
	compressibleIsNotFound := errors.IsNotFound(err)
	if err != nil && !compressibleIsNotFound {
		return err
	}

	if compressibleIsNotFound {
		compressibleMC, err := newCompressibleMachineConfig(poolName, kubeletContents)
		if err != nil {
			return fmt.Errorf("could not create compressible machine config: %w", err)
		}

		if err := retry.RetryOnConflict(updateBackoff, func() error {
			_, err := ctrl.client.MachineconfigurationV1().MachineConfigs().Create(context.TODO(), compressibleMC, metav1.CreateOptions{})
			return err
		}); err != nil {
			return fmt.Errorf("could not create compressible MachineConfig: %w", err)
		}
		klog.Infof("Created compressible kubelet configuration %v for pool %v", compressibleKey, poolName)
	} else {
		klog.V(4).Infof("Compressible kubelet MachineConfig %v already exists for pool %v, skipping creation", compressibleKey, poolName)
	}

	return nil
}

// newCompressibleMachineConfig creates a new machine config for compressible kubelet override
// from the provided kubelet config contents
func newCompressibleMachineConfig(poolName string, kubeletContents []byte) (*mcfgv1.MachineConfig, error) {
	compressibleMCName := fmt.Sprintf(CompressibleMachineConfigNamePrefix, poolName)

	rawCompressibleIgn, err := createCompressibleKubeletIgnConfig(kubeletContents)
	if err != nil {
		return nil, fmt.Errorf("could not create compressible kubelet ignition config: %w", err)
	}

	compressibleMC, err := ctrlcommon.MachineConfigFromRawIgnConfig(poolName, compressibleMCName, rawCompressibleIgn)
	if err != nil {
		return nil, fmt.Errorf("could not create machine config from ignition config: %w", err)
	}

	compressibleMC.ObjectMeta.Annotations = map[string]string{
		"openshift-patch-reference": "machineConfig-to-override-kubelet-conf-for-compressible-resources",
	}

	return compressibleMC, nil
}

// createCompressibleKubeletIgnConfig creates an Ignition config that overrides /etc/kubernetes/kubelet.conf
// from the provided kubelet config contents
func createCompressibleKubeletIgnConfig(kubeletContents []byte) ([]byte, error) {
	// Create an Ignition file that overrides /etc/kubernetes/kubelet.conf
	compressibleFile := ctrlcommon.NewIgnFileBytesOverwriting(KubeletConfPath, kubeletContents)
	compressibleIgnConfig := ctrlcommon.NewIgnConfig()
	compressibleIgnConfig.Storage.Files = append(compressibleIgnConfig.Storage.Files, compressibleFile)

	rawCompressibleIgn, err := json.Marshal(compressibleIgnConfig)
	if err != nil {
		return nil, fmt.Errorf("could not marshal ignition config: %w", err)
	}

	return rawCompressibleIgn, nil
}

// RunCompressibleBootstrap generates compressible machine configs for all pools during bootstrap
func RunCompressibleBootstrap(pools []*mcfgv1.MachineConfigPool, cconfig *mcfgv1.ControllerConfig, templatesDir string, apiServer *configv1.APIServer, fgHandler ctrlcommon.FeatureGatesHandler) ([]*mcfgv1.MachineConfig, error) {
	configs := []*mcfgv1.MachineConfig{}

	for _, pool := range pools {
		// Generate the original kubelet config for this pool
		_, kubeletContents, err := generateOriginalKubeletConfigWithFeatureGates(cconfig, templatesDir, pool.Name, fgHandler, apiServer)
		if err != nil {
			klog.Warningf("Failed to generate kubelet config for pool %v: %v", pool.Name, err)
			continue
		}

		// Create compressible MC
		compressibleMC, err := newCompressibleMachineConfig(pool.Name, kubeletContents)
		if err != nil {
			klog.Warningf("Failed to create compressible machine config for pool %v: %v", pool.Name, err)
			continue
		}

		configs = append(configs, compressibleMC)
	}

	return configs, nil
}
