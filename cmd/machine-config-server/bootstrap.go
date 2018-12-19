package main

import (
	"flag"
	"fmt"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/pkg/server"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
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

	secureServer := *bs
	secureServer.Addr = fmt.Sprintf(":%d", rootOpts.sport)
	insecureServer := *bs
	insecureServer.Addr = fmt.Sprintf(":%d", rootOpts.isport)

	stopCh := make(chan struct{})
	go secureServer.ListenAndServeTLS(rootOpts.cert, rootOpts.key)
	go insecureServer.ListenAndServe()
	<-stopCh
	panic("not possible")
}
