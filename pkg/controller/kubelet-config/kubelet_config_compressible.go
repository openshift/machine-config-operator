package kubeletconfig

import (
	"context"
	"fmt"

	"github.com/clarketm/json"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// newCompressibleMachineConfig creates a new machine config for compressible kubelet override
// from the provided kubelet config contents
func newCompressibleMachineConfig(poolName string, kubeletContents []byte) (*mcfgv1.MachineConfig, error) {
	compressibleMCName := fmt.Sprintf(CompressibleMachineConfigNamePrefix, poolName)
	ignConfig := ctrlcommon.NewIgnConfig()
	compressibleMC, err := ctrlcommon.MachineConfigFromIgnConfig(poolName, compressibleMCName, ignConfig)
	if err != nil {
		return nil, fmt.Errorf("could not create machine config from ignition config: %w", err)
	}

	rawCompressibleIgn, err := createCompressibleKubeletIgnConfig(kubeletContents)
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
