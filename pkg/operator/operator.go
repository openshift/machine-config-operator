package operator

import (
	"context"
	"fmt"
	"time"

	opv1 "github.com/openshift/api/operator/v1"
	operatorinformersv1alpha1 "github.com/openshift/client-go/operator/informers/externalversions/operator/v1alpha1"
	operatorlistersv1alpha1 "github.com/openshift/client-go/operator/listers/operator/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/osimagestream"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"

	configclientset "github.com/openshift/client-go/config/clientset/versioned"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/version"
	corev1 "k8s.io/api/core/v1"
	apiextclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextinformersv1 "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions/apiextensions/v1"
	apiextlistersv1 "k8s.io/apiextensions-apiserver/pkg/client/listers/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	appsinformersv1 "k8s.io/client-go/informers/apps/v1"
	coreinformersv1 "k8s.io/client-go/informers/core/v1"
	rbacinformersv1 "k8s.io/client-go/informers/rbac/v1"
	"k8s.io/client-go/kubernetes"
	coreclientsetv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	appslisterv1 "k8s.io/client-go/listers/apps/v1"
	corelisterv1 "k8s.io/client-go/listers/core/v1"

	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"

	configinformersv1 "github.com/openshift/client-go/config/informers/externalversions/config/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	"github.com/openshift/library-go/pkg/operator/events"

	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/scheme"
	mcfginformersv1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1"
	mcfginformersv1alpha1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1alpha1"

	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	mcfglistersv1alpha1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1alpha1"

	mcopclientset "github.com/openshift/client-go/operator/clientset/versioned"
	mcopinformersv1 "github.com/openshift/client-go/operator/informers/externalversions/operator/v1"
	mcoplistersv1 "github.com/openshift/client-go/operator/listers/operator/v1"

	loglevelhelpers "github.com/openshift/library-go/pkg/operator/loglevel"
)

const (
	// maxRetries is the number of times a machineconfig pool will be retried before it is dropped out of the queue.
	// With the current rate-limiter in use (5ms*2^(maxRetries-1)) the following numbers represent the times
	// a machineconfig pool is going to be requeued:
	//
	// 5ms, 10ms, 20ms, 40ms, 80ms, 160ms, 320ms, 640ms, 1.3s, 2.6s, 5.1s, 10.2s, 20.4s, 41s, 82s
	maxRetries = 15
)

// Operator defines machince config operator.
type Operator struct {
	namespace, name string

	inClusterBringup bool

	imagesFile    string
	templatesPath string

	logLevel int

	vStore *versionStore

	operatorHealthEvents record.EventRecorder

	client        mcfgclientset.Interface
	kubeClient    kubernetes.Interface
	apiExtClient  apiextclientset.Interface
	configClient  configclientset.Interface
	mcopClient    mcopclientset.Interface
	eventRecorder record.EventRecorder
	libgoRecorder events.Recorder

	syncHandler func(ic string) error

	imgLister             configlistersv1.ImageLister
	idmsLister            configlistersv1.ImageDigestMirrorSetLister
	itmsLister            configlistersv1.ImageTagMirrorSetLister
	icspLister            operatorlistersv1alpha1.ImageContentSourcePolicyLister
	crdLister             apiextlistersv1.CustomResourceDefinitionLister
	mcpLister             mcfglistersv1.MachineConfigPoolLister
	msLister              mcfglistersv1.MachineConfigNodeLister
	ccLister              mcfglistersv1.ControllerConfigLister
	mcLister              mcfglistersv1.MachineConfigLister
	deployLister          appslisterv1.DeploymentLister
	daemonsetLister       appslisterv1.DaemonSetLister
	infraLister           configlistersv1.InfrastructureLister
	networkLister         configlistersv1.NetworkLister
	mcoCmLister           corelisterv1.ConfigMapLister
	clusterCmLister       corelisterv1.ConfigMapLister
	proxyLister           configlistersv1.ProxyLister
	oseKubeAPILister      corelisterv1.ConfigMapLister
	nodeLister            corelisterv1.NodeLister
	dnsLister             configlistersv1.DNSLister
	mcoSALister           corelisterv1.ServiceAccountLister
	mcoSecretLister       corelisterv1.SecretLister
	ocCmLister            corelisterv1.ConfigMapLister
	ocSecretLister        corelisterv1.SecretLister
	ocManagedSecretLister corelisterv1.SecretLister
	clusterOperatorLister configlistersv1.ClusterOperatorLister
	mcopLister            mcoplistersv1.MachineConfigurationLister
	mckLister             mcfglistersv1.KubeletConfigLister
	crcLister             mcfglistersv1.ContainerRuntimeConfigLister
	nodeClusterLister     configlistersv1.NodeLister
	moscLister            mcfglistersv1.MachineOSConfigLister
	apiserverLister       configlistersv1.APIServerLister
	clusterVersionLister  configlistersv1.ClusterVersionLister
	osImageStreamLister   mcfglistersv1alpha1.OSImageStreamLister
	iriLister             mcfglistersv1alpha1.InternalReleaseImageLister

	crdListerSynced                  cache.InformerSynced
	deployListerSynced               cache.InformerSynced
	daemonsetListerSynced            cache.InformerSynced
	infraListerSynced                cache.InformerSynced
	networkListerSynced              cache.InformerSynced
	mcpListerSynced                  cache.InformerSynced
	ccListerSynced                   cache.InformerSynced
	mcListerSynced                   cache.InformerSynced
	mcoCmListerSynced                cache.InformerSynced
	clusterCmListerSynced            cache.InformerSynced
	serviceAccountInformerSynced     cache.InformerSynced
	clusterRoleInformerSynced        cache.InformerSynced
	clusterRoleBindingInformerSynced cache.InformerSynced
	proxyListerSynced                cache.InformerSynced
	oseKubeAPIListerSynced           cache.InformerSynced
	nodeListerSynced                 cache.InformerSynced
	dnsListerSynced                  cache.InformerSynced
	maoSecretInformerSynced          cache.InformerSynced
	imgListerSynced                  cache.InformerSynced
	idmsListerSynced                 cache.InformerSynced
	itmsListerSynced                 cache.InformerSynced
	icspListerSynced                 cache.InformerSynced
	mcoSAListerSynced                cache.InformerSynced
	mcoSecretListerSynced            cache.InformerSynced
	ocCmListerSynced                 cache.InformerSynced
	ocSecretListerSynced             cache.InformerSynced
	ocManagedSecretListerSynced      cache.InformerSynced
	clusterOperatorListerSynced      cache.InformerSynced
	mcopListerSynced                 cache.InformerSynced
	mckListerSynced                  cache.InformerSynced
	crcListerSynced                  cache.InformerSynced
	nodeClusterListerSynced          cache.InformerSynced
	moscListerSynced                 cache.InformerSynced
	apiserverListerSynced            cache.InformerSynced
	osImageStreamListerSynced        cache.InformerSynced
	iriListerSynced                  cache.InformerSynced

	// queue only ever has one item, but it has nice error handling backoff/retry semantics
	queue workqueue.TypedRateLimitingInterface[string]

	stopCh <-chan struct{}

	renderConfig *renderConfig

	fgHandler ctrlcommon.FeatureGatesHandler

	ctrlctx *ctrlcommon.ControllerContext
}

// New returns a new machine config operator.
func New(
	namespace, name, imagesFile string,
	mcpInformer mcfginformersv1.MachineConfigPoolInformer,
	mcInformer mcfginformersv1.MachineConfigInformer,
	controllerConfigInformer mcfginformersv1.ControllerConfigInformer,
	serviceAccountInfomer coreinformersv1.ServiceAccountInformer,
	crdInformer apiextinformersv1.CustomResourceDefinitionInformer,
	deployInformer appsinformersv1.DeploymentInformer,
	daemonsetInformer appsinformersv1.DaemonSetInformer,
	clusterRoleInformer rbacinformersv1.ClusterRoleInformer,
	clusterRoleBindingInformer rbacinformersv1.ClusterRoleBindingInformer,
	mcoCmInformer,
	clusterCmInfomer coreinformersv1.ConfigMapInformer,
	infraInformer configinformersv1.InfrastructureInformer,
	networkInformer configinformersv1.NetworkInformer,
	proxyInformer configinformersv1.ProxyInformer,
	dnsInformer configinformersv1.DNSInformer,
	client mcfgclientset.Interface,
	kubeClient kubernetes.Interface,
	apiExtClient apiextclientset.Interface,
	configClient configclientset.Interface,
	oseKubeAPIInformer coreinformersv1.ConfigMapInformer,
	nodeInformer coreinformersv1.NodeInformer,
	maoSecretInformer coreinformersv1.SecretInformer,
	imgInformer configinformersv1.ImageInformer,
	idmsInformer configinformersv1.ImageDigestMirrorSetInformer,
	itmsInformer configinformersv1.ImageTagMirrorSetInformer,
	icspInformer operatorinformersv1alpha1.ImageContentSourcePolicyInformer,
	mcoSAInformer coreinformersv1.ServiceAccountInformer,
	mcoSecretInformer coreinformersv1.SecretInformer,
	ocCmInformer coreinformersv1.ConfigMapInformer,
	ocSecretInformer coreinformersv1.SecretInformer,
	ocManagedSecretInformer coreinformersv1.SecretInformer,
	clusterOperatorInformer configinformersv1.ClusterOperatorInformer,
	mcopClient mcopclientset.Interface,
	mcopInformer mcopinformersv1.MachineConfigurationInformer,
	fgHandler ctrlcommon.FeatureGatesHandler,
	mckInformer mcfginformersv1.KubeletConfigInformer,
	crcInformer mcfginformersv1.ContainerRuntimeConfigInformer,
	nodeClusterInformer configinformersv1.NodeInformer,
	apiserverInformer configinformersv1.APIServerInformer,
	moscInformer mcfginformersv1.MachineOSConfigInformer,
	clusterVersionInformer configinformersv1.ClusterVersionInformer,
	osImageStreamInformer mcfginformersv1alpha1.OSImageStreamInformer,
	iriInformer mcfginformersv1alpha1.InternalReleaseImageInformer,
	ctrlctx *ctrlcommon.ControllerContext,
) *Operator {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&coreclientsetv1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	optr := &Operator{
		namespace:     namespace,
		name:          name,
		imagesFile:    imagesFile,
		vStore:        newVersionStore(),
		client:        client,
		kubeClient:    kubeClient,
		apiExtClient:  apiExtClient,
		configClient:  configClient,
		mcopClient:    mcopClient,
		eventRecorder: ctrlcommon.NamespacedEventRecorder(eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineconfigoperator"})),
		libgoRecorder: events.NewRecorder(kubeClient.CoreV1().Events(ctrlcommon.MCONamespace), "machine-config-operator", &corev1.ObjectReference{
			Kind:       "Deployment",
			Name:       "machine-config-operator",
			Namespace:  ctrlcommon.MCONamespace,
			APIVersion: "apps/v1",
		}, clock.RealClock{}),
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "machineconfigoperator"}),
		fgHandler: fgHandler,
		ctrlctx:   ctrlctx,
		logLevel:  2,
	}

	err := corev1.AddToScheme(scheme.Scheme)
	if err != nil {
		klog.Errorf("Could not modify scheme: %v", err)
	}
	err = opv1.AddToScheme(scheme.Scheme)
	if err != nil {
		klog.Errorf("Could not modify scheme: %v", err)
	}

	informers := []cache.SharedIndexInformer{
		controllerConfigInformer.Informer(),
		serviceAccountInfomer.Informer(),
		crdInformer.Informer(),
		deployInformer.Informer(),
		daemonsetInformer.Informer(),
		clusterRoleInformer.Informer(),
		clusterRoleBindingInformer.Informer(),
		mcoCmInformer.Informer(),
		clusterCmInfomer.Informer(),
		infraInformer.Informer(),
		networkInformer.Informer(),
		mcpInformer.Informer(),
		proxyInformer.Informer(),
		oseKubeAPIInformer.Informer(),
		nodeInformer.Informer(),
		dnsInformer.Informer(),
		maoSecretInformer.Informer(),
		imgInformer.Informer(),
		mcoSAInformer.Informer(),
		mcoSecretInformer.Informer(),
		ocSecretInformer.Informer(),
		ocCmInformer.Informer(),
		ocManagedSecretInformer.Informer(),
		mcopInformer.Informer(),
		mckInformer.Informer(),
		crcInformer.Informer(),
		nodeClusterInformer.Informer(),
		clusterOperatorInformer.Informer(),
		apiserverInformer.Informer(),
		moscInformer.Informer(),
	}
	for _, i := range informers {
		i.AddEventHandler(optr.eventHandler())
	}

	optr.syncHandler = optr.sync

	optr.imgLister = imgInformer.Lister()
	optr.imgListerSynced = imgInformer.Informer().HasSynced
	optr.idmsLister = idmsInformer.Lister()
	optr.idmsListerSynced = idmsInformer.Informer().HasSynced
	optr.itmsLister = itmsInformer.Lister()
	optr.itmsListerSynced = itmsInformer.Informer().HasSynced
	optr.icspLister = icspInformer.Lister()
	optr.icspListerSynced = icspInformer.Informer().HasSynced

	optr.clusterCmLister = clusterCmInfomer.Lister()
	optr.clusterCmListerSynced = clusterCmInfomer.Informer().HasSynced
	optr.mcpLister = mcpInformer.Lister()
	optr.mcpListerSynced = mcpInformer.Informer().HasSynced
	optr.ccLister = controllerConfigInformer.Lister()
	optr.ccListerSynced = controllerConfigInformer.Informer().HasSynced
	optr.mcLister = mcInformer.Lister()
	optr.mcListerSynced = mcInformer.Informer().HasSynced
	optr.proxyLister = proxyInformer.Lister()
	optr.proxyListerSynced = proxyInformer.Informer().HasSynced
	optr.oseKubeAPILister = oseKubeAPIInformer.Lister()
	optr.oseKubeAPIListerSynced = oseKubeAPIInformer.Informer().HasSynced
	optr.nodeLister = nodeInformer.Lister()
	optr.nodeListerSynced = nodeInformer.Informer().HasSynced
	optr.nodeClusterLister = nodeClusterInformer.Lister()
	optr.nodeClusterListerSynced = nodeClusterInformer.Informer().HasSynced

	optr.maoSecretInformerSynced = maoSecretInformer.Informer().HasSynced
	optr.serviceAccountInformerSynced = serviceAccountInfomer.Informer().HasSynced
	optr.clusterRoleInformerSynced = clusterRoleInformer.Informer().HasSynced
	optr.clusterRoleBindingInformerSynced = clusterRoleBindingInformer.Informer().HasSynced
	optr.mcoCmLister = mcoCmInformer.Lister()
	optr.mcoCmListerSynced = mcoCmInformer.Informer().HasSynced
	optr.crdLister = crdInformer.Lister()
	optr.crdListerSynced = crdInformer.Informer().HasSynced
	optr.deployLister = deployInformer.Lister()
	optr.deployListerSynced = deployInformer.Informer().HasSynced
	optr.daemonsetLister = daemonsetInformer.Lister()
	optr.daemonsetListerSynced = daemonsetInformer.Informer().HasSynced
	optr.infraLister = infraInformer.Lister()
	optr.infraListerSynced = infraInformer.Informer().HasSynced
	optr.networkLister = networkInformer.Lister()
	optr.networkListerSynced = networkInformer.Informer().HasSynced
	optr.dnsLister = dnsInformer.Lister()
	optr.dnsListerSynced = dnsInformer.Informer().HasSynced
	optr.mcoSALister = mcoSAInformer.Lister()
	optr.mcoSAListerSynced = mcoSAInformer.Informer().HasSynced
	optr.mcoSecretLister = mcoSecretInformer.Lister()
	optr.mcoSecretListerSynced = mcoSecretInformer.Informer().HasSynced
	optr.ocCmLister = ocCmInformer.Lister()
	optr.ocCmListerSynced = ocCmInformer.Informer().HasSynced
	optr.ocSecretLister = ocSecretInformer.Lister()
	optr.ocSecretListerSynced = ocSecretInformer.Informer().HasSynced
	optr.ocManagedSecretLister = ocManagedSecretInformer.Lister()
	optr.ocManagedSecretListerSynced = ocManagedSecretInformer.Informer().HasSynced
	optr.clusterOperatorLister = clusterOperatorInformer.Lister()
	optr.clusterOperatorListerSynced = clusterOperatorInformer.Informer().HasSynced
	optr.mcopLister = mcopInformer.Lister()
	optr.mcopListerSynced = mcopInformer.Informer().HasSynced
	optr.mckLister = mckInformer.Lister()
	optr.mckListerSynced = mckInformer.Informer().HasSynced
	optr.crcLister = crcInformer.Lister()
	optr.crcListerSynced = crcInformer.Informer().HasSynced
	optr.apiserverLister = apiserverInformer.Lister()
	optr.apiserverListerSynced = apiserverInformer.Informer().HasSynced
	optr.moscLister = moscInformer.Lister()
	optr.moscListerSynced = moscInformer.Informer().HasSynced
	optr.clusterVersionLister = clusterVersionInformer.Lister()
	if osImageStreamInformer != nil && osimagestream.IsFeatureEnabled(optr.fgHandler) {
		optr.osImageStreamLister = osImageStreamInformer.Lister()
		optr.osImageStreamListerSynced = osImageStreamInformer.Informer().HasSynced
	}
	if iriInformer != nil {
		optr.iriLister = iriInformer.Lister()
		optr.iriListerSynced = iriInformer.Informer().HasSynced
	}

	optr.vStore.Set("operator", version.ReleaseVersion)
	optr.vStore.Set("operator-image", version.OperatorImage)

	return optr
}

// Run runs the machine config operator.
func (optr *Operator) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer optr.queue.ShutDown()

	apiClient := optr.apiExtClient.ApiextensionsV1()
	_, err := apiClient.CustomResourceDefinitions().Get(context.TODO(), "controllerconfigs.machineconfiguration.openshift.io", metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.Infof("Couldn't find controllerconfig CRD, in cluster bringup mode")
			optr.inClusterBringup = true
		} else {
			klog.Errorf("While checking for cluster bringup: %v", err)
		}
	}

	cacheSynced := []cache.InformerSynced{
		optr.crdListerSynced,
		optr.deployListerSynced,
		optr.daemonsetListerSynced,
		optr.infraListerSynced,
		optr.mcoCmListerSynced,
		optr.clusterCmListerSynced,
		optr.serviceAccountInformerSynced,
		optr.clusterRoleInformerSynced,
		optr.maoSecretInformerSynced,
		optr.clusterRoleBindingInformerSynced,
		optr.networkListerSynced,
		optr.proxyListerSynced,
		optr.oseKubeAPIListerSynced,
		optr.nodeListerSynced,
		optr.mcpListerSynced,
		optr.mcListerSynced,
		optr.dnsListerSynced,
		optr.imgListerSynced,
		optr.idmsListerSynced,
		optr.itmsListerSynced,
		optr.icspListerSynced,
		optr.mcoSAListerSynced,
		optr.mcoSecretListerSynced,
		optr.ocCmListerSynced,
		optr.ocSecretListerSynced,
		optr.ocManagedSecretListerSynced,
		optr.clusterOperatorListerSynced,
		optr.mcopListerSynced,
		optr.mckListerSynced,
		optr.crcListerSynced,
		optr.nodeClusterListerSynced,
		optr.moscListerSynced,
	}
	if optr.osImageStreamListerSynced != nil && osimagestream.IsFeatureEnabled(optr.fgHandler) {
		cacheSynced = append(cacheSynced, optr.osImageStreamListerSynced)
	}
	if optr.iriListerSynced != nil {
		cacheSynced = append(cacheSynced, optr.iriListerSynced)
	}
	if !cache.WaitForCacheSync(stopCh,
		cacheSynced...) {
		klog.Error("failed to sync caches")
		return
	}

	// these can only be synced after CRDs are installed
	if !optr.inClusterBringup {
		if !cache.WaitForCacheSync(stopCh,
			optr.ccListerSynced,
		) {
			klog.Error("failed to sync caches")
			return
		}
	}

	klog.Info("Starting MachineConfigOperator")
	defer klog.Info("Shutting down MachineConfigOperator")

	optr.stopCh = stopCh

	for i := 0; i < workers; i++ {
		go wait.Until(optr.worker, time.Second, stopCh)
	}

	<-stopCh
}

func (optr *Operator) enqueue(obj interface{}) {
	// we're filtering out config maps that are "leader" based and we don't have logic around them
	// resyncing on these causes the operator to sync every 14s for no good reason
	if cm, ok := obj.(*corev1.ConfigMap); ok {
		if cm.GetAnnotations() != nil && cm.GetAnnotations()[resourcelock.LeaderElectionRecordAnnotationKey] != "" {
			return
		}
		if cm.Name == "kube-apiserver-server-ca" && cm.Namespace == "openshift-config-managed" {
			klog.Info("Change observed to kube-apiserver-server-ca")
		}
	}

	workQueueKey := fmt.Sprintf("%s/%s", optr.namespace, optr.name)
	optr.queue.Add(workQueueKey)
}

func (optr *Operator) eventHandler() cache.ResourceEventHandler {
	return cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			optr.enqueue(obj)
		},
		UpdateFunc: func(_, newObj interface{}) {
			optr.enqueue(newObj)
		},
		DeleteFunc: func(obj interface{}) {
			optr.enqueue(obj)
		},
	}
}

func (optr *Operator) worker() {
	for optr.processNextWorkItem() {
	}
}

func (optr *Operator) processNextWorkItem() bool {
	key, quit := optr.queue.Get()
	if quit {
		return false
	}
	defer optr.queue.Done(key)

	err := optr.syncHandler(key)
	optr.handleErr(err, key)

	return true
}

func (optr *Operator) handleErr(err error, key string) {
	if err == nil {
		optr.queue.Forget(key)
		return
	}

	if optr.queue.NumRequeues(key) < maxRetries {
		klog.V(2).Infof("Error syncing operator %v: %v", key, err)
		optr.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	klog.V(2).Infof("Dropping operator %q out of the queue: %v", key, err)
	optr.queue.Forget(key)
	optr.queue.AddAfter(key, 1*time.Minute)
}

// Sets the operator log level value to the one described in the value passed in
// Levels are set according to the standard described in the API repo:
// https://github.com/openshift/api/blob/83b017b06367bf8564bf94f5c6c1ad8aed5d3ab9/operator/v1/types.go#L96-L109
// This function does not return an error from dynamically seting the loglevel, it
// just logs it for the moment.
func (optr *Operator) setOperatorLogLevel(logLevelFromMachineConfiguration opv1.LogLevel) {
	newLogLevel := loglevelhelpers.LogLevelToVerbosity(logLevelFromMachineConfiguration)

	if newLogLevel != optr.logLevel {
		if err := loglevelhelpers.SetLogLevel(logLevelFromMachineConfiguration); err != nil {
			klog.Errorf("Failed to set log level to %d: %v", optr.logLevel, err)
		} else {
			klog.Infof("Log level changed from %d to %d", optr.logLevel, newLogLevel)
			// Update the operator's global flag for log level, this updates the MCO operand manifests
			optr.logLevel = newLogLevel
		}
	}
}

func (optr *Operator) sync(key string) error {
	startTime := time.Now()
	klog.V(4).Infof("Started syncing operator %q (%v)", key, startTime)
	defer func() {
		klog.V(4).Infof("Finished syncing operator %q (%v)", key, time.Since(startTime))
	}()

	// syncFuncs is the list of sync functions that are executed in order.
	// any error marks sync as failure.
	syncFuncs := []syncFunc{
		// OSImageStream must run FIRST to provide OS image information as RenderConfig will read
		// images references from OSImageStream
		{"OSImageStream", optr.syncOSImageStream},
		// "RenderConfig" should be the first one to run (except OSImageStream) as it sets the renderConfig in
		// the operator for the sync funcs below
		{"RenderConfig", optr.syncRenderConfig},
		{"MachineConfiguration", optr.syncMachineConfiguration},
		{"MachineConfigNode", optr.syncMachineConfigNodes},
		{"MachineConfigPools", optr.syncMachineConfigPools},
		{"MachineConfigDaemon", optr.syncMachineConfigDaemon},
		{"MachineConfigController", optr.syncMachineConfigController},
		{"MachineConfigServer", optr.syncMachineConfigServer},
		{"MachineOSBuilder", optr.syncMachineOSBuilder},
		// this check must always run last since it makes sure the pools are in sync/upgrading correctly
		{"RequiredPools", optr.syncRequiredMachineConfigPools},
	}

	return optr.syncAll(syncFuncs)
}
