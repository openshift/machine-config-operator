package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	rpmostreeclient "github.com/coreos/rpmostree-client-go/pkg/client"
	"github.com/opencontainers/go-digest"
	pivotutils "github.com/openshift/machine-config-operator/pkg/daemon/pivot/utils"
	"gopkg.in/yaml.v2"
	"k8s.io/klog/v2"
)

const (
	// the number of times to retry commands that pull data from the network
	numRetriesNetCommands = 5
	// Default ostreeAuthFile location
	ostreeAuthFile = "/run/ostree/auth.json"
	// Pull secret.  Written by the machine-config-operator
	kubeletAuthFile = "/var/lib/kubelet/config.json"
	// Internal Registry Pull secret + Global Pull secret.  Written by the machine-config-operator.
	imageRegistryAuthFile = "/etc/mco/internal-registry-pull-secret.json"
)

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

// RpmOstreeClient provides all RpmOstree related methods in one structure.
// This structure implements DeploymentClient
//
// TODO(runcom): make this private to pkg/daemon!!!
type RpmOstreeClient struct {
	client rpmostreeclient.Client
}

// NewNodeUpdaterClient is a wrapper to create an RpmOstreeClient
func NewNodeUpdaterClient() RpmOstreeClient {
	return RpmOstreeClient{
		client: rpmostreeclient.NewClient("machine-config-daemon"),
	}
}

// Synchronously invoke rpm-ostree, writing its stdout to our stdout,
// and gathering stderr into a buffer which will be returned in err
// in case of error.
func runRpmOstree(args ...string) error {
	return runCmdSync("rpm-ostree", args...)
}

// See https://bugzilla.redhat.com/show_bug.cgi?id=2111817
func bug2111817Workaround() error {
	targetUnit := "/run/systemd/system/rpm-ostreed.service.d/bug2111817.conf"
	// Do nothing if the file exists
	if _, err := os.Stat(targetUnit); err == nil {
		return nil
	}
	err := os.MkdirAll(filepath.Dir(targetUnit), 0o755)
	if err != nil {
		return err
	}
	dropin := `[Service]
InaccessiblePaths=
`
	if err := writeFileAtomicallyWithDefaults(targetUnit, []byte(dropin)); err != nil {
		return err
	}
	if err := runCmdSync("systemctl", "daemon-reload"); err != nil {
		return err
	}
	klog.Infof("Enabled workaround for bug 2111817")
	return nil
}

func (r *RpmOstreeClient) Initialize() error {
	if err := bug2111817Workaround(); err != nil {
		return err
	}

	// Commands like update and rebase need the pull secrets to pull images and manifests,
	// make sure we get access to them when we Initialize

	err := useMergedPullSecrets()
	if err != nil {
		klog.Errorf("error while linking rpm-ostree pull secrets %v", err)
	}

	return nil
}

func (r *RpmOstreeClient) Peel() *rpmostreeclient.Client {
	return &r.client
}

// GetBootedDeployment returns the current deployment found
func (r *RpmOstreeClient) GetBootedAndStagedDeployment() (booted, staged *rpmostreeclient.Deployment, err error) {
	status, err := r.client.QueryStatus()
	if err != nil {
		return nil, nil, err
	}

	booted, err = status.GetBootedDeployment()
	staged = status.GetStagedDeployment()

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

// GetBootedOSImageURL returns the image URL as well as the OSTree version(for logging) and the ostree commit (for comparisons)
// Returns the empty string if the host doesn't have a custom origin that matches pivot://
// (This could be the case for e.g. FCOS, or a future RHCOS which comes not-pivoted by default)
func (r *RpmOstreeClient) GetBootedOSImageURL() (string, string, string, error) {
	bootedDeployment, _, err := r.GetBootedAndStagedDeployment()
	if err != nil {
		return "", "", "", err
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
		// right now remove ostree remote, and transport from container image reference
		ostreeImageReference, err := bootedDeployment.RequireContainerImage()
		if err != nil {
			return "", "", "", err
		}
		osImageURL = ostreeImageReference.Imgref.Image
	}

	baseChecksum := bootedDeployment.GetBaseChecksum()
	return osImageURL, bootedDeployment.Version, baseChecksum, nil
}

func podmanInspect(imgURL string) (imgdata *imageInspection, err error) {
	// Pull the container image if not already available
	var authArgs []string
	if _, err := os.Stat(ostreeAuthFile); err == nil {
		authArgs = append(authArgs, "--authfile", ostreeAuthFile)
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

// RpmOstreeIsNewEnoughForLayering returns true if the version of rpm-ostree on the
// host system is new enough for layering.
// VersionData represents the static information about rpm-ostree.
type VersionData struct {
	Version  string   `yaml:"Version"`
	Features []string `yaml:"Features"`
	Git      string   `yaml:"Git"`
}

type RpmOstreeVersionData struct {
	Root VersionData `yaml:"rpm-ostree"`
}

// RpmOstreeVersion returns the running rpm-ostree version number
func rpmOstreeVersion() (*VersionData, error) {
	buf, err := runGetOut("rpm-ostree", "--version")
	if err != nil {
		return nil, err
	}

	var q RpmOstreeVersionData
	if err := yaml.Unmarshal(buf, &q); err != nil {
		return nil, fmt.Errorf("failed to parse `rpm-ostree --version` output: %w", err)
	}

	return &q.Root, nil
}

func (r *RpmOstreeClient) IsNewEnoughForLayering() (bool, error) {
	verdata, err := rpmOstreeVersion()
	if err != nil {
		return false, err
	}
	for _, v := range verdata.Features {
		if v == "container" {
			return true, nil
		}
	}
	return false, nil
}

// RebaseLayered rebases system or errors if already rebased
func (r *RpmOstreeClient) RebaseLayered(imgURL string) (err error) {
	// Try to re-link the merged pull secrets if they exist, since it could have been populated without a daemon reboot
	useMergedPullSecrets()
	klog.Infof("Executing rebase to %s", imgURL)
	return runRpmOstree("rebase", "--experimental", "ostree-unverified-registry:"+imgURL)
}

// linkOstreeAuthFile gives the rpm-ostree client access to secrets in the file located at `path` by symlinking so that
// rpm-ostree can use those secrets to pull images. This can be called multiple times to overwrite an older link.
func linkOstreeAuthFile(path string) error {
	if _, err := os.Lstat(ostreeAuthFile); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if err := os.MkdirAll("/run/ostree", 0o544); err != nil {
				return err
			}
		}
	} else {
		// Remove older symlink if it exists since it needs to be overwritten
		if err := os.Remove(ostreeAuthFile); err != nil {
			return err
		}
	}

	klog.Infof("Linking ostree authfile to %s", path)
	err := os.Symlink(path, ostreeAuthFile)
	return err
}

// useMergedSecrets gives the rpm-ostree client access to secrets for the internal registry and the global pull
// secret. It does this by symlinking the merged secrets file into /run/ostree. If it fails to find the
// merged secrets, it will use the default pull secret file instead.
func useMergedPullSecrets() error {

	// check if merged secret file exists
	if _, err := os.Stat(imageRegistryAuthFile); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			klog.Errorf("Merged secret file does not exist; defaulting to cluster pull secret")
			return linkOstreeAuthFile(kubeletAuthFile)
		}
	}
	// Check that merged secret file is valid JSON
	if file, err := os.ReadFile(imageRegistryAuthFile); err != nil {
		klog.Errorf("Merged secret file could not be read; defaulting to cluster pull secret %v", err)
		return linkOstreeAuthFile(kubeletAuthFile)
	} else if !json.Valid(file) {
		klog.Errorf("Merged secret file could not be validated; defaulting to cluster pull secret %v", err)
		return linkOstreeAuthFile(kubeletAuthFile)
	}

	// Attempt to link the merged secrets file
	return linkOstreeAuthFile(imageRegistryAuthFile)
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
	klog.Infof("Running captured: %s %s", command, strings.Join(args, " "))
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
