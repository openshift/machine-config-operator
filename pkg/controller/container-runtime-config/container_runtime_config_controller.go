package containerruntimeconfig

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/clarketm/json"
	signature "github.com/containers/image/v5/signature"
	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	apicfgv1 "github.com/openshift/api/config/v1"
	features "github.com/openshift/api/features"
	apioperatorsv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	configclientset "github.com/openshift/client-go/config/clientset/versioned"
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	cligoinformersv1 "github.com/openshift/client-go/config/informers/externalversions/config/v1"
	cligolistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	runtimeutils "github.com/openshift/runtime-utils/pkg/registries"

	operatorinformersv1alpha1 "github.com/openshift/client-go/operator/informers/externalversions/operator/v1alpha1"

	operatorlistersv1alpha1 "github.com/openshift/client-go/operator/listers/operator/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	kubeErrs "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	coreclientsetv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/scheme"
	mcfginformersv1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	apihelpers "github.com/openshift/machine-config-operator/pkg/apihelpers"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	mtmpl "github.com/openshift/machine-config-operator/pkg/controller/template"
	"github.com/openshift/machine-config-operator/pkg/version"
)

const (
	// maxRetries is the number of times a containerruntimeconfig pool will be retried before it is dropped out of the queue.
	// With the current rate-limiter in use (5ms*2^(maxRetries-1)) the following numbers represent the times
	// a machineconfig pool is going to be requeued:
	//
	// 5ms, 10ms, 20ms, 40ms, 80ms, 160ms, 320ms, 640ms, 1.3s, 2.6s, 5.1s, 10.2s, 20.4s, 41s, 82s
	maxRetries = 15

	builtInLabelKey    = "machineconfiguration.openshift.io/mco-built-in"
	configMapName      = "crio-default-container-runtime"
	forceSyncOnUpgrade = "force-sync-on-upgrade"
)

var (
	// controllerKind contains the schema.GroupVersionKind for this controller type.
	controllerKind = mcfgv1.SchemeGroupVersion.WithKind("ContainerRuntimeConfig")
)

var updateBackoff = wait.Backoff{
	Steps:    5,
	Duration: 100 * time.Millisecond,
	Jitter:   1.0,
}

// Controller defines the container runtime config controller.
type Controller struct {
	templatesDir string

	client        mcfgclientset.Interface
	kubeClient    clientset.Interface
	configClient  configclientset.Interface
	eventRecorder record.EventRecorder

	syncHandler                   func(mcp string) error
	syncImgHandler                func(mcp string) error
	enqueueContainerRuntimeConfig func(*mcfgv1.ContainerRuntimeConfig)

	ccLister       mcfglistersv1.ControllerConfigLister
	ccListerSynced cache.InformerSynced

	mccrLister       mcfglistersv1.ContainerRuntimeConfigLister
	mccrListerSynced cache.InformerSynced

	imgLister       cligolistersv1.ImageLister
	imgListerSynced cache.InformerSynced

	icspLister       operatorlistersv1alpha1.ImageContentSourcePolicyLister
	icspListerSynced cache.InformerSynced

	idmsLister       cligolistersv1.ImageDigestMirrorSetLister
	idmsListerSynced cache.InformerSynced

	itmsLister       cligolistersv1.ImageTagMirrorSetLister
	itmsListerSynced cache.InformerSynced

	configInformerFactory          configinformers.SharedInformerFactory
	clusterImagePolicyLister       cligolistersv1.ClusterImagePolicyLister
	clusterImagePolicyListerSynced cache.InformerSynced

	imagePolicyLister       cligolistersv1.ImagePolicyLister
	imagePolicyListerSynced cache.InformerSynced
	addedPolicyObservers    bool

	mcpLister       mcfglistersv1.MachineConfigPoolLister
	mcpListerSynced cache.InformerSynced

	mckLister       mcfglistersv1.KubeletConfigLister
	mckListerSynced cache.InformerSynced

	apiserverLister       cligolistersv1.APIServerLister
	apiserverListerSynced cache.InformerSynced

	clusterVersionLister       cligolistersv1.ClusterVersionLister
	clusterVersionListerSynced cache.InformerSynced

	fgHandler ctrlcommon.FeatureGatesHandler

	queue    workqueue.TypedRateLimitingInterface[string]
	imgQueue workqueue.TypedRateLimitingInterface[string]
}

// New returns a new container runtime config controller
func New(
	templatesDir string,
	mcpInformer mcfginformersv1.MachineConfigPoolInformer,
	ccInformer mcfginformersv1.ControllerConfigInformer,
	mcrInformer mcfginformersv1.ContainerRuntimeConfigInformer,
	mkuInformer mcfginformersv1.KubeletConfigInformer,
	imgInformer cligoinformersv1.ImageInformer,
	idmsInformer cligoinformersv1.ImageDigestMirrorSetInformer,
	itmsInformer cligoinformersv1.ImageTagMirrorSetInformer,
	configInformerFactory configinformers.SharedInformerFactory,
	icspInformer operatorinformersv1alpha1.ImageContentSourcePolicyInformer,
	clusterVersionInformer cligoinformersv1.ClusterVersionInformer,
	kubeClient clientset.Interface,
	mcfgClient mcfgclientset.Interface,
	configClient configclientset.Interface,
	fgHandler ctrlcommon.FeatureGatesHandler,
) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&coreclientsetv1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	ctrl := &Controller{
		templatesDir:  templatesDir,
		client:        mcfgClient,
		kubeClient:    kubeClient,
		configClient:  configClient,
		eventRecorder: ctrlcommon.NamespacedEventRecorder(eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineconfigcontroller-containerruntimeconfigcontroller"})),
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "machineconfigcontroller-containerruntimeconfigcontroller"}),
		imgQueue: workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]()),
	}

	mcrInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addContainerRuntimeConfig,
		UpdateFunc: ctrl.updateContainerRuntimeConfig,
		DeleteFunc: ctrl.deleteContainerRuntimeConfig,
	})

	imgInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.imageConfAdded,
		UpdateFunc: ctrl.imageConfUpdated,
		DeleteFunc: ctrl.imageConfDeleted,
	})

	icspInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.icspConfAdded,
		UpdateFunc: ctrl.icspConfUpdated,
		DeleteFunc: ctrl.icspConfDeleted,
	})

	idmsInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.idmsConfAdded,
		UpdateFunc: ctrl.idmsConfUpdated,
		DeleteFunc: ctrl.idmsConfDeleted,
	})

	itmsInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.itmsConfAdded,
		UpdateFunc: ctrl.itmsConfUpdated,
		DeleteFunc: ctrl.itmsConfDeleted,
	})

	mkuInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.kubeletConfigAdded,
		UpdateFunc: ctrl.kubeletConfigUpdated,
		DeleteFunc: ctrl.kubeletConfigDeleted,
	})

	apiserverInformer := configInformerFactory.Config().V1().APIServers()
	apiserverInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.apiserverAdded,
		UpdateFunc: ctrl.apiserverUpdated,
		DeleteFunc: ctrl.apiserverDeleted,
	})

	ctrl.syncHandler = ctrl.syncContainerRuntimeConfig
	ctrl.syncImgHandler = ctrl.syncImageConfig
	ctrl.enqueueContainerRuntimeConfig = ctrl.enqueue

	ctrl.mcpLister = mcpInformer.Lister()
	ctrl.mcpListerSynced = mcpInformer.Informer().HasSynced

	ctrl.ccLister = ccInformer.Lister()
	ctrl.ccListerSynced = ccInformer.Informer().HasSynced

	ctrl.mccrLister = mcrInformer.Lister()
	ctrl.mccrListerSynced = mcrInformer.Informer().HasSynced

	ctrl.mckLister = mkuInformer.Lister()
	ctrl.mckListerSynced = mkuInformer.Informer().HasSynced

	ctrl.apiserverLister = apiserverInformer.Lister()
	ctrl.apiserverListerSynced = apiserverInformer.Informer().HasSynced

	ctrl.imgLister = imgInformer.Lister()
	ctrl.imgListerSynced = imgInformer.Informer().HasSynced

	ctrl.icspLister = icspInformer.Lister()
	ctrl.icspListerSynced = icspInformer.Informer().HasSynced

	ctrl.idmsLister = idmsInformer.Lister()
	ctrl.idmsListerSynced = idmsInformer.Informer().HasSynced

	ctrl.itmsLister = itmsInformer.Lister()
	ctrl.itmsListerSynced = itmsInformer.Informer().HasSynced

	ctrl.clusterVersionLister = clusterVersionInformer.Lister()
	ctrl.clusterVersionListerSynced = clusterVersionInformer.Informer().HasSynced
	ctrl.queue.Add(forceSyncOnUpgrade)

	ctrl.fgHandler = fgHandler

	ctrl.configInformerFactory = configInformerFactory

	return ctrl
}

// Run executes the container runtime config controller.
func (ctrl *Controller) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()
	defer ctrl.imgQueue.ShutDown()
	listerCaches := []cache.InformerSynced{ctrl.mcpListerSynced, ctrl.mccrListerSynced, ctrl.ccListerSynced,
		ctrl.mckListerSynced, ctrl.apiserverListerSynced, ctrl.imgListerSynced, ctrl.icspListerSynced, ctrl.idmsListerSynced, ctrl.itmsListerSynced, ctrl.clusterVersionListerSynced}

	if ctrl.sigstoreAPIEnabled() {
		ctrl.addImagePolicyObservers()
		klog.Info("addded image policy observers with sigstore featuregate enabled")
		ctrl.configInformerFactory.Start(stopCh)
		listerCaches = append(listerCaches, ctrl.clusterImagePolicyListerSynced, ctrl.imagePolicyListerSynced)
		ctrl.addedPolicyObservers = true
	}

	if !cache.WaitForCacheSync(stopCh, listerCaches...) {
		return
	}

	klog.Info("Starting MachineConfigController-ContainerRuntimeConfigController")
	defer klog.Info("Shutting down MachineConfigController-ContainerRuntimeConfigController")

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.worker, time.Second, stopCh)
	}

	// Just need one worker for the image config
	go wait.Until(ctrl.imgWorker, time.Second, stopCh)

	<-stopCh
}

func ctrConfigTriggerObjectChange(old, newCRC *mcfgv1.ContainerRuntimeConfig) bool {
	if old.DeletionTimestamp != newCRC.DeletionTimestamp {
		return true
	}
	if !reflect.DeepEqual(old.Spec, newCRC.Spec) {
		return true
	}
	return false
}

func (ctrl *Controller) imageConfAdded(_ interface{}) {
	ctrl.imgQueue.Add("openshift-config")
}

func (ctrl *Controller) imageConfUpdated(_, _ interface{}) {
	ctrl.imgQueue.Add("openshift-config")
}

func (ctrl *Controller) imageConfDeleted(_ interface{}) {
	ctrl.imgQueue.Add("openshift-config")
}

func (ctrl *Controller) icspConfAdded(_ interface{}) {
	ctrl.imgQueue.Add("openshift-config")
}

func (ctrl *Controller) icspConfUpdated(_, _ interface{}) {
	ctrl.imgQueue.Add("openshift-config")
}

func (ctrl *Controller) icspConfDeleted(_ interface{}) {
	ctrl.imgQueue.Add("openshift-config")
}

func (ctrl *Controller) idmsConfAdded(_ interface{}) {
	ctrl.imgQueue.Add("openshift-config")
}

func (ctrl *Controller) idmsConfUpdated(_, _ interface{}) {
	ctrl.imgQueue.Add("openshift-config")
}

func (ctrl *Controller) idmsConfDeleted(_ interface{}) {
	ctrl.imgQueue.Add("openshift-config")
}

func (ctrl *Controller) itmsConfAdded(_ interface{}) {
	ctrl.imgQueue.Add("openshift-config")
}

func (ctrl *Controller) itmsConfUpdated(_, _ interface{}) {
	ctrl.imgQueue.Add("openshift-config")
}

func (ctrl *Controller) itmsConfDeleted(_ interface{}) {
	ctrl.imgQueue.Add("openshift-config")
}

func (ctrl *Controller) kubeletConfigAdded(_ interface{}) {
	ctrl.enqueueAllContainerRuntimeConfigs()
}

func (ctrl *Controller) kubeletConfigUpdated(_, _ interface{}) {
	ctrl.enqueueAllContainerRuntimeConfigs()
}

func (ctrl *Controller) kubeletConfigDeleted(_ interface{}) {
	ctrl.enqueueAllContainerRuntimeConfigs()
}

func (ctrl *Controller) filterAPIServer(apiServer *apicfgv1.APIServer) {
	if apiServer.Name != ctrlcommon.APIServerInstanceName {
		return
	}
	klog.Infof("Re-syncing all ContainerRuntimeConfigs due to APIServer %s change (TLS fallback)", apiServer.Name)
	ctrl.enqueueAllContainerRuntimeConfigs()
}

func (ctrl *Controller) apiserverAdded(obj interface{}) {
	apiServer := obj.(*apicfgv1.APIServer)
	if apiServer.DeletionTimestamp != nil {
		ctrl.apiserverDeleted(apiServer)
		return
	}
	klog.V(4).Infof("Add APIServer %v", apiServer)
	ctrl.filterAPIServer(apiServer)
}

func (ctrl *Controller) apiserverUpdated(old, cur interface{}) {
	oldAPIServer := old.(*apicfgv1.APIServer)
	newAPIServer := cur.(*apicfgv1.APIServer)
	if !reflect.DeepEqual(oldAPIServer.Spec, newAPIServer.Spec) {
		klog.V(4).Infof("Update APIServer: %s", newAPIServer.Name)
		ctrl.filterAPIServer(newAPIServer)
	}
}

func (ctrl *Controller) apiserverDeleted(obj interface{}) {
	apiServer, ok := obj.(*apicfgv1.APIServer)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("couldn't get object from tombstone %#v", obj))
			return
		}
		apiServer, ok = tombstone.Obj.(*apicfgv1.APIServer)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not an APIServer %#v", obj))
			return
		}
	}
	klog.V(4).Infof("Delete APIServer %v", apiServer)
	ctrl.filterAPIServer(apiServer)
}

// enqueueAllContainerRuntimeConfigs lists all ContainerRuntimeConfig CRs and
// enqueues them for re-sync.
func (ctrl *Controller) enqueueAllContainerRuntimeConfigs() {
	crConfigs, err := ctrl.mccrLister.List(labels.Everything())
	if err != nil {
		klog.Warningf("error listing ContainerRuntimeConfigs for re-sync: %v", err)
		return
	}
	for _, cfg := range crConfigs {
		ctrl.enqueueContainerRuntimeConfig(cfg)
	}
}

func (ctrl *Controller) addImagePolicyObservers() {
	ctrl.configInformerFactory.Config().V1().ClusterImagePolicies().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.clusterImagePolicyAdded,
		UpdateFunc: ctrl.clusterImagePolicyUpdated,
		DeleteFunc: ctrl.clusterImagePolicyDeleted,
	})
	ctrl.clusterImagePolicyLister = ctrl.configInformerFactory.Config().V1().ClusterImagePolicies().Lister()
	ctrl.clusterImagePolicyListerSynced = ctrl.configInformerFactory.Config().V1().ClusterImagePolicies().Informer().HasSynced

	ctrl.configInformerFactory.Config().V1().ImagePolicies().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.imagePolicyAdded,
		UpdateFunc: ctrl.imagePolicyUpdated,
		DeleteFunc: ctrl.imagePolicyDeleted,
	})
	ctrl.imagePolicyLister = ctrl.configInformerFactory.Config().V1().ImagePolicies().Lister()
	ctrl.imagePolicyListerSynced = ctrl.configInformerFactory.Config().V1().ImagePolicies().Informer().HasSynced
}

func (ctrl *Controller) clusterImagePolicyAdded(_ interface{}) {
	ctrl.imgQueue.Add("openshift-config")
}

func (ctrl *Controller) clusterImagePolicyUpdated(_, _ interface{}) {
	ctrl.imgQueue.Add("openshift-config")
}

func (ctrl *Controller) clusterImagePolicyDeleted(_ interface{}) {
	ctrl.imgQueue.Add("openshift-config")
}

func (ctrl *Controller) imagePolicyAdded(_ interface{}) {
	ctrl.imgQueue.Add("openshift-config")
}

func (ctrl *Controller) imagePolicyUpdated(_, _ interface{}) {
	ctrl.imgQueue.Add("openshift-config")
}

func (ctrl *Controller) imagePolicyDeleted(_ interface{}) {
	ctrl.imgQueue.Add("openshift-config")
}

func (ctrl *Controller) sigstoreAPIEnabled() bool {
	return ctrl.fgHandler.Enabled(features.FeatureGateSigstoreImageVerification)
}

func (ctrl *Controller) updateContainerRuntimeConfig(oldObj, newObj interface{}) {
	oldCtrCfg := oldObj.(*mcfgv1.ContainerRuntimeConfig)
	newCtrCfg := newObj.(*mcfgv1.ContainerRuntimeConfig)

	if ctrConfigTriggerObjectChange(oldCtrCfg, newCtrCfg) {
		klog.V(4).Infof("Update ContainerRuntimeConfig %s", oldCtrCfg.Name)
		ctrl.enqueueContainerRuntimeConfig(newCtrCfg)
	}
}

func (ctrl *Controller) addContainerRuntimeConfig(obj interface{}) {
	cfg := obj.(*mcfgv1.ContainerRuntimeConfig)
	klog.V(4).Infof("Adding ContainerRuntimeConfig %s", cfg.Name)
	ctrl.enqueueContainerRuntimeConfig(cfg)
}

func (ctrl *Controller) deleteContainerRuntimeConfig(obj interface{}) {
	cfg, ok := obj.(*mcfgv1.ContainerRuntimeConfig)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("couldn't get object from tombstone %#v", obj))
			return
		}
		cfg, ok = tombstone.Obj.(*mcfgv1.ContainerRuntimeConfig)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not a ContainerRuntimeConfig %#v", obj))
			return
		}
	}
	if err := ctrl.cascadeDelete(cfg); err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't delete object %#v: %w", cfg, err))
	} else {
		klog.V(4).Infof("Deleted ContainerRuntimeConfig %s and restored default config", cfg.Name)
	}
}

func (ctrl *Controller) cascadeDelete(cfg *mcfgv1.ContainerRuntimeConfig) error {
	if len(cfg.GetFinalizers()) == 0 {
		return nil
	}
	mcName := cfg.GetFinalizers()[0]
	err := ctrl.client.MachineconfigurationV1().MachineConfigs().Delete(context.TODO(), mcName, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	return ctrl.popFinalizerFromContainerRuntimeConfig(cfg)
}

func (ctrl *Controller) enqueue(cfg *mcfgv1.ContainerRuntimeConfig) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(cfg)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", cfg, err))
		return
	}
	ctrl.queue.Add(key)
}

func (ctrl *Controller) enqueueRateLimited(cfg *mcfgv1.ContainerRuntimeConfig) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(cfg)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", cfg, err))
		return
	}
	ctrl.queue.AddRateLimited(key)
}

// worker runs a worker thread that just dequeues items, processes them, and marks them done.
// It enforces that the syncHandler is never invoked concurrently with the same key.
func (ctrl *Controller) worker() {
	for ctrl.processNextWorkItem() {
	}
}

func (ctrl *Controller) imgWorker() {
	for ctrl.processNextImgWorkItem() {
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

func (ctrl *Controller) processNextImgWorkItem() bool {
	key, quit := ctrl.imgQueue.Get()
	if quit {
		return false
	}
	defer ctrl.imgQueue.Done(key)

	err := ctrl.syncImgHandler(key)
	ctrl.handleImgErr(err, key)

	return true
}

func (ctrl *Controller) handleErr(err error, key string) {
	if err == nil {
		ctrl.queue.Forget(key)
		return
	}

	if ctrl.queue.NumRequeues(key) < maxRetries {
		klog.V(2).Infof("Error syncing containerruntimeconfig %v: %v", key, err)
		ctrl.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	klog.V(2).Infof("Dropping containerruntimeconfig %q out of the queue: %v", key, err)
	ctrl.queue.Forget(key)
	ctrl.queue.AddAfter(key, 1*time.Minute)
}

func (ctrl *Controller) handleImgErr(err error, key string) {
	if err == nil {
		ctrl.imgQueue.Forget(key)
		return
	}

	if ctrl.imgQueue.NumRequeues(key) < maxRetries {
		klog.V(2).Infof("Error syncing image config %v: %v", key, err)
		ctrl.imgQueue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	klog.V(2).Infof("Dropping image config %q out of the queue: %v", key, err)
	ctrl.imgQueue.Forget(key)
	ctrl.imgQueue.AddAfter(key, 1*time.Minute)
}

// generateOriginalContainerRuntimeConfigs returns rendered default storage, registries and policy config files
func generateOriginalContainerRuntimeConfigs(templateDir string, cc *mcfgv1.ControllerConfig, role string) (*ign3types.File, *ign3types.File, *ign3types.File, error) {
	// Render the default templates
	rc := &mtmpl.RenderConfig{
		ControllerConfigSpec: &cc.Spec,
	}
	generatedConfigs, err := mtmpl.GenerateMachineConfigsForRole(rc, role, templateDir)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generateMachineConfigsforRole failed with error %w", err)
	}
	// Find generated storage.conf, registries.conf, and policy.json
	var (
		config, gmcStorageConfig, gmcRegistriesConfig, gmcPolicyJSON *ign3types.File
		errStorage, errRegistries, errPolicy                         error
	)
	// Find storage config
	for _, gmc := range generatedConfigs {
		config, errStorage = findStorageConfig(gmc)
		if errStorage == nil {
			gmcStorageConfig = config
			break
		}
	}
	// Find Registries config
	for _, gmc := range generatedConfigs {
		config, errRegistries = findRegistriesConfig(gmc)
		if errRegistries == nil {
			gmcRegistriesConfig = config
			break
		}
	}
	// Find Policy JSON
	for _, gmc := range generatedConfigs {
		config, errPolicy = findPolicyJSON(gmc)
		if errPolicy == nil {
			gmcPolicyJSON = config
			break
		}
	}
	if errStorage != nil || errRegistries != nil || errPolicy != nil {
		errs := kubeErrs.NewAggregate([]error{errStorage, errRegistries, errPolicy})
		return nil, nil, nil, fmt.Errorf("could not generate old container runtime configs: %w", errs)
	}

	return gmcStorageConfig, gmcRegistriesConfig, gmcPolicyJSON, nil
}

func (ctrl *Controller) syncStatusOnly(cfg *mcfgv1.ContainerRuntimeConfig, err error, args ...interface{}) error {
	statusUpdateErr := retry.RetryOnConflict(updateBackoff, func() error {
		newcfg, getErr := ctrl.mccrLister.Get(cfg.Name)
		if getErr != nil {
			return getErr
		}
		// Update the observedGeneration
		if newcfg.GetGeneration() != newcfg.Status.ObservedGeneration {
			newcfg.Status.ObservedGeneration = newcfg.GetGeneration()
		}
		// To avoid a long list of same statuses, only append a status if it is the first status
		// or if the status message is different from the message of the last status recorded
		// If the last status message is the same as the new one, then update the last status to
		// reflect the latest time stamp from the new status message.
		newStatusCondition := wrapErrorWithCondition(err, args...)
		if len(newcfg.Status.Conditions) == 0 || newStatusCondition.Message != newcfg.Status.Conditions[len(newcfg.Status.Conditions)-1].Message {
			newcfg.Status.Conditions = append(newcfg.Status.Conditions, newStatusCondition)
		} else if newcfg.Status.Conditions[len(newcfg.Status.Conditions)-1].Message == newStatusCondition.Message {
			newcfg.Status.Conditions[len(newcfg.Status.Conditions)-1] = newStatusCondition
		}
		_, updateErr := ctrl.client.MachineconfigurationV1().ContainerRuntimeConfigs().UpdateStatus(context.TODO(), newcfg, metav1.UpdateOptions{})
		return updateErr
	})
	// If an error occurred in updating the status just log it
	if statusUpdateErr != nil {
		klog.Warningf("error updating container runtime config status: %v", statusUpdateErr)
	}
	// Want to return the actual error received from the sync function
	return err
}

// addAnnotation adds the annotions for a ctrcfg object with the given annotationKey and annotationVal
func (ctrl *Controller) addAnnotation(cfg *mcfgv1.ContainerRuntimeConfig, annotationKey, annotationVal string) error {
	annotationUpdateErr := retry.RetryOnConflict(updateBackoff, func() error {
		newcfg, getErr := ctrl.mccrLister.Get(cfg.Name)
		if getErr != nil {
			return getErr
		}
		newcfg.SetAnnotations(map[string]string{
			annotationKey: annotationVal,
		})
		_, updateErr := ctrl.client.MachineconfigurationV1().ContainerRuntimeConfigs().Update(context.TODO(), newcfg, metav1.UpdateOptions{})
		return updateErr
	})
	if annotationUpdateErr != nil {
		klog.Warningf("error updating the container runtime config with annotation key %q and value %q: %v", annotationKey, annotationVal, annotationUpdateErr)
	}
	return annotationUpdateErr
}

// migrateRuncToCrun performs the upgrade migration from runc to crun as the default container runtime.
// This function checks for the existence of the crio-default-container-runtime ConfigMap. If it exists,
// it deletes the MachineConfigs for master and worker pools, then deletes the ConfigMap to prevent
// re-running the migration.
func (ctrl *Controller) migrateRuncToCrun() error {
	// Check if the migration ConfigMap exists
	_, err := ctrl.kubeClient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(context.TODO(), configMapName, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		// ConfigMap doesn't exist, no migration needed
		return nil
	}
	if err != nil {
		return fmt.Errorf("error checking for crio-default-container-runtime configmap: %w", err)
	}

	klog.Info("Found crio-default-container-runtime ConfigMap, starting migration from runc to crun")

	// Get all MachineConfigPools
	pools, err := ctrl.mcpLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("error listing MachineConfigPools: %w", err)
	}

	// Only process master and worker pools for the migration
	for _, pool := range pools {
		if pool.Name != ctrlcommon.MachineConfigPoolMaster && pool.Name != ctrlcommon.MachineConfigPoolWorker {
			continue
		}

		// Get the MachineConfig name for this pool
		mcName := fmt.Sprintf("00-override-%s-generated-crio-default-container-runtime", pool.Name)

		// Delete the existing MachineConfig
		err := ctrl.client.MachineconfigurationV1().MachineConfigs().Delete(context.TODO(), mcName, metav1.DeleteOptions{})
		if errors.IsNotFound(err) {
			klog.Infof("MachineConfig %s not found, skipping migration for pool %s", mcName, pool.Name)
			continue
		}
		if err != nil {
			return fmt.Errorf("error deleting MachineConfig %s: %w", mcName, err)
		}

		klog.Infof("Successfully deleted MachineConfig %s", mcName)
	}

	// Delete the ConfigMap after successful migration
	if err := ctrl.kubeClient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Delete(context.TODO(), configMapName, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("error deleting crio-default-container-runtime configmap: %w", err)
	}

	klog.Info("Successfully completed migration from runc to crun and deleted migration ConfigMap")
	return nil
}

// syncContainerRuntimeConfig will sync the ContainerRuntimeconfig with the given key.
// This function is not meant to be invoked concurrently with the same key.
// nolint: gocyclo
func (ctrl *Controller) syncContainerRuntimeConfig(key string) error {
	startTime := time.Now()
	klog.V(4).Infof("Started syncing ContainerRuntimeconfig %q (%v)", key, startTime)
	defer func() {
		klog.V(4).Infof("Finished syncing ContainerRuntimeconfig %q (%v)", key, time.Since(startTime))
	}()

	// OKD only: Run the migration function at the start of sync
	if version.IsSCOS() {
		if err := ctrl.migrateRuncToCrun(); err != nil {
			return fmt.Errorf("Error during runc to crun migration: %w", err)
		}
	}

	if key == forceSyncOnUpgrade {
		return nil
	}

	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}

	// Fetch the ContainerRuntimeConfig
	cfg, err := ctrl.mccrLister.Get(name)
	if errors.IsNotFound(err) {
		klog.V(2).Infof("ContainerRuntimeConfig %v has been deleted", key)
		return nil
	}
	if err != nil {
		return err
	}

	// Deep-copy otherwise we are mutating our cache.
	cfg = cfg.DeepCopy()

	// Check for Deleted ContainerRuntimeConfig and optionally delete finalizers
	if cfg.DeletionTimestamp != nil {
		if len(cfg.GetFinalizers()) > 0 {
			return ctrl.cascadeDelete(cfg)
		}
		return nil
	}

	// Validate the ContainerRuntimeConfig CR
	if err := validateUserContainerRuntimeConfig(cfg); err != nil {
		return ctrl.syncStatusOnly(cfg, err)
	}

	// Get ControllerConfig
	controllerConfig, err := ctrl.ccLister.Get(ctrlcommon.ControllerConfigName)
	if err != nil {
		return fmt.Errorf("could not get ControllerConfig %w", err)
	}

	// Find all MachineConfigPools
	mcpPools, err := ctrl.getPoolsForContainerRuntimeConfig(cfg)
	if err != nil {
		return ctrl.syncStatusOnly(cfg, err)
	}

	if len(mcpPools) == 0 {
		err := fmt.Errorf("containerRuntimeConfig %v does not match any MachineConfigPools", key)
		klog.V(2).Infof("%v", err)
		return ctrl.syncStatusOnly(cfg, err)
	}

	for _, pool := range mcpPools {
		role := pool.Name
		// Get MachineConfig
		managedKey, err := getManagedKeyCtrCfg(pool, ctrl.client, cfg)
		if err != nil {
			return ctrl.syncStatusOnly(cfg, err, "could not get ctrcfg key: %v", err)
		}
		mc, err := ctrl.client.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), managedKey, metav1.GetOptions{})
		isNotFound := errors.IsNotFound(err)
		if err != nil && !isNotFound {
			return ctrl.syncStatusOnly(cfg, err, "could not find MachineConfig: %v", managedKey)
		}
		// we can skip re-sync only when controller version and TLS settings (from KubeletConfig or APIServer fallback) are unchanged
		if !isNotFound && cfg.Status.ObservedGeneration >= cfg.Generation && cfg.Status.Conditions[len(cfg.Status.Conditions)-1].Type == mcfgv1.ContainerRuntimeConfigSuccess {
			mcCtrlVersion := mc.Annotations[ctrlcommon.GeneratedByControllerVersionAnnotationKey]
			currentTLSVersion := ctrl.getTLSMinVersionForPool(pool)
			lastTLSVersion := mc.Annotations[ctrlcommon.CRIOTLSMinVersionAnnotationKey]

			if mcCtrlVersion == version.Hash && currentTLSVersion == lastTLSVersion {
				return nil
			}
			if currentTLSVersion != lastTLSVersion {
				klog.Infof("TLS min version changed from %q to %q for pool %s, forcing re-sync", lastTLSVersion, currentTLSVersion, role)
			}
		}
		// Generate the original ContainerRuntimeConfig
		originalStorageIgn, _, _, err := generateOriginalContainerRuntimeConfigs(ctrl.templatesDir, controllerConfig, role)
		if err != nil {
			return ctrl.syncStatusOnly(cfg, err, "could not generate origin ContainerRuntime Configs: %v", err)
		}

		var configFileList []generatedConfigFile
		ctrcfg := cfg.Spec.ContainerRuntimeConfig
		if ctrcfg.OverlaySize != nil && !ctrcfg.OverlaySize.IsZero() {
			storageTOML, err := mergeConfigChanges(originalStorageIgn, cfg, updateStorageConfig)
			if err != nil {
				klog.V(2).Infoln(cfg, err, "error merging user changes to storage.conf: %v", err)
				ctrl.syncStatusOnly(cfg, err)
			} else {
				configFileList = append(configFileList, generatedConfigFile{filePath: storageConfigPath, data: storageTOML})
				ctrl.syncStatusOnly(cfg, nil)
			}
		}

		// Create the cri-o drop-in files
		if ctrcfg.LogLevel != "" || ctrcfg.PidsLimit != nil || (ctrcfg.LogSizeMax != nil && !ctrcfg.LogSizeMax.IsZero()) || ctrcfg.DefaultRuntime != mcfgv1.ContainerRuntimeDefaultRuntimeEmpty {
			crioFileConfigs := createCRIODropinFiles(cfg)
			configFileList = append(configFileList, crioFileConfigs...)
		}

		tlsMinVersion := ctrl.getTLSMinVersionForPool(pool)
		if tlsMinVersion != "" {
			klog.Infof("Propagating TLS min version %q from KubeletConfig to CRI-O drop-in for pool %s", tlsMinVersion, role)
			tlsDropinFiles := createCRIOTLSDropinFile(tlsMinVersion)
			configFileList = append(configFileList, tlsDropinFiles...)
		}

		if isNotFound {
			tempIgnCfg := ctrlcommon.NewIgnConfig()
			mc, err = ctrlcommon.MachineConfigFromIgnConfig(role, managedKey, tempIgnCfg)
			if err != nil {
				return ctrl.syncStatusOnly(cfg, err, "could not create MachineConfig from new Ignition config: %v", err)
			}
		}
		_, ok := cfg.GetAnnotations()[ctrlcommon.MCNameSuffixAnnotationKey]
		arr := strings.Split(managedKey, "-")
		// the first managed key value 99-poolname-generated-containerruntime does not have a suffix
		// set "" as suffix annotation to the containerruntime config object
		if _, err := strconv.Atoi(arr[len(arr)-1]); err != nil && !ok {
			if err := ctrl.addAnnotation(cfg, ctrlcommon.MCNameSuffixAnnotationKey, ""); err != nil {
				return ctrl.syncStatusOnly(cfg, err, "could not update annotation for containerruntimeConfig")
			}
		}
		// If the MC name suffix annotation does not exist and the managed key value returned has a suffix, then add the MC name
		// suffix annotation and suffix value to the ctrcfg object
		if len(arr) > 4 && !ok {
			_, err := strconv.Atoi(arr[len(arr)-1])
			if err == nil {
				if err := ctrl.addAnnotation(cfg, ctrlcommon.MCNameSuffixAnnotationKey, arr[len(arr)-1]); err != nil {
					return ctrl.syncStatusOnly(cfg, err, "could not update annotation for containerRuntimeConfig")
				}
			}
		}

		ctrRuntimeConfigIgn := createNewIgnition(configFileList)
		rawCtrRuntimeConfigIgn, err := json.Marshal(ctrRuntimeConfigIgn)
		if err != nil {
			return ctrl.syncStatusOnly(cfg, err, "error marshalling container runtime config Ignition: %v", err)
		}
		mc.Spec.Config.Raw = rawCtrRuntimeConfigIgn

		mc.SetAnnotations(map[string]string{
			ctrlcommon.GeneratedByControllerVersionAnnotationKey: version.Hash,
			ctrlcommon.CRIOTLSMinVersionAnnotationKey:            tlsMinVersion,
		})
		oref := metav1.NewControllerRef(cfg, controllerKind)
		mc.SetOwnerReferences([]metav1.OwnerReference{*oref})

		// Create or Update, on conflict retry
		if err := retry.RetryOnConflict(updateBackoff, func() error {
			var err error
			if isNotFound {
				_, err = ctrl.client.MachineconfigurationV1().MachineConfigs().Create(context.TODO(), mc, metav1.CreateOptions{})
			} else {
				_, err = ctrl.client.MachineconfigurationV1().MachineConfigs().Update(context.TODO(), mc, metav1.UpdateOptions{})
			}
			return err
		}); err != nil {
			return ctrl.syncStatusOnly(cfg, err, "could not Create/Update MachineConfig: %v", err)
		}
		// Add Finalizers to the ContainerRuntimeConfigs
		if err := ctrl.addFinalizerToContainerRuntimeConfig(cfg, mc); err != nil {
			return ctrl.syncStatusOnly(cfg, err, "could not add finalizers to ContainerRuntimeConfig: %v", err)
		}
		klog.Infof("Applied ContainerRuntimeConfig %v on MachineConfigPool %v", key, pool.Name)
		ctrlcommon.UpdateStateMetric(ctrlcommon.MCCSubControllerState, "machine-config-controller-container-runtime-config", "Sync Container Runtime Config", pool.Name)
	}
	if err := ctrl.cleanUpDuplicatedMC(); err != nil {
		return err
	}
	return ctrl.syncStatusOnly(cfg, nil)
}

// cleanUpDuplicatedMC removes the MC of non-updated GeneratedByControllerVersionKey if its name contains 'generated-containerruntimeconfig'.
// BZ 1955517: upgrade when there are more than one configs, the duplicated and upgraded MC will be generated (func getManagedKubeletConfigKey())
// MC with old GeneratedByControllerVersionKey fails the upgrade.
func (ctrl *Controller) cleanUpDuplicatedMC() error {
	generatedCtrCfg := "generated-containerruntime"
	// Get all machine configs
	mcList, err := ctrl.client.MachineconfigurationV1().MachineConfigs().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("error listing containerruntime machine configs: %w", err)
	}
	for _, mc := range mcList.Items {
		if !strings.Contains(mc.Name, generatedCtrCfg) {
			continue
		}
		// Recheck to ensure nothing changed in between
		mcToBeDeleted, err := ctrl.client.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), mc.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		// delete the containerruntime mc if its degraded
		if mcToBeDeleted.Annotations[ctrlcommon.GeneratedByControllerVersionAnnotationKey] != version.Hash {
			if err := ctrl.client.MachineconfigurationV1().MachineConfigs().Delete(context.TODO(), mc.Name, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
				return fmt.Errorf("error deleting degraded containerruntime machine config %s: %w", mc.Name, err)
			}

		}
	}
	return nil
}

// mergeConfigChanges retrieves the original/default config data from the templates, decodes it and merges in the changes given by the Custom Resource.
// It then encodes the new data and returns it.
func mergeConfigChanges(origFile *ign3types.File, cfg *mcfgv1.ContainerRuntimeConfig, update updateConfigFunc) ([]byte, error) {
	if origFile.Contents.Source == nil {
		return nil, fmt.Errorf("original Container Runtime config is empty")
	}
	contents, err := ctrlcommon.DecodeIgnitionFileContents(origFile.Contents.Source, origFile.Contents.Compression)
	if err != nil {
		return nil, fmt.Errorf("could not decode original Container Runtime config: %w", err)
	}
	cfgTOML, err := update(contents, cfg.Spec.ContainerRuntimeConfig)
	if err != nil {
		return nil, fmt.Errorf("could not update container runtime config with new changes: %w", err)
	}
	return cfgTOML, nil
}

// nolint: gocyclo
func (ctrl *Controller) syncImageConfig(key string) error {
	startTime := time.Now()
	klog.V(4).Infof("Started syncing ImageConfig %q (%v)", key, startTime)
	defer func() {
		klog.V(4).Infof("Finished syncing ImageConfig %q (%v)", key, time.Since(startTime))
	}()

	// Fetch the ImageConfig
	imgcfg, err := ctrl.imgLister.Get("cluster")
	if errors.IsNotFound(err) {
		klog.V(2).Infof("ImageConfig 'cluster' does not exist or has been deleted")
		return nil
	}
	if err != nil {
		return err
	}
	// Deep-copy otherwise we are mutating our cache.
	imgcfg = imgcfg.DeepCopy()

	// Fetch the ClusterVersionConfig needed to get the registry being used by the payload
	// so that we can avoid adding that registry to blocked registries in /etc/containers/registries.conf
	clusterVersionCfg, err := ctrl.clusterVersionLister.Get("version")
	if errors.IsNotFound(err) {
		klog.Infof("ClusterVersionConfig 'version' does not exist or has been deleted")
		return nil
	}
	if err != nil {
		return err
	}

	// Find all ImageContentSourcePolicy objects
	icspRules, err := ctrl.icspLister.List(labels.Everything())
	if err != nil && errors.IsNotFound(err) {
		icspRules = []*apioperatorsv1alpha1.ImageContentSourcePolicy{}
	} else if err != nil {
		return err
	}
	// Find all ImageDigestMirrorSet objects
	idmsRules, err := ctrl.idmsLister.List(labels.Everything())
	if err != nil && errors.IsNotFound(err) {
		idmsRules = []*apicfgv1.ImageDigestMirrorSet{}
	} else if err != nil {
		return err
	}

	// Find all ImageTagMirrorSet objects
	itmsRules, err := ctrl.itmsLister.List(labels.Everything())
	if err != nil && errors.IsNotFound(err) {
		itmsRules = []*apicfgv1.ImageTagMirrorSet{}
	} else if err != nil {
		return err
	}

	var (
		registriesBlocked, policyBlocked, allowedRegs []string
		releaseImage                                  string
		clusterImagePolicies                          []*apicfgv1.ClusterImagePolicy
		clusterScopePolicies                          map[string]signature.PolicyRequirements
		imagePolicies                                 []*apicfgv1.ImagePolicy
		scopeNamespacePolicies                        map[string]map[string]signature.PolicyRequirements
	)

	if ctrl.sigstoreAPIEnabled() && ctrl.addedPolicyObservers {
		// Find all ClusterImagePolicy objects
		clusterImagePolicies, err = ctrl.clusterImagePolicyLister.List(labels.Everything())
		if err != nil && errors.IsNotFound(err) {
			clusterImagePolicies = []*apicfgv1.ClusterImagePolicy{}
		} else if err != nil {
			return nil
		}
		// Find all ImagePolicy objects
		imagePolicies, err = ctrl.imagePolicyLister.List(labels.Everything())
		if err != nil && errors.IsNotFound(err) {
			imagePolicies = []*apicfgv1.ImagePolicy{}
		} else if err != nil {
			return nil
		}
	}

	if clusterVersionCfg != nil {
		// The possibility of releaseImage being "" is very unlikely, will only happen if clusterVersionCfg is nil. If this happens
		// then there is something very wrong with the cluster and in that situation it would be best to fail here till clusterVersionCfg
		// has been recovered
		releaseImage = clusterVersionCfg.Status.Desired.Image
		// Go through the registries in the image spec to get and validate the blocked registries
		registriesBlocked, policyBlocked, allowedRegs, err = getValidBlockedAndAllowedRegistries(releaseImage, &imgcfg.Spec, icspRules, idmsRules)
		if err != nil && err != errParsingReference {
			klog.V(2).Infof("%v, skipping....", err)
		} else if err == errParsingReference {
			return err
		}
	}

	if clusterScopePolicies, scopeNamespacePolicies, err = getValidScopePolicies(clusterImagePolicies, imagePolicies, ctrl); err != nil {
		return err
	}

	// Get ControllerConfig
	controllerConfig, err := ctrl.ccLister.Get(ctrlcommon.ControllerConfigName)
	if err != nil {
		return fmt.Errorf("could not get ControllerConfig %w", err)
	}

	sel, err := metav1.LabelSelectorAsSelector(metav1.AddLabelToSelector(&metav1.LabelSelector{}, builtInLabelKey, ""))
	if err != nil {
		return err
	}
	// Find all the MCO built in MachineConfigPools
	mcpPools, err := ctrl.mcpLister.List(sel)
	if err != nil {
		return err
	}
	for _, pool := range mcpPools {
		// To keep track of whether we "actually" got an updated image config
		applied := true
		role := pool.Name
		// Get MachineConfig
		managedKey, err := getManagedKeyReg(pool, ctrl.client)
		if err != nil {
			return err
		}
		if err := retry.RetryOnConflict(updateBackoff, func() error {
			registriesIgn, err := registriesConfigIgnition(ctrl.templatesDir, controllerConfig, role, releaseImage,
				imgcfg.Spec.RegistrySources.InsecureRegistries, registriesBlocked, policyBlocked, allowedRegs,
				imgcfg.Spec.RegistrySources.ContainerRuntimeSearchRegistries, icspRules, idmsRules, itmsRules, clusterScopePolicies, scopeNamespacePolicies)
			if err != nil {
				return err
			}

			applied, err = ctrl.syncIgnitionConfig(managedKey, registriesIgn, pool, ownerReferenceImageConfig(imgcfg))
			if err != nil {
				return fmt.Errorf("could not sync registries Ignition config: %w", err)
			}
			return err
		}); err != nil {
			return fmt.Errorf("could not Create/Update MachineConfig: %w", err)
		}
		if applied {
			klog.Infof("Applied ImageConfig cluster on MachineConfigPool %v", pool.Name)
			ctrlcommon.UpdateStateMetric(ctrlcommon.MCCSubControllerState, "machine-config-controller-container-runtime-config", "Sync Image Config", pool.Name)
		}
	}
	return nil
}

func (ctrl *Controller) syncIgnitionConfig(managedKey string, ignFile *ign3types.Config, pool *mcfgv1.MachineConfigPool, ownerRef metav1.OwnerReference) (bool, error) {
	rawIgn, err := json.Marshal(ignFile)
	if err != nil {
		return false, fmt.Errorf("could not encode Ignition config: %w", err)
	}
	mc, err := ctrl.client.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), managedKey, metav1.GetOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return false, fmt.Errorf("could not find MachineConfig: %w", err)
	}
	isNotFound := errors.IsNotFound(err)
	if !isNotFound && equality.Semantic.DeepEqual(rawIgn, mc.Spec.Config.Raw) {
		// if the configuration for the registries is equal, we still need to compare
		// the generated controller version because during an upgrade we need a new one
		mcCtrlVersion := mc.Annotations[ctrlcommon.GeneratedByControllerVersionAnnotationKey]
		if mcCtrlVersion == version.Hash {
			return false, nil
		}
	}
	if isNotFound {
		tempIgnCfg := ctrlcommon.NewIgnConfig()
		mc, err = ctrlcommon.MachineConfigFromIgnConfig(pool.Name, managedKey, tempIgnCfg)
		if err != nil {
			return false, fmt.Errorf("could not create MachineConfig from new Ignition config: %w", err)
		}
	}
	mc.Spec.Config.Raw = rawIgn
	mc.ObjectMeta.Annotations = map[string]string{
		ctrlcommon.GeneratedByControllerVersionAnnotationKey: version.Hash,
	}
	mc.ObjectMeta.OwnerReferences = []metav1.OwnerReference{ownerRef}
	// Create or Update, on conflict retry
	if isNotFound {
		_, err = ctrl.client.MachineconfigurationV1().MachineConfigs().Create(context.TODO(), mc, metav1.CreateOptions{})
	} else {
		_, err = ctrl.client.MachineconfigurationV1().MachineConfigs().Update(context.TODO(), mc, metav1.UpdateOptions{})
	}

	return true, err
}

func registriesConfigIgnition(templateDir string, controllerConfig *mcfgv1.ControllerConfig, role, releaseImage string,
	insecureRegs, registriesBlocked, policyBlocked, allowedRegs, searchRegs []string,
	icspRules []*apioperatorsv1alpha1.ImageContentSourcePolicy, idmsRules []*apicfgv1.ImageDigestMirrorSet, itmsRules []*apicfgv1.ImageTagMirrorSet,
	clusterScopePolicies map[string]signature.PolicyRequirements, scopeNamespacePolicies map[string]map[string]signature.PolicyRequirements) (*ign3types.Config, error) {

	var (
		registriesTOML               []byte
		policyJSON                   []byte
		sigstoreRegistriesConfigYaml []byte
		namespacedPolicyJSONs        map[string][]byte
		err                          error
	)

	// Generate the original registries config
	_, originalRegistriesIgn, originalPolicyIgn, err := generateOriginalContainerRuntimeConfigs(templateDir, controllerConfig, role)
	if err != nil {
		return nil, fmt.Errorf("could not generate original ContainerRuntime Configs: %w", err)
	}

	if insecureRegs != nil || registriesBlocked != nil || len(icspRules) != 0 || len(idmsRules) != 0 || len(itmsRules) != 0 {
		if originalRegistriesIgn.Contents.Source == nil {
			return nil, fmt.Errorf("original registries config is empty")
		}
		contents, err := ctrlcommon.DecodeIgnitionFileContents(originalRegistriesIgn.Contents.Source, originalRegistriesIgn.Contents.Compression)
		if err != nil {
			return nil, fmt.Errorf("could not decode original registries config: %w", err)
		}
		registriesTOML, err = updateRegistriesConfig(contents, insecureRegs, registriesBlocked, icspRules, idmsRules, itmsRules)
		if err != nil {
			return nil, fmt.Errorf("could not update registries config with new changes: %w", err)
		}
	}
	if policyBlocked != nil || allowedRegs != nil || len(clusterScopePolicies) > 0 || len(scopeNamespacePolicies) > 0 {
		if originalPolicyIgn.Contents.Source == nil {
			return nil, fmt.Errorf("original policy json is empty")
		}
		contents, err := ctrlcommon.DecodeIgnitionFileContents(originalPolicyIgn.Contents.Source, originalPolicyIgn.Contents.Compression)
		if err != nil {
			return nil, fmt.Errorf("could not decode original policy json: %w", err)
		}
		policyJSON, err = updatePolicyJSON(contents, policyBlocked, allowedRegs, releaseImage, clusterScopePolicies)
		if err != nil {
			return nil, fmt.Errorf("could not update policy json with new changes: %w", err)
		}
		// namespacePolicyJSONs inherite the cluster override policyJSON
		namespacedPolicyJSONs, err = updateNamespacedPolicyJSONs(policyJSON, policyBlocked, allowedRegs, scopeNamespacePolicies)
		if err != nil {
			return nil, fmt.Errorf("could not update namespace policy JSON from imagepolicy: %w", err)
		}
		// generates configuration under /etc/containers/registries.d to enable sigstore verification
		sigstoreRegistriesConfigYaml, err = generateSigstoreRegistriesdConfig(clusterScopePolicies, scopeNamespacePolicies, registriesTOML)
		if err != nil {
			return nil, err
		}
	}

	generatedConfigFileList := []generatedConfigFile{
		{filePath: registriesConfigPath, data: registriesTOML},
		{filePath: policyConfigPath, data: policyJSON},
		{filePath: sigstoreRegistriesConfigFilePath, data: sigstoreRegistriesConfigYaml},
	}
	generatedImagePolicyConfigFileList := imagePolicyConfigFileList(namespacedPolicyJSONs)
	generatedConfigFileList = append(generatedConfigFileList, generatedImagePolicyConfigFileList...)
	if searchRegs != nil {
		generatedConfigFileList = append(generatedConfigFileList, updateSearchRegistriesConfig(searchRegs)...)
	}

	registriesIgn := createNewIgnition(generatedConfigFileList)
	return &registriesIgn, nil
}

// getValidScopePolicies returns a map[scope]policyRequirement from ClusterImagePolicy, a map[scope][namespace]policyRequirement from ImagePolicy CRs.
// It skips ImagePolicy scopes that conflict with ClusterImagePolicy scopes and logs the conflicting scopes in the ImagePolicy Status.
func getValidScopePolicies(clusterImagePolicies []*apicfgv1.ClusterImagePolicy, imagePolicies []*apicfgv1.ImagePolicy, ctrl *Controller) (map[string]signature.PolicyRequirements, map[string]map[string]signature.PolicyRequirements, error) {
	clusterScopePolicies := make(map[string]signature.PolicyRequirements)
	namespacePolicies := make(map[string]map[string]signature.PolicyRequirements)

	for _, clusterImagePolicy := range clusterImagePolicies {
		sigstoreSignedPolicyItem, err := policyItemFromSpec(clusterImagePolicy.Spec.Policy)
		if err != nil {
			return nil, nil, err
		}
		for _, scope := range clusterImagePolicy.Spec.Scopes {
			scopeStr := string(scope)
			clusterScopePolicies[scopeStr] = append(clusterScopePolicies[scopeStr], sigstoreSignedPolicyItem)
		}
	}

	for _, imagePolicy := range imagePolicies {
		namespace := imagePolicy.ObjectMeta.Namespace
		sigstoreSignedPolicyItem, err := policyItemFromSpec(imagePolicy.Spec.Policy)
		if err != nil {
			return nil, nil, err
		}
		// skip adding scope to namespacePolicies if it conflicting with clusterimagepolicy scopes
		// but collect conflictScopes for status update
		var conflictScopes []string
	outerScope:
		for _, scope := range imagePolicy.Spec.Scopes {
			scopeStr := string(scope)
			for clusterScope := range clusterScopePolicies {
				if runtimeutils.ScopeIsNestedInsideScope(scopeStr, clusterScope) {
					conflictScopes = append(conflictScopes, scopeStr)
					continue outerScope
				}
			}
			if _, ok := namespacePolicies[scopeStr]; !ok {
				namespacePolicies[scopeStr] = make(map[string]signature.PolicyRequirements)
			}
			namespacePolicies[scopeStr][namespace] = append(namespacePolicies[scopeStr][namespace], sigstoreSignedPolicyItem)
		}
		if ctrl != nil {
			if len(conflictScopes) > 0 {
				msg := fmt.Sprintf("has conflicting scope(s) %q that equal to or nest inside existing clusterimagepolicy, only policy from clusterimagepolicy scope(s) will be applied", conflictScopes)
				klog.V(2).Info(msg)
				ctrl.syncImagePolicyStatusOnly(namespace, imagePolicy.ObjectMeta.Name, apicfgv1.ImagePolicyPending, reasonConflictScopes, msg, metav1.ConditionFalse)
			}
		}
	}

	return clusterScopePolicies, namespacePolicies, nil
}

func (ctrl *Controller) syncImagePolicyStatusOnly(namespace, imagepolicy, conditionType, reason, msg string, status metav1.ConditionStatus) {
	statusUpdateErr := retry.RetryOnConflict(updateBackoff, func() error {
		newImagePolicy, err := ctrl.configClient.ConfigV1().ImagePolicies(namespace).Get(context.TODO(), imagepolicy, metav1.GetOptions{})
		if err != nil {
			return err
		}

		newCondition := apihelpers.NewCondition(conditionType, status, reason, msg)
		if newImagePolicy.GetGeneration() != newCondition.ObservedGeneration {
			newCondition.ObservedGeneration = newImagePolicy.GetGeneration()
		}
		newImagePolicy.Status.Conditions = []metav1.Condition{*newCondition}
		_, updateErr := ctrl.configClient.ConfigV1().ImagePolicies(namespace).UpdateStatus(context.TODO(), newImagePolicy, metav1.UpdateOptions{})
		return updateErr
	})
	if statusUpdateErr != nil {
		klog.Warningf("error updating imagepolicy status: %v", statusUpdateErr)
	}
}

// RunImageBootstrap generates MachineConfig objects for mcpPools that would have been generated by syncImageConfig,
// except that mcfgv1.Image is not available.
func RunImageBootstrap(templateDir string, controllerConfig *mcfgv1.ControllerConfig, mcpPools []*mcfgv1.MachineConfigPool, icspRules []*apioperatorsv1alpha1.ImageContentSourcePolicy,
	idmsRules []*apicfgv1.ImageDigestMirrorSet, itmsRules []*apicfgv1.ImageTagMirrorSet, imgCfg *apicfgv1.Image, clusterImagePolicies []*apicfgv1.ClusterImagePolicy, imagePolicies []*apicfgv1.ImagePolicy,
	fgHandler ctrlcommon.FeatureGatesHandler) ([]*mcfgv1.MachineConfig, error) {

	var (
		insecureRegs, registriesBlocked, policyBlocked, allowedRegs, searchRegs []string
		err                                                                     error
	)

	clusterScopePolicies := map[string]signature.PolicyRequirements{}
	scopeNamespacePolicies := map[string]map[string]signature.PolicyRequirements{}
	if fgHandler.Enabled(features.FeatureGateSigstoreImageVerification) {
		if clusterScopePolicies, scopeNamespacePolicies, err = getValidScopePolicies(clusterImagePolicies, imagePolicies, nil); err != nil {
			return nil, err
		}
	}

	// Read the search, insecure, blocked, and allowed registries from the cluster-wide Image CR if it is not nil
	if imgCfg != nil {
		insecureRegs = imgCfg.Spec.RegistrySources.InsecureRegistries
		searchRegs = imgCfg.Spec.RegistrySources.ContainerRuntimeSearchRegistries
		registriesBlocked, policyBlocked, allowedRegs, err = getValidBlockedAndAllowedRegistries(controllerConfig.Spec.ReleaseImage, &imgCfg.Spec, icspRules, idmsRules)
		if err != nil && err != errParsingReference {
			klog.V(2).Infof("%v, skipping....", err)
		} else if err == errParsingReference {
			return nil, err
		}
		allowedRegs = append(allowedRegs, imgCfg.Spec.RegistrySources.AllowedRegistries...)
	}

	var res []*mcfgv1.MachineConfig
	for _, pool := range mcpPools {
		role := pool.Name
		managedKey, err := getManagedKeyReg(pool, nil)
		if err != nil {
			return nil, err
		}
		registriesIgn, err := registriesConfigIgnition(templateDir, controllerConfig, role, controllerConfig.Spec.ReleaseImage,
			insecureRegs, registriesBlocked, policyBlocked, allowedRegs, searchRegs, icspRules, idmsRules, itmsRules, clusterScopePolicies, scopeNamespacePolicies)
		if err != nil {
			return nil, err
		}
		mc, err := ctrlcommon.MachineConfigFromIgnConfig(role, managedKey, registriesIgn)
		if err != nil {
			return nil, err
		}
		// Explicitly do NOT set GeneratedByControllerVersionAnnotationKey so that the first run of the non-bootstrap controller
		// always rebuilds registries.conf (with the insecureRegs/blockedRegs values actually available).
		mc.ObjectMeta.OwnerReferences = []metav1.OwnerReference{
			{
				APIVersion: apicfgv1.SchemeGroupVersion.String(),
				Kind:       "Image",
				// Name and UID is not set, the first run of syncImageConfig will overwrite these values.
			},
		}
		res = append(res, mc)
	}
	return res, nil
}

func (ctrl *Controller) popFinalizerFromContainerRuntimeConfig(ctrCfg *mcfgv1.ContainerRuntimeConfig) error {
	return retry.RetryOnConflict(updateBackoff, func() error {
		newcfg, err := ctrl.mccrLister.Get(ctrCfg.Name)
		if errors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}

		curJSON, err := json.Marshal(newcfg)
		if err != nil {
			return err
		}

		ctrCfgTmp := newcfg.DeepCopy()
		ctrCfgTmp.Finalizers = append([]string{}, ctrCfg.Finalizers[1:]...)

		modJSON, err := json.Marshal(ctrCfgTmp)
		if err != nil {
			return err
		}

		patch, err := jsonmergepatch.CreateThreeWayJSONMergePatch(curJSON, modJSON, curJSON)
		if err != nil {
			return err
		}
		return ctrl.patchContainerRuntimeConfigs(ctrCfg.Name, patch)
	})
}

func (ctrl *Controller) patchContainerRuntimeConfigs(name string, patch []byte) error {
	_, err := ctrl.client.MachineconfigurationV1().ContainerRuntimeConfigs().Patch(context.TODO(), name, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

func (ctrl *Controller) addFinalizerToContainerRuntimeConfig(ctrCfg *mcfgv1.ContainerRuntimeConfig, mc *mcfgv1.MachineConfig) error {
	return retry.RetryOnConflict(updateBackoff, func() error {
		newcfg, err := ctrl.mccrLister.Get(ctrCfg.Name)
		if errors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}

		curJSON, err := json.Marshal(newcfg)
		if err != nil {
			return err
		}

		ctrCfgTmp := newcfg.DeepCopy()
		// Only append the mc name if it is already not in the list of finalizers.
		// When we update an existing ctrcfg, the generation number increases causing
		// a resync to happen. When this happens, the mc name is the same, so we don't
		// want to add duplicate entries to the list of finalizers.
		if !ctrlcommon.InSlice(mc.Name, ctrCfgTmp.Finalizers) {
			ctrCfgTmp.Finalizers = append(ctrCfgTmp.Finalizers, mc.Name)
		}

		modJSON, err := json.Marshal(ctrCfgTmp)
		if err != nil {
			return err
		}

		patch, err := jsonmergepatch.CreateThreeWayJSONMergePatch(curJSON, modJSON, curJSON)
		if err != nil {
			return err
		}
		return ctrl.patchContainerRuntimeConfigs(ctrCfg.Name, patch)
	})
}

// getTLSMinVersionFromKubeletConfigs finds the TLS minimum version from
// KubeletConfig CRs that match the given MachineConfigPool.
func (ctrl *Controller) getTLSMinVersionFromKubeletConfigs(pool *mcfgv1.MachineConfigPool) string {
	kubeletConfigs, err := ctrl.mckLister.List(labels.Everything())
	if err != nil {
		klog.Warningf("error listing KubeletConfigs for TLS propagation: %v", err)
		return ""
	}

	var tlsMinVersion string
	for _, kc := range kubeletConfigs {
		if kc.Spec.TLSSecurityProfile == nil {
			continue
		}
		// Check if this KubeletConfig targets the same pool
		selector, err := metav1.LabelSelectorAsSelector(kc.Spec.MachineConfigPoolSelector)
		if err != nil {
			klog.Warningf("invalid label selector in KubeletConfig %s: %v", kc.Name, err)
			continue
		}
		if selector.Empty() || !selector.Matches(labels.Set(pool.Labels)) {
			continue
		}
		// Extract TLS min version from the security profile
		minVersion, _ := ctrlcommon.GetSecurityProfileCiphers(kc.Spec.TLSSecurityProfile)
		if minVersion != "" {
			tlsMinVersion = minVersion
		}
	}

	return tlsMinVersion
}

// getTLSMinVersionForPool returns the TLS minimum version for the given pool.
// It first checks KubeletConfig CRs targeting the pool if none specify TLS,
// it falls back to the cluster APIServer config .
func (ctrl *Controller) getTLSMinVersionForPool(pool *mcfgv1.MachineConfigPool) string {
	if v := ctrl.getTLSMinVersionFromKubeletConfigs(pool); v != "" {
		return v
	}
	apiServer, err := ctrl.apiserverLister.Get(ctrlcommon.APIServerInstanceName)
	if err != nil && !errors.IsNotFound(err) {
		klog.Warningf("error getting APIServer for TLS fallback: %v", err)
		return ""
	}
	var apiServerObj *apicfgv1.APIServer
	if err == nil {
		apiServerObj = apiServer
	}
	tlsMinVersion, _ := ctrlcommon.GetSecurityProfileCiphersFromAPIServer(apiServerObj)
	return tlsMinVersion
}

func (ctrl *Controller) getPoolsForContainerRuntimeConfig(config *mcfgv1.ContainerRuntimeConfig) ([]*mcfgv1.MachineConfigPool, error) {
	pList, err := ctrl.mcpLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}

	selector, err := metav1.LabelSelectorAsSelector(config.Spec.MachineConfigPoolSelector)
	if err != nil {
		return nil, fmt.Errorf("invalid label selector: %w", err)
	}

	var pools []*mcfgv1.MachineConfigPool
	for _, p := range pList {
		// If a pool with a nil or empty selector creeps in, it should match nothing, not everything.
		if selector.Empty() || !selector.Matches(labels.Set(p.Labels)) {
			continue
		}
		pools = append(pools, p)
	}

	if len(pools) == 0 {
		return nil, fmt.Errorf("could not find any MachineConfigPool set for ContainerRuntimeConfig %s", config.Name)
	}

	return pools, nil
}
