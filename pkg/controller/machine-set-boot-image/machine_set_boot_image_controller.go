package machineset

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	opv1 "github.com/openshift/api/operator/v1"
	configinformersv1 "github.com/openshift/client-go/config/informers/externalversions/config/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	mcopclientset "github.com/openshift/client-go/operator/clientset/versioned"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformersv1 "k8s.io/client-go/informers/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	corelisterv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/kubectl/pkg/scheme"
	"sigs.k8s.io/yaml"

	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"

	machineclientset "github.com/openshift/client-go/machine/clientset/versioned"
	mapimachineinformers "github.com/openshift/client-go/machine/informers/externalversions/machine/v1beta1"
	machinelisters "github.com/openshift/client-go/machine/listers/machine/v1beta1"
	operatorversion "github.com/openshift/machine-config-operator/pkg/version"

	mcopinformersv1 "github.com/openshift/client-go/operator/informers/externalversions/operator/v1"
	mcoplistersv1 "github.com/openshift/client-go/operator/listers/operator/v1"

	archtranslater "github.com/coreos/stream-metadata-go/arch"
	"github.com/coreos/stream-metadata-go/stream"

	osconfigv1 "github.com/openshift/api/config/v1"
	features "github.com/openshift/api/features"
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

	syncHandler func(ms string) error

	queue workqueue.RateLimitingInterface

	featureGateAccess featuregates.FeatureGateAccess
	featureEnabled    bool
}

const (
	// maxRetries is the number of times a machineset will be retried before dropping out of queue
	maxRetries = 5
	// Name of machine api namespace
	MachineAPINamespace = "openshift-machine-api"

	// Key to access stream data from the boot images configmap
	StreamConfigMapKey = "stream"

	// Labels and Annotations required for determining architecture of a machineset
	MachineSetArchAnnotationKey = "capacity.cluster-autoscaler.kubernetes.io/labels"
	// Name of managed worker secret
	ManagedWorkerSecretName = "worker-user-data-managed"

	ArchLabelKey = "kubernetes.io/arch="
	OSLabelKey   = "machine.openshift.io/os-id"
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
	featureGateAccess featuregates.FeatureGateAccess,
) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&corev1client.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	ctrl := &Controller{
		kubeClient:    kubeClient,
		machineClient: machineClient,
		mcopClient:    mcopClient,
		eventRecorder: ctrlcommon.NamespacedEventRecorder(eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineconfigcontroller-machinesetbootimagecontroller"})),
		queue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "machineconfigcontroller-machinesetbootimagecontroller"),
	}

	mapiMachineSetInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addMAPIMachineSet,
		UpdateFunc: ctrl.updateMAPIMachineSet,
	})

	mcoCmInfomer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addConfigMap,
		UpdateFunc: ctrl.updateConfigMap,
	})

	mcopInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addMachineConfiguration,
		UpdateFunc: ctrl.updateMachineConfiguration,
	})

	ctrl.syncHandler = ctrl.syncMachineResource

	ctrl.mcoCmLister = mcoCmInfomer.Lister()
	ctrl.mapiMachineSetLister = mapiMachineSetInformer.Lister()
	ctrl.infraLister = infraInformer.Lister()
	ctrl.mcopLister = mcopInformer.Lister()

	ctrl.mcoCmListerSynced = mcoCmInfomer.Informer().HasSynced
	ctrl.mapiMachineSetListerSynced = mapiMachineSetInformer.Informer().HasSynced
	ctrl.infraListerSynced = infraInformer.Informer().HasSynced
	ctrl.mcopListerSynced = mcopInformer.Informer().HasSynced

	ctrl.featureGateAccess = featureGateAccess

	ctrl.featureEnabled = false
	ctrl.featureGateAccess.SetChangeHandler(func(featureChange featuregates.FeatureChange) {
		klog.InfoS("FeatureGates changed", "enabled", featureChange.New.Enabled, "disabled", featureChange.New.Disabled)

		var prevfeatureEnabled bool
		if featureChange.Previous == nil {
			// When the initial featuregate is set, the previous version is nil. Default to the feature being disabled.
			prevfeatureEnabled = false
		} else {
			prevfeatureEnabled = featuregates.NewFeatureGate(featureChange.Previous.Enabled, featureChange.Previous.Disabled).
				Enabled(features.FeatureGateManagedBootImages)
		}
		ctrl.featureEnabled = featuregates.NewFeatureGate(featureChange.New.Enabled, featureChange.New.Disabled).
			Enabled(features.FeatureGateManagedBootImages)
		if !prevfeatureEnabled && ctrl.featureEnabled {
			klog.Info("Trigger a sync as this feature was turned on")
			ctrl.enqueueMAPIMachineSets()
		}
	})

	return ctrl
}

// Run executes the machine-set-boot-image controller.
func (ctrl *Controller) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()

	if !cache.WaitForCacheSync(stopCh, ctrl.mcoCmListerSynced, ctrl.mapiMachineSetListerSynced, ctrl.infraListerSynced, ctrl.mcopListerSynced) {
		return
	}

	klog.Info("Starting MachineConfigController-MachineSetBootImageController")
	defer klog.Info("Shutting down MachineConfigController-MachineSetBootImageController")

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.worker, time.Second, stopCh)
	}

	<-stopCh
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

	_, msName, splitErr := cache.SplitMetaNamespaceKey(key.(string))
	if splitErr != nil {
		klog.V(2).Infof("Exception %v handling error during machineset sync %v: %v", splitErr, key, err)
		return
	}

	if err == nil {
		ctrl.queue.Forget(key)
		// Clear any errors as this machineset was successfully patched
		ctrlcommon.MCCBootImageUpdateErr.WithLabelValues(msName).Set(0)
		return
	}

	// Increment error on this machineset
	ctrlcommon.MCCBootImageUpdateErr.WithLabelValues(msName).Inc()
	if ctrl.queue.NumRequeues(key) < maxRetries {
		klog.V(2).Infof("Error syncing machineset %v: %v", key, err)
		ctrl.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	klog.V(2).Infof("Dropping machineset %q out of the work queue: %v", key, err)
	ctrl.queue.Forget(key)
	ctrl.queue.AddAfter(key, 1*time.Minute)
}

func (ctrl *Controller) addMAPIMachineSet(obj interface{}) {
	// No-op if feature is disabled
	if !ctrl.featureEnabled {
		return
	}
	machineSet := obj.(*machinev1beta1.MachineSet)

	klog.Infof("MAPI MachineSet %s added, reconciling enrolled machine resources", machineSet.Name)

	// Update/Check all machinesets instead of just this one. This prevents needing to maintain a local
	// store of machineset conditions. As this is using a lister, it is relatively inexpensive to do
	// this.
	err := ctrl.enqueueMAPIMachineSets()
	if err != nil {
		klog.Errorf("Error enqueuing MAPI machinesets: %v", err)
	}
}

func (ctrl *Controller) updateMAPIMachineSet(oldMS, newMS interface{}) {

	// No-op if feature is disabled
	if !ctrl.featureEnabled {
		return
	}
	oldMachineSet := oldMS.(*machinev1beta1.MachineSet)
	newMachineSet := newMS.(*machinev1beta1.MachineSet)

	// Only take action if the there is an actual change in the MachineSet's Spec, or a change in the labels
	if oldMachineSet.Generation == newMachineSet.Generation && reflect.DeepEqual(oldMachineSet.Labels, newMachineSet.Labels) {
		return
	}

	klog.Infof("MachineSet %s updated, reconciling enrolled machineset resources", oldMachineSet.Name)

	// Update all machinesets instead of just this one. This prevents needing to maintain a local
	// store of machineset conditions. As this is using a lister, it is relatively inexpensive to do
	// this.
	err := ctrl.enqueueMAPIMachineSets()
	if err != nil {
		klog.Errorf("Error enqueuing MAPI machinesets: %v", err)
	}
}

func (ctrl *Controller) addConfigMap(obj interface{}) {
	// No-op if feature is disabled
	if !ctrl.featureEnabled {
		return
	}
	configMap := obj.(*corev1.ConfigMap)

	// Take no action if this isn't the "golden" config map
	if configMap.Name != ctrlcommon.BootImagesConfigMapName {
		klog.V(4).Infof("configMap %s added, but does not match %s, skipping bootimage sync", configMap.Name, ctrlcommon.BootImagesConfigMapName)
		return
	}

	klog.Infof("configMap %s added, reconciling enrolled machine resources", configMap.Name)

	// Update all machinesets since the "golden" configmap has been updated
	err := ctrl.enqueueMAPIMachineSets()
	if err != nil {
		klog.Errorf("Error enqueuing MAPI machinesets: %v", err)
	}
}

func (ctrl *Controller) updateConfigMap(oldCM, newCM interface{}) {
	// No-op if feature is disabled
	if !ctrl.featureEnabled {
		return
	}
	oldConfigMap := oldCM.(*corev1.ConfigMap)
	newConfigMap := newCM.(*corev1.ConfigMap)

	// Take no action if this isn't the "golden" config map
	if oldConfigMap.Name != ctrlcommon.BootImagesConfigMapName {
		klog.V(4).Infof("configMap %s updated, but does not match %s, skipping bootimage sync", oldConfigMap.Name, ctrlcommon.BootImagesConfigMapName)
		return
	}

	// Only take action if the there is an actual change in the configMap Object
	if oldConfigMap.ResourceVersion == newConfigMap.ResourceVersion {
		return
	}

	klog.Infof("configMap %s updated, reconciling enrolled machine resources", oldConfigMap.Name)

	// Update all machinesets since the "golden" configmap has been updated
	err := ctrl.enqueueMAPIMachineSets()
	if err != nil {
		klog.Errorf("Error enqueuing MAPI machinesets: %v", err)
	}
}

func (ctrl *Controller) addMachineConfiguration(obj interface{}) {
	// No-op if feature is disabled
	if !ctrl.featureEnabled {
		return
	}

	machineConfiguration := obj.(*opv1.MachineConfiguration)

	// Take no action if this isn't the "cluster" level MachineConfiguration object
	if machineConfiguration.Name != ctrlcommon.MCOOperatorKnobsObjectName {
		klog.V(4).Infof("MachineConfiguration %s updated, but does not match %s, skipping bootimage sync", machineConfiguration.Name, ctrlcommon.MCOOperatorKnobsObjectName)
		return
	}

	klog.Infof("Bootimages management configuration has been added, reconciling enrolled machine resources")

	// Update/Check machinesets since the boot images configuration knob was updated
	err := ctrl.enqueueMAPIMachineSets()
	if err != nil {
		klog.Errorf("Error enqueuing MAPI machinesets: %v", err)
	}
}

func (ctrl *Controller) updateMachineConfiguration(oldMC, newMC interface{}) {

	// No-op if feature is disabled
	if !ctrl.featureEnabled {
		return
	}
	oldMachineConfiguration := oldMC.(*opv1.MachineConfiguration)
	newMachineConfiguration := newMC.(*opv1.MachineConfiguration)

	// Take no action if this isn't the "cluster" level MachineConfiguration object
	if oldMachineConfiguration.Name != ctrlcommon.MCOOperatorKnobsObjectName {
		klog.V(4).Infof("MachineConfiguration %s updated, but does not match %s, skipping bootimage sync", oldMachineConfiguration.Name, ctrlcommon.MCOOperatorKnobsObjectName)
		return
	}

	// Only take action if the there is an actual change in the MachineConfiguration's ManagedBootImages knob
	if reflect.DeepEqual(oldMachineConfiguration.Spec.ManagedBootImages, newMachineConfiguration.Spec.ManagedBootImages) {
		return
	}

	klog.Infof("Bootimages management configuration has been updated, reconciling enrolled machine resources")

	// Update all machinesets since the boot images configuration knob was updated
	err := ctrl.enqueueMAPIMachineSets()
	if err != nil {
		klog.Errorf("Error enqueuing MAPI machinesets: %v", err)
	}
}

// This function adds MAPI machinesets to the workqueue
func (ctrl *Controller) enqueueMAPIMachineSet(ms *machinev1beta1.MachineSet) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(ms)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", ms, err))
		return
	}

	ctrl.queue.Add(key)
}

// syncMachineSet will attempt to enqueue every machineset
func (ctrl *Controller) enqueueMAPIMachineSets() error {

	// Grab the global operator knobs
	mcop, err := ctrl.mcopLister.Get(ctrlcommon.MCOOperatorKnobsObjectName)
	if err != nil {
		return fmt.Errorf("failed to fetch MachineConfiguration knobs list while enqueueing MAPI MachineSets %w", err)
	}

	// If no machine managers exist; exit the enqueue process.
	if len(mcop.Spec.ManagedBootImages.MachineManagers) == 0 {
		klog.Infof("No MAPI machineset manager was found, so no MAPI machinesets will be enqueued.")
		return nil
	}

	machineManagerFound, machineResourceSelector, err := getMachineResourceSelectorFromMachineManagers(mcop.Spec.ManagedBootImages.MachineManagers, opv1.MachineAPI, opv1.MachineSets)
	if err != nil {
		return fmt.Errorf("failed to create a machineset selector while enqueueing MAPI machineset %w", err)
	}
	if !machineManagerFound {
		klog.Infof("No MAPI machineset manager was found, so no MAPI machinesets will be enqueued.")
		return nil
	}

	machineSets, err := ctrl.mapiMachineSetLister.List(machineResourceSelector)
	if err != nil {
		return fmt.Errorf("failed to fetch MachineSet list while enqueueing MAPI MachineSets %w", err)
	}

	klog.Infof("Reconciling %d MAPI MachineSet(s)", len(machineSets))
	for _, machineSet := range machineSets {
		ctrl.enqueueMAPIMachineSet(machineSet)
	}

	return nil
}

// This function checks if an array of machineManagers contains the target apigroup/resource and returns
// a bool(success/fail), a label selector to filter the target resource and an error, if any.
func getMachineResourceSelectorFromMachineManagers(machineManagers []opv1.MachineManager, apiGroup opv1.MachineManagerMachineSetsAPIGroupType, resource opv1.MachineManagerMachineSetsResourceType) (bool, labels.Selector, error) {

	for _, machineManager := range machineManagers {
		if machineManager.APIGroup == apiGroup && machineManager.Resource == resource {
			if machineManager.Selection.Mode == opv1.Partial {
				selector, err := metav1.LabelSelectorAsSelector(machineManager.Selection.Partial.MachineResourceSelector)
				return true, selector, err
			} else if machineManager.Selection.Mode == opv1.All {
				return true, labels.Everything(), nil
			}
		}
	}
	return false, nil, nil
}

// syncMachineResource will attempt to reconcile the machineresource with the given key.
// This function is not meant to be invoked concurrently with the same key.
func (ctrl *Controller) syncMachineResource(key string) error {

	// TODO: lower this level of all info logs in this function
	startTime := time.Now()
	klog.V(4).Infof("Started syncing machineset %q (%v)", key, startTime)
	defer func() {
		klog.V(4).Infof("Finished syncing machineset %q (%v)", key, time.Since(startTime))
	}()

	// Fetch the bootimage configmap
	configMap, err := ctrl.mcoCmLister.ConfigMaps(ctrlcommon.MCONamespace).Get(ctrlcommon.BootImagesConfigMapName)
	if configMap == nil || err != nil {
		return fmt.Errorf("failed to fetch coreos-bootimages config map during machineset sync: %w", err)
	}

	// Take no action if the MCO hash version stored in the configmap does not match the current controller
	// version. This is done by the operator when a master node successfully updates to a new image. This is
	// to prevent machinesets from being updated before the operator itself has updated.

	versionHashFromCM, versionHashFound := configMap.Data[ctrlcommon.MCOVersionHashKey]
	if !versionHashFound {
		return fmt.Errorf("failed to find mco version hash in %s configmap, sync will exit to wait for the MCO upgrade to complete", ctrlcommon.BootImagesConfigMapName)
	}
	if versionHashFromCM != operatorversion.Hash {
		return fmt.Errorf("mismatch between MCO hash version stored in configmap and current MCO version; sync will exit to wait for the MCO upgrade to complete")
	}
	releaseVersionFromCM, releaseVersionFound := configMap.Data[ctrlcommon.MCOReleaseImageVersionKey]
	if !releaseVersionFound {
		return fmt.Errorf("failed to find mco release version in %s configmap, sync will exit to wait for the MCO upgrade to complete", ctrlcommon.BootImagesConfigMapName)
	}
	if releaseVersionFromCM != operatorversion.ReleaseVersion {
		return fmt.Errorf("mismatch between MCO release version stored in configmap and current MCO release version; sync will exit to wait for the MCO upgrade to complete")
	}

	// TODO: Also check against the release version stored in the configmap under releaseVersion. This is currently broken as the version
	// stored is "0.0.1-snapshot" and does not reflect the correct value. Tracked in this bug https://issues.redhat.com/browse/OCPBUGS-19824
	// The current hash and version check should be enough to skate by for now, but fixing this would be additional safety - djoshy

	// When adding support for multiple kinds of machine resources, the "key" format can be updated to enclose the kind of machine resource. Once decoded,
	// an appropriate processing function can be called. For now only MAPI machinesets are supported, so proceed assuming that.
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}

	// Fetch the machineset
	machineSet, err := ctrl.mapiMachineSetLister.MachineSets(namespace).Get(name)
	if machineSet == nil || apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to fetch machineset object during machineset sync: %w", err)
	}

	// If the machineset has an owner reference, exit and report error. This means
	// that the machineset may be managed by another workflow and should not be reconciled.
	if len(machineSet.GetOwnerReferences()) != 0 {
		return fmt.Errorf("machineset object has an unexpected owner reference : %v and will not be updated. Please remove this machineset from boot image management to avoid errors", machineSet.GetOwnerReferences()[0])
	}

	if os, ok := machineSet.Spec.Template.Labels[OSLabelKey]; ok {
		if os == "Windows" {
			klog.Infof("machineset %s has a windows os label, skipping boot image update", machineSet.Name)
			return nil
		}
	}

	// Fetch the architecture type of this machineset
	arch, err := ctrl.getArchFromMachineSet(machineSet)
	if err != nil {
		return fmt.Errorf("failed to fetch arch during machineset sync: %w", err)
	}

	// Fetch the infra object to determine the platform type
	infra, err := ctrl.infraLister.Get("cluster")
	if err != nil {
		return fmt.Errorf("failed to fetch infra object during machineset sync: %w", err)
	}

	// Check if the this MachineSet requires an update
	patchRequired, newMachineSet, err := checkMachineSet(infra, machineSet, configMap, arch)
	if err != nil {
		return fmt.Errorf("failed to reconcile machineset %s, err: %w", machineSet.Name, err)
	}

	// Patch the machineset if required
	if patchRequired {
		klog.Infof("Patching MAPI machineset %s", machineSet.Name)
		return ctrl.patchMachineSet(machineSet, newMachineSet)
	}
	klog.Infof("No patching required for MAPI machineset %s", machineSet.Name)
	return nil
}

// Returns architecture type for a given machineset
func (ctrl *Controller) getArchFromMachineSet(machineset *machinev1beta1.MachineSet) (arch string, err error) {

	// Valid set of machineset/node architectures
	validArchSet := sets.New[string]("arm64", "s390x", "amd64", "ppc64le")
	// Check if the annotation enclosing arch label is present on this machineset
	archLabel, archLabelMatch := machineset.Annotations[MachineSetArchAnnotationKey]
	if archLabelMatch {
		// Grab arch value from the annotation and check if it is valid
		_, archLabelValue, archLabelValueFound := strings.Cut(archLabel, ArchLabelKey)
		if archLabelValueFound && validArchSet.Has(archLabelValue) {
			return archtranslater.RpmArch(archLabelValue), nil
		}
		return "", fmt.Errorf("invalid architecture value found in annotation: %s ", archLabel)
	}
	// If no arch annotation was found on the machineset, default to the control plane arch.
	// return the architecture of the node running this pod, which will always be a control plane node.
	klog.Infof("Defaulting to control plane architecture")
	return archtranslater.CurrentRpmArch(), nil
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

// This function calls the appropriate reconcile function based on the infra type
// On success, it will return a bool indicating if a patch is required, and an updated
// machineset object if any. It will return an error if any of the above steps fail.
func checkMachineSet(infra *osconfigv1.Infrastructure, machineSet *machinev1beta1.MachineSet, configMap *corev1.ConfigMap, arch string) (bool, *machinev1beta1.MachineSet, error) {
	switch infra.Status.PlatformStatus.Type {
	case osconfigv1.AWSPlatformType:
		return reconcileAWS(machineSet, configMap, arch)
	case osconfigv1.AzurePlatformType:
		return reconcileAzure(machineSet, configMap, arch)
	case osconfigv1.BareMetalPlatformType:
		return reconcileBareMetal(machineSet, configMap, arch)
	case osconfigv1.AlibabaCloudPlatformType:
		return reconcileAliBaba(machineSet, configMap, arch)
	case osconfigv1.OpenStackPlatformType:
		return reconcileOpenStack(machineSet, configMap, arch)
	case osconfigv1.EquinixMetalPlatformType:
		return reconcileEquinixMetal(machineSet, configMap, arch)
	case osconfigv1.GCPPlatformType:
		return reconcileGCP(machineSet, configMap, arch)
	case osconfigv1.KubevirtPlatformType:
		return reconcileKubevirt(machineSet, configMap, arch)
	case osconfigv1.IBMCloudPlatformType:
		return reconcileIBMCCloud(machineSet, configMap, arch)
	case osconfigv1.LibvirtPlatformType:
		return reconcileLibvirt(machineSet, configMap, arch)
	case osconfigv1.VSpherePlatformType:
		return reconcileVSphere(machineSet, configMap, arch)
	case osconfigv1.NutanixPlatformType:
		return reconcileNutanix(machineSet, configMap, arch)
	case osconfigv1.OvirtPlatformType:
		return reconcileOvirt(machineSet, configMap, arch)
	case osconfigv1.ExternalPlatformType:
		return reconcileExternal(machineSet, configMap, arch)
	case osconfigv1.PowerVSPlatformType:
		return reconcilePowerVS(machineSet, configMap, arch)
	case osconfigv1.NonePlatformType:
		return reconcileNone(machineSet, configMap, arch)
	default:
		return unmarshalToFindPlatform(machineSet, configMap, arch)
	}
}

// This function unmarshals the machineset's provider spec into
// a ProviderSpec object. Returns an error if providerSpec field is nil,
// or the unmarshal fails
func unmarshalProviderSpec(ms *machinev1beta1.MachineSet, providerSpec interface{}) error {
	if ms.Spec.Template.Spec.ProviderSpec.Value == nil {
		return fmt.Errorf("providerSpec field was empty")
	}
	if err := yaml.Unmarshal(ms.Spec.Template.Spec.ProviderSpec.Value.Raw, &providerSpec); err != nil {
		return fmt.Errorf("unmarshal into providerSpec failedL %w", err)
	}
	return nil
}

// This function marshals the ProviderSpec object into a MachineSet object.
// Returns an error if ProviderSpec or MachineSet is nil, or if the marshal fails
func marshalProviderSpec(ms *machinev1beta1.MachineSet, providerSpec interface{}) error {
	if ms == nil {
		return fmt.Errorf("MachineSet object was nil")
	}
	if providerSpec == nil {
		return fmt.Errorf("ProviderSpec object was nil")
	}
	rawBytes, err := json.Marshal(providerSpec)
	if err != nil {
		return fmt.Errorf("marshal into machineset failed: %w", err)
	}
	ms.Spec.Template.Spec.ProviderSpec.Value = &kruntime.RawExtension{Raw: rawBytes}
	return nil
}

// This function unmarshals the golden stream configmap into a coreos
// stream object. Returns an error if the unmarshal fails.
func unmarshalStreamDataConfigMap(cm *corev1.ConfigMap, st interface{}) error {
	if err := json.Unmarshal([]byte(cm.Data[StreamConfigMapKey]), &st); err != nil {
		return fmt.Errorf("failed to parse CoreOS stream metadata: %w", err)
	}
	return nil
}

// GCP reconciliation function. Key points:
// -GCP images aren't region specific
// -GCPMachineProviderSpec.Disk(s) stores actual bootimage URL
// -identical for x86_64/amd64 and aarch64/arm64
func reconcileGCP(machineSet *machinev1beta1.MachineSet, configMap *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Reconciling MAPI machineset %s on GCP, with arch %s", machineSet.Name, arch)

	// First, unmarshal the GCP providerSpec
	providerSpec := new(machinev1beta1.GCPMachineProviderSpec)
	if err := unmarshalProviderSpec(machineSet, providerSpec); err != nil {
		return false, nil, err
	}

	// Next, unmarshal the configmap into a stream object
	streamData := new(stream.Stream)
	if err := unmarshalStreamDataConfigMap(configMap, streamData); err != nil {
		return false, nil, err
	}

	// Construct the new target bootimage from the configmap
	// This formatting is based on how the installer constructs
	// the boot image during cluster bootstrap
	newBootImage := fmt.Sprintf("projects/%s/global/images/%s", streamData.Architectures[arch].Images.Gcp.Project, streamData.Architectures[arch].Images.Gcp.Name)

	// Grab what the current bootimage is, compare to the newBootImage
	// There is typically only one element in this Disk array, assume multiple to be safe
	patchRequired = false
	newProviderSpec := providerSpec.DeepCopy()
	for idx, disk := range newProviderSpec.Disks {
		if newBootImage != disk.Image {
			klog.Infof("New target boot image: %s", newBootImage)
			klog.Infof("Current image: %s", disk.Image)
			patchRequired = true
			newProviderSpec.Disks[idx].Image = newBootImage
		}
	}

	// For now, hardcode to the managed worker secret, until Custom Pool Booting is implemented. When that happens, this will have to
	// respect the pool this machineset is targeted for.
	if newProviderSpec.UserDataSecret.Name != ManagedWorkerSecretName {
		newProviderSpec.UserDataSecret.Name = ManagedWorkerSecretName
		patchRequired = true
	}

	// If patch is required, marshal the new providerspec into the machineset
	if patchRequired {
		newMachineSet = machineSet.DeepCopy()
		if err := marshalProviderSpec(newMachineSet, newProviderSpec); err != nil {
			return false, nil, err
		}
	}
	return patchRequired, newMachineSet, nil
}

func reconcileAWS(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type AWS with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

func reconcileAzure(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type Azure with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

func reconcileBareMetal(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type BareMetal with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

func reconcileAliBaba(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type AliBaba with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

func reconcileOpenStack(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type OpenStack with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

func reconcileEquinixMetal(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type EquinixMetal with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

func reconcileKubevirt(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type Kubevirt with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

func reconcileIBMCCloud(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type IBMCCloud with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

func reconcileLibvirt(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type Libvirt with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

func reconcileVSphere(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type VSphere with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

func reconcileNutanix(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type Nutanix with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

func reconcileOvirt(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type Ovirt with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

func reconcilePowerVS(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type PowerVS with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

func reconcileExternal(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type External with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

func reconcileNone(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type None with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

// TODO - unmarshal the providerspec into each ProviderSpec type until it succeeds,
// and then call the appropriate reconcile function. This is needed for multi platform
// support
func unmarshalToFindPlatform(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unknown platform type with %s arch", machineSet.Name, arch)
	return false, nil, nil
}
