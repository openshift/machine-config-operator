package main

import (
	"bufio"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	// Enable sha256 in container image references
	_ "crypto/sha256"

	"github.com/golang/glog"
	daemon "github.com/openshift/machine-config-operator/pkg/daemon"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/daemon/pivot/types"
	errors "github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// flag storage
var keep bool
var fromEtcPullSpec bool

const (
	// etcPivotFile is used for 4.1 bootimages and is how the MCD
	// currently communicated with this service.
	etcPivotFile = "/etc/pivot/image-pullspec"
	// File containing kernel arg changes for tuning
	kernelTuningFile = "/etc/pivot/kernel-args"
	cmdLineFile      = "/proc/cmdline"
)

// TODO: fill out the allowlists
// tuneableRHCOSArgsAllowlist contains allowed keys for tunable kernel arguments on RHCOS
var tuneableRHCOSArgsAllowlist = map[string]bool{
	"nosmt": true,
}

// tuneableFCOSArgsAllowlist contains allowed keys for tunable kernel arguments on FCOS
var tuneableFCOSArgsAllowlist = map[string]bool{
	"systemd.unified_cgroup_hierarchy=0": true,
	"mitigations=auto,nosmt":             true,
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
	pivotCmd.PersistentFlags().BoolVarP(&fromEtcPullSpec, "from-etc-pullspec", "P", false, "Parse /etc/pivot/image-pullspec")
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
}

// isArgTuneable returns if the argument provided is allowed to be modified
func isArgTunable(arg string) (bool, error) {
	operatingSystem, err := daemon.GetHostRunningOS()
	if err != nil {
		return false, errors.Errorf("failed to get OS for determining whether kernel arg is tuneable: %v", err)
	}

	switch operatingSystem {
	case daemon.MachineConfigDaemonOSRHCOS:
		return tuneableRHCOSArgsAllowlist[arg], nil
	case daemon.MachineConfigDaemonOSFCOS:
		return tuneableFCOSArgsAllowlist[arg], nil
	default:
		return false, nil
	}
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
			tuneableKarg, err := isArgTunable(key)
			if err != nil {
				return addArguments, deleteArguments, err
			}
			if tuneableKarg {
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
				glog.Infof("%s not an allowlisted kernel argument", key)
			}
		} else if strings.HasPrefix(line, "DELETE ") {
			// NOTE: Today only specific bare kernel arguments are allowed so
			// there is not a need to split on =.
			key := strings.TrimSpace(line[len("DELETE "):])
			tuneableKarg, err := isArgTunable(key)
			if err != nil {
				return addArguments, deleteArguments, err
			}
			if tuneableKarg {
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
				glog.Infof("%s not an allowlisted kernel argument", key)
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

func run(_ *cobra.Command, args []string) (retErr error) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	var containerName string
	if fromEtcPullSpec || len(args) == 0 {
		// In this case we will just rebase to OSImage provided in etcPivotFile.
		// Extensions won't apply. This should be consistent with old behavior.
		fromEtcPullSpec = true
		data, err := ioutil.ReadFile(etcPivotFile)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("No container specified")
			}
			return errors.Wrapf(err, "failed to read from %s", etcPivotFile)
		}
		container := strings.TrimSpace(string(data))
		unitName := "mco-mount-container"
		glog.Infof("Pulling in image and mounting container on host via systemd-run unit=%s", unitName)
		err = exec.Command("systemd-run", "--wait", "--collect", "--unit="+unitName,
			"-E", "RPMOSTREE_CLIENT_ID=mco", constants.HostSelfBinary, "mount-container", container).Run()
		if err != nil {
			return errors.Wrapf(err, "failed to create extensions repo")
		}
		var containerName string
		if containerName, err = daemon.ReadFromFile(constants.MountedOSContainerName); err != nil {
			return err
		}

		defer func() {
			// Ideally other than MCD pivot, OSContainer shouldn't be needed by others.
			// With above assumption, we should be able to delete OSContainer image as well as associated container with force option
			exec.Command("podman", "rm", containerName).Run()
			exec.Command("podman", "rmi", container).Run()
			glog.Infof("Deleted container %s and corresponding image %s", containerName, container)
		}()

	} else {
		containerName = args[0]
	}

	client := daemon.NewNodeUpdaterClient()

	changed, err := client.PerformRpmOSTreeOperations(containerName, keep)
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
	}

	return nil
}

// Execute runs the command
func Execute(cmd *cobra.Command, args []string) {
	err := run(cmd, args)
	if err != nil {
		fmt.Printf("error: %v\n", err)
		os.MkdirAll(filepath.Dir(types.PivotFailurePath), 0755)
		// write a pivot failure file that we'll read from MCD since we start this with systemd
		// and we just follow logs
		ioutil.WriteFile(types.PivotFailurePath, []byte(err.Error()), 0644)
		os.Exit(1)
	}
}
