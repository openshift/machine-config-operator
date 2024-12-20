package main

import (
	"flag"
	"os"

	commonconsts "github.com/openshift/machine-config-operator/pkg/controller/common/constants"
	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"
)

const (
	internalRegistryHostname string = "image-registry.openshift-image-registry.svc:5000"
	imagestreamName          string = "machine-config-operator"
	imagestreamPullspec      string = internalRegistryHostname + "/" + commonconsts.MCONamespace + "/" + imagestreamName + ":latest"
)

var (
	rootCmd = &cobra.Command{
		Use:   "mco-builder",
		Short: "Automates the build and replacement of the machine-config-operator (MCO) image in an OpenShift cluster for testing purposes.",
		Long:  "",
	}
)

func init() {
	rootCmd.PersistentFlags().AddGoFlagSet(flag.CommandLine)
}

func main() {
	os.Exit(cli.Run(rootCmd))
}
