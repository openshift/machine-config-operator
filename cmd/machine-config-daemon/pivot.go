package main

import (
	"bufio"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"

	// Enable sha256 in container image references
	_ "crypto/sha256"

	"github.com/golang/glog"
	daemon "github.com/openshift/machine-config-operator/pkg/daemon"
	"github.com/openshift/machine-config-operator/pkg/daemon/pivot/types"
	errors "github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// flag storage
var keep bool
var reboot bool
var fromEtcPullSpec bool

const (
	etcPivotFile       = "/etc/pivot/image-pullspec"
	runPivotRebootFile = "/run/pivot/reboot-needed"
	// File containing kernel arg changes for tuning
	kernelTuningFile = "/etc/pivot/kernel-args"
	cmdLineFile      = "/proc/cmdline"
)

// TODO: fill out the whitelist
// tuneableArgsWhitelist contains allowed keys for tunable arguments
var tuneableArgsWhitelist = map[string]bool{
	"nosmt":            true,
	"mitigations=auto": true,
}

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
	pivotCmd.PersistentFlags().BoolVarP(&keep, "keep", "k", false, "Do not remove container image")
	pivotCmd.PersistentFlags().BoolVarP(&reboot, "reboot", "r", false, "Reboot if changed")
	pivotCmd.PersistentFlags().BoolVarP(&fromEtcPullSpec, "from-etc-pullspec", "P", false, "Parse /etc/pivot/image-pullspec")
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
}

// isArgTuneable returns if the argument provided is allowed to be modified
func isArgTunable(arg string) bool {
	return tuneableArgsWhitelist[arg]
}

// isArgInUse checks to see if the argument is already in use by the system currently
func isArgInUse(arg, cmdLinePath string) (bool, error) {
	if cmdLinePath == "" {
		cmdLinePath = cmdLineFile
	}
	content, err := ioutil.ReadFile(cmdLinePath)
	if err != nil {
		return false, err
	}

	checkable := string(content)
	if strings.Contains(checkable, arg) {
		return true, nil
	}
	return false, nil
}

// parseTuningFile parses the kernel argument tuning file
func parseTuningFile(tuningFilePath, cmdLinePath string) ([]types.TuneArgument, []types.TuneArgument, error) {
	addArguments := []types.TuneArgument{}
	deleteArguments := []types.TuneArgument{}
	if tuningFilePath == "" {
		tuningFilePath = kernelTuningFile
	}
	if cmdLinePath == "" {
		cmdLinePath = cmdLineFile
	}
	// Read and parse the file
	file, err := os.Open(tuningFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			// It's ok if the file doesn't exist
			return addArguments, deleteArguments, nil
		}
		return addArguments, deleteArguments, errors.Wrapf(err, "reading %s", tuningFilePath)
	}
	// Clean up
	defer file.Close()

	// Parse the tuning lines
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "ADD ") {
			// NOTE: Today only specific bare kernel arguments are allowed so
			// there is not a need to split on =.
			key := strings.TrimSpace(line[len("ADD "):])
			if isArgTunable(key) {
				// Find out if the argument is in use
				inUse, err := isArgInUse(key, cmdLinePath)
				if err != nil {
					return addArguments, deleteArguments, err
				}
				if !inUse {
					addArguments = append(addArguments, types.TuneArgument{Key: key, Bare: true})
				} else {
					glog.Infof(`skipping "%s" as it is already in use`, key)
				}
			} else {
				glog.Infof("%s not a whitelisted kernel argument", key)
			}
		} else if strings.HasPrefix(line, "DELETE ") {
			// NOTE: Today only specific bare kernel arguments are allowed so
			// there is not a need to split on =.
			key := strings.TrimSpace(line[len("DELETE "):])
			if isArgTunable(key) {
				inUse, err := isArgInUse(key, cmdLinePath)
				if err != nil {
					return addArguments, deleteArguments, err
				}
				if inUse {
					deleteArguments = append(deleteArguments, types.TuneArgument{Key: key, Bare: true})
				} else {
					glog.Infof(`skipping "%s" as it is not present in the current argument list`, key)
				}
			} else {
				glog.Infof("%s not a whitelisted kernel argument", key)
			}
		} else {
			glog.V(2).Infof(`skipping malformed line in %s: "%s"`, tuningFilePath, line)
		}
	}
	return addArguments, deleteArguments, nil
}

// updateTuningArgs executes additions and removals of kernel tuning arguments
func updateTuningArgs(tuningFilePath, cmdLinePath string) (bool, error) {
	if cmdLinePath == "" {
		cmdLinePath = cmdLineFile
	}
	changed := false
	additions, deletions, err := parseTuningFile(tuningFilePath, cmdLinePath)
	if err != nil {
		return changed, err
	}

	// Execute additions
	for _, toAdd := range additions {
		if toAdd.Bare {
			changed = true
			err := exec.Command("rpm-ostree", "kargs", fmt.Sprintf("--append=%s", toAdd.Key)).Run()
			if err != nil {
				return false, errors.Wrapf(err, "adding karg")
			}
		} else {
			panic("Not supported")
		}
	}
	// Execute deletions
	for _, toDelete := range deletions {
		if toDelete.Bare {
			changed = true
			err := exec.Command("rpm-ostree", "kargs", fmt.Sprintf("--delete=%s", toDelete.Key)).Run()
			if err != nil {
				return false, errors.Wrapf(err, "deleting karg")
			}
		} else {
			panic("Not supported")
		}
	}
	return changed, nil
}

func run(_ *cobra.Command, args []string) error {
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

	client := daemon.NewNodeUpdaterClient()

	_, changed, err := client.PullAndRebase(container, keep)
	if err != nil {
		return err
	}

	// Delete the file now that we successfully rebased
	if fromEtcPullSpec {
		if err := os.Remove(etcPivotFile); err != nil {
			if !os.IsNotExist(err) {
				return errors.Wrapf(err, "failed to delete %s", etcPivotFile)
			}
		}
	}

	// Check to see if we need to tune kernel arguments
	tuningChanged, err := updateTuningArgs(kernelTuningFile, cmdLineFile)
	if err != nil {
		return err
	}
	// If tuning changes but the oscontainer didn't we still denote we changed
	// for the reboot
	if tuningChanged {
		changed = true
	}

	if !changed {
		glog.Info("No changes; already at target oscontainer, no kernel args provided")
		return nil
	}

	if reboot {
		glog.Infof("Rebooting as requested by cmdline flag")
	} else {
		// Otherwise see if it's specified by the file
		_, err = os.Stat(runPivotRebootFile)
		if err != nil && !os.IsNotExist(err) {
			return errors.Wrapf(err, "Checking %s", runPivotRebootFile)
		}
		if err == nil {
			glog.Infof("Rebooting due to %s", runPivotRebootFile)
			reboot = true
		}
	}
	if reboot {
		// Reboot the machine if asked to do so
		err := exec.Command("systemctl", "reboot").Run()
		if err != nil {
			return errors.Wrapf(err, "rebooting")
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
