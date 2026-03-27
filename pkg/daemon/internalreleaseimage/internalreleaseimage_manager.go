package internalreleaseimage

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformersv1 "k8s.io/client-go/informers/core/v1"
	corev1lister "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	v1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	cligoinformersv1 "github.com/openshift/client-go/config/informers/externalversions/config/v1"
	cligolistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	mcfginformersv1alpha1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1alpha1"
	mcfglistersv1alpha1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1alpha1"
)

const (
	// backoff configuration
	maxRetries    = 5
	retryDuration = 1 * time.Second
	retryFactor   = 2.0
	retryCap      = 10 * time.Second

	// controller configuration
	maxRetriesController = 15

	// mcn looks for conditions with this prefix if seen will degrade the pool
	degradeMessagePrefix = "Error:"

	iriRegistryPort = 22625
)

// InternalRelelaseImageManager manages the IRI registry data on disk
// and takes care of updating the MCN status IRI fields for the current node.
type InternalRelelaseImageManager struct {
	nodeName string
	backoff  wait.Backoff

	mcfgClient mcfgclientset.Interface

	syncHandler                 func(mcp string) error
	enqueueInternalReleaseImage func(*mcfgv1alpha1.InternalReleaseImage)
	queue                       workqueue.TypedRateLimitingInterface[string]

	iriLister       mcfglistersv1alpha1.InternalReleaseImageLister
	iriListerSynced cache.InformerSynced

	nodeLister       corev1lister.NodeLister
	nodeListerSynced cache.InformerSynced

	infraLister       cligolistersv1.InfrastructureLister
	infraListerSynced cache.InformerSynced
}

// NewInternalReleaseImageManager creates a new internal release image manager.
func NewInternalReleaseImageManager(
	nodeName string,
	mcfgClient mcfgclientset.Interface,
	iriInformer mcfginformersv1alpha1.InternalReleaseImageInformer,
	nodeInformer coreinformersv1.NodeInformer,
	infraInformer cligoinformersv1.InfrastructureInformer,
) *InternalRelelaseImageManager {
	i := &InternalRelelaseImageManager{
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

	i.nodeLister = nodeInformer.Lister()
	i.nodeListerSynced = nodeInformer.Informer().HasSynced

	i.infraLister = infraInformer.Lister()
	i.infraListerSynced = infraInformer.Informer().HasSynced

	iriInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    i.addInternalReleaseImage,
		UpdateFunc: i.updateInternalReleaseImage,
		DeleteFunc: i.deleteInternalReleaseImage,
	})

	return i
}

func (i *InternalRelelaseImageManager) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer i.queue.ShutDown()

	if !cache.WaitForCacheSync(
		stopCh,
		i.iriListerSynced,
		i.nodeListerSynced,
		i.infraListerSynced,
	) {
		klog.Errorf("failed to sync initial listers cache")
		return
	}

	klog.Infof("Starting InternalReleaseImage Manager")
	defer klog.Infof("Shutting down InternalReleaseImage Manager")

	for range workers {
		go wait.Until(i.worker, time.Second, stopCh)
	}

	<-stopCh
}

func (i *InternalRelelaseImageManager) enqueue(iri *mcfgv1alpha1.InternalReleaseImage) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(iri)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", iri, err))
		return
	}
	i.queue.Add(key)
}

// worker runs a worker thread that just dequeues items, processes them, and marks them done.
// It enforces that the syncHandler is never invoked concurrently with the same key.
func (i *InternalRelelaseImageManager) worker() {
	for i.processNextItem() {
	}
}

func (i *InternalRelelaseImageManager) processNextItem() bool {
	key, quit := i.queue.Get()
	if quit {
		return false
	}
	defer i.queue.Done(key)

	err := i.syncHandler(key)
	i.handleErr(err, key)
	return true
}

func (i *InternalRelelaseImageManager) handleErr(err error, key string) {
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

	klog.Warningf(" failed: %s max retries: %d", key, maxRetriesController)
	i.queue.Forget(key)
	i.queue.AddAfter(key, 1*time.Minute)
}

func (i *InternalRelelaseImageManager) addInternalReleaseImage(obj interface{}) {
	iri := obj.(*mcfgv1alpha1.InternalReleaseImage)
	klog.V(4).Infof("Adding InternalReleaseImage %s", iri.Name)
	i.enqueueInternalReleaseImage(iri)
}

func (i *InternalRelelaseImageManager) updateInternalReleaseImage(old, cur interface{}) {
	oldInternalReleaseImage := old.(*mcfgv1alpha1.InternalReleaseImage)
	newInternalReleaseImage := cur.(*mcfgv1alpha1.InternalReleaseImage)

	if i.internalReleaseImageChanged(oldInternalReleaseImage, newInternalReleaseImage) {
		klog.V(4).Infof("mcfgv1alpha1.InternalReleaseImage %s updated", newInternalReleaseImage.Name)
		i.enqueueInternalReleaseImage(newInternalReleaseImage)
	}
}

func (i *InternalRelelaseImageManager) internalReleaseImageChanged(old, newIRI *mcfgv1alpha1.InternalReleaseImage) bool {
	if old.DeletionTimestamp != newIRI.DeletionTimestamp {
		return true
	}
	if !reflect.DeepEqual(old.Spec, newIRI.Spec) {
		return true
	}
	return false
}

func (i *InternalRelelaseImageManager) deleteInternalReleaseImage(obj interface{}) {
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

// getNodeWithRetry gets the node with retries. This avoids some races when the local node
// is new but not found during startup.
func (i *InternalRelelaseImageManager) getNodeWithRetry(nodeName string) (*corev1.Node,
	error) {
	var node *corev1.Node
	err := wait.ExponentialBackoff(i.backoff, func() (bool, error) {
		var err error
		node, err = i.nodeLister.Get(nodeName)
		if err != nil {
			if apierrors.IsNotFound(err) {
				// log warning and retry because we are tolerating unexpected behavior from the informer
				klog.Warningf("Node %q not found, retrying", nodeName)
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
	return node, err
}

func (i *InternalRelelaseImageManager) queryRegistry(iriRegistryUrl string) (*http.Response, error) {
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			// The certificate already installed on the node does not include the current node IP
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, iriRegistryUrl, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (i *InternalRelelaseImageManager) registryHealthCheck() (string, error) {
	node, err := i.getNodeWithRetry(i.nodeName)
	if err != nil {
		return "", fmt.Errorf("failed to get node %q: %v", i.nodeName, err)
	}

	var errorMessages []string
	for _, addr := range node.Status.Addresses {
		if addr.Type != corev1.NodeInternalIP {
			continue
		}

		iriRegistryUrl := fmt.Sprintf("https://%s:%d/v2/", addr.Address, iriRegistryPort)
		klog.V(2).Infof("Checking local InternalReleaseImage registry status for node %s at %s", i.nodeName, iriRegistryUrl)

		resp, err := i.queryRegistry(iriRegistryUrl)
		if err != nil {
			klog.V(2).Infof("Skipping registry check for node %s (%s) due the following error: %v", i.nodeName, addr.Address, err)
			errorMessages = append(errorMessages, err.Error())
			continue
		}
		statusCode := resp.StatusCode
		resp.Body.Close()

		if statusCode == http.StatusOK {
			klog.V(2).Infof("The local InternalReleaseImage registry is available for node %s (%s)", i.nodeName, addr.Address)
			return iriRegistryUrl, nil
		}
		errorMessages = append(errorMessages, fmt.Sprintf("Registry check for for node %s (%s) failed with status code %d", i.nodeName, addr.Address, statusCode))
	}
	return "", fmt.Errorf("no available local InternalReleaseImage registry found for node %s. Errors: %v", i.nodeName, errorMessages)
}

type registryTagsList struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

func (i *InternalRelelaseImageManager) parseTagsList(r io.Reader) (*registryTagsList, error) {
	var resp registryTagsList

	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()

	if err := dec.Decode(&resp); err != nil {
		return nil, fmt.Errorf("decode tags list response: %w", err)
	}
	if resp.Name == "" {
		return nil, fmt.Errorf("missing or empty field %q", "name")
	}
	if resp.Tags == nil {
		resp.Tags = []string{}
	}
	return &resp, nil
}

func (i *InternalRelelaseImageManager) getCurrentReleaseTags(iriRegistryUrl string) (*registryTagsList, error) {
	u, err := url.Parse(iriRegistryUrl)
	if err != nil {
		return nil, err
	}
	u.Path = "/v2/openshift/release-images/tags/list"

	klog.V(2).Infof("Retrieving release images tags from %s", u.String())
	resp, err := i.queryRegistry(u.String())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("error while retrieving registry release tags from %s. Status code: %d", u.String(), resp.StatusCode)
	}
	releaseTags, err := i.parseTagsList(resp.Body)
	if err != nil {
		return nil, err
	}
	return releaseTags, nil
}

func (i *InternalRelelaseImageManager) getReleaseImagePullSpec(releaseTag string) (string, error) {
	// Get the Infrastructure to extract the internal api-int
	infra, err := i.infraLister.Get("cluster")
	if err != nil {
		return "", err
	}

	apiInt, err := url.Parse(infra.Status.APIServerInternalURL)
	if err != nil {
		return "", err
	}
	pullspec := fmt.Sprintf("%s:%d/openshift/release-images@sha256:%s", apiInt.Hostname(), iriRegistryPort, releaseTag)

	return pullspec, nil
}

func (i *InternalRelelaseImageManager) updateMachineConfigNodeStatus(iriRegistryUrl string) error {
	// Get the current OCP releases stored in the local IRI registry.
	releaseTags, err := i.getCurrentReleaseTags(iriRegistryUrl)
	if err != nil {
		return err
	}

	// Get the MachineConfigNode for the current node.
	mcn, err := i.mcfgClient.MachineconfigurationV1().MachineConfigNodes().Get(context.TODO(), i.nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	// Verify that the MCN status is properly reporting the current registry state,
	// by checking that each OCP release images stored into the registry are present
	// into the MCN status.
	missingReleasePullSpecs := []string{}
	for _, releaseTag := range releaseTags.Tags {
		found := false
		releasePullSpec, err := i.getReleaseImagePullSpec(releaseTag)
		if err != nil {
			return err
		}
		for _, r := range mcn.Status.InternalReleaseImage.Releases {
			if r.Image == releasePullSpec {
				found = true
				break
			}
		}
		if !found {
			klog.V(2).Infof("Found missing release image pullspec: %s", releasePullSpec)
			missingReleasePullSpecs = append(missingReleasePullSpecs, releasePullSpec)
		}
	}

	// Let's add any new tag found in the registry but not in the MCN resource.
	if len(missingReleasePullSpecs) > 0 {
		mcnUpdated := mcn.DeepCopy()
		for _, imagePullSpec := range missingReleasePullSpecs {
			relName := "ocp-release-bundle-4.22.0-x86_64" // TODO: retrieve from the registry release-bundles image tag (https://redhat.atlassian.net/browse/AGENT-1475)
			iriRelease := v1.MachineConfigNodeStatusInternalReleaseImageRef{
				Name:  relName,
				Image: imagePullSpec,
				Conditions: []metav1.Condition{
					{
						Type:               string(v1.InternalReleaseImageConditionTypeAvailable),
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             string(v1.InternalReleaseImageConditionTypeAvailable),
						Message:            fmt.Sprintf("Release %s is currently available", relName),
					},
				},
			}
			mcnUpdated.Status.InternalReleaseImage.Releases = append(mcnUpdated.Status.InternalReleaseImage.Releases, iriRelease)
		}
		klog.V(2).Infof("Updating MCN %s with missing release image pullspecs", mcnUpdated.Name)
		_, err = i.mcfgClient.MachineconfigurationV1().MachineConfigNodes().UpdateStatus(context.Background(), mcnUpdated, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update MCN %s InternalReleaseImage Status conditions: %w", mcnUpdated.Name, err)
		}
	}

	return nil
}

func (i *InternalRelelaseImageManager) syncInternalReleaseImage(key string) error {
	klog.V(4).Infof("Syncing InternalReleaseImage %q", key)

	iriRegistryUrl, err := i.registryHealthCheck()
	if err != nil {
		klog.Errorf("error while checking local InternalReleaseImage registry: %v", err)
		//TODO: Mark the MCN status as degraded
		return err
	}

	err = i.updateMachineConfigNodeStatus(iriRegistryUrl)
	if err != nil {
		klog.Errorf("failed to update MachineConfigNode status: %v", err)
		return err
	}

	i.queue.AddAfter(key, 30*time.Second)
	return nil
}
