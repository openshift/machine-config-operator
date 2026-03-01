package kubeletconfig

import (
	"testing"
	"time"

	osev1 "github.com/openshift/api/config/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestShutdownGracePeriodConfiguration verifies that shutdownGracePeriod and
// shutdownGracePeriodCriticalPods are correctly configured in kubelet templates.
// This test ensures that:
// - Master and arbiter nodes get 270s/240s
// - Worker nodes get 90s/60s
// - SNO (SingleReplica) topology gets no shutdownGracePeriod (zero value)
func TestShutdownGracePeriodConfiguration(t *testing.T) {
	testCases := []struct {
		name                                string
		platform                            osev1.PlatformType
		topology                            osev1.TopologyMode
		nodeRole                            string
		expectedShutdownGracePeriod         time.Duration
		expectedShutdownGracePeriodCritical time.Duration
	}{
		{
			name:                                "AWS master",
			platform:                            osev1.AWSPlatformType,
			nodeRole:                            "master",
			expectedShutdownGracePeriod:         270 * time.Second,
			expectedShutdownGracePeriodCritical: 240 * time.Second,
		},
		{
			name:                                "AWS arbiter",
			platform:                            osev1.AWSPlatformType,
			nodeRole:                            "arbiter",
			expectedShutdownGracePeriod:         270 * time.Second,
			expectedShutdownGracePeriodCritical: 240 * time.Second,
		},
		{
			name:                                "AWS worker",
			platform:                            osev1.AWSPlatformType,
			nodeRole:                            "worker",
			expectedShutdownGracePeriod:         90 * time.Second,
			expectedShutdownGracePeriodCritical: 60 * time.Second,
		},
		{
			name:                                "GCP master",
			platform:                            osev1.GCPPlatformType,
			nodeRole:                            "master",
			expectedShutdownGracePeriod:         270 * time.Second,
			expectedShutdownGracePeriodCritical: 240 * time.Second,
		},
		{
			name:                                "None master",
			platform:                            osev1.NonePlatformType,
			nodeRole:                            "master",
			expectedShutdownGracePeriod:         270 * time.Second,
			expectedShutdownGracePeriodCritical: 240 * time.Second,
		},
		{
			name:                                "None worker",
			platform:                            osev1.NonePlatformType,
			nodeRole:                            "worker",
			expectedShutdownGracePeriod:         90 * time.Second,
			expectedShutdownGracePeriodCritical: 60 * time.Second,
		},
		{
			name:                                "SNO master",
			platform:                            osev1.NonePlatformType,
			topology:                            osev1.SingleReplicaTopologyMode,
			nodeRole:                            "master",
			expectedShutdownGracePeriod:         0,
			expectedShutdownGracePeriodCritical: 0,
		},
		{
			name:                                "SNO worker",
			platform:                            osev1.NonePlatformType,
			topology:                            osev1.SingleReplicaTopologyMode,
			nodeRole:                            "worker",
			expectedShutdownGracePeriod:         0,
			expectedShutdownGracePeriodCritical: 0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			fgHandler := ctrlcommon.NewFeatureGatesHardcodedHandler([]osev1.FeatureGateName{"Example"}, nil)

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, tc.platform)
			if tc.topology != "" {
				cc.Spec.Infra.Status.ControlPlaneTopology = tc.topology
			}
			f.ccLister = append(f.ccLister, cc)

			ctrl := f.newController(fgHandler)

			kubeletConfig, err := generateOriginalKubeletConfigIgn(cc, ctrl.templatesDir, tc.nodeRole, &osev1.APIServer{})
			require.NoError(t, err, "Failed to generate kubelet config for %s", tc.name)

			contents, err := ctrlcommon.DecodeIgnitionFileContents(kubeletConfig.Contents.Source, kubeletConfig.Contents.Compression)
			require.NoError(t, err, "Failed to decode ignition file contents for %s", tc.name)

			originalKubeConfig, err := DecodeKubeletConfig(contents)
			require.NoError(t, err, "Failed to decode kubelet config for %s", tc.name)

			assert.Equal(t, metav1.Duration{Duration: tc.expectedShutdownGracePeriod}, originalKubeConfig.ShutdownGracePeriod,
				"shutdownGracePeriod mismatch for %s", tc.name)
			assert.Equal(t, metav1.Duration{Duration: tc.expectedShutdownGracePeriodCritical}, originalKubeConfig.ShutdownGracePeriodCriticalPods,
				"shutdownGracePeriodCriticalPods mismatch for %s", tc.name)
		})
	}
}
