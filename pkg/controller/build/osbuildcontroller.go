package build

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/scheme"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
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

	config    Config
	execQueue *ctrlcommon.WrappedQueue
	ctx       context.Context

	buildReconciler reconciler
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
}

// Creates a Config with sensible production defaults.
func defaultConfig() Config {
	return Config{
		MaxRetries:  5,
		UpdateDelay: time.Second * 5,
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
	)
}

func newOSBuildController(
	ctrlConfig Config,
	mcfgclient mcfgclientset.Interface,
	kubeclient clientset.Interface,
) *OSBuildController {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&coreclientsetv1.EventSinkImpl{Interface: kubeclient.CoreV1().Events("")})

	informers := newInformers(mcfgclient, kubeclient)

	ctrl := &OSBuildController{
		kubeclient:    kubeclient,
		mcfgclient:    mcfgclient,
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

	ctrl.podInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: ctrl.updatePod,
	})

	ctrl.machineConfigPoolInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: ctrl.updateMachineConfigPool,
	})

	ctrl.buildReconciler = newBuildReconciler(mcfgclient, kubeclient, ctrl.listers)

	return ctrl
}

func (ctrl *OSBuildController) Run(parentCtx context.Context, workers int) {
	klog.Infof("Starting OSBuildController")
	defer klog.Infof("Shutting down OSBuildController")

	ctx, cancel := context.WithCancel(parentCtx)
	defer utilruntime.HandleCrash()
	defer ctrl.execQueue.ShutDown()
	defer cancel()

	ctrl.ctx = ctx

	ctrl.informers.start(ctx)

	if !cache.WaitForCacheSync(ctx.Done(), ctrl.hasSynced...) {
		return
	}

	ctrl.execQueue.Start(ctx, workers)

	<-ctx.Done()
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
		return toRun(cctx)
	})
}

func (ctrl *OSBuildController) addMachineOSBuild(cur interface{}) {
	mosb := cur.(*mcfgv1alpha1.MachineOSBuild)
	ctrl.enqueueFuncForObject(mosb, func(ctx context.Context) error {
		return ctrl.buildReconciler.AddMachineOSBuild(ctx, mosb)
	})
}

func (ctrl *OSBuildController) updateMachineOSBuild(old, cur interface{}) {
	oldMOSB := old.(*mcfgv1alpha1.MachineOSBuild)
	curMOSB := cur.(*mcfgv1alpha1.MachineOSBuild)
	ctrl.enqueueFuncForObject(curMOSB, func(ctx context.Context) error {
		return ctrl.buildReconciler.UpdateMachineOSBuild(ctx, oldMOSB, curMOSB)
	})
}

func (ctrl *OSBuildController) deleteMachineOSBuild(cur interface{}) {
	mosb := cur.(*mcfgv1alpha1.MachineOSBuild)
	ctrl.enqueueFuncForObject(mosb, func(ctx context.Context) error {
		return ctrl.buildReconciler.DeleteMachineOSBuild(ctx, mosb)
	})
}

func (ctrl *OSBuildController) addPod(cur interface{}) {
	pod := cur.(*corev1.Pod)
	ctrl.enqueueFuncForObject(pod, func(ctx context.Context) error {
		return ctrl.buildReconciler.AddPod(ctx, pod)
	})
}

func (ctrl *OSBuildController) updatePod(old, cur interface{}) {
	oldPod := old.(*corev1.Pod)
	curPod := cur.(*corev1.Pod)

	ctrl.enqueueFuncForObject(curPod, func(ctx context.Context) error {
		return ctrl.buildReconciler.UpdatePod(ctx, oldPod, curPod)
	})
}

func (ctrl *OSBuildController) addMachineOSConfig(newMOSC interface{}) {
	m := newMOSC.(*mcfgv1alpha1.MachineOSConfig).DeepCopy()
	ctrl.enqueueFuncForObject(m, func(ctx context.Context) error {
		return ctrl.buildReconciler.AddMachineOSConfig(ctx, m)
	})
}

func (ctrl *OSBuildController) updateMachineOSConfig(old, cur interface{}) {
	oldMOSC := old.(*mcfgv1alpha1.MachineOSConfig).DeepCopy()
	curMOSC := cur.(*mcfgv1alpha1.MachineOSConfig).DeepCopy()
	ctrl.enqueueFuncForObject(curMOSC, func(ctx context.Context) error {
		return ctrl.buildReconciler.UpdateMachineOSConfig(ctx, oldMOSC, curMOSC)
	})
}

func (ctrl *OSBuildController) deleteMachineOSConfig(cur interface{}) {
	mosc, ok := cur.(*mcfgv1alpha1.MachineOSConfig)
	if !ok {
		tombstone, ok := cur.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Couldn't get object from tombstone %#v", cur))
			return
		}
		mosc, ok = tombstone.Obj.(*mcfgv1alpha1.MachineOSConfig)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Tombstone contained object that is not a MachineOSConfig %#v", cur))
			return
		}
	}

	ctrl.enqueueFuncForObject(mosc, func(ctx context.Context) error {
		return ctrl.buildReconciler.DeleteMachineOSConfig(ctx, mosc)
	})
}

func (ctrl *OSBuildController) updateMachineConfigPool(old, cur interface{}) {
	oldMCP := old.(*mcfgv1.MachineConfigPool).DeepCopy()
	curMCP := cur.(*mcfgv1.MachineConfigPool).DeepCopy()

	ctrl.enqueueFuncForObject(curMCP, func(ctx context.Context) error {
		return ctrl.buildReconciler.UpdateMachineConfigPool(ctx, oldMCP, curMCP)
	})
}
