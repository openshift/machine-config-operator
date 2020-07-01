package kubeletconfig

import (
	"reflect"
	"testing"

	igntypes "github.com/coreos/ignition/v2/config/v3_1/types"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/vincent-petithory/dataurl"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/helpers"
)

func TestFeatureGateDrift(t *testing.T) {
	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			f.ccLister = append(f.ccLister, cc)

			ctrl := f.newController()
			kubeletConfig, err := ctrl.generateOriginalKubeletConfig("master")
			if err != nil {
				t.Errorf("could not generate kubelet config from templates %v", err)
			}
			dataURL, _ := dataurl.DecodeString(*kubeletConfig.Contents.Source)
			originalKubeConfig, _ := decodeKubeletConfig(dataURL.Data)
			defaultFeatureGates, err := ctrl.generateFeatureMap(createNewDefaultFeatureGate())
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

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			kubeletConfigKey1, _ := getManagedKubeletConfigKey(mcp, nil)
			kubeletConfigKey2, _ := getManagedKubeletConfigKey(mcp2, nil)
			mcs := helpers.NewMachineConfig(kubeletConfigKey1, map[string]string{"node-role/master": ""}, "dummy://", []igntypes.File{{}})
			mcs2 := helpers.NewMachineConfig(kubeletConfigKey2, map[string]string{"node-role/worker": ""}, "dummy://", []igntypes.File{{}})
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
