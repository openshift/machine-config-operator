package bootimage

import (
	"archive/tar"
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/ovf"
	"github.com/vmware/govmomi/ovf/importer"
	"github.com/vmware/govmomi/simulator"
)

// minimalOVFTemplate is a hand-trimmed OVF descriptor (structurally modeled on govmomi's own
// govc/test fixture ovf/fixtures/ttylinux.ovf) referencing a tiny synthetic disk instead of a
// real VMDK. CreateImportSpec only parses this XML text; it never reads the referenced disk
// file, so the disk payload does not need to be a valid VMDK bitstream.
const minimalOVFTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<Envelope xmlns="http://schemas.dmtf.org/ovf/envelope/1" xmlns:ovf="http://schemas.dmtf.org/ovf/envelope/1" xmlns:rasd="http://schemas.dmtf.org/wbem/wscim/1/cim-schema/2/CIM_ResourceAllocationSettingData" xmlns:vssd="http://schemas.dmtf.org/wbem/wscim/1/cim-schema/2/CIM_VirtualSystemSettingData">
  <References>
    <File ovf:href="disk1.vmdk" ovf:id="file1" ovf:size="%d"/>
  </References>
  <DiskSection>
    <Info>Virtual disk information</Info>
    <Disk ovf:capacity="1" ovf:capacityAllocationUnits="byte * 2^20" ovf:diskId="vmdisk1" ovf:fileRef="file1" ovf:format="http://www.vmware.com/interfaces/specifications/vmdk.html#streamOptimized" ovf:populatedSize="%d"/>
  </DiskSection>
  <NetworkSection>
    <Info>The list of logical networks</Info>
    <Network ovf:name="nat">
      <Description>The nat network</Description>
    </Network>
  </NetworkSection>
  <VirtualSystem ovf:id="vm">
    <Info>A virtual machine</Info>
    <Name>mco-test-vm</Name>
    <OperatingSystemSection ovf:id="36">
      <Info>The kind of installed guest operating system</Info>
    </OperatingSystemSection>
    <VirtualHardwareSection>
      <Info>Virtual hardware requirements</Info>
      <System>
        <vssd:ElementName>Virtual Hardware Family</vssd:ElementName>
        <vssd:InstanceID>0</vssd:InstanceID>
        <vssd:VirtualSystemIdentifier>mco-test-vm</vssd:VirtualSystemIdentifier>
        <vssd:VirtualSystemType>vmx-09</vssd:VirtualSystemType>
      </System>
      <Item>
        <rasd:AllocationUnits>hertz * 10^6</rasd:AllocationUnits>
        <rasd:Description>Number of Virtual CPUs</rasd:Description>
        <rasd:ElementName>1 virtual CPU(s)</rasd:ElementName>
        <rasd:InstanceID>1</rasd:InstanceID>
        <rasd:ResourceType>3</rasd:ResourceType>
        <rasd:VirtualQuantity>1</rasd:VirtualQuantity>
      </Item>
      <Item>
        <rasd:AllocationUnits>byte * 2^20</rasd:AllocationUnits>
        <rasd:Description>Memory Size</rasd:Description>
        <rasd:ElementName>32MB of memory</rasd:ElementName>
        <rasd:InstanceID>2</rasd:InstanceID>
        <rasd:ResourceType>4</rasd:ResourceType>
        <rasd:VirtualQuantity>32</rasd:VirtualQuantity>
      </Item>
      <Item>
        <rasd:Address>0</rasd:Address>
        <rasd:Description>IDE Controller</rasd:Description>
        <rasd:ElementName>ideController0</rasd:ElementName>
        <rasd:InstanceID>3</rasd:InstanceID>
        <rasd:ResourceType>5</rasd:ResourceType>
      </Item>
      <Item>
        <rasd:AddressOnParent>0</rasd:AddressOnParent>
        <rasd:ElementName>disk0</rasd:ElementName>
        <rasd:HostResource>ovf:/disk/vmdisk1</rasd:HostResource>
        <rasd:InstanceID>4</rasd:InstanceID>
        <rasd:Parent>3</rasd:Parent>
        <rasd:ResourceType>17</rasd:ResourceType>
      </Item>
      <Item>
        <rasd:AddressOnParent>1</rasd:AddressOnParent>
        <rasd:AutomaticAllocation>true</rasd:AutomaticAllocation>
        <rasd:Connection>nat</rasd:Connection>
        <rasd:Description>E1000 ethernet adapter on &quot;nat&quot;</rasd:Description>
        <rasd:ElementName>ethernet0</rasd:ElementName>
        <rasd:InstanceID>5</rasd:InstanceID>
        <rasd:ResourceSubType>E1000</rasd:ResourceSubType>
        <rasd:ResourceType>10</rasd:ResourceType>
      </Item>
    </VirtualHardwareSection>
  </VirtualSystem>
</Envelope>`

// buildMinimalOVA writes a tiny synthetic .ova (a plain tar of a hand-written .ovf plus a
// dummy disk payload) to a temp dir and returns its path. Used to spike/exercise the OVF
// import flow (CreateImportSpec -> ImportVApp -> NFC lease upload) against vcsim without
// needing a real multi-megabyte RHCOS/ttylinux OVA fixture checked into the repo.
func buildMinimalOVA(t *testing.T) string {
	t.Helper()

	diskContent := []byte("not-a-real-vmdk-just-bytes-for-the-nfc-lease-to-store")
	ovfXML := fmt.Sprintf(minimalOVFTemplate, len(diskContent), len(diskContent))

	dir := t.TempDir()
	ovaPath := filepath.Join(dir, "mco-test.ova")

	f, err := os.Create(ovaPath)
	if err != nil {
		t.Fatalf("failed to create ova file: %v", err)
	}
	defer f.Close()

	tw := tar.NewWriter(f)
	defer tw.Close()

	for name, content := range map[string][]byte{
		"mco-test.ovf": []byte(ovfXML),
		"disk1.vmdk":   diskContent,
	} {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("failed to write tar header for %s: %v", name, err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatalf("failed to write tar content for %s: %v", name, err)
		}
	}

	return ovaPath
}

// TestSpikeVcsimSupportsOvfImport is a throwaway spike (see plan A0) confirming vcsim
// implements enough of CreateImportSpec/ImportVApp/NFC-lease-upload to exercise the same
// import flow as createNewVMTemplateWithNameForFailureDomain. If this test is green, the
// full decision-tree in createNewVMTemplate can be tested end-to-end against vcsim without
// needing a fake-importer seam.
func TestSpikeVcsimSupportsOvfImport(t *testing.T) {
	model := simulator.VPX()
	model.Datacenter = 1
	model.Cluster = 1
	model.Host = 1
	model.Datastore = 1
	if err := model.Create(); err != nil {
		t.Fatalf("failed to create simulator model: %v", err)
	}
	defer model.Remove()

	model.Service.TLS = new(tls.Config)
	server := model.Service.NewServer()
	defer server.Close()

	ctx := context.Background()
	client, err := govmomi.NewClient(ctx, server.URL, true)
	if err != nil {
		t.Fatalf("failed to connect to simulator: %v", err)
	}

	finder := find.NewFinder(client.Client, false)
	dc, err := finder.DefaultDatacenter(ctx)
	if err != nil {
		t.Fatalf("failed to find default datacenter: %v", err)
	}
	finder = finder.SetDatacenter(dc)

	ds, err := finder.DefaultDatastore(ctx)
	if err != nil {
		t.Fatalf("failed to find default datastore: %v", err)
	}
	folders, err := dc.Folders(ctx)
	if err != nil {
		t.Fatalf("failed to find datacenter folders: %v", err)
	}
	cluster, err := finder.DefaultClusterComputeResource(ctx)
	if err != nil {
		t.Fatalf("failed to find default cluster: %v", err)
	}
	pool, err := cluster.ResourcePool(ctx)
	if err != nil {
		t.Fatalf("failed to find cluster resource pool: %v", err)
	}
	hosts, err := cluster.Hosts(ctx)
	if err != nil || len(hosts) == 0 {
		t.Fatalf("failed to find cluster hosts: %v", err)
	}
	networks, err := finder.NetworkList(ctx, "*")
	if err != nil || len(networks) == 0 {
		t.Fatalf("failed to find any network: %v", err)
	}
	network := networks[0]

	ovaPath := buildMinimalOVA(t)
	archive := &importer.TapeArchive{Path: ovaPath}
	ovfDescriptor, err := importer.ReadOvf("*.ovf", archive)
	if err != nil {
		t.Fatalf("failed to read ovf from archive: %v", err)
	}
	ovfEnvelope, err := importer.ReadEnvelope(ovfDescriptor)
	if err != nil {
		t.Fatalf("failed to parse ovf envelope: %v", err)
	}
	if len(ovfEnvelope.Network.Networks) != 1 {
		t.Fatalf("expected exactly one network in test fixture, got %d", len(ovfEnvelope.Network.Networks))
	}

	cisp, err := getCISP(ovfEnvelope, network, "mco-test-import", "")
	if err != nil {
		t.Fatalf("getCISP failed: %v", err)
	}

	m := ovf.NewManager(client.Client)
	spec, err := m.CreateImportSpec(ctx, string(ovfDescriptor), pool.Reference(), ds.Reference(), cisp)
	if err != nil {
		t.Fatalf("CreateImportSpec failed: %v", err)
	}
	if spec.Error != nil {
		t.Fatalf("CreateImportSpec returned spec error: %s", spec.Error[0].LocalizedMessage)
	}

	lease, err := pool.ImportVApp(ctx, spec.ImportSpec, folders.VmFolder, hosts[0])
	if err != nil {
		t.Fatalf("ImportVApp failed: %v", err)
	}

	info, err := lease.Wait(ctx, spec.FileItem)
	if err != nil {
		t.Fatalf("lease.Wait failed: %v", err)
	}

	u := lease.StartUpdater(ctx, info)
	defer u.Done()

	for _, item := range info.Items {
		if err := upload(ctx, archive, lease, item); err != nil {
			t.Fatalf("upload failed: %v", err)
		}
	}

	if err := lease.Complete(ctx); err != nil {
		t.Fatalf("lease.Complete failed: %v", err)
	}

	vm := object.NewVirtualMachine(client.Client, info.Entity)
	if vm == nil {
		t.Fatalf("imported VM not found, managed object id: %s", info.Entity.Value)
	}

	if err := vm.MarkAsTemplate(ctx); err != nil {
		t.Fatalf("MarkAsTemplate failed: %v", err)
	}

	found, err := finder.VirtualMachine(ctx, "mco-test-import")
	if err != nil {
		t.Fatalf("failed to find imported template by name: %v", err)
	}
	if found.Reference().Value != vm.Reference().Value {
		t.Fatalf("found VM does not match imported VM")
	}

	t.Logf("SPIKE RESULT: vcsim fully supports the OVF import flow used by createNewVMTemplateWithNameForFailureDomain")
}
