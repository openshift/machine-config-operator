package main

import (
	"flag"
	"os"

	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"
)

const componentName = "machine-os-builder"

var (
	rootCmd = &cobra.Command{
		Use:   componentName,
		Short: "Run Machine OS Builder",
		Long:  "",
	}
)

func init() {
	rootCmd.PersistentFlags().AddGoFlagSet(flag.CommandLine)
}

func main() {
	os.Exit(cli.Run(rootCmd))
}
