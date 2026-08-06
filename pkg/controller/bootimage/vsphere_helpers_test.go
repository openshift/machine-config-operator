package bootimage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/ovf"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
	"k8s.io/utils/ptr"

	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	osconfigv1 "github.com/openshift/api/config/v1"
)

// TestCreateNewVMTemplate_NoMatchingFailureDomain verifies that when a MachineSet's
// providerSpec.Workspace doesn't match any vCenter/failure domain in the Infrastructure object,
// createNewVMTemplate returns a descriptive error instead of silently no-op'ing. This is the
// degrade-on-no-match behavior added in "bootimage: degrade when vsphere fd not found" — it never
// reaches getClientsFromServerURL (no real vCenter connectivity needed), since the outer loop over
// infra.Spec.PlatformSpec.VSphere.VCenters has nothing to match against.
func TestCreateNewVMTemplate_NoMatchingFailureDomain(t *testing.T) {
	providerSpec := &machinev1beta1.VSphereMachineProviderSpec{
		Workspace: &machinev1beta1.Workspace{
			Server:       "vcenter.example.com",
			Datacenter:   "dc1",
			Datastore:    "datastore1",
			ResourcePool: "/dc1/host/cluster1/Resources",
		},
	}

	infra := &osconfigv1.Infrastructure{
		Spec: osconfigv1.InfrastructureSpec{
			PlatformSpec: osconfigv1.PlatformSpec{
				VSphere: &osconfigv1.VSpherePlatformSpec{
					// Deliberately empty: no vCenters/failure domains for providerSpec.Workspace
					// to match against.
				},
			},
		},
	}

	resolvedName, patchRequired, err := createNewVMTemplate(nil, providerSpec, infra, nil, nil, "x86_64", "9.6.20260210-0")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match any vCenter/failure domain")
	assert.Contains(t, err.Error(), "vcenter.example.com")
	assert.Empty(t, resolvedName)
	assert.False(t, patchRequired)
}

func newTestVM(inventoryPath string) *object.VirtualMachine {
	vm := object.NewVirtualMachine(nil, types.ManagedObjectReference{})
	vm.InventoryPath = inventoryPath
	return vm
}

func newTestFolder(inventoryPath string) *object.Folder {
	folder := object.NewFolder(nil, types.ManagedObjectReference{})
	folder.InventoryPath = inventoryPath
	return folder
}

// TestIsInFolder verifies the folder-scoping check used to distinguish MCO-managed template VMs
// from customer-managed VMs that merely share a name. govmomi's finder searches by name across the
// entire vCenter inventory, so a name match alone doesn't guarantee the VM lives where MCO expects
// (providerSpec.Workspace.Folder) — only a direct child of that folder counts as MCO-owned.
func TestIsInFolder(t *testing.T) {
	workspaceFolder := newTestFolder("/dc1/vm/openshift4-folder")

	tests := []struct {
		name   string
		vm     *object.VirtualMachine
		folder *object.Folder
		want   bool
	}{
		{
			name:   "direct child of workspace folder",
			vm:     newTestVM("/dc1/vm/openshift4-folder/infra-rhcos-fd1"),
			folder: workspaceFolder,
			want:   true,
		},
		{
			name:   "sibling folder",
			vm:     newTestVM("/dc1/vm/customer-folder/infra-rhcos-fd1"),
			folder: workspaceFolder,
			want:   false,
		},
		{
			name:   "directly under the datacenter's default vm folder",
			vm:     newTestVM("/dc1/vm/infra-rhcos-fd1"),
			folder: workspaceFolder,
			want:   false,
		},
		{
			name:   "nested subfolder beneath the workspace folder",
			vm:     newTestVM("/dc1/vm/openshift4-folder/nested/infra-rhcos-fd1"),
			folder: workspaceFolder,
			want:   false,
		},
		{
			name:   "nil folder (workspace folder unresolved) trusts the match",
			vm:     newTestVM("/dc1/vm/customer-folder/infra-rhcos-fd1"),
			folder: nil,
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isInFolder(tt.vm, tt.folder))
		})
	}
}

func TestCheckOvaSecureBoot(t *testing.T) {
	cases := []struct {
		name     string
		envelope *ovf.Envelope
		want     bool
	}{
		{
			name:     "nil virtual system",
			envelope: &ovf.Envelope{},
			want:     false,
		},
		{
			name: "secure boot enabled",
			envelope: &ovf.Envelope{
				VirtualSystem: &ovf.VirtualSystem{
					VirtualHardware: []ovf.VirtualHardwareSection{
						{Config: []ovf.Config{{Key: "bootOptions.efiSecureBootEnabled", Value: "true"}}},
					},
				},
			},
			want: true,
		},
		{
			name: "secure boot explicitly disabled",
			envelope: &ovf.Envelope{
				VirtualSystem: &ovf.VirtualSystem{
					VirtualHardware: []ovf.VirtualHardwareSection{
						{Config: []ovf.Config{{Key: "bootOptions.efiSecureBootEnabled", Value: "false"}}},
					},
				},
			},
			want: false,
		},
		{
			name: "config key absent",
			envelope: &ovf.Envelope{
				VirtualSystem: &ovf.VirtualSystem{
					VirtualHardware: []ovf.VirtualHardwareSection{
						{Config: []ovf.Config{{Key: "some.other.key", Value: "true"}}},
					},
				},
			},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkOvaSecureBoot(tc.envelope); got != tc.want {
				t.Errorf("checkOvaSecureBoot() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsDatastoreAvailable(t *testing.T) {
	ds := object.NewDatastore(nil, types.ManagedObjectReference{Type: "Datastore", Value: "datastore-1"})
	otherDS := types.ManagedObjectReference{Type: "Datastore", Value: "datastore-2"}

	cases := []struct {
		name  string
		hosts []types.ManagedObjectReference
		want  bool
	}{
		{name: "matching datastore present", hosts: []types.ManagedObjectReference{ds.Reference(), otherDS}, want: true},
		{name: "matching datastore absent", hosts: []types.ManagedObjectReference{otherDS}, want: false},
		{name: "no datastores on host", hosts: nil, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDatastoreAvailable(ds, tc.hosts); got != tc.want {
				t.Errorf("isDatastoreAvailable() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsNetworkAvailable(t *testing.T) {
	standardNet := object.NewNetwork(nil, types.ManagedObjectReference{Type: "Network", Value: "network-1"})
	otherNet := types.ManagedObjectReference{Type: "Network", Value: "network-2"}
	dvpg := object.NewDistributedVirtualPortgroup(nil, types.ManagedObjectReference{Type: "DistributedVirtualPortgroup", Value: "dvportgroup-1"})

	cases := []struct {
		name    string
		network object.NetworkReference
		hosts   []types.ManagedObjectReference
		want    bool
	}{
		{name: "standard portgroup present on host", network: standardNet, hosts: []types.ManagedObjectReference{standardNet.Reference(), otherNet}, want: true},
		{name: "standard portgroup absent from host", network: standardNet, hosts: []types.ManagedObjectReference{otherNet}, want: false},
		{name: "distributed portgroup always available", network: dvpg, hosts: []types.ManagedObjectReference{otherNet}, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNetworkAvailable(tc.network, tc.hosts); got != tc.want {
				t.Errorf("isNetworkAvailable() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGetDiskTypeFromExistingVM(t *testing.T) {
	newVM := func(backing types.BaseVirtualDeviceBackingInfo) mo.VirtualMachine {
		var devices []types.BaseVirtualDevice
		if backing != nil {
			devices = append(devices, &types.VirtualDisk{
				VirtualDevice: types.VirtualDevice{Backing: backing},
			})
		}
		return mo.VirtualMachine{
			Config: &types.VirtualMachineConfigInfo{
				Hardware: types.VirtualHardware{Device: devices},
			},
		}
	}

	cases := []struct {
		name    string
		vm      mo.VirtualMachine
		want    string
	}{
		{
			name: "thin provisioned",
			vm:   newVM(&types.VirtualDiskFlatVer2BackingInfo{ThinProvisioned: ptr.To(true)}),
			want: "thin",
		},
		{
			name: "thick provisioned (lazy zeroed)",
			vm:   newVM(&types.VirtualDiskFlatVer2BackingInfo{ThinProvisioned: ptr.To(false)}),
			want: "thick",
		},
		{
			name: "eager zeroed thick",
			vm:   newVM(&types.VirtualDiskFlatVer2BackingInfo{ThinProvisioned: ptr.To(false), EagerlyScrub: ptr.To(true)}),
			want: "eagerZeroedThick",
		},
		{
			name: "thin provisioned flag unset",
			vm:   newVM(&types.VirtualDiskFlatVer2BackingInfo{}),
			want: "",
		},
		{
			name: "no disk device present",
			vm:   newVM(nil),
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := getDiskTypeFromExistingVM(tc.vm); got != tc.want {
				t.Errorf("getDiskTypeFromExistingVM() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAtomicTempName(t *testing.T) {
	nameA := AtomicTempName("mco-tmp", "cluster-rhcos-fd1")
	nameB := AtomicTempName("mco-tmp", "cluster-rhcos-fd1")
	nameC := AtomicTempName("mco-tmp", "cluster-rhcos-fd2")

	if nameA != nameB {
		t.Errorf("AtomicTempName() is not deterministic: %q != %q", nameA, nameB)
	}
	if nameA == nameC {
		t.Errorf("AtomicTempName() collided for different inputs: %q", nameA)
	}
	if len(nameA) != 16 {
		t.Errorf("AtomicTempName() length = %d, want 16", len(nameA))
	}
	if !strings.HasPrefix(nameA, "mco-tmp-") {
		t.Errorf("AtomicTempName() = %q, want prefix %q", nameA, "mco-tmp-")
	}
}

func TestGetCISP(t *testing.T) {
	envelope := &ovf.Envelope{
		Network: &ovf.NetworkSection{Networks: []ovf.Network{{Name: "nat"}}},
	}
	networkRef := object.NewNetwork(nil, types.ManagedObjectReference{Type: "Network", Value: "network-1"})

	cases := []struct {
		name         string
		diskType     string
		wantErr      bool
		wantProvType string
	}{
		{name: "unspecified disk type uses vsphere default policy", diskType: "", wantProvType: ""},
		{name: "thin", diskType: "thin", wantProvType: string(types.OvfCreateImportSpecParamsDiskProvisioningTypeThin)},
		{name: "thick", diskType: "thick", wantProvType: string(types.OvfCreateImportSpecParamsDiskProvisioningTypeThick)},
		{name: "eagerZeroedThick", diskType: "eagerZeroedThick", wantProvType: string(types.OvfCreateImportSpecParamsDiskProvisioningTypeEagerZeroedThick)},
		{name: "invalid disk type", diskType: "bogus", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cisp, err := getCISP(envelope, networkRef, "my-template", tc.diskType)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("getCISP() expected an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("getCISP() unexpected error: %v", err)
			}
			if cisp.EntityName != "my-template" {
				t.Errorf("cisp.EntityName = %q, want %q", cisp.EntityName, "my-template")
			}
			if cisp.DiskProvisioning != tc.wantProvType {
				t.Errorf("cisp.DiskProvisioning = %q, want %q", cisp.DiskProvisioning, tc.wantProvType)
			}
			if len(cisp.NetworkMapping) != 1 || cisp.NetworkMapping[0].Name != "nat" {
				t.Errorf("cisp.NetworkMapping = %+v, want a single mapping for %q", cisp.NetworkMapping, "nat")
			}
			if cisp.NetworkMapping[0].Network != networkRef.Reference() {
				t.Errorf("cisp.NetworkMapping[0].Network = %+v, want %+v", cisp.NetworkMapping[0].Network, networkRef.Reference())
			}
		})
	}
}

func TestDebugCorruptOva(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corrupt.ova")
	content := []byte("not a real ova")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	origErr := "failed to parse ovf descriptor"
	err := debugCorruptOva(path, errUnwrappable(origErr))
	if err == nil {
		t.Fatalf("debugCorruptOva() returned nil error")
	}

	msg := err.Error()
	if !strings.Contains(msg, origErr) {
		t.Errorf("debugCorruptOva() error %q does not contain original error %q", msg, origErr)
	}
	if !strings.Contains(msg, path) {
		t.Errorf("debugCorruptOva() error %q does not contain file path %q", msg, path)
	}
	if !strings.Contains(msg, "sha256") {
		t.Errorf("debugCorruptOva() error %q does not mention sha256", msg)
	}
	if !strings.Contains(msg, "size of 14 bytes") {
		t.Errorf("debugCorruptOva() error %q does not report the correct file size", msg)
	}
}

type errUnwrappable string

func (e errUnwrappable) Error() string { return string(e) }
