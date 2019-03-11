package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coreos/go-systemd/dbus"
	"github.com/coreos/go-systemd/sdjournal"
	"github.com/golang/glog"

	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
)

const (
	pivotUnit      = "pivot.service"
	rpmostreedUnit = "rpm-ostreed.service"
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
	GetStatus() (string, error)
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
		return nil, fmt.Errorf("failed to parse `rpm-ostree status --json` output: %v", err)
	}

	for _, deployment := range rosState.Deployments {
		if deployment.Booted {
			return &deployment, nil
		}
	}

	return nil, fmt.Errorf("not currently booted in a deployment")
}

// GetStatus returns multi-line human-readable text describing system status
func (r *RpmOstreeClient) GetStatus() (string, error) {
	output, err := RunGetOut("rpm-ostree", "status")
	if err != nil {
		return "", err
	}

	return string(output), nil
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
	if err := os.MkdirAll(filepath.Dir(constants.EtcPivotFile), os.FileMode(0755)); err != nil {
		return fmt.Errorf("error creating leading dirs for %s: %v", constants.EtcPivotFile, err)
	}

	if err := ioutil.WriteFile(constants.EtcPivotFile, []byte(osImageURL), 0644); err != nil {
		return fmt.Errorf("error writing to %s: %v", constants.EtcPivotFile, err)
	}

	journalStopCh := make(chan time.Time)
	defer close(journalStopCh)
	go followPivotJournalLogs(journalStopCh)

	conn, err := dbus.NewSystemdConnection()
	if err != nil {
		return fmt.Errorf("error creating systemd conn: %v", err)
	}
	defer conn.Close()

	// start job
	status := make(chan string)
	_, err = conn.StartUnit("pivot.service", "fail", status)
	if err != nil {
		return fmt.Errorf("error starting job: %v", err)
	}

	select {
	case st := <-status:
		if st != "done" {
			return fmt.Errorf("error queuing start job; got %s", st)
		}
	case <-time.After(5 * time.Minute):
		return fmt.Errorf("timed out waiting for start job")
	}

	// wait until inactive/failed
	var failed bool
	eventsCh, errCh := conn.SubscribeUnits(time.Second)
Outer:
	for {
		select {
		case e := <-eventsCh:
			if st, ok := e["pivot.service"]; ok {
				// If the service is disabled, systemd won't keep around
				// metadata about whether it failed/passed, etc... The bindings
				// signal this by just using a `nil` status since it dropped out
				// of `ListUnits()`.
				if st == nil {
					return errors.New("got nil while waiting for pivot; is the service enabled?")
				}
				failed = st.ActiveState == "failed"
				if failed || st.ActiveState == "inactive" {
					break Outer
				}
			}
		case f := <-errCh:
			return fmt.Errorf("error while waiting for pivot: %v", f)
		}
	}

	if failed {
		return errors.New("pivot service did not exit successfully")
	}
	return nil
}

// Proxy pivot and rpm-ostree daemon journal logs until told to stop. Warns if
// we encounter an error.
func followPivotJournalLogs(stopCh <-chan time.Time) {
	reader, err := sdjournal.NewJournalReader(
		sdjournal.JournalReaderConfig{
			Since: time.Duration(1) * time.Second,
			Matches: []sdjournal.Match{
				{
					Field: sdjournal.SD_JOURNAL_FIELD_SYSTEMD_UNIT,
					Value: pivotUnit,
				},
				{
					Field: sdjournal.SD_JOURNAL_FIELD_SYSTEMD_UNIT,
					Value: rpmostreedUnit,
				},
			},
			Formatter: func(entry *sdjournal.JournalEntry) (string, error) {
				msg, ok := entry.Fields["MESSAGE"]
				if !ok {
					return "", fmt.Errorf("missing MESSAGE field in entry")
				}
				return fmt.Sprintf("%s: %s\n", entry.Fields[sdjournal.SD_JOURNAL_FIELD_SYSTEMD_UNIT], msg), nil
			},
		})
	if err != nil {
		glog.Warningf("Failed to open journal: %v", err)
		return
	}
	defer reader.Close()

	// We're kinda abusing the API here. The idea is that the stop channel is
	// used with time.After(), hence the `chan time.Time` type. But really, it
	// works to just never output anything and just close it as we do here.
	if err := reader.Follow(stopCh, os.Stdout); err != sdjournal.ErrExpired {
		glog.Warningf("Failed to follow journal: %v", err)
	}
}
