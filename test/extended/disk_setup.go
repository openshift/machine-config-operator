package extended

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"

	osconfigv1 "github.com/openshift/api/config/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	machineclient "github.com/openshift/client-go/machine/clientset/versioned"
	machineconfigclient "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	extended2 "github.com/openshift/machine-config-operator/test/extended-priv"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/test/e2e/framework"
)

var _ = g.Describe("[sig-mco][OCPFeatureGate:MultiDiskSetup][Suite:openshift/machine-config-operator/disruptive][Serial][Disruptive]", func() {
	defer g.GinkgoRecover()

	var (
		oc = exutil.NewCLI("mco-disk-setup", exutil.KubeConfigPath()).AsAdmin()
	)

	g.BeforeEach(func() {
		// Skip if cluster doesn't use Machine API
		skipUnlessFunctionalMachineAPI(oc)
		// Skip on single node clusters
		skipOnSingleNodeTopology(oc)
	})

	// Note: Disk setup machine configs follow the installer pattern:
	// - Name format: "01-disk-setup-{sanitized-label}-{role}"
	// - Creates systemd mount units for data disks
	// - Creates systemd swap units for swap disks
	// See: https://github.com/openshift/installer/blob/main/pkg/asset/machines/machineconfig/disks.go

	g.Describe("Etcd Disk Setup", g.Label("Serial"), func() {
		g.It("Should validate etcd disk machine config exists and is properly configured [apigroup:machineconfiguration.openshift.io]", g.Label("Conformance"), func() {
			validateEtcdDiskMachineConfig(oc)
		})

		// todo: change this to forcing cpms to rollout a new master
		g.It("Should configure etcd disk on master nodes during scale-up [apigroup:machine.openshift.io]", func() {
			testControlPlaneEtcdCpmsScale(oc)
		})

	})

	g.Describe("Swap Disk Setup", g.Label("Serial"), func() {
		g.It("Should validate swap disk machine config exists and is properly configured [apigroup:machineconfiguration.openshift.io]", g.Label("Conformance"), func() {
			validateSwapDiskMachineConfig(oc)
		})

		// todo: jcallen: you can't day 2 configure the disk
		g.It("Should configure swap disk on worker nodes during scale-up [apigroup:machine.openshift.io]", func() {
			testSwapDiskSetup(oc)
		})
	})

	g.Describe("User-Defined Disk Setup", g.Label("Serial"), func() {
		g.It("Should validate user-defined disk machine config exists and is properly configured [apigroup:machineconfiguration.openshift.io]", g.Label("Conformance"), func() {
			validateUserDefinedDiskMachineConfig(oc)
		})

		g.It("Should configure user-defined disks on worker nodes during scale-up [apigroup:machine.openshift.io]", func() {
			testUserDefinedDiskSetup(oc)
		})

	})

	g.Describe("Multi-Disk Setup ", g.Label("Serial"), func() {
		g.It("Should validate multiple disk types configured together [apigroup:machineconfiguration.openshift.io]", g.Label("Conformance"), func() {
			validateMultiDiskSetup(oc)
		})
	})
})

func testControlPlaneEtcdCpmsScale(oc *exutil.CLI) {
	// TODO: This test needs to be implemented to:
	// 1. Delete a control plane machine
	// 2. Wait for CPMS to create a replacement
	// 3. Find the new node
	// 4. Validate etcd disk configuration on the new node
	// For now, validate etcd disk on existing control plane nodes

	nodes, err := extended2.NewNodeList(oc).GetAll()
	o.Expect(err).NotTo(o.HaveOccurred())

	for _, node := range nodes {
		// Check if this is a control plane node
		cpLabel, err := node.GetLabel("node-role.kubernetes.io/control-plane")
		if err == nil && cpLabel != "" {
			nodeName := node.GetName()
			// Found a control plane node, validate its etcd disk setup
			framework.Logf("Validating etcd disk on control plane node: %s", nodeName)
			validateEtcdDiskOnNode(oc, nodeName)
			// Only need to validate one control plane node for now
			return
		}
	}

	framework.Logf("No control plane nodes found to validate etcd disk setup")
}

// validateEtcdDiskMachineConfig checks that etcd disk machine configs exist and are properly configured
func validateEtcdDiskMachineConfig(oc *exutil.CLI) {
	exutil.By("Validating etcd disk machine config exists")

	machineConfigClient, err := machineconfigclient.NewForConfig(oc.KubeFramework().ClientConfig())
	o.Expect(err).NotTo(o.HaveOccurred())

	// Look for machine configs with etcd disk setup
	machineConfigs, err := machineConfigClient.MachineconfigurationV1().MachineConfigs().List(context.Background(), metav1.ListOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())

	var etcdDiskMC *mcfgv1.MachineConfig
	for _, mc := range machineConfigs.Items {
		// Machine configs follow installer pattern: "01-disk-setup-{label}-{role}"
		// Look for configs with "master" or "control" in the name and etcd disk setup
		if (strings.Contains(mc.Name, "disk-setup") && strings.Contains(mc.Name, "master")) && containsEtcdDiskSetup(&mc) {
			etcdDiskMC = &mc
			break
		}
	}

	if etcdDiskMC != nil {
		framework.Logf("Found etcd disk machine config: %s", etcdDiskMC.Name)
		validateEtcdDiskIgnitionConfig(etcdDiskMC)

		// Validate on control plane nodes
		nodes, err := extended2.NewNodeList(oc).GetAll()
		o.Expect(err).NotTo(o.HaveOccurred())

		for _, node := range nodes {
			cpLabel, err := node.GetLabel("node-role.kubernetes.io/control-plane")
			// Node is a control plane node if the label exists (regardless of value)
			if err == nil && cpLabel != "" {
				nodeName := node.GetName()
				framework.Logf("Validating etcd disk on control plane node: %s", nodeName)
				validateEtcdDiskOnNode(oc, nodeName)
				// Only validate one control plane node
				break
			}
		}

	} else {
		framework.Logf("No etcd disk machine config found - this is expected if no etcd disks are configured")
	}
}

// validateSwapDiskMachineConfig checks that swap disk machine configs exist and are properly configured
func validateSwapDiskMachineConfig(oc *exutil.CLI) {
	exutil.By("Validating swap disk machine config exists")

	machineConfigClient, err := machineconfigclient.NewForConfig(oc.KubeFramework().ClientConfig())
	o.Expect(err).NotTo(o.HaveOccurred())

	machineConfigs, err := machineConfigClient.MachineconfigurationV1().MachineConfigs().List(context.Background(), metav1.ListOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())

	var swapDiskMC *mcfgv1.MachineConfig
	for _, mc := range machineConfigs.Items {
		// Machine configs follow installer pattern: "01-disk-setup-{label}-{role}"
		// Look for configs with "worker" in the name and swap disk setup
		if (strings.Contains(mc.Name, "disk-setup") && strings.Contains(mc.Name, "worker")) && containsSwapDiskSetup(&mc) {
			swapDiskMC = &mc
			break
		}
	}

	if swapDiskMC != nil {
		framework.Logf("Found swap disk machine config: %s", swapDiskMC.Name)
		validateSwapDiskIgnitionConfig(swapDiskMC)
	} else {
		framework.Logf("No swap disk machine config found - this is expected if no swap disks are configured")
	}
}

// validateUserDefinedDiskMachineConfig checks that user-defined disk machine configs exist and are properly configured
func validateUserDefinedDiskMachineConfig(oc *exutil.CLI) {
	exutil.By("Validating user-defined disk machine config exists")

	machineConfigClient, err := machineconfigclient.NewForConfig(oc.KubeFramework().ClientConfig())
	o.Expect(err).NotTo(o.HaveOccurred())

	machineConfigs, err := machineConfigClient.MachineconfigurationV1().MachineConfigs().List(context.Background(), metav1.ListOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())

	var userDefinedDiskMC *mcfgv1.MachineConfig
	for _, mc := range machineConfigs.Items {
		// Machine configs follow installer pattern: "01-disk-setup-{label}-{role}"
		// User-defined disks can be on any role, so just check for disk setup prefix and user-defined mounts
		if strings.Contains(mc.Name, "disk-setup") && containsUserDefinedDiskSetup(&mc) {
			userDefinedDiskMC = &mc
			break
		}
	}

	if userDefinedDiskMC != nil {
		framework.Logf("Found user-defined disk machine config: %s", userDefinedDiskMC.Name)
		validateUserDefinedDiskIgnitionConfig(userDefinedDiskMC)
	} else {
		framework.Logf("No user-defined disk machine config found - this is expected if no user-defined disks are configured")
	}
}

// testSwapDiskSetup tests swap disk setup during machine scale-up
func testSwapDiskSetup(oc *exutil.CLI) {
	exutil.By("Setting up swap disk configuration on worker machineset")

	machineClient, err := machineclient.NewForConfig(oc.KubeFramework().ClientConfig())
	o.Expect(err).NotTo(o.HaveOccurred())

	// Get worker machineset
	workerMachineSet := getRandomMachineSet(machineClient)
	framework.Logf("Testing swap disk setup on worker machineset: %s", workerMachineSet.Name)

	// Verify machine config exists before testing
	exutil.By("Verifying swap disk machine config exists before scale-up")
	validateSwapDiskMachineConfig(oc)

	exutil.By("Scaling up worker machineset to test swap disk provisioning")
	newNodes, cleanupFunc := scaleMachineSet(oc, workerMachineSet.Name, *workerMachineSet.Spec.Replicas+1)
	defer cleanupFunc()

	newNode := newNodes[0]
	exutil.By("Validating swap disk is properly configured on new node")
	validateSwapDiskOnNode(oc, newNode)
}

// testUserDefinedDiskSetup tests user-defined disk setup during machine scale-up
func testUserDefinedDiskSetup(oc *exutil.CLI) {
	exutil.By("Setting up user-defined disk configuration on worker machineset")

	machineClient, err := machineclient.NewForConfig(oc.KubeFramework().ClientConfig())
	o.Expect(err).NotTo(o.HaveOccurred())

	workerMachineSet := getRandomMachineSet(machineClient)
	framework.Logf("Testing user-defined disk setup on worker machineset: %s", workerMachineSet.Name)

	// Verify machine config exists before testing
	exutil.By("Verifying user-defined disk machine config exists before scale-up")
	validateUserDefinedDiskMachineConfig(oc)

	exutil.By("Scaling up worker machineset to test user-defined disk provisioning")
	newNodes, cleanupFunc := scaleMachineSet(oc, workerMachineSet.Name, *workerMachineSet.Spec.Replicas+1)
	defer cleanupFunc()

	newNode := newNodes[0]
	exutil.By("Validating user-defined disk is properly configured on new node")
	validateUserDefinedDiskOnNode(oc, newNode, "/mnt/data")
}

// Helper functions for machine config validation based on https://github.com/openshift/installer/blob/main/pkg/asset/machines/machineconfig/disks.go#L127

// containsEtcdDiskSetup checks if a machine config contains etcd disk setup
func containsEtcdDiskSetup(mc *mcfgv1.MachineConfig) bool {
	if mc.Spec.Config.Raw == nil {
		return false
	}

	var ignitionConfig ign3types.Config
	if err := json.Unmarshal(mc.Spec.Config.Raw, &ignitionConfig); err != nil {
		return false
	}

	// Check for etcd-specific disk configurations
	for _, disk := range ignitionConfig.Storage.Disks {
		for _, partition := range disk.Partitions {
			if partition.Label != nil && strings.Contains(*partition.Label, "etcd") {
				return true
			}
		}
	}

	// Check for etcd mount points in filesystem configurations
	for _, filesystem := range ignitionConfig.Storage.Filesystems {
		if filesystem.Path != nil && strings.Contains(*filesystem.Path, "/var/lib/etcd") {
			return true
		}
	}

	return false
}

// containsSwapDiskSetup checks if a machine config contains swap disk setup
func containsSwapDiskSetup(mc *mcfgv1.MachineConfig) bool {
	if mc.Spec.Config.Raw == nil {
		return false
	}

	var ignitionConfig ign3types.Config
	if err := json.Unmarshal(mc.Spec.Config.Raw, &ignitionConfig); err != nil {
		return false
	}

	// Check for swap-specific disk configurations
	for _, disk := range ignitionConfig.Storage.Disks {
		for _, partition := range disk.Partitions {
			if partition.Label != nil && strings.Contains(*partition.Label, "swap") {
				return true
			}
		}
	}

	// Check for swap filesystem type
	for _, filesystem := range ignitionConfig.Storage.Filesystems {
		if filesystem.Format != nil && *filesystem.Format == "swap" {
			return true
		}
	}

	return false
}

// containsUserDefinedDiskSetup checks if a machine config contains user-defined disk setup
func containsUserDefinedDiskSetup(mc *mcfgv1.MachineConfig) bool {
	if mc.Spec.Config.Raw == nil {
		return false
	}

	var ignitionConfig ign3types.Config
	if err := json.Unmarshal(mc.Spec.Config.Raw, &ignitionConfig); err != nil {
		return false
	}

	// Check for user-defined mount points (not system paths)
	for _, filesystem := range ignitionConfig.Storage.Filesystems {
		if filesystem.Path != nil {
			path := *filesystem.Path
			if strings.HasPrefix(path, "/mnt/") || strings.HasPrefix(path, "/opt/") || strings.HasPrefix(path, "/home/") {
				return true
			}
		}
	}

	return false
}

// validateEtcdDiskIgnitionConfig validates the ignition config for etcd disk setup
func validateEtcdDiskIgnitionConfig(mc *mcfgv1.MachineConfig) {
	o.Expect(mc.Spec.Config.Raw).NotTo(o.BeNil(), "Machine config should have ignition configuration")

	var ignitionConfig ign3types.Config
	err := json.Unmarshal(mc.Spec.Config.Raw, &ignitionConfig)
	o.Expect(err).NotTo(o.HaveOccurred(), "Should be able to parse ignition config")

	// Validate etcd-specific configurations
	foundEtcdMount := false
	for _, filesystem := range ignitionConfig.Storage.Filesystems {
		if filesystem.Path != nil && strings.Contains(*filesystem.Path, "/var/lib/etcd") {
			foundEtcdMount = true
			o.Expect(filesystem.Format).NotTo(o.BeNil(), "Etcd filesystem should have format specified")
			framework.Logf("Found etcd filesystem with format: %s", *filesystem.Format)
		}
	}

	if foundEtcdMount {
		framework.Logf("Etcd disk setup validation passed")
	}
}

// validateSwapDiskIgnitionConfig validates the ignition config for swap disk setup
func validateSwapDiskIgnitionConfig(mc *mcfgv1.MachineConfig) {
	o.Expect(mc.Spec.Config.Raw).NotTo(o.BeNil(), "Machine config should have ignition configuration")

	var ignitionConfig ign3types.Config
	err := json.Unmarshal(mc.Spec.Config.Raw, &ignitionConfig)
	o.Expect(err).NotTo(o.HaveOccurred(), "Should be able to parse ignition config")

	// Validate swap-specific configurations
	foundSwap := false
	for _, filesystem := range ignitionConfig.Storage.Filesystems {
		if filesystem.Format != nil && *filesystem.Format == "swap" {
			foundSwap = true
			framework.Logf("Found swap filesystem configuration")
		}
	}

	if foundSwap {
		framework.Logf("Swap disk setup validation passed")
	}
}

// validateUserDefinedDiskIgnitionConfig validates the ignition config for user-defined disk setup
func validateUserDefinedDiskIgnitionConfig(mc *mcfgv1.MachineConfig) {
	o.Expect(mc.Spec.Config.Raw).NotTo(o.BeNil(), "Machine config should have ignition configuration")

	var ignitionConfig ign3types.Config
	err := json.Unmarshal(mc.Spec.Config.Raw, &ignitionConfig)
	o.Expect(err).NotTo(o.HaveOccurred(), "Should be able to parse ignition config")

	// Validate user-defined mount points
	foundUserMount := false
	for _, filesystem := range ignitionConfig.Storage.Filesystems {
		if filesystem.Path != nil {
			path := *filesystem.Path
			if strings.HasPrefix(path, "/mnt/") || strings.HasPrefix(path, "/opt/") || strings.HasPrefix(path, "/home/") {
				foundUserMount = true
				o.Expect(filesystem.Format).NotTo(o.BeNil(), "User-defined filesystem should have format specified")
				framework.Logf("Found user-defined filesystem at path: %s with format: %s", path, *filesystem.Format)
			}
		}
	}

	if foundUserMount {
		framework.Logf("User-defined disk setup validation passed")
	}
}

// validateMultiDiskSetup validates that multiple disk types can be configured together on the same cluster
func validateMultiDiskSetup(oc *exutil.CLI) {
	exutil.By("Validating multiple disk types are configured together")

	machineConfigClient, err := machineconfigclient.NewForConfig(oc.KubeFramework().ClientConfig())
	o.Expect(err).NotTo(o.HaveOccurred())

	machineConfigs, err := machineConfigClient.MachineconfigurationV1().MachineConfigs().List(context.Background(), metav1.ListOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())

	// Track which disk types are found
	var foundEtcdDisk, foundSwapDisk, foundUserDefinedDisk bool

	for _, mc := range machineConfigs.Items {
		if strings.Contains(mc.Name, "disk-setup") {
			mcCopy := mc
			if containsEtcdDiskSetup(&mcCopy) {
				foundEtcdDisk = true
				framework.Logf("Found etcd disk machine config: %s", mc.Name)
			}
			if containsSwapDiskSetup(&mcCopy) {
				foundSwapDisk = true
				framework.Logf("Found swap disk machine config: %s", mc.Name)
			}
			if containsUserDefinedDiskSetup(&mcCopy) {
				foundUserDefinedDisk = true
				framework.Logf("Found user-defined disk machine config: %s", mc.Name)
			}
		}
	}

	// If multiple disk types are found, validate them together
	if foundEtcdDisk && foundSwapDisk {
		exutil.By("Validating etcd and swap disks are configured together")
		framework.Logf("Both etcd and swap disk configs found - multi-disk setup detected")

		// Find a node that should have both configurations
		nodes, err := extended2.NewNodeList(oc).GetAll()
		o.Expect(err).NotTo(o.HaveOccurred())

		// Check control plane nodes which might have both etcd and swap
		for _, node := range nodes {
			cpLabel, err := node.GetLabel("node-role.kubernetes.io/control-plane")
			if err == nil && cpLabel != "" {
				nodeName := node.GetName()
				framework.Logf("Validating multi-disk setup on control plane node: %s", nodeName)

				// Validate etcd disk
				validateEtcdDiskOnNode(oc, nodeName)

				// Check if this node also has swap configured
				nodeObj := extended2.NewNode(oc, nodeName)
				output, err := nodeObj.DebugNodeWithChroot("swapon", "--show")
				if err == nil && strings.Contains(output, "swap") {
					framework.Logf("Node %s has both etcd and swap disks configured", nodeName)
					validateSwapDiskOnNode(oc, nodeName)
				}

				break
			}
		}
	}

	if foundSwapDisk && foundUserDefinedDisk {
		exutil.By("Validating swap and user-defined disks are configured together")
		framework.Logf("Both swap and user-defined disk configs found - multi-disk setup detected")

		// Find a worker node that might have both
		nodes, err := extended2.NewNodeList(oc).GetAll()
		o.Expect(err).NotTo(o.HaveOccurred())

		for _, node := range nodes {
			workerLabel, err := node.GetLabel("node-role.kubernetes.io/worker")
			if err == nil && workerLabel == "" {
				nodeName := node.GetName()
				framework.Logf("Validating multi-disk setup on worker node: %s", nodeName)

				// Check if this node has swap configured
				nodeObj := extended2.NewNode(oc, nodeName)
				output, err := nodeObj.DebugNodeWithChroot("swapon", "--show")
				if err == nil && strings.Contains(output, "swap") {
					validateSwapDiskOnNode(oc, nodeName)
				}

				// Check if this node has user-defined mount
				output, err = nodeObj.DebugNodeWithChroot("mount")
				if err == nil && (strings.Contains(output, "/mnt/") || strings.Contains(output, "/opt/")) {
					// Find the mount path
					lines := strings.Split(output, "\n")
					for _, line := range lines {
						if strings.Contains(line, "/mnt/") {
							parts := strings.Fields(line)
							if len(parts) >= 3 {
								mountPath := parts[2]
								framework.Logf("Found user-defined mount at: %s", mountPath)
								validateUserDefinedDiskOnNode(oc, nodeName, mountPath)
								break
							}
						}
					}
				}

				break
			}
		}
	}

	if foundEtcdDisk || foundSwapDisk || foundUserDefinedDisk {
		framework.Logf("Multi-disk validation completed. Found: etcd=%v, swap=%v, user-defined=%v",
			foundEtcdDisk, foundSwapDisk, foundUserDefinedDisk)
	} else {
		framework.Logf("No disk setup configurations found - skipping multi-disk validation")
	}
}

// Node validation functions

// validateEtcdDiskOnNode validates etcd disk configuration on a specific node
func validateEtcdDiskOnNode(oc *exutil.CLI, nodeName string) {
	node := extended2.NewNode(oc, nodeName)

	exutil.By("Checking etcd disk mount point")
	output, err := node.DebugNodeWithChroot("lsblk", "-f")
	/* example output
	[root@jcallen-pcdb2-master-0 /]# lsblk -f
	NAME   FSTYPE FSVER LABEL      UUID                                 FSAVAIL FSUSE% MOUNTPOINTS
	loop0  erofs
	sda
	└─sda1 xfs          etcd       78efdd8f-ba84-4758-9a87-ecddea99eb89   31.2G     2% /var/lib/etcd
	sdb
	└─sdb1 xfs          containers 65d0480f-9e26-4465-a916-ac2dc6426018  108.9G    15% /var/lib/containers
	sdc
	├─sdc1
	├─sdc2 vfat   FAT16 EFI-SYSTEM 7B77-95E7
	├─sdc3 ext4   1.0   boot       55fa52ae-3184-4985-9477-9e9572831886    201M    36% /boot
	├─sdc4 xfs          root       e79299c7-2bbf-408b-91c9-add7c9722613   45.4G     6% /sysroot/ostree/deploy/rhcos/var
	│                                                                                  /sysroot
	│                                                                                  /etc
	└─sdc5 xfs                     9e5021d6-5f7a-4822-bd81-2b5c95392094  965.7G     1% /var
	*/
	o.Expect(err).NotTo(o.HaveOccurred())
	o.Expect(output).To(o.ContainSubstring("/var/lib/etcd"), "etcd disk should be mounted at /var/lib/etcd")

	// Platform-specific disk device validation
	platformType := getPlatformType(oc)
	if platformType == osconfigv1.VSpherePlatformType {
		exutil.By("Validating vSphere-specific disk device path for etcd")
		// vSphere uses disk paths like: /dev/disk/by-path/pci-0000:03:00.0-scsi-0:0:1:0
		// The etcd disk is typically the first data disk (index 1)
		output, err = node.DebugNodeWithChroot("ls", "-la", "/dev/disk/by-path/")
		o.Expect(err).NotTo(o.HaveOccurred())
		framework.Logf("vSphere disk devices: %s", output)

		// Verify the disk device path exists and is used
		output, err = node.DebugNodeWithChroot("findmnt", "-n", "-o", "SOURCE", "/var/lib/etcd")
		o.Expect(err).NotTo(o.HaveOccurred())
		framework.Logf("Etcd disk source device: %s", output)
	} else if platformType == osconfigv1.AzurePlatformType {
		exutil.By("Validating Azure-specific disk device path for etcd")
		// Azure uses LUN-based device mapping: /dev/disk/azure/scsi1/lunN
		// List Azure-specific disk paths
		output, err = node.DebugNodeWithChroot("ls", "-la", "/dev/disk/azure/scsi1/")
		if err == nil {
			framework.Logf("Azure disk devices: %s", output)
		}

		// Verify the disk device path exists and is used
		output, err = node.DebugNodeWithChroot("findmnt", "-n", "-o", "SOURCE", "/var/lib/etcd")
		o.Expect(err).NotTo(o.HaveOccurred())
		framework.Logf("Etcd disk source device: %s", output)

		// Verify device is an Azure data disk (typically /dev/sdX or Azure symlink)
		output, err = node.DebugNodeWithChroot("readlink", "-f", "/dev/disk/azure/scsi1/lun0")
		if err == nil {
			framework.Logf("Azure LUN0 resolves to: %s", output)
		}
	}

	exutil.By("Validating etcd disk filesystem")
	output, err = node.DebugNodeWithChroot("df", "-h", "/var/lib/etcd")
	o.Expect(err).NotTo(o.HaveOccurred())
	framework.Logf("Etcd disk filesystem: %s", output)

	exutil.By("Checking etcd disk permissions")
	output, err = node.DebugNodeWithChroot("ls", "-la", "/var/lib/etcd")
	o.Expect(err).NotTo(o.HaveOccurred())
	framework.Logf("Etcd directory permissions: %s", output)

	exutil.By("Validating filesystem type is XFS")
	output, err = node.DebugNodeWithChroot("findmnt", "-n", "-o", "FSTYPE", "/var/lib/etcd")
	o.Expect(err).NotTo(o.HaveOccurred())
	o.Expect(strings.TrimSpace(output)).To(o.Equal("xfs"), "etcd disk should use XFS filesystem")
	framework.Logf("Etcd disk filesystem type: %s", strings.TrimSpace(output))

	exutil.By("Validating systemd mount unit for etcd disk")
	// Check that the systemd mount unit exists and is active
	output, err = node.DebugNodeWithChroot("systemctl", "status", "var-lib-etcd.mount")
	o.Expect(err).NotTo(o.HaveOccurred())
	o.Expect(output).To(o.ContainSubstring("active"), "etcd mount unit should be active")
	framework.Logf("Etcd mount unit status: %s", output)

	// Verify the mount unit is enabled
	output, err = node.DebugNodeWithChroot("systemctl", "is-enabled", "var-lib-etcd.mount")
	o.Expect(err).NotTo(o.HaveOccurred())
	framework.Logf("Etcd mount unit enabled status: %s", output)
}

// validateSwapDiskOnNode validates swap disk configuration on a specific node
func validateSwapDiskOnNode(oc *exutil.CLI, nodeName string) {
	node := extended2.NewNode(oc, nodeName)

	exutil.By("Checking swap activation")
	output, err := node.DebugNodeWithChroot("swapon", "--show")
	o.Expect(err).NotTo(o.HaveOccurred())
	o.Expect(output).To(o.ContainSubstring("swap"), "swap should be active")

	exutil.By("Validating filesystem type is swap")
	output, err = node.DebugNodeWithChroot("swapon", "--show=TYPE", "--noheadings")
	o.Expect(err).NotTo(o.HaveOccurred())
	o.Expect(strings.TrimSpace(output)).To(o.ContainSubstring("partition"), "swap should be a partition type")
	framework.Logf("Swap type: %s", strings.TrimSpace(output))

	// Platform-specific disk device validation
	platformType := getPlatformType(oc)
	if platformType == osconfigv1.VSpherePlatformType {
		exutil.By("Validating vSphere-specific disk device path for swap")
		// vSphere uses disk paths like: /dev/disk/by-path/pci-0000:03:00.0-scsi-0:0:N:0
		output, err = node.DebugNodeWithChroot("ls", "-la", "/dev/disk/by-path/")
		o.Expect(err).NotTo(o.HaveOccurred())
		framework.Logf("vSphere disk devices: %s", output)

		// Verify swap device is from the expected path
		output, err = node.DebugNodeWithChroot("swapon", "--show=NAME,TYPE")
		o.Expect(err).NotTo(o.HaveOccurred())
		framework.Logf("Swap device details: %s", output)
	} else if platformType == osconfigv1.AzurePlatformType {
		exutil.By("Validating Azure-specific disk device path for swap")
		// Azure uses LUN-based device mapping
		output, err = node.DebugNodeWithChroot("ls", "-la", "/dev/disk/azure/scsi1/")
		if err == nil {
			framework.Logf("Azure disk devices: %s", output)
		}

		// Verify swap device details
		output, err = node.DebugNodeWithChroot("swapon", "--show=NAME,TYPE")
		o.Expect(err).NotTo(o.HaveOccurred())
		framework.Logf("Swap device details: %s", output)
	}

	exutil.By("Validating swap in fstab")
	output, err = node.DebugNodeWithChroot("cat", "/etc/fstab")
	o.Expect(err).NotTo(o.HaveOccurred())
	o.Expect(output).To(o.ContainSubstring("swap"), "swap should be configured in fstab")

	exutil.By("Checking swap priority")
	output, err = node.DebugNodeWithChroot("cat", "/proc/swaps")
	o.Expect(err).NotTo(o.HaveOccurred())
	framework.Logf("Swap configuration: %s", output)

	exutil.By("Verifying swap space availability")
	output, err = node.DebugNodeWithChroot("free", "-h")
	o.Expect(err).NotTo(o.HaveOccurred())
	o.Expect(output).To(o.ContainSubstring("Swap:"), "swap space should be available")
	framework.Logf("Memory and swap info: %s", output)

	exutil.By("Validating systemd swap unit")
	// Get the swap device name to construct the systemd unit name
	output, err = node.DebugNodeWithChroot("swapon", "--show=NAME", "--noheadings")
	o.Expect(err).NotTo(o.HaveOccurred())
	swapDevice := strings.TrimSpace(output)
	if swapDevice != "" {
		// Convert device path to systemd unit name (e.g., /dev/sda1 -> dev-sda1.swap)
		// For partition-based swap, systemd creates .swap units
		framework.Logf("Swap device: %s", swapDevice)

		// Check for any active swap units
		output, err = node.DebugNodeWithChroot("systemctl", "list-units", "--type=swap", "--no-pager")
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(output).To(o.ContainSubstring("swap"), "at least one swap unit should be active")
		framework.Logf("Systemd swap units: %s", output)
	}
}

// validateUserDefinedDiskOnNode validates user-defined disk configuration on a specific node
func validateUserDefinedDiskOnNode(oc *exutil.CLI, nodeName string, mountPath string) {
	node := extended2.NewNode(oc, nodeName)

	exutil.By(fmt.Sprintf("Checking user-defined disk mount at %s", mountPath))
	output, err := node.DebugNodeWithChroot("mountpoint", mountPath)
	o.Expect(err).NotTo(o.HaveOccurred())
	o.Expect(output).To(o.ContainSubstring("is a mountpoint"), "user-defined disk should be mounted")

	exutil.By("Validating filesystem type is XFS")
	output, err = node.DebugNodeWithChroot("findmnt", "-n", "-o", "FSTYPE", mountPath)
	o.Expect(err).NotTo(o.HaveOccurred())
	o.Expect(strings.TrimSpace(output)).To(o.Equal("xfs"), "user-defined disk should use XFS filesystem")
	framework.Logf("User-defined disk filesystem type: %s", strings.TrimSpace(output))

	// Platform-specific disk device validation
	platformType := getPlatformType(oc)
	if platformType == osconfigv1.VSpherePlatformType {
		exutil.By("Validating vSphere-specific disk device path for user-defined disk")
		// vSphere uses disk paths like: /dev/disk/by-path/pci-0000:03:00.0-scsi-0:0:N:0
		output, err = node.DebugNodeWithChroot("ls", "-la", "/dev/disk/by-path/")
		o.Expect(err).NotTo(o.HaveOccurred())
		framework.Logf("vSphere disk devices: %s", output)

		// Verify the disk device path exists and is used
		output, err = node.DebugNodeWithChroot("findmnt", "-n", "-o", "SOURCE", mountPath)
		o.Expect(err).NotTo(o.HaveOccurred())
		framework.Logf("User-defined disk source device: %s", output)
	} else if platformType == osconfigv1.AzurePlatformType {
		exutil.By("Validating Azure-specific disk device path for user-defined disk")
		// Azure uses LUN-based device mapping
		output, err = node.DebugNodeWithChroot("ls", "-la", "/dev/disk/azure/scsi1/")
		if err == nil {
			framework.Logf("Azure disk devices: %s", output)
		}

		// Verify the disk device path exists and is used
		output, err = node.DebugNodeWithChroot("findmnt", "-n", "-o", "SOURCE", mountPath)
		o.Expect(err).NotTo(o.HaveOccurred())
		framework.Logf("User-defined disk source device: %s", output)
	}

	exutil.By("Validating filesystem and permissions")
	output, err = node.DebugNodeWithChroot("ls", "-la", mountPath)
	o.Expect(err).NotTo(o.HaveOccurred())
	framework.Logf("User-defined disk contents: %s", output)

	exutil.By("Checking filesystem type and usage")
	output, err = node.DebugNodeWithChroot("df", "-T", mountPath)
	o.Expect(err).NotTo(o.HaveOccurred())
	framework.Logf("User-defined disk filesystem info: %s", output)

	exutil.By("Testing write access to user-defined disk")
	testFile := fmt.Sprintf("%s/test-write", mountPath)
	_, err = node.DebugNodeWithChroot("touch", testFile)
	o.Expect(err).NotTo(o.HaveOccurred())

	_, err = node.DebugNodeWithChroot("rm", testFile)
	o.Expect(err).NotTo(o.HaveOccurred())

	exutil.By("Verifying mount persists in fstab")
	output, err = node.DebugNodeWithChroot("cat", "/etc/fstab")
	o.Expect(err).NotTo(o.HaveOccurred())
	o.Expect(output).To(o.ContainSubstring(mountPath), "user-defined disk mount should be in fstab")

	exutil.By("Validating systemd mount unit for user-defined disk")
	// Convert mount path to systemd unit name (e.g., /mnt/data -> mnt-data.mount)
	unitName := strings.TrimPrefix(mountPath, "/")
	unitName = strings.ReplaceAll(unitName, "/", "-")
	unitName = unitName + ".mount"

	// Check that the systemd mount unit exists and is active
	output, err = node.DebugNodeWithChroot("systemctl", "status", unitName)
	o.Expect(err).NotTo(o.HaveOccurred())
	o.Expect(output).To(o.ContainSubstring("active"), "user-defined disk mount unit should be active")
	framework.Logf("User-defined disk mount unit status: %s", output)

	// Verify the mount unit is enabled
	output, err = node.DebugNodeWithChroot("systemctl", "is-enabled", unitName)
	o.Expect(err).NotTo(o.HaveOccurred())
	framework.Logf("User-defined disk mount unit enabled status: %s", output)
}

// Platform-specific helper functions

// getPlatformType returns the platform type for the cluster
func getPlatformType(oc *exutil.CLI) osconfigv1.PlatformType {
	infra, err := oc.AdminConfigClient().ConfigV1().Infrastructures().Get(context.Background(), "cluster", metav1.GetOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())
	return infra.Status.PlatformStatus.Type
}

// getMasterMachineSet gets a master/control-plane machineset for testing
func getMasterMachineSet(oc *exutil.CLI, machineClient *machineclient.Clientset) machinev1beta1.MachineSet {
	// In some platforms, master nodes are managed by ControlPlaneMachineSet
	// For now, we'll use the existing getRandomMachineSet and filter for master nodes
	machineSets, err := machineClient.MachineV1beta1().MachineSets(MAPINamespace).List(context.Background(), metav1.ListOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())
	o.Expect(machineSets.Items).NotTo(o.BeEmpty(), "Should have at least one machineset")

	// Try to find a master machineset, otherwise use the first available
	for _, ms := range machineSets.Items {
		if strings.Contains(strings.ToLower(ms.Name), "master") || strings.Contains(strings.ToLower(ms.Name), "control") {
			return ms
		}
	}

	// Fallback to first machineset
	return machineSets.Items[0]
}

// restoreMachineSet restores a machineset to its original configuration
func restoreMachineSet(oc *exutil.CLI, originalMachineSet *machinev1beta1.MachineSet) {
	framework.Logf("Restoring machineset %s to original configuration", originalMachineSet.Name)

	machineClient, err := machineclient.NewForConfig(oc.KubeFramework().ClientConfig())
	o.Expect(err).NotTo(o.HaveOccurred())

	_, err = machineClient.MachineV1beta1().MachineSets(MAPINamespace).Update(context.Background(), originalMachineSet, metav1.UpdateOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())
}

func waitForCpmsScale(oc *exutil.CLI) {
	machineClient, err := machineclient.NewForConfig(oc.KubeFramework().ClientConfig())
	o.Expect(err).NotTo(o.HaveOccurred())
	cpmsList, err := machineClient.MachineV1().ControlPlaneMachineSets(MAPINamespace).List(context.Background(), metav1.ListOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())

	o.Expect(cpmsList.Items).To(o.HaveLen(1), "Expected exactly one ControlPlaneMachineSet")
	cpms := &cpmsList.Items[0]

	o.Eventually(func() bool {
		cpms, err = machineClient.MachineV1().ControlPlaneMachineSets(MAPINamespace).Get(context.Background(), cpms.Name, metav1.GetOptions{})

		if err != nil {
			framework.Logf("Error getting ControlPlaneMachineSet: %v", err)
			return false
		}
		return cpms.Status.Replicas == cpms.Status.ReadyReplicas
	}, 15*time.Minute, 30*time.Second).Should(o.BeTrue(), "control plane machineset should scale up successfully")

}

// scaleMachineSet scales a machineset with disk setup and waits for new nodes
func scaleMachineSet(oc *exutil.CLI, machineSetName string, replicas int32) ([]string, func()) {
	framework.Logf("Scaling machineset %s to %d replicas with disk setup", machineSetName, replicas)

	machineClient, err := machineclient.NewForConfig(oc.KubeFramework().ClientConfig())
	o.Expect(err).NotTo(o.HaveOccurred())

	// Get current nodes
	nodesBefore := getNodeNames(oc)

	// Scale the machineset
	err = oc.Run("scale").Args(MAPIMachinesetQualifiedName, machineSetName, "-n", MAPINamespace, fmt.Sprintf("--replicas=%d", replicas)).Execute()
	o.Expect(err).NotTo(o.HaveOccurred())

	// Wait for scale-up to complete
	framework.Logf("Waiting for scale-up to complete...")
	o.Eventually(func() bool {
		machineSet, err := machineClient.MachineV1beta1().MachineSets(MAPINamespace).Get(context.Background(), machineSetName, metav1.GetOptions{})
		if err != nil {
			framework.Logf("Error getting machineset: %v", err)
			return false
		}
		return machineSet.Status.AvailableReplicas == replicas
	}, 15*time.Minute, 30*time.Second).Should(o.BeTrue(), "Machineset should scale up successfully")

	// Get new nodes
	nodesAfter := getNodeNames(oc)
	newNodes := findNewNodes(nodesBefore, nodesAfter)

	framework.Logf("Found %d new nodes after scale-up: %v", len(newNodes), newNodes)

	// Return cleanup function
	cleanupFunc := func() {
		framework.Logf("Scaling down machineset %s", machineSetName)
		err := oc.Run("scale").Args(MAPIMachinesetQualifiedName, machineSetName, "-n", MAPINamespace, fmt.Sprintf("--replicas=%d", replicas-1)).Execute()
		o.Expect(err).NotTo(o.HaveOccurred())

		// Wait for scale-down
		o.Eventually(func() bool {
			machineSet, err := machineClient.MachineV1beta1().MachineSets(MAPINamespace).Get(context.Background(), machineSetName, metav1.GetOptions{})
			if err != nil {
				return false
			}
			return machineSet.Status.AvailableReplicas == replicas-1
		}, 10*time.Minute, 30*time.Second).Should(o.BeTrue(), "Machineset should scale down successfully")
	}

	return newNodes, cleanupFunc
}

// getNodeNames gets a list of all node names in the cluster
func getNodeNames(oc *exutil.CLI) []string {
	nodes, err := oc.KubeFramework().ClientSet.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())

	var nodeNames []string
	for _, node := range nodes.Items {
		nodeNames = append(nodeNames, node.Name)
	}
	return nodeNames
}

// findNewNodes finds nodes that are in the 'after' list but not in the 'before' list
func findNewNodes(before, after []string) []string {
	beforeSet := make(map[string]bool)
	for _, node := range before {
		beforeSet[node] = true
	}

	var newNodes []string
	for _, node := range after {
		if !beforeSet[node] {
			newNodes = append(newNodes, node)
		}
	}
	return newNodes
}
