package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/openshift/api/features"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/openshift/machine-config-operator/internal/clients"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/daemon/cri"
	"github.com/openshift/machine-config-operator/pkg/daemon/nri"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/sirupsen/logrus"
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
		tlsCipherSuites            []string
		tlsMinVersion              string
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
	startCmd.PersistentFlags().StringSliceVar(&startOpts.tlsCipherSuites, "tls-cipher-suites", nil, "Comma-separated list of cipher suites for the metrics server")
	startCmd.PersistentFlags().StringVar(&startOpts.tlsMinVersion, "tls-min-version", "VersionTLS12", "Minimum TLS version supported for the metrics server")
}

//nolint:gocritic
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

	errCh := make(chan error)
	defer close(errCh)

	dn, err := daemon.New(
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
		nodeScopedInformer, nodeScopedInformerStartFunc := ctrlcommon.NewScopedNodeInformerFromClientBuilder(cb, startOpts.nodeName)
		err := dn.HypershiftConnect(
			startOpts.nodeName,
			kubeClient,
			nodeScopedInformer,
			startOpts.hypershiftDesiredConfigMap,
		)
		if err != nil {
			ctrlcommon.WriteTerminationError(err)
		}

		nodeScopedInformerStartFunc(stopCh)
		ctx.KubeInformerFactory.Start(stopCh)
		close(ctx.InformersStarted)

		if err := dn.RunHypershift(stopCh, exitCh); err != nil {
			ctrlcommon.WriteTerminationError(err)
		}
		return
	}

	// Start local metrics listener
	go ctrlcommon.StartMetricsListener(startOpts.promMetricsURL, stopCh, daemon.RegisterMCDMetrics, startOpts.tlsMinVersion, startOpts.tlsCipherSuites)

	ctrlctx := ctrlcommon.CreateControllerContext(ctx, cb)

	// Early start the config informer because feature gate depends on it
	ctrlctx.ConfigInformerFactory.Start(ctrlctx.Stop)
	if fgErr := ctrlctx.FeatureGatesHandler.Connect(ctx); fgErr != nil {
		klog.Fatal(fmt.Errorf("failed to connect to feature gates %w", fgErr))
	}

	nodeScopedInformer, nodeScopedInformerStartFunc := ctrlcommon.NewScopedNodeInformerFromClientBuilder(cb, startOpts.nodeName)

	// create the daemon instance. this also initializes kube client items
	// which need to come from the container and not the chroot.
	err = dn.ClusterConnect(
		startOpts.nodeName,
		kubeClient,
		ctrlctx.ClientBuilder.MachineConfigClientOrDie(componentName),
		ctrlctx.InformerFactory.Machineconfiguration().V1().MachineConfigs(),
		nodeScopedInformer,
		ctrlctx.InformerFactory.Machineconfiguration().V1().ControllerConfigs(),
		ctrlctx.InformerFactory.Machineconfiguration().V1().MachineConfigPools(),
		ctrlctx.OperatorInformerFactory.Operator().V1().MachineConfigurations(),
		ctrlctx.ClientBuilder.OperatorClientOrDie(componentName),
		startOpts.kubeletHealthzEnabled,
		startOpts.kubeletHealthzEndpoint,
		ctrlctx.FeatureGatesHandler,
	)
	if err != nil {
		klog.Fatalf("Failed to initialize: %v", err)
	}

	ctrlctx.KubeInformerFactory.Start(stopCh)
	ctrlctx.KubeNamespacedInformerFactory.Start(stopCh)
	ctrlctx.InformerFactory.Start(stopCh)
	ctrlctx.OperatorInformerFactory.Start(stopCh)
	nodeScopedInformerStartFunc(ctrlctx.Stop)
	close(ctrlctx.InformersStarted)

	// ok to start the rest of the informers now that we have observed the initial feature gates
	if ctrlctx.FeatureGatesHandler.Enabled(features.FeatureGatePinnedImages) && ctrlctx.FeatureGatesHandler.Enabled(features.FeatureGateMachineConfigNodes) {
		klog.Infof("Feature enabled: %s", features.FeatureGatePinnedImages)
		criClient, err := cri.NewClient(ctx, constants.DefaultCRIOSocketPath)
		if err != nil {
			klog.Fatalf("Failed to initialize CRI client: %v", err)
		}

		prefetchTimeout := 2 * time.Minute
		pinnedImageSetManager := daemon.NewPinnedImageSetManager(
			startOpts.nodeName,
			criClient,
			ctrlctx.ClientBuilder.MachineConfigClientOrDie(componentName),
			ctrlctx.InformerFactory.Machineconfiguration().V1().PinnedImageSets(),
			nodeScopedInformer,
			ctrlctx.InformerFactory.Machineconfiguration().V1().MachineConfigPools(),
			resource.MustParse(constants.MinFreeStorageAfterPrefetch),
			constants.DefaultCRIOSocketPath,
			constants.KubeletAuthFile,
			constants.ContainerRegistryConfPath,
			prefetchTimeout,
			ctrlctx.FeatureGatesHandler,
		)

		go pinnedImageSetManager.Run(2, stopCh)
		// start the informers for the pinned image set again after the feature gate is enabled this is allowed.
		// see comments in SharedInformerFactory interface.
		ctrlctx.InformerFactory.Start(stopCh)
	}

	// Initialize and start NRI plugin for mutation control
	nriLogger := logrus.New()
	nriLogger.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})

	nriPlugin, err := nri.NewAllowMutationsPlugin(nriLogger, nri.DefaultConfigPath)
	if err != nil {
		klog.Warningf("Failed to initialize NRI plugin: %v. NRI mutation control will not be available.", err)
	} else {
		klog.Infof("Starting NRI AllowMutations plugin")
		go func() {
			if err := nriPlugin.Start(ctx); err != nil {
				klog.Errorf("NRI plugin exited with error: %v", err)
			}
		}()
	}

	if err := dn.Run(stopCh, exitCh, errCh); err != nil {
		ctrlcommon.WriteTerminationError(err)
		if errors.Is(err, daemon.ErrAuxiliary) {
			dn.CancelSIGTERM()
			dn.Close()
			cancel()
			close(errCh)
			close(exitCh)
			os.Exit(255)
		}
	}
}
