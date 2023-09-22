package state

import (
	"context"
	"fmt"
	"time"

	v1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	mcfginformersv1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1"
	"golang.org/x/time/rate"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
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
	SubControllers []v1.StateSubController
	UpdateDelay    time.Duration
}
type StateController interface {
	Run(int, <-chan struct{}, record.EventRecorder)
}

type informers struct {
	mcpInformer   mcfginformersv1.MachineConfigPoolInformer
	msInformer    mcfginformersv1.MachineStateInformer
	eventInformer corev1eventinformers.EventInformer
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

	mcpListerSynced   cache.InformerSynced
	msListerSynced    cache.InformerSynced
	eventListerSynced cache.InformerSynced

	queue workqueue.RateLimitingInterface

	config StateControllerConfig

	listeners []watch.Interface

	subControllers []v1.StateSubController

	bootstrapHealthController BootstrapStateController
}

func New(
	mcpInformer mcfginformersv1.MachineConfigPoolInformer,
	msInformer mcfginformersv1.MachineStateInformer,
	eventInformer corev1eventinformers.EventInformer,
	cfg StateControllerConfig,
	kubeClient clientset.Interface,
	mcfgClient mcfgclientset.Interface,
) *Controller {

	ctrl := &Controller{
		informers: &informers{
			mcpInformer:   mcpInformer,
			msInformer:    msInformer,
			eventInformer: eventInformer,
		},
		subControllers: cfg.SubControllers,
		config: StateControllerConfig{
			UpdateDelay: time.Second * 5,
		},
	}

	// does component matter? because I have been using it for whatever I want.
	ctrl.eventInformer = eventInformer
	ctrl.msInformer = msInformer
	ctrl.syncHandler = ctrl.syncStateController
	ctrl.enqueueObject = ctrl.enqueueDefault

	ctrl.msListerSynced = msInformer.Informer().HasSynced
	ctrl.mcpListerSynced = mcpInformer.Informer().HasSynced
	ctrl.eventListerSynced = eventInformer.Informer().HasSynced

	ctrl.queue = workqueue.NewNamedRateLimitingQueue(workqueue.NewMaxOfRateLimiter(
		&workqueue.BucketRateLimiter{Limiter: rate.NewLimiter(rate.Limit(updateDelay), 1)},
		workqueue.NewItemExponentialFailureRateLimiter(1*time.Second, maxUpdateBackoff)), "machinestatecontroller")

	ctrl.Clients = &Clients{
		Mcfgclient: mcfgClient,
		Kubeclient: kubeClient,
	}

	for _, entry := range cfg.SubControllers {
		switch entry {
		//	case v1.StateSubControllerPool:
		//		ctrl.upgradeHealthController = *newUpgradeStateController(ctrl.config, upgradeWatcher)
		//	case v1.StateSubControllerOperator:
		//		ctrl.operatorHealthController = *newOperatorStateController(ctrl.config, operatorWatcher)
		case v1.StateSubControllerBootstrap:
			//	ctrl.bootstrapHealthController = *newBootstrapStateController(ctrl.config)
		}
	}

	/*	ctrl.msInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addMachineState,
		UpdateFunc: ctrl.updateMachineState,
		//DeleteFunc: ctrl.deleteMachineState,
	})*/

	if ctrl.eventInformer != nil && ctrl.msInformer != nil {
		ctrl.eventInformer.Informer().AddEventHandler(ctrl.eventHandler())
		ctrl.msInformer.Informer().AddEventHandler(ctrl.eventHandler())
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

// we want to enqueue it, basically just add it to queue
// so then our sync handler can just do some stuff with it
// namely, call out syncAll.
// wait. But does this mean we do not need events?

// we might not even need a controller, but we should. Have daemon send events
// and have the state controller be the only place machine states are modified

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

/*
func (ctrl *Controller) addMachineState(obj interface{}) {
	ms := obj.(*mcfgv1.MachineState).DeepCopy()
	klog.V(4).Infof("Adding MachineConfigPool %s", ms.Name)
	ctrl.enqueueMachineState(ms)
}

func (ctrl *Controller) updateMachineState(old, curr interface{}) {
	currMS := curr.(*mcfgv1.MachineState).DeepCopy()
	oldMS := old.(*mcfgv1.MachineState).DeepCopy()
	if !reflect.DeepEqual(oldMS.Status, currMS.Status) {
		klog.Info("user cannot change MachineState status via the API")
		return
	}
	klog.V(4).Infof("updating MachineConfigPool %s", currMS.Name)
	ctrl.enqueueMachineState(currMS)
}
*/

func (ctrl *Controller) Run(workers int, parentCtx context.Context, stopCh <-chan struct{}, healthEvents record.EventRecorder) {

	klog.Info("Starting MachineStateController")
	defer klog.Info("Shutting down MachineStateController")

	ctx, cancel := context.WithCancel(parentCtx)
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()
	defer cancel()

	//ctrl.informers.start(ctx)

	if !cache.WaitForCacheSync(ctx.Done(), ctrl.mcpListerSynced, ctrl.msListerSynced, ctrl.eventListerSynced) {
		return
	}
	/*	for _, subctrl := range ctrl.subControllers {
		switch subctrl {
		case v1.StateSubControllerPool:
			go ctrl.upgradeHealthController.Run(workers)
		case v1.StateSubControllerBootstrap: // this can be the only one if it exists
			go ctrl.bootstrapHealthController.Run(workers, stopCh)
			go func() {
				shutdown := func() {
					ctrl.bootstrapHealthController.Stop()
				}
				for {
					select {
					case <-stopCh:
						shutdown()
						return
						//	case <-ctrl.bootstrapHealthController.Done():
						//		// We got a stop signal from the Config Drift Monitor.
						//shutdown()
						//		return
					}
				}
			}()
		case v1.StateSubControllerOperator:
			go ctrl.operatorHealthController.Run(workers)
		}
	}*/

	if len(ctrl.subControllers) > 0 && ctrl.subControllers[0] == v1.StateSubControllerBootstrap {
		/*go ctrl.bootstrapHealthController.Run(workers, stopCh)
		go func() {
			shutdown := func() {
				ctrl.bootstrapHealthController.Stop()
			}
			for {
				select {
				case <-stopCh:
					shutdown()
					return
					//	case <-ctrl.bootstrapHealthController.Done():
					//		// We got a stop signal from the Config Drift Monitor.
					//shutdown()
					//		return
				}
			}
		}() */
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
	klog.V(4).Infof("Started syncing machine state %q (%v)", key, startTime)
	defer func() {
		klog.V(4).Infof("Finished syncing machine state %q (%v)", key, time.Since(startTime))
	}()

	var syncFuncs = []syncFunc{
		{string(v1.ControllerState), ctrl.syncMCC},
		{string(v1.DaemonState), ctrl.syncMCD},
		{string(v1.ServerState), ctrl.syncMCS},
		{string(v1.MetricsSync), ctrl.syncMetrics},
		{string(v1.UpgradeProgression), ctrl.syncUpgradingProgression},
		{string(v1.OperatorProgression), ctrl.syncOperatorProgression},
	}
	return ctrl.syncAll(syncFuncs, key)
}
