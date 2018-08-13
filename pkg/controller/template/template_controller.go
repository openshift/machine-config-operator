package template

import (
	"fmt"
	"sort"
	"time"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/lib/resourceapply"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/controller"
	mcfgclientset "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/scheme"
	mcfginformersv1 "github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions/machineconfiguration.openshift.io/v1"
	mcfglistersv1 "github.com/openshift/machine-config-operator/pkg/generated/listers/machineconfiguration.openshift.io/v1"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
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
)

// controllerKind contains the schema.GroupVersionKind for this controller type.
var controllerKind = mcfgv1.SchemeGroupVersion.WithKind("ControllerConfig")

// Controller defines the template controller
type Controller struct {
	templatesDir string

	client        mcfgclientset.Interface
	eventRecorder record.EventRecorder

	syncHandler             func(ccKey string) error
	enqueueControllerConfig func(*mcfgv1.ControllerConfig)

	ccLister mcfglistersv1.ControllerConfigLister
	mcLister mcfglistersv1.MachineConfigLister

	ccListerSynced cache.InformerSynced
	mcListerSynced cache.InformerSynced

	queue workqueue.RateLimitingInterface
}

// New returns a new template controller.
func New(
	templatesDir string,
	ccInformer mcfginformersv1.ControllerConfigInformer,
	mcInformer mcfginformersv1.MachineConfigInformer,
	kubeClient clientset.Interface,
	mcfgClient mcfgclientset.Interface,
) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.Infof)
	eventBroadcaster.StartRecordingToSink(&corev1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	ctrl := &Controller{
		templatesDir:  templatesDir,
		client:        mcfgClient,
		eventRecorder: eventBroadcaster.NewRecorder(scheme.Scheme, v1.EventSource{Component: "controllerconfig"}),
		queue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "controllerconfig"),
	}

	ccInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addControllerConfig,
		UpdateFunc: ctrl.updateControllerConfig,
		// This will enter the sync loop and no-op, because the controllerconfig has been deleted from the store.
		DeleteFunc: ctrl.deleteControllerConfig,
	})

	mcInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addMachineConfig,
		UpdateFunc: ctrl.updateMachineConfig,
		DeleteFunc: ctrl.deleteMachineConfig,
	})

	ctrl.syncHandler = ctrl.syncControllerConfig
	ctrl.enqueueControllerConfig = ctrl.enqueue

	ctrl.ccLister = ccInformer.Lister()
	ctrl.mcLister = mcInformer.Lister()
	ctrl.ccListerSynced = ccInformer.Informer().HasSynced
	ctrl.mcListerSynced = mcInformer.Informer().HasSynced

	return ctrl
}

// Run executes the template controller
func (ctrl *Controller) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()

	glog.Info("Starting Controller")
	defer glog.Info("Shutting down Controller")

	if !cache.WaitForCacheSync(stopCh, ctrl.ccListerSynced, ctrl.mcListerSynced) {
		return
	}

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.worker, time.Second, stopCh)
	}

	<-stopCh
}

func (ctrl *Controller) addControllerConfig(obj interface{}) {
	cfg := obj.(*mcfgv1.ControllerConfig)
	glog.V(4).Infof("Adding ControllerConfig %s", cfg.Name)
	ctrl.enqueueControllerConfig(cfg)
}

func (ctrl *Controller) updateControllerConfig(old, cur interface{}) {
	oldCfg := old.(*mcfgv1.ControllerConfig)
	curCfg := cur.(*mcfgv1.ControllerConfig)
	glog.V(4).Infof("Updating ControllerConfig %s", oldCfg.Name)
	ctrl.enqueueControllerConfig(curCfg)
}

func (ctrl *Controller) deleteControllerConfig(obj interface{}) {
	cfg, ok := obj.(*mcfgv1.ControllerConfig)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Couldn't get object from tombstone %#v", obj))
			return
		}
		cfg, ok = tombstone.Obj.(*mcfgv1.ControllerConfig)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Tombstone contained object that is not a ControllerConfig %#v", obj))
			return
		}
	}
	glog.V(4).Infof("Deleting ControllerConfig %s", cfg.Name)
	// TODO(abhinavdahiya): handle deletes.
}

func (ctrl *Controller) addMachineConfig(obj interface{}) {
	mc := obj.(*mcfgv1.MachineConfig)
	if mc.DeletionTimestamp != nil {
		ctrl.deleteMachineConfig(mc)
		return
	}

	if controllerRef := metav1.GetControllerOf(mc); controllerRef != nil {
		cfg := ctrl.resolveControllerRef(mc.Namespace, controllerRef)
		if cfg == nil {
			return
		}
		glog.V(4).Infof("MachineConfig %s added", mc.Name)
		ctrl.enqueueControllerConfig(cfg)
		return
	}

	// No adopting.
}

func (ctrl *Controller) updateMachineConfig(old, cur interface{}) {
	oldMC := old.(*mcfgv1.MachineConfig)
	curMC := cur.(*mcfgv1.MachineConfig)
	if oldMC.ResourceVersion == curMC.ResourceVersion {
		return
	}

	if controllerRef := metav1.GetControllerOf(curMC); controllerRef != nil {
		cfg := ctrl.resolveControllerRef(curMC.Namespace, controllerRef)
		if cfg == nil {
			return
		}
		glog.V(4).Infof("MachineConfig %s updated", curMC.Name)
		ctrl.enqueueControllerConfig(cfg)
		return
	}

	// No adopting.
}

func (ctrl *Controller) deleteMachineConfig(obj interface{}) {
	mc, ok := obj.(*mcfgv1.MachineConfig)

	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Couldn't get object from tombstone %#v", obj))
			return
		}
		mc, ok = tombstone.Obj.(*mcfgv1.MachineConfig)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Tombstone contained object that is not a MachineConfig %#v", obj))
			return
		}
	}

	controllerRef := metav1.GetControllerOf(mc)
	if controllerRef == nil {
		// No controller should care about orphans being deleted.
		return
	}
	cfg := ctrl.resolveControllerRef(mc.Namespace, controllerRef)
	if cfg == nil {
		return
	}
	glog.V(4).Infof("MachineConfig %s deleted.", mc.Name)
	ctrl.enqueueControllerConfig(cfg)
}

func (ctrl *Controller) resolveControllerRef(namespace string, controllerRef *metav1.OwnerReference) *mcfgv1.ControllerConfig {
	// We can't look up by UID, so look up by Name and then verify UID.
	// Don't even try to look up by Name if it's the wrong Kind.
	if controllerRef.Kind != controllerKind.Kind {
		return nil
	}
	cfg, err := ctrl.ccLister.ControllerConfigs(namespace).Get(controllerRef.Name)
	if err != nil {
		return nil
	}

	if cfg.UID != controllerRef.UID {
		// The controller we found with this Name is not the same one that the
		// ControllerRef points to.
		return nil
	}
	return cfg
}

func (ctrl *Controller) enqueue(config *mcfgv1.ControllerConfig) {
	key, err := controller.KeyFunc(config)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", config, err))
		return
	}

	ctrl.queue.Add(key)
}

func (ctrl *Controller) enqueueRateLimited(controllerconfig *mcfgv1.ControllerConfig) {
	key, err := controller.KeyFunc(controllerconfig)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", controllerconfig, err))
		return
	}

	ctrl.queue.AddRateLimited(key)
}

// enqueueAfter will enqueue a controllerconfig after the provided amount of time.
func (ctrl *Controller) enqueueAfter(controllerconfig *mcfgv1.ControllerConfig, after time.Duration) {
	key, err := controller.KeyFunc(controllerconfig)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", controllerconfig, err))
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
		glog.V(2).Infof("Error syncing controllerconfig %v: %v", key, err)
		ctrl.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	glog.V(2).Infof("Dropping controllerconfig %q out of the queue: %v", key, err)
	ctrl.queue.Forget(key)
}

// syncControllerConfig will sync the controller config with the given key.
// This function is not meant to be invoked concurrently with the same key.
func (ctrl *Controller) syncControllerConfig(key string) error {
	startTime := time.Now()
	glog.V(4).Infof("Started syncing controllerconfig %q (%v)", key, startTime)
	defer func() {
		glog.V(4).Infof("Finished syncing controllerconfig %q (%v)", key, time.Since(startTime))
	}()

	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	controllerconfig, err := ctrl.ccLister.ControllerConfigs(namespace).Get(name)
	if errors.IsNotFound(err) {
		glog.V(2).Infof("ControllerConfig %v has been deleted", key)
		return nil
	}
	if err != nil {
		return err
	}

	// Deep-copy otherwise we are mutating our cache.
	// TODO: Deep-copy only when needed.
	cfg := controllerconfig.DeepCopy()

	mcs, err := getMachineConfigsForControllerConfig(ctrl.templatesDir, cfg)
	if err != nil {
		return err
	}

	for idx := range mcs {
		_, updated, err := resourceapply.ApplyMachineConfig(ctrl.client.MachineconfigurationV1(), mcs[idx])
		if err != nil {
			return err
		}
		if updated {
			glog.V(4).Infof("Machineconfig %s was updated", mcs[idx].Name)
		}
	}

	return nil
}

func getMachineConfigsForControllerConfig(templatesDir string, config *mcfgv1.ControllerConfig) ([]*mcfgv1.MachineConfig, error) {
	rc := &renderConfig{
		ControllerConfigSpec: &config.Spec,
	}
	mcs, err := generateMachineConfigs(rc, templatesDir)
	if err != nil {
		return nil, err
	}

	for i := range mcs {
		mcs[i].SetNamespace(config.GetNamespace())
		oref := metav1.NewControllerRef(config, controllerKind)
		mcs[i].SetOwnerReferences([]metav1.OwnerReference{*oref})
	}

	sort.Slice(mcs, func(i, j int) bool { return mcs[i].Name < mcs[j].Name })
	return mcs, nil
}
