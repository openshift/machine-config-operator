package internalreleaseimage

import (
	"context"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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
	configinformersv1 "github.com/openshift/client-go/config/informers/externalversions/config/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/scheme"
	mcfginformersv1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1"
	mcfginformersv1alpha1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1alpha1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	mcfglistersv1alpha1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	templatectrl "github.com/openshift/machine-config-operator/pkg/controller/template"
	"github.com/openshift/machine-config-operator/pkg/osimagestream"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coreinformersv1 "k8s.io/client-go/informers/core/v1"
	corelistersv1 "k8s.io/client-go/listers/core/v1"
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
	eventRecorder record.EventRecorder

	syncHandler                 func(mcp string) error
	enqueueInternalReleaseImage func(*mcfgv1alpha1.InternalReleaseImage)

	iriLister       mcfglistersv1alpha1.InternalReleaseImageLister
	iriListerSynced cache.InformerSynced

	ccLister       mcfglistersv1.ControllerConfigLister
	ccListerSynced cache.InformerSynced

	mcLister       mcfglistersv1.MachineConfigLister
	mcListerSynced cache.InformerSynced

	clusterVersionLister       configlistersv1.ClusterVersionLister
	clusterVersionListerSynced cache.InformerSynced

	secretLister       corelistersv1.SecretLister
	secretListerSynced cache.InformerSynced

	queue workqueue.TypedRateLimitingInterface[string]
}

// New returns a new InternalReleaseImage controller.
func New(
	iriInformer mcfginformersv1alpha1.InternalReleaseImageInformer,
	ccInformer mcfginformersv1.ControllerConfigInformer,
	mcInformer mcfginformersv1.MachineConfigInformer,
	clusterVersionInformer configinformersv1.ClusterVersionInformer,
	secretInformer coreinformersv1.SecretInformer,
	kubeClient clientset.Interface,
	mcfgClient mcfgclientset.Interface,
) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&corev1client.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	ctrl := &Controller{
		client:        mcfgClient,
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

	// Watch IRI secrets (TLS, auth) and the global pull secret. All are served
	// by the cluster-wide KubeInformerFactory, so a single informer covers all
	// namespaces.
	secretInformer.Informer().AddEventHandler(cache.ResourceEventHandlerDetailedFuncs{
		AddFunc:    ctrl.addSecret,
		UpdateFunc: ctrl.updateSecret,
	})

	ctrl.iriLister = iriInformer.Lister()
	ctrl.iriListerSynced = iriInformer.Informer().HasSynced

	ctrl.ccLister = ccInformer.Lister()
	ctrl.ccListerSynced = ccInformer.Informer().HasSynced

	ctrl.mcLister = mcInformer.Lister()
	ctrl.mcListerSynced = mcInformer.Informer().HasSynced

	ctrl.clusterVersionLister = clusterVersionInformer.Lister()
	ctrl.clusterVersionListerSynced = clusterVersionInformer.Informer().HasSynced

	ctrl.secretLister = secretInformer.Lister()
	ctrl.secretListerSynced = secretInformer.Informer().HasSynced

	return ctrl
}

// Run executes the InternalReleaseImage controller.
func (ctrl *Controller) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()

	if !cache.WaitForCacheSync(stopCh, ctrl.iriListerSynced, ctrl.ccListerSynced, ctrl.mcListerSynced, ctrl.clusterVersionListerSynced, ctrl.secretListerSynced) {
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

func (ctrl *Controller) addSecret(obj interface{}, _ bool) {
	secret := obj.(*corev1.Secret)
	if secret.Name != ctrlcommon.InternalReleaseImageTLSSecretName &&
		secret.Name != ctrlcommon.InternalReleaseImageAuthSecretName {
		return
	}
	klog.V(4).Infof("Secret %s added, re-queuing IRI sync", secret.Name)
	ctrl.queue.Add(ctrlcommon.InternalReleaseImageInstanceName)
}

func (ctrl *Controller) updateSecret(_, cur interface{}) {
	secret := cur.(*corev1.Secret)

	if secret.Name != ctrlcommon.InternalReleaseImageTLSSecretName &&
		secret.Name != ctrlcommon.InternalReleaseImageAuthSecretName {
		return
	}

	klog.V(4).Infof("Secret %s updated, re-queuing IRI sync", secret.Name)
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
func (ctrl *Controller) syncInternalReleaseImage(key string) (syncErr error) {
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

	// Check for Deleted InternalReleaseImage and optionally delete finalizers.
	if !iri.DeletionTimestamp.IsZero() {
		if len(iri.GetFinalizers()) > 0 {
			return ctrl.cascadeDelete(iri)
		}
		return nil
	}

	// Update status condition on function exit based on sync result
	defer func() {
		if statusErr := ctrl.updateInternalReleaseImageStatus(iri, syncErr); statusErr != nil {
			if syncErr != nil {
				// Already have a sync error, just log the status update failure
				klog.Warningf("Error updating InternalReleaseImage status: %v", statusErr)
			} else {
				// Sync succeeded but status update failed, propagate the error
				syncErr = fmt.Errorf("failed to update InternalReleaseImage status: %w", statusErr)
			}
		}
	}()

	cconfig, err := ctrl.ccLister.Get(ctrlcommon.ControllerConfigName)
	if err != nil {
		return fmt.Errorf("could not get ControllerConfig: %w", err)
	}

	iriSecret, err := ctrl.secretLister.Secrets(ctrlcommon.MCONamespace).Get(ctrlcommon.InternalReleaseImageTLSSecretName)
	if err != nil {
		return fmt.Errorf("could not get Secret %s: %w", ctrlcommon.InternalReleaseImageTLSSecretName, err)
	}

	iriRegistryCredentialsSecret, err := ctrl.secretLister.Secrets(ctrlcommon.MCONamespace).Get(ctrlcommon.InternalReleaseImageAuthSecretName)
	if err != nil {
		return fmt.Errorf("could not get Secret %s: %w", ctrlcommon.InternalReleaseImageAuthSecretName, err)
	}

	for _, role := range SupportedRoles {
		r := NewRendererByRole(role, iri, iriSecret, iriRegistryCredentialsSecret, cconfig)

		mc, err := ctrl.mcLister.Get(r.GetMachineConfigName())
		isNotFound := errors.IsNotFound(err)
		if err != nil && !isNotFound {
			return fmt.Errorf("could not get MachineConfig: %w", err)
		}
		if isNotFound {
			mc, err = r.CreateEmptyMachineConfig()
			if err != nil {
				return fmt.Errorf("could not create MachineConfig: %w", err)
			}
		}

		err = r.RenderAndSetIgnition(mc)
		if err != nil {
			return fmt.Errorf("could not generate IRI configs: %w", err)
		}
		err = ctrl.createOrUpdateMachineConfig(isNotFound, mc)
		if err != nil {
			return fmt.Errorf("could not create/update MachineConfig: %w", err)
		}
		if err := ctrl.addFinalizerToInternalReleaseImage(iri, mc); err != nil {
			return fmt.Errorf("could not add finalizer: %w", err)
		}
	}

	// Initialize status if empty
	if err := ctrl.initializeInternalReleaseImageStatus(iri); err != nil {
		return fmt.Errorf("could not initialize status: %w", err)
	}

	return nil
}

// initializeInternalReleaseImageStatus initializes the status of an InternalReleaseImage
// if it is empty. It populates the status with release bundle entries from the spec,
// setting the Image field from the current ClusterVersion and adding initial conditions.
func (ctrl *Controller) initializeInternalReleaseImageStatus(iri *mcfgv1alpha1.InternalReleaseImage) error {
	// Only initialize if status is empty and spec has releases
	if len(iri.Status.Releases) != 0 || len(iri.Spec.Releases) == 0 {
		return nil
	}

	klog.V(4).Infof("Initializing status for InternalReleaseImage %s", iri.Name)

	// Get the release payload image from ClusterVersion
	clusterVersion, err := osimagestream.GetClusterVersion(ctrl.clusterVersionLister)
	if err != nil {
		return fmt.Errorf("error getting ClusterVersion for InternalReleaseImage status initialization: %w", err)
	}
	releaseImage, err := osimagestream.GetReleasePayloadImage(clusterVersion)
	if err != nil {
		return fmt.Errorf("error getting Release Image from ClusterVersion for InternalReleaseImage status initialization: %w", err)
	}

	// Build status releases from spec releases
	statusReleases := make([]mcfgv1alpha1.InternalReleaseImageBundleStatus, 0, len(iri.Spec.Releases))
	for _, specRelease := range iri.Spec.Releases {
		statusRelease := mcfgv1alpha1.InternalReleaseImageBundleStatus{
			Name:  specRelease.Name,
			Image: releaseImage,
			Conditions: []metav1.Condition{
				{
					Type:               string(mcfgv1alpha1.InternalReleaseImageConditionTypeAvailable),
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.Now(),
					Reason:             "Installed",
					Message:            "Release bundle is available",
				},
			},
		}
		statusReleases = append(statusReleases, statusRelease)
	}

	iri.Status.Releases = statusReleases

	// Update the status subresource
	if err := retry.RetryOnConflict(updateBackoff, func() error {
		_, err := ctrl.client.MachineconfigurationV1alpha1().InternalReleaseImages().UpdateStatus(context.TODO(), iri, metav1.UpdateOptions{})
		return err
	}); err != nil {
		return fmt.Errorf("failed to update InternalReleaseImage status: %w", err)
	}

	klog.V(2).Infof("Initialized status for InternalReleaseImage %s with %d releases", iri.Name, len(statusReleases))
	return nil
}

// updateInternalReleaseImageStatus updates the InternalReleaseImage status conditions
// based on the provided error. If err is nil, it sets Degraded=False, otherwise Degraded=True.
func (ctrl *Controller) updateInternalReleaseImageStatus(iri *mcfgv1alpha1.InternalReleaseImage, err error) error {
	return retry.RetryOnConflict(updateBackoff, func() error {
		// Get the latest version of the IRI directly from the API server to avoid conflicts
		latestIRI, getErr := ctrl.client.MachineconfigurationV1alpha1().InternalReleaseImages().Get(context.TODO(), iri.Name, metav1.GetOptions{})
		if getErr != nil {
			return getErr
		}
		newIRI := latestIRI.DeepCopy()

		// Prepare the condition based on error state
		var condition metav1.Condition
		if err != nil {
			// Set Degraded=True when there's an error
			condition = metav1.Condition{
				Type:               string(mcfgv1alpha1.InternalReleaseImageStatusConditionTypeDegraded),
				Status:             metav1.ConditionTrue,
				Reason:             "SyncError",
				Message:            fmt.Sprintf("Error syncing InternalReleaseImage: %v", err),
				ObservedGeneration: newIRI.Generation,
			}
		} else {
			// Set Degraded=False when sync is successful
			condition = metav1.Condition{
				Type:               string(mcfgv1alpha1.InternalReleaseImageStatusConditionTypeDegraded),
				Status:             metav1.ConditionFalse,
				Reason:             "AsExpected",
				Message:            "InternalReleaseImage controller sync successful",
				ObservedGeneration: newIRI.Generation,
			}
		}

		// Update the condition and check if it actually changed
		changed := meta.SetStatusCondition(&newIRI.Status.Conditions, condition)
		if !changed {
			// No changes needed, skip the API call
			return nil
		}

		// Update the status subresource only if the condition changed
		_, updateErr := ctrl.client.MachineconfigurationV1alpha1().InternalReleaseImages().UpdateStatus(context.TODO(), newIRI, metav1.UpdateOptions{})
		return updateErr
	})
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
