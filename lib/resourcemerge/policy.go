package resourcemerge

import (
	policyv1 "k8s.io/api/policy/v1beta1"
	"k8s.io/apimachinery/pkg/api/equality"
)

// EnsurePodDisruptionBudget ensures that the existing matches the required.
// modified is set to true when existing had to be updated with required.
func EnsurePodDisruptionBudget(modified *bool, existing *policyv1.PodDisruptionBudget, required policyv1.PodDisruptionBudget) {
	EnsureObjectMeta(modified, &existing.ObjectMeta, required.ObjectMeta)
	if !equality.Semantic.DeepEqual(existing.Spec, required.Spec) {
		*modified = true
		existing.Spec = required.Spec
	}
}
