package resourceapply

import (
	"context"

	"github.com/openshift/library-go/pkg/operator/resource/resourcemerge"
	mcoResourceMerge "github.com/openshift/machine-config-operator/lib/resourcemerge"
	admissionregistrationv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	admissionregistrationclientv1beta1 "k8s.io/client-go/kubernetes/typed/admissionregistration/v1beta1"
)

// ApplyValidatingAdmissionPolicy applies the required validating policy to the cluster.
func ApplyValidatingAdmissionPolicy(client admissionregistrationclientv1beta1.ValidatingAdmissionPoliciesGetter, required *admissionregistrationv1beta1.ValidatingAdmissionPolicy) (*admissionregistrationv1beta1.ValidatingAdmissionPolicy, bool, error) {
	existing, err := client.ValidatingAdmissionPolicies().Get(context.TODO(), required.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		actual, err := client.ValidatingAdmissionPolicies().Create(context.TODO(), required, metav1.CreateOptions{})
		return actual, true, err
	}
	if err != nil {
		return nil, false, err
	}

	modified := resourcemerge.BoolPtr(false)
	mcoResourceMerge.EnsureValidatingAdmissionPolicy(modified, existing, *required)
	if !*modified {
		return existing, false, nil
	}

	actual, err := client.ValidatingAdmissionPolicies().Update(context.TODO(), existing, metav1.UpdateOptions{})
	return actual, true, err

}

// ApplyValidatingAdmissionPolicyBinding applies the required validating policy binding to the cluster.
func ApplyValidatingAdmissionPolicyBinding(client admissionregistrationclientv1beta1.ValidatingAdmissionPolicyBindingsGetter, required *admissionregistrationv1beta1.ValidatingAdmissionPolicyBinding) (*admissionregistrationv1beta1.ValidatingAdmissionPolicyBinding, bool, error) {
	existing, err := client.ValidatingAdmissionPolicyBindings().Get(context.TODO(), required.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		actual, err := client.ValidatingAdmissionPolicyBindings().Create(context.TODO(), required, metav1.CreateOptions{})
		return actual, true, err
	}
	if err != nil {
		return nil, false, err
	}

	modified := resourcemerge.BoolPtr(false)
	mcoResourceMerge.EnsureValidatingAdmissionPolicyBinding(modified, existing, *required)
	if !*modified {
		return existing, false, nil
	}

	actual, err := client.ValidatingAdmissionPolicyBindings().Update(context.TODO(), existing, metav1.UpdateOptions{})
	return actual, true, err

}
