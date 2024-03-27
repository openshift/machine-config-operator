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
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"

	mcfginformersv1alpha1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	aggerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	coreclientsetv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	buildinformers "github.com/openshift/client-go/build/informers/externalversions"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/scheme"

	buildinformersv1 "github.com/openshift/client-go/build/informers/externalversions/build/v1"

	buildclientset "github.com/openshift/client-go/build/clientset/versioned"

	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"

	mcfginformers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"

	mcfginformersv1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	mcfglistersv1alpha1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1alpha1"
	corelistersv1 "k8s.io/client-go/listers/core/v1"

	coreinformers "k8s.io/client-go/informers"
	coreinformersv1 "k8s.io/client-go/informers/core/v1"

	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/api/equality"
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

type ErrInvalidImageBuilder struct {
	Message     string
	InvalidType string
}

func (e *ErrInvalidImageBuilder) Error() string {
	return e.Message
}

// Image builder constants.

type ImageBuilderType string

const (
	// OpenshiftImageBuilder is the constant indicating use of the OpenShift image builder.
	OpenshiftImageBuilder ImageBuilderType = "OpenShiftImageBuilder"

	// CustomPodImageBuilder is the constant indicating use of the custom pod image builder.
	CustomPodImageBuilder ImageBuilderType = "CustomPodBuilder"
)

var (
	// controllerKind contains the schema.GroupVersionKind for this controller type.
	//nolint:varcheck,deadcode // This will be used eventually
	controllerKind         = mcfgv1.SchemeGroupVersion.WithKind("MachineConfigPool")
	validImageBuilderTypes = sets.New[ImageBuilderType](OpenshiftImageBuilder, CustomPodImageBuilder)
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
	IsBuildRunning(*mcfgv1alpha1.MachineOSBuild, *mcfgv1alpha1.MachineOSConfig) (bool, error)
	DeleteBuildObject(*mcfgv1alpha1.MachineOSBuild, *mcfgv1alpha1.MachineOSConfig) error
}

// Controller defines the build controller.
type Controller struct {
	*Clients
	*informers

	eventRecorder record.EventRecorder

	syncHandler func(build string) error

	cmLister              corelistersv1.ConfigMapLister
	ccLister              mcfglistersv1.ControllerConfigLister
	mcpLister             mcfglistersv1.MachineConfigPoolLister
	machineOSBuildLister  mcfglistersv1alpha1.MachineOSBuildLister
	machineOSConfigLister mcfglistersv1alpha1.MachineOSConfigLister

	machineOSConfigListerSynced cache.InformerSynced
	machineOSBuildListerSynced  cache.InformerSynced
	ccListerSynced              cache.InformerSynced
	mcpListerSynced             cache.InformerSynced
	podListerSynced             cache.InformerSynced

	mosQueue workqueue.RateLimitingInterface

	config           BuildControllerConfig
	imageBuilder     ImageBuilder
	imageBuilderType ImageBuilderType
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
	ccInformer              mcfginformersv1.ControllerConfigInformer
	mcpInformer             mcfginformersv1.MachineConfigPoolInformer
	buildInformer           buildinformersv1.BuildInformer
	podInformer             coreinformersv1.PodInformer
	cmInformer              coreinformersv1.ConfigMapInformer
	machineOSBuildInformer  mcfginformersv1alpha1.MachineOSBuildInformer
	machineOSConfigInformer mcfginformersv1alpha1.MachineOSConfigInformer
	toStart                 []interface{ Start(<-chan struct{}) }
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
	cmInformer := coreinformers.NewFilteredSharedInformerFactory(bcc.kubeclient, 0, ctrlcommon.MCONamespace, nil)
	buildInformer := buildinformers.NewSharedInformerFactoryWithOptions(bcc.buildclient, 0, buildinformers.WithNamespace(ctrlcommon.MCONamespace))
	podInformer := coreinformers.NewSharedInformerFactoryWithOptions(bcc.kubeclient, 0, coreinformers.WithNamespace(ctrlcommon.MCONamespace))
	// this may not work, might need a new mcfg client and or a new informer pkg
	machineOSBuildInformer := mcfginformers.NewSharedInformerFactory(bcc.mcfgclient, 0)
	machineOSConfigInformer := mcfginformers.NewSharedInformerFactory(bcc.mcfgclient, 0)

	return &informers{
		ccInformer:              ccInformer.Machineconfiguration().V1().ControllerConfigs(),
		mcpInformer:             mcpInformer.Machineconfiguration().V1().MachineConfigPools(),
		cmInformer:              cmInformer.Core().V1().ConfigMaps(),
		buildInformer:           buildInformer.Build().V1().Builds(),
		podInformer:             podInformer.Core().V1().Pods(),
		machineOSBuildInformer:  machineOSBuildInformer.Machineconfiguration().V1alpha1().MachineOSBuilds(),
		machineOSConfigInformer: machineOSConfigInformer.Machineconfiguration().V1alpha1().MachineOSConfigs(),
		toStart: []interface{ Start(<-chan struct{}) }{
			ccInformer,
			mcpInformer,
			buildInformer,
			cmInformer,
			podInformer,
			machineOSBuildInformer,
			machineOSConfigInformer,
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
		mosQueue:      workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "machineosbuilder-builds"),
		config:        ctrlConfig,
	}

	ctrl.syncHandler = ctrl.syncMachineOSBuilder

	ctrl.ccLister = ctrl.ccInformer.Lister()
	ctrl.mcpLister = ctrl.mcpInformer.Lister()
	ctrl.machineOSConfigLister = ctrl.machineOSConfigInformer.Lister()

	ctrl.machineOSBuildLister = ctrl.machineOSBuildInformer.Lister()

	ctrl.machineOSBuildInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: ctrl.updateMachineOSBuild,
		DeleteFunc: ctrl.deleteMachineOSBuild,
	})

	ctrl.machineOSConfigInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: ctrl.updateMachineOSConfig,
		AddFunc:    ctrl.addMachineOSConfig,
		DeleteFunc: ctrl.deleteMachineOSConfig,
	})

	ctrl.machineOSConfigListerSynced = ctrl.machineOSConfigInformer.Informer().HasSynced
	ctrl.machineOSBuildListerSynced = ctrl.machineOSBuildInformer.Informer().HasSynced
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
	defer ctrl.mosQueue.ShutDown()
	defer cancel()

	ctrl.informers.start(ctx)

	if !cache.WaitForCacheSync(ctx.Done(), ctrl.mcpListerSynced, ctrl.ccListerSynced) {
		return
	}

	go ctrl.imageBuilder.Run(ctx, workers)

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.mosbWorker, time.Second, ctx.Done())
	}

	<-ctx.Done()
}

func (ctrl *Controller) enqueueMachineOSConfig(mosc *mcfgv1alpha1.MachineOSConfig) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(mosc)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", mosc, err))
		return
	}
	ctrl.mosQueue.Add(key)
}

func (ctrl *Controller) enqueueMachineOSBuild(mosb *mcfgv1alpha1.MachineOSBuild) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(mosb)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", mosb, err))
		return
	}

	ctrl.mosQueue.Add(key)
}

// worker runs a worker thread that just dequeues items, processes them, and marks them done.
// It enforces that the syncHandler is never invoked concurrently with the same key.
func (ctrl *Controller) mosbWorker() {
	for ctrl.processNextMosbWorkItem() {
	}
}

func (ctrl *Controller) processNextMosbWorkItem() bool {
	key, quit := ctrl.mosQueue.Get()
	if quit {
		return false
	}
	defer ctrl.mosQueue.Done(key)

	err := ctrl.syncHandler(key.(string))
	ctrl.handleErr(err, key)

	return true
}

// we don't need this :)
// Checks for new Data in the on-cluster-build-config configmap
// if the imageBuilderType has changed, we need to restart the controller
/*
func (ctrl *Controller) updateConfigMap(old, new interface{}) {
	oldCM := old.(*corev1.ConfigMap).DeepCopy()
	newCM := new.(*corev1.ConfigMap).DeepCopy()
	if newCM.Name == OnClusterBuildConfigMapName && oldCM.Data[ImageBuilderTypeConfigMapKey] != newCM.Data[ImageBuilderTypeConfigMapKey] {
		// restart ctrl and re-init
		mosbs, _ := ctrl.machineOSBuildLister.List(labels.Everything())
		impactedPools := []*mcfgv1.MachineConfigPool{}
		for _, mcp := range mosbs {
			if running, _ := ctrl.imageBuilder.IsBuildRunning(mcp); running {
				// building on this pool, we have changed img builder type. Need to stop build
				impactedPools = append(impactedPools, mcp)
				ps := newPoolState(mcp)
				ctrl.imageBuilder.DeleteBuildObject(mcp)
				ctrl.markBuildInterrupted(ps)
			}

		}
		if ImageBuilderType(newCM.Data[ImageBuilderTypeConfigMapKey]) != OpenshiftImageBuilder && ImageBuilderType(newCM.Data[ImageBuilderTypeConfigMapKey]) != CustomPodImageBuilder {
			ctrl.handleConfigMapError(impactedPools, &ErrInvalidImageBuilder{Message: "Invalid Image Builder Type found in configmap", InvalidType: newCM.Data[ImageBuilderTypeConfigMapKey]}, new)
			os.Exit(255)
		}
		os.Exit(0)
	}
}
*/

// Reconciles the MachineConfigPool state with the state of an OpenShift Image
// Builder object.
// we need to modify this to instead use the API objects
func (ctrl *Controller) imageBuildUpdater(build *buildv1.Build, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig) error {
	var err error
	klog.Infof("Build (%s) is %s", build.Name, build.Status.Phase)

	objRef := toObjectRef(build)

	bs := ctrlcommon.NewMachineOSBuildState(mosb)

	switch build.Status.Phase {
	case buildv1.BuildPhaseNew, buildv1.BuildPhasePending:
		// Build Pending MOSB
		if !bs.IsBuildPending() {
			err = ctrl.markBuildPendingWithObjectRef(mosc, mosb, *objRef)
		}
	case buildv1.BuildPhaseRunning:
		// Building MOSB
		if !bs.IsBuilding() {
			err = ctrl.markBuildInProgress(mosb)
		}
	case buildv1.BuildPhaseComplete:
		// Built MOSB
		// we might need to create a MOSI
		if !bs.IsBuildSuccess() {
			err = ctrl.markBuildSucceeded(mosc, mosb)
		}
	case buildv1.BuildPhaseFailed, buildv1.BuildPhaseError, buildv1.BuildPhaseCancelled:
		// BuildFailed MOSB
		if !bs.IsBuildFailure() || !bs.IsBuildRestarted() {
			err = ctrl.markBuildFailed(mosc, mosb)
		}
	}

	if err != nil {
		return err
	}

	// ctrl.enqueueMachineOSBuild
	// ctrl.enqueueMachineOS ? for both build and image
	ctrl.enqueueMachineOSBuild(mosb)
	return nil
}

// Reconciles the MachineConfigPool state with the state of a custom pod object.
func (ctrl *Controller) customBuildPodUpdater(pod *corev1.Pod) error {
	pool, err := ctrl.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(context.TODO(), pod.Labels[targetMachineConfigPoolLabel], metav1.GetOptions{})
	if err != nil {
		return err
	}

	mosbs, err := ctrl.machineOSBuildLister.List(labels.Everything())
	if err != nil {
		return err
	}

	moscs, err := ctrl.machineOSConfigLister.List(labels.Everything())
	if err != nil {
		return err
	}

	var mosb *mcfgv1alpha1.MachineOSBuild
	var mosc *mcfgv1alpha1.MachineOSConfig
	for _, build := range mosbs {
		if build.Spec.MachineConfigPool.Name == pool.Name {
			mosb = build
			break
		}
	}

	for _, config := range moscs {
		if config.Spec.MachineConfigPool.Name == pool.Name {
			mosc = config
			break
		}
	}

	mosbState := ctrlcommon.NewMachineOSBuildState(mosb)

	switch pod.Status.Phase {
	case corev1.PodPending:
		if !mosbState.IsBuildPending() {
			objRef := toObjectRef(pod)
			err = ctrl.markBuildPendingWithObjectRef(mosc, mosb, *objRef)
		}
	case corev1.PodRunning:
		// If we're running, then there's nothing to do right now.
		if !mosbState.IsBuilding() {
			err = ctrl.markBuildInProgress(mosb)
		}
	case corev1.PodSucceeded:
		// If we've succeeded, we need to update the pool to indicate that.
		if !mosbState.IsBuildSuccess() {
			err = ctrl.markBuildSucceeded(mosc, mosb)
		}
	case corev1.PodFailed:
		// If we've failed, we need to update the pool to indicate that.
		if !mosbState.IsBuildFailure() {
			err = ctrl.markBuildFailed(mosc, mosb)
		}
	}

	if err != nil {
		return err
	}

	ctrl.enqueueMachineOSBuild(mosb)
	return nil
}

func (ctrl *Controller) handleConfigMapError(pools []*mcfgv1.MachineConfigPool, err error, key interface{}) {
	klog.V(2).Infof("Error syncing configmap %v: %v", key, err)
	utilruntime.HandleError(err)
	// get mosb assoc. with pool
	for _, pool := range pools {
		klog.V(2).Infof("Dropping machineconfigpool %q out of the queue: %v", pool.Name, err)
		ctrl.mosQueue.Forget(pool.Name)
		ctrl.mosQueue.AddAfter(pool.Name, 1*time.Minute)
	}

}

func (ctrl *Controller) handleErr(err error, key interface{}) {
	if err == nil {
		ctrl.mosQueue.Forget(key)
		return
	}

	if ctrl.mosQueue.NumRequeues(key) < ctrl.config.MaxRetries {
		klog.V(2).Infof("Error syncing machineosbuild %v: %v", key, err)
		ctrl.mosQueue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	klog.V(2).Infof("Dropping machineosbuild %q out of the queue: %v", key, err)
	ctrl.mosQueue.Forget(key)
	ctrl.mosQueue.AddAfter(key, 1*time.Minute)
}

func (ctrl *Controller) syncMachineOSBuilder(key string) error {
	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	isConfig := false
	var machineOSConfig *mcfgv1alpha1.MachineOSConfig
	machineosbuild, err := ctrl.machineOSBuildLister.Get(name)
	if k8serrors.IsNotFound(err) {
		// if this is not an existing build. This means our machineOsConfig changed
		isConfig = true
		machineOSConfig, err = ctrl.machineOSConfigLister.Get(name)
		if k8serrors.IsNotFound(err) {
			return nil
		}
	}

	// so if the MOSB status updates, we need to react to that and possibly requeue.
	// otherwise we should get the MCP and trigger a build if necessary
	// we still probably need the MCP function to trigger when the desired config changes.

	if !isConfig {
		for _, cond := range machineosbuild.Status.Conditions {
			if cond.Status == metav1.ConditionTrue {
				switch mcfgv1alpha1.BuildProgress(cond.Type) {
				case mcfgv1alpha1.MachineOSBuildPrepared:
					return nil
				case mcfgv1alpha1.MachineOSBuilding:
					//
					return nil
				case mcfgv1alpha1.MachineOSBuildFailed:
					//
					return nil
				case mcfgv1alpha1.MachineOSBuildInterrupted:
					//
					ctrl.enqueueMachineOSBuild(machineosbuild)
				case mcfgv1alpha1.MachineOSBuildRestarted:
					//
					ctrl.enqueueMachineOSBuild(machineosbuild)

				case mcfgv1alpha1.MachineOSReady:
					//
					return nil
				default:
					// should we build? we can determine this by getting the MCP associated with this obj. However,
					// this obj will not update each time the mcp does need to figure out how to reconcile that.
					machineOSConfigs, err := ctrl.machineOSConfigLister.List(labels.Everything())

					var ourConfig *mcfgv1alpha1.MachineOSConfig
					for _, c := range machineOSConfigs {
						if c.Spec.MachineConfigPool.Name == machineosbuild.Spec.MachineConfigPool.Name {
							ourConfig = c
							break
						}
					}

					doABuild, err := shouldWeDoABuild(ctrl.imageBuilder, ourConfig, machineosbuild, machineosbuild)
					if err != nil {
						return err
					}
					if doABuild {
						ctrl.startBuildForMachineConfigPool(machineOSConfig, machineosbuild)
					}

				}

			}
		}
	} else {
		// this is a config change or a config CREATION. We need to possibly make a mosb for this build. The updated config is handlded in the updateMachineOSConfig function
		//	if ctrl.imageBuilder.
		var buildExists bool
		var status *mcfgv1alpha1.MachineOSBuildStatus
		machineosbuild, buildExists = ctrl.doesMOSBExist(machineOSConfig)
		if !buildExists {
			machineosbuild, status, err = ctrl.CreateBuildFromConfig(machineOSConfig)
			if err != nil {
				return err
			}
			machineosbuild.Status = *status

			//	machineosbuild, err = ctrl.mcfgclient.MachineconfigurationV1alpha1().MachineOSBuilds().UpdateStatus(context.TODO(), machineosbuild, metav1.UpdateOptions{})
			//if err != nil {
			//	return err
			//}
			// now we have mosb, and must also trigger a build.
			ctrl.startBuildForMachineConfigPool(machineOSConfig, machineosbuild)
		}
	}

	return ctrl.syncAvailableStatus(machineosbuild)
}

func (ctrl *Controller) markBuildInterrupted(mosc *mcfgv1alpha1.MachineOSConfig, mosb *mcfgv1alpha1.MachineOSBuild) error {
	klog.Errorf("Build interrupted for pool %s", mosb.Spec.MachineConfigPool.Name)

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {

		bs := ctrlcommon.NewMachineOSBuildState(mosb)
		bs.SetBuildConditions([]metav1.Condition{
			{
				Type:   string(mcfgv1alpha1.MachineOSBuildInterrupted),
				Reason: "BuildInterrupted",
				Status: metav1.ConditionTrue,
			},
			{
				Type:   string(mcfgv1alpha1.MachineOSReady),
				Status: metav1.ConditionFalse,
			},
			{
				Type:   string(mcfgv1alpha1.MachineOSBuilding),
				Status: metav1.ConditionFalse,
			},
			{
				Type:   string(mcfgv1alpha1.MachineOSBuildRestarted),
				Status: metav1.ConditionFalse, // I think?
			},
			/*
				{
					Type:   mcfgv1.MachineConfigPoolBuildPending,
					Status: metav1.ConditionFalse,
				},
				{
					Type:   mcfgv1.MachineConfigPoolDegraded,
					Status: metav1.ConditionTrue,
				},
			*/
		})

		//ps.pool.Spec.Configuration.Source = ps.pool.Spec.Configuration.Source[:len(ps.pool.Spec.Configuration.Source)-1]

		// update mosc status
		status := mosc.Status.DeepCopy()

		if status.BuildHistory == nil {
			status.BuildHistory = []mcfgv1alpha1.BuildHistory{}
		}
		for _, build := range status.BuildHistory {
			if build.Name == mosb.Status.BuildName {
				build.BuildFailure = string(mcfgv1alpha1.MachineOSBuildInterrupted)
			}
		}

		return ctrl.updateMOSCAndSyncAvailable(mosc, bs.Build)
	})

}

// Marks a given MachineConfigPool as a failed build.
func (ctrl *Controller) markBuildFailed(mosc *mcfgv1alpha1.MachineOSConfig, mosb *mcfgv1alpha1.MachineOSBuild) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {

		bs := ctrlcommon.NewMachineOSBuildState(mosb)
		bs.SetBuildConditions([]metav1.Condition{
			{
				Type:    string(mcfgv1alpha1.MachineOSBuildInterrupted),
				Status:  metav1.ConditionFalse,
				Reason:  "Interrupted",
				Message: "MOSB Failed",
			},
			{
				Type:    string(mcfgv1alpha1.MachineOSBuildFailed),
				Reason:  "BuildFailed",
				Status:  metav1.ConditionTrue,
				Message: "MOSB Failed",
			},
			{
				Type:    string(mcfgv1alpha1.MachineOSReady),
				Status:  metav1.ConditionFalse,
				Reason:  "Ready",
				Message: "MOSB Failed",
			},
			{
				Type:    string(mcfgv1alpha1.MachineOSBuilding),
				Status:  metav1.ConditionFalse,
				Reason:  "Building",
				Message: "MOSB Failed",
			},
			/*
				{
					Type:   mcfgv1.MachineConfigPoolBuildPending,
					Status: metav1.ConditionFalse,
				},
			*/
			{
				Type:   string(mcfgv1alpha1.MachineOSBuildRestarted),
				Status: metav1.ConditionFalse,
			},
		})

		status := mosc.Status.DeepCopy()

		if status.BuildHistory == nil {
			status.BuildHistory = []mcfgv1alpha1.BuildHistory{}
		}
		for _, build := range status.BuildHistory {
			if build.Name == mosb.Status.BuildName {
				build.BuildFailure = string(mcfgv1alpha1.MachineOSBuildInterrupted)
			}
		}

		return ctrl.updateMOSCAndSyncFailing(mosc, bs.Build)
	})

}

// Marks a given MachineConfigPool as the build is in progress.
func (ctrl *Controller) markBuildInProgress(mosb *mcfgv1alpha1.MachineOSBuild) error {
	klog.Infof("Build in progress for MachineConfigPool %s, config %s", mosb.Spec.MachineConfigPool.Name, mosb.Status.DesiredConfig.Name)

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {

		bs := ctrlcommon.NewMachineOSBuildState(mosb)

		bs.SetBuildConditions([]metav1.Condition{
			{
				Type:    string(mcfgv1alpha1.MachineOSBuildInterrupted),
				Status:  metav1.ConditionFalse,
				Reason:  "Interrupted",
				Message: "MOSB Available",
			},
			{
				Type:    string(mcfgv1alpha1.MachineOSBuildFailed),
				Status:  metav1.ConditionFalse,
				Reason:  "Failed",
				Message: "MOSB Available",
			},
			{
				Type:    string(mcfgv1alpha1.MachineOSReady),
				Status:  metav1.ConditionFalse,
				Reason:  "Ready",
				Message: "MOSB Available",
			},
			{
				Type:    string(mcfgv1alpha1.MachineOSBuilding),
				Reason:  "BuildRunning",
				Status:  metav1.ConditionTrue,
				Message: "Image Build In Progress",
			},
			/*
				{
					Type:   mcfgv1.MachineConfigPoolBuildPending,
					Status: metav1.ConditionFalse},
			*/
		})

		return ctrl.syncAvailableStatus(mosb)
	})
}

// Deletes the ephemeral objects we created to perform this specific build.
func (ctrl *Controller) postBuildCleanup(mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig, ignoreMissing bool) error {
	// Delete the actual build object itself.
	deleteBuildObject := func() error {
		err := ctrl.imageBuilder.DeleteBuildObject(mosb, mosc)

		if err == nil {
			klog.Infof("Deleted build object %s", newImageBuildRequest(mosc, mosb).getBuildName())
		}

		return err
	}

	// Delete the ConfigMap containing the MachineConfig.
	deleteMCConfigMap := func() error {
		ibr := newImageBuildRequest(mosc, mosb)

		err := ctrl.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Delete(context.TODO(), ibr.getMCConfigMapName(), metav1.DeleteOptions{})

		if err == nil {
			klog.Infof("Deleted MachineConfig ConfigMap %s for build %s", ibr.getMCConfigMapName(), ibr.getBuildName())
		}

		return err
	}

	// Delete the ConfigMap containing the rendered Dockerfile.
	deleteDockerfileConfigMap := func() error {
		ibr := newImageBuildRequest(mosc, mosb)

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
func (ctrl *Controller) markBuildSucceeded(mosc *mcfgv1alpha1.MachineOSConfig, mosb *mcfgv1alpha1.MachineOSBuild) error {
	// Perform the MachineConfigPool update.

	// we might need to wire up a way for the pool to be updated when the update is complete...
	// or. We can say if build succeded in the mosb and desired == the url, then `IsDoneAt`==true
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {

		// need to do the below with the mosb
		/*
			ps := newPoolState(mcp)

			// Set the annotation or field to point to the newly-built container image.
			klog.V(4).Infof("Setting new image pullspec for %s to %s", ps.Name(), imagePullspec)
			ps.SetImagePullspec(imagePullspec)

			// Remove the build object reference from the MachineConfigPool since we're
			// not using it anymore.
			ps.DeleteBuildRefForCurrentMachineConfig()
		*/
		// Adjust the MachineConfigPool status to indicate success.
		bs := ctrlcommon.NewMachineOSBuildState(mosb)

		bs.SetBuildConditions([]metav1.Condition{
			{
				Type:   string(mcfgv1alpha1.MachineOSBuildFailed),
				Status: metav1.ConditionFalse,
			},
			{
				Type:   string(mcfgv1alpha1.MachineOSReady),
				Reason: "BuildSucceeded",
				Status: metav1.ConditionTrue,
			},
			{
				Type:   string(mcfgv1alpha1.MachineOSBuilding),
				Status: metav1.ConditionFalse,
			},
			{
				Type:   string(mcfgv1.MachineConfigPoolDegraded),
				Status: metav1.ConditionFalse,
			},
		})

		// update mosc status
		status := mosc.Status.DeepCopy()

		if status.BuildHistory == nil {
			status.BuildHistory = []mcfgv1alpha1.BuildHistory{}
		}
		for _, build := range status.BuildHistory {
			if build.Name == mosb.Status.BuildName {
				build.BuildFailure = string(mcfgv1alpha1.MachineOSReady)
			}
		}
		return ctrl.updateMOSCAndSyncAvailable(mosc, bs.Build)

		// don't SetImagPullSpec. It is already in MOSB. Wherever we check for desiredImage or query the Pool's annotation
		// we need to replace with mosb
	})
}

// Marks a given MachineConfigPool as build pending.
func (ctrl *Controller) markBuildPendingWithObjectRef(mosc *mcfgv1alpha1.MachineOSConfig, mosb *mcfgv1alpha1.MachineOSBuild, objRef corev1.ObjectReference) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		bs := ctrlcommon.NewMachineOSBuildState(mosb)

		bs.SetBuildConditions([]metav1.Condition{
			{
				Type:   string(mcfgv1alpha1.MachineOSBuildInterrupted),
				Status: metav1.ConditionFalse,
			},
			{
				Type:   string(mcfgv1.MachineConfigPoolBuildFailed),
				Status: metav1.ConditionFalse,
			},
			{
				Type:   string(mcfgv1alpha1.MachineOSReady),
				Status: metav1.ConditionFalse,
			},
			// what is the difference between building and build pending?
			{
				Type:   string(mcfgv1alpha1.MachineOSBuilding),
				Reason: "BuildPending",
				Status: metav1.ConditionTrue,
			},
			/*
				{
					Type:   mcfgv1.MachineConfigPoolBuildPending,
					Status: metav1.ConditionTrue,
				},
			*/
		})

		/*
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

		*/

		// add obj ref to mosc
		status := mosc.Status.DeepCopy()

		if status.BuildHistory == nil {
			status.BuildHistory = []mcfgv1alpha1.BuildHistory{}
		}
		status.BuildHistory = append(status.BuildHistory, mcfgv1alpha1.BuildHistory{
			Name: objRef.Name,
		})
		return ctrl.updateConfigAndBuild(mosc, bs.Build)

	})
}

func (ctrl *Controller) updateMOSCAndSyncFailing(mosc *mcfgv1alpha1.MachineOSConfig, mosb *mcfgv1alpha1.MachineOSBuild) error {
	// We need to do an API server round-trip to ensure all of our mutations get
	// propagated.
	_, err := ctrl.mcfgclient.MachineconfigurationV1alpha1().MachineOSConfigs().UpdateStatus(context.TODO(), mosc, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("could not update MachineOSBuild %q: %w", mosb.Name, err)
	}

	return ctrl.syncFailingStatus(mosb, fmt.Errorf("build failed"))
}

func (ctrl *Controller) updateConfigAndBuild(mosc *mcfgv1alpha1.MachineOSConfig, mosb *mcfgv1alpha1.MachineOSBuild) error {
	_, err := ctrl.mcfgclient.MachineconfigurationV1alpha1().MachineOSConfigs().UpdateStatus(context.TODO(), mosc, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("could not update MachineOSBuild %q: %w", mosb.Name, err)
	}
	newMosb, err := ctrl.mcfgclient.MachineconfigurationV1alpha1().MachineOSBuilds().Update(context.TODO(), mosb, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("could not update MachineOSBuild %q: %w", mosb.Name, err)
	}

	newMosb.Status = mosb.Status

	return ctrl.syncAvailableStatus(newMosb)
}

func (ctrl *Controller) updateMOSCAndSyncAvailable(mosc *mcfgv1alpha1.MachineOSConfig, mosb *mcfgv1alpha1.MachineOSBuild) error {
	// We need to do an API server round-trip to ensure all of our mutations get
	// propagated.
	_, err := ctrl.mcfgclient.MachineconfigurationV1alpha1().MachineOSConfigs().UpdateStatus(context.TODO(), mosc, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("could not update MachineOSBuild %q: %w", mosb.Name, err)
	}

	return ctrl.syncAvailableStatus(mosb)
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
func (ctrl *Controller) prepareForBuild(mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig) (ImageBuildRequest, error) {
	ibr := newImageBuildRequestFromBuildInputs(mosb, mosc)

	// populate the "optional" fields, if the user did not specify them
	osImageURL, err := ctrl.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(context.TODO(), machineConfigOSImageURLConfigMapName, metav1.GetOptions{})
	if err != nil {
		return ibr, fmt.Errorf("could not get OS image URL: %w", err)
	}
	moscNew := mosc.DeepCopy()

	url := newExtensionsImageInfo(osImageURL, mosc)
	if len(moscNew.Spec.BuildInputs.BaseOSExtensionsImagePullspec) == 0 {
		moscNew.Spec.BuildInputs.BaseOSExtensionsImagePullspec = url.Pullspec
	}
	url = newBaseImageInfo(osImageURL, mosc)
	if len(moscNew.Spec.BuildInputs.BaseOSImagePullspec) == 0 {
		moscNew.Spec.BuildInputs.BaseOSImagePullspec = url.Pullspec
	}

	ctrl.mcfgclient.MachineconfigurationV1alpha1().MachineOSConfigs().Update(context.TODO(), moscNew, metav1.UpdateOptions{})

	mc, err := ctrl.mcfgclient.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), mosb.Status.DesiredConfig.Name, metav1.GetOptions{})
	if err != nil {
		return ibr, err
	}

	mcConfigMap, err := ibr.toConfigMap(mc) // ??????
	if err != nil {
		return ImageBuildRequest{}, fmt.Errorf("could not convert MachineConfig %s into ConfigMap: %w", mosb.Status.DesiredConfig.Name, err) // ????
	}

	_, err = ctrl.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Create(context.TODO(), mcConfigMap, metav1.CreateOptions{})
	if err != nil {
		return ImageBuildRequest{}, fmt.Errorf("could not load rendered MachineConfig %s into configmap: %w", mcConfigMap.Name, err)
	}

	klog.Infof("Stored MachineConfig %s in ConfigMap %s for build", mosb.Status.DesiredConfig.Name, mcConfigMap.Name)

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
func (ctrl *Controller) startBuildForMachineConfigPool(mosc *mcfgv1alpha1.MachineOSConfig, mosb *mcfgv1alpha1.MachineOSBuild) error {

	// we need to add osImageURL to mosbuild, will reduce api calls to configmaps
	// ocb config will live in th mosb
	// pool will live in the mosb
	// mc we can get based off the pool specified in the mosb.... though, given how we could use this in two places

	machineOSConfigs, err := ctrl.machineOSConfigLister.List(labels.Everything())

	var ourConfig *mcfgv1alpha1.MachineOSConfig
	for _, c := range machineOSConfigs {
		if c.Spec.MachineConfigPool.Name == mosb.Spec.MachineConfigPool.Name {
			ourConfig = c
			break
		}
	}
	ibr, err := ctrl.prepareForBuild(mosb, ourConfig)
	if err != nil {
		return fmt.Errorf("could not start build for MachineConfigPool %s: %w", mosb.Spec.MachineConfigPool.Name, err)
	}

	objRef, err := ctrl.imageBuilder.StartBuild(ibr)

	if err != nil {
		return err
	}

	return ctrl.markBuildPendingWithObjectRef(mosc, mosb, *objRef)
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

func (ctrl *Controller) addMachineOSConfig(cur interface{}) {
	m := cur.(*mcfgv1alpha1.MachineOSConfig).DeepCopy()
	ctrl.enqueueMachineOSConfig(m)
	klog.V(4).Infof("Adding MachineOSConfig %s", m.Name)

}

func (ctrl *Controller) updateMachineOSConfig(old, cur interface{}) {
	oldMOSC := old.(*mcfgv1alpha1.MachineOSConfig).DeepCopy()
	curMOSC := cur.(*mcfgv1alpha1.MachineOSConfig).DeepCopy()

	klog.Infof("Updating MachineOSConfig %s", oldMOSC.Name)

	doABuild := configChangeCauseBuild(oldMOSC, curMOSC)
	if doABuild {
		build, exists := ctrl.doesMOSBExist(curMOSC)
		if exists {
			ctrl.startBuildForMachineConfigPool(curMOSC, build) // ?
		}
		// if the mosb does not exist, lets just enqueue the mosc and let the sync handler take care of the new object creation
	}
	ctrl.enqueueMachineOSConfig(curMOSC)
}

func (ctrl *Controller) deleteMachineOSConfig(cur interface{}) {
	m, ok := cur.(*mcfgv1alpha1.MachineOSConfig)
	// first, we need to stop and delete any existing builds.
	mosb, err := ctrl.machineOSBuildLister.Get(fmt.Sprintf("%s-builder", m.Spec.MachineConfigPool.Name))
	if err == nil {
		if running, _ := ctrl.imageBuilder.IsBuildRunning(mosb, m); running {
			// we need to stop the build.
			ctrl.imageBuilder.DeleteBuildObject(mosb, m)
			ctrl.markBuildInterrupted(m, mosb)
		}
		ctrl.mcfgclient.MachineconfigurationV1alpha1().MachineOSBuilds().Delete(context.TODO(), mosb.Name, metav1.DeleteOptions{})
	}
	if !ok {
		tombstone, ok := cur.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Couldn't get object from tombstone %#v", cur))
			return
		}
		m, ok = tombstone.Obj.(*mcfgv1alpha1.MachineOSConfig)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Tombstone contained object that is not a MachineOSConfig %#v", cur))
			return
		}
	}
	klog.V(4).Infof("Deleting MachineOSBuild %s", m.Name)
}

func (ctrl *Controller) updateMachineOSBuild(old, cur interface{}) {
	oldMOSB := old.(*mcfgv1alpha1.MachineOSBuild).DeepCopy()
	curMOSB := cur.(*mcfgv1alpha1.MachineOSBuild).DeepCopy()

	klog.Infof("Updating MachineOSBuild %s", oldMOSB.Name)
	machineOSConfigs, err := ctrl.machineOSConfigLister.List(labels.Everything())

	var ourConfig *mcfgv1alpha1.MachineOSConfig
	for _, c := range machineOSConfigs {
		if c.Spec.MachineConfigPool.Name == curMOSB.Spec.MachineConfigPool.Name {
			ourConfig = c
			break
		}
	}

	doABuild, err := shouldWeDoABuild(ctrl.imageBuilder, ourConfig, oldMOSB, curMOSB)
	if err != nil {
		return
	}
	if doABuild {
		ctrl.startBuildForMachineConfigPool(ourConfig, curMOSB)
	}
	ctrl.enqueueMachineOSBuild(curMOSB)
}

func (ctrl *Controller) deleteMachineOSBuild(mosb interface{}) {
	m, ok := mosb.(*mcfgv1alpha1.MachineOSBuild)
	if !ok {
		tombstone, ok := mosb.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Couldn't get object from tombstone %#v", mosb))
			return
		}
		m, ok = tombstone.Obj.(*mcfgv1alpha1.MachineOSBuild)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Tombstone contained object that is not a MachineOSBuild %#v", mosb))
			return
		}
	}
	klog.V(4).Infof("Deleting MachineOSBuild %s", m.Name)
}

func (ctrl *Controller) syncAvailableStatus(mosb *mcfgv1alpha1.MachineOSBuild) error {
	// I'm not sure what the consequences are of not doing this.
	//nolint:gocritic // Leaving this here for review purposes.
	/*
		if apihelpers.IsMachineConfigPoolConditionFalse(pool.Status.Conditions, mcfgv1.MachineConfigPoolRenderDegraded) {
			return nil
		}
	*/
	sdegraded := apihelpers.NewMachineOSBuildCondition(string(mcfgv1alpha1.MachineOSBuildFailed), metav1.ConditionFalse, "MOSCAvailable", "MOSC")
	apihelpers.SetMachineOSBuildCondition(&mosb.Status, *sdegraded)

	if _, err := ctrl.mcfgclient.MachineconfigurationV1alpha1().MachineOSBuilds().UpdateStatus(context.TODO(), mosb, metav1.UpdateOptions{}); err != nil {
		return err
	}

	return nil
}

func (ctrl *Controller) syncFailingStatus(mosb *mcfgv1alpha1.MachineOSBuild, err error) error {
	sdegraded := apihelpers.NewMachineOSBuildCondition(string(mcfgv1alpha1.MachineOSBuildFailed), metav1.ConditionTrue, "BuildFailed", fmt.Sprintf("Failed to build configuration for pool %s: %v", mosb.Spec.MachineConfigPool.Name, err))
	apihelpers.SetMachineOSBuildCondition(&mosb.Status, *sdegraded)
	if _, updateErr := ctrl.mcfgclient.MachineconfigurationV1alpha1().MachineOSBuilds().UpdateStatus(context.TODO(), mosb, metav1.UpdateOptions{}); updateErr != nil {
		klog.Errorf("Error updating MachineOSBuild %s: %v", mosb.Name, updateErr)
	}
	return err
}

// Determine if we have a config change.
func isPoolConfigChange(oldPool, curPool *mcfgv1.MachineConfigPool) bool {
	return oldPool.Spec.Configuration.Name != curPool.Spec.Configuration.Name
}

func configChangeCauseBuild(old, cur *mcfgv1alpha1.MachineOSConfig) bool {
	return equality.Semantic.DeepEqual(old.Spec.BuildInputs, cur.Spec.BuildInputs)
}

// Determines if we should do a build based upon the state of our
// MachineConfigPool, the presence of a build pod, etc.
func shouldWeDoABuild(builder interface {
	IsBuildRunning(*mcfgv1alpha1.MachineOSBuild, *mcfgv1alpha1.MachineOSConfig) (bool, error)
}, mosc *mcfgv1alpha1.MachineOSConfig, oldMOSB, curMOSB *mcfgv1alpha1.MachineOSBuild) (bool, error) {
	// get desired and current. If desired != current,
	// assume we are doing a build. remove the whole layered pool annotation workflow

	if oldMOSB.Status.DesiredConfig != curMOSB.Status.DesiredConfig {
		// the desiredConfig changed. We need to do an update
		// but check that there isn't already a build.
		// If a build is found running, we should not do a build.
		isRunning, err := builder.IsBuildRunning(curMOSB, mosc)

		return !isRunning, err

		// check for image pull sped changing?
	}
	return false, nil
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

func (ctrl *Controller) doesMOSBExist(config *mcfgv1alpha1.MachineOSConfig) (*mcfgv1alpha1.MachineOSBuild, bool) {
	mosb, err := ctrl.machineOSBuildLister.Get(fmt.Sprintf("%s-builder", config.Spec.MachineConfigPool.Name))
	if err != nil && k8serrors.IsNotFound(err) {
		return nil, false
	} else if mosb != nil {
		return mosb, true
	}
	return nil, false
}

func (ctrl *Controller) CreateBuildFromConfig(config *mcfgv1alpha1.MachineOSConfig) (*mcfgv1alpha1.MachineOSBuild, *mcfgv1alpha1.MachineOSBuildStatus, error) {
	mcp, err := ctrl.mcpLister.Get(config.Spec.MachineConfigPool.Name)
	if err != nil {
		return nil, nil, err
	}
	now := metav1.Now()
	build := mcfgv1alpha1.MachineOSBuild{
		TypeMeta: metav1.TypeMeta{
			Kind:       "MachineOSBuild",
			APIVersion: "machineconfiguration.openshift.io/v1alpha1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-builder", config.Spec.MachineConfigPool.Name),
		},
		Spec: mcfgv1alpha1.MachineOSBuildSpec{
			MachineConfigPool: mcfgv1alpha1.MachineConfigPoolReference{
				Name: config.Spec.MachineConfigPool.Name,
			},
		},
		Status: mcfgv1alpha1.MachineOSBuildStatus{
			DesiredConfig: mcfgv1alpha1.RenderedMachineConfigReference{
				Name: mcp.Spec.Configuration.Name,
			},
			BuildStart: &now,
			BuildEnd:   &now,
		},
	}
	mosb, err := ctrl.mcfgclient.MachineconfigurationV1alpha1().MachineOSBuilds().Create(context.TODO(), &build, metav1.CreateOptions{})
	return mosb, &build.Status, err
}
