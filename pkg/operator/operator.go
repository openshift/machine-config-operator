package operator

import (
	"context"
	"fmt"
	"time"

	opv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"

	"k8s.io/klog/v2"

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
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	mcfglistersalphav1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1alpha1"
)

const (
	// maxRetries is the number of times a machineconfig pool will be retried before it is dropped out of the queue.
	// With the current rate-limiter in use (5ms*2^(maxRetries-1)) the following numbers represent the times
	// a machineconfig pool is going to be requeued:
	//
	// 5ms, 10ms, 20ms, 40ms, 80ms, 160ms, 320ms, 640ms, 1.3s, 2.6s, 5.1s, 10.2s, 20.4s, 41s, 82s
	maxRetries = 15

	// osImageConfigMapName is the name of our configmap for the osImageURL
	osImageConfigMapName = "machine-config-osimageurl"
)

// Operator defines machince config operator.
type Operator struct {
	namespace, name string

	inClusterBringup bool

	imagesFile string

	vStore *versionStore

	operatorHealthEvents record.EventRecorder

	client        mcfgclientset.Interface
	kubeClient    kubernetes.Interface
	apiExtClient  apiextclientset.Interface
	configClient  configclientset.Interface
	eventRecorder record.EventRecorder
	libgoRecorder events.Recorder

	syncHandler func(ic string) error

	imgLister        configlistersv1.ImageLister
	crdLister        apiextlistersv1.CustomResourceDefinitionLister
	mcpLister        mcfglistersv1.MachineConfigPoolLister
	msLister         mcfglistersalphav1.MachineConfigNodeLister
	ccLister         mcfglistersv1.ControllerConfigLister
	mcLister         mcfglistersv1.MachineConfigLister
	deployLister     appslisterv1.DeploymentLister
	daemonsetLister  appslisterv1.DaemonSetLister
	infraLister      configlistersv1.InfrastructureLister
	networkLister    configlistersv1.NetworkLister
	mcoCmLister      corelisterv1.ConfigMapLister
	clusterCmLister  corelisterv1.ConfigMapLister
	proxyLister      configlistersv1.ProxyLister
	oseKubeAPILister corelisterv1.ConfigMapLister
	nodeLister       corelisterv1.NodeLister
	dnsLister        configlistersv1.DNSLister
	mcoSALister      corelisterv1.ServiceAccountLister
	mcoSecretLister  corelisterv1.SecretLister
	ocSecretLister   corelisterv1.SecretLister
	mcoCOLister      configlistersv1.ClusterOperatorLister

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
	mcoSAListerSynced                cache.InformerSynced
	mcoSecretListerSynced            cache.InformerSynced
	ocSecretListerSynced             cache.InformerSynced
	mcoCOListerSynced                cache.InformerSynced

	// queue only ever has one item, but it has nice error handling backoff/retry semantics
	queue workqueue.RateLimitingInterface

	stopCh <-chan struct{}

	renderConfig *renderConfig

	fgAccessor featuregates.FeatureGateAccess
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
	mcoSAInformer coreinformersv1.ServiceAccountInformer,
	mcoSecretInformer coreinformersv1.SecretInformer,
	ocSecretInformer coreinformersv1.SecretInformer,
	mcoCOInformer configinformersv1.ClusterOperatorInformer,
	fgAccess featuregates.FeatureGateAccess,
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
		eventRecorder: ctrlcommon.NamespacedEventRecorder(eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineconfigoperator"})),
		libgoRecorder: events.NewRecorder(kubeClient.CoreV1().Events(ctrlcommon.MCONamespace), "machine-config-operator", &corev1.ObjectReference{
			Kind:       "Deployment",
			Name:       "machine-config-operator",
			Namespace:  ctrlcommon.MCONamespace,
			APIVersion: "apps/v1",
		}),
		queue:      workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "machineconfigoperator"),
		fgAccessor: fgAccess,
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
		mcoCOInformer.Informer(),
	}

	// this is for the FG
	//	informers = append(informers, moscInformer.Informer())

	for _, i := range informers {
		i.AddEventHandler(optr.eventHandler())
	}

	optr.syncHandler = optr.sync

	optr.imgLister = imgInformer.Lister()
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

	optr.imgListerSynced = imgInformer.Informer().HasSynced
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
	optr.ocSecretLister = ocSecretInformer.Lister()
	optr.ocSecretListerSynced = ocSecretInformer.Informer().HasSynced
	optr.mcoCOLister = mcoCOInformer.Lister()
	optr.mcoCOListerSynced = mcoCOInformer.Informer().HasSynced

	optr.vStore.Set("operator", version.ReleaseVersion)

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

	cacheSynced := []cache.InformerSynced{optr.crdListerSynced,
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
		optr.mcoSAListerSynced,
		optr.mcoSecretListerSynced,
		optr.ocSecretListerSynced,
		optr.mcoCOListerSynced}

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
		UpdateFunc: func(old, new interface{}) {

			optr.enqueue(new)
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

	err := optr.syncHandler(key.(string))
	optr.handleErr(err, key)

	return true
}

func (optr *Operator) handleErr(err error, key interface{}) {
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

func (optr *Operator) sync(key string) error {
	startTime := time.Now()
	klog.V(4).Infof("Started syncing operator %q (%v)", key, startTime)
	defer func() {
		klog.V(4).Infof("Finished syncing operator %q (%v)", key, time.Since(startTime))
	}()

	// syncFuncs is the list of sync functions that are executed in order.
	// any error marks sync as failure.
	var syncFuncs = []syncFunc{
		// "RenderConfig" must always run first as it sets the renderConfig in the operator
		// for the sync funcs below
		{"RenderConfig", optr.syncRenderConfig},
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
