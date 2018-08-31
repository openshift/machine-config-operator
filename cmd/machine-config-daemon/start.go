package main

import (
	"flag"
	"os"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/cmd/common"
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
		rootPrefix string
	}
)

func init() {
	rootCmd.AddCommand(startCmd)
	startCmd.PersistentFlags().StringVar(&startOpts.kubeconfig, "kubeconfig", "", "Kubeconfig file to access a remote cluster (testing only)")
	startCmd.PersistentFlags().StringVar(&startOpts.nodeName, "node-name", "", "kubernetes node name daemon is managing.")
	startCmd.PersistentFlags().StringVar(&startOpts.rootPrefix, "root-prefix", "/rootfs", "where the nodes root filesystem is mounted, for the file stage.")
}

func runStartCmd(cmd *cobra.Command, args []string) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// To help debugging, immediately log version
	glog.Infof("Version: %+v", version.Version)

	if startOpts.nodeName == "" {
		name, ok := os.LookupEnv("NODE_NAME")
		if !ok || name == "" {
			glog.Fatalf("node-name is required")
		}
		startOpts.nodeName = name
	}

	cb, err := common.NewClientBuilder(startOpts.kubeconfig)
	if err != nil {
		glog.Fatalf("error creating clients: %v", err)
	}

	daemon, err := daemon.New(
		startOpts.rootPrefix,
		startOpts.nodeName,
		cb.MachineConfigClientOrDie(componentName),
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
