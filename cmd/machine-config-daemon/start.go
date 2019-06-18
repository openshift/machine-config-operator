package main

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/internal/clients"
	controllercommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
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
		skipReboot             bool
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
	startCmd.PersistentFlags().BoolVar(&startOpts.skipReboot, "skip-reboot", false, "Skips reboot after a sync, applies only in once-from")
	startCmd.PersistentFlags().BoolVar(&startOpts.kubeletHealthzEnabled, "kubelet-healthz-enabled", true, "kubelet healthz endpoint monitoring")
	startCmd.PersistentFlags().StringVar(&startOpts.kubeletHealthzEndpoint, "kubelet-healthz-endpoint", "http://localhost:10248/healthz", "healthz endpoint to check health")
}

// bindPodMounts ensures that the daemon can still see e.g. /run/secrets/kubernetes.io
// service account tokens after chrooting.  This function must be called before chroot.
func bindPodMounts(rootMount string) error {
	targetSecrets := filepath.Join(rootMount, "/run/secrets")
	if err := os.MkdirAll(targetSecrets, 0755); err != nil {
		return err
	}
	// This will only affect our mount namespace, not the host
	mnt := exec.Command("mount", "--rbind", "/run/secrets", targetSecrets)
	return mnt.Run()
}

func runStartCmd(cmd *cobra.Command, args []string) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	glog.V(2).Infof("Options parsed: %+v", startOpts)

	// To help debugging, immediately log version
	glog.Infof("Version: %+v (%s)", version.Version, version.Hash)

	onceFromMode := startOpts.onceFrom != ""
	if !onceFromMode {
		// in the daemon case
		if err := bindPodMounts(startOpts.rootMount); err != nil {
			glog.Fatalf("Binding pod mounts: %s", err)
		}
	}

	glog.Infof(`Calling chroot("%s")`, startOpts.rootMount)
	if err := syscall.Chroot(startOpts.rootMount); err != nil {
		glog.Fatalf("Unable to chroot to %s: %s", startOpts.rootMount, err)
	}

	glog.V(2).Infof("Moving to / inside the chroot")
	if err := os.Chdir("/"); err != nil {
		glog.Fatalf("Unable to change directory to /: %s", err)
	}

	operatingSystem, err := daemon.GetHostRunningOS()
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

	var dn *daemon.Daemon

	// If we are asked to run once and it's a valid file system path use
	// the bare Daemon
	if startOpts.onceFrom != "" {
		dn, err = daemon.New(
			startOpts.nodeName,
			operatingSystem,
			daemon.NewNodeUpdaterClient(),
			startOpts.onceFrom,
			startOpts.skipReboot,
			nil,
			nil,
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

		err = dn.RunOnceFrom()
		if err != nil {
			glog.Fatalf("%v", err)
		}
		return
	}

	cb, err := clients.NewBuilder(startOpts.kubeconfig)
	if err != nil {
		glog.Fatalf("Failed to initialize ClientBuilder: %v", err)
	}

	kubeClient, err := cb.KubeClient(componentName)
	if err != nil {
		glog.Fatalf("Cannot initialize kubeClient: %v", err)
	}

	ctx := controllercommon.CreateControllerContext(cb, stopCh, componentName)
	// create the daemon instance. this also initializes kube client items
	// which need to come from the container and not the chroot.
	dn, err = daemon.NewClusterDrivenDaemon(
		startOpts.nodeName,
		operatingSystem,
		daemon.NewNodeUpdaterClient(),
		ctx.InformerFactory.Machineconfiguration().V1().MachineConfigs(),
		kubeClient,
		startOpts.onceFrom,
		startOpts.skipReboot,
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

	ctx.KubeInformerFactory.Start(stopCh)
	ctx.InformerFactory.Start(stopCh)
	close(ctx.InformersStarted)

	if err := dn.Run(stopCh, exitCh); err != nil {
		controllercommon.WriteTerminationError(err)
	}
}
