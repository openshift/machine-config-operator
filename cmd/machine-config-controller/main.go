package main

import (
	"flag"

	"github.com/golang/glog"
	"github.com/spf13/cobra"
)

const (
	componentName = "machine-config-controller"
)

var (
	rootCmd = &cobra.Command{
		Use:   componentName,
		Short: "Run Machine Config Controller",
		Long:  "",
	}

	rootOpts struct {
		templates string
	}
)

func init() {
	rootCmd.PersistentFlags().AddGoFlagSet(flag.CommandLine)
	startCmd.PersistentFlags().StringVar(&rootOpts.templates, "templates", "/etc/mcc/templates", "Path to the template files used for creating MachineConfig objects")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		glog.Exitf("Error executing MCC: %v", err)
	}
}
