package operator

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/golang/glog"

	configclientset "github.com/openshift/client-go/config/clientset/versioned"
	operatorv1 "github.com/openshift/client-go/operator/informers/externalversions/operator/v1"
	operatorlisterv1 "github.com/openshift/client-go/operator/listers/operator/v1"
	corev1 "k8s.io/api/core/v1"
	apiextclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextinformersv1beta1 "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions/apiextensions/v1beta1"
	apiextlistersv1beta1 "k8s.io/apiextensions-apiserver/pkg/client/listers/apiextensions/v1beta1"
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

	// osImageConfigMapName is the name of our configmap for the osImageURL
	osImageConfigMapName = "machine-config-osimageurl"
)

// Operator defines machince config operator.
type Operator struct {
	namespace, name string

	inClusterBringup bool

	imagesFile string

	vStore *versionStore

	client        mcfgclientset.Interface
	kubeClient    kubernetes.Interface
	apiExtClient  apiextclientset.Interface
	configClient  configclientset.Interface
	eventRecorder record.EventRecorder

	syncHandler func(ic string) error

	crdLister        apiextlistersv1beta1.CustomResourceDefinitionLister
	mcpLister        mcfglistersv1.MachineConfigPoolLister
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
	etcdLister       operatorlisterv1.EtcdLister

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
	etcdSynced                       cache.InformerSynced

	// queue only ever has one item, but it has nice error handling backoff/retry semantics
	queue workqueue.RateLimitingInterface

	stopCh <-chan struct{}

	renderConfig *renderConfig
}

// New returns a new machine config operator.
func New(
	namespace, name, imagesFile string,
	mcpInformer mcfginformersv1.MachineConfigPoolInformer,
	mcInformer mcfginformersv1.MachineConfigInformer,
	controllerConfigInformer mcfginformersv1.ControllerConfigInformer,
	serviceAccountInfomer coreinformersv1.ServiceAccountInformer,
	crdInformer apiextinformersv1beta1.CustomResourceDefinitionInformer,
	deployInformer appsinformersv1.DeploymentInformer,
	daemonsetInformer appsinformersv1.DaemonSetInformer,
	clusterRoleInformer rbacinformersv1.ClusterRoleInformer,
	clusterRoleBindingInformer rbacinformersv1.ClusterRoleBindingInformer,
	mcoCmInformer,
	clusterCmInfomer coreinformersv1.ConfigMapInformer,
	infraInformer configinformersv1.InfrastructureInformer,
	networkInformer configinformersv1.NetworkInformer,
	proxyInformer configinformersv1.ProxyInformer,
	client mcfgclientset.Interface,
	kubeClient kubernetes.Interface,
	apiExtClient apiextclientset.Interface,
	configClient configclientset.Interface,
	oseKubeAPIInformer coreinformersv1.ConfigMapInformer,
	etcdInformer operatorv1.EtcdInformer,
) *Operator {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.Infof)
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
		eventRecorder: eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineconfigoperator"}),
		queue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "machineconfigoperator"),
	}

	for _, i := range []cache.SharedIndexInformer{
		controllerConfigInformer.Informer(),
		serviceAccountInfomer.Informer(),
		crdInformer.Informer(),
		deployInformer.Informer(),
		daemonsetInformer.Informer(),
		clusterRoleInformer.Informer(),
		clusterRoleBindingInformer.Informer(),
		mcoCmInformer.Informer(),
		infraInformer.Informer(),
		networkInformer.Informer(),
		mcpInformer.Informer(),
		proxyInformer.Informer(),
		oseKubeAPIInformer.Informer(),
	} {
		i.AddEventHandler(optr.eventHandler())
	}

	optr.syncHandler = optr.sync

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
	if etcdInformer != nil {
		optr.etcdLister = etcdInformer.Lister()
		optr.etcdSynced = etcdInformer.Informer().HasSynced
		etcdInformer.Informer().AddEventHandler(optr.eventHandler())
	} else {
		optr.etcdLister = nil
		optr.etcdSynced = func() bool {
			// if etcd is not part of CVO it will return true immediately
			return true
		}
	}

	optr.vStore.Set("operator", os.Getenv("RELEASE_VERSION"))

	return optr
}

// Run runs the machine config operator.
func (optr *Operator) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer optr.queue.ShutDown()

	apiClient := optr.apiExtClient.ApiextensionsV1beta1()
	_, err := apiClient.CustomResourceDefinitions().Get(context.TODO(), "machineconfigpools.machineconfiguration.openshift.io", metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			glog.Infof("Couldn't find machineconfigpool CRD, in cluster bringup mode")
			optr.inClusterBringup = true
		} else {
			glog.Errorf("While checking for cluster bringup: %v", err)
		}
	}

	if !cache.WaitForCacheSync(stopCh,
		optr.crdListerSynced,
		optr.deployListerSynced,
		optr.daemonsetListerSynced,
		optr.infraListerSynced,
		optr.mcoCmListerSynced,
		optr.clusterCmListerSynced,
		optr.serviceAccountInformerSynced,
		optr.clusterRoleInformerSynced,
		optr.clusterRoleBindingInformerSynced,
		optr.networkListerSynced,
		optr.proxyListerSynced,
		optr.oseKubeAPIListerSynced,
		optr.etcdSynced) {
		glog.Error("failed to sync caches")
		return
	}

	// these can only be synced after CRDs are installed
	if !optr.inClusterBringup {
		if !cache.WaitForCacheSync(stopCh,
			optr.mcpListerSynced,
			optr.ccListerSynced,
			optr.mcListerSynced,
		) {
			glog.Error("failed to sync caches")
			return
		}
	}

	glog.Info("Starting MachineConfigOperator")
	defer glog.Info("Shutting down MachineConfigOperator")

	optr.stopCh = stopCh

	for i := 0; i < workers; i++ {
		go wait.Until(optr.worker, time.Second, stopCh)
	}

	<-stopCh
}

func (optr *Operator) enqueue(obj interface{}) {
	// we're filtering out config maps that are "leader" based and we don't have logic around them
	// resyncing on these causes the operator to sync every 14s for no good reason
	if cm, ok := obj.(*corev1.ConfigMap); ok && cm.GetAnnotations() != nil && cm.GetAnnotations()[resourcelock.LeaderElectionRecordAnnotationKey] != "" {
		return
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
		glog.V(2).Infof("Error syncing operator %v: %v", key, err)
		optr.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	glog.V(2).Infof("Dropping operator %q out of the queue: %v", key, err)
	optr.queue.Forget(key)
	optr.queue.AddAfter(key, 1*time.Minute)
}

func (optr *Operator) sync(key string) error {
	startTime := time.Now()
	glog.V(4).Infof("Started syncing operator %q (%v)", key, startTime)
	defer func() {
		glog.V(4).Infof("Finished syncing operator %q (%v)", key, time.Since(startTime))
	}()

	// syncFuncs is the list of sync functions that are executed in order.
	// any error marks sync as failure.
	var syncFuncs = []syncFunc{
		// "RenderConfig" must always run first as it sets the renderConfig in the operator
		// for the sync funcs below
		{"RenderConfig", optr.syncRenderConfig},
		{"MachineConfigPools", optr.syncMachineConfigPools},
		{"MachineConfigDaemon", optr.syncMachineConfigDaemon},
		{"MachineConfigController", optr.syncMachineConfigController},
		{"MachineConfigServer", optr.syncMachineConfigServer},
		// this check must always run last since it makes sure the pools are in sync/upgrading correctly
		{"RequiredPools", optr.syncRequiredMachineConfigPools},
	}
	return optr.syncAll(syncFuncs)
}
