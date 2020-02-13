package main

import (
	"flag"

	"github.com/golang/glog"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
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
		kubeconfig   string
		apiserverURL string
	}
)

func init() {
	rootCmd.AddCommand(startCmd)
	startCmd.PersistentFlags().StringVar(&startOpts.kubeconfig, "kubeconfig", "", "Kubeconfig file to access a remote cluster (testing only)")
	startCmd.PersistentFlags().StringVar(&startOpts.apiserverURL, "apiserver-url", "", "URL for apiserver; Used to generate kubeconfig")
}

func runStartCmd(cmd *cobra.Command, args []string) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// To help debugging, immediately log version
	glog.Infof("Version: %+v (%s)", version.Raw, version.Hash)

	if startOpts.apiserverURL == "" {
		glog.Exitf("--apiserver-url cannot be empty")
	}

	cs, err := server.NewClusterServer(startOpts.kubeconfig, startOpts.apiserverURL)
	if err != nil {
		ctrlcommon.WriteTerminationError(err)
	}

	apiHandler := server.NewServerAPIHandler(cs)
	secureServer := server.NewAPIServer(apiHandler, rootOpts.sport, false, rootOpts.cert, rootOpts.key)
	insecureServer := server.NewAPIServer(apiHandler, rootOpts.isport, true, "", "")

	stopCh := make(chan struct{})
	go secureServer.Serve()
	go insecureServer.Serve()
	<-stopCh
	panic("not possible")
}
