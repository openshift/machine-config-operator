package bootimage

import (
	"testing"

	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/types"

	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	osconfigv1 "github.com/openshift/api/config/v1"
)

// TestCreateNewVMTemplate_NoMatchingFailureDomain verifies that when a MachineSet's
// providerSpec.Workspace doesn't match any vCenter/failure domain in the Infrastructure object,
// createNewVMTemplate skips without error rather than degrading the CO. This covers topology-unaware
// clusters where machinesets span datastores not present in the Infrastructure failure domains.
// The function never reaches getClientsFromServerURL (no real vCenter connectivity needed).
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

	require.NoError(t, err)
	assert.Empty(t, resolvedName)
	assert.False(t, patchRequired)
}

// TestCreateNewVMTemplate_MatchingServerNoMatchingFD verifies that when the providerSpec server
// matches a vCenter entry but the workspace fields don't match any failure domain, we skip
// without error and without attempting vCenter authentication.
func TestCreateNewVMTemplate_MatchingServerNoMatchingFD(t *testing.T) {
	providerSpec := &machinev1beta1.VSphereMachineProviderSpec{
		Workspace: &machinev1beta1.Workspace{
			Server:       "vcenter.example.com",
			Datacenter:   "dc1",
			Datastore:    "unregistered-datastore",
			ResourcePool: "/dc1/host/cluster1/Resources",
		},
	}

	infra := &osconfigv1.Infrastructure{
		Spec: osconfigv1.InfrastructureSpec{
			PlatformSpec: osconfigv1.PlatformSpec{
				VSphere: &osconfigv1.VSpherePlatformSpec{
					VCenters: []osconfigv1.VSpherePlatformVCenterSpec{
						{Server: "vcenter.example.com"},
					},
					FailureDomains: []osconfigv1.VSpherePlatformFailureDomainSpec{
						{
							Server: "vcenter.example.com",
							Topology: osconfigv1.VSpherePlatformTopology{
								Datacenter:   "dc1",
								Datastore:    "registered-datastore",
								ResourcePool: "/dc1/host/cluster1/Resources",
							},
						},
					},
				},
			},
		},
	}

	// credsSc is nil — if getClientsFromServerURL were called it would panic.
	resolvedName, patchRequired, err := createNewVMTemplate(nil, providerSpec, infra, nil, nil, "x86_64", "9.6.20260210-0")

	require.NoError(t, err)
	assert.Empty(t, resolvedName)
	assert.False(t, patchRequired)
}

// TestFindMatchingFailureDomain verifies the failure domain search logic that replaced
// hasMatchingFailureDomain and the inner-loop matching in createNewVMTemplate.
func TestFindMatchingFailureDomain(t *testing.T) {
	providerSpec := &machinev1beta1.VSphereMachineProviderSpec{
		Workspace: &machinev1beta1.Workspace{
			Server:       "vcenter.example.com",
			Datacenter:   "dc1",
			Datastore:    "datastore1",
			ResourcePool: "/dc1/host/cluster1/Resources/pool1",
			VMGroup:      "",
		},
	}

	tests := []struct {
		name      string
		infra     *osconfigv1.Infrastructure
		wantFound bool
		wantName  string
	}{
		{
			name: "exact match",
			infra: &osconfigv1.Infrastructure{
				Spec: osconfigv1.InfrastructureSpec{
					PlatformSpec: osconfigv1.PlatformSpec{
						VSphere: &osconfigv1.VSpherePlatformSpec{
							FailureDomains: []osconfigv1.VSpherePlatformFailureDomainSpec{
								{
									Name:   "zone-a",
									Server: "vcenter.example.com",
									Topology: osconfigv1.VSpherePlatformTopology{
										Datacenter:   "dc1",
										Datastore:    "datastore1",
										ResourcePool: "/dc1/host/cluster1/Resources/pool1",
									},
								},
							},
						},
					},
				},
			},
			wantFound: true,
			wantName:  "zone-a",
		},
		{
			name: "datastore mismatch",
			infra: &osconfigv1.Infrastructure{
				Spec: osconfigv1.InfrastructureSpec{
					PlatformSpec: osconfigv1.PlatformSpec{
						VSphere: &osconfigv1.VSpherePlatformSpec{
							FailureDomains: []osconfigv1.VSpherePlatformFailureDomainSpec{
								{
									Name:   "zone-a",
									Server: "vcenter.example.com",
									Topology: osconfigv1.VSpherePlatformTopology{
										Datacenter:   "dc1",
										Datastore:    "different-datastore",
										ResourcePool: "/dc1/host/cluster1/Resources/pool1",
									},
								},
							},
						},
					},
				},
			},
			wantFound: false,
		},
		{
			name: "empty failure domains",
			infra: &osconfigv1.Infrastructure{
				Spec: osconfigv1.InfrastructureSpec{
					PlatformSpec: osconfigv1.PlatformSpec{
						VSphere: &osconfigv1.VSpherePlatformSpec{},
					},
				},
			},
			wantFound: false,
		},
		{
			name: "nil vsphere spec",
			infra: &osconfigv1.Infrastructure{
				Spec: osconfigv1.InfrastructureSpec{
					PlatformSpec: osconfigv1.PlatformSpec{},
				},
			},
			wantFound: false,
		},
		{
			name: "resource pool path normalization",
			infra: &osconfigv1.Infrastructure{
				Spec: osconfigv1.InfrastructureSpec{
					PlatformSpec: osconfigv1.PlatformSpec{
						VSphere: &osconfigv1.VSpherePlatformSpec{
							FailureDomains: []osconfigv1.VSpherePlatformFailureDomainSpec{
								{
									Name:   "zone-a",
									Server: "vcenter.example.com",
									Topology: osconfigv1.VSpherePlatformTopology{
										Datacenter:   "dc1",
										Datastore:    "datastore1",
										ResourcePool: "/dc1/host/cluster1/Resources/pool1/",
									},
								},
							},
						},
					},
				},
			},
			wantFound: true,
			wantName:  "zone-a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fd, found := findMatchingFailureDomain(providerSpec, tt.infra)
			assert.Equal(t, tt.wantFound, found)
			if tt.wantFound {
				assert.Equal(t, tt.wantName, fd.Name)
			}
		})
	}
}

func TestComputeClusterFromResourcePool(t *testing.T) {
	tests := []struct {
		name    string
		rpPath  string
		want    string
		wantErr bool
	}{
		{
			name:   "default resource pool",
			rpPath: "/dc1/host/cluster1/Resources",
			want:   "/dc1/host/cluster1",
		},
		{
			name:   "named resource pool",
			rpPath: "/dc1/host/cluster1/Resources/pool1",
			want:   "/dc1/host/cluster1",
		},
		{
			name:   "nested resource pool",
			rpPath: "/dc1/host/cluster1/Resources/pool1/subpool",
			want:   "/dc1/host/cluster1",
		},
		{
			name:   "trailing slash cleaned",
			rpPath: "/dc1/host/cluster1/Resources/",
			want:   "/dc1/host/cluster1",
		},
		{
			name:   "cluster name starts with Resources",
			rpPath: "/dc1/host/Resources-cluster/Resources/pool1",
			want:   "/dc1/host/Resources-cluster",
		},
		{
			name:    "no Resources segment",
			rpPath:  "/dc1/host/cluster1",
			wantErr: true,
		},
		{
			name:    "empty path",
			rpPath:  "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := computeClusterFromResourcePool(tt.rpPath)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestBuildSyntheticFailureDomain(t *testing.T) {
	tests := []struct {
		name         string
		providerSpec *machinev1beta1.VSphereMachineProviderSpec
		wantErr      bool
		wantCluster  string
		wantNetwork  string
	}{
		{
			name: "valid providerSpec",
			providerSpec: &machinev1beta1.VSphereMachineProviderSpec{
				Workspace: &machinev1beta1.Workspace{
					Server:       "vcenter.example.com",
					Datacenter:   "dc1",
					Datastore:    "datastore1",
					ResourcePool: "/dc1/host/cluster1/Resources/pool1",
				},
				Network: machinev1beta1.NetworkSpec{
					Devices: []machinev1beta1.NetworkDeviceSpec{
						{NetworkName: "VM Network"},
					},
				},
			},
			wantCluster: "/dc1/host/cluster1",
			wantNetwork: "VM Network",
		},
		{
			name: "no resource pool",
			providerSpec: &machinev1beta1.VSphereMachineProviderSpec{
				Workspace: &machinev1beta1.Workspace{
					Server:     "vcenter.example.com",
					Datacenter: "dc1",
				},
				Network: machinev1beta1.NetworkSpec{
					Devices: []machinev1beta1.NetworkDeviceSpec{
						{NetworkName: "VM Network"},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "no network devices",
			providerSpec: &machinev1beta1.VSphereMachineProviderSpec{
				Workspace: &machinev1beta1.Workspace{
					Server:       "vcenter.example.com",
					Datacenter:   "dc1",
					ResourcePool: "/dc1/host/cluster1/Resources",
				},
			},
			wantErr: true,
		},
		{
			name: "empty network name",
			providerSpec: &machinev1beta1.VSphereMachineProviderSpec{
				Workspace: &machinev1beta1.Workspace{
					Server:       "vcenter.example.com",
					Datacenter:   "dc1",
					ResourcePool: "/dc1/host/cluster1/Resources",
				},
				Network: machinev1beta1.NetworkSpec{
					Devices: []machinev1beta1.NetworkDeviceSpec{
						{NetworkName: ""},
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fd, err := buildSyntheticFailureDomain(tt.providerSpec)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "default", fd.Name)
			assert.Equal(t, tt.providerSpec.Workspace.Server, fd.Server)
			assert.Equal(t, tt.providerSpec.Workspace.Datacenter, fd.Topology.Datacenter)
			assert.Equal(t, tt.wantCluster, fd.Topology.ComputeCluster)
			require.Len(t, fd.Topology.Networks, 1)
			assert.Equal(t, tt.wantNetwork, fd.Topology.Networks[0])
			assert.Equal(t, tt.providerSpec.Workspace.Datastore, fd.Topology.Datastore)
			assert.Equal(t, tt.providerSpec.Workspace.ResourcePool, fd.Topology.ResourcePool)
		})
	}
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

// TestTemplateSearchPath verifies the folder-scoped path used to disambiguate templates that share a
// name across folders (OCPBUGS-105426): a bare-name finder.VirtualMachine search matches anywhere in
// vCenter and errors out with "resolves to multiple vms" if two same-named templates exist in different
// folders, even when one of them unambiguously lives in the workspace folder MCO manages.
func TestTemplateSearchPath(t *testing.T) {
	tests := []struct {
		name   string
		folder string
		vmName string
		want   string
	}{
		{
			name:   "scopes to the workspace folder",
			folder: "/dc1/vm/openshift4-folder",
			vmName: "rhcos-template",
			want:   "/dc1/vm/openshift4-folder/rhcos-template",
		},
		{
			name:   "no folder configured stays unscoped",
			folder: "",
			vmName: "rhcos-template",
			want:   "rhcos-template",
		},
		{
			name:   "template already an absolute inventory path stays unscoped",
			folder: "/dc1/vm/openshift4-folder",
			vmName: "/dc1/vm/customer-folder/rhcos-template",
			want:   "/dc1/vm/customer-folder/rhcos-template",
		},
		{
			name:   "trailing slash on folder does not produce a double slash",
			folder: "/dc1/vm/openshift4-folder/",
			vmName: "rhcos-template",
			want:   "/dc1/vm/openshift4-folder/rhcos-template",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, templateSearchPath(tt.folder, tt.vmName))
		})
	}
}
