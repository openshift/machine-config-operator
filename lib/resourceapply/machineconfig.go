package resourceapply

import (
	"context"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgalphav1 "github.com/openshift/api/machineconfiguration/v1alpha1"

	mcfgclientv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/typed/machineconfiguration/v1"
	mcfgclientalphav1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/typed/machineconfiguration/v1alpha1"

	mcoResourceMerge "github.com/openshift/machine-config-operator/lib/resourcemerge"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
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

	modified := false
	mcoResourceMerge.EnsureMachineConfig(&modified, existing, *required)
	if !modified {
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

	modified := false
	mcoResourceMerge.EnsureMachineConfigPool(&modified, existing, *required)
	if !modified {
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

	modified := false
	mcoResourceMerge.EnsureMachineConfigNode(&modified, existing, *required)
	if !modified {
		return existing, false, nil
	}

	actual, err := client.MachineConfigNodes().Update(context.TODO(), existing, metav1.UpdateOptions{})
	return actual, true, err
}

// ApplyControllerConfig applies the required machineconfig to the cluster.
func ApplyControllerConfig(client mcfgclientv1.ControllerConfigsGetter, required *mcfgv1.ControllerConfig) (*mcfgv1.ControllerConfig, bool, error) {
	klog.V(4).Infof("Getting existing ControllerConfig with name: %s", required.GetName())
	existing, err := client.ControllerConfigs().Get(context.TODO(), required.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		klog.Info("ControllerConfig not found, creating new one")
		actual, err := client.ControllerConfigs().Create(context.TODO(), required, metav1.CreateOptions{})
		if err != nil {
			klog.Errorf("Failed to create ControllerConfig: %v", err)
		}
		return actual, true, err
	}
	if err != nil {
		klog.Errorf("Error fetching ControllerConfig: %v", err)
		return nil, false, err
	}

	modified := false
	mcoResourceMerge.EnsureControllerConfig(&modified, existing, *required)
	if !modified {
		klog.V(4).Info("No updates required for the ControllerConfig")
		return existing, false, nil
	}

	klog.V(4).Info("Updating existing ControllerConfig")
	actual, err := client.ControllerConfigs().Update(context.TODO(), existing, metav1.UpdateOptions{})
	if err != nil {
		klog.Errorf("Failed to update ControllerConfig: %v", err)
	}
	return actual, true, err
}
