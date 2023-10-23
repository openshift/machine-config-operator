package main

import (
	"context"
	"flag"
	"net/url"
	"os"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/openshift/machine-config-operator/internal/clients"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

var (
	startCmd = &cobra.Command{
		Use:   "start",
		Short: "Starts Machine Config Daemon",
		Long:  "",
		Run:   runStartCmd,
	}

	startOpts struct {
		kubeconfig                 string
		nodeName                   string
		rootMount                  string
		hypershiftDesiredConfigMap string
		onceFrom                   string
		skipReboot                 bool
		fromIgnition               bool
		kubeletHealthzEnabled      bool
		kubeletHealthzEndpoint     string
		promMetricsURL             string
	}
)

func init() {
	rootCmd.AddCommand(startCmd)
	startCmd.PersistentFlags().StringVar(&startOpts.kubeconfig, "kubeconfig", "", "Kubeconfig file to access a remote cluster (testing only)")
	startCmd.PersistentFlags().StringVar(&startOpts.nodeName, "node-name", "", "kubernetes node name daemon is managing.")
	startCmd.PersistentFlags().StringVar(&startOpts.rootMount, "root-mount", "/rootfs", "where the nodes root filesystem is mounted for chroot and file manipulation.")
	startCmd.PersistentFlags().StringVar(&startOpts.hypershiftDesiredConfigMap, "desired-configmap", "", "Runs the daemon for a Hypershift hosted cluster node. Requires a configmap with desired config as input.")
	startCmd.PersistentFlags().StringVar(&startOpts.onceFrom, "once-from", "", "Runs the daemon once using a provided file path or URL endpoint as its machine config or ignition (.ign) file source")
	startCmd.PersistentFlags().BoolVar(&startOpts.skipReboot, "skip-reboot", false, "Skips reboot after a sync, applies only in once-from")
	startCmd.PersistentFlags().BoolVar(&startOpts.kubeletHealthzEnabled, "kubelet-healthz-enabled", true, "kubelet healthz endpoint monitoring")
	startCmd.PersistentFlags().StringVar(&startOpts.kubeletHealthzEndpoint, "kubelet-healthz-endpoint", "http://localhost:10248/healthz", "healthz endpoint to check health")
	startCmd.PersistentFlags().StringVar(&startOpts.promMetricsURL, "metrics-url", "127.0.0.1:8797", "URL for prometheus metrics listener")
}

func runStartCmd(_ *cobra.Command, _ []string) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	klog.V(2).Infof("Options parsed: %+v", startOpts)

	// To help debugging, immediately log version
	klog.Infof("Version: %+v (%s)", version.Raw, version.Hash)

	// See https://github.com/coreos/rpm-ostree/pull/1880
	os.Setenv("RPMOSTREE_CLIENT_ID", "machine-config-operator")

	onceFromMode := startOpts.onceFrom != ""
	if !onceFromMode {
		// in the daemon case
		if err := daemon.PrepareNamespace(startOpts.rootMount); err != nil {
			klog.Fatalf("Binding pod mounts: %+v", err)
		}
	}

	if err := daemon.ReexecuteForTargetRoot(startOpts.rootMount); err != nil {
		klog.Fatalf("failed to re-exec: %+v", err)
	}

	if startOpts.nodeName == "" {
		name, ok := os.LookupEnv("NODE_NAME")
		if !ok || name == "" {
			klog.Fatalf("node-name is required")
		}
		startOpts.nodeName = name
	}

	// This channel is used to signal Run() something failed and to jump ship.
	// It's purely a chan<- in the Daemon struct for goroutines to write to, and
	// a <-chan in Run() for the main thread to listen on.
	exitCh := make(chan error)
	defer close(exitCh)

	dn, err := daemon.New(
		false,
		exitCh,
	)
	if err != nil {
		klog.Fatalf("Failed to initialize single run daemon: %v", err)
	}

	// If we are asked to run once and it's a valid file system path use
	// the bare Daemon
	if startOpts.onceFrom != "" {
		err = dn.RunOnceFrom(startOpts.onceFrom, startOpts.skipReboot)
		if err != nil {
			klog.Fatalf("%v", err)
		}
		return
	}

	// Use kubelet kubeconfig file to get the URL to kube-api-server
	kubeconfig, err := clientcmd.LoadFromFile("/etc/kubernetes/kubeconfig")
	if err != nil {
		klog.Fatalf("failed to load kubelet kubeconfig: %v", err)
	}
	clusterName := kubeconfig.Contexts[kubeconfig.CurrentContext].Cluster
	apiURL := kubeconfig.Clusters[clusterName].Server

	url, err := url.Parse(apiURL)
	if err != nil {
		klog.Fatalf("failed to parse api url from kubelet kubeconfig: %v", err)
	}

	// The kubernetes in-cluster functions don't let you override the apiserver
	// directly; gotta "pass" it via environment vars.
	klog.Infof("overriding kubernetes api to %s", apiURL)
	os.Setenv("KUBERNETES_SERVICE_HOST", url.Hostname())
	os.Setenv("KUBERNETES_SERVICE_PORT", url.Port())

	cb, err := clients.NewBuilder(startOpts.kubeconfig)
	if err != nil {
		klog.Fatalf("Failed to initialize ClientBuilder: %v", err)
	}

	kubeClient, err := cb.KubeClient(componentName)
	if err != nil {
		klog.Fatalf("Cannot initialize kubeClient: %v", err)
	}

	// This channel is used to ensure all spawned goroutines exit when we exit.
	ctx, cancel := context.WithCancel(context.Background())
	stopCh := ctx.Done()
	defer cancel()

	if startOpts.hypershiftDesiredConfigMap != "" {
		// This is a hypershift-mode daemon
		ctx := ctrlcommon.CreateControllerContext(ctx, cb)
		err := dn.HypershiftConnect(
			startOpts.nodeName,
			kubeClient,
			ctx.KubeInformerFactory.Core().V1().Nodes(),
			startOpts.hypershiftDesiredConfigMap,
		)
		if err != nil {
			ctrlcommon.WriteTerminationError(err)
		}

		ctx.KubeInformerFactory.Start(stopCh)
		close(ctx.InformersStarted)

		if err := dn.RunHypershift(stopCh, exitCh); err != nil {
			ctrlcommon.WriteTerminationError(err)
		}
		return
	}

	// Start local metrics listener
	go ctrlcommon.StartMetricsListener(startOpts.promMetricsURL, stopCh, daemon.RegisterMCDMetrics)

	ctrlctx := ctrlcommon.CreateControllerContext(ctx, cb)
	// create the daemon instance. this also initializes kube client items
	// which need to come from the container and not the chroot.
	err = dn.ClusterConnect(
		startOpts.nodeName,
		kubeClient,
		ctrlctx.InformerFactory.Machineconfiguration().V1().MachineConfigs(),
		ctrlctx.KubeInformerFactory.Core().V1().Nodes(),
		ctrlctx.InformerFactory.Machineconfiguration().V1().ControllerConfigs(),
		startOpts.kubeletHealthzEnabled,
		startOpts.kubeletHealthzEndpoint,
	)
	if err != nil {
		klog.Fatalf("Failed to initialize: %v", err)
	}

	ctrlctx.KubeInformerFactory.Start(stopCh)
	ctrlctx.InformerFactory.Start(stopCh)
	close(ctrlctx.InformersStarted)

	if err := dn.Run(stopCh, exitCh); err != nil {
		ctrlcommon.WriteTerminationError(err)
	}
}
