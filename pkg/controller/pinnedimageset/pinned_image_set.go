package pinnedimageset

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
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

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/scheme"
	mcfginformersv1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1"
	mcfginformersv1alpha1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1alpha1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	mcfglistersv1alpha1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

const (
	// maxRetries is the number of times a machineconfig pool will be retried before it is dropped out of the queue.
	// With the current rate-limiter in use (5ms*2^(maxRetries-1)) the following numbers represent the times
	// a machineconfig pool is going to be requeued:
	//
	// 5ms, 10ms, 20ms, 40ms, 80ms, 160ms, 320ms, 640ms, 1.3s, 2.6s, 5.1s, 10.2s, 20.4s, 41s, 82s
	maxRetries = 15

	// delay is a pause to avoid churn in PinnedImageSets
	delay = 5 * time.Second
)

// Controller defines the pinned image set controller.
type Controller struct {
	client        mcfgclientset.Interface
	eventRecorder record.EventRecorder

	syncHandler              func(mcp string) error
	enqueueMachineConfigPool func(*mcfgv1.MachineConfigPool)

	mcpLister       mcfglistersv1.MachineConfigPoolLister
	mcpListerSynced cache.InformerSynced

	imageSetLister mcfglistersv1alpha1.PinnedImageSetLister
	imageSetSynced cache.InformerSynced

	queue workqueue.TypedRateLimitingInterface[string]
}

// New returns a new pinned image set controller.
func New(
	imageSetInformer mcfginformersv1alpha1.PinnedImageSetInformer,
	mcpInformer mcfginformersv1.MachineConfigPoolInformer,
	kubeClient clientset.Interface,
	mcfgClient mcfgclientset.Interface,
) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&corev1client.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	ctrl := &Controller{
		client:        mcfgClient,
		eventRecorder: ctrlcommon.NamespacedEventRecorder(eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineconfigcontroller-pinnedimagesetcontroller"})),
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "machineconfigcontroller-pinnedimagesetcontroller"}),
	}

	ctrl.syncHandler = ctrl.syncMachineConfigPool
	ctrl.enqueueMachineConfigPool = ctrl.enqueueDefault

	// this must be done after the enqueueMachineConfigPool is configured to
	// avoid panics when the event handler is called.
	mcpInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addMachineConfigPool,
		UpdateFunc: ctrl.updateMachineConfigPool,
		DeleteFunc: ctrl.deleteMachineConfigPool,
	})

	imageSetInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addPinnedImageSet,
		UpdateFunc: ctrl.updatePinnedImageSet,
		DeleteFunc: ctrl.deletePinnedImageSet,
	})

	ctrl.mcpLister = mcpInformer.Lister()
	ctrl.mcpListerSynced = mcpInformer.Informer().HasSynced

	ctrl.imageSetLister = imageSetInformer.Lister()
	ctrl.imageSetSynced = imageSetInformer.Informer().HasSynced

	return ctrl
}

// Run executes the pinned image set controller.
func (ctrl *Controller) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()

	if !cache.WaitForCacheSync(stopCh, ctrl.mcpListerSynced, ctrl.imageSetSynced) {
		return
	}

	klog.Info("Starting MachineConfigController-PinnedImageSetController")
	defer klog.Info("Shutting down MachineConfigController-PinnedImageSetController")

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.worker, time.Second, stopCh)
	}

	<-stopCh
}

func (ctrl *Controller) addMachineConfigPool(obj interface{}) {
	pool := obj.(*mcfgv1.MachineConfigPool)
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
			utilruntime.HandleError(fmt.Errorf("failed to get object from tombstone %#v", obj))
			return
		}
		pool, ok = tombstone.Obj.(*mcfgv1.MachineConfigPool)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not a MachineConfigPool %#v", obj))
			return
		}
	}
	klog.V(4).Infof("Deleting MachineConfigPool %s", pool.Name)
}

func (ctrl *Controller) addPinnedImageSet(obj interface{}) {
	imageSet := obj.(*mcfgv1alpha1.PinnedImageSet)
	if imageSet.DeletionTimestamp != nil {
		ctrl.deletePinnedImageSet(imageSet)
		return
	}

	pools, err := ctrl.getPoolsForPinnedImageSet(imageSet)
	if err != nil {
		klog.Errorf("error finding pools for pinned image set: %v", err)
		return
	}

	klog.V(4).Infof("PinnedImageSet %s added", imageSet.Name)
	for _, p := range pools {
		ctrl.enqueueMachineConfigPool(p)
	}
}

func (ctrl *Controller) updatePinnedImageSet(old, cur interface{}) {
	oldImageSet := old.(*mcfgv1alpha1.PinnedImageSet)
	newImageSet := cur.(*mcfgv1alpha1.PinnedImageSet)

	pools, err := ctrl.getPoolsForPinnedImageSet(newImageSet)
	if err != nil {
		klog.Errorf("error finding pools for pinned image set: %v", err)
		return
	}

	klog.V(4).Infof("PinnedImageSet %s updated", newImageSet.Name)
	if triggerPinnedImageSetChange(oldImageSet, newImageSet) {
		for _, p := range pools {
			ctrl.enqueueMachineConfigPool(p)
		}
	}
}

func triggerPinnedImageSetChange(old, newPinnedImageSet *mcfgv1alpha1.PinnedImageSet) bool {
	if old.DeletionTimestamp != newPinnedImageSet.DeletionTimestamp {
		return true
	}
	if !reflect.DeepEqual(old, newPinnedImageSet) {
		return true
	}
	return false
}

func (ctrl *Controller) deletePinnedImageSet(obj interface{}) {
	imageSet, ok := obj.(*mcfgv1alpha1.PinnedImageSet)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("failed to get object from tombstone %#v", obj))
			return
		}
		imageSet, ok = tombstone.Obj.(*mcfgv1alpha1.PinnedImageSet)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not a PinnedImageSet %#v", obj))
			return
		}
	}

	pools, err := ctrl.getPoolsForPinnedImageSet(imageSet)
	if err != nil {
		klog.Errorf("error finding pools for pinned image set: %v", err)
		return
	}

	klog.V(4).Infof("PinnedImageSet %s deleted", imageSet.Name)
	for _, p := range pools {
		ctrl.enqueueMachineConfigPool(p)
	}
}

func (ctrl *Controller) getPoolsForPinnedImageSet(imageSet *mcfgv1alpha1.PinnedImageSet) ([]*mcfgv1.MachineConfigPool, error) {
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
		if selector.Empty() || !selector.Matches(labels.Set(imageSet.Labels)) {
			continue
		}

		pools = append(pools, p)
	}

	if len(pools) == 0 {
		return nil, fmt.Errorf("could not find any MachineConfigPool set for PinnedImageSet %s with labels: %v", imageSet.Name, imageSet.Labels)
	}
	return pools, nil
}

// enqueueAfter will enqueue a pool after the provided amount of time.
func (ctrl *Controller) enqueueAfter(pool *mcfgv1.MachineConfigPool, after time.Duration) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pool)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("failed to get key for object %#v: %w", pool, err))
		return
	}

	ctrl.queue.AddAfter(key, after)
}

// enqueueDefault calls a default enqueue function
func (ctrl *Controller) enqueueDefault(pool *mcfgv1.MachineConfigPool) {
	ctrl.enqueueAfter(pool, delay)
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
	mcp, err := ctrl.mcpLister.Get(name)
	if errors.IsNotFound(err) {
		klog.V(2).Infof("MachineConfigPool %v has been deleted", key)
		return nil
	}
	if err != nil {
		return err
	}

	pool := mcp.DeepCopy()
	everything := metav1.LabelSelector{}
	if reflect.DeepEqual(pool.Spec.MachineConfigSelector, &everything) {
		return nil
	}

	selector, err := metav1.LabelSelectorAsSelector(pool.Spec.MachineConfigSelector)
	if err != nil {
		return err
	}

	imageSets, err := ctrl.imageSetLister.List(selector)
	if err != nil {
		return err
	}
	sort.SliceStable(imageSets, func(i, j int) bool { return imageSets[i].Name < imageSets[j].Name })

	if err := ctrl.syncPinnedImageSets(pool, imageSets); err != nil {
		klog.Errorf("Error syncing pinned image sets: %v", err)
		return ctrl.syncFailingStatus(pool, err)
	}

	return ctrl.syncAvailableStatus(pool)
}

func (ctrl *Controller) syncAvailableStatus(pool *mcfgv1.MachineConfigPool) error {
	if apihelpers.IsMachineConfigPoolConditionFalse(pool.Status.Conditions, mcfgv1.MachineConfigPoolPinnedImageSetsDegraded) {
		return nil
	}
	sdegraded := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolPinnedImageSetsDegraded, corev1.ConditionFalse, "", "")
	apihelpers.SetMachineConfigPoolCondition(&pool.Status, *sdegraded)
	if _, err := ctrl.client.MachineconfigurationV1().MachineConfigPools().UpdateStatus(context.TODO(), pool, metav1.UpdateOptions{}); err != nil {
		return err
	}
	return nil
}

func (ctrl *Controller) syncFailingStatus(pool *mcfgv1.MachineConfigPool, err error) error {
	sdegraded := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolPinnedImageSetsDegraded, corev1.ConditionTrue, "", fmt.Sprintf("Failed to populate pinned image sets for pool %s: %v", pool.Name, err))
	apihelpers.SetMachineConfigPoolCondition(&pool.Status, *sdegraded)
	if _, updateErr := ctrl.client.MachineconfigurationV1().MachineConfigPools().UpdateStatus(context.TODO(), pool, metav1.UpdateOptions{}); updateErr != nil {
		klog.Errorf("Error updating MachineConfigPool %s: %v", pool.Name, updateErr)
	}
	return err
}

func (ctrl *Controller) syncPinnedImageSets(pool *mcfgv1.MachineConfigPool, imageSets []*mcfgv1alpha1.PinnedImageSet) error {
	pinnedImageSetRefs := make([]mcfgv1.PinnedImageSetRef, 0, len(imageSets))
	for _, imageSet := range imageSets {
		pinnedImageSetRefs = append(pinnedImageSetRefs, mcfgv1.PinnedImageSetRef{
			Name: imageSet.Name,
		})
	}

	if len(pinnedImageSetRefs) == 0 {
		pinnedImageSetRefs = nil
	}

	newPool := pool.DeepCopy()
	newPool.Spec.PinnedImageSets = pinnedImageSetRefs
	_, err := ctrl.client.MachineconfigurationV1().MachineConfigPools().Update(context.TODO(), newPool, metav1.UpdateOptions{})
	return err
}
