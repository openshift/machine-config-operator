package main

import (
	"flag"
	"fmt"
	"os"

	// Enable sha256 in container image references
	_ "crypto/sha256"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var pivotCmd = &cobra.Command{
	Use:                   "pivot",
	DisableFlagsInUseLine: true,
	Short:                 "Allows moving from one OSTree deployment to another",
	Args:                  cobra.MaximumNArgs(1),
	Run:                   Execute,
}

// init executes upon import
func init() {
	rootCmd.AddCommand(pivotCmd)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
}

// Execute runs the command
func Execute(cmd *cobra.Command, args []string) {
	fmt.Println(`
	ERROR: pivot no longer forces a system upgrade. It will be fully removed in a later y release. 
	If you are attempting a manual OS upgrade, please try the following steps:
	-delete the currentconfig(rm /etc/machine-config-daemon/currentconfig)
	-create a forcefile(touch /run/machine-config-daemon-force) to retry the OS upgrade.

	More instructions can be found here: https://access.redhat.com/solutions/5598401
	`)
	os.Exit(1)
}
