package internalreleaseimage

import (
	"context"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
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
	mcfginformersv1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1"
	mcfginformersv1alpha1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1alpha1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	mcfglistersv1alpha1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	templatectrl "github.com/openshift/machine-config-operator/pkg/controller/template"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	maxRetries = 15
)

var (
	// controllerKind contains the schema.GroupVersionKind for this controller type.
	controllerKind = mcfgv1alpha1.SchemeGroupVersion.WithKind("InternalReleaseImage")

	updateBackoff = wait.Backoff{
		Steps:    5,
		Duration: 100 * time.Millisecond,
		Jitter:   1.0,
	}
)

// Controller defines the InternalReleaseImage controller.
type Controller struct {
	client        mcfgclientset.Interface
	kubeClient    clientset.Interface
	eventRecorder record.EventRecorder

	syncHandler                 func(mcp string) error
	enqueueInternalReleaseImage func(*mcfgv1alpha1.InternalReleaseImage)

	iriLister       mcfglistersv1alpha1.InternalReleaseImageLister
	iriListerSynced cache.InformerSynced

	ccLister       mcfglistersv1.ControllerConfigLister
	ccListerSynced cache.InformerSynced

	mcLister       mcfglistersv1.MachineConfigLister
	mcListerSynced cache.InformerSynced

	queue workqueue.TypedRateLimitingInterface[string]
}

// New returns a new InternalReleaseImage controller.
func New(
	iriInformer mcfginformersv1alpha1.InternalReleaseImageInformer,
	ccInformer mcfginformersv1.ControllerConfigInformer,
	mcInformer mcfginformersv1.MachineConfigInformer,
	kubeClient clientset.Interface,
	mcfgClient mcfgclientset.Interface,
) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&corev1client.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	ctrl := &Controller{
		client:        mcfgClient,
		kubeClient:    kubeClient,
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

	ccInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: ctrl.updateControllerConfig,
	})

	mcInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: ctrl.updateMachineConfig,
		DeleteFunc: ctrl.deleteMachineConfig,
	})

	ctrl.iriLister = iriInformer.Lister()
	ctrl.iriListerSynced = iriInformer.Informer().HasSynced

	ctrl.ccLister = ccInformer.Lister()
	ctrl.ccListerSynced = ccInformer.Informer().HasSynced

	ctrl.mcLister = mcInformer.Lister()
	ctrl.mcListerSynced = mcInformer.Informer().HasSynced

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

func (ctrl *Controller) updateControllerConfig(old, cur interface{}) {
	oldCfg := old.(*mcfgv1.ControllerConfig)
	curCfg := cur.(*mcfgv1.ControllerConfig)

	if oldCfg.Spec.Images[templatectrl.DockerRegistryKey] == curCfg.Spec.Images[templatectrl.DockerRegistryKey] {
		// Not a relevant update for the IRI controller, it can be skipped
		return
	}

	klog.V(4).Infof("ControllerConfig %s update", oldCfg.Name)
	ctrl.queue.Add(ctrlcommon.InternalReleaseImageInstanceName)
}

func (ctrl *Controller) updateMachineConfig(old, _ interface{}) {
	ctrl.processMachineConfigEvent(old, "MachineConfig %s update")
}

func (ctrl *Controller) deleteMachineConfig(obj interface{}) {
	ctrl.processMachineConfigEvent(obj, "MachineConfig %s delete")
}

func (ctrl *Controller) processMachineConfigEvent(obj interface{}, logMsg string) {
	mc := obj.(*mcfgv1.MachineConfig)

	// Skip any event not related to the InternalReleaseImage machine configs
	if len(mc.OwnerReferences) == 0 || mc.OwnerReferences[0].Kind != controllerKind.Kind {
		return
	}

	klog.V(4).Infof(logMsg, mc.Name)
	ctrl.queue.Add(ctrlcommon.InternalReleaseImageInstanceName)
}

func (ctrl *Controller) enqueue(iri *mcfgv1alpha1.InternalReleaseImage) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(iri)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", iri, err))
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
	iri, err := ctrl.client.MachineconfigurationV1alpha1().InternalReleaseImages().Get(context.TODO(), name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		klog.V(2).Infof("InternalReleaseImage %v has been deleted", key)
		return nil
	}
	if err != nil {
		return err
	}

	// Deep-copy otherwise we are mutating our cache.
	iri = iri.DeepCopy()

	// Check for Deleted InternalReleaseImage and optionally delete finalizers.
	if !iri.DeletionTimestamp.IsZero() {
		if len(iri.GetFinalizers()) > 0 {
			return ctrl.cascadeDelete(iri)
		}
		return nil
	}

	cconfig, err := ctrl.client.MachineconfigurationV1().ControllerConfigs().Get(context.TODO(), ctrlcommon.ControllerConfigName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("could not get ControllerConfig %w", err)
	}

	iriSecret, err := ctrl.kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(context.TODO(), ctrlcommon.InternalReleaseImageTLSSecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("could not get Secret %s: %w", ctrlcommon.InternalReleaseImageTLSSecretName, err)
	}

	for _, role := range SupportedRoles {
		r := NewRendererByRole(role, iri, iriSecret, cconfig)

		mc, err := ctrl.client.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), r.GetMachineConfigName(), metav1.GetOptions{})
		isNotFound := errors.IsNotFound(err)
		if err != nil && !isNotFound {
			return err // syncStatus, could not find MachineConfig
		}
		if isNotFound {
			mc, err = r.CreateEmptyMachineConfig()
			if err != nil {
				return err // syncStatusOnly, could not create MachineConfig
			}
		}

		err = r.RenderAndSetIgnition(mc)
		if err != nil {
			return err // syncStatus, could not generate IRI configs
		}
		err = ctrl.createOrUpdateMachineConfig(isNotFound, mc)
		if err != nil {
			return err // syncStatus, could not Create/Update MachineConfig
		}
		if err := ctrl.addFinalizerToInternalReleaseImage(iri, mc); err != nil {
			return err // syncStatus , could not add finalizers
		}
	}

	return nil
}

func (ctrl *Controller) createOrUpdateMachineConfig(isNotFound bool, mc *mcfgv1.MachineConfig) error {
	return retry.RetryOnConflict(updateBackoff, func() error {
		var err error
		if isNotFound {
			_, err = ctrl.client.MachineconfigurationV1().MachineConfigs().Create(context.TODO(), mc, metav1.CreateOptions{})
		} else {
			_, err = ctrl.client.MachineconfigurationV1().MachineConfigs().Update(context.TODO(), mc, metav1.UpdateOptions{})
		}
		return err
	})
}

func (ctrl *Controller) addFinalizerToInternalReleaseImage(iri *mcfgv1alpha1.InternalReleaseImage, mc *mcfgv1.MachineConfig) error {
	if ctrlcommon.InSlice(mc.Name, iri.Finalizers) {
		return nil
	}

	iri.Finalizers = append(iri.Finalizers, mc.Name)
	_, err := ctrl.client.MachineconfigurationV1alpha1().InternalReleaseImages().Update(context.TODO(), iri, metav1.UpdateOptions{})
	return err
}

func (ctrl *Controller) cascadeDelete(iri *mcfgv1alpha1.InternalReleaseImage) error {
	mcName := iri.GetFinalizers()[0]
	err := ctrl.client.MachineconfigurationV1().MachineConfigs().Delete(context.TODO(), mcName, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	iri.Finalizers = append([]string{}, iri.Finalizers[1:]...)
	_, err = ctrl.client.MachineconfigurationV1alpha1().InternalReleaseImages().Update(context.TODO(), iri, metav1.UpdateOptions{})
	return err
}
