package internalreleaseimage

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	mcfginformersv1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1"
	mcfginformersv1alpha1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1alpha1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	mcfglistersv1alpha1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
)

const (
	// controller configuration
	maxRetriesController = 15
	syncRetryInterval    = 60 * time.Second
)

// Manager manages the IRI registry data on disk
// and takes care of updating the MCN status IRI fields for the current node.
type Manager struct {
	nodeName string

	mcfgClient     mcfgclientset.Interface
	registryClient *http.Client
	// authToken overrides the token read from the kubelet auth file; used in tests.
	authToken string
	// registryDataPath overrides the default registry data path; used in tests.
	registryDataPath string

	syncHandler                 func(iri string) error
	enqueueInternalReleaseImage func(*mcfgv1alpha1.InternalReleaseImage)
	queue                       workqueue.TypedRateLimitingInterface[string]

	iriLister       mcfglistersv1alpha1.InternalReleaseImageLister
	iriListerSynced cache.InformerSynced

	mcnLister       mcfglistersv1.MachineConfigNodeLister
	mcnListerSynced cache.InformerSynced
}

// NewInternalReleaseImageManager creates a new internal release image manager.
func New(
	nodeName string,
	mcfgClient mcfgclientset.Interface,
	iriInformer mcfginformersv1alpha1.InternalReleaseImageInformer,
	mcnInformer mcfginformersv1.MachineConfigNodeInformer,
) *Manager {
	i := &Manager{
		nodeName: nodeName,
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "internal-release-image-manager"}),
	}

	i.mcfgClient = mcfgClient

	i.syncHandler = i.syncInternalReleaseImage
	i.enqueueInternalReleaseImage = i.enqueue

	i.iriLister = iriInformer.Lister()
	i.iriListerSynced = iriInformer.Informer().HasSynced

	i.mcnLister = mcnInformer.Lister()
	i.mcnListerSynced = mcnInformer.Informer().HasSynced

	iriInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    i.addInternalReleaseImage,
		UpdateFunc: i.updateInternalReleaseImage,
		DeleteFunc: i.deleteInternalReleaseImage,
	})

	mcnInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: i.addMachineConfigNode,
	})

	return i
}

func (i *Manager) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer i.queue.ShutDown()

	if !cache.WaitForCacheSync(
		stopCh,
		i.iriListerSynced,
		i.mcnListerSynced,
	) {
		klog.Errorf("failed to sync initial listers cache")
		return
	}

	if i.registryClient == nil {
		i.registryClient = &http.Client{Timeout: 3 * time.Second}
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

func (i *Manager) addMachineConfigNode(obj interface{}) {
	curMCN := obj.(*mcfgv1.MachineConfigNode)

	if curMCN.Name == i.nodeName {
		klog.V(4).Infof("MachineConfigNode %s added", curMCN.Name)
		i.queue.Add(common.InternalReleaseImageInstanceName)
	}
}

func (i *Manager) updateMCNStatus(mcnOld, mcn *mcfgv1.MachineConfigNode) error {
	if equality.Semantic.DeepEqual(&mcnOld.Status, &mcn.Status) {
		return nil
	}

	_, err := i.mcfgClient.MachineconfigurationV1().MachineConfigNodes().UpdateStatus(context.Background(), mcn, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update MCN %s InternalReleaseImage Status conditions: %w", mcn.Name, err)
	}
	return nil
}

func (i *Manager) refreshMachineConfigNodeStatus(mcn *mcfgv1.MachineConfigNode, iriReg *iriRegistry) error {
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

		iriRelease := mcfgv1.MachineConfigNodeStatusInternalReleaseImageRef{
			Name:  bundle,
			Image: pullSpec,
		}
		mcnUpdated.Status.InternalReleaseImage.Releases = append(mcnUpdated.Status.InternalReleaseImage.Releases, iriRelease)
	}

	// Check release availability for each bundle. If at least one release image is not available
	// then mark the MCN as degraded.
	// When the bundle deletion will be supported, it will be required also to check for missing bundles
	// and update properly the MachineConfigNode resource.
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
			Type:    string(mcfgv1.MachineConfigNodeInternalReleaseImageDegraded),
			Status:  metav1.ConditionFalse,
			Reason:  "AllReleasesAvailable",
			Message: "All the release images are available",
		})
	} else {
		meta.SetStatusCondition(&mcnUpdated.Status.Conditions, metav1.Condition{
			Type:    string(mcfgv1.MachineConfigNodeInternalReleaseImageDegraded),
			Status:  metav1.ConditionTrue,
			Reason:  "ReleaseImageNotFound",
			Message: "One or more release bundle are not available",
		})
	}

	return i.updateMCNStatus(mcn, mcnUpdated)
}

func (i *Manager) setMachineConfigNodeAsDegraded(mcn *mcfgv1.MachineConfigNode, registryErr error) error {
	reason := "RegistryUnreachable"

	mcnUpdated := mcn.DeepCopy()
	meta.SetStatusCondition(&mcnUpdated.Status.Conditions, metav1.Condition{
		Type:    string(mcfgv1.MachineConfigNodeInternalReleaseImageDegraded),
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

func (i *Manager) cleanupMachineConfigNodeStatus(mcn *mcfgv1.MachineConfigNode) error {
	if len(mcn.Status.InternalReleaseImage.Releases) == 0 {
		return nil
	}

	// Remove the IRI condition.
	mcnUpdated := mcn.DeepCopy()
	var filtered []metav1.Condition
	for _, c := range mcnUpdated.Status.Conditions {
		if c.Type != string(mcfgv1.MachineConfigNodeInternalReleaseImageDegraded) {
			filtered = append(filtered, c)
		}
	}
	mcnUpdated.Status.Conditions = filtered

	// Cleanup the IRI status field.
	mcnUpdated.Status.InternalReleaseImage = mcfgv1.MachineConfigNodeStatusInternalReleaseImage{}

	return i.updateMCNStatus(mcn, mcnUpdated)
}

// reclaimRegistryStorage removes the IRI registry data directory contents when safe.
// Returns an error if the directory cannot be removed, or nil if removal succeeded
// or if the directory doesn't exist.
func (i *Manager) reclaimRegistryStorage() error {
	registryDataPath := i.registryDataPath
	if registryDataPath == "" {
		registryDataPath = constants.IRIRegistryDataPath
	}

	// Check if directory exists
	info, err := os.Stat(registryDataPath)
	if err != nil {
		if os.IsNotExist(err) {
			klog.V(2).Infof("Registry data directory %s does not exist, nothing to reclaim", registryDataPath)
			return nil
		}
		return fmt.Errorf("failed to stat registry data path %s: %w", registryDataPath, err)
	}

	// Verify it's a directory
	if !info.IsDir() {
		return fmt.Errorf("registry data path %s exists but is not a directory", registryDataPath)
	}

	base, err := filepath.Abs(registryDataPath)
	if err != nil {
		return fmt.Errorf("failed to resolve registry data path %q: %w", registryDataPath, err)
	}
	base = filepath.Clean(base)

	if base == string(os.PathSeparator) {
		return fmt.Errorf("invalid registry data path")
	}

	entries, err := os.ReadDir(base)
	if err != nil {
		return fmt.Errorf("failed to read registry data path %q: %w", base, err)
	}

	for _, entry := range entries {
		path := filepath.Join(base, entry.Name())

		rel, err := filepath.Rel(base, path)
		if err != nil {
			return fmt.Errorf("failed to validate registry data path %q: %w", path, err)
		}

		if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("refusing to remove path outside registry data path: %q", path)
		}

		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("failed to remove registry data path %q: %w", path, err)
		}

		klog.V(2).Infof("Removed registry data path: %s", path)
	}

	klog.Infof("Successfully reclaimed storage: removed %d entries from %s", len(entries), registryDataPath)
	return nil
}

// getIRIRegistry creates and returns an IRI registry client.
// Returns the registry and an error indicating whether the registry is reachable.
func (i *Manager) getIRIRegistry() (*iriRegistry, error) {
	authToken := i.authToken
	if authToken == "" {
		var err error
		authToken, err = readIRIAuthToken(net.JoinHostPort(iriRegistryHost, fmt.Sprintf("%d", iriRegistryPort)))
		if err != nil {
			return nil, fmt.Errorf("could not read IRI auth token: %w", err)
		}
	}

	iriReg := newIRIRegistry(i.nodeName, i.registryClient, authToken)
	err := iriReg.CheckLocalRegistry()
	return iriReg, err
}

// wasFeatureActivated checks if the NoRegistryClusterInstall feature was ever activated on this node.
// Returns true if the registry data directory exists (feature was activated),
// false if the directory doesn't exist (feature never used),
// or an error if the check failed (permission denied, I/O error, etc.).
func (i *Manager) wasFeatureActivated() (bool, error) {
	registryDataPath := i.registryDataPath
	if registryDataPath == "" {
		registryDataPath = constants.IRIRegistryDataPath
	}
	_, err := os.Stat(registryDataPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // Directory doesn't exist - feature never activated
		}
		return false, fmt.Errorf("failed to check registry data path %s: %w", registryDataPath, err)
	}
	return true, nil
}

// isRegistryPortListening checks if the registry port is accepting connections.
// Returns true if port is listening (registry service is running), false otherwise.
// This is a cheap TCP dial check (~microseconds for localhost) that provides a stronger
// signal than HTTP errors, which can occur for reasons other than service being down.
func (i *Manager) isRegistryPortListening() bool {
	address := net.JoinHostPort(iriRegistryHost, fmt.Sprintf("%d", iriRegistryPort))
	conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// handleIRIDeletion handles the IRI deletion scenario (both in-progress and completed).
// This method is only called when the registry directory exists (feature was activated).
// If registry port is still listening, it waits for shutdown.
// If registry port is down, it cleans up MCN status and reclaims storage.
func (i *Manager) handleIRIDeletion(mcn *mcfgv1.MachineConfigNode) error {
	// Check if registry port is listening
	if i.isRegistryPortListening() {
		// Registry port is still listening - wait for shutdown and avoid any other task
		klog.V(2).Infof("Registry port is still listening, waiting for shutdown before cleanup")
		i.queue.AddAfter(common.InternalReleaseImageInstanceName, syncRetryInterval)
		return nil
	}

	// Registry port is not listening - safe to clean up and reclaim storage
	klog.V(2).Infof("Registry port is not listening - proceeding with cleanup")

	// Clean up MCN status
	if err := i.cleanupMachineConfigNodeStatus(mcn); err != nil {
		return fmt.Errorf("failed to cleanup MCN: %w", err)
	}

	// Reclaim storage
	if err := i.reclaimRegistryStorage(); err != nil {
		return fmt.Errorf("failed to reclaim registry storage: %w", err)
	}

	return nil
}

func (i *Manager) syncInternalReleaseImage(key string) error {
	klog.V(4).Infof("Syncing InternalReleaseImage %q", key)

	// Check if feature was ever activated - if not, skip all work
	wasActivated, err := i.wasFeatureActivated()
	if err != nil {
		return err
	}
	if !wasActivated {
		klog.V(4).Infof("InternalReleaseImage feature never activated")
		i.queue.AddAfter(common.InternalReleaseImageInstanceName, 5*time.Minute)
		return nil
	}

	// Get the MachineConfigNode for the current node.
	mcn, err := i.mcnLister.Get(i.nodeName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.V(2).Infof("MachineConfigNode %s not yet present, waiting for its creation", i.nodeName)
			return nil
		}
		return err
	}

	// Check if IRI resource exists
	iri, err := i.iriLister.Get(common.InternalReleaseImageInstanceName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return i.handleIRIDeletion(mcn)
		}
		return err
	}

	// Check if IRI is being deleted
	if !iri.DeletionTimestamp.IsZero() {
		return i.handleIRIDeletion(mcn)
	}

	// Update MCN status based on registry availability
	iriReg, registryErr := i.getIRIRegistry()
	if registryErr != nil {
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
