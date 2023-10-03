package machineset

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	configinformersv1 "github.com/openshift/client-go/config/informers/externalversions/config/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	kruntime "k8s.io/apimachinery/pkg/runtime"
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
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/kubectl/pkg/scheme"
	"sigs.k8s.io/yaml"

	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"

	machineclientset "github.com/openshift/client-go/machine/clientset/versioned"
	machineinformers "github.com/openshift/client-go/machine/informers/externalversions/machine/v1beta1"
	machinelisters "github.com/openshift/client-go/machine/listers/machine/v1beta1"

	archtranslater "github.com/coreos/stream-metadata-go/arch"

	osconfigv1 "github.com/openshift/api/config/v1"
)

// Controller defines the machine-set-boot-image controller.
type Controller struct {
	kubeClient    clientset.Interface
	machineClient machineclientset.Interface
	eventRecorder record.EventRecorder

	mcoCmLister      corelisterv1.ConfigMapLister
	machineLister    machinelisters.MachineLister
	machineSetLister machinelisters.MachineSetLister
	maoSecretLister  corelisterv1.SecretLister
	infraLister      configlistersv1.InfrastructureLister
	nodeLister       corelisterv1.NodeLister

	mcoCmListerSynced       cache.InformerSynced
	machineListerSynced     cache.InformerSynced
	machineSetListerSynced  cache.InformerSynced
	maoSecretInformerSynced cache.InformerSynced
	infraListerSynced       cache.InformerSynced
	nodeListerSynced        cache.InformerSynced

	syncHandler       func(ms string) error
	enqueueMachineSet func(*machinev1beta1.MachineSet)

	queue workqueue.RateLimitingInterface
}

const (
	// maxRetries is the number of times a machineset will be retried before dropping out of queue
	maxRetries = 5
	// Name of machine api namespace
	MachineAPINamespace = "openshift-machine-api"

	// Key to access stream data from the boot images configmap
	StreamConfigMapKey = "stream"

	// Labels and Annotations required for determining architecture of a machineset
	MachineAnnotationKey = "machine.openshift.io/machine"
	ArchLabelKey         = "kubernetes.io/arch"
)

// New returns a new machine-set-boot-image controller.
func New(
	kubeClient clientset.Interface,
	machineClient machineclientset.Interface,
	mcoCmInfomer coreinformersv1.ConfigMapInformer,
	machineInformer machineinformers.MachineInformer,
	machinesetInformer machineinformers.MachineSetInformer,
	maoSecretInformer coreinformersv1.SecretInformer,
	infraInformer configinformersv1.InfrastructureInformer,
	nodeInformer coreinformersv1.NodeInformer,
) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&corev1client.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	ctrl := &Controller{
		kubeClient:    kubeClient,
		machineClient: machineClient,
		eventRecorder: ctrlcommon.NamespacedEventRecorder(eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineconfigcontroller-machinesetbootimagecontroller"})),
		queue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "machineconfigcontroller-machinesetbootimagecontroller"),
	}

	machinesetInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addMachineSet,
		UpdateFunc: ctrl.updateMachineSet,
	})

	mcoCmInfomer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addConfigMap,
		UpdateFunc: ctrl.updateConfigMap,
	})

	ctrl.syncHandler = ctrl.syncMachineSet
	ctrl.enqueueMachineSet = ctrl.enqueue

	ctrl.mcoCmLister = mcoCmInfomer.Lister()
	ctrl.machineLister = machineInformer.Lister()
	ctrl.machineSetLister = machinesetInformer.Lister()
	ctrl.maoSecretLister = maoSecretInformer.Lister()
	ctrl.infraLister = infraInformer.Lister()
	ctrl.nodeLister = nodeInformer.Lister()

	ctrl.mcoCmListerSynced = mcoCmInfomer.Informer().HasSynced
	ctrl.machineListerSynced = machineInformer.Informer().HasSynced
	ctrl.machineSetListerSynced = machinesetInformer.Informer().HasSynced
	ctrl.maoSecretInformerSynced = maoSecretInformer.Informer().HasSynced
	ctrl.infraListerSynced = infraInformer.Informer().HasSynced
	ctrl.nodeListerSynced = nodeInformer.Informer().HasSynced

	return ctrl
}

// Run executes the machine-set-boot-image controller.
func (ctrl *Controller) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()

	if !cache.WaitForCacheSync(stopCh, ctrl.mcoCmListerSynced, ctrl.machineSetListerSynced, ctrl.maoSecretInformerSynced, ctrl.infraListerSynced,
		ctrl.nodeListerSynced, ctrl.machineListerSynced) {
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
	if err == nil {
		ctrl.queue.Forget(key)
		return
	}

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

func (ctrl *Controller) addMachineSet(obj interface{}) {
	machineSet := obj.(*machinev1beta1.MachineSet)
	klog.Infof("MachineSet %s added", machineSet.Name)

	// Update/Check all machinesets instead of just this one. This prevents needing to maintain a local
	// store of machine set conditions. As this is using a lister, it is relatively inexpensive to do
	// this.
	err := ctrl.enqueueAllMachineSets()
	if err != nil {
		klog.Errorf("Error enqueuing all machine sets: %w", err)
	}
}

// TODO: This callback happens every ~15 minutes or so even if the machineset contents are not updated
// Perhaps worth implementing a diff check before starting the sync?

func (ctrl *Controller) updateMachineSet(old, _ interface{}) {
	oldMachineSet := old.(*machinev1beta1.MachineSet)
	klog.Infof("MachineSet %s updated, reconciling all machinesets", oldMachineSet.Name)

	// Update all machinesets instead of just this once. This prevents needing to maintain a local
	// store of machine set conditions. As this is using a lister, it is relatively inexpensive to do
	// this.
	err := ctrl.enqueueAllMachineSets()
	if err != nil {
		klog.Errorf("Error enqueuing all machine sets: %w", err)
	}
}

func (ctrl *Controller) addConfigMap(obj interface{}) {
	configMap := obj.(*corev1.ConfigMap)

	// Take no action if this isn't the "golden" config map
	if configMap.Name != ctrlcommon.BootImagesConfigMapName {
		klog.V(4).Infof("configMap %s added, but does not match %s, skipping bootimage sync", configMap.Name, ctrlcommon.BootImagesConfigMapName)
		return
	}

	klog.Infof("configMap %s added, reconciling all machine sets", configMap.Name)

	// Update all machine sets since the "golden" configmap has been updated
	err := ctrl.enqueueAllMachineSets()
	if err != nil {
		klog.Errorf("Error enqueuing all machine sets: %w", err)
	}
}

// TODO: This callback happens every ~15 minutes or so even if the configmap contents is not updated
// Perhaps worth implementing a diff check before starting the sync?
func (ctrl *Controller) updateConfigMap(old, _ interface{}) {
	oldConfigMap := old.(*corev1.ConfigMap)

	// Take no action if this isn't the "golden" config map
	if oldConfigMap.Name != ctrlcommon.BootImagesConfigMapName {
		klog.V(4).Infof("configMap %s updated, but does not match %s, skipping bootimage sync", oldConfigMap.Name, ctrlcommon.BootImagesConfigMapName)
		return
	}

	klog.Infof("configMap %s updated, reconciling all machine sets", oldConfigMap.Name)

	// Update all machine sets since the "golden" configmap has been updated
	ctrl.enqueueAllMachineSets()
}

func (ctrl *Controller) enqueue(ms *machinev1beta1.MachineSet) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(ms)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", ms, err))
		return
	}

	ctrl.queue.Add(key)
}

// syncMachineSet will attempt to enqueue every machineset
func (ctrl *Controller) enqueueAllMachineSets() error {

	machineSets, err := ctrl.machineSetLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("failed to fetch MachineSet list during config map update %w", err)
	}

	for _, machineSet := range machineSets {
		ctrl.enqueueMachineSet(machineSet)
	}

	return nil
}

// syncMachineSet will attempt to reconcile the machineset with the given key.
// This function is not meant to be invoked concurrently with the same key.
func (ctrl *Controller) syncMachineSet(key string) error {

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

	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}

	// Fetch the machineset
	machineSet, err := ctrl.machineSetLister.MachineSets(namespace).Get(name)
	if machineSet == nil || apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to fetch machineset object during machineset sync: %w", err)
	}

	// Fetch the architecture type of this machineset
	arch, err := ctrl.getArchFromMachineSet(machineSet)
	if err != nil {
		return fmt.Errorf("failed to fetch architecture type of machineset %s, err: %w", machineSet.Name, err)
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
		klog.Infof("Patching machineset %s", machineSet.Name)
		ctrl.patchMachineSet(machineSet, newMachineSet)
	} else {
		klog.Infof("No patching required for machineset %s", machineSet.Name)
	}
	return nil
}

// Returns architecture type for a given machine set
func (ctrl *Controller) getArchFromMachineSet(machineset *machinev1beta1.MachineSet) (arch string, err error) {
	machineSelector, err := metav1.LabelSelectorAsSelector(&machineset.Spec.Selector)

	if err != nil {
		return " ", fmt.Errorf("could not convert MachineSet label selector to selector, error: %w", err)
	}

	machines, err := ctrl.machineLister.List(machineSelector)
	if err != nil || len(machines) == 0 {
		return " ", fmt.Errorf("could not find any machines linked to machineset, error: %w", err)
	}

	// Any machine from this slice will be of the same architecture, so grab the first one
	// Cycle through nodes, compare to annotations to find node with matching machine

	nodes, err := ctrl.nodeLister.List(labels.Everything())
	for _, node := range nodes {
		if node.Annotations == nil {
			continue
		}
		machine, nodeMatch := node.Annotations[MachineAnnotationKey]
		_, machineName, err := cache.SplitMetaNamespaceKey(machine)
		if err != nil {
			return " ", fmt.Errorf("could not split machine name %s, error: %w", machineName, err)
		}
		// Compare machine name to machine obtained from earlier selector
		// Search the labels of the node to find the arch label
		if nodeMatch && machineName == machines[0].Name {
			archLabelValue, archFound := node.Labels[ArchLabelKey]
			if archFound {
				return archtranslater.RpmArch(archLabelValue), nil
			}
			return " ", fmt.Errorf("architecture of node %s could not be determined from labels", node.Name)

		}

	}
	// At this point, no node was found with this machineSet, exit sync attempt
	return " ", fmt.Errorf("could not find any nodes attached to this machineset, error: %w", err)
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
	klog.Infof("Skipping machineset %s, unsupported platform type GCP with %s arch", machineSet.Name, arch)
	return false, nil, nil
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
