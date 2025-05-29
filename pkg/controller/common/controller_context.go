package common

import (
	"context"
	"math/rand"
	"time"

	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	imageinformers "github.com/openshift/client-go/image/informers/externalversions"
	machineinformersv1beta1 "github.com/openshift/client-go/machine/informers/externalversions"
	mcfginformers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
	operatorinformers "github.com/openshift/client-go/operator/informers/externalversions"
	routeinformers "github.com/openshift/client-go/route/informers/externalversions"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/machine-config-operator/internal/clients"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/version"
	apiextinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/informers"
	corev1informers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

const (
	minResyncPeriod = 20 * time.Minute
)

func resyncPeriod() func() time.Duration {
	return func() time.Duration {
		// Disable gosec here to avoid throwing
		// G404: Use of weak random number generator (math/rand instead of crypto/rand)
		// #nosec
		factor := rand.Float64() + 1
		return time.Duration(float64(minResyncPeriod.Nanoseconds()) * factor)
	}
}

// DefaultResyncPeriod returns a function which generates a random resync period
func DefaultResyncPeriod() func() time.Duration {
	return resyncPeriod()
}

// ControllerContext stores all the informers for a variety of kubernetes objects.
type ControllerContext struct {
	ClientBuilder *clients.Builder

	NamespacedInformerFactory                           mcfginformers.SharedInformerFactory
	InformerFactory                                     mcfginformers.SharedInformerFactory
	TechPreviewInformerFactory                          mcfginformers.SharedInformerFactory
	KubeInformerFactory                                 informers.SharedInformerFactory
	KubeNamespacedInformerFactory                       informers.SharedInformerFactory
	OpenShiftConfigKubeNamespacedInformerFactory        informers.SharedInformerFactory
	OpenShiftConfigManagedKubeNamespacedInformerFactory informers.SharedInformerFactory
	OpenShiftKubeAPIServerKubeNamespacedInformerFactory informers.SharedInformerFactory
	APIExtInformerFactory                               apiextinformers.SharedInformerFactory
	ConfigInformerFactory                               configinformers.SharedInformerFactory
	OperatorInformerFactory                             operatorinformers.SharedInformerFactory
	KubeMAOSharedInformer                               informers.SharedInformerFactory
	MachineInformerFactory                              machineinformersv1beta1.SharedInformerFactory
	ImageInformerFactory                                imageinformers.SharedInformerFactory
	RouteInformerFactory                                routeinformers.SharedInformerFactory

	FeatureGateAccess featuregates.FeatureGateAccess

	AvailableResources map[schema.GroupVersionResource]bool

	Stop <-chan struct{}

	InformersStarted chan struct{}

	ResyncPeriod func() time.Duration
}

// CreateControllerContext creates the ControllerContext with the ClientBuilder.
func CreateControllerContext(ctx context.Context, cb *clients.Builder) *ControllerContext {
	client := cb.MachineConfigClientOrDie("machine-config-shared-informer")
	kubeClient := cb.KubeClientOrDie("kube-shared-informer")
	imageClient := cb.ImageClientOrDie("image-shared-informer")
	routeClient := cb.RouteClientOrDie("route-shared-informer")
	apiExtClient := cb.APIExtClientOrDie("apiext-shared-informer")
	configClient := cb.ConfigClientOrDie("config-shared-informer")
	operatorClient := cb.OperatorClientOrDie("operator-shared-informer")
	machineClient := cb.MachineClientOrDie("machine-shared-informer")
	sharedInformers := mcfginformers.NewSharedInformerFactory(client, resyncPeriod()())
	sharedTechPreviewInformers := mcfginformers.NewSharedInformerFactory(client, resyncPeriod()())
	sharedNamespacedInformers := mcfginformers.NewFilteredSharedInformerFactory(client, resyncPeriod()(), MCONamespace, nil)
	kubeSharedInformer := informers.NewSharedInformerFactory(kubeClient, resyncPeriod()())
	kubeNamespacedSharedInformer := informers.NewFilteredSharedInformerFactory(kubeClient, resyncPeriod()(), MCONamespace, nil)
	openShiftConfigKubeNamespacedSharedInformer := informers.NewFilteredSharedInformerFactory(kubeClient, resyncPeriod()(), "openshift-config", nil)
	openShiftConfigManagedKubeNamespacedSharedInformer := informers.NewFilteredSharedInformerFactory(kubeClient, resyncPeriod()(), OpenshiftConfigManagedNamespace, nil)
	openShiftKubeAPIServerKubeNamespacedSharedInformer := informers.NewFilteredSharedInformerFactory(kubeClient,
		resyncPeriod()(),
		"openshift-kube-apiserver-operator",
		func(opt *metav1.ListOptions) {
			opt.FieldSelector = fields.OneTermEqualSelector("metadata.name", "kube-apiserver-to-kubelet-client-ca").String()
		},
	)
	// this is needed to listen for changes in MAO user data secrets to re-apply the ones we define in the MCO (since we manage them)
	kubeMAOSharedInformer := informers.NewFilteredSharedInformerFactory(kubeClient, resyncPeriod()(), "openshift-machine-api", nil)
	imageSharedInformer := imageinformers.NewSharedInformerFactory(imageClient, resyncPeriod()())
	routeSharedInformer := routeinformers.NewSharedInformerFactory(routeClient, resyncPeriod()())

	// filter out CRDs that do not have the MCO label
	assignFilterLabels := func(opts *metav1.ListOptions) {
		labelsMap, err := labels.ConvertSelectorToLabelsMap(opts.LabelSelector)
		if err != nil {
			klog.Warningf("unable to convert selector %q to map: %v", opts.LabelSelector, err)
			return
		}
		opts.LabelSelector = labels.Merge(labelsMap, map[string]string{daemonconsts.OpenShiftOperatorManagedLabel: ""}).String()
	}
	apiExtSharedInformer := apiextinformers.NewSharedInformerFactoryWithOptions(apiExtClient, resyncPeriod()(),
		apiextinformers.WithNamespace(MCONamespace), apiextinformers.WithTweakListOptions(assignFilterLabels))
	configSharedInformer := configinformers.NewSharedInformerFactory(configClient, resyncPeriod()())
	operatorSharedInformer := operatorinformers.NewSharedInformerFactory(operatorClient, resyncPeriod()())
	machineSharedInformer := machineinformersv1beta1.NewSharedInformerFactoryWithOptions(machineClient, resyncPeriod()(), machineinformersv1beta1.WithNamespace("openshift-machine-api"))

	desiredVersion := version.ReleaseVersion
	missingVersion := "0.0.1-snapshot"

	controllerRef, err := events.GetControllerReferenceForCurrentPod(ctx, kubeClient, MCONamespace, nil)
	if err != nil {
		klog.Warningf("unable to get owner reference (falling back to namespace): %v", err)
	}

	recorder := events.NewKubeRecorder(kubeClient.CoreV1().Events(MCONamespace), "machine-config-operator", controllerRef)

	// By default, this will exit(0) the process if the featuregates ever change to a different set of values.
	featureGateAccessor := featuregates.NewFeatureGateAccess(
		desiredVersion, missingVersion,
		configSharedInformer.Config().V1().ClusterVersions(), configSharedInformer.Config().V1().FeatureGates(),
		recorder,
	)

	go featureGateAccessor.Run(ctx)

	return &ControllerContext{
		ClientBuilder:                                       cb,
		NamespacedInformerFactory:                           sharedNamespacedInformers,
		InformerFactory:                                     sharedInformers,
		TechPreviewInformerFactory:                          sharedTechPreviewInformers,
		KubeInformerFactory:                                 kubeSharedInformer,
		KubeNamespacedInformerFactory:                       kubeNamespacedSharedInformer,
		OpenShiftConfigKubeNamespacedInformerFactory:        openShiftConfigKubeNamespacedSharedInformer,
		OpenShiftKubeAPIServerKubeNamespacedInformerFactory: openShiftKubeAPIServerKubeNamespacedSharedInformer,
		OpenShiftConfigManagedKubeNamespacedInformerFactory: openShiftConfigManagedKubeNamespacedSharedInformer,
		APIExtInformerFactory:                               apiExtSharedInformer,
		ConfigInformerFactory:                               configSharedInformer,
		OperatorInformerFactory:                             operatorSharedInformer,
		MachineInformerFactory:                              machineSharedInformer,
		Stop:                                                ctx.Done(),
		InformersStarted:                                    make(chan struct{}),
		ResyncPeriod:                                        resyncPeriod(),
		KubeMAOSharedInformer:                               kubeMAOSharedInformer,
		FeatureGateAccess:                                   featureGateAccessor,
		ImageInformerFactory:                                imageSharedInformer,
		RouteInformerFactory:                                routeSharedInformer,
	}
}

// Creates a NodeInformer that is bound to a single node. This is for use by
// the MCD to ensure that the MCD only receives watch events for the node that
// it is running on as opposed to all cluster nodes. Doing this helps reduce
// the load on the apiserver. Because the filters are applied to *all*
// informers constructed by the informer factory, we want to ensure that this
// factory is only used to construct a NodeInformer.
//
// Therefore, to ensure that this informer factory is only used for
// constructing a NodeInformer with this specific filter, we only return the
// instantiated NodeInformer instance and a start function.
func NewScopedNodeInformer(kubeclient kubernetes.Interface, nodeName string) (corev1informers.NodeInformer, func(<-chan struct{})) {
	sif := informers.NewSharedInformerFactoryWithOptions(
		kubeclient,
		resyncPeriod()(),
		informers.WithTweakListOptions(func(opts *metav1.ListOptions) {
			opts.FieldSelector = fields.OneTermEqualSelector("metadata.name", nodeName).String()
		}),
	)

	return sif.Core().V1().Nodes(), sif.Start
}

// Creates a scoped node informer that is bound to a single node from a
// clients.Builder instance. It sets the user-agent for the client to
// node-scoped-informer before instantiating the node informer. Returns the
// instantiated NodeInformer and a start function.
func NewScopedNodeInformerFromClientBuilder(cb *clients.Builder, nodeName string) (corev1informers.NodeInformer, func(<-chan struct{})) {
	return NewScopedNodeInformer(cb.KubeClientOrDie("node-scoped-informer"), nodeName)
}
