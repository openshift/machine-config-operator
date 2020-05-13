package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/internal"
	"github.com/openshift/machine-config-operator/internal/clients"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon"
	errors "github.com/pkg/errors"
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
		promMetricsURL         string
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
	startCmd.PersistentFlags().StringVar(&startOpts.promMetricsURL, "metrics-url", "127.0.0.1:8797", "URL for prometheus metrics listener")
}

// bindPodMounts ensures that the daemon can still see e.g. /run/secrets/kubernetes.io
// service account tokens after chrooting.  This function must be called before chroot.
func bindPodMounts(rootMount string) error {
	targetSecrets := filepath.Join(rootMount, "/run/secrets")
	if err := os.MkdirAll(targetSecrets, 0755); err != nil {
		return err
	}
	// This will only affect our mount namespace, not the host
	output, err := exec.Command("mount", "--rbind", "/run/secrets", targetSecrets).CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to mount /run/secrets to %s: %s", targetSecrets, string(output))
	}
	return nil
}

func runStartCmd(cmd *cobra.Command, args []string) {
	internal.InitLogging()
	glog.V(2).Infof("Options parsed: %+v", startOpts)

	onceFromMode := startOpts.onceFrom != ""
	if !onceFromMode {
		// in the daemon case
		if err := bindPodMounts(startOpts.rootMount); err != nil {
			glog.Fatalf("Binding pod mounts: %+v", err)
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

	if startOpts.nodeName == "" {
		name, ok := os.LookupEnv("NODE_NAME")
		if !ok || name == "" {
			glog.Fatalf("node-name is required")
		}
		startOpts.nodeName = name
	}

	// This channel is used to signal Run() something failed and to jump ship.
	// It's purely a chan<- in the Daemon struct for goroutines to write to, and
	// a <-chan in Run() for the main thread to listen on.
	exitCh := make(chan error)
	defer close(exitCh)

	dn, err := daemon.New(
		daemon.NewNodeUpdaterClient(),
		exitCh,
	)
	if err != nil {
		glog.Fatalf("Failed to initialize single run daemon: %v", err)
	}

	// If we are asked to run once and it's a valid file system path use
	// the bare Daemon
	if startOpts.onceFrom != "" {
		err = dn.RunOnceFrom(startOpts.onceFrom, startOpts.skipReboot)
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

	// This channel is used to ensure all spawned goroutines exit when we exit.
	stopCh := make(chan struct{})
	defer close(stopCh)

	// Start local metrics listener
	go daemon.StartMetricsListener(startOpts.promMetricsURL, stopCh)

	ctx := ctrlcommon.CreateControllerContext(cb, stopCh, componentName)
	// create the daemon instance. this also initializes kube client items
	// which need to come from the container and not the chroot.
	dn.ClusterConnect(
		startOpts.nodeName,
		kubeClient,
		ctx.InformerFactory.Machineconfiguration().V1().MachineConfigs(),
		ctx.KubeInformerFactory.Core().V1().Nodes(),
		startOpts.kubeletHealthzEnabled,
		startOpts.kubeletHealthzEndpoint,
	)
	if err != nil {
		glog.Fatalf("Failed to initialize daemon: %v", err)
	}

	ctx.KubeInformerFactory.Start(stopCh)
	ctx.InformerFactory.Start(stopCh)
	close(ctx.InformersStarted)

	if err := dn.Run(stopCh, exitCh); err != nil {
		ctrlcommon.WriteTerminationError(err)
	}
}
