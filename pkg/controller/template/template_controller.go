package template

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/scheme"
	mcfginformersv1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	mcoResourceApply "github.com/openshift/machine-config-operator/lib/resourceapply"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"k8s.io/klog/v2"

	configv1 "github.com/openshift/api/config/v1"
	configinformersv1 "github.com/openshift/client-go/config/informers/externalversions/config/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformersv1 "k8s.io/client-go/informers/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	corev1clientset "k8s.io/client-go/kubernetes/typed/core/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
)

const (
	// maxRetries is the number of times a machineconfig pool will be retried before it is dropped out of the queue.
	// With the current rate-limiter in use (5ms*2^(maxRetries-1)) the following numbers represent the times
	// a machineconfig pool is going to be requeued:
	//
	// 5ms, 10ms, 20ms, 40ms, 80ms, 160ms, 320ms, 640ms, 1.3s, 2.6s, 5.1s, 10.2s, 20.4s, 41s, 82s
	maxRetries = 15
)

// controllerKind contains the schema.GroupVersionKind for this controller type.
var controllerKind = mcfgv1.SchemeGroupVersion.WithKind("ControllerConfig")

// Controller defines the template controller
type Controller struct {
	templatesDir string

	client                  mcfgclientset.Interface
	kubeClient              clientset.Interface
	eventRecorder           record.EventRecorder
	syncHandler             func(ccKey string) error
	enqueueControllerConfig func(*mcfgv1.ControllerConfig)

	ccLister mcfglistersv1.ControllerConfigLister
	mcLister mcfglistersv1.MachineConfigLister

	apiserverLister       configlistersv1.APIServerLister
	apiserverListerSynced cache.InformerSynced

	ccListerSynced        cache.InformerSynced
	mcListerSynced        cache.InformerSynced
	secretsInformerSynced cache.InformerSynced

	queue workqueue.TypedRateLimitingInterface[string]
}

// New returns a new template controller.
func New(
	templatesDir string,
	ccInformer mcfginformersv1.ControllerConfigInformer,
	mcInformer mcfginformersv1.MachineConfigInformer,
	secretsInformer coreinformersv1.SecretInformer,
	apiserverInformer configinformersv1.APIServerInformer,
	kubeClient clientset.Interface,
	mcfgClient mcfgclientset.Interface,
) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&corev1clientset.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	ctrl := &Controller{
		templatesDir:  templatesDir,
		client:        mcfgClient,
		kubeClient:    kubeClient,
		eventRecorder: ctrlcommon.NamespacedEventRecorder(eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineconfigcontroller-templatecontroller"})),
		queue: workqueue.NewTypedRateLimitingQueueWithConfig[string](
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "machineconfigcontroller-templatecontroller"}),
	}

	ccInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addControllerConfig,
		UpdateFunc: ctrl.updateControllerConfig,
		// This will enter the sync loop and no-op, because the controllerconfig has been deleted from the store.
		DeleteFunc: ctrl.deleteControllerConfig,
	})

	mcInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addMachineConfig,
		UpdateFunc: ctrl.updateMachineConfig,
		DeleteFunc: ctrl.deleteMachineConfig,
	})

	secretsInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addSecret,
		UpdateFunc: ctrl.updateSecret,
		DeleteFunc: ctrl.deleteSecret,
	})

	apiserverInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addAPIServer,
		UpdateFunc: ctrl.updateAPIServer,
		DeleteFunc: ctrl.deleteAPIServer,
	})

	ctrl.syncHandler = ctrl.syncControllerConfig
	ctrl.enqueueControllerConfig = ctrl.enqueue

	ctrl.ccLister = ccInformer.Lister()
	ctrl.mcLister = mcInformer.Lister()
	ctrl.ccListerSynced = ccInformer.Informer().HasSynced
	ctrl.mcListerSynced = mcInformer.Informer().HasSynced
	ctrl.secretsInformerSynced = secretsInformer.Informer().HasSynced

	ctrl.apiserverLister = apiserverInformer.Lister()
	ctrl.apiserverListerSynced = apiserverInformer.Informer().HasSynced

	return ctrl
}

func (ctrl *Controller) filterSecret(secret *corev1.Secret) {
	if secret.Name == "pull-secret" {
		ctrl.enqueueController()
		klog.Infof("Re-syncing ControllerConfig due to secret %s change", secret.Name)
	}
}

func (ctrl *Controller) addSecret(obj interface{}) {
	secret := obj.(*corev1.Secret)
	if secret.DeletionTimestamp != nil {
		ctrl.deleteSecret(secret)
		return
	}
	klog.V(4).Infof("Add Secret %v", secret)
	ctrl.filterSecret(secret)
}

func (ctrl *Controller) updateSecret(_, newSecret interface{}) {
	secret := newSecret.(*corev1.Secret)
	klog.V(4).Infof("Update Secret %v", secret)
	ctrl.filterSecret(secret)
}

func (ctrl *Controller) deleteSecret(obj interface{}) {
	secret, ok := obj.(*corev1.Secret)
	klog.V(4).Infof("Delete Secret %v", secret)

	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("couldn't get object from tombstone %#v", obj))
			return
		}
		secret, ok = tombstone.Obj.(*corev1.Secret)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not a Secret %#v", obj))
			return
		}
	}

	if secret.Name == "pull-secret" {
		cfg, err := ctrl.ccLister.Get(ctrlcommon.ControllerConfigName)
		if err != nil {
			utilruntime.HandleError(fmt.Errorf("couldn't get ControllerConfig on secret callback %#w", err))
			return
		}
		klog.V(4).Infof("Re-syncing ControllerConfig %s due to secret deletion", cfg.Name)
		// TODO(runcom): should we resync w/o a secret which is going to just cause the controller to fail when trying to get the secret itself?
		// ctrl.enqueueControllerConfig(cfg)
	}
}

func (ctrl *Controller) filterAPIServer(apiServer *configv1.APIServer) {
	if apiServer.Name == "cluster" {
		ctrl.enqueueController()
		klog.Infof("Re-syncing ControllerConfig due to apiServer %s change", apiServer.Name)
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

func (ctrl *Controller) updateAPIServer(old, cur interface{}) {

	oldAPIServer := old.(*configv1.APIServer)
	newAPIServer := cur.(*configv1.APIServer)
	if !reflect.DeepEqual(oldAPIServer.Spec, newAPIServer.Spec) {
		klog.V(4).Infof("Updating APIServer: %s", newAPIServer.Name)
		ctrl.filterAPIServer(newAPIServer)
	}

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

func (ctrl *Controller) enqueueController() {
	cfg, err := ctrl.ccLister.Get(ctrlcommon.ControllerConfigName)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get ControllerConfig on dependency callback %#w", err))
		return
	}
	klog.V(4).Infof("Re-syncing ControllerConfig %s due to dependency change", cfg.Name)
	ctrl.enqueueControllerConfig(cfg)
}

func (ctrl *Controller) updateFeature(old, cur interface{}) {
	oldFeature := old.(*configv1.FeatureGate)
	newFeature := cur.(*configv1.FeatureGate)
	if !reflect.DeepEqual(oldFeature.Spec, newFeature.Spec) {
		klog.V(4).Infof("Updating Feature: %s", newFeature.Name)
		ctrl.enqueueController()
	}
}

func (ctrl *Controller) addFeature(obj interface{}) {
	features := obj.(*configv1.FeatureGate)
	klog.V(4).Infof("Adding Feature: %s", features.Name)
	ctrl.enqueueController()
}

func (ctrl *Controller) deleteFeature(obj interface{}) {
	features, ok := obj.(*configv1.FeatureGate)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("couldn't get object from tombstone %#v", obj))
			return
		}
		features, ok = tombstone.Obj.(*configv1.FeatureGate)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not a FeatureGate %#v", obj))
			return
		}
	}
	klog.V(4).Infof("Deleting Feature %s", features.Name)
	ctrl.enqueueController()
}

// Run executes the template controller
func (ctrl *Controller) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()

	if !cache.WaitForCacheSync(stopCh, ctrl.ccListerSynced, ctrl.mcListerSynced, ctrl.secretsInformerSynced) {
		return
	}

	klog.Info("Starting MachineConfigController-TemplateController")
	defer klog.Info("Shutting down MachineConfigController-TemplateController")

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.worker, time.Second, stopCh)
	}

	<-stopCh
}

func (ctrl *Controller) addControllerConfig(obj interface{}) {
	cfg := obj.(*mcfgv1.ControllerConfig)
	klog.V(4).Infof("Adding ControllerConfig %s", cfg.Name)
	ctrl.enqueueControllerConfig(cfg)
}

func (ctrl *Controller) updateControllerConfig(old, cur interface{}) {
	oldCfg := old.(*mcfgv1.ControllerConfig)
	curCfg := cur.(*mcfgv1.ControllerConfig)
	klog.V(4).Infof("Updating ControllerConfig %s", oldCfg.Name)
	ctrl.enqueueControllerConfig(curCfg)
}

func (ctrl *Controller) deleteControllerConfig(obj interface{}) {
	cfg, ok := obj.(*mcfgv1.ControllerConfig)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("couldn't get object from tombstone %#v", obj))
			return
		}
		cfg, ok = tombstone.Obj.(*mcfgv1.ControllerConfig)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not a ControllerConfig %#v", obj))
			return
		}
	}
	klog.V(4).Infof("Deleting ControllerConfig %s", cfg.Name)
	// TODO(abhinavdahiya): handle deletes.
}

func (ctrl *Controller) addMachineConfig(obj interface{}) {
	mc := obj.(*mcfgv1.MachineConfig)
	if mc.DeletionTimestamp != nil {
		ctrl.deleteMachineConfig(mc)
		return
	}

	if controllerRef := metav1.GetControllerOf(mc); controllerRef != nil {
		cfg := ctrl.resolveControllerRef(controllerRef)
		if cfg == nil {
			return
		}
		klog.V(4).Infof("MachineConfig %s added", mc.Name)
		ctrl.enqueueControllerConfig(cfg)
		return
	}

	// No adopting.
}

func (ctrl *Controller) updateMachineConfig(_, cur interface{}) {
	curMC := cur.(*mcfgv1.MachineConfig)

	if controllerRef := metav1.GetControllerOf(curMC); controllerRef != nil {
		cfg := ctrl.resolveControllerRef(controllerRef)
		if cfg == nil {
			return
		}
		klog.V(4).Infof("MachineConfig %s updated", curMC.Name)
		ctrl.enqueueControllerConfig(cfg)
		return
	}

	// No adopting.
}

func (ctrl *Controller) deleteMachineConfig(obj interface{}) {
	mc, ok := obj.(*mcfgv1.MachineConfig)

	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("couldn't get object from tombstone %#v", obj))
			return
		}
		mc, ok = tombstone.Obj.(*mcfgv1.MachineConfig)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not a MachineConfig %#v", obj))
			return
		}
	}

	controllerRef := metav1.GetControllerOf(mc)
	if controllerRef == nil {
		// No controller should care about orphans being deleted.
		return
	}
	cfg := ctrl.resolveControllerRef(controllerRef)
	if cfg == nil {
		return
	}
	klog.V(4).Infof("MachineConfig %s deleted.", mc.Name)
	ctrl.enqueueControllerConfig(cfg)
}

func (ctrl *Controller) resolveControllerRef(controllerRef *metav1.OwnerReference) *mcfgv1.ControllerConfig {
	// We can't look up by UID, so look up by Name and then verify UID.
	// Don't even try to look up by Name if it's the wrong Kind.
	if controllerRef.Kind != controllerKind.Kind {
		return nil
	}
	cfg, err := ctrl.ccLister.Get(controllerRef.Name)
	if err != nil {
		return nil
	}

	if cfg.UID != controllerRef.UID {
		// The controller we found with this Name is not the same one that the
		// ControllerRef points to.
		return nil
	}
	return cfg
}

func (ctrl *Controller) enqueue(config *mcfgv1.ControllerConfig) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(config)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", config, err))
		return
	}

	ctrl.queue.Add(key)
}

func (ctrl *Controller) enqueueRateLimited(controllerconfig *mcfgv1.ControllerConfig) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(controllerconfig)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", controllerconfig, err))
		return
	}

	ctrl.queue.AddRateLimited(key)
}

// enqueueAfter will enqueue a controllerconfig after the provided amount of time.
func (ctrl *Controller) enqueueAfter(controllerconfig *mcfgv1.ControllerConfig, after time.Duration) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(controllerconfig)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", controllerconfig, err))
		return
	}

	ctrl.queue.AddAfter(key, after)
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

	if ctrl.queue.NumRequeues(key) < maxRetries {
		klog.V(2).Infof("Error syncing controllerconfig %v: %v", key, err)
		ctrl.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	klog.V(2).Infof("Dropping controllerconfig %q out of the queue: %v", key, err)
	ctrl.queue.Forget(key)
	ctrl.queue.AddAfter(key, 1*time.Minute)
}

// updateControllerConfigCerts parses the raw cert data and places key information about the certs into the controllerconfig status
func updateControllerConfigCerts(config *mcfgv1.ControllerConfig) bool {
	modified := false
	names := []string{
		"KubeAPIServerServingCAData", "CloudProviderCAData", "RootCAData", "AdditionalTrustBundle",
	}
	certs := [][]byte{
		config.Spec.KubeAPIServerServingCAData,
		config.Spec.CloudProviderCAData,
		config.Spec.RootCAData,
		config.Spec.AdditionalTrustBundle,
	}
	newImgCerts := []mcfgv1.ControllerCertificate{}
	newCtrlCerts := []mcfgv1.ControllerCertificate{}
	for i, cert := range certs {
		certs := createNewCert(cert, names[i])
		if len(certs) > 0 {
			modified = true
			newCtrlCerts = append(newCtrlCerts, certs...)
		}
	}
	for _, entry := range config.Spec.ImageRegistryBundleData {
		names = append(names, entry.File)
		certs := createNewCert(entry.Data, entry.File)
		if len(certs) > 0 {
			modified = true
			newImgCerts = append(newImgCerts, certs...)
		}
	}
	for _, entry := range config.Spec.ImageRegistryBundleUserData {
		names = append(names, entry.File)
		certs := createNewCert(entry.Data, entry.File)
		if len(certs) > 0 {
			modified = true
			newImgCerts = append(newImgCerts, certs...)
		}
	}
	stillExists := false
	for _, cert := range config.Status.ControllerCertificates {
		// skip the non-IR certs
		if cert.BundleFile == "KubeAPIServerServingCAData" || cert.BundleFile == "CloudProviderCAData" || cert.BundleFile == "RootCAData" || cert.BundleFile == "AdditionalTrustBundle" {
			continue
		}
		for _, newC := range newImgCerts {
			if newC.BundleFile == cert.BundleFile {
				stillExists = true
				break
			}
		}
		// need to remove old cert path if it does not still exists (only applies to img certs)
		if !stillExists {
			if err := os.RemoveAll(filepath.Join("/etc/docker/certs.d", cert.BundleFile)); err != nil {
				klog.Warningf("Could not remove old certificate: %s", filepath.Join("/etc/docker/certs.d", cert.BundleFile))
			}
		}
		stillExists = false
	}
	config.Status.ControllerCertificates = append(config.Status.ControllerCertificates[:0], append(newCtrlCerts, newImgCerts...)...)
	return modified
}

func createNewCert(cert []byte, name string) []mcfgv1.ControllerCertificate {
	certs := []mcfgv1.ControllerCertificate{}
	var malformed bool

	for len(cert) > 0 && !malformed {
		b, next := pem.Decode(cert)
		if b == nil {
			klog.Infof("Unable to decode cert %s into a PEM block. Cert is either empty or invalid.", name)
			break
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			klog.Errorf("Malformed certificate '%s' detected and is not syncing. Error: %v, Cert data: %s", name, err, cert)
			malformed = true
			continue
		}
		cert = next
		certs = append(certs, mcfgv1.ControllerCertificate{
			Subject:    c.Subject.String(),
			Signer:     c.Issuer.String(),
			BundleFile: name,
			NotBefore:  &metav1.Time{Time: c.NotBefore},
			NotAfter:   &metav1.Time{Time: c.NotAfter},
		})
	}
	return certs
}

// syncControllerConfig will sync the controller config with the given key.
// This function is not meant to be invoked concurrently with the same key.
func (ctrl *Controller) syncControllerConfig(key string) error {
	startTime := time.Now()
	klog.V(4).Infof("Started syncing controllerconfig %q (%v)", key, startTime)
	defer func() {
		klog.V(4).Infof("Finished syncing controllerconfig %q (%v)", key, time.Since(startTime))
	}()

	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	controllerconfig, err := ctrl.ccLister.Get(name)
	if errors.IsNotFound(err) {
		klog.V(2).Infof("ControllerConfig %v has been deleted", key)
		return nil
	}
	if err != nil {
		return err
	}

	// Deep-copy otherwise we are mutating our cache.
	// TODO: Deep-copy only when needed.
	cfg := controllerconfig.DeepCopy()

	if cfg.GetGeneration() != cfg.Status.ObservedGeneration {
		if err := ctrl.syncRunningStatus(cfg); err != nil {
			return err
		}
	}

	modified := updateControllerConfigCerts(cfg)

	if modified {
		if err := ctrl.syncCertificateStatus(cfg); err != nil {
			return err
		}
	}

	var clusterPullSecretRaw []byte

	if cfg.Spec.PullSecret != nil {
		clusterPullSecret, err := ctrl.kubeClient.CoreV1().Secrets(cfg.Spec.PullSecret.Namespace).Get(context.TODO(), cfg.Spec.PullSecret.Name, metav1.GetOptions{})
		if err != nil {
			return ctrl.syncFailingStatus(cfg, err)
		}
		if clusterPullSecret.Type != corev1.SecretTypeDockerConfigJson {
			return ctrl.syncFailingStatus(cfg, fmt.Errorf("expected secret type %s found %s", corev1.SecretTypeDockerConfigJson, clusterPullSecret.Type))
		}
		clusterPullSecretRaw = clusterPullSecret.Data[corev1.DockerConfigJsonKey]
	}

	// Grab the tlsSecurityProfile from the apiserver object
	apiServer, err := ctrl.apiserverLister.Get(ctrlcommon.APIServerInstanceName)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.syncFailingStatus(cfg, err)
	}

	mcs, err := getMachineConfigsForControllerConfig(ctrl.templatesDir, cfg, clusterPullSecretRaw, apiServer)
	if err != nil {
		return ctrl.syncFailingStatus(cfg, err)
	}

	for _, mc := range mcs {
		_, updated, err := mcoResourceApply.ApplyMachineConfig(ctrl.client.MachineconfigurationV1(), mc)
		if err != nil {
			return ctrl.syncFailingStatus(cfg, err)
		}
		if updated {
			klog.V(4).Infof("Machineconfig %s was updated", mc.Name)
		}
		ctrlcommon.UpdateStateMetric(ctrlcommon.MCCSubControllerState, "machine-config-controller-template", "Sync Controller Config", mc.Name)
	}

	return ctrl.syncCompletedStatus(cfg)
}

func getMachineConfigsForControllerConfig(templatesDir string, config *mcfgv1.ControllerConfig, clusterPullSecretRaw []byte, apiServer *configv1.APIServer) ([]*mcfgv1.MachineConfig, error) {
	buf := &bytes.Buffer{}
	if err := json.Compact(buf, clusterPullSecretRaw); err != nil {
		return nil, fmt.Errorf("couldn't compact pullsecret %q: %w", string(clusterPullSecretRaw), err)
	}
	tlsMinVersion, tlsCipherSuites := ctrlcommon.GetSecurityProfileCiphersFromAPIServer(apiServer)
	rc := &RenderConfig{
		ControllerConfigSpec: &config.Spec,
		PullSecret:           string(buf.Bytes()),
		TLSMinVersion:        tlsMinVersion,
		TLSCipherSuites:      tlsCipherSuites,
	}
	mcs, err := generateTemplateMachineConfigs(rc, templatesDir)
	if err != nil {
		return nil, err
	}

	for _, mc := range mcs {
		oref := metav1.NewControllerRef(config, controllerKind)
		mc.SetOwnerReferences([]metav1.OwnerReference{*oref})
	}

	sort.Slice(mcs, func(i, j int) bool { return mcs[i].Name < mcs[j].Name })
	return mcs, nil
}

// RunBootstrap runs the tempate controller in boostrap mode.
func RunBootstrap(templatesDir string, config *mcfgv1.ControllerConfig, pullSecretRaw []byte, apiServer *configv1.APIServer) ([]*mcfgv1.MachineConfig, error) {
	return getMachineConfigsForControllerConfig(templatesDir, config, pullSecretRaw, apiServer)
}
