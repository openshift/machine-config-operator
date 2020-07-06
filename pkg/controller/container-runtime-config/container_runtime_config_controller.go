package containerruntimeconfig

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/clarketm/json"
	igntypes "github.com/coreos/ignition/config/v2_2/types"
	"github.com/golang/glog"
	apicfgv1 "github.com/openshift/api/config/v1"
	apioperatorsv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	configclientset "github.com/openshift/client-go/config/clientset/versioned"
	cligoinformersv1 "github.com/openshift/client-go/config/informers/externalversions/config/v1"
	cligolistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	operatorinformersv1alpha1 "github.com/openshift/client-go/operator/informers/externalversions/operator/v1alpha1"
	operatorlistersv1alpha1 "github.com/openshift/client-go/operator/listers/operator/v1alpha1"
	"github.com/vincent-petithory/dataurl"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	coreclientsetv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	mtmpl "github.com/openshift/machine-config-operator/pkg/controller/template"
	mcfgclientset "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/scheme"
	mcfginformersv1 "github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions/machineconfiguration.openshift.io/v1"
	mcfglistersv1 "github.com/openshift/machine-config-operator/pkg/generated/listers/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/version"
)

const (
	// maxRetries is the number of times a containerruntimeconfig pool will be retried before it is dropped out of the queue.
	// With the current rate-limiter in use (5ms*2^(maxRetries-1)) the following numbers represent the times
	// a machineconfig pool is going to be requeued:
	//
	// 5ms, 10ms, 20ms, 40ms, 80ms, 160ms, 320ms, 640ms, 1.3s, 2.6s, 5.1s, 10.2s, 20.4s, 41s, 82s
	maxRetries = 15

	builtInLabelKey = "machineconfiguration.openshift.io/mco-built-in"
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

	mcpLister       mcfglistersv1.MachineConfigPoolLister
	mcpListerSynced cache.InformerSynced

	clusterVersionLister       cligolistersv1.ClusterVersionLister
	clusterVersionListerSynced cache.InformerSynced

	queue    workqueue.RateLimitingInterface
	imgQueue workqueue.RateLimitingInterface

	// we need this method to mock out patch calls in unit until https://github.com/openshift/machine-config-operator/pull/611#issuecomment-481397185
	// which is probably going to be in kube 1.14
	patchContainerRuntimeConfigsFunc func(string, []byte) error
}

// New returns a new container runtime config controller
func New(
	templatesDir string,
	mcpInformer mcfginformersv1.MachineConfigPoolInformer,
	ccInformer mcfginformersv1.ControllerConfigInformer,
	mcrInformer mcfginformersv1.ContainerRuntimeConfigInformer,
	imgInformer cligoinformersv1.ImageInformer,
	icspInformer operatorinformersv1alpha1.ImageContentSourcePolicyInformer,
	clusterVersionInformer cligoinformersv1.ClusterVersionInformer,
	kubeClient clientset.Interface,
	mcfgClient mcfgclientset.Interface,
	configClient configclientset.Interface,
) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.Infof)
	eventBroadcaster.StartRecordingToSink(&coreclientsetv1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	ctrl := &Controller{
		templatesDir:  templatesDir,
		client:        mcfgClient,
		configClient:  configClient,
		eventRecorder: eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineconfigcontroller-containerruntimeconfigcontroller"}),
		queue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "machineconfigcontroller-containerruntimeconfigcontroller"),
		imgQueue:      workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter()),
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

	ctrl.syncHandler = ctrl.syncContainerRuntimeConfig
	ctrl.syncImgHandler = ctrl.syncImageConfig
	ctrl.enqueueContainerRuntimeConfig = ctrl.enqueue

	ctrl.mcpLister = mcpInformer.Lister()
	ctrl.mcpListerSynced = mcpInformer.Informer().HasSynced

	ctrl.ccLister = ccInformer.Lister()
	ctrl.ccListerSynced = ccInformer.Informer().HasSynced

	ctrl.mccrLister = mcrInformer.Lister()
	ctrl.mccrListerSynced = mcrInformer.Informer().HasSynced

	ctrl.imgLister = imgInformer.Lister()
	ctrl.imgListerSynced = imgInformer.Informer().HasSynced

	ctrl.icspLister = icspInformer.Lister()
	ctrl.icspListerSynced = icspInformer.Informer().HasSynced

	ctrl.clusterVersionLister = clusterVersionInformer.Lister()
	ctrl.clusterVersionListerSynced = clusterVersionInformer.Informer().HasSynced

	ctrl.patchContainerRuntimeConfigsFunc = ctrl.patchContainerRuntimeConfigs

	return ctrl
}

// Run executes the container runtime config controller.
func (ctrl *Controller) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()
	defer ctrl.imgQueue.ShutDown()

	if !cache.WaitForCacheSync(stopCh, ctrl.mcpListerSynced, ctrl.mccrListerSynced, ctrl.ccListerSynced,
		ctrl.imgListerSynced, ctrl.icspListerSynced, ctrl.clusterVersionListerSynced) {
		return
	}

	glog.Info("Starting MachineConfigController-ContainerRuntimeConfigController")
	defer glog.Info("Shutting down MachineConfigController-ContainerRuntimeConfigController")

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.worker, time.Second, stopCh)
	}

	// Just need one worker for the image config
	go wait.Until(ctrl.imgWorker, time.Second, stopCh)

	<-stopCh
}

func ctrConfigTriggerObjectChange(old, new *mcfgv1.ContainerRuntimeConfig) bool {
	if old.DeletionTimestamp != new.DeletionTimestamp {
		return true
	}
	if !reflect.DeepEqual(old.Spec, new.Spec) {
		return true
	}
	return false
}

func (ctrl *Controller) imageConfAdded(obj interface{}) {
	ctrl.imgQueue.Add("openshift-config")
}

func (ctrl *Controller) imageConfUpdated(oldObj, newObj interface{}) {
	ctrl.imgQueue.Add("openshift-config")
}

func (ctrl *Controller) imageConfDeleted(obj interface{}) {
	ctrl.imgQueue.Add("openshift-config")
}

func (ctrl *Controller) icspConfAdded(obj interface{}) {
	ctrl.imgQueue.Add("openshift-config")
}

func (ctrl *Controller) icspConfUpdated(oldObj, newObj interface{}) {
	ctrl.imgQueue.Add("openshift-config")
}

func (ctrl *Controller) icspConfDeleted(obj interface{}) {
	ctrl.imgQueue.Add("openshift-config")
}

func (ctrl *Controller) updateContainerRuntimeConfig(oldObj, newObj interface{}) {
	oldCtrCfg := oldObj.(*mcfgv1.ContainerRuntimeConfig)
	newCtrCfg := newObj.(*mcfgv1.ContainerRuntimeConfig)

	if ctrConfigTriggerObjectChange(oldCtrCfg, newCtrCfg) {
		glog.V(4).Infof("Update ContainerRuntimeConfig %s", oldCtrCfg.Name)
		ctrl.enqueueContainerRuntimeConfig(newCtrCfg)
	}
}

func (ctrl *Controller) addContainerRuntimeConfig(obj interface{}) {
	cfg := obj.(*mcfgv1.ContainerRuntimeConfig)
	glog.V(4).Infof("Adding ContainerRuntimeConfig %s", cfg.Name)
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
		utilruntime.HandleError(fmt.Errorf("couldn't delete object %#v: %v", cfg, err))
	} else {
		glog.V(4).Infof("Deleted ContainerRuntimeConfig %s and restored default config", cfg.Name)
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
	if err := ctrl.popFinalizerFromContainerRuntimeConfig(cfg); err != nil {
		return err
	}
	return nil
}

func (ctrl *Controller) enqueue(cfg *mcfgv1.ContainerRuntimeConfig) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(cfg)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %v", cfg, err))
		return
	}
	ctrl.queue.Add(key)
}

func (ctrl *Controller) enqueueRateLimited(cfg *mcfgv1.ContainerRuntimeConfig) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(cfg)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %v", cfg, err))
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

	err := ctrl.syncHandler(key.(string))
	ctrl.handleErr(err, key)

	return true
}

func (ctrl *Controller) processNextImgWorkItem() bool {
	key, quit := ctrl.imgQueue.Get()
	if quit {
		return false
	}
	defer ctrl.imgQueue.Done(key)

	err := ctrl.syncImgHandler(key.(string))
	ctrl.handleImgErr(err, key)

	return true
}

func (ctrl *Controller) handleErr(err error, key interface{}) {
	if err == nil {
		ctrl.queue.Forget(key)
		return
	}

	if ctrl.queue.NumRequeues(key) < maxRetries {
		glog.V(2).Infof("Error syncing containerruntimeconfig %v: %v", key, err)
		ctrl.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	glog.V(2).Infof("Dropping containerruntimeconfig %q out of the queue: %v", key, err)
	ctrl.queue.Forget(key)
	ctrl.queue.AddAfter(key, 1*time.Minute)
}

func (ctrl *Controller) handleImgErr(err error, key interface{}) {
	if err == nil {
		ctrl.imgQueue.Forget(key)
		return
	}

	if ctrl.imgQueue.NumRequeues(key) < maxRetries {
		glog.V(2).Infof("Error syncing image config %v: %v", key, err)
		ctrl.imgQueue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	glog.V(2).Infof("Dropping image config %q out of the queue: %v", key, err)
	ctrl.imgQueue.Forget(key)
	ctrl.imgQueue.AddAfter(key, 1*time.Minute)
}

// generateOriginalContainerRuntimeConfigs returns rendered default storage, registries and policy config files
func generateOriginalContainerRuntimeConfigs(templateDir string, cc *mcfgv1.ControllerConfig, role string) (*igntypes.File, *igntypes.File, *igntypes.File, error) {
	// Render the default templates
	rc := &mtmpl.RenderConfig{ControllerConfigSpec: &cc.Spec}
	generatedConfigs, err := mtmpl.GenerateMachineConfigsForRole(rc, role, templateDir)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generateMachineConfigsforRole failed with error %s", err)
	}
	// Find generated storage.conf, registries.conf, and policy.json
	var (
		config, gmcStorageConfig, gmcRegistriesConfig, gmcPolicyJSON *igntypes.File
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
		return nil, nil, nil, fmt.Errorf("could not generate old container runtime configs: %v, %v, %v", errStorage, errRegistries, errPolicy)
	}

	return gmcStorageConfig, gmcRegistriesConfig, gmcPolicyJSON, nil
}

func (ctrl *Controller) syncStatusOnly(cfg *mcfgv1.ContainerRuntimeConfig, err error, args ...interface{}) error {
	statusUpdateErr := retry.RetryOnConflict(updateBackoff, func() error {
		if cfg.GetGeneration() != cfg.Status.ObservedGeneration {
			cfg.Status.ObservedGeneration = cfg.GetGeneration()
			cfg.Status.Conditions = append(cfg.Status.Conditions, wrapErrorWithCondition(err, args...))
		} else if cfg.GetGeneration() == cfg.Status.ObservedGeneration && err == nil {
			// If the CR was created before a matching label was added, the CR would be in failure state
			// However the observed generation would be the same, so check if err is nil as well
			// Which means that, the ctrcfg was finally successfully able to sync. In that case update the status
			// to success and clear the previous failure status
			cfg.Status.Conditions = []mcfgv1.ContainerRuntimeConfigCondition{wrapErrorWithCondition(err, args...)}
		}
		_, updateErr := ctrl.client.MachineconfigurationV1().ContainerRuntimeConfigs().UpdateStatus(context.TODO(), cfg, metav1.UpdateOptions{})
		return updateErr
	})
	// If an error occurred in updating the status just log it
	if statusUpdateErr != nil {
		glog.Warningf("error updating container runtime config status: %v", statusUpdateErr)
	}
	// Want to return the actual error received from the sync function
	return err
}

// syncContainerRuntimeConfig will sync the ContainerRuntimeconfig with the given key.
// This function is not meant to be invoked concurrently with the same key.
func (ctrl *Controller) syncContainerRuntimeConfig(key string) error {
	startTime := time.Now()
	glog.V(4).Infof("Started syncing ContainerRuntimeconfig %q (%v)", key, startTime)
	defer func() {
		glog.V(4).Infof("Finished syncing ContainerRuntimeconfig %q (%v)", key, time.Since(startTime))
	}()

	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}

	// Fetch the ContainerRuntimeConfig
	cfg, err := ctrl.mccrLister.Get(name)
	if errors.IsNotFound(err) {
		glog.V(2).Infof("ContainerRuntimeConfig %v has been deleted", key)
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

	// If we have seen this generation and the sync didn't fail, then skip
	if cfg.Status.ObservedGeneration >= cfg.Generation && cfg.Status.Conditions[len(cfg.Status.Conditions)-1].Type == mcfgv1.ContainerRuntimeConfigSuccess {
		// This is the scenario for upgrades where we are moving from crio.conf to crio.conf.d
		// this can be removed in the next release
		fromOldCrio, err := ctrl.isUpdatingFromOldCRIOConf(cfg)
		if err != nil {
			return fmt.Errorf("error checking if we are updating from crio.conf: %v", err)
		}
		// We want to trigger a resync if we are updating from a version where crio.conf.d didn't exist
		if !fromOldCrio {
			return nil
		}
	}

	// Validate the ContainerRuntimeConfig CR
	if err := validateUserContainerRuntimeConfig(cfg); err != nil {
		return ctrl.syncStatusOnly(cfg, err)
	}

	// Get ControllerConfig
	controllerConfig, err := ctrl.ccLister.Get(ctrlcommon.ControllerConfigName)
	if err != nil {
		return fmt.Errorf("could not get ControllerConfig %v", err)
	}

	// Find all MachineConfigPools
	mcpPools, err := ctrl.getPoolsForContainerRuntimeConfig(cfg)
	if err != nil {
		return ctrl.syncStatusOnly(cfg, err)
	}

	if len(mcpPools) == 0 {
		err := fmt.Errorf("containerRuntimeConfig %v does not match any MachineConfigPools", key)
		glog.V(2).Infof("%v", err)
		return ctrl.syncStatusOnly(cfg, err)
	}

	for _, pool := range mcpPools {
		role := pool.Name
		// Get MachineConfig
		managedKey, err := getManagedKeyCtrCfg(pool, ctrl.client)
		if err != nil {
			return ctrl.syncStatusOnly(cfg, err)
		}
		if err := retry.RetryOnConflict(updateBackoff, func() error {
			mc, err := ctrl.client.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), managedKey, metav1.GetOptions{})
			if err != nil && !errors.IsNotFound(err) {
				return ctrl.syncStatusOnly(cfg, err, "could not find MachineConfig: %v", managedKey)
			}
			isNotFound := errors.IsNotFound(err)
			// Generate the original ContainerRuntimeConfig
			originalStorageIgn, _, _, err := generateOriginalContainerRuntimeConfigs(ctrl.templatesDir, controllerConfig, role)
			if err != nil {
				return ctrl.syncStatusOnly(cfg, err, "could not generate origin ContainerRuntime Configs: %v", err)
			}

			var configFileList []generatedConfigFile
			ctrcfg := cfg.Spec.ContainerRuntimeConfig
			if ctrcfg.OverlaySize != (resource.Quantity{}) {
				storageTOML, err := ctrl.mergeConfigChanges(originalStorageIgn, cfg, updateStorageConfig)
				if err != nil {
					glog.V(2).Infoln(cfg, err, "error merging user changes to storage.conf: %v", err)
				} else {
					configFileList = append(configFileList, generatedConfigFile{filePath: storageConfigPath, data: storageTOML})
				}
			}

			// Create the cri-o drop-in files
			if ctrcfg.LogLevel != "" || ctrcfg.PidsLimit != 0 || ctrcfg.LogSizeMax != (resource.Quantity{}) {
				crioFileConfigs := createCRIODropinFiles(cfg)
				configFileList = append(configFileList, crioFileConfigs...)
			}

			if isNotFound {
				tempIgnCfg := ctrlcommon.NewIgnConfig()
				mc, err = ctrlcommon.MachineConfigFromIgnConfig(role, managedKey, tempIgnCfg)
				if err != nil {
					return ctrl.syncStatusOnly(cfg, err, "could not create MachineConfig from new Ignition config: %v", err)
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
			})
			oref := metav1.NewControllerRef(cfg, controllerKind)
			mc.SetOwnerReferences([]metav1.OwnerReference{*oref})

			// Create or Update, on conflict retry
			if isNotFound {
				_, err = ctrl.client.MachineconfigurationV1().MachineConfigs().Create(context.TODO(), mc, metav1.CreateOptions{})
			} else {
				_, err = ctrl.client.MachineconfigurationV1().MachineConfigs().Update(context.TODO(), mc, metav1.UpdateOptions{})
			}

			// Add Finalizers to the ContainerRuntimeConfigs
			if err := ctrl.addFinalizerToContainerRuntimeConfig(cfg, mc); err != nil {
				return ctrl.syncStatusOnly(cfg, err, "could not add finalizers to ContainerRuntimeConfig: %v", err)
			}
			return err
		}); err != nil {
			return ctrl.syncStatusOnly(cfg, err, "could not Create/Update MachineConfig: %v", err)
		}
		glog.Infof("Applied ContainerRuntimeConfig %v on MachineConfigPool %v", key, pool.Name)
	}

	return ctrl.syncStatusOnly(cfg, nil)
}

// mergeConfigChanges retrieves the original/default config data from the templates, decodes it and merges in the changes given by the Custom Resource.
// It then encodes the new data and returns it.
func (ctrl *Controller) mergeConfigChanges(origFile *igntypes.File, cfg *mcfgv1.ContainerRuntimeConfig, update updateConfigFunc) ([]byte, error) {
	dataURL, err := dataurl.DecodeString(origFile.Contents.Source)
	if err != nil {
		return nil, ctrl.syncStatusOnly(cfg, err, "could not decode original Container Runtime config: %v", err)
	}
	cfgTOML, err := update(dataURL.Data, cfg.Spec.ContainerRuntimeConfig)
	if err != nil {
		return nil, ctrl.syncStatusOnly(cfg, err, "could not update container runtime config with new changes: %v", err)
	}
	return cfgTOML, ctrl.syncStatusOnly(cfg, nil)
}

func (ctrl *Controller) syncImageConfig(key string) error {
	startTime := time.Now()
	glog.V(4).Infof("Started syncing ImageConfig %q (%v)", key, startTime)
	defer func() {
		glog.V(4).Infof("Finished syncing ImageConfig %q (%v)", key, time.Since(startTime))
	}()

	// Fetch the ImageConfig
	imgcfg, err := ctrl.imgLister.Get("cluster")
	if errors.IsNotFound(err) {
		glog.V(2).Infof("ImageConfig 'cluster' does not exist or has been deleted")
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
		glog.Infof("ClusterVersionConfig 'version' does not exist or has been deleted")
		return nil
	}
	if err != nil {
		return err
	}

	// Go through the registries in the image spec to get and validate the blocked registries
	blockedRegs, err := getValidBlockedRegistries(&clusterVersionCfg.Status, &imgcfg.Spec)
	if err != nil && err != errParsingReference {
		glog.V(2).Infof("%v, skipping....", err)
	} else if err == errParsingReference {
		return err
	}

	// Get ControllerConfig
	controllerConfig, err := ctrl.ccLister.Get(ctrlcommon.ControllerConfigName)
	if err != nil {
		return fmt.Errorf("could not get ControllerConfig %v", err)
	}

	// Find all ImageContentSourcePolicy objects
	icspRules, err := ctrl.icspLister.List(labels.Everything())
	if err != nil && errors.IsNotFound(err) {
		icspRules = []*apioperatorsv1alpha1.ImageContentSourcePolicy{}
	} else if err != nil {
		return err
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
			registriesIgn, err := registriesConfigIgnition(ctrl.templatesDir, controllerConfig, role,
				imgcfg.Spec.RegistrySources.InsecureRegistries, blockedRegs, imgcfg.Spec.RegistrySources.AllowedRegistries, icspRules)
			if err != nil {
				return err
			}
			rawRegistriesIgn, err := json.Marshal(registriesIgn)
			if err != nil {
				return fmt.Errorf("could not encode registries Ignition config: %v", err)
			}
			mc, err := ctrl.client.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), managedKey, metav1.GetOptions{})
			if err != nil && !errors.IsNotFound(err) {
				return fmt.Errorf("could not find MachineConfig: %v", err)
			}
			isNotFound := errors.IsNotFound(err)
			if !isNotFound && equality.Semantic.DeepEqual(rawRegistriesIgn, mc.Spec.Config.Raw) {
				// if the configuration for the registries is equal, we still need to compare
				// the generated controller version because during an upgrade we need a new one
				mcCtrlVersion := mc.Annotations[ctrlcommon.GeneratedByControllerVersionAnnotationKey]
				if mcCtrlVersion == version.Hash {
					applied = false
					return nil
				}
			}
			if isNotFound {
				tempIgnCfg := ctrlcommon.NewIgnConfig()
				mc, err = ctrlcommon.MachineConfigFromIgnConfig(role, managedKey, tempIgnCfg)
				if err != nil {
					return fmt.Errorf("could not create MachineConfig from new Ignition config: %v", err)
				}
			}
			mc.Spec.Config.Raw = rawRegistriesIgn
			mc.ObjectMeta.Annotations = map[string]string{
				ctrlcommon.GeneratedByControllerVersionAnnotationKey: version.Hash,
			}
			mc.ObjectMeta.OwnerReferences = []metav1.OwnerReference{
				{
					APIVersion: apicfgv1.SchemeGroupVersion.String(),
					Kind:       "Image",
					Name:       imgcfg.Name,
					UID:        imgcfg.UID,
				},
			}
			// Create or Update, on conflict retry
			if isNotFound {
				_, err = ctrl.client.MachineconfigurationV1().MachineConfigs().Create(context.TODO(), mc, metav1.CreateOptions{})
			} else {
				_, err = ctrl.client.MachineconfigurationV1().MachineConfigs().Update(context.TODO(), mc, metav1.UpdateOptions{})
			}

			return err
		}); err != nil {
			return fmt.Errorf("could not Create/Update MachineConfig: %v", err)
		}
		if applied {
			glog.Infof("Applied ImageConfig cluster on MachineConfigPool %v", pool.Name)
		}
	}

	return nil
}

func registriesConfigIgnition(templateDir string, controllerConfig *mcfgv1.ControllerConfig, role string,
	insecureRegs, blockedRegs, allowedRegs []string, icspRules []*apioperatorsv1alpha1.ImageContentSourcePolicy) (*igntypes.Config, error) {

	var (
		registriesTOML []byte
		policyJSON     []byte
	)

	// Generate the original registries config
	_, originalRegistriesIgn, originalPolicyIgn, err := generateOriginalContainerRuntimeConfigs(templateDir, controllerConfig, role)
	if err != nil {
		return nil, fmt.Errorf("could not generate origin ContainerRuntime Configs: %v", err)
	}

	if insecureRegs != nil || blockedRegs != nil || len(icspRules) != 0 {
		dataURL, err := dataurl.DecodeString(originalRegistriesIgn.Contents.Source)
		if err != nil {
			return nil, fmt.Errorf("could not decode original registries config: %v", err)
		}
		registriesTOML, err = updateRegistriesConfig(dataURL.Data, insecureRegs, blockedRegs, icspRules)
		if err != nil {
			return nil, fmt.Errorf("could not update registries config with new changes: %v", err)
		}
	}
	if blockedRegs != nil || allowedRegs != nil {
		dataURL, err := dataurl.DecodeString(originalPolicyIgn.Contents.Source)
		if err != nil {
			return nil, fmt.Errorf("could not decode original policy json: %v", err)
		}
		policyJSON, err = updatePolicyJSON(dataURL.Data, blockedRegs, allowedRegs)
		if err != nil {
			return nil, fmt.Errorf("could not update policy json with new changes: %v", err)
		}
	}
	registriesIgn := createNewIgnition([]generatedConfigFile{
		{filePath: registriesConfigPath, data: registriesTOML},
		{filePath: policyConfigPath, data: policyJSON},
	})
	return &registriesIgn, nil
}

// RunImageBootstrap generates MachineConfig objects for mcpPools that would have been generated by syncImageConfig,
// except that mcfgv1.Image is not available.
func RunImageBootstrap(templateDir string, controllerConfig *mcfgv1.ControllerConfig, mcpPools []*mcfgv1.MachineConfigPool, icspRules []*apioperatorsv1alpha1.ImageContentSourcePolicy) ([]*mcfgv1.MachineConfig, error) {
	// ImageConfig is not available.
	insecureRegs := []string(nil)
	blockedRegs := []string(nil)
	allowedRegs := []string(nil)

	var res []*mcfgv1.MachineConfig
	for _, pool := range mcpPools {
		role := pool.Name
		managedKey, err := getManagedKeyReg(pool, nil)
		if err != nil {
			return nil, err
		}
		registriesIgn, err := registriesConfigIgnition(templateDir, controllerConfig, role,
			insecureRegs, blockedRegs, allowedRegs, icspRules)
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
		ctrCfgTmp.Finalizers = append(ctrCfg.Finalizers[:0], ctrCfg.Finalizers[1:]...)

		modJSON, err := json.Marshal(ctrCfgTmp)
		if err != nil {
			return err
		}

		patch, err := jsonmergepatch.CreateThreeWayJSONMergePatch(curJSON, modJSON, curJSON)
		if err != nil {
			return err
		}
		return ctrl.patchContainerRuntimeConfigsFunc(ctrCfg.Name, patch)
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
		ctrCfgTmp.Finalizers = append(ctrCfgTmp.Finalizers, mc.Name)

		modJSON, err := json.Marshal(ctrCfgTmp)
		if err != nil {
			return err
		}

		patch, err := jsonmergepatch.CreateThreeWayJSONMergePatch(curJSON, modJSON, curJSON)
		if err != nil {
			return err
		}
		return ctrl.patchContainerRuntimeConfigsFunc(ctrCfg.Name, patch)
	})
}

func (ctrl *Controller) getPoolsForContainerRuntimeConfig(config *mcfgv1.ContainerRuntimeConfig) ([]*mcfgv1.MachineConfigPool, error) {
	pList, err := ctrl.mcpLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}

	selector, err := metav1.LabelSelectorAsSelector(config.Spec.MachineConfigPoolSelector)
	if err != nil {
		return nil, fmt.Errorf("invalid label selector: %v", err)
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

// isUpdatingFromOldCRIOConf returns true if the mc associated with cfg has /etc/crio/crio.conf as
// its file path.
func (ctrl *Controller) isUpdatingFromOldCRIOConf(cfg *mcfgv1.ContainerRuntimeConfig) (bool, error) {
	mcpPools, err := ctrl.getPoolsForContainerRuntimeConfig(cfg)
	if err != nil {
		return false, fmt.Errorf("could not get list of machine config pools: %v", err)
	}
	if len(mcpPools) == 0 {
		return false, nil
	}

	for _, pool := range mcpPools {
		managedKey, err := getManagedKeyCtrCfg(pool, ctrl.client)
		if err != nil {
			return false, err
		}
		mc, err := ctrl.client.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), managedKey, metav1.GetOptions{})
		if err != nil && !errors.IsNotFound(err) {
			return false, fmt.Errorf("could not get mc with name %q: %v", managedKey, err)
		}
		if mc.Spec.Config.Raw != nil {
			conf, err := ctrlcommon.IgnParseWrapper(mc.Spec.Config.Raw)
			if err != nil {
				return false, fmt.Errorf("error parsing ignition: %v", err)
			}
			// If the filepath matches /etc/crio/crio.conf return true
			for _, file := range conf.Storage.Files {
				if file.Path == "/etc/crio/crio.conf" {
					return true, nil
				}
			}
		}
	}
	return false, nil
}
