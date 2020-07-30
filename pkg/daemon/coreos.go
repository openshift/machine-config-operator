package daemon

// This file provides routines that apply on Fedora CoreOS style systems,
// including deriviatives like RHEL CoreOS.

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
)

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
