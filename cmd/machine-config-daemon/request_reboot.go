package main

import (
	"flag"
	"fmt"
	"os"

	daemon "github.com/openshift/machine-config-operator/pkg/daemon"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var requestRebootCmd = &cobra.Command{
	Use:                   "request-reboot",
	DisableFlagsInUseLine: true,
	Short:                 "Request a reboot",
	Args:                  cobra.ExactArgs(1),
	Run:                   executeRequestReboot,
}

// init executes upon import
func init() {
	rootCmd.AddCommand(requestRebootCmd)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
}

func runRequestReboot(_ *cobra.Command, args []string) error {
	flag.Set("logtostderr", "true")
	flag.Parse()

	return daemon.RequestReboot(args[0])
}

// Execute runs the command
func executeRequestReboot(cmd *cobra.Command, args []string) {
	err := runRequestReboot(cmd, args)
	if err != nil {
		fmt.Printf("error: %v\n", err)
		os.Exit(1)
	}
}
