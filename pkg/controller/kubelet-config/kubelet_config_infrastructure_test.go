package kubeletconfig

import (
	"reflect"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	osev1 "github.com/openshift/api/config/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/helpers"
)

func TestInfraConfigDefault(t *testing.T) {
	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			f.newController()

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)

			mcs, err := cpuPartitionMC("worker", cpuPartitionIgn())
			if err != nil {
				t.Errorf("could not create CPU Partitioning mc: %v", err)
			}

			infraConfig := createNewDefaultInfraConfig()
			f.infraLister = append(f.infraLister, infraConfig)

			f.expectGetMachineConfigAction(mcs)
			f.expectCreateMachineConfigAction(mcs)

			f.runInfraController(getKeyFromConfigInfra(infraConfig, t), false)
		})
	}
}

func TestBootstrapInfraConfigDefault(t *testing.T) {
	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {

			mcp := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			mcps := []*mcfgv1.MachineConfigPool{mcp}

			mcs, err := RunInfraConfigBootstrap(createNewDefaultInfraConfig(), mcps)
			if err != nil {
				t.Errorf("could not run node config bootstrap: %v", err)
			}
			if len(mcs) == 0 {
				t.Errorf("expected a machine config generated with the default node config, got 0 machine configs")
			}

			expected, err := cpuPartitionMC("worker", cpuPartitionIgn())
			if err != nil {
				t.Errorf("could not run node config bootstrap: %v", err)
			}
			mc := *mcs[0]
			if !reflect.DeepEqual(mc.Spec.Config.Raw, expected.Spec.Config.Raw) {
				t.Errorf("expected raw value of (%s), got: (%s)", expected.Spec.Config.Raw, mcs[0].Spec.Config.Raw)
			}
		})
	}
}

func TestBootstrapNoInfraConfig(t *testing.T) {
	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {

			mcp := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			mcps := []*mcfgv1.MachineConfigPool{mcp}

			mcs, err := RunInfraConfigBootstrap(nil, mcps)
			if err == nil {
				t.Errorf("expected an error while generating the kubelet config with no node config")
			}
			if len(mcs) > 0 {
				t.Errorf("expected no machine configs with no node config but generated %v", len(mcs))
			}
		})
	}
}

func createNewDefaultInfraConfig() *osev1.Infrastructure {
	return &osev1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{
			Name: ctrlcommon.ClusterInfrastructureInstanceName,
		},
		Status: osev1.InfrastructureStatus{
			CPUPartitioning: osev1.CPUPartitioningAllNodes,
		},
	}
}
