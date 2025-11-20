package kubeletconfig

import (
	"context"
	"fmt"

	"github.com/clarketm/json"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

const (
	AutoSizingEnvFilePath = "/etc/node-sizing-enabled.env"

	AutoSizingMachineConfigNamePrefix = "50-%s-auto-sizing-disabled"

	DefaultAutoSizingEnvContent = `NODE_SIZING_ENABLED=false
SYSTEM_RESERVED_MEMORY=1Gi
SYSTEM_RESERVED_CPU=500m
SYSTEM_RESERVED_ES=1Gi
`
)

// ensureAutoSizingMachineConfigs ensures auto-sizing MachineConfigs exist for all MachineConfigPools
func (ctrl *Controller) ensureAutoSizingMachineConfigs(ctx context.Context) error {
	mcpPools, err := ctrl.mcpLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("could not list MachineConfigPools: %w", err)
	}

	for _, pool := range mcpPools {
		if err := ctrl.createAutoSizingMCIfNeeded(ctx, pool); err != nil {
			return fmt.Errorf("could not ensure auto-sizing MachineConfig for pool %v: %w", pool.Name, err)
		}
	}

	return nil
}

// createAutoSizingMCIfNeeded creates an auto-sizing MachineConfig for a given pool if it doesn't exist
func (ctrl *Controller) createAutoSizingMCIfNeeded(ctx context.Context, pool *mcfgv1.MachineConfigPool) error {
	autoSizingKey := fmt.Sprintf(AutoSizingMachineConfigNamePrefix, pool.Name)

	_, err := ctrl.client.MachineconfigurationV1().MachineConfigs().Get(ctx, autoSizingKey, metav1.GetOptions{})
	autoSizingIsNotFound := errors.IsNotFound(err)

	if err != nil && !autoSizingIsNotFound {
		return err
	}

	// Only create the auto-sizing MachineConfig if it doesn't exist
	if autoSizingIsNotFound {
		autoSizingMC, err := newAutoSizingMachineConfig(pool)
		if err != nil {
			return err
		}

		// Create the auto-sizing MachineConfig
		if err := retry.RetryOnConflict(updateBackoff, func() error {
			_, err := ctrl.client.MachineconfigurationV1().MachineConfigs().Create(ctx, autoSizingMC, metav1.CreateOptions{})
			return err
		}); err != nil {
			return fmt.Errorf("could not create auto-sizing MachineConfig, error: %w", err)
		}

		klog.Infof("Created auto-sizing configuration %v on MachineConfigPool %v", autoSizingKey, pool.Name)
	} else {
		klog.V(4).Infof("Auto-sizing MachineConfig %v already exists for pool %v, skipping creation", autoSizingKey, pool.Name)
	}

	return nil
}

// RunAutoSizingBootstrap generates auto-sizing MachineConfig objects for all mcpPools
func RunAutoSizingBootstrap(mcpPools []*mcfgv1.MachineConfigPool) ([]*mcfgv1.MachineConfig, error) {
	configs := make([]*mcfgv1.MachineConfig, 0, len(mcpPools))

	// Create auto-sizing MachineConfigs for each pool
	for _, pool := range mcpPools {
		autoSizingMC, err := newAutoSizingMachineConfig(pool)
		if err != nil {
			return nil, err
		}

		configs = append(configs, autoSizingMC)
	}

	return configs, nil
}

// newAutoSizingMachineConfig creates an auto-sizing MachineConfig for a given pool
func newAutoSizingMachineConfig(pool *mcfgv1.MachineConfigPool) (*mcfgv1.MachineConfig, error) {
	autoSizingDisabledMCName := fmt.Sprintf(AutoSizingMachineConfigNamePrefix, pool.Name)

	ignConfig := ctrlcommon.NewIgnConfig()

	autoSizingMC, err := ctrlcommon.MachineConfigFromIgnConfig(pool.Name, autoSizingDisabledMCName, ignConfig)
	if err != nil {
		return nil, err
	}

	rawAutoSizingIgn, err := createAutoSizingIgnConfig()
	if err != nil {
		return nil, err
	}

	autoSizingMC.Spec.Config.Raw = rawAutoSizingIgn
	// Do not add GeneratedByControllerVersionAnnotationKey annotation to auto-sizing MachineConfig. It will fail upgrade.
	// This annotation is added for informing the user that the auto-sizing MachineConfig was added in a patch release
	// to identify clusters created before 4.21 release.
	autoSizingMC.ObjectMeta.Annotations = map[string]string{
		"openshift-patch-reference": "machineConfig-to-set-the-default-behavior-of-NODE_SIZING_ENABLED",
	}

	return autoSizingMC, nil
}

// createAutoSizingIgnConfig creates the Ignition config with environment variables
// to disable auto-sizing of system reserved resources
func createAutoSizingIgnConfig() ([]byte, error) {
	autoSizingFile := ctrlcommon.NewIgnFileBytes(AutoSizingEnvFilePath, []byte(DefaultAutoSizingEnvContent))

	autoSizingIgnConfig := ctrlcommon.NewIgnConfig()
	autoSizingIgnConfig.Storage.Files = append(autoSizingIgnConfig.Storage.Files, autoSizingFile)

	rawAutoSizingIgn, err := json.Marshal(autoSizingIgnConfig)
	if err != nil {
		return nil, err
	}

	return rawAutoSizingIgn, nil
}
