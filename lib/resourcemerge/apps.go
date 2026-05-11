package resourcemerge

import (
	"github.com/openshift/library-go/pkg/operator/resource/resourcemerge"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/equality"
)

// EnsureDeployment ensures that the existing matches the required.
// modified is set to true when existing had to be updated with required.
func EnsureDeployment(modified *bool, existing *appsv1.Deployment, required appsv1.Deployment) {
	resourcemerge.EnsureObjectMeta(modified, &existing.ObjectMeta, required.ObjectMeta)

	if existing.Spec.Selector == nil {
		*modified = true
		existing.Spec.Selector = required.Spec.Selector
	}
	if !equality.Semantic.DeepEqual(existing.Spec.Selector, required.Spec.Selector) {
		*modified = true
		existing.Spec.Selector = required.Spec.Selector
	}

	if required.Spec.Replicas != nil {
		if existing.Spec.Replicas == nil || *existing.Spec.Replicas != *required.Spec.Replicas {
			*modified = true
			r := *required.Spec.Replicas
			existing.Spec.Replicas = &r
		}
	}

	ensurePodTemplateSpec(modified, &existing.Spec.Template, required.Spec.Template)
}

// EnsureDaemonSet ensures that the existing matches the required.
// modified is set to true when existing had to be updated with required.
func EnsureDaemonSet(modified *bool, existing *appsv1.DaemonSet, required appsv1.DaemonSet) {
	resourcemerge.EnsureObjectMeta(modified, &existing.ObjectMeta, required.ObjectMeta)

	if existing.Spec.Selector == nil {
		*modified = true
		existing.Spec.Selector = required.Spec.Selector
	}
	if !equality.Semantic.DeepEqual(existing.Spec.Selector, required.Spec.Selector) {
		*modified = true
		existing.Spec.Selector = required.Spec.Selector
	}

	if !equality.Semantic.DeepEqual(existing.Spec.UpdateStrategy, required.Spec.UpdateStrategy) {
		*modified = true
		existing.Spec.UpdateStrategy = required.Spec.UpdateStrategy
	}

	ensurePodTemplateSpec(modified, &existing.Spec.Template, required.Spec.Template)
}
