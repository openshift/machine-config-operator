package main

import (
	"flag"

	"github.com/golang/glog"
	"github.com/spf13/cobra"
)

const (
	componentName = "gcp-routes-controller"
)

var (
	rootCmd = &cobra.Command{
		Use:           componentName,
		Short:         "Controls the gcp-routes.service on RHCOS hosts based on health checks",
		Long:          "",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
)

func init() {
	rootCmd.PersistentFlags().AddGoFlagSet(flag.CommandLine)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		glog.Exitf("Error executing %s: %v", componentName, err)
	}
}
