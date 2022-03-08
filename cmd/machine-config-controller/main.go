package main

import (
	"flag"
	"os"

	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"
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
	rootCmd.PersistentFlags().StringVar(&rootOpts.templates, "templates", "/etc/mcc/templates", "Path to the template files used for creating MachineConfig objects")
}

func main() {
	code := cli.Run(rootCmd)
	os.Exit(code)
}
