package internalreleaseimage

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
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

// ErrInconsistentIRICredentials is returned by getDeployedIRICredentials when
// different MachineConfigPools have different IRI credentials in their rendered
// MachineConfigs. This indicates a rollout is in progress and the rotation
// state machine should not advance.
var ErrInconsistentIRICredentials = fmt.Errorf("IRI credentials differ across MachineConfigPool rendered configs")

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

	mcpLister       mcfglistersv1.MachineConfigPoolLister
	mcpListerSynced cache.InformerSynced

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
	mcpInformer mcfginformersv1.MachineConfigPoolInformer,
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

	mcpInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: ctrl.updateMachineConfigPool,
	})

	secretInformer.Informer().AddEventHandler(cache.ResourceEventHandlerDetailedFuncs{
		UpdateFunc: ctrl.updateSecret,
	})

	ctrl.iriLister = iriInformer.Lister()
	ctrl.iriListerSynced = iriInformer.Informer().HasSynced

	ctrl.ccLister = ccInformer.Lister()
	ctrl.ccListerSynced = ccInformer.Informer().HasSynced

	ctrl.mcLister = mcInformer.Lister()
	ctrl.mcListerSynced = mcInformer.Informer().HasSynced

	ctrl.mcpLister = mcpInformer.Lister()
	ctrl.mcpListerSynced = mcpInformer.Informer().HasSynced

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

	if !cache.WaitForCacheSync(stopCh, ctrl.iriListerSynced, ctrl.ccListerSynced, ctrl.mcListerSynced, ctrl.mcpListerSynced, ctrl.clusterVersionListerSynced, ctrl.secretListerSynced) {
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

func (ctrl *Controller) updateMachineConfigPool(old, cur interface{}) {
	oldPool := old.(*mcfgv1.MachineConfigPool)
	curPool := cur.(*mcfgv1.MachineConfigPool)

	// Only trigger a sync when the pool's rollout status changes, which
	// indicates progress relevant to credential rotation phases. We check
	// both machine counts and configuration name convergence to detect all
	// rollout transitions.
	if oldPool.Spec.Configuration.Name == curPool.Spec.Configuration.Name &&
		oldPool.Status.Configuration.Name == curPool.Status.Configuration.Name &&
		oldPool.Status.MachineCount == curPool.Status.MachineCount &&
		oldPool.Status.UpdatedMachineCount == curPool.Status.UpdatedMachineCount &&
		oldPool.Status.ReadyMachineCount == curPool.Status.ReadyMachineCount {
		return
	}

	klog.V(4).Infof("MachineConfigPool %s update status changed", curPool.Name)
	ctrl.queue.Add(ctrlcommon.InternalReleaseImageInstanceName)
}

func (ctrl *Controller) updateSecret(obj, _ interface{}) {
	secret := obj.(*corev1.Secret)

	// Skip any event not related to the InternalReleaseImage secrets
	if secret.Name != ctrlcommon.InternalReleaseImageTLSSecretName &&
		secret.Name != ctrlcommon.InternalReleaseImageAuthSecretName {
		return
	}

	klog.V(4).Infof("Secret %s update", secret.Name)
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

	cconfig, err := ctrl.ccLister.Get(ctrlcommon.ControllerConfigName)
	if err != nil {
		return fmt.Errorf("could not get ControllerConfig %w", err)
	}

	iriSecret, err := ctrl.secretLister.Secrets(ctrlcommon.MCONamespace).Get(ctrlcommon.InternalReleaseImageTLSSecretName)
	if err != nil {
		return fmt.Errorf("could not get Secret %s: %w", ctrlcommon.InternalReleaseImageTLSSecretName, err)
	}

	// Auth secret may not exist during upgrades from non-auth clusters
	iriAuthSecret, err := ctrl.secretLister.Secrets(ctrlcommon.MCONamespace).Get(ctrlcommon.InternalReleaseImageAuthSecretName)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("could not get Secret %s: %w", ctrlcommon.InternalReleaseImageAuthSecretName, err)
	}
	if iriAuthSecret != nil {
		// DeepCopy to avoid mutating the informer cache when updating htpasswd
		iriAuthSecret = iriAuthSecret.DeepCopy()
	}

	// Handle credential rotation using the desired-vs-current pattern.
	// The auth secret holds the desired password; the pull secret holds the current
	// active credentials. When they differ, a multi-phase rotation is performed
	// to avoid authentication failures during rolling MachineConfig updates.
	// See docs/iri-auth-credential-rotation.md for the full design.
	if iriAuthSecret != nil {
		if err := ctrl.reconcileAuthCredentials(iriAuthSecret, cconfig); err != nil {
			return fmt.Errorf("failed to reconcile auth credentials: %w", err)
		}
	}

	for _, role := range SupportedRoles {
		r := NewRendererByRole(role, iri, iriSecret, iriAuthSecret, cconfig)

		mc, err := ctrl.mcLister.Get(r.GetMachineConfigName())
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

	// Initialize status if empty
	if err := ctrl.initializeInternalReleaseImageStatus(iri); err != nil {
		return err
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

// reconcileAuthCredentials implements the desired-vs-current credential rotation
// pattern. It compares the desired password (from the auth secret) with the
// current password (from the pull secret) and manages a safe multi-phase rotation:
//
//  1. Passwords match: credentials are in sync, ensure htpasswd uses a single entry.
//  2. No IRI entry in pull secret: first-time setup, merge credentials directly.
//  3. Passwords differ: rotation needed — generate dual htpasswd with both old and
//     new credentials (using different generation usernames), update the auth secret's
//     htpasswd field, and wait for all MachineConfigPools to finish rolling out before
//     updating the pull secret with the new credentials.
//
// The "current" state is determined by reading the pull secret from the rendered
// MachineConfig (what's actually deployed on nodes), not from the API object,
// to avoid race conditions during rollout.
//
// See docs/iri-auth-credential-rotation.md for the full design.
func (ctrl *Controller) reconcileAuthCredentials(authSecret *corev1.Secret, cconfig *mcfgv1.ControllerConfig) error {
	desiredPassword := string(authSecret.Data["password"])
	if desiredPassword == "" {
		return nil
	}

	if cconfig.Spec.DNS == nil {
		return fmt.Errorf("ControllerConfig DNS is not set")
	}
	baseDomain := cconfig.Spec.DNS.Spec.BaseDomain

	// Read the deployed pull secret from the rendered MachineConfig to determine
	// what credentials are actually on nodes, rather than the API object which
	// may not have been rolled out yet.
	deployedUsername, deployedPassword, err := ctrl.getDeployedIRICredentials(baseDomain)
	if err == ErrInconsistentIRICredentials {
		// Different pools have different IRI credentials in their rendered MCs.
		// This means a pull secret rollout is in progress. Wait for it to
		// complete before making any rotation decisions.
		klog.V(4).Infof("IRI credentials inconsistent across pools, waiting for rollout to converge")
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to read deployed IRI credentials: %w", err)
	}

	// First-time setup: no IRI entry deployed yet, merge directly into pull secret.
	if deployedPassword == "" {
		klog.Infof("No IRI credentials deployed, performing first-time merge into pull secret")
		return ctrl.mergeIRIAuthIntoPullSecret(cconfig, authSecret)
	}

	// Phase 1 / Phase 2: deployed password differs from desired — rotation needed.
	// Phase 1 deploys a dual htpasswd with both old and new credentials.
	// Phase 2 updates the pull secret after all MCPs finish rolling out.
	if deployedPassword != desiredPassword {
		return ctrl.reconcileCredentialRotation(authSecret, cconfig, deployedUsername, deployedPassword, desiredPassword)
	}

	// Phase 3 / steady state: deployed password matches desired.
	// Clean up dual htpasswd left over from a completed rotation, or repair
	// htpasswd if it was manually edited.
	return ctrl.reconcileCredentialsInSync(authSecret, deployedUsername, desiredPassword)
}

// reconcileCredentialsInSync handles Phase 3 / steady state where the deployed
// password matches the desired password. It cleans up dual htpasswd entries left
// over from a completed rotation and repairs htpasswd if the hash doesn't match.
func (ctrl *Controller) reconcileCredentialsInSync(authSecret *corev1.Secret, deployedUsername, desiredPassword string) error {
	currentHtpasswd := string(authSecret.Data["htpasswd"])
	var needsUpdate bool

	// Clean up dual htpasswd entries left over from a completed rotation.
	if strings.Count(currentHtpasswd, "\n") > 1 {
		klog.Infof("Cleaning up dual htpasswd after completed rotation (username=%s)", deployedUsername)
		needsUpdate = true
	}

	// Repair htpasswd if it was manually edited to contain an invalid hash
	// (e.g., user directly modified the auth secret's htpasswd field).
	if !HtpasswdHasValidEntry(currentHtpasswd, deployedUsername, desiredPassword) {
		klog.Infof("Repairing htpasswd: hash does not match desired password (username=%s)", deployedUsername)
		needsUpdate = true
	}

	if needsUpdate {
		singleHtpasswd, err := GenerateHtpasswdEntry(deployedUsername, desiredPassword)
		if err != nil {
			return fmt.Errorf("failed to generate single htpasswd: %w", err)
		}
		authSecret.Data["htpasswd"] = []byte(singleHtpasswd)
		if _, err := ctrl.kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Update(
			context.TODO(), authSecret, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("failed to update auth secret htpasswd: %w", err)
		}
	}
	return nil
}

// reconcileCredentialRotation handles Phase 1 and Phase 2 where the deployed
// password differs from the desired password:
//   - Phase 1: Deploy a dual htpasswd with both old and new credentials so both
//     are accepted during the rolling MachineConfig update.
//   - Phase 2: After all MachineConfigPools finish rolling out, update the pull
//     secret with the new credentials. On subsequent syncs, once the new pull
//     secret is deployed, reconcileCredentialsInSync (Phase 3) will clean up the
//     dual htpasswd.
func (ctrl *Controller) reconcileCredentialRotation(authSecret *corev1.Secret, cconfig *mcfgv1.ControllerConfig, deployedUsername, deployedPassword, desiredPassword string) error {
	klog.Infof("IRI auth credential rotation detected: deployed password differs from desired password")

	newUsername := NextIRIUsername(deployedUsername)
	baseDomain := cconfig.Spec.DNS.Spec.BaseDomain
	currentHtpasswd := string(authSecret.Data["htpasswd"])

	// Check whether the htpasswd already has valid dual entries for both the
	// deployed credentials and the desired credentials. We use
	// bcrypt.CompareHashAndPassword rather than string comparison because bcrypt
	// salts differ on every generation. This also handles mid-rotation password
	// changes: if the desired password changed since the dual htpasswd was
	// written, the new entry's hash won't match and we regenerate.
	//
	// The deployed credentials (oldUser, oldPass) are always preserved as one of
	// the two htpasswd entries. The pull secret (what clients use to authenticate)
	// always contains these old credentials. So even if the desired password
	// changes mid-rotation, clients always authenticate successfully against
	// every htpasswd version.
	hasDualEntries := HtpasswdHasValidEntry(currentHtpasswd, deployedUsername, deployedPassword) &&
		HtpasswdHasValidEntry(currentHtpasswd, newUsername, desiredPassword)

	// Phase 1: deploy dual htpasswd.
	if !hasDualEntries {
		dualHtpasswd, err := GenerateDualHtpasswd(deployedUsername, deployedPassword, newUsername, desiredPassword)
		if err != nil {
			return fmt.Errorf("failed to generate dual htpasswd for rotation: %w", err)
		}
		authSecret.Data["htpasswd"] = []byte(dualHtpasswd)
		if _, err := ctrl.kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Update(
			context.TODO(), authSecret, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("failed to update auth secret with dual htpasswd: %w", err)
		}
		klog.Infof("Updated auth secret htpasswd with dual credentials (%s + %s) for rotation", deployedUsername, newUsername)
		// Return so the MC render on the next sync picks up the new htpasswd
		return nil
	}

	// Phase 2: dual htpasswd is deployed — check if ALL pools have rolled out.
	// Only then is it safe to update the pull secret.
	allUpdated, err := ctrl.areAllPoolsUpdated()
	if err != nil {
		return fmt.Errorf("failed to check MCP status: %w", err)
	}

	if !allUpdated {
		klog.V(4).Infof("Waiting for MachineConfigPool rollout to complete before advancing credential rotation")
		return nil
	}

	// All nodes have the dual htpasswd — safe to update the pull secret.
	// After the pull secret API object is updated, the template controller will
	// re-render and trigger another rollout. On subsequent syncs, the deployed
	// pull secret (from the rendered MC) will eventually match the desired password,
	// at which point Phase 3 handles cleanup.
	klog.Infof("MCP rollout complete, updating pull secret with new credentials (username=%s)", newUsername)
	pullSecret, err := ctrl.kubeClient.CoreV1().Secrets(ctrlcommon.OpenshiftConfigNamespace).Get(
		context.TODO(), ctrlcommon.GlobalPullSecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("could not get pull-secret: %w", err)
	}

	mergedBytes, err := MergeIRIAuthIntoPullSecretWithUsername(
		pullSecret.Data[corev1.DockerConfigJsonKey], newUsername, desiredPassword, baseDomain)
	if err != nil {
		return fmt.Errorf("failed to merge new credentials into pull secret: %w", err)
	}

	pullSecret.Data[corev1.DockerConfigJsonKey] = mergedBytes
	if _, err := ctrl.kubeClient.CoreV1().Secrets(ctrlcommon.OpenshiftConfigNamespace).Update(
		context.TODO(), pullSecret, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to update pull secret with rotated credentials: %w", err)
	}
	klog.Infof("Updated pull secret with rotated IRI credentials (username=%s)", newUsername)
	return nil
}

// getDeployedIRICredentials reads the IRI registry credentials from the pull
// secret that is actually deployed on nodes, by parsing the rendered
// MachineConfig for each pool. This is more accurate than reading the
// openshift-config/pull-secret API object, which may not have been rolled out yet.
//
// All pools must have consistent IRI credentials. If different pools have
// different passwords (e.g., one pool's rendered MC was re-rendered with a new
// pull secret while another pool still has the old one), this returns
// ErrInconsistentIRICredentials. The caller should treat this as "rollout in
// progress" and avoid advancing the rotation state machine.
func (ctrl *Controller) getDeployedIRICredentials(baseDomain string) (username, password string, err error) {
	pools, err := ctrl.mcpLister.List(labels.Everything())
	if err != nil {
		return "", "", fmt.Errorf("failed to list MachineConfigPools: %w", err)
	}

	var foundUsername, foundPassword string

	for _, pool := range pools {
		renderedMCName := pool.Status.Configuration.Name
		if renderedMCName == "" {
			continue
		}

		renderedMC, err := ctrl.mcLister.Get(renderedMCName)
		if err != nil {
			return "", "", fmt.Errorf("failed to get rendered MachineConfig %s: %w", renderedMCName, err)
		}

		ignConfig, err := ctrlcommon.ParseAndConvertConfig(renderedMC.Spec.Config.Raw)
		if err != nil {
			return "", "", fmt.Errorf("failed to parse ignition from rendered MachineConfig %s: %w", renderedMCName, err)
		}

		pullSecretData, err := ctrlcommon.GetIgnitionFileDataByPath(&ignConfig, "/var/lib/kubelet/config.json")
		if err != nil {
			return "", "", fmt.Errorf("failed to extract pull secret from rendered MachineConfig %s: %w", renderedMCName, err)
		}
		if pullSecretData == nil {
			continue
		}

		u, p := ExtractIRICredentialsFromPullSecret(pullSecretData, baseDomain)
		if p == "" {
			continue
		}

		if foundPassword == "" {
			foundUsername = u
			foundPassword = p
		} else if foundPassword != p || foundUsername != u {
			return "", "", ErrInconsistentIRICredentials
		}
	}

	return foundUsername, foundPassword, nil
}

// areAllPoolsUpdated checks whether all MachineConfigPools have finished
// rolling out (all machines are at the desired configuration). Both master
// and worker pools must be checked because worker nodes are also clients
// of the IRI registry via api-int.
func (ctrl *Controller) areAllPoolsUpdated() (bool, error) {
	pools, err := ctrl.mcpLister.List(labels.Everything())
	if err != nil {
		return false, err
	}

	for _, pool := range pools {
		// Ensure the pool has converged on its desired configuration.
		// Without this check, machine counts could report "complete" for
		// the previous config while the new config is still being rolled out.
		if pool.Spec.Configuration.Name == "" || pool.Status.Configuration.Name == "" {
			return false, nil
		}
		if pool.Spec.Configuration.Name != pool.Status.Configuration.Name {
			return false, nil
		}
		if pool.Status.MachineCount != pool.Status.UpdatedMachineCount {
			return false, nil
		}
		if pool.Status.MachineCount != pool.Status.ReadyMachineCount {
			return false, nil
		}
	}

	return true, nil
}

func (ctrl *Controller) mergeIRIAuthIntoPullSecret(cconfig *mcfgv1.ControllerConfig, authSecret *corev1.Secret) error {
	password := string(authSecret.Data["password"])
	if password == "" {
		return nil
	}

	if cconfig.Spec.DNS == nil {
		return fmt.Errorf("ControllerConfig DNS is not set")
	}
	baseDomain := cconfig.Spec.DNS.Spec.BaseDomain

	// Fetch current pull secret from openshift-config
	pullSecret, err := ctrl.kubeClient.CoreV1().Secrets(ctrlcommon.OpenshiftConfigNamespace).Get(
		context.TODO(), ctrlcommon.GlobalPullSecretName, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		klog.V(4).Infof("Pull secret %s/%s not found, will retry on next sync", ctrlcommon.OpenshiftConfigNamespace, ctrlcommon.GlobalPullSecretName)
		return nil
	}
	if err != nil {
		return fmt.Errorf("could not get pull-secret: %w", err)
	}

	mergedBytes, err := MergeIRIAuthIntoPullSecret(pullSecret.Data[corev1.DockerConfigJsonKey], password, baseDomain)
	if err != nil {
		return err
	}

	// No change needed
	if bytes.Equal(mergedBytes, pullSecret.Data[corev1.DockerConfigJsonKey]) {
		return nil
	}

	pullSecret.Data[corev1.DockerConfigJsonKey] = mergedBytes
	_, err = ctrl.kubeClient.CoreV1().Secrets(ctrlcommon.OpenshiftConfigNamespace).Update(
		context.TODO(), pullSecret, metav1.UpdateOptions{})
	if err == nil {
		klog.Infof("Updated pull secret with IRI registry auth credentials from secret %s/%s (uid=%s, resourceVersion=%s)", authSecret.Namespace, authSecret.Name, authSecret.UID, authSecret.ResourceVersion)
	}
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
