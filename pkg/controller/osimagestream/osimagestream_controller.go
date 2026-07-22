package osimagestream

import (
	"context"
	"fmt"
	"github.com/openshift/machine-config-operator/pkg/version"
	"maps"
	"reflect"
	"sync"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	apioperatorsv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	configinformersv1 "github.com/openshift/client-go/config/informers/externalversions/config/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/scheme"
	mcfginformersv1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	operatorinformersv1alpha1 "github.com/openshift/client-go/operator/informers/externalversions/operator/v1alpha1"
	operatorlistersv1alpha1 "github.com/openshift/client-go/operator/listers/operator/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/imageutils"
	"github.com/openshift/machine-config-operator/pkg/osimagestream"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	coreinformersv1 "k8s.io/client-go/informers/core/v1"
	corelistersv1 "k8s.io/client-go/listers/core/v1"
)

const (
	maxRetries = 15
)

type Controller struct {
	kubeClient    clientset.Interface
	mcfgClient    mcfgclientset.Interface
	eventRecorder record.EventRecorder
	fgHandler     ctrlcommon.FeatureGatesHandler

	syncHandler func(key string) error

	osImageStreamLister       mcfglistersv1.OSImageStreamLister
	osImageStreamListerSynced cache.InformerSynced

	ccLister       mcfglistersv1.ControllerConfigLister
	ccListerSynced cache.InformerSynced

	clusterVersionLister       configlistersv1.ClusterVersionLister
	clusterVersionListerSynced cache.InformerSynced

	secretLister       corelistersv1.SecretLister
	secretListerSynced cache.InformerSynced

	mcoCmLister       corelistersv1.ConfigMapLister
	mcoCmListerSynced cache.InformerSynced

	icspLister       operatorlistersv1alpha1.ImageContentSourcePolicyLister
	icspListerSynced cache.InformerSynced

	idmsLister       configlistersv1.ImageDigestMirrorSetLister
	idmsListerSynced cache.InformerSynced

	itmsLister       configlistersv1.ImageTagMirrorSetLister
	itmsListerSynced cache.InformerSynced

	imgLister       configlistersv1.ImageLister
	imgListerSynced cache.InformerSynced

	inspectorFactory osimagestream.ImagesInspectorFactory

	queue workqueue.TypedRateLimitingInterface[string]

	initialSyncDone chan error
	initialSyncOnce sync.Once
}

func New(
	osImageStreamInformer mcfginformersv1.OSImageStreamInformer,
	ccInformer mcfginformersv1.ControllerConfigInformer,
	clusterVersionInformer configinformersv1.ClusterVersionInformer,
	secretInformer coreinformersv1.SecretInformer,
	cmInformer coreinformersv1.ConfigMapInformer,
	icspInformer operatorinformersv1alpha1.ImageContentSourcePolicyInformer,
	idmsInformer configinformersv1.ImageDigestMirrorSetInformer,
	itmsInformer configinformersv1.ImageTagMirrorSetInformer,
	imgInformer configinformersv1.ImageInformer,
	kubeClient clientset.Interface,
	mcfgClient mcfgclientset.Interface,
	fgHandler ctrlcommon.FeatureGatesHandler,
	inspectorFactory osimagestream.ImagesInspectorFactory,
) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&corev1client.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	ctrl := &Controller{
		kubeClient:       kubeClient,
		mcfgClient:       mcfgClient,
		eventRecorder:    ctrlcommon.NamespacedEventRecorder(eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineconfigcontroller-osimagestreamcontroller"})),
		fgHandler:        fgHandler,
		inspectorFactory: inspectorFactory,
		initialSyncDone:  make(chan error, 1),
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "machineconfigcontroller-osimagestreamcontroller"}),
	}

	ctrl.syncHandler = ctrl.syncOSImageStream

	osImageStreamInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addOSImageStream,
		UpdateFunc: ctrl.updateOSImageStream,
		DeleteFunc: ctrl.deleteOSImageStream,
	})

	ctrl.osImageStreamLister = osImageStreamInformer.Lister()
	ctrl.osImageStreamListerSynced = osImageStreamInformer.Informer().HasSynced

	ctrl.ccLister = ccInformer.Lister()
	ctrl.ccListerSynced = ccInformer.Informer().HasSynced

	ctrl.clusterVersionLister = clusterVersionInformer.Lister()
	ctrl.clusterVersionListerSynced = clusterVersionInformer.Informer().HasSynced

	ctrl.secretLister = secretInformer.Lister()
	ctrl.secretListerSynced = secretInformer.Informer().HasSynced

	ctrl.mcoCmLister = cmInformer.Lister()
	ctrl.mcoCmListerSynced = cmInformer.Informer().HasSynced

	ctrl.icspLister = icspInformer.Lister()
	ctrl.icspListerSynced = icspInformer.Informer().HasSynced

	ctrl.idmsLister = idmsInformer.Lister()
	ctrl.idmsListerSynced = idmsInformer.Informer().HasSynced

	ctrl.itmsLister = itmsInformer.Lister()
	ctrl.itmsListerSynced = itmsInformer.Informer().HasSynced

	ctrl.imgLister = imgInformer.Lister()
	ctrl.imgListerSynced = imgInformer.Informer().HasSynced

	return ctrl
}

func (ctrl *Controller) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer ctrl.queue.ShutDown()

	if !cache.WaitForCacheSync(stopCh,
		ctrl.osImageStreamListerSynced,
		ctrl.ccListerSynced,
		ctrl.clusterVersionListerSynced,
		ctrl.secretListerSynced,
		ctrl.mcoCmListerSynced,
		ctrl.icspListerSynced,
		ctrl.idmsListerSynced,
		ctrl.itmsListerSynced,
		ctrl.imgListerSynced,
	) {
		ctrl.initialSyncDone <- fmt.Errorf("caches never synced for OSImageStream controller")
		return
	}

	if workers > 1 {
		klog.Warningf("OSImageStream controller requested %d workers, but only 1 is supported; ignoring extra workers", workers)
	}

	klog.Info("Starting MachineConfigController-OSImageStreamController")
	defer klog.Info("Shutting down MachineConfigController-OSImageStreamController")

	go wait.Until(ctrl.worker, time.Second, stopCh)

	// Run once at start at least to make sure the OSImageStream is created when creating the cluster
	ctrl.enqueue()

	<-stopCh
}

// EnsureOSImageStream blocks until the controller's first successful sync
// completes, guaranteeing the OSImageStream CR exists.
func (ctrl *Controller) EnsureOSImageStream(ctx context.Context) error {
	klog.Info("Waiting for initial OSImageStream sync")
	select {
	case err := <-ctrl.initialSyncDone:
		if err != nil {
			return fmt.Errorf("OSImageStream controller failed to start: %w", err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("context cancelled while waiting for OSImageStream: %w", ctx.Err())
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
		klog.V(2).Infof("Error syncing OSImageStream %v: %v", key, err)
		ctrl.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	klog.V(2).Infof("Dropping OSImageStream %q out of the queue: %v", key, err)
	ctrl.queue.Forget(key)
	ctrl.queue.AddAfter(key, 1*time.Minute)
}

func (ctrl *Controller) enqueue() {
	ctrl.queue.Add(ctrlcommon.ClusterInstanceNameOSImageStream)
}

func (ctrl *Controller) addOSImageStream(obj interface{}) {
	osis := obj.(*mcfgv1.OSImageStream)
	klog.V(4).Infof("Adding OSImageStream %s", osis.Name)
	ctrl.enqueue()
}

func (ctrl *Controller) deleteOSImageStream(obj interface{}) {
	osis, ok := obj.(*mcfgv1.OSImageStream)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("couldn't get object from tombstone %#v", obj))
			return
		}
		osis, ok = tombstone.Obj.(*mcfgv1.OSImageStream)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not an OSImageStream %#v", obj))
			return
		}
	}
	klog.V(4).Infof("OSImageStream %s deleted", osis.Name)

	// Enqueue a work item so the controller recreates the OSImageStream singleton
	ctrl.enqueue()
}

func (ctrl *Controller) updateOSImageStream(old, cur interface{}) {
	oldOSIS := old.(*mcfgv1.OSImageStream)
	curOSIS := cur.(*mcfgv1.OSImageStream)

	if reflect.DeepEqual(oldOSIS.Spec, curOSIS.Spec) && maps.Equal(oldOSIS.Annotations, curOSIS.Annotations) {
		return
	}
	klog.V(4).Infof("OSImageStream %s updated", curOSIS.Name)
	ctrl.enqueue()
}

func (ctrl *Controller) syncOSImageStream(key string) error {
	startTime := time.Now()
	klog.V(4).Infof("Started syncing OSImageStream %q (%v)", key, startTime)
	defer func() {
		klog.V(4).Infof("Finished syncing OSImageStream %q (%v)", key, time.Since(startTime))
	}()

	if !osimagestream.IsFeatureEnabled(ctrl.fgHandler) {
		klog.V(4).Info("OSImageStream feature is not enabled, skipping sync")
		return nil
	}

	existing, rebuildRequired, err := ctrl.isOSImageStreamBuildRequired()
	if err != nil {
		return err
	}

	if rebuildRequired {
		if err := ctrl.buildOSImageStream(context.TODO(), existing); err != nil {
			return err
		}
	} else if existing != nil {
		if err := ctrl.handleOSImageStreamUpdate(context.TODO(), existing); err != nil {
			return err
		}
	}

	ctrl.initialSyncOnce.Do(func() {
		if osis, err := ctrl.getExistingOSImageStream(); err == nil && osis != nil {
			klog.Infof("OSImageStream synced successfully. Available streams: %s. Default stream: %s",
				osimagestream.GetStreamSetsNames(osis.Status.AvailableStreams),
				osis.Status.DefaultStream)
		}
		ctrl.initialSyncDone <- nil
	})
	return nil
}

func (ctrl *Controller) isOSImageStreamBuildRequired() (*mcfgv1.OSImageStream, bool, error) {
	existing, err := ctrl.getExistingOSImageStream()
	if err != nil {
		return nil, true, err
	}

	// Get the release payload image to check if it has changed
	clusterVersion, err := osimagestream.GetClusterVersion(ctrl.clusterVersionLister)
	if err != nil {
		return nil, true, fmt.Errorf("getting cluster version for OSImageStream rebuild check: %w", err)
	}
	releaseImage, err := osimagestream.GetReleasePayloadImage(clusterVersion)
	if err != nil {
		return nil, true, fmt.Errorf("getting release image for OSImageStream rebuild check: %w", err)
	}

	if !osImageStreamRequiresRebuild(existing, releaseImage) {
		klog.V(4).Info("OSImageStream is already up-to-date, skipping sync")
		return existing, false, nil
	}

	// During an upgrade the CVO updates ClusterVersion.Status.Desired before
	// replacing the operator pod. The old operator would see the new payload
	// image and rebuild the OSImageStream with its own stale version.Hash,
	// poisoning the annotation for the incoming operator. Defer the rebuild
	// to the new operator whose version matches the target.
	if clusterVersion.Status.Desired.Version != version.ReleaseVersion {
		klog.Infof("OSImageStream rebuild deferred: ClusterVersion targets %s but operator is %s",
			clusterVersion.Status.Desired.Version, version.ReleaseVersion)
		return existing, false, nil
	}

	return existing, true, nil
}

func (ctrl *Controller) getExistingOSImageStream() (*mcfgv1.OSImageStream, error) {
	osis, err := ctrl.osImageStreamLister.Get(ctrlcommon.ClusterInstanceNameOSImageStream)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to retrieve existing OSImageStream: %w", err)
		}
		return nil, nil
	}
	return osis, nil
}

func (ctrl *Controller) buildOSImageStream(ctx context.Context, existing *mcfgv1.OSImageStream) error {
	klog.Info("Starting building of the OSImageStream instance")

	cc, err := ctrl.ccLister.Get(ctrlcommon.ControllerConfigName)
	if err != nil {
		return fmt.Errorf("getting ControllerConfig for OSImageStream build: %w", err)
	}

	clusterVersion, err := osimagestream.GetClusterVersion(ctrl.clusterVersionLister)
	if err != nil {
		return fmt.Errorf("getting cluster version for OSImageStream inspection: %w", err)
	}
	image, err := osimagestream.GetReleasePayloadImage(clusterVersion)
	if err != nil {
		return fmt.Errorf("getting the Release Image digest from the ClusterVersion for OSImageStream sync: %w", err)
	}

	installVersion, err := osimagestream.GetInstallVersion(clusterVersion)
	if err != nil {
		klog.Warningf("Unable to get install version for OSImageStream build: %s", err)
	}

	clusterPullSecret, err := ctrl.secretLister.Secrets(ctrlcommon.OpenshiftConfigNamespace).Get("pull-secret")
	if err != nil {
		return fmt.Errorf("could not get the cluster PullSecret for OSImageStream sync: %w", err)
	}

	sysCtxFactory := func() (*imageutils.SysContext, error) {
		builder, err := ctrl.getSysContextBuilder(clusterPullSecret, cc)
		if err != nil {
			return nil, fmt.Errorf("could not build SysContext for OSImageStream build: %w", err)
		}
		return builder.Build()
	}

	buildCtx, buildCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer buildCancel()

	factory := osimagestream.NewDefaultStreamSourceFactory(ctrl.inspectorFactory)
	osImageStream, err := factory.Create(
		buildCtx,
		sysCtxFactory,
		osimagestream.CreateOptions{
			ReleaseImage:          image,
			ConfigMapLister:       ctrl.mcoCmLister,
			ExistingOSImageStream: existing,
			InstallVersion:        installVersion,
		})
	if err != nil {
		return fmt.Errorf("building the OSImageStream: %w", err)
	}

	var updateOSImageStream *mcfgv1.OSImageStream
	if existing == nil {
		klog.V(4).Info("Creating OSImageStream singleton instance")
		updateOSImageStream, err = ctrl.mcfgClient.MachineconfigurationV1().OSImageStreams().Create(ctx, osImageStream, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("error creating the OSImageStream: %w", err)
		}
		klog.Infof("Created OSImageStream with %d available streams, default stream: %s",
			len(osImageStream.Status.AvailableStreams), osImageStream.Status.DefaultStream)
	} else {
		oldPayloadImage := existing.Annotations[ctrlcommon.ReleasePayloadImageAnnotationKey]
		klog.V(4).Infof("Updating OSImageStream (previous release image: %s, new release image: %s)", oldPayloadImage, image)
		desired := existing.DeepCopy()
		if desired.Annotations == nil {
			desired.Annotations = map[string]string{}
		}
		maps.Copy(desired.Annotations, osImageStream.Annotations)
		updateOSImageStream, err = ctrl.mcfgClient.MachineconfigurationV1().OSImageStreams().Update(ctx, desired, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("error updating the OSImageStream: %w", err)
		}
	}

	updateOSImageStream.Status = osImageStream.Status
	if _, err = ctrl.mcfgClient.
		MachineconfigurationV1().
		OSImageStreams().
		UpdateStatus(ctx, updateOSImageStream, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("error updating the OSImageStream status: %w", err)
	}

	return nil
}

func (ctrl *Controller) handleOSImageStreamUpdate(ctx context.Context, existing *mcfgv1.OSImageStream) error {
	requestedDefault := osimagestream.GetOSImageStreamSpecDefault(existing)
	if requestedDefault == "" {
		return nil
	}

	currentDefault := existing.Status.DefaultStream
	if currentDefault != requestedDefault {
		if _, err := osimagestream.GetOSImageStreamSetByName(existing, requestedDefault); err != nil {
			return fmt.Errorf("syncing default OSImageStream with OSImageStream %s: %w", requestedDefault, err)
		}

		osis := existing.DeepCopy()
		osis.Status.DefaultStream = requestedDefault
		if _, err := ctrl.mcfgClient.
			MachineconfigurationV1().
			OSImageStreams().
			UpdateStatus(ctx, osis, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("updating the default OSImageStream status: %w", err)
		}

		klog.Infof("OSImageStream default has changed from %s to %s", currentDefault, requestedDefault)
	}
	return nil
}

// osImageStreamRequiresRebuild checks if the OSImageStream needs to be created or updated.
// Returns true if the OSImageStream is nil, if the release payload image changed, or if
// the MCO binary version changed. The release payload image check detects real content
// changes (new RHCOS images). The version.Hash check ensures the render controller's
// version-skew guard is satisfied — it compares ReleaseImageVersionAnnotationKey against
// its own version.Hash, so the annotation must be kept in sync even when the release
// payload image hasn't changed (e.g., CI upgrade jobs where only the MCO binary is rebuilt).
func osImageStreamRequiresRebuild(osImageStream *mcfgv1.OSImageStream, releaseImage string) bool {
	if osImageStream == nil {
		return true
	}
	storedImage, ok := osImageStream.Annotations[ctrlcommon.ReleasePayloadImageAnnotationKey]
	if !ok || storedImage != releaseImage {
		return true
	}
	storedVersion, ok := osImageStream.Annotations[ctrlcommon.ReleaseImageVersionAnnotationKey]
	return !ok || storedVersion != version.Hash
}

func (ctrl *Controller) getSysContextBuilder(clusterPullSecret *corev1.Secret, cc *mcfgv1.ControllerConfig) (*imageutils.SysContextBuilder, error) {
	sysCtxBuilder := imageutils.NewSysContextBuilder().
		WithSecret(clusterPullSecret).
		WithControllerConfig(cc)

	imageConfig, err := ctrl.imgLister.Get("cluster")
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("could not get image configuration: %w", err)
	}

	icspRules, err := ctrl.icspLister.List(labels.Everything())
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("could not get ICSP rules: %w", err)
		}
		icspRules = []*apioperatorsv1alpha1.ImageContentSourcePolicy{}
	}

	idmsRules, err := ctrl.idmsLister.List(labels.Everything())
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("could not get IDMS rules: %w", err)
		}
		idmsRules = []*configv1.ImageDigestMirrorSet{}
	}

	itmsRules, err := ctrl.itmsLister.List(labels.Everything())
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("could not get ITMS rules: %w", err)
		}
		itmsRules = []*configv1.ImageTagMirrorSet{}
	}

	if imageConfig != nil || len(icspRules) != 0 || len(idmsRules) != 0 || len(itmsRules) != 0 {
		registriesConf, err := imageutils.GenerateRegistriesConfig(imageConfig, icspRules, idmsRules, itmsRules)
		if err != nil {
			return nil, fmt.Errorf("could not build registries configuration: %w", err)
		}
		sysCtxBuilder.WithRegistriesConfig(registriesConf)
	}

	return sysCtxBuilder, nil
}

// Retain implements CacheEvicter — it keeps cached digests referenced by the
// OSImage and OSExtensionsImage of each available stream in the OSImageStream.
func (ctrl *Controller) Retain(digests []string) []string {
	osis, err := ctrl.getExistingOSImageStream()
	if err != nil || osis == nil {
		return digests
	}

	active := sets.New[string]()
	for _, s := range osis.Status.AvailableStreams {
		if d := imageutils.DigestFromPullspec(string(s.OSImage)); d != "" {
			active.Insert(d)
		}
		if d := imageutils.DigestFromPullspec(string(s.OSExtensionsImage)); d != "" {
			active.Insert(d)
		}
	}

	return sets.New(digests...).Intersection(active).UnsortedList()
}
