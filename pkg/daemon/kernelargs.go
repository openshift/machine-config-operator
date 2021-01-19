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
	"github.com/openshift/machine-config-operator/pkg/daemon/pivot/types"
	errors "github.com/pkg/errors"
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

// tuneableFCOSArgsAllowlist contains allowed keys for tunable kernel arguments on FCOS
var tuneableFCOSArgsAllowlist = map[string]bool{
	"systemd.unified_cgroup_hierarchy=0": true,
	"mitigations=auto,nosmt":             true,
}

// isArgTuneable returns if the argument provided is allowed to be modified
func isArgTunable(arg string) (bool, error) {
	os, err := GetHostRunningOS()
	if err != nil {
		return false, errors.Errorf("failed to get OS for determining whether kernel arg is tuneable: %v", err)
	}

	if os.IsRHCOS() {
		return tuneableRHCOSArgsAllowlist[arg], nil
	} else if os.IsFCOS() {
		return tuneableFCOSArgsAllowlist[arg], nil
	}
	return false, nil
}

// isKernelArgInUse checks to see if the argument is already in use by the system currently
func isKernelArgInUse(arg string) (bool, error) {
	nu := NewNodeUpdaterClient()
	checkable, err := nu.GetKernelArgs()
	if err != nil {
		return false, err
	}

	for _, v := range checkable {
		if strings.HasPrefix(v, arg) {
			return true, nil
		}
	}
	return false, nil
}

// parseTuningFile parses the kernel argument tuning file
func parseTuningFile(tuningFilePath string) ([]types.TuneArgument, []types.TuneArgument, error) {
	addArguments := []types.TuneArgument{}
	deleteArguments := []types.TuneArgument{}
	if tuningFilePath == "" {
		tuningFilePath = KernelTuningFile
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

		var key string
		var operation string
		for _, k := range []string{"ADD", "DELETE"} {
			if strings.HasPrefix(line, k) {
				key = strings.TrimPrefix(fmt.Sprintf("%s ", k), k)
				operation = strings.TrimSpace(k)
				glog.V(2).Infof("Requested to %s kernel argument %s", operation, key)
				break
			}
		}

		if key == "" {
			glog.Warningf("Malformed or unknown kernel arg directive %q, skipping", line)
			continue
		}

		// NOTE: Today only specific bare kernel arguments are allowed so
		// there is not a need to split on =.
		tuneableKarg, err := isArgTunable(key)
		if err != nil {
			return addArguments, deleteArguments, err
		}
		if !tuneableKarg {
			glog.Infof("Skipping unsupported kernel tunable %q", key)
			continue
		}

		// Find out if the argument is in use
		inUse, err := isKernelArgInUse(key)
		if err != nil {
			return addArguments, deleteArguments, err
		}

		if operation == "ADD" {
			if !inUse {
				addArguments = append(addArguments, types.TuneArgument{Key: key, Bare: true})
			} else {
				glog.Infof("Kernal Argument %q is already used, skipping addition", key)
			}
		}
		if operation == "DELETE" {
			if inUse {
				deleteArguments = append(deleteArguments, types.TuneArgument{Key: key, Bare: true})
			} else {
				glog.Infof("Kernel argument %q is not currently used, skipping removal", key)
			}
		}
	}
	return addArguments, deleteArguments, nil
}

// UpdateTuningArgs executes additions and removals of kernel tuning arguments
func UpdateTuningArgs(tuningFilePath string) (bool, error) {
	changed := false
	additions, deletions, err := parseTuningFile(tuningFilePath)
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
