package build

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/scheme"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	corev1 "k8s.io/api/core/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	coreclientsetv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
)

type BuildController struct {
	*controllerBase

	eventRecorder record.EventRecorder

	config    BuildControllerConfig
	execQueue *ctrlcommon.WrappedQueue
	ctx       context.Context

	buildReconciler reconciler
}

func NewBuildController(
	ctrlConfig BuildControllerConfig,
	clients *Clients,
) *BuildController {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&coreclientsetv1.EventSinkImpl{Interface: clients.kubeclient.CoreV1().Events("")})

	ctrl := &BuildController{
		controllerBase: newControllerBase(clients),
		eventRecorder:  eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineosbuildcontroller"}),
		execQueue:      ctrlcommon.NewWrappedQueue("machineosbuildcontroller"),
		config:         ctrlConfig,
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

	ctrl.buildReconciler = newBuildReconciler(ctrl.Clients, ctrl.listers)

	return ctrl
}

func (ctrl *BuildController) Run(parentCtx context.Context, workers int) {
	klog.Infof("Starting BuildController")
	defer klog.Infof("Shutting down BuildController")

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

func (ctrl *BuildController) enqueueFuncForObject(obj kubeObject, toRun func(context.Context) error) {
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

	ctrl.execQueue.EnqueueWithNameAndContext(ctrl.ctx, info.String(), func(ctx context.Context) error {
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()
		return toRun(cctx)
	})
}

func (ctrl *BuildController) addMachineOSBuild(cur interface{}) {
	mosb := cur.(*mcfgv1alpha1.MachineOSBuild)
	ctrl.enqueueFuncForObject(mosb, func(ctx context.Context) error {
		return ctrl.buildReconciler.AddMachineOSBuild(ctx, mosb)
	})
}

func (ctrl *BuildController) updateMachineOSBuild(old, cur interface{}) {
	oldMOSB := old.(*mcfgv1alpha1.MachineOSBuild)
	curMOSB := cur.(*mcfgv1alpha1.MachineOSBuild)
	ctrl.enqueueFuncForObject(curMOSB, func(ctx context.Context) error {
		return ctrl.buildReconciler.UpdateMachineOSBuild(ctx, oldMOSB, curMOSB)
	})
}

func (ctrl *BuildController) deleteMachineOSBuild(cur interface{}) {
	mosb := cur.(*mcfgv1alpha1.MachineOSBuild)
	ctrl.enqueueFuncForObject(mosb, func(ctx context.Context) error {
		return ctrl.buildReconciler.DeleteMachineOSBuild(ctx, mosb)
	})
}

func (ctrl *BuildController) addPod(cur interface{}) {
	pod := cur.(*corev1.Pod)
	ctrl.enqueueFuncForObject(pod, func(ctx context.Context) error {
		return ctrl.buildReconciler.AddPod(ctx, pod)
	})
}

func (ctrl *BuildController) updatePod(old, cur interface{}) {
	oldPod := old.(*corev1.Pod)
	curPod := cur.(*corev1.Pod)

	ctrl.enqueueFuncForObject(curPod, func(ctx context.Context) error {
		return ctrl.buildReconciler.UpdatePod(ctx, oldPod, curPod)
	})
}

func (ctrl *BuildController) addMachineOSConfig(newMOSC interface{}) {
	m := newMOSC.(*mcfgv1alpha1.MachineOSConfig).DeepCopy()
	ctrl.enqueueFuncForObject(m, func(ctx context.Context) error {
		return ctrl.buildReconciler.AddMachineOSConfig(ctx, m)
	})
}

func (ctrl *BuildController) updateMachineOSConfig(old, cur interface{}) {
	oldMOSC := old.(*mcfgv1alpha1.MachineOSConfig).DeepCopy()
	curMOSC := cur.(*mcfgv1alpha1.MachineOSConfig).DeepCopy()
	ctrl.enqueueFuncForObject(curMOSC, func(ctx context.Context) error {
		return ctrl.buildReconciler.UpdateMachineOSConfig(ctx, oldMOSC, curMOSC)
	})
}

func (ctrl *BuildController) deleteMachineOSConfig(cur interface{}) {
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

func (ctrl *BuildController) updateMachineConfigPool(old, cur interface{}) {
	oldMCP := old.(*mcfgv1.MachineConfigPool).DeepCopy()
	curMCP := cur.(*mcfgv1.MachineConfigPool).DeepCopy()

	ctrl.enqueueFuncForObject(curMCP, func(ctx context.Context) error {
		return ctrl.buildReconciler.UpdateMachineConfigPool(ctx, oldMCP, curMCP)
	})
}
