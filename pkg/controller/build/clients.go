package build

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coreinformers "k8s.io/client-go/informers"
	batchinformersv1 "k8s.io/client-go/informers/batch/v1"
	clientset "k8s.io/client-go/kubernetes"
	batchlisterv1 "k8s.io/client-go/listers/batch/v1"
	"k8s.io/client-go/tools/cache"

	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	mcfginformers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
	mcfginformersv1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	coreinformersv1 "k8s.io/client-go/informers/core/v1"
	corelistersv1 "k8s.io/client-go/listers/core/v1"

	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

// Holds and starts each of the infomrers used by the Build Controller and its subcontrollers.
type informers struct {
	controllerConfigInformer  mcfginformersv1.ControllerConfigInformer
	machineConfigPoolInformer mcfginformersv1.MachineConfigPoolInformer
	machineConfigInformer     mcfginformersv1.MachineConfigInformer
	jobInformer               batchinformersv1.JobInformer
	machineOSBuildInformer    mcfginformersv1.MachineOSBuildInformer
	machineOSConfigInformer   mcfginformersv1.MachineOSConfigInformer
	nodeInformer              coreinformersv1.NodeInformer
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
		machineConfigLister:     i.machineConfigInformer.Lister(),
		jobLister:               i.jobInformer.Lister(),
		controllerConfigLister:  i.controllerConfigInformer.Lister(),
		nodeLister:              i.nodeInformer.Lister(),
	}
}

// Holds all of the required listers so that they can be passed around and reused.
type listers struct {
	machineOSBuildLister    mcfglistersv1.MachineOSBuildLister
	machineOSConfigLister   mcfglistersv1.MachineOSConfigLister
	machineConfigPoolLister mcfglistersv1.MachineConfigPoolLister
	machineConfigLister     mcfglistersv1.MachineConfigLister
	jobLister               batchlisterv1.JobLister
	controllerConfigLister  mcfglistersv1.ControllerConfigLister
	nodeLister              corelistersv1.NodeLister
}

func (l *listers) utilListers() *utils.Listers {
	return &utils.Listers{
		MachineOSBuildLister:    l.machineOSBuildLister,
		MachineOSConfigLister:   l.machineOSConfigLister,
		MachineConfigPoolLister: l.machineConfigPoolLister,
		NodeLister:              l.nodeLister,
	}
}

// Creates new informer instances from a given Clients(set).
func newInformers(mcfgclient mcfgclientset.Interface, kubeclient clientset.Interface) *informers {
	// Filters build objects for the core informer.
	ephemeralBuildObjectsOpts := func(opts *metav1.ListOptions) {
		opts.LabelSelector = utils.EphemeralBuildObjectSelector().String()
	}

	mcoInformerFactory := mcfginformers.NewSharedInformerFactory(mcfgclient, 0)

	coreInformerFactory := coreinformers.NewSharedInformerFactoryWithOptions(
		kubeclient,
		0,
		coreinformers.WithNamespace(ctrlcommon.MCONamespace),
		coreinformers.WithTweakListOptions(ephemeralBuildObjectsOpts),
	)
	coreInformerFactoryNodes := coreinformers.NewSharedInformerFactory(kubeclient, 0)

	controllerConfigInformer := mcoInformerFactory.Machineconfiguration().V1().ControllerConfigs()
	machineConfigPoolInformer := mcoInformerFactory.Machineconfiguration().V1().MachineConfigPools()
	machineOSBuildInformer := mcoInformerFactory.Machineconfiguration().V1().MachineOSBuilds()
	machineConfigInformer := mcoInformerFactory.Machineconfiguration().V1().MachineConfigs()
	machineOSConfigInformer := mcoInformerFactory.Machineconfiguration().V1().MachineOSConfigs()
	jobInformer := coreInformerFactory.Batch().V1().Jobs()
	nodeInformer := coreInformerFactoryNodes.Core().V1().Nodes()

	return &informers{
		controllerConfigInformer:  controllerConfigInformer,
		machineConfigPoolInformer: machineConfigPoolInformer,
		machineOSBuildInformer:    machineOSBuildInformer,
		machineOSConfigInformer:   machineOSConfigInformer,
		machineConfigInformer:     machineConfigInformer,
		jobInformer:               jobInformer,
		nodeInformer:              nodeInformer,
		toStart: []interface{ Start(<-chan struct{}) }{
			mcoInformerFactory,
			coreInformerFactory,
			coreInformerFactoryNodes,
		},
		hasSynced: []cache.InformerSynced{
			controllerConfigInformer.Informer().HasSynced,
			machineConfigPoolInformer.Informer().HasSynced,
			jobInformer.Informer().HasSynced,
			machineOSBuildInformer.Informer().HasSynced,
			machineOSConfigInformer.Informer().HasSynced,
			nodeInformer.Informer().HasSynced,
		},
	}
}
