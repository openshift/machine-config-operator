package state

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	machineconfigurationv1 "github.com/openshift/client-go/machineconfiguration/applyconfigurations/machineconfiguration/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	mcfginformersv1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1"
	operatorcfginformersv1 "github.com/openshift/client-go/operator/informers/externalversions/operator/v1"

	"golang.org/x/time/rate"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	msInformer          mcfginformersv1.MachineConfigStateInformer
	eventInformer       corev1eventinformers.EventInformer
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

	msListerSynced    cache.InformerSynced
	eventListerSynced cache.InformerSynced
	nodeListerSynced  cache.InformerSynced

	queue workqueue.RateLimitingInterface

	config StateControllerConfig

	listeners []watch.Interface

	bootstrapHealthController BootstrapStateController
}

func New(
	msInformer mcfginformersv1.MachineConfigStateInformer,
	eventInformer corev1eventinformers.EventInformer,
	nodeInformer coreinformersv1.NodeInformer,
	cfg StateControllerConfig,
	kubeClient clientset.Interface,
	mcfgClient mcfgclientset.Interface,
) *Controller {

	ctrl := &Controller{
		informers: &informers{
			msInformer:    msInformer,
			eventInformer: eventInformer,
			nodeInformer:  nodeInformer,
		},
		config: StateControllerConfig{
			UpdateDelay: time.Second * 5,
		},
	}

	// does component matter? because I have been using it for whatever I want.
	ctrl.eventInformer = eventInformer
	ctrl.msInformer = msInformer
	ctrl.nodeInformer = nodeInformer
	ctrl.syncHandler = ctrl.syncStateController
	ctrl.enqueueObject = ctrl.enqueueDefault

	ctrl.msListerSynced = msInformer.Informer().HasSynced
	ctrl.eventListerSynced = eventInformer.Informer().HasSynced
	ctrl.nodeListerSynced = nodeInformer.Informer().HasSynced

	ctrl.queue = workqueue.NewNamedRateLimitingQueue(workqueue.NewMaxOfRateLimiter(
		&workqueue.BucketRateLimiter{Limiter: rate.NewLimiter(rate.Limit(updateDelay), 1)},
		workqueue.NewItemExponentialFailureRateLimiter(1*time.Second, maxUpdateBackoff)), "machinestatecontroller")

	ctrl.Clients = &Clients{
		Mcfgclient: mcfgClient,
		Kubeclient: kubeClient,
	}

	if ctrl.eventInformer != nil && ctrl.msInformer != nil {
		ctrl.eventInformer.Informer().AddEventHandler(ctrl.eventHandler())
		ctrl.msInformer.Informer().AddEventHandler(ctrl.eventHandler())
		ctrl.nodeInformer.Informer().AddEventHandler(ctrl.eventHandler())

	}

	return ctrl
}

// if we react to nodes, mcps, mc, kc, cc AFTER they change. I guess that is just as good as catching when they change. Maybe we only event if we error
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
func (ctrl *Controller) addMachineConfigState(obj interface{}) {
	ms := obj.(*mcfgv1.MachineConfigState).DeepCopy()
	klog.V(4).Infof("Adding MachineConfigPool %s", ms.Name)
	ctrl.enqueueMachineConfigState(ms)
}

func (ctrl *Controller) updateMachineConfigState(old, curr interface{}) {
	currMS := curr.(*mcfgv1.MachineConfigState).DeepCopy()
	oldMS := old.(*mcfgv1.MachineConfigState).DeepCopy()
	if !reflect.DeepEqual(oldMS.Status, currMS.Status) {
		klog.Info("user cannot change MachineConfigState status via the API")
		return
	}
	klog.V(4).Infof("updating MachineConfigPool %s", currMS.Name)
	ctrl.enqueueMachineConfigState(currMS)
}
*/

func (ctrl *Controller) Run(workers int, parentCtx context.Context, stopCh <-chan struct{}, healthEvents record.EventRecorder) {

	klog.Info("Starting MachineConfigStateController")
	defer klog.Info("Shutting down MachineConfigStateController")

	ctx, cancel := context.WithCancel(parentCtx)
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()
	defer cancel()

	//ctrl.informers.start(ctx)

	if !cache.WaitForCacheSync(ctx.Done(), ctrl.nodeListerSynced, ctrl.msListerSynced, ctrl.eventListerSynced) {
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

	/*
		var syncFuncs = []syncFunc{
			{string(v1.ControllerState), ctrl.syncMCC},
			{string(v1.DaemonState), ctrl.syncMCD},
			{string(v1.ServerState), ctrl.syncMCS},
			{string(v1.MetricsSync), ctrl.syncMetrics},
			{string(v1.UpgradeProgression), ctrl.syncUpgradingProgression},
			{string(v1.OperatorProgression), ctrl.syncOperatorProgression},
		}
	*/

	objectToUse := ""
	objAndNamespace := strings.Split(key, "/")
	if len(objAndNamespace) != 2 {
		klog.Infof("Obj: %s", key)
		// just try it
		objectToUse = objAndNamespace[0]
		//return fmt.Errorf("Object could not be split up, %s", key)
	} else {
		objectToUse = objAndNamespace[1]
	}
	msList := ctrl.GenerateMachineConfigStates(objectToUse)
	for _, item := range msList {
		klog.Infof("MSLIST ITEM: %s", item.Name)
		o, _ := json.Marshal(item.Status)
		klog.Infof("STATUS: %s", o)
	}
	for _, newState := range msList {
		cfgApplyConfig := machineconfigurationv1.MachineConfigStateConfig().WithKind(newState.Kind).WithName(newState.Name).WithAPIVersion(newState.APIVersion).WithResourceVersion(newState.ResourceVersion).WithUID(newState.UID)
		//progressionConditionApplyConfig := machineconfigurationv1.ProgressionCondition().WithKind(newCondition.Kind).WithName(newCondition.Name).WithPhase(newCondition.Name).WithReason(newCondition.Reason).WithState(newCondition.State).WithTime(newCondition.Time)
		progressionHistoryApplyConfigs := []*machineconfigurationv1.ProgressionHistoryApplyConfiguration{}
		for _, s := range newState.Status.ProgressionHistory {
			progressionHistoryApplyConfigs = append(progressionHistoryApplyConfigs, machineconfigurationv1.ProgressionHistory().WithNameAndState(s.NameAndState).WithPhase(s.Phase).WithReason(s.Reason))
		}
		progressionApplyConfigs := []*machineconfigurationv1.ProgressionConditionApplyConfiguration{}
		for _, s := range newState.Status.MostRecentState {
			progressionApplyConfigs = append(progressionApplyConfigs, machineconfigurationv1.ProgressionCondition().WithName(s.Name).WithPhase(s.Phase).WithReason(s.Reason).WithState(s.State).WithTime(s.Time))
		}
		statusApplyConfig := machineconfigurationv1.MachineConfigStateStatus().WithConfig(cfgApplyConfig).WithHealth(newState.Status.Health).WithMostRecentError(newState.Status.MostRecentError).WithMostRecentState(progressionApplyConfigs...).WithProgressionHistory(progressionHistoryApplyConfigs...)
		specApplyConfig := machineconfigurationv1.MachineConfigStateSpec().WithConfig(cfgApplyConfig)
		msApplyConfig := machineconfigurationv1.MachineConfigState(newState.Name).WithStatus(statusApplyConfig).WithSpec(specApplyConfig)
		//ctrl.Clients.Mcfgclient.MachineconfigurationV1().MachineConfigStates().Patch(context.TODO(), newState.Name, types.MergePatchType)

		applyConfig, _ := json.Marshal(msApplyConfig)
		klog.Infof("Updating Machine State Controller apply config Status to %s", string(applyConfig))
		oldms, _ := ctrl.Clients.Mcfgclient.MachineconfigurationV1().MachineConfigStates().Get(context.TODO(), newState.Name, metav1.GetOptions{})
		if !equality.Semantic.DeepEqual(newState, oldms) {
			ms, err := ctrl.Clients.Mcfgclient.MachineconfigurationV1().MachineConfigStates().ApplyStatus(context.TODO(), msApplyConfig, metav1.ApplyOptions{FieldManager: "machine-config-operator", Force: true})
			if err != nil {
				return err
			}
			m, err := json.Marshal(ms)
			klog.Infof("MACHINESTATE: %s", string(m))
		}
	}
	return nil
}
