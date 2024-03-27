package kubeletconfig

import (
	"context"
	"reflect"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	configv1 "github.com/openshift/api/config/v1"
	osev1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"
)

func TestFeatureGateDrift(t *testing.T) {
	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			f.ccLister = append(f.ccLister, cc)

			fgAccess := featuregates.NewHardcodedFeatureGateAccess([]osev1.FeatureGateName{}, []osev1.FeatureGateName{})
			ctrl := f.newController(fgAccess)

			kubeletConfig, err := generateOriginalKubeletConfigIgn(cc, ctrl.templatesDir, "master")
			if err != nil {
				t.Errorf("could not generate kubelet config from templates %v", err)
			}
			contents, err := ctrlcommon.DecodeIgnitionFileContents(kubeletConfig.Contents.Source, kubeletConfig.Contents.Compression)
			require.NoError(t, err)
			originalKubeConfig, err := decodeKubeletConfig(contents)
			require.NoError(t, err)

			defaultFeatureGates, err := generateFeatureMap(fgAccess)
			if err != nil {
				t.Errorf("could not generate defaultFeatureGates: %v", err)
			}
			if !reflect.DeepEqual(originalKubeConfig.FeatureGates, *defaultFeatureGates) {
				var found = map[string]bool{}
				for featureGate := range originalKubeConfig.FeatureGates {
					for apiGate := range *defaultFeatureGates {
						if featureGate == apiGate {
							found[apiGate] = true
						}
					}
				}
				for featureGate := range originalKubeConfig.FeatureGates {
					if _, ok := found[featureGate]; !ok {
						t.Logf("%s is not present in api", featureGate)
					}
				}
				for featureGate := range *defaultFeatureGates {
					if _, ok := found[featureGate]; !ok {
						t.Logf("%s is not present in template", featureGate)
					}
				}
				t.Errorf("template FeatureGates do not match openshift/api FeatureGates: (tmpl=[%v], api=[%v]", originalKubeConfig.FeatureGates, defaultFeatureGates)
			}
		})
	}
}

func TestFeaturesDefault(t *testing.T) {
	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			fgAccess := featuregates.NewHardcodedFeatureGateAccess([]osev1.FeatureGateName{}, []osev1.FeatureGateName{})
			f.newController(fgAccess)

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
			f.expectGetMachineConfigAction(mcs2)
			f.expectGetMachineConfigAction(mcs2Deprecated)
			f.expectGetMachineConfigAction(mcs2)

			f.runFeature(getKeyFromFeatureGate(features, t), fgAccess)
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

			// Ensure the FG Access matches the feature gate.
			fgAccess := featuregates.NewHardcodedFeatureGateAccess(features.Spec.FeatureGateSelection.CustomNoUpgrade.Enabled, features.Spec.FeatureGateSelection.CustomNoUpgrade.Disabled)

			f := newFixture(t)
			f.newController(fgAccess)

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
			f.runFeature(getKeyFromFeatureGate(features, t), fgAccess)
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

			fgAccess := featuregates.NewHardcodedFeatureGateAccess([]osev1.FeatureGateName{}, []osev1.FeatureGateName{})

			mcs, err := RunFeatureGateBootstrap("../../../templates", fgAccess, nil, cc, mcps)
			if err != nil {
				t.Errorf("could not run feature gate bootstrap: %v", err)
			}
			if len(mcs) > 0 {
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

			fgAccess := featuregates.NewHardcodedFeatureGateAccess([]osev1.FeatureGateName{"CSIMigration"}, nil)

			mcs, err := RunFeatureGateBootstrap("../../../templates", fgAccess, nil, cc, mcps)
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

				originalKubeConfig, err := decodeKubeletConfig(conf)
				require.NoError(t, err)

				fgAccess := featuregates.NewHardcodedFeatureGateAccess([]osev1.FeatureGateName{}, []osev1.FeatureGateName{})
				defaultFeatureGates, err := generateFeatureMap(fgAccess)
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

			// Ensure the FG Access matches the feature gate.
			fgAccess := featuregates.NewHardcodedFeatureGateAccess(features.Spec.FeatureGateSelection.CustomNoUpgrade.Enabled, features.Spec.FeatureGateSelection.CustomNoUpgrade.Disabled)

			f := newFixture(t)
			f.newController(fgAccess)

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
			c := f.newController(fgAccess)

			// Ensure the FG Access matches the feature gate.
			c.featureGateAccess = featuregates.NewHardcodedFeatureGateAccess(features.Spec.FeatureGateSelection.CustomNoUpgrade.Enabled, features.Spec.FeatureGateSelection.CustomNoUpgrade.Disabled)

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
