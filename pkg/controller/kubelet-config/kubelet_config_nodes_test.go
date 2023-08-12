package kubeletconfig

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
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
	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			f.ccLister = append(f.ccLister, cc)

			fgAccess := createNewDefaultFeatureGateAccess()
			ctrl := f.newController(fgAccess)

			kubeletConfig, err := generateOriginalKubeletConfigIgn(cc, ctrl.templatesDir, "master", fgAccess)
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
	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			fgAccess := createNewDefaultFeatureGateAccess()
			f.newController(fgAccess)

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
	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp1 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			mcps := []*mcfgv1.MachineConfigPool{mcp}
			mcps = append(mcps, mcp1)

			fgAccess := createNewDefaultFeatureGateAccess()
			configNode := createNewDefaultNodeconfig()

			mcs, err := RunNodeConfigBootstrap("../../../templates", fgAccess, cc, configNode, mcps)
			if err != nil {
				t.Errorf("could not run node config bootstrap: %v", err)
			}
			if len(mcs) != 2 {
				t.Errorf("expected %v machine configs generated with the default node config, got 0 machine configs", len(mcs))
			}
		})
	}
}

func TestBootstrapNoNodeConfig(t *testing.T) {
	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {

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

func TestNodeConfigCustom(t *testing.T) {
	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			fgAccess := createNewDefaultFeatureGateAccess()
			f.newController(fgAccess)

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			mcp1 := helpers.NewMachineConfigPool("custom", nil, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "node-role/custom", ""), "v0")

			kc := newKubeletConfig("smaller-max-pods", &kubeletconfigv1beta1.KubeletConfiguration{MaxPods: 100}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/worker", ""))
			kubeletConfigKey, err := getManagedKubeletConfigKey(mcp, f.client, kc)
			require.NoError(t, err)

			nodeKeyCustom, err := getManagedNodeConfigKey(mcp1, f.client)
			require.NoError(t, err)

			mcs := helpers.NewMachineConfig(kubeletConfigKey, map[string]string{"node-role/worker": ""}, "dummy://", []ign3types.File{{}})
			mcs1 := helpers.NewMachineConfig(nodeKeyCustom, map[string]string{}, "dummy://", []ign3types.File{{}})
			mcsDeprecated := mcs.DeepCopy()
			mcsDeprecated.Name = fmt.Sprintf("97-%s-%s-kubelet", mcp.Name, mcp.ObjectMeta.UID)

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)

			nodeConfig := &osev1.Node{

				ObjectMeta: metav1.ObjectMeta{
					Name: ctrlcommon.ClusterNodeInstanceName,
				},
				Spec: osev1.NodeSpec{
					CgroupMode: "v1",
				},
			}

			nodeConfig.Spec.WorkerLatencyProfile = osev1.DefaultUpdateDefaultReaction
			nodeConfig.Spec.CgroupMode = osev1.CgroupModeDefault
			f.nodeLister = append(f.nodeLister, nodeConfig)
			f.oseobjects = append(f.oseobjects, nodeConfig)

			c := f.newController(fgAccess)

			mcCustom, err := c.client.MachineconfigurationV1().MachineConfigs().Create(context.TODO(), mcs1, metav1.CreateOptions{})
			require.NoError(t, err)
			require.Equal(t, nodeKeyCustom, mcCustom.Name)

			mcList, err := c.client.MachineconfigurationV1().MachineConfigs().List(context.TODO(), metav1.ListOptions{})
			require.NoError(t, err)
			require.Len(t, mcList.Items, 1)
			require.Equal(t, nodeKeyCustom, mcList.Items[0].Name)

			err = c.syncNodeConfigHandler(nodeConfig.Name)
			require.NoError(t, err)

			mcList, err = c.client.MachineconfigurationV1().MachineConfigs().List(context.TODO(), metav1.ListOptions{})
			require.NoError(t, err)
			require.Len(t, mcList.Items, 1)
			require.NotEqual(t, nodeKeyCustom, mcList.Items[0].Name)
		})
	}
}
