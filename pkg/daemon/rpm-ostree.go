package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RpmOstreeState houses zero or more RpmOstreeDeployments
// Subset of `rpm-ostree status --json`
// https://github.com/projectatomic/rpm-ostree/blob/bce966a9812df141d38e3290f845171ec745aa4e/src/daemon/rpmostreed-deployment-utils.c#L227
type RpmOstreeState struct {
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

// NodeUpdaterClient is an interface describing how to interact with the host
// around content deployment
type NodeUpdaterClient interface {
	GetBootedOSImageURL(string) (string, string, error)
	RunPivot(string) error
}

// RpmOstreeClient provides all RpmOstree related methods in one structure.
// This structure implements DeploymentClient
type RpmOstreeClient struct{}

// NewNodeUpdaterClient returns a new instance of the default DeploymentClient (RpmOstreeClient)
func NewNodeUpdaterClient() NodeUpdaterClient {
	return &RpmOstreeClient{}
}

// getBootedDeployment returns the current deployment found
func (r *RpmOstreeClient) getBootedDeployment(rootMount string) (*RpmOstreeDeployment, error) {
	var rosState RpmOstreeState
	output, err := RunGetOut("chroot", rootMount, "rpm-ostree", "status", "--json")
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(output, &rosState); err != nil {
		return nil, fmt.Errorf("Failed to parse `rpm-ostree status --json` output: %v", err)
	}

	for _, deployment := range rosState.Deployments {
		if deployment.Booted {
			return &deployment, nil
		}
	}

	return nil, fmt.Errorf("Not currently booted in a deployment")
}

// GetBootedOSImageURL returns the image URL as well as the OSTree version (for logging)
func (r *RpmOstreeClient) GetBootedOSImageURL(rootMount string) (string, string, error) {
	bootedDeployment, err := r.getBootedDeployment(rootMount)
	if err != nil {
		return "", "", err
	}

	// the canonical image URL is stored in the custom origin field by the pivot tool
	osImageURL := "<not pivoted>"
	if len(bootedDeployment.CustomOrigin) > 0 {
		if strings.HasPrefix(bootedDeployment.CustomOrigin[0], "pivot://") {
			osImageURL = bootedDeployment.CustomOrigin[0][len("pivot://"):]
		}
	}

	return osImageURL, bootedDeployment.Version, nil
}

// RunPivot executes a pivot from one deployment to another as found in the referenced
// osImageURL. See https://github.com/openshift/pivot.
func (r *RpmOstreeClient) RunPivot(osImageURL string) error {
	return Run("/bin/pivot", osImageURL)
}
