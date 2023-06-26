package daemon

// This file provides routines that apply on Fedora CoreOS style systems,
// including deriviatives like RHEL CoreOS.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"k8s.io/klog/v2"
)

// alephPath contains information on the original bootimage; for more
// information see e.g. https://github.com/coreos/fedora-coreos-tracker/blob/main/internals%2FREADME-internals.md#aleph-version
const alephPath = "/sysroot/.coreos-aleph-version.json"

type coreosAleph struct {
	Build string `json:"build"`
	Imgid string `json:"imgid"`
}

// ignitionProvisioningPath is written by Ignition, see
// https://github.com/coreos/ignition/commit/556bc9404cfff08ea63c2a865bd3586ece7e8e44
const ignitionProvisioningPath = "/etc/.ignition-result.json"

// ignitionReport is JSON data written by (newer versions of) Ignition.
// See https://github.com/coreos/ignition/blob/9a7533ccf57156725e03ec239e5568de2d36f117/internal/exec/stages/files/filesystemEntries.go#L173
// We only really care about the provisioning date right now.
type ignitionReport struct {
	ProvisioningDate string `json:"provisioningDate"`
}

func logAlephInformation() error {
	f, err := os.Open(alephPath)
	if err != nil {
		// We assume this one should exist on CoreOS systems; if it somehow doesn't we'll just
		// log the error but continue anyways.
		return err
	}
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	contents, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	var alephData coreosAleph
	if err := json.Unmarshal(contents, &alephData); err != nil {
		return err
	}
	klog.Infof("CoreOS aleph version: mtime=%v build=%v imgid=%v\n", stat.ModTime().UTC(), alephData.Build, alephData.Imgid)
	return nil
}

func logInitionProvisioning() error {
	contents, err := os.ReadFile(ignitionProvisioningPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("No %s found", ignitionProvisioningPath)
			return nil
		}
		return err
	}
	var ignProvisioning ignitionReport
	if err := json.Unmarshal(contents, &ignProvisioning); err != nil {
		return err
	}
	klog.Infof("Ignition provisioning: time=%v\n", ignProvisioning.ProvisioningDate)
	return nil
}

func logProvisioningInformation() {
	if err := logAlephInformation(); err != nil {
		klog.Warningf("Failed to get aleph information: %v", err)
	}
	if err := logInitionProvisioning(); err != nil {
		klog.Warningf("Failed to get Ignition provisioning information: %v", err)
	}
}
