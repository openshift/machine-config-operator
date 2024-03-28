package resourcemerge

import (
	"github.com/openshift/library-go/pkg/operator/resource/resourcemerge"
	"k8s.io/apimachinery/pkg/api/equality"

	admissionregistrationv1beta1 "k8s.io/api/admissionregistration/v1beta1"
)

// EnsureValidatingAdmissionPolicy ensures that the existing matches the required.
// modified is set to true when existing had to be updated with required.
func EnsureValidatingAdmissionPolicy(modified *bool, existing *admissionregistrationv1beta1.ValidatingAdmissionPolicy, required admissionregistrationv1beta1.ValidatingAdmissionPolicy) {
	resourcemerge.EnsureObjectMeta(modified, &existing.ObjectMeta, required.ObjectMeta)

	if !equality.Semantic.DeepEqual(existing.Spec, required.Spec) {
		*modified = true
		existing.Spec = required.Spec
	}

}

// EnsureValidatingAdmissionPolicyBinding ensures that the existing matches the required.
// modified is set to true when existing had to be updated with required.
func EnsureValidatingAdmissionPolicyBinding(modified *bool, existing *admissionregistrationv1beta1.ValidatingAdmissionPolicyBinding, required admissionregistrationv1beta1.ValidatingAdmissionPolicyBinding) {
	resourcemerge.EnsureObjectMeta(modified, &existing.ObjectMeta, required.ObjectMeta)

	if !equality.Semantic.DeepEqual(existing.Spec, required.Spec) {
		*modified = true
		existing.Spec = required.Spec
	}

}
