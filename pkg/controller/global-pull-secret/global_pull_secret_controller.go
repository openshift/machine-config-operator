package globalpullsecret

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	applyconfigcorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	coreinformers "k8s.io/client-go/informers/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

const (
	maxRetries     = 15
	syncKey        = "global-pull-secret"
	controllerName = "machineconfigcontroller-globalpullsecretcontroller"
	fieldManager   = "global-pull-credential-controller"

	managedByLabel = "machineconfiguration.openshift.io/managed-by"
	managedByValue = "global-pull-credential-controller"

	appliedCondition         = "Applied"
	mergedReason             = "Merged"
	baselinePrecedenceReason = "BaselinePrecedence"
	reconcileErrorReason     = "ReconcileError"
)

var globalPullCredentialGVR = schema.GroupVersionResource{
	Group:    "config.openshift.io",
	Version:  "v1alpha1",
	Resource: "globalpullcredentials",
}

// Controller merges GlobalPullCredential resources into the controller-owned
// openshift-config/pull-secret-merged Secret. The baseline pull-secret is
// never modified.
type Controller struct {
	kubeClient         clientset.Interface
	dynamicClient      dynamic.Interface
	featureGateHandler ctrlcommon.FeatureGatesHandler

	secretLister       corelisters.SecretNamespaceLister
	secretListerSynced cache.InformerSynced

	credentialLister       cache.GenericLister
	credentialListerSynced cache.InformerSynced
	dynamicFactory         dynamicinformer.DynamicSharedInformerFactory

	queue workqueue.TypedRateLimitingInterface[string]
}

// New creates a global pull secret controller. secretInformer must be scoped
// to the openshift-config namespace.
func New(
	secretInformer coreinformers.SecretInformer,
	dynamicClient dynamic.Interface,
	kubeClient clientset.Interface,
	featureGateHandler ctrlcommon.FeatureGatesHandler,
) *Controller {
	ctrl := &Controller{
		kubeClient:         kubeClient,
		dynamicClient:      dynamicClient,
		featureGateHandler: featureGateHandler,
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: controllerName},
		),
	}

	secretInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addSecret,
		UpdateFunc: ctrl.updateSecret,
		DeleteFunc: ctrl.deleteSecret,
	})
	ctrl.secretLister = secretInformer.Lister().Secrets(ctrlcommon.OpenshiftConfigNamespace)
	ctrl.secretListerSynced = secretInformer.Informer().HasSynced

	ctrl.dynamicFactory = dynamicinformer.NewDynamicSharedInformerFactory(dynamicClient, 0)
	credentialInformer := ctrl.dynamicFactory.ForResource(globalPullCredentialGVR)
	credentialInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addCredential,
		UpdateFunc: ctrl.updateCredential,
		DeleteFunc: ctrl.deleteCredential,
	})
	ctrl.credentialLister = credentialInformer.Lister()
	ctrl.credentialListerSynced = credentialInformer.Informer().HasSynced

	return ctrl
}

// Run starts the controller workers. It returns immediately when the feature
// gate is disabled.
func (ctrl *Controller) Run(ctx context.Context, workers int) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()

	if !ctrl.featureGateHandler.Enabled(ctrlcommon.MultiSecretGlobalPullConfig) {
		klog.Infof("%s: feature gate %s disabled, not starting", controllerName, ctrlcommon.MultiSecretGlobalPullConfig)
		return
	}

	ctrl.dynamicFactory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), ctrl.secretListerSynced, ctrl.credentialListerSynced) {
		return
	}

	klog.Infof("Starting %s", controllerName)
	defer klog.Infof("Shutting down %s", controllerName)

	for i := 0; i < workers; i++ {
		go wait.Until(func() { ctrl.worker(ctx) }, time.Second, ctx.Done())
	}

	<-ctx.Done()
}

func (ctrl *Controller) addSecret(obj interface{}) {
	ctrl.enqueueIfSecretChanged(obj.(*corev1.Secret).Name)
}

func (ctrl *Controller) updateSecret(_, cur interface{}) {
	ctrl.enqueueIfSecretChanged(cur.(*corev1.Secret).Name)
}

func (ctrl *Controller) deleteSecret(obj interface{}) {
	secret, ok := obj.(*corev1.Secret)
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
	ctrl.enqueueIfSecretChanged(secret.Name)
}

func (ctrl *Controller) enqueueIfSecretChanged(secretName string) {
	if secretName == ctrlcommon.GlobalPullSecretName || secretName == ctrlcommon.GlobalPullSecretMergedName {
		ctrl.queue.Add(syncKey)
	}
}

func (ctrl *Controller) addCredential(interface{}) {
	ctrl.queue.Add(syncKey)
}

func (ctrl *Controller) updateCredential(oldObj, newObj interface{}) {
	oldCredential, oldOK := oldObj.(*unstructured.Unstructured)
	newCredential, newOK := newObj.(*unstructured.Unstructured)
	if oldOK && newOK && oldCredential.GetGeneration() == newCredential.GetGeneration() {
		return
	}
	ctrl.queue.Add(syncKey)
}

func (ctrl *Controller) deleteCredential(interface{}) {
	ctrl.queue.Add(syncKey)
}

func (ctrl *Controller) worker(ctx context.Context) {
	for ctrl.processNextWorkItem(ctx) {
	}
}

func (ctrl *Controller) processNextWorkItem(ctx context.Context) bool {
	key, quit := ctrl.queue.Get()
	if quit {
		return false
	}
	defer ctrl.queue.Done(key)

	ctrl.handleErr(ctrl.sync(ctx), key)
	return true
}

func (ctrl *Controller) handleErr(err error, key string) {
	if err == nil {
		ctrl.queue.Forget(key)
		return
	}
	if ctrl.queue.NumRequeues(key) < maxRetries {
		klog.V(2).Infof("Error syncing %s: %v", key, err)
		ctrl.queue.AddRateLimited(key)
		return
	}
	utilruntime.HandleError(err)
	klog.V(2).Infof("Dropping %s out of the queue: %v", key, err)
	ctrl.queue.Forget(key)
}

func (ctrl *Controller) sync(ctx context.Context) error {
	startTime := time.Now()
	klog.V(4).Infof("Started syncing %s", controllerName)
	defer func() {
		klog.V(4).Infof("Finished syncing %s (%v)", controllerName, time.Since(startTime))
	}()

	pullSecret, err := ctrl.secretLister.Get(ctrlcommon.GlobalPullSecretName)
	if apierrors.IsNotFound(err) {
		klog.Warningf("%s/%s not found, skipping", ctrlcommon.OpenshiftConfigNamespace, ctrlcommon.GlobalPullSecretName)
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting pull-secret: %w", err)
	}

	credentials, entries, err := ctrl.listCredentials()
	if err != nil {
		statusErr := ctrl.setApplied(ctx, credentials, errorStatuses(credentials, err.Error()))
		return errors.Join(err, statusErr)
	}

	result, err := MergeCredentials(pullSecret.Data[corev1.DockerConfigJsonKey], entries)
	if err != nil {
		mergeErr := fmt.Errorf("merging global pull credentials: %w", err)
		statusErr := ctrl.setApplied(ctx, credentials, errorStatuses(credentials, mergeErr.Error()))
		return errors.Join(mergeErr, statusErr)
	}

	if err := ctrl.applyMergedSecret(ctx, result.MergedJSON); err != nil {
		statusErr := ctrl.setApplied(ctx, credentials, errorStatuses(credentials, err.Error()))
		return errors.Join(err, statusErr)
	}

	return ctrl.setApplied(ctx, credentials, result.Statuses)
}

func (ctrl *Controller) listCredentials() ([]*unstructured.Unstructured, []CredentialEntry, error) {
	objects, err := ctrl.credentialLister.List(labels.Everything())
	if err != nil {
		return nil, nil, fmt.Errorf("listing GlobalPullCredentials: %w", err)
	}

	credentials := make([]*unstructured.Unstructured, 0, len(objects))
	entries := make([]CredentialEntry, 0, len(objects))
	for _, object := range objects {
		credential, ok := object.(*unstructured.Unstructured)
		if !ok {
			return nil, nil, fmt.Errorf("unexpected GlobalPullCredential object %T", object)
		}
		credentials = append(credentials, credential)

		entry, err := credentialEntry(credential)
		if err != nil {
			return credentials, nil, err
		}
		entries = append(entries, entry)
	}
	return credentials, entries, nil
}

func credentialEntry(credential *unstructured.Unstructured) (CredentialEntry, error) {
	hostname, err := requiredString(credential, "hostname", "spec", "hostname")
	if err != nil {
		return CredentialEntry{}, err
	}
	username, err := requiredBase64String(credential, "username", "spec", "auth", "username")
	if err != nil {
		return CredentialEntry{}, err
	}
	if strings.Contains(username, ":") {
		return CredentialEntry{}, fmt.Errorf("invalid username for GlobalPullCredential %q: username must not contain a colon", credential.GetName())
	}
	password, err := requiredBase64String(credential, "password", "spec", "auth", "password")
	if err != nil {
		return CredentialEntry{}, err
	}
	return CredentialEntry{Name: credential.GetName(), Hostname: hostname, Username: username, Password: password}, nil
}

func requiredString(credential *unstructured.Unstructured, field string, path ...string) (string, error) {
	value, ok, err := unstructured.NestedString(credential.Object, path...)
	if err != nil {
		return "", fmt.Errorf("reading %s for GlobalPullCredential %q: %w", field, credential.GetName(), err)
	}
	if !ok {
		return "", fmt.Errorf("missing %s for GlobalPullCredential %q", field, credential.GetName())
	}
	return value, nil
}

func requiredBase64String(credential *unstructured.Unstructured, field string, path ...string) (string, error) {
	encoded, err := requiredString(credential, field, path...)
	if err != nil {
		return "", err
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decoding %s for GlobalPullCredential %q: %w", field, credential.GetName(), err)
	}
	if len(decoded) == 0 {
		return "", fmt.Errorf("decoded %s for GlobalPullCredential %q must not be empty", field, credential.GetName())
	}
	return string(decoded), nil
}

func (ctrl *Controller) applyMergedSecret(ctx context.Context, mergedJSON []byte) error {
	required := applyconfigcorev1.
		Secret(ctrlcommon.GlobalPullSecretMergedName, ctrlcommon.OpenshiftConfigNamespace).
		WithLabels(map[string]string{managedByLabel: managedByValue}).
		WithType(corev1.SecretTypeDockerConfigJson).
		WithData(map[string][]byte{corev1.DockerConfigJsonKey: mergedJSON})

	if _, err := ctrl.kubeClient.CoreV1().Secrets(ctrlcommon.OpenshiftConfigNamespace).Apply(ctx, required, metav1.ApplyOptions{FieldManager: fieldManager}); err != nil {
		return fmt.Errorf("applying %s: %w", ctrlcommon.GlobalPullSecretMergedName, err)
	}
	return nil
}

func errorStatuses(credentials []*unstructured.Unstructured, message string) map[string]credentialStatus {
	statuses := make(map[string]credentialStatus, len(credentials))
	for _, credential := range credentials {
		statuses[credential.GetName()] = credentialStatus{
			Reason:  reconcileErrorReason,
			Message: message,
		}
	}
	return statuses
}

func (ctrl *Controller) setApplied(
	ctx context.Context,
	credentials []*unstructured.Unstructured,
	statuses map[string]credentialStatus,
) error {
	var errs []error
	for _, credential := range credentials {
		status, ok := statuses[credential.GetName()]
		if !ok {
			errs = append(errs, fmt.Errorf("missing reconcile status for GlobalPullCredential %q", credential.GetName()))
			continue
		}
		condition := metav1.Condition{
			Type:               appliedCondition,
			Status:             metav1.ConditionFalse,
			Reason:             status.Reason,
			Message:            status.Message,
			ObservedGeneration: credential.GetGeneration(),
		}
		if status.Applied {
			condition.Status = metav1.ConditionTrue
		}
		if err := ctrl.updateCredentialStatus(ctx, credential, condition); err != nil {
			errs = append(errs, fmt.Errorf("updating status for GlobalPullCredential %q: %w", credential.GetName(), err))
		}
	}
	return errors.Join(errs...)
}

func (ctrl *Controller) updateCredentialStatus(ctx context.Context, credential *unstructured.Unstructured, condition metav1.Condition) error {
	var status struct {
		Conditions []metav1.Condition `json:"conditions"`
	}
	if rawStatus, ok := credential.Object["status"]; ok {
		statusMap, ok := rawStatus.(map[string]interface{})
		if !ok {
			return fmt.Errorf("unexpected status for GlobalPullCredential %q", credential.GetName())
		}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(statusMap, &status); err != nil {
			return fmt.Errorf("parsing status for GlobalPullCredential %q: %w", credential.GetName(), err)
		}
	}

	if !meta.SetStatusCondition(&status.Conditions, condition) {
		return nil
	}

	updated := credential.DeepCopy()
	statusMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&status)
	if err != nil {
		return fmt.Errorf("serializing status for GlobalPullCredential %q: %w", credential.GetName(), err)
	}
	if err := unstructured.SetNestedField(updated.Object, statusMap["conditions"], "status", "conditions"); err != nil {
		return fmt.Errorf("setting status for GlobalPullCredential %q: %w", credential.GetName(), err)
	}

	if _, err := ctrl.dynamicClient.Resource(globalPullCredentialGVR).UpdateStatus(ctx, updated, metav1.UpdateOptions{FieldManager: fieldManager}); err != nil {
		return fmt.Errorf("updating status for GlobalPullCredential %q: %w", credential.GetName(), err)
	}
	return nil
}
