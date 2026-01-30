package render

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/openshift/api/features"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/api/machineconfiguration/v1alpha1"
	opv1 "github.com/openshift/api/operator/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/scheme"
	mcfginformersv1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1"
	mcfginformersv1alpha1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1alpha1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	mcfglistersv1alpha1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1alpha1"
	mcopinformersv1 "github.com/openshift/client-go/operator/informers/externalversions/operator/v1"
	mcoplistersv1 "github.com/openshift/client-go/operator/listers/operator/v1"
	mcoResourceApply "github.com/openshift/machine-config-operator/lib/resourceapply"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/osimagestream"
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
	"k8s.io/klog/v2"
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

	mcpLister           mcfglistersv1.MachineConfigPoolLister
	mcLister            mcfglistersv1.MachineConfigLister
	osImageStreamLister mcfglistersv1alpha1.OSImageStreamLister

	mcpListerSynced           cache.InformerSynced
	mcListerSynced            cache.InformerSynced
	osImageStreamListerSynced cache.InformerSynced

	ccLister       mcfglistersv1.ControllerConfigLister
	ccListerSynced cache.InformerSynced

	crcLister       mcfglistersv1.ContainerRuntimeConfigLister
	crcListerSynced cache.InformerSynced

	mckLister       mcfglistersv1.KubeletConfigLister
	mckListerSynced cache.InformerSynced

	mcopLister       mcoplistersv1.MachineConfigurationLister
	mcopListerSynced cache.InformerSynced

	fgHandler ctrlcommon.FeatureGatesHandler

	queue workqueue.TypedRateLimitingInterface[string]
}

// New returns a new render controller.
func New(
	mcpInformer mcfginformersv1.MachineConfigPoolInformer,
	mcInformer mcfginformersv1.MachineConfigInformer,
	ccInformer mcfginformersv1.ControllerConfigInformer,
	crcInformer mcfginformersv1.ContainerRuntimeConfigInformer,
	mckInformer mcfginformersv1.KubeletConfigInformer,
	mcopInformer mcopinformersv1.MachineConfigurationInformer,
	osImageStreamInformer mcfginformersv1alpha1.OSImageStreamInformer,
	kubeClient clientset.Interface,
	mcfgClient mcfgclientset.Interface,
	featureGatesHandler ctrlcommon.FeatureGatesHandler,
) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&corev1client.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	ctrl := &Controller{
		client:        mcfgClient,
		eventRecorder: ctrlcommon.NamespacedEventRecorder(eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineconfigcontroller-rendercontroller"})),
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "machineconfigcontroller-rendercontroller"}),
		fgHandler: featureGatesHandler,
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
	ctrl.crcLister = crcInformer.Lister()
	ctrl.crcListerSynced = crcInformer.Informer().HasSynced
	ctrl.mckLister = mckInformer.Lister()
	ctrl.mckListerSynced = mckInformer.Informer().HasSynced
	ctrl.mcopLister = mcopInformer.Lister()
	ctrl.mcopListerSynced = mcopInformer.Informer().HasSynced

	if osImageStreamInformer != nil && osimagestream.IsFeatureEnabled(ctrl.fgHandler) {
		ctrl.osImageStreamLister = osImageStreamInformer.Lister()
		ctrl.osImageStreamListerSynced = osImageStreamInformer.Informer().HasSynced
	}
	return ctrl
}

// Run executes the render controller.
func (ctrl *Controller) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()

	listerCaches := []cache.InformerSynced{ctrl.mcpListerSynced, ctrl.mcListerSynced, ctrl.ccListerSynced}

	// OSImageStreams and MCPs fetched only if FeatureGateOSStreams active
	if ctrl.osImageStreamListerSynced != nil {
		listerCaches = append(listerCaches, ctrl.osImageStreamListerSynced)
	}
	if !cache.WaitForCacheSync(stopCh, listerCaches...) {
		return
	}

	klog.Info("Starting MachineConfigController-RenderController")
	defer klog.Info("Shutting down MachineConfigController-RenderController")

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.worker, time.Second, stopCh)
	}

	<-stopCh
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
			utilruntime.HandleError(fmt.Errorf("Couldn't get object from tombstone %#v", obj))
			return
		}
		pool, ok = tombstone.Obj.(*mcfgv1.MachineConfigPool)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Tombstone contained object that is not a MachineConfigPool %#v", obj))
			return
		}
	}
	klog.V(4).Infof("Deleting MachineConfigPool %s", pool.Name)
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
			klog.V(4).Infof("MachineConfig %s added", mc.Name)
			ctrl.enqueueMachineConfigPool(pool)
			return
		}
	}

	pools, err := ctrl.getPoolsForMachineConfig(mc)
	if err != nil {
		klog.Errorf("error finding pools for machineconfig: %v", err)
		return
	}

	klog.V(4).Infof("MachineConfig %s added", mc.Name)
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
		klog.Errorf("machineconfig has changed controller, not allowed.")
		return
	}

	if curControllerRef != nil {
		if pool := ctrl.resolveControllerRef(curControllerRef); pool != nil {
			klog.V(4).Infof("MachineConfig %s updated", curMC.Name)
			ctrl.enqueueMachineConfigPool(pool)
			return
		}
	}

	pools, err := ctrl.getPoolsForMachineConfig(curMC)
	if err != nil {
		klog.Errorf("error finding pools for machineconfig: %v", err)
		return
	}

	klog.V(4).Infof("MachineConfig %s updated", curMC.Name)
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
			klog.V(4).Infof("MachineConfig %s deleted", mc.Name)
			ctrl.enqueueMachineConfigPool(pool)
			return
		}
	}

	pools, err := ctrl.getPoolsForMachineConfig(mc)
	if err != nil {
		klog.Errorf("error finding pools for machineconfig: %v", err)
		return
	}

	klog.V(4).Infof("MachineConfig %s deleted", mc.Name)
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
			return nil, fmt.Errorf("invalid label selector: %w", err)
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
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %w", pool, err))
		return
	}

	ctrl.queue.Add(key)
}

func (ctrl *Controller) enqueueRateLimited(pool *mcfgv1.MachineConfigPool) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pool)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %w", pool, err))
		return
	}

	ctrl.queue.AddRateLimited(key)
}

// enqueueAfter will enqueue a pool after the provided amount of time.
func (ctrl *Controller) enqueueAfter(pool *mcfgv1.MachineConfigPool, after time.Duration) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pool)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %w", pool, err))
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

	err := ctrl.syncHandler(key)
	ctrl.handleErr(err, key)

	return true
}

func (ctrl *Controller) handleErr(err error, key string) {
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

	// Deep-copy otherwise we are mutating our cache.
	// TODO: Deep-copy only when needed.
	pool := machineconfigpool.DeepCopy()
	everything := metav1.LabelSelector{}
	if reflect.DeepEqual(pool.Spec.MachineConfigSelector, &everything) {
		ctrl.eventRecorder.Eventf(pool, corev1.EventTypeWarning, "SelectingAll", "This machineconfigpool is selecting all machineconfigs. A non-empty selector is required.")
		return nil
	}

	selector, err := metav1.LabelSelectorAsSelector(pool.Spec.MachineConfigSelector)
	if err != nil {
		return err
	}

	// TODO(runcom): add tests in render_controller_test.go for this condition
	if err := apihelpers.IsControllerConfigCompleted(ctrlcommon.ControllerConfigName, ctrl.ccLister.Get); err != nil {
		return err
	}

	if err := apihelpers.AreMCGeneratingSubControllersCompletedForPool(ctrl.crcLister.List, ctrl.mckLister.List, pool.Labels); err != nil {
		return err
	}

	mcs, err := ctrl.mcLister.List(selector)
	if err != nil {
		return err
	}
	sort.SliceStable(mcs, func(i, j int) bool { return mcs[i].Name < mcs[j].Name })

	if len(mcs) == 0 {
		return ctrl.syncFailingStatus(pool, fmt.Errorf("no MachineConfigs found matching selector %v", selector))
	}

	if err := ctrl.syncGeneratedMachineConfig(pool, mcs); err != nil {
		klog.Errorf("Error syncing Generated MCFG: %v", err)
		return ctrl.syncFailingStatus(pool, err)
	}
	return ctrl.syncAvailableStatus(pool)
}

func (ctrl *Controller) syncAvailableStatus(pool *mcfgv1.MachineConfigPool) error {
	if apihelpers.IsMachineConfigPoolConditionFalse(pool.Status.Conditions, mcfgv1.MachineConfigPoolRenderDegraded) {
		return nil
	}
	sdegraded := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolRenderDegraded, corev1.ConditionFalse, "", "")
	apihelpers.SetMachineConfigPoolCondition(&pool.Status, *sdegraded)
	if _, err := ctrl.client.MachineconfigurationV1().MachineConfigPools().UpdateStatus(context.TODO(), pool, metav1.UpdateOptions{}); err != nil {
		return err
	}
	return nil
}

func (ctrl *Controller) syncFailingStatus(pool *mcfgv1.MachineConfigPool, err error) error {
	sdegraded := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolRenderDegraded, corev1.ConditionTrue, "", fmt.Sprintf("Failed to render configuration for pool %s: %v", pool.Name, err))
	apihelpers.SetMachineConfigPoolCondition(&pool.Status, *sdegraded)
	if _, updateErr := ctrl.client.MachineconfigurationV1().MachineConfigPools().UpdateStatus(context.TODO(), pool, metav1.UpdateOptions{}); updateErr != nil {
		klog.Errorf("Error updating MachineConfigPool %s: %v", pool.Name, updateErr)
	}
	return err
}

// This function will eventually contain a sane garbage collection policy for rendered MachineConfigs;
// see https://github.com/openshift/machine-config-operator/issues/301
// It will probably involve making sure we're only GCing a config after all nodes don't have it
// in either desired or current config.
func (ctrl *Controller) garbageCollectRenderedConfigs(_ *mcfgv1.MachineConfigPool) error {
	// Temporarily until https://github.com/openshift/machine-config-operator/pull/318
	// which depends on the strategy for https://github.com/openshift/machine-config-operator/issues/301
	return nil
}

func (ctrl *Controller) getRenderedMachineConfig(pool *mcfgv1.MachineConfigPool, configs []*mcfgv1.MachineConfig, cc *mcfgv1.ControllerConfig, osImageStreamSet *v1alpha1.OSImageStreamSet) (*mcfgv1.MachineConfig, error) {
	// If we don't yet have a rendered MachineConfig on the pool, we cannot
	// perform reconciliation. So we must solely generate the rendered
	// MachineConfig.
	if pool.Spec.Configuration.Name == "" {
		return generateRenderedMachineConfig(pool, configs, cc, osImageStreamSet)
	}

	// The pool has a rendered MachineConfig, so we can do more advanced
	// reconciliation as we generate it.
	currentMC, err := ctrl.mcLister.Get(pool.Spec.Configuration.Name)

	// Degenerate case: When the renderedMC that the MCP is currently pointing to is deleted
	if apierrors.IsNotFound(err) {
		generated, err := generateRenderedMachineConfig(pool, configs, cc, osImageStreamSet)
		if err != nil {
			return nil, err
		}
		return generated, nil
	}
	if err != nil {
		return nil, fmt.Errorf("could not get current MachineConfig %s for MachineConfigPool %s: %w", pool.Spec.Configuration.Name, pool.Name, err)
	}

	var mcop opv1.MachineConfiguration
	if ctrl.fgHandler != nil && ctrl.fgHandler.Enabled(features.FeatureGateIrreconcilableMachineConfig) {
		mcopPtr, err := ctrlcommon.GetIrreconcilableOverrides(ctrl.mcopLister)
		if err != nil {
			return nil, err
		}
		mcop = *mcopPtr
	}

	return generateAndValidateRenderedMachineConfig(currentMC, pool, configs, cc, &mcop.Spec.IrreconcilableValidationOverrides, osImageStreamSet)
}

func (ctrl *Controller) getOSImageStreamForPool(pool *mcfgv1.MachineConfigPool) (*v1alpha1.OSImageStreamSet, error) {
	if !osimagestream.IsFeatureEnabled(ctrl.fgHandler) || ctrl.osImageStreamLister == nil {
		return nil, nil
	}

	imageStream, err := ctrl.osImageStreamLister.Get(ctrlcommon.ClusterInstanceNameOSImageStream)
	if err != nil {
		return nil, fmt.Errorf("could not get OSImageStream for pool %s: %w", pool.Name, err)
	}

	imageStreamSet, err := osimagestream.GetOSImageStreamSetByName(imageStream, pool.Spec.OSImageStream.Name)
	if err != nil {
		return nil, fmt.Errorf("could not get OSImageStreamSet for pool %s: %w", pool.Name, err)
	}
	return imageStreamSet, nil
}

func (ctrl *Controller) syncGeneratedMachineConfig(pool *mcfgv1.MachineConfigPool, configs []*mcfgv1.MachineConfig) error {
	if len(configs) == 0 {
		return nil
	}

	cc, err := ctrl.ccLister.Get(ctrlcommon.ControllerConfigName)
	if err != nil {
		return err
	}

	osImageStreamSet, err := ctrl.getOSImageStreamForPool(pool)
	if err != nil {
		return err
	}

	generated, err := ctrl.getRenderedMachineConfig(pool, configs, cc, osImageStreamSet)
	if err != nil {
		return fmt.Errorf("could not generate rendered MachineConfig: %w", err)
	}

	// Collect metric when OSImageURL was overridden
	var isOSImageURLOverridden bool
	if generated.Spec.OSImageURL != ctrlcommon.GetBaseImageContainer(&cc.Spec, osImageStreamSet) {
		ctrlcommon.OSImageURLOverride.WithLabelValues(pool.Name).Set(1)
		isOSImageURLOverridden = true
	} else {
		// Reset metric when OSImageURL has not been overridden
		ctrlcommon.OSImageURLOverride.WithLabelValues(pool.Name).Set(0)
	}

	source := getMachineConfigRefs(configs)

	_, err = ctrl.mcLister.Get(generated.Name)
	if apierrors.IsNotFound(err) {
		_, err = ctrl.client.MachineconfigurationV1().MachineConfigs().Create(context.TODO(), generated, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		if isOSImageURLOverridden {
			ctrl.eventRecorder.Eventf(generated, corev1.EventTypeNormal, "OSImageURLOverridden", "OSImageURL was overridden via machineconfig in %s (was: %s is: %s)", generated.Name, cc.Spec.OSImageURL, generated.Spec.OSImageURL)
		}
		klog.V(2).Infof("Generated machineconfig %s from %d configs: %s", generated.Name, len(source), source)
		ctrl.eventRecorder.Eventf(pool, corev1.EventTypeNormal, "RenderedConfigGenerated", "%s successfully generated (release version: %s, controller version: %s)",
			generated.Name, generated.Annotations[ctrlcommon.ReleaseImageVersionAnnotationKey], generated.Annotations[ctrlcommon.GeneratedByControllerVersionAnnotationKey])
	}
	if err != nil {
		return err
	}

	newPool := pool.DeepCopy()
	newPool.Spec.Configuration.Source = source

	if pool.Spec.Configuration.Name == generated.Name {
		_, _, err = mcoResourceApply.ApplyMachineConfig(ctrl.client.MachineconfigurationV1(), generated)
		if err != nil {
			return err
		}
		_, err = ctrl.client.MachineconfigurationV1().MachineConfigPools().Update(context.TODO(), newPool, metav1.UpdateOptions{})
		return err
	}

	newPool.Spec.Configuration.Name = generated.Name
	// TODO(walters) Use subresource or JSON patch, but the latter isn't supported by the unit test mocks
	pool, err = ctrl.client.MachineconfigurationV1().MachineConfigPools().Update(context.TODO(), newPool, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	klog.V(2).Infof("Pool %s: now targeting: %s", pool.Name, pool.Spec.Configuration.Name)
	ctrlcommon.UpdateStateMetric(ctrlcommon.MCCSubControllerState, "machine-config-controller-render", "Sync Machine Config Pool with new MC", pool.Name)
	return ctrl.garbageCollectRenderedConfigs(pool)
}

// generateRenderedMachineConfig takes all MCs for a given pool and returns a single rendered MC. For ex master-XXXX or worker-XXXX
func generateRenderedMachineConfig(pool *mcfgv1.MachineConfigPool, configs []*mcfgv1.MachineConfig, cconfig *mcfgv1.ControllerConfig, osImageStreamSet *v1alpha1.OSImageStreamSet) (*mcfgv1.MachineConfig, error) {
	// Suppress rendered config generation until a corresponding new controller can roll out too.
	// https://bugzilla.redhat.com/show_bug.cgi?id=1879099
	if genver, ok := cconfig.Annotations[daemonconsts.GeneratedByVersionAnnotationKey]; ok {
		if genver != version.Raw {
			return nil, fmt.Errorf("Ignoring controller config generated from %s (my version: %s)", genver, version.Raw)
		}
	} else {
		return nil, fmt.Errorf("Ignoring controller config generated without %s annotation (my version: %s)", daemonconsts.GeneratedByVersionAnnotationKey, version.Raw)
	}

	// As an additional check, we should wait until all MCO-owned configs have been regenerated by the newest controller,
	// before attempting to generate a new MC. See: https://issues.redhat.com/browse/OCPBUGS-3821
	for _, config := range configs {
		generatedByControllerVersion := config.Annotations[ctrlcommon.GeneratedByControllerVersionAnnotationKey]
		if generatedByControllerVersion != "" && generatedByControllerVersion != version.Hash {
			return nil, fmt.Errorf("Ignoring MC %s generated by older version %s (my version: %s)", config.Name, generatedByControllerVersion, version.Hash)
		}
	}

	if cconfig.Spec.BaseOSContainerImage == "" {
		klog.Warningf("No BaseOSContainerImage set")
	}

	// Before merging all MCs for a specific pool, let's make sure MachineConfigs are valid
	for _, config := range configs {
		if err := ctrlcommon.ValidateMachineConfig(config.Spec); err != nil {
			return nil, err
		}
	}

	merged, err := ctrlcommon.MergeMachineConfigs(configs, cconfig, osImageStreamSet)

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
	merged.Annotations[ctrlcommon.ReleaseImageVersionAnnotationKey] = cconfig.Annotations[ctrlcommon.ReleaseImageVersionAnnotationKey]

	// The operator needs to know the user overrode this, so it knows if it needs to skip the
	// OSImageURL check during upgrade -- if the user took over managing OS upgrades this way,
	// the operator shouldn't stop the rest of the upgrade from progressing/completing.
	if merged.Spec.OSImageURL != ctrlcommon.GetBaseImageContainer(&cconfig.Spec, osImageStreamSet) {
		merged.Annotations[ctrlcommon.OSImageURLOverriddenKey] = "true"
		// Log a warning if the osImageURL is set using a tag instead of a digest
		if !strings.Contains(merged.Spec.OSImageURL, "sha256:") {
			klog.Warningf("OSImageURL %q for MachineConfig %s is set using a tag instead of a digest. It is highly recommended to use a digest", merged.Spec.OSImageURL, merged.Name)
		}
	}

	return merged, nil
}

// This function takes into account the current MachineConfig that is present
// on the MachineConfigPool. It first attempts to verify that the newly
// generated rendered MachineConfig is reconcilable against the current
// MachineConfig. If it is not reconcilable, the component MachineConfigs that
// were used to generate the rendered MachineConfig will be evaluated
// one-by-one to identify which MachineConfig is not reconcilable. The name of
// the unreconcilable MachineConfig is included in the returned error.
func generateAndValidateRenderedMachineConfig(
	currentMC *mcfgv1.MachineConfig,
	pool *mcfgv1.MachineConfigPool,
	configs []*mcfgv1.MachineConfig,
	cconfig *mcfgv1.ControllerConfig,
	validationOverrides *opv1.IrreconcilableValidationOverrides,
	osImageStreamSet *v1alpha1.OSImageStreamSet) (*mcfgv1.MachineConfig, error) {
	source := getMachineConfigRefs(configs)
	klog.V(4).Infof("Considering %d configs %s for MachineConfig generation", len(source), source)

	generated, err := generateRenderedMachineConfig(pool, configs, cconfig, osImageStreamSet)
	if err != nil {
		return nil, err
	}

	klog.V(4).Infof("Considering generated MachineConfig %q", generated.Name)

	if fullErr := ctrlcommon.IsRenderedConfigReconcilable(currentMC, generated, validationOverrides); fullErr != nil {
		fullMsg := fullErr.Error()

		// trying to match error reason suffix
		for _, cfg := range configs {
			singleErr := ctrlcommon.IsComponentConfigsReconcilable(
				currentMC, []*mcfgv1.MachineConfig{cfg},
				validationOverrides,
			)
			if singleErr == nil {
				continue
			}
			singleMsg := singleErr.Error()

			// split off prefix
			parts := strings.SplitN(singleMsg, ": ", 2)
			if len(parts) == 2 {
				reason := parts[1]
				if strings.Contains(fullMsg, reason) {
					klog.V(4).Infof("match found for base %q on reason %q", cfg.Name, reason)
					return nil, fmt.Errorf("reconciliation failed between current config %q and base config %q: %s", currentMC.Name, cfg.Name, reason)
				}
			}
		}

		// fallback
		compErr := ctrlcommon.IsComponentConfigsReconcilable(currentMC, configs, validationOverrides)
		return nil, fmt.Errorf("render reconciliation error: %v; component errors: %v", fullErr, compErr)
	}

	klog.V(4).Infof("Rendered MachineConfig %q is reconcilable against %q", generated.Name, currentMC.Name)

	return generated, nil
}

// RunBootstrap runs the render controller in bootstrap mode.
// For each pool, it matches the machineconfigs based on label selector and
// returns the generated machineconfigs and pool with CurrentMachineConfig status field set.
func RunBootstrap(pools []*mcfgv1.MachineConfigPool, configs []*mcfgv1.MachineConfig, cconfig *mcfgv1.ControllerConfig, osImageStream *v1alpha1.OSImageStream) ([]*mcfgv1.MachineConfigPool, []*mcfgv1.MachineConfig, error) {
	var (
		opools   []*mcfgv1.MachineConfigPool
		oconfigs []*mcfgv1.MachineConfig
	)
	for _, pool := range pools {
		pcs, err := getMachineConfigsForPool(pool, configs)
		if err != nil {
			return nil, nil, err
		}
		var osImageStreamSet *v1alpha1.OSImageStreamSet
		if osImageStream != nil {
			osImageStreamSet, err = osimagestream.GetOSImageStreamSetByName(osImageStream, pool.Spec.OSImageStream.Name)
			if err != nil {
				return nil, nil, fmt.Errorf("couldn't get the OSImageStream for pool %s %w", pool.Name, err)
			}
		}

		generated, err := generateRenderedMachineConfig(pool, pcs, cconfig, osImageStreamSet)
		if err != nil {
			return nil, nil, err
		}

		source := getMachineConfigRefs(configs)

		pool.Spec.Configuration.Name = generated.Name
		pool.Spec.Configuration.Source = source
		pool.Status.Configuration.Name = generated.Name
		pool.Status.Configuration.Source = source
		opools = append(opools, pool)
		oconfigs = append(oconfigs, generated)
	}
	return opools, oconfigs, nil
}

// getMachineConfigsForPool is called by RunBootstrap and returns configs that match label from configs for a pool.
func getMachineConfigsForPool(pool *mcfgv1.MachineConfigPool, configs []*mcfgv1.MachineConfig) ([]*mcfgv1.MachineConfig, error) {
	selector, err := metav1.LabelSelectorAsSelector(pool.Spec.MachineConfigSelector)
	if err != nil {
		return nil, fmt.Errorf("invalid label selector: %w", err)
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

func getMachineConfigRefs(mcfgs []*mcfgv1.MachineConfig) []corev1.ObjectReference {
	refs := []corev1.ObjectReference{}

	for _, cfg := range mcfgs {
		refs = append(refs, corev1.ObjectReference{Kind: machineconfigKind.Kind, Name: cfg.GetName(), APIVersion: machineconfigKind.GroupVersion().String()})
	}

	return refs
}
