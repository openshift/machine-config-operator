package kubeletconfig

import (
	"context"
	"reflect"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"

	configv1 "github.com/openshift/api/config/v1"
	osev1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/require"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/helpers"
)

func TestFeatureGateDrift(t *testing.T) {
	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			f.ccLister = append(f.ccLister, cc)

			features := &osev1.FeatureGate{
				ObjectMeta: metav1.ObjectMeta{
					Name: ctrlcommon.ClusterFeatureInstanceName,
				},
				Spec: osev1.FeatureGateSpec{
					FeatureGateSelection: osev1.FeatureGateSelection{
						FeatureSet: osev1.CustomNoUpgrade,
						CustomNoUpgrade: &osev1.CustomFeatureGates{
							Enabled:  []osev1.FeatureGateName{"CSIMigration"},
							Disabled: []osev1.FeatureGateName{"DisableKubeletCloudCredentialProviders"},
						},
					},
				},
			}
			fgHandler := ctrlcommon.NewFeatureGatesHardcodedHandler(features.Spec.FeatureGateSelection.CustomNoUpgrade.Enabled, features.Spec.FeatureGateSelection.CustomNoUpgrade.Disabled)
			ctrl := f.newController(fgHandler)

			// Generate kubelet config with feature gates applied
			kubeletConfig, _, err := generateOriginalKubeletConfigWithFeatureGates(cc, ctrl.templatesDir, "master", fgHandler, nil)
			require.NoError(t, err)

			t.Logf("Generated Kubelet Config Feature Gates: %v", kubeletConfig.FeatureGates)

			defaultFeatureGates := generateFeatureMap(fgHandler)
			require.NoError(t, err)
			t.Logf("Expected Feature Gates: %v", *defaultFeatureGates)

			if !reflect.DeepEqual(kubeletConfig.FeatureGates, *defaultFeatureGates) {
				t.Errorf("Generated kubelet configuration feature gates do not match expected feature gates: generated=%v, expected=%v", kubeletConfig.FeatureGates, *defaultFeatureGates)
			}
		})
	}
}

func TestFeaturesDefault(t *testing.T) {
	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			fgHandler := ctrlcommon.NewFeatureGatesHardcodedHandler([]osev1.FeatureGateName{"CSIMigration"}, nil)
			f.newController(fgHandler)

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			kc1 := newKubeletConfig("smaller-max-pods", &kubeletconfigv1beta1.KubeletConfiguration{MaxPods: 100}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))
			kc2 := newKubeletConfig("bigger-max-pods", &kubeletconfigv1beta1.KubeletConfiguration{MaxPods: 250}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))
			kubeletConfigKey1, err := getManagedKubeletConfigKey(mcp, f.client, kc1)
			require.NoError(t, err)
			kubeletConfigKey2, err := getManagedKubeletConfigKey(mcp2, f.client, kc2)
			require.NoError(t, err)
			mcs := helpers.NewMachineConfig(kubeletConfigKey1, map[string]string{"node-role/master": ""}, "dummy://", []ign3types.File{{}})
			mcs2 := helpers.NewMachineConfig(kubeletConfigKey2, map[string]string{"node-role/worker": ""}, "dummy://", []ign3types.File{{}})
			mcsDeprecated := mcs.DeepCopy()
			mcsDeprecated.Name = getManagedFeaturesKeyDeprecated(mcp)
			mcs2Deprecated := mcs2.DeepCopy()
			mcs2Deprecated.Name = getManagedFeaturesKeyDeprecated(mcp2)

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)

			features := createNewDefaultFeatureGate()
			f.featLister = append(f.featLister, features)

			f.expectGetMachineConfigAction(mcs)
			f.expectGetMachineConfigAction(mcsDeprecated)
			f.expectGetMachineConfigAction(mcs)
			f.expectCreateMachineConfigAction(mcs)
			f.expectGetMachineConfigAction(mcs2)
			f.expectGetMachineConfigAction(mcs2Deprecated)
			f.expectGetMachineConfigAction(mcs2)
			f.expectCreateMachineConfigAction(mcs2)
			f.expectGetMachineConfigAction(mcs2Deprecated)
			f.expectGetMachineConfigAction(mcs2)

			f.runFeature(getKeyFromFeatureGate(features, t), fgHandler)
		})
	}
}

func TestFeaturesCustomNoUpgrade(t *testing.T) {
	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
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

			f := newFixture(t)
			f.newController(fgHandler)

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			kc1 := newKubeletConfig("smaller-max-pods", &kubeletconfigv1beta1.KubeletConfiguration{MaxPods: 100}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))
			kc2 := newKubeletConfig("bigger-max-pods", &kubeletconfigv1beta1.KubeletConfiguration{MaxPods: 250}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))
			kubeletConfigKey1, err := getManagedKubeletConfigKey(mcp, f.client, kc1)
			require.NoError(t, err)
			kubeletConfigKey2, err := getManagedKubeletConfigKey(mcp2, f.client, kc2)
			require.NoError(t, err)
			mcs := helpers.NewMachineConfig(kubeletConfigKey1, map[string]string{"node-role/master": ""}, "dummy://", []ign3types.File{{}})
			mcs2 := helpers.NewMachineConfig(kubeletConfigKey2, map[string]string{"node-role/worker": ""}, "dummy://", []ign3types.File{{}})
			mcsDeprecated := mcs.DeepCopy()
			mcsDeprecated.Name = getManagedFeaturesKeyDeprecated(mcp)
			mcs2Deprecated := mcs2.DeepCopy()
			mcs2Deprecated.Name = getManagedFeaturesKeyDeprecated(mcp2)

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.featLister = append(f.featLister, features)

			f.expectGetMachineConfigAction(mcs)
			f.expectGetMachineConfigAction(mcsDeprecated)
			f.expectGetMachineConfigAction(mcs)
			f.expectCreateMachineConfigAction(mcs)
			f.expectGetMachineConfigAction(mcs2)
			f.expectGetMachineConfigAction(mcs2Deprecated)
			f.expectGetMachineConfigAction(mcs2)
			f.expectCreateMachineConfigAction(mcs2)
			f.expectGetMachineConfigAction(mcs2Deprecated)
			f.expectGetMachineConfigAction(mcs2)
			f.runFeature(getKeyFromFeatureGate(features, t), fgHandler)
		})
	}
}

func TestBootstrapFeaturesDefault(t *testing.T) {
	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			mcps := []*mcfgv1.MachineConfigPool{mcp, mcp2}

			fgHandler := ctrlcommon.NewFeatureGatesHardcodedHandler(nil, nil)

			mcs, err := RunFeatureGateBootstrap("../../../templates", fgHandler, nil, cc, mcps, nil)
			if err != nil {
				t.Errorf("could not run feature gate bootstrap: %v", err)
			}
			if len(mcs) == 0 {
				t.Errorf("expected no machine config generated with the default feature gate, got %d configs", len(mcs))
			}
		})
	}
}

func TestBootstrapFeaturesCustomNoUpgrade(t *testing.T) {
	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			mcps := []*mcfgv1.MachineConfigPool{mcp, mcp2}

			fgHandler := ctrlcommon.NewFeatureGatesHardcodedHandler([]osev1.FeatureGateName{"CSIMigration"}, nil)

			mcs, err := RunFeatureGateBootstrap("../../../templates", fgHandler, nil, cc, mcps, nil)
			if err != nil {
				t.Errorf("could not run feature gate bootstrap: %v", err)
			}
			if len(mcs) != 2 {
				t.Errorf("expected 2 machine configs generated with the custom feature gate, got %d configs", len(mcs))
			}

			for _, mc := range mcs {
				ignCfg, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
				regfile := ignCfg.Storage.Files[0]
				conf, err := ctrlcommon.DecodeIgnitionFileContents(regfile.Contents.Source, regfile.Contents.Compression)
				require.NoError(t, err)

				originalKubeConfig, err := DecodeKubeletConfig(conf)
				require.NoError(t, err)

				features := &osev1.FeatureGate{
					ObjectMeta: metav1.ObjectMeta{
						Name: ctrlcommon.ClusterFeatureInstanceName,
					},
					Spec: osev1.FeatureGateSpec{
						FeatureGateSelection: osev1.FeatureGateSelection{
							FeatureSet: osev1.CustomNoUpgrade,
							CustomNoUpgrade: &osev1.CustomFeatureGates{
								Enabled: []osev1.FeatureGateName{"Example"},
							},
						},
					},
				}

				fgHandler = ctrlcommon.NewFeatureGatesHardcodedHandler(features.Spec.FeatureGateSelection.CustomNoUpgrade.Enabled, features.Spec.FeatureGateSelection.CustomNoUpgrade.Disabled)
				defaultFeatureGates := generateFeatureMap(fgHandler)
				if err != nil {
					t.Errorf("could not generate defaultFeatureGates: %v", err)
				}
				if reflect.DeepEqual(originalKubeConfig.FeatureGates, *defaultFeatureGates) {
					t.Errorf("template FeatureGates should not match default openshift/api FeatureGates: (default=%v)", defaultFeatureGates)
				}
				if !originalKubeConfig.FeatureGates["CSIMigration"] {
					t.Errorf("template FeatureGates should contain CSIMigration: %v", originalKubeConfig.FeatureGates)
				}
			}
		})
	}
}

func TestFeaturesCustomNoUpgradeRemoveUnmanagedMC(t *testing.T) {
	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
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

			f := newFixture(t)
			f.newController(fgHandler)

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			mcp3 := helpers.NewMachineConfigPool("custom", nil, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "node-role/custom", ""), "v0")
			kc1 := newKubeletConfig("smaller-max-pods", &kubeletconfigv1beta1.KubeletConfiguration{MaxPods: 100}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))
			kc2 := newKubeletConfig("bigger-max-pods", &kubeletconfigv1beta1.KubeletConfiguration{MaxPods: 250}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))
			kubeletConfigKey1, err := getManagedKubeletConfigKey(mcp, f.client, kc1)
			require.NoError(t, err)
			kubeletConfigKey2, err := getManagedKubeletConfigKey(mcp2, f.client, kc2)
			require.NoError(t, err)

			featureKeyCustom, err := getManagedFeaturesKey(mcp3, f.client)
			require.NoError(t, err)
			mcs := helpers.NewMachineConfig(kubeletConfigKey1, map[string]string{"node-role/master": ""}, "dummy://", []ign3types.File{{}})
			mcs2 := helpers.NewMachineConfig(kubeletConfigKey2, map[string]string{"node-role/worker": ""}, "dummy://", []ign3types.File{{}})
			mcs3 := helpers.NewMachineConfig(featureKeyCustom, map[string]string{}, "dummy://", []ign3types.File{{}})
			mcsDeprecated := mcs.DeepCopy()
			mcsDeprecated.Name = getManagedFeaturesKeyDeprecated(mcp)
			mcs2Deprecated := mcs2.DeepCopy()
			mcs2Deprecated.Name = getManagedFeaturesKeyDeprecated(mcp2)

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)

			f.featLister = append(f.featLister, features)
			c := f.newController(fgHandler)

			c.fgHandler = ctrlcommon.NewFeatureGatesHardcodedHandler(features.Spec.FeatureGateSelection.CustomNoUpgrade.Enabled, features.Spec.FeatureGateSelection.CustomNoUpgrade.Disabled)

			mcCustom, err := c.client.MachineconfigurationV1().MachineConfigs().Create(context.TODO(), mcs3, metav1.CreateOptions{})
			require.NoError(t, err)
			require.Equal(t, featureKeyCustom, mcCustom.Name)

			mcList, err := c.client.MachineconfigurationV1().MachineConfigs().List(context.TODO(), metav1.ListOptions{})
			require.NoError(t, err)
			require.Len(t, mcList.Items, 1)
			require.Equal(t, featureKeyCustom, mcList.Items[0].Name)

			err = c.syncFeatureHandler(features.Name)
			require.NoError(t, err)

			mcList, err = c.client.MachineconfigurationV1().MachineConfigs().List(context.TODO(), metav1.ListOptions{})
			require.NoError(t, err)
			require.Len(t, mcList.Items, 2)
			for _, mc := range mcList.Items {
				require.NotEqual(t, featureKeyCustom, mc.Name)
			}
		})
	}
}
