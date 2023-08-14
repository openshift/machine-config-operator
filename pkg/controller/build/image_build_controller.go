package build

import (
	"context"
	"fmt"
	"strings"
	"time"

	buildv1 "github.com/openshift/api/build/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	buildlistersv1 "github.com/openshift/client-go/build/listers/build/v1"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/scheme"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreclientsetv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

// Controller defines the build controller.
type ImageBuildController struct {
	*Clients
	*informers

	eventRecorder record.EventRecorder

	// The function to call whenever we've encountered a Build. This function is
	// responsible for examining the Build to determine what state its in and map
	// that state to the appropriate MachineConfigPool object.
	buildHandler func(*buildv1.Build) error

	syncHandler  func(pod string) error
	enqueueBuild func(*buildv1.Build)

	buildLister buildlistersv1.BuildLister

	buildListerSynced cache.InformerSynced

	queue workqueue.RateLimitingInterface

	config BuildControllerConfig
}

var _ ImageBuilder = (*ImageBuildController)(nil)

// Returns a new image build controller.
func newImageBuildController(
	ctrlConfig BuildControllerConfig,
	clients *Clients,
	buildHandler func(*buildv1.Build) error,
) *ImageBuildController {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&coreclientsetv1.EventSinkImpl{Interface: clients.kubeclient.CoreV1().Events("")})

	ctrl := &ImageBuildController{
		Clients:       clients,
		informers:     newInformers(clients),
		eventRecorder: eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineosbuilder-imagebuildcontroller"}),
		queue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "machineosbuilder-imagebuildcontroller"),
		config:        ctrlConfig,
		buildHandler:  buildHandler,
	}

	// As an aside, why doesn't the constructor here set up all the informers?
	ctrl.buildInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addBuild,
		UpdateFunc: ctrl.updateBuild,
		DeleteFunc: ctrl.deleteBuild,
	})

	ctrl.buildLister = ctrl.buildInformer.Lister()
	ctrl.buildListerSynced = ctrl.buildInformer.Informer().HasSynced

	ctrl.syncHandler = ctrl.syncBuild
	ctrl.enqueueBuild = ctrl.enqueueDefault

	return ctrl
}

func (ctrl *ImageBuildController) enqueue(build *buildv1.Build) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(build)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", build, err))
		return
	}

	ctrl.queue.Add(key)
}

func (ctrl *ImageBuildController) enqueueRateLimited(build *buildv1.Build) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(build)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", build, err))
		return
	}

	ctrl.queue.AddRateLimited(key)
}

// enqueueAfter will enqueue a build after the provided amount of time.
func (ctrl *ImageBuildController) enqueueAfter(build *buildv1.Build, after time.Duration) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(build)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", build, err))
		return
	}

	ctrl.queue.AddAfter(key, after)
}

// enqueueDefault calls a default enqueue function
func (ctrl *ImageBuildController) enqueueDefault(build *buildv1.Build) {
	ctrl.enqueueAfter(build, ctrl.config.UpdateDelay)
}

// Syncs Builds.
func (ctrl *ImageBuildController) syncBuild(key string) error { //nolint:dupl // This does have commonality with the PodBuildController.
	start := time.Now()
	defer func() {
		klog.Infof("Finished syncing pod %s: %s", key, time.Since(start))
	}()
	klog.Infof("Started syncing pod %s", key)

	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	// TODO: Why do I need to set the namespace here?
	build, err := ctrl.buildLister.Builds(ctrlcommon.MCONamespace).Get(name)
	if k8serrors.IsNotFound(err) {
		klog.V(2).Infof("Build %v has been deleted", key)
		return nil
	}
	if err != nil {
		return err
	}

	build, err = ctrl.buildclient.BuildV1().Builds(ctrlcommon.MCONamespace).Get(context.TODO(), build.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	if !hasAllRequiredOSBuildLabels(build.Labels) {
		klog.Infof("Ignoring non-OS image build %s", build.Name)
		return nil
	}

	if err := ctrl.buildHandler(build); err != nil {
		return fmt.Errorf("unable to update with build status: %w", err)
	}

	klog.Infof("Updated MachineConfigPool with build status. Build %s in %s", build.Name, build.Status.Phase)

	return nil
}

// Starts the Image Build Controller.
func (ctrl *ImageBuildController) Run(ctx context.Context, workers int) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()

	ctrl.informers.start(ctx)

	if !cache.WaitForCacheSync(ctx.Done(), ctrl.buildListerSynced) {
		return
	}

	klog.Info("Starting MachineOSBuilder-ImageBuildController")
	defer klog.Info("Shutting down MachineOSBuilder-ImageBuildController")

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.worker, time.Second, ctx.Done())
	}

	<-ctx.Done()
}

// Gets the final image pullspec. In this case, we can interrogate the Build
// object for this information.
func (ctrl *ImageBuildController) FinalPullspec(pool *mcfgv1.MachineConfigPool) (string, error) {
	buildName := newImageBuildRequest(pool).getBuildName()

	build, err := ctrl.buildclient.BuildV1().Builds(ctrlcommon.MCONamespace).Get(context.TODO(), buildName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("could not get build %s for pool %s: %w", buildName, pool.Name, err)
	}

	// Get the image digest from the completed build and replace the tag with
	// the digest.
	if build.Status.OutputDockerImageReference == "" {
		return "", fmt.Errorf("no image reference outputted")
	}

	if build.Status.Output.To.ImageDigest == "" {
		return "", fmt.Errorf("no image digest found")
	}

	return parseImagePullspec(build.Status.OutputDockerImageReference, build.Status.Output.To.ImageDigest)
}

// Deletes the underlying Build object.
func (ctrl *ImageBuildController) DeleteBuildObject(pool *mcfgv1.MachineConfigPool) error {
	buildName := newImageBuildRequest(pool).getBuildName()
	return ctrl.buildclient.BuildV1().Builds(ctrlcommon.MCONamespace).Delete(context.TODO(), buildName, metav1.DeleteOptions{})
}

// Determines if a build is currently running by looking for a corresponding Build.
func (ctrl *ImageBuildController) IsBuildRunning(pool *mcfgv1.MachineConfigPool) (bool, error) {
	buildName := newImageBuildRequest(pool).getBuildName()

	// First check if we have a build in progress for this MachineConfigPool and rendered config.
	_, err := ctrl.buildclient.BuildV1().Builds(ctrlcommon.MCONamespace).Get(context.TODO(), buildName, metav1.GetOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return false, err
	}

	return err == nil, nil
}

// Starts a new build, assuming one is not found first. In that case, it
// returns an object reference to the preexisting Build object.
func (ctrl *ImageBuildController) StartBuild(ibr ImageBuildRequest) (*corev1.ObjectReference, error) {
	targetMC := ibr.Pool.Spec.Configuration.Name

	buildName := ibr.getBuildName()

	// TODO: Find a constant for this:
	if !strings.HasPrefix(targetMC, "rendered-") {
		return nil, fmt.Errorf("%s is not a rendered MachineConfig", targetMC)
	}

	// First check if we have a build in progress for this MachineConfigPool and rendered config.
	build, err := ctrl.buildclient.BuildV1().Builds(ctrlcommon.MCONamespace).Get(context.TODO(), buildName, metav1.GetOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return nil, err
	}

	// This means we found a preexisting build build.
	if build != nil && err == nil && hasAllRequiredOSBuildLabels(build.Labels) {
		klog.Infof("Found preexisting OS image build (%s) for pool %s", build.Name, ibr.Pool.Name)
		return toObjectRef(build), nil
	}

	klog.Infof("Starting build for pool %s", ibr.Pool.Name)
	klog.Infof("Build name: %s", buildName)
	klog.Infof("Final image will be pushed to %q, using secret %q", ibr.FinalImage.Pullspec, ibr.FinalImage.PullSecret.Name)

	build, err = ctrl.buildclient.BuildV1().Builds(ctrlcommon.MCONamespace).Create(context.TODO(), ibr.toBuild(), metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not create OS image build: %w", err)
	}

	klog.Infof("Build started for pool %s in %s!", ibr.Pool.Name, build.Name)

	return toObjectRef(build), nil
}

// Fires whenever a Build is added.
func (ctrl *ImageBuildController) addBuild(obj interface{}) {
	build := obj.(*buildv1.Build).DeepCopy()
	klog.V(4).Infof("Adding Build %s. Is OS image build? %v", build.Name, hasAllRequiredOSBuildLabels(build.Labels))
	if hasAllRequiredOSBuildLabels(build.Labels) {
		ctrl.enqueueBuild(build)
	}
}

// Fires whenever a Build is updated.
func (ctrl *ImageBuildController) updateBuild(_, curObj interface{}) {
	curBuild := curObj.(*buildv1.Build).DeepCopy()

	isOSImageBuild := hasAllRequiredOSBuildLabels(curBuild.Labels)

	klog.Infof("Updating build %s. Is OS image build? %v", curBuild.Name, isOSImageBuild)

	// Ignore non-OS image builds.
	// TODO: Figure out if we can add the filter criteria onto the lister.
	if !isOSImageBuild {
		return
	}

	klog.Infof("Build %s updated", curBuild.Name)

	ctrl.enqueueBuild(curBuild)
}

func (ctrl *ImageBuildController) handleErr(err error, key interface{}) {
	if err == nil {
		ctrl.queue.Forget(key)
		return
	}

	if ctrl.queue.NumRequeues(key) < ctrl.config.MaxRetries {
		klog.V(2).Infof("Error syncing build %v: %v", key, err)
		ctrl.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	klog.V(2).Infof("Dropping build %q out of the queue: %v", key, err)
	ctrl.queue.Forget(key)
	ctrl.queue.AddAfter(key, 1*time.Minute)
}

func (ctrl *ImageBuildController) deleteBuild(obj interface{}) {
	build := obj.(*buildv1.Build).DeepCopy()
	klog.V(4).Infof("Deleting Build %s. Is OS image build? %v", build.Name, hasAllRequiredOSBuildLabels(build.Labels))
	ctrl.enqueueBuild(build)
}

// worker runs a worker thread that just dequeues items, processes them, and marks them done.
// It enforces that the syncHandler is never invoked concurrently with the same key.
func (ctrl *ImageBuildController) worker() {
	for ctrl.processNextWorkItem() {
	}
}

func (ctrl *ImageBuildController) processNextWorkItem() bool {
	key, quit := ctrl.queue.Get()
	if quit {
		return false
	}
	defer ctrl.queue.Done(key)

	err := ctrl.syncHandler(key.(string))
	ctrl.handleErr(err, key)

	return true
}
