package main

import (
	"flag"
	"os"

	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"
)

const (
	componentName = "machine-state-controller"
)

var (
	rootCmd = &cobra.Command{
		Use:   componentName,
		Short: "Run Machine State Controller",
		Long:  "",
	}
)

func init() {
	rootCmd.PersistentFlags().AddGoFlagSet(flag.CommandLine)
	rootCmd.PersistentFlags().StringVar(&version.ReleaseVersion, "payload-version", version.ReleaseVersion, "Version of the openshift release")
}

func main() {
	code := cli.Run(rootCmd)
	os.Exit(code)
}
