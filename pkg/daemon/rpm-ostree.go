package daemon

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
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
	Transaction *[]string
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
	Initialize() error
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
		return nil, errors.Wrapf(err, "failed to parse `rpm-ostree status --json` output (%s)", truncate(string(output), 30))
	}

	return &rosState, nil
}

func (r *RpmOstreeClient) Initialize() error {
	// This replicates https://github.com/coreos/rpm-ostree/pull/2945
	// and can be removed when we have a new enough rpm-ostree with
	// that PR.
	err := runCmdSync("systemctl", "start", "rpm-ostreed")
	if err != nil {
		// If this fails for some reason, let's dump the unit status
		// into our logs to aid future debugging.
		cmd := exec.Command("systemctl", "status", "rpm-ostreed")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Run()
		return err
	}

	status, err := r.loadStatus()
	if err != nil {
		return err
	}

	// If there's an active transaction when the MCD starts up,
	// it's possible that we hit
	// https://bugzilla.redhat.com/show_bug.cgi?id=1982389
	// This is fixed upstream, but we need a newer RHEL for that,
	// so let's just restart the service as a workaround.
	if status.Transaction != nil {
		glog.Warningf("Detected active transaction during daemon startup, restarting to clear it")
		err := runCmdSync("systemctl", "restart", "rpm-ostreed")
		if err != nil {
			return err
		}
	}

	return nil
}

// GetBootedDeployment returns the current deployment found
func (r *RpmOstreeClient) GetBootedDeployment() (*RpmOstreeDeployment, error) {
	status, err := r.loadStatus()
	if err != nil {
		return nil, err
	}

	for _, deployment := range status.Deployments {
		if deployment.Booted {
			deployment := deployment
			return &deployment, nil
		}
	}

	return nil, fmt.Errorf("not currently booted in a deployment")
}

func (r *RpmOstreeClient) GetStatusStructured() (*RpmOstreeStatus, error) {

	var rosState RpmOstreeStatus
	output, err := runGetOut("rpm-ostree", "status", "--json")
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(output, &rosState); err != nil {
		return nil, errors.Wrapf(err, "failed to parse `rpm-ostree status --json` output (%s)", truncate(string(output), 30))
	}
	return &rosState, nil
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

// Rebase potentially rebases the system to the image in osImageContentDir if not already rebased.
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

	if err = runRpmOstree(args...); err != nil {
		return
	}

	changed = true
	return
}

// RebaseLayered rebases system or errors if already rebased
func (r *RpmOstreeClient) RebaseLayered(imgURL string) (err error) {
	glog.Infof("Executing rebase to %s", imgURL)
	args := []string{"rebase", "--experimental", "ostree-unverified-registry:" + imgURL}

	return runRpmOstree(args...)
}

// Live apply live-applies whatever we rebased to
func (r *RpmOstreeClient) ApplyLive() (err error) {

	glog.Infof("Applying live")

	return runRpmOstree("ex", "apply-live", "--allow-replacement")
}

type FileTree struct {
	children map[string]*FileTree
}

// ensures nodes exist for every element in a path
func (node *FileTree) insert(path []string) {
	child, ok := node.children[path[0]]
	if !ok {
		// if we're inserting the first child for this node we need to create the map
		if node.children == nil {
			node.children = make(map[string]*FileTree)
		}
		// there isn't a node for the first element in the path, so we need to create one
		child = &FileTree{}
		node.children[path[0]] = child
	}
	if len(path) > 1 {
		child.insert(path[1:])
	}
}

// walk the tree, generating a list of all paths to leaves
func (node *FileTree) walk() (paths []string) {
	for childPath, child := range node.children {
		pathsToLeaves := child.walk()
		if len(pathsToLeaves) == 0 {
			// child is a leaf, so add it to paths
			paths = append(paths, path.Join("/", childPath))
		} else {
			// else prepend childPath to all pathsToLeaves
			for _, pathToLeaf := range pathsToLeaves {
				paths = append(paths, path.Join("/", childPath, pathToLeaf))
			}
		}
	}
	return
}

func Diff(fromRev, toRev string) ([]string, error) {
	stdout, err := runGetOut("ostree", "diff", fromRev, toRev)
	if err != nil {
		return []string{}, fmt.Errorf("failed to run ostree diff: %w", err)
	}
	return ParseDiff(stdout), nil
}

func ParseDiff(stdout []byte) []string {
	paths := FileTree{}
	lines := strings.Split(string(stdout), "\n")
	for _, line := range lines {
		words := strings.Fields(line)
		// lines will be of the form
		// {A,D,M} path
		// all we care about is getting the path
		// add len check since last line will be empty
		if len(words) >= 1 {
			splitPath := strings.Split(words[1], string(os.PathSeparator))
			// pass splitPath[1:] since splitPath will have "" as its first element
			paths.insert(splitPath[1:])
		}
	}
	return paths.walk()
}

func Cat(stateroot, commit, path string) ([]byte, error) {
	return ioutil.ReadFile(filepath.Join(fmt.Sprintf("/ostree/deploy/%s/deploy/%s.0", stateroot, commit), path))
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

type RpmOstreeStatus struct {
	Deployments []struct {
		RequestedLocalPackages []interface{} `json:"requested-local-packages"`
		BaseCommitMeta         struct {
			OstreeContainerImageConfig  string   `json:"ostree.container.image-config"`
			OstreeManifest              string   `json:"ostree.manifest"`
			OstreeManifestDigest        string   `json:"ostree.manifest-digest"`
			OstreeImporterVersion       string   `json:"ostree.importer.version"`
			CoreosAssemblerConfigGitrev string   `json:"coreos-assembler.config-gitrev"`
			RpmostreeInitramfsArgs      []string `json:"rpmostree.initramfs-args"`
			OstreeLinux                 string   `json:"ostree.linux"`
			RpmostreeRpmmdRepos         []struct {
				ID        string `json:"id"`
				Timestamp int64  `json:"timestamp"`
			} `json:"rpmostree.rpmmd-repos"`
			CoreosAssemblerConfigDirty string                    `json:"coreos-assembler.config-dirty"`
			OstreeBootable             bool                      `json:"ostree.bootable"`
			CoreosAssemblerBasearch    string                    `json:"coreos-assembler.basearch"`
			Version                    string                    `json:"version"`
			RpmostreeInputhash         string                    `json:"rpmostree.inputhash"`
			OstreeTarFiltered          map[string]map[string]int `json:"ostree.tar-filtered"`
		} `json:"base-commit-meta,omitempty"`
		BaseRemovals                       []interface{} `json:"base-removals"`
		Unlocked                           string        `json:"unlocked"`
		Booted                             bool          `json:"booted"`
		RequestedLocalFileoverridePackages []interface{} `json:"requested-local-fileoverride-packages"`
		ID                                 string        `json:"id"`
		Osname                             string        `json:"osname"`
		Pinned                             bool          `json:"pinned"`
		ModulesEnabled                     []interface{} `json:"modules-enabled"`
		RegenerateInitramfs                bool          `json:"regenerate-initramfs"`
		BaseLocalReplacements              []interface{} `json:"base-local-replacements"`
		ContainerImageReference            string        `json:"container-image-reference,omitempty"`
		Checksum                           string        `json:"checksum"`
		RequestedBaseLocalReplacements     []interface{} `json:"requested-base-local-replacements"`
		RequestedModules                   []interface{} `json:"requested-modules"`
		RequestedPackages                  []interface{} `json:"requested-packages"`
		Serial                             int           `json:"serial"`
		Timestamp                          int           `json:"timestamp"`
		Packages                           []interface{} `json:"packages"`
		Staged                             bool          `json:"staged"`
		RequestedBaseRemovals              []interface{} `json:"requested-base-removals"`
		Modules                            []interface{} `json:"modules"`
		ContainerImageReferenceDigest      string        `json:"container-image-reference-digest,omitempty"`
		Origin                             string        `json:"origin,omitempty"`
		Version                            string        `json:"version,omitempty"`
		CustomOrigin                       []string      `json:"custom-origin,omitempty"`
	} `json:"deployments"`
	Transaction  interface{} `json:"transaction"`
	CachedUpdate interface{} `json:"cached-update"`
	UpdateDriver interface{} `json:"update-driver"`
}
