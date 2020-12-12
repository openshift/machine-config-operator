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
	Start() error
	GetStatus() (string, error)
	GetBootedOSImageURL() (string, string, error)
	Rebase(string, string) (bool, error)
	GetBootedDeployment() (*RpmOstreeDeployment, error)
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

// GetBootedDeployment returns the current deployment found
func (r *RpmOstreeClient) GetBootedDeployment() (*RpmOstreeDeployment, error) {
	var rosState rpmOstreeState
	output, err := runGetOut("rpm-ostree", "status", "--json")
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

// Start ensures the daemon is running; the DBus activation timeout
// is shorter than the systemd timeout.  xref
// https://bugzilla.redhat.com/show_bug.cgi?id=1888565#c3
func (r *RpmOstreeClient) Start() error {
	_, err := runGetOut("systemctl", "start", "rpm-ostreed")
	if err != nil {
		return fmt.Errorf("failed to start rpm-ostreed: %v", err)
	}

	return nil
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

	var out []byte
	if out, err = runGetOut("rpm-ostree", args...); err != nil {
		// capture stdout output as well in case of error
		err = errors.Wrapf(err, "with stdout output: %v", string(out))
		return
	}

	changed = true
	return
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
