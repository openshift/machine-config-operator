package rollout

import (
	"context"
	"fmt"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/hack/internal/pkg/utils"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/framework"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
)

var pollInterval = time.Second

func WaitForMachineConfigPoolsToComplete(cs *framework.ClientSet, poolNames []string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	klog.Infof("Waiting up to %s for MachineConfigPools %v to reach updated state", timeout, poolNames)

	return WaitForMachineConfigPoolsToCompleteWithContext(ctx, cs, poolNames)
}

func WaitForMachineConfigPoolsToCompleteWithContext(ctx context.Context, cs *framework.ClientSet, poolNames []string) error {
	return waitForMachineConfigPoolsToCompleteWithContext(ctx, cs, poolNames)
}

func WaitForMachineConfigPoolUpdateToCompleteWithContext(ctx context.Context, cs *framework.ClientSet, poolName string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Wait for the pool to begin updating.
	if err := waitForMachineConfigPoolToStart(ctx, cs, poolName); err != nil {
		return fmt.Errorf("pool %s did not start updating: %w", poolName, err)
	}

	return waitForMachineConfigPoolAndNodesToComplete(ctx, cs, poolName)
}

func WaitForMachineConfigPoolUpdateToComplete(cs *framework.ClientSet, timeout time.Duration, poolName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	klog.Infof("Waiting up to %s for MachineConfigPool %s to reach updated state", timeout, poolName)

	return WaitForMachineConfigPoolUpdateToCompleteWithContext(ctx, cs, poolName)
}

func validatePoolIsNotDegraded(mcp *mcfgv1.MachineConfigPool) error {
	degraded := []mcfgv1.MachineConfigPoolConditionType{
		mcfgv1.MachineConfigPoolNodeDegraded,
		mcfgv1.MachineConfigPoolRenderDegraded,
		mcfgv1.MachineConfigPoolDegraded,
		mcfgv1.MachineConfigPoolPinnedImageSetsDegraded,
		mcfgv1.MachineConfigPoolSynchronizerDegraded,
		mcfgv1.MachineConfigPoolBuildFailed,
	}

	for _, item := range degraded {
		if apihelpers.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, item) {
			return fmt.Errorf("pool %s degraded %s", mcp.Name, item)
		}
	}

	return nil
}

func WaitForAllMachineConfigPoolsToComplete(cs *framework.ClientSet, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	poolNames, err := getMachineConfigPoolNames(ctx, cs)
	if err != nil {
		return err
	}

	klog.Infof("Watching MachineConfigPool(s): %v", poolNames)

	start := time.Now()

	err = waitForMachineConfigPoolsToCompleteWithContext(ctx, cs, poolNames)
	if err == nil {
		klog.Infof("All pools updated in %s", time.Since(start))
		return nil
	}

	return err
}

func WaitForMachineConfigPoolToComplete(cs *framework.ClientSet, poolName string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	return waitForNodesToComplete(ctx, cs, poolName)
}

func waitForMachineConfigPoolsToCompleteWithContext(ctx context.Context, cs *framework.ClientSet, poolNames []string) error {
	eg := errgroup.Group{}

	for _, poolName := range poolNames {
		poolName := poolName

		eg.Go(func() error {
			return waitForMachineConfigPoolAndNodesToComplete(ctx, cs, poolName)
		})
	}

	return eg.Wait()
}

func waitForMachineConfigPoolToStart(ctx context.Context, cs *framework.ClientSet, poolName string) error {
	start := time.Now()

	return wait.PollUntilContextCancel(ctx, pollInterval, true, func(ctx context.Context) (bool, error) {
		mcp, err := cs.MachineConfigPools().Get(ctx, poolName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		if apihelpers.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolUpdating) {
			klog.Infof("MachineConfigPool %s began updating after %s", mcp.Name, time.Since(start))
			return true, nil
		}

		return false, validatePoolIsNotDegraded(mcp)
	})
}

func waitForMachineConfigPoolAndNodesToComplete(ctx context.Context, cs *framework.ClientSet, poolName string) error {
	eg := errgroup.Group{}

	eg.Go(func() error {
		return waitForMachineConfigPoolToComplete(ctx, cs, poolName)
	})

	eg.Go(func() error {
		return waitForNodesToComplete(ctx, cs, poolName)
	})

	return eg.Wait()
}

func waitForMachineConfigPoolToComplete(ctx context.Context, cs *framework.ClientSet, poolName string) error {
	start := time.Now()

	return wait.PollUntilContextCancel(ctx, pollInterval, true, func(ctx context.Context) (bool, error) {
		mcp, err := cs.MachineConfigPools().Get(ctx, poolName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		if apihelpers.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolUpdated) {
			klog.Infof("MachineConfigPool %s finished updating after %s", mcp.Name, time.Since(start))
			return true, nil
		}

		return false, validatePoolIsNotDegraded(mcp)
	})
}

func waitForNodesToComplete(ctx context.Context, cs *framework.ClientSet, poolName string) error {
	doneNodes := sets.New[string]()
	nodesForPool := sets.New[string]()

	nodes, err := cs.CoreV1Interface.Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("node-role.kubernetes.io/%s", poolName),
	})

	if err != nil {
		return err
	}

	for _, node := range nodes.Items {
		nodesForPool.Insert(node.Name)
	}

	start := time.Now()

	klog.Infof("Current nodes for pool %q are: %v", poolName, sets.List(nodesForPool))

	mcp, err := cs.MachineConfigPools().Get(ctx, poolName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	mosc, err := utils.GetMachineOSConfigForPool(ctx, cs, mcp)
	if err != nil && !apierrs.IsNotFound(err) {
		return fmt.Errorf("could not get MachineOSConfig: %w", err)
	}

	if mosc == nil && apierrs.IsNotFound(err) {
		klog.Infof("No MachineOSConfig found, will only consider MachineConfigs")
	}

	return wait.PollUntilContextCancel(ctx, pollInterval, true, func(ctx context.Context) (bool, error) {
		nodes, err := cs.CoreV1Interface.Nodes().List(ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("node-role.kubernetes.io/%s", poolName),
		})

		if err != nil {
			return false, err
		}

		for _, node := range nodes.Items {
			node := node

			if !nodesForPool.Has(node.Name) {
				klog.Infof("Pool %s has gained a new node %s", poolName, node.Name)
				nodesForPool.Insert(node.Name)
			}

			if doneNodes.Has(node.Name) {
				continue
			}

			isDone := false
			if mosc == nil {
				isDone = isNodeDoneAtPool(mcp, &node)
			} else {
				isDone = isNodeDoneAtPool(mcp, &node) && isNodeDoneAtMosc(mosc, &node)
			}

			if isDone {
				doneNodes.Insert(node.Name)
				diff := sets.List(nodesForPool.Difference(doneNodes))
				klog.Infof("Node %s in pool %s updated after %s. %d node(s) remaining: %v", node.Name, poolName, time.Since(start), len(diff), diff)
			}
		}

		isDone := doneNodes.Equal(nodesForPool)
		if isDone {
			klog.Infof("%d node(s) in pool %s updated after %s", nodesForPool.Len(), poolName, time.Since(start))
		}

		return isDone, nil
	})
}

func getMachineConfigPoolNames(ctx context.Context, cs *framework.ClientSet) ([]string, error) {
	pools, err := cs.MachineconfigurationV1Interface.MachineConfigPools().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	out := []string{}

	for _, pool := range pools.Items {
		out = append(out, pool.Name)
	}

	return out, nil
}

func isNodeConfigDone(node *corev1.Node) bool {
	current := node.Annotations[daemonconsts.CurrentMachineConfigAnnotationKey]
	desired := node.Annotations[daemonconsts.DesiredMachineConfigAnnotationKey]
	return isNodeDone(node) && current == desired
}

func isNodeDoneAtPool(mcp *mcfgv1.MachineConfigPool, node *corev1.Node) bool {
	current := node.Annotations[daemonconsts.CurrentMachineConfigAnnotationKey]
	return isNodeDone(node) && isNodeConfigDone(node) && isNodeImageDone(node) && current == mcp.Spec.Configuration.Name
}

func isNodeImageDone(node *corev1.Node) bool {
	current := node.Annotations[daemonconsts.CurrentImageAnnotationKey]
	desired := node.Annotations[daemonconsts.DesiredImageAnnotationKey]
	return isNodeDone(node) && current == desired
}

func isNodeDoneAtMosc(mosc *mcfgv1alpha1.MachineOSConfig, node *corev1.Node) bool {
	current := node.Annotations[daemonconsts.CurrentImageAnnotationKey]
	desired := node.Annotations[daemonconsts.DesiredImageAnnotationKey]
	return isNodeDone(node) && isNodeImageDone(node) && desired == mosc.Status.CurrentImagePullspec && desired != "" && current != ""
}

func isNodeDone(node *corev1.Node) bool {
	return node.Annotations[daemonconsts.MachineConfigDaemonStateAnnotationKey] == daemonconsts.MachineConfigDaemonStateDone
}
