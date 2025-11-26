package internalreleaseimage

import (
	"context"

	"fmt"
	"reflect"
	"time"

	"github.com/clarketm/json"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/scheme"
	mcfginformersv1alpha1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1alpha1"
	mcfglistersv1alpha1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/version"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	iriRole              = "master"
	iriMachineConfigName = "02-master-internalreleaseimage"
)

// controllerKind contains the schema.GroupVersionKind for this controller type.
var controllerKind = mcfgv1alpha1.SchemeGroupVersion.WithKind("InternalReleaseImage")

var updateBackoff = wait.Backoff{
	Steps:    5,
	Duration: 100 * time.Millisecond,
	Jitter:   1.0,
}

// Controller defines the render controller.
type Controller struct {
	client        mcfgclientset.Interface
	eventRecorder record.EventRecorder

	syncHandler                 func(mcp string) error
	enqueueInternalReleaseImage func(*mcfgv1alpha1.InternalReleaseImage)

	iriLister       mcfglistersv1alpha1.InternalReleaseImageLister
	iriListerSynced cache.InformerSynced

	queue workqueue.TypedRateLimitingInterface[string]
}

// New returns a new InternalReleaseImage controller.
func New(
	iriInformer mcfginformersv1alpha1.InternalReleaseImageInformer,
	kubeClient clientset.Interface,
	mcfgClient mcfgclientset.Interface,
) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&corev1client.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	ctrl := &Controller{
		client: mcfgClient,

		eventRecorder: ctrlcommon.NamespacedEventRecorder(eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineconfigcontroller-internalreleaseimagecontroller"})),
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "machineconfigcontroller-internalreleaseimagecontroller"}),
	}

	ctrl.syncHandler = ctrl.syncInternalReleaseImage
	ctrl.enqueueInternalReleaseImage = ctrl.enqueue

	iriInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addInternalReleaseImage,
		UpdateFunc: ctrl.updateInternalReleaseImage,
		DeleteFunc: ctrl.deleteInternalReleaseImage,
	})

	ctrl.iriLister = iriInformer.Lister()
	ctrl.iriListerSynced = iriInformer.Informer().HasSynced

	return ctrl
}

// Run executes the InternalReleaseImage controller.
func (ctrl *Controller) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()

	if !cache.WaitForCacheSync(stopCh, ctrl.iriListerSynced) {
		return
	}

	klog.Info("Starting MachineConfigController-InternalReleaseImageController")
	defer klog.Info("Shutting down MachineConfigController-InternalReleaseImageController")

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.worker, time.Second, stopCh)
	}

	<-stopCh
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
		klog.V(2).Infof("Error syncing internalreleaseimage %v: %v", key, err)
		ctrl.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	klog.V(2).Infof("Dropping internalreleaseimage %q out of the queue: %v", key, err)
	ctrl.queue.Forget(key)
	ctrl.queue.AddAfter(key, 1*time.Minute)
}

func (ctrl *Controller) addInternalReleaseImage(obj interface{}) {
	iri := obj.(*mcfgv1alpha1.InternalReleaseImage)
	klog.V(4).Infof("Adding InternalReleaseImage %s", iri.Name)
	ctrl.enqueueInternalReleaseImage(iri)
}

func (ctrl *Controller) updateInternalReleaseImage(old, cur interface{}) {
	oldInternalReleaseImage := old.(*mcfgv1alpha1.InternalReleaseImage)
	newInternalReleaseImage := cur.(*mcfgv1alpha1.InternalReleaseImage)

	if ctrl.internalReleaseImageChanged(oldInternalReleaseImage, newInternalReleaseImage) {
		klog.V(4).Infof("mcfgv1alpha1.InternalReleaseImage %s updated", newInternalReleaseImage.Name)
		ctrl.enqueueInternalReleaseImage(newInternalReleaseImage)
	}
}

func (ctrl *Controller) internalReleaseImageChanged(old, newIRI *mcfgv1alpha1.InternalReleaseImage) bool {
	if old.DeletionTimestamp != newIRI.DeletionTimestamp {
		return true
	}
	if !reflect.DeepEqual(old.Spec, newIRI.Spec) {
		return true
	}
	return false
}

func (ctrl *Controller) deleteInternalReleaseImage(obj interface{}) {
	iri, ok := obj.(*mcfgv1alpha1.InternalReleaseImage)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("failed to get object from tombstone %#v", obj))
			return
		}
		iri, ok = tombstone.Obj.(*mcfgv1alpha1.InternalReleaseImage)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not a InternalReleaseImage %#v", obj))
			return
		}
	}

	klog.V(4).Infof("InternalReleaseImage %s deleted", iri.Name)
	ctrl.enqueueInternalReleaseImage(iri)
}

func (ctrl *Controller) enqueue(cfg *mcfgv1alpha1.InternalReleaseImage) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(cfg)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", cfg, err))
		return
	}
	ctrl.queue.Add(key)
}

// syncInternalReleaseImage will sync the InternalReleaseImage with the given key.
// This function is not meant to be invoked concurrently with the same key.
// nolint: gocyclo
func (ctrl *Controller) syncInternalReleaseImage(key string) error {
	startTime := time.Now()
	klog.V(4).Infof("Started syncing InternalReleaseImage %q (%v)", key, startTime)
	defer func() {
		klog.V(4).Infof("Finished syncing InternalReleaseImage %q (%v)", key, time.Since(startTime))
	}()

	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}

	// Fetch the InternalReleaseImage
	iri, err := ctrl.iriLister.Get(name)
	if errors.IsNotFound(err) {
		klog.V(2).Infof("InternalReleaseImage %v has been deleted", key)
		return nil
	}
	if err != nil {
		return err
	}

	// Deep-copy otherwise we are mutating our cache.
	iri = iri.DeepCopy()

	// Check for Deleted InternalReleaseImage and optionally delete finalizers
	if iri.DeletionTimestamp != nil {
		if len(iri.GetFinalizers()) > 0 {
			return ctrl.cascadeDelete(iri)
		}
		return nil
	}

	// Create or update InternalReleaseImage MachineConfig
	mc, err := ctrl.client.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), iriMachineConfigName, metav1.GetOptions{})
	isNotFound := errors.IsNotFound(err)
	if err != nil && !isNotFound {
		return err //syncstatus, could not find MachineConfig
	}

	if isNotFound {
		mc, err = generateInternalReleaseImageMachineConfig(iri)
		if err != nil {
			return err //syncstatus, could not create MachineConfig
		}
	}

	if err := retry.RetryOnConflict(updateBackoff, func() error {
		var err error
		if isNotFound {
			_, err = ctrl.client.MachineconfigurationV1().MachineConfigs().Create(context.TODO(), mc, metav1.CreateOptions{})
		} else {
			_, err = ctrl.client.MachineconfigurationV1().MachineConfigs().Update(context.TODO(), mc, metav1.UpdateOptions{})
		}
		return err
	}); err != nil {
		return err //synstatus, could not Create/Update MachineConfig
	}

	// Add finalizer to the InternalReleaseImage
	if err := ctrl.addFinalizerToInternalReleaseImage(iri, mc); err != nil {
		return err //syncStatus , could not add finalizers
	}

	return nil
}

func (ctrl *Controller) addFinalizerToInternalReleaseImage(iri *mcfgv1alpha1.InternalReleaseImage, mc *mcfgv1.MachineConfig) error {
	return retry.RetryOnConflict(updateBackoff, func() error {
		newiri, err := ctrl.iriLister.Get(iri.Name)
		if errors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}

		curJSON, err := json.Marshal(newiri)
		if err != nil {
			return err
		}

		iriTmp := newiri.DeepCopy()
		// Only append the mc name if it is already not in the list of finalizers.
		// When we update an existing iri, the generation number increases causing
		// a resync to happen. When this happens, the mc name is the same, so we don't
		// want to add duplicate entries to the list of finalizers.
		if !ctrlcommon.InSlice(mc.Name, iriTmp.Finalizers) {
			iriTmp.Finalizers = append(iriTmp.Finalizers, mc.Name)
		}

		modJSON, err := json.Marshal(iriTmp)
		if err != nil {
			return err
		}

		patch, err := jsonmergepatch.CreateThreeWayJSONMergePatch(curJSON, modJSON, curJSON)
		if err != nil {
			return err
		}
		return ctrl.patchInternalReleaseImage(newiri.Name, patch)
	})
}

func (ctrl *Controller) popFinalizerFromInternalReleaseImage(iri *mcfgv1alpha1.InternalReleaseImage) error {
	return retry.RetryOnConflict(updateBackoff, func() error {
		newiri, err := ctrl.iriLister.Get(iri.Name)
		if errors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}

		curJSON, err := json.Marshal(newiri)
		if err != nil {
			return err
		}

		iriTmp := newiri.DeepCopy()
		iriTmp.Finalizers = append([]string{}, iri.Finalizers[1:]...)
		modJSON, err := json.Marshal(iriTmp)
		if err != nil {
			return err
		}

		patch, err := jsonmergepatch.CreateThreeWayJSONMergePatch(curJSON, modJSON, curJSON)
		if err != nil {
			return err
		}
		return ctrl.patchInternalReleaseImage(newiri.Name, patch)
	})
}

func (ctrl *Controller) patchInternalReleaseImage(name string, patch []byte) error {
	_, err := ctrl.client.MachineconfigurationV1alpha1().InternalReleaseImages().Patch(context.TODO(), name, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

func (ctrl *Controller) cascadeDelete(iri *mcfgv1alpha1.InternalReleaseImage) error {
	if len(iri.GetFinalizers()) == 0 {
		return nil
	}
	mcName := iri.GetFinalizers()[0]
	err := ctrl.client.MachineconfigurationV1().MachineConfigs().Delete(context.TODO(), mcName, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	return ctrl.popFinalizerFromInternalReleaseImage(iri)
}

func generateInternalReleaseImageMachineConfig(iri *mcfgv1alpha1.InternalReleaseImage) (*mcfgv1.MachineConfig, error) {
	ignCfg, err := ctrlcommon.TranspileCoreOSConfigToIgn(nil, []string{iriRegistryServiceTemplate})
	if err != nil {
		return nil, fmt.Errorf("error transpiling CoreOS config to Ignition config: %w", err)
	}

	mcfg, err := ctrlcommon.MachineConfigFromIgnConfig(iriRole, iriMachineConfigName, ignCfg)
	if err != nil {
		return nil, fmt.Errorf("error creating MachineConfig from Ignition config: %w", err)
	}

	cref := metav1.NewControllerRef(iri, controllerKind)
	mcfg.SetOwnerReferences([]metav1.OwnerReference{*cref})
	mcfg.SetAnnotations(map[string]string{
		ctrlcommon.GeneratedByControllerVersionAnnotationKey: version.Hash,
	})

	return mcfg, nil
}
