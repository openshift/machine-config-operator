package internalreleaseimage

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"reflect"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	v1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	mcfginformersv1alpha1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1alpha1"
	mcfglistersv1alpha1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/controller/common"
)

const (
	// backoff configuration
	maxRetries    = 5
	retryDuration = 1 * time.Second
	retryFactor   = 2.0
	retryCap      = 10 * time.Second

	// controller configuration
	maxRetriesController = 15
	syncRetryInterval    = 30 * time.Second
)

// Manager manages the IRI registry data on disk
// and takes care of updating the MCN status IRI fields for the current node.
type Manager struct {
	nodeName string
	backoff  wait.Backoff

	mcfgClient     mcfgclientset.Interface
	registryClient *http.Client

	syncHandler                 func(iri string) error
	enqueueInternalReleaseImage func(*mcfgv1alpha1.InternalReleaseImage)
	queue                       workqueue.TypedRateLimitingInterface[string]

	iriLister       mcfglistersv1alpha1.InternalReleaseImageLister
	iriListerSynced cache.InformerSynced
}

// NewInternalReleaseImageManager creates a new internal release image manager.
func New(
	nodeName string,
	mcfgClient mcfgclientset.Interface,
	iriInformer mcfginformersv1alpha1.InternalReleaseImageInformer,
) *Manager {
	i := &Manager{
		nodeName: nodeName,
		queue: workqueue.NewTypedRateLimitingQueueWithConfig[string](
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "internal-release-image-manager"}),
		backoff: wait.Backoff{
			Steps:    maxRetries,
			Duration: retryDuration,
			Factor:   retryFactor,
			Cap:      retryCap,
		},
	}

	i.mcfgClient = mcfgClient

	i.syncHandler = i.syncInternalReleaseImage
	i.enqueueInternalReleaseImage = i.enqueue

	i.iriLister = iriInformer.Lister()
	i.iriListerSynced = iriInformer.Informer().HasSynced

	iriInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    i.addInternalReleaseImage,
		UpdateFunc: i.updateInternalReleaseImage,
		DeleteFunc: i.deleteInternalReleaseImage,
	})

	return i
}

func (i *Manager) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer i.queue.ShutDown()

	if !cache.WaitForCacheSync(
		stopCh,
		i.iriListerSynced,
	) {
		klog.Errorf("failed to sync initial listers cache")
		return
	}

	if i.registryClient == nil {
		i.registryClient = &http.Client{Timeout: 3 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true,
				},
			},
		}
	}

	klog.Infof("Starting InternalReleaseImage Manager")
	defer klog.Infof("Shutting down InternalReleaseImage Manager")

	for range workers {
		go wait.Until(i.worker, time.Second, stopCh)
	}

	<-stopCh
}

func (i *Manager) enqueue(iri *mcfgv1alpha1.InternalReleaseImage) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(iri)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", iri, err))
		return
	}
	i.queue.Add(key)
}

// worker runs a worker thread that just dequeues items, processes them, and marks them done.
// It enforces that the syncHandler is never invoked concurrently with the same key.
func (i *Manager) worker() {
	for i.processNextItem() {
	}
}

func (i *Manager) processNextItem() bool {
	key, quit := i.queue.Get()
	if quit {
		return false
	}
	defer i.queue.Done(key)

	err := i.syncHandler(key)
	i.handleErr(err, key)
	return true
}

func (i *Manager) handleErr(err error, key string) {
	if err == nil {
		i.queue.Forget(key)
		return
	}

	if i.queue.NumRequeues(key) < maxRetriesController {
		klog.V(4).Infof("Requeue InternalReleaseImage %v: %v", key, err)
		i.queue.AddRateLimited(key)
		return
	}
	utilruntime.HandleError(err)

	klog.Warningf("failed: %s max retries: %d", key, maxRetriesController)
	i.queue.Forget(key)
	i.queue.AddAfter(key, 1*time.Minute)
}

func (i *Manager) addInternalReleaseImage(obj interface{}) {
	iri := obj.(*mcfgv1alpha1.InternalReleaseImage)
	klog.V(4).Infof("Adding InternalReleaseImage %s", iri.Name)
	i.enqueueInternalReleaseImage(iri)
}

func (i *Manager) updateInternalReleaseImage(old, cur interface{}) {
	oldInternalReleaseImage := old.(*mcfgv1alpha1.InternalReleaseImage)
	newInternalReleaseImage := cur.(*mcfgv1alpha1.InternalReleaseImage)

	if i.internalReleaseImageChanged(oldInternalReleaseImage, newInternalReleaseImage) {
		klog.V(4).Infof("mcfgv1alpha1.InternalReleaseImage %s updated", newInternalReleaseImage.Name)
		i.enqueueInternalReleaseImage(newInternalReleaseImage)
	}
}

func (i *Manager) internalReleaseImageChanged(old, newIRI *mcfgv1alpha1.InternalReleaseImage) bool {
	if old.DeletionTimestamp != newIRI.DeletionTimestamp {
		return true
	}
	if !reflect.DeepEqual(old.Spec, newIRI.Spec) {
		return true
	}
	return false
}

func (i *Manager) deleteInternalReleaseImage(obj interface{}) {
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
	i.enqueueInternalReleaseImage(iri)
}

func (i *Manager) updateMCNStatus(mcnOld, mcn *v1.MachineConfigNode) error {
	if equality.Semantic.DeepEqual(&mcnOld.Status, &mcn.Status) {
		return nil
	}

	_, err := i.mcfgClient.MachineconfigurationV1().MachineConfigNodes().UpdateStatus(context.Background(), mcn, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update MCN %s InternalReleaseImage Status conditions: %w", mcn.Name, err)
	}
	return nil
}

func (i *Manager) refreshMachineConfigNodeStatus(mcn *v1.MachineConfigNode, iriReg *iriRegistry) error {
	// Get the current OCP releases bundles stored in the local IRI registry.
	registryBundles, err := iriReg.GetOCPBundlesTags()
	if err != nil {
		return err
	}

	// Check if there is any new release bundle in the registry not yet reported in the status.
	newBundles := []string{}
	for _, bundleName := range registryBundles.Tags {
		found := false
		for _, r := range mcn.Status.InternalReleaseImage.Releases {
			if bundleName == r.Name {
				found = true
				break
			}
		}
		if found {
			continue
		}

		klog.V(2).Infof("New release bundle found: %s", bundleName)
		newBundles = append(newBundles, bundleName)
	}

	// Add new bundles, if any.
	mcnUpdated := mcn.DeepCopy()
	for _, bundle := range newBundles {
		ocpReleaseTag, err := iriReg.GetOCPBundleReleaseTag(bundle)
		if err != nil {
			return err
		}
		pullSpec := iriReg.GetOCPReleasePullSpec(ocpReleaseTag)

		iriRelease := v1.MachineConfigNodeStatusInternalReleaseImageRef{
			Name:  bundle,
			Image: pullSpec,
		}
		mcnUpdated.Status.InternalReleaseImage.Releases = append(mcnUpdated.Status.InternalReleaseImage.Releases, iriRelease)
	}

	// Check release availability for each bundle. If at least one release image is not available
	// then mark the MCN as degraded.
	mcnDegraded := false
	for n := range mcnUpdated.Status.InternalReleaseImage.Releases {
		r := &mcnUpdated.Status.InternalReleaseImage.Releases[n]

		err := iriReg.CheckImageAvailability(r.Image)
		if err == nil {
			meta.SetStatusCondition(&r.Conditions, metav1.Condition{
				Type:    string(mcfgv1alpha1.InternalReleaseImageConditionTypeDegraded),
				Status:  metav1.ConditionFalse,
				Reason:  "ReleaseImageAvailable",
				Message: "ReleaseImageAvailable",
			})
			meta.SetStatusCondition(&r.Conditions, metav1.Condition{
				Type:    string(mcfgv1alpha1.InternalReleaseImageConditionTypeAvailable),
				Status:  metav1.ConditionTrue,
				Reason:  "ReleaseImageAvailable",
				Message: "The specified release image is available",
			})
		} else {
			mcnDegraded = true
			klog.Errorf("Release image %s not available for bundle %s. Error: %v", r.Image, r.Name, err)
			meta.SetStatusCondition(&r.Conditions, metav1.Condition{
				Type:    string(mcfgv1alpha1.InternalReleaseImageConditionTypeDegraded),
				Status:  metav1.ConditionTrue,
				Reason:  "ReleaseImageNotFound",
				Message: err.Error(),
			})
			meta.SetStatusCondition(&r.Conditions, metav1.Condition{
				Type:    string(mcfgv1alpha1.InternalReleaseImageConditionTypeAvailable),
				Status:  metav1.ConditionFalse,
				Reason:  "ReleaseImageNotFound",
				Message: "The specified release image was not found in the registry",
			})
		}
	}
	if !mcnDegraded {
		meta.SetStatusCondition(&mcnUpdated.Status.Conditions, metav1.Condition{
			Type:    string(v1.MachineConfigNodeInternalReleaseImageDegraded),
			Status:  metav1.ConditionFalse,
			Reason:  "AllReleasesAvailable",
			Message: "All the release images are available",
		})
	} else {
		meta.SetStatusCondition(&mcnUpdated.Status.Conditions, metav1.Condition{
			Type:    string(v1.MachineConfigNodeInternalReleaseImageDegraded),
			Status:  metav1.ConditionTrue,
			Reason:  "ReleaseImageNotFound",
			Message: "One or more release bundle are not available",
		})
	}

	return i.updateMCNStatus(mcn, mcnUpdated)
}

func (i *Manager) setMachineConfigNodeAsDegraded(mcn *v1.MachineConfigNode, registryErr error) error {
	reason := "RegistryUnreachable"

	mcnUpdated := mcn.DeepCopy()
	meta.SetStatusCondition(&mcnUpdated.Status.Conditions, metav1.Condition{
		Type:    string(v1.MachineConfigNodeInternalReleaseImageDegraded),
		Status:  metav1.ConditionTrue,
		Reason:  reason,
		Message: registryErr.Error(),
	})

	// Mark all the current releases as Degraded and not Available.
	for n := range mcnUpdated.Status.InternalReleaseImage.Releases {
		r := &mcnUpdated.Status.InternalReleaseImage.Releases[n]

		meta.SetStatusCondition(&r.Conditions, metav1.Condition{
			Type:    string(mcfgv1alpha1.InternalReleaseImageConditionTypeDegraded),
			Status:  metav1.ConditionTrue,
			Reason:  reason,
			Message: registryErr.Error(),
		})
		meta.SetStatusCondition(&r.Conditions, metav1.Condition{
			Type:    string(mcfgv1alpha1.InternalReleaseImageConditionTypeAvailable),
			Status:  metav1.ConditionFalse,
			Reason:  reason,
			Message: "Release bundle is unavailable: failed to reach the registry",
		})
	}

	return i.updateMCNStatus(mcn, mcnUpdated)
}

func (i *Manager) syncInternalReleaseImage(key string) error {
	klog.V(4).Infof("Syncing InternalReleaseImage %q", key)

	// Fetch the InternalReleaseImage.
	_, err := i.iriLister.Get(common.InternalReleaseImageInstanceName)
	if errors.IsNotFound(err) {
		// Manage the feature only when the IRI resource was defined.
		return nil
	}
	if err != nil {
		return err
	}

	// Get the MachineConfigNode for the current node.
	mcn, err := i.mcfgClient.MachineconfigurationV1().MachineConfigNodes().Get(context.TODO(), i.nodeName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.V(2).Infof("MachineConfigNode %s not yet present, waiting for its creation", i.nodeName)
			return nil
		}
		return err
	}

	iriReg := newIRIRegistry(i.nodeName, i.registryClient)
	if registryErr := iriReg.CheckLocalRegistry(); registryErr != nil {
		err = i.setMachineConfigNodeAsDegraded(mcn, registryErr)
	} else {
		err = i.refreshMachineConfigNodeStatus(mcn, iriReg)
	}
	if err != nil {
		klog.Errorf("failed to update MachineConfigNode status: %v", err)
		return err
	}

	i.queue.AddAfter(common.InternalReleaseImageInstanceName, syncRetryInterval)
	return nil
}
