package machineset

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	opv1 "github.com/openshift/api/operator/v1"
	configinformersv1 "github.com/openshift/client-go/config/informers/externalversions/config/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	mcopclientset "github.com/openshift/client-go/operator/clientset/versioned"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformersv1 "k8s.io/client-go/informers/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	corelisterv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
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

	featureGateAccess featuregates.FeatureGateAccess
}

const (
	// Name of machine api namespace
	MachineAPINamespace = "openshift-machine-api"

	// Key to access stream data from the boot images configmap
	StreamConfigMapKey = "stream"

	// Labels and Annotations required for determining architecture of a machineset
	MachineSetArchAnnotationKey = "capacity.cluster-autoscaler.kubernetes.io/labels"
	ArchLabelKey                = "kubernetes.io/arch="

	// Name of managed worker secret
	ManagedWorkerSecretName = "worker-user-data-managed"
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

	ctrl.mcoCmLister = mcoCmInfomer.Lister()
	ctrl.mapiMachineSetLister = mapiMachineSetInformer.Lister()
	ctrl.infraLister = infraInformer.Lister()
	ctrl.mcopLister = mcopInformer.Lister()

	ctrl.mcoCmListerSynced = mcoCmInfomer.Informer().HasSynced
	ctrl.mapiMachineSetListerSynced = mapiMachineSetInformer.Informer().HasSynced
	ctrl.infraListerSynced = infraInformer.Informer().HasSynced
	ctrl.mcopListerSynced = mcopInformer.Informer().HasSynced

	ctrl.featureGateAccess = featureGateAccess

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
	go func() { ctrl.syncMAPIMachineSets() }()
}

func (ctrl *Controller) updateMAPIMachineSet(oldMS, newMS interface{}) {

	oldMachineSet := oldMS.(*machinev1beta1.MachineSet)
	newMachineSet := newMS.(*machinev1beta1.MachineSet)

	// Don't take action if the there is no change in the MachineSet's ProviderSpec, labels and ownerreferences
	if reflect.DeepEqual(oldMachineSet.Spec.Template.Spec.ProviderSpec, newMachineSet.Spec.Template.Spec.ProviderSpec) && reflect.DeepEqual(oldMachineSet.GetLabels(), newMachineSet.GetLabels()) && reflect.DeepEqual(oldMachineSet.GetOwnerReferences(), newMachineSet.GetOwnerReferences()) {
		return
	}

	klog.Infof("MachineSet %s updated, reconciling enrolled machineset resources", oldMachineSet.Name)

	// Update all machinesets instead of just this one. This prevents needing to maintain a local
	// store of machineset conditions. As this is using a lister, it is relatively inexpensive to do
	// this.
	go func() { ctrl.syncMAPIMachineSets() }()
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
	go func() { ctrl.syncMAPIMachineSets() }()
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
	go func() { ctrl.syncMAPIMachineSets() }()
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
	go func() { ctrl.syncMAPIMachineSets() }()
}

func (ctrl *Controller) updateMachineConfiguration(oldMC, newMC interface{}) {

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
	// TODO: Add go routines for CAPI resources here
	go func() { ctrl.syncMAPIMachineSets() }()
}

// syncMAPIMachineSets will attempt to enqueue every machineset
func (ctrl *Controller) syncMAPIMachineSets() {

	// Grab the global operator knobs
	mcop, err := ctrl.mcopLister.Get(ctrlcommon.MCOOperatorKnobsObjectName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// This typically means the cache for the knobs lister is empty; don't error on this. If this
			// object doesn't exist, something else has gone wrong in the operator's syncMachineConfiguration loop
			klog.Infof("MachineConfiguration knobs was not found, so no MAPI machinesets will be enqueued.")
		} else {
			klog.Errorf("failed to fetch MachineConfiguration knobs while enqueueing MAPI MachineSets %v", err)
		}
		return
	}

	// If no machine managers exist; exit the enqueue process.
	if len(mcop.Spec.ManagedBootImages.MachineManagers) == 0 {
		klog.Infof("No MAPI machineset manager was found, so no MAPI machinesets will be enqueued.")
		return
	}

	machineManagerFound, machineResourceSelector, err := getMachineResourceSelectorFromMachineManagers(mcop.Spec.ManagedBootImages.MachineManagers, opv1.MachineAPI, opv1.MachineSets)
	if err != nil {
		klog.Errorf("failed to create a machineset selector while enqueueing MAPI machineset %v", err)
		return
	}
	if !machineManagerFound {
		klog.Infof("No MAPI machineset manager was found, so no MAPI machinesets will be enqueued.")
	}

	mapiMachineSets, err := ctrl.mapiMachineSetLister.List(machineResourceSelector)
	if err != nil {
		klog.Errorf("failed to fetch MachineSet list while enqueueing MAPI MachineSets %v", err)
		return
	}

	if len(mapiMachineSets) == 0 {
		klog.Infof("No MAPI machinesets were enrolled, so no MAPI machinesets will be enqueued.")
		return
	}

	klog.Infof("Reconciling %d MAPI MachineSet(s)", len(mapiMachineSets))

	for _, machineSet := range mapiMachineSets {
		err := ctrl.syncMAPIMachineSet(machineSet)
		if err != nil {
			klog.Errorf("Error syncing MAPI MachineSet %v", err)
		}
	}
}

// syncMAPIMachineSet will attempt to reconcile the provided machineset
func (ctrl *Controller) syncMAPIMachineSet(machineSet *machinev1beta1.MachineSet) error {

	// TODO: lower this level of all info logs in this function
	startTime := time.Now()
	klog.V(4).Infof("Started syncing MAPI machineset %q (%v)", machineSet.Name, startTime)
	defer func() {
		klog.V(4).Infof("Finished syncing MAPI machineset %q (%v)", machineSet.Name, time.Since(startTime))
	}()

	// Wait until the MCO hash version stored in the configmap matches the current MCO
	// version. This is done by the operator when a master node successfully updates to a new image. This is
	// to prevent machinesets from being updated before the operator itself has updated.
	// Could return an error(and cause degrade) immediately here, but seems excessive. Waiting with a timeout
	// is a bit more graceful.
	var configMap *corev1.ConfigMap
	var err error
	if err := wait.PollUntilContextTimeout(context.TODO(), 1*time.Minute, 15*time.Minute, true, func(ctx context.Context) (bool, error) {
		// Fetch the bootimage configmap
		configMap, err = ctrl.mcoCmLister.ConfigMaps(ctrlcommon.MCONamespace).Get(ctrlcommon.BootImagesConfigMapName)
		if configMap == nil || err != nil {
			err = fmt.Errorf("failed to fetch coreos-bootimages config map during machineset sync: %w", err)
			return false, nil
		}
		versionHashFromCM, versionHashFound := configMap.Data[ctrlcommon.MCOVersionHashKey]
		if !versionHashFound {
			err = fmt.Errorf("failed to find mco version hash in %s configmap, sync will exit to wait for the MCO upgrade to complete", ctrlcommon.BootImagesConfigMapName)
			return false, nil
		}
		if versionHashFromCM != operatorversion.Hash {
			err = fmt.Errorf("mismatch between MCO hash version stored in configmap and current MCO version; sync will exit to wait for the MCO upgrade to complete")
			return false, nil
		}
		releaseVersionFromCM, releaseVersionFound := configMap.Data[ctrlcommon.MCOReleaseImageVersionKey]
		if !releaseVersionFound {
			err = fmt.Errorf("failed to find mco release version in %s configmap, sync will exit to wait for the MCO upgrade to complete", ctrlcommon.BootImagesConfigMapName)
			return false, nil
		}
		if releaseVersionFromCM != operatorversion.ReleaseVersion {
			err = fmt.Errorf("mismatch between MCO release version stored in configmap and current MCO release version; sync will exit to wait for the MCO upgrade to complete")
			return false, nil
		}
		return true, nil

	}); err != nil {
		klog.Errorf("Timed out waiting for coreos-bootimages config map: %v", err)
		return fmt.Errorf("timed out waiting for coreos-bootimages config map: %v", err)
	}

	// TODO: Also check against the release version stored in the configmap under releaseVersion. This is currently broken as the version
	// stored is "0.0.1-snapshot" and does not reflect the correct value. Tracked in this bug https://issues.redhat.com/browse/OCPBUGS-19824
	// The current hash and version check should be enough to skate by for now, but fixing this would be additional safety - djoshy

	// If the machineset has an owner reference, exit and report error. This means
	// that the machineset may be managed by another workflow and should not be reconciled.
	if len(machineSet.GetOwnerReferences()) != 0 {
		return fmt.Errorf("machineset object has an unexpected owner reference : %v and will not be updated. Please remove this machineset from boot image management to avoid errors", machineSet.GetOwnerReferences()[0])
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
