package resourceapply

import (
	"context"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgclientv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/typed/machineconfiguration/v1"
	"github.com/openshift/library-go/pkg/operator/resource/resourcemerge"
	mcoResourceMerge "github.com/openshift/machine-config-operator/lib/resourcemerge"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ApplyMachineConfig applies the required machineconfig to the cluster.
func ApplyMachineConfig(client mcfgclientv1.MachineConfigsGetter, required *mcfgv1.MachineConfig) (*mcfgv1.MachineConfig, bool, error) {
	existing, err := client.MachineConfigs().Get(context.TODO(), required.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		actual, err := client.MachineConfigs().Create(context.TODO(), required, metav1.CreateOptions{})
		return actual, true, err
	}
	if err != nil {
		return nil, false, err
	}

	modified := resourcemerge.BoolPtr(false)
	mcoResourceMerge.EnsureMachineConfig(modified, existing, *required)
	if !*modified {
		return existing, false, nil
	}

	actual, err := client.MachineConfigs().Update(context.TODO(), existing, metav1.UpdateOptions{})
	return actual, true, err
}

// ApplyMachineConfigPool applies the required machineconfig to the cluster.
func ApplyMachineConfigPool(client mcfgclientv1.MachineConfigPoolsGetter, required *mcfgv1.MachineConfigPool) (*mcfgv1.MachineConfigPool, bool, error) {
	existing, err := client.MachineConfigPools().Get(context.TODO(), required.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		actual, err := client.MachineConfigPools().Create(context.TODO(), required, metav1.CreateOptions{})
		return actual, true, err
	}
	if err != nil {
		return nil, false, err
	}

	modified := resourcemerge.BoolPtr(false)
	mcoResourceMerge.EnsureMachineConfigPool(modified, existing, *required)
	if !*modified {
		return existing, false, nil
	}

	actual, err := client.MachineConfigPools().Update(context.TODO(), existing, metav1.UpdateOptions{})
	return actual, true, err
}

// ApplyMachineConfigPool applies the required machineconfig to the cluster.
func ApplyMachineState(client mcfgclientv1.MachineStatesGetter, required *mcfgv1.MachineState) (*mcfgv1.MachineState, bool, error) {
	existing, err := client.MachineStates().Get(context.TODO(), required.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		actual, err := client.MachineStates().Create(context.TODO(), required, metav1.CreateOptions{})
		return actual, true, err
	}
	if err != nil {
		return nil, false, err
	}

	modified := resourcemerge.BoolPtr(false)
	mcoResourceMerge.EnsureMachineState(modified, existing, *required)
	if !*modified {
		return existing, false, nil
	}

	actual, err := client.MachineStates().Update(context.TODO(), existing, metav1.UpdateOptions{})
	return actual, true, err
}

// ApplyControllerConfig applies the required machineconfig to the cluster.
func ApplyControllerConfig(client mcfgclientv1.ControllerConfigsGetter, required *mcfgv1.ControllerConfig) (*mcfgv1.ControllerConfig, bool, error) {
	existing, err := client.ControllerConfigs().Get(context.TODO(), required.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		actual, err := client.ControllerConfigs().Create(context.TODO(), required, metav1.CreateOptions{})
		return actual, true, err
	}
	if err != nil {
		return nil, false, err
	}

	modified := resourcemerge.BoolPtr(false)
	mcoResourceMerge.EnsureControllerConfig(modified, existing, *required)
	if !*modified {
		return existing, false, nil
	}

	actual, err := client.ControllerConfigs().Update(context.TODO(), existing, metav1.UpdateOptions{})
	return actual, true, err
}
