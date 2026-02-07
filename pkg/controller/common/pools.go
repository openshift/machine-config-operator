package common

import (
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	labelMCOBuiltIn = "machineconfiguration.openshift.io/mco-built-in"
	labelPoolPrefix = "pools.operator.machineconfiguration.openshift.io"
)

func GenerateManagedPools(infra *configv1.Infrastructure, osImageStreamName string) map[string]mcfgv1.MachineConfigPool {
	var osImageStream mcfgv1.OSImageStreamReference
	if osImageStreamName != "" {
		osImageStream = mcfgv1.OSImageStreamReference{
			Name: osImageStreamName,
		}
	}

	mcps := map[string]mcfgv1.MachineConfigPool{
		MachineConfigPoolMaster: createMachineConfigPool(MachineConfigPoolMaster, true, osImageStream),
		MachineConfigPoolWorker: createMachineConfigPool(MachineConfigPoolWorker, false, osImageStream),
	}

	if infra != nil && infra.Status.ControlPlaneTopology == configv1.HighlyAvailableArbiterMode {
		mcps[MachineConfigPoolArbiter] = createMachineConfigPool(MachineConfigPoolArbiter, true, osImageStream)
	}

	return mcps
}

// createMachineConfigPool creates a MachineConfigPool with the given name and configuration.
// requiredForUpgrade indicates whether the pool is required for cluster upgrades (master and arbiter pools).
func createMachineConfigPool(poolName string, requiredForUpgrade bool, osImageStream mcfgv1.OSImageStreamReference) mcfgv1.MachineConfigPool {
	labels := map[string]string{
		labelMCOBuiltIn: "",
		fmt.Sprintf("%s/%s", labelPoolPrefix, poolName): "",
	}
	if requiredForUpgrade {
		labels[MachineConfigPoolRequiredForUpgradeLabel] = ""
	}

	return mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:   poolName,
			Labels: labels,
		},
		Spec: mcfgv1.MachineConfigPoolSpec{
			MachineConfigSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					MachineConfigRoleLabel: poolName,
				},
			},
			NodeSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					fmt.Sprintf("node-role.kubernetes.io/%s", poolName): "",
				},
			},
			OSImageStream: osImageStream,
		},
	}
}
