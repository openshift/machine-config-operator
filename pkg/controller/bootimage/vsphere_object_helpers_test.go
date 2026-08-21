package bootimage

import (
	"context"
	"fmt"
	"path"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vapi/tags"
	"github.com/vmware/govmomi/vim25/types"

	osconfigv1 "github.com/openshift/api/config/v1"
)

var testTagCounter atomic.Int64

// createTestVM builds a real named, templated VM directly (folder.CreateVM + MarkAsTemplate),
// without going through the OVA import flow. Deliberately lighter-weight than driving
// createNewVMTemplateWithNameForFailureDomain: that function always stages the new VM under a
// deterministic "mco-tmp-<hash>" name derived from the *final* name before renaming it, and
// vcsim's Rename does not relocate a VM's underlying datastore folder - so a fixture built that
// way would permanently occupy the "mco-tmp-<hash>" folder for this name, and any later real
// import driven by the code under test (which reuses that same deterministic tempName) would
// fail with FileAlreadyExists. Building the fixture directly under its final name sidesteps that
// vcsim fidelity gap entirely.
func createTestVM(t *testing.T, vc *simulatedVCenter, fd osconfigv1.VSpherePlatformFailureDomainSpec, name string) *object.VirtualMachine {
	t.Helper()
	vc.activate()
	ctx := context.Background()

	ps := buildVSphereProviderSpec(vc, fd, name)
	vr, err := findAllRequiredResources(ctx, vc.Finder, ps, fd, nil, name)
	if err != nil {
		t.Fatalf("createTestVM: findAllRequiredResources failed: %v", err)
	}
	if vr.existingVM != nil {
		t.Fatalf("createTestVM: a VM named %q already exists", name)
	}

	spec := types.VirtualMachineConfigSpec{
		Name:    name,
		GuestId: string(types.VirtualMachineGuestOsIdentifierOtherGuest),
		Files:   &types.VirtualMachineFileInfo{VmPathName: fmt.Sprintf("[%s]", path.Base(vc.DatastorePath))},
	}
	task, err := vr.folder.CreateVM(ctx, spec, vr.resourcePool, nil)
	if err != nil {
		t.Fatalf("createTestVM: CreateVM failed: %v", err)
	}
	info, err := task.WaitForResult(ctx)
	if err != nil {
		t.Fatalf("createTestVM: CreateVM task failed: %v", err)
	}
	vmRef, ok := info.Result.(types.ManagedObjectReference)
	if !ok {
		t.Fatalf("createTestVM: unexpected CreateVM task result type %T", info.Result)
	}
	newVM := object.NewVirtualMachine(vc.Client.Client, vmRef)
	if err := newVM.MarkAsTemplate(ctx); err != nil {
		t.Fatalf("createTestVM: MarkAsTemplate failed: %v", err)
	}

	vm, err := vc.Finder.VirtualMachine(ctx, name)
	if err != nil {
		t.Fatalf("createTestVM: failed to find newly created fixture VM %q: %v", name, err)
	}
	return vm
}

// createTestTag creates a tag category and a single tag within it, returning the tag's ID.
func createTestTag(t *testing.T, vc *simulatedVCenter) string {
	t.Helper()
	ctx := context.Background()

	n := testTagCounter.Add(1)
	categoryID, err := vc.TagManager.CreateCategory(ctx, &tags.Category{
		Name:            fmt.Sprintf("mco-test-category-%d", n),
		Cardinality:     "SINGLE",
		AssociableTypes: []string{"VirtualMachine"},
	})
	if err != nil {
		t.Fatalf("createTestTag: failed to create category: %v", err)
	}

	tagID, err := vc.TagManager.CreateTag(ctx, &tags.Tag{
		Name:       fmt.Sprintf("mco-test-tag-%d", n),
		CategoryID: categoryID,
	})
	if err != nil {
		t.Fatalf("createTestTag: failed to create tag: %v", err)
	}
	return tagID
}

// createInfraTag creates a category+tag mirroring what openshift-installer sets up at cluster
// install time (installer/pkg/infrastructure/vsphere/clusterapi/tags.go:createClusterTagID):
// a category named "openshift-<infraName>" containing a tag whose NAME (not ID) is exactly
// infraName. createNewVMTemplateWithNameForFailureDomain's attachTag call passes
// infra.Status.InfrastructureName straight through as govmomi's tagID argument; govmomi's
// vapi/tags.Manager treats any non-"urn:"-prefixed string as a name and transparently resolves
// it to the real tag ID via a name lookup (see tags.isName/tagID), so this - not a raw ID -
// is the fixture that actually matches production/installer behavior.
func createInfraTag(t *testing.T, vc *simulatedVCenter, infraName string) {
	t.Helper()
	ctx := context.Background()

	categoryID, err := vc.TagManager.CreateCategory(ctx, &tags.Category{
		Name:            fmt.Sprintf("openshift-%s", infraName),
		Cardinality:     "SINGLE",
		AssociableTypes: []string{"VirtualMachine"},
	})
	if err != nil {
		t.Fatalf("createInfraTag: failed to create category: %v", err)
	}
	if _, err := vc.TagManager.CreateTag(ctx, &tags.Tag{
		Name:       infraName,
		CategoryID: categoryID,
	}); err != nil {
		t.Fatalf("createInfraTag: failed to create tag: %v", err)
	}
}

func TestGetClientsFromServerURL(t *testing.T) {
	vc := newSimulatedVCenter(t)
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		client, tagManager, err := getClientsFromServerURL(ctx, vc.Server, vc.Username, vc.Password)
		if err != nil {
			t.Fatalf("getClientsFromServerURL() unexpected error: %v", err)
		}
		if client == nil || tagManager == nil {
			t.Fatalf("getClientsFromServerURL() returned nil client or tagManager")
		}
		defer client.Logout(ctx)
	})

	t.Run("unreachable host", func(t *testing.T) {
		_, _, err := getClientsFromServerURL(ctx, "127.0.0.1:1", vc.Username, vc.Password)
		if err == nil {
			t.Fatalf("getClientsFromServerURL() expected an error for an unreachable host")
		}
	})
}

func TestFindAllRequiredResources(t *testing.T) {
	vc := newSimulatedVCenter(t)
	ctx := context.Background()
	fd := buildFailureDomain(vc, "fd0")

	t.Run("happy path, no existing VM", func(t *testing.T) {
		ps := buildVSphereProviderSpec(vc, fd, "does-not-exist-yet")
		vr, err := findAllRequiredResources(ctx, vc.Finder, ps, fd, nil, "does-not-exist-yet")
		if err != nil {
			t.Fatalf("findAllRequiredResources() unexpected error: %v", err)
		}
		if vr.folder == nil || vr.cluster == nil || vr.resourcePool == nil || vr.networkRef == nil || vr.datastore == nil {
			t.Fatalf("findAllRequiredResources() left a required resource nil: %+v", vr)
		}
		if vr.existingVM != nil {
			t.Fatalf("findAllRequiredResources() found an existingVM that should not exist")
		}
	})

	t.Run("existing VM is resolved", func(t *testing.T) {
		name := "existing-template"
		createTestVM(t, vc, fd, name)

		ps := buildVSphereProviderSpec(vc, fd, name)
		vr, err := findAllRequiredResources(ctx, vc.Finder, ps, fd, nil, name)
		if err != nil {
			t.Fatalf("findAllRequiredResources() unexpected error: %v", err)
		}
		if vr.existingVM == nil {
			t.Fatalf("findAllRequiredResources() did not find the existing VM")
		}
	})

	t.Run("bad folder", func(t *testing.T) {
		ps := buildVSphereProviderSpec(vc, fd, "x")
		ps.Workspace.Folder = "/DC0/vm/does-not-exist"
		if _, err := findAllRequiredResources(ctx, vc.Finder, ps, fd, nil, "x"); err == nil || !strings.Contains(err.Error(), "failed to find folder") {
			t.Fatalf("findAllRequiredResources() expected a folder-not-found error, got: %v", err)
		}
	})

	t.Run("bad cluster", func(t *testing.T) {
		ps := buildVSphereProviderSpec(vc, fd, "x")
		badFD := fd
		badFD.Topology.ComputeCluster = "/DC0/host/does-not-exist"
		if _, err := findAllRequiredResources(ctx, vc.Finder, ps, badFD, nil, "x"); err == nil || !strings.Contains(err.Error(), "failed to find compute cluster") {
			t.Fatalf("findAllRequiredResources() expected a cluster-not-found error, got: %v", err)
		}
	})

	t.Run("bad resource pool", func(t *testing.T) {
		ps := buildVSphereProviderSpec(vc, fd, "x")
		ps.Workspace.ResourcePool = "/DC0/host/DC0_C0/Resources/does-not-exist"
		if _, err := findAllRequiredResources(ctx, vc.Finder, ps, fd, nil, "x"); err == nil || !strings.Contains(err.Error(), "failed to find resource pool") {
			t.Fatalf("findAllRequiredResources() expected a resource-pool-not-found error, got: %v", err)
		}
	})

	t.Run("bad network", func(t *testing.T) {
		ps := buildVSphereProviderSpec(vc, fd, "x")
		badFD := fd
		badFD.Topology.Networks = []string{"does-not-exist"}
		if _, err := findAllRequiredResources(ctx, vc.Finder, ps, badFD, nil, "x"); err == nil || !strings.Contains(err.Error(), "failed to find network") {
			t.Fatalf("findAllRequiredResources() expected a network-not-found error, got: %v", err)
		}
	})

	t.Run("bad datastore", func(t *testing.T) {
		ps := buildVSphereProviderSpec(vc, fd, "x")
		ps.Workspace.Datastore = "/DC0/datastore/does-not-exist"
		if _, err := findAllRequiredResources(ctx, vc.Finder, ps, fd, nil, "x"); err == nil || !strings.Contains(err.Error(), "failed to find datastore") {
			t.Fatalf("findAllRequiredResources() expected a datastore-not-found error, got: %v", err)
		}
	})
}

func TestFindAvailableHostSystems(t *testing.T) {
	vc := newSimulatedVCenter(t)
	ctx := context.Background()
	fd := buildFailureDomain(vc, "fd0")
	ps := buildVSphereProviderSpec(vc, fd, "x")

	vr, err := findAllRequiredResources(ctx, vc.Finder, ps, fd, nil, "x")
	if err != nil {
		t.Fatalf("setup: findAllRequiredResources failed: %v", err)
	}
	hosts, err := vr.cluster.Hosts(ctx)
	if err != nil || len(hosts) == 0 {
		t.Fatalf("setup: failed to list cluster hosts: %v", err)
	}

	t.Run("a healthy host with network+datastore is selected", func(t *testing.T) {
		host, err := findAvailableHostSystems(ctx, hosts, vr.networkRef, vr.datastore)
		if err != nil {
			t.Fatalf("findAvailableHostSystems() unexpected error: %v", err)
		}
		if host == nil {
			t.Fatalf("findAvailableHostSystems() returned a nil host")
		}
	})

	t.Run("all hosts unavailable", func(t *testing.T) {
		unavailableRef := vr.networkRef.Reference()
		unavailableRef.Value = "does-not-exist"
		bogusNetwork := object.NewNetwork(vc.Client.Client, unavailableRef)

		if _, err := findAvailableHostSystems(ctx, hosts, bogusNetwork, vr.datastore); err == nil {
			t.Fatalf("findAvailableHostSystems() expected an error when no host has the required network")
		}
	})

	t.Run("host in maintenance mode is skipped", func(t *testing.T) {
		hostObj := vc.Registry.Get(hosts[0].Reference()).(*simulator.HostSystem)
		hostObj.Runtime.InMaintenanceMode = true
		t.Cleanup(func() { hostObj.Runtime.InMaintenanceMode = false })

		host, err := findAvailableHostSystems(ctx, hosts[:1], vr.networkRef, vr.datastore)
		if err == nil {
			t.Fatalf("findAvailableHostSystems() expected an error, the only host is in maintenance mode, got host %v", host)
		}
	})
}

func TestDestroyVMIfPresent(t *testing.T) {
	vc := newSimulatedVCenter(t)
	ctx := context.Background()
	fd := buildFailureDomain(vc, "fd0")

	t.Run("no-op when VM does not exist", func(t *testing.T) {
		if err := destroyVMIfPresent(ctx, vc.Finder, "does-not-exist"); err != nil {
			t.Fatalf("destroyVMIfPresent() unexpected error for absent VM: %v", err)
		}
	})

	t.Run("destroys an existing VM", func(t *testing.T) {
		name := "to-be-destroyed"
		createTestVM(t, vc, fd, name)

		if err := destroyVMIfPresent(ctx, vc.Finder, name); err != nil {
			t.Fatalf("destroyVMIfPresent() unexpected error: %v", err)
		}
		if _, err := vc.Finder.VirtualMachine(ctx, name); err == nil {
			t.Fatalf("destroyVMIfPresent() did not actually destroy the VM")
		}
	})
}

func TestSwapTemplate(t *testing.T) {
	vc := newSimulatedVCenter(t)
	ctx := context.Background()
	fd := buildFailureDomain(vc, "fd0")

	prodName := "prod-template"
	existingVM := createTestVM(t, vc, fd, prodName)
	newVM := createTestVM(t, vc, fd, "new-incoming-template")
	oldTempName := AtomicTempName("mco-old", prodName)

	if err := swapTemplate(ctx, existingVM, newVM, prodName, oldTempName); err != nil {
		t.Fatalf("swapTemplate() unexpected error: %v", err)
	}

	found, err := vc.Finder.VirtualMachine(ctx, prodName)
	if err != nil {
		t.Fatalf("swapTemplate() left no VM named %q: %v", prodName, err)
	}
	if found.Reference().Value != newVM.Reference().Value {
		t.Fatalf("swapTemplate() left the wrong VM under %q", prodName)
	}
	if _, err := vc.Finder.VirtualMachine(ctx, oldTempName); err == nil {
		t.Fatalf("swapTemplate() left the old VM behind under %q instead of destroying it", oldTempName)
	}
}

func TestAttachTag(t *testing.T) {
	vc := newSimulatedVCenter(t)
	ctx := context.Background()
	fd := buildFailureDomain(vc, "fd0")
	vm := createTestVM(t, vc, fd, "tag-target")
	tagID := createTestTag(t, vc)

	t.Run("success", func(t *testing.T) {
		if err := attachTag(ctx, vc.TagManager, vm.Reference().Value, tagID); err != nil {
			t.Fatalf("attachTag() unexpected error: %v", err)
		}
	})

	t.Run("invalid tag ID", func(t *testing.T) {
		if err := attachTag(ctx, vc.TagManager, vm.Reference().Value, "bogus-tag-id"); err == nil {
			t.Fatalf("attachTag() expected an error for an invalid tag ID")
		}
	})
}
