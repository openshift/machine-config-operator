package util

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/tidwall/gjson"

	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/ovf/importer"
	"github.com/vmware/govmomi/vim25/mo"
)

// DownloadOVAIfURL downloads an OVA file from a URL if the path is a URL,
// otherwise returns the path as-is. The caller is responsible for cleaning
// up the temporary file if the returned path is different from the input.
func DownloadOVAIfURL(ovaPath string) (string, error) {
	// If it's not a URL, return the path as-is
	if !strings.HasPrefix(ovaPath, "http://") && !strings.HasPrefix(ovaPath, "https://") {
		return ovaPath, nil
	}

	// Download OVA from URL
	logger.Infof("Downloading OVA from %s", ovaPath)
	tmpFile, err := os.CreateTemp("", "rhcos-*.ova")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary file: %w", err)
	}
	defer tmpFile.Close()

	resp, err := http.Get(ovaPath)
	if err != nil {
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("failed to download OVA: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("failed to download OVA: HTTP status %d", resp.StatusCode)
	}

	logger.Infof("Content-Length: %d bytes", resp.ContentLength)
	bytesWritten, err := io.Copy(tmpFile, resp.Body)
	if err != nil {
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("failed to save OVA: %w", err)
	}

	logger.Infof("Downloaded %d bytes", bytesWritten)

	// Verify the file was fully written
	if resp.ContentLength > 0 && bytesWritten != resp.ContentLength {
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("incomplete download: expected %d bytes, got %d bytes", resp.ContentLength, bytesWritten)
	}

	// Ensure data is flushed to disk
	err = tmpFile.Sync()
	if err != nil {
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("failed to sync file: %w", err)
	}

	localPath := tmpFile.Name()
	logger.Infof("OVA downloaded to %s", localPath)

	return localPath, nil
}

// UploadBaseImageToVsphere uploads a base image OVA to vSphere and converts it to a template.
// The baseImageSrc can be either a local file path or a URL.
func UploadBaseImageToVsphere(baseImageSrc, baseImageDest string, vsInfo *VSphereConnectionInfo, folder string) error {
	ctx := context.Background()

	// Build vSphere URL without credentials
	u, err := url.Parse(fmt.Sprintf("https://%s/sdk", vsInfo.Server))
	if err != nil {
		return fmt.Errorf("failed to parse vSphere URL for server %s", vsInfo.Server)
	}

	// Set credentials separately
	u.User = url.UserPassword(vsInfo.User, vsInfo.Password)

	logger.Infof("Uploading base image %s to vsphere with name %s", baseImageSrc, baseImageDest)

	// Connect to vSphere
	c, err := govmomi.NewClient(ctx, u, true)
	if err != nil {
		return fmt.Errorf("failed to connect to vSphere: %w", err)
	}
	defer c.Logout(ctx)

	// Create finder
	finder := find.NewFinder(c.Client, true)

	// Find datacenter
	dc, err := finder.Datacenter(ctx, vsInfo.DataCenter)
	if err != nil {
		return fmt.Errorf("failed to find datacenter %s: %w", vsInfo.DataCenter, err)
	}
	finder.SetDatacenter(dc)

	// Find datastore
	ds, err := finder.Datastore(ctx, vsInfo.DataStore)
	if err != nil {
		return fmt.Errorf("failed to find datastore %s: %w", vsInfo.DataStore, err)
	}

	// Find resource pool
	pool, err := finder.ResourcePool(ctx, vsInfo.ResourcePool)
	if err != nil {
		return fmt.Errorf("failed to find resource pool %s: %w", vsInfo.ResourcePool, err)
	}

	// Find VM folder
	var vmFolder *object.Folder
	if folder != "" {
		vmFolder, err = finder.Folder(ctx, folder)
		if err != nil {
			return fmt.Errorf("failed to find folder %s: %w", folder, err)
		}
		logger.Infof("Using workspace folder %s", folder)
	} else {
		folders, err := dc.Folders(ctx)
		if err != nil {
			return fmt.Errorf("failed to get datacenter folders: %w", err)
		}
		vmFolder = folders.VmFolder
		logger.Infof("Using datacenter root VM folder")
	}

	// Check if VM already exists
	var vm *object.VirtualMachine
	existingVM, err := finder.VirtualMachine(ctx, baseImageDest)
	if err == nil {
		logger.Infof("Image %s already exists in the cloud, we don't upload it again", baseImageDest)
		vm = existingVM
	} else {
		// Download OVA if it's a URL
		localOvaPath, err := DownloadOVAIfURL(baseImageSrc)
		if err != nil {
			return err
		}
		if localOvaPath != baseImageSrc {
			defer os.Remove(localOvaPath)
		}

		// Create archive from OVA file (OVA is a TAR archive)
		archive := importer.TapeArchive{Path: localOvaPath}
		archive.Client = c.Client

		// The OVA contains network adapter definitions that require a valid
		// vSphere network during import.
		// We map the OVF networks to the network from the failure domain topology.
		var networkMapping []importer.Network
		ovfDescriptor, err := importer.ReadOvf("*.ovf", &archive)
		if err != nil {
			return fmt.Errorf("failed to read OVF from OVA: %w", err)
		}
		ovfEnvelope, err := importer.ReadEnvelope(ovfDescriptor)
		if err != nil {
			return fmt.Errorf("failed to parse OVF envelope: %w", err)
		}
		if ovfEnvelope.Network != nil && len(ovfEnvelope.Network.Networks) > 0 {
			net, err := finder.Network(ctx, vsInfo.Network)
			if err != nil {
				return fmt.Errorf("failed to find network %s from failure domain: %w", vsInfo.Network, err)
			}
			netRef := net.Reference().String()
			logger.Infof("Mapping OVF networks to failure domain network %s (%s)", vsInfo.Network, netRef)
			for _, n := range ovfEnvelope.Network.Networks {
				networkMapping = append(networkMapping, importer.Network{
					Name:    n.Name,
					Network: netRef,
				})
			}
		}

		// Setup importer
		imp := importer.Importer{
			Client:       c.Client,
			Finder:       finder,
			Datacenter:   dc,
			Datastore:    ds,
			ResourcePool: pool,
			Folder:       vmFolder,
			Log: func(s string) (int, error) {
				logger.Infof("%s", s)
				return len(s), nil
			},
		}

		// Import options
		opts := importer.Options{
			Name:           &baseImageDest,
			NetworkMapping: networkMapping,
		}

		// Set archive
		imp.Archive = &archive

		// Import the OVA - use wildcard pattern to find the OVF file inside the TAR
		logger.Infof("Importing OVA %s as VM %s", localOvaPath, baseImageDest)
		vmRef, err := imp.Import(ctx, "*.ovf", opts)
		if err != nil {
			return fmt.Errorf("failed to import OVA: %w", err)
		}

		// Get the VM object
		vm = object.NewVirtualMachine(c.Client, *vmRef)
	}

	// Best-effort operations: upgrade hardware and mark as template
	// These operations are attempted regardless of whether the VM was just imported or already existed
	// Errors in these operations won't fail the test
	logger.Infof("Attempting hardware upgrade and template conversion (best-effort, won't fail on errors)")

	// Upgrade VM hardware
	logger.Infof("Upgrading VM's hardware")
	task, err := vm.UpgradeVM(ctx, "")
	if err != nil {
		logger.Warnf("ERROR UPGRADING HARDWARE: %s", err)
	} else {
		err = task.Wait(ctx)
		if err != nil {
			logger.Warnf("ERROR UPGRADING HARDWARE: %s", err)
		}
	}

	// Mark as template
	logger.Infof("Transforming VM into template")
	err = vm.MarkAsTemplate(ctx)
	if err != nil {
		logger.Warnf("ERROR CONVERTING INTO TEMPLATE: %s", err)
	}

	return nil
}

// GetReleaseFromVsphereTemplate gets the release version from a vSphere template
func GetReleaseFromVsphereTemplate(vsphereTemplate, server, dataCenter, user, password string) (string, error) {
	ctx := context.Background()

	// Build vSphere URL without credentials
	u, err := url.Parse(fmt.Sprintf("https://%s/sdk", server))
	if err != nil {
		return "", fmt.Errorf("failed to parse vSphere URL for server %s", server)
	}

	// Set credentials separately
	u.User = url.UserPassword(user, password)

	logger.Infof("Getting information about vsphere template %s", vsphereTemplate)

	// Connect to vSphere
	c, err := govmomi.NewClient(ctx, u, true)
	if err != nil {
		return "", fmt.Errorf("failed to connect to vSphere: %w", err)
	}
	defer c.Logout(ctx)

	// Create finder
	finder := find.NewFinder(c.Client, true)

	// Find datacenter
	dc, err := finder.Datacenter(ctx, dataCenter)
	if err != nil {
		return "", fmt.Errorf("failed to find datacenter %s: %w", dataCenter, err)
	}
	finder.SetDatacenter(dc)

	// Find the VM/template
	vm, err := finder.VirtualMachine(ctx, vsphereTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to find VM/template %s: %w", vsphereTemplate, err)
	}

	// Get VM properties
	var moVM mo.VirtualMachine
	err = vm.Properties(ctx, vm.Reference(), []string{"summary.config.product"}, &moVM)
	if err != nil {
		return "", fmt.Errorf("failed to get VM properties: %w", err)
	}

	if moVM.Summary.Config.Product == nil || moVM.Summary.Config.Product.Version == "" {
		return "", fmt.Errorf("cannot get version from VM %s", vsphereTemplate)
	}

	version := moVM.Summary.Config.Product.Version
	logger.Infof("Version for vm %s: %s", vsphereTemplate, version)
	return version, nil
}

// VSphereConnectionInfo holds vSphere connection parameters extracted from the cluster
type VSphereConnectionInfo struct {
	Server       string
	DataCenter   string
	DataStore    string
	ResourcePool string
	Network      string
	User         string
	Password     string
}

// GetVSphereConnectionInfoFromFailureDomain builds a VSphereConnectionInfo from a failure domain JSON string
func GetVSphereConnectionInfoFromFailureDomain(oc *CLI, failureDomain string) (*VSphereConnectionInfo, error) {
	if failureDomain == "" {
		return nil, fmt.Errorf("empty failure domain")
	}

	info := &VSphereConnectionInfo{
		Server:       gjson.Get(failureDomain, "server").String(),
		DataCenter:   gjson.Get(failureDomain, "topology.datacenter").String(),
		DataStore:    gjson.Get(failureDomain, "topology.datastore").String(),
		ResourcePool: gjson.Get(failureDomain, "topology.resourcePool").String(),
		Network:      gjson.Get(failureDomain, "topology.networks.0").String(),
	}

	if info.Server == "" || info.DataCenter == "" || info.DataStore == "" || info.ResourcePool == "" || info.Network == "" {
		return nil, fmt.Errorf("incomplete failure domain: server=%s datacenter=%s datastore=%s resourcePool=%s network=%s",
			info.Server, info.DataCenter, info.DataStore, info.ResourcePool, info.Network)
	}

	secretData, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("secret", "vsphere-creds", "-n", "kube-system", "-o", "jsonpath={.data}").Output()
	if err != nil {
		return nil, err
	}

	dataMap := map[string]string{}
	if err := json.Unmarshal([]byte(secretData), &dataMap); err != nil {
		return nil, err
	}

	// The secret keys are formatted as "<server>.username" and "<server>.password"
	userKey := info.Server + ".username"
	passKey := info.Server + ".password"

	userB64, ok := dataMap[userKey]
	if !ok {
		return nil, fmt.Errorf("vsphere credentials key %s not found in vsphere-creds secret", userKey)
	}
	userBytes, err := base64.StdEncoding.DecodeString(userB64)
	if err != nil {
		return nil, fmt.Errorf("cannot decode secret value for key %s: %w", userKey, err)
	}
	info.User = string(userBytes)

	passB64, ok := dataMap[passKey]
	if !ok {
		return nil, fmt.Errorf("vsphere credentials key %s not found in vsphere-creds secret", passKey)
	}
	passBytes, err := base64.StdEncoding.DecodeString(passB64)
	if err != nil {
		return nil, fmt.Errorf("cannot decode secret value for key %s: %w", passKey, err)
	}
	info.Password = string(passBytes)

	return info, nil
}

// GetVSphereConnectionInfo extracts vSphere connection parameters from the first failure domain
// in the infrastructure resource and the credentials secret
func GetVSphereConnectionInfo(oc *CLI) (*VSphereConnectionInfo, error) {
	failureDomain, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("infrastructure", "cluster", "-o", "jsonpath={.spec.platformSpec.vsphere.failureDomains[0]}").Output()
	if err != nil {
		return nil, fmt.Errorf("cannot get the failureDomain from the infrastructure resource: %w", err)
	}

	return GetVSphereConnectionInfoFromFailureDomain(oc, failureDomain)
}
