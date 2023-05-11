package daemon

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"

	// Enable sha256 in container image references
	_ "crypto/sha256"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/pkg/daemon/osrelease"
	"github.com/openshift/machine-config-operator/pkg/daemon/pivot/types"
)

const (
	// KernelTuningFile is a path to the file containing kernel arg changes for tuning
	KernelTuningFile = "/etc/pivot/kernel-args"
	// CmdLineFile is a path to file with kernel cmdline
	CmdLineFile = "/proc/cmdline"
)

// TODO: fill out the allowlists
// tuneableRHCOSArgsAllowlist contains allowed keys for tunable kernel arguments on RHCOS
var tuneableRHCOSArgsAllowlist = map[string]bool{
	"nosmt": true,
}

// isArgTuneable returns if the argument provided is allowed to be modified
func isArgTunable(arg string) (bool, error) {
	os, err := osrelease.GetHostRunningOS()
	if err != nil {
		return false, fmt.Errorf("failed to get OS for determining whether kernel arg is tuneable: %w", err)
	}

	if os.IsEL() {
		return tuneableRHCOSArgsAllowlist[arg], nil
	} else if os.IsFCOS() {
		return true, nil
	}
	return false, nil
}

// isArgInUse checks to see if the argument is already in use by the system currently
func isArgInUse(arg, cmdLinePath string) (bool, error) {
	if cmdLinePath == "" {
		cmdLinePath = CmdLineFile
	}
	content, err := os.ReadFile(cmdLinePath)
	if err != nil {
		return false, err
	}

	checkable := strings.TrimSpace(string(content))
	if strings.Contains(" "+checkable+" ", " "+arg+" ") {
		return true, nil
	}
	return false, nil
}

// parseTuningFile parses the kernel argument tuning file
func parseTuningFile(tuningFilePath, cmdLinePath string) ([]types.TuneArgument, []types.TuneArgument, error) {
	addArguments := []types.TuneArgument{}
	deleteArguments := []types.TuneArgument{}
	if tuningFilePath == "" {
		tuningFilePath = KernelTuningFile
	}
	if cmdLinePath == "" {
		cmdLinePath = CmdLineFile
	}
	// Read and parse the file
	file, err := os.Open(tuningFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			// It's ok if the file doesn't exist
			return addArguments, deleteArguments, nil
		}
		return addArguments, deleteArguments, fmt.Errorf("reading %s: %w", tuningFilePath, err)
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

// UpdateTuningArgs executes additions and removals of kernel tuning arguments
func UpdateTuningArgs(tuningFilePath, cmdLinePath string) error {
	if cmdLinePath == "" {
		cmdLinePath = CmdLineFile
	}
	changed := false
	additions, deletions, err := parseTuningFile(tuningFilePath, cmdLinePath)
	if err != nil {
		return err
	}

	// Execute additions
	for _, toAdd := range additions {
		if toAdd.Bare {
			changed = true
			err := exec.Command("rpm-ostree", "kargs", fmt.Sprintf("--append=%s", toAdd.Key)).Run()
			if err != nil {
				return fmt.Errorf("failed adding karg: %w", err)
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
				return fmt.Errorf("failed deleting karg: %w", err)
			}
		} else {
			panic("Not supported")
		}
	}

	if changed {
		glog.Info("Updated kernel tuning arguments")
	}
	return nil
}
