package resourceapply

import (
	"context"
	"strings"

	mcoResourceMerge "github.com/openshift/machine-config-operator/lib/resourcemerge"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/klog/v2"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	appsclientv1 "k8s.io/client-go/kubernetes/typed/apps/v1"
)

func IsApplyErrorRetriable(err error) bool {
	// Retry on brief rpc errors
	// See https://issues.redhat.com/browse/OCPBUGS-24228 for more information.
	// String compare isn't great, but could not find a good error type for this in the standard libraries
	if strings.Contains(err.Error(), "rpc error") {
		return true
	}
	// Retry on resource conflict errors, i.e "the object has been modified; please apply your changes to the latest version and try again"
	// See last few comments on https://issues.redhat.com/browse/OCPBUGS-9108 for more information
	if apierrors.IsConflict(err) {
		return true
	}
	// Retry when the server takes too long to respond to the apply requests.
	if apierrors.IsTimeout(err) {
		return true
	}
	// Add any other errors to be added to the retry here.

	klog.Infof("Skipping retry in Apply fn for error: %s", err)
	return false
}

// ApplyDaemonSet applies the required daemonset to the cluster.
func ApplyDaemonSet(client appsclientv1.DaemonSetsGetter, required *appsv1.DaemonSet) (*appsv1.DaemonSet, bool, error) {
	existing, err := client.DaemonSets(required.Namespace).Get(context.TODO(), required.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		actual, err := client.DaemonSets(required.Namespace).Create(context.TODO(), required, metav1.CreateOptions{})
		return actual, true, err
	}
	if err != nil {
		return nil, false, err
	}

	modified := false
	mcoResourceMerge.EnsureDaemonSet(&modified, existing, *required)
	if !modified {
		return existing, false, nil
	}

	actual, err := client.DaemonSets(required.Namespace).Update(context.TODO(), existing, metav1.UpdateOptions{})
	return actual, true, err
}

// ApplyDeployment applies the required deployment to the cluster.
func ApplyDeployment(client appsclientv1.DeploymentsGetter, required *appsv1.Deployment) (*appsv1.Deployment, bool, error) {
	existing, err := client.Deployments(required.Namespace).Get(context.TODO(), required.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		actual, err := client.Deployments(required.Namespace).Create(context.TODO(), required, metav1.CreateOptions{})
		return actual, true, err
	}
	if err != nil {
		return nil, false, err
	}

	modified := false
	mcoResourceMerge.EnsureDeployment(&modified, existing, *required)
	if !modified {
		return existing, false, nil
	}

	actual, err := client.Deployments(required.Namespace).Update(context.TODO(), existing, metav1.UpdateOptions{})
	return actual, true, err
}
