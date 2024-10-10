package main

import (
	"flag"
	"os"

	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"
)

const componentName = "machine-config"

var (
	rootCmd = &cobra.Command{
		Use:   componentName,
		Short: "Run Machine Config Operator",
		Long:  "",
	}
)

func init() {
	rootCmd.PersistentFlags().AddGoFlagSet(flag.CommandLine)
	rootCmd.PersistentFlags().StringVar(&version.ReleaseVersion, "payload-version", version.ReleaseVersion, "Version of the openshift release")
	rootCmd.PersistentFlags().StringVar(&version.OperatorImage, "operator-image", version.OperatorImage, "Image pullspec for the current machine-config operator")
}

func main() {
	code := cli.Run(rootCmd)
	os.Exit(code)
}
