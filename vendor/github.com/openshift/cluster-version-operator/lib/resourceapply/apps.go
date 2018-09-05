package resourceapply

import (
	"github.com/openshift/cluster-version-operator/lib/resourcemerge"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	appsclientv1 "k8s.io/client-go/kubernetes/typed/apps/v1"
	appslisterv1 "k8s.io/client-go/listers/apps/v1"
	"k8s.io/utils/pointer"
)

// ApplyDeployment applies the required deployment to the cluster.
func ApplyDeployment(client appsclientv1.DeploymentsGetter, required *appsv1.Deployment) (*appsv1.Deployment, bool, error) {
	existing, err := client.Deployments(required.Namespace).Get(required.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		actual, err := client.Deployments(required.Namespace).Create(required)
		return actual, true, err
	}
	if err != nil {
		return nil, false, err
	}

	modified := pointer.BoolPtr(false)
	resourcemerge.EnsureDeployment(modified, existing, *required)
	if !*modified {
		return existing, false, nil
	}

	actual, err := client.Deployments(required.Namespace).Update(existing)
	return actual, true, err
}

// ApplyDeploymentFromCache applies the required deployment to the cluster.
func ApplyDeploymentFromCache(lister appslisterv1.DeploymentLister, client appsclientv1.DeploymentsGetter, required *appsv1.Deployment) (*appsv1.Deployment, bool, error) {
	existing, err := lister.Deployments(required.Namespace).Get(required.Name)
	if apierrors.IsNotFound(err) {
		actual, err := client.Deployments(required.Namespace).Create(required)
		return actual, true, err
	}
	if err != nil {
		return nil, false, err
	}

	existing = existing.DeepCopy()
	modified := pointer.BoolPtr(false)
	resourcemerge.EnsureDeployment(modified, existing, *required)
	if !*modified {
		return existing, false, nil
	}

	actual, err := client.Deployments(required.Namespace).Update(existing)
	return actual, true, err
}
