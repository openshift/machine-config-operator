package kubeletconfig

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/clarketm/json"
	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/imdario/mergo"
	corev1 "k8s.io/api/core/v1"
	macherrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	coreclientsetv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"

	configv1 "github.com/openshift/api/config/v1"
	osev1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	configclientset "github.com/openshift/client-go/config/clientset/versioned"
	oseinformersv1 "github.com/openshift/client-go/config/informers/externalversions/config/v1"
	oselistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/scheme"
	mcfginformersv1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	mtmpl "github.com/openshift/machine-config-operator/pkg/controller/template"
	"github.com/openshift/machine-config-operator/pkg/version"
)

const (
	// maxRetries is the number of times a machineconfig pool will be retried before it is dropped out of the queue.
	// With the current rate-limiter in use (5ms*2^(maxRetries-1)) the following numbers represent the times
	// a machineconfig pool is going to be requeued:
	//
	// 18 allows for retries up to about 10 minutes to allow for slower machines to catchup.
	maxRetries = 18
)

// controllerKind contains the schema.GroupVersionKind for this controller type.
var controllerKind = mcfgv1.SchemeGroupVersion.WithKind("KubeletConfig")

var updateBackoff = wait.Backoff{
	Steps:    5,
	Duration: 100 * time.Millisecond,
	Jitter:   1.0,
}

var errCouldNotFindMCPSet = errors.New("could not find any MachineConfigPool set for KubeletConfig")

// Controller defines the kubelet config controller.
type Controller struct {
	templatesDir string

	client       mcfgclientset.Interface
	configClient configclientset.Interface

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

	nodeConfigLister       oselistersv1.NodeLister
	nodeConfigListerSynced cache.InformerSynced

	apiserverLister       oselistersv1.APIServerLister
	apiserverListerSynced cache.InformerSynced

	queue           workqueue.TypedRateLimitingInterface[string]
	featureQueue    workqueue.TypedRateLimitingInterface[string]
	nodeConfigQueue workqueue.TypedRateLimitingInterface[string]

	featureGateAccess featuregates.FeatureGateAccess
}

// New returns a new kubelet config controller
func New(
	templatesDir string,
	mcpInformer mcfginformersv1.MachineConfigPoolInformer,
	ccInformer mcfginformersv1.ControllerConfigInformer,
	mkuInformer mcfginformersv1.KubeletConfigInformer,
	featInformer oseinformersv1.FeatureGateInformer,
	nodeConfigInformer oseinformersv1.NodeInformer,
	apiserverInformer oseinformersv1.APIServerInformer,
	kubeClient clientset.Interface,
	mcfgClient mcfgclientset.Interface,
	configclient configclientset.Interface,
	fgAccess featuregates.FeatureGateAccess,
) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&coreclientsetv1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	ctrl := &Controller{
		templatesDir:  templatesDir,
		client:        mcfgClient,
		configClient:  configclient,
		eventRecorder: ctrlcommon.NamespacedEventRecorder(eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineconfigcontroller-kubeletconfigcontroller"})),
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "machineconfigcontroller-kubeletconfigcontroller"}),
		featureQueue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "machineconfigcontroller-featurecontroller"}),
		nodeConfigQueue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "machineconfigcontroller-nodeConfigcontroller"}),
		featureGateAccess: fgAccess,
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

	nodeConfigInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addNodeConfig,
		UpdateFunc: ctrl.updateNodeConfig,
		DeleteFunc: ctrl.deleteNodeConfig,
	})

	apiserverInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addAPIServer,
		UpdateFunc: ctrl.updateAPIServer,
		DeleteFunc: ctrl.deleteAPIServer,
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

	ctrl.nodeConfigLister = nodeConfigInformer.Lister()
	ctrl.nodeConfigListerSynced = nodeConfigInformer.Informer().HasSynced

	ctrl.apiserverLister = apiserverInformer.Lister()
	ctrl.apiserverListerSynced = apiserverInformer.Informer().HasSynced

	return ctrl
}

// Run executes the kubelet config controller.
func (ctrl *Controller) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()
	defer ctrl.featureQueue.ShutDown()
	defer ctrl.nodeConfigQueue.ShutDown()

	if !cache.WaitForCacheSync(stopCh, ctrl.mcpListerSynced, ctrl.mckListerSynced, ctrl.ccListerSynced, ctrl.featListerSynced, ctrl.apiserverListerSynced) {
		return
	}

	klog.Info("Starting MachineConfigController-KubeletConfigController")
	defer klog.Info("Shutting down MachineConfigController-KubeletConfigController")

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.worker, time.Second, stopCh)
	}

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.featureWorker, time.Second, stopCh)
	}

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.nodeConfigWorker, time.Second, stopCh)
	}

	<-stopCh
}
func (ctrl *Controller) filterAPIServer(apiServer *configv1.APIServer) {
	if apiServer.Name != "cluster" {
		return
	}
	klog.Infof("Re-syncing all kubelet config controller generated MachineConfigs due to apiServer %s change", apiServer.Name)
	// sync the node config controller. This also calls back the sync function
	// for the kubelet config controller and feature gate controller, so no need
	// to do them here again. Do a direct get call here in case the node config
	// lister's cache is empty.
	if nodeConfig, err := ctrl.configClient.ConfigV1().Nodes().Get(context.TODO(), ctrlcommon.ClusterNodeInstanceName, metav1.GetOptions{}); err != nil {
		utilruntime.HandleError(fmt.Errorf("could not get NodeConfigs, err: %v", err))
	} else {
		ctrl.enqueueNodeConfig(nodeConfig)
	}

}

func (ctrl *Controller) updateAPIServer(old, cur interface{}) {
	oldAPIServer := old.(*configv1.APIServer)
	newAPIServer := cur.(*configv1.APIServer)

	if !reflect.DeepEqual(oldAPIServer.Spec, newAPIServer.Spec) {
		klog.V(4).Infof("Updating APIServer: %s", newAPIServer.Name)
		ctrl.filterAPIServer(newAPIServer)
	}
}

func (ctrl *Controller) addAPIServer(obj interface{}) {
	apiServer := obj.(*configv1.APIServer)
	if apiServer.DeletionTimestamp != nil {
		ctrl.deleteAPIServer(apiServer)
		return
	}
	klog.V(4).Infof("Add API Server %v", apiServer)
	ctrl.filterAPIServer(apiServer)
}

func (ctrl *Controller) deleteAPIServer(obj interface{}) {
	apiServer, ok := obj.(*configv1.APIServer)
	klog.V(4).Infof("Delete API Server %v", apiServer)

	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("couldn't get object from tombstone %#v", obj))
			return
		}
		apiServer, ok = tombstone.Obj.(*configv1.APIServer)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not a apiServer %#v", obj))
			return
		}
	}
	ctrl.filterAPIServer(apiServer)
}

func kubeletConfigTriggerObjectChange(old, newKubeletConfig *mcfgv1.KubeletConfig) bool {
	if old.DeletionTimestamp != newKubeletConfig.DeletionTimestamp {
		return true
	}
	if !reflect.DeepEqual(old.Spec, newKubeletConfig.Spec) {
		return true
	}
	return false
}

func (ctrl *Controller) updateKubeletConfig(old, cur interface{}) {
	oldConfig := old.(*mcfgv1.KubeletConfig)
	newConfig := cur.(*mcfgv1.KubeletConfig)

	if kubeletConfigTriggerObjectChange(oldConfig, newConfig) {
		klog.V(4).Infof("Update KubeletConfig %s", oldConfig.Name)
		ctrl.enqueueKubeletConfig(newConfig)
	}
}

func (ctrl *Controller) addKubeletConfig(obj interface{}) {
	cfg := obj.(*mcfgv1.KubeletConfig)
	klog.V(4).Infof("Adding KubeletConfig %s", cfg.Name)
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
	if err := ctrl.cascadeDelete(cfg); err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't delete object %#v: %w", cfg, err))
	} else {
		klog.V(4).Infof("Deleted KubeletConfig %s and restored default config", cfg.Name)
	}
}

func (ctrl *Controller) cascadeDelete(cfg *mcfgv1.KubeletConfig) error {
	if len(cfg.GetFinalizers()) == 0 {
		return nil
	}
	finalizerName := cfg.GetFinalizers()[0]
	mcs, err := ctrl.client.MachineconfigurationV1().MachineConfigs().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, mc := range mcs.Items {
		if string(mc.ObjectMeta.GetUID()) == finalizerName || mc.GetName() == finalizerName {
			err := ctrl.client.MachineconfigurationV1().MachineConfigs().Delete(context.TODO(), mc.GetName(), metav1.DeleteOptions{})
			if err != nil && !macherrors.IsNotFound(err) {
				return err
			}
			break
		}
	}
	return ctrl.popFinalizerFromKubeletConfig(cfg)
}

func (ctrl *Controller) enqueue(cfg *mcfgv1.KubeletConfig) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(cfg)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", cfg, err))
		return
	}
	ctrl.queue.Add(key)
}

func (ctrl *Controller) enqueueRateLimited(cfg *mcfgv1.KubeletConfig) {
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

	if _, ok := err.(*forgetError); ok {
		ctrl.queue.Forget(key)
		return
	}

	if ctrl.queue.NumRequeues(key) < maxRetries {
		klog.V(2).Infof("Error syncing kubeletconfig %v: %v", key, err)
		ctrl.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	klog.V(2).Infof("Dropping kubeletconfig %q out of the queue: %v", key, err)
	ctrl.queue.Forget(key)
	ctrl.queue.AddAfter(key, 1*time.Minute)
}

func (ctrl *Controller) handleFeatureErr(err error, key string) {
	if err == nil {
		ctrl.featureQueue.Forget(key)
		return
	}

	if ctrl.featureQueue.NumRequeues(key) < maxRetries {
		klog.V(2).Infof("Error syncing kubeletconfig %v: %v", key, err)
		ctrl.featureQueue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	klog.V(2).Infof("Dropping featureconfig %q out of the queue: %v", key, err)
	ctrl.featureQueue.Forget(key)
	ctrl.featureQueue.AddAfter(key, 1*time.Minute)
}

// generateOriginalKubeletConfigWithFeatureGates generates a KubeletConfig and ensure the correct feature gates are set
// based on the given FeatureGate.
func generateOriginalKubeletConfigWithFeatureGates(cc *mcfgv1.ControllerConfig, templatesDir, role string, featureGateAccess featuregates.FeatureGateAccess, apiServer *configv1.APIServer) (*kubeletconfigv1beta1.KubeletConfiguration, error) {
	originalKubeletIgn, err := generateOriginalKubeletConfigIgn(cc, templatesDir, role, apiServer)
	if err != nil {
		return nil, fmt.Errorf("could not generate the original Kubelet config ignition: %w", err)
	}
	if originalKubeletIgn.Contents.Source == nil {
		return nil, fmt.Errorf("the original Kubelet source string is empty: %w", err)
	}
	contents, err := ctrlcommon.DecodeIgnitionFileContents(originalKubeletIgn.Contents.Source, originalKubeletIgn.Contents.Compression)
	if err != nil {
		return nil, fmt.Errorf("could not decode the original Kubelet source string: %w", err)
	}
	originalKubeConfig, err := DecodeKubeletConfig(contents)
	if err != nil {
		return nil, fmt.Errorf("could not deserialize the Kubelet source: %w", err)
	}

	featureGates, err := generateFeatureMap(featureGateAccess, openshiftOnlyFeatureGates...)
	if err != nil {
		return nil, fmt.Errorf("could not generate features map: %w", err)
	}

	// Merge in Feature Gates.
	// If they are the same, this will be a no-op
	if err := mergo.Merge(&originalKubeConfig.FeatureGates, featureGates, mergo.WithOverride); err != nil {
		return nil, fmt.Errorf("could not merge feature gates: %w", err)
	}

	return originalKubeConfig, nil
}

func generateOriginalKubeletConfigIgn(cc *mcfgv1.ControllerConfig, templatesDir, role string, apiServer *osev1.APIServer) (*ign3types.File, error) {
	// Render the default templates
	tlsMinVersion, tlsCipherSuites := ctrlcommon.GetSecurityProfileCiphersFromAPIServer(apiServer)
	rc := &mtmpl.RenderConfig{ControllerConfigSpec: &cc.Spec, TLSMinVersion: tlsMinVersion, TLSCipherSuites: tlsCipherSuites}
	generatedConfigs, err := mtmpl.GenerateMachineConfigsForRole(rc, role, templatesDir)
	if err != nil {
		return nil, fmt.Errorf("GenerateMachineConfigsforRole failed with error: %w", err)
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
		// Keeps a list of three status to avoid a long list of same statuses,
		// only append a status if it is the first status
		// or if the status message is different from the message of the last status recorded
		// If the last status message is the same as the new one, then update the last status to
		// reflect the latest time stamp from the new status message.
		newStatusCondition := wrapErrorWithCondition(err, args...)
		cleanUpStatusConditions(&newcfg.Status.Conditions, newStatusCondition)
		_, lerr := ctrl.client.MachineconfigurationV1().KubeletConfigs().UpdateStatus(context.TODO(), newcfg, metav1.UpdateOptions{})
		return lerr
	})
	if statusUpdateError != nil {
		klog.Warningf("error updating kubeletconfig status: %v", statusUpdateError)
	}
	return err
}

// cleanUpStatusConditions keeps at most three conditions of different timestamps for the kubelet config object
func cleanUpStatusConditions(statusConditions *[]mcfgv1.KubeletConfigCondition, newStatusCondition mcfgv1.KubeletConfigCondition) {
	statusLimit := 3
	statusLen := len(*statusConditions)
	if statusLen > 0 && (*statusConditions)[statusLen-1].Message == newStatusCondition.Message {
		(*statusConditions)[statusLen-1].LastTransitionTime = newStatusCondition.LastTransitionTime
	} else {
		*statusConditions = append(*statusConditions, newStatusCondition)
	}
	if len(*statusConditions) > statusLimit {
		*statusConditions = (*statusConditions)[len(*statusConditions)-statusLimit:]
	}
}

// addAnnotation adds the annotions for a kubeletconfig object with the given annotationKey and annotationVal
func (ctrl *Controller) addAnnotation(cfg *mcfgv1.KubeletConfig, annotationKey, annotationVal string) error {
	annotationUpdateErr := retry.RetryOnConflict(updateBackoff, func() error {
		newcfg, getErr := ctrl.mckLister.Get(cfg.Name)
		if getErr != nil {
			return getErr
		}
		newcfg.SetAnnotations(map[string]string{
			annotationKey: annotationVal,
		})
		_, updateErr := ctrl.client.MachineconfigurationV1().KubeletConfigs().Update(context.TODO(), newcfg, metav1.UpdateOptions{})
		return updateErr
	})
	if annotationUpdateErr != nil {
		klog.Warningf("error updating the kubelet config with annotation key %q and value %q: %v", annotationKey, annotationVal, annotationUpdateErr)
	}
	return annotationUpdateErr
}

// syncKubeletConfig will sync the kubeletconfig with the given key.
// This function is not meant to be invoked concurrently with the same key.
//
//nolint:gocyclo
func (ctrl *Controller) syncKubeletConfig(key string) error {
	startTime := time.Now()
	klog.V(4).Infof("Started syncing kubeletconfig %q (%v)", key, startTime)
	defer func() {
		klog.V(4).Infof("Finished syncing kubeletconfig %q (%v)", key, time.Since(startTime))
	}()

	// Wait to apply a kubelet config if the controller config is not completed
	if err := apihelpers.IsControllerConfigCompleted(ctrlcommon.ControllerConfigName, ctrl.ccLister.Get); err != nil {
		return err
	}

	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}

	// Fetch the KubeletConfig
	cfg, err := ctrl.mckLister.Get(name)
	if macherrors.IsNotFound(err) {
		klog.V(2).Infof("KubeletConfig %v has been deleted", key)
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
		return ctrl.syncStatusOnly(cfg, newForgetError(err))
	}

	// Find all MachineConfigPools
	mcpPools, err := ctrl.getPoolsForKubeletConfig(cfg)
	if err != nil {
		return ctrl.syncStatusOnly(cfg, err)
	}

	if len(mcpPools) == 0 {
		err := fmt.Errorf("KubeletConfig %v does not match any MachineConfigPools", key)
		klog.V(2).Infof("%v", err)
		return ctrl.syncStatusOnly(cfg, err)
	}

	// Fetch the NodeConfig
	nodeConfig, err := ctrl.nodeConfigLister.Get(ctrlcommon.ClusterNodeInstanceName)
	if macherrors.IsNotFound(err) {
		nodeConfig = createNewDefaultNodeconfig()
	}

	// Grab APIServer to populate TLS settings in the default kubelet config
	apiServer, err := ctrl.apiserverLister.Get(ctrlcommon.APIServerInstanceName)
	if err != nil && !macherrors.IsNotFound(err) {
		return ctrl.syncStatusOnly(cfg, err, "could not get the TLSSecurityProfile from %v: %v", ctrlcommon.APIServerInstanceName, err)
	}

	for _, pool := range mcpPools {
		if pool.Spec.Configuration.Name == "" {
			updateDelay := 5 * time.Second
			// Previously we spammed the logs about empty pools.
			// Let's just pause for a bit here to let the renderer
			// initialize them.
			time.Sleep(updateDelay)
			return fmt.Errorf("Pool %s is unconfigured, pausing %v for renderer to initialize", pool.Name, updateDelay)
		}
		role := pool.Name
		// Get MachineConfig
		managedKey, err := getManagedKubeletConfigKey(pool, ctrl.client, cfg)
		if err != nil {
			return ctrl.syncStatusOnly(cfg, err, "could not get kubelet config key: %v", err)
		}
		mc, err := ctrl.client.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), managedKey, metav1.GetOptions{})
		if err != nil && !macherrors.IsNotFound(err) {
			return ctrl.syncStatusOnly(cfg, err, "could not find MachineConfig: %v", managedKey)
		}
		isNotFound := macherrors.IsNotFound(err)

		// Generate the original KubeletConfig
		cc, err := ctrl.ccLister.Get(ctrlcommon.ControllerConfigName)
		if err != nil {
			return fmt.Errorf("could not get ControllerConfig %w", err)
		}

		originalKubeConfig, err := generateOriginalKubeletConfigWithFeatureGates(cc, ctrl.templatesDir, role, ctrl.featureGateAccess, apiServer)
		if err != nil {
			return ctrl.syncStatusOnly(cfg, err, "could not get original kubelet config: %v", err)
		}
		// updating the originalKubeConfig based on the nodeConfig on a worker node
		if role == ctrlcommon.MachineConfigPoolWorker {
			updateOriginalKubeConfigwithNodeConfig(nodeConfig, originalKubeConfig)
		}

		// If the provided kubeletconfig has a TLS profile, override the one generated from templates.
		if cfg.Spec.TLSSecurityProfile != nil {
			klog.Infof("Using tlsSecurityProfile provided by KubeletConfig %s", cfg.Name)
			observedMinTLSVersion, observedCipherSuites := ctrlcommon.GetSecurityProfileCiphers(cfg.Spec.TLSSecurityProfile)
			originalKubeConfig.TLSMinVersion = observedMinTLSVersion
			originalKubeConfig.TLSCipherSuites = observedCipherSuites
		}

		kubeletIgnition, logLevelIgnition, autoSizingReservedIgnition, err := generateKubeletIgnFiles(cfg, originalKubeConfig)
		if err != nil {
			return ctrl.syncStatusOnly(cfg, err)
		}

		if isNotFound {
			ignConfig := ctrlcommon.NewIgnConfig()
			mc, err = ctrlcommon.MachineConfigFromIgnConfig(role, managedKey, ignConfig)
			if err != nil {
				return ctrl.syncStatusOnly(cfg, err, "could not create MachineConfig from new Ignition config: %v", err)
			}
			mc.ObjectMeta.UID = uuid.NewUUID()
		}
		_, ok := cfg.GetAnnotations()[ctrlcommon.MCNameSuffixAnnotationKey]
		arr := strings.Split(managedKey, "-")
		// the first managed key value 99-poolname-generated-kubelet does not have a suffix
		// set "" as suffix annotation to the kubelet config object
		if _, err := strconv.Atoi(arr[len(arr)-1]); err != nil && !ok {
			if err := ctrl.addAnnotation(cfg, ctrlcommon.MCNameSuffixAnnotationKey, ""); err != nil {
				return ctrl.syncStatusOnly(cfg, err, "could not update annotation for kubeletConfig")
			}
		}
		// If the MC name suffix annotation does not exist and the managed key value returned has a suffix, then add the MC name
		// suffix annotation and suffix value to the kubelet config object
		if len(arr) > 4 && !ok {
			_, err := strconv.Atoi(arr[len(arr)-1])
			if err == nil {
				if err := ctrl.addAnnotation(cfg, ctrlcommon.MCNameSuffixAnnotationKey, arr[len(arr)-1]); err != nil {
					return ctrl.syncStatusOnly(cfg, err, "could not update annotation for kubeletConfig")
				}
			}
		}

		tempIgnConfig := ctrlcommon.NewIgnConfig()
		if autoSizingReservedIgnition != nil {
			tempIgnConfig.Storage.Files = append(tempIgnConfig.Storage.Files, *autoSizingReservedIgnition)
		}
		if logLevelIgnition != nil {
			tempIgnConfig.Storage.Files = append(tempIgnConfig.Storage.Files, *logLevelIgnition)
		}
		if kubeletIgnition != nil {
			tempIgnConfig.Storage.Files = append(tempIgnConfig.Storage.Files, *kubeletIgnition)
		}

		rawIgn, err := json.Marshal(tempIgnConfig)
		if err != nil {
			return ctrl.syncStatusOnly(cfg, err, "could not marshal kubelet config Ignition: %v", err)
		}
		mc.Spec.Config.Raw = rawIgn

		mc.SetAnnotations(map[string]string{
			ctrlcommon.GeneratedByControllerVersionAnnotationKey: version.Hash,
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
		// Add Finalizers to the KubletConfig
		if err := ctrl.addFinalizerToKubeletConfig(cfg, mc); err != nil {
			return ctrl.syncStatusOnly(cfg, err, "could not add finalizers to KubeletConfig: %v", err)
		}
		klog.Infof("Applied KubeletConfig %v on MachineConfigPool %v", key, pool.Name)
		ctrlcommon.UpdateStateMetric(ctrlcommon.MCCSubControllerState, "machine-config-controller-kubelet-config", "Sync Kubelet Config", pool.Name)
	}
	if err := ctrl.cleanUpDuplicatedMC(managedKubeletConfigKeyPrefix); err != nil {
		return err
	}
	return ctrl.syncStatusOnly(cfg, nil)
}

// cleanUpDuplicatedMC removes the MC of non-updated GeneratedByControllerVersionKey if its name contains 'generated-kubelet'.
// BZ 1955517: upgrade when there are more than one configs, the duplicated and upgraded MC will be generated (func getManagedKubeletConfigKey())
// MC with old GeneratedByControllerVersionKey fails the upgrade.
// This can also clean up unmanaged machineconfigs that their correcponding pool is removed.
func (ctrl *Controller) cleanUpDuplicatedMC(prefix string) error {
	generatedKubeletCfg := "generated-kubelet"
	// Get all machine configs
	mcList, err := ctrl.client.MachineconfigurationV1().MachineConfigs().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("error listing kubelet machine configs: %w", err)
	}
	for _, mc := range mcList.Items {
		if !strings.Contains(mc.Name, generatedKubeletCfg) {
			continue
		}
		if !strings.HasPrefix(mc.Name, prefix) {
			continue
		}
		// delete the mc if its degraded
		if mc.Annotations[ctrlcommon.GeneratedByControllerVersionAnnotationKey] != version.Hash {
			if err := ctrl.client.MachineconfigurationV1().MachineConfigs().Delete(context.TODO(), mc.Name, metav1.DeleteOptions{}); err != nil && !macherrors.IsNotFound(err) {
				return fmt.Errorf("error deleting degraded kubelet machine config %s: %w", mc.Name, err)
			}
		}
	}
	return nil
}

func (ctrl *Controller) popFinalizerFromKubeletConfig(kc *mcfgv1.KubeletConfig) error {
	return retry.RetryOnConflict(updateBackoff, func() error {
		newcfg, err := ctrl.mckLister.Get(kc.Name)
		if macherrors.IsNotFound(err) {
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
		kcTmp.Finalizers = append([]string{}, kc.Finalizers[1:]...)
		modJSON, err := json.Marshal(kcTmp)
		if err != nil {
			return err
		}

		patch, err := jsonmergepatch.CreateThreeWayJSONMergePatch(curJSON, modJSON, curJSON)
		if err != nil {
			return err
		}
		return ctrl.patchKubeletConfigs(newcfg.Name, patch)
	})
}

func (ctrl *Controller) patchKubeletConfigs(name string, patch []byte) error {
	_, err := ctrl.client.MachineconfigurationV1().KubeletConfigs().Patch(context.TODO(), name, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

func (ctrl *Controller) addFinalizerToKubeletConfig(kc *mcfgv1.KubeletConfig, mc *mcfgv1.MachineConfig) error {
	return retry.RetryOnConflict(updateBackoff, func() error {
		newcfg, err := ctrl.mckLister.Get(kc.Name)
		if macherrors.IsNotFound(err) {
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
		// We want to use the mc name as the finalizer instead of the uid because
		// every time a resync happens, a new uid is generated. This is why the list
		// of finalizers had multiple entries. So check if the list of finalizers consists
		// of uids, if it does then clear the list of finalizers and we will add the mc
		// name to it, ensuring we don't have duplicate or multiple finalizers.
		for _, finalizerName := range newcfg.Finalizers {
			if !strings.Contains(finalizerName, "kubelet") {
				kcTmp.ObjectMeta.SetFinalizers([]string{})
			}
		}
		// Only append the mc name if it is not already in the list of finalizers.
		// When we update an existing kubeletconfig, the generation number increases causing
		// a resync to happen. When this happens, the mc name is the same, so we don't
		// want to add duplicate entries to the list of finalizers.
		if !ctrlcommon.InSlice(mc.Name, kcTmp.ObjectMeta.Finalizers) {
			kcTmp.ObjectMeta.Finalizers = append(kcTmp.ObjectMeta.Finalizers, mc.Name)
		}

		modJSON, err := json.Marshal(kcTmp)
		if err != nil {
			return err
		}
		patch, err := jsonmergepatch.CreateThreeWayJSONMergePatch(curJSON, modJSON, curJSON)
		if err != nil {
			return err
		}
		return ctrl.patchKubeletConfigs(newcfg.Name, patch)
	})
}

func (ctrl *Controller) getPoolsForKubeletConfig(config *mcfgv1.KubeletConfig) ([]*mcfgv1.MachineConfigPool, error) {
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
		return nil, errCouldNotFindMCPSet
	}

	return pools, nil
}
