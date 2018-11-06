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
	}
)

func init() {
	rootCmd.PersistentFlags().AddGoFlagSet(flag.CommandLine)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		glog.Exitf("Error executing mcc: %v", err)
	}
}
