package build

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/golang/glog"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/scheme"
	corev1 "k8s.io/api/core/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	coreclientsetv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"

	imagelistersv1 "github.com/openshift/client-go/image/listers/image/v1"

	buildlistersv1 "github.com/openshift/client-go/build/listers/build/v1"

	buildclientset "github.com/openshift/client-go/build/clientset/versioned"
	imageclientset "github.com/openshift/client-go/image/clientset/versioned"

	mcfgclientset "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
	mcfginformersv1 "github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions/machineconfiguration.openshift.io/v1"
	mcfglistersv1 "github.com/openshift/machine-config-operator/pkg/generated/listers/machineconfiguration.openshift.io/v1"

	coreinformersv1 "k8s.io/client-go/informers/core/v1"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corelistersv1 "k8s.io/client-go/listers/core/v1"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
)

const (
	targetMachineConfigPoolLabel = "machineconfiguration.openshift.io/targetMachineConfigPool"
	// TODO(zzlotnik): Is there a constant for this someplace else?
	desiredConfigLabel = "machineconfiguration.openshift.io/desiredConfig"
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
	podLister corelistersv1.PodLister

	ccListerSynced  cache.InformerSynced
	mcpListerSynced cache.InformerSynced
	podListerSynced cache.InformerSynced

	queue workqueue.RateLimitingInterface

	config BuildControllerConfig
}

func DefaultBuildControllerConfig() BuildControllerConfig {
	return BuildControllerConfig{
		MaxRetries:  5,
		UpdateDelay: time.Second * 5,
	}
}

// New returns a new node controller.
func New(
	ctrlConfig BuildControllerConfig,
	podInformer coreinformersv1.PodInformer,
	ccInformer mcfginformersv1.ControllerConfigInformer,
	mcpInformer mcfginformersv1.MachineConfigPoolInformer,
	mcfgClient mcfgclientset.Interface,
	kubeClient clientset.Interface,
) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.Infof)
	eventBroadcaster.StartRecordingToSink(&coreclientsetv1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	ctrl := &Controller{
		client:        mcfgClient,
		kubeclient:    kubeClient,
		eventRecorder: eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineosbuilder-buildcontroller"}),
		queue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "machineosbuilder-buildcontroller"),
		config:        ctrlConfig,
	}

	// As an aside, why doesn't the constructor here set up all the informers?
	podInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addPod,
		UpdateFunc: ctrl.updatePod,
		DeleteFunc: ctrl.deletePod,
	})

	mcpInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addMachineConfigPool,
		UpdateFunc: ctrl.updateMachineConfigPool,
		DeleteFunc: ctrl.deleteMachineConfigPool,
	})

	ctrl.syncHandler = ctrl.syncMachineConfigPool
	ctrl.enqueueMachineConfigPool = ctrl.enqueueDefault

	ctrl.ccLister = ccInformer.Lister()
	ctrl.mcpLister = mcpInformer.Lister()
	ctrl.podLister = podInformer.Lister()

	ctrl.ccListerSynced = ccInformer.Informer().HasSynced
	ctrl.mcpListerSynced = mcpInformer.Informer().HasSynced
	ctrl.podListerSynced = podInformer.Informer().HasSynced

	return ctrl
}

func (ctrl *Controller) addPod(obj interface{}) {
	pod := obj.(*corev1.Pod)
	glog.V(4).Infof("Adding Pod %s. Is build pod? %v", pod.Name, isBuildPod(pod))
}

func (ctrl *Controller) updatePod(oldObj, curObj interface{}) {
	curPod := curObj.(*corev1.Pod).DeepCopy()

	// Ignore non-build pods.
	// TODO: Figure out if we can add the filter criteria onto the lister.
	if !isBuildPod(curPod) {
		return
	}

	pool, err := ctrl.client.MachineconfigurationV1().MachineConfigPools().Get(context.TODO(), curPod.Labels[targetMachineConfigPoolLabel], metav1.GetOptions{})
	if err != nil {
		ctrl.handleErr(err, curPod.Name)
		return
	}

	switch curPod.Status.Phase {
	case corev1.PodPending:
		glog.Infof("Build pod (%s) is pending", curPod.Name)
		if !mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolBuildPending) {
			err = ctrl.markBuildPending(pool)
		}
	case corev1.PodRunning:
		// If we're running, then there's nothing to do right now.
		glog.Infof("Build pod (%s) is running", curPod.Name)
		if !mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolBuilding) {
			err = ctrl.markBuildInProgress(pool)
		}
	case corev1.PodSucceeded:
		// If we've succeeded, we need to update the pool to indicate that.
		glog.Infof("Build pod (%s) has succeeded", curPod.Name)
		if !mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolBuildSuccess) {
			err = ctrl.markBuildSucceeded(pool)
		}
	case corev1.PodFailed:
		// If we've failed, we need to update the pool to indicate that.
		glog.Infof("Build pod (%s) failed", curPod.Name)
		if !mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolBuildFailed) {
			err = ctrl.markBuildFailed(pool)
		}
	}

	if err != nil {
		ctrl.handleErr(err, pool.Name)
		return
	}

	ctrl.enqueueMachineConfigPool(pool)
}

func (ctrl *Controller) deletePod(obj interface{}) {
	pod := obj.(*corev1.Pod)
	glog.V(4).Infof("Deleting Pod %s. Is build pod? %v", pod.Name, isBuildPod(pod))
}

// Run executes the render controller.
func (ctrl *Controller) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()

	if !cache.WaitForCacheSync(stopCh, ctrl.mcpListerSynced, ctrl.ccListerSynced, ctrl.podListerSynced) {
		return
	}

	glog.Info("Starting MachineOSBuilder-BuildController")
	defer glog.Info("Shutting down MachineOSBuilder-BuildController")

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

func (ctrl *Controller) handleErr(err error, key interface{}) {
	if err == nil {
		ctrl.queue.Forget(key)
		return
	}

	if ctrl.queue.NumRequeues(key) < ctrl.config.MaxRetries {
		glog.V(2).Infof("Error syncing machineconfigpool %v: %v", key, err)
		ctrl.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	glog.V(2).Infof("Dropping machineconfigpool %q out of the queue: %v", key, err)
	ctrl.queue.Forget(key)
	ctrl.queue.AddAfter(key, 1*time.Minute)
}

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
	if k8serrors.IsNotFound(err) {
		glog.V(2).Infof("MachineConfigPool %v has been deleted", key)
		return nil
	}
	if err != nil {
		return err
	}

	// TODO: Doing a deep copy of this pool object from our cache and using it to
	// determine our next course of action sometimes causes a race condition. I'm
	// not sure if it's better to get a current copy from the API server or what.
	// pool := machineconfigpool.DeepCopy()
	pool, err := ctrl.client.MachineconfigurationV1().MachineConfigPools().Get(context.TODO(), machineconfigpool.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	// Not a layered pool, so stop here.
	if !ctrlcommon.IsLayeredPool(pool) {
		glog.V(4).Infof("MachineConfigPool %s is not opted-in for layering, ignoring", pool.Name)
		return nil
	}

	// If we need to do a build, we let updateMachineConfigPool() handle that
	// determination. It registers its intent to build by setting
	// ctrlcommon.MachineCnofigPoolBuildPending on the MachineConfigPool.
	//
	// We look for ctrlcommon.MachineConfigPoolBuildPending and if found, we
	// start the build and set the condition to
	// ctrlcommon.MachineConfigPoolBuilding.
	//
	// We use the PodInformer to determine whether the build is complete. The
	// PodInformer will set either MachineConfigPoolBuildSuccess or
	// MachineConfigPoolBuildFailed.

	switch {
	case mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolDegraded):
		glog.V(4).Infof("MachineConfigPool %s is degraded, requeueing", pool.Name)
		ctrl.enqueueMachineConfigPool(pool)
		return nil
	case mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolRenderDegraded):
		glog.V(4).Infof("MachineConfigPool %s is render degraded, requeueing", pool.Name)
		ctrl.enqueueMachineConfigPool(pool)
		return nil
	case mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolBuildPending):
		glog.V(4).Infof("MachineConfigPool %s needs a build, starting", pool.Name)
		return ctrl.startBuildForMachineConfigPool(pool)
	case mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolBuilding):
		glog.V(4).Infof("MachineConfigPool %s is building", pool.Name)
		return nil
	case mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolBuildSuccess):
		glog.V(4).Infof("MachineConfigPool %s has successfully built", pool.Name)
		return nil
	default:
		glog.V(4).Infof("Nothing to do for pool %q", pool.Name)
	}

	// For everything else
	return ctrl.syncAvailableStatus(pool)
}

// Marks a given MachineConfigPool as a failed build.
func (ctrl *Controller) markBuildFailed(pool *mcfgv1.MachineConfigPool) error {
	glog.Errorf("Build failed for pool %s", pool.Name)

	setMCPBuildConditions(pool, []mcfgv1.MachineConfigPoolCondition{
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

	return ctrl.syncFailingStatus(pool, fmt.Errorf("build failed"))
}

// Marks a given MachineConfigPool as the build is in progress.
func (ctrl *Controller) markBuildInProgress(pool *mcfgv1.MachineConfigPool) error {
	glog.Infof("Build in progress for MachineConfigPool %s, config %s", pool.Name, pool.Spec.Configuration.Name)

	setMCPBuildConditions(pool, []mcfgv1.MachineConfigPoolCondition{
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

	return ctrl.syncAvailableStatus(pool)
}

// Marks a given MachineConfigPool as build successful and cleans up after itself.
func (ctrl *Controller) markBuildSucceeded(pool *mcfgv1.MachineConfigPool) error {
	glog.Infof("Build succeeded for MachineConfigPool %s, config %s", pool.Name, pool.Spec.Configuration.Name)

	if err := ctrl.kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).Delete(context.TODO(), getBuildPodName(pool), metav1.DeleteOptions{}); err != nil {
		return fmt.Errorf("unable to delete build pod: %w", err)
	}

	// Set the annotation or field to point to the newly-built container image.
	// TODO: Figure out how to get that from the build interface.
	deleteBuildPodRefFromMachineConfigPool(pool)
	pool.Labels[ctrlcommon.ExperimentalNewestLayeredImageEquivalentConfigAnnotationKey] = "new-image-pullspec"

	setMCPBuildConditions(pool, []mcfgv1.MachineConfigPoolCondition{
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

	// We need to do an API server round-trip to ensure all of our mutations get
	// propagated.
	updatedPool, err := ctrl.client.MachineconfigurationV1().MachineConfigPools().Update(context.TODO(), pool, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("could not update MachineConfigPool %q: %w", pool.Name, err)
	}

	return ctrl.syncAvailableStatus(updatedPool)
}

// Marks a given MachineConfigPool as build pending.
func (ctrl *Controller) markBuildPending(pool *mcfgv1.MachineConfigPool) error {
	glog.Infof("Build for %s marked pending", pool.Name)

	setMCPBuildConditions(pool, []mcfgv1.MachineConfigPoolCondition{
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

	return ctrl.syncAvailableStatus(pool)
}

// Machine Config Pools

func (ctrl *Controller) addMachineConfigPool(obj interface{}) {
	pool := obj.(*mcfgv1.MachineConfigPool).DeepCopy()
	glog.V(4).Infof("Adding MachineConfigPool %s", pool.Name)
	ctrl.enqueueMachineConfigPool(pool)
}

func (ctrl *Controller) isBuildRunningForPool(pool *mcfgv1.MachineConfigPool) (bool, error) {
	// First check if we have a build in progress for this MachineConfigPool and rendered config.
	_, err := ctrl.kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).Get(context.TODO(), getBuildPodName(pool), metav1.GetOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return false, err
	}

	return err == nil, nil
}

// Determines if we should run a build, then starts a build pod to perform the
// build, and updates the MachineConfigPool with an object reference for the
// build pod.
func (ctrl *Controller) startBuildForMachineConfigPool(pool *mcfgv1.MachineConfigPool) error {
	targetMC := pool.Spec.Configuration.Name

	// TODO: Find a constant for this:
	if !strings.HasPrefix(targetMC, "rendered-") {
		return fmt.Errorf("%s is not a rendered MachineConfig", targetMC)
	}

	isBuildRunning, err := ctrl.isBuildRunningForPool(pool)
	if err != nil {
		return fmt.Errorf("could not determine if a preexisting build is running for %s: %w", pool.Name, err)
	}

	if isBuildRunning {
		return nil
	}

	glog.Infof("Starting build for pool %s", pool.Name)
	glog.Infof("Build pod name: %s", getBuildPodName(pool))

	// TODO: Figure out how to use the Builder interface for starting the build instead of this.
	pod, err := ctrl.kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).Create(context.TODO(), newBuildPod(pool), metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("could not create build pod: %w", err)
	}

	if !machineConfigPoolHasBuildPodRef(pool) {
		ref := corev1.ObjectReference{
			Kind:      "Pod",
			Name:      pod.Name,
			Namespace: pod.Namespace,
			UID:       pod.UID,
		}

		pool.Spec.Configuration.Source = append(pool.Spec.Configuration.Source, ref)

		pool.Status.Configuration.Source = append(pool.Status.Configuration.Source, ref)
	}

	return ctrl.syncAvailableStatus(pool)
}

// If one wants to opt out, this removes all of the statuses and object
// references from a given MachineConfigPool.
func (ctrl *Controller) finalizeOptOut(pool *mcfgv1.MachineConfigPool) error {
	deleteBuildPodRefFromMachineConfigPool(pool)

	conditions := []mcfgv1.MachineConfigPoolCondition{}

	for _, condition := range pool.Status.Conditions {
		buildConditionFound := false
		for _, buildConditionType := range getMachineConfigPoolBuildConditions() {
			if condition.Type == buildConditionType {
				buildConditionFound = true
				break
			}
		}

		if !buildConditionFound {
			conditions = append(conditions, condition)
		}
	}

	pool.Status.Conditions = conditions
	return ctrl.syncAvailableStatus(pool)
}

func (ctrl *Controller) updateMachineConfigPool(old, cur interface{}) {
	oldPool := old.(*mcfgv1.MachineConfigPool).DeepCopy()
	curPool := cur.(*mcfgv1.MachineConfigPool).DeepCopy()

	glog.V(4).Infof("Updating MachineConfigPool %s", oldPool.Name)

	doABuild, err := shouldWeDoABuild(ctrl.kubeclient, oldPool, curPool)
	if err != nil {
		glog.Errorln(err)
		ctrl.handleErr(err, curPool.Name)
		return
	}

	switch {
	case ctrlcommon.IsLayeredPool(oldPool) && !ctrlcommon.IsLayeredPool(curPool):
		glog.V(4).Infof("MachineConfigPool %s has opted out of layering", curPool.Name)
		if err := ctrl.finalizeOptOut(curPool); err != nil {
			glog.Errorln(err)
			ctrl.handleErr(err, curPool.Name)
			return
		}
	case doABuild:
		glog.V(4).Infof("MachineConfigPool %s has changed, requiring a build", curPool.Name)
		if err := ctrl.markBuildPending(curPool); err != nil {
			glog.Errorln(err)
			ctrl.handleErr(err, curPool.Name)
			return
		}
	default:
		glog.V(4).Infof("MachineConfigPool %s up-to-date", curPool.Name)
	}

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

func (ctrl *Controller) syncAvailableStatus(pool *mcfgv1.MachineConfigPool) error {
	// I'm not sure what the consequences are of not doing this.
	//nolint:gocritic // Leaving this here for review purposes.
	/*
		if mcfgv1.IsMachineConfigPoolConditionFalse(pool.Status.Conditions, mcfgv1.MachineConfigPoolRenderDegraded) {
			return nil
		}
	*/
	sdegraded := mcfgv1.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolRenderDegraded, corev1.ConditionFalse, "", "")
	mcfgv1.SetMachineConfigPoolCondition(&pool.Status, *sdegraded)

	if _, err := ctrl.client.MachineconfigurationV1().MachineConfigPools().UpdateStatus(context.TODO(), pool, metav1.UpdateOptions{}); err != nil {
		return err
	}

	return nil
}

func (ctrl *Controller) syncFailingStatus(pool *mcfgv1.MachineConfigPool, err error) error {
	sdegraded := mcfgv1.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolRenderDegraded, corev1.ConditionTrue, "", fmt.Sprintf("Failed to build configuration for pool %s: %v", pool.Name, err))
	mcfgv1.SetMachineConfigPoolCondition(&pool.Status, *sdegraded)
	if _, updateErr := ctrl.client.MachineconfigurationV1().MachineConfigPools().UpdateStatus(context.TODO(), pool, metav1.UpdateOptions{}); updateErr != nil {
		glog.Errorf("Error updating MachineConfigPool %s: %v", pool.Name, updateErr)
	}
	return err
}

// Determines if a MachineConfigPool has a build pod reference.
func machineConfigPoolHasBuildPodRef(pool *mcfgv1.MachineConfigPool) bool {
	buildPodName := getBuildPodName(pool)

	searchFunc := func(cfg mcfgv1.MachineConfigPoolStatusConfiguration) bool {
		for _, src := range cfg.Source {
			if src.Name == buildPodName && src.Kind == "Pod" {
				return true
			}
		}

		return false
	}

	return searchFunc(pool.Spec.Configuration) && searchFunc(pool.Status.Configuration)
}

// Computes the build pod name based upon the MachineConfigPool name and the
// current rendered config.
func getBuildPodName(pool *mcfgv1.MachineConfigPool) string {
	return fmt.Sprintf("build-%s-%s", pool.Name, pool.Spec.Configuration.Name)
}

// Deletes the build pod references from the MachineConfigPool.
func deleteBuildPodRefFromMachineConfigPool(pool *mcfgv1.MachineConfigPool) {
	buildPodName := getBuildPodName(pool)

	deleteFunc := func(cfg mcfgv1.MachineConfigPoolStatusConfiguration) []corev1.ObjectReference {
		configSources := []corev1.ObjectReference{}

		for _, src := range cfg.Source {
			if src.Name != buildPodName {
				configSources = append(configSources, src)
			}
		}

		return configSources
	}

	pool.Spec.Configuration.Source = deleteFunc(pool.Spec.Configuration)
	pool.Status.Configuration.Source = deleteFunc(pool.Status.Configuration)
}

// Determines if two conditions are equal. Note: I purposely do not include the
// timestamp in the equality test, since we do not directly set it.
func isConditionEqual(cond1, cond2 mcfgv1.MachineConfigPoolCondition) bool {
	return cond1.Type == cond2.Type &&
		cond1.Status == cond2.Status &&
		cond1.Message == cond2.Message &&
		cond1.Reason == cond2.Reason
}

// Sets MCP build conditions on a given MachineConfigPool.
func setMCPBuildConditions(pool *mcfgv1.MachineConfigPool, conditions []mcfgv1.MachineConfigPoolCondition) {
	for _, condition := range conditions {
		condition := condition
		currentCondition := mcfgv1.GetMachineConfigPoolCondition(pool.Status, condition.Type)
		if currentCondition != nil && isConditionEqual(*currentCondition, condition) {
			continue
		}

		mcpCondition := mcfgv1.NewMachineConfigPoolCondition(condition.Type, condition.Status, condition.Reason, condition.Message)
		mcfgv1.SetMachineConfigPoolCondition(&pool.Status, *mcpCondition)
	}
}

// Determine if we have a config change.
func isPoolConfigChange(oldPool, curPool *mcfgv1.MachineConfigPool) bool {
	return oldPool.Spec.Configuration.Name != curPool.Spec.Configuration.Name
}

// Determine if we have an image pullspec label.
func hasImagePullspecLabel(pool *mcfgv1.MachineConfigPool) bool {
	imagePullspecLabel, ok := pool.Labels[ctrlcommon.ExperimentalNewestLayeredImageEquivalentConfigAnnotationKey]
	return imagePullspecLabel != "" && ok
}

// Check our pool state to see if we have a build in progress or a failed build.
func isPoolConditionBuild(pool *mcfgv1.MachineConfigPool) bool {
	return !mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolBuilding) &&
		!mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolBuildPending) &&
		!mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolBuildFailed)
}

// Determines if we should do a build based upon the state of our
// MachineConfigPool, the presence of a build pod, etc.
func shouldWeDoABuild(kubeclient clientset.Interface, oldPool, curPool *mcfgv1.MachineConfigPool) (bool, error) {
	// If we don't have a layered pool, we should not build.
	poolStateSuggestsBuild := ctrlcommon.IsLayeredPool(curPool) &&
		// If our pool state indicates that we do not have a build in progress, then
		// we should do a build.
		isPoolConditionBuild(curPool) &&
		// If we have a config change or we're missing an image pullspec label, we
		// should do a build.
		(isPoolConfigChange(oldPool, curPool) || !hasImagePullspecLabel(curPool)) &&
		// If we're missing a build pod reference, it likely means we don't need to
		// do a build.
		!machineConfigPoolHasBuildPodRef(curPool)

	if !poolStateSuggestsBuild {
		return false, nil
	}

	// Look for a build pod.
	_, err := kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).Get(context.TODO(), getBuildPodName(curPool), metav1.GetOptions{})

	// If we have an error and it's not because the build pod was not found, return said error.
	if err != nil && !k8serrors.IsNotFound(err) {
		return false, err
	}

	return k8serrors.IsNotFound(err), nil
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

// Creates a new build pod object.
func newBuildPod(pool *mcfgv1.MachineConfigPool) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      getBuildPodName(pool),
			Namespace: ctrlcommon.MCONamespace,
			Labels: map[string]string{
				ctrlcommon.OSImageBuildPodLabel: "",
				targetMachineConfigPoolLabel:    pool.Name,
				desiredConfigLabel:              pool.Spec.Configuration.Name,
			},
		},
		Spec: corev1.PodSpec{},
	}
}

// Determines if a pod is a build pod by examining its labels.
func isBuildPod(pod *corev1.Pod) bool {
	requiredLabels := []string{
		ctrlcommon.OSImageBuildPodLabel,
		targetMachineConfigPoolLabel,
		desiredConfigLabel,
	}

	for _, label := range requiredLabels {
		if _, ok := pod.Labels[label]; !ok {
			return false
		}
	}

	return true
}
