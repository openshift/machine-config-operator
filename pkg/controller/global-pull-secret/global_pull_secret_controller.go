package globalpullsecret

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformers "k8s.io/client-go/informers/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/scheme"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

const (
	maxRetries    = 15
	syncKey       = "global-pull-secret" // single key; controller has one reconcile target
	controllerTag = "machineconfigcontroller-globalpullsecretcontroller"

	// MultiSecretGlobalPullConfig is the feature gate name for this controller.
	// Until the corresponding openshift/api PR defines this gate, it will be
	// unknown to FeatureGatesHandler and Enabled() returns false — keeping
	// the controller inactive.
	MultiSecretGlobalPullConfig configv1.FeatureGateName = "MultiSecretGlobalPullConfig"
)

// Controller reconciles modular pull Secrets in openshift-config into
// the cluster-wide pull-secret.
type Controller struct {
	kubeClient         clientset.Interface
	eventRecorder      record.EventRecorder
	featureGateHandler ctrlcommon.FeatureGatesHandler

	secretLister       corelisters.SecretNamespaceLister
	secretListerSynced cache.InformerSynced

	queue workqueue.TypedRateLimitingInterface[string]

	// baseline holds the original pull-secret .dockerconfigjson bytes captured
	// at first controller activation. It is never refreshed from the live
	// pull-secret after initial capture to avoid re-reading merged output.
	baseline []byte
}

// New creates a global pull secret controller.
// secretInformer must be scoped to the openshift-config namespace.
func New(
	secretInformer coreinformers.SecretInformer,
	kubeClient clientset.Interface,
	featureGateHandler ctrlcommon.FeatureGatesHandler,
) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&corev1client.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	ctrl := &Controller{
		kubeClient:         kubeClient,
		featureGateHandler: featureGateHandler,
		eventRecorder:      ctrlcommon.NamespacedEventRecorder(eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: controllerTag})),
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: controllerTag},
		),
	}

	secretInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addSecret,
		UpdateFunc: ctrl.updateSecret,
		DeleteFunc: ctrl.deleteSecret,
	})

	ctrl.secretLister = secretInformer.Lister().Secrets(ctrlcommon.OpenshiftConfigNamespace)
	ctrl.secretListerSynced = secretInformer.Informer().HasSynced

	return ctrl
}

// Run starts the controller workers. If the feature gate is not enabled,
// the controller exits immediately without starting any workers.
func (ctrl *Controller) Run(ctx context.Context, workers int) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()

	if !ctrl.featureGateHandler.Enabled(MultiSecretGlobalPullConfig) {
		klog.Infof("GlobalPullSecretController: feature gate %s not enabled, skipping", MultiSecretGlobalPullConfig)
		return
	}

	if !cache.WaitForCacheSync(ctx.Done(), ctrl.secretListerSynced) {
		return
	}

	klog.Info("Starting MachineConfigController-GlobalPullSecretController")
	defer klog.Info("Shutting down MachineConfigController-GlobalPullSecretController")

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.worker, time.Second, ctx.Done())
	}

	<-ctx.Done()
}

// enqueue adds the single reconcile key to the work queue.
func (ctrl *Controller) enqueue() {
	ctrl.queue.Add(syncKey)
}

// isRelevantSecret returns true for pull-secret or labeled modular Secrets.
func isRelevantSecret(s *corev1.Secret) bool {
	if s.Namespace != ctrlcommon.OpenshiftConfigNamespace {
		return false
	}
	if s.Name == ctrlcommon.GlobalPullSecretName {
		return true
	}
	return s.Labels[GlobalPullSecretLabel] == "true"
}

func (ctrl *Controller) addSecret(obj interface{}) {
	secret := obj.(*corev1.Secret)
	if isRelevantSecret(secret) {
		klog.V(4).Infof("GlobalPullSecret: Secret %s/%s added", secret.Namespace, secret.Name)
		ctrl.enqueue()
	}
}

func (ctrl *Controller) updateSecret(old, cur interface{}) {
	oldSecret := old.(*corev1.Secret)
	curSecret := cur.(*corev1.Secret)
	if isRelevantSecret(oldSecret) || isRelevantSecret(curSecret) {
		klog.V(4).Infof("GlobalPullSecret: Secret %s/%s updated", curSecret.Namespace, curSecret.Name)
		ctrl.enqueue()
	}
}

func (ctrl *Controller) deleteSecret(obj interface{}) {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("failed to get object from tombstone %#v", obj))
			return
		}
		secret, ok = tombstone.Obj.(*corev1.Secret)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not a Secret %#v", obj))
			return
		}
	}
	if isRelevantSecret(secret) {
		klog.V(4).Infof("GlobalPullSecret: Secret %s/%s deleted", secret.Namespace, secret.Name)
		ctrl.enqueue()
	}
}

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

	err := ctrl.sync()
	ctrl.handleErr(err, key)
	return true
}

func (ctrl *Controller) handleErr(err error, key string) {
	if err == nil {
		ctrl.queue.Forget(key)
		return
	}
	if ctrl.queue.NumRequeues(key) < maxRetries {
		klog.V(2).Infof("Error syncing global pull secret: %v", err)
		ctrl.queue.AddRateLimited(key)
		return
	}
	utilruntime.HandleError(err)
	klog.V(2).Infof("Dropping global pull secret sync out of the queue: %v", err)
	ctrl.queue.Forget(key)
	ctrl.queue.AddAfter(key, 1*time.Minute)
}

// sync is the main reconcile loop.
func (ctrl *Controller) sync() error {
	startTime := time.Now()
	klog.V(4).Infof("Started syncing global pull secret (%v)", startTime)
	defer func() {
		klog.V(4).Infof("Finished syncing global pull secret (%v)", time.Since(startTime))
	}()

	// Get the current pull-secret.
	pullSecret, err := ctrl.secretLister.Get(ctrlcommon.GlobalPullSecretName)
	if errors.IsNotFound(err) {
		klog.Warningf("pull-secret not found in %s, skipping", ctrlcommon.OpenshiftConfigNamespace)
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting pull-secret: %w", err)
	}

	// Capture baseline once on first reconcile.
	if ctrl.baseline == nil {
		raw, ok := pullSecret.Data[corev1.DockerConfigJsonKey]
		if !ok {
			return fmt.Errorf("pull-secret missing %s key", corev1.DockerConfigJsonKey)
		}
		ctrl.baseline = make([]byte, len(raw))
		copy(ctrl.baseline, raw)
		klog.Infof("GlobalPullSecret: captured baseline pull-secret (%d bytes)", len(ctrl.baseline))
	}

	// List all labeled modular Secrets.
	labelSelector := labels.SelectorFromSet(labels.Set{GlobalPullSecretLabel: "true"})
	allSecrets, err := ctrl.secretLister.List(labelSelector)
	if err != nil {
		return fmt.Errorf("listing labeled secrets: %w", err)
	}

	// Merge.
	result, err := MergeGlobalPullSecret(ctrl.baseline, allSecrets)
	if err != nil {
		return fmt.Errorf("merging global pull secret: %w", err)
	}

	// Log any invalid modular Secrets.
	for _, v := range result.InvalidSources {
		klog.Warningf("GlobalPullSecret: skipping invalid secret: %s", v.Error())
	}

	// Diff-before-write: only update if the merged output differs from current.
	currentRaw := pullSecret.Data[corev1.DockerConfigJsonKey]
	if bytes.Equal(normalizeJSON(currentRaw), normalizeJSON(result.MergedJSON)) {
		klog.V(4).Info("GlobalPullSecret: no change in merged pull-secret, skipping write")
		return nil
	}

	// Write merged result back to pull-secret.
	updated := pullSecret.DeepCopy()
	updated.Data[corev1.DockerConfigJsonKey] = result.MergedJSON
	_, err = ctrl.kubeClient.CoreV1().Secrets(ctrlcommon.OpenshiftConfigNamespace).Update(
		context.TODO(), updated, metav1.UpdateOptions{},
	)
	if err != nil {
		return fmt.Errorf("updating pull-secret: %w", err)
	}

	klog.Infof("GlobalPullSecret: pull-secret updated with %d modular sources (%d invalid skipped)",
		len(allSecrets)-len(result.InvalidSources), len(result.InvalidSources))
	return nil
}

// normalizeJSON re-marshals JSON to remove formatting differences.
func normalizeJSON(data []byte) []byte {
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return data
	}
	out, err := json.Marshal(v)
	if err != nil {
		return data
	}
	return out
}
