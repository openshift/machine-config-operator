package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/containers/image/v5/types"
	"github.com/golang/glog"
	"github.com/opencontainers/go-digest"
	pivotutils "github.com/openshift/machine-config-operator/pkg/daemon/pivot/utils"
)

const (
	// the number of times to retry commands that pull data from the network
	numRetriesNetCommands = 5
	// Pull secret.  Written by the machine-config-operator
	kubeletAuthFile = "/var/lib/kubelet/config.json"
)

// rpmOstreeState houses zero or more RpmOstreeDeployments
// Subset of `rpm-ostree status --json`
// https://github.com/projectatomic/rpm-ostree/blob/bce966a9812df141d38e3290f845171ec745aa4e/src/daemon/rpmostreed-deployment-utils.c#L227
type rpmOstreeState struct {
	Deployments []RpmOstreeDeployment
	Transaction *[]string
}

// RpmOstreeDeployment represents a single deployment on a node
type RpmOstreeDeployment struct {
	ID                      string   `json:"id"`
	OSName                  string   `json:"osname"`
	Serial                  int32    `json:"serial"`
	Checksum                string   `json:"checksum"`
	Version                 string   `json:"version"`
	Timestamp               uint64   `json:"timestamp"`
	Booted                  bool     `json:"booted"`
	Staged                  bool     `json:"staged"`
	LiveReplaced            string   `json:"live-replaced,omitempty"`
	Origin                  string   `json:"origin"`
	CustomOrigin            []string `json:"custom-origin"`
	ContainerImageReference string   `json:"container-image-reference"`
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
	Initialize() error
	GetStatus() (string, error)
	GetBootedOSImageURL() (string, string, error)
	Rebase(string, string) (bool, error)
	RebaseLayered(string) error
	IsBootableImage(string) (bool, error)
	GetBootedAndStagedDeployment() (*RpmOstreeDeployment, *RpmOstreeDeployment, error)
}

// RpmOstreeClient provides all RpmOstree related methods in one structure.
// This structure implements DeploymentClient
//
// TODO(runcom): make this private to pkg/daemon!!!
type RpmOstreeClient struct{}

// NewNodeUpdaterClient returns a new instance of the default DeploymentClient (RpmOstreeClient)
func NewNodeUpdaterClient() NodeUpdaterClient {
	return &RpmOstreeClient{}
}

// Synchronously invoke rpm-ostree, writing its stdout to our stdout,
// and gathering stderr into a buffer which will be returned in err
// in case of error.
func runRpmOstree(args ...string) error {
	return runCmdSync("rpm-ostree", args...)
}

func (r *RpmOstreeClient) loadStatus() (*rpmOstreeState, error) {
	var rosState rpmOstreeState
	output, err := runGetOut("rpm-ostree", "status", "--json")
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(output, &rosState); err != nil {
		return nil, fmt.Errorf("failed to parse `rpm-ostree status --json` output (%s): %w", truncate(string(output), 30), err)
	}

	return &rosState, nil
}

func (r *RpmOstreeClient) Initialize() error {
	// This used to have some workarounds for rpm-ostree bugs, but we no longer need those.
	return nil
}

// GetBootedDeployment returns the current deployment found
func (r *RpmOstreeClient) GetBootedAndStagedDeployment() (booted, staged *RpmOstreeDeployment, err error) {
	status, err := r.loadStatus()
	if err != nil {
		return nil, nil, err
	}

	booted, err = status.getBootedDeployment()
	staged = status.getStagedDeployment()

	return
}

// GetStatus returns multi-line human-readable text describing system status
func (r *RpmOstreeClient) GetStatus() (string, error) {
	output, err := runGetOut("rpm-ostree", "status")
	if err != nil {
		return "", err
	}

	return string(output), nil
}

// GetBootedOSImageURL returns the image URL as well as the OSTree version (for logging)
// Returns the empty string if the host doesn't have a custom origin that matches pivot://
// (This could be the case for e.g. FCOS, or a future RHCOS which comes not-pivoted by default)
func (r *RpmOstreeClient) GetBootedOSImageURL() (string, string, error) {
	bootedDeployment, _, err := r.GetBootedAndStagedDeployment()
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

	// we have container images now, make sure we can parse those too
	if bootedDeployment.ContainerImageReference != "" {
		// right now they start with "ostree-unverified-registry:", so scrape that off
		tokens := strings.SplitN(bootedDeployment.ContainerImageReference, ":", 2)
		if len(tokens) > 1 {
			osImageURL = tokens[1]
		}
	}
	return osImageURL, bootedDeployment.Version, nil
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
		err = fmt.Errorf("unmarshaling podman inspect: %w", err)
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
	defaultDeployment, _, err := r.GetBootedAndStagedDeployment()
	if err != nil {
		return
	}

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
			err = fmt.Errorf("multiple refs found in repo")
			return
		} else {
			// XXX: in the future, possibly scan the repo to find a unique .commit object
			err = fmt.Errorf("no refs found in repo")
			return
		}
	}

	// This will be what will be displayed in `rpm-ostree status` as the "origin spec"
	customURL := fmt.Sprintf("pivot://%s", imgURL)
	glog.Infof("Executing rebase from repo path %s with customImageURL %s and checksum %s", repo, customURL, ostreeCsum)

	args := []string{"rebase", "--experimental", fmt.Sprintf("%s:%s", repo, ostreeCsum),
		"--custom-origin-url", customURL, "--custom-origin-description", "Managed by machine-config-operator"}

	if err = runRpmOstree(args...); err != nil {
		return
	}

	changed = true
	return
}

// IsBootableImage determines if the image is a bootable (new container formet) image, or a wrapper (old container format)
func (r *RpmOstreeClient) IsBootableImage(imgURL string) (bool, error) {

	// TODO(jkyros): This is duplicated-ish from Rebase(), do we still need to carry this around?
	var isBootableImage string
	var imageData *types.ImageInspectInfo
	var err error
	if imageData, err = imageInspect(imgURL); err != nil {
		if err != nil {
			var podmanImgData *imageInspection
			glog.Infof("Falling back to using podman inspect")

			if podmanImgData, err = podmanInspect(imgURL); err != nil {
				return false, err
			}
			isBootableImage = podmanImgData.Labels["ostree.bootable"]
		}
	} else {
		isBootableImage = imageData.Labels["ostree.bootable"]
	}
	// We may have pulled in OSContainer image as fallback during podmanCopy() or podmanInspect()
	defer exec.Command("podman", "rmi", imgURL).Run()

	return isBootableImage == "true", nil
}

// RebaseLayered rebases system or errors if already rebased
func (r *RpmOstreeClient) RebaseLayered(imgURL string) (err error) {
	glog.Infof("Executing rebase to %s", imgURL)

	// For now, just let ostree use the kublet config.json,
	err = useKubeletConfigSecrets()
	if err != nil {
		return fmt.Errorf("Error while ensuring access to kublet config.json pull secrets: %w", err)
	}

	return runRpmOstree("rebase", "--experimental", "ostree-unverified-registry:"+imgURL)
}

// useKubeletConfigSecrets gives the rpm-ostree client access to secrets in the kubelet config.json by symlinking so that
// rpm-ostree can use those secrets to pull images. It does this by symlinking the kubelet's config.json into /run/ostree.
func useKubeletConfigSecrets() error {
	if _, err := os.Stat("/run/ostree/auth.json"); err != nil {

		if errors.Is(err, os.ErrNotExist) {

			err := os.MkdirAll("/run/ostree", 0o544)
			if err != nil {
				return err
			}

			err = os.Symlink(kubeletAuthFile, "/run/ostree/auth.json")
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// truncate a string using runes/codepoints as limits.
// This specifically will avoid breaking a UTF-8 value.
func truncate(input string, limit int) string {
	asRunes := []rune(input)
	l := len(asRunes)

	if limit >= l {
		return input
	}

	return fmt.Sprintf("%s [%d more chars]", string(asRunes[:limit]), l-limit)
}

// runGetOut executes a command, logging it, and return the stdout output.
func runGetOut(command string, args ...string) ([]byte, error) {
	glog.Infof("Running captured: %s %s", command, strings.Join(args, " "))
	cmd := exec.Command(command, args...)
	rawOut, err := cmd.Output()
	if err != nil {
		errtext := ""
		if e, ok := err.(*exec.ExitError); ok {
			// Trim to max of 256 characters
			errtext = fmt.Sprintf("\n%s", truncate(string(e.Stderr), 256))
		}
		return nil, fmt.Errorf("error running %s %s: %s%s", command, strings.Join(args, " "), err, errtext)
	}
	return rawOut, nil
}

func (state *rpmOstreeState) getBootedDeployment() (*RpmOstreeDeployment, error) {
	for num := range state.Deployments {
		deployment := state.Deployments[num]
		if deployment.Booted {
			return &deployment, nil
		}
	}
	return &RpmOstreeDeployment{}, fmt.Errorf("not currently booted in a deployment")
}

func (state *rpmOstreeState) getStagedDeployment() *RpmOstreeDeployment {
	for num := range state.Deployments {
		deployment := state.Deployments[num]
		if deployment.Staged {
			return &deployment
		}
	}
	return &RpmOstreeDeployment{}
}
