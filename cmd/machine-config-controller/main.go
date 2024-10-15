package main

import (
	"flag"
	"os"

	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"
)

const (
	componentName      = "machine-config-controller"
	componentNamespace = "openshift-machine-config-operator"
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
	rootCmd.PersistentFlags().StringVar(&version.ReleaseVersion, "payload-version", version.ReleaseVersion, "Version of the openshift release")
}

func main() {
	code := cli.Run(rootCmd)
	os.Exit(code)
}
