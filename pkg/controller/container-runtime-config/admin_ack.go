package containerruntimeconfig

import (
	"context"
	"fmt"
	"reflect"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

const (
	SigstoreKey = "ack-4.20-sigstore-in-4.21"
	SigstoreMsg = "This cluster has mirrors configured. 4.21 will require Sigstore signatures for quay.io/openshift-release-dev/ocp-release release verification. Please ensure that any registries configured as mirrors of quay.io/openshift-release-dev/ocp-release images contain the Sigstore signature images associated with any release images before updating to 4.21. See https://docs.redhat.com/en/documentation/openshift_container_platform/4.20/html/nodes/nodes-sigstore-using.html#nodes-sigstore-prepare-for-4.21_nodes-sigstore-using for more details."

	AdminAckGatesConfigMapName = "admin-gates"
)

// Syncs the admin-ack configmap in the openshift-config-managed namespace, and adds MCO specific keys if needed.
func (ctrl *Controller) syncAdminAckConfigMap(ctx context.Context, hasMirrorConfig bool) error {
	cm, err := ctrl.ocManagedConfigMapLister.ConfigMaps(ctrlcommon.OpenshiftConfigManagedNamespace).Get(AdminAckGatesConfigMapName)
	if err != nil {
		return fmt.Errorf("error fetching configmap %s during sync: %w", AdminAckGatesConfigMapName, err)
	}

	newCM := cm.DeepCopy()

	updateGuardKeyIfNeeded(hasMirrorConfig, SigstoreKey, SigstoreMsg, newCM)

	// Only send an API request if an update is needed
	if !reflect.DeepEqual(cm.Data, newCM.Data) {
		_, err = ctrl.kubeClient.CoreV1().ConfigMaps(ctrlcommon.OpenshiftConfigManagedNamespace).Update(ctx, newCM, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("error updating configmap %s during sync: %w", AdminAckGatesConfigMapName, err)
		}
		klog.Infof("%s configmap update successful", newCM.Name)
	}

	return nil
}

// updateGuardKeyIfNeeded adds a key with a message depending on:
// If guard is needed, the key exists, and the message diverges from the expected message => update the message
// If guard is needed and key exists, and the message matches the expected message => nothing to do
// If guard is needed and key doesn't exist => create it
// If guard is not needed and key exists => delete it
// If guard is not needed and key doesn't exist => nothing to do
func updateGuardKeyIfNeeded(guardNeeded bool, key, msg string, cm *corev1.ConfigMap) {
	keyExists := cm.Data != nil && cm.Data[key] != ""
	if guardNeeded {
		if keyExists && msg != cm.Data[key] {
			klog.Infof("Updating key %s in configmap %s from %q to %q", key, cm.Name, cm.Data[key], msg)
			cm.Data[key] = msg
		} else if !keyExists {
			if cm.Data == nil {
				cm.Data = map[string]string{}
			}
			cm.Data[key] = msg
			klog.Infof("Adding key %s to configmap %s", key, cm.Name)
		}
	} else if keyExists {
		delete(cm.Data, key)
		klog.Infof("Deleting key %s from configmap %s", key, cm.Name)
	}
}
