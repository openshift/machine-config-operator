package main

import (
	"flag"

	"github.com/golang/glog"
	"github.com/spf13/cobra"
)

const (
	componentName = "apisever-watcher"
)

var (
	rootCmd = &cobra.Command{
		Use:           componentName,
		Short:         "Monitors the local apiserver and writes cloud-routes downfiles",
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
