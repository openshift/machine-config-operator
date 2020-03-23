package kubeletconfig

import (
	"reflect"
	"testing"

	ignTypes "github.com/coreos/ignition/v2/config/v3_1_experimental/types"
	"github.com/vincent-petithory/dataurl"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/helpers"
)

func TestFeatureGateDrift(t *testing.T) {
	for _, platform := range []string{"aws", "none", "unrecognized"} {
		t.Run(platform, func(t *testing.T) {
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
	for _, platform := range []string{"aws", "none", "unrecognized"} {
		t.Run(platform, func(t *testing.T) {
			f := newFixture(t)

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			mcs := helpers.NewMachineConfigV3(getManagedKubeletConfigKey(mcp), map[string]string{"node-role/master": ""}, "dummy://", []ignTypes.File{{}})
			mcs2 := helpers.NewMachineConfigV3(getManagedKubeletConfigKey(mcp2), map[string]string{"node-role/worker": ""}, "dummy://", []ignTypes.File{{}})

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)

			features := createNewDefaultFeatureGate()
			f.featLister = append(f.featLister, features)

			f.expectGetMachineConfigAction(mcs)
			f.expectGetMachineConfigAction(mcs2)

			f.runFeature(getKeyFromFeatureGate(features, t))
		})
	}
}
