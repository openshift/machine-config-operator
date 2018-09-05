package operator

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"time"

	"github.com/golang/glog"
	securityclientset "github.com/openshift/client-go/security/clientset/versioned"
	securityinformersv1 "github.com/openshift/client-go/security/informers/externalversions/security/v1"
	cvoclientset "github.com/openshift/cluster-version-operator/pkg/generated/clientset/versioned"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	mcfgclientset "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/scheme"
	mcfginformersv1 "github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions/machineconfiguration.openshift.io/v1"
	mcfglistersv1 "github.com/openshift/machine-config-operator/pkg/generated/listers/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/version"
	"k8s.io/api/core/v1"
	apiextclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextinformersv1beta1 "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions/apiextensions/v1beta1"
	apiextlistersv1beta1 "k8s.io/apiextensions-apiserver/pkg/client/listers/apiextensions/v1beta1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	appsinformersv1 "k8s.io/client-go/informers/apps/v1"
	coreinformersv1 "k8s.io/client-go/informers/core/v1"
	rbacinformersv1 "k8s.io/client-go/informers/rbac/v1"
	"k8s.io/client-go/kubernetes"
	coreclientsetv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	appslisterv1 "k8s.io/client-go/listers/apps/v1"
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

// Operator defines machince config operator.
type Operator struct {
	namespace, name string

	etcdCAFile, rootCAFile string

	imagesFile string

	client         mcfgclientset.Interface
	kubeClient     kubernetes.Interface
	securityClient securityclientset.Interface
	apiExtClient   apiextclientset.Interface
	cvoClient      cvoclientset.Interface
	eventRecorder  record.EventRecorder

	syncHandler func(ic string) error

	crdLister       apiextlistersv1beta1.CustomResourceDefinitionLister
	mcoconfigLister mcfglistersv1.MCOConfigLister
	deployLister    appslisterv1.DeploymentLister
	daemonsetLister appslisterv1.DaemonSetLister

	crdListerSynced       cache.InformerSynced
	mcoconfigListerSynced cache.InformerSynced
	deployListerSynced    cache.InformerSynced
	daemonsetListerSynced cache.InformerSynced

	// queue only ever has one item, but it has nice error handling backoff/retry semantics
	queue workqueue.RateLimitingInterface
}

// New returns a new machine config operator.
func New(
	namespace, name string,
	etcdCAFile, rootCAFile string,
	imagesFile string,
	mcoconfigInformer mcfginformersv1.MCOConfigInformer,
	controllerConfigInformer mcfginformersv1.ControllerConfigInformer,
	configMapInformer coreinformersv1.ConfigMapInformer,
	serviceAccountInfomer coreinformersv1.ServiceAccountInformer,
	crdInformer apiextinformersv1beta1.CustomResourceDefinitionInformer,
	deployInformer appsinformersv1.DeploymentInformer,
	daemonsetInformer appsinformersv1.DaemonSetInformer,
	clusterRoleInformer rbacinformersv1.ClusterRoleInformer,
	clusterRoleBindingInformer rbacinformersv1.ClusterRoleBindingInformer,
	securityInformer securityinformersv1.SecurityContextConstraintsInformer,
	client mcfgclientset.Interface,
	kubeClient kubernetes.Interface,
	securityClient securityclientset.Interface,
	apiExtClient apiextclientset.Interface,
	cvoClient cvoclientset.Interface,
) *Operator {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.Infof)
	eventBroadcaster.StartRecordingToSink(&coreclientsetv1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	optr := &Operator{
		namespace:      namespace,
		name:           name,
		etcdCAFile:     etcdCAFile,
		rootCAFile:     rootCAFile,
		client:         client,
		kubeClient:     kubeClient,
		securityClient: securityClient,
		apiExtClient:   apiExtClient,
		cvoClient:      cvoClient,
		eventRecorder:  eventBroadcaster.NewRecorder(scheme.Scheme, v1.EventSource{Component: "machineconfigoperator"}),
		queue:          workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "machineconfigoperator"),
	}

	mcoconfigInformer.Informer().AddEventHandler(optr.eventHandler())
	controllerConfigInformer.Informer().AddEventHandler(optr.eventHandler())
	configMapInformer.Informer().AddEventHandler(optr.eventHandler())
	serviceAccountInfomer.Informer().AddEventHandler(optr.eventHandler())
	crdInformer.Informer().AddEventHandler(optr.eventHandler())
	deployInformer.Informer().AddEventHandler(optr.eventHandler())
	daemonsetInformer.Informer().AddEventHandler(optr.eventHandler())
	clusterRoleInformer.Informer().AddEventHandler(optr.eventHandler())
	clusterRoleBindingInformer.Informer().AddEventHandler(optr.eventHandler())
	securityInformer.Informer().AddEventHandler(optr.eventHandler())

	optr.syncHandler = optr.sync

	optr.crdLister = crdInformer.Lister()
	optr.crdListerSynced = crdInformer.Informer().HasSynced
	optr.mcoconfigLister = mcoconfigInformer.Lister()
	optr.mcoconfigListerSynced = mcoconfigInformer.Informer().HasSynced
	optr.deployLister = deployInformer.Lister()
	optr.deployListerSynced = deployInformer.Informer().HasSynced
	optr.daemonsetLister = daemonsetInformer.Lister()
	optr.daemonsetListerSynced = daemonsetInformer.Informer().HasSynced

	return optr
}

// Run runs the machine config operator.
func (optr *Operator) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer optr.queue.ShutDown()

	glog.Info("Starting MachineConfigOperator")
	defer glog.Info("Shutting down MachineConfigOperator")

	if !cache.WaitForCacheSync(stopCh,
		optr.crdListerSynced,
		optr.mcoconfigListerSynced,
		optr.deployListerSynced,
		optr.daemonsetListerSynced) {
		glog.Error("failed to sync caches")
		return
	}

	for i := 0; i < workers; i++ {
		go wait.Until(optr.worker, time.Second, stopCh)
	}

	<-stopCh
}

func (optr *Operator) eventHandler() cache.ResourceEventHandler {
	workQueueKey := fmt.Sprintf("%s/%s", optr.namespace, optr.name)
	return cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { optr.queue.Add(workQueueKey) },
		UpdateFunc: func(old, new interface{}) { optr.queue.Add(workQueueKey) },
		DeleteFunc: func(obj interface{}) { optr.queue.Add(workQueueKey) },
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
		//TODO: set operator Done.

		optr.queue.Forget(key)
		return
	}

	//TODO: set operator degraded.

	if optr.queue.NumRequeues(key) < maxRetries {
		glog.V(2).Infof("Error syncing operator %v: %v", key, err)
		optr.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	glog.V(2).Infof("Dropping operator %q out of the queue: %v", key, err)
	optr.queue.Forget(key)
}

func (optr *Operator) sync(key string) error {
	startTime := time.Now()
	glog.V(4).Infof("Started syncing operator %q (%v)", key, startTime)
	defer func() {
		glog.V(4).Infof("Finished syncing operator %q (%v)", key, time.Since(startTime))
	}()

	if err := optr.syncCustomResourceDefinitions(); err != nil {
		return err
	}

	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}

	obj, err := optr.mcoconfigLister.MCOConfigs(namespace).Get(name)
	if err != nil {
		return err
	}

	mcoconfig := obj.DeepCopy()

	filesData := map[string][]byte{}
	files := []string{
		optr.rootCAFile,
		optr.etcdCAFile,
		optr.imagesFile,
	}
	for _, file := range files {
		data, err := ioutil.ReadFile(file)
		if err != nil {
			return err
		}
		filesData[file] = data
	}

	var imgs images
	if err := json.Unmarshal(filesData[optr.imagesFile], &imgs); err != nil {
		return err
	}

	rc := getRenderConfig(mcoconfig, filesData[optr.etcdCAFile], filesData[optr.rootCAFile], imgs)
	return optr.syncAll(rc)
}

func getRenderConfig(mc *mcfgv1.MCOConfig, etcdCAData, rootCAData []byte, imgs images) renderConfig {
	controllerconfig := mcfgv1.ControllerConfigSpec{
		ClusterDNSIP:        mc.Spec.ClusterDNSIP,
		CloudProviderConfig: mc.Spec.CloudProviderConfig,
		ClusterName:         mc.Spec.ClusterName,
		Platform:            mc.Spec.Platform,
		BaseDomain:          mc.Spec.BaseDomain,
		EtcdInitialCount:    mc.Spec.EtcdInitialCount,
		EtcdCAData:          etcdCAData,
		RootCAData:          rootCAData,
	}
	return renderConfig{
		TargetNamespace:  mc.GetNamespace(),
		Version:          version.Raw,
		ControllerConfig: controllerconfig,
		Images:           imgs,
	}
}
