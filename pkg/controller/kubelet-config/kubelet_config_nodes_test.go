package kubeletconfig

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"

	configv1 "github.com/openshift/api/config/v1"
	osev1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/helpers"
)

func TestOriginalKubeletConfigDefaultNodeConfig(t *testing.T) {
	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			f.ccLister = append(f.ccLister, cc)

			fgHandler := ctrlcommon.NewFeatureGatesHardcodedHandler([]osev1.FeatureGateName{"Example"}, nil)
			ctrl := f.newController(fgHandler)

			kubeletConfig, err := generateOriginalKubeletConfigIgn(cc, ctrl.templatesDir, "master", nil)
			if err != nil {
				t.Errorf("could not generate kubelet config from templates %v", err)
			}
			contents, err := ctrlcommon.DecodeIgnitionFileContents(kubeletConfig.Contents.Source, kubeletConfig.Contents.Compression)
			require.NoError(t, err)
			originalKubeConfig, err := DecodeKubeletConfig(contents)
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
			fgHandler := ctrlcommon.NewFeatureGatesHardcodedHandler([]osev1.FeatureGateName{"Example"}, nil)
			f.newController(fgHandler)

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
			nodeConfig.Spec.CgroupMode = osev1.CgroupModeV2
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
	configNodeCgroupDefault := createNewDefaultNodeconfig()
	configNodeCgroupV2 := createNewDefaultNodeconfigWithCgroup(osev1.CgroupModeV2)
	configNodeCgroupV1 := createNewDefaultNodeconfigWithCgroup(osev1.CgroupMode("v1"))

	expected := map[*osev1.Node]struct {
		Name             string
		MasterKernelArgs []string
		WorkerKernelArgs []string
	}{
		configNodeCgroupDefault: {
			Name:             "Default",
			MasterKernelArgs: []string{"systemd.unified_cgroup_hierarchy=1", "cgroup_no_v1=\"all\"", "psi=0"},
			WorkerKernelArgs: []string{"systemd.unified_cgroup_hierarchy=1", "cgroup_no_v1=\"all\"", "psi=0"},
		},
		configNodeCgroupV2: {
			Name:             "Cgroupv2",
			MasterKernelArgs: []string{"systemd.unified_cgroup_hierarchy=1", "cgroup_no_v1=\"all\"", "psi=0"},
			WorkerKernelArgs: []string{"systemd.unified_cgroup_hierarchy=1", "cgroup_no_v1=\"all\"", "psi=0"},
		},
	}

	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp1 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			mcps := []*mcfgv1.MachineConfigPool{mcp}
			mcps = append(mcps, mcp1)
			fgHandler := ctrlcommon.NewFeatureGatesHardcodedHandler([]osev1.FeatureGateName{"Example"}, nil)

			for _, configNode := range []*osev1.Node{configNodeCgroupDefault, configNodeCgroupV2, configNodeCgroupV1} {
				expect := expected[configNode]
				t.Run(fmt.Sprintf("Testing %v", expect.Name), func(t *testing.T) {
					mcs, err := RunNodeConfigBootstrap("../../../templates", fgHandler, cc, configNode, mcps, nil)
					if configNode == configNodeCgroupV1 {
						require.Error(t, err)
					} else {
						if err != nil {
							t.Errorf("could not run node config bootstrap: %v", err)
						}
						// Bootstrap now creates 2 MachineConfigs (auto-sizing MCs are created separately):
						// 1. master node config (97-master-generated-kubelet)
						// 2. worker node config (97-worker-generated-kubelet)
						expectedCount := 2
						if len(mcs) != expectedCount {
							t.Errorf("expected %v machine configs generated with the default node config, got %d machine configs", expectedCount, len(mcs))
						}
						// The kernel args are on the node config MCs (indices 0 and 1)
						require.Equal(t, expect.MasterKernelArgs, mcs[0].Spec.KernelArguments)
					}
				})
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

			mcs, err := RunNodeConfigBootstrap("../../../templates", nil, cc, nil, mcps, nil)
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
			features := &osev1.FeatureGate{
				ObjectMeta: metav1.ObjectMeta{
					Name: ctrlcommon.ClusterFeatureInstanceName,
				},
				Spec: osev1.FeatureGateSpec{
					FeatureGateSelection: osev1.FeatureGateSelection{
						FeatureSet: osev1.CustomNoUpgrade,
						CustomNoUpgrade: &osev1.CustomFeatureGates{
							Enabled: []osev1.FeatureGateName{"CSIMigration"},
						},
					},
				},
			}
			fgHandler := ctrlcommon.NewFeatureGatesHardcodedHandler(features.Spec.FeatureGateSelection.CustomNoUpgrade.Enabled, features.Spec.FeatureGateSelection.CustomNoUpgrade.Disabled)
			f.newController(fgHandler)

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
			nodeConfig.Spec.CgroupMode = osev1.CgroupModeV2
			f.nodeLister = append(f.nodeLister, nodeConfig)
			f.oseobjects = append(f.oseobjects, nodeConfig)

			c := f.newController(fgHandler)

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
			// Now expecting 1 MachineConfig:
			// 1. Node config MC for worker (97-worker-generated-kubelet)
			// Note: The pre-existing nodeKeyCustom (97-custom-generated-kubelet) is cleaned up by
			// cleanUpDuplicatedMC because it was created without the proper annotation
			require.Len(t, mcList.Items, 1)
			// Verify MachineConfig is present
			require.Equal(t, "97-worker-generated-kubelet", mcList.Items[0].Name)
		})
	}
}
