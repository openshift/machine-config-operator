package machineset

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"time"

	opv1 "github.com/openshift/api/operator/v1"
	configinformersv1 "github.com/openshift/client-go/config/informers/externalversions/config/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	mcopclientset "github.com/openshift/client-go/operator/clientset/versioned"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubeErrs "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformersv1 "k8s.io/client-go/informers/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	corelisterv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	"k8s.io/kubectl/pkg/scheme"

	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	machineclientset "github.com/openshift/client-go/machine/clientset/versioned"
	mapimachineinformers "github.com/openshift/client-go/machine/informers/externalversions/machine/v1beta1"
	machinelisters "github.com/openshift/client-go/machine/listers/machine/v1beta1"
	operatorversion "github.com/openshift/machine-config-operator/pkg/version"

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

	mcoCmLister          corelisterv1.ConfigMapLister
	mapiMachineSetLister machinelisters.MachineSetLister
	infraLister          configlistersv1.InfrastructureLister
	mcopLister           mcoplistersv1.MachineConfigurationLister

	mcoCmListerSynced          cache.InformerSynced
	mapiMachineSetListerSynced cache.InformerSynced
	infraListerSynced          cache.InformerSynced
	mcopListerSynced           cache.InformerSynced

	mapiStats                  MachineResourceStats
	capiMachineSetStats        MachineResourceStats
	capiMachineDeploymentStats MachineResourceStats
	mapiBootImageState         map[string]BootImageState
	conditionMutex             sync.Mutex
	mapiSyncMutex              sync.Mutex

	fgHandler ctrlcommon.FeatureGatesHandler
}

// Stats structure for local bookkeeping of machine resources
type MachineResourceStats struct {
	inProgress   int
	erroredCount int
	totalCount   int
}

// State structure uses for detecting hot loops. Reset when cluster is opted
// out of boot image updates.
type BootImageState struct {
	value        []byte
	hotLoopCount int
}

// Helper function that checks if all resources have been evaluated
func (mrs MachineResourceStats) isFinished() bool {
	return mrs.totalCount == (mrs.inProgress + mrs.erroredCount)
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
)

// New returns a new machine-set-boot-image controller.
func New(
	kubeClient clientset.Interface,
	machineClient machineclientset.Interface,
	mcoCmInfomer coreinformersv1.ConfigMapInformer,
	mapiMachineSetInformer mapimachineinformers.MachineSetInformer,
	infraInformer configinformersv1.InfrastructureInformer,
	mcopClient mcopclientset.Interface,
	mcopInformer mcopinformersv1.MachineConfigurationInformer,
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
	}

	ctrl.mcoCmLister = mcoCmInfomer.Lister()
	ctrl.mapiMachineSetLister = mapiMachineSetInformer.Lister()
	ctrl.infraLister = infraInformer.Lister()
	ctrl.mcopLister = mcopInformer.Lister()

	ctrl.mcoCmListerSynced = mcoCmInfomer.Informer().HasSynced
	ctrl.mapiMachineSetListerSynced = mapiMachineSetInformer.Informer().HasSynced
	ctrl.infraListerSynced = infraInformer.Informer().HasSynced
	ctrl.mcopListerSynced = mcopInformer.Informer().HasSynced

	mapiMachineSetInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addMAPIMachineSet,
		UpdateFunc: ctrl.updateMAPIMachineSet,
		DeleteFunc: ctrl.deleteMAPIMachineSet,
	})

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

	return ctrl
}

// Run executes the machine-set-boot-image controller.
func (ctrl *Controller) Run(stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()

	if !cache.WaitForCacheSync(stopCh, ctrl.mcoCmListerSynced, ctrl.mapiMachineSetListerSynced, ctrl.infraListerSynced, ctrl.mcopListerSynced) {
		return
	}

	klog.Info("Starting MachineConfigController-MachineSetBootImageController")
	defer klog.Info("Shutting down MachineConfigController-MachineSetBootImageController")

	<-stopCh
}

func (ctrl *Controller) addMAPIMachineSet(obj interface{}) {

	machineSet := obj.(*machinev1beta1.MachineSet)

	klog.Infof("MAPI MachineSet %s added, reconciling enrolled machine resources", machineSet.Name)

	// Update/Check all machinesets instead of just this one. This prevents needing to maintain a local
	// store of machineset conditions. As this is using a lister, it is relatively inexpensive to do
	// this.
	go func() { ctrl.syncMAPIMachineSets("MAPIMachinesetAdded") }()
}

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
	go func() { ctrl.syncMAPIMachineSets("MAPIMachinesetUpdated") }()
}

func (ctrl *Controller) deleteMAPIMachineSet(deletedMS interface{}) {

	deletedMachineSet := deletedMS.(*machinev1beta1.MachineSet)

	klog.Infof("MachineSet %s deleted, reconciling enrolled machineset resources", deletedMachineSet.Name)

	// Update all machinesets. This prevents needing to maintain a local
	// store of machineset conditions. As this is using a lister, it is relatively inexpensive to do
	// this.
	go func() { ctrl.syncMAPIMachineSets("MAPIMachinesetDeleted") }()
}

func (ctrl *Controller) addConfigMap(obj interface{}) {

	configMap := obj.(*corev1.ConfigMap)

	// Take no action if this isn't the "golden" config map
	if configMap.Name != ctrlcommon.BootImagesConfigMapName {
		return
	}

	klog.Infof("configMap %s added, reconciling enrolled machine resources", configMap.Name)

	// Update all machinesets since the "golden" configmap has been added
	// TODO: Add go routines for CAPI resources here
	go func() { ctrl.syncMAPIMachineSets("BootImageConfigMapAdded") }()
}

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
	// TODO: Add go routines for CAPI resources here
	go func() { ctrl.syncMAPIMachineSets("BootImageConfigMapUpdated") }()
}

func (ctrl *Controller) deleteConfigMap(obj interface{}) {

	configMap := obj.(*corev1.ConfigMap)

	// Take no action if this isn't the "golden" config map
	if configMap.Name != ctrlcommon.BootImagesConfigMapName {
		return
	}

	klog.Infof("configMap %s deleted, reconciling enrolled machine resources", configMap.Name)

	// Update all machinesets since the "golden" configmap has been deleted
	go func() { ctrl.syncMAPIMachineSets("BootImageConfigMapDeleted") }()
}

func (ctrl *Controller) addMachineConfiguration(obj interface{}) {

	machineConfiguration := obj.(*opv1.MachineConfiguration)

	// Take no action if this isn't the "cluster" level MachineConfiguration object
	if machineConfiguration.Name != ctrlcommon.MCOOperatorKnobsObjectName {
		klog.V(4).Infof("MachineConfiguration %s updated, but does not match %s, skipping bootimage sync", machineConfiguration.Name, ctrlcommon.MCOOperatorKnobsObjectName)
		return
	}

	klog.Infof("Bootimages management configuration has been added, reconciling enrolled machine resources")

	// Update/Check machinesets since the boot images configuration knob was updated
	// TODO: Add go routines for CAPI resources here
	go func() { ctrl.syncMAPIMachineSets("BootImageUpdateConfigurationAdded") }()
}

func (ctrl *Controller) updateMachineConfiguration(oldMC, newMC interface{}) {

	oldMachineConfiguration := oldMC.(*opv1.MachineConfiguration)
	newMachineConfiguration := newMC.(*opv1.MachineConfiguration)

	// Take no action if this isn't the "cluster" level MachineConfiguration object
	if oldMachineConfiguration.Name != ctrlcommon.MCOOperatorKnobsObjectName {
		klog.V(4).Infof("MachineConfiguration %s updated, but does not match %s, skipping bootimage sync", oldMachineConfiguration.Name, ctrlcommon.MCOOperatorKnobsObjectName)
		return
	}

	// Only take action if the there is an actual change in the MachineConfiguration's ManagedBootImagesStatus
	if reflect.DeepEqual(oldMachineConfiguration.Status.ManagedBootImagesStatus, newMachineConfiguration.Status.ManagedBootImagesStatus) {
		return
	}

	klog.Infof("Bootimages management configuration has been updated, reconciling enrolled machine resources")

	// Update all machinesets since the boot images configuration knob was updated
	// TODO: Add go routines for CAPI resources here
	go func() { ctrl.syncMAPIMachineSets("BootImageUpdateConfigurationUpdated") }()
}

func (ctrl *Controller) deleteMachineConfiguration(obj interface{}) {

	machineConfiguration := obj.(*opv1.MachineConfiguration)

	// Take no action if this isn't the "cluster" level MachineConfiguration object
	if machineConfiguration.Name != ctrlcommon.MCOOperatorKnobsObjectName {
		klog.V(4).Infof("MachineConfiguration %s deleted, but does not match %s, skipping bootimage sync", machineConfiguration.Name, ctrlcommon.MCOOperatorKnobsObjectName)
		return
	}

	klog.Infof("Bootimages management configuration has been deleted, reconciling enrolled machine resources")

	// Update/Check machinesets since the boot images configuration knob was updated
	// TODO: Add go routines for CAPI resources here
	go func() { ctrl.syncMAPIMachineSets("BootImageUpdateConfigurationDeleted") }()
}

// syncMAPIMachineSets will attempt to enqueue every machineset
func (ctrl *Controller) syncMAPIMachineSets(reason string) {

	ctrl.mapiSyncMutex.Lock()
	defer ctrl.mapiSyncMutex.Unlock()

	var mcop *opv1.MachineConfiguration
	var pollError error
	// Wait for mcop.Status to populate, otherwise error out. This shouldn't take very long
	// as this is done by the operator sync loop.
	if err := wait.PollUntilContextTimeout(context.TODO(), 5*time.Second, 2*time.Minute, true, func(_ context.Context) (bool, error) {
		mcop, pollError = ctrl.mcopLister.Get(ctrlcommon.MCOOperatorKnobsObjectName)
		if pollError != nil {
			klog.Errorf("MachineConfiguration/cluster has not been created yet")
			return false, nil
		}

		// Ensure status.ObservedGeneration matches the last generation of MachineConfiguration
		if mcop.Generation != mcop.Status.ObservedGeneration {
			klog.Errorf("MachineConfiguration.Status is not up to date.")
			pollError = fmt.Errorf("MachineConfiguration.Status is not up to date")
			return false, nil
		}
		return true, nil
	}); err != nil {
		klog.Errorf("MachineConfiguration was not ready: %v", pollError)
		ctrl.updateConditions(reason, fmt.Errorf("MachineConfiguration was not ready:  while enqueueing MAPI MachineSets %v", err), opv1.MachineConfigurationBootImageUpdateDegraded)
		return
	}

	machineManagerFound, machineResourceSelector, err := getMachineResourceSelectorFromMachineManagers(mcop.Status.ManagedBootImagesStatus.MachineManagers, opv1.MachineAPI, opv1.MachineSets)
	if err != nil {
		klog.Errorf("failed to create a machineset selector while enqueueing MAPI machineset %v", err)
		ctrl.updateConditions(reason, fmt.Errorf("failed to create a machineset selector while enqueueing MAPI machineset %v", err), opv1.MachineConfigurationBootImageUpdateDegraded)
		return
	}
	if !machineManagerFound {
		klog.V(4).Infof("No MAPI machineset manager was found, so no MAPI machinesets will be enrolled.")
		// clear out MAPI boot image history
		for k := range ctrl.mapiBootImageState {
			delete(ctrl.mapiBootImageState, k)
		}

	}

	mapiMachineSets, err := ctrl.mapiMachineSetLister.List(machineResourceSelector)
	if err != nil {
		klog.Errorf("failed to fetch MachineSet list while enqueueing MAPI MachineSets %v", err)
		ctrl.updateConditions(reason, fmt.Errorf("failed to fetch MachineSet list while enqueueing MAPI MachineSets %v", err), opv1.MachineConfigurationBootImageUpdateDegraded)
		return
	}

	// If no machine resources were enrolled; exit the enqueue process without errors.
	if len(mapiMachineSets) == 0 {
		klog.Infof("No MAPI machinesets were enrolled, so no MAPI machinesets will be enqueued.")
		// clear out MAPI boot image history
		for k := range ctrl.mapiBootImageState {
			delete(ctrl.mapiBootImageState, k)
		}
	}

	// Reset stats before initiating reconciliation loop
	ctrl.mapiStats.inProgress = 0
	ctrl.mapiStats.totalCount = len(mapiMachineSets)
	ctrl.mapiStats.erroredCount = 0

	// Signal start of reconciliation process, by setting progressing to true
	var syncErrors []error
	ctrl.updateConditions(reason, nil, opv1.MachineConfigurationBootImageUpdateProgressing)

	for _, machineSet := range mapiMachineSets {
		err := ctrl.syncMAPIMachineSet(machineSet)
		if err == nil {
			ctrl.mapiStats.inProgress++
		} else {
			klog.Errorf("Error syncing MAPI MachineSet %v", err)
			syncErrors = append(syncErrors, fmt.Errorf("error syncing MAPI MachineSet %s: %v", machineSet.Name, err))
			ctrl.mapiStats.erroredCount++
		}
		// Update progressing conditions every step of the loop
		ctrl.updateConditions(reason, nil, opv1.MachineConfigurationBootImageUpdateProgressing)
	}
	// Update/Clear degrade conditions based on errors from this loop
	ctrl.updateConditions(reason, kubeErrs.NewAggregate(syncErrors), opv1.MachineConfigurationBootImageUpdateDegraded)
}

// syncMAPIMachineSet will attempt to reconcile the provided machineset
func (ctrl *Controller) syncMAPIMachineSet(machineSet *machinev1beta1.MachineSet) error {

	startTime := time.Now()
	klog.V(4).Infof("Started syncing MAPI machineset %q (%v)", machineSet.Name, startTime)
	defer func() {
		klog.V(4).Infof("Finished syncing MAPI machineset %q (%v)", machineSet.Name, time.Since(startTime))
	}()

	// If the machineset has an owner reference, exit and report error. This means
	// that the machineset may be managed by another workflow and should not be reconciled.
	if len(machineSet.GetOwnerReferences()) != 0 {
		klog.Infof("machineset %s has OwnerReference: %v, skipping boot image update", machineSet.GetOwnerReferences()[0].Kind+"/"+machineSet.GetOwnerReferences()[0].Name, machineSet.Name)
		return nil
	}

	if os, ok := machineSet.Spec.Template.Labels[OSLabelKey]; ok {
		if os == "Windows" {
			klog.Infof("machineset %s has a windows os label, skipping boot image update", machineSet.Name)
			return nil
		}
	}

	// Fetch the architecture type of this machineset
	arch, err := getArchFromMachineSet(machineSet)
	if err != nil {
		return fmt.Errorf("failed to fetch arch during machineset sync: %w", err)
	}

	// Fetch the infra object to determine the platform type
	infra, err := ctrl.infraLister.Get("cluster")
	if err != nil {
		return fmt.Errorf("failed to fetch infra object during machineset sync: %w", err)
	}

	// Fetch the bootimage configmap & ensure it has been stamped by the operator. This is done by
	// the operator when a master node successfully updates to a new image. This is
	// to prevent machinesets from being updated before the operator itself has updated.
	// If it hasn't been updated, exit and wait for a resync.
	configMap, err := ctrl.mcoCmLister.ConfigMaps(ctrlcommon.MCONamespace).Get(ctrlcommon.BootImagesConfigMapName)
	if err != nil {
		return fmt.Errorf("failed to fetch coreos-bootimages config map during machineset sync: %w", err)
	}
	versionHashFromCM, versionHashFound := configMap.Data[ctrlcommon.MCOVersionHashKey]
	if !versionHashFound {
		klog.Infof("failed to find mco version hash in %s configmap, sync will exit to wait for the MCO upgrade to complete", ctrlcommon.BootImagesConfigMapName)
		return nil
	}
	if versionHashFromCM != operatorversion.Hash {
		klog.Infof("mismatch between MCO hash version stored in configmap and current MCO version; sync will exit to wait for the MCO upgrade to complete")
		return nil
	}
	releaseVersionFromCM, releaseVersionFound := configMap.Data[ctrlcommon.OCPReleaseVersionKey]
	if !releaseVersionFound {
		klog.Infof("failed to find OCP release version in %s configmap, sync will exit to wait for the MCO upgrade to complete", ctrlcommon.BootImagesConfigMapName)
		return nil
	}
	if releaseVersionFromCM != operatorversion.ReleaseVersion {
		klog.Infof("mismatch between OCP release version stored in configmap and current MCO release version; sync will exit to wait for the MCO upgrade to complete")
		return nil
	}

	// Check if the this MachineSet requires an update
	patchRequired, newMachineSet, err := checkMachineSet(infra, machineSet, configMap, arch, ctrl.kubeClient)
	if err != nil {
		return fmt.Errorf("failed to reconcile machineset %s, err: %w", machineSet.Name, err)
	}

	// Patch the machineset if required
	if patchRequired {
		// First, check if we're hot looping
		if ctrl.checkMAPIMachineSetHotLoop(newMachineSet) {
			return fmt.Errorf("refusing to reconcile machineset %s, hot loop detected. Please opt-out of boot image updates, adjust your machine provisioning workflow to prevent hot loops and opt back in to resume boot image updates", machineSet.Name)
		}
		klog.Infof("Patching MAPI machineset %s", machineSet.Name)
		return ctrl.patchMachineSet(machineSet, newMachineSet)
	}
	klog.Infof("No patching required for MAPI machineset %s", machineSet.Name)
	return nil
}

// Checks against a local store of boot image updates to detect hot looping
func (ctrl *Controller) checkMAPIMachineSetHotLoop(machineSet *machinev1beta1.MachineSet) bool {
	bis, ok := ctrl.mapiBootImageState[machineSet.Name]
	if !ok {
		// If the machineset doesn't currently have a record, create a new one.
		ctrl.mapiBootImageState[machineSet.Name] = BootImageState{
			value:        machineSet.Spec.Template.Spec.ProviderSpec.Value.Raw,
			hotLoopCount: 1,
		}
	} else {
		hotLoopCount := 1
		// If the controller is updating to a value that was previously updated to, increase the hot loop counter
		if bytes.Equal(bis.value, machineSet.Spec.Template.Spec.ProviderSpec.Value.Raw) {
			hotLoopCount = (bis.hotLoopCount) + 1
		}
		// Return an error and degrade if the hot loop counter is above threshold
		if hotLoopCount > HotLoopLimit {
			return true
		}
		ctrl.mapiBootImageState[machineSet.Name] = BootImageState{
			value:        machineSet.Spec.Template.Spec.ProviderSpec.Value.Raw,
			hotLoopCount: hotLoopCount,
		}
	}
	return false
}

// This function patches the machineset object using the machineClient
// Returns an error if marshsalling or patching fails.
func (ctrl *Controller) patchMachineSet(oldMachineSet, newMachineSet *machinev1beta1.MachineSet) error {
	machineSetMarshal, err := json.Marshal(oldMachineSet)
	if err != nil {
		return fmt.Errorf("unable to marshal old machineset: %w", err)
	}
	newMachineSetMarshal, err := json.Marshal(newMachineSet)
	if err != nil {
		return fmt.Errorf("unable to marshal new machineset: %w", err)
	}
	patchBytes, err := jsonmergepatch.CreateThreeWayJSONMergePatch(machineSetMarshal, newMachineSetMarshal, machineSetMarshal)
	if err != nil {
		return fmt.Errorf("unable to create patch for new machineset: %w", err)
	}
	_, err = ctrl.machineClient.MachineV1beta1().MachineSets(MachineAPINamespace).Patch(context.TODO(), oldMachineSet.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("unable to patch new machineset: %w", err)
	}
	klog.Infof("Successfully patched machineset %s", oldMachineSet.Name)
	return nil
}

func (ctrl *Controller) updateConditions(newReason string, syncError error, targetConditionType string) {
	ctrl.conditionMutex.Lock()
	defer ctrl.conditionMutex.Unlock()
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
				newConditions[i].Message = fmt.Sprintf("Reconciled %d of %d MAPI MachineSets | Reconciled %d of %d CAPI MachineSets | Reconciled %d of %d CAPI MachineDeployments", ctrl.mapiStats.inProgress, ctrl.mapiStats.totalCount, ctrl.capiMachineSetStats.inProgress, ctrl.capiMachineSetStats.totalCount, ctrl.capiMachineDeploymentStats.inProgress, ctrl.capiMachineDeploymentStats.totalCount)
				newConditions[i].Reason = newReason
				// If all machine resources have been processed, then the controller is no longer progressing.
				if ctrl.mapiStats.isFinished() && ctrl.capiMachineSetStats.isFinished() && ctrl.capiMachineDeploymentStats.isFinished() {
					newConditions[i].Status = metav1.ConditionFalse
				} else {
					newConditions[i].Status = metav1.ConditionTrue
				}
			} else if condition.Type == opv1.MachineConfigurationBootImageUpdateDegraded {
				if syncError == nil {
					newConditions[i].Message = fmt.Sprintf("%d Degraded MAPI MachineSets | %d Degraded CAPI MachineSets | %d CAPI MachineDeployments", ctrl.mapiStats.erroredCount, ctrl.capiMachineSetStats.erroredCount, ctrl.capiMachineDeploymentStats.erroredCount)
				} else {
					newConditions[i].Message = fmt.Sprintf("%d Degraded MAPI MachineSets | %d Degraded CAPI MachineSets | %d CAPI MachineDeployments | Error(s): %s", ctrl.mapiStats.erroredCount, ctrl.capiMachineSetStats.erroredCount, ctrl.capiMachineDeploymentStats.erroredCount, syncError.Error())
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
		ctrl.updateMachineConfigurationStatus(mcop, newConditions)
	}
}

func (ctrl *Controller) updateMachineConfigurationStatus(mcop *opv1.MachineConfiguration, newConditions []metav1.Condition) {

	// Using a retry here as there may be concurrent reconiliation loops updating conditions for multiple
	// resources at the same time and their local stores may be out of date
	if !reflect.DeepEqual(mcop.Status.Conditions, newConditions) {
		klog.V(4).Infof("%v", newConditions)
		if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			mcop, err := ctrl.mcopClient.OperatorV1().MachineConfigurations().Get(context.TODO(), ctrlcommon.MCOOperatorKnobsObjectName, metav1.GetOptions{})
			if err != nil {
				return err
			}
			mcop.Status.Conditions = newConditions
			_, err = ctrl.mcopClient.OperatorV1().MachineConfigurations().UpdateStatus(context.TODO(), mcop, metav1.UpdateOptions{})
			if err != nil {
				return err
			}
			return nil
		}); err != nil {
			klog.Errorf("error updating MachineConfiguration status: %v", err)
		}
	}
}

func getDefaultConditions() []metav1.Condition {
	// These are boilerplate conditions, with no machine resources enrolled.
	return []metav1.Condition{
		{
			Type:               opv1.MachineConfigurationBootImageUpdateProgressing,
			Message:            "Reconciled 0 of 0 MAPI MachineSets | Reconciled 0 of 0 CAPI MachineSets | Reconciled 0 of 0 CAPI MachineDeployments",
			Reason:             "NA",
			LastTransitionTime: metav1.Now(),
			Status:             metav1.ConditionFalse,
		},
		{
			Type:               opv1.MachineConfigurationBootImageUpdateDegraded,
			Message:            "0 Degraded MAPI MachineSets | 0 Degraded CAPI MachineSets | 0 CAPI MachineDeployments",
			Reason:             "NA",
			LastTransitionTime: metav1.Now(),
			Status:             metav1.ConditionFalse,
		}}

}
