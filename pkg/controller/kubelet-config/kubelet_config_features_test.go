package kubeletconfig

import (
	"reflect"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	configv1 "github.com/openshift/api/config/v1"
	osev1 "github.com/openshift/api/config/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
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

			ctrl := f.newController()
			kubeletConfig, err := generateOriginalKubeletConfigIgn(cc, ctrl.templatesDir, "master", nil)
			if err != nil {
				t.Errorf("could not generate kubelet config from templates %v", err)
			}
			contents, err := ctrlcommon.DecodeIgnitionFileContents(kubeletConfig.Contents.Source, kubeletConfig.Contents.Compression)
			require.NoError(t, err)
			originalKubeConfig, err := decodeKubeletConfig(contents)
			require.NoError(t, err)
			defaultFeatureGates, err := generateFeatureMap(createNewDefaultFeatureGate())
			if err != nil {
				t.Errorf("could not generate defaultFeatureGates: %v", err)
			}
			if !reflect.DeepEqual(originalKubeConfig.FeatureGates, *defaultFeatureGates) {
				t.Errorf("template FeatureGates do not match openshift/api FeatureGates: (tmpl=[%v], api=[%v]", originalKubeConfig.FeatureGates, defaultFeatureGates)
			}
		})
	}
}

func TestFeaturesDefault(t *testing.T) {
	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			f.newController()

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

			f.runFeature(getKeyFromFeatureGate(features, t))
		})
	}
}

func TestFeaturesCustomNoUpgrade(t *testing.T) {
	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			f.newController()

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

			features := &osev1.FeatureGate{
				ObjectMeta: metav1.ObjectMeta{
					Name: ctrlcommon.ClusterFeatureInstanceName,
				},
				Spec: osev1.FeatureGateSpec{
					FeatureGateSelection: osev1.FeatureGateSelection{
						FeatureSet: osev1.CustomNoUpgrade,
						CustomNoUpgrade: &osev1.CustomFeatureGates{
							Enabled: []string{"CSIMigration"},
						},
					},
				},
			}

			f.featLister = append(f.featLister, features)

			f.expectGetMachineConfigAction(mcs)
			f.expectGetMachineConfigAction(mcsDeprecated)
			f.expectGetMachineConfigAction(mcs)
			f.expectCreateMachineConfigAction(mcs)
			f.expectGetMachineConfigAction(mcs2)
			f.expectGetMachineConfigAction(mcs2Deprecated)
			f.expectGetMachineConfigAction(mcs2)
			f.expectCreateMachineConfigAction(mcs2)
			f.runFeature(getKeyFromFeatureGate(features, t))
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

			features := createNewDefaultFeatureGate()

			mcs, err := RunFeatureGateBootstrap("../../../templates", features, cc, mcps)
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

			features := &osev1.FeatureGate{
				ObjectMeta: metav1.ObjectMeta{
					Name: ctrlcommon.ClusterFeatureInstanceName,
				},
				Spec: osev1.FeatureGateSpec{
					FeatureGateSelection: osev1.FeatureGateSelection{
						FeatureSet: osev1.CustomNoUpgrade,
						CustomNoUpgrade: &osev1.CustomFeatureGates{
							Enabled: []string{"CSIMigration"},
						},
					},
				},
			}

			mcs, err := RunFeatureGateBootstrap("../../../templates", features, cc, mcps)
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
				defaultFeatureGates, err := generateFeatureMap(createNewDefaultFeatureGate())
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
