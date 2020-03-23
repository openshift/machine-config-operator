package render

import (
	"fmt"
	"reflect"
	"time"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/lib/resourceapply"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	mcfgclientset "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/scheme"
	mcfginformersv1 "github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions/machineconfiguration.openshift.io/v1"
	mcfglistersv1 "github.com/openshift/machine-config-operator/pkg/generated/listers/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/version"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
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

	// renderDelay is a pause to avoid churn in MachineConfigs; see
	// https://github.com/openshift/machine-config-operator/issues/301
	renderDelay = 5 * time.Second
)

var (
	// controllerKind contains the schema.GroupVersionKind for this controller type.
	controllerKind = mcfgv1.SchemeGroupVersion.WithKind("MachineConfigPool")

	machineconfigKind = mcfgv1.SchemeGroupVersion.WithKind("MachineConfig")
)

// Controller defines the render controller.
type Controller struct {
	client        mcfgclientset.Interface
	eventRecorder record.EventRecorder

	syncHandler              func(mcp string) error
	enqueueMachineConfigPool func(*mcfgv1.MachineConfigPool)

	mcpLister mcfglistersv1.MachineConfigPoolLister
	mcLister  mcfglistersv1.MachineConfigLister

	mcpListerSynced cache.InformerSynced
	mcListerSynced  cache.InformerSynced

	ccLister       mcfglistersv1.ControllerConfigLister
	ccListerSynced cache.InformerSynced

	queue workqueue.RateLimitingInterface
}

// New returns a new render controller.
func New(
	mcpInformer mcfginformersv1.MachineConfigPoolInformer,
	mcInformer mcfginformersv1.MachineConfigInformer,
	ccInformer mcfginformersv1.ControllerConfigInformer,
	kubeClient clientset.Interface,
	mcfgClient mcfgclientset.Interface,
) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.Infof)
	eventBroadcaster.StartRecordingToSink(&corev1client.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	ctrl := &Controller{
		client:        mcfgClient,
		eventRecorder: eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineconfigcontroller-rendercontroller"}),
		queue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "machineconfigcontroller-rendercontroller"),
	}

	mcpInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addMachineConfigPool,
		UpdateFunc: ctrl.updateMachineConfigPool,
		DeleteFunc: ctrl.deleteMachineConfigPool,
	})
	mcInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addMachineConfig,
		UpdateFunc: ctrl.updateMachineConfig,
		DeleteFunc: ctrl.deleteMachineConfig,
	})

	ctrl.syncHandler = ctrl.syncMachineConfigPool
	ctrl.enqueueMachineConfigPool = ctrl.enqueueDefault

	ctrl.mcpLister = mcpInformer.Lister()
	ctrl.mcLister = mcInformer.Lister()
	ctrl.mcpListerSynced = mcpInformer.Informer().HasSynced
	ctrl.mcListerSynced = mcInformer.Informer().HasSynced
	ctrl.ccLister = ccInformer.Lister()
	ctrl.ccListerSynced = ccInformer.Informer().HasSynced

	return ctrl
}

// Run executes the render controller.
func (ctrl *Controller) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()

	if !cache.WaitForCacheSync(stopCh, ctrl.mcpListerSynced, ctrl.mcListerSynced, ctrl.ccListerSynced) {
		return
	}

	glog.Info("Starting MachineConfigController-RenderController")
	defer glog.Info("Shutting down MachineConfigController-RenderController")

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

func (ctrl *Controller) addMachineConfig(obj interface{}) {
	mc := obj.(*mcfgv1.MachineConfig)
	if mc.DeletionTimestamp != nil {
		ctrl.deleteMachineConfig(mc)
		return
	}

	controllerRef := metav1.GetControllerOf(mc)
	if controllerRef != nil {
		if pool := ctrl.resolveControllerRef(controllerRef); pool != nil {
			glog.V(4).Infof("MachineConfig %s added", mc.Name)
			ctrl.enqueueMachineConfigPool(pool)
			return
		}
	}

	pools, err := ctrl.getPoolsForMachineConfig(mc)
	if err != nil {
		glog.Errorf("error finding pools for machineconfig: %v", err)
		return
	}

	glog.V(4).Infof("MachineConfig %s added", mc.Name)
	for _, p := range pools {
		ctrl.enqueueMachineConfigPool(p)
	}
}

func (ctrl *Controller) updateMachineConfig(old, cur interface{}) {
	oldMC := old.(*mcfgv1.MachineConfig)
	curMC := cur.(*mcfgv1.MachineConfig)

	curControllerRef := metav1.GetControllerOf(curMC)
	oldControllerRef := metav1.GetControllerOf(oldMC)
	controllerRefChanged := !reflect.DeepEqual(curControllerRef, oldControllerRef)
	if controllerRefChanged && oldControllerRef != nil {
		glog.Errorf("machineconfig has changed controller, not allowed.")
		return
	}

	if curControllerRef != nil {
		if pool := ctrl.resolveControllerRef(curControllerRef); pool != nil {
			glog.V(4).Infof("MachineConfig %s updated", curMC.Name)
			ctrl.enqueueMachineConfigPool(pool)
			return
		}
	}

	pools, err := ctrl.getPoolsForMachineConfig(curMC)
	if err != nil {
		glog.Errorf("error finding pools for machineconfig: %v", err)
		return
	}

	glog.V(4).Infof("MachineConfig %s updated", curMC.Name)
	for _, p := range pools {
		ctrl.enqueueMachineConfigPool(p)
	}
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
	if controllerRef != nil {
		if pool := ctrl.resolveControllerRef(controllerRef); pool != nil {
			glog.V(4).Infof("MachineConfig %s deleted", mc.Name)
			ctrl.enqueueMachineConfigPool(pool)
			return
		}
	}

	pools, err := ctrl.getPoolsForMachineConfig(mc)
	if err != nil {
		glog.Errorf("error finding pools for machineconfig: %v", err)
		return
	}

	glog.V(4).Infof("MachineConfig %s deleted", mc.Name)
	for _, p := range pools {
		ctrl.enqueueMachineConfigPool(p)
	}
}

func (ctrl *Controller) resolveControllerRef(controllerRef *metav1.OwnerReference) *mcfgv1.MachineConfigPool {
	// We can't look up by UID, so look up by Name and then verify UID.
	// Don't even try to look up by Name if it's the wrong Kind.
	if controllerRef.Kind != controllerKind.Kind {
		return nil
	}
	pool, err := ctrl.mcpLister.Get(controllerRef.Name)
	if err != nil {
		return nil
	}

	if pool.UID != controllerRef.UID {
		// The controller we found with this Name is not the same one that the
		// ControllerRef points to.
		return nil
	}
	return pool
}

func (ctrl *Controller) getPoolsForMachineConfig(config *mcfgv1.MachineConfig) ([]*mcfgv1.MachineConfigPool, error) {
	if len(config.Labels) == 0 {
		return nil, fmt.Errorf("no MachineConfigPool found for MachineConfig %v because it has no labels", config.Name)
	}

	pList, err := ctrl.mcpLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}

	var pools []*mcfgv1.MachineConfigPool
	for _, p := range pList {
		selector, err := metav1.LabelSelectorAsSelector(p.Spec.MachineConfigSelector)
		if err != nil {
			return nil, fmt.Errorf("invalid label selector: %v", err)
		}

		// If a pool with a nil or empty selector creeps in, it should match nothing, not everything.
		if selector.Empty() || !selector.Matches(labels.Set(config.Labels)) {
			continue
		}

		pools = append(pools, p)
	}

	if len(pools) == 0 {
		return nil, fmt.Errorf("could not find any MachineConfigPool set for MachineConfig %s with labels: %v", config.Name, config.Labels)
	}
	return pools, nil
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
func (ctrl *Controller) enqueueAfter(pool *mcfgv1.MachineConfigPool, after time.Duration) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pool)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", pool, err))
		return
	}

	ctrl.queue.AddAfter(key, after)
}

// enqueueDefault calls a default enqueue function
func (ctrl *Controller) enqueueDefault(pool *mcfgv1.MachineConfigPool) {
	ctrl.enqueueAfter(pool, renderDelay)
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

	// Deep-copy otherwise we are mutating our cache.
	// TODO: Deep-copy only when needed.
	pool := machineconfigpool.DeepCopy()
	everything := metav1.LabelSelector{}
	if reflect.DeepEqual(pool.Spec.MachineConfigSelector, &everything) {
		ctrl.eventRecorder.Eventf(pool, corev1.EventTypeWarning, "SelectingAll", "This machineconfigpool is selecting all machineconfigs. A non-empty selector is require.")
		return nil
	}

	selector, err := metav1.LabelSelectorAsSelector(pool.Spec.MachineConfigSelector)
	if err != nil {
		return err
	}

	// TODO(runcom): add tests in render_controller_test.go for this condition
	if err := mcfgv1.IsControllerConfigCompleted(ctrlcommon.ControllerConfigName, ctrl.ccLister.Get); err != nil {
		return err
	}

	mcs, err := ctrl.mcLister.List(selector)
	if err != nil {
		return err
	}
	if len(mcs) == 0 {
		return ctrl.syncFailingStatus(pool, fmt.Errorf("no MachineConfigs found matching selector %v", selector))
	}

	if err := ctrl.syncGeneratedMachineConfig(pool, mcs); err != nil {
		return ctrl.syncFailingStatus(pool, err)
	}

	return ctrl.syncAvailableStatus(pool)
}

func (ctrl *Controller) syncAvailableStatus(pool *mcfgv1.MachineConfigPool) error {
	if mcfgv1.IsMachineConfigPoolConditionFalse(pool.Status.Conditions, mcfgv1.MachineConfigPoolRenderDegraded) {
		return nil
	}
	sdegraded := mcfgv1.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolRenderDegraded, corev1.ConditionFalse, "", "")
	mcfgv1.SetMachineConfigPoolCondition(&pool.Status, *sdegraded)
	if _, err := ctrl.client.MachineconfigurationV1().MachineConfigPools().UpdateStatus(pool); err != nil {
		return err
	}
	return nil
}

func (ctrl *Controller) syncFailingStatus(pool *mcfgv1.MachineConfigPool, err error) error {
	sdegraded := mcfgv1.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolRenderDegraded, corev1.ConditionTrue, "", fmt.Sprintf("Failed to render configuration for pool %s: %v", pool.Name, err))
	mcfgv1.SetMachineConfigPoolCondition(&pool.Status, *sdegraded)
	if _, updateErr := ctrl.client.MachineconfigurationV1().MachineConfigPools().UpdateStatus(pool); updateErr != nil {
		glog.Errorf("Error updating MachineConfigPool %s: %v", pool.Name, updateErr)
	}
	return err
}

// This function will eventually contain a sane garbage collection policy for rendered MachineConfigs;
// see https://github.com/openshift/machine-config-operator/issues/301
// It will probably involve making sure we're only GCing a config after all nodes don't have it
// in either desired or current config.
func (ctrl *Controller) garbageCollectRenderedConfigs(pool *mcfgv1.MachineConfigPool) error {
	// Temporarily until https://github.com/openshift/machine-config-operator/pull/318
	// which depends on the strategy for https://github.com/openshift/machine-config-operator/issues/301
	return nil
}

func (ctrl *Controller) syncGeneratedMachineConfig(pool *mcfgv1.MachineConfigPool, configs []*mcfgv1.MachineConfig) error {
	if len(configs) == 0 {
		return nil
	}

	cc, err := ctrl.ccLister.Get(ctrlcommon.ControllerConfigName)
	if err != nil {
		return err
	}

	generated, err := generateRenderedMachineConfigV3(pool, configs, cc)
	if err != nil {
		return err
	}

	source := []corev1.ObjectReference{}
	for _, cfg := range configs {
		source = append(source, corev1.ObjectReference{Kind: machineconfigKind.Kind, Name: cfg.GetName(), APIVersion: machineconfigKind.GroupVersion().String()})
	}

	_, err = ctrl.mcLister.Get(generated.Name)
	if apierrors.IsNotFound(err) {
		_, err = ctrl.client.MachineconfigurationV1().MachineConfigs().Create(generated)
		glog.V(2).Infof("Generated machineconfig %s from %d configs: %s", generated.Name, len(source), source)
	}
	if err != nil {
		return err
	}

	if pool.Spec.Configuration.Name == generated.Name {
		_, _, err = resourceapply.ApplyMachineConfig(ctrl.client.MachineconfigurationV1(), generated)
		return err
	}

	newPool := pool.DeepCopy()
	newPool.Spec.Configuration.Name = generated.Name
	newPool.Spec.Configuration.Source = source
	// TODO(walters) Use subresource or JSON patch, but the latter isn't supported by the unit test mocks
	pool, err = ctrl.client.MachineconfigurationV1().MachineConfigPools().Update(newPool)
	if err != nil {
		return err
	}
	glog.V(2).Infof("Pool %s: now targeting: %s", pool.Name, pool.Spec.Configuration.Name)

	if err := ctrl.garbageCollectRenderedConfigs(pool); err != nil {
		return err
	}

	return nil
}

// generateRenderedMachineConfigV2 takes all MCs for a given pool and returns a single rendered MC. For ex master-XXXX or worker-XXXX
func generateRenderedMachineConfigV2(pool *mcfgv1.MachineConfigPool, configs []*mcfgv1.MachineConfig, cconfig *mcfgv1.ControllerConfig) (*mcfgv1.MachineConfig, error) {
	// Before merging all MCs for a specific pool, let's make sure MachineConfigs are valid
	for _, config := range configs {
		if err := ctrlcommon.ValidateMachineConfigV2(config.Spec); err != nil {
			return nil, err
		}
	}
	merged, err := ctrlcommon.MergeMachineConfigsV2(configs, cconfig.Spec.OSImageURL)
	if err != nil {
		return nil, err
	}
	hashedName, err := getMachineConfigHashedName(pool, merged)
	if err != nil {
		return nil, err
	}
	oref := metav1.NewControllerRef(pool, controllerKind)

	merged.SetName(hashedName)
	merged.SetOwnerReferences([]metav1.OwnerReference{*oref})
	if merged.Annotations == nil {
		merged.Annotations = map[string]string{}
	}
	merged.Annotations[ctrlcommon.GeneratedByControllerVersionAnnotationKey] = version.Hash

	return merged, nil
}

// generateRenderedMachineConfigV3 takes all MCs for a given pool and returns a single rendered MC. For ex master-XXXX or worker-XXXX
func generateRenderedMachineConfigV3(pool *mcfgv1.MachineConfigPool, configs []*mcfgv1.MachineConfig, cconfig *mcfgv1.ControllerConfig) (*mcfgv1.MachineConfig, error) {
	// Before merging all MCs for a specific pool, let's make sure MachineConfigs are valid
	for _, config := range configs {
		if err := ctrlcommon.ValidateMachineConfigV3(config.Spec); err != nil {
			return nil, err
		}
	}
	merged, err := ctrlcommon.MergeMachineConfigsV3(configs, cconfig.Spec.OSImageURL)
	if err != nil {
		return nil, err
	}
	hashedName, err := getMachineConfigHashedName(pool, merged)
	if err != nil {
		return nil, err
	}
	oref := metav1.NewControllerRef(pool, controllerKind)

	merged.SetName(hashedName)
	merged.SetOwnerReferences([]metav1.OwnerReference{*oref})
	if merged.Annotations == nil {
		merged.Annotations = map[string]string{}
	}
	merged.Annotations[ctrlcommon.GeneratedByControllerVersionAnnotationKey] = version.Hash

	return merged, nil
}

// RunBootstrapV2 runs the render controller in bootstrap mode.
// For each pool, it matches the machineconfigs based on label selector and
// returns the generated machineconfigs and pool with CurrentMachineConfig status field set.
func RunBootstrapV2(pools []*mcfgv1.MachineConfigPool, configs []*mcfgv1.MachineConfig, cconfig *mcfgv1.ControllerConfig) ([]*mcfgv1.MachineConfigPool, []*mcfgv1.MachineConfig, error) {
	var (
		opools   []*mcfgv1.MachineConfigPool
		oconfigs []*mcfgv1.MachineConfig
	)
	for _, pool := range pools {
		pcs, err := getMachineConfigsForPool(pool, configs)
		if err != nil {
			return nil, nil, err
		}

		generated, err := generateRenderedMachineConfigV2(pool, pcs, cconfig)
		if err != nil {
			return nil, nil, err
		}

		pool.Spec.Configuration.Name = generated.Name
		pool.Status.Configuration.Name = generated.Name
		opools = append(opools, pool)
		oconfigs = append(oconfigs, generated)
	}
	return opools, oconfigs, nil
}

// RunBootstrapV3 runs the render controller in bootstrap mode.
// For each pool, it matches the machineconfigs based on label selector and
// returns the generated machineconfigs and pool with CurrentMachineConfig status field set.
func RunBootstrapV3(pools []*mcfgv1.MachineConfigPool, configs []*mcfgv1.MachineConfig, cconfig *mcfgv1.ControllerConfig) ([]*mcfgv1.MachineConfigPool, []*mcfgv1.MachineConfig, error) {
	var (
		opools   []*mcfgv1.MachineConfigPool
		oconfigs []*mcfgv1.MachineConfig
	)
	for _, pool := range pools {
		pcs, err := getMachineConfigsForPool(pool, configs)
		if err != nil {
			return nil, nil, err
		}

		generated, err := generateRenderedMachineConfigV3(pool, pcs, cconfig)
		if err != nil {
			return nil, nil, err
		}

		pool.Spec.Configuration.Name = generated.Name
		pool.Status.Configuration.Name = generated.Name
		opools = append(opools, pool)
		oconfigs = append(oconfigs, generated)
	}
	return opools, oconfigs, nil
}

// getMachineConfigsForPool is called by RunBootstrap and returns configs that match label from configs for a pool.
func getMachineConfigsForPool(pool *mcfgv1.MachineConfigPool, configs []*mcfgv1.MachineConfig) ([]*mcfgv1.MachineConfig, error) {
	selector, err := metav1.LabelSelectorAsSelector(pool.Spec.MachineConfigSelector)
	if err != nil {
		return nil, fmt.Errorf("invalid label selector: %v", err)
	}

	var out []*mcfgv1.MachineConfig

	// If a pool with a nil or empty selector creeps in, it should match nothing
	if selector.Empty() {
		return out, nil
	}
	for idx, config := range configs {
		if selector.Matches(labels.Set(config.Labels)) {
			out = append(out, configs[idx])
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("couldn't find any MachineConfigs for pool: %v", pool.Name)
	}
	return out, nil
}
