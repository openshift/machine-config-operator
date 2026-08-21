package rollout

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/openshift/machine-config-operator/devex/internal/pkg/releasecontroller"
	"github.com/openshift/machine-config-operator/test/framework"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	corev1 "k8s.io/api/core/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

var (
	mcoDaemonsets = []string{
		"machine-config-daemon",
		"machine-config-server",
	}

	mcoDeployments = []string{
		"machine-config-operator",
		"machine-config-controller",
		"machine-os-builder",
	}
)

const (
	cvoName      string = "cluster-version-operator"
	cvoNamespace string = "openshift-cluster-version"
	mcoName      string = "machine-config-operator"

	mcoImagesConfigMap string = "machine-config-operator-images"
	mcoImageKey        string = "machineConfigOperator"
	mcoImagesJSON      string = "images.json"
)

func RevertToOriginalMCOImage(ctx context.Context, cs *framework.ClientSet, forceRestart bool) error {
	clusterVersion, err := cs.ConfigV1Interface.ClusterVersions().Get(ctx, "version", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("could not get cluster version: %w", err)
	}

	currentRelease := clusterVersion.Status.Desired.Image
	originalMCOImage, err := releasecontroller.GetComponentPullspecForRelease(mcoName, currentRelease)
	if err != nil {
		return fmt.Errorf("could not get MCO pullspec for cluster version %s: %w", currentRelease, err)
	}

	klog.Infof("Found original MCO image %s for the currently running cluster release (%s)", originalMCOImage, currentRelease)

	if err := ReplaceMCOImage(ctx, cs, originalMCOImage, forceRestart); err != nil {
		return fmt.Errorf("could not roll MCO back to image %s: %w", originalMCOImage, err)
	}

	if err := setDeploymentReplicas(ctx, cs, cvoName, cvoNamespace, 1); err != nil {
		return fmt.Errorf("could not restore cluster version operator to default replica count of 1")
	}

	return nil
}

// cleanupTimeout is the maximum time allowed for restoring scaled-down
// deployments when the caller's context has already been cancelled.
const cleanupTimeout = 30 * time.Second

func ReplaceMCOImage(ctx context.Context, cs *framework.ClientSet, pullspec string, forceRestart bool) error {
	var cvoScaledDown, mcoScaledDown bool

	if err := setDeploymentReplicas(ctx, cs, cvoName, cvoNamespace, 0); err != nil {
		return fmt.Errorf("could not scale cluster version operator down to zero: %w", err)
	}
	cvoScaledDown = true

	if err := setDeploymentReplicas(ctx, cs, mcoName, ctrlcommon.MCONamespace, 0); err != nil {
		return fmt.Errorf("could not scale machine config operator down to zero: %w", err)
	}
	mcoScaledDown = true

	if err := setPullspecOnObjects(ctx, cs, pullspec, forceRestart); err != nil {
		if ctx.Err() != nil {
			restoreAfterInterruption(cs, cvoScaledDown, mcoScaledDown)
		}
		return err
	}

	if err := setDeploymentReplicas(ctx, cs, mcoName, ctrlcommon.MCONamespace, 1); err != nil {
		if ctx.Err() != nil {
			restoreAfterInterruption(cs, cvoScaledDown, false)
		}
		return fmt.Errorf("could not scale machine config operator back up: %w", err)
	}

	return nil
}

// restoreAfterInterruption attempts to scale deployments back to one replica
// after the caller's context was cancelled or its deadline exceeded. It uses a
// fresh, bounded context so that the cleanup calls are not immediately rejected.
func restoreAfterInterruption(cs *framework.ClientSet, cvoScaledDown, mcoScaledDown bool) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()

	if mcoScaledDown {
		if err := setDeploymentReplicas(cleanupCtx, cs, mcoName, ctrlcommon.MCONamespace, 1); err != nil {
			klog.Errorf("cleanup: could not restore MCO replicas after interruption: %v", err)
		}
	}

	if cvoScaledDown {
		if err := setDeploymentReplicas(cleanupCtx, cs, cvoName, cvoNamespace, 1); err != nil {
			klog.Errorf("cleanup: could not restore CVO replicas after interruption: %v", err)
		}
	}
}

func RestartMCO(ctx context.Context, cs *framework.ClientSet, forceRestart bool) error {
	if forceRestart {
		return forceRestartMCO(ctx, cs)
	}

	_, images, err := loadMCOImagesConfigMap(ctx, cs)
	if err != nil {
		return fmt.Errorf("could not load or parse ConfigMap %s: %w", mcoImagesConfigMap, err)
	}

	return ReplaceMCOImage(ctx, cs, images[mcoImageKey], forceRestart)
}

func forceRestartMCO(ctx context.Context, cs *framework.ClientSet) error {
	eg := errgroup.Group{}

	for _, name := range append(mcoDeployments, mcoDaemonsets...) {
		name := name
		eg.Go(func() error {
			return forceRestartPodsForDeploymentOrDaemonset(ctx, cs, name)
		})
	}

	return eg.Wait()
}

func forceRestartPodsForDeploymentOrDaemonset(ctx context.Context, cs *framework.ClientSet, name string) error {
	podList, err := cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("k8s-app==%s", name),
	})

	klog.Infof("Found (%d) pods for %s", len(podList.Items), name)

	if err != nil {
		return err
	}

	eg := errgroup.Group{}

	for _, pod := range podList.Items {
		pod := pod
		eg.Go(func() error {
			if err := cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil {
				return fmt.Errorf("could not delete pod %s: %w", pod.Name, err)
			}

			klog.Infof("Deleted pod %s", pod.Name)

			return nil
		})
	}

	return eg.Wait()
}

func setPullspecOnObjects(ctx context.Context, cs *framework.ClientSet, pullspec string, forceRestart bool) error {
	eg := errgroup.Group{}

	eg.Go(func() error {
		if err := maybeUpdateMCOConfigMap(ctx, cs, pullspec); err != nil {
			return fmt.Errorf("could not update MCO images ConfigMap: %w", err)
		}

		return nil
	})

	eg.Go(func() error {
		if err := updateDaemonsets(ctx, cs, pullspec, forceRestart); err != nil {
			return fmt.Errorf("could not update daemonsets: %w", err)
		}

		return nil
	})

	eg.Go(func() error {
		if err := updateDeployments(ctx, cs, pullspec, forceRestart); err != nil {
			return fmt.Errorf("could not update deployments: %w", err)
		}

		return nil
	})

	return eg.Wait()
}

func updateDeployments(ctx context.Context, cs *framework.ClientSet, pullspec string, forceRestart bool) error {
	eg := errgroup.Group{}

	for _, name := range mcoDeployments {
		name := name
		eg.Go(func() error {
			if err := updateDeployment(ctx, cs, name, pullspec); err != nil {
				return fmt.Errorf("could not update deployment/%s: %w", name, err)
			}

			if forceRestart {
				return forceRestartPodsForDeploymentOrDaemonset(ctx, cs, name)
			}

			return nil
		})
	}

	return eg.Wait()
}

func updateDaemonsets(ctx context.Context, cs *framework.ClientSet, pullspec string, forceRestart bool) error {
	eg := errgroup.Group{}

	for _, name := range mcoDaemonsets {
		name := name
		eg.Go(func() error {
			if err := updateDaemonset(ctx, cs, name, pullspec); err != nil {
				return fmt.Errorf("could not update daemonset/%s: %w", name, err)
			}

			if forceRestart {
				return forceRestartPodsForDeploymentOrDaemonset(ctx, cs, name)
			}

			return nil
		})
	}

	return eg.Wait()
}

func loadMCOImagesConfigMap(ctx context.Context, cs *framework.ClientSet) (*corev1.ConfigMap, map[string]string, error) {
	cm, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, mcoImagesConfigMap, metav1.GetOptions{})
	if err != nil {
		return nil, nil, err
	}

	_, ok := cm.Data[mcoImagesJSON]
	if !ok {
		return nil, nil, fmt.Errorf("expected Configmap %s to have key %s, but was missing", mcoImagesConfigMap, mcoImagesJSON)
	}

	images := map[string]string{}

	if err := json.Unmarshal([]byte(cm.Data[mcoImagesJSON]), &images); err != nil {
		return nil, nil, fmt.Errorf("could not unpack %s in Configmap %s: %w", mcoImagesJSON, mcoImagesConfigMap, err)
	}

	if _, ok := images[mcoImageKey]; !ok {
		return nil, nil, fmt.Errorf("expected %s in Configmap %s to have key %s, but was missing", mcoImagesJSON, mcoImagesConfigMap, mcoImageKey)
	}

	return cm, images, nil
}

func maybeUpdateMCOConfigMap(ctx context.Context, cs *framework.ClientSet, pullspec string) error {
	_, images, err := loadMCOImagesConfigMap(ctx, cs)
	if err != nil {
		return fmt.Errorf("could not load or parse ConfigMap %s: %w", mcoImagesConfigMap, err)
	}

	if images[mcoImageKey] != pullspec {
		klog.Warningf("ConfigMap %s has pullspec %s, which will change to %s. A MachineConfig update will occur as a result.", mcoImagesConfigMap, images[mcoImageKey], pullspec)
		if err := updateMCOConfigMap(ctx, cs, pullspec); err != nil {
			return err
		}
	} else {
		klog.Infof("ConfigMap %s already has pullspec %s. Will restart MCO components to cause an update.", mcoImagesConfigMap, pullspec)
	}

	return nil
}

func updateMCOConfigMap(ctx context.Context, cs *framework.ClientSet, pullspec string) error {
	err := retryOnConflictContext(ctx, retry.DefaultRetry, func() error {
		cm, images, err := loadMCOImagesConfigMap(ctx, cs)
		if err != nil {
			return err
		}

		images[mcoImageKey] = pullspec

		imagesBytes, err := json.Marshal(images)
		if err != nil {
			return err
		}

		cm.Data[mcoImagesJSON] = string(imagesBytes)

		_, err = cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Update(ctx, cm, metav1.UpdateOptions{})
		return err
	})

	if err == nil {
		klog.Infof("Set %s in %s in ConfigMap %s to %s", mcoImageKey, mcoImagesJSON, mcoImagesConfigMap, pullspec)
		return nil
	}

	return fmt.Errorf("could not update ConfigMap %s: %w", mcoImagesConfigMap, err)
}

func updateDeployment(ctx context.Context, cs *framework.ClientSet, name, pullspec string) error {
	return retryOnConflictContext(ctx, retry.DefaultBackoff, func() error {
		deploy, err := cs.AppsV1Interface.Deployments(ctrlcommon.MCONamespace).Get(ctx, name, metav1.GetOptions{})
		if name == "machine-os-builder" && apierrs.IsNotFound(err) {
			return nil
		}

		if err != nil {
			return err
		}

		if containersNeedUpdated(name, pullspec, deploy.Spec.Template.Spec.Containers) {
			klog.Infof("Updating deployment/%s", name)
			deploy.Spec.Template.Spec.Containers = updateContainers(name, pullspec, deploy.Spec.Template.Spec.Containers)
		} else {
			// Cribbed from: https://github.com/kubernetes/kubectl/blob/master/pkg/polymorphichelpers/objectrestarter.go#L32-L119 and https://github.com/derailed/k9s/blob/master/internal/dao/dp.go#L68-L114
			klog.Infof("Restarting deployment/%s", name)
			deploy.Spec.Template.ObjectMeta.Annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().Format(time.RFC3339)
		}

		_, err = cs.AppsV1Interface.Deployments(ctrlcommon.MCONamespace).Update(ctx, deploy, metav1.UpdateOptions{})
		return err
	})
}

func updateDaemonset(ctx context.Context, cs *framework.ClientSet, name, pullspec string) error {
	return retryOnConflictContext(ctx, retry.DefaultBackoff, func() error {
		ds, err := cs.AppsV1Interface.DaemonSets(ctrlcommon.MCONamespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		if containersNeedUpdated(name, pullspec, ds.Spec.Template.Spec.Containers) {
			klog.Infof("Updating daemonset/%s", name)
			ds.Spec.Template.Spec.Containers = updateContainers(name, pullspec, ds.Spec.Template.Spec.Containers)
		} else {
			// Cribbed from: https://github.com/kubernetes/kubectl/blob/master/pkg/polymorphichelpers/objectrestarter.go#L32-L119 and https://github.com/derailed/k9s/blob/master/internal/dao/dp.go#L68-L114
			klog.Infof("Restarting daemonset/%s", name)
			ds.Spec.Template.ObjectMeta.Annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().Format(time.RFC3339)
		}

		_, err = cs.AppsV1Interface.DaemonSets(ctrlcommon.MCONamespace).Update(ctx, ds, metav1.UpdateOptions{})
		return err
	})
}

func containersNeedUpdated(name, pullspec string, containers []corev1.Container) bool {
	for _, container := range containers {
		if container.Name == name {
			return container.Image != pullspec
		}
	}

	return false
}

func updateContainers(name, pullspec string, containers []corev1.Container) []corev1.Container {
	out := []corev1.Container{}

	for _, container := range containers {
		if container.Name == name {
			container.Image = pullspec
			container.ImagePullPolicy = corev1.PullAlways
		}

		out = append(out, container)
	}

	return out
}

func setDeploymentReplicas(ctx context.Context, cs *framework.ClientSet, deploymentName, namespace string, replicas int32) error {
	klog.Infof("Setting replicas for %s/%s to %d", namespace, deploymentName, replicas)
	return retryOnConflictContext(ctx, retry.DefaultBackoff, func() error {
		scale, err := cs.AppsV1Interface.Deployments(namespace).GetScale(ctx, deploymentName, metav1.GetOptions{})
		if err != nil {
			return err
		}

		scale.Spec.Replicas = replicas

		_, err = cs.AppsV1Interface.Deployments(namespace).UpdateScale(ctx, deploymentName, scale, metav1.UpdateOptions{})
		return err
	})
}

// retryOnConflictContext behaves like retry.RetryOnConflict but also stops
// retrying when ctx is cancelled or its deadline is exceeded. The backoff
// sleep between attempts is interrupted by context cancellation so callers
// are not stuck waiting for the next attempt when the context is already done.
func retryOnConflictContext(ctx context.Context, backoff wait.Backoff, fn func() error) error {
	return retry.OnError(backoff, func(err error) bool {
		if ctx.Err() != nil {
			return false
		}
		return apierrs.IsConflict(err)
	}, fn)
}
