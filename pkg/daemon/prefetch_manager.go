package daemon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/containers/image/v5/docker/reference"
	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformersv1 "k8s.io/client-go/informers/core/v1"
	corev1lister "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
	"k8s.io/klog/v2"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfginformersv1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/daemon/cri"
)

const (
	// cri gRPC connection parameters taken from kubelet
	criPrefetchInterval             = 30 * time.Second
	defaultPrefetchWorkers          = 5
	defaultControlPlaneWorkers      = 1
	defaultPrefetchThrottleDuration = 1 * time.Second

	crioPinnedImagesDropInFilePath = "/etc/crio/crio.conf.d/50-pinned-images"

	// backoff configuration
	maxRetries    = 5
	retryDuration = 1 * time.Second
	retryFactor   = 2.0
	retryCap      = 10 * time.Second

	// controller configuration
	maxRetriesController = 15

	ImageSetReasonInProgress = "in progress"
	imageSetReasonCancelled  = "cancelled"
	imageSetReason           = constants.MachineConfigDaemonPinnedImageSetPrefetchReasonAnnotationKey
	imageSetDesired          = constants.MachineConfigDaemonPinnedImageSetPrefetchDesiredAnnotationKey
	imageSetCurrent          = constants.MachineConfigDaemonPinnedImageSetPrefetchCurrentAnnotationKey
)

var (
	errInsufficientStorage     = errors.New("storage available is less than minimum required")
	errFailedToPullImage       = errors.New("failed to pull image")
	errNotFound                = errors.New("not found")
	errNoMatchingPool          = errors.New("no matching pool for PinnedImageSet")
	errMultiplePinnedImageSets = errors.New("multiple PinnedImageSets defined for MachineConfigPool")
)

// PrefetchManager manages the prefetching of images.
type PrefetchManager struct {
	// nodeName is the name of the node.
	nodeName string

	// nodeWriter is used to safely update the node annotations.
	nodeWriter NodeWriter

	imageSetLister mcfglistersv1.PinnedImageSetLister
	imageSetSynced cache.InformerSynced

	nodeLister       corev1lister.NodeLister
	nodeListerSynced cache.InformerSynced

	mcpLister mcfglistersv1.MachineConfigPoolLister
	mcpSynced cache.InformerSynced

	// minimum storage available after prefetching
	minStorageAvailableBytes int64
	// path to the authfile
	authFilePath string
	// endpoint of the container runtime service
	runtimeEndpoint string
	// timeout for the prefetch operation
	prefetchTimeout time.Duration
	// backoff configuration for retries.
	backoff wait.Backoff

	taskManager taskManager

	syncHandler           func(string) error
	enqueuePinnedImageSet func(*mcfgv1.PinnedImageSet)
	queue                 workqueue.RateLimitingInterface
	testMode              bool
}

// newPrefetchManager creates a new PrefetchImageManager.
func newPrefetchManager(
	nodeName string,
	nodeWriter NodeWriter,
	imageSetInformer mcfginformersv1.PinnedImageSetInformer,
	nodeInformer coreinformersv1.NodeInformer,
	mcpInformer mcfginformersv1.MachineConfigPoolInformer,
	minStorageAvailableBytes int64,
	runtimeEndpoint string,
	authFilePath string,
	prefetchTimeout time.Duration,
) *PrefetchManager {
	p := &PrefetchManager{
		nodeName:                 nodeName,
		nodeWriter:               nodeWriter,
		runtimeEndpoint:          runtimeEndpoint,
		authFilePath:             authFilePath,
		prefetchTimeout:          prefetchTimeout,
		minStorageAvailableBytes: minStorageAvailableBytes,
		taskManager:              taskManager{cancelFuncs: make(map[types.UID]context.CancelFunc)},
		queue:                    workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "prefetch-manager"),
		backoff: wait.Backoff{
			Steps:    maxRetries,
			Duration: retryDuration,
			Factor:   retryFactor,
			Cap:      retryCap,
		},
	}

	imageSetInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    p.handleAdd,
		UpdateFunc: p.handleUpdate,
		DeleteFunc: p.handleDelete,
	})

	p.syncHandler = p.sync
	p.enqueuePinnedImageSet = p.enqueue

	p.imageSetLister = imageSetInformer.Lister()
	p.imageSetSynced = imageSetInformer.Informer().HasSynced

	p.nodeLister = nodeInformer.Lister()
	p.nodeListerSynced = nodeInformer.Informer().HasSynced

	p.mcpLister = mcpInformer.Lister()
	p.mcpSynced = mcpInformer.Informer().HasSynced

	return p
}

func (p *PrefetchManager) sync(key string) error {
	klog.V(4).Infof("Syncing PinnedImageSet %q", key)

	imageSet, err := p.imageSetLister.Get(key)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	imageSet = imageSet.DeepCopy()

	requeue, err := p.ensurePinnedImageSetForNode(imageSet)
	if err != nil && !requeue {
		return nil
	}
	if err != nil {
		return err
	}

	// stop any existing prefetch tasks
	p.taskManager.stop(imageSet)

	err = p.ensurePinnedImageSet(imageSet)
	if err != nil {
		annotation := p.newAnnotation().
			WithReason(err.Error())
		updateErr := p.updateNodeAnnotation(annotation)
		if updateErr != nil {
			klog.Warningf("failed to update node annotations: %v", updateErr)
		}
		return err
	}

	err = p.ensurePrefetch(imageSet)
	if errors.Is(err, context.Canceled) {
		annotation := p.newAnnotation().
			WithReason(imageSetReasonCancelled)
		updateErr := p.updateNodeAnnotation(annotation)
		if updateErr != nil {
			klog.Warningf("failed to update node annotations: %v", updateErr)
		}
		return nil
	}
	if err != nil {
		annotation := p.newAnnotation().
			WithReason(err.Error())
		updateErr := p.updateNodeAnnotation(annotation)
		if updateErr != nil {
			klog.Warningf("failed to update node annotations: %v", updateErr)
		}
		return err
	}

	annotation := p.newAnnotation().
		WithCurrent(string(imageSet.UID)).
		WithReason("")
	return p.updateNodeAnnotation(annotation)
}

func (p *PrefetchManager) ensurePinnedImageSet(imageSet *mcfgv1.PinnedImageSet) error {
	node, err := p.nodeLister.Get(p.nodeName)
	if err != nil {
		return fmt.Errorf("failed to get node %q: %w", p.nodeName, err)
	}

	imageSetUID := string(imageSet.UID)
	if node.Annotations[imageSetDesired] != imageSetUID {
		// update desired state
		annotation := p.newAnnotation().WithDesired(imageSetUID).WithReason("")
		if err := p.updateNodeAnnotation(annotation); err != nil {
			return fmt.Errorf("failed to update node annotation: %w", err)
		}
	}

	// create ignition file for crio drop-in config. this is done to use the existing
	// writeFiles functionality.
	ignFile, err := createCrioConfigIgnitionFile(imageSet)
	if err != nil {
		return fmt.Errorf("failed to create crio config ignition file: %w", err)
	}

	if err := p.writeFiles([]ign3types.File{ignFile}); err != nil {
		return fmt.Errorf("failed to write crio config file: %w", err)
	}

	return p.postActionCrioReload()
}

func (p *PrefetchManager) ensurePrefetch(imageSet *mcfgv1.PinnedImageSet) error {
	ctx, cancel := context.WithTimeout(context.Background(), p.prefetchTimeout)
	p.taskManager.add(imageSet, cancel)
	defer p.taskManager.stop(imageSet)

	prefetchImages := imageSet.Spec.PinnedImages
	if len(prefetchImages) == 0 {
		klog.Infof("PinnedImageSet has no images to prefetch: %s", imageSet.Name)
		return nil
	}

	if err := p.ensureNodeAllocatableStorage(ctx, prefetchImages); err != nil {
		klog.Warningf("Failed to ensure nodes available storage: %v", err)
		return err
	}

	if err := p.startWorkerPool(ctx, prefetchImages); err != nil {
		return err
	}

	return nil
}

func (p *PrefetchManager) writeFiles(files []ign3types.File) error {
	if p.testMode {
		return nil
	}
	skipCertificateWrite := true
	if err := writeFiles(files, skipCertificateWrite); err != nil {
		return fmt.Errorf("failed to write crio config file: %w", err)
	}
	return nil
}

func (p *PrefetchManager) deleteConfigFileForPinnedImageSet(imageSet *mcfgv1.PinnedImageSet) error {
	// remove the crio config file
	ignFile, err := createCrioConfigIgnitionFile(imageSet)
	if err != nil {
		return fmt.Errorf("failed to create crio config ignition file for deletion: %w", err)
	}

	if err := os.Remove(ignFile.Path); err != nil {
		return fmt.Errorf("failed to remove crio config file: %w", err)
	}

	return p.postActionCrioReload()
}

// getWorkerCount returns the number of workers to use for prefetching images.
func (p *PrefetchManager) getWorkerCount() (int, error) {
	node, err := p.nodeLister.Get(p.nodeName)
	if err != nil {
		return 0, fmt.Errorf("failed to get node %q: %w", p.nodeName, err)
	}

	// default to 5 workers
	workers := defaultPrefetchWorkers

	// master nodes are a special case and should have a concurrency of 1 to
	// mitigate I/O contention with the control plane.
	// TODO: make this configurable via CRD?
	if _, ok := node.Labels["node-role.kubernetes.io/master"]; ok {
		workers = defaultControlPlaneWorkers
	}

	return workers, nil
}

// prefetchWorker is a worker that pulls images from the container runtime.
func (p *PrefetchManager) prefetchWorker(
	ctx context.Context,
	wg *sync.WaitGroup,
	client *cri.Client,
	throttleInterval time.Duration,
	prefetchCh chan prefetch,
	errCh chan error,
) {
	defer wg.Done()
	for task := range prefetchCh {
		if err := p.ensurePullImage(ctx, client, task.image, task.auth); err != nil {
			select {
			case errCh <- err:
			case <-ctx.Done(): // don't block
				return
			}
		}

		// throttle prefetching to avoid overloading the file system
		select {
		case <-time.After(throttleInterval):
		case <-ctx.Done():
			return
		}
	}
}

// startWorkerPool ensures that the prefetch work is scheduled to the available
// workers. It will block until the work is complete, timeout or context
// cancelled.
func (p *PrefetchManager) startWorkerPool(ctx context.Context, prefetchImages []mcfgv1.PinnedImageRef) error {
	// create a client conn to the container runtime.
	conn, err := cri.NewClientConn(ctx, p.runtimeEndpoint)
	if err != nil {
		return fmt.Errorf("failed to create client connection: %w", err)
	}
	defer conn.Close()

	// create a registry auth lookup from the authfile.
	client := cri.NewClient(conn)
	registryAuth, err := newRegistryAuth(p.authFilePath)
	if err != nil {
		return fmt.Errorf("failed to create registry auth map: %w", err)
	}

	workerCount, err := p.getWorkerCount()
	if err != nil {
		return err
	}

	// start workers
	prefetchCh := make(chan prefetch, len(prefetchImages))
	errCh := make(chan error, len(prefetchImages))
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go p.prefetchWorker(ctx, &wg, client, defaultPrefetchThrottleDuration, prefetchCh, errCh)
	}

	// update node annotation to indicate prefetching is in progress
	annotation := p.newAnnotation().
		WithReason(ImageSetReasonInProgress)
	if err := p.updateNodeAnnotation(annotation); err != nil {
		return fmt.Errorf("failed to update node annotation: %w", err)
	}

	// schedule work
	for _, imageRef := range prefetchImages {
		image := string(imageRef)
		authConfig, err := registryAuth.getAuthConfigForImage(image)
		if err != nil {
			return fmt.Errorf("failed to get auth config for image %s: %w", image, err)
		}
		prefetchCh <- prefetch{
			image: image,
			auth:  authConfig,
		}
	}
	close(prefetchCh)

	var lastErr error
	errDone := make(chan struct{})
	// log any errors that occurred during prefetching
	go func() {
		defer close(errDone)
		for err := range errCh {
			if err != nil {
				klog.Errorf("prefetch: %v", err)
				lastErr = err
			}
		}
	}()

	wg.Wait()
	close(errCh)
	<-errDone // wait for logs

	return lastErr
}

// ensurePullImage first checks if the image exists locally and then will attempt to pull
// the image from the container runtime with a retry/backoff.
func (p *PrefetchManager) ensurePullImage(ctx context.Context, client *cri.Client, image string, authConfig *runtimeapi.AuthConfig) error {
	exists, err := client.ImageStatus(ctx, image)
	if err != nil {
		return err
	}
	if exists {
		klog.V(4).Infof("image %q already exists", image)
		return nil
	}

	var lastErr error
	tries := 0
	err = wait.ExponentialBackoffWithContext(ctx, p.backoff, func(ctx context.Context) (bool, error) {
		tries++
		err := client.PullImage(ctx, image, authConfig)
		if err != nil {
			lastErr = err
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		mcdPrefetchImageFailure.Add(1)
		return fmt.Errorf("%w %q (%d tries): %w: %w", errFailedToPullImage, image, tries, err, lastErr)
	}

	// successful pull
	klog.V(4).Infof("image %q pulled", image)
	mcdPrefetchImageSuccess.Add(1)
	return nil
}

// ensurePinnedImageSetForNode ensures that the PinnedImageSet is applied to a MachineConfig deployed on this node.
// if the PinnedImageSet is not targeting this node, it will return an error and a boolean indicating if the error
// should be retried.
func (p *PrefetchManager) ensurePinnedImageSetForNode(imageSet *mcfgv1.PinnedImageSet) (bool, error) {
	node, err := p.nodeLister.Get(p.nodeName)
	if err != nil {
		return true, fmt.Errorf("failed to get the local node %q: %w", p.nodeName, err)
	}

	pools, err := ctrlcommon.GetPoolsForPinnedImageSet(p.mcpLister, imageSet)
	if err != nil {
		return true, fmt.Errorf("failed to get pools for PinnedImageSet %q: %w", imageSet.Name, err)
	}

	for _, pool := range pools {
		// only one PinnedImageSet allowed per MachineConfigPool
		sets, err := ctrlcommon.GetPinnedImageSetsForPool(p.imageSetLister, pool)
		if err != nil {
			return true, err
		}
		if len(sets) > 1 {
			return true, fmt.Errorf("%w: %s: ", errMultiplePinnedImageSets, pool.Name)
		}

		selector := labels.SelectorFromSet(pool.Spec.NodeSelector.MatchLabels)
		if selector.Matches(labels.Set(node.Labels)) {
			return false, nil
		}
	}
	// no requeue for this error
	return false, errNoMatchingPool
}

func (p *PrefetchManager) handleUpdate(oldObj, newObj interface{}) {
	oldImageSet := oldObj.(*mcfgv1.PinnedImageSet)
	newImageSet := newObj.(*mcfgv1.PinnedImageSet)

	if triggerObjectChange(oldImageSet, newImageSet) {
		p.enqueuePinnedImageSet(newImageSet)
	}
}

func (p *PrefetchManager) handleAdd(obj interface{}) {
	imageSet := obj.(*mcfgv1.PinnedImageSet)
	p.enqueuePinnedImageSet(imageSet)
}

func (p *PrefetchManager) handleDelete(obj interface{}) {
	imageSet := obj.(*mcfgv1.PinnedImageSet)
	_, err := p.ensurePinnedImageSetForNode(imageSet)
	if err != nil {
		klog.V(4).Infof("Deleted PinnedImageSet %s is not targeting this node: %v", imageSet.Name, err)
		return
	}

	node, err := p.nodeLister.Get(p.nodeName)
	if err != nil {
		klog.Errorf("failed to get node %q: %v", p.nodeName, err)
		return
	}
	// if this pinned image set is not the desired UID, do not delete the file.
	desiredUID := node.Annotations[imageSetDesired]
	if desiredUID != string(imageSet.UID) {
		klog.V(4).Infof("PinnedImageSet %s is not desired on this node: %s", imageSet.Name, desiredUID)
		return
	}

	// stop any existing tasks for this PinnedImageSet
	p.taskManager.stop(imageSet)

	if err := p.deleteConfigFileForPinnedImageSet(imageSet); err != nil {
		klog.Errorf("failed to delete crio config file: %v", err)
	}

	// reset node annotations
	annotation := p.newAnnotation().WithCurrent("").WithReason("").WithDesired("")
	if err := p.updateNodeAnnotation(annotation); err != nil {
		klog.Errorf("failed to update node annotation: %v", err)
	}
}

// worker runs a worker thread that just dequeues items, processes them, and marks them done.
// It enforces that the syncHandler is never invoked concurrently with the same key.
func (p *PrefetchManager) worker() {
	for p.processNextWorkItem() {
	}
}

func (p *PrefetchManager) Run(workers int, stopCh <-chan struct{}) {
	defer func() {
		utilruntime.HandleCrash()
		p.queue.ShutDown()
	}()

	if !cache.WaitForCacheSync(
		stopCh,
		p.imageSetSynced,
		p.nodeListerSynced,
		p.mcpSynced,
	) {
		return
	}

	klog.Infof("Starting PrefetchImageManager")
	defer klog.Infof("Shutting down PrefetchImageManager")

	for i := 0; i < workers; i++ {
		go wait.Until(p.worker, time.Second, stopCh)
	}

	<-stopCh
}

func (p *PrefetchManager) enqueue(cfg *mcfgv1.PinnedImageSet) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(cfg)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", cfg, err))
		return
	}
	p.queue.Add(key)
}

func (p *PrefetchManager) processNextWorkItem() bool {
	key, quit := p.queue.Get()
	if quit {
		return false
	}
	defer p.queue.Done(key)

	err := p.syncHandler(key.(string))
	p.handleErr(err, key)
	return true
}

func (p *PrefetchManager) handleErr(err error, key interface{}) {
	if err == nil {
		p.queue.Forget(key)
		return
	}

	if p.queue.NumRequeues(key) < maxRetriesController {
		klog.V(4).Infof("Requeue PinnedImageSet %v: %v", key, err)
		p.queue.AddRateLimited(key)
		return
	}
	utilruntime.HandleError(err)

	klog.Warningf(fmt.Sprintf("PinnedImageSet failed: %s max retries: %d", key, maxRetriesController))
	p.queue.Forget(key)
	p.queue.AddAfter(key, 1*time.Minute)
}

func (p *PrefetchManager) ensureNodeAllocatableStorage(ctx context.Context, images []mcfgv1.PinnedImageRef) error {
	node, err := p.nodeLister.Get(p.nodeName)
	if err != nil {
		return fmt.Errorf("failed to get node %q: %w", p.nodeName, err)
	}

	allocatableStorageBytes, ok := node.Status.Allocatable[corev1.ResourceEphemeralStorage]
	if !ok {
		return fmt.Errorf("node %q has no allocatable ephemeral storage", p.nodeName)
	}
	if allocatableStorageBytes.Value() < p.minStorageAvailableBytes {
		return errInsufficientStorage
	}

	// TODO: uncomment/inline when podman is updated to support image size auth
	// https://issues.redhat.com/browse/OCPNODE-1986
	// ensure the storage is available for the images
	// if err := p.ensureImagePayloadStorage(ctx, images, allocatableStorageBytes.Value()); err != nil {
	// 	return err
	// }

	return nil
}

func (p *PrefetchManager) ensureImagePayloadStorage(ctx context.Context, images []mcfgv1.PinnedImageRef, allocatableStorage int64) error {
	// calculate total required storage for all images.
	requiredStorage := int64(0)
	for _, image := range images {
		size, err := getImageSize(ctx, image, p.authFilePath)
		if err != nil {
			return err
		}

		// account for decompression
		requiredStorage += size * 2
	}

	klog.V(4).Infof("storage: allocatable=%d, required=%d", allocatableStorage, requiredStorage)
	if requiredStorage >= allocatableStorage-p.minStorageAvailableBytes {
		return errInsufficientStorage
	}

	return nil
}

func (p *PrefetchManager) postActionCrioReload() error {
	serviceName := constants.CRIOServiceName
	if err := reloadService(serviceName); err != nil {
		p.nodeWriter.Eventf(corev1.EventTypeWarning, "FailedServiceReload", fmt.Sprintf("Reloading %s service failed. Error: %v", serviceName, err))
		return fmt.Errorf("could not apply update: reloading %s configuration failed. Error: %w", serviceName, err)
	}
	return nil
}

func (p *PrefetchManager) updateNodeAnnotation(a *imageSetAnnotations) error {
	annotations := a.Get()
	if _, annoErr := p.nodeWriter.SetAnnotations(annotations); annoErr != nil {
		return fmt.Errorf("failed to set node annotation %v", annoErr)
	}

	return nil
}

func (p *PrefetchManager) newAnnotation() *imageSetAnnotations {
	return newImageSetAnnotations()
}

func getImageSize(_ context.Context, image mcfgv1.PinnedImageRef, authFilePath string) (int64, error) {
	args := []string{
		"manifest",
		"inspect",
		"--authfile", authFilePath,
		string(image),
	}

	output, err := exec.Command("podman", args...).CombinedOutput()
	if err != nil && strings.Contains(err.Error(), "manifest unknown") {
		return 0, errNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("failed to execute podman manifest inspect for %q: %w", image, err)
	}

	type Layer struct {
		MediaType string `json:"mediaType"`
		Size      int    `json:"size"`
		Digest    string `json:"digest"`
	}

	type manifests struct {
		Layers []Layer `json:"manifests"`
	}

	var manifest manifests
	err = json.Unmarshal(output, &manifest)
	if err != nil {
		return 0, fmt.Errorf("failed to unmarshal manifest for %q: %w", image, err)
	}

	var totalSize int64
	for _, layer := range manifest.Layers {
		totalSize += int64(layer.Size)
	}

	return totalSize, nil
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

func newRegistryAuth(authfile string) (*registryAuth, error) {
	data, err := os.ReadFile(authfile)
	if err != nil {
		return nil, fmt.Errorf("failed to read authfile: %w", err)
	}

	type authEntry struct {
		Auth  string `json:"auth"`
		Email string `json:"email"`
	}

	type auths struct {
		Auths map[string]authEntry `json:"auths"`
	}

	var a auths
	err = json.Unmarshal(data, &a)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal pull secret: %w", err)
	}

	authMap := make(map[string]*runtimeapi.AuthConfig, len(a.Auths))
	for registry, entry := range a.Auths {
		// auth is base64 encoded and in the format username:password
		decoded, err := base64.StdEncoding.DecodeString(entry.Auth)
		if err != nil {
			return nil, fmt.Errorf("failed to decode auth for registry %q: %w", registry, err)
		}
		parts := strings.SplitN(string(decoded), ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid auth for registry %q", registry)
		}
		authMap[registry] = &runtimeapi.AuthConfig{
			Username: parts[0],
			Password: parts[1],
		}
	}

	return &registryAuth{auth: authMap}, nil
}

type registryAuth struct {
	mu   sync.Mutex
	auth map[string]*runtimeapi.AuthConfig
}

func (r *registryAuth) getAuthConfigForImage(image string) (*runtimeapi.AuthConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	parsed, err := reference.ParseNormalizedNamed(strings.TrimSpace(image))
	if err != nil {
		return nil, err
	}

	auth, ok := r.auth[reference.Domain(parsed)]
	if ok {
		return auth, nil
	}
	return nil, nil
}

// createCrioConfigIgnitionFile creates an ignition file for the pinned-image crio config.
func createCrioConfigIgnitionFile(imageSet *mcfgv1.PinnedImageSet) (ign3types.File, error) {
	type tomlConfig struct {
		Crio struct {
			Image struct {
				PinnedImages []mcfgv1.PinnedImageRef `toml:"pinned_images,omitempty"`
			} `toml:"image"`
		} `toml:"crio"`
	}

	tomlConf := tomlConfig{}
	tomlConf.Crio.Image.PinnedImages = imageSet.Spec.PinnedImages

	// TODO: custom encoder that can write each image on newline
	var buf bytes.Buffer
	encoder := toml.NewEncoder(&buf)
	if err := encoder.Encode(tomlConf); err != nil {
		return ign3types.File{}, fmt.Errorf("failed to encode toml: %w", err)
	}

	return ctrlcommon.NewIgnFileBytes(crioPinnedImagesDropInFilePath, buf.Bytes()), nil
}

// prefetch represents a task to prefetch an image.
type prefetch struct {
	image string
	auth  *runtimeapi.AuthConfig
}

// taskManager manages the worker tasks for each PinnedImageSet.
type taskManager struct {
	mu          sync.Mutex
	cancelFuncs map[types.UID]context.CancelFunc
}

func (t *taskManager) add(imageSet *mcfgv1.PinnedImageSet, cancelFunc context.CancelFunc) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.cancelFuncs[imageSet.UID] = cancelFunc
}

func (t *taskManager) stop(imageSet *mcfgv1.PinnedImageSet) {
	t.mu.Lock()
	defer t.mu.Unlock()

	uid := imageSet.UID
	cancelFn, ok := t.cancelFuncs[uid]
	if !ok {
		klog.V(4).Infof("no tasks currently managed for PinnedImageSet UID: %s", uid)
		return
	}

	klog.V(4).Infof("stopping tasks for PinnedImageSet UID: %s", uid)
	cancelFn()
	delete(t.cancelFuncs, uid)
}

func newImageSetAnnotations() *imageSetAnnotations {
	return &imageSetAnnotations{
		annos: make(map[string]string),
	}
}

type imageSetAnnotations struct {
	annos map[string]string
}

func (p *imageSetAnnotations) WithDesired(desired string) *imageSetAnnotations {
	p.annos[constants.MachineConfigDaemonPinnedImageSetPrefetchDesiredAnnotationKey] = desired
	return p
}

func (p *imageSetAnnotations) WithCurrent(current string) *imageSetAnnotations {
	p.annos[constants.MachineConfigDaemonPinnedImageSetPrefetchCurrentAnnotationKey] = current
	return p
}

func (p *imageSetAnnotations) WithReason(reason string) *imageSetAnnotations {
	p.annos[constants.MachineConfigDaemonPinnedImageSetPrefetchReasonAnnotationKey] = reason
	return p
}

func (p *imageSetAnnotations) Get() map[string]string {
	return p.annos
}
