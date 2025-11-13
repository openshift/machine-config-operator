package kubeletconfig

import (
	"context"
	"fmt"

	"github.com/clarketm/json"
	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
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

// createCompressibleKubeletIgnConfig creates an Ignition config that overrides /etc/kubernetes/kubelet.conf
func createCompressibleKubeletIgnConfig(cc *mcfgv1.ControllerConfig, role, templatesDir string, apiServer *configv1.APIServer) ([]byte, error) {
	// Get the kubelet config from templates
	kubeletFile, err := generateOriginalKubeletConfigIgn(cc, templatesDir, role, apiServer)
	if err != nil {
		return nil, fmt.Errorf("could not generate kubelet config from templates: %w", err)
	}

	if kubeletFile == nil || kubeletFile.Contents.Source == nil {
		return nil, fmt.Errorf("kubelet config file or contents is nil")
	}

	// Decode the kubelet config contents
	kubeletContents, err := ctrlcommon.DecodeIgnitionFileContents(kubeletFile.Contents.Source, kubeletFile.Contents.Compression)
	if err != nil {
		return nil, fmt.Errorf("could not decode kubelet config contents: %w", err)
	}

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

// newCompressibleMachineConfig creates a new machine config for compressible kubelet override
func newCompressibleMachineConfig(pool *mcfgv1.MachineConfigPool, cc *mcfgv1.ControllerConfig, templatesDir string, apiServer *configv1.APIServer) (*mcfgv1.MachineConfig, error) {
	compressibleMCName := fmt.Sprintf(CompressibleMachineConfigNamePrefix, pool.Name)
	ignConfig := ctrlcommon.NewIgnConfig()
	compressibleMC, err := ctrlcommon.MachineConfigFromIgnConfig(pool.Name, compressibleMCName, ignConfig)
	if err != nil {
		return nil, fmt.Errorf("could not create machine config from ignition config: %w", err)
	}

	// Determine the role for template selection
	role := pool.Name
	if role != ctrlcommon.MachineConfigPoolMaster && role != ctrlcommon.MachineConfigPoolWorker {
		// Custom pools inherit from worker templates
		role = ctrlcommon.MachineConfigPoolWorker
	}

	rawCompressibleIgn, err := createCompressibleKubeletIgnConfig(cc, role, templatesDir, apiServer)
	if err != nil {
		return nil, fmt.Errorf("could not create compressible kubelet ignition config: %w", err)
	}

	compressibleMC.Spec.Config.Raw = rawCompressibleIgn
	compressibleMC.ObjectMeta.Annotations = map[string]string{
		"openshift-patch-reference": "machineConfig-to-override-kubelet-conf-for-compressible-resources",
	}

	return compressibleMC, nil
}

// createCompressibleMachineConfigIfNeeded creates a compressible machine config if it doesn't exist
func (ctrl *Controller) createCompressibleMachineConfigIfNeeded(pool *mcfgv1.MachineConfigPool, cc *mcfgv1.ControllerConfig, apiServer *configv1.APIServer) error {
	compressibleKey := fmt.Sprintf(CompressibleMachineConfigNamePrefix, pool.Name)
	_, err := ctrl.client.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), compressibleKey, metav1.GetOptions{})
	compressibleIsNotFound := errors.IsNotFound(err)
	if err != nil && !compressibleIsNotFound {
		return err
	}

	if compressibleIsNotFound {
		compressibleMC, err := newCompressibleMachineConfig(pool, cc, ctrl.templatesDir, apiServer)
		if err != nil {
			return fmt.Errorf("could not create compressible machine config: %w", err)
		}

		if err := retry.RetryOnConflict(updateBackoff, func() error {
			_, err := ctrl.client.MachineconfigurationV1().MachineConfigs().Create(context.TODO(), compressibleMC, metav1.CreateOptions{})
			return err
		}); err != nil {
			return fmt.Errorf("could not create compressible MachineConfig: %w", err)
		}
		klog.Infof("Created compressible kubelet configuration %v on MachineConfigPool %v", compressibleKey, pool.Name)
	} else {
		klog.V(4).Infof("Compressible kubelet MachineConfig %v already exists for pool %v, skipping creation", compressibleKey, pool.Name)
	}

	return nil
}

// ensureCompressibleMachineConfigs ensures compressible machine configs exist for all pools
func (ctrl *Controller) ensureCompressibleMachineConfigs() error {
	mcpPools, err := ctrl.mcpLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("could not list MachineConfigPools: %w", err)
	}

	// Get the controller config
	cc, err := ctrl.ccLister.Get(ctrlcommon.ControllerConfigName)
	if err != nil {
		return fmt.Errorf("could not get ControllerConfig: %w", err)
	}

	// Get the APIServer config for TLS settings
	apiServer, err := ctrl.apiserverLister.Get(ctrlcommon.APIServerInstanceName)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("could not get APIServer config: %w", err)
	}

	for _, pool := range mcpPools {
		if err := ctrl.createCompressibleMachineConfigIfNeeded(pool, cc, apiServer); err != nil {
			return fmt.Errorf("could not ensure compressible MachineConfig for pool %v: %w", pool.Name, err)
		}
	}

	return nil
}

// RunCompressibleBootstrap creates compressible machine configs during bootstrap
func RunCompressibleBootstrap(pools []*mcfgv1.MachineConfigPool, cc *mcfgv1.ControllerConfig, templatesDir string, apiServer *configv1.APIServer) ([]*mcfgv1.MachineConfig, error) {
	configs := []*mcfgv1.MachineConfig{}

	for _, pool := range pools {
		compressibleMC, err := newCompressibleMachineConfig(pool, cc, templatesDir, apiServer)
		if err != nil {
			return nil, fmt.Errorf("could not create compressible machine config for pool %v: %w", pool.Name, err)
		}
		configs = append(configs, compressibleMC)
	}

	return configs, nil
}
