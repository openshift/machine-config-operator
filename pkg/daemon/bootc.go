package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/containers/image/v5/types"
	"k8s.io/klog/v2"
)

// rpmOstreeState houses zero or more RpmOstreeDeployments
// Subset of `bootc status --json`
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
	BaseChecksum            string   `json:"base-checksum,omitempty"`
	Version                 string   `json:"version"`
	Timestamp               uint64   `json:"timestamp"`
	Booted                  bool     `json:"booted"`
	Staged                  bool     `json:"staged"`
	LiveReplaced            string   `json:"live-replaced,omitempty"`
	Origin                  string   `json:"origin"`
	CustomOrigin            []string `json:"custom-origin"`
	ContainerImageReference string   `json:"container-image-reference"`
}

// NewNodeImageUpdaterClient is an interface describing how to interact with the host
// around content deployment
type NodeImageUpdaterClient interface {
	Initialize() error
	GetStatus() (string, error)
	GetBootedOSImageURL() (string, string, string, error)
	Rebase(string, string) (bool, error)
	RebaseLayered(string) error
	IsBootableImage(string) (bool, error)
	GetBootedAndStagedDeployment() (*RpmOstreeDeployment, *RpmOstreeDeployment, error)
}

// BootcClient provides all bootc related methods in one structure.

type BootcClient struct{}

// NewNodeUpdaterClient returns a new instance of the default DeploymentClient (BootcClient)
func NewNodeImageUpdaterClient() NodeImageUpdaterClient {
	return &BootcClient{}
}

// Synchronously invoke bootc, writing its stdout to our stdout,
// and gathering stderr into a buffer which will be returned in err
// in case of error.
func runBootc(args ...string) error {
	return runCmdSync("bootc", args...)
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
	if err := bug2111817Workaround(); err != nil {
		return err
	}

	// Commands like update and rebase need the pull secrets to pull images and manifests,
	// make sure we get access to them when we Initialize
	err := useKubeletConfigSecrets()
	if err != nil {
		return fmt.Errorf("Error while ensuring access to kublet config.json pull secrets: %w", err)
	}

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
// Bootc is working on status update: https://github.com/containers/bootc/issues/408
func (r *BootcClient) GetStatus() (string, error) {
	output, err := runGetOut("bootc", "status")
	if err != nil {
		return "", err
	}

	return string(output), nil
}

// GetBootedOSImageURL returns the image URL as well as the OSTree version(for logging) and the ostree commit (for comparisons)
// Returns the empty string if the host doesn't have a custom origin that matches pivot://
// (This could be the case for e.g. FCOS, or a future RHCOS which comes not-pivoted by default)
func (r *RpmOstreeClient) GetBootedOSImageURL() (string, string, string, error) {
	bootedDeployment, _, err := r.GetBootedAndStagedDeployment()
	if err != nil {
		return "", "", "", err
	}

	// TODO(jkyros): take this out, I just want to see when/why it's empty?
	j, _ := json.MarshalIndent(bootedDeployment, "", "    ")
	klog.Infof("%s", j)

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

	// BaseChecksum is populated if the system has been modified in a way that changes the base checksum
	// (like via an RPM install) so prefer BaseCheksum that if it's present, otherwise just use Checksum.
	baseChecksum := bootedDeployment.Checksum
	if bootedDeployment.BaseChecksum != "" {
		baseChecksum = bootedDeployment.BaseChecksum
	}

	return osImageURL, bootedDeployment.Version, baseChecksum, nil
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
			klog.Infof("Previous pivot: %s", previousPivot)
		} else {
			klog.Infof("Previous custom origin: %s", defaultDeployment.CustomOrigin[0])
		}
	} else {
		klog.Info("Current origin is not custom")
	}

	var imageData *types.ImageInspectInfo
	if imageData, err = imageInspect(imgURL); err != nil {
		if err != nil {
			var podmanImgData *imageInspection
			klog.Infof("Falling back to using podman inspect")
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
			klog.Infof("Pivoting to: %s (%s)", ostreeVersion, ostreeCsum)
		} else {
			klog.Infof("Pivoting to: %s", ostreeCsum)
		}
	} else {
		klog.Infof("No com.coreos.ostree-commit label found in metadata! Inspecting...")
		var refText []byte
		refText, err = runGetOut("ostree", "refs", "--repo", repo)
		if err != nil {
			return
		}
		refs := strings.Split(strings.TrimSpace(string(refText)), "\n")
		if len(refs) == 1 {
			klog.Infof("Using ref %s", refs[0])
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
	klog.Infof("Executing rebase from repo path %s with customImageURL %s and checksum %s", repo, customURL, ostreeCsum)

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
			klog.Infof("Falling back to using podman inspect")

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
	klog.Infof("Executing rebase to %s", imgURL)
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
