package main

import (
	"flag"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/server"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"

	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/log"
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

func runStartCmd(_ *cobra.Command, _ []string) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// Initialize controller-runtime logger before any controller-runtime components are used
	log.SetLogger(klog.NewKlogr())

	// To help debugging, immediately log version
	klog.Infof("Version: %+v (%s)", version.Raw, version.Hash)

	if startOpts.apiserverURL == "" {
		klog.Exitf("--apiserver-url cannot be empty")
	}

	cs, err := server.NewClusterServer(startOpts.kubeconfig, startOpts.apiserverURL)
	if err != nil {
		ctrlcommon.WriteTerminationError(err)
	}

	klog.Infof("Launching server with tls min version: %v & cipher suites %v", rootOpts.tlsminversion, rootOpts.tlsciphersuites)
	tlsConfig := ctrlcommon.GetGoTLSConfig(rootOpts.tlsminversion, rootOpts.tlsciphersuites)

	apiHandler := server.NewServerAPIHandler(cs)
	secureServer := server.NewAPIServer(apiHandler, rootOpts.sport, false, rootOpts.cert, rootOpts.key, tlsConfig)
	insecureServer := server.NewAPIServer(apiHandler, rootOpts.isport, true, "", "", tlsConfig)

	stopCh := make(chan struct{})
	go secureServer.Serve()
	go insecureServer.Serve()
	<-stopCh
	panic("not possible")
}
