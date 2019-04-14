package kubeletconfig

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
	"github.com/openshift/machine-config-operator/pkg/controller/common"
)

func TestFeaturesDefault(t *testing.T) {
	for _, platform := range []string{"aws", "none", "unrecognized"} {
		t.Run(platform, func(t *testing.T) {
			f := newFixture(t)

			cc := newControllerConfig(common.ControllerConfigName, platform)
			mcp := newMachineConfigPool("master", map[string]string{"kubeletType": "small-pods"}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "node-role", "master"), "v0")
			mcp2 := newMachineConfigPool("worker", map[string]string{"kubeletType": "large-pods"}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "node-role", "worker"), "v0")
			mcs := newMachineConfig(getManagedKubeletConfigKey(mcp), map[string]string{"node-role": "master"}, "dummy://", []ignv2_2types.File{{}})
			mcs2 := newMachineConfig(getManagedKubeletConfigKey(mcp2), map[string]string{"node-role": "worker"}, "dummy://", []ignv2_2types.File{{}})

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)

			features := createNewDefaultFeatureGate()
			f.featLister = append(f.featLister, features)

			f.expectGetMachineConfigAction(mcs)
			f.expectCreateMachineConfigAction(mcs)
			f.expectGetMachineConfigAction(mcs2)
			f.expectCreateMachineConfigAction(mcs2)

			f.runFeature(getKeyFromFeatureGate(features, t))
		})
	}
}
