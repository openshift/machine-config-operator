package kubeletconfig

import (
	"testing"

	osev1 "github.com/openshift/api/config/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFailSwapOnConfiguration verifies that failSwapOn is correctly configured in kubelet templates.
// This test ensures that:
// - Master and arbiter nodes have failSwapOn: true (swap is disabled)
// - Worker nodes have failSwapOn: false (swap is allowed but controlled via memorySwap.swapBehavior)
// - All nodes have memorySwap.swapBehavior set to "NoSwap"
func TestFailSwapOnConfiguration(t *testing.T) {
	for _, platform := range []osev1.PlatformType{osev1.AWSPlatformType, osev1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			fgHandler := ctrlcommon.NewFeatureGatesHardcodedHandler([]osev1.FeatureGateName{"Example"}, nil)

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			f.ccLister = append(f.ccLister, cc)

			ctrl := f.newController(fgHandler)
			testCases := []struct {
				nodeRole     string
				expectedFail bool
			}{
				{"master", true},
				{"arbiter", true},
				{"worker", false},
			}

			for _, tc := range testCases {
				t.Run(tc.nodeRole, func(t *testing.T) {
					kubeletConfig, err := generateOriginalKubeletConfigIgn(cc, ctrl.templatesDir, tc.nodeRole, &osev1.APIServer{})
					require.NoError(t, err, "Failed to generate kubelet config for %s", tc.nodeRole)

					contents, err := ctrlcommon.DecodeIgnitionFileContents(kubeletConfig.Contents.Source, kubeletConfig.Contents.Compression)
					require.NoError(t, err, "Failed to decode ignition file contents for %s", tc.nodeRole)

					originalKubeConfig, err := DecodeKubeletConfig(contents)
					require.NoError(t, err, "Failed to decode kubelet config for %s", tc.nodeRole)

					require.NotNil(t, originalKubeConfig.FailSwapOn, "failSwapOn should not be nil for %s node role", tc.nodeRole)
					assert.Equal(t, tc.expectedFail, *originalKubeConfig.FailSwapOn, "failSwapOn should be %v for %s node role", tc.expectedFail, tc.nodeRole)

					assert.Equal(t, "NoSwap", originalKubeConfig.MemorySwap.SwapBehavior, "memorySwap.swapBehavior should be NoSwap for %s node role", tc.nodeRole)
				})
			}
		})
	}
}
