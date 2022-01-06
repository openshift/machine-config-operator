package main

import (
	"flag"
	"os"

	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"
)

const (
	componentName = "machine-config-daemon"
)

var (
	rootCmd = &cobra.Command{
		Use:   componentName,
		Short: "Run Machine Config Daemon",
		Long:  "Runs the Machine Config Daemon which handles communication between the host and the cluster as well as applying machineconfigs to the host",
	}
)

func init() {
	rootCmd.PersistentFlags().AddGoFlagSet(flag.CommandLine)
}

func main() {
	code := cli.Run(rootCmd)
	os.Exit(code)
}
