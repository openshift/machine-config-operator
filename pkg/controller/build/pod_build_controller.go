package build

import (
	"context"
	"fmt"
	"strings"
	"time"

	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/scheme"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	aggerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreclientsetv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	corelistersv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

// Controller defines the build controller.
type PodBuildController struct {
	*Clients
	*informers

	eventRecorder record.EventRecorder

	// The function to call whenever we've encountered a build pod. This function is
	// responsible for examining the build pod to determine what state its in and map
	// that state to the appropriate MachineConfigPool object.
	podHandler func(*corev1.Pod) error

	syncHandler func(pod string) error
	enqueuePod  func(*corev1.Pod)

	podLister corelistersv1.PodLister

	podListerSynced cache.InformerSynced

	queue workqueue.RateLimitingInterface

	config BuildControllerConfig
}

var _ ImageBuilder = (*PodBuildController)(nil)

// Returns a new pod build controller.
func newPodBuildController(
	ctrlConfig BuildControllerConfig,
	clients *Clients,
	podHandler func(*corev1.Pod) error,
) *PodBuildController {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&coreclientsetv1.EventSinkImpl{Interface: clients.kubeclient.CoreV1().Events("")})

	ctrl := &PodBuildController{
		Clients:       clients,
		informers:     newInformers(clients),
		eventRecorder: eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineosbuilder-podbuildcontroller"}),
		queue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "machineosbuilder-podbuildcontroller"),
		config:        ctrlConfig,
		podHandler:    podHandler,
	}

	// As an aside, why doesn't the constructor here set up all the informers?
	ctrl.podInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addPod,
		UpdateFunc: ctrl.updatePod,
		DeleteFunc: ctrl.deletePod,
	})

	ctrl.podLister = ctrl.podInformer.Lister()

	ctrl.podListerSynced = ctrl.podInformer.Informer().HasSynced

	ctrl.syncHandler = ctrl.syncPod
	ctrl.enqueuePod = ctrl.enqueueDefault

	return ctrl
}

func (ctrl *PodBuildController) enqueue(pod *corev1.Pod) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pod)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", pod, err))
		return
	}

	ctrl.queue.Add(key)
}

func (ctrl *PodBuildController) enqueueRateLimited(pod *corev1.Pod) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pod)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", pod, err))
		return
	}

	ctrl.queue.AddRateLimited(key)
}

// enqueueAfter will enqueue a pod after the provided amount of time.
func (ctrl *PodBuildController) enqueueAfter(pod *corev1.Pod, after time.Duration) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pod)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", pod, err))
		return
	}

	ctrl.queue.AddAfter(key, after)
}

// enqueueDefault calls a default enqueue function
func (ctrl *PodBuildController) enqueueDefault(pod *corev1.Pod) {
	ctrl.enqueueAfter(pod, ctrl.config.UpdateDelay)
}

// Syncs pods.
func (ctrl *PodBuildController) syncPod(key string) error { //nolint:dupl // This does have commonality with the ImageBuildController.
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
	pod, err := ctrl.podLister.Pods(ctrlcommon.MCONamespace).Get(name)
	if k8serrors.IsNotFound(err) {
		klog.V(2).Infof("Pod %v has been deleted", key)
		return nil
	}
	if err != nil {
		return err
	}

	pod, err = ctrl.kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).Get(context.TODO(), pod.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	// If we don't have all of the OS build labels attached to this pod, we
	// ignore it. There is probably something we can do along the lines looking
	// at ownership though.
	if !hasAllRequiredOSBuildLabels(pod.Labels) {
		klog.Infof("Ignoring non-build pod %s", pod.Name)
		return nil
	}

	if err := ctrl.podHandler(pod); err != nil {
		return fmt.Errorf("unable to update with build pod status: %w", err)
	}

	klog.Infof("Updated MachineConfigPool with build pod status. Build pod %s in %s", pod.Name, pod.Status.Phase)

	return nil
}

// Starts the Pod Build Controller.
func (ctrl *PodBuildController) Run(ctx context.Context, workers int) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()

	ctrl.informers.start(ctx)

	if !cache.WaitForCacheSync(ctx.Done(), ctrl.podListerSynced) {
		return
	}

	klog.Info("Starting MachineOSBuilder-PodBuildController")
	defer klog.Info("Shutting down MachineOSBuilder-PodBuildController")

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.worker, time.Second, ctx.Done())
	}

	<-ctx.Done()
}

// Deletes the underlying build pod.
func (ctrl *PodBuildController) DeleteBuildObject(mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig) error {
	// We want to ignore when a pod or ConfigMap is deleted if it is not found.
	// This is because when a pool is opted out of layering *after* a successful
	// build, no pod nor ConfigMap will remain. So we want to be able to
	// idempotently call this function in that case.
	return aggerrors.AggregateGoroutines(
		func() error {
			ibr := newImageBuildRequest(mosc, mosb)
			return ignoreIsNotFoundErr(ctrl.kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).Delete(context.TODO(), ibr.getBuildName(), metav1.DeleteOptions{}))
		},
		func() error {
			ibr := newImageBuildRequest(mosc, mosb)
			return ignoreIsNotFoundErr(ctrl.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Delete(context.TODO(), ibr.getDigestConfigMapName(), metav1.DeleteOptions{}))
		},
	)
}

// Determines if a build is currently running by looking for a corresponding pod.
func (ctrl *PodBuildController) IsBuildRunning(mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig) (bool, error) {
	ibr := newImageBuildRequest(mosc, mosb)

	// First check if we have a build in progress for this MachineConfigPool and rendered config.
	_, err := ctrl.kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).Get(context.TODO(), ibr.getBuildName(), metav1.GetOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return false, err
	}

	return err == nil, nil
}

// Starts a new build pod, assuming one is not found first. In that case, it returns an object reference to the preexisting build pod.
func (ctrl *PodBuildController) StartBuild(ibr ImageBuildRequest) (*corev1.ObjectReference, error) {
	targetMC := ibr.MachineOSBuild.Spec.DesiredConfig.Name

	// TODO: Find a constant for this:
	if !strings.HasPrefix(targetMC, "rendered-") {
		return nil, fmt.Errorf("%s is not a rendered MachineConfig", targetMC)
	}

	// First check if we have a build in progress for this MachineConfigPool and rendered config.
	pod, err := ctrl.kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).Get(context.TODO(), ibr.getBuildName(), metav1.GetOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return nil, err
	}

	// This means we found a preexisting build pod.
	if pod != nil && err == nil && hasAllRequiredOSBuildLabels(pod.Labels) {
		klog.Infof("Found preexisting build pod (%s) for pool %s", pod.Name, ibr.MachineOSConfig.Spec.MachineConfigPool.Name)
		return toObjectRef(pod), nil
	}

	klog.Infof("Starting build for pool %s", ibr.MachineOSConfig.Spec.MachineConfigPool.Name)
	klog.Infof("Build pod name: %s", ibr.getBuildName())
	klog.Infof("Final image will be pushed to %q, using secret %q", ibr.MachineOSConfig.Status.CurrentImagePullspec, ibr.MachineOSConfig.Spec.BuildOutputs.CurrentImagePullSecret.Name)

	pod, err = ctrl.kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).Create(context.TODO(), ibr.toBuildPod(), metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not create build pod: %w", err)
	}

	klog.Infof("Build started for pool %s in %s!", ibr.MachineOSConfig.Spec.MachineConfigPool.Name, pod.Name)

	return toObjectRef(pod), nil
}

// Fires whenever a new pod is started.
func (ctrl *PodBuildController) addPod(obj interface{}) {
	pod := obj.(*corev1.Pod).DeepCopy()
	isBuildPod := hasAllRequiredOSBuildLabels(pod.Labels)
	klog.V(4).Infof("Adding Pod %s. Is build pod? %v", pod.Name, isBuildPod)
	if isBuildPod {
		ctrl.enqueuePod(pod)
	}
}

// Fires whenever a pod is updated.
func (ctrl *PodBuildController) updatePod(oldObj, curObj interface{}) {
	oldPod := oldObj.(*corev1.Pod).DeepCopy()
	curPod := curObj.(*corev1.Pod).DeepCopy()

	isBuildPod := hasAllRequiredOSBuildLabels(curPod.Labels)

	// Ignore non-build pods.
	// TODO: Figure out if we can add the filter criteria onto the lister.
	if !isBuildPod {
		return
	}

	klog.Infof("Updating pod %s. Is build pod? %v", curPod.Name, isBuildPod)

	if oldPod.Status.Phase != curPod.Status.Phase {
		klog.Infof("Pod %s changed from %s to %s", oldPod.Name, oldPod.Status.Phase, curPod.Status.Phase)
	}

	klog.Infof("Pod %s updated", curPod.Name)

	ctrl.enqueuePod(curPod)
}

func (ctrl *PodBuildController) handleErr(err error, key interface{}) {
	if err == nil {
		ctrl.queue.Forget(key)
		return
	}

	if ctrl.queue.NumRequeues(key) < ctrl.config.MaxRetries {
		klog.V(2).Infof("Error syncing pod %v: %v", key, err)
		ctrl.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	klog.V(2).Infof("Dropping pod %q out of the queue: %v", key, err)
	ctrl.queue.Forget(key)
	ctrl.queue.AddAfter(key, 1*time.Minute)
}

// Fires whenever a pod is deleted.
func (ctrl *PodBuildController) deletePod(obj interface{}) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return
	}
	pod = pod.DeepCopy()
	klog.V(4).Infof("Deleting Pod %s. Is build pod? %v", pod.Name, hasAllRequiredOSBuildLabels(pod.Labels))
	ctrl.enqueuePod(pod)
}

// worker runs a worker thread that just dequeues items, processes them, and marks them done.
// It enforces that the syncHandler is never invoked concurrently with the same key.
func (ctrl *PodBuildController) worker() {
	for ctrl.processNextWorkItem() {
	}
}

func (ctrl *PodBuildController) processNextWorkItem() bool {
	key, quit := ctrl.queue.Get()
	if quit {
		return false
	}
	defer ctrl.queue.Done(key)

	err := ctrl.syncHandler(key.(string))
	ctrl.handleErr(err, key)

	return true
}
