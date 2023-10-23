package build

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/containers/image/v5/docker/reference"
	buildv1 "github.com/openshift/api/build/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/scheme"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	corev1 "k8s.io/api/core/v1"
	aggerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	coreclientsetv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	buildinformers "github.com/openshift/client-go/build/informers/externalversions"

	buildinformersv1 "github.com/openshift/client-go/build/informers/externalversions/build/v1"

	buildclientset "github.com/openshift/client-go/build/clientset/versioned"

	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	mcfginformers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"

	mcfginformersv1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"

	coreinformers "k8s.io/client-go/informers"
	coreinformersv1 "k8s.io/client-go/informers/core/v1"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/openshift/machine-config-operator/internal/clients"
)

const (
	targetMachineConfigPoolLabel = "machineconfiguration.openshift.io/targetMachineConfigPool"
	// TODO(zzlotnik): Is there a constant for this someplace else?
	desiredConfigLabel = "machineconfiguration.openshift.io/desiredConfig"
)

// on-cluster-build-custom-dockerfile ConfigMap name.
const (
	customDockerfileConfigMapName = "on-cluster-build-custom-dockerfile"
)

// on-cluster-build-config ConfigMap keys.
const (
	// Name of ConfigMap which contains knobs for configuring the build controller.
	OnClusterBuildConfigMapName = "on-cluster-build-config"

	// The on-cluster-build-config ConfigMap key which contains a K8s secret capable of pulling of the base OS image.
	BaseImagePullSecretNameConfigKey = "baseImagePullSecretName"

	// The on-cluster-build-config ConfigMap key which contains a K8s secret capable of pushing the final OS image.
	FinalImagePushSecretNameConfigKey = "finalImagePushSecretName"

	// The on-cluster-build-config ConfigMap key which contains the pullspec of where to push the final OS image (e.g., registry.hostname.com/org/repo:tag).
	FinalImagePullspecConfigKey = "finalImagePullspec"
)

// machine-config-osimageurl ConfigMap keys.
const (
	// TODO: Is this a constant someplace else?
	machineConfigOSImageURLConfigMapName = "machine-config-osimageurl"

	// The machine-config-osimageurl ConfigMap key which contains the pullspec of the base OS image (e.g., registry.hostname.com/org/repo:tag).
	baseOSContainerImageConfigKey = "baseOSContainerImage"

	// The machine-config-osimageurl ConfigMap key which contains the pullspec of the base OS image (e.g., registry.hostname.com/org/repo:tag).
	baseOSExtensionsContainerImageConfigKey = "baseOSExtensionsContainerImage"

	// The machine-config-osimageurl ConfigMap key which contains the current OpenShift release version.
	releaseVersionConfigKey = "releaseVersion"

	// The machine-config-osimageurl ConfigMap key which contains the osImageURL
	// value. I don't think we actually use this anywhere though.
	osImageURLConfigKey = "osImageURL"
)

// Image builder constants.
const (
	// ImageBuilderTypeConfigMapKey is the key in the ConfigMap that determines which type of image builder to use.
	ImageBuilderTypeConfigMapKey string = "imageBuilderType"

	// OpenshiftImageBuilder is the constant indicating use of the OpenShift image builder.
	OpenshiftImageBuilder string = "openshift-image-builder"

	// CustomPodImageBuilder is the constant indicating use of the custom pod image builder.
	CustomPodImageBuilder string = "custom-pod-builder"
)

var (
	// controllerKind contains the schema.GroupVersionKind for this controller type.
	//nolint:varcheck,deadcode // This will be used eventually
	controllerKind = mcfgv1.SchemeGroupVersion.WithKind("MachineConfigPool")
)

//nolint:revive // If I name this ControllerConfig, that name will be overloaded :P
type BuildControllerConfig struct {
	// updateDelay is a pause to deal with churn in MachineConfigs; see
	// https://github.com/openshift/machine-config-operator/issues/301
	// Default: 5 seconds
	UpdateDelay time.Duration

	// maxRetries is the number of times a machineconfig pool will be retried before it is dropped out of the queue.
	// With the current rate-limiter in use (5ms*2^(maxRetries-1)) the following numbers represent the times
	// a machineconfig pool is going to be requeued:
	//
	// 5ms, 10ms, 20ms, 40ms, 80ms, 160ms, 320ms, 640ms, 1.3s, 2.6s, 5.1s, 10.2s, 20.4s, 41s, 82s
	// Default: 5
	MaxRetries int
}

type ImageBuilder interface {
	Run(context.Context, int)
	StartBuild(ImageBuildRequest) (*corev1.ObjectReference, error)
	IsBuildRunning(*mcfgv1.MachineConfigPool) (bool, error)
	DeleteBuildObject(*mcfgv1.MachineConfigPool) error
	FinalPullspec(*mcfgv1.MachineConfigPool) (string, error)
}

// Controller defines the build controller.
type Controller struct {
	*Clients
	*informers

	eventRecorder record.EventRecorder

	syncHandler              func(mcp string) error
	enqueueMachineConfigPool func(*mcfgv1.MachineConfigPool)

	ccLister  mcfglistersv1.ControllerConfigLister
	mcpLister mcfglistersv1.MachineConfigPoolLister

	ccListerSynced  cache.InformerSynced
	mcpListerSynced cache.InformerSynced
	podListerSynced cache.InformerSynced

	queue workqueue.RateLimitingInterface

	config       BuildControllerConfig
	imageBuilder ImageBuilder
}

// Creates a BuildControllerConfig with sensible production defaults.
func DefaultBuildControllerConfig() BuildControllerConfig {
	return BuildControllerConfig{
		MaxRetries:  5,
		UpdateDelay: time.Second * 5,
	}
}

// Holds each of the clients used by the Build Controller and its subcontrollers.
type Clients struct {
	mcfgclient  mcfgclientset.Interface
	kubeclient  clientset.Interface
	buildclient buildclientset.Interface
}

func NewClientsFromControllerContext(ctrlCtx *ctrlcommon.ControllerContext) *Clients {
	return NewClients(ctrlCtx.ClientBuilder)
}

func NewClients(cb *clients.Builder) *Clients {
	return &Clients{
		mcfgclient:  cb.MachineConfigClientOrDie("machine-os-builder"),
		kubeclient:  cb.KubeClientOrDie("machine-os-builder"),
		buildclient: cb.BuildClientOrDie("machine-os-builder"),
	}
}

// Holds and starts each of the infomrers used by the Build Controller and its subcontrollers.
type informers struct {
	ccInformer    mcfginformersv1.ControllerConfigInformer
	mcpInformer   mcfginformersv1.MachineConfigPoolInformer
	buildInformer buildinformersv1.BuildInformer
	podInformer   coreinformersv1.PodInformer
	toStart       []interface{ Start(<-chan struct{}) }
}

// Starts the informers, wiring them up to the provided context.
func (i *informers) start(ctx context.Context) {
	for _, startable := range i.toStart {
		startable.Start(ctx.Done())
	}
}

// Creates new informer instances from a given Clients(set).
func newInformers(bcc *Clients) *informers {
	ccInformer := mcfginformers.NewSharedInformerFactory(bcc.mcfgclient, 0)
	mcpInformer := mcfginformers.NewSharedInformerFactory(bcc.mcfgclient, 0)
	buildInformer := buildinformers.NewSharedInformerFactoryWithOptions(bcc.buildclient, 0, buildinformers.WithNamespace(ctrlcommon.MCONamespace))
	podInformer := coreinformers.NewSharedInformerFactoryWithOptions(bcc.kubeclient, 0, coreinformers.WithNamespace(ctrlcommon.MCONamespace))

	return &informers{
		ccInformer:    ccInformer.Machineconfiguration().V1().ControllerConfigs(),
		mcpInformer:   mcpInformer.Machineconfiguration().V1().MachineConfigPools(),
		buildInformer: buildInformer.Build().V1().Builds(),
		podInformer:   podInformer.Core().V1().Pods(),
		toStart: []interface{ Start(<-chan struct{}) }{
			ccInformer,
			mcpInformer,
			buildInformer,
			podInformer,
		},
	}
}

// Creates a basic Build Controller instance without configuring an ImageBuilder.
func newBuildController(
	ctrlConfig BuildControllerConfig,
	clients *Clients,
) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&coreclientsetv1.EventSinkImpl{Interface: clients.kubeclient.CoreV1().Events("")})

	ctrl := &Controller{
		informers:     newInformers(clients),
		Clients:       clients,
		eventRecorder: eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineosbuilder-buildcontroller"}),
		queue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "machineosbuilder-buildcontroller"),
		config:        ctrlConfig,
	}

	ctrl.mcpInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addMachineConfigPool,
		UpdateFunc: ctrl.updateMachineConfigPool,
		DeleteFunc: ctrl.deleteMachineConfigPool,
	})

	ctrl.syncHandler = ctrl.syncMachineConfigPool
	ctrl.enqueueMachineConfigPool = ctrl.enqueueDefault

	ctrl.ccLister = ctrl.ccInformer.Lister()
	ctrl.mcpLister = ctrl.mcpInformer.Lister()

	ctrl.ccListerSynced = ctrl.ccInformer.Informer().HasSynced
	ctrl.mcpListerSynced = ctrl.mcpInformer.Informer().HasSynced

	return ctrl
}

// Creates a Build Controller instance with a custom pod builder implementation
// for the ImageBuilder.
func NewWithCustomPodBuilder(
	ctrlConfig BuildControllerConfig,
	clients *Clients,
) *Controller {
	ctrl := newBuildController(ctrlConfig, clients)
	ctrl.imageBuilder = newPodBuildController(ctrlConfig, clients, ctrl.customBuildPodUpdater)
	return ctrl
}

// Creates a Build Controller instance with an OpenShift Image Builder
// implementation for the ImageBuilder.
func NewWithImageBuilder(
	ctrlConfig BuildControllerConfig,
	clients *Clients,
) *Controller {
	ctrl := newBuildController(ctrlConfig, clients)
	ctrl.imageBuilder = newImageBuildController(ctrlConfig, clients, ctrl.imageBuildUpdater)
	return ctrl
}

// Run executes the render controller.
// TODO: Make this use a context instead of a stop channel.
func (ctrl *Controller) Run(parentCtx context.Context, workers int) {
	klog.Info("Starting MachineOSBuilder-BuildController")
	defer klog.Info("Shutting down MachineOSBuilder-BuildController")

	// Not sure if I actually need a child context here or not.
	ctx, cancel := context.WithCancel(parentCtx)
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()
	defer cancel()

	ctrl.informers.start(ctx)

	if !cache.WaitForCacheSync(ctx.Done(), ctrl.mcpListerSynced, ctrl.ccListerSynced) {
		return
	}

	go ctrl.imageBuilder.Run(ctx, workers)

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.worker, time.Second, ctx.Done())
	}

	<-ctx.Done()
}

func (ctrl *Controller) enqueue(pool *mcfgv1.MachineConfigPool) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pool)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", pool, err))
		return
	}

	ctrl.queue.Add(key)
}

func (ctrl *Controller) enqueueRateLimited(pool *mcfgv1.MachineConfigPool) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pool)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", pool, err))
		return
	}

	ctrl.queue.AddRateLimited(key)
}

// enqueueAfter will enqueue a pool after the provided amount of time.
func (ctrl *Controller) enqueueAfter(pool *mcfgv1.MachineConfigPool, after time.Duration) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pool)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", pool, err))
		return
	}

	ctrl.queue.AddAfter(key, after)
}

// enqueueDefault calls a default enqueue function
func (ctrl *Controller) enqueueDefault(pool *mcfgv1.MachineConfigPool) {
	ctrl.enqueueAfter(pool, ctrl.config.UpdateDelay)
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

// Reconciles the MachineConfigPool state with the state of an OpenShift Image
// Builder object.
func (ctrl *Controller) imageBuildUpdater(build *buildv1.Build) error {
	pool, err := ctrl.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(context.TODO(), build.Labels[targetMachineConfigPoolLabel], metav1.GetOptions{})
	if err != nil {
		return err
	}

	klog.Infof("Build (%s) is %s", build.Name, build.Status.Phase)

	objRef := toObjectRef(build)

	ps := newPoolState(pool)

	switch build.Status.Phase {
	case buildv1.BuildPhaseNew, buildv1.BuildPhasePending:
		if !ps.IsBuildPending() {
			err = ctrl.markBuildPendingWithObjectRef(ps, *objRef)
		}
	case buildv1.BuildPhaseRunning:
		// If we're running, then there's nothing to do right now.
		if !ps.IsBuilding() {
			err = ctrl.markBuildInProgress(ps)
		}
	case buildv1.BuildPhaseComplete:
		// If we've succeeded, we need to update the pool to indicate that.
		if !ps.IsBuildSuccess() {
			err = ctrl.markBuildSucceeded(ps)
		}
	case buildv1.BuildPhaseFailed, buildv1.BuildPhaseError, buildv1.BuildPhaseCancelled:
		// If we've failed, errored, or cancelled, we need to update the pool to indicate that.
		if !ps.IsBuildFailure() {
			err = ctrl.markBuildFailed(ps)
		}
	}

	if err != nil {
		return err
	}

	ctrl.enqueueMachineConfigPool(pool)
	return nil
}

// Reconciles the MachineConfigPool state with the state of a custom pod object.
func (ctrl *Controller) customBuildPodUpdater(pod *corev1.Pod) error {
	pool, err := ctrl.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(context.TODO(), pod.Labels[targetMachineConfigPoolLabel], metav1.GetOptions{})
	if err != nil {
		return err
	}

	klog.Infof("Build pod (%s) is %s", pod.Name, pod.Status.Phase)

	ps := newPoolState(pool)

	switch pod.Status.Phase {
	case corev1.PodPending:
		if !ps.IsBuildPending() {
			objRef := toObjectRef(pod)
			err = ctrl.markBuildPendingWithObjectRef(ps, *objRef)
		}
	case corev1.PodRunning:
		// If we're running, then there's nothing to do right now.
		if !ps.IsBuilding() {
			err = ctrl.markBuildInProgress(ps)
		}
	case corev1.PodSucceeded:
		// If we've succeeded, we need to update the pool to indicate that.
		if !ps.IsBuildSuccess() {
			err = ctrl.markBuildSucceeded(ps)
		}
	case corev1.PodFailed:
		// If we've failed, we need to update the pool to indicate that.
		if !ps.IsBuildFailure() {
			err = ctrl.markBuildFailed(ps)
		}
	}

	if err != nil {
		return err
	}

	ctrl.enqueueMachineConfigPool(pool)
	return nil
}

func (ctrl *Controller) handleErr(err error, key interface{}) {
	if err == nil {
		ctrl.queue.Forget(key)
		return
	}

	if ctrl.queue.NumRequeues(key) < ctrl.config.MaxRetries {
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
	machineconfigpool, err := ctrl.mcpLister.Get(name)
	if k8serrors.IsNotFound(err) {
		klog.V(2).Infof("MachineConfigPool %v has been deleted", key)
		return nil
	}
	if err != nil {
		return err
	}

	// TODO: Doing a deep copy of this pool object from our cache and using it to
	// determine our next course of action sometimes causes a race condition. I'm
	// not sure if it's better to get a current copy from the API server or what.
	// pool := machineconfigpool.DeepCopy()
	pool, err := ctrl.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(context.TODO(), machineconfigpool.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	ps := newPoolState(pool)

	// Not a layered pool, so stop here.
	if !ps.IsLayered() {
		klog.V(4).Infof("MachineConfigPool %s is not opted-in for layering, ignoring", pool.Name)
		return nil
	}

	switch {
	case ps.IsDegraded():
		klog.V(4).Infof("MachineConfigPool %s is degraded, requeueing", pool.Name)
		ctrl.enqueueMachineConfigPool(pool)
		return nil
	case ps.IsRenderDegraded():
		klog.V(4).Infof("MachineConfigPool %s is render degraded, requeueing", pool.Name)
		ctrl.enqueueMachineConfigPool(pool)
		return nil
	case ps.IsBuildPending():
		klog.V(4).Infof("MachineConfigPool %s is build pending", pool.Name)
		return nil
	case ps.IsBuilding():
		klog.V(4).Infof("MachineConfigPool %s is building", pool.Name)
		return nil
	case ps.IsBuildSuccess():
		klog.V(4).Infof("MachineConfigPool %s has successfully built", pool.Name)
		return nil
	default:
		shouldBuild, err := shouldWeDoABuild(ctrl.imageBuilder, pool, pool)
		if err != nil {
			return fmt.Errorf("could not determine if a build is required for MachineConfigPool %q: %w", pool.Name, err)
		}

		if shouldBuild {
			return ctrl.startBuildForMachineConfigPool(ps)
		}

		klog.V(4).Infof("Nothing to do for pool %q", pool.Name)
	}

	// For everything else
	return ctrl.syncAvailableStatus(pool)
}

// Marks a given MachineConfigPool as a failed build.
func (ctrl *Controller) markBuildFailed(ps *poolState) error {
	klog.Errorf("Build failed for pool %s", ps.Name())

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		mcp, err := ctrl.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(context.TODO(), ps.Name(), metav1.GetOptions{})
		if err != nil {
			return err
		}

		ps := newPoolState(mcp)
		ps.SetBuildConditions([]mcfgv1.MachineConfigPoolCondition{
			{
				Type:   mcfgv1.MachineConfigPoolBuildFailed,
				Reason: "BuildFailed",
				Status: corev1.ConditionTrue,
			},
			{
				Type:   mcfgv1.MachineConfigPoolBuildSuccess,
				Status: corev1.ConditionFalse,
			},
			{
				Type:   mcfgv1.MachineConfigPoolBuilding,
				Status: corev1.ConditionFalse,
			},
			{
				Type:   mcfgv1.MachineConfigPoolBuildPending,
				Status: corev1.ConditionFalse,
			},
			{
				Type:   mcfgv1.MachineConfigPoolDegraded,
				Status: corev1.ConditionTrue,
			},
		})

		return ctrl.syncFailingStatus(ps.MachineConfigPool(), fmt.Errorf("build failed"))
	})
}

// Marks a given MachineConfigPool as the build is in progress.
func (ctrl *Controller) markBuildInProgress(ps *poolState) error {
	klog.Infof("Build in progress for MachineConfigPool %s, config %s", ps.Name(), ps.CurrentMachineConfig())

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		mcp, err := ctrl.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(context.TODO(), ps.Name(), metav1.GetOptions{})
		if err != nil {
			return err
		}

		ps := newPoolState(mcp)
		ps.SetBuildConditions([]mcfgv1.MachineConfigPoolCondition{
			{
				Type:   mcfgv1.MachineConfigPoolBuildFailed,
				Status: corev1.ConditionFalse,
			},
			{
				Type:   mcfgv1.MachineConfigPoolBuildSuccess,
				Status: corev1.ConditionFalse,
			},
			{
				Type:   mcfgv1.MachineConfigPoolBuilding,
				Reason: "BuildRunning",
				Status: corev1.ConditionTrue,
			},
			{
				Type:   mcfgv1.MachineConfigPoolBuildPending,
				Status: corev1.ConditionFalse,
			},
		})

		return ctrl.syncAvailableStatus(ps.MachineConfigPool())
	})
}

// Deletes the ephemeral objects we created to perform this specific build.
func (ctrl *Controller) postBuildCleanup(pool *mcfgv1.MachineConfigPool, ignoreMissing bool) error {
	// Delete the actual build object itself.
	deleteBuildObject := func() error {
		err := ctrl.imageBuilder.DeleteBuildObject(pool)

		if err == nil {
			klog.Infof("Deleted build object %s", newImageBuildRequest(pool).getBuildName())
		}

		return err
	}

	// Delete the ConfigMap containing the MachineConfig.
	deleteMCConfigMap := func() error {
		ibr := newImageBuildRequest(pool)

		err := ctrl.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Delete(context.TODO(), ibr.getMCConfigMapName(), metav1.DeleteOptions{})

		if err == nil {
			klog.Infof("Deleted MachineConfig ConfigMap %s for build %s", ibr.getMCConfigMapName(), ibr.getBuildName())
		}

		return err
	}

	// Delete the ConfigMap containing the rendered Dockerfile.
	deleteDockerfileConfigMap := func() error {
		ibr := newImageBuildRequest(pool)

		err := ctrl.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Delete(context.TODO(), ibr.getDockerfileConfigMapName(), metav1.DeleteOptions{})

		if err == nil {
			klog.Infof("Deleted Dockerfile ConfigMap %s for build %s", ibr.getDockerfileConfigMapName(), ibr.getBuildName())
		}

		return err
	}

	maybeIgnoreMissing := func(f func() error) func() error {
		return func() error {
			if ignoreMissing {
				return ignoreIsNotFoundErr(f())
			}

			return f()
		}
	}

	// If *any* of these we fail, we want to emit an error. If *all* fail, we
	// want all of the error messages.
	return aggerrors.AggregateGoroutines(
		maybeIgnoreMissing(deleteBuildObject),
		maybeIgnoreMissing(deleteMCConfigMap),
		maybeIgnoreMissing(deleteDockerfileConfigMap),
	)
}

// Marks a given MachineConfigPool as build successful and cleans up after itself.
func (ctrl *Controller) markBuildSucceeded(ps *poolState) error {
	klog.Infof("Build succeeded for MachineConfigPool %s, config %s", ps.Name(), ps.CurrentMachineConfig())

	pool := ps.MachineConfigPool()

	// Get the final image pullspec.
	imagePullspec, err := ctrl.imageBuilder.FinalPullspec(pool)
	if err != nil {
		return fmt.Errorf("could not get final image pullspec for pool %s: %w", ps.Name(), err)
	}

	if imagePullspec == "" {
		return fmt.Errorf("image pullspec empty for pool %s", ps.Name())
	}

	// Perform the post-build cleanup.
	if err := ctrl.postBuildCleanup(pool, false); err != nil {
		return fmt.Errorf("could not do post-build cleanup: %w", err)
	}

	// Perform the MachineConfigPool update.
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		mcp, err := ctrl.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(context.TODO(), ps.Name(), metav1.GetOptions{})
		if err != nil {
			return err
		}

		ps := newPoolState(mcp)

		// Set the annotation or field to point to the newly-built container image.
		klog.V(4).Infof("Setting new image pullspec for %s to %s", ps.Name(), imagePullspec)
		ps.SetImagePullspec(imagePullspec)

		// Remove the build object reference from the MachineConfigPool since we're
		// not using it anymore.
		ps.DeleteBuildRefForCurrentMachineConfig()

		// Adjust the MachineConfigPool status to indicate success.
		ps.SetBuildConditions([]mcfgv1.MachineConfigPoolCondition{
			{
				Type:   mcfgv1.MachineConfigPoolBuildFailed,
				Status: corev1.ConditionFalse,
			},
			{
				Type:   mcfgv1.MachineConfigPoolBuildSuccess,
				Reason: "BuildSucceeded",
				Status: corev1.ConditionTrue,
			},
			{
				Type:   mcfgv1.MachineConfigPoolBuilding,
				Status: corev1.ConditionFalse,
			},
			{
				Type:   mcfgv1.MachineConfigPoolDegraded,
				Status: corev1.ConditionFalse,
			},
		})

		return ctrl.updatePoolAndSyncAvailableStatus(ps.MachineConfigPool())
	})
}

// Marks a given MachineConfigPool as build pending.
func (ctrl *Controller) markBuildPendingWithObjectRef(ps *poolState, objRef corev1.ObjectReference) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		mcp, err := ctrl.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(context.TODO(), ps.Name(), metav1.GetOptions{})
		if err != nil {
			return err
		}

		ps := newPoolState(mcp)

		klog.Infof("Build for %s marked pending with object reference %v", ps.Name(), objRef)

		ps.SetBuildConditions([]mcfgv1.MachineConfigPoolCondition{
			{
				Type:   mcfgv1.MachineConfigPoolBuildFailed,
				Status: corev1.ConditionFalse,
			},
			{
				Type:   mcfgv1.MachineConfigPoolBuildSuccess,
				Status: corev1.ConditionFalse,
			},
			{
				Type:   mcfgv1.MachineConfigPoolBuilding,
				Status: corev1.ConditionFalse,
			},
			{
				Type:   mcfgv1.MachineConfigPoolBuildPending,
				Reason: "BuildPending",
				Status: corev1.ConditionTrue,
			},
		})

		// If the MachineConfigPool has the build object reference, we just want to
		// update the MachineConfigPool's status.
		if ps.HasBuildObjectRef(objRef) {
			return ctrl.syncAvailableStatus(ps.MachineConfigPool())
		}

		// If we added the build object reference, we need to update both the
		// MachineConfigPool itself and its status.
		if err := ps.AddBuildObjectRef(objRef); err != nil {
			return err
		}

		return ctrl.updatePoolAndSyncAvailableStatus(ps.MachineConfigPool())
	})
}

func (ctrl *Controller) updatePoolAndSyncAvailableStatus(pool *mcfgv1.MachineConfigPool) error {
	// We need to do an API server round-trip to ensure all of our mutations get
	// propagated.
	updatedPool, err := ctrl.mcfgclient.MachineconfigurationV1().MachineConfigPools().Update(context.TODO(), pool, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("could not update MachineConfigPool %q: %w", pool.Name, err)
	}

	updatedPool.Status = pool.Status

	return ctrl.syncAvailableStatus(updatedPool)
}

// Machine Config Pools

func (ctrl *Controller) addMachineConfigPool(obj interface{}) {
	pool := obj.(*mcfgv1.MachineConfigPool).DeepCopy()
	klog.V(4).Infof("Adding MachineConfigPool %s", pool.Name)
	ctrl.enqueueMachineConfigPool(pool)
}

func (ctrl *Controller) getBuildInputs(ps *poolState) (*buildInputs, error) {
	osImageURL, err := ctrl.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(context.TODO(), machineConfigOSImageURLConfigMapName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not get OS image URL: %w", err)
	}

	onClusterBuildConfig, err := ctrl.getOnClusterBuildConfig(ps)
	if err != nil {
		return nil, fmt.Errorf("could not get configmap %q: %w", OnClusterBuildConfigMapName, err)
	}

	customDockerfiles, err := ctrl.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(context.TODO(), customDockerfileConfigMapName, metav1.GetOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return nil, fmt.Errorf("could not retrieve %s ConfigMap: %w", customDockerfileConfigMapName, err)
	}

	currentMC := ps.CurrentMachineConfig()

	mc, err := ctrl.mcfgclient.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), currentMC, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not get MachineConfig %s: %w", currentMC, err)
	}

	inputs := &buildInputs{
		onClusterBuildConfig: onClusterBuildConfig,
		osImageURL:           osImageURL,
		customDockerfiles:    customDockerfiles,
		pool:                 ps.MachineConfigPool(),
		machineConfig:        mc,
	}

	return inputs, nil
}

// Prepares all of the objects needed to perform an image build.
func (ctrl *Controller) prepareForBuild(inputs *buildInputs) (ImageBuildRequest, error) {
	ibr := newImageBuildRequestFromBuildInputs(inputs)

	mcConfigMap, err := ibr.toConfigMap(inputs.machineConfig)
	if err != nil {
		return ImageBuildRequest{}, fmt.Errorf("could not convert MachineConfig %s into ConfigMap: %w", inputs.machineConfig.Name, err)
	}

	_, err = ctrl.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Create(context.TODO(), mcConfigMap, metav1.CreateOptions{})
	if err != nil {
		return ImageBuildRequest{}, fmt.Errorf("could not load rendered MachineConfig %s into configmap: %w", mcConfigMap.Name, err)
	}

	klog.Infof("Stored MachineConfig %s in ConfigMap %s for build", inputs.machineConfig.Name, mcConfigMap.Name)

	dockerfileConfigMap, err := ibr.dockerfileToConfigMap()
	if err != nil {
		return ImageBuildRequest{}, fmt.Errorf("could not generate Dockerfile ConfigMap: %w", err)
	}

	_, err = ctrl.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Create(context.TODO(), dockerfileConfigMap, metav1.CreateOptions{})
	if err != nil {
		return ImageBuildRequest{}, fmt.Errorf("could not load rendered Dockerfile %s into configmap: %w", dockerfileConfigMap.Name, err)
	}

	klog.Infof("Stored Dockerfile for build %s in ConfigMap %s for build", ibr.getBuildName(), dockerfileConfigMap.Name)

	return ibr, nil
}

// Determines if we should run a build, then starts a build pod to perform the
// build, and updates the MachineConfigPool with an object reference for the
// build pod.
func (ctrl *Controller) startBuildForMachineConfigPool(ps *poolState) error {
	inputs, err := ctrl.getBuildInputs(ps)
	if err != nil {
		return fmt.Errorf("could not fetch build inputs: %w", err)
	}

	ibr, err := ctrl.prepareForBuild(inputs)
	if err != nil {
		return fmt.Errorf("could not start build for MachineConfigPool %s: %w", ps.Name(), err)
	}

	objRef, err := ctrl.imageBuilder.StartBuild(ibr)

	if err != nil {
		return err
	}

	return ctrl.markBuildPendingWithObjectRef(ps, *objRef)
}

// Gets the ConfigMap which specifies the name of the base image pull secret, final image pull secret, and final image pullspec.
func (ctrl *Controller) getOnClusterBuildConfig(ps *poolState) (*corev1.ConfigMap, error) {
	onClusterBuildConfigMap, err := ctrl.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(context.TODO(), OnClusterBuildConfigMapName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not get build controller config %q: %w", OnClusterBuildConfigMapName, err)
	}

	requiredKeys := []string{
		BaseImagePullSecretNameConfigKey,
		FinalImagePushSecretNameConfigKey,
		FinalImagePullspecConfigKey,
	}

	needToUpdateConfigMap := false
	finalImagePullspecWithTag := ""

	currentMC := ps.CurrentMachineConfig()

	for _, key := range requiredKeys {
		val, ok := onClusterBuildConfigMap.Data[key]
		if !ok {
			return nil, fmt.Errorf("missing required key %q in configmap %s", key, OnClusterBuildConfigMapName)
		}

		if key == BaseImagePullSecretNameConfigKey || key == FinalImagePushSecretNameConfigKey {
			secret, err := ctrl.validatePullSecret(val)
			if err != nil {
				return nil, err
			}

			if strings.Contains(secret.Name, "canonical") {
				klog.Infof("Updating build controller config %s to indicate we have a canonicalized secret %s", OnClusterBuildConfigMapName, secret.Name)
				onClusterBuildConfigMap.Data[key] = secret.Name
				needToUpdateConfigMap = true
			}
		}

		if key == FinalImagePullspecConfigKey {
			// Replace the user-supplied tag (if present) with the name of the
			// rendered MachineConfig for uniqueness. This will also allow us to
			// eventually do a pre-build registry query to determine if we need to
			// perform a build.
			named, err := reference.ParseNamed(val)
			if err != nil {
				return nil, fmt.Errorf("could not parse %s with %q: %w", key, val, err)
			}

			tagged, err := reference.WithTag(named, currentMC)
			if err != nil {
				return nil, fmt.Errorf("could not add tag %s to image pullspec %s: %w", currentMC, val, err)
			}

			finalImagePullspecWithTag = tagged.String()
		}
	}

	// If we had to canonicalize a secret, that means the ConfigMap no longer
	// points to the expected secret. So let's update the ConfigMap in the API
	// server for the sake of consistency.
	if needToUpdateConfigMap {
		klog.Infof("Updating build controller config")
		// TODO: Figure out why this causes failures with resourceVersions.
		onClusterBuildConfigMap, err = ctrl.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Update(context.TODO(), onClusterBuildConfigMap, metav1.UpdateOptions{})
		if err != nil {
			return nil, fmt.Errorf("could not update configmap %q: %w", OnClusterBuildConfigMapName, err)
		}
	}

	// We don't want to write this back to the API server since it's only useful
	// for this specific build. TODO: Migrate this to the ImageBuildRequest
	// object so that it's generated on-demand instead.
	onClusterBuildConfigMap.Data[FinalImagePullspecConfigKey] = finalImagePullspecWithTag

	return onClusterBuildConfigMap, err
}

// Ensure that the supplied pull secret exists, is in the correct format, etc.
func (ctrl *Controller) validatePullSecret(name string) (*corev1.Secret, error) {
	secret, err := ctrl.kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	oldSecretName := secret.Name

	secret, err = canonicalizePullSecret(secret)
	if err != nil {
		return nil, err
	}

	// If a Docker pull secret lacks the top-level "auths" key, this means that
	// it is a legacy-style pull secret. Buildah does not know how to correctly
	// use one of these secrets. With that in mind, we "canonicalize" it, meaning
	// we inject the existing legacy secret into a {"auths": {}} schema that
	// Buildah can understand. We create a new K8s secret with this info and pass
	// that secret into our image builder instead.
	if strings.HasSuffix(secret.Name, canonicalSecretSuffix) {
		klog.Infof("Found legacy-style secret %s, canonicalizing as %s", oldSecretName, secret.Name)
		return ctrl.handleCanonicalizedPullSecret(secret)
	}

	return secret, nil
}

// Attempt to create a canonicalized pull secret. If the secret already exsits, we should update it.
func (ctrl *Controller) handleCanonicalizedPullSecret(secret *corev1.Secret) (*corev1.Secret, error) {
	out, err := ctrl.kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(context.TODO(), secret.Name, metav1.GetOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return nil, fmt.Errorf("could not get canonical secret %q: %w", secret.Name, err)
	}

	// We don't have a canonical secret, so lets create one.
	if k8serrors.IsNotFound(err) {
		out, err = ctrl.kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Create(context.TODO(), secret, metav1.CreateOptions{})
		if err != nil {
			return nil, fmt.Errorf("could not create canonical secret %q: %w", secret.Name, err)
		}

		klog.Infof("Created canonical secret %s", secret.Name)
		return out, nil
	}

	// Check if the canonical secret from the API server matches the one we have.
	// If they match, then we don't need to do an update.
	if bytes.Equal(secret.Data[corev1.DockerConfigJsonKey], out.Data[corev1.DockerConfigJsonKey]) {
		klog.Infof("Canonical secret %q up-to-date", secret.Name)
		return out, nil
	}

	// If we got here, it means that our secret needs to be updated.
	out.Data = secret.Data
	out, err = ctrl.kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Update(context.TODO(), out, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not update canonical secret %q: %w", secret.Name, err)
	}

	klog.Infof("Updated canonical secret %s", secret.Name)

	return out, nil
}

// If one wants to opt out, this removes all of the statuses and object
// references from a given MachineConfigPool.
func (ctrl *Controller) finalizeOptOut(ps *poolState) error {
	if err := ctrl.postBuildCleanup(ps.MachineConfigPool(), true); err != nil {
		return err
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		mcp, err := ctrl.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(context.TODO(), ps.MachineConfigPool().Name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		ps := newPoolState(mcp)
		ps.DeleteBuildRefForCurrentMachineConfig()
		ps.ClearImagePullspec()
		ps.ClearAllBuildConditions()

		return ctrl.updatePoolAndSyncAvailableStatus(ps.MachineConfigPool())
	})
}

// Fires whenever a MachineConfigPool is updated.
func (ctrl *Controller) updateMachineConfigPool(old, cur interface{}) {
	oldPool := old.(*mcfgv1.MachineConfigPool).DeepCopy()
	curPool := cur.(*mcfgv1.MachineConfigPool).DeepCopy()

	klog.V(4).Infof("Updating MachineConfigPool %s", oldPool.Name)

	doABuild, err := shouldWeDoABuild(ctrl.imageBuilder, oldPool, curPool)
	if err != nil {
		klog.Errorln(err)
		ctrl.handleErr(err, curPool.Name)
		return
	}

	switch {
	// We've transitioned from a layered pool to a non-layered pool.
	case ctrlcommon.IsLayeredPool(oldPool) && !ctrlcommon.IsLayeredPool(curPool):
		klog.V(4).Infof("MachineConfigPool %s has opted out of layering", curPool.Name)
		if err := ctrl.finalizeOptOut(newPoolState(curPool)); err != nil {
			klog.Errorln(err)
			ctrl.handleErr(err, curPool.Name)
			return
		}
	// We need to do a build.
	case doABuild:
		klog.V(4).Infof("MachineConfigPool %s has changed, requiring a build", curPool.Name)
		if err := ctrl.startBuildForMachineConfigPool(newPoolState(curPool)); err != nil {
			klog.Errorln(err)
			ctrl.handleErr(err, curPool.Name)
			return
		}
	// Everything else.
	default:
		klog.V(4).Infof("MachineConfigPool %s up-to-date", curPool.Name)
	}

	ctrl.enqueueMachineConfigPool(curPool)
}

// Fires whenever a MachineConfigPool is deleted. TODO: Wire up checks for
// deleting any in-progress builds.
func (ctrl *Controller) deleteMachineConfigPool(obj interface{}) {
	pool, ok := obj.(*mcfgv1.MachineConfigPool)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Couldn't get object from tombstone %#v", obj))
			return
		}
		pool, ok = tombstone.Obj.(*mcfgv1.MachineConfigPool)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Tombstone contained object that is not a MachineConfigPool %#v", obj))
			return
		}
	}
	klog.V(4).Infof("Deleting MachineConfigPool %s", pool.Name)
}

func (ctrl *Controller) syncAvailableStatus(pool *mcfgv1.MachineConfigPool) error {
	// I'm not sure what the consequences are of not doing this.
	//nolint:gocritic // Leaving this here for review purposes.
	/*
		if apihelpers.IsMachineConfigPoolConditionFalse(pool.Status.Conditions, mcfgv1.MachineConfigPoolRenderDegraded) {
			return nil
		}
	*/
	sdegraded := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolRenderDegraded, corev1.ConditionFalse, "", "")
	apihelpers.SetMachineConfigPoolCondition(&pool.Status, *sdegraded)

	if _, err := ctrl.mcfgclient.MachineconfigurationV1().MachineConfigPools().UpdateStatus(context.TODO(), pool, metav1.UpdateOptions{}); err != nil {
		return err
	}

	return nil
}

func (ctrl *Controller) syncFailingStatus(pool *mcfgv1.MachineConfigPool, err error) error {
	sdegraded := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolRenderDegraded, corev1.ConditionTrue, "", fmt.Sprintf("Failed to build configuration for pool %s: %v", pool.Name, err))
	apihelpers.SetMachineConfigPoolCondition(&pool.Status, *sdegraded)
	if _, updateErr := ctrl.mcfgclient.MachineconfigurationV1().MachineConfigPools().UpdateStatus(context.TODO(), pool, metav1.UpdateOptions{}); updateErr != nil {
		klog.Errorf("Error updating MachineConfigPool %s: %v", pool.Name, updateErr)
	}
	return err
}

// Determine if we have a config change.
func isPoolConfigChange(oldPool, curPool *mcfgv1.MachineConfigPool) bool {
	return oldPool.Spec.Configuration.Name != curPool.Spec.Configuration.Name
}

// Checks our pool to see if we can do a build. We base this off of a few criteria:
// 1. Is the pool opted into layering?
// 2. Do we have an object reference to an in-progress build?
// 3. Is the pool degraded?
// 4. Is our build in a specific state?
//
// Returns true if we are able to build.
func canPoolBuild(ps *poolState) bool {
	// If we don't have a layered pool, we should not build.
	if !ps.IsLayered() {
		return false
	}

	// If we have a reference to an in-progress build, we should not build.
	if ps.HasBuildObjectForCurrentMachineConfig() {
		return false
	}

	// If the pool is degraded, we should not build.
	if ps.IsAnyDegraded() {
		return false
	}

	// If the pool is in any of these states, we should not build.
	if ps.IsBuilding() {
		return false
	}

	if ps.IsBuildPending() {
		return false
	}

	if ps.IsBuildFailure() {
		return false
	}

	return true
}

// Determines if we should do a build based upon the state of our
// MachineConfigPool, the presence of a build pod, etc.
func shouldWeDoABuild(builder interface {
	IsBuildRunning(*mcfgv1.MachineConfigPool) (bool, error)
}, oldPool, curPool *mcfgv1.MachineConfigPool) (bool, error) {
	ps := newPoolState(curPool)

	// If we don't have a layered pool, we should not build.
	poolStateSuggestsBuild := canPoolBuild(ps) &&
		// If we have a config change or we're missing an image pullspec label, we
		// should do a build.
		(isPoolConfigChange(oldPool, curPool) || !ps.HasOSImage()) &&
		// If we're missing a build pod reference, it likely means we don't need to
		// do a build.
		!ps.HasBuildObjectRefName(newImageBuildRequest(curPool).getBuildName())

	if !poolStateSuggestsBuild {
		return false, nil
	}

	// If a build is found running, we should not do a build.
	isRunning, err := builder.IsBuildRunning(curPool)

	return !isRunning, err
}

// Enumerates all of the build-related MachineConfigPool condition types.
func getMachineConfigPoolBuildConditions() []mcfgv1.MachineConfigPoolConditionType {
	return []mcfgv1.MachineConfigPoolConditionType{
		mcfgv1.MachineConfigPoolBuildFailed,
		mcfgv1.MachineConfigPoolBuildPending,
		mcfgv1.MachineConfigPoolBuildSuccess,
		mcfgv1.MachineConfigPoolBuilding,
	}
}

// Determines if a pod or build is managed by this controller by examining its labels.
func hasAllRequiredOSBuildLabels(labels map[string]string) bool {
	requiredLabels := []string{
		ctrlcommon.OSImageBuildPodLabel,
		targetMachineConfigPoolLabel,
		desiredConfigLabel,
	}

	for _, label := range requiredLabels {
		if _, ok := labels[label]; !ok {
			return false
		}
	}

	return true
}
