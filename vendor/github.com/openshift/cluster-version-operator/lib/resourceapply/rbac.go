package resourceapply

import (
	"github.com/openshift/cluster-version-operator/lib/resourcemerge"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	rbacclientv1 "k8s.io/client-go/kubernetes/typed/rbac/v1"
	"k8s.io/utils/pointer"
)

// ApplyClusterRoleBinding applies the required clusterrolebinding to the cluster.
func ApplyClusterRoleBinding(client rbacclientv1.ClusterRoleBindingsGetter, required *rbacv1.ClusterRoleBinding) (*rbacv1.ClusterRoleBinding, bool, error) {
	existing, err := client.ClusterRoleBindings().Get(required.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		actual, err := client.ClusterRoleBindings().Create(required)
		return actual, true, err
	}
	if err != nil {
		return nil, false, err
	}

	modified := pointer.BoolPtr(false)
	resourcemerge.EnsureClusterRoleBinding(modified, existing, *required)
	if !*modified {
		return existing, false, nil
	}

	actual, err := client.ClusterRoleBindings().Update(existing)
	return actual, true, err
}

// ApplyClusterRole applies the required clusterrole to the cluster.
func ApplyClusterRole(client rbacclientv1.ClusterRolesGetter, required *rbacv1.ClusterRole) (*rbacv1.ClusterRole, bool, error) {
	existing, err := client.ClusterRoles().Get(required.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		actual, err := client.ClusterRoles().Create(required)
		return actual, true, err
	}
	if err != nil {
		return nil, false, err
	}

	modified := pointer.BoolPtr(false)
	resourcemerge.EnsureClusterRole(modified, existing, *required)
	if !*modified {
		return existing, false, nil
	}

	actual, err := client.ClusterRoles().Update(existing)
	return actual, true, err
}
