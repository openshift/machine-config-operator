package main

import (
	"fmt"

	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
)

var (
	versionCmd = &cobra.Command{
		Use:   "version",
		Short: "Print the version number of Machine Config Server",
		Long:  `All software has versions. This is Machine Config Server's.`,
		Run:   runVersionCmd,
	}
)

func init() {
	rootCmd.AddCommand(versionCmd)
}

func runVersionCmd(cmd *cobra.Command, args []string) {
	program := "MachineConfigServer"
	version := version.Raw + "-" + version.Hash

	fmt.Println(program, version)
}
