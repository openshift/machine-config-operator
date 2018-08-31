package common

import (
	"time"

	securityinformers "github.com/openshift/client-go/security/informers/externalversions"
	mcfginformers "github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions"
	apiextinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/informers"
)

// ControllerContext stores all the informers for a variety of kubernetes objects.
type ControllerContext struct {
	ClientBuilder *ClientBuilder

	NamespacedInformerFactory     mcfginformers.SharedInformerFactory
	InformerFactory               mcfginformers.SharedInformerFactory
	KubeInformerFactory           informers.SharedInformerFactory
	KubeNamespacedInformerFactory informers.SharedInformerFactory
	APIExtInformerFactory         apiextinformers.SharedInformerFactory
	SecurityInformerFactory       securityinformers.SharedInformerFactory

	AvailableResources map[schema.GroupVersionResource]bool

	Stop <-chan struct{}

	InformersStarted chan struct{}

	KubeInformersStarted chan struct{}

	ResyncPeriod func() time.Duration
}

// CreateControllerContext creates the ControllerContext with the ClientBuilder.
func CreateControllerContext(cb *ClientBuilder, stop <-chan struct{}, targetNamespace string) *ControllerContext {
	client := cb.MachineConfigClientOrDie("machine-config-shared-informer")
	kubeClient := cb.KubeClientOrDie("kube-shared-informer")
	apiExtClient := cb.APIExtClientOrDie("apiext-shared-informer")
	securityClient := cb.SecurityClientOrDie("security-shared-informer")
	sharedInformers := mcfginformers.NewSharedInformerFactory(client, resyncPeriod()())
	sharedNamespacedInformers := mcfginformers.NewFilteredSharedInformerFactory(client, resyncPeriod()(), targetNamespace, nil)
	kubeSharedInformer := informers.NewSharedInformerFactory(kubeClient, resyncPeriod()())
	kubeNamespacedSharedInformer := informers.NewFilteredSharedInformerFactory(kubeClient, resyncPeriod()(), targetNamespace, nil)
	apiExtSharedInformer := apiextinformers.NewSharedInformerFactory(apiExtClient, resyncPeriod()())
	securityInformer := securityinformers.NewSharedInformerFactory(securityClient, resyncPeriod()())

	return &ControllerContext{
		ClientBuilder:                 cb,
		NamespacedInformerFactory:     sharedNamespacedInformers,
		InformerFactory:               sharedInformers,
		KubeInformerFactory:           kubeSharedInformer,
		KubeNamespacedInformerFactory: kubeNamespacedSharedInformer,
		APIExtInformerFactory:         apiExtSharedInformer,
		SecurityInformerFactory:       securityInformer,
		Stop:                 stop,
		InformersStarted:     make(chan struct{}),
		KubeInformersStarted: make(chan struct{}),
		ResyncPeriod:         resyncPeriod(),
	}
}
