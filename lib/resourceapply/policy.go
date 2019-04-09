package resourceapply

import (
	"github.com/openshift/machine-config-operator/lib/resourcemerge"
	policyv1 "k8s.io/api/policy/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	policyclientv1 "k8s.io/client-go/kubernetes/typed/policy/v1beta1"
)

// ApplyPodDisruptionBudget applies the required podDisruptionBudget to the cluster.
func ApplyPodDisruptionBudget(client policyclientv1.PodDisruptionBudgetsGetter, required *policyv1.PodDisruptionBudget) (*policyv1.PodDisruptionBudget, bool, error) {
	existing, err := client.PodDisruptionBudgets(required.Namespace).Get(required.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		actual, err := client.PodDisruptionBudgets(required.Namespace).Create(required)
		return actual, true, err
	}
	if err != nil {
		return nil, false, err
	}

	modified := resourcemerge.BoolPtr(false)
	resourcemerge.EnsurePodDisruptionBudget(modified, existing, *required)
	if !*modified {
		return existing, false, nil
	}

	actual, err := client.PodDisruptionBudgets(required.Namespace).Update(existing)
	return actual, true, err
}
