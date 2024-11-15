package rollout

import (
	"context"
	"fmt"
	"time"

	commonconsts "github.com/openshift/machine-config-operator/pkg/controller/common/constants"
	"github.com/openshift/machine-config-operator/test/framework"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
)

func WaitForRolloutToComplete(cs *framework.ClientSet, digestedPullspec string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	allComponents := sets.New[string](mcoDaemonsets...).Insert(mcoDeployments...)
	updatedComponents := sets.New[string]()

	return wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		for _, daemonset := range mcoDaemonsets {
			if updatedComponents.Has(daemonset) {
				continue
			}

			isUpdated, err := isDaemonsetUpToDate(ctx, cs, daemonset, digestedPullspec)
			if err != nil {
				return false, err
			}

			if isUpdated {
				updatedComponents.Insert(daemonset)
			}
		}

		for _, deployment := range mcoDeployments {
			if updatedComponents.Has(deployment) {
				continue
			}

			isUpdated, err := isDeploymenttUpToDate(ctx, cs, deployment, digestedPullspec)
			if err != nil {
				return false, err
			}

			if isUpdated {
				updatedComponents.Insert(deployment)
			}
		}

		return updatedComponents.Equal(allComponents), nil
	})
}

func isAllPodsForComponentUpdated(ctx context.Context, cs *framework.ClientSet, componentName, digestedPullspec string) (bool, error) {
	pods, err := cs.CoreV1Interface.Pods(commonconsts.MCONamespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("k8s-app=%s", componentName),
	})

	if err != nil {
		return false, err
	}

	for _, pod := range pods.Items {
		pod := pod
		if !isPodOnLatestPullspec(&pod, componentName, digestedPullspec) {
			return false, nil
		}
	}

	return true, nil
}

func isPodOnLatestPullspec(pod *corev1.Pod, componentName, digestedPullspec string) bool {
	trueBool := true

	for _, status := range pod.Status.ContainerStatuses {
		if status.Name != componentName {
			continue
		}

		if status.ImageID != digestedPullspec {
			return false
		}

		if !status.Ready {
			return false
		}

		if status.Started == nil {
			return false
		}

		if status.Started != &trueBool {
			return false
		}

		return true
	}

	return false
}

func isDeploymenttUpToDate(ctx context.Context, cs *framework.ClientSet, componentName, digestedPullspec string) (bool, error) {
	dp, err := cs.AppsV1Interface.Deployments(commonconsts.MCONamespace).Get(ctx, componentName, metav1.GetOptions{})
	if err != nil {
		return false, err
	}

	if dp.Status.Replicas != dp.Status.UpdatedReplicas {
		return false, nil
	}

	return isAllPodsForComponentUpdated(ctx, cs, componentName, digestedPullspec)
}

func isDaemonsetUpToDate(ctx context.Context, cs *framework.ClientSet, componentName, digestedPullspec string) (bool, error) {
	ds, err := cs.AppsV1Interface.DaemonSets(commonconsts.MCONamespace).Get(ctx, componentName, metav1.GetOptions{})
	if err != nil {
		return false, err
	}

	if ds.Status.DesiredNumberScheduled != ds.Status.UpdatedNumberScheduled {
		return false, nil
	}

	return isAllPodsForComponentUpdated(ctx, cs, componentName, digestedPullspec)
}
