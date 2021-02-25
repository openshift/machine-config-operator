package main

import (
	"flag"

	"github.com/golang/glog"
	"github.com/spf13/cobra"
)

const (
	componentName   = "apiserver-watcher"
	iptablesTimeout = 5
)

var (
	rootCmd = &cobra.Command{
		Use:           componentName,
		Short:         "Monitors the local apiserver and install apiserver redirection rules",
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
