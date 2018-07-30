package controller

import (
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	mcfgclientv1 "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/typed/machineconfiguration.openshift.io/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ApplyMachineConfig applies the required machineconfig to the cluster.
func ApplyMachineConfig(client mcfgclientv1.MachineConfigsGetter, required *mcfgv1.MachineConfig) (*mcfgv1.MachineConfig, bool, error) {
	existing, err := client.MachineConfigs(required.GetNamespace()).Get(required.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		actual, err := client.MachineConfigs(required.GetNamespace()).Create(required)
		return actual, true, err
	}
	if err != nil {
		return nil, false, err
	}

	modified := boolPtr(false)
	mergeMap(modified, &existing.ObjectMeta.Annotations, required.GetAnnotations())
	mergeMap(modified, &existing.ObjectMeta.Labels, required.GetLabels())
	mergeOwnerRefs(modified, &existing.ObjectMeta.OwnerReferences, required.GetOwnerReferences())

	osImageSame := equality.Semantic.DeepEqual(existing.Spec.OSImageURL, required.Spec.OSImageURL)
	configSame := equality.Semantic.DeepEqual(existing.Spec.Config, required.Spec.Config)
	if configSame && osImageSame && !*modified {
		return existing, false, nil
	}

	existing.Spec = required.Spec

	actual, err := client.MachineConfigs(required.GetNamespace()).Update(existing)
	return actual, true, err
}

func mergeMap(modified *bool, existing *map[string]string, required map[string]string) {
	if *existing == nil {
		*existing = map[string]string{}
	}
	for k, v := range required {
		if existingV, ok := (*existing)[k]; !ok || v != existingV {
			*modified = true
			(*existing)[k] = v
		}
	}
}

func mergeOwnerRefs(modified *bool, existing *[]metav1.OwnerReference, required []metav1.OwnerReference) {
	for ridx := range required {
		found := false
		for eidx := range *existing {
			if required[ridx].UID == (*existing)[eidx].UID {
				found = true
				if !equality.Semantic.DeepEqual((*existing)[eidx], required[ridx]) {
					*modified = true
					(*existing)[eidx] = required[ridx]
				}
				break
			}
		}
		if !found {
			*modified = true
			*existing = append(*existing, required[ridx])
		}
	}
}

func boolPtr(val bool) *bool {
	return &val
}
