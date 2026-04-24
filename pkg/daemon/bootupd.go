package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"k8s.io/apimachinery/pkg/util/uuid"
)

// runGetOut executes a command on the host and returns its stdout output.
func runGetOut(cmdName string, args ...string) ([]byte, error) {
	return (&CommandRunnerOS{}).RunGetOut(cmdName, args...)
}

const espPartTypeGUID = "c12a7328-f81f-11d2-ba4b-00a0c93ec93b"

type lsblkDevice struct {
	Name     string        `json:"name"`
	PartType string        `json:"parttype"`
	FSType   string        `json:"fstype"`
	Children []lsblkDevice `json:"children"`
}

type lsblkOutput struct {
	BlockDevices []lsblkDevice `json:"blockdevices"`
}

// runBootupdViaContainer runs bootupctl update from the incoming container image before
// the OS pivot reboot. This ensures the bootloader trusts any new Secure Boot signing
// keys present in the target image before the new (signed) kernel becomes active.
//
// INVOCATION_ID is passed into the container so bootupctl's ensure_running_in_systemd()
// check passes.
//
// -v /boot:/boot:rslave: bootupctl operates directly on /boot (no --sysroot flag).
// :rslave propagates the nested /boot/efi (EFI System Partition) mount into the container.
//
// -v /dev:/dev: bootupd mounts the ESP from host block devices dynamically; without this
// the container only sees a synthetic devtmpfs with no host-specific nodes.
func (dn *Daemon) runBootupdViaContainer(imageURL string) error {
	if !dn.os.IsCoreOSVariant() {
		return nil
	}
	// For now, only attempt bootloader updates on x86_64 and aarch64 as they are the ones
	// affected by the secure boot issue.
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return nil
	}
	logSystem("runBootupdViaContainer: attempting bootloader update")

	systemdPodmanArgs := []string{"--unit", "machine-config-daemon-bootupd", "-p", "EnvironmentFile=-/etc/mco/proxy.env", "--collect", "--wait", "--", "podman"}

	// Only pull if the image is not already in local podman storage (e.g. via PinnedImageSet
	// or a previous pull). This avoids unnecessary network access in disconnected/bootstrap
	// environments where outbound registry traffic is blocked.
	localImage, err := dn.podmanInterface.GetPodmanImageInfoByReference(imageURL)
	if err != nil {
		return fmt.Errorf("checking local podman storage for bootupctl image: %w", err)
	}
	if localImage == nil {
		pullArgs := append([]string{}, systemdPodmanArgs...)
		pullArgs = append(pullArgs, "pull")
		if _, err := os.Stat("/var/lib/kubelet/config.json"); err == nil {
			pullArgs = append(pullArgs, "--authfile", "/var/lib/kubelet/config.json")
		}
		if !podmanSupportsSigstore() {
			if _, err := os.Stat("/etc/machine-config-daemon/policy-for-old-podman.json"); err == nil {
				pullArgs = append(pullArgs, "--signature-policy", "/etc/machine-config-daemon/policy-for-old-podman.json")
			}
		}
		pullArgs = append(pullArgs, imageURL)
		if err := runCmdSync("systemd-run", pullArgs...); err != nil {
			return fmt.Errorf("pulling image for bootupctl: %w", err)
		}
	}

	// Always run an update: this will handle adoption for pre-bootupd cases if needed.
	logSystem("bootupctl: running update from container image")
	if err := runCmdSync("systemd-run",
		"--unit", "machine-config-daemon-bootupd-update",
		"-p", "EnvironmentFile=-/etc/mco/proxy.env",
		"--collect", "--wait",
		"--", "podman", "run",
		"--env", "INVOCATION_ID="+string(uuid.NewUUID()),
		"--privileged", "--pid=host", "--net=host", "--rm",
		"-v", "/boot:/boot:rslave",
		"-v", "/dev:/dev",
		imageURL, "bootupctl", "update",
	); err != nil {
		// bootupctl update can fail on old kernels that lack FAT32 RENAME_EXCHANGE support.
		// Fall back to a manual EFI copy. Logic mirrors https://github.com/openshift/os/pull/1795,
		// handling RAID (all member disks), multi-ESP (booted disk only), and single-ESP cases.
		logSystem("bootupctl update failed (%v); falling back to manual EFI copy", err)
		if err := dn.runBootupdFallback(imageURL); err != nil {
			return fmt.Errorf("bootupctl fallback EFI copy: %w", err)
		}
		logSystem("bootupctl: fallback EFI copy completed")
	}
	return nil
}

// runBootupdFallback extracts EFI update files from the container image and copies
// them directly to the ESP(s) on the host. All device detection runs on the host,
// avoiding container namespace issues with sysfs and mount info.
func (dn *Daemon) runBootupdFallback(imageURL string) error {
	containerOut, err := dn.podmanInterface.CreatePodmanContainer(nil, "bootupd-efi-extract", imageURL)
	if err != nil {
		return fmt.Errorf("creating container for EFI extraction: %w", err)
	}
	containerID := strings.TrimSpace(string(containerOut))
	defer runCmdSync("podman", "rm", "-f", containerID) //nolint:errcheck

	efiDir, err := os.MkdirTemp("", "bootupd-efi-")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(efiDir)

	if _, err := runGetOut("podman", "cp",
		containerID+":/usr/lib/bootupd/updates/EFI",
		efiDir,
	); err != nil {
		return fmt.Errorf("extracting EFI files from container: %w", err)
	}

	espDevices, err := findESPDevices()
	if err != nil {
		return err
	}

	for _, dev := range espDevices {
		logSystem("bootupd fallback: copying EFI files to %s", dev)
		if err := copyEFIToESP(dev, efiDir+"/EFI"); err != nil {
			return fmt.Errorf("copying EFI to %s: %w", dev, err)
		}
	}

	return runCmdSync("sync")
}

// findESPDevices returns the ESP device path(s) to update, handling RAID,
// multi-disk non-RAID, and single-disk layouts.
func findESPDevices() ([]string, error) {
	// Enumerate block devices with the columns we need: partition type GUID and
	// filesystem type are sufficient to identify ESPs and RAID members.
	lsblkJSON, err := runGetOut("lsblk", "--paths", "--json", "-o", "NAME,PARTTYPE,FSTYPE")
	if err != nil {
		return nil, fmt.Errorf("lsblk: %w", err)
	}
	var blk lsblkOutput
	if err := json.Unmarshal(lsblkJSON, &blk); err != nil {
		return nil, fmt.Errorf("parsing lsblk output: %w", err)
	}

	// Identify the block device backing /boot — this tells us which disk (or RAID
	// array) we actually booted from so we update the right ESP.
	bootDevOut, err := runGetOut("findmnt", "-n", "-o", "SOURCE", "--target", "/boot")
	if err != nil {
		return nil, fmt.Errorf("findmnt /boot: %w", err)
	}
	bootDev := strings.TrimSpace(string(bootDevOut))
	// Resolve symlinks so by-uuid/by-id paths match the canonical names lsblk emits.
	if resolved, err := filepath.EvalSymlinks(bootDev); err == nil {
		bootDev = resolved
	}

	// RAID case: findmnt returns the assembled md device (e.g. /dev/md0), which
	// lsblk reports with the filesystem type of the data on it (e.g. "ext4").
	// Only the underlying member partitions carry FSType "linux_raid_member".
	// findBootPartition mimics the original shell script: it finds the partition
	// that either IS the boot device or has it as a direct child, so in the RAID
	// case it returns the member partition and its FSType is correct.
	bootPart := findBootPartition(blk.BlockDevices, bootDev)
	if bootPart != nil && bootPart.FSType == "linux_raid_member" {
		esps := findRAIDESPs(blk.BlockDevices, bootDev)
		if len(esps) == 0 {
			return nil, fmt.Errorf("no ESP replicas found for RAID device %s", bootDev)
		}
		return esps, nil
	}

	// Multi-disk non-RAID case (e.g. CNV nodes with multiple disks each having an
	// ESP): update only the ESP on the same disk as /boot to avoid touching disks
	// that are unrelated to the boot path.
	if countDisksWithESP(blk.BlockDevices) > 1 {
		esp := findESPOnBootDisk(blk.BlockDevices, bootDev)
		if esp == "" {
			return nil, fmt.Errorf("could not find ESP on boot disk containing %s", bootDev)
		}
		return []string{esp}, nil
	}

	// Simple single-disk case: update the one ESP present.
	esp := findAnyESP(blk.BlockDevices)
	if esp == "" {
		return nil, fmt.Errorf("no EFI System Partition found")
	}
	return []string{esp}, nil
}

// copyEFIToESP mounts device at /boot/efi, copies the EFI directory from efiSrc
// onto it, logs before/after sha256sums for verification, then unmounts.
func copyEFIToESP(device, efiSrc string) (retErr error) {
	const mountPoint = "/boot/efi"
	if err := runCmdSync("mount", device, mountPoint); err != nil {
		return fmt.Errorf("mounting %s: %w", device, err)
	}
	defer func() {
		if err := runCmdSync("umount", mountPoint); err != nil {
			logSystem("bootupd fallback: umount %s: %v", mountPoint, err)
			if retErr == nil {
				retErr = fmt.Errorf("unmounting %s: %w", mountPoint, err)
			}
		}
	}()

	// Remove .btmp.* directories left behind by the failed bootupctl update.
	// bootupd writes new files into .btmp.<dirname> before atomically renaming
	// the directory into place; a failed update leaves these orphaned.
	// This is best effort, the update will work even if the removal fails.
	if entries, err := os.ReadDir(mountPoint + "/EFI"); err == nil {
		for _, entry := range entries {
			if entry.IsDir() && strings.HasPrefix(entry.Name(), ".btmp.") {
				btmpPath := mountPoint + "/EFI/" + entry.Name()
				logSystem("bootupd fallback: removing leftover %s", btmpPath)
				if err := os.RemoveAll(btmpPath); err != nil {
					logSystem("bootupd fallback: failed to remove %s: %v", btmpPath, err)
				}
			}
		}
	}

	beforeSums, _ := runGetOut("sh", "-c", "find /boot/efi -type f | xargs sha256sum")
	logSystem("bootupd fallback [Before %s]:\n%s", device, string(beforeSums))

	if err := runCmdSync("cp", "-rp", efiSrc, mountPoint+"/"); err != nil {
		return fmt.Errorf("copying EFI files: %w", err)
	}

	afterSums, _ := runGetOut("sh", "-c", "find /boot/efi -type f | xargs sha256sum")
	logSystem("bootupd fallback [After %s]:\n%s", device, string(afterSums))

	return nil
}

// findDeviceByName recursively searches the device tree for a device with the given name.
func findDeviceByName(devices []lsblkDevice, name string) *lsblkDevice {
	for i := range devices {
		if devices[i].Name == name {
			return &devices[i]
		}
		if child := findDeviceByName(devices[i].Children, name); child != nil {
			return child
		}
	}
	return nil
}

// findBootPartition returns the partition that either IS bootDev or has it as a
// direct child. The child case handles RAID: findmnt returns the assembled md
// device, but the FSType we need ("linux_raid_member") is on the member partition
// one level up in the tree.
func findBootPartition(devices []lsblkDevice, bootDev string) *lsblkDevice {
	for _, disk := range devices {
		for i, child := range disk.Children {
			if child.Name == bootDev {
				return &disk.Children[i]
			}
			for _, grandchild := range child.Children {
				if grandchild.Name == bootDev {
					return &disk.Children[i]
				}
			}
		}
	}
	return nil
}

// countDisksWithESP returns the number of top-level disks that have an ESP partition.
func countDisksWithESP(devices []lsblkDevice) int {
	count := 0
	for _, disk := range devices {
		for _, child := range disk.Children {
			if child.PartType == espPartTypeGUID {
				count++
				break
			}
		}
	}
	return count
}

// findESPOnBootDisk returns the ESP partition on the same disk as bootDev.
// Used in multi-disk non-RAID setups to pick the correct ESP.
func findESPOnBootDisk(devices []lsblkDevice, bootDev string) string {
	for _, disk := range devices {
		for _, child := range disk.Children {
			if child.Name == bootDev {
				for _, sibling := range disk.Children {
					if sibling.PartType == espPartTypeGUID {
						return sibling.Name
					}
				}
			}
		}
	}
	return ""
}

// findAnyESP returns the first ESP partition found across all disks.
// Used in the simple single-disk case.
func findAnyESP(devices []lsblkDevice) string {
	for _, disk := range devices {
		for _, child := range disk.Children {
			if child.PartType == espPartTypeGUID {
				return child.Name
			}
		}
	}
	return ""
}

// findRAIDESPs returns the ESP partition on every disk that is a member of the
// RAID array containing raidDev. All replicas must be updated so the node can
// boot from any surviving member after a disk failure.
func findRAIDESPs(devices []lsblkDevice, raidDev string) []string {
	var esps []string
	for _, disk := range devices {
		diskHasRAIDMember := false
		for _, child := range disk.Children {
			for _, grandchild := range child.Children {
				if grandchild.Name == raidDev {
					diskHasRAIDMember = true
				}
			}
		}
		if diskHasRAIDMember {
			for _, child := range disk.Children {
				if child.PartType == espPartTypeGUID {
					esps = append(esps, child.Name)
				}
			}
		}
	}
	return esps
}
