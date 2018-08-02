package main

import (
	"flag"
	"math/rand"
	"os"
	"time"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/pkg/controller/node"
	"github.com/openshift/machine-config-operator/pkg/controller/render"
	"github.com/openshift/machine-config-operator/pkg/controller/template"
	mcfgclientset "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
	mcfginformers "github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/tools/record"
)

const (
	minResyncPeriod = 10 * time.Second

	leaseDuration = 90 * time.Second
	renewDeadline = 60 * time.Second
	retryPeriod   = 30 * time.Second
)

var (
	startCmd = &cobra.Command{
		Use:   "start",
		Short: "Starts Machine Config Controller",
		Long:  "",
		Run:   runStartCmd,
	}

	startOpts struct {
		kubeconfig string
		templates  string

		resourceLockNamespace string
	}
)

func init() {
	rootCmd.AddCommand(startCmd)
	startCmd.PersistentFlags().StringVar(&startOpts.kubeconfig, "kubeconfig", "", "Kubeconfig file to access a remote cluster (testing only)")
	startCmd.PersistentFlags().StringVar(&startOpts.templates, "templates", "/etc/templates", "Path to the template files used for creating MachineConfig objects")
	startCmd.PersistentFlags().StringVar(&startOpts.resourceLockNamespace, "resourcelock-namespace", metav1.NamespaceSystem, "Path to the template files used for creating MachineConfig objects")
}

func runStartCmd(cmd *cobra.Command, args []string) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// To help debugging, immediately log version
	glog.Infof("Version: %+v", version.Version)

	cb, err := newClientBuilder(startOpts.kubeconfig)
	if err != nil {
		glog.Fatalf("error creating clients: %v", err)
	}
	stopCh := make(chan struct{})
	run := func(stop <-chan struct{}) {

		ctx := createControllerContext(cb, stopCh)
		if err := startControllers(ctx); err != nil {
			glog.Fatalf("error starting controllers: %v", err)
		}

		ctx.InformerFactory.Start(ctx.Stop)
		ctx.KubeInformerFactory.Start(ctx.Stop)
		close(ctx.InformersStarted)
		close(ctx.KubeInformersStarted)

		select {}
	}

	leaderelection.RunOrDie(leaderelection.LeaderElectionConfig{
		Lock:          createResourceLock(cb),
		LeaseDuration: leaseDuration,
		RenewDeadline: renewDeadline,
		RetryPeriod:   retryPeriod,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: run,
			OnStoppedLeading: func() {
				glog.Fatalf("leaderelection lost")
			},
		},
	})
	panic("unreachable")
}

func createResourceLock(cb *clientBuilder) resourcelock.Interface {
	recorder := record.
		NewBroadcaster().
		NewRecorder(runtime.NewScheme(), v1.EventSource{Component: componentName})

	id, err := os.Hostname()
	if err != nil {
		glog.Fatalf("error creating lock: %v", err)
	}

	// add a uniquifier so that two processes on the same host don't accidentally both become active
	id = id + "_" + string(uuid.NewUUID())

	return &resourcelock.ConfigMapLock{
		ConfigMapMeta: metav1.ObjectMeta{
			Namespace: startOpts.resourceLockNamespace,
			Name:      componentName,
		},
		Client: cb.KubeClientOrDie("leader-election").CoreV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity:      id,
			EventRecorder: recorder,
		},
	}
}

func resyncPeriod() func() time.Duration {
	return func() time.Duration {
		factor := rand.Float64() + 1
		return time.Duration(float64(minResyncPeriod.Nanoseconds()) * factor)
	}
}

type clientBuilder struct {
	config *rest.Config
}

func (cb *clientBuilder) ClientOrDie(name string) mcfgclientset.Interface {
	return mcfgclientset.NewForConfigOrDie(rest.AddUserAgent(cb.config, name))
}

func (cb *clientBuilder) KubeClientOrDie(name string) kubernetes.Interface {
	return kubernetes.NewForConfigOrDie(rest.AddUserAgent(cb.config, name))
}

func newClientBuilder(kubeconfig string) (*clientBuilder, error) {
	var config *rest.Config
	var err error

	if kubeconfig != "" {
		glog.V(4).Infof("Loading kube client config from path %q", kubeconfig)
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		glog.V(4).Infof("Using in-cluster kube client config")
		config, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, err
	}

	return &clientBuilder{
		config: config,
	}, nil
}

type controllerContext struct {
	ClientBuilder *clientBuilder

	InformerFactory     mcfginformers.SharedInformerFactory
	KubeInformerFactory informers.SharedInformerFactory

	AvailableResources map[schema.GroupVersionResource]bool

	Stop <-chan struct{}

	InformersStarted     chan struct{}
	KubeInformersStarted chan struct{}

	ResyncPeriod func() time.Duration
}

func createControllerContext(cb *clientBuilder, stop <-chan struct{}) *controllerContext {
	client := cb.ClientOrDie("shared-informer")
	kubeClient := cb.KubeClientOrDie("kube-shared-informer")
	sharedInformers := mcfginformers.NewSharedInformerFactory(client, resyncPeriod()())
	kubeSharedInformer := informers.NewSharedInformerFactory(kubeClient, resyncPeriod()())

	return &controllerContext{
		ClientBuilder:        cb,
		InformerFactory:      sharedInformers,
		KubeInformerFactory:  kubeSharedInformer,
		Stop:                 stop,
		InformersStarted:     make(chan struct{}),
		KubeInformersStarted: make(chan struct{}),
		ResyncPeriod:         resyncPeriod(),
	}
}

func startControllers(ctx *controllerContext) error {
	go template.New(
		startOpts.templates,
		ctx.InformerFactory.Machineconfiguration().V1().ControllerConfigs(),
		ctx.InformerFactory.Machineconfiguration().V1().MachineConfigs(),
		ctx.ClientBuilder.KubeClientOrDie("template-controller"),
		ctx.ClientBuilder.ClientOrDie("template-controller"),
	).Run(2, ctx.Stop)

	go render.New(
		ctx.InformerFactory.Machineconfiguration().V1().MachineConfigPools(),
		ctx.InformerFactory.Machineconfiguration().V1().MachineConfigs(),
		ctx.ClientBuilder.KubeClientOrDie("render-controller"),
		ctx.ClientBuilder.ClientOrDie("render-controller"),
	).Run(2, ctx.Stop)

	go node.New(
		ctx.InformerFactory.Machineconfiguration().V1().MachineConfigPools(),
		ctx.KubeInformerFactory.Core().V1().Nodes(),
		ctx.ClientBuilder.KubeClientOrDie("node-update-controller"),
		ctx.ClientBuilder.ClientOrDie("node-update-controller"),
	).Run(2, ctx.Stop)

	return nil
}
