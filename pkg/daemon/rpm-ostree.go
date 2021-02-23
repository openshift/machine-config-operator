package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/containers/image/v5/types"
	"github.com/golang/glog"
	"github.com/opencontainers/go-digest"
	pivotutils "github.com/openshift/machine-config-operator/pkg/daemon/pivot/utils"
	"github.com/pkg/errors"
)

const (
	// the number of times to retry commands that pull data from the network
	numRetriesNetCommands = 5
	// Pull secret.  Written by the machine-config-operator
	kubeletAuthFile = "/var/lib/kubelet/config.json"

	// realRpmOstreeCmd is the binary name for rpmOstree
	realRpmOstreeCmd = "/usr/bin/rpm-ostree"
)

// rpmOstreeState houses zero or more RpmOstreeDeployments
// Subset of `rpm-ostree status --json`
// https://github.com/projectatomic/rpm-ostree/blob/bce966a9812df141d38e3290f845171ec745aa4e/src/daemon/rpmostreed-deployment-utils.c#L227
type rpmOstreeState struct {
	Deployments []RpmOstreeDeployment
}

// RpmOstreeDeployment represents a single deployment on a node
type RpmOstreeDeployment struct {
	ID           string   `json:"id"`
	OSName       string   `json:"osname"`
	Serial       int32    `json:"serial"`
	Checksum     string   `json:"checksum"`
	Version      string   `json:"version"`
	Timestamp    uint64   `json:"timestamp"`
	Booted       bool     `json:"booted"`
	Origin       string   `json:"origin"`
	CustomOrigin []string `json:"custom-origin"`
}

// imageInspection is a public implementation of
// https://github.com/containers/skopeo/blob/82186b916faa9c8c70cfa922229bafe5ae024dec/cmd/skopeo/inspect.go#L20-L31
type imageInspection struct {
	Name          string `json:",omitempty"`
	Tag           string `json:",omitempty"`
	Digest        digest.Digest
	RepoDigests   []string
	Created       *time.Time
	DockerVersion string
	Labels        map[string]string
	Architecture  string
	Os            string
	Layers        []string
}

// NodeUpdaterClient is an interface describing how to interact with the host
// around content deployment
type NodeUpdaterClient interface {
	GetBootedDeployment() (*RpmOstreeDeployment, error)
	GetBootedOSImageURL() (string, string, error)
	GetKernelArgs() ([]string, error)
	GetStatus() (string, error)
	Rebase(string, string) (bool, error)
	RemovePendingDeployment() error
	SetKernelArgs([]KernelArgument) (string, error)
	RunRpmOstree(string, ...string) ([]byte, error)
}

// RpmOstreeClient provides all RpmOstree related methods in one structure.
// This structure implements DeploymentClient
//
// TODO(runcom): make this private to pkg/daemon!!!
type RpmOstreeClient struct {
	runRpmOstreeFunc rpmOstreeCommander
}

// NewNodeUpdaterClient returns a new instance of the default DeploymentClient (RpmOstreeClient)
func NewNodeUpdaterClient() NodeUpdaterClient {
	os, err := GetHostRunningOS()
	if err != nil {
		panic(fmt.Sprintf("Failed to query operating system: %v", err))
	}
	if !os.IsCoreOSVariant() {
		glog.Infof("Host operating system %q is not a CoreOS variant", os.ID)
		return &notCoreOSClient{}
	}

	return &RpmOstreeClient{
		runRpmOstreeFunc: runRpmOstree,
	}
}

// rpmOstreeCommander is a function for wrapping and mocking 'rpm-ostree'
type rpmOstreeCommander func(string, ...string) ([]byte, error)

// RunRpmOstree is an rpmOstreeCommander and executes r.rpmOstreeFunc
func (r *RpmOstreeClient) RunRpmOstree(noun string, args ...string) ([]byte, error) {
	return r.runRpmOstreeFunc(noun, args...)
}

// GetBootedDeployment returns the current deployment found
func (r *RpmOstreeClient) GetBootedDeployment() (*RpmOstreeDeployment, error) {
	var rosState rpmOstreeState
	output, err := r.RunRpmOstree("status", "--json")
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(output, &rosState); err != nil {
		return nil, fmt.Errorf("failed to parse `rpm-ostree status --json` output: %v", err)
	}

	for _, deployment := range rosState.Deployments {
		if deployment.Booted {
			deployment := deployment
			return &deployment, nil
		}
	}

	return nil, fmt.Errorf("not currently booted in a deployment")
}

// GetStatus returns multi-line human-readable text describing system status
func (r *RpmOstreeClient) GetStatus() (string, error) {
	output, err := r.RunRpmOstree("status")
	if err != nil {
		return "", err
	}

	return string(output), nil
}

// GetBootedOSImageURL returns the image URL as well as the OSTree version (for logging)
// Returns the empty string if the host doesn't have a custom origin that matches pivot://
// (This could be the case for e.g. FCOS, or a future RHCOS which comes not-pivoted by default)
func (r *RpmOstreeClient) GetBootedOSImageURL() (string, string, error) {
	bootedDeployment, err := r.GetBootedDeployment()
	if err != nil {
		return "", "", err
	}

	// the canonical image URL is stored in the custom origin field.
	osImageURL := ""
	if len(bootedDeployment.CustomOrigin) > 0 {
		if strings.HasPrefix(bootedDeployment.CustomOrigin[0], "pivot://") {
			osImageURL = bootedDeployment.CustomOrigin[0][len("pivot://"):]
		}
	}

	return osImageURL, bootedDeployment.Version, nil
}

// quoteSpaceSplit splits on spaces unless the space is quoted
// For example, 'boo=bar="YIPPIE KA YAY" baz foo' will split into
// ['boo=bar="YIPPIE KA YAY"', "baz", "foo"]
func quoteSpaceSplit(s string) []string {
	quoted := false
	return strings.FieldsFunc(s, func(r rune) bool {
		if r == '"' {
			quoted = !quoted
		}
		return !quoted && r == ' '
	})
}

// GetKernelArgs returns the kernel arguments known to rpm-ostree
func (r *RpmOstreeClient) GetKernelArgs() ([]string, error) {
	out, err := r.RunRpmOstree("kargs")
	if err != nil {
		return nil, err
	}
	return quoteSpaceSplit(string(out)), nil
}

func podmanInspect(imgURL string) (imgdata *imageInspection, err error) {
	// Pull the container image if not already available
	var authArgs []string
	if _, err := os.Stat(kubeletAuthFile); err == nil {
		authArgs = append(authArgs, "--authfile", kubeletAuthFile)
	}
	args := []string{"pull", "-q"}
	args = append(args, authArgs...)
	args = append(args, imgURL)
	_, err = pivotutils.RunExt(numRetriesNetCommands, "podman", args...)
	if err != nil {
		return
	}

	inspectArgs := []string{"inspect", "--type=image"}
	inspectArgs = append(inspectArgs, fmt.Sprintf("%s", imgURL))
	var output []byte
	output, err = runGetOut("podman", inspectArgs...)
	if err != nil {
		return
	}
	var imagedataArray []imageInspection
	err = json.Unmarshal(output, &imagedataArray)
	if err != nil {
		err = errors.Wrapf(err, "unmarshaling podman inspect")
		return
	}
	imgdata = &imagedataArray[0]
	return

}

// Rebase potentially rebases system if not already rebased.
func (r *RpmOstreeClient) Rebase(imgURL, osImageContentDir string) (changed bool, err error) {
	var (
		ostreeCsum    string
		ostreeVersion string
	)
	defaultDeployment, err := r.GetBootedDeployment()
	if err != nil {
		return
	}

	glog.Infof("Updating OS to %s", imgURL)

	previousPivot := ""
	if len(defaultDeployment.CustomOrigin) > 0 {
		if strings.HasPrefix(defaultDeployment.CustomOrigin[0], "pivot://") {
			previousPivot = defaultDeployment.CustomOrigin[0][len("pivot://"):]
			glog.Infof("Previous pivot: %s", previousPivot)
		} else {
			glog.Infof("Previous custom origin: %s", defaultDeployment.CustomOrigin[0])
		}
	} else {
		glog.Info("Current origin is not custom")
	}

	var imageData *types.ImageInspectInfo
	if imageData, err = imageInspect(imgURL); err != nil {
		if err != nil {
			var podmanImgData *imageInspection
			glog.Infof("Falling back to using podman inspect")
			if podmanImgData, err = podmanInspect(imgURL); err != nil {
				return
			}
			ostreeCsum = podmanImgData.Labels["com.coreos.ostree-commit"]
			ostreeVersion = podmanImgData.Labels["version"]
		}
	} else {
		ostreeCsum = imageData.Labels["com.coreos.ostree-commit"]
		ostreeVersion = imageData.Labels["version"]
	}
	// We may have pulled in OSContainer image as fallback during podmanCopy() or podmanInspect()
	defer exec.Command("podman", "rmi", imgURL).Run()

	repo := fmt.Sprintf("%s/srv/repo", osImageContentDir)

	// Now we need to figure out the commit to rebase to
	// Commit label takes priority
	if ostreeCsum != "" {
		if ostreeVersion != "" {
			glog.Infof("Pivoting to: %s (%s)", ostreeVersion, ostreeCsum)
		} else {
			glog.Infof("Pivoting to: %s", ostreeCsum)
		}
	} else {
		glog.Infof("No com.coreos.ostree-commit label found in metadata! Inspecting...")
		var refText []byte
		refText, err = runGetOut("ostree", "refs", "--repo", repo)
		if err != nil {
			return
		}
		refs := strings.Split(strings.TrimSpace(string(refText)), "\n")
		if len(refs) == 1 {
			glog.Infof("Using ref %s", refs[0])
			var ostreeCsumBytes []byte
			ostreeCsumBytes, err = runGetOut("ostree", "rev-parse", "--repo", repo, refs[0])
			if err != nil {
				return
			}
			ostreeCsum = strings.TrimSpace(string(ostreeCsumBytes))
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
	customURL := fmt.Sprintf("pivot://%s", imgURL)
	glog.Infof("Executing rebase from repo path %s with customImageURL %s and checksum %s", repo, customURL, ostreeCsum)

	args := []string{"rebase", "--experimental", fmt.Sprintf("%s:%s", repo, ostreeCsum),
		"--custom-origin-url", customURL, "--custom-origin-description", "Managed by machine-config-operator"}

	_, err = r.RunRpmOstree(args[0], args[1:]...)
	changed = true
	return
}

// constant for describing kernel argument operations
const (
	kargRemove int = iota
	kargAdd
)

// KernelArgument describes an operation for karg handling
type KernelArgument struct {
	Operation int
	Name      string
}

// validateRpmOstreeCommand checks the "noun" and arguments to ensure
// that the expected number of arguments (or lack thereof) are present. This function
// is not exhaustive of the rpm-ostree commands, just the commands that the MCD uses.
// The validateRpmOstreeCommand is intended for us in runRpmOstree and mocking functions.
func validateRpmOstreeCommand(noun string, args ...string) error {
	errNotEnoughArgs := errors.New("rpm-ostree command does not have enough arguments")
	errTooManyArgs := errors.New("rpm-ostree command called with too many arguments")
	errArgsNotSupported := errors.New("daemon does not support specific rpm-ostree command")

	hasArgs := func() bool {
		if args != nil && len(args) > 0 {
			return true
		}
		return false
	}

	checker := func(cmd string, supported []string, allowEmpty bool) error {
		if allowEmpty && !hasArgs() {
			return nil
		}
		if !allowEmpty && !hasArgs() {
			return fmt.Errorf("'%s': %v", errNotEnoughArgs, cmd)
		}

		for _, v := range args {
			found := false
			for _, s := range supported {
				if strings.HasPrefix(v, s) {
					found = true
					continue
				}
			}
			if !found {
				return fmt.Errorf("'%s %s': %v", errArgsNotSupported, cmd, v)
			}
		}
		return nil
	}

	switch noun {
	// Argless commands
	case "cancel", "rollback":
		if hasArgs() {
			return errTooManyArgs
		}
		return nil

	// Check that "kargs" is either a query or a supported append/delete command.
	case "kargs":
		return checker("rpm-ostree kargs", []string{"--append=", "--delete"}, true)

	// Dumb checks for package operations.
	case "install", "rebase", "override", "upgrade", "uninstall":
		if hasArgs() {
			return nil
		}
		return errNotEnoughArgs

	// cleanup only supports -p
	case "cleanup":
		return checker("rpm-ostree cleanup", []string{"-p"}, false)

	case "status":
		return checker("rpm-ostree status", []string{"--json", "--peer"}, true)

	// known error conditions
	case "":
		return errNotEnoughArgs
	default:
		return fmt.Errorf("unsupported command 'rpm-ostree %s'", noun)
	}
}

// runRpmOStree wraps the rpm-ostree command. Unless mocked, this
// is the rpmOstreeCommander that is used by RpmOstreeClient.
func runRpmOstree(noun string, args ...string) ([]byte, error) {
	fullArgs := []string{noun}
	fullArgs = append(fullArgs, args...)

	if err := validateRpmOstreeCommand(noun, args...); err != nil {
		return nil, err
	}

	glog.Infof("Executing cmd: '%s %s'", realRpmOstreeCmd, fullArgs)
	out, err := runGetOut(realRpmOstreeCmd, args...)
	if err != nil {
		glog.Errorf("'%s %v' failed to run: %v:\n %s", realRpmOstreeCmd, fullArgs, string(out), err)
	}
	return out, err
}

// SetKernelArgs sets kernel arguments on an RPM OStree systeR
func (r *RpmOstreeClient) SetKernelArgs(args []KernelArgument) (string, error) {
	var kargs []string
	for _, v := range args {
		switch v.Operation {
		case kargAdd:
			kargs = append(kargs, fmt.Sprintf("--append=%s", v.Name))
		case kargRemove:
			inUse, err := r.isKernelArgInUse(v.Name)
			if err != nil {
				return "", err
			}
			if inUse {
				kargs = append(kargs, fmt.Sprintf("--delete=%s", v.Name))
			}
		}
	}
	if len(kargs) == 0 {
		return "", nil
	}
	out, err := r.RunRpmOstree("kargs", kargs...)
	return string(out), err
}

// isKernelArgInUse checks to see if the argument is already in use by the system currently
func (r RpmOstreeClient) isKernelArgInUse(arg string) (bool, error) {
	checkable, err := r.GetKernelArgs()
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

// RemovePendingDeployment removes any pending rpm-ostree deployments
func (r *RpmOstreeClient) RemovePendingDeployment() error {
	_, err := r.RunRpmOstree("cleanup", "-p")
	return err
}

// runGetOut executes a command, logging it, and return the stdout output.
func runGetOut(command string, args ...string) ([]byte, error) {
	glog.Infof("Running captured: %s %s", command, strings.Join(args, " "))
	cmd := exec.Command(command, args...)
	rawOut, err := cmd.CombinedOutput()
	if err != nil {
		return nil, errors.Wrapf(err, "error running %s %s: %s", command, strings.Join(args, " "), string(rawOut))
	}
	return rawOut, nil
}
