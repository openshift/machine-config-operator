package operator

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"time"

	"github.com/ghodss/yaml"
	"github.com/golang/glog"

	"k8s.io/api/core/v1"
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
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"

	configclientset "github.com/openshift/client-go/config/clientset/versioned"
	installertypes "github.com/openshift/installer/pkg/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	templatectrl "github.com/openshift/machine-config-operator/pkg/controller/template"
	mcfgclientset "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/scheme"
	mcfginformersv1 "github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions/machineconfiguration.openshift.io/v1"
	mcfglistersv1 "github.com/openshift/machine-config-operator/pkg/generated/listers/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/version"
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

	imagesFile string

	client        mcfgclientset.Interface
	kubeClient    kubernetes.Interface
	apiExtClient  apiextclientset.Interface
	configClient  configclientset.Interface
	eventRecorder record.EventRecorder

	syncHandler func(ic string) error

	crdLister       apiextlistersv1beta1.CustomResourceDefinitionLister
	mcoconfigLister mcfglistersv1.MCOConfigLister
	mcpLister       mcfglistersv1.MachineConfigPoolLister
	ccLister        mcfglistersv1.ControllerConfigLister
	mcLister        mcfglistersv1.MachineConfigLister
	deployLister    appslisterv1.DeploymentLister
	daemonsetLister appslisterv1.DaemonSetLister
	cmLister        corelisterv1.ConfigMapLister

	crdListerSynced       cache.InformerSynced
	mcoconfigListerSynced cache.InformerSynced
	deployListerSynced    cache.InformerSynced
	daemonsetListerSynced cache.InformerSynced
	cmListerSynced        cache.InformerSynced

	// queue only ever has one item, but it has nice error handling backoff/retry semantics
	queue workqueue.RateLimitingInterface
}

// New returns a new machine config operator.
func New(
	namespace, name string,
	imagesFile string,
	mcoconfigInformer mcfginformersv1.MCOConfigInformer,
	mcpInformer mcfginformersv1.MachineConfigPoolInformer,
	ccInformer mcfginformersv1.ControllerConfigInformer,
	mcInformer mcfginformersv1.MachineConfigInformer,
	controllerConfigInformer mcfginformersv1.ControllerConfigInformer,
	serviceAccountInfomer coreinformersv1.ServiceAccountInformer,
	crdInformer apiextinformersv1beta1.CustomResourceDefinitionInformer,
	deployInformer appsinformersv1.DeploymentInformer,
	daemonsetInformer appsinformersv1.DaemonSetInformer,
	clusterRoleInformer rbacinformersv1.ClusterRoleInformer,
	clusterRoleBindingInformer rbacinformersv1.ClusterRoleBindingInformer,
	cmInformer coreinformersv1.ConfigMapInformer,
	client mcfgclientset.Interface,
	kubeClient kubernetes.Interface,
	apiExtClient apiextclientset.Interface,
	configClient configclientset.Interface,
) *Operator {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.Infof)
	eventBroadcaster.StartRecordingToSink(&coreclientsetv1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	optr := &Operator{
		namespace:     namespace,
		name:          name,
		imagesFile:    imagesFile,
		client:        client,
		kubeClient:    kubeClient,
		apiExtClient:  apiExtClient,
		configClient:  configClient,
		eventRecorder: eventBroadcaster.NewRecorder(scheme.Scheme, v1.EventSource{Component: "machineconfigoperator"}),
		queue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "machineconfigoperator"),
	}

	mcoconfigInformer.Informer().AddEventHandler(optr.eventHandler())
	controllerConfigInformer.Informer().AddEventHandler(optr.eventHandler())
	serviceAccountInfomer.Informer().AddEventHandler(optr.eventHandler())
	crdInformer.Informer().AddEventHandler(optr.eventHandler())
	deployInformer.Informer().AddEventHandler(optr.eventHandler())
	daemonsetInformer.Informer().AddEventHandler(optr.eventHandler())
	clusterRoleInformer.Informer().AddEventHandler(optr.eventHandler())
	clusterRoleBindingInformer.Informer().AddEventHandler(optr.eventHandler())
	cmInformer.Informer().AddEventHandler(optr.eventHandler())

	optr.syncHandler = optr.sync

	optr.crdLister = crdInformer.Lister()
	optr.crdListerSynced = crdInformer.Informer().HasSynced
	optr.mcoconfigLister = mcoconfigInformer.Lister()
	optr.mcoconfigListerSynced = mcoconfigInformer.Informer().HasSynced
	optr.mcpLister = mcpInformer.Lister()
	optr.ccLister = ccInformer.Lister()
	optr.mcLister = mcInformer.Lister()
	optr.deployLister = deployInformer.Lister()
	optr.deployListerSynced = deployInformer.Informer().HasSynced
	optr.daemonsetLister = daemonsetInformer.Lister()
	optr.daemonsetListerSynced = daemonsetInformer.Informer().HasSynced
	optr.cmLister = cmInformer.Lister()
	optr.cmListerSynced = cmInformer.Informer().HasSynced

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
		optr.cmListerSynced,
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
		optr.queue.Forget(key)
		return
	}

	optr.syncFailingStatus(err)

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

	var obj *mcfgv1.MCOConfig
	obj, err = optr.mcoconfigLister.MCOConfigs(namespace).Get(name)
	if apierrors.IsNotFound(err) {
		obj, err = discoverMCOConfig(optr.getInstallConfig)
		if err == nil {
			obj.SetNamespace(namespace)
			obj.SetName(name)
		}
	}
	if err != nil {
		return err
	}
	mcoconfig := obj.DeepCopy()

	imgsRaw, err := ioutil.ReadFile(optr.imagesFile)
	if err != nil {
		return err
	}
	imgs := DefaultImages()
	if err := json.Unmarshal(imgsRaw, &imgs); err != nil {
		return err
	}

	etcdCA, err := optr.getCAsFromConfigMap("kube-system", "etcd-serving-ca", "ca-bundle.crt")
	if err != nil {
		return err
	}
	rootCA, err := optr.getCAsFromConfigMap("kube-system", "root-ca", "ca.crt")
	if err != nil {
		return err
	}

	osimageurl, err := optr.getOsImageURL(namespace)
	if err != nil {
		return err
	}
	imgs.MachineOSContent = osimageurl

	rc := getRenderConfig(mcoconfig, etcdCA, rootCA, &v1.ObjectReference{Namespace: "kube-system", Name: "coreos-pull-secret"}, imgs)
	return optr.syncAll(rc)
}

func (optr *Operator) getOsImageURL(namespace string) (string, error) {
	cm, err := optr.kubeClient.CoreV1().ConfigMaps(namespace).Get(osImageConfigMapName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	return cm.Data["osImageURL"], nil
}

func (optr *Operator) getCAsFromConfigMap(namespace, name, key string) ([]byte, error) {
	cm, err := optr.kubeClient.CoreV1().ConfigMaps(namespace).Get(name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if bd, bdok := cm.BinaryData[key]; bdok {
		return bd, nil
	} else if d, dok := cm.Data[key]; dok {
		raw, err := base64.StdEncoding.DecodeString(d)
		if err != nil {
			// this is actually the result of a bad assumption.  configmap values are not encoded.
			// After the installer pull merges, this entire attempt to decode can go away.
			return []byte(d), nil
		}
		return raw, nil
	} else {
		return nil, fmt.Errorf("%s not found in %s/%s", key, namespace, name)
	}
}

func (optr *Operator) getInstallConfig() (installertypes.InstallConfig, error) {
	var (
		clusterConfigNamespace = "kube-system"
		clusterConfigName      = "cluster-config-v1"
	)
	clusterConfig, err := optr.kubeClient.CoreV1().ConfigMaps(clusterConfigNamespace).Get(clusterConfigName, metav1.GetOptions{})
	if err != nil {
		return installertypes.InstallConfig{}, err
	}
	return icFromClusterConfig(clusterConfig)
}

func icFromClusterConfig(cm *v1.ConfigMap) (installertypes.InstallConfig, error) {
	var (
		icKey = "install-config"
		ic    installertypes.InstallConfig
	)
	icData, ok := cm.Data[icKey]
	if !ok {
		return ic, fmt.Errorf("%s doesn't exist", icKey)
	}

	if err := yaml.Unmarshal([]byte(icData), &ic); err != nil {
		return ic, err
	}
	return ic, nil
}

func getRenderConfig(mc *mcfgv1.MCOConfig, etcdCAData, rootCAData []byte, ps *v1.ObjectReference, imgs Images) renderConfig {
	controllerconfig := mcfgv1.ControllerConfigSpec{
		ClusterDNSIP:        mc.Spec.ClusterDNSIP,
		CloudProviderConfig: mc.Spec.CloudProviderConfig,
		ClusterName:         mc.Spec.ClusterName,
		Platform:            mc.Spec.Platform,
		BaseDomain:          mc.Spec.BaseDomain,
		EtcdCAData:          etcdCAData,
		RootCAData:          rootCAData,
		PullSecret:          ps,
		SSHKey:              mc.Spec.SSHKey,
		OSImageURL:          imgs.MachineOSContent,
		Images: map[string]string{
			templatectrl.EtcdImageKey:    imgs.Etcd,
			templatectrl.SetupEtcdEnvKey: imgs.SetupEtcdEnv,
		},
	}
	return renderConfig{
		TargetNamespace:  mc.GetNamespace(),
		Version:          version.Raw,
		ControllerConfig: controllerconfig,
		Images:           imgs,
	}
}
