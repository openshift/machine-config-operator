package main

import (
	"flag"

	"github.com/golang/glog"
	"github.com/spf13/cobra"
)

const (
	componentName      = "machine-config-operator"
	componentNamespace = "openshift-machine-config-operator"
)

var (
	rootCmd = &cobra.Command{
		Use:   componentName,
		Short: "Run Machine Config Controller",
		Long:  "",
	}

	rootOpts struct {
		etcdCAFile string
		rootCAFile string
	}
)

func init() {
	rootCmd.PersistentFlags().StringVar(&rootOpts.etcdCAFile, "etcd-ca", "/etc/ssl/etcd/ca.crt", "path to etcd CA certificate")
	rootCmd.PersistentFlags().StringVar(&rootOpts.rootCAFile, "root-ca", "/etc/ssl/kubernetes/ca.crt", "path to root CA certificate")
	rootCmd.PersistentFlags().AddGoFlagSet(flag.CommandLine)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		glog.Exitf("Error executing mcc: %v", err)
	}
}
