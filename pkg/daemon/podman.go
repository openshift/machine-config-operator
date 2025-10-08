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
	Id          string   `json:"Id"`
	Digest      string   `json:"Digest"`
	RepoDigests []string `json:"RepoDigests"`
	RepoDigest  string   `json:"-"` // Filled with matching digest from RepoDigests
}

// GetPodmanImageInfoByReference retrieves image info for the given reference.
// Returns nil if no image is found.
func GetPodmanImageInfoByReference(reference string) (*PodmanImageInfo, error) {
	output, err := runGetOut("podman", "images", "--format=json", "--filter", "reference="+reference)
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
func GetPodmanInfo() (*PodmanInfo, error) {
	output, err := runGetOut("podman", "system", "info", "--format=json")
	if err != nil {
		return nil, err
	}
	var podmanInfo PodmanInfo
	if err := json.Unmarshal(output, &podmanInfo); err != nil {
		return nil, fmt.Errorf("failed to decode podman image ls output: %v", err)
	}
	return &podmanInfo, nil
}
