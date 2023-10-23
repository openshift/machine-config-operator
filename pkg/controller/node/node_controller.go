package node

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	cligoinformersv1 "github.com/openshift/client-go/config/informers/externalversions/config/v1"
	cligolistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/scheme"
	mcfginformersv1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	"github.com/openshift/machine-config-operator/internal"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/constants"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/controller/state"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	kubeErrs "k8s.io/apimachinery/pkg/util/errors"
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
	"k8s.io/klog/v2"
)

const (
	// WorkerLabel defines the label associated with worker node.
	WorkerLabel = "node-role.kubernetes.io/worker"

	// maxRetries is the number of times a machineconfig pool will be retried before it is dropped out of the queue.
	// With the current rate-limiter in use (5ms*2^(maxRetries-1)) the following numbers represent the times
	// a machineconfig pool is going to be requeued:
	//
	// 5ms, 10ms, 20ms, 40ms, 80ms, 160ms, 320ms, 640ms, 1.3s, 2.6s, 5.1s, 10.2s, 20.4s, 41s, 82s
	maxRetries = 15

	// defaultUpdateDelay is a pause to deal with churn in MachineConfigs; see
	// https://github.com/openshift/machine-config-operator/issues/301
	defaultUpdateDelay = 5 * time.Second

	// osLabel is used to identify which type of OS the node has
	osLabel = "kubernetes.io/os"

	// zoneLabel is for https://kubernetes.io/docs/setup/best-practices/multiple-zones/
	zoneLabel = "topology.kubernetes.io/zone"

	// schedulerCRName that we're interested in watching.
	schedulerCRName = "cluster"
)

// Controller defines the node controller.
type Controller struct {
	client               mcfgclientset.Interface
	kubeClient           clientset.Interface
	eventRecorder        record.EventRecorder
	healthEventsRecorder record.EventRecorder

	syncHandler              func(mcp string) error
	enqueueMachineConfigPool func(*mcfgv1.MachineConfigPool)

	ccLister   mcfglistersv1.ControllerConfigLister
	mcLister   mcfglistersv1.MachineConfigLister
	mcpLister  mcfglistersv1.MachineConfigPoolLister
	nodeLister corelisterv1.NodeLister
	podLister  corelisterv1.PodLister

	ccListerSynced   cache.InformerSynced
	mcListerSynced   cache.InformerSynced
	mcpListerSynced  cache.InformerSynced
	nodeListerSynced cache.InformerSynced

	schedulerList         cligolistersv1.SchedulerLister
	schedulerListerSynced cache.InformerSynced

	queue workqueue.RateLimitingInterface

	// updateDelay is a pause to deal with churn in MachineConfigs; see
	// https://github.com/openshift/machine-config-operator/issues/301
	updateDelay time.Duration
}

func New(
	ccInformer mcfginformersv1.ControllerConfigInformer,
	mcInformer mcfginformersv1.MachineConfigInformer,
	mcpInformer mcfginformersv1.MachineConfigPoolInformer,
	nodeInformer coreinformersv1.NodeInformer,
	podInformer coreinformersv1.PodInformer,
	schedulerInformer cligoinformersv1.SchedulerInformer,
	kubeClient clientset.Interface,
	mcfgClient mcfgclientset.Interface,
) *Controller {
	return newController(
		ccInformer,
		mcInformer,
		mcpInformer,
		nodeInformer,
		podInformer,
		schedulerInformer,
		kubeClient,
		mcfgClient,
		defaultUpdateDelay,
	)
}

func NewWithCustomUpdateDelay(
	ccInformer mcfginformersv1.ControllerConfigInformer,
	mcInformer mcfginformersv1.MachineConfigInformer,
	mcpInformer mcfginformersv1.MachineConfigPoolInformer,
	nodeInformer coreinformersv1.NodeInformer,
	podInformer coreinformersv1.PodInformer,
	schedulerInformer cligoinformersv1.SchedulerInformer,
	kubeClient clientset.Interface,
	mcfgClient mcfgclientset.Interface,
	updateDelay time.Duration,
) *Controller {
	return newController(
		ccInformer,
		mcInformer,
		mcpInformer,
		nodeInformer,
		podInformer,
		schedulerInformer,
		kubeClient,
		mcfgClient,
		updateDelay,
	)
}

// new returns a new node controller.
func newController(
	ccInformer mcfginformersv1.ControllerConfigInformer,
	mcInformer mcfginformersv1.MachineConfigInformer,
	mcpInformer mcfginformersv1.MachineConfigPoolInformer,
	nodeInformer coreinformersv1.NodeInformer,
	podInformer coreinformersv1.PodInformer,
	schedulerInformer cligoinformersv1.SchedulerInformer,
	kubeClient clientset.Interface,
	mcfgClient mcfgclientset.Interface,
	updateDelay time.Duration,
) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&coreclientsetv1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	ctrl := &Controller{
		client:        mcfgClient,
		kubeClient:    kubeClient,
		eventRecorder: ctrlcommon.NamespacedEventRecorder(eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineconfigcontroller-nodecontroller"})),
		queue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "machineconfigcontroller-nodecontroller"),
		updateDelay:   updateDelay,
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
	schedulerInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.checkMasterNodesOnAdd,
		UpdateFunc: ctrl.checkMasterNodesOnUpdate,
		DeleteFunc: ctrl.checkMasterNodesOnDelete,
	})
	ctrl.syncHandler = ctrl.syncMachineConfigPool
	ctrl.enqueueMachineConfigPool = ctrl.enqueueDefault

	ctrl.ccLister = ccInformer.Lister()
	ctrl.mcLister = mcInformer.Lister()
	ctrl.mcpLister = mcpInformer.Lister()
	ctrl.nodeLister = nodeInformer.Lister()
	ctrl.podLister = podInformer.Lister()
	ctrl.ccListerSynced = ccInformer.Informer().HasSynced
	ctrl.mcListerSynced = mcInformer.Informer().HasSynced
	ctrl.mcpListerSynced = mcpInformer.Informer().HasSynced
	ctrl.nodeListerSynced = nodeInformer.Informer().HasSynced

	ctrl.schedulerList = schedulerInformer.Lister()
	ctrl.schedulerListerSynced = schedulerInformer.Informer().HasSynced

	return ctrl
}

// Run executes the render controller.
func (ctrl *Controller) Run(workers int, stopCh <-chan struct{}, healthEvents record.EventRecorder) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()
	ctrl.healthEventsRecorder = healthEvents
	if !cache.WaitForCacheSync(stopCh, ctrl.ccListerSynced, ctrl.mcListerSynced, ctrl.mcpListerSynced, ctrl.nodeListerSynced, ctrl.schedulerListerSynced) {
		return
	}

	klog.Info("Starting MachineConfigController-NodeController")
	defer klog.Info("Shutting down MachineConfigController-NodeController")

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.worker, time.Second, stopCh)
	}

	<-stopCh
}

func (ctrl *Controller) getCurrentMasters() ([]*corev1.Node, error) {
	nodeList, err := ctrl.nodeLister.List(labels.SelectorFromSet(labels.Set{ctrlcommon.MasterLabel: ""}))
	if err != nil {
		return nil, fmt.Errorf("error while listing master nodes %w", err)
	}
	return nodeList, nil
}

// checkMasterNodesOnAdd makes the master nodes schedulable/unschedulable whenever scheduler config CR with name
// cluster is created
func (ctrl *Controller) checkMasterNodesOnAdd(_ interface{}) {
	ctrl.reconcileMasters()
}

// checkMasterNodesOnDelete makes the master nodes schedulable/unschedulable whenever scheduler config CR with name
// cluster is created
func (ctrl *Controller) checkMasterNodesOnDelete(obj interface{}) {
	scheduler := obj.(*configv1.Scheduler)
	if scheduler.Name != schedulerCRName {
		klog.V(4).Infof("We don't care about CRs other than cluster created for scheduler config")
		return
	}
	currentMasters, err := ctrl.getCurrentMasters()
	if err != nil {
		err = fmt.Errorf("reconciling to make master nodes schedulable/unschedulable failed: %w", err)
		klog.Error(err)
		return
	}
	// On deletion make all masters unschedulable to restore default behaviour
	errs := ctrl.makeMastersUnSchedulable(currentMasters)
	if len(errs) > 0 {
		err = v1helpers.NewMultiLineAggregate(errs)
		err = fmt.Errorf("reconciling to make nodes schedulable/unschedulable failed: %w", err)
		klog.Error(err)
		return
	}
	return
}

// checkMasterNodesonUpdate makes the master nodes schedulable/unschedulable whenever scheduler
// config CR with name cluster is updated
func (ctrl *Controller) checkMasterNodesOnUpdate(old, cur interface{}) {
	oldScheduler := old.(*configv1.Scheduler)
	curScheduler := cur.(*configv1.Scheduler)

	if oldScheduler.Name != schedulerCRName || curScheduler.Name != schedulerCRName {
		klog.V(4).Infof("We don't care about CRs other than cluster created for scheduler config")
		return
	}

	if reflect.DeepEqual(oldScheduler.Spec, curScheduler.Spec) {
		klog.V(4).Info("Scheduler config did not change")
		return
	}

	ctrl.reconcileMasters()
}

// makeMastersUnSchedulable makes all the masters in the cluster unschedulable
func (ctrl *Controller) makeMastersUnSchedulable(currentMasters []*corev1.Node) []error {
	var errs []error
	for _, node := range currentMasters {
		if err := ctrl.makeMasterNodeUnSchedulable(node); err != nil {
			errs = append(errs, fmt.Errorf("failed making node %v schedulable with error %w", node.Name, err))
		}
	}
	return errs

}

// makeMasterNodeUnSchedulable makes master node unschedulable by removing worker label and adding `NoSchedule`
// master taint to the master node
func (ctrl *Controller) makeMasterNodeUnSchedulable(node *corev1.Node) error {
	_, err := internal.UpdateNodeRetry(ctrl.kubeClient.CoreV1().Nodes(), ctrl.nodeLister, node.Name, func(node *corev1.Node) {
		// Remove worker label
		newLabels := node.Labels
		if _, hasWorkerLabel := newLabels[WorkerLabel]; hasWorkerLabel {
			delete(newLabels, WorkerLabel)
		}
		node.Labels = newLabels
		// Add master taint
		hasMasterTaint := false
		for _, taint := range node.Spec.Taints {
			if taint.Key == ctrlcommon.MasterLabel && taint.Effect == corev1.TaintEffectNoSchedule {
				hasMasterTaint = true
			}
		}
		if !hasMasterTaint {
			newTaints := node.Spec.Taints
			masterUnSchedulableTaint := corev1.Taint{Key: ctrlcommon.MasterLabel, Effect: corev1.TaintEffectNoSchedule}
			newTaints = append(newTaints, masterUnSchedulableTaint)
			node.Spec.Taints = newTaints
		}
	})
	if err != nil {
		return err
	}
	return nil
}

// makeMasterNodeSchedulable makes master node schedulable by removing NoSchedule master taint and
// adding worker label
func (ctrl *Controller) makeMasterNodeSchedulable(node *corev1.Node) error {
	_, err := internal.UpdateNodeRetry(ctrl.kubeClient.CoreV1().Nodes(), ctrl.nodeLister, node.Name, func(node *corev1.Node) {
		// Add worker label
		newLabels := node.Labels
		if _, hasWorkerLabel := newLabels[WorkerLabel]; !hasWorkerLabel {
			newLabels[WorkerLabel] = ""
		}
		node.Labels = newLabels
		// Remove master taint
		newTaints := []corev1.Taint{}
		for _, t := range node.Spec.Taints {
			if t.Key == ctrlcommon.MasterLabel && t.Effect == corev1.TaintEffectNoSchedule {
				continue
			}
			newTaints = append(newTaints, t)
		}
		node.Spec.Taints = newTaints
	})
	if err != nil {
		return err
	}
	return nil
}

func (ctrl *Controller) addMachineConfigPool(obj interface{}) {
	pool := obj.(*mcfgv1.MachineConfigPool)
	klog.V(4).Infof("Adding MachineConfigPool %s", pool.Name)
	ctrl.enqueueMachineConfigPool(pool)
}

func (ctrl *Controller) updateMachineConfigPool(old, cur interface{}) {
	oldPool := old.(*mcfgv1.MachineConfigPool)
	curPool := cur.(*mcfgv1.MachineConfigPool)

	klog.V(4).Infof("Updating MachineConfigPool %s", oldPool.Name)
	ctrl.enqueueMachineConfigPool(curPool)
}

func (ctrl *Controller) deleteMachineConfigPool(obj interface{}) {
	pool, ok := obj.(*mcfgv1.MachineConfigPool)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("couldn't get object from tombstone %#v", obj))
			return
		}
		pool, ok = tombstone.Obj.(*mcfgv1.MachineConfigPool)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not a MachineConfigPool %#v", obj))
			return
		}
	}
	klog.V(4).Infof("Deleting MachineConfigPool %s", pool.Name)
	// TODO(abhinavdahiya): handle deletes.
}

// Determine if masters are currently configured as schedulable
func (ctrl *Controller) getMastersSchedulable() (bool, error) {
	schedulerList, err := ctrl.schedulerList.List(labels.SelectorFromSet(nil))
	if err != nil {
		return false, fmt.Errorf("error while listing scheduler config %w", err)
	}
	for _, sched := range schedulerList {
		if sched.Name == schedulerCRName {
			return sched.Spec.MastersSchedulable, nil
		}
	}
	// If the scheduler list is empty, or there is no match. Return an error so it is a no-op.
	return false, fmt.Errorf("cluster scheduler couldn't be found")
}

// Determine if a given Node is a master
func (ctrl *Controller) isMaster(node *corev1.Node) bool {
	_, master := node.ObjectMeta.Labels[ctrlcommon.MasterLabel]
	return master
}

// isWindows checks if given node is a Windows node or a Linux node
func isWindows(node *corev1.Node) bool {
	windowsOsValue := "windows"
	if value, ok := node.ObjectMeta.Labels[osLabel]; ok {
		if value == windowsOsValue {
			return true
		}
		return false
	}
	// All the nodes should have a OS label populated by kubelet, if not just to maintain
	// backwards compatibility, we can returning true here.
	return false
}

// Given a master Node, ensure it reflects the current mastersSchedulable setting
func (ctrl *Controller) reconcileMaster(node *corev1.Node) {
	mastersSchedulable, err := ctrl.getMastersSchedulable()
	if err != nil {
		err = fmt.Errorf("getting scheduler config failed: %w", err)
		klog.Error(err)
		return
	}
	if mastersSchedulable {
		err = ctrl.makeMasterNodeSchedulable(node)
		if err != nil {
			err = fmt.Errorf("failed making master Node schedulable: %w", err)
			klog.Error(err)
			return
		}
	} else if !mastersSchedulable {
		err = ctrl.makeMasterNodeUnSchedulable(node)
		if err != nil {
			err = fmt.Errorf("failed making master Node unschedulable: %w", err)
			klog.Error(err)
			return
		}
	}
}

// Get a list of current masters and apply scheduler config to them
// TODO: Taint reconciliation should happen elsewhere, in a generic taint/label reconciler
func (ctrl *Controller) reconcileMasters() {
	currentMasters, err := ctrl.getCurrentMasters()
	if err != nil {
		err = fmt.Errorf("reconciling to make master nodes schedulable/unschedulable failed: %w", err)
		klog.Error(err)
		return
	}
	for _, node := range currentMasters {
		ctrl.reconcileMaster(node)
	}
}

func (ctrl *Controller) addNode(obj interface{}) {
	node := obj.(*corev1.Node)
	if node.DeletionTimestamp != nil {
		ctrl.deleteNode(node)
		return
	}

	if ctrl.isMaster(node) {
		ctrl.reconcileMaster(node)
	}

	pools, err := ctrl.getPoolsForNode(node)
	if err != nil {
		klog.Errorf("error finding pools for node %s: %v", node.Name, err)
		return
	}
	if pools == nil {
		return
	}
	klog.V(4).Infof("Node %s added", node.Name)
	for _, pool := range pools {
		ctrl.enqueueMachineConfigPool(pool)
	}
}

func (ctrl *Controller) logPool(pool *mcfgv1.MachineConfigPool, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	klog.Infof("Pool %s: %s", pool.Name, msg)
}

func (ctrl *Controller) logPoolNode(pool *mcfgv1.MachineConfigPool, node *corev1.Node, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	zone, zok := node.Labels[zoneLabel]
	zonemsg := ""
	if zok {
		zonemsg = fmt.Sprintf("[zone=%s]", zone)
	}
	klog.Infof("Pool %s%s: node %s: %s", pool.Name, zonemsg, node.Name, msg)
}

func (ctrl *Controller) updateNode(old, cur interface{}) {
	oldNode := old.(*corev1.Node)
	curNode := cur.(*corev1.Node)

	if !isNodeManaged(curNode) {
		return
	}

	if ctrl.isMaster(curNode) {
		ctrl.reconcileMaster(curNode)
	}

	pool, err := ctrl.getPrimaryPoolForNode(curNode)
	if err != nil {
		klog.Errorf("error finding pool for node: %v", err)
		return
	}
	if pool == nil {
		return
	}
	klog.V(4).Infof("Node %s updated", curNode.Name)

	// Let's be verbose when a node changes pool
	oldPool, err := ctrl.getPrimaryPoolForNode(oldNode)
	if err == nil && oldPool.Name != pool.Name {
		ctrl.logPoolNode(pool, curNode, "changed from pool %s", oldPool.Name)
		// Let's also make sure the old pool node counts/status get updated
		ctrl.enqueueMachineConfigPool(oldPool)
	}

	var changed bool
	oldReadyErr := checkNodeReady(oldNode)
	newReadyErr := checkNodeReady(curNode)

	oldReady := getErrorString(oldReadyErr)
	newReady := getErrorString(newReadyErr)

	if oldReady != newReady {
		changed = true
		if newReadyErr != nil {
			ctrl.logPoolNode(pool, curNode, "Reporting unready: %v", newReadyErr)
		} else {
			ctrl.logPoolNode(pool, curNode, "Reporting ready")
		}
	}

	// Specifically log when a node has completed an update so the MCC logs are a useful central aggregate of state changes
	if oldNode.Annotations[daemonconsts.CurrentMachineConfigAnnotationKey] != oldNode.Annotations[daemonconsts.DesiredMachineConfigAnnotationKey] &&
		isNodeDone(curNode) {
		ctrl.logPoolNode(pool, curNode, "Completed update to %s", curNode.Annotations[daemonconsts.DesiredMachineConfigAnnotationKey])
		changed = true
	} else {
		annos := []string{
			daemonconsts.CurrentMachineConfigAnnotationKey,
			daemonconsts.DesiredMachineConfigAnnotationKey,
			daemonconsts.MachineConfigDaemonStateAnnotationKey,
			daemonconsts.MachineConfigDaemonReasonAnnotationKey,
			daemonconsts.CurrentImageAnnotationKey,
			daemonconsts.DesiredImageAnnotationKey,
		}

		for _, anno := range annos {
			if !hasNodeAnnotationChanged(oldNode, curNode, anno) {
				continue
			}

			newValue, ok := curNode.Annotations[anno]

			var changedMsg string
			var controlPlaneChangedMsg string
			if ok {
				changedMsg = fmt.Sprintf("changed annotation %s = %s", anno, newValue)
				controlPlaneChangedMsg = fmt.Sprintf("Node %s now has %s=%s", curNode.Name, anno, newValue)
			} else {
				changedMsg = fmt.Sprintf("lost annotation %s", anno)
				controlPlaneChangedMsg = fmt.Sprintf("Node %s no longer has %s", curNode.Name, anno)
			}
			ctrl.logPoolNode(pool, curNode, changedMsg)
			changed = true
			// For the control plane, emit events for these since they're important
			if pool.Name == ctrlcommon.MachineConfigPoolMaster {
				ctrl.eventRecorder.Eventf(pool, corev1.EventTypeNormal, "AnnotationChange", controlPlaneChangedMsg)
			}
		}
		if !reflect.DeepEqual(oldNode.Labels, curNode.Labels) {
			ctrl.logPoolNode(pool, curNode, "changed labels")
			changed = true
		}
		if !reflect.DeepEqual(oldNode.Spec.Taints, curNode.Spec.Taints) {
			ctrl.logPoolNode(pool, curNode, "changed taints")
			changed = true
		}
	}

	if !changed {
		return
	}

	pools, err := ctrl.getPoolsForNode(curNode)
	if err != nil {
		klog.Errorf("error finding pools for node: %v", err)
		return
	}
	if pools == nil {
		return
	}
	for _, pool := range pools {
		ctrl.enqueueMachineConfigPool(pool)
	}
}

func hasNodeAnnotationChanged(oldNode, curNode *corev1.Node, key string) bool {
	oldValue, oldOK := oldNode.Annotations[key]
	newValue, newOK := curNode.Annotations[key]

	// If we had an annotation, but no longer have it, we've changed.
	if oldOK != newOK {
		return true
	}

	// If the old value does not match the current value, we've changed.
	return oldValue != newValue
}

func (ctrl *Controller) deleteNode(obj interface{}) {
	node, ok := obj.(*corev1.Node)

	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("couldn't get object from tombstone %#v", obj))
			return
		}
		node, ok = tombstone.Obj.(*corev1.Node)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not a Node %#v", obj))
			return
		}
	}

	pools, err := ctrl.getPoolsForNode(node)
	if err != nil {
		klog.Errorf("error finding pools for node: %v", err)
		return
	}
	if pools == nil {
		return
	}

	// Clear any associated MCCDrainErr, if any.
	if ctrlcommon.MCCDrainErr.DeleteLabelValues(node.Name) {
		klog.Infof("Cleaning up MCCDrain error for node(%s) as it is being deleted", node.Name)
	}

	klog.V(4).Infof("Node %s delete", node.Name)
	for _, pool := range pools {
		ctrl.enqueueMachineConfigPool(pool)
	}
}

// getPoolsForNode chooses the MachineConfigPools that should be used for a given node.
// It disambiguates in the case where e.g. a node has both master/worker roles applied,
// and where a custom role may be used. It returns a slice of all the pools the node belongs to.
// It also ignores the Windows nodes.
func (ctrl *Controller) getPoolsForNode(node *corev1.Node) ([]*mcfgv1.MachineConfigPool, error) {
	if isWindows(node) {
		// This is not an error, is this a Windows Node and it won't be managed by MCO. We're explicitly logging
		// here at a high level to disambiguate this from other pools = nil  scenario
		klog.V(4).Infof("Node %v is a windows node so won't be managed by MCO", node.Name)
		return nil, nil
	}
	pl, err := ctrl.mcpLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}

	var pools []*mcfgv1.MachineConfigPool
	for _, p := range pl {
		selector, err := metav1.LabelSelectorAsSelector(p.Spec.NodeSelector)
		if err != nil {
			return nil, fmt.Errorf("invalid label selector: %w", err)
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

	var master, worker *mcfgv1.MachineConfigPool
	var custom []*mcfgv1.MachineConfigPool
	for _, pool := range pools {
		if pool.Name == ctrlcommon.MachineConfigPoolMaster {
			master = pool
		} else if pool.Name == ctrlcommon.MachineConfigPoolWorker {
			worker = pool
		} else {
			custom = append(custom, pool)
		}
	}

	if len(custom) > 1 {
		return nil, fmt.Errorf("node %s belongs to %d custom roles, cannot proceed with this Node", node.Name, len(custom))
	} else if len(custom) == 1 {
		pls := []*mcfgv1.MachineConfigPool{}
		if master != nil {
			// if we have a custom pool and master, defer to master and return.
			klog.Infof("Found master node that matches selector for custom pool %v, defaulting to master. This node will not have any custom role configuration as a result. Please review the node to make sure this is intended", custom[0].Name)
			ctrlcommon.MCCPoolAlert.WithLabelValues(node.Name).Set(1)
			pls = append(pls, master)
		} else {
			ctrlcommon.MCCPoolAlert.WithLabelValues(node.Name).Set(0)
			pls = append(pls, custom[0])
		}
		if worker != nil {
			pls = append(pls, worker)
		}
		// this allows us to have master, worker, infra but be in the master pool.
		// or if !worker and !master then we just use the custom pool.
		return pls, nil
	} else if master != nil {
		// In the case where a node is both master/worker, have it live under
		// the master pool. This occurs in CodeReadyContainers and general
		// "single node" deployments, which one may want to do for testing bare
		// metal, etc.
		ctrlcommon.MCCPoolAlert.WithLabelValues(node.Name).Set(0)
		return []*mcfgv1.MachineConfigPool{master}, nil
	}
	// Otherwise, it's a worker with no custom roles.
	ctrlcommon.MCCPoolAlert.WithLabelValues(node.Name).Set(0)
	return []*mcfgv1.MachineConfigPool{worker}, nil
}

// getPrimaryPoolForNode uses getPoolsForNode and returns the first one which is the one the node targets
func (ctrl *Controller) getPrimaryPoolForNode(node *corev1.Node) (*mcfgv1.MachineConfigPool, error) {
	pools, err := ctrl.getPoolsForNode(node)
	if err != nil {
		return nil, err
	}
	if pools == nil {
		return nil, nil
	}
	return pools[0], nil
}

func (ctrl *Controller) enqueue(pool *mcfgv1.MachineConfigPool) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pool)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", pool, err))
		return
	}

	ctrl.queue.Add(key)
}

func (ctrl *Controller) enqueueRateLimited(pool *mcfgv1.MachineConfigPool) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pool)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", pool, err))
		return
	}

	ctrl.queue.AddRateLimited(key)
}

// enqueueAfter will enqueue a pool after the provided amount of time.
func (ctrl *Controller) enqueueAfter(pool *mcfgv1.MachineConfigPool, after time.Duration) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pool)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", pool, err))
		return
	}

	ctrl.queue.AddAfter(key, after)
}

// enqueueDefault calls a default enqueue function
func (ctrl *Controller) enqueueDefault(pool *mcfgv1.MachineConfigPool) {
	ctrl.enqueueAfter(pool, ctrl.updateDelay)
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
		klog.V(2).Infof("Error syncing machineconfigpool %v: %v", key, err)
		ctrl.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	klog.V(2).Infof("Dropping machineconfigpool %q out of the queue: %v", key, err)
	ctrl.queue.Forget(key)
	ctrl.queue.AddAfter(key, 1*time.Minute)
}

// Determines if we should continue processing a layered MachineConfigPool or
// if we should requeue. This targets the following scenarios:
// 1. If a pool is opted into layering, we should wait for the initial OS image
// build to be ready before we attempt to roll it out to the nodes.
// 2. If a MachineConfig changes, we should wait for the OS image build to be
// ready so we can update both the nodes' desired MachineConfig and desired
// image annotations simultaneously.
func (ctrl *Controller) canLayeredPoolContinue(pool *mcfgv1.MachineConfigPool) (string, bool, error) {
	lps := ctrlcommon.NewLayeredPoolState(pool)

	hasImage := lps.HasOSImage()
	pullspec := lps.GetOSImage()

	if !hasImage {
		return fmt.Sprintf("Image annotation %s is not set", ctrlcommon.ExperimentalNewestLayeredImageEquivalentConfigAnnotationKey), false, nil
	}

	switch {
	// If the build is successful and we have the image pullspec, we can proceed
	// with rolling out the new OS image.
	case lps.IsBuildSuccess() && hasImage:
		msg := fmt.Sprintf("Image built successfully, pullspec: %s", pullspec)
		return msg, true, nil
	case lps.IsBuildPending():
		return "Image build pending", false, nil
	case lps.IsBuilding():
		return "Image build in progress", false, nil
	case lps.IsBuildFailure():
		return "Image build failed", false, fmt.Errorf("image build for MachineConfigPool %s failed", pool.Name)
	default:
		return "Image is not ready yet", false, nil
	}
}

// syncMachineConfigPool will sync the machineconfig pool with the given key.
// This function is not meant to be invoked concurrently with the same key.
func (ctrl *Controller) syncMachineConfigPool(key string) error {
	startTime := time.Now()
	klog.V(4).Infof("Started syncing machineconfigpool %q (%v)", key, startTime)
	defer func() {
		klog.V(4).Infof("Finished syncing machineconfigpool %q (%v)", key, time.Since(startTime))
	}()

	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	machineconfigpool, err := ctrl.mcpLister.Get(name)
	if errors.IsNotFound(err) {
		klog.V(2).Infof("MachineConfigPool %v has been deleted", key)
		return nil
	}
	if err != nil {
		return err
	}

	// make this a function
	// update all nodes for pool with pool prog.
	nodes, err := ctrl.getNodesForPool(machineconfigpool)
	if err != nil {
		return err
	}
	annos := make(map[string]string)
	annos["ms"] = "ControllerState"
	annos["state"] = "StateControllerSyncController"
	annos["ObjectKind"] = string(mcfgv1.MCP)
	annos["ObjectName"] = machineconfigpool.Name

	s, err := state.StateControllerPod(ctrl.kubeClient)
	if err != nil {
		klog.Error(err)
	}
	ctrl.EmitHealthEvent(s, annos, corev1.EventTypeNormal, "GotMachineConfigPool", fmt.Sprintf("Got Machine Config Pool %s", key))
	if machineconfigpool.Spec.Configuration.Name == "" {
		// Previously we spammed the logs about empty pools.
		// Let's just pause for a bit here to let the renderer
		// initialize them.
		klog.Infof("Pool %s is unconfigured, pausing %v for renderer to initialize", name, ctrl.updateDelay)
		time.Sleep(ctrl.updateDelay)
		return nil
	}

	// Deep-copy otherwise we are mutating our cache.
	// TODO: Deep-copy only when needed.
	pool := machineconfigpool.DeepCopy()
	everything := metav1.LabelSelector{}

	if reflect.DeepEqual(pool.Spec.NodeSelector, &everything) {
		ctrl.eventRecorder.Eventf(pool, corev1.EventTypeWarning, "SelectingAll", "This machineconfigpool is selecting all nodes. A non-empty selector is required.")
		return nil
	}

	if pool.DeletionTimestamp != nil {
		return ctrl.syncStatusOnly(pool)
	}

	if pool.Spec.Paused {
		if apihelpers.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolUpdating) {
			klog.Infof("Pool %s is paused and will not update.", pool.Name)
		}
		return ctrl.syncStatusOnly(pool)
	}

	if ctrlcommon.IsLayeredPool(pool) {
		reason, canApplyUpdates, err := ctrl.canLayeredPoolContinue(pool)
		if err != nil {
			klog.Infof("Layered pool %s encountered an error: %s", pool.Name, err)
			return err
		}

		if !canApplyUpdates {
			// The MachineConfigPool is not ready to continue, so requeue.
			klog.Infof("Requeueing layered pool %s: %s", pool.Name, reason)
			return ctrl.syncStatusOnly(pool)
		}

		klog.V(4).Infof("Continuing updates for layered pool %s", pool.Name)
	} else {
		klog.V(4).Infof("Pool %s is not layered", pool.Name)
	}

	ctrl.EmitHealthEvent(s, annos, corev1.EventTypeNormal, "SyncingPoolStatus", fmt.Sprintf("Syncing MachineConfigPool Status %s", key))
	nodes, err = ctrl.getNodesForPool(pool)
	if err != nil {
		if syncErr := ctrl.syncStatusOnly(pool); syncErr != nil {
			errs := kubeErrs.NewAggregate([]error{syncErr, err})
			return fmt.Errorf("error getting nodes for pool %q, sync error: %w", pool.Name, errs)
		}
		return err
	}

	maxunavail, err := maxUnavailable(pool, nodes)
	if err != nil {
		if syncErr := ctrl.syncStatusOnly(pool); syncErr != nil {
			errs := kubeErrs.NewAggregate([]error{syncErr, err})
			return fmt.Errorf("error getting max unavailable count for pool %q, sync error: %w", pool.Name, errs)
		}
		return err
	}

	if err := ctrl.setClusterConfigAnnotation(nodes); err != nil {
		return fmt.Errorf("error setting clusterConfig Annotation for node in pool %q, error: %w", pool.Name, err)
	}
	// Taint all the nodes in the node pool, irrespective of their upgrade status.
	ctx := context.TODO()
	for _, node := range nodes {
		// All the nodes that need to be upgraded should have `NodeUpdateInProgressTaint` so that they're less likely
		// to be chosen during the scheduling cycle.
		hasInProgressTaint := checkIfNodeHasInProgressTaint(node)

		lns := ctrlcommon.NewLayeredNodeState(node)

		if lns.IsDesiredEqualToPool(pool) {
			if hasInProgressTaint {
				if err := ctrl.removeUpdateInProgressTaint(ctx, node.Name); err != nil {
					err = fmt.Errorf("failed removing %s taint for node %s: %w", constants.NodeUpdateInProgressTaint.Key, node.Name, err)
					klog.Error(err)
				}
			}
		} else {
			if !hasInProgressTaint {
				if err := ctrl.setUpdateInProgressTaint(ctx, node.Name); err != nil {
					err = fmt.Errorf("failed applying %s taint for node %s: %w", constants.NodeUpdateInProgressTaint.Key, node.Name, err)
					klog.Error(err)
				}
			}
		}
	}
	ctrl.EmitHealthEvent(s, annos, corev1.EventTypeNormal, "SyncingPoolMachines", fmt.Sprintf("Syncing MachineConfigPool's Machine with the proper Config Annotations %s", key))
	candidates, capacity := getAllCandidateMachines(pool, nodes, maxunavail)
	if len(candidates) > 0 {
		zones := make(map[string]bool)
		for _, candidate := range candidates {
			zone, ok := candidate.Labels[zoneLabel]
			if ok {
				zones[zone] = true
			}
		}
		ctrl.logPool(pool, "%d candidate nodes in %d zones for update, capacity: %d", len(candidates), len(zones), capacity)
		if err := ctrl.updateCandidateMachines(pool, candidates, capacity); err != nil {
			if syncErr := ctrl.syncStatusOnly(pool); syncErr != nil {
				errs := kubeErrs.NewAggregate([]error{syncErr, err})
				return fmt.Errorf("error setting annotations for pool %q, sync error: %w", pool.Name, errs)
			}
			return err
		}
	}
	return ctrl.syncStatusOnly(pool)
}

func (ctrl *Controller) EmitHealthEvent(pod *corev1.Pod, annos map[string]string, eventType, reason, message string) {
	if ctrl.healthEventsRecorder == nil {
		return
	}
	if pod == nil {
		healthPod, err := state.StateControllerPod(ctrl.kubeClient)
		if err != nil {
			klog.Errorf("Could not get state controller pod yet %w", err)
			return
		} else {
			pod = healthPod
		}
	}
	ctrl.healthEventsRecorder.AnnotatedEventf(pod, annos, eventType, reason, message)
}

// checkIfNodeHasInProgressTaint checks if the given node has in progress taint
func checkIfNodeHasInProgressTaint(node *corev1.Node) bool {
	for _, taint := range node.Spec.Taints {
		if taint.MatchTaint(constants.NodeUpdateInProgressTaint) {
			return true
		}
	}
	return false
}

func (ctrl *Controller) getNodesForPool(pool *mcfgv1.MachineConfigPool) ([]*corev1.Node, error) {
	selector, err := metav1.LabelSelectorAsSelector(pool.Spec.NodeSelector)
	if err != nil {
		return nil, fmt.Errorf("invalid label selector: %w", err)
	}

	initialNodes, err := ctrl.nodeLister.List(selector)
	if err != nil {
		return nil, err
	}

	nodes := []*corev1.Node{}
	for _, n := range initialNodes {
		p, err := ctrl.getPrimaryPoolForNode(n)
		if err != nil {
			klog.Warningf("can't get pool for node %q: %v", n.Name, err)
			continue
		}
		if p == nil {
			continue
		}
		if p.Name != pool.Name {
			continue
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

// setClusterConfigAnnotation reads cluster configs set into controllerConfig
// and add/updates required annotation to node such as ControlPlaneTopology
// from infrastructure object.
func (ctrl *Controller) setClusterConfigAnnotation(nodes []*corev1.Node) error {
	cc, err := ctrl.ccLister.Get(ctrlcommon.ControllerConfigName)
	if err != nil {
		return err
	}

	for _, node := range nodes {
		if node.Annotations[daemonconsts.ClusterControlPlaneTopologyAnnotationKey] != string(cc.Spec.Infra.Status.ControlPlaneTopology) {
			oldAnn := node.Annotations[daemonconsts.ClusterControlPlaneTopologyAnnotationKey]
			_, err := internal.UpdateNodeRetry(ctrl.kubeClient.CoreV1().Nodes(), ctrl.nodeLister, node.Name, func(node *corev1.Node) {
				node.Annotations[daemonconsts.ClusterControlPlaneTopologyAnnotationKey] = string(cc.Spec.Infra.Status.ControlPlaneTopology)
			})
			if err != nil {
				return err
			}
			klog.Infof("Updated controlPlaneTopology annotation of node %s from %s to %s", node.Name, oldAnn, node.Annotations[daemonconsts.ClusterControlPlaneTopologyAnnotationKey])
		}
	}
	return nil
}

func (ctrl *Controller) updateCandidateNode(nodeName string, pool *mcfgv1.MachineConfigPool) error {
	return clientretry.RetryOnConflict(constants.NodeUpdateBackoff, func() error {
		oldNode, err := ctrl.kubeClient.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		oldData, err := json.Marshal(oldNode)
		if err != nil {
			return err
		}

		lns := ctrlcommon.NewLayeredNodeState(oldNode)
		if lns.IsDesiredEqualToPool(pool) {
			// If the node's desired annotations match the pool, return directly without updating the node.
			klog.Infof("no update is needed")
			return nil
		}

		// Set the desired state to match the pool.
		lns.SetDesiredStateFromPool(pool)

		newData, err := json.Marshal(lns.Node())
		if err != nil {
			return err
		}

		patchBytes, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, corev1.Node{})
		if err != nil {
			return fmt.Errorf("failed to create patch for node %q: %w", nodeName, err)
		}
		_, err = ctrl.kubeClient.CoreV1().Nodes().Patch(context.TODO(), nodeName, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
		return err
	})
}

// getAllCandidateMachines returns all possible nodes which can be updated to the target config, along with a maximum
// capacity.  It is the reponsibility of the caller to choose a subset of the nodes given the capacity.
func getAllCandidateMachines(pool *mcfgv1.MachineConfigPool, nodesInPool []*corev1.Node, maxUnavailable int) ([]*corev1.Node, uint) {
	unavail := getUnavailableMachines(nodesInPool, pool)
	if len(unavail) >= maxUnavailable {
		klog.Infof("No nodes available for updates")
		return nil, 0
	}
	capacity := maxUnavailable - len(unavail)
	failingThisConfig := 0
	// We only look at nodes which aren't already targeting our desired config
	var nodes []*corev1.Node
	for _, node := range nodesInPool {
		lns := ctrlcommon.NewLayeredNodeState(node)
		if lns.IsDesiredEqualToPool(pool) {
			if isNodeMCDFailing(node) {
				failingThisConfig++
			}
			continue
		}
		nodes = append(nodes, node)
	}
	// Nodes which are failing to target this config also count against
	// availability - it might be a transient issue, and if the issue
	// clears we don't want multiple to update at once.
	if failingThisConfig >= capacity {
		return nil, 0
	}
	capacity -= failingThisConfig
	return nodes, uint(capacity)
}

// getCandidateMachines returns the maximum subset of nodes which can be updated to the target config given availability constraints.
func getCandidateMachines(pool *mcfgv1.MachineConfigPool, nodesInPool []*corev1.Node, maxUnavailable int) []*corev1.Node {
	nodes, capacity := getAllCandidateMachines(pool, nodesInPool, maxUnavailable)
	if uint(len(nodes)) < capacity {
		return nodes
	}
	return nodes[:capacity]
}

// getOperatorPodNodeName fetches the name of the current node running the machine-config-operator pod
func (ctrl *Controller) getOperatorNodeName() (string, error) {
	// Create a selector object with  a filter on the machine-config-operator pod
	parsedSelector, err := labels.Parse("k8s-app=machine-config-operator")
	if err != nil {
		klog.Infof("Fetching machine-config-operator pod selector object failed: %v", err)
		return "", err
	}
	// Query pod list with selector, this slice should only have 1 element
	podList, err := ctrl.podLister.List(parsedSelector)
	if err != nil || len(podList) != 1 {
		klog.Infof("Fetching machine-config-operator pod object failed: %v", err)
		return "", err
	}
	return podList[0].Spec.NodeName, err
}

// filterControlPlaneCandidateNodes adjusts the candidates and capacity specifically
// for the control plane, e.g. based on which node is running the operator node at the time.
// nolint:unparam
func (ctrl *Controller) filterControlPlaneCandidateNodes(pool *mcfgv1.MachineConfigPool, candidates []*corev1.Node, capacity uint) ([]*corev1.Node, uint, error) {
	if len(candidates) <= 1 {
		return candidates, capacity, nil
	}
	operatorNodeName, err := ctrl.getOperatorNodeName()
	if err != nil {
		klog.Warningf("Failed to find current operator node (continuing anyways): %v", err)
	}
	var newCandidates []*corev1.Node
	for _, node := range candidates {
		if node.Name == operatorNodeName {
			ctrl.eventRecorder.Eventf(pool, corev1.EventTypeNormal, "DeferringOperatorNodeUpdate", "Deferring update of machine config operator node %s", node.Name)
			klog.Infof("Deferring update of machine config operator node: %s", node.Name)
			continue
		}
		newCandidates = append(newCandidates, node)
	}
	return newCandidates, capacity, nil
}

// updateCandidateMachines sets the desiredConfig annotation the candidate machines
func (ctrl *Controller) updateCandidateMachines(pool *mcfgv1.MachineConfigPool, candidates []*corev1.Node, capacity uint) error {
	if pool.Name == ctrlcommon.MachineConfigPoolMaster {
		var err error
		candidates, capacity, err = ctrl.filterControlPlaneCandidateNodes(pool, candidates, capacity)
		if err != nil {
			return err
		}
		// In practice right now these counts will be 1 but let's stay general to support 5 etcd nodes in the future
		ctrl.logPool(pool, "filtered to %d candidate nodes for update, capacity: %d", len(candidates), capacity)
	}
	if capacity < uint(len(candidates)) {
		// when list is longer than maxUnavailable, rollout nodes in zone order, zones without zone label
		// are done last from oldest to youngest. this reduces likelihood of randomly picking nodes
		// across multiple zones that run the same types of pods resulting in an outage in HA clusters
		candidates = sortNodeList(candidates)

		candidates = candidates[:capacity]
	}

	return ctrl.setDesiredAnnotations(pool, candidates)
}

func (ctrl *Controller) setDesiredAnnotations(pool *mcfgv1.MachineConfigPool, candidates []*corev1.Node) error {
	eventName := "SetDesiredConfig"

	if ctrlcommon.IsLayeredPool(pool) {
		eventName = "SetDesiredConfigAndOSImage"

		klog.Infof("Continuing to sync layered MachineConfigPool %s", pool.Name)
	}

	for _, node := range candidates {
		ctrl.logPool(pool, "Setting node %s target to %s", node.Name, getPoolUpdateLine(pool))
		if err := ctrl.updateCandidateNode(node.Name, pool); err != nil {
			return fmt.Errorf("setting desired %s for node %s: %w", getPoolUpdateLine(pool), node.Name, err)
		}
	}

	if len(candidates) == 1 {
		candidate := candidates[0]
		ctrl.eventRecorder.Eventf(pool, corev1.EventTypeNormal, eventName, "Targeted node %s to %s", candidate.Name, getPoolUpdateLine(pool))
	} else {
		ctrl.eventRecorder.Eventf(pool, corev1.EventTypeNormal, eventName, "Set target for %d nodes to %s", len(candidates), getPoolUpdateLine(pool))
	}

	return nil
}

// sortNodeList sorts the list of candidate nodes by label topology.kubernetes.io/zone
// nodes without label are at end of list and sorted by age (oldest to youngest)
func sortNodeList(nodes []*corev1.Node) []*corev1.Node {
	sort.Slice(nodes, func(i, j int) bool {
		iZone, iOk := nodes[i].Labels[zoneLabel]
		jZone, jOk := nodes[j].Labels[zoneLabel]
		// if both nodes have zone label, sort by zone, push nodes without label to end of list
		if iOk && jOk {
			if iZone == jZone {
				// if nodes have same labels sortby creationTime oldest to newest
				return nodes[i].GetObjectMeta().GetCreationTimestamp().Time.Before(nodes[j].GetObjectMeta().GetCreationTimestamp().Time)
			}
			return iZone < jZone
		} else if jOk {
			return false
		} else if !iOk && !jOk {
			// if nodes have no labels sortby creationTime oldest to newest
			return nodes[i].GetObjectMeta().GetCreationTimestamp().Time.Before(nodes[j].GetObjectMeta().GetCreationTimestamp().Time)
		}

		return true
	})
	return nodes
}

// setUpdateInProgressTaint applies in progress taint to all the nodes that are to be updated.
// The taint on the individual node is removed by MCC once the update of the node is complete.
// This is to ensure that the updated nodes are being preferred to non-updated nodes there by
// reducing the number of reschedules.
func (ctrl *Controller) setUpdateInProgressTaint(ctx context.Context, nodeName string) error {
	return clientretry.RetryOnConflict(constants.NodeUpdateBackoff, func() error {
		oldNode, err := ctrl.kubeClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		oldData, err := json.Marshal(oldNode)
		if err != nil {
			return err
		}

		newNode := oldNode.DeepCopy()
		if newNode.Spec.Taints == nil {
			newNode.Spec.Taints = []corev1.Taint{}
		}

		newNode.Spec.Taints = append(newNode.Spec.Taints, *constants.NodeUpdateInProgressTaint)
		newData, err := json.Marshal(newNode)
		if err != nil {
			return err
		}

		patchBytes, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, corev1.Node{})
		if err != nil {
			return fmt.Errorf("failed to create patch for node %q: %v", nodeName, err)
		}
		_, err = ctrl.kubeClient.CoreV1().Nodes().Patch(ctx, nodeName, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
		return err
	})
}

// removeUpdateInProgressTaint removes the update in progress taint for given node.
func (ctrl *Controller) removeUpdateInProgressTaint(ctx context.Context, nodeName string) error {
	return clientretry.RetryOnConflict(constants.NodeUpdateBackoff, func() error {
		oldNode, err := ctrl.kubeClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		oldData, err := json.Marshal(oldNode)
		if err != nil {
			return err
		}

		newNode := oldNode.DeepCopy()

		// New taints to be copied.
		var taintsAfterUpgrade []corev1.Taint
		for _, taint := range newNode.Spec.Taints {
			if taint.MatchTaint(constants.NodeUpdateInProgressTaint) {
				continue
			}
			taintsAfterUpgrade = append(taintsAfterUpgrade, taint)
		}

		// Remove the NodeUpdateInProgressTaint.
		newNode.Spec.Taints = taintsAfterUpgrade
		newData, err := json.Marshal(newNode)
		if err != nil {
			return err
		}

		patchBytes, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, corev1.Node{})
		if err != nil {
			return fmt.Errorf("failed to create patch for node %q: %w", nodeName, err)
		}
		_, err = ctrl.kubeClient.CoreV1().Nodes().Patch(ctx, nodeName, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
		return err
	})
}

func maxUnavailable(pool *mcfgv1.MachineConfigPool, nodes []*corev1.Node) (int, error) {
	intOrPercent := intstrutil.FromInt(1)
	if pool.Spec.MaxUnavailable != nil {
		intOrPercent = *pool.Spec.MaxUnavailable
	}
	maxunavail, err := intstrutil.GetScaledValueFromIntOrPercent(&intOrPercent, len(nodes), false)
	if err != nil {
		return 0, err
	}
	if maxunavail == 0 {
		maxunavail = 1
	}
	return maxunavail, nil
}

// getErrorString returns error string if not nil and empty string if error is nil
func getErrorString(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}
