package daemon

import (
	"encoding/json"
	"fmt"
)

// PodmanStorageConfig contains storage configuration from Podman.
type PodmanStorageConfig struct {
	GraphDriverName string `json:"graphDriverName"`
	GraphRoot       string `json:"graphRoot"`
}

// PodmanInfo contains system information from Podman.
type PodmanInfo struct {
	Store PodmanStorageConfig `json:"store"`
}

// PodmanImageInfo contains image metadata from Podman.
type PodmanImageInfo struct {
	ID          string   `json:"Id"`
	Digest      string   `json:"Digest"`
	RepoDigests []string `json:"RepoDigests"`
	RepoDigest  string   `json:"-"` // Filled with matching digest from RepoDigests
}

// PodmanInterface abstracts podman operations for testing and flexibility.
type PodmanInterface interface {
	// GetPodmanImageInfoByReference retrieves image info for the given reference.
	// Returns nil if no image is found.
	GetPodmanImageInfoByReference(reference string) (*PodmanImageInfo, error)
	// GetPodmanInfo retrieves Podman system information.
	GetPodmanInfo() (*PodmanInfo, error)
}

// PodmanExecInterface is the production implementation that executes real podman commands.
type PodmanExecInterface struct {
	cmdRunner CommandRunner
}

// NewPodmanExec creates a new PodmanExecInterface with the given command runner.
func NewPodmanExec(commandRunner CommandRunner) *PodmanExecInterface {
	return &PodmanExecInterface{cmdRunner: commandRunner}
}

// GetPodmanImageInfoByReference retrieves image info for the given reference.
// It executes 'podman images --format=json --filter reference=<reference>'.
// Returns nil if no image is found.
// The RepoDigest field is populated if the reference matches one of the RepoDigests.
func (p *PodmanExecInterface) GetPodmanImageInfoByReference(reference string) (*PodmanImageInfo, error) {
	output, err := p.cmdRunner.RunGetOut("podman", "images", "--format=json", "--filter", "reference="+reference)
	if err != nil {
		return nil, err
	}
	var podmanInfos []PodmanImageInfo
	if err := json.Unmarshal(output, &podmanInfos); err != nil {
		return nil, fmt.Errorf("failed to decode podman image ls output: %v", err)
	}
	if len(podmanInfos) == 0 {

		return nil, nil
	}

	info := &podmanInfos[0]
	// Fill the custom RepoDigest field with the digest that matches the
	// requested reference as it's convenient to be used by the caller
	for _, digest := range info.RepoDigests {
		if digest == reference {
			info.RepoDigest = digest
			break
		}
	}

	return info, nil
}

// GetPodmanInfo retrieves Podman system information.
// It executes 'podman system info --format=json'.
func (p *PodmanExecInterface) GetPodmanInfo() (*PodmanInfo, error) {
	output, err := p.cmdRunner.RunGetOut("podman", "system", "info", "--format=json")
	if err != nil {
		return nil, err
	}
	var podmanInfo PodmanInfo
	if err := json.Unmarshal(output, &podmanInfo); err != nil {
		return nil, fmt.Errorf("failed to decode podman system info output: %v", err)
	}
	return &podmanInfo, nil
}
