package kubeletconfig

import (
	"fmt"
	"reflect"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	configv1 "github.com/openshift/api/config/v1"
	osev1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"
)

func TestOriginalKubeletConfigDefaultNodeConfig(t *testing.T) {
	t.Parallel()
	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.NonePlatformType, "unrecognized"} {
		platform := platform
		t.Run(string(platform), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			f.ccLister = append(f.ccLister, cc)

			ctrl := f.newController()
			kubeletConfig, err := generateOriginalKubeletConfigIgn(cc, ctrl.templatesDir, "master", nil)
			if err != nil {
				t.Errorf("could not generate kubelet config from templates %v", err)
			}
			contents, err := ctrlcommon.DecodeIgnitionFileContents(kubeletConfig.Contents.Source, kubeletConfig.Contents.Compression)
			require.NoError(t, err)
			originalKubeConfig, err := decodeKubeletConfig(contents)
			require.NoError(t, err)

			if reflect.DeepEqual(originalKubeConfig.NodeStatusReportFrequency, metav1.Duration{osev1.DefaultNodeStatusUpdateFrequency}) {
				t.Errorf("expected the default node status update frequency to be %v, got: %v", osev1.DefaultNodeStatusUpdateFrequency, originalKubeConfig.NodeStatusReportFrequency)
			}
		})
	}
}

func TestNodeConfigDefault(t *testing.T) {
	t.Parallel()
	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.NonePlatformType, "unrecognized"} {
		platform := platform
		t.Run(string(platform), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.newController()

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			kc := newKubeletConfig("smaller-max-pods", &kubeletconfigv1beta1.KubeletConfiguration{MaxPods: 100}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/worker", ""))
			kubeletConfigKey, err := getManagedKubeletConfigKey(mcp, f.client, kc)
			require.NoError(t, err)
			mcs := helpers.NewMachineConfig(kubeletConfigKey, map[string]string{"node-role/worker": ""}, "dummy://", []ign3types.File{{}})
			mcsDeprecated := mcs.DeepCopy()
			mcsDeprecated.Name = fmt.Sprintf("97-%s-%s-kubelet", mcp.Name, mcp.ObjectMeta.UID)

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)

			nodeConfig := createNewDefaultNodeconfig()
			nodeConfig.Spec.WorkerLatencyProfile = osev1.DefaultUpdateDefaultReaction
			nodeConfig.Spec.CgroupMode = osev1.CgroupModeDefault
			f.nodeLister = append(f.nodeLister, nodeConfig)
			f.oseobjects = append(f.oseobjects, nodeConfig)

			f.expectGetMachineConfigAction(mcs)
			f.expectGetMachineConfigAction(mcsDeprecated)
			f.expectGetMachineConfigAction(mcs)
			f.expectCreateMachineConfigAction(mcs)
			f.runNode(getKeyFromConfigNode(nodeConfig, t))
		})
	}
}

func TestBootstrapNodeConfigDefault(t *testing.T) {
	t.Parallel()
	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.NonePlatformType, "unrecognized"} {
		platform := platform
		t.Run(string(platform), func(t *testing.T) {
			t.Parallel()

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp1 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			mcps := []*mcfgv1.MachineConfigPool{mcp}
			mcps = append(mcps, mcp1)

			features := createNewDefaultFeatureGate()
			configNode := createNewDefaultNodeconfig()

			mcs, err := RunNodeConfigBootstrap("../../../templates", features, cc, configNode, mcps)
			if err != nil {
				t.Errorf("could not run node config bootstrap: %v", err)
			}
			if len(mcs) != 2 {
				t.Errorf("expected a machine config generated with the default node config, got 0 machine configs")
			}
		})
	}
}

func TestBootstrapNoNodeConfig(t *testing.T) {
	t.Parallel()
	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.NonePlatformType, "unrecognized"} {
		platform := platform
		t.Run(string(platform), func(t *testing.T) {
			t.Parallel()

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			mcps := []*mcfgv1.MachineConfigPool{mcp}

			mcs, err := RunNodeConfigBootstrap("../../../templates", nil, cc, nil, mcps)
			if err == nil {
				t.Errorf("expected an error while generating the kubelet config with no node config")
			}
			if len(mcs) > 0 {
				t.Errorf("expected no machine configs with no node config but generated %v", len(mcs))
			}
		})
	}
}
