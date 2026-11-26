package extended

import (
	"fmt"
	"slices"
	"strings"

	o "github.com/onsi/gomega"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

func platformBasedDisksPatch(platform string, ms ManagedMachineResource) error {
	_, isCAPI := ms.(*CAPIMachineSet)
	switch platform {
	case AWSPlatform:
		if isCAPI {
			return ms.AppendDisks(platform,
				`{"deviceName":"/dev/sdb","encrypted":false,"iops":0,"size":120,"type":"gp3"}`,
				`{"deviceName":"/dev/sdc","encrypted":false,"iops":0,"size":120,"type":"gp3"}`,
			)
		}
		return ms.AppendDisks(platform,
			`{"deviceName":"/dev/sdb","ebs":{"encrypted":false,"iops":0,"volumeSize":120,"volumeType":"gp3"}}`,
			`{"deviceName":"/dev/sdc","ebs":{"encrypted":false,"iops":0,"volumeSize":120,"volumeType":"gp3"}}`,
		)
	case GCPPlatform:
		return ms.AppendDisks(platform,
			`{"autoDelete":true,"boot":false,"sizeGb":16,"type":"pd-standard"}`,
			`{"autoDelete":true,"boot":false,"sizeGb":16,"type":"pd-standard"}`,
		)
	case AzurePlatform:
		return ms.PatchDisks(platform, `[{"nameSuffix":"disk1","diskSizeGB":16,"lun":0,"deletionPolicy":"Delete"},{"nameSuffix":"disk2","diskSizeGB":16,"lun":1,"deletionPolicy":"Delete"}]`)
	case VspherePlatform:
		return ms.PatchDisks(platform, `[{"name":"data-disk-1","sizeGiB":16},{"name":"data-disk-2","sizeGiB":16}]`)
	case BaremetalPlatform:
		return nil
	default:
		return fmt.Errorf("Unknown platform: %v", platform)
	}
}

func platformBasedDisksNames(platform string) []string {
	switch platform {
	case AWSPlatform:
		// See: https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/nvme-ebs-volumes.html
		return []string{
			"/dev/nvme1n1",
			"/dev/nvme2n1",
			"/dev/nvme3n1",
			"/dev/nvme4n1",
		}
	case GCPPlatform:
		// See: https://docs.cloud.google.com/compute/docs/disks/set-persistent-device-name-in-linux-vm#persistent-disk-id
		return []string{
			"/dev/disk/by-id/google-persistent-disk-1",
			"/dev/disk/by-id/google-persistent-disk-2",
			"/dev/disk/by-id/google-persistent-disk-3",
			"/dev/disk/by-id/google-persistent-disk-4",
		}
	case AzurePlatform:
		// See: https://learn.microsoft.com/en-us/troubleshoot/azure/virtual-machines/linux/troubleshoot-device-names-problems
		return []string{
			"/dev/disk/azure/scsi1/lun0",
			"/dev/disk/azure/scsi1/lun1",
			"/dev/disk/azure/scsi1/lun2",
			"/dev/disk/azure/scsi1/lun3",
		}
	case VspherePlatform:
		// vSphere data disks are added sequentially to the SCSI controller starting
		// at unit 1 (unit 0 is the boot disk), so /dev/sd[b-e] mapping is stable
		// for simple configurations without extra SCSI controllers.
		return []string{
			"/dev/sdb",
			"/dev/sdc",
			"/dev/sdd",
			"/dev/sde",
		}
	case BaremetalPlatform:
		return []string{
			"/dev/vda",
			"/dev/vdb",
			"/dev/vdc",
			"/dev/vdd",
		}
	}
	return []string{}
}

// discoverNVMeByPathDisks discovers the /dev/disk/by-path/ entries for non-boot NVMe
// devices on an AWS node. These PCI-based paths are stable per instance type, unlike
// /dev/nvmeXn1 which depends on device enumeration order.
func discoverNVMeByPathDisks(node *Node) []string {
	script := `for p in /dev/disk/by-path/*nvme*; do [[ $p == *part* ]] && continue; ls ${p}-part* &>/dev/null && continue; echo "$p"; done | sort`
	stdout, _, err := node.DebugNodeWithChrootStd("bash", "-c", script)
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Failed to discover NVMe by-path disks on node %s", node.GetName())

	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			paths = append(paths, line)
		}
	}
	logger.Infof("Discovered %d non-boot NVMe by-path disks on node %s: %v", len(paths), node.GetName(), paths)
	return paths
}

type bareMetalCleanupDisk struct {
	device    NodeBlockDevice
	partition *NodeBlockDevice
}

type bareMetalCleanupState struct {
	disks   [2]bareMetalCleanupDisk
	raid    *NodeBlockDevice
	mounted bool
}

// inspectBareMetalTestDisks verifies topology, mounts and signatures before cleanup.
func inspectBareMetalTestDisks(node *Node, disks []string) (bareMetalCleanupState, error) {
	var state bareMetalCleanupState
	if len(disks) != len(state.disks) {
		return state, fmt.Errorf("expected two verified disks, got %d", len(disks))
	}
	devices, err := node.GetBlockDevices()
	if err != nil {
		return state, err
	}
	mounts, err := node.GetMounts()
	if err != nil {
		return state, err
	}

	knownDevices := map[string]bool{}
	var collectDevices func([]NodeBlockDevice)
	collectDevices = func(devices []NodeBlockDevice) {
		for _, device := range devices {
			if device.MajorMinor != "" {
				knownDevices[device.MajorMinor] = true
			}
			collectDevices(device.Children)
		}
	}
	collectDevices(devices)
	testDevices := map[string]bool{}
	partitions := map[string]bool{}
	raidMembers := 0
	for i, diskPath := range disks {
		resolved, err := node.ResolveHostPath(diskPath)
		if err != nil {
			return state, err
		}
		for _, device := range devices {
			if device.Name == resolved {
				if state.disks[i].device.Name != "" || device.Type != "disk" || device.MajorMinor == "" {
					return state, fmt.Errorf("unexpected block device for %s", diskPath)
				}
				state.disks[i].device = device
			}
		}
		disk := state.disks[i].device
		if disk.Name == "" || testDevices[disk.MajorMinor] {
			return state, fmt.Errorf("cannot identify distinct disk %s", diskPath)
		}
		testDevices[disk.MajorMinor] = true
		member, err := inspectBareMetalPartition(node, diskPath, i, &state, testDevices, partitions)
		if err != nil {
			return state, err
		}
		if member {
			raidMembers++
		}
	}
	if err := checkBareMetalRAIDTopology(node, state, devices, partitions, testDevices, raidMembers); err != nil {
		return state, err
	}
	if err := checkBareMetalCleanupMounts(mounts, knownDevices, testDevices, &state); err != nil {
		return state, err
	}
	if err := checkBareMetalCleanupSignatures(node, state); err != nil {
		return state, err
	}
	return state, nil
}

// inspectBareMetalPartition validates a test disk's partition and RAID membership.
func inspectBareMetalPartition(node *Node, diskPath string, index int, state *bareMetalCleanupState, testDevices, partitions map[string]bool) (bool, error) {
	disk := state.disks[index].device
	if len(disk.Children) > 1 {
		return false, fmt.Errorf("unexpected partitions on %s", disk.Name)
	}
	if len(disk.Children) == 0 {
		return false, nil
	}
	partition := disk.Children[0]
	if partition.Type != "part" || partition.MajorMinor == "" || partition.PartLabel != fmt.Sprintf("raid.1.%d", index+1) {
		return false, fmt.Errorf("unexpected partition on %s", disk.Name)
	}
	resolvedPart, err := node.ResolveHostPath(diskPath + "-part1")
	if err != nil {
		return false, fmt.Errorf("cannot verify partition on %s: %w", disk.Name, err)
	}
	if resolvedPart != partition.Name {
		return false, fmt.Errorf("partition on %s resolves to %s, not %s", disk.Name, resolvedPart, partition.Name)
	}
	if testDevices[partition.MajorMinor] {
		return false, fmt.Errorf("duplicate partition on %s", disk.Name)
	}
	state.disks[index].partition = &partition
	testDevices[partition.MajorMinor] = true
	partitions[partition.MajorMinor] = true
	if len(partition.Children) > 1 {
		return false, fmt.Errorf("unexpected devices on %s", partition.Name)
	}
	if len(partition.Children) == 0 {
		return false, nil
	}
	raid := partition.Children[0]
	if raid.Type != "raid0" || raid.MajorMinor == "" || len(raid.Children) != 0 {
		return false, fmt.Errorf("unexpected RAID device on %s", partition.Name)
	}
	if state.raid != nil && (state.raid.Name != raid.Name || state.raid.MajorMinor != raid.MajorMinor) {
		return false, fmt.Errorf("test partitions belong to different RAID devices")
	}
	state.raid = &raid
	return true, nil
}

// checkBareMetalRAIDTopology rejects RAID devices with unexpected members.
func checkBareMetalRAIDTopology(node *Node, state bareMetalCleanupState, devices []NodeBlockDevice, partitions, testDevices map[string]bool, raidMembers int) error {
	if state.raid == nil {
		return nil
	}
	if raidMembers != 2 || testDevices[state.raid.MajorMinor] {
		return fmt.Errorf("unexpected RAID members")
	}
	resolvedRAID, err := node.ResolveHostPath("/dev/md/data")
	if err != nil {
		return fmt.Errorf("cannot verify test RAID device: %w", err)
	}
	if resolvedRAID != state.raid.Name {
		return fmt.Errorf("test RAID resolves to %s, not %s", resolvedRAID, state.raid.Name)
	}
	testDevices[state.raid.MajorMinor] = true
	var checkParents func([]NodeBlockDevice) error
	checkParents = func(devices []NodeBlockDevice) error {
		for _, device := range devices {
			for _, child := range device.Children {
				if child.MajorMinor == state.raid.MajorMinor && !partitions[device.MajorMinor] {
					return fmt.Errorf("unexpected RAID member %s", device.Name)
				}
			}
			if err := checkParents(device.Children); err != nil {
				return err
			}
		}
		return nil
	}
	return checkParents(devices)
}

// checkBareMetalCleanupMounts ensures that the test devices do not back host mounts.
func checkBareMetalCleanupMounts(mounts []NodeMount, knownDevices, testDevices map[string]bool, state *bareMetalCleanupState) error {
	for _, target := range []string{"/sysroot", "/boot", "/boot/efi"} {
		mount, err := MountForPath(mounts, target)
		if err != nil {
			return fmt.Errorf("cannot verify %s: %w", target, err)
		}
		if !knownDevices[mount.MajorMinor] || testDevices[mount.MajorMinor] {
			return fmt.Errorf("cannot prove test disks are separate from %s (device %s)", target, mount.MajorMinor)
		}
	}
	for _, mount := range mounts {
		if mount.MajorMinor == "" {
			return fmt.Errorf("cannot identify the device mounted at %s", mount.Target)
		}
		if mount.Target == "/var/lib/data" {
			if state.raid == nil || mount.MajorMinor != state.raid.MajorMinor || state.mounted {
				return fmt.Errorf("unexpected mount at %s", mount.Target)
			}
			state.mounted = true
		} else if testDevices[mount.MajorMinor] {
			return fmt.Errorf("test device is unexpectedly mounted at %s", mount.Target)
		}
	}
	return nil
}

// checkBareMetalCleanupSignatures permits only the signatures created by the test.
func checkBareMetalCleanupSignatures(node *Node, state bareMetalCleanupState) error {
	for _, disk := range state.disks {
		allowed := []string{}
		if disk.partition != nil {
			allowed = []string{"gpt", "PMBR"}
			if err := checkBareMetalSignatures(node, disk.partition.Name, "linux_raid_member"); err != nil {
				return err
			}
		}
		if err := checkBareMetalSignatures(node, disk.device.Name, allowed...); err != nil {
			return err
		}
	}
	if state.raid != nil {
		if err := checkBareMetalSignatures(node, state.raid.Name, "ext4"); err != nil {
			return err
		}
	}
	return nil
}

// checkBareMetalSignatures rejects signatures not created by this test.
func checkBareMetalSignatures(node *Node, device string, allowed ...string) error {
	signatures, err := node.GetDeviceSignatures(device)
	if err != nil {
		return err
	}
	for _, signature := range signatures {
		if !slices.Contains(allowed, signature) {
			return fmt.Errorf("unexpected signature %s on %s", signature, device)
		}
	}
	return nil
}

// checkBareMetalDevicesUnmounted rechecks test devices immediately before a wipe.
func checkBareMetalDevicesUnmounted(node *Node, state bareMetalCleanupState) error {
	devices := map[string]bool{}
	for _, disk := range state.disks {
		devices[disk.device.MajorMinor] = true
		if disk.partition != nil {
			devices[disk.partition.MajorMinor] = true
		}
	}
	if state.raid != nil {
		devices[state.raid.MajorMinor] = true
	}
	mounts, err := node.GetMounts()
	if err != nil {
		return err
	}
	for _, mount := range mounts {
		if mount.MajorMinor == "" {
			return fmt.Errorf("cannot identify the device mounted at %s", mount.Target)
		}
		if devices[mount.MajorMinor] {
			return fmt.Errorf("test device is mounted at %s", mount.Target)
		}
	}
	return nil
}

// checkBareMetalDiskIdentity rechecks the selected disk and partition before a wipe.
func checkBareMetalDiskIdentity(node *Node, diskPath string, expected bareMetalCleanupDisk) error {
	resolvedDisk, err := node.ResolveHostPath(diskPath)
	if err != nil {
		return err
	}
	if expected.partition == nil {
		return fmt.Errorf("cannot verify partition on %s", diskPath)
	}
	resolvedPart, err := node.ResolveHostPath(diskPath + "-part1")
	if err != nil {
		return err
	}
	if resolvedDisk != expected.device.Name || resolvedPart != expected.partition.Name {
		return fmt.Errorf("disk paths changed during cleanup: %s, %s", resolvedDisk, resolvedPart)
	}
	devices, err := node.GetBlockDevices()
	if err != nil {
		return err
	}
	for _, disk := range devices {
		if disk.Name != resolvedDisk {
			continue
		}
		if disk.Type != "disk" || disk.MajorMinor != expected.device.MajorMinor || len(disk.Children) != 1 {
			return fmt.Errorf("disk identity or topology changed for %s", diskPath)
		}
		part := disk.Children[0]
		if part.Name != resolvedPart || part.Type != "part" || part.MajorMinor != expected.partition.MajorMinor ||
			part.PartLabel != expected.partition.PartLabel || len(part.Children) != 0 {
			return fmt.Errorf("partition identity or topology changed for %s", diskPath)
		}
		return nil
	}
	return fmt.Errorf("cannot find disk %s during cleanup", diskPath)
}

// cleanBareMetalRAID stops the test RAID and rechecks its member identities.
func cleanBareMetalRAID(node *Node, disks []string, state bareMetalCleanupState) (bareMetalCleanupState, error) {
	if state.mounted {
		if _, err := node.RunHostCommand("umount", "/var/lib/data"); err != nil {
			return state, err
		}
	}
	if err := checkBareMetalDevicesUnmounted(node, state); err != nil {
		return state, err
	}
	if err := checkBareMetalSignatures(node, state.raid.Name, "ext4"); err != nil {
		return state, err
	}
	if _, err := node.RunHostCommand("wipefs", "--all", state.raid.Name); err != nil {
		return state, err
	}
	if err := checkBareMetalSignatures(node, state.raid.Name); err != nil {
		return state, err
	}
	if _, err := node.RunHostCommand("mdadm", "--stop", state.raid.Name); err != nil {
		return state, err
	}
	updated, err := inspectBareMetalTestDisks(node, disks)
	if err != nil {
		return state, err
	}
	if updated.raid != nil {
		return state, fmt.Errorf("RAID device is still active after cleanup")
	}
	for i := range state.disks {
		previous, current := state.disks[i], updated.disks[i]
		if previous.device.Name != current.device.Name || previous.device.MajorMinor != current.device.MajorMinor ||
			previous.partition == nil || current.partition == nil ||
			previous.partition.Name != current.partition.Name || previous.partition.MajorMinor != current.partition.MajorMinor {
			return state, fmt.Errorf("disk topology changed during RAID cleanup")
		}
	}
	return updated, nil
}

// cleanBareMetalTestDisks removes and verifies only this test's disk signatures.
func cleanBareMetalTestDisks(node *Node, disks []string) error {
	state, err := inspectBareMetalTestDisks(node, disks)
	if err != nil {
		return err
	}
	if state.raid != nil {
		state, err = cleanBareMetalRAID(node, disks, state)
		if err != nil {
			return err
		}
	}
	for i, disk := range state.disks {
		if disk.partition == nil {
			continue
		}
		if err := checkBareMetalDevicesUnmounted(node, state); err != nil {
			return err
		}
		if err := checkBareMetalSignatures(node, disk.partition.Name, "linux_raid_member"); err != nil {
			return err
		}
		if err := checkBareMetalDiskIdentity(node, disks[i], disk); err != nil {
			return err
		}
		if _, err := node.RunHostCommand("wipefs", "--all", disk.partition.Name); err != nil {
			return err
		}
		if err := checkBareMetalSignatures(node, disk.partition.Name); err != nil {
			return err
		}
	}
	for i, disk := range state.disks {
		if disk.partition != nil {
			if err := checkBareMetalDevicesUnmounted(node, state); err != nil {
				return err
			}
			if err := checkBareMetalSignatures(node, disk.device.Name, "gpt", "PMBR"); err != nil {
				return err
			}
			if err := checkBareMetalDiskIdentity(node, disks[i], disk); err != nil {
				return err
			}
			if _, err := node.RunHostCommand("wipefs", "--all", disk.device.Name); err != nil {
				return err
			}
		}
		if err := checkBareMetalSignatures(node, disk.device.Name); err != nil {
			return err
		}
	}
	return nil
}
