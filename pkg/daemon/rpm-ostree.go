package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	rpmostreeclient "github.com/coreos/rpmostree-client-go/pkg/client"
	"gopkg.in/yaml.v2"
	"k8s.io/klog/v2"
)

// RpmOstreeClient provides all RpmOstree related methods in one structure.
// This structure implements DeploymentClient
//
// TODO(runcom): make this private to pkg/daemon!!!
type RpmOstreeClient struct {
	client rpmostreeclient.Client
}

// NewNodeOstreeClient is a wrapper to create an RpmOstreeClient
func NewNodeOstreeClient() RpmOstreeClient {
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
	err := useMergedPullSecrets(rpmOstreeSystem)
	if err != nil {
		klog.Errorf("error while linking rpm-ostree pull secrets %v", err)
	}

	return nil
}

func (r *RpmOstreeClient) Peel() *rpmostreeclient.Client {
	return &r.client
}

// GetBootedDeployment returns the current deployment found
func (r *RpmOstreeClient) GetBootedAndStagedDeployment() (*rpmostreeclient.Deployment, *rpmostreeclient.Deployment, error) {
	status, err := r.client.QueryStatus()
	if err != nil {
		return nil, nil, err
	}

	booted, err := status.GetBootedDeployment()
	if err != nil {
		return nil, nil, err
	}
	staged := status.GetStagedDeployment()

	return booted, staged, nil
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

// RebaseLayered rebases system or errors if already rebased.
func (r *RpmOstreeClient) RebaseLayered(imgURL string) error {
	// Try to re-link the merged pull secrets if they exist, since it could have been populated without a daemon reboot
	if err := useMergedPullSecrets(rpmOstreeSystem); err != nil {
		return fmt.Errorf("Error while ensuring access to pull secrets: %w", err)
	}
	klog.Infof("Executing rebase to %s", imgURL)
	return runRpmOstree("rebase", "--experimental", "ostree-unverified-registry:"+imgURL)
}

// RebaseLayeredFromContainerStorage rebases the system from an existing local container storage image.
func (r *RpmOstreeClient) RebaseLayeredFromContainerStorage(imgURL string) error {
	// Try to re-link the merged pull secrets if they exist, since it could have been populated without a daemon reboot
	if err := useMergedPullSecrets(rpmOstreeSystem); err != nil {
		return fmt.Errorf("Error while ensuring access to pull secrets: %w", err)
	}
	klog.Infof("Executing local container storage rebase to %s", imgURL)
	return runRpmOstree("rebase", "--experimental", "ostree-unverified-image:containers-storage:"+imgURL)
}
