package drain

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/golang/glog"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	mcfgclientset "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/scheme"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubeErrs "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformersv1 "k8s.io/client-go/informers/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	coreclientsetv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	corelisterv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/kubectl/pkg/drain"
)

const (
	// maxRetries is the number of times a machineconfig pool will be retried before it is dropped out of the queue.
	// With the current rate-limiter in use (5ms*2^(maxRetries-1)) the following numbers represent the times
	// a machineconfig pool is going to be requeued:
	//
	// 5ms, 10ms, 20ms, 40ms, 80ms, 160ms, 320ms, 640ms, 1.3s, 2.6s, 5.1s, 10.2s, 20.4s, 41s, 82s
	maxRetries = 15

	updateDelay = 5 * time.Second

	// drainTimeoutDuration specifies when we should error
	drainTimeoutDuration = 1 * time.Hour

	// drainRequeueDelay specifies the delay before we retry the drain
	drainRequeueDelay = 1 * time.Minute

	// drainRequeueFailingThreshold specifies the time after which we slow down retries
	drainRequeueFailingThreshold = 10 * time.Minute
	// drainRequeueFailingDelay specifies the delay before we retry the drain,
	// if a node drain has been failing for > drainRequeueFailingThreshold time
	drainRequeueFailingDelay = 5 * time.Minute
)

// Controller defines the node controller.
type Controller struct {
	client        mcfgclientset.Interface
	kubeClient    clientset.Interface
	eventRecorder record.EventRecorder

	syncHandler func(node string) error
	enqueueNode func(*corev1.Node)

	nodeLister       corelisterv1.NodeLister
	nodeListerSynced cache.InformerSynced

	queue         workqueue.RateLimitingInterface
	ongoingDrains map[string]time.Time
}

// New returns a new node controller.
func New(
	nodeInformer coreinformersv1.NodeInformer,
	kubeClient clientset.Interface,
	mcfgClient mcfgclientset.Interface,
) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.Infof)
	eventBroadcaster.StartRecordingToSink(&coreclientsetv1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	ctrl := &Controller{
		client:        mcfgClient,
		kubeClient:    kubeClient,
		eventRecorder: eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineconfigcontroller-nodecontroller"}),
		queue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "machineconfigcontroller-nodecontroller"),
	}

	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.handleNodeEvent,
		UpdateFunc: func(oldObj, newObj interface{}) { ctrl.handleNodeEvent(newObj) },
	})

	ctrl.syncHandler = ctrl.syncNode
	ctrl.enqueueNode = ctrl.enqueueDefault

	ctrl.nodeLister = nodeInformer.Lister()
	ctrl.nodeListerSynced = nodeInformer.Informer().HasSynced

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

	if !cache.WaitForCacheSync(stopCh, ctrl.nodeListerSynced) {
		return
	}

	ongoingDrains := make(map[string]time.Time)
	ctrl.ongoingDrains = ongoingDrains

	glog.Info("Starting MachineConfigController-DrainController")
	defer glog.Info("Shutting down MachineConfigController-DrainController")

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.worker, time.Second, stopCh)
	}

	<-stopCh
}

// logNode emits a log message at informational level, prefixed with the node name in a consistent fashion.
func (ctrl *Controller) logNode(node *corev1.Node, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	glog.Infof("node %s: %s", node.Name, msg)
}

func (ctrl *Controller) handleNodeEvent(node interface{}) {
	n := node.(*corev1.Node)
	glog.V(4).Infof("Updating Node %s", n.Name)
	ctrl.enqueueNode(n)
}

func (ctrl *Controller) enqueue(node *corev1.Node) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(node)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", node, err))
		return
	}

	ctrl.queue.Add(key)
}

func (ctrl *Controller) enqueueRateLimited(node *corev1.Node) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(node)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", node, err))
		return
	}

	ctrl.queue.AddRateLimited(key)
}

// enqueueAfter will enqueue a pool after the provided amount of time.
func (ctrl *Controller) enqueueAfter(node *corev1.Node, after time.Duration) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(node)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", node, err))
		return
	}

	ctrl.queue.AddAfter(key, after)
}

// enqueueDefault calls a default enqueue function
func (ctrl *Controller) enqueueDefault(node *corev1.Node) {
	ctrl.enqueueAfter(node, updateDelay)
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

	err := ctrl.syncHandler(key.(string))
	ctrl.handleErr(err, key)

	return true
}

func (ctrl *Controller) handleErr(err error, key interface{}) {
	if err == nil {
		ctrl.queue.Forget(key)
		return
	}

	if ctrl.queue.NumRequeues(key) < maxRetries {
		glog.V(2).Infof("Error syncing node %v: %v", key, err)
		ctrl.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	glog.V(2).Infof("Dropping node %q out of the queue: %v", key, err)
	ctrl.queue.Forget(key)
	ctrl.queue.AddAfter(key, 1*time.Minute)
}

func (ctrl *Controller) syncNode(key string) error {
	startTime := time.Now()
	glog.V(4).Infof("Started syncing node %q (%v)", key, startTime)
	defer func() {
		glog.V(4).Infof("Finished syncing node %q (%v)", key, time.Since(startTime))
	}()

	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}

	// First check if a drain request is needed for this node
	node, err := ctrl.nodeLister.Get(name)
	if apierrors.IsNotFound(err) {
		glog.V(2).Infof("node %v has been deleted", key)
		return nil
	}
	if err != nil {
		return err
	}

	desiredState := node.Annotations[daemonconsts.DesiredDrainerAnnotationKey]
	if desiredState == node.Annotations[daemonconsts.LastAppliedDrainerAnnotationKey] {
		glog.V(4).Infof("Node %v has the correct drain", key)
		return nil
	}

	drainer := &drain.Helper{
		Client:              ctrl.kubeClient,
		Force:               true,
		IgnoreAllDaemonSets: true,
		DeleteEmptyDirData:  true,
		GracePeriodSeconds:  -1,
		Timeout:             90 * time.Second,
		OnPodDeletedOrEvicted: func(pod *corev1.Pod, usingEviction bool) {
			verbStr := "Deleted"
			if usingEviction {
				verbStr = "Evicted"
			}
			ctrl.logNode(node, "%s pod %s/%s", verbStr, pod.Namespace, pod.Name)
		},
		Out:    writer{glog.Info},
		ErrOut: writer{glog.Error},
		Ctx:    context.TODO(),
	}

	desiredVerb := strings.Split(desiredState, "-")[0]
	switch desiredVerb {
	case daemonconsts.DrainerStateUncordon:
		ctrl.logNode(node, "uncordoning")
		// perform uncordon
		if err := ctrl.cordonOrUncordonNode(false, node, drainer); err != nil {
			return fmt.Errorf("failed to uncordon node %v: %w", node.Name, err)
		}
	case daemonconsts.DrainerStateDrain:
		// First check if we have an ongoing drain
		// This is currently stored in the object itself as a map but,
		// Practically during upgrades the control plane node this controller
		// pod is running on will also be terminated (the drainer will skip it).
		// This is a bit problematic in practice since we don't really have a previous state.
		// TODO (jerzhang) consider using a new CRD for coordination

		ongoingDrain := false
		var duration time.Duration
		for k, v := range ctrl.ongoingDrains {
			if k != node.Name {
				continue
			}
			ongoingDrain = true
			duration = time.Now().Sub(v)
			glog.Infof("Previous node drain found. Drain has been going on for %v hours", duration.Hours())
			if duration > drainTimeoutDuration {
				// TODO right now the daemon will alert to match previous behaviour. Consider having controller do so instead.
				glog.Errorf("node %s: drain exceeded timeout: %v. Will continue to retry.", node.Name, drainTimeoutDuration)
			}
			break
		}
		if !ongoingDrain {
			ctrl.logNode(node, "cordoning")
			// perform cordon
			if err := ctrl.cordonOrUncordonNode(true, node, drainer); err != nil {
				return fmt.Errorf("node %s: failed to cordon: %w", node.Name, err)
			}
			ctrl.ongoingDrains[node.Name] = time.Now()
		}

		// Attempt drain
		ctrl.logNode(node, "initiating drain")
		if err := drain.RunNodeDrain(drainer, node.Name); err != nil {
			// To mimic our old daemon logic, we should probably have a more nuanced backoff.
			// However since the controller is processing all drains, it is less deterministic how soon the next drain will retry,
			// Anywhere between instant (if a node change happened) or up to hours (if there are many nodes competing for resources)
			// For now, let's say if a node has been trying for a set amount of time, we make it less prioritized.
			if duration > drainRequeueFailingThreshold {
				ctrl.logNode(node, "Drain failed. Drain has been failing for more than %v minutes. Waiting %v minutes then retrying. "+
					"Error message from drain: %v", drainRequeueFailingThreshold.Minutes(), drainRequeueFailingDelay.Minutes(), err)
				ctrl.enqueueAfter(node, drainRequeueFailingDelay)
			} else {
				ctrl.logNode(node, "Drain failed. Waiting %v minute then retrying. Error message from drain: %v",
					drainRequeueDelay.Minutes(), err)
				ctrl.enqueueAfter(node, drainRequeueDelay)
			}
			return nil
		}

		// Drain was successful. Delete the ongoing drain, then set the annotation
		delete(ctrl.ongoingDrains, node.Name)
	default:
		return fmt.Errorf("node %s: non-recognized drain verb detected %s", node.Name, desiredVerb)
	}

	ctrl.logNode(node, "operation successful; applying completion annotation")
	// write annotation for either cordon+drain or uncordon success
	annotations := map[string]string{
		daemonconsts.LastAppliedDrainerAnnotationKey: desiredState,
	}
	if err := ctrl.setNodeAnnotations(node.Name, annotations); err != nil {
		return fmt.Errorf("node %s: failed to set node uncordoned annotation: %w", node.Name, err)
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
			return fmt.Errorf("node %s: failed to create patch for: %w", nodeName, err)
		}

		_, err = ctrl.kubeClient.CoreV1().Nodes().Patch(context.TODO(), nodeName, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
		return err
	}); err != nil {
		// may be conflict if max retries were hit
		return fmt.Errorf("node %s: unable to update: %w", nodeName, err)
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

	backoff := wait.Backoff{
		Steps:    5,
		Duration: 10 * time.Second,
		Factor:   2,
	}
	var lastErr error
	if err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		// Log has been added to ensure that MCO is correctly performing cordon/uncordon.
		// This should help us with debugging bugs like https://bugzilla.redhat.com/show_bug.cgi?id=2022387
		ctrl.logNode(node, "initiating %s (currently schedulable: %t)", verb, !node.Spec.Unschedulable)
		err := drain.RunCordonOrUncordon(drainer, node, desired)
		if err != nil {
			lastErr = err
			glog.Infof("%s failed with: %v, retrying", verb, err)
			return false, nil
		}

		// Re-fetch node so that we are not using cached information
		var updatedNode *corev1.Node
		if updatedNode, err = ctrl.kubeClient.CoreV1().Nodes().Get(context.TODO(), node.Name, metav1.GetOptions{}); err != nil {
			lastErr = err
			glog.Errorf("Failed to fetch node %v, retrying", err)
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
		if err == wait.ErrWaitTimeout {
			errs := kubeErrs.NewAggregate([]error{err, lastErr})
			return fmt.Errorf("node %s: failed to %s (%d tries): %w", node.Name, verb, backoff.Steps, errs)
		}
		return fmt.Errorf("node %s: failed to %s: %w", node.Name, verb, err)
	}

	return nil
}
