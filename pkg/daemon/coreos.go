package daemon

// This file provides routines that apply on Fedora CoreOS style systems,
// including deriviatives like RHEL CoreOS.

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/golang/glog"
	"github.com/pkg/errors"
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

// byLabel returns the udev generated symlink to the block device with the given label
func byLabel(label string) string {
	return fmt.Sprintf("/dev/disk/by-label/%s", label)
}

// getParentDeviceSysfs returns e.g. /sys/devices/pci0000:00/0000:00:05.0/virtio2/block/vda from /dev/vda4, though
// it can be more complex than that with e.g. NVMe.
func getParentDeviceSysfs(device string) (string, error) {
	target, err := os.Readlink(device)
	if err != nil {
		return "", errors.Wrapf(err, "reading %s", device)
	}
	sysfsDevLink := fmt.Sprintf("/sys/class/block/%s", filepath.Base(target))
	sysfsDev, err := filepath.EvalSymlinks(sysfsDevLink)
	if err != nil {
		return "", errors.Wrapf(err, "parsing %s", sysfsDevLink)
	}
	if _, err := os.Stat(filepath.Join(sysfsDev, "partition")); err == nil {
		sysfsDev = filepath.Dir(sysfsDev)
	}
	return sysfsDev, nil
}

// getRootBlockDeviceSysfs returns the path to the block
// device backing the root partition on a FCOS system
func getRootBlockDeviceSysfs() (string, error) {
	// Check for the `crypt_rootfs` label; this exists for RHCOS >= 4.3 but <= 4.6.
	// See https://github.com/openshift/enhancements/blob/master/enhancements/rhcos/automated-policy-based-disk-encryption.md
	luksRoot := byLabel("crypt_rootfs")
	if _, err := os.Stat(luksRoot); err == nil {
		return getParentDeviceSysfs(luksRoot)
	}
	// This is what we expect on FCOS and RHCOS <= 4.2
	root := byLabel("root")
	if _, err := os.Stat(root); err == nil {
		return getParentDeviceSysfs(root)
	}
	return "", fmt.Errorf("Failed to find %s", root)
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
	contents, err := ioutil.ReadAll(f)
	if err != nil {
		return err
	}
	var alephData coreosAleph
	if err := json.Unmarshal(contents, &alephData); err != nil {
		return err
	}
	glog.Infof("CoreOS aleph version: mtime=%v build=%v imgid=%v\n", stat.ModTime().UTC(), alephData.Build, alephData.Imgid)
	return nil
}

func logInitionProvisioning() error {
	contents, err := ioutil.ReadFile(ignitionProvisioningPath)
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
	glog.Infof("Ignition provisioning: time=%v\n", ignProvisioning.ProvisioningDate)
	return nil
}

func logProvisioningInformation() {
	if err := logAlephInformation(); err != nil {
		glog.Warningf("Failed to get aleph information: %v", err)
	}
	if err := logInitionProvisioning(); err != nil {
		glog.Warningf("Failed to get Ignition provisioning information: %v", err)
	}
}
