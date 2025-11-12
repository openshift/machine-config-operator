package kubeletconfig

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"
	kubeletypes "k8s.io/kubernetes/pkg/kubelet/types"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

// TestGenerateKubeletIgnFilesWithReservedSystemCPUs tests that when reservedSystemCPUs is set,
// the systemReservedCgroup is cleared and enforceNodeAllocatable is set to ["pods"] only.
func TestGenerateKubeletIgnFilesWithReservedSystemCPUs(t *testing.T) {
	testCases := []struct {
		name                              string
		reservedSystemCPUs                string
		initialSystemReservedCgroup       string
		initialEnforceNodeAllocatable     []string
		expectedSystemReservedCgroup      string
		expectedEnforceNodeAllocatable    []string
		shouldDisableSystemReservedCgroup bool
	}{
		{
			name:                              "reservedSystemCPUs set - should disable systemReservedCgroup",
			reservedSystemCPUs:                "0-1",
			initialSystemReservedCgroup:       "/system.slice",
			initialEnforceNodeAllocatable:     []string{kubeletypes.NodeAllocatableEnforcementKey, kubeletypes.SystemReservedCompressibleEnforcementKey},
			expectedSystemReservedCgroup:      "",
			expectedEnforceNodeAllocatable:    []string{kubeletypes.NodeAllocatableEnforcementKey},
			shouldDisableSystemReservedCgroup: true,
		},
		{
			name:                              "reservedSystemCPUs not set - should preserve systemReservedCgroup",
			reservedSystemCPUs:                "",
			initialSystemReservedCgroup:       "/system.slice",
			initialEnforceNodeAllocatable:     []string{kubeletypes.NodeAllocatableEnforcementKey, kubeletypes.SystemReservedCompressibleEnforcementKey},
			expectedSystemReservedCgroup:      "/system.slice",
			expectedEnforceNodeAllocatable:    []string{kubeletypes.NodeAllocatableEnforcementKey, kubeletypes.SystemReservedCompressibleEnforcementKey},
			shouldDisableSystemReservedCgroup: false,
		},
		{
			name:                              "reservedSystemCPUs set with empty systemReservedCgroup",
			reservedSystemCPUs:                "0-3",
			initialSystemReservedCgroup:       "",
			initialEnforceNodeAllocatable:     []string{kubeletypes.NodeAllocatableEnforcementKey},
			expectedSystemReservedCgroup:      "",
			expectedEnforceNodeAllocatable:    []string{kubeletypes.NodeAllocatableEnforcementKey},
			shouldDisableSystemReservedCgroup: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Setup: Create a base kubelet configuration with the initial values
			originalKubeConfig := &kubeletconfigv1beta1.KubeletConfiguration{
				ReservedSystemCPUs:     tc.reservedSystemCPUs,
				SystemReservedCgroup:   tc.initialSystemReservedCgroup,
				EnforceNodeAllocatable: tc.initialEnforceNodeAllocatable,
			}

			// Create a KubeletConfig CR (can be nil for this test)
			kubeletConfig := &mcfgv1.KubeletConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-kubelet-config",
				},
				Spec: mcfgv1.KubeletConfigSpec{},
			}

			// Execute: Generate the kubelet ignition files
			kubeletIgnition, _, _, err := generateKubeletIgnFiles(kubeletConfig, originalKubeConfig)
			require.NoError(t, err, "generateKubeletIgnFiles should not return an error")
			require.NotNil(t, kubeletIgnition, "kubelet ignition file should not be nil")

			// Verify: Decode the generated kubelet configuration from the ignition file
			contents, err := ctrlcommon.DecodeIgnitionFileContents(kubeletIgnition.Contents.Source, kubeletIgnition.Contents.Compression)
			require.NoError(t, err, "decoding ignition file contents should succeed")

			// Parse the YAML contents back into a KubeletConfiguration
			decodedConfig, err := DecodeKubeletConfig(contents)
			require.NoError(t, err, "decoding kubelet config should succeed")

			// Verify: Check that systemReservedCgroup matches expected value
			require.Equal(t, tc.expectedSystemReservedCgroup, decodedConfig.SystemReservedCgroup,
				"systemReservedCgroup should be %q but got %q", tc.expectedSystemReservedCgroup, decodedConfig.SystemReservedCgroup)

			// Verify: Check that enforceNodeAllocatable matches expected value
			require.Equal(t, tc.expectedEnforceNodeAllocatable, decodedConfig.EnforceNodeAllocatable,
				"enforceNodeAllocatable should be %v but got %v", tc.expectedEnforceNodeAllocatable, decodedConfig.EnforceNodeAllocatable)

			// Verify: Check that reservedSystemCPUs is preserved
			require.Equal(t, tc.reservedSystemCPUs, decodedConfig.ReservedSystemCPUs,
				"reservedSystemCPUs should be %q but got %q", tc.reservedSystemCPUs, decodedConfig.ReservedSystemCPUs)
		})
	}
}

// TestGenerateKubeletIgnFilesWithKubeletConfigSpec tests that generateKubeletIgnFiles
// properly merges user-provided kubelet configuration with the original config.
func TestGenerateKubeletIgnFilesWithKubeletConfigSpec(t *testing.T) {
	// Setup: Create a base kubelet configuration
	originalKubeConfig := &kubeletconfigv1beta1.KubeletConfiguration{
		MaxPods:                110,
		ReservedSystemCPUs:     "0-1",
		SystemReservedCgroup:   "/system.slice",
		EnforceNodeAllocatable: []string{kubeletypes.NodeAllocatableEnforcementKey, kubeletypes.SystemReservedCompressibleEnforcementKey},
	}

	// Setup: Create user-provided kubelet configuration with reservedSystemCPUs
	userKubeletConfig := &kubeletconfigv1beta1.KubeletConfiguration{
		MaxPods:            250,
		ReservedSystemCPUs: "0-3", // User wants to reserve more CPUs
	}

	// Encode the user config
	userKubeletConfigRaw, err := EncodeKubeletConfig(userKubeletConfig, kubeletconfigv1beta1.SchemeGroupVersion, runtime.ContentTypeYAML)
	require.NoError(t, err)

	// Create a KubeletConfig CR with user config
	kubeletConfig := &mcfgv1.KubeletConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-kubelet-config",
		},
		Spec: mcfgv1.KubeletConfigSpec{
			KubeletConfig: &runtime.RawExtension{
				Raw: userKubeletConfigRaw,
			},
		},
	}

	// Execute: Generate the kubelet ignition files
	kubeletIgnition, _, _, err := generateKubeletIgnFiles(kubeletConfig, originalKubeConfig)
	require.NoError(t, err, "generateKubeletIgnFiles should not return an error")
	require.NotNil(t, kubeletIgnition, "kubelet ignition file should not be nil")

	// Verify: Decode the generated kubelet configuration from the ignition file
	contents, err := ctrlcommon.DecodeIgnitionFileContents(kubeletIgnition.Contents.Source, kubeletIgnition.Contents.Compression)
	require.NoError(t, err, "decoding ignition file contents should succeed")

	// Parse the YAML contents back into a KubeletConfiguration
	decodedConfig, err := DecodeKubeletConfig(contents)
	require.NoError(t, err, "decoding kubelet config should succeed")

	// Verify: Check that user config was merged (MaxPods should be from user config)
	require.Equal(t, int32(250), decodedConfig.MaxPods,
		"MaxPods should be 250 from user config but got %d", decodedConfig.MaxPods)

	// Verify: Check that reservedSystemCPUs was merged from user config
	require.Equal(t, "0-3", decodedConfig.ReservedSystemCPUs,
		"reservedSystemCPUs should be 0-3 from user config but got %q", decodedConfig.ReservedSystemCPUs)

	// Verify: Check that systemReservedCgroup was cleared (since reservedSystemCPUs is set)
	require.Equal(t, "", decodedConfig.SystemReservedCgroup,
		"systemReservedCgroup should be empty when reservedSystemCPUs is set but got %q", decodedConfig.SystemReservedCgroup)

	// Verify: Check that enforceNodeAllocatable was set to only ["pods"]
	require.Equal(t, []string{kubeletypes.NodeAllocatableEnforcementKey}, decodedConfig.EnforceNodeAllocatable,
		"enforceNodeAllocatable should be [pods] when reservedSystemCPUs is set but got %v", decodedConfig.EnforceNodeAllocatable)
}

// TestGenerateKubeletIgnFilesWithEmptyStringOverride tests that when a user explicitly
// sets systemReservedCgroup to an empty string
func TestGenerateKubeletIgnFilesWithEmptyStringOverride(t *testing.T) {
	originalKubeConfig := &kubeletconfigv1beta1.KubeletConfiguration{
		MaxPods:                110,
		SystemReservedCgroup:   "/system.slice",
		EnforceNodeAllocatable: []string{kubeletypes.NodeAllocatableEnforcementKey, kubeletypes.SystemReservedCompressibleEnforcementKey},
	}

	userKubeletConfig := &kubeletconfigv1beta1.KubeletConfiguration{
		MaxPods:                100,
		SystemReservedCgroup:   "",                                                    // User explicitly wants to clear this
		EnforceNodeAllocatable: []string{kubeletypes.NodeAllocatableEnforcementKey}, // User only wants pods enforcement
	}

	userKubeletConfigRaw, err := EncodeKubeletConfig(userKubeletConfig, kubeletconfigv1beta1.SchemeGroupVersion, runtime.ContentTypeYAML)
	require.NoError(t, err)

	kubeletConfig := &mcfgv1.KubeletConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-empty-override",
		},
		Spec: mcfgv1.KubeletConfigSpec{
			KubeletConfig: &runtime.RawExtension{
				Raw: userKubeletConfigRaw,
			},
		},
	}

	kubeletIgnition, _, _, err := generateKubeletIgnFiles(kubeletConfig, originalKubeConfig)
	require.NoError(t, err, "generateKubeletIgnFiles should not return an error")
	require.NotNil(t, kubeletIgnition, "kubelet ignition file should not be nil")

	contents, err := ctrlcommon.DecodeIgnitionFileContents(kubeletIgnition.Contents.Source, kubeletIgnition.Contents.Compression)
	require.NoError(t, err, "decoding ignition file contents should succeed")

	decodedConfig, err := DecodeKubeletConfig(contents)
	require.NoError(t, err, "decoding kubelet config should succeed")

	require.Equal(t, int32(100), decodedConfig.MaxPods,
		"MaxPods should be 100 from user config but got %d", decodedConfig.MaxPods)

	// Verify: Check that systemReservedCgroup was set to empty string (not ignored)
	// This is the critical test - the empty string should override the default "/system.slice"
	require.Equal(t, "", decodedConfig.SystemReservedCgroup,
		"systemReservedCgroup should be empty string (overriding default) but got %q", decodedConfig.SystemReservedCgroup)

	require.Equal(t, []string{kubeletypes.NodeAllocatableEnforcementKey}, decodedConfig.EnforceNodeAllocatable,
		"enforceNodeAllocatable should be [pods] from user config but got %v", decodedConfig.EnforceNodeAllocatable)
}
