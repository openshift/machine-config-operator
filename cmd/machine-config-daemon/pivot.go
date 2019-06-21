package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"strings"

	// Enable sha256 in container image references
	_ "crypto/sha256"

	"github.com/golang/glog"
	daemon "github.com/openshift/machine-config-operator/pkg/daemon"
	"github.com/openshift/machine-config-operator/pkg/daemon/pivot/types"
	"github.com/openshift/machine-config-operator/pkg/daemon/pivot/utils"
	errors "github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// flag storage
var keep bool
var reboot bool

const (
	// the number of times to retry commands that pull data from the network
	numRetriesNetCommands = 5
	etcPivotFile          = "/etc/pivot/image-pullspec"
	runPivotRebootFile    = "/run/pivot/reboot-needed"
	// Pull secret.  Written by the machine-config-operator
	kubeletAuthFile = "/var/lib/kubelet/config.json"
	// File containing kernel arg changes for tuning
	kernelTuningFile = "/etc/pivot/kernel-args"
	cmdLineFile      = "/proc/cmdline"
)

// TODO: fill out the whitelist
// tuneableArgsWhitelist contains allowed keys for tunable arguments
var tuneableArgsWhitelist = map[string]bool{
	"nosmt": true,
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
			utils.Run("rpm-ostree", "kargs", fmt.Sprintf("--append=%s", toAdd.Key))
		} else {
			panic("Not supported")
		}
	}
	// Execute deletions
	for _, toDelete := range deletions {
		if toDelete.Bare {
			changed = true
			utils.Run("rpm-ostree", "kargs", fmt.Sprintf("--delete=%s", toDelete.Key))
		} else {
			panic("Not supported")
		}
	}
	return changed, nil
}

// podmanRemove kills and removes a container
func podmanRemove(cid string) {
	utils.RunIgnoreErr("podman", "kill", cid)
	utils.RunIgnoreErr("podman", "rm", "-f", cid)
}

// getDefaultDeployment uses rpm-ostree status --json to get the current deployment
func getDefaultDeployment() (*types.RpmOstreeDeployment, error) {
	// use --status for now, we can switch to D-Bus if we need more info
	var rosState types.RpmOstreeState
	output := utils.RunGetOut("rpm-ostree", "status", "--json")
	if err := json.Unmarshal([]byte(output), &rosState); err != nil {
		return nil, errors.Wrapf(err, "Failed to parse `rpm-ostree status --json` output")
	}

	// just make it a hard error if we somehow don't have any deployments
	if len(rosState.Deployments) == 0 {
		return nil, errors.New("Not currently booted in a deployment")
	}

	return &rosState.Deployments[0], nil
}

// pullAndRebase potentially rebases system if not already rebased.
func pullAndRebase(container string) (imgid string, changed bool, err error) {
	defaultDeployment, err := getDefaultDeployment()
	if err != nil {
		return
	}

	previousPivot := ""
	if len(defaultDeployment.CustomOrigin) > 0 {
		if strings.HasPrefix(defaultDeployment.CustomOrigin[0], "pivot://") {
			previousPivot = defaultDeployment.CustomOrigin[0][len("pivot://"):]
			glog.Infof("Previous pivot: %s", previousPivot)
		}
	}

	var authArgs []string
	if utils.FileExists(kubeletAuthFile) {
		authArgs = append(authArgs, "--authfile", kubeletAuthFile)
	}

	// If we're passed a non-canonical image, resolve it to its sha256 now
	isCanonicalForm := true
	if _, err = daemon.GetRefDigest(container); err != nil {
		isCanonicalForm = false
		// In non-canonical form, we pull unconditionally right now
		args := []string{"pull", "-q"}
		args = append(args, authArgs...)
		args = append(args, container)
		utils.RunExt(false, numRetriesNetCommands, "podman", args...)
	} else {
		var targetMatched bool
		targetMatched, err = daemon.CompareOSImageURL(previousPivot, container)
		if err != nil {
			return
		}
		if targetMatched {
			changed = false
			return
		}

		// Pull the image
		args := []string{"pull", "-q"}
		args = append(args, authArgs...)
		args = append(args, container)
		utils.RunExt(false, numRetriesNetCommands, "podman", args...)
	}

	inspectArgs := []string{"inspect", "--type=image"}
	inspectArgs = append(inspectArgs, fmt.Sprintf("%s", container))
	output := utils.RunExt(true, 1, "podman", inspectArgs...)
	var imagedataArray []types.ImageInspection
	json.Unmarshal([]byte(output), &imagedataArray)
	imagedata := imagedataArray[0]
	if !isCanonicalForm {
		imgid = imagedata.RepoDigests[0]
		glog.Infof("Resolved to: %s", imgid)
	} else {
		imgid = container
	}

	// Clean up a previous container
	podmanRemove(types.PivotName)

	// `podman mount` wants a container, so let's make create a dummy one, but not run it
	cid := utils.RunGetOut("podman", "create", "--net=none", "--name", types.PivotName, imgid)
	// Use the container ID to find its mount point
	mnt := utils.RunGetOut("podman", "mount", cid)
	repo := fmt.Sprintf("%s/srv/repo", mnt)

	// Now we need to figure out the commit to rebase to

	// Commit label takes priority
	ostreeCsum, ok := imagedata.Labels["com.coreos.ostree-commit"]
	if ok {
		if ostreeVersion, ok := imagedata.Labels["version"]; ok {
			glog.Infof("Pivoting to: %s (%s)", ostreeVersion, ostreeCsum)
		} else {
			glog.Infof("Pivoting to: %s", ostreeCsum)
		}
	} else {
		glog.Infof("No com.coreos.ostree-commit label found in metadata! Inspecting...")
		refs := strings.Split(utils.RunGetOut("ostree", "refs", "--repo", repo), "\n")
		if len(refs) == 1 {
			glog.Infof("Using ref %s", refs[0])
			ostreeCsum = utils.RunGetOut("ostree", "rev-parse", "--repo", repo, refs[0])
		} else if len(refs) > 1 {
			err = errors.New("multiple refs found in repo")
			return
		} else {
			// XXX: in the future, possibly scan the repo to find a unique .commit object
			err = errors.New("No refs found in repo")
			return
		}
	}

	// This will be what will be displayed in `rpm-ostree status` as the "origin spec"
	customURL := fmt.Sprintf("pivot://%s", imgid)

	// RPM-OSTree can now directly slurp from the mounted container!
	// https://github.com/projectatomic/rpm-ostree/pull/1732
	utils.Run("rpm-ostree", "rebase", "--experimental",
		fmt.Sprintf("%s:%s", repo, ostreeCsum),
		"--custom-origin-url", customURL,
		"--custom-origin-description", "Managed by machine-config-operator")

	// Kill our dummy container
	podmanRemove(types.PivotName)

	changed = true
	return
}

func run(_ *cobra.Command, args []string) error {
	var fromFile bool
	var container string
	if len(args) > 0 {
		container = args[0]
		fromFile = false
	} else {
		glog.Infof("Using image pullspec from %s", etcPivotFile)
		data, err := ioutil.ReadFile(etcPivotFile)
		if err != nil {
			return errors.Wrapf(err, "failed to read from %s", etcPivotFile)
		}
		container = strings.TrimSpace(string(data))
		fromFile = true
	}

	imgid, changed, err := pullAndRebase(container)
	if err != nil {
		return err
	}

	// Delete the file now that we successfully rebased
	if fromFile {
		if err := os.Remove(etcPivotFile); err != nil {
			if !os.IsNotExist(err) {
				return errors.Wrapf(err, "failed to delete %s", etcPivotFile)
			}
		}
	}

	// By default, delete the image.
	if !keep {
		// Related: https://github.com/containers/libpod/issues/2234
		utils.RunIgnoreErr("podman", "rmi", imgid)
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
		glog.Info("Already at target oscontainer")
	} else if reboot || utils.FileExists(runPivotRebootFile) {
		// Reboot the machine if asked to do so
		utils.Run("systemctl", "reboot")
	}
	return nil
}

// Execute runs the command
func Execute(cmd *cobra.Command, args []string) {
	err := run(cmd, args)
	if err != nil {
		glog.Fatalf("%v", err)
	}
}
