package build

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/golang/glog"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/scheme"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	coreclientsetv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	imageinformersv1 "github.com/openshift/client-go/image/informers/externalversions/image/v1"
	imagelistersv1 "github.com/openshift/client-go/image/listers/image/v1"

	buildinformersv1 "github.com/openshift/client-go/build/informers/externalversions/build/v1"
	buildlistersv1 "github.com/openshift/client-go/build/listers/build/v1"

	buildclientset "github.com/openshift/client-go/build/clientset/versioned"
	imageclientset "github.com/openshift/client-go/image/clientset/versioned"

	mcfgclientset "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
	mcfginformersv1 "github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions/machineconfiguration.openshift.io/v1"
	mcfglistersv1 "github.com/openshift/machine-config-operator/pkg/generated/listers/machineconfiguration.openshift.io/v1"

	buildv1 "github.com/openshift/api/build/v1"
	imagev1 "github.com/openshift/api/image/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	"github.com/vincent-petithory/dataurl"
)

const (

	// maxRetries is the number of times a machineconfig pool will be retried before it is dropped out of the queue.
	// With the current rate-limiter in use (5ms*2^(maxRetries-1)) the following numbers represent the times
	// a machineconfig pool is going to be requeued:
	//
	// 5ms, 10ms, 20ms, 40ms, 80ms, 160ms, 320ms, 640ms, 1.3s, 2.6s, 5.1s, 10.2s, 20.4s, 41s, 82s
	maxRetries = 15

	// updateDelay is a pause to deal with churn in MachineConfigs; see
	// https://github.com/openshift/machine-config-operator/issues/301
	updateDelay = 5 * time.Second

	machineConfigContentDockerfile = `
	# Multistage build, we need to grab the files from our config imagestream 
	FROM image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/{{.RenderedConfig }} AS machineconfig
	
	# We're actually basing on the "new format" image from the coreos base image stream 
	FROM image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/coreos

	# Pull in the files from our machineconfig stage 
	COPY --from=machineconfig /machine-config-ignition.json /etc/machine-config-ignition.json

	# Make the config drift checker happy
	COPY --from=machineconfig /machine-config.json /etc/machine-config-daemon/currentconfig 

	# Apply the config to the image 
	ENV container=1
	RUN exec -a ignition-apply  /usr/lib/dracut/modules.d/30ignition/ignition --ignore-unsupported /etc/machine-config-ignition.json

	# Rebuild origin.d (I included an /etc/yum.repos.d/ file in my machineconfig so it could find the RPMS, that's why this works)
	RUN rpm-ostree ex rebuild && rm -rf /var/cache /etc/rpm-ostree/origin.d

	# clean up. We want to be particularly strict so that live apply works
	RUN rm /etc/machine-config-ignition.json
	# TODO remove these hacks once we have
	# https://github.com/coreos/rpm-ostree/pull/3544
	# and
	# https://github.com/coreos/ignition/issues/1339 is fixed
	# don't fail if wildcard has no matches
	RUN bash -c "rm /usr/share/rpm/__db.*"; true
	# to keep live apply working
	RUN bash -c "if [[ -e /etc/systemd/system-preset/20-ignition.preset ]]; then sort /etc/systemd/system-preset/20-ignition.preset -o /etc/systemd/system-preset/20-ignition.preset; fi"

	# This is so we can get the machineconfig injected
	ARG machineconfig=unknown
	# Apply the injected machineconfig name as a label so node_controller can check it
	LABEL machineconfig=$machineconfig
	`
	dummyDockerfile = `FROM dummy`
)

var (
	// controllerKind contains the schema.GroupVersionKind for this controller type.
	controllerKind = mcfgv1.SchemeGroupVersion.WithKind("MachineConfigPool")
)

// Controller defines the build controller.
type Controller struct {
	client      mcfgclientset.Interface
	imageclient imageclientset.Interface
	buildclient buildclientset.Interface
	kubeclient  clientset.Interface

	eventRecorder record.EventRecorder

	syncHandler              func(mcp string) error
	enqueueMachineConfigPool func(*mcfgv1.MachineConfigPool)

	ccLister  mcfglistersv1.ControllerConfigLister
	mcpLister mcfglistersv1.MachineConfigPoolLister
	bLister   buildlistersv1.BuildLister
	bcLister  buildlistersv1.BuildConfigLister
	isLister  imagelistersv1.ImageStreamLister

	ccListerSynced  cache.InformerSynced
	mcpListerSynced cache.InformerSynced
	bListerSynced   cache.InformerSynced
	bcListerSynced  cache.InformerSynced
	isListerSynced  cache.InformerSynced

	queue workqueue.RateLimitingInterface
}

// New returns a new node controller.
func New(
	ccInformer mcfginformersv1.ControllerConfigInformer,
	mcpInformer mcfginformersv1.MachineConfigPoolInformer,
	isInformer imageinformersv1.ImageStreamInformer,
	bcInformer buildinformersv1.BuildConfigInformer,
	bInformer buildinformersv1.BuildInformer,
	mcfgClient mcfgclientset.Interface,
	kubeClient clientset.Interface,
	imageClient imageclientset.Interface,
	buildClient buildclientset.Interface,
) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.Infof)
	eventBroadcaster.StartRecordingToSink(&coreclientsetv1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	ctrl := &Controller{
		client:        mcfgClient,
		imageclient:   imageClient,
		kubeclient:    kubeClient,
		buildclient:   buildClient,
		eventRecorder: eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineconfigcontroller-buildcontroller"}),
		queue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "machineconfigcontroller-buildcontroller"),
	}

	mcpInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addMachineConfigPool,
		UpdateFunc: ctrl.updateMachineConfigPool,
		DeleteFunc: ctrl.deleteMachineConfigPool,
	})
	bInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addBuild,
		UpdateFunc: ctrl.updateBuild,
		DeleteFunc: ctrl.deleteBuild,
	})
	bcInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addBuildConfig,
		UpdateFunc: ctrl.updateBuildConfig,
		DeleteFunc: ctrl.deleteBuildConfig,
	})
	isInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addImageStream,
		UpdateFunc: ctrl.updateImageStream,
		DeleteFunc: ctrl.deleteImageStream,
	})

	ctrl.syncHandler = ctrl.syncMachineConfigPool
	ctrl.enqueueMachineConfigPool = ctrl.enqueueDefault

	ctrl.ccLister = ccInformer.Lister()
	ctrl.mcpLister = mcpInformer.Lister()
	ctrl.isLister = isInformer.Lister()
	ctrl.bcLister = bcInformer.Lister()
	ctrl.bLister = bInformer.Lister()

	ctrl.ccListerSynced = ccInformer.Informer().HasSynced
	ctrl.mcpListerSynced = mcpInformer.Informer().HasSynced
	ctrl.isListerSynced = isInformer.Informer().HasSynced
	ctrl.bcListerSynced = bcInformer.Informer().HasSynced
	ctrl.bListerSynced = bInformer.Informer().HasSynced

	return ctrl
}

// Run executes the render controller.
func (ctrl *Controller) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()

	if !cache.WaitForCacheSync(stopCh, ctrl.mcpListerSynced, ctrl.ccListerSynced, ctrl.bListerSynced, ctrl.bcListerSynced, ctrl.isListerSynced) {
		return
	}

	glog.Info("Starting MachineConfigController-BuildController")
	defer glog.Info("Shutting down MachineConfigController-BuildController")

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.worker, time.Second, stopCh)
	}

	<-stopCh
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
	ctrl.enqueueAfter(pool, updateDelay)
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

func (ctrl *Controller) handleErr(err error, key interface{}) {
	if err == nil {
		ctrl.queue.Forget(key)
		return
	}

	if ctrl.queue.NumRequeues(key) < maxRetries {
		glog.V(2).Infof("Error syncing machineconfigpool %v: %v", key, err)
		ctrl.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	glog.V(2).Infof("Dropping machineconfigpool %q out of the queue: %v", key, err)
	ctrl.queue.Forget(key)
	ctrl.queue.AddAfter(key, 1*time.Minute)
}

// TODO(jkyros): the question we're trying to answer is "is there any content that has changed that is not reflected in the current image for the pool"

// syncMachineConfigPool will sync the machineconfig pool with the given key.
// This function is not meant to be invoked concurrently with the same key.
func (ctrl *Controller) syncMachineConfigPool(key string) error {
	startTime := time.Now()
	glog.V(4).Infof("Started syncing machineconfigpool %q (%v)", key, startTime)
	defer func() {
		glog.V(4).Infof("Finished syncing machineconfigpool %q (%v)", key, time.Since(startTime))
	}()

	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	machineconfigpool, err := ctrl.mcpLister.Get(name)
	if errors.IsNotFound(err) {
		glog.V(2).Infof("MachineConfigPool %v has been deleted", key)
		return nil
	}
	if err != nil {
		return err
	}

	// Make sure the shared base CoreOS imagestream exists
	// TODO(jkyros): There seems to be a delay (probably the time it takes to pull the image) before the image tag shows up. As a result,
	// when we create our base imagestream later, it's empty until this gets populated and triggers it.
	_, err = ctrl.ensureCoreOSImageStream()
	if err != nil {
		return err
	}

	pool := machineconfigpool.DeepCopy()

	// TODO(jkyros): take this out when we decide actual UX, this just forces the layered label on to
	// the pool if its name is the string "layered"
	if pool.Name == "layered" {
		if pool.Labels == nil {
			pool.Labels = map[string]string{}
		}
		// TODO(jkyros): we'll see if we like this, but we need a way to specify which imagestream it should use
		pool.Labels[ctrlcommon.ExperimentalLayeringPoolLabel] = ""

		// TODO(jkyros): Don't update this here. We're just doing this now to "steal" the pool from render_controller
		_, err = ctrl.client.MachineconfigurationV1().MachineConfigPools().Update(context.TODO(), pool, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
	}

	// If this pool isn't managed by us, the render controller will handle it
	if !ctrlcommon.IsLayeredPool(pool) {
		return nil
	}

	// TODO(jkyros): I *could* have the build controller do the config rendering here for the pools
	// that the build controller manages, but there is no escaping at least some modification to the render
	// controller telling it to ignore the pools the build controller is managing.

	// Stuff an entitlements machineconfig into the pool

	ctrl.experimentalAddEntitlements(pool)

	glog.V(2).Infof("Ensuring image streams exist for pool %s", pool.Name)

	// Get the mapping/list of resources this pool should ensure and own
	pbr := PoolBuildResources(pool)

	// Our list of imagestreams we need to ensure exists
	var ensureImageStreams = []string{
		pbr.ImageStream.Base,
		pbr.ImageStream.ExternalBase,
		pbr.ImageStream.RenderedConfig,
		pbr.ImageStream.Content,
		pbr.ImageStream.CustomContent,
		pbr.ImageStream.External,
	}

	// Make sure the imagestreams exist so we can populate them with our image builds
	for _, imageStreamName := range ensureImageStreams {
		_, err := ctrl.ensureImageStreamForPool(pool, imageStreamName, pbr)
		if err != nil {
			// I don't know if it existed or not, I couldn't get it
			return fmt.Errorf("Failed to ensure ImageStream %s: %w", imageStreamName, err)
		}

	}

	// Magically switch imagestreams if custom/external end up with images in them
	err = ctrl.ensureImageStreamPrecedenceIfPopulated(pool)
	if err != nil {
		return fmt.Errorf("Could not ensure proper imagestream was selected for pool %s: %w", pool.Name, err)
	}

	// TODO(jkyros): we could have just now set our imagestream based on changes, but we might not have a build yet

	// Figure out which imagestream the pool is deploying from
	poolImageStreamName, err := ctrlcommon.GetPoolImageStream(pool)
	if err != nil {
		return err
	}

	// Get the actual image stream object for that imagestream
	poolImageStream, err := ctrl.isLister.ImageStreams(ctrlcommon.MCONamespace).Get(poolImageStreamName)
	if err != nil {
		return err
	}

	// Get the most recent image from that stream if it exists
	// TODO(jkyros): this can be nil
	mostRecentPoolImage := ctrl.getMostRecentImageTagForImageStream(poolImageStream, "latest")

	// Our list of imagestreams we need to ensure exists
	var ensureBuildConfigs = []PoolBuildConfig{
		pbr.BuildConfig.Content,
		pbr.BuildConfig.CustomContent,
	}

	for num, pbc := range ensureBuildConfigs {

		checkBuildConfig, err := ctrl.ensureBuildConfigForPool(pool, &ensureBuildConfigs[num])
		if err != nil {
			// I don't know if it existed or not, I couldn't get it
			return fmt.Errorf("Failed to ensure BuildConfig %s: %w", pbc.Name, err)
		}

		// We're looking for builds that belong to this buildconfig, so craft a filter
		ourBuildReq, err := labels.NewRequirement("buildconfig", selection.In, []string{checkBuildConfig.Name})
		if err != nil {
			return err
		}
		// Make a selector based on our requirement
		ourBuildSelector := labels.NewSelector().Add(*ourBuildReq)

		// Retrieve those builds that belong to this buildconfig
		builds, err := ctrl.bLister.Builds(ctrlcommon.MCONamespace).List(ourBuildSelector)
		if err != nil {
			return err
		}

		// If builds exist for this buildconfig
		if len(builds) > 0 {
			// Sort the builds in descending order, we want the newest first
			sort.Slice(builds, func(i, j int) bool {
				return builds[i].CreationTimestamp.After(builds[j].CreationTimestamp.Time)
			})

			// This is the newest, and we know it can't be outof bounds because of how we got here
			// TODO(jkyros): If a newer build has been queued, should we terminate the old one?
			mostRecentBuild := builds[0]

			// TODO(jkyros): We need to find a "level triggered" way to figure out if the image we have is representative
			// of the state of our "build ladder" so we know if a failed build is a problem or not. Ultimately a metadata problem.
			if mostRecentPoolImage == nil || mostRecentPoolImage.Created.Before(&mostRecentBuild.CreationTimestamp) {
				// If they failed/are in bad phase, we're probably in trouble
				switch mostRecentBuild.Status.Phase {
				case buildv1.BuildPhaseError:
					glog.Errorf("Need to degrade, build %s is %s", mostRecentBuild.Name, mostRecentBuild.Status.Phase)
				case buildv1.BuildPhaseFailed:
					glog.Errorf("Need to degrade, build %s is %s", mostRecentBuild.Name, mostRecentBuild.Status.Phase)
				case buildv1.BuildPhaseCancelled:
					glog.Errorf("Need to degrade, build %s is %s", mostRecentBuild.Name, mostRecentBuild.Status.Phase)
				case buildv1.BuildPhaseComplete:
					glog.Errorf("A build %s has completed for pool %s", mostRecentBuild.Name, mostRecentBuild.Status.Phase)
				default:
					// If they worked okay, we're building, we can update our status?
					glog.Infof("A build %s is in progress (%s) for pool %s", mostRecentBuild.Name, mostRecentBuild.Status.Phase, pool.Name)
				}

			}
		}

	}

	// Do we have an image stream for this pool? We should if we got here.
	is, err := ctrl.isLister.ImageStreams(ctrlcommon.MCONamespace).Get(poolImageStream.Name)
	if apierrors.IsNotFound(err) {
		// TODO(jkyros): As cgwalters points out, this should probably degrade because it should exist
		glog.Warningf("ImageStream for %s does not exist (yet?): %s", pool.Name, err)
	} else {
		// If there is an image ready, annotate the pool with it so node controller can use it if it's the right one
		err := ctrl.annotatePoolWithNewestImage(is, pool)
		if err != nil {
			return err
		}
	}

	// TODO(jkyros): Only update if we changed, don't always update. Also, if we update here and then update status again, that seems
	// wasteful.
	_, err = ctrl.client.MachineconfigurationV1().MachineConfigPools().Update(context.TODO(), pool, metav1.UpdateOptions{})
	if err != nil {
		return err
	}

	return ctrl.syncAvailableStatus(pool)

}

// Machine Config Pools

func (ctrl *Controller) addMachineConfigPool(obj interface{}) {
	pool := obj.(*mcfgv1.MachineConfigPool)
	glog.V(4).Infof("Adding MachineConfigPool %s", pool.Name)
	ctrl.enqueueMachineConfigPool(pool)

}

func (ctrl *Controller) updateMachineConfigPool(old, cur interface{}) {
	oldPool := old.(*mcfgv1.MachineConfigPool)
	curPool := cur.(*mcfgv1.MachineConfigPool)

	glog.V(4).Infof("Updating MachineConfigPool %s", oldPool.Name)
	ctrl.enqueueMachineConfigPool(curPool)
}

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
	glog.V(4).Infof("Deleting MachineConfigPool %s", pool.Name)
}

// ImagStreams

func (ctrl *Controller) addImageStream(obj interface{}) {

}

func (ctrl *Controller) updateImageStream(old, cur interface{}) {
	imagestream := cur.(*imagev1.ImageStream)
	controllerRef := metav1.GetControllerOf(imagestream)

	if controllerRef != nil {

		if pool := ctrl.resolveControllerRef(controllerRef); pool != nil {

			glog.Infof("ImageStream %s changed for pool %s", imagestream.Name, pool.Name)

			// TODO(jkyros): This is a race I usually win, but I won't always, and we need a better
			// way to get this metadata in
			if imagestream.Name == pool.Name+ctrlcommon.ImageStreamSuffixRenderedConfig {
				ctrl.cheatMachineConfigLabelIntoBuildConfig(imagestream, pool)
			}

			ctrl.enqueueMachineConfigPool(pool)

		}

	}
}

func (ctrl *Controller) deleteImageStream(obj interface{}) {
	// TODO(jkyros):  probably worth enqueueing the pool again here just so
	// our sync can figure out that this newly-deleted stream is now empty and update the mappings ?
}

// Builds

func (ctrl *Controller) addBuild(obj interface{}) {
	build := obj.(*buildv1.Build)

	glog.Infof("Added a build: %s", build.Name)

	// TODO(jkyros):  Is this one of our builds that belongs to our imagestream?
	// If it is, we should mark that somewhere so we know the pool is "building"

}

func (ctrl *Controller) updateBuild(old, cur interface{}) {
	build := old.(*buildv1.Build)

	glog.Infof("Updated a build: %s", build.Name)
	// Builds will move through phases which cause them to change
	// Most of those phases are standard/good, but some of them are bad
	// We want to know if we end up in a bad phase and need to retry
	ctrl.enqueuePoolIfBuildProblems(build)

}

func (ctrl *Controller) deleteBuild(obj interface{}) {
	build := obj.(*buildv1.Build)

	glog.Infof("Deleted a build: %s", build.Name)

}

// Buildconfigs

func (ctrl *Controller) addBuildConfig(obj interface{}) {
	buildconfig := obj.(*buildv1.BuildConfig)

	glog.Infof("Added a buildconfig: %s", buildconfig.Name)

}

func (ctrl *Controller) updateBuildConfig(old, cur interface{}) {
	buildconfig := old.(*buildv1.BuildConfig)
	newbuildconfig := cur.(*buildv1.BuildConfig)

	glog.Infof("Updated a buildconfig: %s", buildconfig.Name)

	// Every time a buildconfig is instantiated it bumps the generation, so it always looks like it's changing
	// For now we really only care if the user edited the dockerfile, and that string is a pointer
	if buildconfig.Spec.Source.Dockerfile != nil && newbuildconfig.Spec.Source.Dockerfile != nil {
		if *buildconfig.Spec.Source.Dockerfile != *newbuildconfig.Spec.Source.Dockerfile {

			glog.Infof("The dockerfile for buildconfig %s changed, triggering a build", buildconfig.Name)
			// TODO(jkyros); If this is the mco content, we need the machineconfig name
			// so go get the image from that imagestream and put the name in. Otherwise just start it.

			br := &buildv1.BuildRequest{
				//TypeMeta:    metav1.TypeMeta{},
				ObjectMeta: metav1.ObjectMeta{Name: buildconfig.Name},
				//Env: []corev1.EnvVar{whichConfig},
				TriggeredBy: []buildv1.BuildTriggerCause{
					{Message: "The machine config controller"},
				},
				DockerStrategyOptions: &buildv1.DockerStrategyOptions{
					//BuildArgs: []corev1.EnvVar{whichConfig},
					//NoCache:   new(bool),
				},
			}

			_, err := ctrl.buildclient.BuildV1().BuildConfigs(ctrlcommon.MCONamespace).Instantiate(context.TODO(), br.Name, br, metav1.CreateOptions{})
			if err != nil {
				glog.Errorf("Failed to trigger image build: %s", err)
			}
		}
	}

}

func (ctrl *Controller) deleteBuildConfig(obj interface{}) {
	buildconfig := obj.(*buildv1.BuildConfig)

	glog.Infof("Deleted a buildconfig: %s", buildconfig.Name)

}

// experimentalAddEntitlements grabs the cluster entitlement certificates out of the openshift-config-managed namespace and
// stuffs them into a machineconfig for our layered pool, so we can have entitled builds. This is a terrible practice, and
// we should probably just sync the secrets into our namespace so our builds can use them directly rather than expose them via machineconfig.
func (ctrl *Controller) experimentalAddEntitlements(pool *mcfgv1.MachineConfigPool) {

	var entitledConfigName = fmt.Sprintf("99-%s-entitled-build", pool.Name)

	// If it's not there, put it there, otherwise do nothing
	_, err := ctrl.client.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), entitledConfigName, metav1.GetOptions{})
	if errors.IsNotFound(err) {

		// Repo configuration for redhat package entitlements ( I just added base and appstream)
		// TODO(jkyros): do this right once subscription-manager is included in RHCOS
		redhatRepo := `[rhel-8-for-x86_64-baseos-rpms]
name = Red Hat Enterprise Linux 8 for x86_64 - BaseOS (RPMs)
baseurl = https://cdn.redhat.com/content/dist/rhel8/8/x86_64/baseos/os
enabled = 1
gpgcheck = 0
sslverify = 0
sslclientkey = /etc/pki/entitlement/entitlement-key.pem
sslclientcert = /etc/pki/entitlement/entitlement.pem
metadata_expire = 86400
enabled_metadata = 1

[rhel-8-for-x86_64-appstream-rpms]
name = Red Hat Enterprise Linux 8 for x86_64 - AppStream (RPMs)
baseurl = https://cdn.redhat.com/content/dist/rhel8/8/x86_64/appstream/os
enabled = 1
gpgcheck = 0
sslverify = 0
sslclientkey = /etc/pki/entitlement/entitlement-key.pem
sslclientcert = /etc/pki/entitlement/entitlement.pem
metadata_expire = 86400
enabled_metadata = 1
`

		// Make an ignition to stuff into our machineconfig
		ignConfig := ctrlcommon.NewIgnConfig()
		ignConfig.Storage.Files = append(ignConfig.Storage.Files, NewIgnFile("/etc/yum.repos.d/redhat.repo", redhatRepo))

		// Get our entitlement secrets out of the managed namespace
		entitlements, err := ctrl.kubeclient.CoreV1().Secrets("openshift-config-managed").Get(context.TODO(), "etc-pki-entitlement", metav1.GetOptions{})
		if err != nil {
			glog.Warningf("Could not retrieve entitlement secret: %s", err)
			return
		}

		// Add the key to the file list
		if key, ok := entitlements.Data["entitlement-key.pem"]; ok {
			ignConfig.Storage.Files = append(ignConfig.Storage.Files, NewIgnFile("/etc/pki/entitlement/entitlement-key.pem", string(key)))
		}

		// Add the public key to the file list
		if pub, ok := entitlements.Data["entitlement.pem"]; ok {
			ignConfig.Storage.Files = append(ignConfig.Storage.Files, NewIgnFile("/etc/pki/entitlement/entitlement.pem", string(pub)))
		}

		// Now it's a machineconfig
		mc, err := ctrlcommon.MachineConfigFromIgnConfig(pool.Name, entitledConfigName, ignConfig)
		if err != nil {
			glog.Warningf("Could not create machineconfig for entitlements: %s", err)
		}

		// Add it to the list for this pool
		_, err = ctrl.client.MachineconfigurationV1().MachineConfigs().Create(context.TODO(), mc, metav1.CreateOptions{})
		if err != nil {
			glog.Warningf("Failed to add entitlements to layered pool: %s", err)
		}
	}

}

// annotatePoolWithNewestImage looks in the corresponding image stream for a pool and annotates the name of the image, and it's
// corresponding rendered-config, which it retrieves from the image's docker metadata labels that we added during our build
func (ctrl *Controller) annotatePoolWithNewestImage(imageStream *imagev1.ImageStream, pool *mcfgv1.MachineConfigPool) error {

	// We don't want to crash if these are empty
	if pool.Annotations == nil {
		pool.Annotations = map[string]string{}
	}

	// Grab the latest tag from the imagestream. If we don't have one, nothing happens
	for _, tag := range imageStream.Status.Tags {
		if len(tag.Items) == 0 {
			continue
		}

		// I might have an older image that has right machine config content, but some
		// other content might have changed (like, I dunno, base image) so we shouldn't go back
		// to older images
		image := tag.Items[0]

		// If this is different than our current tag, grab it and annotate the pool
		glog.Infof("imagestream %s newest is: %s (%s)", imageStream.Name, image.DockerImageReference, image.Image)
		if pool.Spec.Configuration.Name == image.Image {
			// We're already theer, don't touch it
			return nil
		}

		// get the actual image so we can read its labels
		fullImage, err := ctrl.imageclient.ImageV1().Images().Get(context.TODO(), image.Image, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("Could not retrieve image %s: %w", image.Image, err)
		}

		// We need the labels out of the docker image but it's a raw extension
		dockerLabels := struct {
			Config struct {
				Labels map[string]string `json:"Labels"`
			} `json:"Config"`
		}{}

		// Get the labels out and see what config this is
		err = json.Unmarshal(fullImage.DockerImageMetadata.Raw, &dockerLabels)
		if err != nil {
			return fmt.Errorf("Could not get labels from docker image metadata: %w", err)
		}

		// Tag what config this came from so we know it's the right image
		if machineconfig, ok := dockerLabels.Config.Labels["machineconfig"]; ok {
			pool.Annotations[ctrlcommon.ExperimentalNewestLayeredImageEquivalentConfigAnnotationKey] = machineconfig
			pool.Spec.Configuration.ObjectReference = corev1.ObjectReference{
				Kind: "Image",
				Name: image.Image,
			}
			// TODO(jkyros): Kind of cheating using this as metadata showback for the user until we figure out our "level triggering" strategy
			pool.Spec.Configuration.Source = []corev1.ObjectReference{
				// What machine config was the assigned image build using
				{Kind: "MachineConfig", Name: machineconfig, Namespace: ctrlcommon.MCONamespace},
				// What imagestream did it come out of
				{Kind: "ImageStream", Name: imageStream.Name, Namespace: ctrlcommon.MCONamespace},
				// The non-sha image reference just for convenience
				{Kind: "DockerImageReference", Name: image.DockerImageReference, Namespace: ctrlcommon.MCONamespace},
			}

		}

		// TODO(jkyros): Probably need to go through our eventing "state machine" to make sure our steps make sense
		ctrl.eventRecorder.Event(pool, corev1.EventTypeNormal, "Updated", "Moved pool "+pool.Name+" to layered image "+image.DockerImageReference)

	}

	return nil
}

func (ctrl *Controller) CreateBuildConfigForImageStream(pool *mcfgv1.MachineConfigPool, buildConfigName, sourceImageStreamName string, targetImageStream *imagev1.ImageStream, dockerFile string, triggerOnImageTags ...string) (*buildv1.BuildConfig, error) {
	// Construct a buildconfig for this pool if it doesn't exist

	skipLayers := buildv1.ImageOptimizationSkipLayers
	buildConfig := &buildv1.BuildConfig{

		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Name:      buildConfigName,
			Namespace: ctrlcommon.MCONamespace,
			Annotations: map[string]string{
				"machineconfiguration.openshift.io/pool": pool.Name,
			},
		},
		Spec: buildv1.BuildConfigSpec{
			RunPolicy: "Serial",
			// Simple dockerfile build, just the text from the dockerfile
			CommonSpec: buildv1.CommonSpec{
				Source: buildv1.BuildSource{
					Type:       "Dockerfile",
					Dockerfile: &dockerFile,
				},
				Strategy: buildv1.BuildStrategy{
					DockerStrategy: &buildv1.DockerBuildStrategy{
						// This will override the last FROM in our builds, but we want that
						From: &corev1.ObjectReference{
							Kind: "ImageStreamTag",
							Name: sourceImageStreamName + ":latest",
						},
						// Squashing layers is good as long as it doesn't cause problems with what
						// the users want to do. It says "some syntax is not supported"
						ImageOptimizationPolicy: &skipLayers,
					},
					Type: "Docker",
				},
				// Output to the imagestreams we made before
				Output: buildv1.BuildOutput{
					To: &corev1.ObjectReference{
						Kind: "ImageStreamTag",
						Name: targetImageStream.Name + ":latest",
					},
					ImageLabels: []buildv1.ImageLabel{
						// The pool that this image was built for
						{Name: "io.openshift.machineconfig.pool", Value: pool.Name},
					},
					// TODO(jkyros): I want to label these images with which rendered config they were built from
					// but there doesn't seem to be a way to get it in there easily
				},
			},

			Triggers: []buildv1.BuildTriggerPolicy{
				{
					// This blank one signifies "just trigger on the from image specified in the strategy"
					Type:        "ImageChange",
					ImageChange: &buildv1.ImageChangeTrigger{},
				},
			},
		},
	}

	// Pause the custom build config by default so it doesn't build automatically unless we enable it
	if buildConfigName == pool.Name+"-build"+ctrlcommon.ImageStreamSuffixMCOContentCustom {
		buildConfig.Spec.Triggers[0].ImageChange.Paused = true
	}

	// TODO(jkyros): pull this out if we handle these triggers ourselves, because we might need the control
	// If additional triggers, add them to the config

	for _, tag := range triggerOnImageTags {
		buildConfig.Spec.Triggers = append(buildConfig.Spec.Triggers, buildv1.BuildTriggerPolicy{
			Type: "ImageChange",
			ImageChange: &buildv1.ImageChangeTrigger{
				LastTriggeredImageID: "",
				From: &corev1.ObjectReference{
					Kind: "ImageStreamTag",
					Name: tag,
				},
			},
		})

	}

	// Set the owner references so these get cleaned up if the pool gets deleted
	poolKind := mcfgv1.SchemeGroupVersion.WithKind("MachineConfigPool")
	oref := metav1.NewControllerRef(pool, poolKind)
	buildConfig.SetOwnerReferences([]metav1.OwnerReference{*oref})

	// Create the buildconfig
	return ctrl.buildclient.BuildV1().BuildConfigs(ctrlcommon.MCONamespace).Create(context.TODO(), buildConfig, metav1.CreateOptions{})

}

// TODO(jkyros): don't leave this here, expose it properly if you're gonna use it
// StrToPtr returns a pointer to a string
func StrToPtr(s string) *string {
	return &s
}

// TODO(jkyros): don't leave this here, expose it properly if you're gonna use it
// NewIgnFile returns a simple ignition3 file from just path and file contents
func NewIgnFile(path, contents string) ign3types.File {
	return ign3types.File{
		Node: ign3types.Node{
			Path: path,
		},
		FileEmbedded1: ign3types.FileEmbedded1{
			Contents: ign3types.Resource{
				Source: StrToPtr(dataurl.EncodeBytes([]byte(contents)))},
		},
	}
}

// TODO(jkyros): some quick functions to go with our image stream informer so we can watch imagestream update
func (ctrl *Controller) resolveControllerRef(controllerRef *metav1.OwnerReference) *mcfgv1.MachineConfigPool {
	// We can't look up by UID, so look up by Name and then verify UID.
	// Don't even try to look up by Name if it's the wrong Kind.
	if controllerRef.Kind != controllerKind.Kind {
		return nil
	}
	pool, err := ctrl.mcpLister.Get(controllerRef.Name)
	if err != nil {
		return nil
	}

	if pool.UID != controllerRef.UID {
		// The controller we found with this Name is not the same one that the
		// ControllerRef points to.
		return nil
	}
	return pool
}

//nolint:unparam
func (ctrl *Controller) getMostRecentImageTagForImageStream(poolImageStream *imagev1.ImageStream, desiredTag string) *imagev1.TagEvent {
	// Get the most recent image
	for _, tag := range poolImageStream.Status.Tags {
		if tag.Tag == desiredTag {
			// TODO(jkyros): don't crash if this is empty
			if len(tag.Items) > 0 {
				return &tag.Items[0]
			}
		}
	}
	return nil
}

func (ctrl *Controller) syncAvailableStatus(pool *mcfgv1.MachineConfigPool) error {
	if mcfgv1.IsMachineConfigPoolConditionFalse(pool.Status.Conditions, mcfgv1.MachineConfigPoolRenderDegraded) {
		return nil
	}
	sdegraded := mcfgv1.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolRenderDegraded, corev1.ConditionFalse, "", "")
	mcfgv1.SetMachineConfigPoolCondition(&pool.Status, *sdegraded)
	if _, err := ctrl.client.MachineconfigurationV1().MachineConfigPools().UpdateStatus(context.TODO(), pool, metav1.UpdateOptions{}); err != nil {
		return err
	}
	return nil
}

func (ctrl *Controller) syncFailingStatus(pool *mcfgv1.MachineConfigPool, err error) error {
	sdegraded := mcfgv1.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolRenderDegraded, corev1.ConditionTrue, "", fmt.Sprintf("Failed to render configuration for pool %s: %v", pool.Name, err))
	mcfgv1.SetMachineConfigPoolCondition(&pool.Status, *sdegraded)
	if _, updateErr := ctrl.client.MachineconfigurationV1().MachineConfigPools().UpdateStatus(context.TODO(), pool, metav1.UpdateOptions{}); updateErr != nil {
		glog.Errorf("Error updating MachineConfigPool %s: %v", pool.Name, updateErr)
	}
	return err
}

func (ctrl *Controller) updateBuildConfigWithLabels(buildConfig *buildv1.BuildConfig, labels map[string]string) (*buildv1.BuildConfig, error) {

	newBuildConfig := buildConfig.DeepCopy()
	for labelKey, labelValue := range labels {
		il := buildv1.ImageLabel{Name: labelKey, Value: labelValue}
		newBuildConfig.Spec.Output.ImageLabels = append(newBuildConfig.Spec.Output.ImageLabels, il)
	}

	return ctrl.buildclient.BuildV1().BuildConfigs(ctrlcommon.MCONamespace).Update(context.TODO(), newBuildConfig, metav1.UpdateOptions{})
}

// ensureCoreOSImageStream creates the base CoreOS imagestream that is owned by no pool and serves as the default source of the
// base images for the layered pools' base image streams
func (ctrl *Controller) ensureCoreOSImageStream() (*imagev1.ImageStream, error) {

	checkImageStream, err := ctrl.isLister.ImageStreams(ctrlcommon.MCONamespace).Get(ctrlcommon.CoreOSImageStreamName)
	if apierrors.IsNotFound(err) {
		controllerConfig, err := ctrl.ccLister.Get(ctrlcommon.ControllerConfigName)
		if err != nil {
			return nil, fmt.Errorf("could not get ControllerConfig %w", err)
		}

		newImageStream := &imagev1.ImageStream{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ctrlcommon.CoreOSImageStreamName,
				Namespace: ctrlcommon.MCONamespace,
			},
			Spec: imagev1.ImageStreamSpec{
				LookupPolicy:          imagev1.ImageLookupPolicy{Local: false},
				DockerImageRepository: "",
				Tags: []imagev1.TagReference{
					{
						Name: "latest",
						From: &corev1.ObjectReference{
							Kind: "DockerImage",
							Name: controllerConfig.Spec.BaseOperatingSystemContainer,
						},
					},
				},
			},
		}
		checkImageStream, err = ctrl.imageclient.ImageV1().ImageStreams(ctrlcommon.MCONamespace).Create(context.TODO(), newImageStream, metav1.CreateOptions{})
		if err != nil {
			return nil, fmt.Errorf("Attempted to create ImageStream %s but failed: %w", newImageStream, err)
		}
		glog.Infof("Created image stream %s", ctrlcommon.CoreOSImageStreamName)
	} else if err != nil {
		return nil, err
	}

	return checkImageStream, nil

}

func (ctrl *Controller) ensureImageStreamForPool(pool *mcfgv1.MachineConfigPool, imageStreamName string, pbr *PoolResourceNames) (*imagev1.ImageStream, error) {
	// Check to see if we have the imagestream already
	checkImageStream, err := ctrl.isLister.ImageStreams(ctrlcommon.MCONamespace).Get(imageStreamName)
	if apierrors.IsNotFound(err) {
		// Create the imagestream if it doesn't already exist
		// It doesn't exist, so we need to make it, otherwise our builds will fail
		newImageStream := &imagev1.ImageStream{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					"machineconfiguration.openshift.io/pool": pool.Name,
				},
			},
		}
		newImageStream.Name = imageStreamName
		newImageStream.Namespace = ctrlcommon.MCONamespace
		newImageStream.Spec.LookupPolicy.Local = false

		// Set ownerships so these get cleaned up if we delete the pool
		// TODO(jkyros): I have no idea if this actually cleans the images out of the stream if we delete it?
		poolKind := mcfgv1.SchemeGroupVersion.WithKind("MachineConfigPool")
		oref := metav1.NewControllerRef(pool, poolKind)
		newImageStream.SetOwnerReferences([]metav1.OwnerReference{*oref})

		// coreos imagestream is base, it's special, it needs to pull that image
		if imageStreamName == pbr.ImageStream.Base {

			newImageStream.Spec = imagev1.ImageStreamSpec{
				LookupPolicy:          imagev1.ImageLookupPolicy{Local: false},
				DockerImageRepository: "",
				Tags: []imagev1.TagReference{
					{
						Name: "latest",
						From: &corev1.ObjectReference{
							Kind: "DockerImage",
							Name: "image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/coreos",
						},
					},
				},
			}

		}

		// TODO(jkyros): your data structure for this is clearly inelegant, fix it
		if imageStreamName == pbr.ImageStream.Base || imageStreamName == pbr.ImageStream.RenderedConfig {
			newImageStream.Annotations["machineconfig.openshift.io/buildconfig"] = pbr.BuildConfig.Content.Name
		}
		if imageStreamName == pbr.ImageStream.Content {
			newImageStream.Annotations["machineconfig.openshift.io/buildconfig"] = pbr.BuildConfig.CustomContent.Name
		}

		// It didn't exist, put the imagestream in the cluster
		checkImageStream, err = ctrl.imageclient.ImageV1().ImageStreams(ctrlcommon.MCONamespace).Create(context.TODO(), newImageStream, metav1.CreateOptions{})
		if err != nil {
			return nil, fmt.Errorf("Attempted to create ImageStream %s but failed: %w", newImageStream, err)
		}
		glog.Infof("Created image stream %s", imageStreamName)
	}
	return checkImageStream, nil
}

func (ctrl *Controller) ensureBuildConfigForPool(pool *mcfgv1.MachineConfigPool, pbc *PoolBuildConfig) (*buildv1.BuildConfig, error) {
	checkBuildConfig, err := ctrl.bcLister.BuildConfigs(ctrlcommon.MCONamespace).Get(pbc.Name)
	if apierrors.IsNotFound(err) {

		// We are making this buildconfig owned by the imagestream it's building to
		// TODO(jkyros): I really do feel like the buildconfig belongs to the stream because it populates the stream
		ownerStream, err := ctrl.isLister.ImageStreams(ctrlcommon.MCONamespace).Get(pbc.Target)
		if err != nil {
			return nil, fmt.Errorf("Failed to retrieve owner imagestream: %w", err)
		}
		// Make the build since it doesn't exist, and set checkBuildConfig so we can use it below
		checkBuildConfig, err = ctrl.CreateBuildConfigForImageStream(pool, pbc.Name, pbc.Source, ownerStream, pbc.DockerfileContent, pbc.TriggeredByStreams...)
		if err != nil {
			return nil, err
		}
		glog.Infof("BuildConfig %s has been created for pool %s", pbc.Name, pool.Name)
	} else if err != nil {
		// some other error happened
		return nil, err
	}
	return checkBuildConfig, nil
}

func (ctrl *Controller) enqueuePoolIfBuildProblems(build *buildv1.Build) {
	// If it's in a good state, save the suffering and move on
	if isGoodBuildPhase(build.Status.Phase) {
		return
	}

	// TODO(jkyros): sequester this in a function somewhere

	// If it's in a bad phase, our pool might care if it's one of ours

	// See who owns the build
	controllerRef := metav1.GetControllerOf(build)

	// If the build is owned by a buildconfig, see if it's one of ours
	if controllerRef.Kind == "BuildConfig" {
		buildConfig, err := ctrl.bcLister.BuildConfigs(ctrlcommon.MCONamespace).Get(controllerRef.Name)
		if err != nil {
			glog.Errorf("Failed to retrieve controlling buildconfig %s for build %s: %s", controllerRef.Name, build.Name, err)
		}

		// See if the buildconfig is controlled by our pool
		buildConfigControllerRef := metav1.GetControllerOf(buildConfig)
		if controllerRef != nil {
			pool := ctrl.resolveControllerRef(buildConfigControllerRef)
			// If it is our pool, then enqueue it
			if pool != nil {
				ctrl.enqueueMachineConfigPool(pool)
			}

		}

	}
}

// isGoodBuildPhase determines whether a build is okay, or if it had a problem that we potentially need to take action on. This is used to decide
// whether or not re-queue a machineconfig pool to check on its builds if the build came from one of its build controllers.
func isGoodBuildPhase(buildPhase buildv1.BuildPhase) bool {

	if buildPhase != buildv1.BuildPhaseFailed && buildPhase != buildv1.BuildPhaseCancelled && buildPhase != buildv1.BuildPhaseError {
		return true
	}
	return false
}

func (ctrl *Controller) getLabelsForImageRef(imageRef string) (map[string]string, error) {
	fullImage, err := ctrl.imageclient.ImageV1().Images().Get(context.TODO(), imageRef, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("Could not retrieve image %s: %w", imageRef, err)
	}

	// We need the labels out of the docker image but it's a raw extension
	dockerLabels := struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}{}

	// Get the labels out and see what config this is
	err = json.Unmarshal(fullImage.DockerImageMetadata.Raw, &dockerLabels)
	if err != nil {
		return nil, fmt.Errorf("Could not get labels from docker image metadata: %w", err)
	}
	return dockerLabels.Config.Labels, nil
}

// ensureImageStreamPrecedenceIfPopulated tries to make the UX cleaner by automatically switching the pool to use custom/external imagestreams
// if it looks like the user has populated them. It will switch back if those imagestreams get cleared out. This is really just to save the user from
// having to update the pool annotations themselves.
func (ctrl *Controller) ensureImageStreamPrecedenceIfPopulated(pool *mcfgv1.MachineConfigPool) error {
	glog.Infof("Ensuring imagestreams are populated for %s", pool.Name)
	// Get the list of what resources should exist for this pool
	pbr := PoolBuildResources(pool)

	// Get the imagestream object for the external base imagestream
	coreosImageStream, err := ctrl.isLister.ImageStreams(ctrlcommon.MCONamespace).Get(ctrlcommon.CoreOSImageStreamName)
	if err != nil {
		return err
	}

	// Get the imagestream object for the external base imagestream
	baseImageStream, err := ctrl.isLister.ImageStreams(ctrlcommon.MCONamespace).Get(pbr.ImageStream.Base)
	if err != nil {
		return err
	}

	// Get the imagestream object for the external base imagestream
	externalBaseImageStream, err := ctrl.isLister.ImageStreams(ctrlcommon.MCONamespace).Get(pbr.ImageStream.ExternalBase)
	if err != nil {
		return err
	}

	// Get the imagestream object for the external imagestream
	externalImageStream, err := ctrl.isLister.ImageStreams(ctrlcommon.MCONamespace).Get(pbr.ImageStream.External)
	if err != nil {
		return err
	}

	// Get the imagestream objects for the custom imagestream, too
	customImageStream, err := ctrl.isLister.ImageStreams(ctrlcommon.MCONamespace).Get(pbr.ImageStream.CustomContent)
	if err != nil {
		return err
	}

	// Retrieve the name of the imagestream we're currently using
	poolImageStreamName, _ := ctrlcommon.GetPoolImageStream(pool)

	// This is the place where we set the pool image stream if it's not set, so it's not an error here
	if poolImageStreamName == "" {
		ctrlcommon.SetPoolImageStream(pool, pbr.ImageStream.Content)
	}

	// Get the latest tag from external base
	latestExternalBaseImageTag := ctrl.getMostRecentImageTagForImageStream(externalBaseImageStream, "latest")
	latestBaseImageTag := ctrl.getMostRecentImageTagForImageStream(baseImageStream, "latest")
	latestCoreOSImageTag := ctrl.getMostRecentImageTagForImageStream(coreosImageStream, "latest")

	// If there is something in external base, we need to tag it into our base
	if latestExternalBaseImageTag != nil {

		if latestBaseImageTag == nil || latestBaseImageTag.Image != latestExternalBaseImageTag.Image {
			if latestBaseImageTag != nil {
				glog.Infof("Latest base: %s Latest external: %s", latestBaseImageTag.Image, latestExternalBaseImageTag.Image)
			} else {
				glog.Infof("Latest base image tag was empty, assigning external")
			}
			err := ctrl.tagImageIntoStream(externalBaseImageStream.Name, baseImageStream.Name, latestExternalBaseImageTag.Image, "latest")
			if err != nil {
				return err
			}
		}
	} else {
		// If there is nothing in external base, we should use coreos as our base
		if latestBaseImageTag == nil {
			if latestCoreOSImageTag == nil {
				return fmt.Errorf("we don't have a CoreOS image yet -- probably still downloading, need to wait")
			}
		} else {
			glog.Infof("Latest base: %s Latest coreos: %s", latestBaseImageTag.Image, latestCoreOSImageTag.Image)

			// If what we have is different than what coreos has, we should use what coreos has instead
			if latestBaseImageTag.Image != latestCoreOSImageTag.Image {
				err := ctrl.tagImageIntoStream(coreosImageStream.Name, baseImageStream.Name, latestCoreOSImageTag.Image, "latest")
				if err != nil {
					return err
				}
			}
		}

	}

	// If we aren't using the external image stream, and it is populated, we should switch to it
	if poolImageStreamName != externalImageStream.Name && ctrl.getMostRecentImageTagForImageStream(externalImageStream, "latest") != nil {
		// TODO(jkyros): Technically I event here before the update happens down below later, that seems dishonest for the user since
		// at this point what I say happened hasn't happened yet
		ctrl.eventRecorder.Event(pool, corev1.EventTypeNormal, "ImageStreamChange", "Image stream for pool "+pool.Name+" changed to "+externalImageStream.Name+
			" because it takes precedence and an image is present in it")
		ctrlcommon.SetPoolImageStream(pool, pbr.ImageStream.External)

		// External isn't populated, see if we shuold fall back to custom if it has an image or an updated buildconfig
	} else if poolImageStreamName != customImageStream.Name && ctrl.getMostRecentImageTagForImageStream(customImageStream, "latest") != nil {
		ctrl.eventRecorder.Event(pool, corev1.EventTypeNormal, "ImageStreamChange", "Image stream for pool  "+pool.Name+" changed to "+customImageStream.Name+
			" because it takes precedence and an image is present in it")
		ctrlcommon.SetPoolImageStream(pool, pbr.ImageStream.CustomContent)

	} else if poolImageStreamName != pbr.ImageStream.Content {
		// If we didn't catch one of the previous if blocks, we should be using the default MCO content stream. This lets us fall back
		// if/when someone cleans out or deletes one of the imagestreams
		// TODO(jkyros): This self-healing behavior does keep people from assigning arbitrary imagstreams (whether that's good or
		// bad is up to us)
		ctrl.eventRecorder.Event(pool, corev1.EventTypeNormal, "ImageStreamChange", "Image stream for pool  "+pool.Name+" falling back to "+pbr.ImageStream.Content+
			" as others are unpopulated")
		ctrlcommon.SetPoolImageStream(pool, pbr.ImageStream.Content)
	}
	return nil
}

func (ctrl *Controller) tagImageIntoStream(sourceImageStreamName, targetImageStreamName, imageName, tagName string) error {

	var internalRegistry = "image-registry.openshift-image-registry.svc:5000/"
	fullTargetTagName := targetImageStreamName + ":" + tagName
	//  If you don't get the namespace prefix on there, it tries to pull it from docker.io and fails
	fullSourceName := internalRegistry + ctrlcommon.MCONamespace + "/" + sourceImageStreamName + "@" + imageName

	var tag *imagev1.ImageStreamTag
	tag, err := ctrl.imageclient.ImageV1().ImageStreamTags(ctrlcommon.MCONamespace).Get(context.TODO(), fullTargetTagName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {

			it := &imagev1.ImageStreamTag{

				ObjectMeta: metav1.ObjectMeta{
					Name:      fullTargetTagName,
					Namespace: ctrlcommon.MCONamespace,
				},
				Tag: &imagev1.TagReference{
					Name: tagName,
					From: &corev1.ObjectReference{
						Kind:      "ImageStreamImage",
						Namespace: ctrlcommon.MCONamespace,
						Name:      fullSourceName,
					},
					ReferencePolicy: imagev1.TagReferencePolicy{
						Type: imagev1.SourceTagReferencePolicy,
					},
				},
			}
			glog.Infof("Tagging image %s from %s into imagestream %s", imageName+":"+tagName, sourceImageStreamName, targetImageStreamName)
			_, err = ctrl.imageclient.ImageV1().ImageStreamTags(ctrlcommon.MCONamespace).Create(context.TODO(), it, metav1.CreateOptions{})
			return err

		}
		return err

	}
	tag.Tag.From.Name = fullSourceName
	glog.Infof("Updating image tag %s from %s into imagestream %s", imageName+":"+tagName, sourceImageStreamName, targetImageStreamName)

	_, err = ctrl.imageclient.ImageV1().ImageStreamTags(ctrlcommon.MCONamespace).Update(context.TODO(), tag, metav1.UpdateOptions{})
	return err

}

func (ctrl *Controller) cheatMachineConfigLabelIntoBuildConfig(imageStream *imagev1.ImageStream, pool *mcfgv1.MachineConfigPool) error {
	// This is the mco content imagestream
	latestImageTag := ctrl.getMostRecentImageTagForImageStream(imageStream, "latest")
	if latestImageTag == nil {
		return fmt.Errorf("No 'latest' image tag in imagestream %s: ", imageStream.Name)

	}
	labels, err := ctrl.getLabelsForImageRef(latestImageTag.Image)
	if err != nil {
		return fmt.Errorf("Failed to retrieve labels for imagestream tag %s: %w", latestImageTag.DockerImageReference, err)
	}

	buildConfig, err := ctrl.bcLister.BuildConfigs(ctrlcommon.MCONamespace).Get(pool.Name + "-build" + ctrlcommon.ImageStreamSuffixMCOContent)
	if err != nil {
		return fmt.Errorf("Failed to retrieve corresponding buildconfig: %w", err)
	}

	// Get buildconfig
	_, err = ctrl.updateBuildConfigWithLabels(buildConfig, labels)
	if err != nil {
		return fmt.Errorf("Failed to update buildconfig %s with labels: %w", buildConfig.Name, err)
	}

	return nil
}
