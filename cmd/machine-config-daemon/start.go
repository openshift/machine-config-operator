package main

import (
	"flag"
	"os"
	"syscall"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/internal/clients"
	controllercommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
)

var (
	startCmd = &cobra.Command{
		Use:   "start",
		Short: "Starts Machine Config Daemon",
		Long:  "",
		Run:   runStartCmd,
	}

	startOpts struct {
		kubeconfig             string
		nodeName               string
		rootMount              string
		onceFrom               string
		fromIgnition           bool
		kubeletHealthzEnabled  bool
		kubeletHealthzEndpoint string
	}
)

func init() {
	rootCmd.AddCommand(startCmd)
	startCmd.PersistentFlags().StringVar(&startOpts.kubeconfig, "kubeconfig", "", "Kubeconfig file to access a remote cluster (testing only)")
	startCmd.PersistentFlags().StringVar(&startOpts.nodeName, "node-name", "", "kubernetes node name daemon is managing.")
	startCmd.PersistentFlags().StringVar(&startOpts.rootMount, "root-mount", "/rootfs", "where the nodes root filesystem is mounted for chroot and file manipulation.")
	startCmd.PersistentFlags().StringVar(&startOpts.onceFrom, "once-from", "", "Runs the daemon once using a provided file path or URL endpoint as its machine config or ignition (.ign) file source")
	startCmd.PersistentFlags().BoolVar(&startOpts.kubeletHealthzEnabled, "kubelet-healthz-enabled", true, "kubelet healthz endpoint monitoring")
	startCmd.PersistentFlags().StringVar(&startOpts.kubeletHealthzEndpoint, "kubelet-healthz-endpoint", "http://localhost:10248/healthz", "healthz endpoint to check health")
}

func runStartCmd(cmd *cobra.Command, args []string) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	glog.V(2).Infof("Options parsed: %+v", startOpts)

	// To help debugging, immediately log version
	glog.Infof("Version: %+v", version.Version)

	operatingSystem, err := daemon.GetHostRunningOS(startOpts.rootMount)
	if err != nil {
		glog.Fatalf("Error found when checking operating system: %s", err)
	}

	if startOpts.nodeName == "" {
		name, ok := os.LookupEnv("NODE_NAME")
		if !ok || name == "" {
			glog.Fatalf("node-name is required")
		}
		startOpts.nodeName = name
	}

	// Ensure that the rootMount exists
	if _, err := os.Stat(startOpts.rootMount); err != nil {
		if os.IsNotExist(err) {
			glog.Fatalf("rootMount %s does not exist", startOpts.rootMount)
		}
		glog.Fatalf("Unable to verify rootMount %s exists: %s", startOpts.rootMount, err)
	}

	// This channel is used to ensure all spawned goroutines exit when we exit.
	stopCh := make(chan struct{})
	defer close(stopCh)

	// This channel is used to signal Run() something failed and to jump ship.
	// It's purely a chan<- in the Daemon struct for goroutines to write to, and
	// a <-chan in Run() for the main thread to listen on.
	exitCh := make(chan error)
	defer close(exitCh)

	glog.Info("Starting node writer")
	nodeWriter := daemon.NewNodeWriter()
	go nodeWriter.Run(stopCh)

	cb, err := clients.NewBuilder(startOpts.kubeconfig)
	if err != nil {
		if startOpts.onceFrom != "" {
			glog.Info("Cannot initialize ClientBuilder, likely in onceFrom mode with Ignition")
		} else {
			glog.Fatalf("Failed to initialize ClientBuilder: %v", err)
		}
	}

	var kubeClient kubernetes.Interface
	if cb != nil {
		kubeClient, err = cb.KubeClient(componentName)
		if err != nil {
			glog.Info("Cannot initialize kubeClient, likely in onceFrom mode with Ignition")
		}
	}

	var dn *daemon.Daemon

	// If we are asked to run once and it's a valid file system path use
	// the bare Daemon
	if startOpts.onceFrom != "" {
		mcClient, err := cb.MachineConfigClient(componentName)
		if err != nil {
			glog.Info("Cannot initialize MC client, likely in onceFrom mode with Ignition")
		}
		dn, err = daemon.New(
			startOpts.rootMount,
			startOpts.nodeName,
			operatingSystem,
			daemon.NewNodeUpdaterClient(),
			startOpts.onceFrom,
			mcClient,
			kubeClient,
			startOpts.kubeletHealthzEnabled,
			startOpts.kubeletHealthzEndpoint,
			nodeWriter,
			exitCh,
			stopCh,
		)
		if err != nil {
			glog.Fatalf("Failed to initialize single run daemon: %v", err)
		}
		// Else we use the cluster driven daemon
	} else {
		if kubeClient == nil {
			panic("Running in cluster mode without a kubeClient")
		}
		ctx := controllercommon.CreateControllerContext(cb, stopCh, componentName)
		// create the daemon instance. this also initializes kube client items
		// which need to come from the container and not the chroot.
		dn, err = daemon.NewClusterDrivenDaemon(
			startOpts.rootMount,
			startOpts.nodeName,
			operatingSystem,
			daemon.NewNodeUpdaterClient(),
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigs(),
			kubeClient,
			startOpts.onceFrom,
			ctx.KubeInformerFactory.Core().V1().Nodes(),
			startOpts.kubeletHealthzEnabled,
			startOpts.kubeletHealthzEndpoint,
			nodeWriter,
			exitCh,
			stopCh,
		)
		if err != nil {
			glog.Fatalf("Failed to initialize daemon: %v", err)
		}

		// in the daemon case
		if err := dn.BindPodMounts(); err != nil {
			glog.Fatalf("Binding pod mounts: %s", err)
		}

		ctx.KubeInformerFactory.Start(stopCh)
		ctx.InformerFactory.Start(stopCh)
		close(ctx.InformersStarted)
	}

	glog.Infof(`Calling chroot("%s")`, startOpts.rootMount)
	if err := syscall.Chroot(startOpts.rootMount); err != nil {
		glog.Fatalf("Unable to chroot to %s: %s", startOpts.rootMount, err)
	}

	glog.V(2).Infof("Moving to / inside the chroot")
	if err := os.Chdir("/"); err != nil {
		glog.Fatalf("Unable to change directory to /: %s", err)
	}

	glog.Info("Starting MachineConfigDaemon")
	defer glog.Info("Shutting down MachineConfigDaemon")

	if err := dn.Run(stopCh, exitCh); err != nil {
		glog.Fatalf("Failed to run: %v", err)
	}
}
