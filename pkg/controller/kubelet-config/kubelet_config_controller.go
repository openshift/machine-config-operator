package kubeletconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
	"github.com/golang/glog"
	"github.com/imdario/mergo"
	"github.com/vincent-petithory/dataurl"

	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/util/yaml"
	clientset "k8s.io/client-go/kubernetes"
	coreclientsetv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"
	kubeletconfigscheme "k8s.io/kubernetes/pkg/kubelet/apis/kubeletconfig/scheme"
	kubeletconfigv1beta1 "k8s.io/kubernetes/pkg/kubelet/apis/kubeletconfig/v1beta1"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	mtmpl "github.com/openshift/machine-config-operator/pkg/controller/template"
	mcfgclientset "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/scheme"
	mcfginformersv1 "github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions/machineconfiguration.openshift.io/v1"
	mcfglistersv1 "github.com/openshift/machine-config-operator/pkg/generated/listers/machineconfiguration.openshift.io/v1"
)

const (
	// maxRetries is the number of times a machineconfig pool will be retried before it is dropped out of the queue.
	// With the current rate-limiter in use (5ms*2^(maxRetries-1)) the following numbers represent the times
	// a machineconfig pool is going to be requeued:
	//
	// 5ms, 10ms, 20ms, 40ms, 80ms, 160ms, 320ms, 640ms, 1.3s, 2.6s, 5.1s, 10.2s, 20.4s, 41s, 82s
	maxRetries = 15
)

var updateBackoff = wait.Backoff{
	Steps:    5,
	Duration: 100 * time.Millisecond,
	Jitter:   1.0,
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

	queue workqueue.RateLimitingInterface
}

// New returns a new kubelet config controller
func New(
	templatesDir string,
	mcpInformer mcfginformersv1.MachineConfigPoolInformer,
	ccInformer mcfginformersv1.ControllerConfigInformer,
	mkuInformer mcfginformersv1.KubeletConfigInformer,
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
	}

	mkuInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addKubeletConfig,
		UpdateFunc: ctrl.updateKubeletConfig,
		DeleteFunc: ctrl.deleteKubeletConfig,
	})

	ctrl.syncHandler = ctrl.syncKubeletConfig
	ctrl.enqueueKubeletConfig = ctrl.enqueue

	ctrl.mcpLister = mcpInformer.Lister()
	ctrl.mcpListerSynced = mcpInformer.Informer().HasSynced

	ctrl.ccLister = ccInformer.Lister()
	ctrl.ccListerSynced = ccInformer.Informer().HasSynced

	ctrl.mckLister = mkuInformer.Lister()
	ctrl.mckListerSynced = mkuInformer.Informer().HasSynced

	return ctrl
}

// Run executes the kubelet config controller.
func (ctrl *Controller) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()

	glog.Info("Starting MachineConfigController-KubeletConfigController")
	defer glog.Info("Shutting down MachineConfigController-KubeletConfigController")

	if !cache.WaitForCacheSync(stopCh, ctrl.mcpListerSynced, ctrl.mckListerSynced, ctrl.ccListerSynced) {
		return
	}

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.worker, time.Second, stopCh)
	}

	<-stopCh
}

func (ctrl *Controller) updateKubeletConfig(oldObj interface{}, newObj interface{}) {
	oldKC := oldObj.(*mcfgv1.KubeletConfig)
	newKC := newObj.(*mcfgv1.KubeletConfig)
	if oldKC != newKC {
		glog.V(4).Infof("Update KubeletConfig %s", oldKC.Name)
		ctrl.enqueueKubeletConfig(newKC)
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
			utilruntime.HandleError(fmt.Errorf("Couldn't get object from tombstone %#v", obj))
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
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", cfg, err))
		return
	}
	ctrl.queue.Add(key)
}

func (ctrl *Controller) enqueueRateLimited(cfg *mcfgv1.KubeletConfig) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(cfg)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", cfg, err))
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

func createNewKubeletIgnition(ymlconfig []byte) ignv2_2types.Config {
	var tempIgnConfig ignv2_2types.Config
	mode := 0644
	du := dataurl.New(ymlconfig, "text/plain")
	du.Encoding = dataurl.EncodingASCII
	tempFile := ignv2_2types.File{
		Node: ignv2_2types.Node{
			Filesystem: "root",
			Path:       "/etc/kubernetes/kubelet.conf",
		},
		FileEmbedded1: ignv2_2types.FileEmbedded1{
			Mode: &mode,
			Contents: ignv2_2types.FileContents{
				Source: du.String(),
			},
		},
	}
	tempIgnConfig.Storage.Files = append(tempIgnConfig.Storage.Files, tempFile)
	return tempIgnConfig
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

func findKubeletConfig(mc *mcfgv1.MachineConfig) (*ignv2_2types.File, error) {
	for _, c := range mc.Spec.Config.Storage.Files {
		if c.Path == "/etc/kubernetes/kubelet.conf" {
			return &c, nil
		}
	}
	return nil, fmt.Errorf("Could not find Kubelet Config")
}

func (ctrl *Controller) generateOriginalKubeletConfig(role string) (*ignv2_2types.File, error) {
	// Enumerate the controller config
	cc, err := ctrl.ccLister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("Could not enumerate ControllerConfig %s", err)
	}
	if len(cc) == 0 {
		return nil, fmt.Errorf("ControllerConfigList is empty")
	}
	// Render the default templates
	tmplPath := filepath.Join(ctrl.templatesDir, role)
	rc := &mtmpl.RenderConfig{ControllerConfigSpec: &cc[0].Spec}
	generatedConfigs, err := mtmpl.GenerateMachineConfigsForRole(rc, role, tmplPath)
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
	return nil, fmt.Errorf("Could not generate old kubelet config")
}

func getManagedKey(pool *mcfgv1.MachineConfigPool, config *mcfgv1.KubeletConfig) string {
	return fmt.Sprintf("99-%s-%s-kubelet", pool.Name, pool.ObjectMeta.UID)
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

	// Check for Deleted KubeletConfig and optionally delete finalizers
	if cfg.DeletionTimestamp != nil {
		if len(cfg.GetFinalizers()) > 0 {
			return ctrl.cascadeDelete(cfg)
		}
		return nil
	}

	// Find all MachineConfigPools
	mcpPools, err := ctrl.getPoolsForKubeletConfig(cfg)
	if err != nil {
		return err
	}

	if len(mcpPools) == 0 {
		glog.V(2).Infof("KubeletConfig %v does not match any MachineConfigPools", key)
		return nil
	}

	for _, pool := range mcpPools {
		role := pool.Name
		// Get MachineConfig
		managedKey := getManagedKey(pool, cfg)
		mc, err := ctrl.client.Machineconfiguration().MachineConfigs().Get(managedKey, metav1.GetOptions{})
		if err != nil && !errors.IsNotFound(err) {
			return err
		}
		isNotFound := errors.IsNotFound(err)
		// If the managed MachineConfig exists then try the next pool. This
		// prevents an infinite recursion of recreating MachineConfigs.
		if err == nil && !isNotFound && mc != nil {
			continue
		}
		// Generate the original KubeletConfig
		originalKubeletIgn, err := ctrl.generateOriginalKubeletConfig(role)
		if err != nil {
			return err
		}
		dataURL, err := dataurl.DecodeString(originalKubeletIgn.Contents.Source)
		if err != nil {
			return err
		}
		originalKubeConfig, err := decodeKubeletConfig(dataURL.Data)
		if err != nil {
			return err
		}
		// Merge the Old and New
		err = mergo.Merge(originalKubeConfig, cfg.Spec.KubeletConfig, mergo.WithOverride)
		if err != nil {
			return err
		}
		// Encode the new config into YAML
		cfgYAML, err := encodeKubeletConfig(originalKubeConfig, kubeletconfigv1beta1.SchemeGroupVersion)
		if err != nil {
			return err
		}
		if isNotFound {
			mc = mtmpl.MachineConfigFromIgnConfig(role, managedKey, &ignv2_2types.Config{})
		}
		mc.Spec.Config = createNewKubeletIgnition(cfgYAML)
		mc.ObjectMeta.OwnerReferences = []metav1.OwnerReference{
			metav1.OwnerReference{
				APIVersion: mcfgv1.SchemeGroupVersion.String(),
				Kind:       "KubeletConfig",
				Name:       cfg.Name,
				UID:        cfg.UID,
			},
		}
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
			return err
		}
		// Add Finalizers to the KubletConfig
		if err := ctrl.addFinalizerToKubeletConfig(cfg, mc); err != nil {
			return err
		}
		glog.Infof("Applied KubeletConfig %v on MachineConfigPool %v", key, pool.Name)
	}

	return nil
}

func (ctrl *Controller) popFinalizerFromKubeletConfig(kc *mcfgv1.KubeletConfig) error {
	curJSON, err := json.Marshal(kc)
	if err != nil {
		return err
	}

	kcTmp := kc.DeepCopy()
	kcTmp.Finalizers = append(kc.Finalizers[:0], kc.Finalizers[1:]...)

	modJSON, err := json.Marshal(kcTmp)
	if err != nil {
		return err
	}

	patch, err := jsonmergepatch.CreateThreeWayJSONMergePatch(curJSON, modJSON, curJSON)
	if err != nil {
		return err
	}

	return retry.RetryOnConflict(updateBackoff, func() error {
		_, err = ctrl.client.Machineconfiguration().KubeletConfigs().Patch(kc.Name, types.MergePatchType, patch)
		return err
	})
}

func (ctrl *Controller) addFinalizerToKubeletConfig(kc *mcfgv1.KubeletConfig, mc *mcfgv1.MachineConfig) error {
	curJSON, err := json.Marshal(kc)
	if err != nil {
		return err
	}

	kcTmp := kc.DeepCopy()
	kcTmp.Finalizers = append(kcTmp.Finalizers, mc.Name)

	modJSON, err := json.Marshal(kcTmp)
	if err != nil {
		return err
	}

	patch, err := jsonmergepatch.CreateThreeWayJSONMergePatch(curJSON, modJSON, curJSON)
	if err != nil {
		return err
	}

	return retry.RetryOnConflict(updateBackoff, func() error {
		_, err := ctrl.client.Machineconfiguration().KubeletConfigs().Patch(kc.Name, types.MergePatchType, patch)
		return err
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

func decodeKubeletConfig(data []byte) (*kubeletconfigv1beta1.KubeletConfiguration, error) {
	config := &kubeletconfigv1beta1.KubeletConfiguration{}
	d := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), len(data))
	if err := d.Decode(config); err != nil {
		return nil, err
	}
	return config, nil
}

func encodeKubeletConfig(internal *kubeletconfigv1beta1.KubeletConfiguration, targetVersion schema.GroupVersion) ([]byte, error) {
	encoder, err := newKubeletconfigYAMLEncoder(targetVersion)
	if err != nil {
		return nil, err
	}
	data, err := runtime.Encode(encoder, internal)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func newKubeletconfigYAMLEncoder(targetVersion schema.GroupVersion) (runtime.Encoder, error) {
	_, codecs, err := kubeletconfigscheme.NewSchemeAndCodecs()
	if err != nil {
		return nil, err
	}
	mediaType := "application/yaml"
	info, ok := runtime.SerializerInfoForMediaType(codecs.SupportedMediaTypes(), mediaType)
	if !ok {
		return nil, fmt.Errorf("unsupported media type %q", mediaType)
	}
	return codecs.EncoderForVersion(info.Serializer, targetVersion), nil
}
