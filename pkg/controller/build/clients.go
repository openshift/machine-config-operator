package build

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coreinformers "k8s.io/client-go/informers"
	coreinformersv1 "k8s.io/client-go/informers/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	corelistersv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"

	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	mcfginformers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
	mcfginformersv1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1"
	mcfginformersv1alpha1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1alpha1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	mcfglistersv1alpha1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1alpha1"

	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"

	"github.com/openshift/machine-config-operator/internal/clients"
)

//nolint:revive // If I name this ControllerConfig, that name will be overloaded :P
type BuildControllerConfig struct {
	// updateDelay is a pause to deal with churn in MachineConfigs; see
	// https://github.com/openshift/machine-config-operator/issues/301
	// Default: 5 seconds
	UpdateDelay time.Duration

	// maxRetries is the number of times a machineconfig pool will be retried before it is dropped out of the queue.
	// With the current rate-limiter in use (5ms*2^(maxRetries-1)) the following numbers represent the times
	// a machineconfig pool is going to be requeued:
	//
	// 5ms, 10ms, 20ms, 40ms, 80ms, 160ms, 320ms, 640ms, 1.3s, 2.6s, 5.1s, 10.2s, 20.4s, 41s, 82s
	// Default: 5
	MaxRetries int
}

// Holds each of the clients used by the Build Controller and its subcontrollers.
type Clients struct {
	mcfgclient mcfgclientset.Interface
	kubeclient clientset.Interface
}

func NewClientsFromControllerContext(ctrlCtx *ctrlcommon.ControllerContext) *Clients {
	return NewClients(ctrlCtx.ClientBuilder)
}

func NewClients(cb *clients.Builder) *Clients {
	return &Clients{
		mcfgclient: cb.MachineConfigClientOrDie("machine-os-builder"),
		kubeclient: cb.KubeClientOrDie("machine-os-builder"),
	}
}

// Creates a BuildControllerConfig with sensible production defaults.
func DefaultBuildControllerConfig() BuildControllerConfig {
	return BuildControllerConfig{
		MaxRetries:  5,
		UpdateDelay: time.Second * 5,
	}
}

type controllerBase struct {
	*Clients
	*listers
	*informers
}

func newControllerBase(c *Clients) *controllerBase {
	informers := newInformers(c)

	return &controllerBase{
		Clients:   c,
		informers: informers,
		listers:   informers.listers(),
	}
}

// Holds and starts each of the infomrers used by the Build Controller and its subcontrollers.
type informers struct {
	controllerConfigInformer  mcfginformersv1.ControllerConfigInformer
	machineConfigPoolInformer mcfginformersv1.MachineConfigPoolInformer
	podInformer               coreinformersv1.PodInformer
	machineOSBuildInformer    mcfginformersv1alpha1.MachineOSBuildInformer
	machineOSConfigInformer   mcfginformersv1alpha1.MachineOSConfigInformer
	toStart                   []interface{ Start(<-chan struct{}) }
	hasSynced                 []cache.InformerSynced
}

// Starts the informers, wiring them up to the provided context.
func (i *informers) start(ctx context.Context) {
	for _, startable := range i.toStart {
		startable.Start(ctx.Done())
	}
}

// Instantiates the required listers from the informers.
func (i *informers) listers() *listers {
	return &listers{
		machineOSBuildLister:    i.machineOSBuildInformer.Lister(),
		machineOSConfigLister:   i.machineOSConfigInformer.Lister(),
		machineConfigPoolLister: i.machineConfigPoolInformer.Lister(),
		podLister:               i.podInformer.Lister(),
		controllerConfigLister:  i.controllerConfigInformer.Lister(),
	}
}

// Holds all of the required listers so that they can be passed around and reused.
type listers struct {
	machineOSBuildLister    mcfglistersv1alpha1.MachineOSBuildLister
	machineOSConfigLister   mcfglistersv1alpha1.MachineOSConfigLister
	machineConfigPoolLister mcfglistersv1.MachineConfigPoolLister
	podLister               corelistersv1.PodLister
	controllerConfigLister  mcfglistersv1.ControllerConfigLister
}

// Creates new informer instances from a given Clients(set).
func newInformers(bcc *Clients) *informers {
	// Filters build objects for the core informer.
	ephemeralBuildObjectsOpts := func(opts *metav1.ListOptions) {
		opts.LabelSelector = utils.EphemeralBuildObjectSelector().String()
	}

	mcoInformerFactory := mcfginformers.NewSharedInformerFactory(bcc.mcfgclient, 0)

	coreInformerFactory := coreinformers.NewSharedInformerFactoryWithOptions(
		bcc.kubeclient,
		0,
		coreinformers.WithNamespace(ctrlcommon.MCONamespace),
		coreinformers.WithTweakListOptions(ephemeralBuildObjectsOpts),
	)

	controllerConfigInformer := mcoInformerFactory.Machineconfiguration().V1().ControllerConfigs()
	machineConfigPoolInformer := mcoInformerFactory.Machineconfiguration().V1().MachineConfigPools()
	machineOSBuildInformer := mcoInformerFactory.Machineconfiguration().V1alpha1().MachineOSBuilds()
	machineOSConfigInformer := mcoInformerFactory.Machineconfiguration().V1alpha1().MachineOSConfigs()
	podInformer := coreInformerFactory.Core().V1().Pods()

	return &informers{
		controllerConfigInformer:  controllerConfigInformer,
		machineConfigPoolInformer: machineConfigPoolInformer,
		machineOSBuildInformer:    machineOSBuildInformer,
		machineOSConfigInformer:   machineOSConfigInformer,
		podInformer:               podInformer,
		toStart: []interface{ Start(<-chan struct{}) }{
			mcoInformerFactory,
			coreInformerFactory,
		},
		hasSynced: []cache.InformerSynced{
			controllerConfigInformer.Informer().HasSynced,
			machineConfigPoolInformer.Informer().HasSynced,
			podInformer.Informer().HasSynced,
			machineOSBuildInformer.Informer().HasSynced,
			machineOSConfigInformer.Informer().HasSynced,
		},
	}
}
