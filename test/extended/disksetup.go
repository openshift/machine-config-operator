package extended

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	machineclient "github.com/openshift/client-go/machine/clientset/versioned"
	machineconfigclient "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	e2eskipper "k8s.io/kubernetes/test/e2e/framework/skipper"
)

const (
	// diskSetupMCPrefix is the name prefix the installer gives every MachineConfig it
	// generates from a machine pool's diskSetup stanza.
	diskSetupMCPrefix = "01-disk-setup-"

	// diskSetupPartlabelPrefix is the by-partlabel symlink directory that the generated
	// filesystem and systemd unit both address the partition through.
	diskSetupPartlabelPrefix = "/dev/disk/by-partlabel/"

	// azureLunDevicePrefix is the device path the installer partitions on Azure; the data
	// disk's LUN is appended to it.
	azureLunDevicePrefix = "/dev/disk/azure/scsi1/lun"

	// prjquotaMountOption is required on data disks so the kubelet can enforce project
	// quotas on the mounted filesystem.
	prjquotaMountOption = "prjquota"

	// machineAnnotation links a Node to the Machine that provisioned it.
	machineAnnotation = "machine.openshift.io/machine"

	// gibiByte converts the API's diskSizeGB into the byte count lsblk reports.
	gibiByte = 1024 * 1024 * 1024

	// diskSizeTolerance is the fraction of the requested size the observed block device is
	// allowed to fall short by, since the partition table costs some addressable space.
	diskSizeTolerance = 0.98

	// diskStateTimeout bounds how long to wait for a node's disk to reach the expected
	// state. The disks are set up by Ignition on first boot, so they are normally ready
	// well before these tests run; this only absorbs a slow or restarting debug pod.
	diskStateTimeout = 2 * time.Minute

	// diskStateInterval is how often to retry collecting a node's disk state. Each attempt
	// starts a debug pod, so this is deliberately coarse.
	diskStateInterval = 20 * time.Second
)

// azureExpectedDisk describes one Azure data disk that the install-config under test is
// expected to have configured. The lun and storageAccountType fields are Azure's, and the
// assertions that read them live in assertAzureDataDisk; another platform would need its own
// fixture and its own provider-spec check, but can reuse everything else in this file. These values are the contract between this test and the CI step that
// generates the install-config
// (ci-operator/step-registry/ipi/conf/azure/multidisk in openshift/release).
type azureExpectedDisk struct {
	role               string
	mountPath          string
	lun                int32
	sizeGB             int32
	storageAccountType string
}

// azureExpectedDisks is the disk layout this suite asserts: two extra disks on the control plane
// and two on the compute nodes. etcd disk setup is only valid on the control plane, so the
// compute pool carries two user-defined disks instead.
var azureExpectedDisks = []azureExpectedDisk{
	{role: "master", mountPath: "/var/lib/etcd", lun: 0, sizeGB: 64, storageAccountType: "Premium_LRS"},
	{role: "master", mountPath: "/var/lib/containers", lun: 1, sizeGB: 32, storageAccountType: "StandardSSD_LRS"},
	{role: "worker", mountPath: "/var/lib/containers", lun: 0, sizeGB: 32, storageAccountType: "Premium_LRS"},
	{role: "worker", mountPath: "/var/lib/kubelet", lun: 1, sizeGB: 16, storageAccountType: "StandardSSD_LRS"},
}

// ignitionDiskConfig is a minimal view of the Ignition config the installer embeds in a
// disk-setup MachineConfig. It decodes only the fields asserted on here so that it stays
// compatible across Ignition spec revisions.
type ignitionDiskConfig struct {
	Storage struct {
		Disks []struct {
			Device     string `json:"device"`
			Partitions []struct {
				Label *string `json:"label"`
			} `json:"partitions"`
		} `json:"disks"`
		Filesystems []struct {
			Device       string   `json:"device"`
			Format       *string  `json:"format"`
			MountOptions []string `json:"mountOptions"`
			Path         *string  `json:"path"`
		} `json:"filesystems"`
	} `json:"storage"`
	Systemd struct {
		Units []struct {
			Name    string `json:"name"`
			Enabled *bool  `json:"enabled"`
		} `json:"units"`
	} `json:"systemd"`
}

// findmntResult decodes `findmnt --json`.
type findmntResult struct {
	Filesystems []struct {
		Source  string `json:"source"`
		FSType  string `json:"fstype"`
		Options string `json:"options"`
	} `json:"filesystems"`
}

// diskSetup is a disk-setup MachineConfig decoded into the facts the assertions need.
type diskSetup struct {
	mcName string
	role   string
	// partLabel is the GPT partition label applied to the data disk. Note that this is not
	// the platformDiskID: the installer uses the disk setup's type as the label for etcd and
	// swap disks, and strips every non-alphanumeric character from it. Use the LUN, not this,
	// to pair a disk setup with its Azure data disk.
	partLabel     string
	backingDevice string
	format        string
	mountPath     string
	mountOptions  []string
	unitName      string
}

func (d diskSetup) partitionDevice() string {
	return diskSetupPartlabelPrefix + d.partLabel
}

// azureLUN extracts the Azure LUN the disk is attached at from the partitioned device path. The
// LUN is unique per virtual machine and is what identifies the data disk, so it is the
// reliable way to pair a disk setup with its entry in the Machine provider spec.
func (d diskSetup) azureLUN() (int32, error) {
	if !strings.HasPrefix(d.backingDevice, azureLunDevicePrefix) {
		return 0, fmt.Errorf("device %q is not an Azure LUN device", d.backingDevice)
	}
	lun, err := strconv.ParseInt(strings.TrimPrefix(d.backingDevice, azureLunDevicePrefix), 10, 32)
	if err != nil {
		return 0, fmt.Errorf("device %q has a malformed LUN: %w", d.backingDevice, err)
	}
	return int32(lun), nil
}

// getDiskSetups returns every disk-setup MachineConfig in the cluster, decoded.
func getDiskSetups(ctx context.Context, client *machineconfigclient.Clientset) ([]diskSetup, []string) {
	mcList, err := client.MachineconfigurationV1().MachineConfigs().List(ctx, metav1.ListOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "Error listing MachineConfigs.")

	setups := []diskSetup{}
	problems := []string{}
	for _, mc := range mcList.Items {
		if !strings.HasPrefix(mc.Name, diskSetupMCPrefix) {
			continue
		}
		setup, err := decodeDiskSetup(mc)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		setups = append(setups, setup)
	}
	return setups, problems
}

// decodeDiskSetup parses a disk-setup MachineConfig into a diskSetup. It returns an error
// rather than failing the spec, so that a cluster carrying a disk setup this suite does not
// describe is skipped by the layout check rather than failed here.
func decodeDiskSetup(mc mcfgv1.MachineConfig) (diskSetup, error) {
	ign := ignitionDiskConfig{}
	if err := json.Unmarshal(mc.Spec.Config.Raw, &ign); err != nil {
		return diskSetup{}, fmt.Errorf("decoding the Ignition config of MachineConfig %q: %w", mc.Name, err)
	}

	if len(ign.Storage.Disks) != 1 || len(ign.Storage.Disks[0].Partitions) != 1 ||
		len(ign.Storage.Filesystems) != 1 || len(ign.Systemd.Units) != 1 {
		return diskSetup{}, fmt.Errorf("MachineConfig %q does not describe exactly one disk, partition, filesystem and unit", mc.Name)
	}

	partition := ign.Storage.Disks[0].Partitions[0]
	if partition.Label == nil {
		return diskSetup{}, fmt.Errorf("MachineConfig %q does not label its partition", mc.Name)
	}

	filesystem := ign.Storage.Filesystems[0]
	if filesystem.Format == nil {
		return diskSetup{}, fmt.Errorf("MachineConfig %q does not set a filesystem format", mc.Name)
	}

	// An enabled unit is what actually activates the disk; a disabled one would leave the
	// partition formatted but never mounted.
	unit := ign.Systemd.Units[0]
	if unit.Enabled == nil || !*unit.Enabled {
		return diskSetup{}, fmt.Errorf("MachineConfig %q does not enable its systemd unit", mc.Name)
	}

	setup := diskSetup{
		mcName:        mc.Name,
		role:          mc.Labels["machineconfiguration.openshift.io/role"],
		partLabel:     *partition.Label,
		backingDevice: ign.Storage.Disks[0].Device,
		format:        *filesystem.Format,
		mountOptions:  filesystem.MountOptions,
		unitName:      ign.Systemd.Units[0].Name,
	}
	if filesystem.Path != nil {
		setup.mountPath = *filesystem.Path
	}
	return setup, nil
}

// indexDiskSetups keys the disk setups by role and mount path, failing on duplicates so
// that a malformed layout is reported rather than silently resolved to the first match.
func indexDiskSetups(setups []diskSetup) (map[string]diskSetup, []string) {
	indexed := map[string]diskSetup{}
	problems := []string{}
	for _, setup := range setups {
		key := setup.role + " " + setup.mountPath
		if _, duplicate := indexed[key]; duplicate {
			problems = append(problems, fmt.Sprintf("more than one disk setup mounting %s on the %s pool", setup.mountPath, setup.role))
			continue
		}
		indexed[key] = setup
	}
	return indexed, problems
}

// diskSetupFor returns the disk setup for a role and mount path, failing when it is absent.
func diskSetupFor(indexed map[string]diskSetup, role, mountPath string) diskSetup {
	setup, ok := indexed[role+" "+mountPath]
	if !ok {
		g.Fail(fmt.Sprintf("Expected a disk setup mounting %s on the %s pool.", mountPath, role))
	}
	return setup
}

// getNodesWithRole returns the nodes carrying the given role, failing when there are none.
func getNodesWithRole(ctx context.Context, oc *exutil.CLI, role string) []corev1.Node {
	names := exutil.GetNodeListByLabel(oc, "node-role.kubernetes.io/"+role)
	o.Expect(names).NotTo(o.BeEmpty(), fmt.Sprintf("Expected at least one node with role %q.", role))

	nodes := make([]corev1.Node, 0, len(names))
	for _, name := range names {
		node, err := oc.AsAdmin().KubeClient().CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("Error getting node %q.", name))
		nodes = append(nodes, *node)
	}
	return nodes
}

// nodeDiskState is everything one node reports about a single data disk, gathered in a
// single debug pod so that a check costs one pod rather than one per command.
type nodeDiskState struct {
	resolvedPartition     string
	partitionFSType       string
	mountSource           string
	mountFSType           string
	mountOptions          string
	resolvedBackingDevice string
	partitionParent       string
}

// collectNodeDiskState gathers a node's view of one data disk in one debug pod.
//
// Paths are passed to the shell as positional parameters rather than interpolated into the
// script, so that a mount path containing a space or a shell metacharacter cannot change
// what runs.
func collectNodeDiskState(oc *exutil.CLI, nodeName, partDevice, mountPath, backingDevice string) (nodeDiskState, error) {
	const sep = "---8<---"
	// Every command's output is captured rather than relied on to succeed: the script always
	// exits 0 so that a failure in any one of them is diagnosed here from the sections it did
	// produce, instead of collapsing into an opaque non-zero exit from the debug container.
	//
	// The root filesystem is deliberately not consulted, for two reasons.
	//
	// It cannot be, reliably: on RHCOS / is a composefs overlay, so findmnt reports its
	// source as the literal string "composefs" rather than a device, and passing that to
	// lsblk or readlink fails.
	//
	// It does not need to be. The separation of the data disk from the OS disk is guaranteed
	// by which device is partitioned, not by comparing against /. Azure's udev rules give the
	// OS disk /dev/disk/azure/root, the ephemeral disk /dev/disk/azure/resource, and data
	// disks /dev/disk/azure/scsi1/lun<N>. The installer partitions the LUN path, so a
	// partition whose parent is that device is on a data disk by construction and cannot be
	// on the OS disk. Note that the kernel device names carry no such guarantee: in the run
	// that motivated this, lun1 resolved to /dev/sda.
	//
	// If a future change needs the root device after all, /sysroot is the real block-device
	// mount on RHCOS and is what to compare against; / is not.
	script := fmt.Sprintf(`
readlink -e "$1"; echo '%[1]s'
blkid -s TYPE -o value "$1"; echo '%[1]s'
findmnt --json --output SOURCE,FSTYPE,OPTIONS --mountpoint "$2"; echo '%[1]s'
readlink -e "$3"; echo '%[1]s'
lsblk --nodeps --noheadings --output PKNAME "$1"
exit 0
`, sep)

	out, err := exutil.DebugNodeWithOptionsAndChroot(oc, nodeName, []string{},
		"sh", "-c", script, "disk-setup-check", partDevice, mountPath, backingDevice)
	if err != nil {
		return nodeDiskState{}, fmt.Errorf("running the disk inspection script on node %q: %w", nodeName, err)
	}

	sections := strings.Split(out, sep)
	if len(sections) != 5 {
		return nodeDiskState{}, fmt.Errorf("expected 5 sections from node %q, got %d in: %s", nodeName, len(sections), out)
	}

	state := nodeDiskState{
		resolvedPartition:     strings.TrimSpace(sections[0]),
		partitionFSType:       strings.TrimSpace(sections[1]),
		resolvedBackingDevice: strings.TrimSpace(sections[3]),
	}

	// The MCO debug helper joins stderr onto stdout, so take only the first line, which is
	// the command's own output.
	state.partitionParent = wholeDevice(firstLine(sections[4]), "")

	mounts := findmntResult{}
	if trimmed := strings.TrimSpace(sections[2]); trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &mounts); err != nil {
			return nodeDiskState{}, fmt.Errorf("decoding findmnt output for %s on node %q: %w", mountPath, nodeName, err)
		}
	}
	if len(mounts.Filesystems) == 0 {
		return nodeDiskState{}, fmt.Errorf("%s is not a mount point on node %q", mountPath, nodeName)
	}
	// findmnt reports a bind mount as "<device>[<subpath>]". That suffix is deliberately not
	// stripped: the disk setup mounts the partition directly, so a bind mount here is not
	// what was asked for and should fail the comparison below.
	state.mountSource = mounts.Filesystems[0].Source
	state.mountFSType = mounts.Filesystems[0].FSType
	state.mountOptions = mounts.Filesystems[0].Options

	return state, nil
}

// wholeDevice turns an lsblk PKNAME answer into a device path. lsblk reports no parent for
// a device that is already top-level, and the debug helper mixes stderr into the output, so
// anything that is not a bare device name falls back to the supplied device.
func wholeDevice(pkname, fallback string) string {
	if pkname != "" && !strings.ContainsAny(pkname, " \t:/") {
		return "/dev/" + pkname
	}
	return fallback
}

// firstLine returns the first non-empty line of s, trimmed.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// assertDiskIsMountedAt asserts the full chain for one data disk on every node of its pool:
// the partition exists under its label, it holds an xfs filesystem, that filesystem is
// mounted at the expected path, and the partition was carved out of the Azure data disk the
// MachineConfig targeted rather than out of the disk holding the root filesystem.
func assertDiskIsMountedAt(ctx context.Context, oc *exutil.CLI, setup diskSetup, mountPath string) {
	o.Expect(setup.format).To(o.Equal("xfs"),
		fmt.Sprintf("MachineConfig %q should format its data disk as xfs.", setup.mcName))
	o.Expect(setup.mountOptions).To(o.ContainElement(prjquotaMountOption),
		fmt.Sprintf("MachineConfig %q should request the %s mount option.", setup.mcName, prjquotaMountOption))

	expectedUnit := strings.ReplaceAll(strings.Trim(mountPath, "/"), "/", "-") + ".mount"
	o.Expect(setup.unitName).To(o.Equal(expectedUnit),
		fmt.Sprintf("MachineConfig %q should enable the systemd mount unit for %s.", setup.mcName, mountPath))

	for _, node := range getNodesWithRole(ctx, oc, setup.role) {
		nodeName := node.Name
		logger.Infof("Checking %s on node %s", mountPath, nodeName)

		o.Eventually(func() error {
			state, err := collectNodeDiskState(oc, nodeName, setup.partitionDevice(), mountPath, setup.backingDevice)
			if err != nil {
				return err
			}

			if !strings.HasPrefix(state.resolvedPartition, "/dev/") {
				return fmt.Errorf("partition %q should resolve to a block device, got %q",
					setup.partitionDevice(), state.resolvedPartition)
			}
			if state.partitionFSType != "xfs" {
				return fmt.Errorf("partition %q should hold an xfs filesystem, got %q",
					setup.partitionDevice(), state.partitionFSType)
			}
			if state.mountSource != state.resolvedPartition {
				return fmt.Errorf("%s should be backed by the %q data disk at %q, got %q",
					mountPath, setup.partLabel, state.resolvedPartition, state.mountSource)
			}
			if state.resolvedBackingDevice == "" {
				return fmt.Errorf("data disk %q should resolve to a block device", setup.backingDevice)
			}
			if state.partitionParent == "" {
				return fmt.Errorf("partition %q should report the device it was carved out of", state.resolvedPartition)
			}
			if state.partitionParent != state.resolvedBackingDevice {
				return fmt.Errorf("%s should be backed by a partition of %q (%q), but its partition %q lives on %q",
					mountPath, setup.backingDevice, state.resolvedBackingDevice, state.resolvedPartition, state.partitionParent)
			}
			if state.mountFSType != "xfs" {
				return fmt.Errorf("%s should be an xfs filesystem, got %q", mountPath, state.mountFSType)
			}
			if !strings.Contains(","+state.mountOptions+",", ","+prjquotaMountOption+",") {
				return fmt.Errorf("%s should be mounted with %s, got %q", mountPath, prjquotaMountOption, state.mountOptions)
			}
			return nil
		}, diskStateTimeout, diskStateInterval).Should(o.Succeed(),
			fmt.Sprintf("Data disk %q was not set up at %s on node %q.", setup.partLabel, mountPath, nodeName))
	}
}

// azureDataDisksByNode returns the data disks declared in the Machine provider spec of every
// machine with the given role, keyed by node name.
func azureDataDisksByNode(ctx context.Context, oc *exutil.CLI, client *machineclient.Clientset, role string) map[string][]machinev1beta1.DataDisk {
	byNode := map[string][]machinev1beta1.DataDisk{}

	for _, node := range getNodesWithRole(ctx, oc, role) {
		annotation, ok := node.Annotations[machineAnnotation]
		o.Expect(ok).To(o.BeTrue(), fmt.Sprintf("Node %q should carry the %s annotation.", node.Name, machineAnnotation))

		namespace, name, found := strings.Cut(annotation, "/")
		o.Expect(found).To(o.BeTrue(),
			fmt.Sprintf("The %s annotation on node %q should be namespace/name, got %q.", machineAnnotation, node.Name, annotation))

		machine, err := client.MachineV1beta1().Machines(namespace).Get(ctx, name, metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("Error getting Machine %q for node %q.", annotation, node.Name))
		o.Expect(machine.Spec.ProviderSpec.Value).NotTo(o.BeNil(),
			fmt.Sprintf("Machine %q should have a provider spec.", annotation))

		providerSpec := machinev1beta1.AzureMachineProviderSpec{}
		err = json.Unmarshal(machine.Spec.ProviderSpec.Value.Raw, &providerSpec)
		o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("Error decoding the Azure provider spec of Machine %q.", annotation))

		byNode[node.Name] = providerSpec.DataDisks
	}

	return byNode
}

// assertAzureDataDisk checks the Azure-specific properties of one data disk against the
// Machine provider spec of every machine in its pool, and returns the LUN it was matched on.
//
// This is where the platform-specific knowledge lives: the LUN encoded in the device path the
// MachineConfig partitions, and the size and storage account type recorded in the provider
// spec. Everything else in this file works from the MachineConfig and the node, and would
// apply unchanged to another platform.
func assertAzureDataDisk(expected azureExpectedDisk, setup diskSetup, dataDisksByNode map[string][]machinev1beta1.DataDisk) int32 {
	lun, err := setup.azureLUN()
	o.Expect(err).NotTo(o.HaveOccurred(),
		fmt.Sprintf("MachineConfig %q should partition an Azure LUN device.", setup.mcName))
	o.Expect(lun).To(o.Equal(expected.lun),
		fmt.Sprintf("MachineConfig %q should partition the LUN the install-config requested for %s.", setup.mcName, expected.mountPath))

	// Pair the disk setup with its data disk by LUN. The partition label cannot be used: the
	// installer labels etcd and swap partitions with the disk setup's type and strips
	// non-alphanumeric characters, so it does not generally equal the data disk's nameSuffix.
	for node, dataDisks := range dataDisksByNode {
		var matched *machinev1beta1.DataDisk
		luns := []int32{}
		for i := range dataDisks {
			luns = append(luns, dataDisks[i].Lun)
			if dataDisks[i].Lun == lun {
				matched = &dataDisks[i]
			}
		}
		o.Expect(matched).NotTo(o.BeNil(),
			fmt.Sprintf("Machine for node %q should declare a data disk at LUN %d, found LUNs %v.", node, lun, luns))

		o.Expect(matched.DiskSizeGB).To(o.Equal(expected.sizeGB),
			fmt.Sprintf("Data disk at LUN %d on node %q should be %d GB.", lun, node, expected.sizeGB))
		o.Expect(string(matched.ManagedDisk.StorageAccountType)).To(o.Equal(expected.storageAccountType),
			fmt.Sprintf("Data disk at LUN %d on node %q should use storage account type %q.", lun, node, expected.storageAccountType))
	}

	return lun
}

// These tests assert that the data disks described by a machine pool's diskSetup stanza were
// attached, partitioned, formatted and mounted on the nodes, and that the Azure-specific
// knobs in the install-config reached the Machine provider spec.
//
// They only read cluster and node state. Like any test built on exutil.NewCLI they get a
// project of their own for the duration of each spec, and inspecting a node starts a debug
// pod in it, but nothing outside that project is changed. That makes them safe to run in the
// parallel conformance suite.
//
// They run only on a cluster installed with the layout in azureExpectedDisks and skip otherwise,
// because other Azure jobs configure a different one.
var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/parallel][OCPFeatureGate:AzureMultiDisk][OCPFeatureGate:MultiDiskSetup] Azure machine pool disk setup",
	g.Label("Platform:azure"), func() {
		defer g.GinkgoRecover()

		var (
			oc            = exutil.NewCLI("mco-disk-setup", exutil.KubeConfigPath()).AsAdmin()
			mcClientSet   *machineconfigclient.Clientset
			machineClient *machineclient.Clientset
			setups        map[string]diskSetup
		)

		g.BeforeEach(func(ctx context.Context) {
			var err error
			mcClientSet, err = machineconfigclient.NewForConfig(oc.KubeFramework().ClientConfig())
			o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the machineconfiguration client.")

			machineClient, err = machineclient.NewForConfig(oc.KubeFramework().ClientConfig())
			o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the machine client.")

			decoded, problems := getDiskSetups(ctx, mcClientSet)
			logger.Infof("Found %d disk-setup MachineConfig(s) in the cluster.", len(decoded))

			// Disk setup is opt-in, so most Azure clusters have none, and a job that does
			// configure it may use a layout other than the one asserted here. Neither is
			// what this suite describes, so run only when the whole expected layout is
			// present rather than failing a job that describes something else.
			var duplicates []string
			setups, duplicates = indexDiskSetups(decoded)
			problems = append(problems, duplicates...)

			wanted := map[string]bool{}
			for _, expected := range azureExpectedDisks {
				wanted[expected.role+" "+expected.mountPath] = true
			}

			missing := []string{}
			for key := range wanted {
				if _, ok := setups[key]; !ok {
					missing = append(missing, key)
				}
			}
			extra := []string{}
			for key := range setups {
				if !wanted[key] {
					extra = append(extra, key)
				}
			}
			sort.Strings(missing)
			sort.Strings(extra)

			// A layout that is missing one of these disks, or that carries one these tests
			// say nothing about, is a different fixture. Note the consequence: a drift
			// between azureExpectedDisks and the job's install-config is silent here. It is not
			// silent overall, since these tests then stop reporting runs at all.
			if len(missing) > 0 || len(extra) > 0 || len(problems) > 0 {
				e2eskipper.Skipf("Skipping these tests since the cluster was not installed with the disk layout they describe. Missing: %v. Unexpected: %v. Undecodable: %v.",
					missing, extra, problems)
			}
		})

		g.It("should render the MachineConfig of every configured disk setup into its MachineConfigPool [apigroup:machineconfiguration.openshift.io]", func(ctx context.Context) {
			pools, err := mcClientSet.MachineconfigurationV1().MachineConfigPools().List(ctx, metav1.ListOptions{})
			o.Expect(err).NotTo(o.HaveOccurred(), "Error listing MachineConfigPools.")

			for _, expected := range azureExpectedDisks {
				setup := diskSetupFor(setups, expected.role, expected.mountPath)

				var pool *mcfgv1.MachineConfigPool
				for i := range pools.Items {
					if pools.Items[i].Name == setup.role {
						pool = &pools.Items[i]
						break
					}
				}
				o.Expect(pool).NotTo(o.BeNil(), fmt.Sprintf("Expected a MachineConfigPool named %q.", setup.role))

				sourced := false
				for _, source := range pool.Status.Configuration.Source {
					if source.Name == setup.mcName {
						sourced = true
						break
					}
				}
				o.Expect(sourced).To(o.BeTrue(),
					fmt.Sprintf("MachineConfig %q should be a source of the rendered config of pool %q.", setup.mcName, pool.Name))
			}
		})

		g.It("should provision, partition and mount an etcd data disk on control plane nodes [apigroup:machineconfiguration.openshift.io]", func(ctx context.Context) {
			assertDiskIsMountedAt(ctx, oc, diskSetupFor(setups, "master", "/var/lib/etcd"), "/var/lib/etcd")
		})

		g.It("should provision, partition and mount a user-defined data disk on control plane nodes [apigroup:machineconfiguration.openshift.io]", func(ctx context.Context) {
			assertDiskIsMountedAt(ctx, oc, diskSetupFor(setups, "master", "/var/lib/containers"), "/var/lib/containers")
		})

		g.It("should provision, partition and mount a user-defined data disk on compute nodes [apigroup:machineconfiguration.openshift.io]", func(ctx context.Context) {
			assertDiskIsMountedAt(ctx, oc, diskSetupFor(setups, "worker", "/var/lib/containers"), "/var/lib/containers")
		})

		g.It("should provision, partition and mount a second user-defined data disk on compute nodes [apigroup:machineconfiguration.openshift.io]", func(ctx context.Context) {
			assertDiskIsMountedAt(ctx, oc, diskSetupFor(setups, "worker", "/var/lib/kubelet"), "/var/lib/kubelet")
		})

		g.It("should attach every data disk with the configured storage account type, size and LUN [apigroup:machineconfiguration.openshift.io]", func(ctx context.Context) {
			// Machine lookups are per role, so do them once rather than once per disk.
			dataDisksByRole := map[string]map[string][]machinev1beta1.DataDisk{}
			for _, expected := range azureExpectedDisks {
				if _, done := dataDisksByRole[expected.role]; !done {
					dataDisksByRole[expected.role] = azureDataDisksByNode(ctx, oc, machineClient, expected.role)
				}
			}

			for _, expected := range azureExpectedDisks {
				setup := diskSetupFor(setups, expected.role, expected.mountPath)
				assertAzureDataDisk(expected, setup, dataDisksByRole[expected.role])

				// The size the install-config asked for should also be what the kernel sees,
				// which proves the disk Azure attached is the one that was requested.
				minBytes := int64(float64(int64(expected.sizeGB)*gibiByte) * diskSizeTolerance)
				for _, node := range getNodesWithRole(ctx, oc, expected.role) {
					out, err := exutil.DebugNodeWithOptionsAndChroot(oc, node.Name, []string{},
						"lsblk", "--bytes", "--nodeps", "--noheadings", "--output", "SIZE", setup.backingDevice)
					o.Expect(err).NotTo(o.HaveOccurred(),
						fmt.Sprintf("Error reading the size of %q on node %q.", setup.backingDevice, node.Name))

					observed, err := strconv.ParseInt(firstLine(out), 10, 64)
					o.Expect(err).NotTo(o.HaveOccurred(),
						fmt.Sprintf("lsblk should report a byte count for %q on node %q, got %q.", setup.backingDevice, node.Name, out))
					o.Expect(observed).To(o.BeNumerically(">=", minBytes),
						fmt.Sprintf("Device %q on node %q should be at least %d bytes for a %d GB data disk, got %d.",
							setup.backingDevice, node.Name, minBytes, expected.sizeGB, observed))
				}
			}
		})
	})
