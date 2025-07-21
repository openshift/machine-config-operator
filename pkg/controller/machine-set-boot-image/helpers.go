package machineset

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	opv1 "github.com/openshift/api/operator/v1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

// TODO - unmarshal the providerspec into each ProviderSpec type until it succeeds,
// and then call the appropriate reconcile function. This is needed for multi platform
// support
func unmarshalToFindPlatform(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unknown platform type with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

// This function unmarshals the machineset's provider spec into
// a ProviderSpec object. Returns an error if providerSpec field is nil,
// or the unmarshal fails
func unmarshalProviderSpec(ms *machinev1beta1.MachineSet, providerSpec interface{}) error {
	if ms == nil {
		return fmt.Errorf("MachineSet object was nil")
	}
	if ms.Spec.Template.Spec.ProviderSpec.Value == nil {
		return fmt.Errorf("providerSpec field was empty")
	}
	if err := yaml.Unmarshal(ms.Spec.Template.Spec.ProviderSpec.Value.Raw, &providerSpec); err != nil {
		return fmt.Errorf("unmarshal into providerSpec failed %w", err)
	}
	return nil
}

// This function marshals the ProviderSpec object into a MachineSet object.
// Returns an error if ProviderSpec or MachineSet is nil, or if the marshal fails
func marshalProviderSpec(ms *machinev1beta1.MachineSet, providerSpec interface{}) error {
	if ms == nil {
		return fmt.Errorf("MachineSet object was nil")
	}
	if providerSpec == nil {
		return fmt.Errorf("ProviderSpec object was nil")
	}
	rawBytes, err := json.Marshal(providerSpec)
	if err != nil {
		return fmt.Errorf("marshal into machineset failed: %w", err)
	}
	ms.Spec.Template.Spec.ProviderSpec.Value = &kruntime.RawExtension{Raw: rawBytes}
	return nil
}

// This function unmarshals the golden stream configmap into a coreos
// stream object. Returns an error if the unmarshal fails.
func unmarshalStreamDataConfigMap(cm *corev1.ConfigMap, st interface{}) error {
	if err := json.Unmarshal([]byte(cm.Data[StreamConfigMapKey]), &st); err != nil {
		return fmt.Errorf("failed to parse CoreOS stream metadata: %w", err)
	}
	return nil
}

// This function checks if an array of machineManagers contains the target apigroup/resource and returns
// a bool(success/fail), a label selector to filter the target resource and an error, if any.
func getMachineResourceSelectorFromMachineManagers(machineManagers []opv1.MachineManager, apiGroup opv1.MachineManagerMachineSetsAPIGroupType, resource opv1.MachineManagerMachineSetsResourceType) (bool, labels.Selector, error) {
	// If no machine managers exist; exit the enqueue process without errors.
	if len(machineManagers) == 0 {
		klog.Infof("No machine managers were found, so no machine resources will be enqueued.")
		return false, labels.Nothing(), nil
	}
	for _, machineManager := range machineManagers {
		if machineManager.APIGroup == apiGroup && machineManager.Resource == resource {
			switch machineManager.Selection.Mode {
			case opv1.Partial:
				selector, err := metav1.LabelSelectorAsSelector(machineManager.Selection.Partial.MachineResourceSelector)
				return true, selector, err
			case opv1.All:
				return true, labels.Everything(), nil
			case opv1.None:
				return true, labels.Nothing(), nil
			}
		}
	}
	return false, labels.Nothing(), nil
}

// Upgrades the Ignition stub enclosed in referenced secret if required
func upgradeStubIgnitionIfRequired(secretName string, secretClient clientset.Interface) error {
	secret, err := secretClient.CoreV1().Secrets(ctrlcommon.MachineAPINamespace).Get(context.TODO(), secretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error grabbing user data secret referenced in machineset: %w", err)
	}
	userData := secret.Data[ctrlcommon.UserDataKey]
	var userDataIgn interface{}
	if err := json.Unmarshal(userData, &userDataIgn); err != nil {
		return fmt.Errorf("failed to unmarshal decoded user-data to json (secret %s): %wt", secret.Name, err)
	}
	versionPath := []string{ctrlcommon.IgnFieldIgnition, ctrlcommon.IgnFieldVersion}
	version, _, err := unstructured.NestedString(userDataIgn.(map[string]any), versionPath...)
	if err != nil {
		return fmt.Errorf("failed to find version field in ignition (user data secret %s): %w", secret.Name, err)
	}
	// If stub is not spec 3, attempt an upgrade. If an upgrade isn't possible, return an error.
	if !strings.HasPrefix(version, ctrlcommon.MinimumAcceptableStubIgnitionSpec) {
		klog.Infof("Out of date version=%s stub Ignition detected in %s, attempting upgrade", version, secret.Name)
		userDataIgnUpgraded, err := ctrlcommon.ParseAndConvertConfig(userData)
		if err != nil {
			return fmt.Errorf("converting ignition stub failed: %v", err)
		}
		klog.Infof("ignition stub upgrade to %s successful", userDataIgnUpgraded.Ignition.Version)
		// Annotate the secret if an Ignition upgrade took place
		metav1.SetMetaDataAnnotation(&secret.ObjectMeta, ctrlcommon.StubIgnitionVersionAnnotation, userDataIgnUpgraded.Ignition.Version)
		metav1.SetMetaDataAnnotation(&secret.ObjectMeta, ctrlcommon.StubIgnitionTimestampAnnotation, metav1.Now().Format(time.RFC3339))

		updatedIgnition, err := json.Marshal(userDataIgnUpgraded)
		if err != nil {
			return fmt.Errorf("failed to marshal updated ignition back to json (secret %s): %w", secret.Name, err)
		}
		secret.Data[ctrlcommon.UserDataKey] = updatedIgnition
		_, err = secretClient.CoreV1().Secrets(ctrlcommon.MachineAPINamespace).Update(context.TODO(), secret, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("could not update secret %s: %w", secret.Name, err)
		}

	}
	return nil
}
