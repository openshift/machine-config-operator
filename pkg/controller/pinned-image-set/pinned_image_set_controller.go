package pinnedimageset

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformersv1 "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	coreclientsetv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	corelisterv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/scheme"
	mcfginformersv1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/helpers"
)

const (
	DegradedCondition = "Degraded"

	ProgressingReason = "Progressing"
	ErrorReason       = "SynchronizationError"
	ExpectedReason    = "AsExpected"

	maxRetries = 18
)

var (
	errNoMatchingPool          = errors.New("no matching MachineConfigPool found for PinnedImageSet")
	errNodePrefetchNotComplete = errors.New("prefetch is not yet complete for node")
	errMultiplePinnedImageSets = errors.New("multiple PinnedImageSets defined for MachineConfigPool")
)

type Controller struct {
	kubeClient    kubernetes.Interface
	mcfgClient    mcfgclientset.Interface
	eventRecorder record.EventRecorder

	syncHandler           func(string) error
	enqueuePinnedImageSet func(*mcfgv1.PinnedImageSet)

	nodeLister       corelisterv1.NodeLister
	nodeListerSynced cache.InformerSynced

	imageSetLister mcfglistersv1.PinnedImageSetLister
	imageSetSynced cache.InformerSynced

	mcpLister       mcfglistersv1.MachineConfigPoolLister
	mcpListerSynced cache.InformerSynced

	featureGatesAccessor featuregates.FeatureGateAccess
	queue                workqueue.RateLimitingInterface

	waitBackOff wait.Backoff
}

func New(
	imageSetInformer mcfginformersv1.PinnedImageSetInformer,
	mcpInformer mcfginformersv1.MachineConfigPoolInformer,
	nodeInformer coreinformersv1.NodeInformer,
	kubeClient kubernetes.Interface,
	mcfgClient mcfgclientset.Interface,
	fgAccessor featuregates.FeatureGateAccess,
) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&coreclientsetv1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	ctrl := &Controller{
		kubeClient:    kubeClient,
		mcfgClient:    mcfgClient,
		eventRecorder: ctrlcommon.NamespacedEventRecorder(eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineconfigcontroller-pinnedimagesetcontroller"})),
		queue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "machineconfigcontroller-pinnedimagesetcontroller"),
		waitBackOff: wait.Backoff{
			Steps:    5,
			Duration: 100 * time.Millisecond,
			Jitter:   1.0,
		},
	}

	imageSetInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addPinnedImageSet,
		UpdateFunc: ctrl.updatePinnedImageSet,
		DeleteFunc: ctrl.deletePinnedImageSet,
	})

	ctrl.syncHandler = ctrl.syncPinnedImageSet
	ctrl.enqueuePinnedImageSet = ctrl.enqueue

	ctrl.nodeLister = nodeInformer.Lister()
	ctrl.nodeListerSynced = nodeInformer.Informer().HasSynced

	ctrl.imageSetLister = imageSetInformer.Lister()
	ctrl.imageSetSynced = imageSetInformer.Informer().HasSynced

	ctrl.mcpLister = mcpInformer.Lister()
	ctrl.mcpListerSynced = mcpInformer.Informer().HasSynced

	ctrl.featureGatesAccessor = fgAccessor

	return ctrl
}

// PinnedImageSetNodeStatus represents the status of a node in relation to a PinnedImageSet
type PinnedImageSetNodeStatus string

const (
	PinnedImageSetNodeStatusUnknown     PinnedImageSetNodeStatus = "Unknown"
	PinnedImageSetNodeStatusProgressing PinnedImageSetNodeStatus = "Progressing"
	PinnedImageSetNodeStatusComplete    PinnedImageSetNodeStatus = "Complete"
)

func (ctrl *Controller) syncPinnedImageSet(key string) error {
	startTime := time.Now()
	klog.V(4).Infof("Started syncing PinnedImageSet %q (%v)", key, startTime)
	defer func() {
		duration := time.Since(startTime)
		klog.V(4).Infof("Finished syncing PinnedImageSet %q (%v)", key, duration)
	}()

	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}

	imageSet, err := ctrl.imageSetLister.Get(name)
	if apierrors.IsNotFound(err) {
		klog.V(2).Infof("PinnedImageSet %v has been deleted", key)
		return nil
	}
	if err != nil {
		return err
	}

	imageSet = imageSet.DeepCopy()

	err = ctrl.ensurePinnedImageSet(imageSet)
	if err != nil {
		condition := metav1.Condition{
			Type:               DegradedCondition,
			Status:             metav1.ConditionTrue,
			Reason:             ErrorReason,
			Message:            err.Error(),
			LastTransitionTime: metav1.NewTime(time.Now()),
		}
		statusErr := ctrl.updateStatus(imageSet, condition)
		if statusErr != nil {
			klog.Warningf("Error updating status for PinnedImageSet %v: %v", key, statusErr)
		}
		return err
	}

	requeue, err := ctrl.ensureNodeImagePrefetch(imageSet)
	if requeue {
		return err
	}
	if err != nil {
		condition := metav1.Condition{
			Type:               DegradedCondition,
			Status:             metav1.ConditionTrue,
			Reason:             ErrorReason,
			Message:            err.Error(),
			LastTransitionTime: metav1.NewTime(time.Now()),
		}
		statusErr := ctrl.updateStatus(imageSet, condition)
		if statusErr != nil {
			klog.Warningf("Error updating status for PinnedImageSet %v: %v", key, statusErr)
		}
		return err
	}

	condition := metav1.Condition{
		Type:               DegradedCondition,
		Status:             metav1.ConditionFalse,
		Reason:             ExpectedReason,
		LastTransitionTime: metav1.NewTime(time.Now()),
	}
	if err = ctrl.updateStatus(imageSet, condition); err != nil {
		klog.Warningf("Error updating status for PinnedImageSet %v: %v", key, err)
		return err
	}

	return nil
}

func (ctrl *Controller) ensurePinnedImageSet(imageSet *mcfgv1.PinnedImageSet) error {
	if imageSet.DeletionTimestamp != nil {
		return nil
	}
	mcpPools, err := ctrlcommon.GetPoolsForPinnedImageSet(ctrl.mcpLister, imageSet)
	if err != nil {
		return err
	}
	if len(mcpPools) == 0 {
		return fmt.Errorf("%w: %s", errNoMatchingPool, imageSet.Name)
	}

	for _, pool := range mcpPools {
		// only one PinnedImageSet allowed per MachineConfigPool
		sets, err := ctrlcommon.GetPinnedImageSetsForPool(ctrl.imageSetLister, pool)
		if err != nil {
			return err
		}
		if len(sets) > 1 {
			return fmt.Errorf("%w: %s", errMultiplePinnedImageSets, pool.Name)
		}
	}

	return nil
}

func (ctrl *Controller) ensureNodeImagePrefetch(imageSet *mcfgv1.PinnedImageSet) (bool, error) {
	nodes, err := ctrl.getNodesForPinnedImageSet(imageSet)
	if err != nil {
		return false, err
	}

	nodesInProgress, nodesComplete, nodesUnknown := 0, 0, 0
	for _, node := range nodes {
		s := ctrl.getNodePinnedImageSetStatus(node, imageSet)
		switch s {
		case PinnedImageSetNodeStatusUnknown:
			nodesUnknown++
		case PinnedImageSetNodeStatusProgressing:
			nodesInProgress++
		case PinnedImageSetNodeStatusComplete:
			nodesComplete++
		default:
			return false, fmt.Errorf("unknown status for node %s: %s", node.Name, s)
		}
	}

	if nodesComplete < len(nodes) {
		condition := metav1.Condition{
			Type:               DegradedCondition,
			Status:             metav1.ConditionTrue,
			Reason:             ProgressingReason,
			LastTransitionTime: metav1.NewTime(time.Now()),
			Message: fmt.Sprintf("Prefetching images nodes total %d: in progress: %d: unknown: %d: complete: %d",
				len(nodes), nodesInProgress, nodesUnknown, nodesComplete,
			),
		}
		statusErr := ctrl.updateStatus(imageSet, condition)
		if statusErr != nil {
			klog.Warningf("Error updating status for PinnedImageSet %v: %v", imageSet.Name, statusErr)
		}
		return true, fmt.Errorf("%w: %s", errNodePrefetchNotComplete, imageSet.Name)
	}

	return false, nil
}

func (ctrl *Controller) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()

	if !cache.WaitForCacheSync(
		stopCh,
		ctrl.imageSetSynced,
		ctrl.mcpListerSynced,
		ctrl.nodeListerSynced,
	) {
		return
	}

	klog.Infof("Starting PinnedImageSet controller")
	defer klog.Infof("Shutting down PinnedImageSet controller")

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.worker, time.Second, stopCh)
	}

	<-stopCh
}

func (ctrl *Controller) getNodePinnedImageSetStatus(node *corev1.Node, imageSet *mcfgv1.PinnedImageSet) PinnedImageSetNodeStatus {
	currentNodeImageSetUID := node.Annotations[constants.MachineConfigDaemonPinnedImageSetPrefetchCurrentAnnotationKey]
	desiredNodeImageSetUID := node.Annotations[constants.MachineConfigDaemonPinnedImageSetPrefetchDesiredAnnotationKey]
	reasonNodeImageSet := node.Annotations[constants.MachineConfigDaemonPinnedImageSetPrefetchReasonAnnotationKey]

	imageSetUID := string(imageSet.UID)
	if string(imageSetUID) != desiredNodeImageSetUID {
		return PinnedImageSetNodeStatusUnknown
	}

	// if the prefetch manager is in progress the reason will be set
	if currentNodeImageSetUID == desiredNodeImageSetUID && len(reasonNodeImageSet) == 0 {
		return PinnedImageSetNodeStatusComplete
	}

	return PinnedImageSetNodeStatusProgressing
}

func (ctrl *Controller) getNodesForPinnedImageSet(imageSet *mcfgv1.PinnedImageSet) ([]*corev1.Node, error) {
	pools, err := ctrlcommon.GetPoolsForPinnedImageSet(ctrl.mcpLister, imageSet)
	if err != nil {
		return nil, err
	}

	var nodeList []*corev1.Node
	for _, pool := range pools {
		// the nodes returned are primary so the list should be unique
		nodes, err := helpers.GetNodesForPool(ctrl.mcpLister, ctrl.nodeLister, pool)
		if err != nil {
			return nil, err
		}
		nodeList = append(nodeList, nodes...)
	}

	return nodeList, nil
}

func (ctrl *Controller) updatePinnedImageSet(oldObj, newObj interface{}) {
	oldImageSet := oldObj.(*mcfgv1.PinnedImageSet)
	newImageSet := newObj.(*mcfgv1.PinnedImageSet)

	if triggerObjectChange(oldImageSet, newImageSet) {
		klog.V(4).Infof("Update PinnedImageSet %s", oldImageSet.Name)
		ctrl.enqueuePinnedImageSet(newImageSet)
	}
}

func (ctrl *Controller) addPinnedImageSet(obj interface{}) {
	imageSet := obj.(*mcfgv1.PinnedImageSet)
	klog.V(4).Infof("Adding PinnedImageSet %s", imageSet.Name)
	ctrl.enqueuePinnedImageSet(imageSet)
}

func (ctrl *Controller) deletePinnedImageSet(obj interface{}) {
	imageSet, ok := obj.(*mcfgv1.PinnedImageSet)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("couldn't get object from tombstone %#v", obj))
			return
		}
		imageSet, ok = tombstone.Obj.(*mcfgv1.PinnedImageSet)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not a PinnedImageSet %#v", obj))
			return
		}
	}
	klog.V(4).Infof("Deleting PinnedImageSet %s", imageSet.Name)
}

func (ctrl *Controller) enqueue(cfg *mcfgv1.PinnedImageSet) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(cfg)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", cfg, err))
		return
	}
	ctrl.queue.Add(key)
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
		klog.V(4).Infof("Requeue PinnedImageSet %v: %v", key, err)
		ctrl.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	klog.V(4).Infof("Dropping PinnedImageSet %q out of the queue: %v", key, err)
	ctrl.queue.Forget(key)
	ctrl.queue.AddAfter(key, 1*time.Minute)
}

func (ctrl *Controller) updateStatus(
	imageSet *mcfgv1.PinnedImageSet,
	condition metav1.Condition,
) error {
	return retry.RetryOnConflict(ctrl.waitBackOff, func() error {
		existingImageSet, err := ctrl.imageSetLister.Get(imageSet.Name)
		if err != nil {
			return err
		}

		newImageSet := existingImageSet.DeepCopy()
		if imageSet.GetGeneration() != imageSet.Status.ObservedGeneration {
			imageSet.Status.ObservedGeneration = imageSet.GetGeneration()
		}

		v1helpers.SetCondition(&newImageSet.Status.Conditions, condition)
		if equality.Semantic.DeepEqual(existingImageSet.Status, newImageSet.Status) {
			klog.V(4).Infof("No status change for PinnedImageSet %s", imageSet.Name)
			return nil
		}

		_, err = ctrl.mcfgClient.MachineconfigurationV1().PinnedImageSets().UpdateStatus(context.TODO(), newImageSet, metav1.UpdateOptions{})
		return err
	})
}

func triggerObjectChange(old, new *mcfgv1.PinnedImageSet) bool {
	if old.DeletionTimestamp != new.DeletionTimestamp {
		return true
	}
	if !reflect.DeepEqual(old.Spec, new.Spec) {
		return true
	}
	return false
}
