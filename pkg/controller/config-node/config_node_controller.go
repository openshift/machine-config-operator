package confignode

import (
	"fmt"
	"time"

	"github.com/golang/glog"
	configv1 "github.com/openshift/api/config/v1"
	cligoinformersv1 "github.com/openshift/client-go/config/informers/externalversions/config/v1"
	cligolistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	mcfgclientset "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/scheme"
	mcfginformersv1 "github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions/machineconfiguration.openshift.io/v1"
	mcfglistersv1 "github.com/openshift/machine-config-operator/pkg/generated/listers/machineconfiguration.openshift.io/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	coreclientsetv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
)

const (

	// maxRetries is the number of times a machineconfig pool will be retried before it is dropped out of the queue.
	// With the current rate-limiter in use (5ms*2^(maxRetries-1)) the following numbers represent the times
	// a machineconfig pool is going to be requeued:
	//
	// 5ms, 10ms, 20ms, 40ms, 80ms, 160ms, 320ms, 640ms, 1.3s, 2.6s, 5.1s, 10.2s, 20.4s, 41s, 82s
	maxRetries = 15

	// updateDelay is a pause to deal with churn in MachineConfigs; see
	// https://github.com/openshift/machine-config-operator/issues/301
	updateDelay = 5 * time.Second

	// Default update frequency of the kubelet
	// Please refer - https://github.com/openshift/enhancements/blob/master/enhancements/worker-latency-profile/worker-latency-profile.md#default-update-and-default-reaction
	defaultKubeletUpdateFrequency = 10

	// Medium update frequency of the kubelet
	// Please refer - https://github.com/openshift/enhancements/blob/master/enhancements/worker-latency-profile/worker-latency-profile.md#medium-update-and-average-reaction
	mediauKubeletUpdateFrequency = 20

	// Low update frequency of the kubelet
	// Please refer - https://github.com/openshift/enhancements/blob/master/enhancements/worker-latency-profile/worker-latency-profile.md#low-update-and-slow-reaction
	LowKubeletUpdateFrequency = 60
)

// Controller defines the node controller.
type Controller struct {
	client        mcfgclientset.Interface
	kubeClient    clientset.Interface
	eventRecorder record.EventRecorder

	syncHandler       func(mcp string) error
	enqueueConfigNode func(*configv1.Node)

	ccLister mcfglistersv1.ControllerConfigLister
	cnLister cligolistersv1.NodeLister

	ccListerSynced cache.InformerSynced
	cnListerSynced cache.InformerSynced

	queue workqueue.RateLimitingInterface
}

// New returns a new node controller.
func New(
	ccInformer mcfginformersv1.ControllerConfigInformer,
	cnInformer cligoinformersv1.NodeInformer,
	kubeClient clientset.Interface,
	mcfgClient mcfgclientset.Interface,
) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.Infof)
	eventBroadcaster.StartRecordingToSink(&coreclientsetv1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	ctrl := &Controller{
		client:        mcfgClient,
		kubeClient:    kubeClient,
		eventRecorder: eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineconfigcontroller-confignodecontroller"}),
		queue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "machineconfigcontroller-confignodecontroller"),
	}

	cnInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addConfiNode,
		UpdateFunc: ctrl.updateConfigNode,
		DeleteFunc: ctrl.deleteConfigNode,
	})

	ctrl.syncHandler = ctrl.syncConfigNode
	ctrl.enqueueConfigNode = ctrl.enqueueDefault

	ctrl.ccLister = ccInformer.Lister()
	ctrl.cnLister = cnInformer.Lister()
	ctrl.ccListerSynced = ccInformer.Informer().HasSynced
	ctrl.cnListerSynced = cnInformer.Informer().HasSynced

	return ctrl
}

// syncConfigNode will sync the confignode with the given key.
// This function is not meant to be invoked concurrently with the same key.
func (ctrl *Controller) syncConfigNode(key string) error {
	startTime := time.Now()
	glog.V(4).Infof("Started syncing config node %q (%v)", key, startTime)
	defer func() {
		glog.V(4).Infof("Finished syncing config node %q (%v)", key, time.Since(startTime))
	}()

	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	confignode, err := ctrl.cnLister.Get(name)
	if errors.IsNotFound(err) {
		glog.V(2).Infof("Configuration Node %v has been deleted", key)
		return nil
	}
	if err != nil {
		return err
	}

	if confignode != nil {
		if &confignode.Spec.CgroupMode != nil {
			// Create and track the KubeletConfig that enables cgroups v2
		}

		if &confignode.Spec.WorkerLatencyProfile != nil {
			// Create and track the KubeletConfig that updates frequency argument
			if confignode.Spec.WorkerLatencyProfile == configv1.DefaultUpdateDefaultReaction {

			} else if confignode.Spec.WorkerLatencyProfile == configv1.MediumUpdateAverageReaction {

			} else if confignode.Spec.WorkerLatencyProfile == configv1.LowUpdateSlowReaction {

			}
		}
	}

	return nil
}

// Run executes the render controller.
func (ctrl *Controller) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()

	if !cache.WaitForCacheSync(stopCh, ctrl.ccListerSynced, ctrl.cnListerSynced) {
		return
	}

	glog.Info("Starting MachineConfigController-ConfigNodeController")
	defer glog.Info("Shutting down MachineConfigController-ConfigNodeController")

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.worker, time.Second, stopCh)
	}

	<-stopCh
}

func (ctrl *Controller) addConfiNode(obj interface{}) {
	pool := obj.(*configv1.Node)
	glog.V(4).Infof("Adding ConfigNode %s", pool.Name)
	ctrl.enqueueConfigNode(pool)
}

func (ctrl *Controller) updateConfigNode(old, cur interface{}) {
	oldPool := old.(*configv1.Node)
	curPool := cur.(*configv1.Node)

	glog.V(4).Infof("Updating ConfigNode %s", oldPool.Name)
	ctrl.enqueueConfigNode(curPool)
}

func (ctrl *Controller) deleteConfigNode(obj interface{}) {
	pool, ok := obj.(*configv1.Node)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Couldn't get object from tombstone %#v", obj))
			return
		}
		pool, ok = tombstone.Obj.(*configv1.Node)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Tombstone contained object that is not a ConfigNode %#v", obj))
			return
		}
	}
	glog.V(4).Infof("Deleting ConfigNode %s", pool.Name)
	// TODO(abhinavdahiya): handle deletes.
}

func (ctrl *Controller) enqueue(pool *configv1.Node) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pool)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", pool, err))
		return
	}

	ctrl.queue.Add(key)
}

func (ctrl *Controller) enqueueRateLimited(pool *configv1.Node) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pool)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", pool, err))
		return
	}

	ctrl.queue.AddRateLimited(key)
}

// enqueueAfter will enqueue a pool after the provided amount of time.
func (ctrl *Controller) enqueueAfter(pool *configv1.Node, after time.Duration) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pool)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", pool, err))
		return
	}

	ctrl.queue.AddAfter(key, after)
}

// enqueueDefault calls a default enqueue function
func (ctrl *Controller) enqueueDefault(pool *configv1.Node) {
	ctrl.enqueueAfter(pool, updateDelay)
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
		glog.V(2).Infof("Error syncing confignode %v: %v", key, err)
		ctrl.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	glog.V(2).Infof("Dropping config node %q out of the queue: %v", key, err)
	ctrl.queue.Forget(key)
	ctrl.queue.AddAfter(key, 1*time.Minute)
}

// getErrorString returns error string if not nil and empty string if error is nil
func getErrorString(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}
