package daemon

import (
	"testing"
)

// singleDiskTree represents a simple single-disk layout:
//
//	sda
//	  sda1  BIOS boot
//	  sda2  ESP (vfat)
//	  sda3  /boot (ext4)
//	  sda4  / (xfs)
var singleDiskTree = []lsblkDevice{
	{Name: "/dev/sda", Children: []lsblkDevice{
		{Name: "/dev/sda1", PartType: "21686148-6449-6e6f-744e-656564454649"},
		{Name: "/dev/sda2", PartType: espPartTypeGUID, FSType: "vfat"},
		{Name: "/dev/sda3", PartType: "0fc63daf-8483-4772-8e79-3d69d8477de4", FSType: "ext4"},
		{Name: "/dev/sda4", PartType: "0fc63daf-8483-4772-8e79-3d69d8477de4", FSType: "xfs"},
	}},
}

// multiDiskTree represents two disks each with their own ESP (e.g. CNV):
//
//	sda
//	  sda1  ESP
//	  sda2  /boot
//	sdb
//	  sdb1  ESP
//	  sdb2  data
var multiDiskTree = []lsblkDevice{
	{Name: "/dev/sda", Children: []lsblkDevice{
		{Name: "/dev/sda1", PartType: espPartTypeGUID, FSType: "vfat"},
		{Name: "/dev/sda2", PartType: "0fc63daf-8483-4772-8e79-3d69d8477de4", FSType: "ext4"},
	}},
	{Name: "/dev/sdb", Children: []lsblkDevice{
		{Name: "/dev/sdb1", PartType: espPartTypeGUID, FSType: "vfat"},
		{Name: "/dev/sdb2", PartType: "0fc63daf-8483-4772-8e79-3d69d8477de4", FSType: "xfs"},
	}},
}

// raidDiskTree represents a two-disk RAID setup:
//
//	sda
//	  sda1  ESP
//	  sda2  RAID member → md0
//	sdb
//	  sdb1  ESP
//	  sdb2  RAID member → md0
//	md0  /boot (assembled RAID)
var raidDiskTree = []lsblkDevice{
	{Name: "/dev/sda", Children: []lsblkDevice{
		{Name: "/dev/sda1", PartType: espPartTypeGUID, FSType: "vfat"},
		{Name: "/dev/sda2", PartType: "0fc63daf-8483-4772-8e79-3d69d8477de4", FSType: "linux_raid_member", Children: []lsblkDevice{
			{Name: "/dev/md0"},
		}},
	}},
	{Name: "/dev/sdb", Children: []lsblkDevice{
		{Name: "/dev/sdb1", PartType: espPartTypeGUID, FSType: "vfat"},
		{Name: "/dev/sdb2", PartType: "0fc63daf-8483-4772-8e79-3d69d8477de4", FSType: "linux_raid_member", Children: []lsblkDevice{
			{Name: "/dev/md0"},
		}},
	}},
}

func TestFindDeviceByName(t *testing.T) {
	dev := findDeviceByName(singleDiskTree, "/dev/sda3")
	if dev == nil {
		t.Fatal("expected to find /dev/sda3, got nil")
	}
	if dev.FSType != "ext4" {
		t.Errorf("expected ext4, got %q", dev.FSType)
	}

	if findDeviceByName(singleDiskTree, "/dev/sdz") != nil {
		t.Error("expected nil for nonexistent device")
	}
}

func TestCountDisksWithESP(t *testing.T) {
	if n := countDisksWithESP(singleDiskTree); n != 1 {
		t.Errorf("single disk: expected 1, got %d", n)
	}
	if n := countDisksWithESP(multiDiskTree); n != 2 {
		t.Errorf("multi disk: expected 2, got %d", n)
	}
}

func TestFindAnyESP(t *testing.T) {
	esp := findAnyESP(singleDiskTree)
	if esp != "/dev/sda2" {
		t.Errorf("expected /dev/sda2, got %q", esp)
	}

	if esp := findAnyESP([]lsblkDevice{}); esp != "" {
		t.Errorf("expected empty for no devices, got %q", esp)
	}
}

func TestFindESPOnBootDisk(t *testing.T) {
	// /boot is on sda2 → ESP should be sda1
	esp := findESPOnBootDisk(multiDiskTree, "/dev/sda2")
	if esp != "/dev/sda1" {
		t.Errorf("expected /dev/sda1, got %q", esp)
	}

	// /boot is on sdb2 → ESP should be sdb1
	esp = findESPOnBootDisk(multiDiskTree, "/dev/sdb2")
	if esp != "/dev/sdb1" {
		t.Errorf("expected /dev/sdb1, got %q", esp)
	}

	if esp := findESPOnBootDisk(multiDiskTree, "/dev/sdz9"); esp != "" {
		t.Errorf("expected empty for unknown boot device, got %q", esp)
	}
}

func TestFindBootPartition(t *testing.T) {
	// Single disk: boot device is a direct partition — should return it directly.
	part := findBootPartition(singleDiskTree, "/dev/sda3")
	if part == nil || part.Name != "/dev/sda3" {
		t.Errorf("expected /dev/sda3, got %v", part)
	}

	// RAID: boot device is the md device — should return the member partition,
	// whose FSType is "linux_raid_member", not the md device itself.
	part = findBootPartition(raidDiskTree, "/dev/md0")
	if part == nil {
		t.Fatal("expected a member partition for /dev/md0, got nil")
	}
	if part.FSType != "linux_raid_member" {
		t.Errorf("expected linux_raid_member FSType, got %q (name: %s)", part.FSType, part.Name)
	}

	if findBootPartition(singleDiskTree, "/dev/sdz9") != nil {
		t.Error("expected nil for unknown device")
	}
}

func TestFindRAIDESPs(t *testing.T) {
	esps := findRAIDESPs(raidDiskTree, "/dev/md0")
	if len(esps) != 2 {
		t.Fatalf("expected 2 ESP replicas, got %d: %v", len(esps), esps)
	}
	found := map[string]bool{"/dev/sda1": false, "/dev/sdb1": false}
	for _, esp := range esps {
		found[esp] = true
	}
	for dev, ok := range found {
		if !ok {
			t.Errorf("expected %s in RAID ESP list", dev)
		}
	}

	if esps := findRAIDESPs(raidDiskTree, "/dev/md99"); len(esps) != 0 {
		t.Errorf("expected empty for unknown RAID device, got %v", esps)
	}
}
