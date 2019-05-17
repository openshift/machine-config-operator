package kubeletconfig

import (
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	igntypes "github.com/coreos/ignition/config/v2_2/types"
	"github.com/golang/glog"
	"github.com/imdario/mergo"
	"github.com/vincent-petithory/dataurl"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
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
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"

	oseinformersv1 "github.com/openshift/client-go/config/informers/externalversions/config/v1"
	oselistersv1 "github.com/openshift/client-go/config/listers/config/v1"
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
	// maxRetries is the number of times a machineconfig pool will be retried before it is dropped out of the queue.
	// With the current rate-limiter in use (5ms*2^(maxRetries-1)) the following numbers represent the times
	// a machineconfig pool is going to be requeued:
	//
	// 5ms, 10ms, 20ms, 40ms, 80ms, 160ms, 320ms, 640ms, 1.3s, 2.6s, 5.1s, 10.2s, 20.4s, 41s, 82s
	maxRetries = 15
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

// A list of fields a user cannot set within the KubeletConfig CR. If a user
// were to set these values, then the system may become unrecoverable (ie: not
// recover after a reboot).
//
// If the KubeletConfig CR instance contains a non-zero or non-empty value for
// the following fields, then the MCC will not apply the CR and log the message.
var blacklistKubeletConfigurationFields = []string{
	"CgroupDriver",
	"ClusterDNS",
	"ClusterDomain",
	// Bugfix to force cache based configmap and secret watches. This should be
	// removed with Kubernetes 1.14.
	//   https://github.com/kubernetes/kubernetes/issues/74412
	"ConfigMapAndSecretChangeDetectionStrategy",
	"FeatureGates",
	"RuntimeRequestTimeout",
	"StaticPodPath",
}

// Controller defines the kubelet config controller.
type Controller struct {
	templatesDir string

	client        mcfgclientset.Interface
	eventRecorder record.EventRecorder

	syncHandler          func(mcp string) error
	enqueueKubeletConfig func(*mcfgv1.KubeletConfig)

	ccLister       mcfglistersv1.ControllerConfigLister
	ccListerSynced cache.InformerSynced

	mckLister       mcfglistersv1.KubeletConfigLister
	mckListerSynced cache.InformerSynced

	mcpLister       mcfglistersv1.MachineConfigPoolLister
	mcpListerSynced cache.InformerSynced

	featLister       oselistersv1.FeatureGateLister
	featListerSynced cache.InformerSynced

	queue        workqueue.RateLimitingInterface
	featureQueue workqueue.RateLimitingInterface

	// we need this method to mock out patch calls in unit until https://github.com/openshift/machine-config-operator/pull/611#issuecomment-481397185
	// which is probably going to be in kube 1.14
	patchKubeletConfigsFunc func(string, []byte) error
}

// New returns a new kubelet config controller
func New(
	templatesDir string,
	mcpInformer mcfginformersv1.MachineConfigPoolInformer,
	ccInformer mcfginformersv1.ControllerConfigInformer,
	mkuInformer mcfginformersv1.KubeletConfigInformer,
	featInformer oseinformersv1.FeatureGateInformer,
	kubeClient clientset.Interface,
	mcfgClient mcfgclientset.Interface,
) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.Infof)
	eventBroadcaster.StartRecordingToSink(&coreclientsetv1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	ctrl := &Controller{
		templatesDir:  templatesDir,
		client:        mcfgClient,
		eventRecorder: eventBroadcaster.NewRecorder(scheme.Scheme, v1.EventSource{Component: "machineconfigcontroller-kubeletconfigcontroller"}),
		queue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "machineconfigcontroller-kubeletconfigcontroller"),
		featureQueue:  workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "machineconfigcontroller-featurecontroller"),
	}

	mkuInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addKubeletConfig,
		UpdateFunc: ctrl.updateKubeletConfig,
		DeleteFunc: ctrl.deleteKubeletConfig,
	})

	featInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addFeature,
		UpdateFunc: ctrl.updateFeature,
		DeleteFunc: ctrl.deleteFeature,
	})

	ctrl.syncHandler = ctrl.syncKubeletConfig
	ctrl.enqueueKubeletConfig = ctrl.enqueue

	ctrl.mcpLister = mcpInformer.Lister()
	ctrl.mcpListerSynced = mcpInformer.Informer().HasSynced

	ctrl.ccLister = ccInformer.Lister()
	ctrl.ccListerSynced = ccInformer.Informer().HasSynced

	ctrl.mckLister = mkuInformer.Lister()
	ctrl.mckListerSynced = mkuInformer.Informer().HasSynced

	ctrl.featLister = featInformer.Lister()
	ctrl.featListerSynced = featInformer.Informer().HasSynced
	
	ctrl.patchKubeletConfigsFunc = ctrl.patchKubeletConfigs

	return ctrl
}

// Run executes the kubelet config controller.
func (ctrl *Controller) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()
	defer ctrl.featureQueue.ShutDown()

	if !cache.WaitForCacheSync(stopCh, ctrl.mcpListerSynced, ctrl.mckListerSynced, ctrl.ccListerSynced, ctrl.featListerSynced) {
		return
	}

	glog.Info("Starting MachineConfigController-KubeletConfigController")
	defer glog.Info("Shutting down MachineConfigController-KubeletConfigController")

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.worker, time.Second, stopCh)
	}

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.featureWorker, time.Second, stopCh)
	}

	<-stopCh
}

func kubeletConfigTriggerObjectChange(old, new *mcfgv1.KubeletConfig) bool {
	if old.DeletionTimestamp != new.DeletionTimestamp {
		return true
	}
	if !reflect.DeepEqual(old.Spec, new.Spec) {
		return true
	}
	return false
}

func (ctrl *Controller) updateKubeletConfig(old, cur interface{}) {
	oldConfig := old.(*mcfgv1.KubeletConfig)
	newConfig := cur.(*mcfgv1.KubeletConfig)

	if kubeletConfigTriggerObjectChange(oldConfig, newConfig) {
		glog.V(4).Infof("Update KubeletConfig %s", oldConfig.Name)
		ctrl.enqueueKubeletConfig(newConfig)
	}
}

func (ctrl *Controller) addKubeletConfig(obj interface{}) {
	cfg := obj.(*mcfgv1.KubeletConfig)
	glog.V(4).Infof("Adding KubeletConfig %s", cfg.Name)
	ctrl.enqueueKubeletConfig(cfg)
}

func (ctrl *Controller) deleteKubeletConfig(obj interface{}) {
	cfg, ok := obj.(*mcfgv1.KubeletConfig)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("couldn't get object from tombstone %#v", obj))
			return
		}
		cfg, ok = tombstone.Obj.(*mcfgv1.KubeletConfig)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Tombstone contained object that is not a KubeletConfig %#v", obj))
			return
		}
	}
	ctrl.cascadeDelete(cfg)
	glog.V(4).Infof("Deleted KubeletConfig %s and restored default config", cfg.Name)
}

func (ctrl *Controller) cascadeDelete(cfg *mcfgv1.KubeletConfig) error {
	if len(cfg.GetFinalizers()) == 0 {
		return nil
	}
	mcName := cfg.GetFinalizers()[0]
	err := ctrl.client.Machineconfiguration().MachineConfigs().Delete(mcName, &metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	if err := ctrl.popFinalizerFromKubeletConfig(cfg); err != nil {
		return err
	}
	return nil
}

func (ctrl *Controller) enqueue(cfg *mcfgv1.KubeletConfig) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(cfg)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %v", cfg, err))
		return
	}
	ctrl.queue.Add(key)
}

func (ctrl *Controller) enqueueRateLimited(cfg *mcfgv1.KubeletConfig) {
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
		glog.V(2).Infof("Error syncing kubeletconfig %v: %v", key, err)
		ctrl.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	glog.V(2).Infof("Dropping kubeletconfig %q out of the queue: %v", key, err)
	ctrl.queue.Forget(key)
	ctrl.queue.AddAfter(key, 1*time.Minute)
}

func (ctrl *Controller) handleFeatureErr(err error, key interface{}) {
	if err == nil {
		ctrl.featureQueue.Forget(key)
		return
	}

	if ctrl.featureQueue.NumRequeues(key) < maxRetries {
		glog.V(2).Infof("Error syncing kubeletconfig %v: %v", key, err)
		ctrl.featureQueue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	glog.V(2).Infof("Dropping featureconfig %q out of the queue: %v", key, err)
	ctrl.featureQueue.Forget(key)
	ctrl.featureQueue.AddAfter(key, 1*time.Minute)
}

func (ctrl *Controller) generateOriginalKubeletConfig(role string) (*igntypes.File, error) {
	cc, err := ctrl.ccLister.Get(ctrlcommon.ControllerConfigName)
	if err != nil {
		return nil, fmt.Errorf("could not get ControllerConfig %v", err)
	}
	// Render the default templates
	rc := &mtmpl.RenderConfig{ControllerConfigSpec: &cc.Spec}
	generatedConfigs, err := mtmpl.GenerateMachineConfigsForRole(rc, role, ctrl.templatesDir)
	if err != nil {
		return nil, fmt.Errorf("GenerateMachineConfigsforRole failed with error %s", err)
	}
	// Find generated kubelet.config
	for _, gmc := range generatedConfigs {
		gmcKubeletConfig, err := findKubeletConfig(gmc)
		if err != nil {
			continue
		}
		return gmcKubeletConfig, nil
	}
	return nil, fmt.Errorf("could not generate old kubelet config")
}

func (ctrl *Controller) syncStatusOnly(cfg *mcfgv1.KubeletConfig, err error, args ...interface{}) error {
	statusUpdateError := retry.RetryOnConflict(updateBackoff, func() error {
		newcfg, getErr := ctrl.mckLister.Get(cfg.Name)
		if getErr != nil {
			return getErr
		}
		newcfg.Status.Conditions = append(newcfg.Status.Conditions, wrapErrorWithCondition(err, args...))
		_, lerr := ctrl.client.MachineconfigurationV1().KubeletConfigs().UpdateStatus(newcfg)
		return lerr
	})
	if statusUpdateError != nil {
		glog.Warningf("error updating kubeletconfig status: %v", statusUpdateError)
	}
	return err
}

// syncKubeletConfig will sync the kubeletconfig with the given key.
// This function is not meant to be invoked concurrently with the same key.
func (ctrl *Controller) syncKubeletConfig(key string) error {
	startTime := time.Now()
	glog.V(4).Infof("Started syncing kubeletconfig %q (%v)", key, startTime)
	defer func() {
		glog.V(4).Infof("Finished syncing kubeletconfig %q (%v)", key, time.Since(startTime))
	}()

	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}

	// Fetch the KubeletConfig
	cfg, err := ctrl.mckLister.Get(name)
	if errors.IsNotFound(err) {
		glog.V(2).Infof("KubeletConfig %v has been deleted", key)
		return nil
	}
	if err != nil {
		return err
	}

	// Deep-copy otherwise we are mutating our cache.
	cfg = cfg.DeepCopy()

	// Check for Deleted KubeletConfig and optionally delete finalizers
	if cfg.DeletionTimestamp != nil {
		if len(cfg.GetFinalizers()) > 0 {
			return ctrl.cascadeDelete(cfg)
		}
		return nil
	}

	// If we have seen this generation then skip
	if cfg.Status.ObservedGeneration >= cfg.Generation {
		return nil
	}

	// Validate the KubeletConfig CR
	if err := validateUserKubeletConfig(cfg); err != nil {
		return ctrl.syncStatusOnly(cfg, err)
	}

	// Find all MachineConfigPools
	mcpPools, err := ctrl.getPoolsForKubeletConfig(cfg)
	if err != nil {
		return ctrl.syncStatusOnly(cfg, err)
	}

	if len(mcpPools) == 0 {
		err := fmt.Errorf("KubeletConfig %v does not match any MachineConfigPools", key)
		glog.V(2).Infof("%v", err)
		return ctrl.syncStatusOnly(cfg, err)
	}

	features, err := ctrl.featLister.Get(clusterFeatureInstanceName)
	if errors.IsNotFound(err) {
		features = createNewDefaultFeatureGate()
	} else if err != nil {
		glog.V(2).Infof("%v", err)
		err := fmt.Errorf("could not fetch FeatureGates: %v", err)
		return ctrl.syncStatusOnly(cfg, err)
	}
	featureGates, err := ctrl.generateFeatureMap(features)
	if err != nil {
		err := fmt.Errorf("could not generate FeatureMap: %v", err)
		glog.V(2).Infof("%v", err)
		return ctrl.syncStatusOnly(cfg, err)
	}

	for _, pool := range mcpPools {
		role := pool.Name
		// Get MachineConfig
		managedKey := getManagedKubeletConfigKey(pool)
		mc, err := ctrl.client.Machineconfiguration().MachineConfigs().Get(managedKey, metav1.GetOptions{})
		if err != nil && !errors.IsNotFound(err) {
			return ctrl.syncStatusOnly(cfg, err, "could not find MachineConfig: %v", managedKey)
		}
		isNotFound := errors.IsNotFound(err)
		// Generate the original KubeletConfig
		originalKubeletIgn, err := ctrl.generateOriginalKubeletConfig(role)
		if err != nil {
			return ctrl.syncStatusOnly(cfg, err, "could not generate the original Kubelet config: %v", err)
		}
		dataURL, err := dataurl.DecodeString(originalKubeletIgn.Contents.Source)
		if err != nil {
			return ctrl.syncStatusOnly(cfg, err, "could not decode the original Kubelet source string: %v", err)
		}
		originalKubeConfig, err := decodeKubeletConfig(dataURL.Data)
		if err != nil {
			return ctrl.syncStatusOnly(cfg, err, "could not deserialize the Kubelet source: %v", err)
		}
		// Merge the Old and New
		err = mergo.Merge(originalKubeConfig, cfg.Spec.KubeletConfig, mergo.WithOverride)
		if err != nil {
			return ctrl.syncStatusOnly(cfg, err, "could not merge original config and new config: %v", err)
		}
		// Merge in Feature Gates
		err = mergo.Merge(&originalKubeConfig.FeatureGates, featureGates, mergo.WithOverride)
		if err != nil {
			return ctrl.syncStatusOnly(cfg, err, "could not merge FeatureGates: %v", err)
		}
		// Encode the new config into YAML
		cfgYAML, err := encodeKubeletConfig(originalKubeConfig, kubeletconfigv1beta1.SchemeGroupVersion)
		if err != nil {
			return ctrl.syncStatusOnly(cfg, err, "could not encode YAML: %v", err)
		}
		if isNotFound {
			ignConfig := ctrlcommon.NewIgnConfig()
			mc = mtmpl.MachineConfigFromIgnConfig(role, managedKey, &ignConfig)
		}
		mc.Spec.Config = createNewKubeletIgnition(cfgYAML)
		mc.SetAnnotations(map[string]string{
			ctrlcommon.GeneratedByControllerVersionAnnotationKey: version.Hash,
		})
		oref := metav1.NewControllerRef(cfg, controllerKind)
		mc.SetOwnerReferences([]metav1.OwnerReference{*oref})

		// Create or Update, on conflict retry
		if err := retry.RetryOnConflict(updateBackoff, func() error {
			var err error
			if isNotFound {
				_, err = ctrl.client.Machineconfiguration().MachineConfigs().Create(mc)
			} else {
				_, err = ctrl.client.Machineconfiguration().MachineConfigs().Update(mc)
			}
			return err
		}); err != nil {
			return ctrl.syncStatusOnly(cfg, err, "could not Create/Update MachineConfig: %v", err)
		}
		// Add Finalizers to the KubletConfig
		if err := ctrl.addFinalizerToKubeletConfig(cfg, mc); err != nil {
			return ctrl.syncStatusOnly(cfg, err, "could not add finalizers to KubeletConfig: %v", err)
		}
		glog.Infof("Applied KubeletConfig %v on MachineConfigPool %v", key, pool.Name)
	}

	return ctrl.syncStatusOnly(cfg, nil)
}

func (ctrl *Controller) popFinalizerFromKubeletConfig(kc *mcfgv1.KubeletConfig) error {
	return retry.RetryOnConflict(updateBackoff, func() error {
		newcfg, err := ctrl.mckLister.Get(kc.Name)
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

		kcTmp := newcfg.DeepCopy()
		kcTmp.Finalizers = append(kc.Finalizers[:0], kc.Finalizers[1:]...)

		modJSON, err := json.Marshal(kcTmp)
		if err != nil {
			return err
		}

		patch, err := jsonmergepatch.CreateThreeWayJSONMergePatch(curJSON, modJSON, curJSON)
		if err != nil {
			return err
		}
		return ctrl.patchKubeletConfigsFunc(newcfg.Name, patch)
	})
}

func (ctrl *Controller) patchKubeletConfigs(name string, patch []byte) error {
	_, err := ctrl.client.MachineconfigurationV1().KubeletConfigs().Patch(name, types.MergePatchType, patch)
	return err
}

func (ctrl *Controller) addFinalizerToKubeletConfig(kc *mcfgv1.KubeletConfig, mc *mcfgv1.MachineConfig) error {
	return retry.RetryOnConflict(updateBackoff, func() error {
		newcfg, err := ctrl.mckLister.Get(kc.Name)
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

		kcTmp := newcfg.DeepCopy()
		kcTmp.Finalizers = append(kcTmp.Finalizers, mc.Name)

		modJSON, err := json.Marshal(kcTmp)
		if err != nil {
			return err
		}
		patch, err := jsonmergepatch.CreateThreeWayJSONMergePatch(curJSON, modJSON, curJSON)
		if err != nil {
			return err
		}
		return ctrl.patchKubeletConfigsFunc(newcfg.Name, patch)
	})
}

func (ctrl *Controller) getPoolsForKubeletConfig(config *mcfgv1.KubeletConfig) ([]*mcfgv1.MachineConfigPool, error) {
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
		return nil, fmt.Errorf("could not find any MachineConfigPool set for KubeletConfig %s", config.Name)
	}

	return pools, nil
}
