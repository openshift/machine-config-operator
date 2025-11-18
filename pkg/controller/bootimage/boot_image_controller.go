package bootimage

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	features "github.com/openshift/api/features"
	opv1 "github.com/openshift/api/operator/v1"
	configinformersv1 "github.com/openshift/client-go/config/informers/externalversions/config/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	mcopclientset "github.com/openshift/client-go/operator/clientset/versioned"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	k8sversion "k8s.io/apimachinery/pkg/util/version"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformersv1 "k8s.io/client-go/informers/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	corelisterv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/kubectl/pkg/scheme"

	machinev1 "github.com/openshift/api/machine/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	machineclientset "github.com/openshift/client-go/machine/clientset/versioned"
	mapimachineinformersv1 "github.com/openshift/client-go/machine/informers/externalversions/machine/v1"
	mapimachineinformersv1beta1 "github.com/openshift/client-go/machine/informers/externalversions/machine/v1beta1"
	machinelistersv1 "github.com/openshift/client-go/machine/listers/machine/v1"
	machinelistersv1beta1 "github.com/openshift/client-go/machine/listers/machine/v1beta1"

	mcopinformersv1 "github.com/openshift/client-go/operator/informers/externalversions/operator/v1"
	mcoplistersv1 "github.com/openshift/client-go/operator/listers/operator/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

// Controller defines the machine-set-boot-image controller.
type Controller struct {
	kubeClient    clientset.Interface
	machineClient machineclientset.Interface
	mcopClient    mcopclientset.Interface
	eventRecorder record.EventRecorder

	syncHandler func(event string) error

	mcoCmLister          corelisterv1.ConfigMapLister
	mapiMachineSetLister machinelistersv1beta1.MachineSetLister
	cpmsLister           machinelistersv1.ControlPlaneMachineSetLister
	infraLister          configlistersv1.InfrastructureLister
	mcopLister           mcoplistersv1.MachineConfigurationLister
	clusterVersionLister configlistersv1.ClusterVersionLister

	mcoCmListerSynced          cache.InformerSynced
	mapiMachineSetListerSynced cache.InformerSynced
	cpmsListerSynced           cache.InformerSynced
	infraListerSynced          cache.InformerSynced
	mcopListerSynced           cache.InformerSynced
	clusterVersionListerSynced cache.InformerSynced

	queue workqueue.TypedRateLimitingInterface[string]

	mapiStats                  MachineResourceStats
	cpmsStats                  MachineResourceStats
	capiMachineSetStats        MachineResourceStats
	capiMachineDeploymentStats MachineResourceStats
	mapiBootImageState         map[string]BootImageState
	cpmsBootImageState         map[string]BootImageState

	fgHandler ctrlcommon.FeatureGatesHandler
}

// Stats structure for local bookkeeping of machine resources
type MachineResourceStats struct {
	inProgress   int
	skippedCount int
	erroredCount int
	totalCount   int
}

// State structure uses for detecting hot loops. Reset when cluster is opted
// out of boot image updates.
// nolint: revive
type BootImageState struct {
	value        []byte
	hotLoopCount int
}

// isFinished checks if all resources have been evaluated
func (mrs MachineResourceStats) isFinished() bool {
	return mrs.totalCount == (mrs.inProgress + mrs.erroredCount)
}

func (mrs MachineResourceStats) getProgressingStatusMessage(name string) string {
	if mrs.skippedCount > 0 {
		return fmt.Sprintf("Reconciled %d of %d %s (%d skipped)", mrs.inProgress-mrs.skippedCount, mrs.totalCount, name, mrs.skippedCount)
	}
	return fmt.Sprintf("Reconciled %d of %d %s", mrs.inProgress, mrs.totalCount, name)
}

func (mrs MachineResourceStats) getDegradedStatusMessage(name string) string {
	return fmt.Sprintf("%d Degraded %s", mrs.erroredCount, name)
}

const (
	// Name of machine api namespace
	MachineAPINamespace = "openshift-machine-api"

	// Key to access stream data from the boot images configmap
	StreamConfigMapKey = "stream"

	// Labels and Annotations required for determining architecture of a machineset
	MachineSetArchAnnotationKey = "capacity.cluster-autoscaler.kubernetes.io/labels"

	ArchLabelKey = "kubernetes.io/arch="
	OSLabelKey   = "machine.openshift.io/os-id"

	// Threshold for hot loop detection
	HotLoopLimit = 3

	// maxRetries is the number of times a sync will be retried before it is dropped out of the queue.
	maxRetries = 15
)

// New returns a new machine-set-boot-image controller.
func New(
	kubeClient clientset.Interface,
	machineClient machineclientset.Interface,
	mcoCmInfomer coreinformersv1.ConfigMapInformer,
	mapiMachineSetInformer mapimachineinformersv1beta1.MachineSetInformer,
	cpmsInformer mapimachineinformersv1.ControlPlaneMachineSetInformer,
	infraInformer configinformersv1.InfrastructureInformer,
	mcopClient mcopclientset.Interface,
	mcopInformer mcopinformersv1.MachineConfigurationInformer,
	clusterVersionInformer configinformersv1.ClusterVersionInformer,
	fgHandler ctrlcommon.FeatureGatesHandler,
) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&corev1client.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	ctrl := &Controller{
		kubeClient:    kubeClient,
		machineClient: machineClient,
		mcopClient:    mcopClient,
		eventRecorder: ctrlcommon.NamespacedEventRecorder(eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineconfigcontroller-machinesetbootimagecontroller"})),
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "machineconfigcontroller-machinesetbootimagecontroller"}),
	}

	ctrl.syncHandler = ctrl.syncAll

	ctrl.mcoCmLister = mcoCmInfomer.Lister()
	ctrl.mapiMachineSetLister = mapiMachineSetInformer.Lister()
	ctrl.cpmsLister = cpmsInformer.Lister()
	ctrl.infraLister = infraInformer.Lister()
	ctrl.mcopLister = mcopInformer.Lister()
	ctrl.clusterVersionLister = clusterVersionInformer.Lister()

	ctrl.mcoCmListerSynced = mcoCmInfomer.Informer().HasSynced
	ctrl.mapiMachineSetListerSynced = mapiMachineSetInformer.Informer().HasSynced
	ctrl.cpmsListerSynced = cpmsInformer.Informer().HasSynced
	ctrl.infraListerSynced = infraInformer.Informer().HasSynced
	ctrl.mcopListerSynced = mcopInformer.Informer().HasSynced
	ctrl.clusterVersionListerSynced = clusterVersionInformer.Informer().HasSynced

	mapiMachineSetInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addMAPIMachineSet,
		UpdateFunc: ctrl.updateMAPIMachineSet,
		DeleteFunc: ctrl.deleteMAPIMachineSet,
	})

	if fgHandler.Enabled(features.FeatureGateManagedBootImagesCPMS) {
		klog.V(4).Infof("ManagedBootImagesCPMS feature gate is enabled, adding CPMS event handlers")
		cpmsInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    ctrl.addControlPlaneMachineSet,
			UpdateFunc: ctrl.updateControlPlaneMachineSet,
			DeleteFunc: ctrl.deleteControlPlaneMachineSet,
		})
	}

	mcoCmInfomer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addConfigMap,
		UpdateFunc: ctrl.updateConfigMap,
		DeleteFunc: ctrl.deleteConfigMap,
	})

	mcopInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addMachineConfiguration,
		UpdateFunc: ctrl.updateMachineConfiguration,
		DeleteFunc: ctrl.deleteMachineConfiguration,
	})

	ctrl.fgHandler = fgHandler

	ctrl.mapiBootImageState = map[string]BootImageState{}
	ctrl.cpmsBootImageState = map[string]BootImageState{}

	return ctrl
}

// Run executes the machine-set-boot-image controller.
func (ctrl *Controller) Run(stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()

	if !cache.WaitForCacheSync(stopCh, ctrl.mcoCmListerSynced, ctrl.mapiMachineSetListerSynced, ctrl.infraListerSynced, ctrl.mcopListerSynced, ctrl.clusterVersionListerSynced) {
		return
	}

	klog.Info("Starting MachineConfigController-MachineSetBootImageController")
	defer klog.Info("Shutting down MachineConfigController-MachineSetBootImageController")

	// This controller needs to run in single thread mode, as the work unit per sync are
	// the same and shouldn't overlap each other.
	go wait.Until(ctrl.worker, time.Second, stopCh)

	<-stopCh
}

// enqueueEvent adds a event to the work queue.
func (ctrl *Controller) enqueueEvent(event string) {
	ctrl.queue.Add(event)
}

// worker runs a worker thread that just dequeues items, processes them, and marks them done.
// It enforces that the syncHandler is never invoked concurrently with the same key.
func (ctrl *Controller) worker() {
	for ctrl.processNextWorkItem() {
	}
}

// processNextWorkItem processes the next work item from the queue.
func (ctrl *Controller) processNextWorkItem() bool {
	event, quit := ctrl.queue.Get()
	if quit {
		return false
	}
	defer ctrl.queue.Done(event)

	err := ctrl.syncHandler(event)
	ctrl.handleErr(err, event)

	return true
}

// handleErr checks if an error happened and makes sure we will retry later.
func (ctrl *Controller) handleErr(err error, event string) {
	if err == nil {
		ctrl.queue.Forget(event)
		return
	}

	if ctrl.queue.NumRequeues(event) < maxRetries {
		klog.V(2).Infof("Error syncing boot image controller for event %v: %v", event, err)
		ctrl.queue.AddRateLimited(event)
		return
	}

	utilruntime.HandleError(err)
	klog.V(2).Infof("Dropping event %q out of the queue: %v", event, err)
	ctrl.queue.Forget(event)
	ctrl.queue.AddAfter(event, 1*time.Minute)
}

// addMAPIMachineSet handles the addition of a MAPI MachineSet by triggering
// a reconciliation of all enrolled MAPI MachineSets.
func (ctrl *Controller) addMAPIMachineSet(obj interface{}) {

	machineSet := obj.(*machinev1beta1.MachineSet)

	klog.Infof("MAPI MachineSet %s added, reconciling enrolled machine resources", machineSet.Name)

	// Update/Check all machinesets instead of just this one. This prevents needing to maintain a local
	// store of machineset conditions. As this is using a lister, it is relatively inexpensive to do
	// this.
	ctrl.enqueueEvent("MAPIMachineSetAdded")
}

// updateMAPIMachineSet handles updates to a MAPI MachineSet by triggering
// a reconciliation if the ProviderSpec, labels, annotations, or owner references changed.
func (ctrl *Controller) updateMAPIMachineSet(oldMS, newMS interface{}) {

	oldMachineSet := oldMS.(*machinev1beta1.MachineSet)
	newMachineSet := newMS.(*machinev1beta1.MachineSet)

	// Don't take action if the there is no change in the MachineSet's ProviderSpec, labels, annotations and ownerreferences
	if reflect.DeepEqual(oldMachineSet.Spec.Template.Spec.ProviderSpec, newMachineSet.Spec.Template.Spec.ProviderSpec) &&
		reflect.DeepEqual(oldMachineSet.GetLabels(), newMachineSet.GetLabels()) &&
		reflect.DeepEqual(oldMachineSet.GetAnnotations(), newMachineSet.GetAnnotations()) &&
		reflect.DeepEqual(oldMachineSet.GetOwnerReferences(), newMachineSet.GetOwnerReferences()) {
		return
	}

	klog.Infof("MachineSet %s updated, reconciling enrolled machineset resources", oldMachineSet.Name)

	// Update all machinesets instead of just this one. This prevents needing to maintain a local
	// store of machineset conditions. As this is using a lister, it is relatively inexpensive to do
	// this.
	ctrl.enqueueEvent("MAPIMachineSetUpdated")
}

// deleteMAPIMachineSet handles the deletion of a MAPI MachineSet by triggering
// a reconciliation of all enrolled MAPI MachineSets.
func (ctrl *Controller) deleteMAPIMachineSet(deletedMS interface{}) {

	deletedMachineSet := deletedMS.(*machinev1beta1.MachineSet)

	klog.Infof("MachineSet %s deleted, reconciling enrolled machineset resources", deletedMachineSet.Name)

	// Update all machinesets. This prevents needing to maintain a local
	// store of machineset conditions. As this is using a lister, it is relatively inexpensive to do
	// this.
	ctrl.enqueueEvent("MAPIMachineSetDeleted")
}

// addControlPlaneMachineSet handles the addition of a ControlPlaneMachineSet by triggering
// a reconciliation of all enrolled ControlPlaneMachineSets.
func (ctrl *Controller) addControlPlaneMachineSet(obj interface{}) {

	machineSet := obj.(*machinev1.ControlPlaneMachineSet)

	klog.Infof("ControlPlaneMachineSet %s added, reconciling enrolled machine resources", machineSet.Name)

	// Update/Check all ControlPlaneMachineSets instead of just this one. This prevents needing to maintain a local
	// store of machineset conditions. As this is using a lister, it is relatively inexpensive to do
	// this.
	ctrl.enqueueEvent("ControlPlaneMachineSetAdded")
}

// updateControlPlaneMachineSet handles updates to a ControlPlaneMachineSet by triggering
// a reconciliation if the ProviderSpec, labels, annotations, or owner references changed.
func (ctrl *Controller) updateControlPlaneMachineSet(oldCPMS, newCPMS interface{}) {

	oldMS := oldCPMS.(*machinev1.ControlPlaneMachineSet)
	newMS := newCPMS.(*machinev1.ControlPlaneMachineSet)

	// Don't take action if the there is no change in the MachineSet's ProviderSpec, labels, annotations and ownerreferences
	if reflect.DeepEqual(oldMS.Spec.Template.OpenShiftMachineV1Beta1Machine.Spec.ProviderSpec, newMS.Spec.Template.OpenShiftMachineV1Beta1Machine.Spec.ProviderSpec) &&
		reflect.DeepEqual(oldMS.GetLabels(), newMS.GetLabels()) &&
		reflect.DeepEqual(oldMS.GetAnnotations(), newMS.GetAnnotations()) &&
		reflect.DeepEqual(oldMS.GetOwnerReferences(), newMS.GetOwnerReferences()) {
		return
	}

	klog.Infof("ControlPlaneMachineSet %s updated, reconciling enrolled machineset resources", oldMS.Name)

	// Update all ControlPlaneMachineSets instead of just this one. This prevents needing to maintain a local
	// store of machineset conditions. As this is using a lister, it is relatively inexpensive to do
	// this.
	ctrl.enqueueEvent("ControlPlaneMachineSetUpdated")
}

// deleteControlPlaneMachineSet handles the deletion of a ControlPlaneMachineSet by triggering
// a reconciliation of all enrolled ControlPlaneMachineSets.
func (ctrl *Controller) deleteControlPlaneMachineSet(deletedCPMS interface{}) {

	deletedMachineSet := deletedCPMS.(*machinev1.ControlPlaneMachineSet)

	klog.Infof("ControlPlaneMachineSet %s deleted, reconciling enrolled machineset resources", deletedMachineSet.Name)

	// Update all ControlPlaneMachineSets. This prevents needing to maintain a local
	// store of machineset conditions. As this is using a lister, it is relatively inexpensive to do
	// this.
	ctrl.enqueueEvent("ControlPlaneMachineSetDeleted")
}

// addConfigMap handles the addition of the boot images ConfigMap by triggering
// a reconciliation of all enrolled machine resources.
func (ctrl *Controller) addConfigMap(obj interface{}) {

	configMap := obj.(*corev1.ConfigMap)

	// Take no action if this isn't the "golden" config map
	if configMap.Name != ctrlcommon.BootImagesConfigMapName {
		return
	}

	klog.Infof("configMap %s added, reconciling enrolled machine resources", configMap.Name)

	// Update all machinesets since the "golden" configmap has been added
	ctrl.enqueueEvent("BootImageConfigMapAdded")
}

// updateConfigMap handles updates to the boot images ConfigMap by triggering
// a reconciliation of all enrolled machine resources if the resource version changed.
func (ctrl *Controller) updateConfigMap(oldCM, newCM interface{}) {

	oldConfigMap := oldCM.(*corev1.ConfigMap)
	newConfigMap := newCM.(*corev1.ConfigMap)

	// Take no action if this isn't the "golden" config map
	if oldConfigMap.Name != ctrlcommon.BootImagesConfigMapName {
		return
	}

	// Only take action if the there is an actual change in the configMap Object
	if oldConfigMap.ResourceVersion == newConfigMap.ResourceVersion {
		return
	}

	klog.Infof("configMap %s updated, reconciling enrolled machine resources", oldConfigMap.Name)

	// Update all machinesets since the "golden" configmap has been updated
	ctrl.enqueueEvent("BootImageConfigMapUpdated")
}

// deleteConfigMap handles the deletion of the boot images ConfigMap by triggering
// a reconciliation of all enrolled machine resources.
func (ctrl *Controller) deleteConfigMap(obj interface{}) {

	configMap := obj.(*corev1.ConfigMap)

	// Take no action if this isn't the "golden" config map
	if configMap.Name != ctrlcommon.BootImagesConfigMapName {
		return
	}

	klog.Infof("configMap %s deleted, reconciling enrolled machine resources", configMap.Name)

	// Update all machinesets since the "golden" configmap has been deleted
	ctrl.enqueueEvent("BootImageConfigMapDeleted")
}

// addMachineConfiguration handles the addition of the cluster-level MachineConfiguration
// by triggering a reconciliation of all enrolled machine resources.
func (ctrl *Controller) addMachineConfiguration(obj interface{}) {

	machineConfiguration := obj.(*opv1.MachineConfiguration)

	// Take no action if this isn't the "cluster" level MachineConfiguration object
	if machineConfiguration.Name != ctrlcommon.MCOOperatorKnobsObjectName {
		klog.V(4).Infof("MachineConfiguration %s updated, but does not match %s, skipping bootimage sync", machineConfiguration.Name, ctrlcommon.MCOOperatorKnobsObjectName)
		return
	}

	klog.Infof("Bootimages management configuration has been added, reconciling enrolled machine resources")

	// Update/Check machinesets since the boot images configuration knob was updated
	ctrl.enqueueEvent("BootImageUpdateConfigurationAdded")
}

// updateMachineConfiguration handles updates to the cluster-level MachineConfiguration
// by triggering a reconciliation if the ManagedBootImagesStatus changed.
func (ctrl *Controller) updateMachineConfiguration(oldMC, newMC interface{}) {

	oldMachineConfiguration := oldMC.(*opv1.MachineConfiguration)
	newMachineConfiguration := newMC.(*opv1.MachineConfiguration)

	// Take no action if this isn't the "cluster" level MachineConfiguration object
	if oldMachineConfiguration.Name != ctrlcommon.MCOOperatorKnobsObjectName {
		klog.V(4).Infof("MachineConfiguration %s updated, but does not match %s, skipping bootimage sync", oldMachineConfiguration.Name, ctrlcommon.MCOOperatorKnobsObjectName)
		return
	}

	// Skip reconciliation if neither ManagedBootImagesStatus nor BootImageSkewEnforcementStatus has changed.
	// BootImageSkewEnforcementStatus is only checked when the BootImageSkewEnforcement feature gate is enabled.
	if reflect.DeepEqual(oldMachineConfiguration.Status.ManagedBootImagesStatus, newMachineConfiguration.Status.ManagedBootImagesStatus) &&
		(!ctrl.fgHandler.Enabled(features.FeatureGateBootImageSkewEnforcement) ||
			reflect.DeepEqual(oldMachineConfiguration.Status.BootImageSkewEnforcementStatus, newMachineConfiguration.Status.BootImageSkewEnforcementStatus)) {
		return
	}

	klog.Infof("Bootimages management configuration has been updated, reconciling enrolled machine resources")

	// Update all machinesets since the boot images configuration knob was updated
	ctrl.enqueueEvent("BootImageUpdateConfigurationUpdated")
}

// deleteMachineConfiguration handles the deletion of the cluster-level MachineConfiguration
// by triggering a reconciliation of all enrolled machine resources.
func (ctrl *Controller) deleteMachineConfiguration(obj interface{}) {

	machineConfiguration := obj.(*opv1.MachineConfiguration)

	// Take no action if this isn't the "cluster" level MachineConfiguration object
	if machineConfiguration.Name != ctrlcommon.MCOOperatorKnobsObjectName {
		klog.V(4).Infof("MachineConfiguration %s deleted, but does not match %s, skipping bootimage sync", machineConfiguration.Name, ctrlcommon.MCOOperatorKnobsObjectName)
		return
	}

	klog.Infof("Bootimages management configuration has been deleted, reconciling enrolled machine resources")

	// Update/Check machinesets since the boot images configuration knob was updated
	ctrl.enqueueEvent("BootImageUpdateConfigurationDeleted")
}

// updateConditions updates the boot image update conditions on the MachineConfiguration status
// based on the current state of machine resource reconciliation.
func (ctrl *Controller) updateConditions(newReason string, syncError error, targetConditionType string) {

	mcop, err := ctrl.mcopClient.OperatorV1().MachineConfigurations().Get(context.TODO(), ctrlcommon.MCOOperatorKnobsObjectName, metav1.GetOptions{})
	if err != nil {
		klog.Errorf("error updating progressing condition: %s", err)
		return
	}
	newConditions := mcop.Status.DeepCopy().Conditions
	// If no conditions exist, populate some sane defaults
	if newConditions == nil {
		newConditions = getDefaultConditions()
	}

	for i, condition := range newConditions {
		if condition.Type == targetConditionType {
			if condition.Type == opv1.MachineConfigurationBootImageUpdateProgressing {
				messages := []string{
					ctrl.mapiStats.getProgressingStatusMessage("MAPI MachineSets"),
					ctrl.cpmsStats.getProgressingStatusMessage("ControlPlaneMachineSets"),
					ctrl.capiMachineSetStats.getProgressingStatusMessage("CAPI MachineSets"),
					ctrl.capiMachineDeploymentStats.getProgressingStatusMessage("CAPI MachineDeployments"),
				}
				newConditions[i].Message = strings.Join(messages, " | ")
				newConditions[i].Reason = newReason
				// If all machine resources have been processed, then the controller is no longer progressing.
				if ctrl.mapiStats.isFinished() && ctrl.cpmsStats.isFinished() && ctrl.capiMachineSetStats.isFinished() && ctrl.capiMachineDeploymentStats.isFinished() {
					newConditions[i].Status = metav1.ConditionFalse
				} else {
					newConditions[i].Status = metav1.ConditionTrue
				}
			} else if condition.Type == opv1.MachineConfigurationBootImageUpdateDegraded {
				messages := []string{
					ctrl.mapiStats.getDegradedStatusMessage("MAPI MachineSets"),
					ctrl.cpmsStats.getDegradedStatusMessage("ControlPlaneMachineSets"),
					ctrl.capiMachineSetStats.getDegradedStatusMessage("CAPI MachineSets"),
					ctrl.capiMachineDeploymentStats.getDegradedStatusMessage("CAPI MachineDeployments"),
				}
				if syncError == nil {
					newConditions[i].Message = strings.Join(messages, " | ")
				} else {
					newConditions[i].Message = fmt.Sprintf("%s | Error(s): %s", strings.Join(messages, " | "), syncError.Error())
				}
				newConditions[i].Reason = newReason
				if syncError != nil {
					newConditions[i].Status = metav1.ConditionTrue
				} else {
					newConditions[i].Status = metav1.ConditionFalse
				}
			}
			// Check if there is a change in the condition before updating LastTransitionTime
			if len(mcop.Status.Conditions) == 0 || !reflect.DeepEqual(newConditions[i], mcop.Status.Conditions[i]) {
				newConditions[i].LastTransitionTime = metav1.Now()
			}
			break
		}
	}
	// Only make an API call if there is an update to the Conditions field
	if !reflect.DeepEqual(newConditions, mcop.Status.Conditions) {
		mcop.Status.Conditions = newConditions
		ctrl.updateMachineConfigurationStatus(mcop.Status)
	}
}

// updateClusterBootImage updates the cluster boot image record if the skew enforcement is set to Automatic mode.
func (ctrl *Controller) updateClusterBootImage() {

	mcop, err := ctrl.mcopClient.OperatorV1().MachineConfigurations().Get(context.TODO(), ctrlcommon.MCOOperatorKnobsObjectName, metav1.GetOptions{})
	if err != nil {
		klog.Errorf("error updating cluster boot image record: %s", err)
		return
	}
	// No action to take if not in automatic mode
	if mcop.Status.BootImageSkewEnforcementStatus.Mode != opv1.BootImageSkewEnforcementModeStatusAutomatic {
		return
	}

	// Get OCP version of last boot image update from configmap
	configMap, err := ctrl.mcoCmLister.ConfigMaps(ctrlcommon.MCONamespace).Get(ctrlcommon.BootImagesConfigMapName)
	if err != nil {
		klog.Warningf("Failed to get boot images configmap: %v, skipping cluster boot image record update", err)
		return
	}

	releaseVersion, found := configMap.Data[ctrlcommon.OCPReleaseVersionKey]
	if !found {
		klog.Warningf("OCP release version not found in boot images configmap, skipping cluster boot image record update")
		return
	}

	// Parse and extract semantic version (major.minor.patch) for API validation
	parsedVersion, err := k8sversion.ParseGeneric(releaseVersion)
	if err != nil {
		klog.Warningf("Failed to parse release version %q from configmap: %v, skipping cluster boot image record update", releaseVersion, err)
		return
	}
	ocpVersion := fmt.Sprintf("%d.%d.%d", parsedVersion.Major(), parsedVersion.Minor(), parsedVersion.Patch())

	newBootImageSkewEnforcementStatus := mcop.Status.BootImageSkewEnforcementStatus.DeepCopy()
	newBootImageSkewEnforcementStatus.Automatic = opv1.ClusterBootImageAutomatic{
		OCPVersion: ocpVersion,
	}

	// Only make an API call if there is an update to the skew enforcement status
	if !reflect.DeepEqual(mcop.Status.BootImageSkewEnforcementStatus, newBootImageSkewEnforcementStatus) {
		mcop.Status.BootImageSkewEnforcementStatus = *newBootImageSkewEnforcementStatus
		ctrl.updateMachineConfigurationStatus(mcop.Status)
	}
}

// updateMachineConfigurationStatus updates the MachineConfiguration status using retry logic to handle concurrent updates.
func (ctrl *Controller) updateMachineConfigurationStatus(mcopStatus opv1.MachineConfigurationStatus) {
	// Using a retry here as there may be concurrent reconiliation loops updating conditions for multiple
	// resources at the same time and their local stores may be out of date
	klog.V(4).Infof("MachineConfiguration status update: %v", mcopStatus)
	if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		mcop, err := ctrl.mcopClient.OperatorV1().MachineConfigurations().Get(context.TODO(), ctrlcommon.MCOOperatorKnobsObjectName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		mcop.Status = mcopStatus
		_, err = ctrl.mcopClient.OperatorV1().MachineConfigurations().UpdateStatus(context.TODO(), mcop, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
		return nil
	}); err != nil {
		klog.Errorf("error updating MachineConfiguration status: %v", err)
	}

}

// getDefaultConditions returns the default boot image update conditions when no
// machine resources are enrolled.
func getDefaultConditions() []metav1.Condition {
	// These are boilerplate conditions, with no machine resources enrolled.
	return []metav1.Condition{
		{
			Type:               opv1.MachineConfigurationBootImageUpdateProgressing,
			Message:            "Reconciled 0 of 0 MAPI MachineSets | Reconciled 0 of 0 ControlPlaneMachineSets | Reconciled 0 of 0 CAPI MachineSets | Reconciled 0 of 0 CAPI MachineDeployments",
			Reason:             "NA",
			LastTransitionTime: metav1.Now(),
			Status:             metav1.ConditionFalse,
		},
		{
			Type:               opv1.MachineConfigurationBootImageUpdateDegraded,
			Message:            "0 Degraded MAPI MachineSets | 0 Degraded ControlPlaneMachineSets | 0 Degraded CAPI MachineSets | 0 CAPI MachineDeployments",
			Reason:             "NA",
			LastTransitionTime: metav1.Now(),
			Status:             metav1.ConditionFalse,
		}}

}

// syncAll will attempt to sync all supported machine resources
func (ctrl *Controller) syncAll(event string) error {
	klog.V(4).Infof("Syncing boot image controller for event: %s", event)

	// Wait for MachineConfiguration/cluster to be ready before syncing any machine resources
	if err := ctrl.waitForMachineConfigurationReady(); err != nil {
		ctrl.updateConditions(event, fmt.Errorf("MachineConfiguration was not ready: %v", err), opv1.MachineConfigurationBootImageUpdateDegraded)
		return err
	}

	ctrl.syncControlPlaneMachineSets(event)
	ctrl.syncMAPIMachineSets(event)
	return nil
}
