package main

import (
	"fmt"

	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
)

var (
	versionCmd = &cobra.Command{
		Use:   "version",
		Short: "Print the version number of Setup Etcd Environment",
		Long:  `All software has versions. This is Setup Etcd Environment's.`,
		Run:   runVersionCmd,
	}
)

func init() {
	rootCmd.AddCommand(versionCmd)
}

func runVersionCmd(cmd *cobra.Command, args []string) {
	program := "Setup Etcd Environment"
	version := version.Raw + "-" + version.Hash

	fmt.Println(program, version)
}
