package drain

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/openshift/api/machineconfiguration/v1alpha1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/scheme"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/helpers"
	"github.com/openshift/machine-config-operator/pkg/upgrademonitor"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubeErrs "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/apimachinery/pkg/util/wait"

	mcfginformersv1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	corev1 "k8s.io/api/core/v1"
	coreinformersv1 "k8s.io/client-go/informers/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	coreclientsetv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	corelisterv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/kubectl/pkg/drain"
)

type Config struct {
	// MaxRetries is the number of times a machineconfig pool will be retried before it is dropped out of the queue.
	// With the current rate-limiter in use (5ms*2^(MaxRetries-1)) the following numbers represent the times
	// a machineconfig pool is going to be requeued:
	//
	// 5ms, 10ms, 20ms, 40ms, 80ms, 160ms, 320ms, 640ms, 1.3s, 2.6s, 5.1s, 10.2s, 20.4s, 41s, 82s
	MaxRetries int

	UpdateDelay time.Duration

	// DrainTimeoutDuration specifies when we should error
	DrainTimeoutDuration time.Duration

	// DrainRequeueDelay specifies the delay before we retry the drain
	DrainRequeueDelay time.Duration

	// DrainRequeueFailingThreshold specifies the time after which we slow down retries
	DrainRequeueFailingThreshold time.Duration

	// DrainRequeueFailingDelay specifies the delay before we retry the drain,
	// if a node drain has been failing for > DrainRequeueFailingThreshold time
	DrainRequeueFailingDelay time.Duration

	// Drain helper timeout
	DrainHelperTimeout time.Duration

	// How long before backing off during the cordon uncordon operation
	CordonOrUncordonBackoff wait.Backoff

	WaitUntil time.Duration
}

func DefaultConfig() Config {
	return Config{
		MaxRetries:                   15,
		UpdateDelay:                  5 * time.Second,
		DrainTimeoutDuration:         1 * time.Hour,
		DrainRequeueDelay:            1 * time.Minute,
		DrainRequeueFailingThreshold: 10 * time.Minute,
		DrainRequeueFailingDelay:     5 * time.Minute,
		DrainHelperTimeout:           90 * time.Second,
		CordonOrUncordonBackoff: wait.Backoff{
			Steps:    5,
			Duration: 10 * time.Second,
			Factor:   2,
		},
		WaitUntil: time.Second,
	}
}

// Controller defines the node controller.
type Controller struct {
	client        mcfgclientset.Interface
	kubeClient    clientset.Interface
	eventRecorder record.EventRecorder

	syncHandler func(node string) error
	enqueueNode func(*corev1.Node)

	nodeLister       corelisterv1.NodeLister
	nodeListerSynced cache.InformerSynced

	mcpLister       mcfglistersv1.MachineConfigPoolLister
	mcpListerSynced cache.InformerSynced

	queue         workqueue.TypedRateLimitingInterface[string]
	ongoingDrains map[string]time.Time

	cfg Config

	featureGatesAccessor featuregates.FeatureGateAccess
}

// New returns a new node controller.
func New(
	cfg Config,
	nodeInformer coreinformersv1.NodeInformer,
	mcpInformer mcfginformersv1.MachineConfigPoolInformer,
	kubeClient clientset.Interface,
	mcfgClient mcfgclientset.Interface,
	fgAccessor featuregates.FeatureGateAccess,
) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&coreclientsetv1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	ctrl := &Controller{
		client:        mcfgClient,
		kubeClient:    kubeClient,
		eventRecorder: ctrlcommon.NamespacedEventRecorder(eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineconfigcontroller-nodecontroller"})),
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "machineconfigcontroller-draincontroller"}),
		cfg:                  cfg,
		featureGatesAccessor: fgAccessor,
	}

	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(newObj interface{}) { ctrl.handleNodeEvent(nil, newObj) },
		UpdateFunc: func(oldObj, newObj interface{}) { ctrl.handleNodeEvent(oldObj, newObj) },
	})

	ctrl.syncHandler = ctrl.syncNode
	ctrl.enqueueNode = ctrl.enqueueDefault

	ctrl.nodeLister = nodeInformer.Lister()
	ctrl.nodeListerSynced = nodeInformer.Informer().HasSynced

	ctrl.mcpLister = mcpInformer.Lister()
	ctrl.mcpListerSynced = mcpInformer.Informer().HasSynced

	return ctrl
}

// writer implements io.Writer interface as a pass-through for klog.
type writer struct {
	logFunc func(args ...interface{})
}

// Write passes string(p) into writer's logFunc and always returns len(p)
func (w writer) Write(p []byte) (n int, err error) {
	w.logFunc(string(p))
	return len(p), nil
}

// Run executes the drain controller.
func (ctrl *Controller) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()

	if !cache.WaitForCacheSync(stopCh, ctrl.nodeListerSynced, ctrl.mcpListerSynced) {
		return
	}

	ongoingDrains := make(map[string]time.Time)
	ctrl.ongoingDrains = ongoingDrains

	klog.Info("Starting MachineConfigController-DrainController")
	defer klog.Info("Shutting down MachineConfigController-DrainController")

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.worker, ctrl.cfg.WaitUntil, stopCh)

	}

	<-stopCh
}

// logNode emits a log message at informational level, prefixed with the node name in a consistent fashion.
func (ctrl *Controller) logNode(node *corev1.Node, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	klog.Infof("node %s: %s", node.Name, msg)
}

func (ctrl *Controller) handleNodeEvent(oldObj, newObj interface{}) {

	var oldNode, newNode *corev1.Node

	newNode = newObj.(*corev1.Node)
	klog.V(4).Infof("Updating Node (drain controller) %s", newNode.Name)

	if oldObj != nil {
		oldNode = oldObj.(*corev1.Node)
	} else {
		ctrl.enqueueNode(newNode)
		return
	}

	// If the desiredDrain annotation are identical between oldNode and newNode, no new action is required by the drain controller
	if oldNode.Annotations[daemonconsts.DesiredDrainerAnnotationKey] == newNode.Annotations[daemonconsts.DesiredDrainerAnnotationKey] {
		return
	}
	ctrl.enqueueNode(newNode)
}

// enqueueAfter will enqueue a pool after the provided amount of time.
func (ctrl *Controller) enqueueAfter(node *corev1.Node, after time.Duration) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(node)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %v", node, err))
		return
	}

	ctrl.queue.AddAfter(key, after)
}

// enqueueDefault calls a default enqueue function
func (ctrl *Controller) enqueueDefault(node *corev1.Node) {
	ctrl.enqueueAfter(node, ctrl.cfg.UpdateDelay)
}

// worker runs a worker thread that just dequeues items, processes them, and marks them done.
// It enforces that the syncHandler is never invoked concurrently with the same key.
func (ctrl *Controller) worker() {
	for ctrl.processNextWorkItem() {
	}
}

func (ctrl *Controller) processNextWorkItem() bool {
	key, quit := ctrl.queue.Get()
	if quit {
		return false
	}
	defer ctrl.queue.Done(key)

	err := ctrl.syncHandler(key)
	ctrl.handleErr(err, key)

	return true
}

func (ctrl *Controller) handleErr(err error, key string) {
	if err == nil {
		ctrl.queue.Forget(key)
		return
	}

	if ctrl.queue.NumRequeues(key) < ctrl.cfg.MaxRetries {
		klog.V(2).Infof("Error syncing node %v: %v", key, err)
		ctrl.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	klog.V(2).Infof("Dropping node %q out of the queue: %v", key, err)
	ctrl.queue.Forget(key)
	ctrl.queue.AddAfter(key, 1*time.Minute)
}

// TODO: look into idea of passing poolname into this function to apply to mcn
func (ctrl *Controller) syncNode(key string) error {
	klog.Errorf("in syncNode with key: %v", key)
	startTime := time.Now()
	klog.V(4).Infof("Started syncing node %q (%v)", key, startTime)
	defer func() {
		klog.V(4).Infof("Finished syncing node %q (%v)", key, time.Since(startTime))
	}()

	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}

	// First check if a drain request is needed for this node
	node, err := ctrl.nodeLister.Get(name)
	if apierrors.IsNotFound(err) {
		klog.V(2).Infof("node %v has been deleted", key)
		return nil
	}
	if err != nil {
		return err
	}

	desiredState := node.Annotations[daemonconsts.DesiredDrainerAnnotationKey]
	if desiredState == node.Annotations[daemonconsts.LastAppliedDrainerAnnotationKey] {
		klog.V(4).Infof("Node %v has the correct drain", key)
		return nil
	}

	drainer := &drain.Helper{
		Client:              ctrl.kubeClient,
		Force:               true,
		IgnoreAllDaemonSets: true,
		DeleteEmptyDirData:  true,
		GracePeriodSeconds:  -1,
		Timeout:             ctrl.cfg.DrainHelperTimeout,
		OnPodDeletedOrEvicted: func(pod *corev1.Pod, usingEviction bool) {
			verbStr := "Deleted"
			if usingEviction {
				verbStr = "Evicted"
			}
			ctrl.logNode(node, "%s pod %s/%s", verbStr, pod.Namespace, pod.Name)
		},
		Out:    writer{klog.Info},
		ErrOut: writer{klog.Error},
		Ctx:    context.TODO(),
	}

	// Get MCP associated with node
	primaryPool, err := helpers.GetPrimaryPoolForNode(ctrl.mcpLister, node)
	if err != nil {
		klog.Errorf("Error getting primary pool for node: %v", node.Name)
		return err
	}
	var pool string = primaryPool.Name

	desiredVerb := strings.Split(desiredState, "-")[0]
	switch desiredVerb {
	case daemonconsts.DrainerStateUncordon:
		ctrl.logNode(node, "uncordoning")
		// perform uncordon
		if err := ctrl.cordonOrUncordonNode(false, node, drainer); err != nil {
			nErr := upgrademonitor.GenerateAndApplyMachineConfigNodes(&upgrademonitor.Condition{State: v1alpha1.MachineConfigNodeUpdateComplete, Reason: string(v1alpha1.MachineConfigNodeUpdateUncordoned), Message: "Failed to Uncordon Node as part of completing upgrade phase"},
				&upgrademonitor.Condition{State: v1alpha1.MachineConfigNodeUpdateUncordoned, Reason: fmt.Sprintf("%s%s", string(v1alpha1.MachineConfigNodeUpdateComplete), string(v1alpha1.MachineConfigNodeUpdateUncordoned)), Message: fmt.Sprintf("Error: Failed to UnCordon node. Error is: %s The node is reporting Unschedulable = %t", err.Error(), node.Spec.Unschedulable)},
				metav1.ConditionUnknown,
				metav1.ConditionUnknown,
				node,
				ctrl.client,
				ctrl.featureGatesAccessor,
				pool,
			)
			if nErr != nil {
				klog.Errorf("Error making MCN for Uncordon failure: %v", err)
			}
			return fmt.Errorf("failed to uncordon node %v: %v", node.Name, err)

		}

		err = upgrademonitor.GenerateAndApplyMachineConfigNodes(&upgrademonitor.Condition{State: v1alpha1.MachineConfigNodeUpdateComplete, Reason: string(v1alpha1.MachineConfigNodeUpdateUncordoned), Message: "Uncordoned Node as part of completing upgrade phase"},
			&upgrademonitor.Condition{State: v1alpha1.MachineConfigNodeUpdateUncordoned, Reason: fmt.Sprintf("%s%s", string(v1alpha1.MachineConfigNodeUpdateComplete), string(v1alpha1.MachineConfigNodeUpdateUncordoned)), Message: fmt.Sprintf("UnCordoned node. The node is reporting Unschedulable = %t", node.Spec.Unschedulable)},
			metav1.ConditionTrue,
			metav1.ConditionTrue,
			node,
			ctrl.client,
			ctrl.featureGatesAccessor,
			pool,
		)
		if err != nil {
			klog.Errorf("Error making MCN for UnCordon success: %v", err)
		}
	case daemonconsts.DrainerStateDrain:

		if err := ctrl.drainNode(node, drainer); err != nil {
			// If we get an error from drainNode, that means the drain failed.
			// However, we want to requeue and try again. So we need to return nil
			// from here so that we can requeue.
			return nil
		}
	default:
		return fmt.Errorf("node %s: non-recognized drain verb detected %s", node.Name, desiredVerb)
	}

	ctrl.logNode(node, "operation successful; applying completion annotation")
	// write annotation for either cordon+drain or uncordon success
	annotations := map[string]string{
		daemonconsts.LastAppliedDrainerAnnotationKey: desiredState,
	}
	if err := ctrl.setNodeAnnotations(node.Name, annotations); err != nil {
		return fmt.Errorf("node %s: failed to set node uncordoned annotation: %v", node.Name, err)
	}
	ctrlcommon.UpdateStateMetric(ctrlcommon.MCCSubControllerState, "machine-config-controller-drain", desiredVerb, node.Name)
	return nil
}

func (ctrl *Controller) drainNode(node *corev1.Node, drainer *drain.Helper) error {
	// First check if we have an ongoing drain
	// This is currently stored in the object itself as a map but,
	// Practically during upgrades the control plane node this controller
	// pod is running on will also be terminated (the drainer will skip it).
	// This is a bit problematic in practice since we don't really have a previous state.
	// TODO (jerzhang) consider using a new CRD for coordination
	isOngoingDrain := false
	var duration time.Duration

	for k, v := range ctrl.ongoingDrains {
		if k != node.Name {
			continue
		}
		isOngoingDrain = true
		duration = time.Since(v)
		klog.Infof("Previous node drain found. Drain has been going on for %v hours", duration.Hours())
		if duration > ctrl.cfg.DrainTimeoutDuration {
			logMessage := fmt.Sprintf("node %s: drain exceeded timeout: %v. Will continue to retry.", node.Name, ctrl.cfg.DrainTimeoutDuration)
			klog.Error(logMessage)
			ctrl.eventRecorder.Eventf(node, corev1.EventTypeWarning, "DrainFailed", logMessage)
			ctrlcommon.MCCDrainErr.WithLabelValues(node.Name).Set(1)
		}
		break
	}

	// Get MCP associated with node
	primaryPool, err := helpers.GetPrimaryPoolForNode(ctrl.mcpLister, node)
	if err != nil {
		klog.Errorf("Error getting primary pool for node: %v", node.Name)
		return err
	}
	var pool string = primaryPool.Name

	if !isOngoingDrain {
		ctrl.logNode(node, "cordoning")
		// perform cordon
		if err := ctrl.cordonOrUncordonNode(true, node, drainer); err != nil {
			Nerr := upgrademonitor.GenerateAndApplyMachineConfigNodes(&upgrademonitor.Condition{State: v1alpha1.MachineConfigNodeUpdateExecuted, Reason: string(v1alpha1.MachineConfigNodeUpdateCordoned), Message: "Failed to Cordon Node as part of In progress update phase"},
				&upgrademonitor.Condition{State: v1alpha1.MachineConfigNodeUpdateCordoned, Reason: fmt.Sprintf("%s%s", string(v1alpha1.MachineConfigNodeUpdateExecuted), string(v1alpha1.MachineConfigNodeUpdateCordoned)), Message: fmt.Sprintf("Error: Failed to Cordon node. Error is %s, The node is reporting Unschedulable = %t", err.Error(), node.Spec.Unschedulable)},
				metav1.ConditionUnknown,
				metav1.ConditionUnknown,
				node,
				ctrl.client,
				ctrl.featureGatesAccessor,
				pool,
			)
			if Nerr != nil {
				klog.Errorf("Error making MCN for Cordon Failure: %v", Nerr)
			}
			return fmt.Errorf("node %s: failed to cordon: %v", node.Name, err)
		}
		ctrl.ongoingDrains[node.Name] = time.Now()
		err := upgrademonitor.GenerateAndApplyMachineConfigNodes(&upgrademonitor.Condition{State: v1alpha1.MachineConfigNodeUpdateExecuted, Reason: string(v1alpha1.MachineConfigNodeUpdateCordoned), Message: "Cordoned Node as part of update executed phase"},
			&upgrademonitor.Condition{State: v1alpha1.MachineConfigNodeUpdateCordoned, Reason: fmt.Sprintf("%s%s", string(v1alpha1.MachineConfigNodeUpdateExecuted), string(v1alpha1.MachineConfigNodeUpdateCordoned)), Message: fmt.Sprintf("Cordoned node. The node is reporting Unschedulable = %t", node.Spec.Unschedulable)},
			metav1.ConditionUnknown,
			metav1.ConditionTrue,
			node,
			ctrl.client,
			ctrl.featureGatesAccessor,
			pool,
		)
		if err != nil {
			klog.Errorf("Error making MCN for Cordon Success: %v", err)
		}
	}

	// Attempt drain
	ctrl.logNode(node, "initiating drain")
	err = upgrademonitor.GenerateAndApplyMachineConfigNodes(
		&upgrademonitor.Condition{State: v1alpha1.MachineConfigNodeUpdateExecuted, Reason: string(v1alpha1.MachineConfigNodeUpdateDrained), Message: "Draining Node as part of update executed phase"},
		&upgrademonitor.Condition{State: v1alpha1.MachineConfigNodeUpdateDrained, Reason: fmt.Sprintf("%s%s", string(v1alpha1.MachineConfigNodeUpdateExecuted), string(v1alpha1.MachineConfigNodeUpdateDrained)), Message: fmt.Sprintf("Draining node. The drain will not be complete until desired drainer %s matches current drainer %s", node.Annotations[daemonconsts.DesiredDrainerAnnotationKey], node.Annotations[daemonconsts.LastAppliedDrainerAnnotationKey])},
		metav1.ConditionUnknown,
		metav1.ConditionUnknown,
		node,
		ctrl.client,
		ctrl.featureGatesAccessor,
		pool,
	)
	if err != nil {
		klog.Errorf("Error making MCN for Drain beginning: %v", err)
	}
	if err := drain.RunNodeDrain(drainer, node.Name); err != nil {
		// To mimic our old daemon logic, we should probably have a more nuanced backoff.
		// However since the controller is processing all drains, it is less deterministic how soon the next drain will retry,
		// Anywhere between instant (if a node change happened) or up to hours (if there are many nodes competing for resources)
		// For now, let's say if a node has been trying for a set amount of time, we make it less prioritized.
		if duration > ctrl.cfg.DrainRequeueFailingThreshold {
			logMessage := fmt.Sprintf("Drain failed. Drain has been failing for more than %v minutes. Waiting %v minutes then retrying. "+
				"Error message from drain: %v", ctrl.cfg.DrainRequeueFailingThreshold.Minutes(), ctrl.cfg.DrainRequeueFailingDelay.Minutes(), err)
			ctrl.logNode(node, "%s", logMessage)
			ctrl.eventRecorder.Eventf(node, corev1.EventTypeWarning, "DrainThresholdExceeded", logMessage)
			ctrl.enqueueAfter(node, ctrl.cfg.DrainRequeueFailingDelay)
		} else {
			ctrl.logNode(node, "Drain failed. Waiting %v minute then retrying. Error message from drain: %v",
				ctrl.cfg.DrainRequeueDelay.Minutes(), err)
			ctrl.enqueueAfter(node, ctrl.cfg.DrainRequeueDelay)
		}

		nErr := upgrademonitor.GenerateAndApplyMachineConfigNodes(
			&upgrademonitor.Condition{State: v1alpha1.MachineConfigNodeUpdateExecuted, Reason: string(v1alpha1.MachineConfigNodeUpdateDrained), Message: "Node Drain has not succeeded"},
			&upgrademonitor.Condition{State: v1alpha1.MachineConfigNodeUpdateDrained, Reason: fmt.Sprintf("%s%s", string(v1alpha1.MachineConfigNodeUpdateExecuted), string(v1alpha1.MachineConfigNodeUpdateDrained)), Message: fmt.Sprintf("Error: Node Drain has not succeeded. Error is: %s The drain will not be complete until desired drainer %s matches current drainer %s", err.Error(), node.Annotations[daemonconsts.DesiredDrainerAnnotationKey], node.Annotations[daemonconsts.LastAppliedDrainerAnnotationKey])},
			metav1.ConditionUnknown,
			metav1.ConditionUnknown,
			node,
			ctrl.client,
			ctrl.featureGatesAccessor,
			pool,
		)
		if nErr != nil {
			klog.Errorf("Error making MCN for Drain failure: %v", nErr)
		}

		// Return early without deleting the ongoing drain.
		return err
	}
	err = upgrademonitor.GenerateAndApplyMachineConfigNodes(
		&upgrademonitor.Condition{State: v1alpha1.MachineConfigNodeUpdateExecuted, Reason: string(v1alpha1.MachineConfigNodeUpdateDrained), Message: "Drained Node as part of update executed phase"},
		&upgrademonitor.Condition{State: v1alpha1.MachineConfigNodeUpdateDrained, Reason: fmt.Sprintf("%s%s", string(v1alpha1.MachineConfigNodeUpdateExecuted), string(v1alpha1.MachineConfigNodeUpdateDrained)), Message: fmt.Sprintf("Drained node. The drain is complete as the desired drainer matches current drainer: %s", node.Annotations[daemonconsts.DesiredDrainerAnnotationKey])},
		metav1.ConditionUnknown,
		metav1.ConditionTrue,
		node,
		ctrl.client,
		ctrl.featureGatesAccessor,
		pool,
	)
	if err != nil {
		klog.Errorf("Error making MCN for Drain success: %v", err)
	}

	// Drain was successful. Delete the ongoing drain.
	delete(ctrl.ongoingDrains, node.Name)

	// Clear the MCCDrainErr, if any.
	if ctrlcommon.MCCDrainErr.DeleteLabelValues(node.Name) {
		klog.Infof("Cleaning up MCCDrain error for node(%s) as drain was completed", node.Name)
	}

	return nil
}

func (ctrl *Controller) setNodeAnnotations(nodeName string, annotations map[string]string) error {
	// TODO dedupe
	if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		n, err := ctrl.kubeClient.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		oldNode, err := json.Marshal(n)
		if err != nil {
			return err
		}

		nodeClone := n.DeepCopy()
		for k, v := range annotations {
			nodeClone.Annotations[k] = v
		}

		newNode, err := json.Marshal(nodeClone)
		if err != nil {
			return err
		}

		patchBytes, err := strategicpatch.CreateTwoWayMergePatch(oldNode, newNode, corev1.Node{})
		if err != nil {
			return fmt.Errorf("node %s: failed to create patch for: %v", nodeName, err)
		}

		_, err = ctrl.kubeClient.CoreV1().Nodes().Patch(context.TODO(), nodeName, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
		return err
	}); err != nil {
		// may be conflict if max retries were hit
		return fmt.Errorf("node %s: unable to update: %v", nodeName, err)
	}
	return nil
}

func (ctrl *Controller) cordonOrUncordonNode(desired bool, node *corev1.Node, drainer *drain.Helper) error {
	// Copied over from daemon
	// TODO this code has some sync issues
	verb := "cordon"
	if !desired {
		verb = "uncordon"
	}

	var lastErr error
	if err := wait.ExponentialBackoff(ctrl.cfg.CordonOrUncordonBackoff, func() (bool, error) {
		// Log has been added to ensure that MCO is correctly performing cordon/uncordon.
		// This should help us with debugging bugs like https://bugzilla.redhat.com/show_bug.cgi?id=2022387
		ctrl.logNode(node, "initiating %s (currently schedulable: %t)", verb, !node.Spec.Unschedulable)
		err := drain.RunCordonOrUncordon(drainer, node, desired)
		if err != nil {
			lastErr = err
			klog.Infof("%s failed with: %v, retrying", verb, err)
			return false, nil
		}

		// Re-fetch node so that we are not using cached information
		var updatedNode *corev1.Node
		if updatedNode, err = ctrl.kubeClient.CoreV1().Nodes().Get(context.TODO(), node.Name, metav1.GetOptions{}); err != nil {
			lastErr = err
			klog.Errorf("Failed to fetch node %v, retrying", err)
			return false, nil
		}

		if updatedNode.Spec.Unschedulable != desired {
			// See https://bugzilla.redhat.com/show_bug.cgi?id=2022387
			ctrl.logNode(node, "RunCordonOrUncordon() succeeded but node is still not in %s state, retrying", verb)
			return false, nil
		}

		ctrl.logNode(node, "%s succeeded (currently schedulable: %t)", verb, !updatedNode.Spec.Unschedulable)
		return true, nil
	}); err != nil {
		if wait.Interrupted(err) {
			errs := kubeErrs.NewAggregate([]error{err, lastErr})
			return fmt.Errorf("node %s: failed to %s (%d tries): %v", node.Name, verb, ctrl.cfg.CordonOrUncordonBackoff.Steps, errs)
		}
		return fmt.Errorf("node %s: failed to %s: %v", node.Name, verb, err)
	}

	return nil
}
