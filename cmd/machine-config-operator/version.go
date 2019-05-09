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
		Short: "Print the version number of Machine Config Operator",
		Long:  `All software has versions. This is Machine Config Operator's.`,
		Run:   runVersionCmd,
	}
)

func init() {
	rootCmd.AddCommand(versionCmd)
}

func runVersionCmd(cmd *cobra.Command, args []string) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	program := "MachineConfigOperator"
	version := "v" + version.Version.String() + "-" + version.Hash

	fmt.Println(program, version)
}
