package main

import (
	"flag"
	"os"
	"syscall"

	"github.com/golang/glog"
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
		kubeconfig string
		nodeName   string
		rootMount  string
		onceFrom   string
	}
)

func init() {
	rootCmd.AddCommand(startCmd)
	startCmd.PersistentFlags().StringVar(&startOpts.kubeconfig, "kubeconfig", "", "Kubeconfig file to access a remote cluster (testing only)")
	startCmd.PersistentFlags().StringVar(&startOpts.nodeName, "node-name", "", "kubernetes node name daemon is managing.")
	startCmd.PersistentFlags().StringVar(&startOpts.rootMount, "root-mount", "/rootfs", "where the nodes root filesystem is mounted for chroot and file manipulation.")
	startCmd.PersistentFlags().StringVar(&startOpts.onceFrom, "once-from", "", "Runs the daemon once using a provided file path or URL endpoint as its machine config source")
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
		glog.Fatalf("unable to verify rootMount %s exists: %s", startOpts.rootMount, err)
	}

	stopCh := make(chan struct{})
	defer close(stopCh)
	var dn *daemon.Daemon

	// If we are asked to run once and it's a valid file system path use
	// the bare Daemon
	if startOpts.onceFrom != "" && daemon.ValidPath(startOpts.onceFrom) {
		dn, err = daemon.New(
			startOpts.rootMount,
			startOpts.nodeName,
			operatingSystem,
			daemon.NewNodeUpdaterClient(),
			daemon.NewFileSystemClient(),
			startOpts.onceFrom,
		)
		if err != nil {
			glog.Fatalf("failed to initialize single run daemon: %v", err)
		}
		// Else we use the cluster driven daemon
	} else {
		// create the daemon instance. this also initializes kube client items
		// which need to come from the container and not the chroot.
		dn, err = daemon.NewClusterDrivenDaemon(
			startOpts.rootMount,
			startOpts.nodeName,
			operatingSystem,
			daemon.NewNodeUpdaterClient(),
			startOpts.kubeconfig,
			daemon.NewFileSystemClient(),
			startOpts.onceFrom,
			stopCh,
			componentName,
		)
		if err != nil {
			glog.Fatalf("failed to initialize daemon: %v", err)
		}
		err = dn.CheckStateOnBoot(stopCh)
		if err != nil {
			glog.Fatalf("error checking initial state of node: %v", err)
		}
	}

	glog.Infof(`Calling chroot("%s")`, startOpts.rootMount)
	if err := syscall.Chroot(startOpts.rootMount); err != nil {
		glog.Fatalf("unable to chroot to %s: %s", startOpts.rootMount, err)
	}

	glog.V(2).Infof("Moving to / inside the chroot")
	if err := os.Chdir("/"); err != nil {
		glog.Fatalf("unable to change directory to /: %s", err)
	}

	glog.Info("Starting MachineConfigDaemon")
	defer glog.Info("Shutting down MachineConfigDaemon")

	err = dn.Run(stopCh)
	if err != nil {
		glog.Fatalf("failed to run: %v", err)
	}
}
