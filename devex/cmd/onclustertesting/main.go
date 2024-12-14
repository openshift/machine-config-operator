package main

import (
	"flag"
	"os"

	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"
)

var (
	rootCmd = &cobra.Command{
		Use:   "onclustertesting",
		Short: "Help with testing on-cluster builds",
		Long:  "",
	}
)

func init() {
	rootCmd.PersistentFlags().AddGoFlagSet(flag.CommandLine)
}

func main() {
	os.Exit(cli.Run(rootCmd))
}
