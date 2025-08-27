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

// AzureVariant represents the different Azure marketplace image variants
type AzureVariant string

const (
	// Standard OCP variant used by openshift-installer and ARO. Clusters on legacy images will
	// also fall into this category as they are transitioned to the unpaid marketplace images.
	AzureVariantMarketplaceNoPlan AzureVariant = "MarketplaceNoPlan"

	// Paid variants of the OCP product line that are available in the marketplace.
	// These clusters use an image defined in the InstallConfig at install time.
	AzureVariantOCP AzureVariant = "MarketplaceWithPlan-OCP"
	AzureVariantOPP AzureVariant = "MarketplaceWithPlan-OPP"
	AzureVariantOKE AzureVariant = "MarketplaceWithPlan-OKE"

	// EMEA flavor of the 3 paid variants listed above.
	AzureVariantOCPEMEA AzureVariant = "MarketplaceWithPlan-OCPEMEA"
	AzureVariantOPPEMEA AzureVariant = "MarketplaceWithPlan-OPPEMEA"
	AzureVariantOKEEMEA AzureVariant = "MarketplaceWithPlan-OKEEMEA"
)

// publisherOffer represents a publisher-offer combination for Azure marketplace images
type publisherOffer struct {
	publisher, offer string
}

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

	// Determine the target image stream variant
	azureVariant, err := determineAzureVariant(usesLegacyImageUpload, currentImage)
	if err != nil {
		return false, nil, err
	}

	// Extract RHCOS stream for the architecture of this machineSet
	streamArch, err := streamData.GetArchitecture(arch)
	if err != nil {
		return false, nil, err
	}

	// Sanity check: On OKD clusters marketplace streams are not available, so they should be skipped for updates.
	// TODO: Determine if OKD azure clusters are even in use/tested
	if streamArch.RHELCoreOSExtensions.Marketplace == nil {
		klog.Infof("Skipping machineset %s, marketplace streams are not available", machineSetName)
		return false, nil, nil
	}
	if streamArch.RHELCoreOSExtensions.Marketplace.Azure == nil {
		klog.Infof("Skipping machineset %s, Azure marketplace streams are not available", machineSetName)
		return false, nil, nil
	}

	// There are two types to consider: hyperGenV1 & hyperGenV2. This determination
	// can be done from the existing image information, and then used to pick from the stream data.
	//
	// Uploaded images(legacy) have a "gen2" in the resourceID field to indicate hyperGenV2
	//
	// Unpaid marketplace images:
	// - have a "v2" in the SKU field to indicate hyperGenV2
	// - aarch64 machinesets can only use hyperGenV2 images
	//
	// Paid marketplace images(MCO-1790):
	// - have a "-gen1" in the SKU field to indicate hyperGenV1
	// - we do not support paid marketplace images for aarch64
	var usesHyperVGen2 bool
	switch {
	case usesLegacyImageUpload:
		usesHyperVGen2 = strings.Contains(currentImage.ResourceID, "gen2")
	case providerSpec.Image.Type == machinev1beta1.AzureImageTypeMarketplaceNoPlan:
		usesHyperVGen2 = strings.Contains(currentImage.SKU, "v2") || arch == "aarch64"
	default:
		usesHyperVGen2 = !strings.Contains(currentImage.SKU, "gen1")
	}

	// Determine target image from RHCOS stream
	targetImage, err := getTargetImageFromStream(streamArch, azureVariant, usesHyperVGen2, arch)
	if err != nil {
		return false, nil, err
	}

	// If the current image matches, nothing to do here
	// Q: Should we enhance this to do version comparisons?
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

// determineAzureVariant determines which Azure marketplace variant to use based on the current image configuration
func determineAzureVariant(usesLegacyImageUpload bool, currentImage machinev1beta1.Image) (AzureVariant, error) {
	// Unpaid images(ARO) are publised under the "azureopenshift" published
	if usesLegacyImageUpload || currentImage.Publisher == "azureopenshift" {
		return AzureVariantMarketplaceNoPlan, nil
	}

	// For paid images:
	// - Three product lines are published in the marketplace: OCP, OPP, OKE.
	// - EMEA regions have a slightly different publisher, "redhat-limited", but all other
	// fields are the same.

	variants := map[publisherOffer]AzureVariant{
		{"redhat", "rh-ocp-worker"}:         AzureVariantOCP,
		{"redhat", "rh-opp-worker"}:         AzureVariantOPP,
		{"redhat", "rh-oke-worker"}:         AzureVariantOKE,
		{"redhat-limited", "rh-ocp-worker"}: AzureVariantOCPEMEA,
		{"redhat-limited", "rh-opp-worker"}: AzureVariantOPPEMEA,
		{"redhat-limited", "rh-oke-worker"}: AzureVariantOKEEMEA,
	}

	key := publisherOffer{currentImage.Publisher, currentImage.Offer}
	if variant, exists := variants[key]; exists {
		return variant, nil
	}

	return "", fmt.Errorf("could not determine azure marketplace variant, cannot update boot images")
}

// getTargetImageFromStream determines the correct Azure marketplace image based on architecture and variant
func getTargetImageFromStream(streamArch *stream.Arch, variant AzureVariant, usesHyperVGen2 bool, arch string) (machinev1beta1.Image, error) {
	marketplace := streamArch.RHELCoreOSExtensions.Marketplace.Azure

	var imageSet *rhcos.AzureMarketplaceImages

	// Select the appropriate image set based on variant
	switch variant {
	case AzureVariantMarketplaceNoPlan:
		imageSet = marketplace.NoPurchasePlan
	case AzureVariantOCP:
		imageSet = marketplace.OCP
	case AzureVariantOPP:
		imageSet = marketplace.OPP
	case AzureVariantOKE:
		imageSet = marketplace.OKE
	case AzureVariantOCPEMEA:
		imageSet = marketplace.OCPEMEA
	case AzureVariantOPPEMEA:
		imageSet = marketplace.OPPEMEA
	case AzureVariantOKEEMEA:
		imageSet = marketplace.OKEEMEA
	default:
		return machinev1beta1.Image{}, fmt.Errorf("unsupported Azure variant")
	}

	if imageSet == nil {
		return machinev1beta1.Image{}, fmt.Errorf("no Azure marketplace images available for variant %s", variant)
	}

	var streamImage *rhcos.AzureMarketplaceImage

	// arm64 only uses hyperGenV2
	if usesHyperVGen2 {
		if imageSet.Gen2 == nil {
			return machinev1beta1.Image{}, fmt.Errorf("no Gen2 Azure marketplace image available for architecture %s", arch)
		}
		streamImage = imageSet.Gen2
	} else {
		if imageSet.Gen1 == nil {
			return machinev1beta1.Image{}, fmt.Errorf("no Gen1 Azure marketplace image available for architecture %s", arch)
		}
		streamImage = imageSet.Gen1
	}

	isPaidImage := (variant != AzureVariantMarketplaceNoPlan)
	// Convert stream image to Azure machine image
	targetImage := getAzureImageFromStreamImage(*streamImage, isPaidImage)

	return targetImage, nil
}
