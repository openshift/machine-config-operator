package rollout

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	errhelpers "github.com/openshift/machine-config-operator/devex/internal/pkg/errors"
	"github.com/openshift/machine-config-operator/devex/internal/pkg/utils"
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
var retryableErrThreshold = time.Minute

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

// Waits for the MachineConfigPool to complete. This does not consider
// individual node state and does not provide progress output.
func WaitForOnlyMachineConfigPoolToComplete(cs *framework.ClientSet, poolName string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Wait for the pool to begin updating.
	if err := waitForMachineConfigPoolToStart(ctx, cs, poolName); err != nil {
		return fmt.Errorf("pool %s did not start updating: %w", poolName, err)
	}

	return waitForMachineConfigPoolToComplete(ctx, cs, poolName)
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

	retryer := errhelpers.NewTimeRetryer(retryableErrThreshold)

	return wait.PollUntilContextCancel(ctx, pollInterval, true, func(ctx context.Context) (bool, error) {
		mcp, err := cs.MachineConfigPools().Get(ctx, poolName, metav1.GetOptions{})

		shouldContinue, err := handleQueryErr(err, retryer)
		if err != nil {
			return false, err
		}

		if !shouldContinue {
			return false, nil
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

	initial, err := cs.MachineConfigPools().Get(ctx, poolName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	retryer := errhelpers.NewTimeRetryer(retryableErrThreshold)

	return wait.PollUntilContextCancel(ctx, pollInterval, true, func(ctx context.Context) (bool, error) {
		mcp, err := cs.MachineConfigPools().Get(ctx, poolName, metav1.GetOptions{})

		shouldContinue, err := handleQueryErr(err, retryer)
		if err != nil {
			return false, err
		}

		if !shouldContinue {
			return false, nil
		}

		if hasMachineConfigPoolStatusChanged(initial, mcp) {
			logMCPChange(initial, mcp, start)
			initial = mcp
		}

		if apihelpers.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolUpdated) {
			klog.Infof("MachineConfigPool %s finished updating after %s", mcp.Name, time.Since(start))
			return true, nil
		}

		return false, validatePoolIsNotDegraded(mcp)
	})
}

// Determines if the API error is retryable and ensures we can only retry for a
// finite amount of time. If the error keeps occurring after the given
// threshold, it will return nil. Returns a boolean indicating whether to
// continue from the current point and an error in the event that it is either
// retryable or the threshold has been met.
func handleQueryErr(err error, retryer errhelpers.Retryer) (bool, error) {
	if err == nil {
		if !retryer.IsEmpty() {
			// If error is nil when it was previously not nil, clear the retryer and
			// continue.
			klog.Infof("The transient error is no longer occurring")
			retryer.Clear()
		}

		// If no error was encountered, continue as usual.
		return true, nil
	}

	// If the error is not retryable, stop here.
	if !isRetryableErr(err) {
		return false, err
	}

	// If the retryer is empty and we've encountered an error, log the error.
	if retryer.IsEmpty() {
		klog.Infof("An error has occurred, will retry for %s or until the error is no longer encountered", retryableErrThreshold)
		klog.Warning(err)
	}

	// This checks whether the retryer has reached the limit. If the retryer is
	// empty, it will populate itself before retrying.
	if retryer.IsReached() {
		// If no change after our threshold, we stop here.
		klog.Infof("Threshold %s reached", retryableErrThreshold)
		klog.Warning(err)
		return false, err
	}

	// At this point, we have a retryable error and know that we can try again.
	return false, nil
}

// Logs the detected MachineConfigPool change.
func logMCPChange(initial, current *mcfgv1.MachineConfigPool, start time.Time) {
	changes := []string{
		fmt.Sprintf("MachineConfigPool %s has changed:", initial.Name),
		fmt.Sprintf("Degraded: %d -> %d,", initial.Status.DegradedMachineCount, current.Status.DegradedMachineCount),
		fmt.Sprintf("Machines: %d -> %d,", initial.Status.MachineCount, current.Status.MachineCount),
		fmt.Sprintf("Ready: %d -> %d,", initial.Status.ReadyMachineCount, current.Status.ReadyMachineCount),
		fmt.Sprintf("Unavailable: %d -> %d,", initial.Status.UnavailableMachineCount, current.Status.UnavailableMachineCount),
		fmt.Sprintf("Updated: %d -> %d", initial.Status.UpdatedMachineCount, current.Status.UpdatedMachineCount),
		fmt.Sprintf("after %s", time.Since(start)),
	}

	klog.Info(strings.Join(changes, " "))
}

// Determines if the MachineConfigPool status has changed by examining the various machine counts.
func hasMachineConfigPoolStatusChanged(initial, current *mcfgv1.MachineConfigPool) bool {
	if initial.Status.DegradedMachineCount != current.Status.DegradedMachineCount {
		return true
	}

	if initial.Status.MachineCount != current.Status.MachineCount {
		return true
	}

	if initial.Status.ReadyMachineCount != current.Status.ReadyMachineCount {
		return true
	}

	if initial.Status.UnavailableMachineCount != current.Status.UnavailableMachineCount {
		return true
	}

	if initial.Status.UpdatedMachineCount != current.Status.UpdatedMachineCount {
		return true
	}

	return false
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

	retryer := errhelpers.NewTimeRetryer(retryableErrThreshold)

	return wait.PollUntilContextCancel(ctx, pollInterval, true, func(ctx context.Context) (bool, error) {
		nodes, err := cs.CoreV1Interface.Nodes().List(ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("node-role.kubernetes.io/%s", poolName),
		})

		shouldContinue, err := handleQueryErr(err, retryer)
		if err != nil {
			return false, err
		}

		if !shouldContinue {
			return false, nil
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

// Determines if an error is retryable. Currently, that means whether the
// context has been canceled or the deadline has been exceeded. In either of
// those scenarios, we cannot retry.
func isRetryableErr(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	return true
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

func isNodeDoneAtMosc(mosc *mcfgv1.MachineOSConfig, node *corev1.Node) bool {
	current := node.Annotations[daemonconsts.CurrentImageAnnotationKey]
	desired := node.Annotations[daemonconsts.DesiredImageAnnotationKey]
	return isNodeDone(node) && isNodeImageDone(node) && desired == string(mosc.Status.CurrentImagePullSpec) && desired != "" && current != ""
}

func isNodeDone(node *corev1.Node) bool {
	return node.Annotations[daemonconsts.MachineConfigDaemonStateAnnotationKey] == daemonconsts.MachineConfigDaemonStateDone
}
