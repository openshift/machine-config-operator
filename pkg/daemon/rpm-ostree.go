package daemon

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/golang/glog"
	"github.com/pkg/errors"

	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
)

// rpmOstreeState houses zero or more RpmOstreeDeployments
// Subset of `rpm-ostree status --json`
// https://github.com/projectatomic/rpm-ostree/blob/bce966a9812df141d38e3290f845171ec745aa4e/src/daemon/rpmostreed-deployment-utils.c#L227
type rpmOstreeState struct {
	Deployments []rpmOstreeDeployment
}

// rpmOstreeDeployment represents a single deployment on a node
type rpmOstreeDeployment struct {
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
	GetStatus() (string, error)
	GetBootedOSImageURL() (string, string, error)
	RunPivot(string) error
}

// TODO(runcom): make this private to pkg/daemon!!!
//
// RpmOstreeClient provides all RpmOstree related methods in one structure.
// This structure implements DeploymentClient
type RpmOstreeClient struct{}

// NewNodeUpdaterClient returns a new instance of the default DeploymentClient (RpmOstreeClient)
func NewNodeUpdaterClient() NodeUpdaterClient {
	return &RpmOstreeClient{}
}

// getBootedDeployment returns the current deployment found
func (r *RpmOstreeClient) getBootedDeployment() (*rpmOstreeDeployment, error) {
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

// GetStatus returns multi-line human-readable text describing system status
func (r *RpmOstreeClient) GetStatus() (string, error) {
	output, err := runGetOut("rpm-ostree", "status")
	if err != nil {
		return "", err
	}

	return string(output), nil
}

// GetBootedOSImageURL returns the image URL as well as the OSTree version (for logging)
func (r *RpmOstreeClient) GetBootedOSImageURL() (string, string, error) {
	bootedDeployment, err := r.getBootedDeployment()
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
// osImageURL. This was originally https://github.com/openshift/pivot but it's now
// imported into the MCD itself.  The reason we execute on the host is due to SELinux;
// see https://github.com/openshift/pivot/pull/31 and
// https://github.com/openshift/machine-config-operator/issues/314
// Basically rpm_ostree_t has mac_admin, container_t doesn't.
func (r *RpmOstreeClient) RunPivot(osImageURL string) error {
	if err := os.MkdirAll(filepath.Dir(constants.EtcPivotFile), os.FileMode(0755)); err != nil {
		return fmt.Errorf("error creating leading dirs for %s: %v", constants.EtcPivotFile, err)
	}

	if err := ioutil.WriteFile(constants.EtcPivotFile, []byte(osImageURL), 0644); err != nil {
		return fmt.Errorf("error writing to %s: %v", constants.EtcPivotFile, err)
	}

	journalStopCh := make(chan time.Time)
	defer close(journalStopCh)
	go followPivotJournalLogs(journalStopCh)

	service := "machine-config-daemon-host.service"
	// We need to use pivot if it's there, because machine-config-daemon-host.service
	// currently has a ConditionPathExists=!/usr/bin/pivot.  This code can be dropped
	// once we don't need to care about compat with older RHCOS.
	var err error
	_, err = os.Stat("/usr/bin/pivot")
	if err != nil {
		if !os.IsNotExist(err) {
			return errors.Wrapf(err, "stat(/usr/bin/pivot)")
		}
	} else {
		service = "pivot.service"
	}
	err = exec.Command("systemctl", "start", service).Run()
	if err != nil {
		return errors.Wrapf(err, "failed to start %s", service)
	}
	return nil
}

// Proxy pivot and rpm-ostree daemon journal logs until told to stop. Warns if
// we encounter an error.
func followPivotJournalLogs(stopCh <-chan time.Time) {
	cmd := exec.Command("journalctl", "-f", "-b", "-o", "cat",
		"-u", "rpm-ostreed",
		"-u", "pivot")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		glog.Fatal(err)
	}

	go func() {
		<-stopCh
		cmd.Process.Kill()
	}()
}

// runGetOut executes a command, logging it, and return the stdout output.
func runGetOut(command string, args ...string) ([]byte, error) {
	glog.Infof("Running captured: %s %s\n", command, strings.Join(args, " "))
	cmd := exec.Command(command, args...)
	cmd.Stderr = os.Stderr
	rawOut, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return rawOut, nil
}
