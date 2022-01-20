package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"strings"

	// Enable sha256 in container image references
	_ "crypto/sha256"

	daemon "github.com/openshift/machine-config-operator/pkg/daemon"
	errors "github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var fromEtcPullSpec bool

const (
	// etcPivotFile is used for 4.1 bootimages and is how the MCD
	// currently communicated with this service.
	etcPivotFile = "/etc/pivot/image-pullspec"
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
	pivotCmd.PersistentFlags().BoolVarP(&fromEtcPullSpec, "from-etc-pullspec", "P", false, "Parse /etc/pivot/image-pullspec")
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
}

func run(_ *cobra.Command, args []string) (retErr error) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	var container string
	if fromEtcPullSpec || len(args) == 0 {
		fromEtcPullSpec = true
		data, err := ioutil.ReadFile(etcPivotFile)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("No container specified")
			}
			return errors.Wrapf(err, "failed to read from %s", etcPivotFile)
		}
		container = strings.TrimSpace(string(data))
	} else {
		container = args[0]
	}

	// I'm pretty sure we no longer use pivot directly anymore, although we have recommended users to
	// use this utility in the past.
	// Perhaps with coreos layering we should just remove this entirely.

	osImageContentDir, err := daemon.ExtractAndUpdateOS(container)
	if err != nil {
		return err
	}

	defer os.RemoveAll(osImageContentDir)

	// Delete the file now that we successfully rebased
	if fromEtcPullSpec {
		if err := os.Remove(etcPivotFile); err != nil {
			if !os.IsNotExist(err) {
				return errors.Wrapf(err, "failed to delete %s", etcPivotFile)
			}
		}
	}

	return nil
}

// Execute runs the command
func Execute(cmd *cobra.Command, args []string) {
	err := run(cmd, args)
	if err != nil {
		fmt.Printf("error: %v\n", err)
		os.Exit(1)
	}
}
