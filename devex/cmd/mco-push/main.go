package main

import (
	"flag"
	"os"

	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"
)

var (
	rootCmd = &cobra.Command{
		Use:   "mco-push",
		Short: "Automates the replacement of the machine-config-operator (MCO) image in an OpenShift cluster for testing purposes.",
		Long:  "",
	}
)

func init() {
	rootCmd.PersistentFlags().AddGoFlagSet(flag.CommandLine)
}

func main() {
	os.Exit(cli.Run(rootCmd))
}
