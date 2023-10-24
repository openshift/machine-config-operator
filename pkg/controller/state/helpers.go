package state

import (
	"context"
	"fmt"

	mcfgalphav1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	mcfgalphav1listers "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1alpha1"
	apiextclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	clientset "k8s.io/client-go/kubernetes"
)

func StateControllerPod(client clientset.Interface) (*v1.Pod, error) {
	listOptions := metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{"k8s-app": "machine-state-controller"}).String(),
	}

	podList, err := client.CoreV1().Pods("openshift-machine-config-operator").List(context.TODO(), listOptions)
	if err != nil {
		return nil, err
	}
	if len(podList.Items) == 1 {
		return &podList.Items[0], nil
	} else {
		return nil, fmt.Errorf("not enough or too many pods. Pod Amount: %d", len(podList.Items))
	}
}

func ConvertStateControllerToPoolType(stateType mcfgalphav1.StateProgress) mcfgv1.MachineConfigPoolConditionType {
	switch stateType {
	case mcfgalphav1.MachineConfigPoolUpdatePreparing:
		return mcfgv1.MachineConfigPoolUpdating
	case mcfgalphav1.MachineConfigPoolUpdateInProgress:
		return mcfgv1.MachineConfigPoolUpdating
	case mcfgalphav1.MachineConfigPoolUpdatePostAction:
		return mcfgv1.MachineConfigPoolUpdating
	case mcfgalphav1.MachineConfigPoolUpdateCompleting:
		return mcfgv1.MachineConfigPoolUpdating
	case mcfgalphav1.MachineConfigPoolUpdateComplete:
		return mcfgv1.MachineConfigPoolUpdated
	case mcfgalphav1.MachineConfigNodeErrored:
		return mcfgv1.MachineConfigPoolDegraded
	}
	return mcfgv1.MachineConfigPoolUpdated // ?
}

func IsUpgradingProgressionTrue(which mcfgalphav1.StateProgress, pool mcfgv1.MachineConfigPool, msLister mcfgalphav1listers.MachineConfigNodeLister, apiCli apiextclientset.Interface) (bool, error) {
	if _, err := apiCli.ApiextensionsV1().CustomResourceDefinitions().Get(context.TODO(), "machinestates.machineconfiguration.openshift.io", metav1.GetOptions{}); err != nil {
		for _, condition := range pool.Status.Conditions {
			if condition.Type == ConvertStateControllerToPoolType(which) {
				return condition.Status == corev1.ConditionTrue, nil
			}
		}
		return false, nil
	}
	ms, err := GetMachineConfigNodeForPool(pool, msLister)
	if err != nil || ms == nil {
		// if for some reason the machinestate has been deleted or DNE, fallback to old method
		for _, condition := range pool.Status.Conditions {
			if condition.Type == ConvertStateControllerToPoolType(which) {
				return condition.Status == corev1.ConditionTrue, nil
			}
		}
	}
	for _, stateOnNode := range ms.Status.Conditions {
		if mcfgalphav1.StateProgress(stateOnNode.Type) == which && stateOnNode.Status == metav1.ConditionTrue {
			klog.Infof("Upgrading progression true")
			return true, nil
		}
	}
	klog.Infof("Upgrading progression false")
	return false, nil
}

func GetMachineConfigNodeForPool(pool mcfgv1.MachineConfigPool, msLister mcfgalphav1listers.MachineConfigNodeLister) (*mcfgalphav1.MachineConfigNode, error) {
	return msLister.Get(fmt.Sprintf("upgrade-%s", pool.Name))
}
