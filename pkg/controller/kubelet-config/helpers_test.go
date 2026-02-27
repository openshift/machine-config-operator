package kubeletconfig

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"
	kubeletypes "k8s.io/kubernetes/pkg/kubelet/types"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
)

func TestWrapErrorWithCondition(t *testing.T) {
	tests := []struct {
		name            string
		err             error
		args            []interface{}
		expectedType    mcfgv1.KubeletConfigStatusConditionType
		expectedStatus  corev1.ConditionStatus
		expectedMessage string
	}{
		{
			name:            "error without args produces Failure condition with status True",
			err:             fmt.Errorf("KubeletConfiguration: swapBehavior is not allowed to be set, but contains: LimitedSwap"),
			args:            nil,
			expectedType:    mcfgv1.KubeletConfigFailure,
			expectedStatus:  corev1.ConditionTrue,
			expectedMessage: "Error: KubeletConfiguration: swapBehavior is not allowed to be set, but contains: LimitedSwap",
		},
		{
			name:            "error with formatted args produces Failure condition with status True",
			err:             fmt.Errorf("validation failed"),
			args:            []interface{}{"Failed to validate %s: %v", "kubelet config", "invalid field"},
			expectedType:    mcfgv1.KubeletConfigFailure,
			expectedStatus:  corev1.ConditionTrue,
			expectedMessage: "Failed to validate kubelet config: invalid field",
		},
		{
			name:            "nil error produces Success condition with status True",
			err:             nil,
			args:            nil,
			expectedType:    mcfgv1.KubeletConfigSuccess,
			expectedStatus:  corev1.ConditionTrue,
			expectedMessage: "Success",
		},
		{
			name:            "nil error with args still produces Success condition",
			err:             nil,
			args:            []interface{}{"Custom success message"},
			expectedType:    mcfgv1.KubeletConfigSuccess,
			expectedStatus:  corev1.ConditionTrue,
			expectedMessage: "Custom success message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			condition := wrapErrorWithCondition(tt.err, tt.args...)

			if condition.Type != tt.expectedType {
				t.Errorf("expected condition type %v, got %v", tt.expectedType, condition.Type)
			}

			if condition.Status != tt.expectedStatus {
				t.Errorf("expected condition status %v, got %v", tt.expectedStatus, condition.Status)
			}

			if condition.Message != tt.expectedMessage {
				t.Errorf("expected message %q, got %q", tt.expectedMessage, condition.Message)
			}
		})
	}
}

// TestReserveSystemCPUs tests that when reservedSystemCPUs is set,
// the systemReservedCgroup is cleared and enforceNodeAllocatable is set to ["pods"] only.
func TestReserveSystemCPUs(t *testing.T) {
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

			kubeletIgnition, _, _, err := generateKubeletIgnFiles(kubeletConfig, originalKubeConfig)
			require.NoError(t, err, "generateKubeletIgnFiles should not return an error")
			require.NotNil(t, kubeletIgnition, "kubelet ignition file should not be nil")

			contents, err := ctrlcommon.DecodeIgnitionFileContents(kubeletIgnition.Contents.Source, kubeletIgnition.Contents.Compression)
			require.NoError(t, err, "decoding ignition file contents should succeed")

			decodedConfig, err := DecodeKubeletConfig(contents)
			require.NoError(t, err, "decoding kubelet config should succeed")

			require.Equal(t, tc.expectedSystemReservedCgroup, decodedConfig.SystemReservedCgroup,
				"systemReservedCgroup should be %q but got %q", tc.expectedSystemReservedCgroup, decodedConfig.SystemReservedCgroup)

			require.Equal(t, tc.expectedEnforceNodeAllocatable, decodedConfig.EnforceNodeAllocatable,
				"enforceNodeAllocatable should be %v but got %v", tc.expectedEnforceNodeAllocatable, decodedConfig.EnforceNodeAllocatable)

			require.Equal(t, tc.reservedSystemCPUs, decodedConfig.ReservedSystemCPUs,
				"reservedSystemCPUs should be %q but got %q", tc.reservedSystemCPUs, decodedConfig.ReservedSystemCPUs)
		})
	}
}

// TestCGroupKubeletConfigSpec tests that generateKubeletIgnFiles
// properly merges user-provided kubelet configuration with the original config.
func TestCGroupKubeletConfigSpec(t *testing.T) {
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

// TestEmptyStringOverride tests that when a user explicitly
// sets systemReservedCgroup to an empty string
func TestEmptyStringOverride(t *testing.T) {
	originalKubeConfig := &kubeletconfigv1beta1.KubeletConfiguration{
		MaxPods:                110,
		SystemReservedCgroup:   "/system.slice",
		EnforceNodeAllocatable: []string{kubeletypes.NodeAllocatableEnforcementKey, kubeletypes.SystemReservedCompressibleEnforcementKey},
	}

	userKubeletConfig := &kubeletconfigv1beta1.KubeletConfiguration{
		MaxPods:                100,
		SystemReservedCgroup:   "",                                                  // User explicitly wants to clear this
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

// TestPartialUserConfig tests the bug scenario where:
// - Base config has systemReservedCgroup="/system.slice" and enforceNodeAllocatable with system-reserved-compressible
// - User provides custom config that doesn't mention systemReservedCgroup at all (e.g., only sets maxPods)
// - Expected: systemReservedCgroup should be preserved from base config (not cleared)
// This prevents validation error: "systemReservedCgroup must be specified when system-reserved is in enforceNodeAllocatable"
func TestPartialUserConfig(t *testing.T) {
	originalKubeConfig := &kubeletconfigv1beta1.KubeletConfiguration{
		MaxPods:                110,
		SystemReservedCgroup:   "/system.slice",
		EnforceNodeAllocatable: []string{kubeletypes.NodeAllocatableEnforcementKey, kubeletypes.SystemReservedCompressibleEnforcementKey},
	}

	// User only sets maxPods, doesn't mention systemReservedCgroup or enforceNodeAllocatable
	userKubeletConfig := &kubeletconfigv1beta1.KubeletConfiguration{
		MaxPods: 200,
	}

	userKubeletConfigRaw, err := EncodeKubeletConfig(userKubeletConfig, kubeletconfigv1beta1.SchemeGroupVersion, runtime.ContentTypeYAML)
	require.NoError(t, err)

	kubeletConfig := &mcfgv1.KubeletConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-partial-config",
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

	// Verify: maxPods was updated
	require.Equal(t, int32(200), decodedConfig.MaxPods,
		"MaxPods should be 200 from user config but got %d", decodedConfig.MaxPods)

	// Verify: systemReservedCgroup was preserved from base config (not cleared)
	require.Equal(t, "/system.slice", decodedConfig.SystemReservedCgroup,
		"systemReservedCgroup should be preserved as /system.slice from base config but got %q", decodedConfig.SystemReservedCgroup)

	// Verify: enforceNodeAllocatable was preserved from base config
	require.Equal(t, []string{kubeletypes.NodeAllocatableEnforcementKey, kubeletypes.SystemReservedCompressibleEnforcementKey}, decodedConfig.EnforceNodeAllocatable,
		"enforceNodeAllocatable should be preserved from base config but got %v", decodedConfig.EnforceNodeAllocatable)
}
