package operator

import (
	"context"
	"fmt"
	"reflect"

	configv1 "github.com/openshift/api/config/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
)

const (
	BootImageAWSGCPKey = "ack-4.18-boot-image-opt-out-in-4.19"
	BootImageAWSGCPMsg = "GCP or AWS platform with no boot image configuration detected. OCP will automatically opt-in all GCP and AWS clusters that currently do not a boot image configuration in 4.19. Please add a configuration to disable boot image updates if this is not desired. See [insert-doc-link] "

	AArch64BootImageKey = "ack-4.18-aarch64-bootloader-4.19"
	AArch64BootImageMsg = "aarch64 nodes detected. Please ensure boot image are updated by following the KCS:(insertlink) prior to upgrading to 4.19."

	AdminAckGatesConfigMapName = "admin-gates"
)

// Syncs the admin-ack configmap in the openshift-config-managed namespace, and adds MCO specific keys if needed.
func (optr *Operator) syncAdminAckConfigMap() error {
	cm, err := optr.ocManagedConfigMapLister.ConfigMaps(ctrlcommon.OpenshiftConfigManagedNamespace).Get(AdminAckGatesConfigMapName)
	if err != nil {
		return fmt.Errorf("error fetching configmap %s during sync: %w", AdminAckGatesConfigMapName, err)
	}

	newCM := cm.DeepCopy()

	// Evaluate AWS/GCP boot image guards
	_, bootImageAWSGCPKeyExists := newCM.Data[BootImageAWSGCPKey]
	bootImageAWSGCPAdminGuardNeeded, err := optr.checkAWSGCPBootImageGuard()
	if err != nil {
		return fmt.Errorf("error determining aws/gcp boot image guard during configmap %s sync: %w", AdminAckGatesConfigMapName, err)
	}
	updateGuardKeyIfNeeded(bootImageAWSGCPAdminGuardNeeded, bootImageAWSGCPKeyExists, BootImageAWSGCPKey, BootImageAWSGCPMsg, newCM)

	// Evaluate aarch64 guards
	_, aarch64BootImageKeyExists := newCM.Data[AArch64BootImageKey]
	aarch64BootImageAdminGuardNeeded, err := optr.checkAArch64BootImageGuard()
	if err != nil {
		return fmt.Errorf("error determining aarch64 boot image guard during configmap %s sync: %w", AdminAckGatesConfigMapName, err)
	}
	updateGuardKeyIfNeeded(aarch64BootImageAdminGuardNeeded, aarch64BootImageKeyExists, AArch64BootImageKey, AArch64BootImageMsg, newCM)

	// Only send an API request if an update is needed
	if !reflect.DeepEqual(cm.Data, newCM.Data) {
		_, err = optr.kubeClient.CoreV1().ConfigMaps(ctrlcommon.OpenshiftConfigManagedNamespace).Update(context.TODO(), newCM, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("error updating configmap %s during sync: %w", AdminAckGatesConfigMapName, err)
		}
		klog.Infof("%s configmap update successful", newCM.Name)
	}

	return nil
}

// checkAWSGCPBootImageGuard checks if AWS & GCP Boot Image admin guard is needed
func (optr *Operator) checkAWSGCPBootImageGuard() (bool, error) {

	// First, check if the cluster is on AWS or GCP platform.
	infra, err := optr.infraLister.Get("cluster")
	if err != nil {
		klog.Errorf("Could not get infra: %v", err)
		return false, err
	}
	platforms := sets.New(configv1.GCPPlatformType, configv1.AWSPlatformType)
	if !platforms.Has(infra.Status.PlatformStatus.Type) {
		return false, nil
	}

	// Next, check if there is any boot image configuration.
	mcop, err := optr.mcopLister.Get(ctrlcommon.MCOOperatorKnobsObjectName)
	if err != nil {
		klog.Errorf("Could not get MachineConfiguration object: %v", err)
		return false, err
	}

	// If no machine managers exist, no configuration has been defined
	return mcop.Spec.ManagedBootImages.MachineManagers == nil, nil
}

// checkAArch64BootImageGuard checks if aarch64 Boot Image admin guard is needed
func (optr *Operator) checkAArch64BootImageGuard() (bool, error) {

	// Grab all current nodes
	nodes, err := optr.nodeLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("Could not get nodes: %v", err)
		return false, err
	}

	// Check if any node is of the arm64 type (translates to aarch64 in RPM world)
	for _, node := range nodes {
		if node.Status.NodeInfo.Architecture == "arm64" {
			return true, nil
		}
	}

	return false, nil
}

// updateGuardKeyIfNeeded adds a key with a message depending on:
// If guard is needed and key doesn't exist => create it
// If guard is needed and key exists => nothing to do
// If guard is not needed and key exists => delete it
// If guard is not needed and key doesn't exist => nothing to do
func updateGuardKeyIfNeeded(guardNeeded, keyExists bool, key, msg string, cm *corev1.ConfigMap) {
	if guardNeeded && !keyExists {
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[key] = msg
		klog.Infof("Adding key %s to configmap %s", key, cm.Name)
	} else if !guardNeeded && keyExists {
		delete(cm.Data, key)
		klog.Infof("Deleting key %s from configmap %s", key, cm.Name)
	}
}
