package node

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"sort"
	"strings"
	"time"

	helpers "github.com/openshift/machine-config-operator/pkg/helpers"
	"github.com/openshift/machine-config-operator/pkg/upgrademonitor"

	configv1 "github.com/openshift/api/config/v1"
	features "github.com/openshift/api/features"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	opv1 "github.com/openshift/api/operator/v1"

	cligoinformersv1 "github.com/openshift/client-go/config/informers/externalversions/config/v1"
	cligolistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/scheme"
	mcfginformersv1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1"
	mcopinformersv1 "github.com/openshift/client-go/operator/informers/externalversions/operator/v1"
	mcoplistersv1 "github.com/openshift/client-go/operator/listers/operator/v1"

	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	"github.com/openshift/machine-config-operator/internal"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/constants"
	buildconstants "github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	kubeErrs "k8s.io/apimachinery/pkg/util/errors"
	intstrutil "k8s.io/apimachinery/pkg/util/intstr"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformersv1 "k8s.io/client-go/informers/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	coreclientsetv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	corelisterv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	clientretry "k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

const (
	// WorkerLabel defines the label associated with worker node.
	WorkerLabel = "node-role.kubernetes.io/worker"
	// ControlPlaneLabel defines the label associated with master/control-plane node.
	ControlPlaneLabel = "node-role.kubernetes.io/control-plane"

	// maxRetries is the number of times a machineconfig pool will be retried before it is dropped out of the queue.
	// With the current rate-limiter in use (5ms*2^(maxRetries-1)) the following numbers represent the times
	// a machineconfig pool is going to be requeued:
	//
	// 5ms, 10ms, 20ms, 40ms, 80ms, 160ms, 320ms, 640ms, 1.3s, 2.6s, 5.1s, 10.2s, 20.4s, 41s, 82s
	maxRetries = 15

	// defaultUpdateDelay is a pause to deal with churn in MachineConfigs; see
	// https://github.com/openshift/machine-config-operator/issues/301
	defaultUpdateDelay = 5 * time.Second

	// osLabel is used to identify which type of OS the node has
	osLabel = "kubernetes.io/os"

	// zoneLabel is for https://kubernetes.io/docs/setup/best-practices/multiple-zones/
	zoneLabel = "topology.kubernetes.io/zone"

	// schedulerCRName that we're interested in watching.
	schedulerCRName = "cluster"
)

// Controller defines the node controller.
type Controller struct {
	client        mcfgclientset.Interface
	kubeClient    clientset.Interface
	eventRecorder record.EventRecorder

	syncHandler              func(mcp string) error
	enqueueMachineConfigPool func(*mcfgv1.MachineConfigPool)

	ccLister   mcfglistersv1.ControllerConfigLister
	mcLister   mcfglistersv1.MachineConfigLister
	mcpLister  mcfglistersv1.MachineConfigPoolLister
	moscLister mcfglistersv1.MachineOSConfigLister
	mosbLister mcfglistersv1.MachineOSBuildLister
	nodeLister corelisterv1.NodeLister
	podLister  corelisterv1.PodLister
	mcnLister  mcfglistersv1.MachineConfigNodeLister

	ccListerSynced   cache.InformerSynced
	mcListerSynced   cache.InformerSynced
	mcpListerSynced  cache.InformerSynced
	moscListerSynced cache.InformerSynced
	mosbListerSynced cache.InformerSynced
	nodeListerSynced cache.InformerSynced
	mcnListerSynced  cache.InformerSynced

	schedulerList         cligolistersv1.SchedulerLister
	schedulerListerSynced cache.InformerSynced

	mcopLister       mcoplistersv1.MachineConfigurationLister
	mcopListerSynced cache.InformerSynced

	queue workqueue.TypedRateLimitingInterface[string]

	fgHandler ctrlcommon.FeatureGatesHandler

	// updateDelay is a pause to deal with churn in MachineConfigs; see
	// https://github.com/openshift/machine-config-operator/issues/301
	updateDelay time.Duration
}

func New(
	ccInformer mcfginformersv1.ControllerConfigInformer,
	mcInformer mcfginformersv1.MachineConfigInformer,
	mcpInformer mcfginformersv1.MachineConfigPoolInformer,
	nodeInformer coreinformersv1.NodeInformer,
	podInformer coreinformersv1.PodInformer,
	moscInformer mcfginformersv1.MachineOSConfigInformer,
	mosbInformer mcfginformersv1.MachineOSBuildInformer,
	mcnInformer mcfginformersv1.MachineConfigNodeInformer,
	schedulerInformer cligoinformersv1.SchedulerInformer,
	mcopInformer mcopinformersv1.MachineConfigurationInformer,
	kubeClient clientset.Interface,
	mcfgClient mcfgclientset.Interface,
	fgHandler ctrlcommon.FeatureGatesHandler,
) *Controller {
	return newController(
		ccInformer,
		mcInformer,
		mcpInformer,
		moscInformer,
		mosbInformer,
		nodeInformer,
		podInformer,
		mcnInformer,
		schedulerInformer,
		mcopInformer,
		kubeClient,
		mcfgClient,
		defaultUpdateDelay,
		fgHandler,
	)
}

func NewWithCustomUpdateDelay(
	ccInformer mcfginformersv1.ControllerConfigInformer,
	mcInformer mcfginformersv1.MachineConfigInformer,
	mcpInformer mcfginformersv1.MachineConfigPoolInformer,
	nodeInformer coreinformersv1.NodeInformer,
	podInformer coreinformersv1.PodInformer,
	moscInformer mcfginformersv1.MachineOSConfigInformer,
	mosbInformer mcfginformersv1.MachineOSBuildInformer,
	mcnInformer mcfginformersv1.MachineConfigNodeInformer,
	schedulerInformer cligoinformersv1.SchedulerInformer,
	mcopInformer mcopinformersv1.MachineConfigurationInformer,
	kubeClient clientset.Interface,
	mcfgClient mcfgclientset.Interface,
	updateDelay time.Duration,
	fgHandler ctrlcommon.FeatureGatesHandler,
) *Controller {
	return newController(
		ccInformer,
		mcInformer,
		mcpInformer,
		moscInformer,
		mosbInformer,
		nodeInformer,
		podInformer,
		mcnInformer,
		schedulerInformer,
		mcopInformer,
		kubeClient,
		mcfgClient,
		updateDelay,
		fgHandler,
	)
}

// new returns a new node controller.
func newController(
	ccInformer mcfginformersv1.ControllerConfigInformer,
	mcInformer mcfginformersv1.MachineConfigInformer,
	mcpInformer mcfginformersv1.MachineConfigPoolInformer,
	moscInformer mcfginformersv1.MachineOSConfigInformer,
	mosbInformer mcfginformersv1.MachineOSBuildInformer,
	nodeInformer coreinformersv1.NodeInformer,
	podInformer coreinformersv1.PodInformer,
	mcnInformer mcfginformersv1.MachineConfigNodeInformer,
	schedulerInformer cligoinformersv1.SchedulerInformer,
	mcopInformer mcopinformersv1.MachineConfigurationInformer,
	kubeClient clientset.Interface,
	mcfgClient mcfgclientset.Interface,
	updateDelay time.Duration,
	fgHandler ctrlcommon.FeatureGatesHandler,
) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&coreclientsetv1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	ctrl := &Controller{
		client:        mcfgClient,
		kubeClient:    kubeClient,
		eventRecorder: ctrlcommon.NamespacedEventRecorder(eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineconfigcontroller-nodecontroller"})),
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "machineconfigcontroller-nodecontroller"}),
		updateDelay: updateDelay,
		fgHandler:   fgHandler,
	}
	moscInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addMachineOSConfig,
		UpdateFunc: ctrl.updateMachineOSConfig,
		DeleteFunc: ctrl.deleteMachineOSConfig,
	})
	mosbInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addMachineOSBuild,
		UpdateFunc: ctrl.updateMachineOSBuild,
		DeleteFunc: ctrl.deleteMachineOSBuild,
	})
	mcpInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addMachineConfigPool,
		UpdateFunc: ctrl.updateMachineConfigPool,
		DeleteFunc: ctrl.deleteMachineConfigPool,
	})
	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addNode,
		UpdateFunc: ctrl.updateNode,
		DeleteFunc: ctrl.deleteNode,
	})
	schedulerInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.checkMasterNodesOnAdd,
		UpdateFunc: ctrl.checkMasterNodesOnUpdate,
		DeleteFunc: ctrl.checkMasterNodesOnDelete,
	})
	mcnInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addMachineConfigNode,
		UpdateFunc: ctrl.updateMachineConfigNode,
		DeleteFunc: ctrl.deleteMachineConfigNode,
	})
	mcopInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addMachineConfiguration,
		UpdateFunc: ctrl.updateMachineConfiguration,
		DeleteFunc: ctrl.deleteMachineConfiguration,
	})

	ctrl.syncHandler = ctrl.syncMachineConfigPool
	ctrl.enqueueMachineConfigPool = ctrl.enqueueDefault

	ctrl.ccLister = ccInformer.Lister()
	ctrl.mcLister = mcInformer.Lister()
	ctrl.mcpLister = mcpInformer.Lister()
	ctrl.moscLister = moscInformer.Lister()
	ctrl.mosbLister = mosbInformer.Lister()
	ctrl.nodeLister = nodeInformer.Lister()
	ctrl.podLister = podInformer.Lister()
	ctrl.mcnLister = mcnInformer.Lister()
	ctrl.ccListerSynced = ccInformer.Informer().HasSynced
	ctrl.mcListerSynced = mcInformer.Informer().HasSynced
	ctrl.mcpListerSynced = mcpInformer.Informer().HasSynced
	ctrl.moscListerSynced = moscInformer.Informer().HasSynced
	ctrl.mosbListerSynced = mosbInformer.Informer().HasSynced
	ctrl.nodeListerSynced = nodeInformer.Informer().HasSynced
	ctrl.mcnListerSynced = mcnInformer.Informer().HasSynced

	ctrl.schedulerList = schedulerInformer.Lister()
	ctrl.schedulerListerSynced = schedulerInformer.Informer().HasSynced

	ctrl.mcopLister = mcopInformer.Lister()
	ctrl.mcopListerSynced = mcopInformer.Informer().HasSynced

	return ctrl
}

// Run executes the render controller.
func (ctrl *Controller) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()

	if !cache.WaitForCacheSync(stopCh, ctrl.ccListerSynced, ctrl.mcListerSynced, ctrl.mcpListerSynced, ctrl.moscListerSynced, ctrl.mosbListerSynced, ctrl.nodeListerSynced, ctrl.schedulerListerSynced, ctrl.mcopListerSynced) {
		return
	}

	klog.Info("Starting MachineConfigController-NodeController")
	defer klog.Info("Shutting down MachineConfigController-NodeController")

	// TODO (MCO-1775): Once ImageModeStatusReporting has been GA for an entire release version
	// (for example >=4.22.0), the below migration logic can be removed.
	// Perform one-time migration from legacy MachineConfigNodeUpdateFilesAndOS condition
	// to new ImageModeStatusReporting conditions when feature gate is enabled
	if ctrl.fgHandler.Enabled(features.FeatureGateImageModeStatusReporting) {
		go ctrl.performImageModeStatusReportingConditionMigration()
	}

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.worker, time.Second, stopCh)
	}

	<-stopCh
}

func (ctrl *Controller) getCurrentMasters() ([]*corev1.Node, error) {
	nodeList, err := ctrl.nodeLister.List(labels.SelectorFromSet(labels.Set{ctrlcommon.MasterLabel: ""}))
	if err != nil {
		return nil, fmt.Errorf("error while listing master nodes %w", err)
	}
	return nodeList, nil
}

// checkMasterNodesOnAdd makes the master nodes schedulable/unschedulable whenever scheduler config CR with name
// cluster is created
func (ctrl *Controller) checkMasterNodesOnAdd(_ interface{}) {
	ctrl.reconcileMasters()
}

// checkMasterNodesOnDelete makes the master nodes schedulable/unschedulable whenever scheduler config CR with name
// cluster is created
func (ctrl *Controller) checkMasterNodesOnDelete(obj interface{}) {
	scheduler := obj.(*configv1.Scheduler)
	if scheduler.Name != schedulerCRName {
		klog.V(4).Infof("We don't care about CRs other than cluster created for scheduler config")
		return
	}
	currentMasters, err := ctrl.getCurrentMasters()
	if err != nil {
		err = fmt.Errorf("reconciling to make master nodes schedulable/unschedulable failed: %w", err)
		klog.Error(err)
		return
	}
	// On deletion make all masters unschedulable to restore default behaviour
	errs := ctrl.makeMastersUnSchedulable(currentMasters)
	if len(errs) > 0 {
		err = v1helpers.NewMultiLineAggregate(errs)
		err = fmt.Errorf("reconciling to make nodes schedulable/unschedulable failed: %w", err)
		klog.Error(err)
		return
	}
	return
}

// checkMasterNodesonUpdate makes the master nodes schedulable/unschedulable whenever scheduler
// config CR with name cluster is updated
func (ctrl *Controller) checkMasterNodesOnUpdate(old, cur interface{}) {
	oldScheduler := old.(*configv1.Scheduler)
	curScheduler := cur.(*configv1.Scheduler)

	if oldScheduler.Name != schedulerCRName || curScheduler.Name != schedulerCRName {
		klog.V(4).Infof("We don't care about CRs other than cluster created for scheduler config")
		return
	}

	if reflect.DeepEqual(oldScheduler.Spec, curScheduler.Spec) {
		klog.V(4).Info("Scheduler config did not change")
		return
	}

	ctrl.reconcileMasters()
}

// makeMastersUnSchedulable makes all the masters in the cluster unschedulable
func (ctrl *Controller) makeMastersUnSchedulable(currentMasters []*corev1.Node) []error {
	var errs []error
	for _, node := range currentMasters {
		if err := ctrl.makeMasterNodeUnSchedulable(node); err != nil {
			errs = append(errs, fmt.Errorf("failed making node %v schedulable with error %w", node.Name, err))
		}
	}
	return errs
}

// updateMasterNodeControlPlaneLabel ensures the control-plane label is on a node
func (ctrl *Controller) updateMasterNodeControlPlaneLabel(node *corev1.Node) error {
	// If the control plane label is already set then no-op.
	if _, hasControlPlaneLabel := node.Labels[ControlPlaneLabel]; hasControlPlaneLabel {
		return nil
	}
	_, err := internal.UpdateNodeRetry(ctrl.kubeClient.CoreV1().Nodes(), ctrl.nodeLister, node.Name, func(node *corev1.Node) {
		node.Labels[ControlPlaneLabel] = ""
	})
	if err != nil {
		return err
	}
	return nil
}

// makeMasterNodeUnSchedulable makes master node unschedulable by removing worker label and adding `NoSchedule`
// master taint to the master node
func (ctrl *Controller) makeMasterNodeUnSchedulable(node *corev1.Node) error {
	_, err := internal.UpdateNodeRetry(ctrl.kubeClient.CoreV1().Nodes(), ctrl.nodeLister, node.Name, func(node *corev1.Node) {
		// Remove worker label
		newLabels := node.Labels
		if _, hasWorkerLabel := newLabels[WorkerLabel]; hasWorkerLabel {
			delete(newLabels, WorkerLabel)
		}
		node.Labels = newLabels
		// Add master taint
		hasMasterTaint := false
		for _, taint := range node.Spec.Taints {
			if taint.Key == ctrlcommon.MasterLabel && taint.Effect == corev1.TaintEffectNoSchedule {
				hasMasterTaint = true
			}
		}
		if !hasMasterTaint {
			newTaints := node.Spec.Taints
			masterUnSchedulableTaint := corev1.Taint{Key: ctrlcommon.MasterLabel, Effect: corev1.TaintEffectNoSchedule}
			newTaints = append(newTaints, masterUnSchedulableTaint)
			node.Spec.Taints = newTaints
		}
	})
	if err != nil {
		return err
	}
	return nil
}

// makeMasterNodeSchedulable makes master node schedulable by removing NoSchedule master taint and
// adding worker label
func (ctrl *Controller) makeMasterNodeSchedulable(node *corev1.Node) error {
	_, err := internal.UpdateNodeRetry(ctrl.kubeClient.CoreV1().Nodes(), ctrl.nodeLister, node.Name, func(node *corev1.Node) {
		// Add worker label
		newLabels := node.Labels
		if _, hasWorkerLabel := newLabels[WorkerLabel]; !hasWorkerLabel {
			newLabels[WorkerLabel] = ""
		}
		node.Labels = newLabels
		// Remove master taint
		newTaints := []corev1.Taint{}
		for _, t := range node.Spec.Taints {
			if t.Key == ctrlcommon.MasterLabel && t.Effect == corev1.TaintEffectNoSchedule {
				continue
			}
			newTaints = append(newTaints, t)
		}
		node.Spec.Taints = newTaints
	})
	if err != nil {
		return err
	}
	return nil
}

func (ctrl *Controller) addMachineOSConfig(obj interface{}) {
	curMOSC := obj.(*mcfgv1.MachineOSConfig)
	klog.V(4).Infof("Adding MachineOSConfig %s", curMOSC.Name)
	mcp, err := ctrl.mcpLister.Get(curMOSC.Spec.MachineConfigPool.Name)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get MachineConfigPool from MachineOSConfig %#v", curMOSC))
		return
	}
	klog.V(4).Infof("MachineConfigPool %s opt in to OCL", mcp.Name)
	ctrl.enqueueMachineConfigPool(mcp)
}

func (ctrl *Controller) updateMachineOSConfig(old, cur interface{}) {
	oldMOSC := old.(*mcfgv1.MachineOSConfig)
	curMOSC := cur.(*mcfgv1.MachineOSConfig)
	if equality.Semantic.DeepEqual(oldMOSC.Status.CurrentImagePullSpec, curMOSC.Status.CurrentImagePullSpec) {
		// we do not want to trigger an update func just if the image is not ready
		return
	}
	klog.V(4).Infof("Updating MachineOSConfig %s", oldMOSC.Name)
	mcp, err := ctrl.mcpLister.Get(curMOSC.Spec.MachineConfigPool.Name)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get Machine Config Pool from MachineOSConfig %#v", curMOSC))
		return
	}
	klog.V(4).Infof("Image is ready for MachineConfigPool %s", mcp.Name)
	ctrl.enqueueMachineConfigPool(mcp)
}

func (ctrl *Controller) deleteMachineOSConfig(cur interface{}) {
	curMOSC, ok := cur.(*mcfgv1.MachineOSConfig)
	if !ok {
		tombstone, ok := cur.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("couldn't get object from tombstone %#v", cur))
			return
		}
		curMOSC, ok = tombstone.Obj.(*mcfgv1.MachineOSConfig)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not a MachineOSConfig %#v", cur))
			return
		}
	}
	klog.V(4).Infof("Deleting MachineOSConfig %s", curMOSC.Name)

	mcp, err := ctrl.mcpLister.Get(curMOSC.Spec.MachineConfigPool.Name)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get MachineConfigPool from MachineOSConfig %#v", curMOSC))
		return
	}
	ctrl.enqueueMachineConfigPool(mcp)
}

func (ctrl *Controller) deleteMachineOSBuild(obj interface{}) {
	curMOSB, ok := obj.(*mcfgv1.MachineOSBuild)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("couldn't get object from tombstone %#v", obj))
			return
		}
		curMOSB, ok = tombstone.Obj.(*mcfgv1.MachineOSBuild)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not a MOSB %#v", obj))
			return
		}
	}
	klog.V(4).Infof("Deleting MachineOSBuild %s", curMOSB.Name)
}

func (ctrl *Controller) addMachineOSBuild(obj interface{}) {
	curMOSB := obj.(*mcfgv1.MachineOSBuild)
	klog.V(4).Infof("Adding MachineOSBuild %s", curMOSB.Name)

	// Find the associated MachineConfigPool from the MachineOSBuild
	if curMOSB.Labels == nil {
		return
	}

	poolName, ok := curMOSB.Labels[buildconstants.TargetMachineConfigPoolLabelKey]
	if !ok {
		return
	}

	mcp, err := ctrl.mcpLister.Get(poolName)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get MachineConfigPool from MachineOSBuild %#v: %v", curMOSB, err))
		return
	}
	klog.V(4).Infof("MachineOSBuild %s affects MachineConfigPool %s", curMOSB.Name, mcp.Name)
	ctrl.enqueueMachineConfigPool(mcp)
}

func (ctrl *Controller) updateMachineOSBuild(old, cur interface{}) {
	oldMOSB := old.(*mcfgv1.MachineOSBuild)
	curMOSB := cur.(*mcfgv1.MachineOSBuild)

	// Only process if the build status or phase has changed
	if oldMOSB.Status.BuildStart == curMOSB.Status.BuildStart &&
		oldMOSB.Status.BuildEnd == curMOSB.Status.BuildEnd &&
		equality.Semantic.DeepEqual(oldMOSB.Status.Conditions, curMOSB.Status.Conditions) {
		return
	}

	klog.V(4).Infof("Updating MachineOSBuild %s", curMOSB.Name)

	// Find the associated MachineConfigPool from the MachineOSBuild
	if curMOSB.Labels == nil {
		return
	}

	poolName, ok := curMOSB.Labels[buildconstants.TargetMachineConfigPoolLabelKey]
	if !ok {
		return
	}

	mcp, err := ctrl.mcpLister.Get(poolName)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get MachineConfigPool from MachineOSBuild %#v: %v", curMOSB, err))
		return
	}
	klog.V(4).Infof("MachineOSBuild %s status changed for MachineConfigPool %s", curMOSB.Name, mcp.Name)
	ctrl.enqueueMachineConfigPool(mcp)
}

func (ctrl *Controller) addMachineConfigNode(obj interface{}) {
	curMCN := obj.(*mcfgv1.MachineConfigNode)
	klog.V(4).Infof("Adding MachineConfigNode %s", curMCN.Name)

	// Find the associated MachineConfigPool from the MachineConfigNode. If the pool value is
	// "not-yet-set" it means that the MCN does not have an associated MCP yet.
	poolName := curMCN.Spec.Pool.Name
	if poolName == upgrademonitor.NotYetSet {
		return
	}

	mcp, err := ctrl.mcpLister.Get(poolName)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get MachineConfigPool from MachineConfigNode %v: %v", curMCN, err))
		return
	}
	klog.V(4).Infof("MachineConfigNode %s affects MachineConfigPool %s", curMCN.Name, mcp.Name)
	ctrl.enqueueMachineConfigPool(mcp)
}

func (ctrl *Controller) updateMachineConfigNode(old, cur interface{}) {
	oldMCN := old.(*mcfgv1.MachineConfigNode)
	curMCN := cur.(*mcfgv1.MachineConfigNode)

	// Only process if the MCN conditions, desired config, or pool association has changed. If the
	// pool name value is "not-yet-set" it means that the MCN does not have an associated MCP yet,
	// which is also a condition we should skip on.
	curPoolName := curMCN.Spec.Pool.Name
	if curPoolName == upgrademonitor.NotYetSet || (oldMCN.Spec.Pool.Name == curPoolName &&
		oldMCN.Spec.ConfigVersion.Desired == curMCN.Spec.ConfigVersion.Desired &&
		equality.Semantic.DeepEqual(oldMCN.Status.Conditions, curMCN.Status.Conditions)) {
		return
	}

	klog.V(4).Infof("Updating MachineConfigNode %s", curMCN.Name)

	mcp, err := ctrl.mcpLister.Get(curPoolName)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get MachineConfigPool from MachineConfigNode %v: %v", curMCN.Name, err))
		return
	}
	klog.V(4).Infof("MachineConfigNode %s status changed for MachineConfigPool %s", curMCN.Name, mcp.Name)
	ctrl.enqueueMachineConfigPool(mcp)
}

func (ctrl *Controller) deleteMachineConfigNode(obj interface{}) {
	curMCN, ok := obj.(*mcfgv1.MachineConfigNode)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("couldn't get object from tombstone %#v", obj))
			return
		}
		curMCN, ok = tombstone.Obj.(*mcfgv1.MachineConfigNode)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not a MCN %#v", obj))
			return
		}
	}
	klog.V(4).Infof("Deleting MachineConfigNode %s", curMCN.Name)

	// Find the associated MachineConfigPool from the MachineConfigNode. If the pool value is
	// "not-yet-set" it means that the MCN does not have an associated MCP yet.
	mcpName := curMCN.Spec.Pool.Name
	if mcpName == upgrademonitor.NotYetSet {
		return
	}
	mcp, err := ctrl.mcpLister.Get(mcpName)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get MachineConfigPool from MachineConfigNode %v", curMCN.Name))
		return
	}
	ctrl.enqueueMachineConfigPool(mcp)
}

func (ctrl *Controller) addMachineConfigPool(obj interface{}) {
	pool := obj.(*mcfgv1.MachineConfigPool)
	klog.V(4).Infof("Adding MachineConfigPool %s", pool.Name)
	ctrl.enqueueMachineConfigPool(pool)
}

func (ctrl *Controller) updateMachineConfigPool(old, cur interface{}) {
	oldPool := old.(*mcfgv1.MachineConfigPool)
	curPool := cur.(*mcfgv1.MachineConfigPool)

	klog.V(4).Infof("Updating MachineConfigPool %s", oldPool.Name)
	ctrl.enqueueMachineConfigPool(curPool)
}

func (ctrl *Controller) deleteMachineConfigPool(obj interface{}) {
	pool, ok := obj.(*mcfgv1.MachineConfigPool)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("couldn't get object from tombstone %#v", obj))
			return
		}
		pool, ok = tombstone.Obj.(*mcfgv1.MachineConfigPool)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not a MachineConfigPool %#v", obj))
			return
		}
	}
	klog.V(4).Infof("Deleting MachineConfigPool %s", pool.Name)
	// TODO(abhinavdahiya): handle deletes.
}

// Determine if masters are currently configured as schedulable
func (ctrl *Controller) getMastersSchedulable() (bool, error) {
	schedulerList, err := ctrl.schedulerList.List(labels.SelectorFromSet(nil))
	if err != nil {
		return false, fmt.Errorf("error while listing scheduler config %w", err)
	}
	for _, sched := range schedulerList {
		if sched.Name == schedulerCRName {
			return sched.Spec.MastersSchedulable, nil
		}
	}
	// If the scheduler list is empty, or there is no match. Return an error so it is a no-op.
	return false, fmt.Errorf("cluster scheduler couldn't be found")
}

// Determine if a given Node is a master
func (ctrl *Controller) isMaster(node *corev1.Node) bool {
	_, master := node.ObjectMeta.Labels[ctrlcommon.MasterLabel]
	return master
}

// Given a master Node, ensure it reflects the current mastersSchedulable
// setting and make sure the control-plane label is set.
func (ctrl *Controller) reconcileMaster(node *corev1.Node) {
	err := ctrl.updateMasterNodeControlPlaneLabel(node)
	if err != nil {
		err = fmt.Errorf("failed adding the control-plane label to master Node: %w", err)
		klog.Error(err)
		return
	}

	mastersSchedulable, err := ctrl.getMastersSchedulable()
	if err != nil {
		err = fmt.Errorf("getting scheduler config failed: %w", err)
		klog.Error(err)
		return
	}
	if mastersSchedulable {
		err = ctrl.makeMasterNodeSchedulable(node)
		if err != nil {
			err = fmt.Errorf("failed making master Node schedulable: %w", err)
			klog.Error(err)
			return
		}
	} else if !mastersSchedulable {
		err = ctrl.makeMasterNodeUnSchedulable(node)
		if err != nil {
			err = fmt.Errorf("failed making master Node unschedulable: %w", err)
			klog.Error(err)
			return
		}
	}
}

// Get a list of current masters and apply scheduler config to them
// TODO: Taint reconciliation should happen elsewhere, in a generic taint/label reconciler
func (ctrl *Controller) reconcileMasters() {
	currentMasters, err := ctrl.getCurrentMasters()
	if err != nil {
		err = fmt.Errorf("reconciling to make master nodes schedulable/unschedulable failed: %w", err)
		klog.Error(err)
		return
	}
	for _, node := range currentMasters {
		ctrl.reconcileMaster(node)
	}
}

func (ctrl *Controller) addNode(obj interface{}) {
	node := obj.(*corev1.Node)
	if node.DeletionTimestamp != nil {
		ctrl.deleteNode(node)
		return
	}

	if ctrl.isMaster(node) {
		ctrl.reconcileMaster(node)
	}

	pools, err := ctrl.getPoolsForNode(node)
	if err != nil {
		klog.Errorf("error finding pools for node %s: %v", node.Name, err)
		return
	}
	if pools == nil {
		return
	}
	klog.V(4).Infof("Node %s added", node.Name)
	for _, pool := range pools {
		ctrl.enqueueMachineConfigPool(pool)
	}
}

func (ctrl *Controller) logPool(pool *mcfgv1.MachineConfigPool, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	klog.Infof("Pool %s: %s", pool.Name, msg)
}

func (ctrl *Controller) logPoolNode(pool *mcfgv1.MachineConfigPool, node *corev1.Node, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	zone, zok := node.Labels[zoneLabel]
	zonemsg := ""
	if zok {
		zonemsg = fmt.Sprintf("[zone=%s]", zone)
	}
	klog.Infof("Pool %s%s: node %s: %s", pool.Name, zonemsg, node.Name, msg)
}

//nolint:gocyclo
func (ctrl *Controller) updateNode(old, cur interface{}) {
	oldNode := old.(*corev1.Node)
	curNode := cur.(*corev1.Node)

	if !isNodeManaged(curNode) {
		return
	}

	if ctrl.isMaster(curNode) {
		ctrl.reconcileMaster(curNode)
	}

	pool, err := ctrl.getPrimaryPoolForNode(curNode)
	if err != nil {
		klog.Errorf("error finding pool for node: %v", err)
		return
	}
	if pool == nil {
		utilruntime.HandleError(fmt.Errorf("all nodes must be in a pool if managed by the MCO. Node %s is not in a pool", curNode.Name))
		return
	}
	klog.V(4).Infof("Node %s updated", curNode.Name)

	// Let's be verbose when a node changes pool
	oldPool, err := ctrl.getPrimaryPoolForNode(oldNode)
	if err == nil && oldPool != nil && oldPool.Name != pool.Name {
		ctrl.logPoolNode(pool, curNode, "changed from pool %s", oldPool.Name)
		// Let's also make sure the old pool node counts/status get updated
		ctrl.enqueueMachineConfigPool(oldPool)
	} else if err != nil {
		// getPrimaryPoolForNode may error due to multiple custom pools. In this scenario, let's
		// queue all of them so that when the node attempts to exit from this error state, the MCP
		// statuses are updated correctly.
		klog.Errorf("error fetching old primary pool for node %s, attempting to sync all old pools", oldNode.Name)
		masterPool, workerPool, customPools, listErr := helpers.ListPools(oldNode, ctrl.mcpLister)
		if listErr == nil {
			for _, pool := range customPools {
				ctrl.enqueueMachineConfigPool(pool)
			}
			if masterPool != nil {
				ctrl.enqueueMachineConfigPool(masterPool)
			}
			if workerPool != nil {
				ctrl.enqueueMachineConfigPool(workerPool)
			}
		} else {
			klog.Errorf("error listing old pools %v for node %s", listErr, oldNode.Name)
		}

	}

	var changed bool
	oldLNS := ctrlcommon.NewLayeredNodeState(oldNode)
	curLNS := ctrlcommon.NewLayeredNodeState(curNode)
	oldReadyErr := oldLNS.CheckNodeReady()
	newReadyErr := curLNS.CheckNodeReady()

	oldReady := getErrorString(oldReadyErr)
	newReady := getErrorString(newReadyErr)

	if oldReady != newReady {
		changed = true
		if newReadyErr != nil {
			ctrl.logPoolNode(pool, curNode, "Reporting unready: %v", newReadyErr)
		} else {
			ctrl.logPoolNode(pool, curNode, "Reporting ready")
		}
	}

	// Specifically log when a node has completed an update so the MCC logs are a useful central aggregate of state changes
	if oldNode.Annotations[daemonconsts.CurrentMachineConfigAnnotationKey] != oldNode.Annotations[daemonconsts.DesiredMachineConfigAnnotationKey] &&
		curLNS.IsNodeDone() {
		ctrl.logPoolNode(pool, curNode, "Completed update to %s", curNode.Annotations[daemonconsts.DesiredMachineConfigAnnotationKey])
		changed = true
	} else {
		annos := []string{
			daemonconsts.CurrentMachineConfigAnnotationKey,
			daemonconsts.DesiredMachineConfigAnnotationKey,
			daemonconsts.MachineConfigDaemonStateAnnotationKey,
			daemonconsts.MachineConfigDaemonReasonAnnotationKey,
			daemonconsts.CurrentImageAnnotationKey,
			daemonconsts.DesiredImageAnnotationKey,
		}

		for _, anno := range annos {
			if !hasNodeAnnotationChanged(oldNode, curNode, anno) {
				continue
			}

			newValue, ok := curNode.Annotations[anno]

			var changedMsg string
			var controlPlaneChangedMsg string
			if ok {
				changedMsg = fmt.Sprintf("changed annotation %s = %s", anno, newValue)
				controlPlaneChangedMsg = fmt.Sprintf("Node %s now has %s=%s", curNode.Name, anno, newValue)
			} else {
				changedMsg = fmt.Sprintf("lost annotation %s", anno)
				controlPlaneChangedMsg = fmt.Sprintf("Node %s no longer has %s", curNode.Name, anno)
			}
			ctrl.logPoolNode(pool, curNode, "%s", changedMsg)
			changed = true
			// For the control plane, emit events for these since they're important
			if pool.Name == ctrlcommon.MachineConfigPoolMaster {
				ctrl.eventRecorder.Eventf(pool, corev1.EventTypeNormal, "AnnotationChange", controlPlaneChangedMsg)
			}
		}
		if !reflect.DeepEqual(oldNode.Labels, curNode.Labels) {
			ctrl.logPoolNode(pool, curNode, "changed labels")
			changed = true
		}
		if !reflect.DeepEqual(oldNode.Spec.Taints, curNode.Spec.Taints) {
			ctrl.logPoolNode(pool, curNode, "changed taints")
			changed = true
		}
	}

	pools, err := ctrl.getPoolsForNode(curNode)
	if err != nil {
		klog.Errorf("error finding pools for node: %v", err)
		return
	}
	if pools == nil {
		return
	}

	if ctrl.fgHandler.Enabled(features.FeatureGatePinnedImages) {
		for _, pool := range pools {
			if isPinnedImageSetsInProgressForPool(pool) {
				changed = true
			}
		}
	}

	if !changed {
		return
	}

	for _, pool := range pools {
		ctrl.enqueueMachineConfigPool(pool)
	}
}

func hasNodeAnnotationChanged(oldNode, curNode *corev1.Node, key string) bool {
	oldValue, oldOK := oldNode.Annotations[key]
	newValue, newOK := curNode.Annotations[key]

	// If we had an annotation, but no longer have it, we've changed.
	if oldOK != newOK {
		return true
	}

	// If the old value does not match the current value, we've changed.
	return oldValue != newValue
}

func (ctrl *Controller) deleteNode(obj interface{}) {
	node, ok := obj.(*corev1.Node)

	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("couldn't get object from tombstone %#v", obj))
			return
		}
		node, ok = tombstone.Obj.(*corev1.Node)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not a Node %#v", obj))
			return
		}
	}

	pools, err := ctrl.getPoolsForNode(node)
	if err != nil {
		klog.Errorf("error finding pools for node: %v", err)
		return
	}
	if pools == nil {
		return
	}

	// Clear any associated MCCDrainErr, if any.
	if ctrlcommon.MCCDrainErr.DeleteLabelValues(node.Name) {
		klog.Infof("Cleaning up MCCDrain error for node(%s) as it is being deleted", node.Name)
	}

	klog.V(4).Infof("Node %s delete", node.Name)
	for _, pool := range pools {
		ctrl.enqueueMachineConfigPool(pool)
	}
}

// getPoolsForNode chooses the MachineConfigPools that should be used for a given node.
// It disambiguates in the case where e.g. a node has both master/worker roles applied,
// and where a custom role may be used. It returns a slice of all the pools the node belongs to.
// It also ignores the Windows nodes.
func (ctrl *Controller) getPoolsForNode(node *corev1.Node) ([]*mcfgv1.MachineConfigPool, error) {
	pools, metric, err := helpers.GetPoolsForNode(ctrl.mcpLister, node)
	if err != nil {
		return nil, err
	}
	if pools == nil {
		return nil, nil
	}
	if metric != nil {
		ctrlcommon.MCCPoolAlert.WithLabelValues(node.Name).Set(float64(*metric))
	}
	return pools, nil
}

// getPrimaryPoolForNode uses getPoolsForNode and returns the first one which is the one the node targets
func (ctrl *Controller) getPrimaryPoolForNode(node *corev1.Node) (*mcfgv1.MachineConfigPool, error) {
	pools, err := ctrl.getPoolsForNode(node)
	if err != nil {
		return nil, err
	}
	if pools == nil {
		return nil, nil
	}
	return pools[0], nil
}

func (ctrl *Controller) enqueue(pool *mcfgv1.MachineConfigPool) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pool)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", pool, err))
		return
	}

	ctrl.queue.Add(key)
}

func (ctrl *Controller) enqueueRateLimited(pool *mcfgv1.MachineConfigPool) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pool)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", pool, err))
		return
	}

	ctrl.queue.AddRateLimited(key)
}

// enqueueAfter will enqueue a pool after the provided amount of time.
func (ctrl *Controller) enqueueAfter(pool *mcfgv1.MachineConfigPool, after time.Duration) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pool)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", pool, err))
		return
	}

	ctrl.queue.AddAfter(key, after)
}

// enqueueDefault calls a default enqueue function
func (ctrl *Controller) enqueueDefault(pool *mcfgv1.MachineConfigPool) {
	ctrl.enqueueAfter(pool, ctrl.updateDelay)
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

	err := ctrl.syncHandler(key)
	ctrl.handleErr(err, key)

	return true
}

func (ctrl *Controller) handleErr(err error, key string) {
	if err == nil {
		ctrl.queue.Forget(key)
		return
	}

	if ctrl.queue.NumRequeues(key) < maxRetries {
		klog.V(2).Infof("Error syncing machineconfigpool %v: %v", key, err)
		ctrl.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	klog.V(2).Infof("Dropping machineconfigpool %q out of the queue: %v", key, err)
	ctrl.queue.Forget(key)
	ctrl.queue.AddAfter(key, 1*time.Minute)
}

// Determines if we should continue processing a layered MachineConfigPool or
// if we should requeue. This targets the following scenarios:
// 1. If a pool is opted into layering, we should wait for the initial OS image
// build to be ready before we attempt to roll it out to the nodes.
// 2. If a MachineConfig changes, we should wait for the OS image build to be
// ready so we can update both the nodes' desired MachineConfig and desired
// image annotations simultaneously.
func (ctrl *Controller) getConfigAndBuildAndLayeredStatus(pool *mcfgv1.MachineConfigPool) (*mcfgv1.MachineOSConfig, *mcfgv1.MachineOSBuild, bool, error) {
	mosc, mosb, err := ctrl.getConfigAndBuild(pool)
	// If we attempt to list resources which are not present either because none
	// exist or they're behind an inactive feature gate, they will return an
	// IsNotFound error. Any other errors should be returned to the caller.
	if err != nil && !errors.IsNotFound(err) {
		return nil, nil, false, err
	}

	return mosc, mosb, ctrl.isLayeredPool(mosc, mosb), nil
}

func (ctrl *Controller) getConfigAndBuild(pool *mcfgv1.MachineConfigPool) (*mcfgv1.MachineOSConfig, *mcfgv1.MachineOSBuild, error) {
	var ourConfig *mcfgv1.MachineOSConfig
	var ourBuild *mcfgv1.MachineOSBuild

	// Use listers instead of API calls for better performance and immediate cache updates
	configList, err := ctrl.moscLister.List(labels.Everything())
	if err != nil {
		return nil, nil, err
	}

	for _, config := range configList {
		if config.Spec.MachineConfigPool.Name == pool.Name {
			ourConfig = config
			break
		}
	}

	if ourConfig == nil {
		return nil, nil, nil
	}

	buildList, err := ctrl.mosbLister.List(labels.Everything())
	if err != nil {
		return nil, nil, err
	}

	// First, try to get the MOSB from the current-machine-os-build annotation on the MOSC
	// This ensures we get the correct build when multiple MOSBs exist for the same rendered MC
	if currentBuildName, hasAnnotation := ourConfig.Annotations[buildconstants.CurrentMachineOSBuildAnnotationKey]; hasAnnotation {
		for _, build := range buildList {
			if build.Name == currentBuildName {
				// Validate that the build matches the pool's current rendered config
				// to prevent using stale builds during config transitions
				if build.Spec.MachineConfig.Name == pool.Spec.Configuration.Name {
					ourBuild = build
					klog.V(4).Infof("Found current MachineOSBuild %q from annotation for MachineOSConfig %q", currentBuildName, ourConfig.Name)
					return ourConfig, ourBuild, nil
				}
				klog.Warningf("MachineOSBuild %q from annotation is for rendered config %q, but pool has %q - annotation is stale", currentBuildName, build.Spec.MachineConfig.Name, pool.Spec.Configuration.Name)
				// Don't return here - fall through to the fallback logic
				break
			}
		}
		klog.Warningf("MachineOSConfig %q has current-machine-os-build annotation pointing to %q, but that build was not found", ourConfig.Name, currentBuildName)
	}

	// Fallback: if annotation is not present or build not found, use the old logic
	// This handles backwards compatibility and edge cases
	for _, build := range buildList {
		if build.Spec.MachineOSConfig.Name == ourConfig.Name && build.Spec.MachineConfig.Name == pool.Spec.Configuration.Name {
			ourBuild = build
			klog.V(4).Infof("Using fallback logic to find MachineOSBuild %q for MachineOSConfig %q and rendered config %q", build.Name, ourConfig.Name, pool.Spec.Configuration.Name)
			break
		}
	}

	return ourConfig, ourBuild, nil
}

func (ctrl *Controller) canLayeredContinue(mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild) (string, bool, error) {
	if mosc == nil && mosb != nil {
		msg := fmt.Sprintf("orphaned MachineOSBuild %q found, but MachineOSConfig %q not found", mosb.Name, mosb.Labels[buildconstants.MachineOSConfigNameLabelKey])
		return msg, false, fmt.Errorf("%s", msg)
	}

	if !ctrl.isConfigAndBuildPresent(mosc, mosb) {
		return "No MachineOSConfig or MachineOSBuild for this pool", false, nil
	}

	cs := ctrlcommon.NewMachineOSConfigState(mosc)
	bs := ctrlcommon.NewMachineOSBuildState(mosb)

	hasImage := cs.HasOSImage()
	pullspec := cs.GetOSImage()

	if !hasImage {
		return "Desired image not set in MachineOSConfig", false, nil
	}

	switch {
	// If the build is successful and the MachineOSConfig has the matching pullspec, we can proceed
	// with rolling out the new OS image.
	case bs.IsBuildSuccess() && hasImage && cs.MachineOSBuildIsCurrent(mosb):
		msg := fmt.Sprintf("Image built successfully, pullspec: %s", pullspec)
		return msg, true, nil
	case bs.IsBuildSuccess() && hasImage && !cs.MachineOSBuildIsCurrent(mosb):
		msg := fmt.Sprintf("Image built successfully, pullspec: %s, but MachineOSConfig %q has not updated yet", pullspec, mosc.Name)
		return msg, false, nil
	case bs.IsBuildPending():
		return "Image build pending", false, nil
	case bs.IsBuilding():
		return "Image build in progress", false, nil
	case bs.IsBuildFailure():
		return "Image build failed", false, fmt.Errorf("image build for MachineConfigPool %s failed", mosb.Name)
	default:
		return "Image is not ready yet", false, nil
	}
}

// syncMachineConfigPool will sync the machineconfig pool with the given key.
// This function is not meant to be invoked concurrently with the same key.
//
//nolint:gocyclo
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
	if errors.IsNotFound(err) {
		klog.V(2).Infof("MachineConfigPool %v has been deleted", key)
		return nil
	}
	if err != nil {
		return err
	}

	if machineconfigpool.Spec.Configuration.Name == "" {
		// Previously we spammed the logs about empty pools.
		// Let's just pause for a bit here to let the renderer
		// initialize them.
		klog.Infof("Pool %s is unconfigured, pausing %v for renderer to initialize", name, ctrl.updateDelay)
		time.Sleep(ctrl.updateDelay)
		return nil
	}

	// Deep-copy otherwise we are mutating our cache.
	// TODO: Deep-copy only when needed.
	pool := machineconfigpool.DeepCopy()
	everything := metav1.LabelSelector{}

	if reflect.DeepEqual(pool.Spec.NodeSelector, &everything) {
		ctrl.eventRecorder.Eventf(pool, corev1.EventTypeWarning, "SelectingAll", "This machineconfigpool is selecting all nodes. A non-empty selector is required.")
		return nil
	}

	if pool.DeletionTimestamp != nil {
		return ctrl.syncStatusOnly(pool)
	}

	if pool.Spec.Paused {
		if apihelpers.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolUpdating) {
			klog.Infof("Pool %s is paused and will not update.", pool.Name)
		}
		return ctrl.syncStatusOnly(pool)
	}

	mosc, mosb, layered, err := ctrl.getConfigAndBuildAndLayeredStatus(pool)
	if err != nil {
		return fmt.Errorf("could not get config and build: %w", err)
	}

	if layered {
		klog.V(4).Infof("Continuing updates for layered pool %s", pool.Name)
		reason, canApplyUpdates, err := ctrl.canLayeredContinue(mosc, mosb)
		if err != nil {
			klog.Infof("Layered pool %s encountered an error: %s", pool.Name, err)
			return err
		}

		if !canApplyUpdates {
			// The MachineConfigPool is not ready to continue, so requeue.
			klog.Infof("Requeueing layered pool %s: %s", pool.Name, reason)
			return ctrl.syncStatusOnly(pool)
		}
	}

	nodes, err := ctrl.getNodesForPool(pool)
	if err != nil {
		if syncErr := ctrl.syncStatusOnly(pool); syncErr != nil {
			errs := kubeErrs.NewAggregate([]error{syncErr, err})
			return fmt.Errorf("error getting nodes for pool %q, sync error: %w", pool.Name, errs)
		}
		return err
	}
	maxunavail, err := maxUnavailable(pool, nodes)
	if err != nil {
		if syncErr := ctrl.syncStatusOnly(pool); syncErr != nil {
			errs := kubeErrs.NewAggregate([]error{syncErr, err})
			return fmt.Errorf("error getting max unavailable count for pool %q, sync error: %w", pool.Name, errs)
		}
		return err
	}

	if err := ctrl.setClusterConfigAnnotation(nodes); err != nil {
		return fmt.Errorf("error setting clusterConfig Annotation for node in pool %q, error: %w", pool.Name, err)
	}
	// Taint all the nodes in the node pool, irrespective of their upgrade status.
	ctx := context.TODO()
	for _, node := range nodes {
		// All the nodes that need to be upgraded should have `NodeUpdateInProgressTaint` so that they're less likely
		// to be chosen during the scheduling cycle. This includes nodes which are:
		// (i) In a Pool being updated to a new MC or image
		// (ii) In a Pool that is being opted out of layering
		hasInProgressTaint := checkIfNodeHasInProgressTaint(node)

		lns := ctrlcommon.NewLayeredNodeState(node)

		if (!layered && lns.IsDesiredMachineConfigEqualToPool(pool) && !lns.AreImageAnnotationsPresentOnNode()) || (layered && lns.IsDesiredEqualToBuild(mosc, mosb)) {
			if hasInProgressTaint {
				if err := ctrl.removeUpdateInProgressTaint(ctx, node.Name); err != nil {
					err = fmt.Errorf("failed removing %s taint for node %s: %w", constants.NodeUpdateInProgressTaint.Key, node.Name, err)
					klog.Error(err)
				}
			}
		} else {
			if !hasInProgressTaint {
				if err := ctrl.setUpdateInProgressTaint(ctx, node.Name); err != nil {
					err = fmt.Errorf("failed applying %s taint for node %s: %w", constants.NodeUpdateInProgressTaint.Key, node.Name, err)
					klog.Error(err)
				}
			}
		}
	}
	candidates, capacity := getAllCandidateMachines(layered, mosc, mosb, pool, nodes, maxunavail)
	if len(candidates) > 0 {
		zones := make(map[string]bool)
		for _, candidate := range candidates {
			zone, ok := candidate.Labels[zoneLabel]
			if ok {
				zones[zone] = true
			}
		}
		ctrl.logPool(pool, "%d candidate nodes in %d zones for update, capacity: %d", len(candidates), len(zones), capacity)
		if err := ctrl.updateCandidateMachines(layered, mosc, mosb, pool, candidates, capacity); err != nil {
			if syncErr := ctrl.syncStatusOnly(pool); syncErr != nil {
				errs := kubeErrs.NewAggregate([]error{syncErr, err})
				return fmt.Errorf("error setting annotations for pool %q, sync error: %w", pool.Name, errs)
			}
			return err
		}
		ctrlcommon.UpdateStateMetric(ctrlcommon.MCCSubControllerState, "machine-config-controller-node", "Sync Machine Config Pool", pool.Name)
	}

	if err := ctrl.syncStatusOnly(pool); err != nil {
		return err
	}

	// Update metrics after syncing the pool status
	return ctrl.syncMetrics()
}

// checkIfNodeHasInProgressTaint checks if the given node has in progress taint
func checkIfNodeHasInProgressTaint(node *corev1.Node) bool {
	for _, taint := range node.Spec.Taints {
		if taint.MatchTaint(constants.NodeUpdateInProgressTaint) {
			return true
		}
	}
	return false
}

func (ctrl *Controller) getNodesForPool(pool *mcfgv1.MachineConfigPool) ([]*corev1.Node, error) {
	selector, err := metav1.LabelSelectorAsSelector(pool.Spec.NodeSelector)
	if err != nil {
		return nil, fmt.Errorf("invalid label selector: %w", err)
	}

	initialNodes, err := ctrl.nodeLister.List(selector)
	if err != nil {
		return nil, err
	}

	nodes := []*corev1.Node{}
	for _, n := range initialNodes {
		p, err := ctrl.getPrimaryPoolForNode(n)
		if err != nil {
			klog.Warningf("can't get pool for node %q: %v", n.Name, err)
			continue
		}
		if p == nil {
			continue
		}
		if p.Name != pool.Name {
			continue
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

// setClusterConfigAnnotation reads cluster configs set into controllerConfig
// and add/updates required annotation to node such as ControlPlaneTopology
// from infrastructure object.
func (ctrl *Controller) setClusterConfigAnnotation(nodes []*corev1.Node) error {
	cc, err := ctrl.ccLister.Get(ctrlcommon.ControllerConfigName)
	if err != nil {
		return err
	}

	for _, node := range nodes {
		if node.Annotations[daemonconsts.ClusterControlPlaneTopologyAnnotationKey] != string(cc.Spec.Infra.Status.ControlPlaneTopology) {
			oldAnn := node.Annotations[daemonconsts.ClusterControlPlaneTopologyAnnotationKey]
			_, err := internal.UpdateNodeRetry(ctrl.kubeClient.CoreV1().Nodes(), ctrl.nodeLister, node.Name, func(node *corev1.Node) {
				node.Annotations[daemonconsts.ClusterControlPlaneTopologyAnnotationKey] = string(cc.Spec.Infra.Status.ControlPlaneTopology)
			})
			if err != nil {
				return err
			}
			klog.Infof("Updated controlPlaneTopology annotation of node %s from %s to %s", node.Name, oldAnn, node.Annotations[daemonconsts.ClusterControlPlaneTopologyAnnotationKey])
		}
	}
	return nil
}

// updateCandidateNode needs to understand MOSB
// specifically, the LayeredNodeState probably needs to understand mosb
func (ctrl *Controller) updateCandidateNode(mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild, nodeName string, pool *mcfgv1.MachineConfigPool, layered bool) error {
	return clientretry.RetryOnConflict(constants.NodeUpdateBackoff, func() error {
		oldNode, err := ctrl.kubeClient.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		oldData, err := json.Marshal(oldNode)
		if err != nil {
			return err
		}

		lns := ctrlcommon.NewLayeredNodeState(oldNode)
		desiredConfig := ""
		desiredImage := ""
		if !layered {
			lns.SetDesiredStateFromPool(pool)
			// If pool is not layered, the desired image annotation is removed (see the `delete`
			// call in `SetDesiredStateFromPool`), so only the desired config version must be set.
			desiredConfig = lns.GetDesiredAnnotationsFromMachineConfigPool(pool)
		} else {
			lns.SetDesiredStateFromMachineOSConfig(mosc, mosb)
			desiredConfig, desiredImage = lns.GetDesiredAnnotationsFromMachineOSConfig(mosc, mosb)
		}

		// Set the desired state to match the pool.
		newData, err := json.Marshal(lns.Node())
		if err != nil {
			return err
		}

		// Don't make a patch call if no update is needed.
		if reflect.DeepEqual(newData, oldData) {
			return nil
		}

		// Populate the desired config version and image annotations in the node's MCN
		err = upgrademonitor.UpdateMachineConfigNodeSpecDesiredAnnotations(ctrl.fgHandler, ctrl.client, nodeName, desiredConfig, desiredImage)
		if err != nil {
			klog.Errorf("error populating MCN for desired config version and image updates: %v", err)
		}

		klog.V(4).Infof("Pool %s: layered=%v node %s update is needed", pool.Name, layered, nodeName)
		patchBytes, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, corev1.Node{})
		if err != nil {
			return fmt.Errorf("failed to create patch for node %q: %w", nodeName, err)
		}
		_, err = ctrl.kubeClient.CoreV1().Nodes().Patch(context.TODO(), nodeName, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
		return err
	})
}

// getAllCandidateMachines returns all possible nodes which can be updated to the target config, along with a maximum
// capacity.  It is the reponsibility of the caller to choose a subset of the nodes given the capacity.
func getAllCandidateMachines(layered bool, config *mcfgv1.MachineOSConfig, build *mcfgv1.MachineOSBuild, pool *mcfgv1.MachineConfigPool, nodesInPool []*corev1.Node, maxUnavailable int) ([]*corev1.Node, uint) {
	unavail := getUnavailableMachines(nodesInPool)
	if len(unavail) >= maxUnavailable {
		klog.V(4).Infof("getAllCandidateMachines: No capacity left for pool %s (unavail=%d >= maxUnavailable=%d)",
			pool.Name, len(unavail), maxUnavailable)
		return nil, 0
	}
	capacity := maxUnavailable - len(unavail)
	klog.V(4).Infof("getAllCandidateMachines: Computed capacity=%d for pool %s", capacity, pool.Name)

	failingThisConfig := 0
	// We only look at nodes which aren't already targeting our desired config
	var nodes []*corev1.Node
	for _, node := range nodesInPool {
		lns := ctrlcommon.NewLayeredNodeState(node)
		if !lns.CheckNodeCandidacyForUpdate(layered, pool, config, build) {
			if lns.IsNodeMCDFailing() {
				failingThisConfig++
			}
			continue
		}
		// Ignore nodes that are currently mid-update or unscheduled
		if !lns.IsNodeReady() {
			klog.V(4).Infof("node %s skipped during candidate selection as it is currently unscheduled", node.Name)
			continue
		}
		klog.Infof("Pool %s: selected candidate node %s", pool.Name, node.Name)
		nodes = append(nodes, node)
	}
	// Nodes which are failing to target this config also count against
	// availability - it might be a transient issue, and if the issue
	// clears we don't want multiple to update at once.
	if failingThisConfig >= capacity {
		return nil, 0
	}
	capacity -= failingThisConfig

	if capacity < 0 {
		return nil, 0
	}
	return nodes, uint(capacity)
}

// getOperatorPodNodeName fetches the name of the current node running the machine-config-operator pod
func (ctrl *Controller) getOperatorNodeName() (string, error) {
	// Create a selector object with  a filter on the machine-config-operator pod
	parsedSelector, err := labels.Parse("k8s-app=machine-config-operator")
	if err != nil {
		klog.Infof("Fetching machine-config-operator pod selector object failed: %v", err)
		return "", err
	}
	// Query pod list with selector, this slice should only have 1 element
	podList, err := ctrl.podLister.List(parsedSelector)
	if err != nil || len(podList) != 1 {
		klog.Infof("Fetching machine-config-operator pod object failed: %v", err)
		return "", err
	}
	return podList[0].Spec.NodeName, err
}

// filterControlPlaneCandidateNodes adjusts the candidates and capacity specifically
// for the control plane, e.g. based on which node is running the operator node at the time.
// nolint:unparam
func (ctrl *Controller) filterControlPlaneCandidateNodes(pool *mcfgv1.MachineConfigPool, candidates []*corev1.Node, capacity uint) ([]*corev1.Node, uint, error) {
	if len(candidates) <= 1 {
		return candidates, capacity, nil
	}
	operatorNodeName, err := ctrl.getOperatorNodeName()
	if err != nil {
		klog.Warningf("Failed to find current operator node (continuing anyways): %v", err)
	}
	var newCandidates []*corev1.Node
	for _, node := range candidates {
		if node.Name == operatorNodeName {
			ctrl.eventRecorder.Eventf(pool, corev1.EventTypeNormal, "DeferringOperatorNodeUpdate", "Deferring update of machine config operator node %s", node.Name)
			klog.Infof("Deferring update of machine config operator node: %s", node.Name)
			continue
		}
		newCandidates = append(newCandidates, node)
	}
	return newCandidates, capacity, nil
}

// filterCustomPoolBootedNodes adjusts the candidate list if a node that directly booted
// into a custom pool is found and updates the node with the appropriate label.
func (ctrl *Controller) filterCustomPoolBootedNodes(candidates []*corev1.Node) []*corev1.Node {
	var newCandidates []*corev1.Node
	for _, node := range candidates {
		isCustomBootNode, poolName := ctrl.isCustomPoolBootedNode(node)
		if isCustomBootNode {
			if err := ctrl.applyCustomPoolLabels(node, poolName); err != nil {
				// best effort, log on failure, keep in candidate list
				klog.Errorf("Failed to apply custom pool labels to node %s: %v", node.Name, err)
			} else {
				// On a successful update of the custom pool label, remove it from the candidate list
				klog.Infof("Node %s was booted on custom pool %s; dropping from candidate list", node.Name, poolName)
				continue
			}
		}
		newCandidates = append(newCandidates, node)
	}
	return newCandidates
}

// isCustomPoolBootedNode checks if a node directly booted into a custom pool
// by checking if it has the FirstPivotMachineConfigAnnotation and if that
// MachineConfig belongs to a custom pool (not master/worker).
// Returns a boolean and associated custom pool name.
func (ctrl *Controller) isCustomPoolBootedNode(node *corev1.Node) (bool, string) {

	// Check if custom label has already been automatically applied, nothing to do in that case
	_, customPoolApplied := node.Annotations[daemonconsts.CustomPoolLabelsAppliedAnnotationKey]
	if customPoolApplied {
		return false, ""
	}

	// Get first pivot machineConfig, nothing to do if it doesn't exist
	mcName, isFirstBoot := node.Annotations[daemonconsts.FirstPivotMachineConfigAnnotationKey]
	if !isFirstBoot {
		return false, ""
	}

	// Get the MachineConfig to check its owner references
	mc, err := ctrl.mcLister.Get(mcName)
	if err != nil {
		klog.V(4).Infof("Failed to get MachineConfig %s: %v", mcName, err)
		return false, ""
	}

	// Check if the MachineConfig has an owner reference to a MachineConfigPool
	ownerRefs := mc.GetOwnerReferences()
	if len(ownerRefs) == 0 {
		klog.V(4).Infof("MachineConfig %s has no owner references", mcName)
		return false, ""
	}

	// Get the pool name from the first owner reference
	poolName := ownerRefs[0].Name

	// Return true only if this is NOT a standard master or worker pool, along with poolName
	return poolName != ctrlcommon.MachineConfigPoolMaster && poolName != ctrlcommon.MachineConfigPoolWorker, poolName
}

// applyCustomPoolLabels applies the node selector labels from a custom MachineConfigPool
// to the node if the rendered MachineConfig belongs to a pool other than master/worker.
func (ctrl *Controller) applyCustomPoolLabels(node *corev1.Node, poolName string) error {

	// Get the MachineConfigPool
	pool, err := ctrl.mcpLister.Get(poolName)
	if err != nil {
		return fmt.Errorf("failed to get MachineConfigPool %s: %w", poolName, err)
	}

	// Extract labels from the pool's node selector
	if pool.Spec.NodeSelector == nil || pool.Spec.NodeSelector.MatchLabels == nil {
		klog.V(4).Infof("MachineConfigPool %s has no node selector labels", poolName)
		return nil
	}

	labelsToApply := pool.Spec.NodeSelector.MatchLabels
	if len(labelsToApply) == 0 {
		return nil
	}

	klog.Infof("Node %s was booted into custom pool %s; applying node selector labels: %v", poolName, node.Name, labelsToApply)

	// Apply the labels to the node and add annotation indicating custom pool labels were applied
	_, err = internal.UpdateNodeRetry(ctrl.kubeClient.CoreV1().Nodes(), ctrl.nodeLister, node.Name, func(node *corev1.Node) {
		// Apply the custom pool labels
		maps.Copy(node.Labels, labelsToApply)

		// Add annotation to signal that custom pool labels were automatically applied
		node.Annotations[daemonconsts.CustomPoolLabelsAppliedAnnotationKey] = ""
	})
	if err != nil {
		return fmt.Errorf("failed to apply custom pool labels to node %s: %w", node.Name, err)
	}

	klog.Infof("Successfully applied custom pool labels to node %s", node.Name)
	return nil
}

// SetDesiredStateFromPool in old mco explains how this works. Somehow you need to NOT FAIL if the mosb doesn't exist. So
// we still need to base this whole things on pools but isLayeredPool == does mosb exist
// updateCandidateMachines sets the desiredConfig annotation the candidate machines
func (ctrl *Controller) updateCandidateMachines(layered bool, mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild, pool *mcfgv1.MachineConfigPool, candidates []*corev1.Node, capacity uint) error {
	if pool.Name == ctrlcommon.MachineConfigPoolMaster {
		var err error
		candidates, capacity, err = ctrl.filterControlPlaneCandidateNodes(pool, candidates, capacity)
		if err != nil {
			return err
		}
		// In practice right now these counts will be 1 but let's stay general to support 5 etcd nodes in the future
		ctrl.logPool(pool, "filtered to %d candidate nodes for update, capacity: %d", len(candidates), capacity)
	}
	// Filter out any nodes that have booted into a custom pool from candidate list
	candidates = ctrl.filterCustomPoolBootedNodes(candidates)
	if len(candidates) == 0 {
		return nil
	}
	if capacity < uint(len(candidates)) {
		// when list is longer than maxUnavailable, rollout nodes in zone order, zones without zone label
		// are done last from oldest to youngest. this reduces likelihood of randomly picking nodes
		// across multiple zones that run the same types of pods resulting in an outage in HA clusters
		candidates = sortNodeList(candidates)

		candidates = candidates[:capacity]
	}

	return ctrl.setDesiredAnnotations(layered, mosc, mosb, pool, candidates)
}

func (ctrl *Controller) setDesiredAnnotations(layered bool, mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild, pool *mcfgv1.MachineConfigPool, candidates []*corev1.Node) error {
	eventName := "SetDesiredConfig"
	updateName := fmt.Sprintf("MachineConfig: %s", pool.Spec.Configuration.Name)

	if layered {
		eventName = "SetDesiredConfigAndOSImage"
		moscImage := ctrlcommon.NewMachineOSConfigState(mosc).GetOSImage()
		updateName = fmt.Sprintf("%s / Image: %s", updateName, moscImage)
	}

	for _, node := range candidates {
		if err := ctrl.updateCandidateNode(mosc, mosb, node.Name, pool, layered); err != nil {
			return fmt.Errorf("setting desired %s for node %s: %w", pool.Spec.Configuration.Name, node.Name, err)
		}
	}

	if len(candidates) == 1 {
		candidate := candidates[0]
		ctrl.eventRecorder.Eventf(pool, corev1.EventTypeNormal, eventName, "Targeted node %s to %s", candidate.Name, updateName)
	} else {
		ctrl.eventRecorder.Eventf(pool, corev1.EventTypeNormal, eventName, "Set target for %d nodes to %s", len(candidates), updateName)
	}

	return nil
}

// sortNodeList sorts the list of candidate nodes by label topology.kubernetes.io/zone
// nodes without label are at end of list and sorted by age (oldest to youngest)
func sortNodeList(nodes []*corev1.Node) []*corev1.Node {
	sort.Slice(nodes, func(i, j int) bool {
		iZone, iOk := nodes[i].Labels[zoneLabel]
		jZone, jOk := nodes[j].Labels[zoneLabel]

		switch {
		case iOk && jOk:
			if iZone == jZone {
				// if nodes have same labels, sort by creationTime oldest to newest
				return nodes[i].GetObjectMeta().GetCreationTimestamp().Time.Before(nodes[j].GetObjectMeta().GetCreationTimestamp().Time)
			}
			return iZone < jZone
		case jOk:
			return false
		case !iOk && !jOk:
			// if nodes have no labels, sort by creationTime oldest to newest
			return nodes[i].GetObjectMeta().GetCreationTimestamp().Time.Before(nodes[j].GetObjectMeta().GetCreationTimestamp().Time)
		default:
			return true
		}
	})
	return nodes
}

// setUpdateInProgressTaint applies in progress taint to all the nodes that are to be updated.
// The taint on the individual node is removed by MCC once the update of the node is complete.
// This is to ensure that the updated nodes are being preferred to non-updated nodes there by
// reducing the number of reschedules.
func (ctrl *Controller) setUpdateInProgressTaint(ctx context.Context, nodeName string) error {
	return clientretry.RetryOnConflict(constants.NodeUpdateBackoff, func() error {
		oldNode, err := ctrl.kubeClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		oldData, err := json.Marshal(oldNode)
		if err != nil {
			return err
		}

		newNode := oldNode.DeepCopy()
		if newNode.Spec.Taints == nil {
			newNode.Spec.Taints = []corev1.Taint{}
		}

		newNode.Spec.Taints = append(newNode.Spec.Taints, *constants.NodeUpdateInProgressTaint)
		newData, err := json.Marshal(newNode)
		if err != nil {
			return err
		}

		patchBytes, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, corev1.Node{})
		if err != nil {
			return fmt.Errorf("failed to create patch for node %q: %v", nodeName, err)
		}
		_, err = ctrl.kubeClient.CoreV1().Nodes().Patch(ctx, nodeName, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
		return err
	})
}

// removeUpdateInProgressTaint removes the update in progress taint for given node.
func (ctrl *Controller) removeUpdateInProgressTaint(ctx context.Context, nodeName string) error {
	return clientretry.RetryOnConflict(constants.NodeUpdateBackoff, func() error {
		oldNode, err := ctrl.kubeClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		oldData, err := json.Marshal(oldNode)
		if err != nil {
			return err
		}

		newNode := oldNode.DeepCopy()

		// New taints to be copied.
		var taintsAfterUpgrade []corev1.Taint
		for _, taint := range newNode.Spec.Taints {
			if taint.MatchTaint(constants.NodeUpdateInProgressTaint) {
				continue
			}
			taintsAfterUpgrade = append(taintsAfterUpgrade, taint)
		}

		// Remove the NodeUpdateInProgressTaint.
		newNode.Spec.Taints = taintsAfterUpgrade
		newData, err := json.Marshal(newNode)
		if err != nil {
			return err
		}

		patchBytes, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, corev1.Node{})
		if err != nil {
			return fmt.Errorf("failed to create patch for node %q: %w", nodeName, err)
		}
		_, err = ctrl.kubeClient.CoreV1().Nodes().Patch(ctx, nodeName, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
		return err
	})
}

func maxUnavailable(pool *mcfgv1.MachineConfigPool, nodes []*corev1.Node) (int, error) {
	intOrPercent := intstrutil.FromInt(1)
	if pool.Spec.MaxUnavailable != nil {
		intOrPercent = *pool.Spec.MaxUnavailable
	}
	maxunavail, err := intstrutil.GetScaledValueFromIntOrPercent(&intOrPercent, len(nodes), false)
	if err != nil {
		return 0, fmt.Errorf("\"maxUnavailable\" %v", err)
	}
	if maxunavail == 0 {
		maxunavail = 1
	}
	return maxunavail, nil
}

// getErrorString returns error string if not nil and empty string if error is nil
func getErrorString(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}

func (ctrl *Controller) isLayeredPool(mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild) bool {
	return ctrl.isConfigOrBuildPresent(mosc, mosb)
}

func (ctrl *Controller) isConfigOrBuildPresent(mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild) bool {
	return (mosc != nil || mosb != nil)
}

func (ctrl *Controller) isConfigAndBuildPresent(mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild) bool {
	return (mosc != nil && mosb != nil)
}

// syncMetrics updates the metrics for all pools
func (ctrl *Controller) syncMetrics() error {
	pools, err := ctrl.mcpLister.List(labels.Everything())
	if err != nil {
		return err
	}
	// set metrics per pool, we need to get the latest condition to log for the state
	var latestTime metav1.Time
	latestTime.Time = time.Time{}
	var cond mcfgv1.MachineConfigPoolCondition
	for _, pool := range pools {
		for _, condition := range pool.Status.Conditions {
			if condition.Status == corev1.ConditionTrue && condition.LastTransitionTime.After(latestTime.Time) {
				cond = condition
				latestTime = cond.LastTransitionTime
			}
		}

		nodes, _ := helpers.GetNodesForPool(ctrl.mcpLister, ctrl.nodeLister, pool)
		for _, node := range nodes {
			ctrlcommon.MCCState.WithLabelValues(node.Name, pool.Name, string(cond.Type), cond.Reason).SetToCurrentTime()
		}
		ctrlcommon.MCCMachineCount.WithLabelValues(pool.Name).Set(float64(pool.Status.MachineCount))
		ctrlcommon.MCCUpdatedMachineCount.WithLabelValues(pool.Name).Set(float64(pool.Status.UpdatedMachineCount))
		ctrlcommon.MCCDegradedMachineCount.WithLabelValues(pool.Name).Set(float64(pool.Status.DegradedMachineCount))
		ctrlcommon.MCCUnavailableMachineCount.WithLabelValues(pool.Name).Set(float64(pool.Status.UnavailableMachineCount))
	}
	return nil
}

// addMachineConfiguration handles MachineConfiguration add events to update the boot image skew enforcement metric.
func (ctrl *Controller) addMachineConfiguration(obj any) {
	if ctrl.fgHandler == nil || !ctrl.fgHandler.Enabled(features.FeatureGateBootImageSkewEnforcement) {
		return
	}

	ctrl.syncBootImageSkewEnforcementMetric(obj)
}

// updateMachineConfiguration handles MachineConfiguration update events to update the boot image skew enforcement metric.
// Only takes action if BootImageSkewEnforcementStatus has changed.
func (ctrl *Controller) updateMachineConfiguration(old, cur any) {
	if ctrl.fgHandler == nil || !ctrl.fgHandler.Enabled(features.FeatureGateBootImageSkewEnforcement) {
		return
	}

	oldMCOP, ok := old.(*opv1.MachineConfiguration)
	if !ok {
		return
	}
	curMCOP, ok := cur.(*opv1.MachineConfiguration)
	if !ok {
		return
	}

	// Only update metric if BootImageSkewEnforcementStatus mode changed
	if oldMCOP.Status.BootImageSkewEnforcementStatus.Mode == curMCOP.Status.BootImageSkewEnforcementStatus.Mode {
		return
	}

	ctrl.syncBootImageSkewEnforcementMetric(cur)
}

// deleteMachineConfiguration handles MachineConfiguration delete events to reset the boot image skew enforcement metric.
func (ctrl *Controller) deleteMachineConfiguration(_ any) {
	if ctrl.fgHandler == nil || !ctrl.fgHandler.Enabled(features.FeatureGateBootImageSkewEnforcement) {
		return
	}

	// Reset metric to 0 when MachineConfiguration is deleted
	ctrlcommon.MCCBootImageSkewEnforcementNone.Set(0)
}

// syncBootImageSkewEnforcementMetric updates the mcc_boot_image_skew_enforcement_none metric
// based on the current BootImageSkewEnforcementStatus mode in MachineConfiguration.
// The metric is set to 1 when mode is "None", indicating that scaling operations may
// not be successful.
func (ctrl *Controller) syncBootImageSkewEnforcementMetric(obj any) {

	mcop, ok := obj.(*opv1.MachineConfiguration)
	if !ok {
		klog.Warningf("Expected MachineConfiguration object, got %T", obj)
		return
	}

	if mcop.Status.BootImageSkewEnforcementStatus.Mode == opv1.BootImageSkewEnforcementModeStatusNone {
		ctrlcommon.MCCBootImageSkewEnforcementNone.Set(1)
	} else {
		ctrlcommon.MCCBootImageSkewEnforcementNone.Set(0)
	}
}

// migrateMCNConditionsToImageModeStatusReporting migrates MCN condition formats from the legacy
// MachineConfigNodeUpdateFilesAndOS condition to the new ImageModeStatusReporting conditions
// (MachineConfigNodeUpdateFiles, MachineConfigNodeUpdateOS, and MachineConfigNodeImagePulledFromRegistry).
// Removes the legacy condition and adds the new conditions with appropriate default values.
// Returns the number of MCNs that had their conditions migrated.
func (ctrl *Controller) migrateMCNConditionsToImageModeStatusReporting() (int, error) {
	// Get all MachineConfigNodes
	mcns, err := ctrl.client.MachineconfigurationV1().MachineConfigNodes().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return 0, fmt.Errorf("failed to list MachineConfigNodes for condition migration: %w", err)
	}

	// Loop through all the cluster's MCNs and migrate them if needed
	migratedCount := 0
	for _, mcn := range mcns.Items {
		// Check if the legacy condition exists and filter it out
		hasLegacyCondition := false
		// If we need to clean the legacy condition, the new conditions should not exist, but we
		// will check to fully prevent condition duplication
		needsUpdateFiles := true
		needsUpdateOS := true
		needsImagePulledFromRegistry := true
		var newConditions []metav1.Condition
		for _, condition := range mcn.Status.Conditions {
			//nolint:gocritic // (ijanssen) the linter thinks this block would be clearer as a switch statement, but I disagree
			if condition.Type == string(mcfgv1.MachineConfigNodeUpdateFilesAndOS) {
				hasLegacyCondition = true
				klog.V(4).Infof("Removing legacy MachineConfigNodeUpdateFilesAndOS condition from MCN %s", mcn.Name)
				continue // Skip adding this condition to the new list
			} else if condition.Type == string(mcfgv1.MachineConfigNodeUpdateFiles) {
				needsUpdateFiles = false
			} else if condition.Type == string(mcfgv1.MachineConfigNodeUpdateOS) {
				needsUpdateOS = false
			} else if condition.Type == string(mcfgv1.MachineConfigNodeImagePulledFromRegistry) {
				needsImagePulledFromRegistry = false
			}

			newConditions = append(newConditions, condition)
		}

		// Only update the MCN if we found and removed the legacy condition or if any new
		// conditions need to be added
		if hasLegacyCondition || needsUpdateOS || needsUpdateFiles || needsImagePulledFromRegistry {
			// Add the new ImageModeStatusReporting conditions with default values only if they don't exist
			now := metav1.Now()

			if needsUpdateFiles {
				defaultCondition := metav1.Condition{
					Type:               string(mcfgv1.MachineConfigNodeUpdateFiles),
					Message:            fmt.Sprintf("This node has not yet entered the %s phase", string(mcfgv1.MachineConfigNodeUpdateFiles)),
					Reason:             "NotYetOccurred",
					LastTransitionTime: now,
					Status:             metav1.ConditionFalse,
				}
				newConditions = append(newConditions, defaultCondition)
			}

			if needsUpdateOS {
				defaultCondition := metav1.Condition{
					Type:               string(mcfgv1.MachineConfigNodeUpdateOS),
					Message:            fmt.Sprintf("This node has not yet entered the %s phase", string(mcfgv1.MachineConfigNodeUpdateOS)),
					Reason:             "NotYetOccurred",
					LastTransitionTime: now,
					Status:             metav1.ConditionFalse,
				}
				newConditions = append(newConditions, defaultCondition)
			}

			if needsImagePulledFromRegistry {
				defaultCondition := metav1.Condition{
					Type:               string(mcfgv1.MachineConfigNodeImagePulledFromRegistry),
					Message:            fmt.Sprintf("This node has not yet entered the %s phase", string(mcfgv1.MachineConfigNodeImagePulledFromRegistry)),
					Reason:             "NotYetOccurred",
					LastTransitionTime: now,
					Status:             metav1.ConditionFalse,
				}
				newConditions = append(newConditions, defaultCondition)
			}

			mcnCopy := mcn.DeepCopy()
			mcnCopy.Status.Conditions = newConditions
			_, err = ctrl.client.MachineconfigurationV1().MachineConfigNodes().UpdateStatus(context.TODO(), mcnCopy, metav1.UpdateOptions{})
			if err != nil {
				return migratedCount, fmt.Errorf("failed to update MCN %s during condition migration to ImageModeStatusReporting: %w", mcn.Name, err)
			}
			migratedCount++

			// Create descriptive log message based on what was done
			var actions []string
			if hasLegacyCondition {
				actions = append(actions, "removed legacy MachineConfigNodeUpdateFilesAndOS condition")
			}
			if needsUpdateFiles || needsUpdateOS || needsImagePulledFromRegistry {
				var addedConditions []string
				if needsUpdateFiles {
					addedConditions = append(addedConditions, "MachineConfigNodeUpdateFiles")
				}
				if needsUpdateOS {
					addedConditions = append(addedConditions, "MachineConfigNodeUpdateOS")
				}
				if needsImagePulledFromRegistry {
					addedConditions = append(addedConditions, "MachineConfigNodeImagePulledFromRegistry")
				}
				actions = append(actions, fmt.Sprintf("added %s condition(s)", strings.Join(addedConditions, ", ")))
			}

			klog.Infof("Successfully migrated conditions for MCN %s: %s", mcn.Name, strings.Join(actions, " and "))
		}
	}

	return migratedCount, nil
}

// performImageModeStatusReportingConditionMigration runs once at controller startup to migrate MCN conditions
// from legacy MachineConfigNodeUpdateFilesAndOS condition to new ImageModeStatusReporting condition format.
// This runs in a goroutine to avoid blocking controller startup.
func (ctrl *Controller) performImageModeStatusReportingConditionMigration() {
	klog.Info("Starting one-time MCN condition migration to ImageModeStatusReporting format")

	migratedCount, err := ctrl.migrateMCNConditionsToImageModeStatusReporting()
	if err != nil {
		klog.Errorf("Failed to migrate MCN condition formats to ImageModeStatusReporting: %v", err)
		return
	}

	if migratedCount > 0 {
		klog.Infof("Completed MCN condition migration to ImageModeStatusReporting format for %d total nodes", migratedCount)
	} else {
		klog.Info("No MCN condition migration to ImageModeStatusReporting format required")
	}
}
