package operator

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"time"

	"github.com/golang/glog"

	configclientset "github.com/openshift/client-go/config/clientset/versioned"
	v1 "k8s.io/api/core/v1"
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
	policylisterv1 "k8s.io/client-go/listers/policy/v1beta1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"

	configv1 "github.com/openshift/api/config/v1"
	configinformersv1 "github.com/openshift/client-go/config/informers/externalversions/config/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"

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

	inClusterBringup bool

	imagesFile string

	vStore *versionStore

	client        mcfgclientset.Interface
	kubeClient    kubernetes.Interface
	apiExtClient  apiextclientset.Interface
	configClient  configclientset.Interface
	eventRecorder record.EventRecorder

	syncHandler func(ic string) error

	crdLister       apiextlistersv1beta1.CustomResourceDefinitionLister
	mcpLister       mcfglistersv1.MachineConfigPoolLister
	ccLister        mcfglistersv1.ControllerConfigLister
	mcLister        mcfglistersv1.MachineConfigLister
	deployLister    appslisterv1.DeploymentLister
	daemonsetLister appslisterv1.DaemonSetLister
	infraLister     configlistersv1.InfrastructureLister
	networkLister   configlistersv1.NetworkLister
	mcoCmLister     corelisterv1.ConfigMapLister
	clusterCmLister corelisterv1.ConfigMapLister
	pdbLister	policylisterv1.PodDisruptionBudgetLister

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

	// queue only ever has one item, but it has nice error handling backoff/retry semantics
	queue workqueue.RateLimitingInterface

	stopCh <-chan struct{}
}

// New returns a new machine config operator.
func New(
	namespace, name string,
	imagesFile string,
	mcpInformer mcfginformersv1.MachineConfigPoolInformer,
	mcInformer mcfginformersv1.MachineConfigInformer,
	controllerConfigInformer mcfginformersv1.ControllerConfigInformer,
	serviceAccountInfomer coreinformersv1.ServiceAccountInformer,
	crdInformer apiextinformersv1beta1.CustomResourceDefinitionInformer,
	deployInformer appsinformersv1.DeploymentInformer,
	daemonsetInformer appsinformersv1.DaemonSetInformer,
	clusterRoleInformer rbacinformersv1.ClusterRoleInformer,
	clusterRoleBindingInformer rbacinformersv1.ClusterRoleBindingInformer,
	mcoCmInformer coreinformersv1.ConfigMapInformer,
	clusterCmInfomer coreinformersv1.ConfigMapInformer,
	infraInformer configinformersv1.InfrastructureInformer,
	networkInformer configinformersv1.NetworkInformer,
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
		vStore:        newVersionStore(),
		client:        client,
		kubeClient:    kubeClient,
		apiExtClient:  apiExtClient,
		configClient:  configClient,
		eventRecorder: eventBroadcaster.NewRecorder(scheme.Scheme, v1.EventSource{Component: "machineconfigoperator"}),
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

	optr.vStore.Set("operator", os.Getenv("RELEASE_VERSION"))

	return optr
}

// Run runs the machine config operator.
func (optr *Operator) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer optr.queue.ShutDown()

	apiClient := optr.apiExtClient.ApiextensionsV1beta1()
	_, err := apiClient.CustomResourceDefinitions().Get("machineconfigpools.machineconfiguration.openshift.io", metav1.GetOptions{})
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
		optr.networkListerSynced) {
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

	if err := optr.syncCustomResourceDefinitions(); err != nil {
		return err
	}

	if optr.inClusterBringup {
		glog.V(4).Info("Starting inClusterBringup informers cache sync")
		// sync now our own informers after having installed the CRDs
		if !cache.WaitForCacheSync(optr.stopCh, optr.mcpListerSynced, optr.mcListerSynced, optr.ccListerSynced) {
			return errors.New("failed to sync caches for informers")
		}
		glog.V(4).Info("Finished inClusterBringup informers cache sync")
	}

	namespace, _, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}

	// sync up the images used by operands.
	imgsRaw, err := ioutil.ReadFile(optr.imagesFile)
	if err != nil {
		return err
	}
	imgs := Images{}
	if err := json.Unmarshal(imgsRaw, &imgs); err != nil {
		return err
	}

	// sync up CAs
	etcdCA, err := optr.getCAsFromConfigMap("openshift-config", "etcd-serving-ca", "ca-bundle.crt")
	if err != nil {
		return err
	}
	etcdMetricCA, err := optr.getCAsFromConfigMap("openshift-config", "etcd-metric-serving-ca", "ca-bundle.crt")
	if err != nil {
		return err
	}
	rootCA, err := optr.getCAsFromConfigMap("kube-system", "root-ca", "ca.crt")
	if err != nil {
		return err
	}
	// as described by the name this is essentially static, but it no worse than what was here before.  Since changes disrupt workloads
	// and since must perfectly match what the installer creates, this is effectively frozen in time.
	kubeAPIServerServingCABytes, err := optr.getCAsFromConfigMap("openshift-config", "initial-kube-apiserver-server-ca", "ca-bundle.crt")
	if err != nil {
		return err
	}
	bundle := make([]byte, 0)
	bundle = append(bundle, rootCA...)
	bundle = append(bundle, kubeAPIServerServingCABytes...)

	// sync up os image url
	// TODO: this should probably be part of the imgs
	osimageurl, err := optr.getOsImageURL(namespace)
	if err != nil {
		return err
	}
	imgs.MachineOSContent = osimageurl

	// sync up the ControllerConfigSpec
	infra, network, err := optr.getGlobalConfig()
	if err != nil {
		return err
	}
	spec, err := createDiscoveredControllerConfigSpec(infra, network)
	if err != nil {
		return err
	}

	spec.EtcdCAData = etcdCA
	spec.EtcdMetricCAData = etcdMetricCA
	spec.RootCAData = bundle
	spec.PullSecret = &v1.ObjectReference{Namespace: "openshift-config", Name: "pull-secret"}
	spec.OSImageURL = imgs.MachineOSContent
	spec.Images = map[string]string{
		templatectrl.EtcdImageKey:            imgs.Etcd,
		templatectrl.SetupEtcdEnvKey:         imgs.SetupEtcdEnv,
		templatectrl.InfraImageKey:           imgs.InfraImage,
		templatectrl.KubeClientAgentImageKey: imgs.KubeClientAgent,
	}

	// create renderConfig
	rc := getRenderConfig(namespace, string(kubeAPIServerServingCABytes), spec, imgs, infra.Status.APIServerURL)
	// syncFuncs is the list of sync functions that are executed in order.
	// any error marks sync as failure but continues to next syncFunc
	var syncFuncs = []syncFunc{
		{"pools", optr.syncMachineConfigPools},
		{"mcc", optr.syncMachineConfigController},
		{"mcs", optr.syncMachineConfigServer},
		{"mcd", optr.syncMachineConfigDaemon},
		{"required-pools", optr.syncRequiredMachineConfigPools},
	}
	return optr.syncAll(rc, syncFuncs)
}

func (optr *Operator) getOsImageURL(namespace string) (string, error) {
	cm, err := optr.mcoCmLister.ConfigMaps(namespace).Get(osImageConfigMapName)
	if err != nil {
		return "", err
	}
	return cm.Data["osImageURL"], nil
}

func (optr *Operator) getCAsFromConfigMap(namespace, name, key string) ([]byte, error) {
	cm, err := optr.clusterCmLister.ConfigMaps(namespace).Get(name)
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

// getGlobalConfig gets global configuration for the cluster, namely, the Infrastructure and Network types.
// Each type of global configuration is named `cluster` for easy discovery in the cluster.
func (optr *Operator) getGlobalConfig() (*configv1.Infrastructure, *configv1.Network, error) {
	infra, err := optr.infraLister.Get("cluster")
	if err != nil {
		return nil, nil, err
	}
	network, err := optr.networkLister.Get("cluster")
	if err != nil {
		return nil, nil, err
	}
	return infra, network, nil
}

func getRenderConfig(tnamespace, kubeAPIServerServingCA string, ccSpec *mcfgv1.ControllerConfigSpec, imgs Images, apiServerURL string) renderConfig {
	return renderConfig{
		TargetNamespace:        tnamespace,
		Version:                version.Raw,
		ControllerConfig:       *ccSpec,
		Images:                 imgs,
		APIServerURL:           apiServerURL,
		KubeAPIServerServingCA: kubeAPIServerServingCA,
	}
}
