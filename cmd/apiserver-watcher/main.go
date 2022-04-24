package main

import (
	"flag"

	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"
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
	_ = cli.Run(rootCmd)
}
