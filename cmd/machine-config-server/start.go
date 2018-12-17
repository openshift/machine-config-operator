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
	glog.Infof("Version: %+v", version.Version)

	if startOpts.apiserverURL == "" {
		glog.Exitf("--apiserver-url cannot be empty")
	}

	cs, err := server.NewClusterServer(startOpts.kubeconfig, startOpts.apiserverURL)
	if err != nil {
		glog.Exitf("Machine Config Server exited with error: %v", err)
	}

	secureServer := *cs
	secureServer.Addr = fmt.Sprintf(":%d", rootOpts.sport)
	insecureServer := *cs
	insecureServer.Addr = fmt.Sprintf(":%d", rootOpts.isport)

	stopCh := make(chan struct{})
	go secureServer.ListenAndServeTLS(rootOpts.cert, rootOpts.key)
	go insecureServer.ListenAndServe()
	<-stopCh
	panic("not possible")
}
