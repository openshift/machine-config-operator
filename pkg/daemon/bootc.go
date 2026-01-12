package daemon

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"k8s.io/klog/v2"
)

// bootc-client-go.go
// BootcStatus summarizes the current worldview of the bootc status --json.
type BootcStatus struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Metadata   Metadata `json:"metadata"`
	Spec       Spec     `json:"spec"`
	Status     Status   `json:"status"`
}

type Metadata struct {
	Name string `json:"name"`
}

// Spec collects the host specification
// This mirrors https://github.com/containers/bootc/blob/94521ecb19b8d0f2bfc36220322c60cafa036296/lib/src/spec.rs#L46
type Spec struct {
	Image     ImageReference `json:"image"`
	BootOrder string         `json:"bootOrder"`
}

// Status collects the status of the host system
// This mirrors https://github.com/containers/bootc/blob/94521ecb19b8d0f2bfc36220322c60cafa036296/lib/src/spec.rs#L132
type Status struct {
	Staged         *BootEntry `json:"staged,omitempty"`
	Booted         *BootEntry `json:"booted,omitempty"`
	Rollback       *BootEntry `json:"rollback,omitempty"`
	RollbackQueued bool       `json:"rollbackQueued"`
	Type           *string    `json:"type,omitempty"`
}

// BootEntry represents a bootable entry
// This mirrors https://github.com/containers/bootc/blob/94521ecb19b8d0f2bfc36220322c60cafa036296/lib/src/spec.rs#L106
type BootEntry struct {
	Image        *ImageStatus     `json:"image,omitempty"`
	CachedUpdate *ImageStatus     `json:"cachedUpdate,omitempty"`
	Incompatible bool             `json:"incompatible"`
	Pinned       bool             `json:"pinned"`
	Ostree       *BootEntryOstree `json:"ostree,omitempty"`
}

// BootEntryOstree represents a bootable entry
// This mirrors https://github.com/containers/bootc/blob/94521ecb19b8d0f2bfc36220322c60cafa036296/lib/src/spec.rs#L96
type BootEntryOstree struct {
	Checksum     string `json:"checksum"`
	DeploySerial uint32 `json:"deploy_serial"`
}

// ImageStatus represents the status of the booted image
// This mirrors https://github.com/containers/bootc/blob/94521ecb19b8d0f2bfc36220322c60cafa036296/lib/src/spec.rs#L82
type ImageStatus struct {
	Image       ImageReference `json:"image"`
	Version     *string        `json:"version,omitempty"`
	Timestamp   *time.Time     `json:"timestamp,omitempty"`
	ImageDigest string         `json:"imageDigest"`
}

// Client is a handle for interacting with an bootc based system.
type Client struct {
	clientid string
}

// NewClient creates a new bootc client.  The client identifier should be a short, unique and ideally machine-readable string.
// This could be as simple as `examplecorp-management-agent`.
// If you want to be more verbose, you could use a URL, e.g. `https://gitlab.com/examplecorp/management-agent`.
func NewClient(id string) Client {
	return Client{
		clientid: id,
	}
}

func (client *Client) newCmd(args ...string) *exec.Cmd {
	r := exec.Command("bootc", args...)
	r.Env = append(r.Env, "BOOTC_CLIENT_ID", client.clientid)
	return r
}

func (client *Client) run(args ...string) error {
	c := client.newCmd(args...)
	return c.Run()
}

// QueryStatus loads the current system state.
func (client *Client) QueryStatus() (*BootcStatus, error) {
	var q BootcStatus
	c := client.newCmd("status", "--json")
	buf, err := c.Output()
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(buf, &q); err != nil {
		return nil, fmt.Errorf("failed to parse `bootc status --json` output: %w", err)
	}

	return &q, nil
}

// GetBootedImage finds the booted image, or returns nil if none is found.
func (s *Status) GetBootedImage() *BootEntry {
	return s.Booted
}

// GetStagedImage finds the staged image for the next boot, or returns nil if none is found.
func (s *Status) GetStagedImage() *BootEntry {
	return s.Staged
}

// GetRollbackImage finds the rollback deployment, or returns nil if none is found.
func (s *Status) GetRollbackImage() *BootEntry {
	return s.Rollback
}

// GetBaseChecksum returns the ostree commit used as a base.
func (b *BootEntry) GetBaseChecksum() string {
	return b.Ostree.Checksum
}

// GetContainerImageReference returns the the container image reference.
func (b *BootEntry) GetContainerImageReference() string {
	return b.Image.Image.Image
}

// imgref.go
// ImageReference captures an image signature verification policy alongside an image reference.
// This mirrors https://github.com/containers/bootc/blob/94521ecb19b8d0f2bfc36220322c60cafa036296/lib/src/spec.rs#L69
type ImageReference struct {
	Image     string          `json:"image"`
	Transport string          `json:"transport"`
	Signature *ImageSignature `json:"signature,omitempty"`
}

// ImageSignature mirrors https://github.com/containers/bootc/blob/94521ecb19b8d0f2bfc36220322c60cafa036296/lib/src/spec.rs#L57
// Same as https://docs.rs/ostree-ext/latest/ostree_ext/container/enum.SignatureSource.html
type ImageSignature struct {
	AllowInsecure bool
	OstreeRemote  string
}

// bootc.go

// BootcClient provides all Bootc related methods in one structure.
type BootcClient struct {
	client Client
}

// Synchronously invoke bootc, writing its stdout to our stdout,
// and gathering stderr into a buffer which will be returned in err
// in case of error.
func runBootc(args ...string) error {
	return runCmdSync("bootc", args...)
}

func (b *BootcClient) Initialize() error {
	// Commands like update and rebase need the pull secrets to pull images and manifests,
	// make sure we get access to them when we Initialize
	err := useMergedPullSecrets(bootcSystem)
	if err != nil {
		return fmt.Errorf("Error while ensuring access to pull secrets: %w", err)
	}
	return nil
}

// GetBootedAndStagedImage returns the current booted and staged image found
func (b *BootcClient) GetBootedAndStagedImage() (*BootEntry, *BootEntry, error) {
	status, err := b.client.QueryStatus()
	if err != nil {
		return nil, nil, err
	}
	return status.Status.GetBootedImage(), status.Status.GetStagedImage(), nil
}

// GetBootedImageInfo() returns the image URL as well as the image version(for logging) and the ostree commit (for comparisons)
func (b *BootcClient) GetBootedImageInfo() (*BootedImageInfo, error) {
	bootedImage, _, err := b.GetBootedAndStagedImage()
	if err != nil {
		return nil, err
	}
	osImageURL := ""
	if bootedImage.GetContainerImageReference() != "" {
		osImageURL = bootedImage.GetContainerImageReference()
	}
	var baseChecksum string
	if bootedImage.Ostree != nil {
		baseChecksum = bootedImage.GetBaseChecksum()
	} else {
		baseChecksum = ""
	}

	bootedImageInfo := BootedImageInfo{
		OSImageURL:   osImageURL,
		ImageVersion: *bootedImage.Image.Version,
		BaseChecksum: baseChecksum,
	}
	return &bootedImageInfo, nil
}

// Switch target a new container image reference to boot for the system or errors if already switched.
func (b *BootcClient) Switch(imgURL string) error {
	// Try to re-link the merged pull secrets if they exist, since it could have been populated without a daemon reboot
	if err := useMergedPullSecrets(bootcSystem); err != nil {
		return fmt.Errorf("Error while ensuring access to pull secrets: %w", err)
	}
	klog.Infof("Executing switch to %s", imgURL)
	return runBootc("switch", imgURL)
}
