package state

import (
	"context"
	"fmt"
	"time"

	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	operatorcfginformersv1 "github.com/openshift/client-go/operator/informers/externalversions/operator/v1"

	"golang.org/x/time/rate"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	coreinformersv1 "k8s.io/client-go/informers/core/v1"
	corev1eventinformers "k8s.io/client-go/informers/core/v1"

	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

const (
	maxRetries  = 15
	updateDelay = 5 * time.Second

	// maxUpdateBackoff is the maximum time to react to a change as we back off
	// in the face of errors.
	maxUpdateBackoff = 60 * time.Second
)

type syncFunc struct {
	name string
	fn   func(interface{}) error
}

// need to establish if this is the sort of thing
// that needs an on/off switch like build controller
// It probably doesn't, as long as the MCC starts this as
// it does other controllers

// it seems that other controllers (besides build) do not have a seprate "non generic"
// controller struct. they just figure out how to do things without data
// but we want bookkeeping, so we will need to probably have a struct that has a new "bookkeeping" style entity
// as well as specific health related structs
type StateControllerConfig struct {
	UpdateDelay time.Duration
}
type StateController interface {
	Run(int, <-chan struct{}, record.EventRecorder)
}

type informers struct {
	operatorCfgInformer operatorcfginformersv1.MachineConfigurationInformer
	nodeInformer        coreinformersv1.NodeInformer
	eventInformer       corev1eventinformers.EventInformer
	serviceInformer     coreinformersv1.ServiceInformer
}
type Clients struct {
	Mcfgclient mcfgclientset.Interface
	Kubeclient clientset.Interface
}

type Controller struct {
	*Clients
	*informers

	syncHandler   func(key string) error
	enqueueObject func(interface{})

	serviceInformerSynced cache.InformerSynced

	queue workqueue.RateLimitingInterface

	config StateControllerConfig

	listeners []watch.Interface
}

func New(
	serviceInformer coreinformersv1.ServiceInformer,
	cfg StateControllerConfig,
	kubeClient clientset.Interface,
	mcfgClient mcfgclientset.Interface,
) *Controller {

	ctrl := &Controller{
		informers: &informers{
			serviceInformer: serviceInformer,
		},
		config: StateControllerConfig{
			UpdateDelay: time.Second * 5,
		},
	}

	ctrl.serviceInformer = serviceInformer
	ctrl.syncHandler = ctrl.syncStateController
	ctrl.enqueueObject = ctrl.enqueueDefault

	ctrl.serviceInformerSynced = serviceInformer.Informer().HasSynced

	ctrl.queue = workqueue.NewNamedRateLimitingQueue(workqueue.NewMaxOfRateLimiter(
		&workqueue.BucketRateLimiter{Limiter: rate.NewLimiter(rate.Limit(updateDelay), 1)},
		workqueue.NewItemExponentialFailureRateLimiter(1*time.Second, maxUpdateBackoff)), "machinestatecontroller")

	ctrl.Clients = &Clients{
		Mcfgclient: mcfgClient,
		Kubeclient: kubeClient,
	}

	if ctrl.eventInformer != nil {
		ctrl.serviceInformer.Informer().AddEventHandler(ctrl.eventHandler())
	}

	return ctrl
}

func (ctrl *Controller) eventHandler() cache.ResourceEventHandler {
	return cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			ctrl.enqueueDefault(obj)
		},
		UpdateFunc: func(old, new interface{}) {
			ctrl.enqueueDefault(new)
		},
		DeleteFunc: func(obj interface{}) {
			ctrl.enqueueDefault(obj)
		},
	}
}

// enqueueAfter will enqueue a pool after the provided amount of time.
func (ctrl *Controller) enqueueAfter(obj interface{}, after time.Duration) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", obj, err))
		return
	}

	ctrl.queue.AddAfter(key, after)
}

// need to look at the diff between the api updated structs and the events
// the api updated structs are only going to be user input
// while the events are going to be internally updated
// enqueueDefault calls a default enqueue function
func (ctrl *Controller) enqueueDefault(obj interface{}) {
	ctrl.enqueueAfter(obj, ctrl.config.UpdateDelay)
}

func (ctrl *Controller) Run(workers int, parentCtx context.Context, stopCh <-chan struct{}, healthEvents record.EventRecorder) {

	klog.Info("Starting MachineConfigStateController")
	defer klog.Info("Shutting down MachineConfigStateController")

	ctx, cancel := context.WithCancel(parentCtx)
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()
	defer cancel()

	//ctrl.informers.start(ctx)

	if !cache.WaitForCacheSync(ctx.Done(), ctrl.serviceInformerSynced) {
		return
	}

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.worker, time.Second, stopCh)
	}

	<-stopCh
}

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
		klog.V(2).Infof("Error syncing state controller %v: %v", key, err)
		ctrl.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	klog.V(2).Infof("Dropping state controller %q out of the queue: %v", key, err)
	ctrl.queue.Forget(key)
	ctrl.queue.AddAfter(key, 1*time.Minute)
}

func (ctrl *Controller) syncStateController(key string) error {
	startTime := time.Now()
	klog.V(4).Infof("Started syncing controller %q (%v)", key, startTime)
	defer func() {
		klog.V(4).Infof("Finished syncing machine state %q (%v)", key, time.Since(startTime))
	}()

	// add metrics stuff somewhere in here
	// maybe call out to another function and make a metrics.go or something

	return nil
}
