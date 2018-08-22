package resourceapply

import (
	"github.com/openshift/machine-config-operator/lib/resourcemerge"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	mcfgclientv1 "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/typed/machineconfiguration.openshift.io/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ApplyMachineConfig applies the required machineconfig to the cluster.
func ApplyMachineConfig(client mcfgclientv1.MachineConfigsGetter, required *mcfgv1.MachineConfig) (*mcfgv1.MachineConfig, bool, error) {
	existing, err := client.MachineConfigs().Get(required.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		actual, err := client.MachineConfigs().Create(required)
		return actual, true, err
	}
	if err != nil {
		return nil, false, err
	}

	modified := resourcemerge.BoolPtr(false)
	resourcemerge.EnsureMachineConfig(modified, existing, *required)
	if !*modified {
		return existing, false, nil
	}

	actual, err := client.MachineConfigs().Update(existing)
	return actual, true, err
}

// ApplyMachineConfigPool applies the required machineconfig to the cluster.
func ApplyMachineConfigPool(client mcfgclientv1.MachineConfigPoolsGetter, required *mcfgv1.MachineConfigPool) (*mcfgv1.MachineConfigPool, bool, error) {
	existing, err := client.MachineConfigPools().Get(required.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		actual, err := client.MachineConfigPools().Create(required)
		return actual, true, err
	}
	if err != nil {
		return nil, false, err
	}

	modified := resourcemerge.BoolPtr(false)
	resourcemerge.EnsureMachineConfigPool(modified, existing, *required)
	if !*modified {
		return existing, false, nil
	}

	actual, err := client.MachineConfigPools().Update(existing)
	return actual, true, err
}

// ApplyControllerConfig applies the required machineconfig to the cluster.
func ApplyControllerConfig(client mcfgclientv1.ControllerConfigsGetter, required *mcfgv1.ControllerConfig) (*mcfgv1.ControllerConfig, bool, error) {
	existing, err := client.ControllerConfigs(required.GetNamespace()).Get(required.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		actual, err := client.ControllerConfigs(required.GetNamespace()).Create(required)
		return actual, true, err
	}
	if err != nil {
		return nil, false, err
	}

	modified := resourcemerge.BoolPtr(false)
	resourcemerge.EnsureControllerConfig(modified, existing, *required)
	if !*modified {
		return existing, false, nil
	}

	actual, err := client.ControllerConfigs(required.GetNamespace()).Update(existing)
	return actual, true, err
}
