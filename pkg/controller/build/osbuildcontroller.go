package build

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	imagev1clientset "github.com/openshift/client-go/image/clientset/versioned"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/scheme"
	routeclientset "github.com/openshift/client-go/route/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientset "k8s.io/client-go/kubernetes"
	coreclientsetv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
)

type OSBuildController struct {
	*listers
	*informers

	eventRecorder record.EventRecorder
	mcfgclient    mcfgclientset.Interface
	kubeclient    clientset.Interface
	imageclient   imagev1clientset.Interface
	routeclient   routeclientset.Interface

	config    Config
	execQueue *ctrlcommon.WrappedQueue
	ctx       context.Context

	buildReconciler reconciler

	// Handles determining whether a delay is needed for controller shut down and
	// polls until pending objects, if any, are deleted or until the context it
	// is invoked with is canceled.
	shutdownDelayHandler *shutdownDelayHandler

	// This channel is primarily used for testing purposes to ensure that
	shutdownChan chan struct{}
}

type Config struct {
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

	MaxShutdownDelay     time.Duration
	ShutdownPollInterval time.Duration
}

// Creates a Config with sensible production defaults.
func defaultConfig() Config {
	return Config{
		MaxRetries:           5,
		UpdateDelay:          time.Second * 5,
		MaxShutdownDelay:     time.Second * 10,
		ShutdownPollInterval: time.Millisecond * 100,
	}
}

func NewOSBuildControllerFromControllerContext(ctrlCtx *ctrlcommon.ControllerContext) *OSBuildController {
	return NewOSBuildControllerFromControllerContextWithConfig(ctrlCtx, defaultConfig())
}

func NewOSBuildControllerFromControllerContextWithConfig(ctrlCtx *ctrlcommon.ControllerContext, cfg Config) *OSBuildController {
	return newOSBuildController(
		cfg,
		ctrlCtx.ClientBuilder.MachineConfigClientOrDie("machine-os-builder"),
		ctrlCtx.ClientBuilder.KubeClientOrDie("machine-os-builder"),
		ctrlCtx.ClientBuilder.ImageClientOrDie("machine-os-builder"),
		ctrlCtx.ClientBuilder.RouteClientOrDie("machine-os-builder"),
	)
}

func newOSBuildController(
	ctrlConfig Config,
	mcfgclient mcfgclientset.Interface,
	kubeclient clientset.Interface,
	imageclient imagev1clientset.Interface,
	routeclient routeclientset.Interface,
) *OSBuildController {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&coreclientsetv1.EventSinkImpl{Interface: kubeclient.CoreV1().Events("")})

	informers := newInformers(mcfgclient, kubeclient)

	ctrl := &OSBuildController{
		kubeclient:    kubeclient,
		mcfgclient:    mcfgclient,
		imageclient:   imageclient,
		routeclient:   routeclient,
		informers:     informers,
		listers:       informers.listers(),
		eventRecorder: eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineosbuilder"}),
		execQueue: ctrlcommon.NewWrappedQueueWithOpts(ctrlcommon.WrappedQueueOpts{
			Name:       "machineosbuilder",
			MaxRetries: ctrlConfig.MaxRetries,
			RetryAfter: ctrlConfig.UpdateDelay,
		}),
		config: ctrlConfig,
	}

	ctrl.machineOSBuildInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addMachineOSBuild,
		DeleteFunc: ctrl.deleteMachineOSBuild,
		UpdateFunc: ctrl.updateMachineOSBuild,
	})

	ctrl.machineOSConfigInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: ctrl.updateMachineOSConfig,
		AddFunc:    ctrl.addMachineOSConfig,
		DeleteFunc: ctrl.deleteMachineOSConfig,
	})

	ctrl.jobInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addJob,
		UpdateFunc: ctrl.updateJob,
		DeleteFunc: ctrl.deleteJob,
	})

	ctrl.machineConfigPoolInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addMachineConfigPool,
		UpdateFunc: ctrl.updateMachineConfigPool,
	})

	ctrl.buildReconciler = newBuildReconciler(mcfgclient, kubeclient, imageclient, routeclient, ctrl.listers)
	ctrl.shutdownDelayHandler = newShutdownDelayHandler(ctrl.listers)
	ctrl.shutdownChan = make(chan struct{})

	return ctrl
}

func (ctrl *OSBuildController) shutdownController() {
	// Time box the controller shutdown delay process to ten seconds. This is
	// approximately how long it takes the kubelet to send a SIGKILL to a
	// process that it has already sent a SIGTERM to.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), ctrl.config.MaxShutdownDelay)
	defer shutdownCancel()

	klog.Infof("Determining if a shutdown delay up to %s is required", ctrl.config.MaxShutdownDelay)

	if err := ctrl.shutdownDelayHandler.handleShutdown(shutdownCtx, ctrl.config.ShutdownPollInterval); err != nil {
		klog.Warningf("Error occurred during graceful shutdown: %s. Some objects may be orphaned as a result.", err)
	}

	// Shut down the work queue.
	ctrl.execQueue.ShutDown()

	// Handle any crashes.
	utilruntime.HandleCrash()

	klog.Infof("OSBuildController has shut down")
	close(ctrl.shutdownChan)
}

// Returns a channel that can be used to determine whether the controller
// shutdown is complete.
func (ctrl *OSBuildController) ShutdownChan() <-chan struct{} {
	return ctrl.shutdownChan
}

func (ctrl *OSBuildController) Run(parentCtx context.Context, workers int) {
	klog.Infof("Starting OSBuildController")

	// The main controller context. We purposely do not inherit from the parent
	// context because we need to control the shutdown process.
	ctrlCtx, ctrlCancel := context.WithCancel(context.Background())
	defer func() {
		klog.Infof("Shutting down OSBuildController")
		ctrl.shutdownController()
		ctrlCancel()
	}()

	ctrl.ctx = ctrlCtx

	ctrl.informers.start(ctrlCtx)

	if !cache.WaitForCacheSync(ctrlCtx.Done(), ctrl.hasSynced...) {
		return
	}

	ctrl.execQueue.Start(ctrlCtx, workers)

	<-parentCtx.Done()
}

type kubeObject interface {
	k8sruntime.Object
	GetName() string
}

type enqueuedFuncInfo struct {
	kubeObject kubeObject
	funcName   string
}

func (e *enqueuedFuncInfo) getFuncName() string {
	split := strings.Split(e.funcName, ".")
	splitLen := len(split)
	return strings.Join(split[splitLen-2:splitLen], ".")
}

func (e *enqueuedFuncInfo) String() string {
	kind, err := utils.GetKindForObject(e.kubeObject)
	if err != nil && kind == "" {
		kind = "<unknown object kind>"
	}

	name := e.kubeObject.GetName()

	if e.funcName == "" {
		return fmt.Sprintf("<kind: %q, name: %q>", kind, name)
	}

	return fmt.Sprintf("<kind: %q, name: %q, func: %q>", kind, name, e.getFuncName())
}

func (ctrl *OSBuildController) enqueueFuncForObject(obj kubeObject, toRun func(context.Context) error) {
	info := &enqueuedFuncInfo{
		kubeObject: obj,
	}

	// Add the callers name to the id so that if / when something is dropped out
	// of the queue, it's more obvious what was happening at the time.
	pc, _, _, ok := runtime.Caller(1)
	details := runtime.FuncForPC(pc)
	if ok && details != nil {
		info.funcName = details.Name()
	}

	ctrl.execQueue.EnqueueAfterWithNameAndContext(ctrl.ctx, info.String(), ctrl.config.UpdateDelay, func(ctx context.Context) error {
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()

		err := toRun(cctx)
		if err == nil {
			return nil
		}

		// If the parent context is canceled, we cannot retry the current
		// operation. Including ErrDropFromQueue is probably unnecessary, but doing
		// so should be fine.
		if ctrl.ctx.Err() != nil {
			klog.Warning("Parent context canceled, additional retries will not be attempted")
			return errors.Join(err, ctrlcommon.ErrDropFromQueue)
		}

		return err
	})
}

func (ctrl *OSBuildController) addMachineOSBuild(cur interface{}) {
	mosb := cur.(*mcfgv1.MachineOSBuild)
	ctrl.enqueueFuncForObject(mosb, func(ctx context.Context) error {
		return ctrl.buildReconciler.AddMachineOSBuild(ctx, mosb)
	})
}

func (ctrl *OSBuildController) updateMachineOSBuild(old, cur interface{}) {
	oldMOSB := old.(*mcfgv1.MachineOSBuild)
	curMOSB := cur.(*mcfgv1.MachineOSBuild)
	ctrl.enqueueFuncForObject(curMOSB, func(ctx context.Context) error {
		return ctrl.buildReconciler.UpdateMachineOSBuild(ctx, oldMOSB, curMOSB)
	})
}

func (ctrl *OSBuildController) deleteMachineOSBuild(cur interface{}) {
	mosb := cur.(*mcfgv1.MachineOSBuild)
	ctrl.enqueueFuncForObject(mosb, func(ctx context.Context) error {
		return ctrl.buildReconciler.DeleteMachineOSBuild(ctx, mosb)
	})
}

func (ctrl *OSBuildController) addJob(cur interface{}) {
	job := cur.(*batchv1.Job)
	ctrl.enqueueFuncForObject(job, func(ctx context.Context) error {
		return ctrl.buildReconciler.AddJob(ctx, job)
	})
}

func (ctrl *OSBuildController) updateJob(old, cur interface{}) {
	oldJob := old.(*batchv1.Job)
	curJob := cur.(*batchv1.Job)

	ctrl.enqueueFuncForObject(curJob, func(ctx context.Context) error {
		return ctrl.buildReconciler.UpdateJob(ctx, oldJob, curJob)
	})
}

func (ctrl *OSBuildController) deleteJob(cur interface{}) {
	job := cur.(*batchv1.Job)
	ctrl.enqueueFuncForObject(job, func(ctx context.Context) error {
		return ctrl.buildReconciler.DeleteJob(ctx, job)
	})
}

func (ctrl *OSBuildController) addMachineOSConfig(newMOSC interface{}) {
	m := newMOSC.(*mcfgv1.MachineOSConfig).DeepCopy()
	ctrl.enqueueFuncForObject(m, func(ctx context.Context) error {
		return ctrl.buildReconciler.AddMachineOSConfig(ctx, m)
	})
}

func (ctrl *OSBuildController) updateMachineOSConfig(old, cur interface{}) {
	oldMOSC := old.(*mcfgv1.MachineOSConfig).DeepCopy()
	curMOSC := cur.(*mcfgv1.MachineOSConfig).DeepCopy()
	ctrl.enqueueFuncForObject(curMOSC, func(ctx context.Context) error {
		return ctrl.buildReconciler.UpdateMachineOSConfig(ctx, oldMOSC, curMOSC)
	})
}

func (ctrl *OSBuildController) deleteMachineOSConfig(cur interface{}) {
	mosc, ok := cur.(*mcfgv1.MachineOSConfig)
	if !ok {
		tombstone, ok := cur.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Couldn't get object from tombstone %#v", cur))
			return
		}
		mosc, ok = tombstone.Obj.(*mcfgv1.MachineOSConfig)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Tombstone contained object that is not a MachineOSConfig %#v", cur))
			return
		}
	}

	ctrl.enqueueFuncForObject(mosc, func(ctx context.Context) error {
		return ctrl.buildReconciler.DeleteMachineOSConfig(ctx, mosc)
	})
}

func (ctrl *OSBuildController) addMachineConfigPool(newMCP interface{}) {
	mcp := newMCP.(*mcfgv1.MachineConfigPool).DeepCopy()
	ctrl.enqueueFuncForObject(mcp, func(ctx context.Context) error {
		return ctrl.buildReconciler.AddMachineConfigPool(ctx, mcp)
	})
}

func (ctrl *OSBuildController) updateMachineConfigPool(old, cur interface{}) {
	oldMCP := old.(*mcfgv1.MachineConfigPool).DeepCopy()
	curMCP := cur.(*mcfgv1.MachineConfigPool).DeepCopy()

	ctrl.enqueueFuncForObject(curMCP, func(ctx context.Context) error {
		return ctrl.buildReconciler.UpdateMachineConfigPool(ctx, oldMCP, curMCP)
	})
}
