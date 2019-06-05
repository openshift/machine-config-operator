package common

import (
	"math/rand"
	"time"

	"github.com/golang/glog"
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	"github.com/openshift/machine-config-operator/internal/clients"
	mcfginformers "github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions"
	apiextinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	NamespacedInformerFactory                    mcfginformers.SharedInformerFactory
	InformerFactory                              mcfginformers.SharedInformerFactory
	KubeInformerFactory                          informers.SharedInformerFactory
	ClusterRoleAndRoleBindingsInformerFactory    informers.SharedInformerFactory
	KubeNamespacedInformerFactory                informers.SharedInformerFactory
	OpenShiftConfigKubeNamespacedInformerFactory informers.SharedInformerFactory
	APIExtInformerFactory                        apiextinformers.SharedInformerFactory
	ConfigInformerFactory                        configinformers.SharedInformerFactory

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
	sharedInformers := mcfginformers.NewSharedInformerFactory(client, resyncPeriod()())
	sharedNamespacedInformers := mcfginformers.NewFilteredSharedInformerFactory(client, resyncPeriod()(), targetNamespace, nil)
	kubeSharedInformer := informers.NewSharedInformerFactory(kubeClient, resyncPeriod()())
	kubeNamespacedSharedInformer := informers.NewFilteredSharedInformerFactory(kubeClient, resyncPeriod()(), targetNamespace, nil)
	// This informer is specifically filtering for mco-built-in labels for cluster-role and cluster-role-bindings
	filterClusterRoleAndClusterRoleBindings := func(opts *metav1.ListOptions) {
		ls, err := labels.ConvertSelectorToLabelsMap(opts.LabelSelector)
		if err != nil {
			glog.Warningf("unable to convert selector %q to map: %v", opts.LabelSelector, err)
			return
		}
		opts.LabelSelector = labels.Merge(ls, map[string]string{"mco-built-in/cluster-role": "", "mco-built-in/cluster-role-binding": ""}).String()
	}
	clusterRoleAndBindingsInformer := informers.NewFilteredSharedInformerFactory(kubeClient, resyncPeriod()(), targetNamespace, filterClusterRoleAndClusterRoleBindings)
	openShiftConfigKubeNamespacedSharedInformer := informers.NewFilteredSharedInformerFactory(kubeClient, resyncPeriod()(), "openshift-config", nil)
	apiExtSharedInformer := apiextinformers.NewSharedInformerFactory(apiExtClient, resyncPeriod()())
	configSharedInformer := configinformers.NewSharedInformerFactory(configClient, resyncPeriod()())

	return &ControllerContext{
		ClientBuilder:                                cb,
		NamespacedInformerFactory:                    sharedNamespacedInformers,
		InformerFactory:                              sharedInformers,
		KubeInformerFactory:                          kubeSharedInformer,
		KubeNamespacedInformerFactory:                kubeNamespacedSharedInformer,
		ClusterRoleAndRoleBindingsInformerFactory:    clusterRoleAndBindingsInformer,
		OpenShiftConfigKubeNamespacedInformerFactory: openShiftConfigKubeNamespacedSharedInformer,
		APIExtInformerFactory:                        apiExtSharedInformer,
		ConfigInformerFactory:                        configSharedInformer,
		Stop:                                         stop,
		InformersStarted:                             make(chan struct{}),
		ResyncPeriod:                                 resyncPeriod(),
	}
}
