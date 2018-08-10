package main

import (
	"flag"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/pkg/server"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
)

var (
	startCmd = &cobra.Command{
		Use:   "start",
		Short: "Starts Machine Config Server",
		Long:  "",
		Run:   runStartCmd,
	}

	startOpts struct {
		kubeconfig      string
		targetNamespace string
	}
)

func init() {
	rootCmd.AddCommand(startCmd)
	startCmd.PersistentFlags().StringVar(&startOpts.kubeconfig, "kubeconfig", "", "Kubeconfig file to access a remote cluster (testing only)")
	startCmd.PersistentFlags().StringVar(&startOpts.targetNamespace, "target-namespace", "openshift-machine-config", "target namespace within which the machine config, pool reside")
}

func runStartCmd(cmd *cobra.Command, args []string) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// To help debugging, immediately log version
	glog.Infof("Version: %+v", version.Version)

	cs, err := server.NewClusterServer(startOpts.kubeconfig, startOpts.targetNamespace)
	if err != nil {
		glog.Exitf("Machine Config Server exited with error: %v", err)
	}

	apiHandler := server.NewServerAPIHandler(cs)
	apiServer := server.NewAPIServer(apiHandler, rootOpts.port, rootOpts.debug, rootOpts.cert, rootOpts.key)
	apiServer.Serve()
}
