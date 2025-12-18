package bootimage

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/nfc"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/ovf"
	"github.com/vmware/govmomi/ovf/importer"
	"github.com/vmware/govmomi/vapi/rest"
	"github.com/vmware/govmomi/vapi/tags"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/coreos/stream-metadata-go/stream"
	osconfigv1 "github.com/openshift/api/config/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	"github.com/openshift/machine-config-operator/pkg/controller/bootimage/cache"
)

type VsphereResources struct {
	folder       *object.Folder
	cluster      *object.ClusterComputeResource
	resourcePool *object.ResourcePool
	networkRef   object.NetworkReference
	datastore    *object.Datastore
	existingVM   *object.VirtualMachine
}

// checkOvaSecureBoot returns true if the OVF descriptor indicates that EFI secure boot is enabled.
// https://github.com/openshift/installer/blob/main/pkg/infrastructure/vsphere/clusterapi/import.go#L46
func checkOvaSecureBoot(ovfEnvelope *ovf.Envelope) bool {
	if ovfEnvelope.VirtualSystem != nil {
		for _, vh := range ovfEnvelope.VirtualSystem.VirtualHardware {
			for _, c := range vh.Config {
				if c.Key == "bootOptions.efiSecureBootEnabled" {
					if c.Value == "true" {
						return true
					}
				}
			}
		}
	}
	return false
}

// debugCorruptOva prints SHA256 checksum and size of a possibly corrupt OVA file
// https://github.com/openshift/installer/blob/main/pkg/infrastructure/vsphere/clusterapi/import.go#L27
func debugCorruptOva(cachedImage string, err error) error {
	// Open the corrupt OVA file
	f, ferr := os.Open(cachedImage)
	if ferr != nil {
		err = fmt.Errorf("%s, %w", err.Error(), ferr)
	}
	defer f.Close()

	// Get a sha256 on the corrupt OVA file
	// and the size of the file
	h := sha256.New()
	written, cerr := io.Copy(h, f)
	if cerr != nil {
		err = fmt.Errorf("%s, %w", err.Error(), cerr)
	}

	return fmt.Errorf("ova %s has a sha256 of %x and a size of %d bytes, failed to read the ovf descriptor %w", cachedImage, h.Sum(nil), written, err)
}

// isDatastoreAvailable checks if the given datastore is accessible from the ESXi host.
// https://github.com/openshift/installer/blob/main/pkg/infrastructure/vsphere/clusterapi/import.go#L252
func isDatastoreAvailable(datastore *object.Datastore, hostDatastoreManagedObjectRefs []types.ManagedObjectReference) bool {
	for _, dsMoRef := range hostDatastoreManagedObjectRefs {
		if dsMoRef.Value == datastore.Reference().Value {
			return true
		}
	}
	return false
}

// isNetworkAvailable determines if the network object is available on the ESXi host.
// https://github.com/openshift/installer/blob/main/pkg/infrastructure/vsphere/clusterapi/import.go#L261
func isNetworkAvailable(networkObjectRef object.NetworkReference, hostNetworkManagedObjectRefs []types.ManagedObjectReference) bool {
	// If the object.NetworkReference is a standard portgroup make
	// sure that it exists on esxi host that the OVA will be imported to.
	if _, ok := networkObjectRef.(*object.Network); ok {
		for _, n := range hostNetworkManagedObjectRefs {
			if n.Value == networkObjectRef.Reference().Value {
				return true
			}
		}
	} else {
		// networkObjectReference is not a standard port group
		// and the other types are distributed so return true
		return true
	}
	return false
}

// findAvailableHostSystems locates an accessible ESXi host in the cluster that has access to the required network and datastore
// https://github.com/openshift/installer/blob/main/pkg/infrastructure/vsphere/clusterapi/import.go#L228
func findAvailableHostSystems(ctx context.Context, clusterHostSystems []*object.HostSystem, networkObjectRef object.NetworkReference, datastore *object.Datastore) (*object.HostSystem, error) {
	var hostSystemManagedObject mo.HostSystem
	for _, hostObj := range clusterHostSystems {
		err := hostObj.Properties(ctx, hostObj.Reference(), []string{"config.product", "network", "datastore", "runtime"}, &hostSystemManagedObject)
		if err != nil {
			return nil, err
		}

		// if distributed port group the cast will fail
		networkFound := isNetworkAvailable(networkObjectRef, hostSystemManagedObject.Network)
		datastoreFound := isDatastoreAvailable(datastore, hostSystemManagedObject.Datastore)

		// if the network or datastore is not found or the ESXi host is in maintenance mode continue the loop
		if !networkFound || !datastoreFound || hostSystemManagedObject.Runtime.InMaintenanceMode {
			continue
		}

		klog.V(2).Infof("using ESXi %s to import the OVA image", hostObj.Name())
		return hostObj, nil
	}
	return nil, errors.New("all hosts unavailable")
}

// attachTag applies a vSphere tag to the virtual machine
// https://github.com/openshift/installer/blob/4b3b91314bdb06c8646e3d4f07d0c9b7f4da7e58/pkg/infrastructure/vsphere/clusterapi/tags.go#L12
func attachTag(ctx context.Context, tagManager *tags.Manager, vmMoRefValue, tagID string) error {
	moRef := types.ManagedObjectReference{
		Value: vmMoRefValue,
		Type:  "VirtualMachine",
	}

	err := tagManager.AttachTag(ctx, tagID, moRef)

	if err != nil {
		return fmt.Errorf("unable to attach tag: %w", err)
	}
	return nil
}

// upload uploads a single file from the OVA archive to the vSphere host using NFC lease.
// Used govc/importx/ovf.go as an example to implement
// resourceVspherePrivateImportOvaCreate and upload functions
// See: https://github.com/vmware/govmomi/blob/cc10a0758d5b4d4873388bcea417251d1ad03e42/govc/importx/ovf.go#L196-L324
func upload(ctx context.Context, archive *importer.TapeArchive, lease *nfc.Lease, item nfc.FileItem) error {
	file := item.Path

	f, size, err := archive.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()

	opts := soap.Upload{
		ContentLength: size,
	}

	return lease.Upload(ctx, item, f, opts)
}

// findAllRequiredResources locates all vSphere objects (folder, cluster, pool, network, etc.)
func findAllRequiredResources(ctx context.Context, finder *find.Finder, providerSpec *machinev1beta1.VSphereMachineProviderSpec, failureDomain osconfigv1.VSpherePlatformFailureDomainSpec, name string) (*VsphereResources, error) {
	vr := VsphereResources{}
	folder, err := finder.Folder(ctx, providerSpec.Workspace.Folder)
	if err != nil {
		return nil, fmt.Errorf("failed to find folder: %w", err)
	}
	vr.folder = folder
	vr.cluster, err = finder.ClusterComputeResource(ctx, failureDomain.Topology.ComputeCluster)
	if err != nil {
		return nil, fmt.Errorf("failed to find compute cluster: %w", err)
	}
	vr.resourcePool, err = finder.ResourcePool(ctx, providerSpec.Workspace.ResourcePool)
	if err != nil {
		return nil, fmt.Errorf("failed to find resource pool: %w", err)
	}
	networkPath := path.Join(vr.cluster.InventoryPath, failureDomain.Topology.Networks[0])
	vr.networkRef, err = finder.Network(ctx, networkPath)
	if err != nil {
		return nil, fmt.Errorf("failed to find network: %w", err)
	}
	vr.datastore, err = finder.Datastore(ctx, providerSpec.Workspace.Datastore)
	if err != nil {
		return nil, fmt.Errorf("failed to find datastore: %w", err)
	}
	vr.existingVM, err = finder.VirtualMachine(ctx, name)
	if err != nil {
		if _, ok := err.(*find.NotFoundError); ok {
			klog.Infof("VM Template with name %s does not already exists", name)
		} else {
			return nil, fmt.Errorf("finder had error: %w", err)
		}
	}
	return &vr, nil
}

// getDiskTypeFromExistingVM inspects the given VM's disk backing configuration and returns its disk provisioning type (thin, thick, eagerZeroedThick).
func getDiskTypeFromExistingVM(vmMo mo.VirtualMachine) string {
	diskType := ""
	for _, device := range vmMo.Config.Hardware.Device {
		if disk, ok := device.(*types.VirtualDisk); ok {
			if backing, ok := disk.Backing.(*types.VirtualDiskFlatVer2BackingInfo); ok {
				if backing.ThinProvisioned != nil {
					if *backing.ThinProvisioned {
						diskType = "thin"
					} else {
						if backing.EagerlyScrub != nil && *backing.EagerlyScrub {
							diskType = "eagerZeroedThick"
						} else {
							diskType = "thick"
						}
					}
				}
			}
		}
	}
	return diskType
}

// getCISP generates an OVF CreateImportSpecParams based on network and disk configuration
func getCISP(ovfEnvelope *ovf.Envelope, networkRef object.NetworkReference, name, diskType string) (types.OvfCreateImportSpecParams, error) {
	// Create mapping between OVF and the network object
	// found by Name
	networkMappings := []types.OvfNetworkMapping{{
		Name:    ovfEnvelope.Network.Networks[0].Name,
		Network: networkRef.Reference(),
	}}

	// This is a very minimal spec for importing an OVF.
	cisp := types.OvfCreateImportSpecParams{
		EntityName:     name,
		NetworkMapping: networkMappings,
	}

	switch diskType {
	case "":
		// Disk provisioning type will be set according to the default storage policy of vsphere.
	case "thin":
		cisp.DiskProvisioning = string(types.OvfCreateImportSpecParamsDiskProvisioningTypeThin)
	case "thick":
		cisp.DiskProvisioning = string(types.OvfCreateImportSpecParamsDiskProvisioningTypeThick)
	case "eagerZeroedThick":
		cisp.DiskProvisioning = string(types.OvfCreateImportSpecParamsDiskProvisioningTypeEagerZeroedThick)
	default:
		return types.OvfCreateImportSpecParams{}, fmt.Errorf("disk provisioning type %q is not supported", diskType)
	}
	return cisp, nil
}

func createNewVMTemplateWithNameForFailureDomain(ctx context.Context, providerSpec *machinev1beta1.VSphereMachineProviderSpec, failureDomain osconfigv1.VSpherePlatformFailureDomainSpec, finder *find.Finder, client *govmomi.Client, tagManager *tags.Manager, name, ovaPath, infraID, diskType string) error {
	vr, err := findAllRequiredResources(ctx, finder, providerSpec, failureDomain, name)
	if err != nil {
		return err
	}

	// OVF Descriptor
	archive := &importer.TapeArchive{Path: ovaPath}
	ovfDescriptor, err := importer.ReadOvf("*.ovf", archive)
	if err != nil {
		return debugCorruptOva(ovaPath, err)
	}

	ovfEnvelope, err := importer.ReadEnvelope(ovfDescriptor)
	if err != nil {
		return fmt.Errorf("failed to parse ovf: %w", err)
	}

	// The fcos ova enables secure boot by default, this causes
	// scos to fail once
	secureBoot := checkOvaSecureBoot(ovfEnvelope)

	// The RHCOS OVA only has one network defined by default
	// The OVF envelope defines this.  We need a 1:1 mapping
	// between networks with the OVF and the host
	if len(ovfEnvelope.Network.Networks) != 1 {
		return fmt.Errorf("expected the OVA to only have a single network adapter")
	}

	clusterHostSystems, err := vr.cluster.Hosts(ctx)
	if err != nil {
		return fmt.Errorf("failed to get cluster hosts: %w", err)
	}
	if len(clusterHostSystems) == 0 {
		return fmt.Errorf("the vCenter cluster %s has no ESXi nodes", failureDomain.Topology.ComputeCluster)
	}

	cisp, err := getCISP(ovfEnvelope, vr.networkRef, name, diskType)
	if err != nil {
		return fmt.Errorf("failed to get CISP: %w", err)
	}

	m := ovf.NewManager(client.Client)
	spec, err := m.CreateImportSpec(ctx,
		string(ovfDescriptor),
		vr.resourcePool.Reference(),
		vr.datastore.Reference(),
		cisp)

	if err != nil {
		return fmt.Errorf("failed to create import spec: %w", err)
	}
	if spec.Error != nil {
		return errors.New(spec.Error[0].LocalizedMessage)
	}

	hostSystem, err := findAvailableHostSystems(ctx, clusterHostSystems, vr.networkRef, vr.datastore)
	if err != nil {
		return fmt.Errorf("failed to find available host system: %w", err)
	}

	if vr.existingVM != nil {
		klog.Infof("VM Template with name %s already exists", name)
		destroyTask, err := vr.existingVM.Destroy(ctx)
		if err != nil {
			return fmt.Errorf("failed to initiate destroy existing VM: %w", err)
		}
		err = destroyTask.Wait(ctx)
		if err != nil {
			return fmt.Errorf("failed to complete destroying existing VM: %w", err)
		}
		klog.Infof("VM %s successfully destroyed", name)
	}

	lease, err := vr.resourcePool.ImportVApp(ctx, spec.ImportSpec, vr.folder, hostSystem)
	if err != nil {
		return fmt.Errorf("failed to import vapp: %w", err)
	}

	info, err := lease.Wait(ctx, spec.FileItem)
	if err != nil {
		return fmt.Errorf("failed to lease wait: %w", err)
	}

	u := lease.StartUpdater(ctx, info)
	defer u.Done()

	for _, i := range info.Items {
		// upload the vmdk to which ever host that was first
		// available with the required network and datastore.
		err = upload(ctx, archive, lease, i)
		if err != nil {
			return fmt.Errorf("failed to upload: %w", err)
		}
	}

	err = lease.Complete(ctx)
	if err != nil {
		return fmt.Errorf("failed to lease complete: %w", err)
	}

	vm := object.NewVirtualMachine(client.Client, info.Entity)
	if vm == nil {
		return fmt.Errorf("error VirtualMachine not found, managed object id: %s", info.Entity.Value)
	}
	if secureBoot {
		bootOptions, err := vm.BootOptions(ctx)
		if err != nil {
			return fmt.Errorf("failed to get boot options: %w", err)
		}
		bootOptions.EfiSecureBootEnabled = ptr.To(false)

		err = vm.SetBootOptions(ctx, bootOptions)
		if err != nil {
			return fmt.Errorf("failed to set boot options: %w", err)
		}
	}

	err = vm.MarkAsTemplate(ctx)
	if err != nil {
		return fmt.Errorf("failed to mark vm as template: %w", err)
	}

	err = attachTag(ctx, tagManager, vm.Reference().Value, infraID)
	if err != nil {
		return fmt.Errorf("failed to attach tag: %w", err)
	}

	for _, tagID := range providerSpec.TagIDs {
		err = attachTag(ctx, tagManager, vm.Reference().Value, tagID)
		if err != nil {
			return fmt.Errorf("failed to attach tag: %w", err)
		}
	}

	return nil
}

// getClientsFromServerURL authenticates to vCenter using SOAP and REST APIs
func getClientsFromServerURL(ctx context.Context, server, username, password string) (*govmomi.Client, *tags.Manager, error) {
	vcenterURL := &url.URL{
		Scheme: "https",
		Host:   server,
		Path:   "/sdk",
		User:   url.UserPassword(username, password),
	}
	client, err := govmomi.NewClient(ctx, vcenterURL, true)
	if err != nil {
		return nil, nil, fmt.Errorf("failed in govmomi.NewClient(%s): %w", vcenterURL, err)
	}

	restClient := rest.NewClient(client.Client)
	err = restClient.Login(ctx, vcenterURL.User)
	if err != nil {
		logoutErr := client.Logout(context.TODO())
		if logoutErr != nil {
			err = logoutErr
		}
		return nil, nil, fmt.Errorf("failed in restClient.Login %w", err)
	}

	tagManager := tags.NewManager(restClient)

	return client, tagManager, nil
}

// Creates a Template VM which has the relevant OVA/OVF file
func createNewVMTemplate(streamData *stream.Stream, providerSpec *machinev1beta1.VSphereMachineProviderSpec, infra *osconfigv1.Infrastructure, credsSc *corev1.Secret, arch, release string) (string, bool, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var name string
	for _, vcenter := range infra.Spec.PlatformSpec.VSphere.VCenters {
		if vcenter.Server != providerSpec.Workspace.Server {
			continue
		}

		username := string(credsSc.Data[fmt.Sprintf("%s.username", vcenter.Server)])
		password := string(credsSc.Data[fmt.Sprintf("%s.password", vcenter.Server)])
		client, tagManager, err := getClientsFromServerURL(ctx, vcenter.Server, username, password)
		if err != nil {
			return "", false, fmt.Errorf("failed in getClientsFromServerURL: %w", err)
		}

		finder := find.NewFinder(client.Client, false)

		for _, failureDomain := range infra.Spec.PlatformSpec.VSphere.FailureDomains {
			// A failure domain is abstract and doesn't exist in vSphere. It represents a logical grouping of infrastructure components used to place and manage virtual machines in a way that ensures high availability and fault tolerance.
			vmGroup := ""
			if failureDomain.ZoneAffinity != nil {
				if failureDomain.ZoneAffinity.HostGroup != nil {
					if failureDomain.ZoneAffinity.HostGroup.VMGroup != "" {
						vmGroup = failureDomain.ZoneAffinity.HostGroup.VMGroup
					}
				}
			}

			// This condition directly follows from https://github.com/openshift/cluster-control-plane-machine-set-operator/blob/main/pkg/machineproviders/providers/openshift/machine/v1beta1/providerconfig/vsphere.go#L194
			if providerSpec.Workspace.Datastore != failureDomain.Topology.Datastore &&
				vcenter.Server != failureDomain.Server &&
				providerSpec.Workspace.VMGroup != vmGroup &&
				path.Clean(providerSpec.Workspace.ResourcePool) == path.Clean(failureDomain.Topology.ResourcePool) {
				continue
			}
			infraID := infra.Status.InfrastructureName

			datacenter, err := finder.Datacenter(ctx, failureDomain.Topology.Datacenter)
			if err != nil {
				return "", false, fmt.Errorf("failed to find datacenter: %w", err)
			}
			finder = finder.SetDatacenter(datacenter)

			name = fmt.Sprintf("%s-rhcos-%s", infraID, failureDomain.Name)

			existingTemplateVM, err := finder.VirtualMachine(ctx, name)
			if err != nil {
				return "", false, fmt.Errorf("finder had error: %w", err)
			}

			var vmMo mo.VirtualMachine
			err = existingTemplateVM.Properties(ctx, existingTemplateVM.Reference(), nil, &vmMo)
			if err != nil {
				return "", false, fmt.Errorf("Unable to extract properties from existing Template VM: %w", err)
			}

			if vmMo.Summary.Config.Product != nil {
				templateProductVersion := vmMo.Summary.Config.Product.Version
				if templateProductVersion == "" {
					return "", false, fmt.Errorf("unable to determine RHCOS version of virtual machine: %s", providerSpec.Template)
				}

				if templateProductVersion != release {
					klog.Infof("Existing RHCOS v%s does not match current RHCOS v%s. Starting reconciliation process.", templateProductVersion, release)

					// Find and download the relevant OVA file
					ova, err := streamData.QueryDisk(arch, "vmware", "ova")
					if err != nil {
						return "", false, err
					}

					ovaPath, err := cache.DownloadOva(ova)
					if err != nil {
						return "", false, fmt.Errorf("Failed to download %s: %w", ova.Location, err)
					}

					if len(name) > 80 {
						return "", false, fmt.Errorf("Length of VM template name `%s` exceeds the permitted limit of 80 characters", name)
					}

					diskType := getDiskTypeFromExistingVM(vmMo)

					err = createNewVMTemplateWithNameForFailureDomain(ctx, providerSpec, failureDomain, finder, client, tagManager, name, ovaPath, infraID, diskType)
					if err != nil {
						return "", false, err
					}
					return name, true, nil
				}

				klog.Infof("Existing RHCOS v%s does match current RHCOS v%s. Skipping reconciliation process using govmomi.", templateProductVersion, release)
				if providerSpec.Template != name {
					klog.Infof("ProviderSpec template name: %s has diverged from the VM Template of name: %s that exists in VSphere. Reconciling the name change.", providerSpec.Template, name)
					return name, true, nil
				}

			} else {
				return "", false, fmt.Errorf("unable to determine RHCOS version of virtual machine: %s", providerSpec.Template)
			}
		}
	}

	return "", false, nil
}
