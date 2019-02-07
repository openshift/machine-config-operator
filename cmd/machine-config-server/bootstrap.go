package main

import (
	"flag"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/pkg/server"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
)

const (
	// we are transitioning from legacy ports 49500/49501 -> 22623/22624
	// this can be removed once fully transitioned in the installer.
	legacySecurePort   = 49500
	legacyInsecurePort = 49501
)

var (
	bootstrapCmd = &cobra.Command{
		Use:   "bootstrap",
		Short: "Run the machine config server in the bootstrap mode",
		Long:  "",
		Run:   runBootstrapCmd,
	}

	bootstrapOpts struct {
		serverBaseDir    string
		serverKubeConfig string
	}
)

func init() {
	rootCmd.AddCommand(bootstrapCmd)
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.serverBaseDir, "server-basedir", "/etc/mcs/bootstrap", "base directory on the host, relative to which machine-configs and pools can be found.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.serverKubeConfig, "bootstrap-kubeconfig", "/etc/kubernetes/kubeconfig", "path to bootstrap kubeconfig served by the bootstrap server.")
}

func runBootstrapCmd(cmd *cobra.Command, args []string) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// To help debugging, immediately log version
	glog.Infof("Version: %+v", version.Version)

	bs, err := server.NewBootstrapServer(bootstrapOpts.serverBaseDir, bootstrapOpts.serverKubeConfig)

	if err != nil {
		glog.Exitf("Machine Config Server exited with error: %v", err)
	}

	apiHandler := server.NewServerAPIHandler(bs)
	secureServer := server.NewAPIServer(apiHandler, rootOpts.sport, false, rootOpts.cert, rootOpts.key)
	insecureServer := server.NewAPIServer(apiHandler, rootOpts.isport, true, "", "")
	legacySecureServer := server.NewAPIServer(apiHandler, legacySecurePort, false, rootOpts.cert, rootOpts.key)
	legacyInsecureServer := server.NewAPIServer(apiHandler, legacyInsecurePort, true, "", "")

	stopCh := make(chan struct{})
	go secureServer.Serve()
	go insecureServer.Serve()
	go legacySecureServer.Serve()
	go legacyInsecureServer.Serve()
	<-stopCh
	panic("not possible")
}
