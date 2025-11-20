package kubeletconfig

import (
	"context"
	"fmt"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/helpers"
)

// TestCreateAutoSizingIgnConfig verifies that createAutoSizingIgnConfig generates a valid
// Ignition configuration containing the auto-sizing environment file with correct path and content.
// This test ensures the basic building block for auto-sizing machine configs is properly constructed.
func TestCreateAutoSizingIgnConfig(t *testing.T) {
	// Setup: Generate the auto-sizing ignition configuration
	rawIgn, err := createAutoSizingIgnConfig()
	require.NoError(t, err, "createAutoSizingIgnConfig should not return an error")
	require.NotNil(t, rawIgn, "generated ignition config should not be nil")

	// Parse the raw ignition config into structured format
	ignConfig, err := ctrlcommon.ParseAndConvertConfig(rawIgn)
	require.NoError(t, err, "parsing ignition config should succeed")
	require.Len(t, ignConfig.Storage.Files, 1, "ignition config should contain exactly one file")

	// Verify the file path matches the expected auto-sizing environment file location
	file := ignConfig.Storage.Files[0]
	require.Equal(t, AutoSizingEnvFilePath, file.Path,
		"file path should be %s but got %s", AutoSizingEnvFilePath, file.Path)

	// Decode and verify the file contents match the expected auto-sizing configuration
	contents, err := ctrlcommon.DecodeIgnitionFileContents(file.Contents.Source, file.Contents.Compression)
	require.NoError(t, err, "decoding file contents should succeed")
	require.Equal(t, DefaultAutoSizingEnvContent, string(contents),
		"file contents should match default auto-sizing environment content")
}

// TestNewAutoSizingMachineConfig validates that newAutoSizingMachineConfig creates MachineConfigs
// with correct naming, labels, annotations, and ignition content for different machine config pools.
// This ensures the auto-sizing configuration can be applied to any pool type (worker, master, custom).
func TestNewAutoSizingMachineConfig(t *testing.T) {
	testCases := []struct {
		name         string
		poolName     string
		expectedName string
		description  string
	}{
		{
			name:         "worker pool generates correct MC",
			poolName:     "worker",
			expectedName: "50-worker-auto-sizing-disabled",
			description:  "worker pools should get properly formatted auto-sizing MC",
		},
		{
			name:         "master pool generates correct MC",
			poolName:     "master",
			expectedName: "50-master-auto-sizing-disabled",
			description:  "master pools should get properly formatted auto-sizing MC",
		},
		{
			name:         "custom pool generates correct MC",
			poolName:     "custom",
			expectedName: "50-custom-auto-sizing-disabled",
			description:  "custom pools should get properly formatted auto-sizing MC",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Setup: Create a machine config pool with the specified name
			pool := helpers.NewMachineConfigPool(tc.poolName, nil, helpers.WorkerSelector, "v0")

			// Execute: Generate the auto-sizing MachineConfig for this pool
			mc, err := newAutoSizingMachineConfig(pool)
			require.NoError(t, err, "newAutoSizingMachineConfig should not return an error for pool %s", tc.poolName)
			require.NotNil(t, mc, "generated MachineConfig should not be nil")

			// Verify: Check the MachineConfig name follows the expected pattern
			require.Equal(t, tc.expectedName, mc.Name,
				"MachineConfig name should be %s but got %s", tc.expectedName, mc.Name)

			// Verify: Check the MachineConfig has the correct role label
			require.Equal(t, tc.poolName, mc.Labels[mcfgv1.MachineConfigRoleLabelKey],
				"MachineConfig should have role label %s but got %s",
				tc.poolName, mc.Labels[mcfgv1.MachineConfigRoleLabelKey])

			// Verify: Check the annotation is present and correct
			require.Contains(t, mc.Annotations, "openshift-patch-reference",
				"MachineConfig should contain openshift-patch-reference annotation")
			require.Equal(t, "machineConfig-to-set-the-default-behavior-of-NODE_SIZING_ENABLED",
				mc.Annotations["openshift-patch-reference"],
				"openshift-patch-reference annotation should have the correct value")

			// Verify: Parse and validate the embedded ignition configuration
			ignConfig, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
			require.NoError(t, err, "parsing embedded ignition config should succeed")
			require.Len(t, ignConfig.Storage.Files, 1,
				"ignition config should contain exactly one file")

			// Verify: Check the file path is correct
			file := ignConfig.Storage.Files[0]
			require.Equal(t, AutoSizingEnvFilePath, file.Path,
				"file path should be %s but got %s", AutoSizingEnvFilePath, file.Path)

			// Verify: Decode and check the file contents
			contents, err := ctrlcommon.DecodeIgnitionFileContents(file.Contents.Source, file.Contents.Compression)
			require.NoError(t, err, "decoding file contents should succeed")
			require.Equal(t, DefaultAutoSizingEnvContent, string(contents),
				"file contents should match default auto-sizing environment content")
		})
	}
}

// TestCreateAutoSizingMachineConfigIfNeeded tests the idempotent creation of auto-sizing MachineConfigs.
// It verifies that the function creates a new MC when needed and gracefully handles the case when
// the MC already exists, ensuring no duplicate MCs are created.
func TestCreateAutoSizingMachineConfigIfNeeded(t *testing.T) {
	t.Run("creates MC when it doesn't exist", func(t *testing.T) {
		// Setup: Initialize test fixture and controller
		f := newFixture(t)
		ctrl := f.newController(nil)

		// Setup: Create a test pool and determine the expected MC name
		pool := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
		autoSizingKey := fmt.Sprintf(AutoSizingMachineConfigNamePrefix, pool.Name)

		// Setup: Configure expectation that the MC doesn't exist initially
		f.expectGetMachineConfigAction(helpers.NewMachineConfig(autoSizingKey, map[string]string{"node-role/worker": ""}, "dummy://", []ign3types.File{{}}))

		// Execute: Attempt to create the auto-sizing MC if needed
		ctx := context.Background()
		err := ctrl.createAutoSizingMCIfNeeded(ctx, pool)
		require.NoError(t, err, "createAutoSizingMCIfNeeded should successfully create MC when it doesn't exist")

		// Verify: Confirm the MachineConfig was created with the correct name
		mc, err := ctrl.client.MachineconfigurationV1().MachineConfigs().Get(ctx, autoSizingKey, metav1.GetOptions{})
		require.NoError(t, err, "getting the created MachineConfig should succeed")
		require.Equal(t, autoSizingKey, mc.Name,
			"created MachineConfig name should be %s but got %s", autoSizingKey, mc.Name)
	})

	t.Run("skips creation when MC already exists", func(t *testing.T) {
		// Setup: Initialize test fixture and controller
		f := newFixture(t)
		ctrl := f.newController(nil)

		// Setup: Create a test pool and determine the expected MC name
		pool := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
		autoSizingKey := fmt.Sprintf(AutoSizingMachineConfigNamePrefix, pool.Name)

		ctx := context.Background()

		// Setup: Pre-create the MachineConfig to simulate it already existing
		existingMC := helpers.NewMachineConfig(autoSizingKey, map[string]string{"node-role/worker": ""}, "dummy://", []ign3types.File{{}})
		_, err := ctrl.client.MachineconfigurationV1().MachineConfigs().Create(ctx, existingMC, metav1.CreateOptions{})
		require.NoError(t, err, "pre-creating existing MachineConfig should succeed")

		// Execute: Attempt to create the auto-sizing MC (should be idempotent)
		err = ctrl.createAutoSizingMCIfNeeded(ctx, pool)
		require.NoError(t, err, "createAutoSizingMCIfNeeded should not error when MC already exists")

		// Verify: Confirm no duplicate MCs were created
		mcList, err := ctrl.client.MachineconfigurationV1().MachineConfigs().List(ctx, metav1.ListOptions{})
		require.NoError(t, err, "listing MachineConfigs should succeed")
		require.Len(t, mcList.Items, 1,
			"should have exactly one MachineConfig, no duplicates should be created")
	})
}

// TestEnsureAutoSizingMachineConfigs verifies that the controller correctly ensures auto-sizing
// MachineConfigs exist for all machine config pools in the cluster. This tests the high-level
// orchestration function that processes multiple pools.
func TestEnsureAutoSizingMachineConfigs(t *testing.T) {
	t.Run("creates MCs for all pools", func(t *testing.T) {
		// Setup: Initialize test fixture and disable action validation for simplicity
		f := newFixture(t)
		f.skipActionsValidation = true

		// Setup: Create multiple machine config pools (worker and master)
		workerPool := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
		masterPool := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
		f.mcpLister = append(f.mcpLister, workerPool, masterPool)

		ctrl := f.newController(nil)

		// Execute: Ensure auto-sizing MCs exist for all pools
		ctx := context.Background()
		err := ctrl.ensureAutoSizingMachineConfigs(ctx)
		require.NoError(t, err, "ensureAutoSizingMachineConfigs should succeed for multiple pools")

		// Verify: Confirm MachineConfigs were created for both pools
		mcList, err := ctrl.client.MachineconfigurationV1().MachineConfigs().List(ctx, metav1.ListOptions{})
		require.NoError(t, err, "listing MachineConfigs should succeed")
		require.Len(t, mcList.Items, 2,
			"should have exactly 2 MachineConfigs (one for worker, one for master)")

		// Verify: Check that both expected MCs are present by name
		mcNames := make(map[string]bool)
		for _, mc := range mcList.Items {
			mcNames[mc.Name] = true
		}

		require.True(t, mcNames["50-worker-auto-sizing-disabled"],
			"should have created MC for worker pool")
		require.True(t, mcNames["50-master-auto-sizing-disabled"],
			"should have created MC for master pool")
	})

	t.Run("handles pools with no existing MCs", func(t *testing.T) {
		// Setup: Initialize test fixture with a custom pool
		f := newFixture(t)
		f.skipActionsValidation = true

		// Setup: Create a custom machine config pool with specific selector
		customPool := helpers.NewMachineConfigPool("custom", nil, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "node-role/custom", ""), "v0")
		f.mcpLister = append(f.mcpLister, customPool)

		ctrl := f.newController(nil)

		// Execute: Ensure auto-sizing MC exists for the custom pool
		ctx := context.Background()
		err := ctrl.ensureAutoSizingMachineConfigs(ctx)
		require.NoError(t, err, "ensureAutoSizingMachineConfigs should succeed for custom pool")

		// Verify: Confirm a single MachineConfig was created for the custom pool
		mcList, err := ctrl.client.MachineconfigurationV1().MachineConfigs().List(ctx, metav1.ListOptions{})
		require.NoError(t, err, "listing MachineConfigs should succeed")
		require.Len(t, mcList.Items, 1,
			"should have exactly one MachineConfig for the custom pool")
		require.Equal(t, "50-custom-auto-sizing-disabled", mcList.Items[0].Name,
			"MachineConfig name should be 50-custom-auto-sizing-disabled but got %s", mcList.Items[0].Name)
	})
}

// TestRunAutoSizingBootstrap validates the bootstrap function that generates auto-sizing MachineConfigs
// for cluster initialization. This function is used during cluster bootstrap to ensure all pools
// have auto-sizing configurations from the start.
func TestRunAutoSizingBootstrap(t *testing.T) {
	t.Run("generates MCs for all pools", func(t *testing.T) {
		// Setup: Create worker and master pools for a typical cluster
		workerPool := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
		masterPool := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
		pools := []*mcfgv1.MachineConfigPool{workerPool, masterPool}

		// Execute: Generate auto-sizing MachineConfigs for bootstrap
		mcs, err := RunAutoSizingBootstrap(pools)
		require.NoError(t, err, "RunAutoSizingBootstrap should not return an error")
		require.Len(t, mcs, 2, "should generate 2 MachineConfigs (one for each pool)")

		// Verify: Build a map of MC names to MC objects for easy lookup
		mcNames := make(map[string]*mcfgv1.MachineConfig)
		for _, mc := range mcs {
			mcNames[mc.Name] = mc
		}

		// Verify: Check that both expected MCs were generated
		require.Contains(t, mcNames, "50-worker-auto-sizing-disabled",
			"should contain worker auto-sizing MC")
		require.Contains(t, mcNames, "50-master-auto-sizing-disabled",
			"should contain master auto-sizing MC")

		// Verify: Validate the worker MC has correct labels and annotations
		workerMC := mcNames["50-worker-auto-sizing-disabled"]
		require.Equal(t, "worker", workerMC.Labels[mcfgv1.MachineConfigRoleLabelKey],
			"worker MC should have correct role label")
		require.Equal(t, "machineConfig-to-set-the-default-behavior-of-NODE_SIZING_ENABLED",
			workerMC.Annotations["openshift-patch-reference"],
			"worker MC should have correct patch reference annotation")

		// Verify: Validate the ignition config structure
		ignConfig, err := ctrlcommon.ParseAndConvertConfig(workerMC.Spec.Config.Raw)
		require.NoError(t, err, "parsing worker MC ignition config should succeed")
		require.Len(t, ignConfig.Storage.Files, 1,
			"worker MC ignition config should contain exactly one file")
		require.Equal(t, AutoSizingEnvFilePath, ignConfig.Storage.Files[0].Path,
			"file path should be %s but got %s", AutoSizingEnvFilePath, ignConfig.Storage.Files[0].Path)
	})

	t.Run("handles empty pool list", func(t *testing.T) {
		// Setup: Create an empty pool list (edge case)
		pools := []*mcfgv1.MachineConfigPool{}

		// Execute: Generate auto-sizing MCs for empty pool list
		mcs, err := RunAutoSizingBootstrap(pools)
		require.NoError(t, err, "RunAutoSizingBootstrap should handle empty pool list gracefully")
		require.Len(t, mcs, 0, "should generate no MachineConfigs for empty pool list")
	})

	t.Run("handles single pool", func(t *testing.T) {
		// Setup: Create a single custom pool
		customPool := helpers.NewMachineConfigPool("custom", nil, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "node-role/custom", ""), "v0")
		pools := []*mcfgv1.MachineConfigPool{customPool}

		// Execute: Generate auto-sizing MC for a single pool
		mcs, err := RunAutoSizingBootstrap(pools)
		require.NoError(t, err, "RunAutoSizingBootstrap should handle single pool")
		require.Len(t, mcs, 1, "should generate exactly one MachineConfig for single pool")
		require.Equal(t, "50-custom-auto-sizing-disabled", mcs[0].Name,
			"MC name should be 50-custom-auto-sizing-disabled but got %s", mcs[0].Name)
	})
}

// TestAutoSizingConstants validates that critical auto-sizing constants have the expected values.
// These constants define the file paths, naming patterns, and default content for auto-sizing
// configurations. Changes to these values could break compatibility with existing clusters.
func TestAutoSizingConstants(t *testing.T) {
	t.Run("verify constant values", func(t *testing.T) {
		// Verify: Check the auto-sizing environment file path is correct
		require.Equal(t, "/etc/node-sizing-enabled.env", AutoSizingEnvFilePath,
			"AutoSizingEnvFilePath should be /etc/node-sizing-enabled.env but got %s", AutoSizingEnvFilePath)

		// Verify: Check the MachineConfig naming pattern is correct
		require.Equal(t, "50-%s-auto-sizing-disabled", AutoSizingMachineConfigNamePrefix,
			"AutoSizingMachineConfigNamePrefix should be 50-%%s-auto-sizing-disabled but got %s", AutoSizingMachineConfigNamePrefix)

		// Verify: Check that default content includes the NODE_SIZING_ENABLED setting
		require.Contains(t, DefaultAutoSizingEnvContent, "NODE_SIZING_ENABLED=false",
			"DefaultAutoSizingEnvContent should contain NODE_SIZING_ENABLED=false")

		// Verify: Check that default content includes system reserved memory
		require.Contains(t, DefaultAutoSizingEnvContent, "SYSTEM_RESERVED_MEMORY=1Gi",
			"DefaultAutoSizingEnvContent should contain SYSTEM_RESERVED_MEMORY=1Gi")

		// Verify: Check that default content includes system reserved CPU
		require.Contains(t, DefaultAutoSizingEnvContent, "SYSTEM_RESERVED_CPU=500m",
			"DefaultAutoSizingEnvContent should contain SYSTEM_RESERVED_CPU=500m")

		// Verify: Check that default content includes system reserved ephemeral storage
		require.Contains(t, DefaultAutoSizingEnvContent, "SYSTEM_RESERVED_ES=1Gi",
			"DefaultAutoSizingEnvContent should contain SYSTEM_RESERVED_ES=1Gi")
	})
}
