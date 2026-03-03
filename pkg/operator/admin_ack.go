package operator

import (
	"context"
	"fmt"
	"reflect"

	configv1 "github.com/openshift/api/config/v1"
	opv1 "github.com/openshift/api/operator/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

const (
	BootImageAzureVSphereKey = "ack-4.21-boot-image-opt-out-in-4.22"
	BootImageAzureVSphereMsg = "This cluster is Azure or vSphere but lacks a boot image configuration. OCP will automatically opt this cluster into boot image management in 4.22. Please add a configuration to disable boot image updates if this is not desired. See https://docs.redhat.com/en/documentation/openshift_container_platform/4.21/html/machine_configuration/mco-update-boot-images#mco-update-boot-images-disable_machine-configs-configure for more details."

	AdminAckGatesConfigMapName = "admin-gates"
)

// syncAdminAckConfigMap manages the admin-gates ConfigMap in the openshift-config-managed namespace.
// It adds or removes guard keys for Azure and vSphere boot image management based on whether those
// platforms require admin acknowledgment before upgrade.
func (optr *Operator) syncAdminAckConfigMap() error {
	// The admin-gates ConfigMap is owned and created by the CVO; return an error if it cannot be found.
	adminGatesCM, err := optr.clusterCmLister.ConfigMaps(ctrlcommon.OpenshiftConfigManagedNamespace).Get(AdminAckGatesConfigMapName)
	if err != nil {
		return fmt.Errorf("error getting %s/%s ConfigMap: %w", ctrlcommon.OpenshiftConfigManagedNamespace, AdminAckGatesConfigMapName, err)
	}

	newCM := adminGatesCM.DeepCopy()

	azureVsphereGuardNeeded, err := optr.checkAzureVSphereBootImageGuard()
	if err != nil {
		return fmt.Errorf("error checking Azure/vSphere boot image guard: %w", err)
	}

	_, bootImageAzureVSphereKeyExists := newCM.Data[BootImageAzureVSphereKey]

	updateGuardKeyIfNeeded(azureVsphereGuardNeeded, bootImageAzureVSphereKeyExists, newCM, BootImageAzureVSphereKey, BootImageAzureVSphereMsg)

	if reflect.DeepEqual(adminGatesCM.Data, newCM.Data) {
		return nil
	}

	klog.Infof("Updating %s/%s ConfigMap", ctrlcommon.OpenshiftConfigManagedNamespace, AdminAckGatesConfigMapName)
	_, err = optr.kubeClient.CoreV1().ConfigMaps(ctrlcommon.OpenshiftConfigManagedNamespace).Update(context.TODO(), newCM, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("error updating %s/%s ConfigMap: %w", ctrlcommon.OpenshiftConfigManagedNamespace, AdminAckGatesConfigMapName, err)
	}

	return nil
}

// checkAzureVSphereBootImageGuard returns true if the cluster is on Azure (excluding AzureStackCloud)
// or vSphere and does not have an explicit boot image configuration in the MachineConfiguration object.
func (optr *Operator) checkAzureVSphereBootImageGuard() (bool, error) {
	infra, err := optr.infraLister.Get("cluster")
	if err != nil {
		return false, fmt.Errorf("error getting infrastructure: %w", err)
	}

	if infra.Status.PlatformStatus == nil {
		return false, nil
	}

	switch infra.Status.PlatformStatus.Type {
	case configv1.AzurePlatformType:
		// AzureStackCloud is not supported for boot image updates
		if infra.Status.PlatformStatus.Azure != nil && infra.Status.PlatformStatus.Azure.CloudName == configv1.AzureStackCloud {
			return false, nil
		}
	case configv1.VSpherePlatformType:
		// vSphere is supported, fall through to the boot image config check
	default:
		return false, nil
	}

	return optr.isMissingBootImageConfig()
}

// isMissingBootImageConfig returns true if the MachineConfiguration cluster object has no
// explicit MachineSet boot image manager defined in its spec.
func (optr *Operator) isMissingBootImageConfig() (bool, error) {
	mcop, err := optr.mcopLister.Get(ctrlcommon.MCOOperatorKnobsObjectName)
	if err != nil {
		klog.Errorf("Could not get MachineConfiguration object: %v", err)
		return false, fmt.Errorf("error getting MachineConfiguration: %w", err)
	}

	// Guard is needed when no MachineManagers have been defined at all (nil), or when a list exists
	// but contains no explicit entry for MachineSets. Both cases mean the cluster has no deliberate
	// stance on MachineSet boot image management and would be silently auto-opted-in on upgrade.
	return mcop.Spec.ManagedBootImages.MachineManagers == nil ||
		!apihelpers.HasMAPIMachineSetManager(mcop.Spec.ManagedBootImages.MachineManagers, opv1.MachineSets), nil
}

// updateGuardKeyIfNeeded adds a key to the ConfigMap when the guard is needed but the key is absent,
// and removes it when the guard is no longer needed but the key still exists.
func updateGuardKeyIfNeeded(guardNeeded, keyExists bool, cm *corev1.ConfigMap, key, message string) {
	switch {
	case guardNeeded && !keyExists:
		klog.Infof("Adding admin-ack gate %q to %s/%s", key, cm.Namespace, cm.Name)
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[key] = message
	case !guardNeeded && keyExists:
		klog.Infof("Removing admin-ack gate %q from %s/%s", key, cm.Namespace, cm.Name)
		delete(cm.Data, key)
	}
}
