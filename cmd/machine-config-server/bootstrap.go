package main

import (
	"flag"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
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

	// Read-in bootstrap apiserver file /etc/mcs/bootstrap/api-server/apiserver.yaml.
	apiServer, err := ctrlcommon.GetBootstrapAPIServer()
	if err != nil {
		// If an error happened, log it. Exiting here would end the installation which may not be desirable.
		// In such cases of error, the MCS will default to using the intermediate tls profile as apiServer==nil
		klog.Infof("error found during grabbing apiserver from bootstrap manifest %v", err)
	}

	// Determine tls settings from APIServer object.
	// If apiServer==nil, this call will default to the intermediate profile
	tlsminversion, tlsciphersuites := ctrlcommon.GetSecurityProfileCiphersFromAPIServer(apiServer)
	klog.Infof("Launching bootstrap server with tls min version: %v & cipher suites %v", tlsminversion, tlsciphersuites)
	tlsConfig := ctrlcommon.GetGoTLSConfig(tlsminversion, tlsciphersuites)

	apiHandler := server.NewServerAPIHandler(bs)
	secureServer := server.NewAPIServer(apiHandler, rootOpts.sport, false, rootOpts.cert, rootOpts.key, tlsConfig)
	insecureServer := server.NewAPIServer(apiHandler, rootOpts.isport, true, "", "", tlsConfig)

	stopCh := make(chan struct{})
	go secureServer.Serve()
	go insecureServer.Serve()
	<-stopCh
	panic("not possible")
}
