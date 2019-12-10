package common

import (
	"math/rand"
	"time"

	"github.com/golang/glog"
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	operatorclientset "github.com/openshift/client-go/operator/clientset/versioned"
	operatorinformers "github.com/openshift/client-go/operator/informers/externalversions"
	operatorv1 "github.com/openshift/client-go/operator/informers/externalversions/operator/v1"
	"github.com/openshift/machine-config-operator/internal/clients"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	mcfginformers "github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions"
	apiextinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/informers"
)

const (
	minResyncPeriod = 20 * time.Minute
)

func resyncPeriod() func() time.Duration {
	return func() time.Duration {
		factor := rand.Float64() + 1
		return time.Duration(float64(minResyncPeriod.Nanoseconds()) * factor)
	}
}

// ControllerContext stores all the informers for a variety of kubernetes objects.
type ControllerContext struct {
	ClientBuilder *clients.Builder

	NamespacedInformerFactory                           mcfginformers.SharedInformerFactory
	InformerFactory                                     mcfginformers.SharedInformerFactory
	KubeInformerFactory                                 informers.SharedInformerFactory
	KubeNamespacedInformerFactory                       informers.SharedInformerFactory
	OpenShiftConfigKubeNamespacedInformerFactory        informers.SharedInformerFactory
	OpenShiftKubeAPIServerKubeNamespacedInformerFactory informers.SharedInformerFactory
	APIExtInformerFactory                               apiextinformers.SharedInformerFactory
	ConfigInformerFactory                               configinformers.SharedInformerFactory
	OperatorInformerFactory                             operatorinformers.SharedInformerFactory
	EtcdInformer                                        operatorv1.EtcdInformer

	AvailableResources map[schema.GroupVersionResource]bool

	Stop <-chan struct{}

	InformersStarted chan struct{}

	ResyncPeriod func() time.Duration
}

// CreateControllerContext creates the ControllerContext with the ClientBuilder.
func CreateControllerContext(cb *clients.Builder, stop <-chan struct{}, targetNamespace string) *ControllerContext {
	client := cb.MachineConfigClientOrDie("machine-config-shared-informer")
	kubeClient := cb.KubeClientOrDie("kube-shared-informer")
	apiExtClient := cb.APIExtClientOrDie("apiext-shared-informer")
	configClient := cb.ConfigClientOrDie("config-shared-informer")
	operatorClient := cb.OperatorClientOrDie("operator-shared-informer")
	sharedInformers := mcfginformers.NewSharedInformerFactory(client, resyncPeriod()())
	sharedNamespacedInformers := mcfginformers.NewFilteredSharedInformerFactory(client, resyncPeriod()(), targetNamespace, nil)
	kubeSharedInformer := informers.NewSharedInformerFactory(kubeClient, resyncPeriod()())
	kubeNamespacedSharedInformer := informers.NewFilteredSharedInformerFactory(kubeClient, resyncPeriod()(), targetNamespace, nil)
	openShiftConfigKubeNamespacedSharedInformer := informers.NewFilteredSharedInformerFactory(kubeClient, resyncPeriod()(), "openshift-config", nil)
	openShiftKubeAPIServerKubeNamespacedSharedInformer := informers.NewFilteredSharedInformerFactory(kubeClient,
		resyncPeriod()(),
		"openshift-kube-apiserver-operator",
		func(opt *metav1.ListOptions) {
			opt.FieldSelector = fields.OneTermEqualSelector("metadata.name", "kube-apiserver-to-kubelet-client-ca").String()
		},
	)

	// filter out CRDs that do not have the MCO label
	assignFilterLabels := func(opts *metav1.ListOptions) {
		labelsMap, err := labels.ConvertSelectorToLabelsMap(opts.LabelSelector)
		if err != nil {
			glog.Warningf("unable to convert selector %q to map: %v", opts.LabelSelector, err)
			return
		}
		opts.LabelSelector = labels.Merge(labelsMap, map[string]string{daemonconsts.OpenShiftOperatorManagedLabel: ""}).String()
	}
	apiExtSharedInformer := apiextinformers.NewSharedInformerFactoryWithOptions(apiExtClient, resyncPeriod()(),
		apiextinformers.WithNamespace(targetNamespace), apiextinformers.WithTweakListOptions(assignFilterLabels))
	configSharedInformer := configinformers.NewSharedInformerFactory(configClient, resyncPeriod()())
	operatorSharedInformer := operatorinformers.NewSharedInformerFactory(operatorClient, resyncPeriod()())

	etcdInformer := getEtcdInformer(operatorClient, operatorSharedInformer)

	return &ControllerContext{
		ClientBuilder:                                       cb,
		NamespacedInformerFactory:                           sharedNamespacedInformers,
		InformerFactory:                                     sharedInformers,
		KubeInformerFactory:                                 kubeSharedInformer,
		KubeNamespacedInformerFactory:                       kubeNamespacedSharedInformer,
		OpenShiftConfigKubeNamespacedInformerFactory:        openShiftConfigKubeNamespacedSharedInformer,
		OpenShiftKubeAPIServerKubeNamespacedInformerFactory: openShiftKubeAPIServerKubeNamespacedSharedInformer,
		APIExtInformerFactory:                               apiExtSharedInformer,
		ConfigInformerFactory:                               configSharedInformer,
		OperatorInformerFactory:                             operatorSharedInformer,
		EtcdInformer:                                        etcdInformer,
		Stop:                                                stop,
		InformersStarted:                                    make(chan struct{}),
		ResyncPeriod:                                        resyncPeriod(),
	}
}

func getEtcdInformer(operatorClient operatorclientset.Interface, operatorSharedInformer operatorinformers.SharedInformerFactory) operatorv1.EtcdInformer {
	operatorGroups, err := operatorClient.Discovery().ServerResourcesForGroupVersion("operator.openshift.io/v1")
	if err != nil {
		glog.Errorf("unable to get operatorGroups: %#v", err)
		return nil
	}

	for _, o := range operatorGroups.APIResources {
		if o.Kind == "Etcd" {
			return operatorSharedInformer.Operator().V1().Etcds()
		}
	}
	return nil
}
