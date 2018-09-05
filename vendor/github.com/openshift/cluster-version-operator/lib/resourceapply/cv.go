package resourceapply

import (
	"fmt"

	"github.com/openshift/cluster-version-operator/lib/resourcemerge"
	"github.com/openshift/cluster-version-operator/pkg/apis/clusterversion.openshift.io/v1"
	clientv1 "github.com/openshift/cluster-version-operator/pkg/generated/clientset/versioned/typed/clusterversion.openshift.io/v1"
	listersv1 "github.com/openshift/cluster-version-operator/pkg/generated/listers/clusterversion.openshift.io/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"
)

func ApplyOperatorStatus(client clientv1.OperatorStatusesGetter, required *v1.OperatorStatus) (*v1.OperatorStatus, bool, error) {
	if required.Extension.Raw != nil && required.Extension.Object != nil {
		return nil, false, fmt.Errorf("both extension.Raw and extension.Object should not be set")
	}
	existing, err := client.OperatorStatuses(required.Namespace).Get(required.Name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		actual, err := client.OperatorStatuses(required.Namespace).Create(required)
		return actual, true, err
	}
	if err != nil {
		return nil, false, err
	}

	modified := pointer.BoolPtr(false)
	resourcemerge.EnsureOperatorStatus(modified, existing, *required)
	if !*modified {
		return existing, false, nil
	}

	actual, err := client.OperatorStatuses(required.Namespace).Update(existing)
	return actual, true, err
}

func ApplyOperatorStatusFromCache(lister listersv1.OperatorStatusLister, client clientv1.OperatorStatusesGetter, required *v1.OperatorStatus) (*v1.OperatorStatus, bool, error) {
	if required.Extension.Raw != nil && required.Extension.Object != nil {
		return nil, false, fmt.Errorf("both extension.Raw and extension.Object should not be set")
	}
	existing, err := lister.OperatorStatuses(required.Namespace).Get(required.Name)
	if errors.IsNotFound(err) {
		actual, err := client.OperatorStatuses(required.Namespace).Create(required)
		return actual, true, err
	}
	if err != nil {
		return nil, false, err
	}

	// Don't want to mutate cache.
	existing = existing.DeepCopy()
	modified := pointer.BoolPtr(false)
	resourcemerge.EnsureOperatorStatus(modified, existing, *required)
	if !*modified {
		return existing, false, nil
	}

	actual, err := client.OperatorStatuses(required.Namespace).Update(existing)
	return actual, true, err
}

func ApplyCVOConfig(client clientv1.CVOConfigsGetter, required *v1.CVOConfig) (*v1.CVOConfig, bool, error) {
	existing, err := client.CVOConfigs(required.Namespace).Get(required.Name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		actual, err := client.CVOConfigs(required.Namespace).Create(required)
		return actual, true, err
	}
	if err != nil {
		return nil, false, err
	}

	modified := pointer.BoolPtr(false)
	resourcemerge.EnsureCVOConfig(modified, existing, *required)
	if !*modified {
		return existing, false, nil
	}

	actual, err := client.CVOConfigs(required.Namespace).Update(existing)
	return actual, true, err
}

func ApplyCVOConfigFromCache(lister listersv1.CVOConfigLister, client clientv1.CVOConfigsGetter, required *v1.CVOConfig) (*v1.CVOConfig, bool, error) {
	obj, err := lister.CVOConfigs(required.Namespace).Get(required.Name)
	if errors.IsNotFound(err) {
		actual, err := client.CVOConfigs(required.Namespace).Create(required)
		return actual, true, err
	}
	if err != nil {
		return nil, false, err
	}

	// Don't want to mutate cache.
	existing := new(v1.CVOConfig)
	obj.DeepCopyInto(existing)
	modified := pointer.BoolPtr(false)
	resourcemerge.EnsureCVOConfig(modified, existing, *required)
	if !*modified {
		return existing, false, nil
	}

	actual, err := client.CVOConfigs(required.Namespace).Update(existing)
	return actual, true, err
}
