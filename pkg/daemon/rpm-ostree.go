package daemon

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/golang/glog"
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

func getBootedDeployment(rootMount string) (*RpmOstreeDeployment, error) {
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

// getBootedOSImageURL returns the image URL as well as the OSTree version (for logging)
func getBootedOSImageURL(rootMount string) (string, string, error) {
	bootedDeployment, err := getBootedDeployment(rootMount)
	if err != nil {
		return "", "", err
	}

	// the canonical image URL is stored in the custom origin field by the pivot tool
	osImageURL := ""
	if len(bootedDeployment.CustomOrigin) > 0 {
		if strings.HasPrefix(bootedDeployment.CustomOrigin[0], "pivot://") {
			osImageURL = bootedDeployment.CustomOrigin[0][len("pivot://"):]
		}
	}

	// XXX: the installer doesn't pivot yet so for now, just make "" equivalent
	// to "://dummy" so that we don't immediately try to pivot to this dummy
	// URL. See also: https://github.com/openshift/installer/issues/281
	if osImageURL == "" {
		osImageURL = "://dummy"
		glog.Warningf(`Working around "://dummy" OS image URL until installer ➰ pivots`)
	}

	return osImageURL, bootedDeployment.Version, nil
}

func runPivot(osImageURL string) error {
	return Run("/bin/pivot", osImageURL)
}
