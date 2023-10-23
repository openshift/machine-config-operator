package resourceapply

import (
	"context"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgalphav1 "github.com/openshift/api/machineconfiguration/v1alpha1"

	opv1 "github.com/openshift/api/operator/v1"
	operatorclientv1 "github.com/openshift/client-go/operator/clientset/versioned/typed/operator/v1"

	mcfgclientv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/typed/machineconfiguration/v1"
	mcfgclientalphav1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/typed/machineconfiguration/v1alpha1"

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

// ApplyMachineConfigNode applies the required machineconfignode to the cluster.
func ApplyMachineConfigNode(client mcfgclientalphav1.MachineConfigNodesGetter, required *mcfgalphav1.MachineConfigNode) (*mcfgalphav1.MachineConfigNode, bool, error) {
	existing, err := client.MachineConfigNodes().Get(context.TODO(), required.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		actual, err := client.MachineConfigNodes().Create(context.TODO(), required, metav1.CreateOptions{})
		return actual, true, err
	}
	if err != nil {
		return nil, false, err
	}

	modified := resourcemerge.BoolPtr(false)
	mcoResourceMerge.EnsureMachineConfigNode(modified, existing, *required)
	if !*modified {
		return existing, false, nil
	}

	actual, err := client.MachineConfigNodes().Update(context.TODO(), existing, metav1.UpdateOptions{})
	return actual, true, err
}

func ApplyMachineConfguration(client operatorclientv1.MachineConfigurationsGetter, required *opv1.MachineConfiguration) (*opv1.MachineConfiguration, bool, error) {
	existing, err := client.MachineConfigurations().Get(context.TODO(), required.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		actual, err := client.MachineConfigurations().Create(context.TODO(), required, metav1.CreateOptions{})
		return actual, true, err
	}
	if err != nil {
		return nil, false, err
	}

	modified := resourcemerge.BoolPtr(false)
	mcoResourceMerge.EnsureMachineConfiguration(modified, existing, *required)
	if !*modified {
		return existing, false, nil
	}

	actual, err := client.MachineConfigurations().Update(context.TODO(), existing, metav1.UpdateOptions{})
	return actual, true, err
}

func ApplyMachineConfguration(client operatorclientv1.MachineConfigurationsGetter, required *opv1.MachineConfiguration) (*opv1.MachineConfiguration, bool, error) {
	existing, err := client.MachineConfigurations().Get(context.TODO(), required.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		actual, err := client.MachineConfigurations().Create(context.TODO(), required, metav1.CreateOptions{})
		return actual, true, err
	}
	if err != nil {
		return nil, false, err
	}

	modified := resourcemerge.BoolPtr(false)
	mcoResourceMerge.EnsureMachineConfiguration(modified, existing, *required)
	if !*modified {
		return existing, false, nil
	}

	actual, err := client.MachineConfigurations().Update(context.TODO(), existing, metav1.UpdateOptions{})
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
