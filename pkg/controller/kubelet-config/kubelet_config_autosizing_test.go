package kubeletconfig

import (
	"context"
	"fmt"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/helpers"
)

func TestCreateAutoSizingIgnConfig(t *testing.T) {
	rawIgn, err := createAutoSizingIgnConfig()
	require.NoError(t, err)
	require.NotNil(t, rawIgn)

	// Decode and verify the ignition config
	ignConfig, err := ctrlcommon.ParseAndConvertConfig(rawIgn)
	require.NoError(t, err)
	require.Len(t, ignConfig.Storage.Files, 1)

	// Verify file path and contents
	file := ignConfig.Storage.Files[0]
	require.Equal(t, AutoSizingEnvFilePath, file.Path)

	contents, err := ctrlcommon.DecodeIgnitionFileContents(file.Contents.Source, file.Contents.Compression)
	require.NoError(t, err)
	require.Equal(t, DefaultAutoSizingEnvContent, string(contents))
}

func TestNewAutoSizingMachineConfig(t *testing.T) {
	testCases := []struct {
		name         string
		poolName     string
		expectedName string
	}{
		{
			name:         "worker pool",
			poolName:     "worker",
			expectedName: "50-worker-auto-sizing-disabled",
		},
		{
			name:         "master pool",
			poolName:     "master",
			expectedName: "50-master-auto-sizing-disabled",
		},
		{
			name:         "custom pool",
			poolName:     "custom",
			expectedName: "50-custom-auto-sizing-disabled",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			pool := helpers.NewMachineConfigPool(tc.poolName, nil, helpers.WorkerSelector, "v0")
			mc, err := newAutoSizingMachineConfig(pool)
			require.NoError(t, err)
			require.NotNil(t, mc)

			// Verify MachineConfig name
			require.Equal(t, tc.expectedName, mc.Name)

			// Verify MachineConfig labels
			require.Equal(t, tc.poolName, mc.Labels[mcfgv1.MachineConfigRoleLabelKey])

			// Verify annotation
			require.Contains(t, mc.Annotations, "openshift-patch-reference")
			require.Equal(t, "machineConfig-to-set-the-default-behavior-of-NODE_SIZING_ENABLED", mc.Annotations["openshift-patch-reference"])

			// Verify the ignition config has the auto-sizing file
			ignConfig, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
			require.NoError(t, err)
			require.Len(t, ignConfig.Storage.Files, 1)

			file := ignConfig.Storage.Files[0]
			require.Equal(t, AutoSizingEnvFilePath, file.Path)

			contents, err := ctrlcommon.DecodeIgnitionFileContents(file.Contents.Source, file.Contents.Compression)
			require.NoError(t, err)
			require.Equal(t, DefaultAutoSizingEnvContent, string(contents))
		})
	}
}

func TestCreateAutoSizingMachineConfigIfNeeded(t *testing.T) {
	t.Run("creates MC when it doesn't exist", func(t *testing.T) {
		f := newFixture(t)
		ctrl := f.newController(nil)

		pool := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
		autoSizingKey := fmt.Sprintf(AutoSizingMachineConfigNamePrefix, pool.Name)

		// Expect Get to return NotFound
		f.expectGetMachineConfigAction(helpers.NewMachineConfig(autoSizingKey, map[string]string{"node-role/worker": ""}, "dummy://", []ign3types.File{{}}))

		err := ctrl.createAutoSizingMachineConfigIfNeeded(pool)
		require.NoError(t, err)

		// Verify MachineConfig was created
		mc, err := ctrl.client.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), autoSizingKey, metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, autoSizingKey, mc.Name)
	})

	t.Run("skips creation when MC already exists", func(t *testing.T) {
		f := newFixture(t)
		ctrl := f.newController(nil)

		pool := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
		autoSizingKey := fmt.Sprintf(AutoSizingMachineConfigNamePrefix, pool.Name)

		// Pre-create the MachineConfig
		existingMC := helpers.NewMachineConfig(autoSizingKey, map[string]string{"node-role/worker": ""}, "dummy://", []ign3types.File{{}})
		_, err := ctrl.client.MachineconfigurationV1().MachineConfigs().Create(context.TODO(), existingMC, metav1.CreateOptions{})
		require.NoError(t, err)

		// Should not error when MC already exists
		err = ctrl.createAutoSizingMachineConfigIfNeeded(pool)
		require.NoError(t, err)

		// Verify only one MC exists
		mcList, err := ctrl.client.MachineconfigurationV1().MachineConfigs().List(context.TODO(), metav1.ListOptions{})
		require.NoError(t, err)
		require.Len(t, mcList.Items, 1)
	})
}

func TestEnsureAutoSizingMachineConfigs(t *testing.T) {
	t.Run("creates MCs for all pools", func(t *testing.T) {
		f := newFixture(t)
		f.skipActionsValidation = true

		// Create multiple pools
		workerPool := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
		masterPool := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
		f.mcpLister = append(f.mcpLister, workerPool, masterPool)

		ctrl := f.newController(nil)

		err := ctrl.ensureAutoSizingMachineConfigs()
		require.NoError(t, err)

		// Verify MachineConfigs were created for both pools
		mcList, err := ctrl.client.MachineconfigurationV1().MachineConfigs().List(context.TODO(), metav1.ListOptions{})
		require.NoError(t, err)
		require.Len(t, mcList.Items, 2)

		// Verify names
		mcNames := make(map[string]bool)
		for _, mc := range mcList.Items {
			mcNames[mc.Name] = true
		}
		require.True(t, mcNames["50-worker-auto-sizing-disabled"])
		require.True(t, mcNames["50-master-auto-sizing-disabled"])
	})

	t.Run("handles pools with no existing MCs", func(t *testing.T) {
		f := newFixture(t)
		f.skipActionsValidation = true

		customPool := helpers.NewMachineConfigPool("custom", nil, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "node-role/custom", ""), "v0")
		f.mcpLister = append(f.mcpLister, customPool)

		ctrl := f.newController(nil)

		err := ctrl.ensureAutoSizingMachineConfigs()
		require.NoError(t, err)

		mcList, err := ctrl.client.MachineconfigurationV1().MachineConfigs().List(context.TODO(), metav1.ListOptions{})
		require.NoError(t, err)
		require.Len(t, mcList.Items, 1)
		require.Equal(t, "50-custom-auto-sizing-disabled", mcList.Items[0].Name)
	})
}

func TestRunAutoSizingBootstrap(t *testing.T) {
	t.Run("generates MCs for all pools", func(t *testing.T) {
		workerPool := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
		masterPool := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
		pools := []*mcfgv1.MachineConfigPool{workerPool, masterPool}

		mcs, err := RunAutoSizingBootstrap(pools)
		require.NoError(t, err)
		require.Len(t, mcs, 2)

		// Verify MC names and content
		mcNames := make(map[string]*mcfgv1.MachineConfig)
		for _, mc := range mcs {
			mcNames[mc.Name] = mc
		}

		require.Contains(t, mcNames, "50-worker-auto-sizing-disabled")
		require.Contains(t, mcNames, "50-master-auto-sizing-disabled")

		// Verify worker MC
		workerMC := mcNames["50-worker-auto-sizing-disabled"]
		require.Equal(t, "worker", workerMC.Labels[mcfgv1.MachineConfigRoleLabelKey])
		require.Equal(t, "machineConfig-to-set-the-default-behavior-of-NODE_SIZING_ENABLED", workerMC.Annotations["openshift-patch-reference"])

		ignConfig, err := ctrlcommon.ParseAndConvertConfig(workerMC.Spec.Config.Raw)
		require.NoError(t, err)
		require.Len(t, ignConfig.Storage.Files, 1)
		require.Equal(t, AutoSizingEnvFilePath, ignConfig.Storage.Files[0].Path)
	})

	t.Run("handles empty pool list", func(t *testing.T) {
		pools := []*mcfgv1.MachineConfigPool{}

		mcs, err := RunAutoSizingBootstrap(pools)
		require.NoError(t, err)
		require.Len(t, mcs, 0)
	})

	t.Run("handles single pool", func(t *testing.T) {
		customPool := helpers.NewMachineConfigPool("custom", nil, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "node-role/custom", ""), "v0")
		pools := []*mcfgv1.MachineConfigPool{customPool}

		mcs, err := RunAutoSizingBootstrap(pools)
		require.NoError(t, err)
		require.Len(t, mcs, 1)
		require.Equal(t, "50-custom-auto-sizing-disabled", mcs[0].Name)
	})
}

func TestAutoSizingConstants(t *testing.T) {
	t.Run("verify constant values", func(t *testing.T) {
		require.Equal(t, "/etc/node-sizing-enabled.env", AutoSizingEnvFilePath)
		require.Equal(t, "50-%s-auto-sizing-disabled", AutoSizingMachineConfigNamePrefix)
		require.Contains(t, DefaultAutoSizingEnvContent, "NODE_SIZING_ENABLED=false")
		require.Contains(t, DefaultAutoSizingEnvContent, "SYSTEM_RESERVED_MEMORY=1Gi")
		require.Contains(t, DefaultAutoSizingEnvContent, "SYSTEM_RESERVED_CPU=500m")
		require.Contains(t, DefaultAutoSizingEnvContent, "SYSTEM_RESERVED_ES=1Gi")
	})
}
