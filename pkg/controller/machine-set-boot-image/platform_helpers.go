package machineset

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/coreos/stream-metadata-go/stream"
	"github.com/coreos/stream-metadata-go/stream/rhcos"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	osconfigv1 "github.com/openshift/api/config/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
)

// This function calls the appropriate reconcile function based on the infra type
// On success, it will return a bool indicating if a patch is required, and an updated
// machineset object if any. It will return an error if any of the above steps fail.
func checkMachineSet(infra *osconfigv1.Infrastructure, machineSet *machinev1beta1.MachineSet, configMap *corev1.ConfigMap, arch string, secretClient clientset.Interface) (bool, *machinev1beta1.MachineSet, error) {
	switch infra.Status.PlatformStatus.Type {
	case osconfigv1.AWSPlatformType:
		return reconcilePlatform(machineSet, infra, configMap, arch, secretClient, reconcileAWSProviderSpec)
	case osconfigv1.AzurePlatformType:
		return reconcilePlatform(machineSet, infra, configMap, arch, secretClient, reconcileAzureProviderSpec)
	case osconfigv1.GCPPlatformType:
		return reconcilePlatform(machineSet, infra, configMap, arch, secretClient, reconcileGCPProviderSpec)
	case osconfigv1.VSpherePlatformType:
		return reconcilePlatform(machineSet, infra, configMap, arch, secretClient, reconcileVSphereProviderSpec)
	default:
		klog.Infof("Skipping machineset %s, unsupported platform %s", machineSet.Name, infra.Status.PlatformStatus.Type)
		return false, nil, nil
	}
}

// Generic reconcile function that handles the common pattern across all platforms
func reconcilePlatform[T any](
	machineSet *machinev1beta1.MachineSet,
	infra *osconfigv1.Infrastructure,
	configMap *corev1.ConfigMap,
	arch string,
	secretClient clientset.Interface,
	reconcileProviderSpec func(*stream.Stream, string, *osconfigv1.Infrastructure, *T, string, clientset.Interface) (bool, *T, error),
) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Reconciling MAPI machineset %s on %s, with arch %s", machineSet.Name, string(infra.Status.PlatformStatus.Type), arch)

	// Unmarshal the provider spec
	providerSpec := new(T)
	if err := unmarshalProviderSpec(machineSet, providerSpec); err != nil {
		return false, nil, err
	}

	// Unmarshal the configmap into a stream object
	streamData := new(stream.Stream)
	if err := unmarshalStreamDataConfigMap(configMap, streamData); err != nil {
		return false, nil, err
	}

	// Reconcile the provider spec
	patchRequired, newProviderSpec, err := reconcileProviderSpec(streamData, arch, infra, providerSpec, machineSet.Name, secretClient)
	if err != nil {
		return false, nil, err
	}

	// If no patch is required, exit early
	if !patchRequired {
		return false, nil, nil
	}

	// If patch is required, marshal the new providerspec into the machineset
	newMachineSet = machineSet.DeepCopy()
	if err := marshalProviderSpec(newMachineSet, newProviderSpec); err != nil {
		return false, nil, err
	}
	return patchRequired, newMachineSet, nil
}

// reconcileGCPProviderSpec reconciles the GCP provider spec by updating boot images
// Returns whether a patch is required, the updated provider spec, and any error
func reconcileGCPProviderSpec(streamData *stream.Stream, arch string, _ *osconfigv1.Infrastructure, providerSpec *machinev1beta1.GCPMachineProviderSpec, machineSetName string, secretClient clientset.Interface) (bool, *machinev1beta1.GCPMachineProviderSpec, error) {

	// Construct the new target bootimage from the configmap
	// This formatting is based on how the installer constructs
	// the boot image during cluster bootstrap
	newBootImage := fmt.Sprintf("projects/%s/global/images/%s", streamData.Architectures[arch].Images.Gcp.Project, streamData.Architectures[arch].Images.Gcp.Name)

	// Grab what the current bootimage is, compare to the newBootImage
	// There is typically only one element in this Disk array, assume multiple to be safe
	patchRequired := false
	newProviderSpec := providerSpec.DeepCopy()
	for idx, disk := range newProviderSpec.Disks {
		// Do not update non-boot disks
		if !disk.Boot {
			continue
		}
		// Nothing to update on a match
		if newBootImage == disk.Image {
			continue
		}
		klog.Infof("New target boot image: %s", newBootImage)
		klog.Infof("Current image: %s", disk.Image)
		// If image does not start with "projects/rhcos-cloud/global/images", this is a custom boot image.
		if !strings.HasPrefix(disk.Image, "projects/rhcos-cloud/global/images") {
			klog.Infof("current boot image %s is unknown, skipping update of MachineSet %s", disk.Image, machineSetName)
			return false, nil, nil
		}
		patchRequired = true
		newProviderSpec.Disks[idx].Image = newBootImage
	}

	if patchRequired {
		// Ensure the ignition stub is the minimum acceptable spec required for boot image updates
		if err := upgradeStubIgnitionIfRequired(providerSpec.UserDataSecret.Name, secretClient); err != nil {
			return false, nil, err
		}
	}

	return patchRequired, newProviderSpec, nil
}

// reconcileAWSProviderSpec reconciles the AWS provider spec by updating AMIs
// Returns whether a patch is required, the updated provider spec, and any error
func reconcileAWSProviderSpec(streamData *stream.Stream, arch string, _ *osconfigv1.Infrastructure, providerSpec *machinev1beta1.AWSMachineProviderConfig, machineSetName string, secretClient clientset.Interface) (bool, *machinev1beta1.AWSMachineProviderConfig, error) {

	// Extract the region from the Placement field
	region := providerSpec.Placement.Region

	// Use the GetAwsRegionImage function to find the correct AMI for the region and architecture
	awsRegionImage, err := streamData.GetAwsRegionImage(arch, region)
	if err != nil {
		// On a region not found error, log and skip this MachineSet
		klog.Infof("failed to get AMI for region %s: %v, skipping update of MachineSet %s", region, err, machineSetName)
		return false, nil, nil
	}

	newAMI := awsRegionImage.Image

	// Perform rest of bootimage logic here
	newProviderSpec := providerSpec.DeepCopy()

	// If the MachineSet does not use an AMI ID, this is unsupported, log and skip the MachineSet
	// This happens when the installer has copied an AMI at install-time
	// Related bug: https://issues.redhat.com/browse/OCPBUGS-57506
	if newProviderSpec.AMI.ID == nil {
		klog.Infof("current AMI.ID is undefined, skipping update of MachineSet %s", machineSetName)
		return false, nil, nil
	}

	currentAMI := *newProviderSpec.AMI.ID

	// If the current AMI matches target AMI, nothing to do here
	if newAMI == currentAMI {
		return false, nil, nil
	}

	// Validate that we're allowed to update from the current AMI
	if !AllowedAMIs.Has(currentAMI) {
		klog.Infof("current AMI %s is unknown, skipping update of MachineSet %s", currentAMI, machineSetName)
		return false, nil, nil
	}

	klog.Infof("Current image: %s: %s", region, currentAMI)
	klog.Infof("New target boot image: %s: %s", region, newAMI)

	// Only one of ID, ARN or Filters in the AMI may be specified, so define
	// a new AMI object with only an ID field.
	newProviderSpec.AMI = machinev1beta1.AWSResourceReference{
		ID: &newAMI,
	}

	// Ensure the ignition stub is the minimum acceptable spec required for boot image updates
	if err := upgradeStubIgnitionIfRequired(providerSpec.UserDataSecret.Name, secretClient); err != nil {
		return false, nil, err
	}

	return true, newProviderSpec, nil
}

func reconcileVSphereProviderSpec(streamData *stream.Stream, arch string, infra *osconfigv1.Infrastructure, providerSpec *machinev1beta1.VSphereMachineProviderSpec, _ string, secretClient clientset.Interface) (bool, *machinev1beta1.VSphereMachineProviderSpec, error) {

	if infra.Spec.PlatformSpec.VSphere == nil {
		klog.Warningf("Reconcile skipped: VSphere field is nil in PlatformSpec %v", infra.Spec.PlatformSpec)
		return false, nil, nil
	}

	streamArch, err := streamData.GetArchitecture(arch)
	if err != nil {
		return false, nil, err
	}

	artifacts := streamArch.Artifacts["vmware"]
	if artifacts.Release == "" {
		return false, nil, fmt.Errorf("%s: artifact '%s' not found", streamData.FormatPrefix(arch), "vmware")
	}

	newProviderSpec := providerSpec.DeepCopy()

	// Fetch the creds configmap
	credsSc, err := secretClient.CoreV1().Secrets("kube-system").Get(context.TODO(), "vsphere-creds", metav1.GetOptions{})
	if err != nil {
		return false, nil, fmt.Errorf("failed to fetch vsphere-creds Secret during machineset sync: %w", err)
	}

	newBootImg, patchRequired, err := createNewVMTemplate(streamData, providerSpec, infra, credsSc, arch, artifacts.Release)
	if err != nil {
		return false, nil, err
	}

	// If patch is required, marshal the new providerspec into the machineset
	if patchRequired {
		// Ensure the ignition stub is the minimum acceptable spec required for boot image updates
		if err := upgradeStubIgnitionIfRequired(providerSpec.UserDataSecret.Name, secretClient); err != nil {
			return false, nil, err
		}
		newProviderSpec.Template = newBootImg
	}

	return patchRequired, newProviderSpec, nil
}

// reconcileAzureProviderSpec reconciles the Azure provider spec by updating AMIs
// Returns whether a patch is required, the updated provider spec, and any error
func reconcileAzureProviderSpec(streamData *stream.Stream, arch string, _ *osconfigv1.Infrastructure, providerSpec *machinev1beta1.AzureMachineProviderSpec, machineSetName string, secretClient clientset.Interface) (bool, *machinev1beta1.AzureMachineProviderSpec, error) {

	if providerSpec.Image.Type == machinev1beta1.AzureImageTypeMarketplaceWithPlan {
		// TODO: see https://issues.redhat.com/browse/MCO-1790
		klog.Infof("Skipping machineset %s, paid marketplace images cannot be updated via boot image updates", machineSetName)
		return false, nil, nil
	}

	if arch == "ppc64le" || arch == "s390x" {
		klog.Infof("Skipping machineset %s, machinesets with arch %s are not supported for Azure", machineSetName, arch)
		return false, nil, nil
	}

	currentImage := providerSpec.Image

	// Machinesets that have a non empty resourceID are provisioned via an image that was
	// uploaded at install-time. For these cases, the MCO will transition them to unpaid
	// marketplace images. As part of https://issues.redhat.com//browse/CORS-3652, standard installs
	// will also begin to use the unpaid marketplace images(aka ARO images)
	usesLegacyImageUpload := (currentImage.ResourceID != "")

	// There are two variants to consider: hyperGenV1 & hyperGenV2. This determination
	// can be done from the existing image information, and then used to pick from the stream data.
	// Unpaid marketplace images have a "v2" in the SKU field to indicate hyperGenV2
	// Uploaded images(legacy) have a "gen2" in the resourceID field to indicate hyperGenV2
	// Paid marketplace images(MCO-1790) have a "-gen1" in the SKU field to indicate hyperGenV1
	var usesHyperVGen2 bool
	if usesLegacyImageUpload {
		usesHyperVGen2 = strings.Contains(currentImage.ResourceID, "gen2")
	} else {
		usesHyperVGen2 = strings.Contains(currentImage.SKU, "v2")
	}

	// Temporary hack for testing
	targetImage := machinev1beta1.Image{
		Offer:      "aro4",
		Publisher:  "azureopenshift",
		ResourceID: "",
		SKU:        "419-v2",
		Version:    "419.6.20250523",
		Type:       machinev1beta1.AzureImageTypeMarketplaceNoPlan,
	}

	if !usesHyperVGen2 {
		targetImage.SKU = "aro_419"
	}
	/*
		var targetImage machinev1beta1.Image
		streamArch, err := streamData.GetArchitecture(arch)
			if err != nil {
				return false, nil, err
			}
				streamAzureImages := streamArch.RHELCoreOSExtensions.Marketplace.Azure.NoPurchasePlan
				// arm64 only uses hyperGenV2
				if usesHyperVGen2 || arch == "aarch64" {
					targetImage = getAzureImageFromStreamImage(*streamAzureImages.Gen2, false)
				} else {
					targetImage = getAzureImageFromStreamImage(*streamAzureImages.Gen1, false)
				}
	*/
	// If the current image matches, nothing to do here
	if reflect.DeepEqual(currentImage, targetImage) {
		return false, nil, nil
	}

	klog.Infof("Current boot image version: %s", currentImage.Version)
	klog.Infof("New target boot image version: %s", targetImage.Version)

	// Update the machine set with the new image
	newProviderSpec := providerSpec.DeepCopy()
	newProviderSpec.Image = targetImage

	// Ensure the ignition stub is the minimum acceptable spec required for boot image updates
	if err := upgradeStubIgnitionIfRequired(providerSpec.UserDataSecret.Name, secretClient); err != nil {
		return false, nil, err
	}

	return true, newProviderSpec, nil
}

// getAzureImageFromStreamImage converts a stream marketplace image to an Azure machine image
// Sets the image type based on whether it's a paid marketplace image(unused at the time) or not
func getAzureImageFromStreamImage(streamImage rhcos.AzureMarketplaceImage, isPaidImage bool) machinev1beta1.Image {
	azureImage := machinev1beta1.Image{
		Offer:      streamImage.Offer,
		Publisher:  streamImage.Publisher,
		ResourceID: "",
		SKU:        streamImage.SKU,
		Version:    streamImage.Version,
		Type:       machinev1beta1.AzureImageTypeMarketplaceNoPlan,
	}
	if isPaidImage {
		azureImage.Type = machinev1beta1.AzureImageTypeMarketplaceWithPlan
	}
	return azureImage
}
