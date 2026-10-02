package resourceapply

import (
	"context"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgclientv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/typed/machineconfiguration/v1"

	mcoResourceMerge "github.com/openshift/machine-config-operator/lib/resourcemerge"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
)

// ApplyMachineConfig applies the required machineconfig to the cluster.
func ApplyMachineConfig(client mcfgclientv1.MachineConfigsGetter, required *mcfgv1.MachineConfig) (*mcfgv1.MachineConfig, bool, error) {
	existing, err := client.MachineConfigs().Get(context.TODO(), required.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		actual, err := client.MachineConfigs().Create(context.TODO(), required, metav1.CreateOptions{})
		return actual, true, err
	}
	if err != nil {
		return nil, false, err
	}

	modified := ptr.To(false)
	mcoResourceMerge.EnsureMachineConfig(modified, existing, *required)
	if !*modified {
		return existing, false, nil
	}

	actual, err := client.MachineConfigs().Update(context.TODO(), existing, metav1.UpdateOptions{})
	return actual, true, err
}

// ApplyMachineConfigPool applies the required machineconfig to the cluster.
func ApplyMachineConfigPool(client mcfgclientv1.MachineConfigPoolsGetter, required *mcfgv1.MachineConfigPool) (*mcfgv1.MachineConfigPool, bool, error) {
	existing, err := client.MachineConfigPools().Get(context.TODO(), required.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		actual, err := client.MachineConfigPools().Create(context.TODO(), required, metav1.CreateOptions{})
		return actual, true, err
	}
	if err != nil {
		return nil, false, err
	}

	modified := ptr.To(false)
	mcoResourceMerge.EnsureMachineConfigPool(modified, existing, *required)
	if !*modified {
		return existing, false, nil
	}

	actual, err := client.MachineConfigPools().Update(context.TODO(), existing, metav1.UpdateOptions{})
	return actual, true, err
}

// ApplyMachineConfigNode applies the required machineconfignode to the cluster.
func ApplyMachineConfigNode(ctx context.Context, client mcfgclientv1.MachineConfigNodesGetter, required *mcfgv1.MachineConfigNode) (*mcfgv1.MachineConfigNode, bool, error) {
	var actual *mcfgv1.MachineConfigNode
	modified := false
	err := retryOnConflictWithContext(ctx, retry.DefaultBackoff, func(ctx context.Context) error {
		actual = nil
		modified = false

		existing, err := client.MachineConfigNodes().Get(ctx, required.GetName(), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			actual, err = client.MachineConfigNodes().Create(ctx, required, metav1.CreateOptions{})
			modified = true
			return err
		}
		if err != nil {
			return err
		}

		merged := ptr.To(false)
		mcoResourceMerge.EnsureMachineConfigNode(merged, existing, *required)
		if !*merged {
			actual = existing
			return nil
		}

		actual, err = client.MachineConfigNodes().Update(ctx, existing, metav1.UpdateOptions{})
		modified = true
		return err
	})
	return actual, modified, err
}

func retryOnConflictWithContext(ctx context.Context, backoff wait.Backoff, fn func(context.Context) error) error {
	var lastErr error
	err := wait.ExponentialBackoffWithContext(ctx, backoff, func(ctx context.Context) (bool, error) {
		err := fn(ctx)
		switch {
		case err == nil:
			return true, nil
		case apierrors.IsConflict(err):
			lastErr = err
			return false, nil
		default:
			return false, err
		}
	})
	if wait.Interrupted(err) {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return lastErr
	}
	return err
}

// ApplyControllerConfig applies the required machineconfig to the cluster.
func ApplyControllerConfig(client mcfgclientv1.ControllerConfigsGetter, required *mcfgv1.ControllerConfig) (*mcfgv1.ControllerConfig, bool, error) {
	klog.V(4).Infof("Getting existing ControllerConfig with name: %s", required.GetName())
	existing, err := client.ControllerConfigs().Get(context.TODO(), required.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		klog.Info("ControllerConfig not found, creating new one")
		actual, err := client.ControllerConfigs().Create(context.TODO(), required, metav1.CreateOptions{})
		if err != nil {
			klog.Errorf("Failed to create ControllerConfig: %v", err)
		}
		return actual, true, err
	}
	if err != nil {
		klog.Errorf("Error fetching ControllerConfig: %v", err)
		return nil, false, err
	}

	modified := ptr.To(false)
	mcoResourceMerge.EnsureControllerConfig(modified, existing, *required)
	if !*modified {
		klog.V(4).Info("No updates required for the ControllerConfig")
		return existing, false, nil
	}

	klog.V(4).Info("Updating existing ControllerConfig")
	actual, err := client.ControllerConfigs().Update(context.TODO(), existing, metav1.UpdateOptions{})
	if err != nil {
		klog.Errorf("Failed to update ControllerConfig: %v", err)
	}
	return actual, true, err
}
