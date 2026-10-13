package main

import (
	"flag"
	"fmt"

	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
)

var (
	versionCmd = &cobra.Command{
		Use:   "version",
		Short: "Print the version number of Machine OS Builder",
		Long:  `All software has versions. This is Machine OS Builder's.`,
		Run:   runVersionCmd,
	}
)

func init() {
	rootCmd.AddCommand(versionCmd)
}

func runVersionCmd(_ *cobra.Command, _ []string) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	program := "MachineConfigController"
	version := version.Raw + "-" + version.Hash

	fmt.Println(program, version)
}
