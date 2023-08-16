package main

import (
	"flag"

	"github.com/openshift/machine-config-operator/pkg/server"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
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
		certificates     []string
	}
)

func init() {
	rootCmd.AddCommand(bootstrapCmd)
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.serverBaseDir, "server-basedir", "/etc/mcs/bootstrap", "base directory on the host, relative to which machine-configs and pools can be found.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.serverKubeConfig, "bootstrap-kubeconfig", "/etc/kubernetes/kubeconfig", "path to bootstrap kubeconfig served by the bootstrap server.")
	bootstrapCmd.PersistentFlags().StringArrayVar(&bootstrapOpts.certificates, "bootstrap-certs", []string{}, "a certificate bundle formatted in a string array with the format key=value,key=value")
}

func runBootstrapCmd(_ *cobra.Command, _ []string) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// To help debugging, immediately log version
	klog.Infof("Version: %+v (%s)", version.Raw, version.Hash)

	bs, err := server.NewBootstrapServer(bootstrapOpts.serverBaseDir, bootstrapOpts.serverKubeConfig, bootstrapOpts.certificates)

	if err != nil {
		klog.Exitf("Machine Config Server exited with error: %v", err)
	}

	apiHandler := server.NewServerAPIHandler(bs)
	secureServer := server.NewAPIServer(apiHandler, rootOpts.sport, false, rootOpts.cert, rootOpts.key)
	insecureServer := server.NewAPIServer(apiHandler, rootOpts.isport, true, "", "")

	stopCh := make(chan struct{})
	go secureServer.Serve()
	go insecureServer.Serve()
	<-stopCh
	panic("not possible")
}
