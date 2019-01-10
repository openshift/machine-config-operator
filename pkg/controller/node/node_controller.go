package node

import (
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/golang/glog"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/daemon"
	mcfgclientset "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/scheme"
	mcfginformersv1 "github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions/machineconfiguration.openshift.io/v1"
	mcfglistersv1 "github.com/openshift/machine-config-operator/pkg/generated/listers/machineconfiguration.openshift.io/v1"
	"k8s.io/api/core/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	intstrutil "k8s.io/apimachinery/pkg/util/intstr"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformersv1 "k8s.io/client-go/informers/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	coreclientsetv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	corelisterv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	clientretry "k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"
)

const (
	// maxRetries is the number of times a machineconfig pool will be retried before it is dropped out of the queue.
	// With the current rate-limiter in use (5ms*2^(maxRetries-1)) the following numbers represent the times
	// a machineconfig pool is going to be requeued:
	//
	// 5ms, 10ms, 20ms, 40ms, 80ms, 160ms, 320ms, 640ms, 1.3s, 2.6s, 5.1s, 10.2s, 20.4s, 41s, 82s
	maxRetries = 15
)

// controllerKind contains the schema.GroupVersionKind for this controller type.
var controllerKind = mcfgv1.SchemeGroupVersion.WithKind("MachineConfigPool")

var nodeUpdateBackoff = wait.Backoff{
	Steps:    5,
	Duration: 100 * time.Millisecond,
	Jitter:   1.0,
}

// Controller defines the node controller.
type Controller struct {
	client        mcfgclientset.Interface
	kubeClient    clientset.Interface
	eventRecorder record.EventRecorder

	syncHandler              func(mcp string) error
	enqueueMachineConfigPool func(*mcfgv1.MachineConfigPool)

	mcpLister  mcfglistersv1.MachineConfigPoolLister
	nodeLister corelisterv1.NodeLister

	mcpListerSynced  cache.InformerSynced
	nodeListerSynced cache.InformerSynced

	queue workqueue.RateLimitingInterface
}

// New returns a new node controller.
func New(
	mcpInformer mcfginformersv1.MachineConfigPoolInformer,
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
		eventRecorder: eventBroadcaster.NewRecorder(scheme.Scheme, v1.EventSource{Component: "machineconfigcontroller-nodecontroller"}),
		queue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "machineconfigcontroller-nodecontroller"),
	}

	mcpInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addMachineConfigPool,
		UpdateFunc: ctrl.updateMachineConfigPool,
		DeleteFunc: ctrl.deleteMachineConfigPool,
	})
	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addNode,
		UpdateFunc: ctrl.updateNode,
		DeleteFunc: ctrl.deleteNode,
	})

	ctrl.syncHandler = ctrl.syncMachineConfigPool
	ctrl.enqueueMachineConfigPool = ctrl.enqueue

	ctrl.mcpLister = mcpInformer.Lister()
	ctrl.nodeLister = nodeInformer.Lister()
	ctrl.mcpListerSynced = mcpInformer.Informer().HasSynced
	ctrl.nodeListerSynced = nodeInformer.Informer().HasSynced

	return ctrl
}

// Run executes the render controller.
func (ctrl *Controller) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()

	glog.Info("Starting MachineConfigController-NodeController")
	defer glog.Info("Shutting down MachineConfigController-NodeController")

	if !cache.WaitForCacheSync(stopCh, ctrl.mcpListerSynced, ctrl.nodeListerSynced) {
		return
	}

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.worker, time.Second, stopCh)
	}

	<-stopCh
}

func (ctrl *Controller) addMachineConfigPool(obj interface{}) {
	pool := obj.(*mcfgv1.MachineConfigPool)
	glog.V(4).Infof("Adding MachineConfigPool %s", pool.Name)
	ctrl.enqueueMachineConfigPool(pool)

}
func (ctrl *Controller) updateMachineConfigPool(old, cur interface{}) {
	oldPool := old.(*mcfgv1.MachineConfigPool)
	curPool := cur.(*mcfgv1.MachineConfigPool)

	glog.V(4).Infof("Updating MachineConfigPool %s", oldPool.Name)
	ctrl.enqueueMachineConfigPool(curPool)
}
func (ctrl *Controller) deleteMachineConfigPool(obj interface{}) {
	pool, ok := obj.(*mcfgv1.MachineConfigPool)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Couldn't get object from tombstone %#v", obj))
			return
		}
		pool, ok = tombstone.Obj.(*mcfgv1.MachineConfigPool)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Tombstone contained object that is not a MachineConfigPool %#v", obj))
			return
		}
	}
	glog.V(4).Infof("Deleting MachineConfigPool %s", pool.Name)
	// TODO(abhinavdahiya): handle deletes.
}

func (ctrl *Controller) addNode(obj interface{}) {
	node := obj.(*corev1.Node)
	if node.DeletionTimestamp != nil {
		ctrl.deleteNode(node)
		return
	}

	pool, err := ctrl.getPoolForNode(node)
	if err != nil {
		glog.Errorf("error finding pools for node: %v", err)
		return
	}
	if pool == nil {
		return
	}
	glog.V(4).Infof("Node %s added", node.Name)
	ctrl.enqueueMachineConfigPool(pool)
}

func (ctrl *Controller) updateNode(old, cur interface{}) {
	oldNode := old.(*corev1.Node)
	curNode := cur.(*corev1.Node)

	if !nodeChanged(oldNode, curNode) {
		return
	}

	pool, err := ctrl.getPoolForNode(curNode)
	if err != nil {
		glog.Errorf("error finding pools for node: %v", err)
		return
	}
	if pool == nil {
		return
	}
	glog.V(4).Infof("Node %s updated", curNode.Name)
	ctrl.enqueueMachineConfigPool(pool)
}

func (ctrl *Controller) deleteNode(obj interface{}) {
	node, ok := obj.(*corev1.Node)

	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Couldn't get object from tombstone %#v", obj))
			return
		}
		node, ok = tombstone.Obj.(*corev1.Node)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Tombstone contained object that is not a Node %#v", obj))
			return
		}
	}

	pool, err := ctrl.getPoolForNode(node)
	if err != nil {
		glog.Errorf("error finding pools for node: %v", err)
		return
	}
	if pool == nil {
		return
	}
	glog.V(4).Infof("Node %s delete", node.Name)
	ctrl.enqueueMachineConfigPool(pool)
}

func nodeChanged(old, cur *corev1.Node) bool {
	if old.Annotations == nil && cur.Annotations != nil ||
		old.Annotations != nil && cur.Annotations == nil {
		return true
	}

	if old.Annotations == nil && cur.Annotations == nil {
		return false
	}

	if old.Annotations[daemon.CurrentMachineConfigAnnotationKey] != cur.Annotations[daemon.CurrentMachineConfigAnnotationKey] ||
		old.Annotations[daemon.DesiredMachineConfigAnnotationKey] != cur.Annotations[daemon.DesiredMachineConfigAnnotationKey] {
		return true
	}

	return false
}

func (ctrl *Controller) getPoolForNode(node *corev1.Node) (*mcfgv1.MachineConfigPool, error) {
	pl, err := ctrl.mcpLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}

	var pools []*mcfgv1.MachineConfigPool
	for _, p := range pl {
		selector, err := metav1.LabelSelectorAsSelector(p.Spec.MachineSelector)
		if err != nil {
			return nil, fmt.Errorf("invalid label selector: %v", err)
		}

		// If a pool with a nil or empty selector creeps in, it should match nothing, not everything.
		if selector.Empty() || !selector.Matches(labels.Set(node.Labels)) {
			continue
		}

		pools = append(pools, p)
	}

	if len(pools) == 0 {
		// This is not an error, as there might be nodes in cluster that are not managed by machineconfigpool.
		return nil, nil
	}
	if len(pools) > 1 {
		return nil, fmt.Errorf("node %s belongs to more than one MachineConfigPool, cannot proceed with this Node", node.Name)
	}
	return pools[0], nil
}

func (ctrl *Controller) enqueue(pool *mcfgv1.MachineConfigPool) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pool)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", pool, err))
		return
	}

	ctrl.queue.Add(key)
}

func (ctrl *Controller) enqueueRateLimited(pool *mcfgv1.MachineConfigPool) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pool)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", pool, err))
		return
	}

	ctrl.queue.AddRateLimited(key)
}

// enqueueAfter will enqueue a pool after the provided amount of time.
func (ctrl *Controller) enqueueAfter(pool *mcfgv1.MachineConfig, after time.Duration) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pool)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", pool, err))
		return
	}

	ctrl.queue.AddAfter(key, after)
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
		glog.V(2).Infof("Error syncing machineconfigpool %v: %v", key, err)
		ctrl.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	glog.V(2).Infof("Dropping machineconfigpool %q out of the queue: %v", key, err)
	ctrl.queue.Forget(key)
	ctrl.queue.AddAfter(key, 1*time.Minute)
}

// syncMachineConfigPool will sync the machineconfig pool with the given key.
// This function is not meant to be invoked concurrently with the same key.
func (ctrl *Controller) syncMachineConfigPool(key string) error {
	startTime := time.Now()
	glog.V(4).Infof("Started syncing machineconfigpool %q (%v)", key, startTime)
	defer func() {
		glog.V(4).Infof("Finished syncing machineconfigpool %q (%v)", key, time.Since(startTime))
	}()

	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	machineconfigpool, err := ctrl.mcpLister.Get(name)
	if errors.IsNotFound(err) {
		glog.V(2).Infof("MachineConfigPool %v has been deleted", key)
		return nil
	}
	if err != nil {
		return err
	}

	if machineconfigpool.Status.Configuration.Name == "" {
		return fmt.Errorf("Empty Current MachineConfig")
	}

	// Deep-copy otherwise we are mutating our cache.
	// TODO: Deep-copy only when needed.
	pool := machineconfigpool.DeepCopy()
	everything := metav1.LabelSelector{}
	if reflect.DeepEqual(pool.Spec.MachineSelector, &everything) {
		ctrl.eventRecorder.Eventf(pool, v1.EventTypeWarning, "SelectingAll", "This machineconfigpool is selecting all machines. A non-empty selector is require.")
		return nil
	}

	if pool.DeletionTimestamp != nil {
		return ctrl.syncStatusOnly(pool)
	}

	if pool.Spec.Paused {
		return ctrl.syncStatusOnly(pool)
	}
	selector, err := metav1.LabelSelectorAsSelector(pool.Spec.MachineSelector)
	if err != nil {
		return err
	}
	nodes, err := ctrl.nodeLister.List(selector)
	if err != nil {
		return err
	}

	progress, err := makeProgress(pool, nodes)
	if err != nil {
		return err
	}

	if progress == 0 {
		return ctrl.syncStatusOnly(pool)
	}

	candidates := getCandidateMachines(pool, nodes, progress)
	for _, node := range candidates {
		if err := ctrl.setDesiredMachineConfigAnnotation(node.Name, pool.Status.Configuration.Name); err != nil {
			return err
		}
	}
	return ctrl.syncStatusOnly(pool)
}

func (ctrl *Controller) setDesiredMachineConfigAnnotation(nodeName, currentConfig string) error {
	return clientretry.RetryOnConflict(nodeUpdateBackoff, func() error {
		oldNode, err := ctrl.kubeClient.CoreV1().Nodes().Get(nodeName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		oldData, err := json.Marshal(oldNode)
		if err != nil {
			return err
		}

		newNode := oldNode.DeepCopy()
		if newNode.Annotations == nil {
			newNode.Annotations = map[string]string{}
		}
		newNode.Annotations[daemon.DesiredMachineConfigAnnotationKey] = currentConfig
		newData, err := json.Marshal(newNode)
		if err != nil {
			return err
		}

		patchBytes, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, v1.Node{})
		if err != nil {
			return fmt.Errorf("failed to create patch for node %q: %v", nodeName, err)
		}
		_, err = ctrl.kubeClient.CoreV1().Nodes().Patch(nodeName, types.StrategicMergePatchType, patchBytes)
		return err
	})
}

func makeProgress(pool *mcfgv1.MachineConfigPool, nodes []*corev1.Node) (int32, error) {
	maxunavail, err := maxUnavailable(pool, nodes)
	if err != nil {
		return 0, err
	}
	unavail := int32(len(getUnavailableMachines(pool.Status.Configuration.Name, nodes)))
	progress := int32(0)
	if unavail < maxunavail {
		progress = maxunavail - unavail
	}

	return progress, nil
}

func getCandidateMachines(pool *mcfgv1.MachineConfigPool, nodes []*corev1.Node, progress int32) []*corev1.Node {
	acted := getReadyMachines(pool.Status.Configuration.Name, nodes)
	acted = append(acted, getUnavailableMachines(pool.Status.Configuration.Name, nodes)...)

	actedMap := map[string]struct{}{}
	for _, node := range acted {
		actedMap[node.Name] = struct{}{}
	}

	var candidates []*corev1.Node
	for idx, node := range nodes {
		if _, ok := actedMap[node.Name]; !ok {
			candidates = append(candidates, nodes[idx])
		}
	}

	if int32(len(candidates)) <= progress {
		return candidates
	}
	return candidates[:progress]
}

func maxUnavailable(pool *mcfgv1.MachineConfigPool, nodes []*corev1.Node) (int32, error) {
	intOrPercent := intstrutil.FromInt(1)
	if pool.Spec.MaxUnavailable != nil {
		intOrPercent = *pool.Spec.MaxUnavailable
	}

	maxunavail, err := intstrutil.GetValueFromIntOrPercent(&intOrPercent, len(nodes), false)
	if err != nil {
		return 0, err
	}
	if maxunavail == 0 {
		maxunavail = 1
	}
	return int32(maxunavail), nil
}
