package main

import (
	"flag"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/pkg/daemon"
	mcfgclientset "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	startCmd = &cobra.Command{
		Use:   "start",
		Short: "Starts Machine Config Daemon",
		Long:  "",
		Run:   runStartCmd,
	}

	startOpts struct {
		kubeconfig      string
		nodeName        string
		targetNamespace string
		rootPrefix      string
	}
)

func init() {
	rootCmd.AddCommand(startCmd)
	startCmd.PersistentFlags().StringVar(&startOpts.kubeconfig, "kubeconfig", "", "Kubeconfig file to access a remote cluster (testing only)")
	startCmd.PersistentFlags().StringVar(&startOpts.nodeName, "node-name", "", "kubernetes node name daemon is managing.")
	startCmd.PersistentFlags().StringVar(&startOpts.nodeName, "target-namespace", "openshift-machine-config", "namespace is where the daemon looks for machineconfigs.")
	startCmd.PersistentFlags().StringVar(&startOpts.nodeName, "root-prefix", "/rootfs", "where the nodes root filesystem is mounted, for the file stage.")
}

func runStartCmd(cmd *cobra.Command, args []string) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// To help debugging, immediately log version
	glog.Infof("Version: %+v", version.Version)

	if startOpts.nodeName == "" {
		glog.Fatalf("node-name is required")
	}

	cb, err := newClientBuilder(startOpts.kubeconfig)
	if err != nil {
		glog.Fatalf("error creating clients: %v", err)
	}

	daemon, err := daemon.New(
		startOpts.rootPrefix,
		startOpts.nodeName,
		startOpts.targetNamespace,
		cb.ClientOrDie(componentName),
		cb.KubeClientOrDie(componentName),
	)
	if err != nil {
		glog.Fatalf("failed to initialize daemon: %v", err)
	}

	stopCh := make(chan struct{})
	defer close(stopCh)
	err = daemon.Run(stopCh)
	if err != nil {
		glog.Fatalf("failed to run: %v", err)
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
