package machineset

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/ovf"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/coreos/stream-metadata-go/stream"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	osconfigv1 "github.com/openshift/api/config/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
)

// GCP reconciliation function. Key points:
// -GCP images aren't region specific
// -GCPMachineProviderSpec.Disk(s) stores actual bootimage URL
// -identical for x86_64/amd64 and aarch64/arm64
func reconcileGCP(machineSet *machinev1beta1.MachineSet, configMap *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Reconciling MAPI machineset %s on GCP, with arch %s", machineSet.Name, arch)

	// First, unmarshal the GCP providerSpec
	providerSpec := new(machinev1beta1.GCPMachineProviderSpec)
	if err := unmarshalProviderSpec(machineSet, providerSpec); err != nil {
		return false, nil, err
	}

	// Next, unmarshal the configmap into a stream object
	streamData := new(stream.Stream)
	if err := unmarshalStreamDataConfigMap(configMap, streamData); err != nil {
		return false, nil, err
	}

	// Construct the new target bootimage from the configmap
	// This formatting is based on how the installer constructs
	// the boot image during cluster bootstrap
	newBootImage := fmt.Sprintf("projects/%s/global/images/%s", streamData.Architectures[arch].Images.Gcp.Project, streamData.Architectures[arch].Images.Gcp.Name)

	// Grab what the current bootimage is, compare to the newBootImage
	// There is typically only one element in this Disk array, assume multiple to be safe
	patchRequired = false
	newProviderSpec := providerSpec.DeepCopy()
	for idx, disk := range newProviderSpec.Disks {
		if newBootImage != disk.Image {
			klog.Infof("New target boot image: %s", newBootImage)
			klog.Infof("Current image: %s", disk.Image)
			patchRequired = true
			newProviderSpec.Disks[idx].Image = newBootImage
		}
	}

	// For now, hardcode to the managed worker secret, until Custom Pool Booting is implemented. When that happens, this will have to
	// respect the pool this machineset is targeted for.
	if newProviderSpec.UserDataSecret.Name != ManagedWorkerSecretName {
		newProviderSpec.UserDataSecret.Name = ManagedWorkerSecretName
		patchRequired = true
	}

	// If patch is required, marshal the new providerspec into the machineset
	if patchRequired {
		newMachineSet = machineSet.DeepCopy()
		if err := marshalProviderSpec(newMachineSet, newProviderSpec); err != nil {
			return false, nil, err
		}
	}
	return patchRequired, newMachineSet, nil
}

// This function calls the appropriate reconcile function based on the infra type
// On success, it will return a bool indicating if a patch is required, and an updated
// machineset object if any. It will return an error if any of the above steps fail.
func checkMachineSet(infra *osconfigv1.Infrastructure, machineSet *machinev1beta1.MachineSet, configMap *corev1.ConfigMap, arch string) (bool, *machinev1beta1.MachineSet, error) {
	switch infra.Status.PlatformStatus.Type {
	case osconfigv1.AWSPlatformType:
		return reconcileAWS(machineSet, configMap, arch)
	case osconfigv1.AzurePlatformType:
		return reconcileAzure(machineSet, configMap, arch)
	case osconfigv1.BareMetalPlatformType:
		return reconcileBareMetal(machineSet, configMap, arch)
	case osconfigv1.OpenStackPlatformType:
		return reconcileOpenStack(machineSet, configMap, arch)
	case osconfigv1.EquinixMetalPlatformType:
		return reconcileEquinixMetal(machineSet, configMap, arch)
	case osconfigv1.GCPPlatformType:
		return reconcileGCP(machineSet, configMap, arch)
	case osconfigv1.KubevirtPlatformType:
		return reconcileKubevirt(machineSet, configMap, arch)
	case osconfigv1.IBMCloudPlatformType:
		return reconcileIBMCCloud(machineSet, configMap, arch)
	case osconfigv1.LibvirtPlatformType:
		return reconcileLibvirt(machineSet, configMap, arch)
	case osconfigv1.VSpherePlatformType:
		return reconcileVSphere(machineSet, configMap, arch)
	case osconfigv1.NutanixPlatformType:
		return reconcileNutanix(machineSet, configMap, arch)
	case osconfigv1.OvirtPlatformType:
		return reconcileOvirt(machineSet, configMap, arch)
	case osconfigv1.ExternalPlatformType:
		return reconcileExternal(machineSet, configMap, arch)
	case osconfigv1.PowerVSPlatformType:
		return reconcilePowerVS(machineSet, configMap, arch)
	case osconfigv1.NonePlatformType:
		return reconcileNone(machineSet, configMap, arch)
	default:
		return unmarshalToFindPlatform(machineSet, configMap, arch)
	}
}

func reconcileAWS(machineSet *machinev1beta1.MachineSet, configMap *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Reconciling MAPI machineset %s on AWS, with arch %s", machineSet.Name, arch)

	// First, unmarshal the AWS providerSpec
	providerSpec := new(machinev1beta1.AWSMachineProviderConfig)
	if err := unmarshalProviderSpec(machineSet, providerSpec); err != nil {
		return false, nil, err
	}

	// Next, unmarshal the configmap into a stream object
	streamData := new(stream.Stream)
	if err := unmarshalStreamDataConfigMap(configMap, streamData); err != nil {
		return false, nil, err
	}
	// Extract the region from the Placement field
	region := providerSpec.Placement.Region

	// Use the GetAwsRegionImage function to find the correct AMI for the region and architecture
	awsRegionImage, err := streamData.GetAwsRegionImage(arch, region)
	if err != nil {
		return false, nil, fmt.Errorf("failed to get AMI for region %s: %v", region, err)
	}

	newami := awsRegionImage.Image

	// Perform rest of bootimage logic here

	patchRequired = false
	newProviderSpec := providerSpec.DeepCopy()
	currentAMI := *newProviderSpec.AMI.ID
	if newami != currentAMI {
		klog.Infof("New target boot image: %s: %s", region, newami)
		klog.Infof("Current image: %s: %s", region, currentAMI)
		patchRequired = true
		newProviderSpec.AMI.ID = &newami
	}

	if newProviderSpec.UserDataSecret.Name != ManagedWorkerSecretName {
		newProviderSpec.UserDataSecret.Name = ManagedWorkerSecretName
		patchRequired = true
	}

	if patchRequired {
		newMachineSet = machineSet.DeepCopy()
		if err := marshalProviderSpec(newMachineSet, newProviderSpec); err != nil {
			return false, nil, err
		}
	}

	return patchRequired, newMachineSet, nil
}

func reconcileAzure(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type Azure with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

func reconcileBareMetal(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type BareMetal with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

func reconcileOpenStack(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type OpenStack with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

func reconcileEquinixMetal(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type EquinixMetal with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

func reconcileKubevirt(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type Kubevirt with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

func reconcileIBMCCloud(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type IBMCCloud with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

func reconcileLibvirt(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type Libvirt with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

type ProgressReader struct {
	io.Reader
	Reporter func(r int64)
}

func getTotalBytesRead(totalBytes *int64) int64 {
	return atomic.LoadInt64(totalBytes)
}

func incrementTotalBytesRead(totalBytesRead *int64, n int64) {
	atomic.StoreInt64(totalBytesRead, getTotalBytesRead(totalBytesRead)+n)
}

func upload(ctx context.Context, client *govmomi.Client, item types.OvfFileItem, f io.Reader, rawURL string, size int64, totalBytesRead *int64) error {
	u, err := client.Client.ParseURL(rawURL)
	if err != nil {
		return err
	}
	url := u.String()
	c := client.Client.Client

	param := soap.Upload{
		ContentLength: size,
	}

	if item.Create {
		param.Method = "PUT"
		param.Headers = map[string]string{
			"Overwrite": "t",
		}
	} else {
		param.Method = "POST"
		param.Type = "application/x-vnd.vmware-streamVmdk"
	}

	pr := &ProgressReader{f, func(r int64) {
		incrementTotalBytesRead(totalBytesRead, r)
	}}
	f = pr

	req, err := http.NewRequest(param.Method, url, f)
	if err != nil {
		return err
	}

	req = req.WithContext(ctx)
	req.ContentLength = param.ContentLength
	req.Header.Set("Content-Type", param.Type)

	for k, v := range param.Headers {
		req.Header.Add(k, v)
	}
	if param.Ticket != nil {
		req.AddCookie(param.Ticket)
	}

	res, err := c.Client.Do(req)
	if err != nil {
		return err
	}

	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(res.Body)

	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusCreated:
	default:
		err = errors.New(res.Status)
	}
	return err
}

func findAndUploadDiskFromOva(client *govmomi.Client, ovaFile io.Reader, diskName string, ovfFileItem types.OvfFileItem, deviceObj types.HttpNfcLeaseDeviceUrl, currBytesRead *int64) error {
	ovaReader := tar.NewReader(ovaFile)
	for {
		fileHdr, err := ovaReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if fileHdr.Name == diskName {
			err = upload(context.Background(), client, ovfFileItem, ovaReader, deviceObj.Url, ovfFileItem.Size, currBytesRead)
			if err != nil {
				return fmt.Errorf("error while uploading the file %s %s", diskName, err)
			}
			return nil
		}
	}
	return fmt.Errorf("disk %s not found inside ova", diskName)
}

func uploadOvaDisksFromLocal(client *govmomi.Client, filePath string, ovfFileItem types.OvfFileItem, deviceObj types.HttpNfcLeaseDeviceUrl, currBytesRead *int64) error {
	diskName := ovfFileItem.Path
	ovaFile, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer func(ovaFile *os.File) {
		_ = ovaFile.Close()
	}(ovaFile)

	err = findAndUploadDiskFromOva(client, ovaFile, diskName, ovfFileItem, deviceObj, currBytesRead)
	return err
}

func getOvfDescriptorFromOva(ovaFile io.Reader) (string, error) {
	ovaReader := tar.NewReader(ovaFile)
	for {
		fileHdr, err := ovaReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if strings.HasSuffix(fileHdr.Name, ".ovf") {
			content, _ := io.ReadAll(ovaReader)
			ovfDescriptor := string(content)
			return ovfDescriptor, nil
		}
	}
	return "", fmt.Errorf("ovf file not found inside the ova")
}

func createNewVMTemplateWithName(name string, streamData *stream.Stream, providerSpec *machinev1beta1.VSphereMachineProviderSpec, arch string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Find and download the relevant OVA file
	ova, err := streamData.QueryDisk(arch, "vmware", "ova")
	if err != nil {
		return err
	}

	klog.Infof("Downloading %s\n", ova.Location)
	ovaPath, err := ova.Download(".")
	if err != nil {
		return fmt.Errorf("Failed to download %s: %w", ova.Location, err)
	}
	klog.Infof("Downloaded %s\n", ovaPath)

	// Get the client
	if providerSpec.Workspace.Server == "" {
		return fmt.Errorf("Error: IP address or FQDN of the vSphere endpoint is not provided")
	}
	vcenterURL, err := url.Parse(providerSpec.Workspace.Server)
	if err != nil {
		return err
	}
	client, err := govmomi.NewClient(ctx, vcenterURL, true)
	if err != nil {
		return err
	}

	// Resource Pool
	finder := find.NewFinder(client.Client, false)
	resourcePool, err := finder.ResourcePool(ctx, providerSpec.Workspace.ResourcePool)
	if err != nil {
		return err
	}

	// Folder
	finder = find.NewFinder(client.Client, false)
	folder, err := finder.Folder(ctx, providerSpec.Workspace.Folder)
	if err != nil {
		return err
	}

	// DataStore
	finder = find.NewFinder(client.Client, false)
	dataStore, err := finder.Datastore(ctx, providerSpec.Workspace.Datastore)
	if err != nil {
		return err
	}

	// OVF Descriptor
	ovaFile, err := os.Open(ovaPath)
	defer func(ovaFile *os.File) {
		_ = ovaFile.Close()
	}(ovaFile)
	if err != nil {
		return err
	}
	ovfDescriptor, err := getOvfDescriptorFromOva(ovaFile)
	if err != nil {
		return err
	}

	// Import Spec Parameters
	importSpecParam := types.OvfCreateImportSpecParams{
		EntityName: name,
	}

	// Import Spec
	ovfManager := ovf.NewManager(client.Client)
	spec, err := ovfManager.CreateImportSpec(ctx, ovfDescriptor, resourcePool.Reference(), dataStore.Reference(), importSpecParam)
	if err != nil {
		return err
	}

	nfcLease, err := resourcePool.ImportVApp(ctx, spec.ImportSpec, folder, nil)
	if err != nil {
		return err
	}

	leaseInfo, err := nfcLease.Wait(ctx, spec.FileItem)
	if err != nil {
		return err
	}

	u := nfcLease.StartUpdater(ctx, leaseInfo)
	defer u.Done()

	var totalBytes int64
	var currBytesRead int64

	for _, ovfFileItem := range spec.FileItem {
		totalBytes += ovfFileItem.Size
	}
	klog.Infof("Total size of files to upload is %v bytes", totalBytes)

	statusChannel := make(chan bool)
	// Create a go routine to update progress regularly
	go func() {
		for {
			select {
			case <-statusChannel:
				break
			default:
				if totalBytes == 0 {
					_ = nfcLease.Progress(context.Background(), 100)
					return
				}
				klog.Infof("Uploaded %v of %v Bytes", getTotalBytesRead(&currBytesRead), totalBytes)
				progress := (getTotalBytesRead(&currBytesRead) / totalBytes) * 100
				_ = nfcLease.Progress(context.Background(), int32(progress))
				time.Sleep(10 * time.Second)
			}
		}
	}()

	for _, ovfFileItem := range spec.FileItem {
		for _, deviceObj := range leaseInfo.DeviceUrl {
			if ovfFileItem.DeviceId != deviceObj.ImportKey {
				continue
			}
			err = uploadOvaDisksFromLocal(client, ovaPath, ovfFileItem, deviceObj, &currBytesRead)
			if err != nil {
				return fmt.Errorf("error while uploading the disk %s %s", ovfFileItem.Path, err)
			}
			klog.Info(" DEBUG : Completed uploading the vmdk file", ovfFileItem.Path)
		}
	}

	klog.Infof("the vm %s has been successfully created", name)

	err = nfcLease.Complete(ctx)
	if err != nil {
		return fmt.Errorf("error while completing the VM creation")
	}

	// DataCenter
	finder = find.NewFinder(client.Client, false)
	dataCenter, err := finder.Datacenter(ctx, providerSpec.Workspace.Datacenter)
	if err != nil {
		return err
	}

	searchPath := name
	if folder != nil && folder.InventoryPath != "" {
		searchPath = path.Join(folder.InventoryPath, searchPath)
	}

	// VM
	finder = find.NewFinder(client.Client, false)
	if dataCenter != nil {
		finder.SetDatacenter(dataCenter)
	}
	vm, err := finder.VirtualMachine(ctx, searchPath)
	if err != nil {
		return err
	}

	err = vm.MarkAsTemplate(ctx)
	if err != nil {
		return err
	}

	return nil
}

func reconcileVSphere(machineSet *machinev1beta1.MachineSet, configMap *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Reconciling MAPI machineset %s on vSphere, with arch %s", machineSet.Name, arch)

	// First, unmarshal the VSphere providerSpec
	providerSpec := new(machinev1beta1.VSphereMachineProviderSpec)
	if err := unmarshalProviderSpec(machineSet, providerSpec); err != nil {
		return false, nil, err
	}

	// Next, unmarshal the configmap into a stream object
	streamData := new(stream.Stream)
	if err := unmarshalStreamDataConfigMap(configMap, streamData); err != nil {
		return false, nil, err
	}

	newBootImg := fmt.Sprintf("%s-%s-rhcos", streamData.Architectures[arch].Artifacts["vmware"].Release, "clsuterName")

	err = createNewVMTemplateWithName(newBootImg, streamData, providerSpec, arch)
	if err != nil {
		return false, nil, err
	}

	patchRequired = false
	newProviderSpec := providerSpec.DeepCopy()

	currentBootImg := newProviderSpec.Template
	if newBootImg != currentBootImg {
		klog.Infof("New target boot image: %s", newBootImg)
		klog.Infof("Current image: %s", currentBootImg)
		patchRequired = true
		newProviderSpec.Template = newBootImg
	}

	if newProviderSpec.UserDataSecret.Name != ManagedWorkerSecretName {
		newProviderSpec.UserDataSecret.Name = ManagedWorkerSecretName
		patchRequired = true
	}

	// If patch is required, marshal the new providerspec into the machineset
	if patchRequired {
		newMachineSet = machineSet.DeepCopy()
		if err := marshalProviderSpec(newMachineSet, newProviderSpec); err != nil {
			return false, nil, err
		}
	}

	return patchRequired, newMachineSet, nil
}

func reconcileNutanix(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type Nutanix with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

func reconcileOvirt(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type Ovirt with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

func reconcilePowerVS(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type PowerVS with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

func reconcileExternal(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type External with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

func reconcileNone(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type None with %s arch", machineSet.Name, arch)
	return false, nil, nil
}
