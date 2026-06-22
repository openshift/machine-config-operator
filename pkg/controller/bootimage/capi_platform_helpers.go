package bootimage

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/coreos/stream-metadata-go/stream"
	osconfigv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"

	capav1beta2 "sigs.k8s.io/cluster-api-provider-aws/v2/api/v1beta2"
	capzv1beta1 "sigs.k8s.io/cluster-api-provider-azure/api/v1beta1"
	capgv1beta1 "sigs.k8s.io/cluster-api-provider-gcp/api/v1beta1"
	capvv1beta1 "sigs.k8s.io/cluster-api-provider-vsphere/api/govmomi/v1beta1"
)

// GVRs for CAPI infrastructure templates, one per provider.
// Defined here alongside the provider imports they correspond to.
var (
	awsMachineTemplateGVR     = capav1beta2.GroupVersion.WithResource("awsmachinetemplates")
	azureMachineTemplateGVR   = capzv1beta1.GroupVersion.WithResource("azuremachinetemplates")
	gcpMachineTemplateGVR     = capgv1beta1.GroupVersion.WithResource("gcpmachinetemplates")
	vsphereMachineTemplateGVR = capvv1beta1.GroupVersion.WithResource("vspheremachinetemplates")
)

// capiInfraTemplateGVR returns the GroupVersionResource for the infrastructure template
// corresponding to the given platform.
func capiInfraTemplateGVR(platform osconfigv1.PlatformType) (schema.GroupVersionResource, error) {
	switch platform {
	case osconfigv1.AWSPlatformType:
		return awsMachineTemplateGVR, nil
	case osconfigv1.AzurePlatformType:
		return azureMachineTemplateGVR, nil
	case osconfigv1.GCPPlatformType:
		return gcpMachineTemplateGVR, nil
	case osconfigv1.VSpherePlatformType:
		return vsphereMachineTemplateGVR, nil
	default:
		return schema.GroupVersionResource{}, fmt.Errorf("unsupported platform type %s for CAPI infrastructure template", platform)
	}
}

// checkCAPIMachineSet dispatches to the platform-specific reconcile function.
// Returns (patchRequired, patchSkipped, newTemplate, error).
func checkCAPIMachineSet(infra *osconfigv1.Infrastructure, msName string, currentTemplate *unstructured.Unstructured, configMap *corev1.ConfigMap, arch string) (bool, bool, *unstructured.Unstructured, error) {
	switch infra.Status.PlatformStatus.Type {
	case osconfigv1.AWSPlatformType:
		return reconcileAWSCAPIMachineInfraTemplate(infra, msName, currentTemplate, configMap, arch)
	case osconfigv1.AzurePlatformType:
		return reconcileAzureCAPIMachineInfraTemplate(msName, currentTemplate, configMap, arch)
	case osconfigv1.GCPPlatformType:
		return reconcileGCPCAPIMachineInfraTemplate(msName, currentTemplate, configMap, arch)
	case osconfigv1.VSpherePlatformType:
		return reconcileVSphereCAPIMachineInfraTemplate(msName, currentTemplate, configMap, arch)
	default:
		klog.Infof("Skipping CAPI MachineSet %s, unsupported platform %s", msName, infra.Status.PlatformStatus.Type)
		return false, false, nil, nil
	}
}

// reconcileAWSCAPIMachineInfraTemplate reconciles an AWSMachineTemplate for a CAPI MachineSet.
func reconcileAWSCAPIMachineInfraTemplate(infra *osconfigv1.Infrastructure, msName string, currentTemplate *unstructured.Unstructured, configMap *corev1.ConfigMap, arch string) (bool, bool, *unstructured.Unstructured, error) {
	klog.Infof("Reconciling CAPI MachineSet %s on AWS with arch %s", msName, arch)

	streamData := new(stream.Stream)
	if err := unmarshalStreamDataConfigMap(configMap, streamData); err != nil {
		return false, false, nil, err
	}

	// Region is not stored in AWSMachineTemplate; read it from the Infrastructure object.
	if infra.Status.PlatformStatus.AWS == nil {
		return false, false, nil, fmt.Errorf("AWS platform status is nil in Infrastructure object")
	}
	region := infra.Status.PlatformStatus.AWS.Region

	awsRegionImage, err := streamData.GetAwsRegionImage(arch, region)
	if err != nil {
		klog.Infof("failed to get AMI for region %s: %v, skipping update of CAPI MachineSet %s", region, err, msName)
		return false, true, nil, nil
	}
	newAMI := awsRegionImage.Image

	awsTemplate := &capav1beta2.AWSMachineTemplate{}
	if err := kruntime.DefaultUnstructuredConverter.FromUnstructured(currentTemplate.Object, awsTemplate); err != nil {
		return false, false, nil, fmt.Errorf("failed to convert AWSMachineTemplate %s: %w", currentTemplate.GetName(), err)
	}

	if awsTemplate.Spec.Template.Spec.AMI.ID == nil {
		klog.Infof("current AMI.ID is undefined in infrastructure template for CAPI MachineSet %s, skipping", msName)
		return false, true, nil, nil
	}
	currentAMI := *awsTemplate.Spec.Template.Spec.AMI.ID

	if newAMI == currentAMI {
		return false, false, nil, nil
	}

	if !AllowedAMIs.Has(currentAMI) {
		klog.Infof("current AMI %s is unknown, skipping update of CAPI MachineSet %s", currentAMI, msName)
		return false, true, nil, nil
	}

	klog.Infof("Current image: %s: %s", region, currentAMI)
	klog.Infof("New target boot image: %s: %s", region, newAMI)

	newAWSTemplate := awsTemplate.DeepCopy()
	newAWSTemplate.Spec.Template.Spec.AMI = capav1beta2.AMIReference{ID: &newAMI}

	newObj, err := kruntime.DefaultUnstructuredConverter.ToUnstructured(newAWSTemplate)
	if err != nil {
		return false, false, nil, fmt.Errorf("failed to convert updated AWSMachineTemplate to unstructured: %w", err)
	}
	return true, false, &unstructured.Unstructured{Object: newObj}, nil
}

// reconcileAzureCAPIMachineInfraTemplate reconciles an AzureMachineTemplate for a CAPI MachineSet.
// Only Marketplace images are reconciled; ComputeGallery and custom ID images are skipped.
func reconcileAzureCAPIMachineInfraTemplate(msName string, currentTemplate *unstructured.Unstructured, configMap *corev1.ConfigMap, arch string) (bool, bool, *unstructured.Unstructured, error) {
	klog.Infof("Reconciling CAPI MachineSet %s on Azure with arch %s", msName, arch)

	if arch == "ppc64le" || arch == "s390x" {
		klog.Infof("Skipping update for CAPI MachineSet %s, arch %s is not supported for Azure", msName, arch)
		return false, false, nil, nil
	}

	azureTemplate := &capzv1beta1.AzureMachineTemplate{}
	if err := kruntime.DefaultUnstructuredConverter.FromUnstructured(currentTemplate.Object, azureTemplate); err != nil {
		return false, false, nil, fmt.Errorf("failed to convert AzureMachineTemplate %s: %w", currentTemplate.GetName(), err)
	}

	if azureTemplate.Spec.Template.Spec.SecurityProfile != nil &&
		azureTemplate.Spec.Template.Spec.SecurityProfile.SecurityType != "" {
		klog.Infof("Skipping update for CAPI MachineSet %s, SecurityType is set (%s)", msName, azureTemplate.Spec.Template.Spec.SecurityProfile.SecurityType)
		return false, false, nil, nil
	}

	img := azureTemplate.Spec.Template.Spec.Image
	if img == nil || img.Marketplace == nil {
		klog.Infof("CAPI MachineSet %s does not use a Marketplace image, skipping boot image update", msName)
		return false, true, nil, nil
	}
	currentMarketplace := img.Marketplace

	// Determine the variant and HyperV generation from the current image metadata.
	// Reuse MAPI helpers via a publisher/offer lookup.
	variants := map[publisherOffer]AzureVariant{
		{"redhat", "rh-ocp-worker"}:         AzureVariantOCP,
		{"redhat", "rh-opp-worker"}:         AzureVariantOPP,
		{"redhat", "rh-oke-worker"}:         AzureVariantOKE,
		{"redhat-limited", "rh-ocp-worker"}: AzureVariantOCPEMEA,
		{"redhat-limited", "rh-opp-worker"}: AzureVariantOPPEMEA,
		{"redhat-limited", "rh-oke-worker"}: AzureVariantOKEEMEA,
	}
	var variant AzureVariant
	if currentMarketplace.ImagePlan.Publisher == "azureopenshift" {
		variant = AzureVariantMarketplaceNoPlan
	} else {
		key := publisherOffer{currentMarketplace.ImagePlan.Publisher, currentMarketplace.ImagePlan.Offer}
		var ok bool
		if variant, ok = variants[key]; !ok {
			return false, false, nil, fmt.Errorf("could not determine azure marketplace variant for CAPI MachineSet %s", msName)
		}
	}

	isPaidImage := (variant != AzureVariantMarketplaceNoPlan)
	usesHyperVGen2 := false
	if !isPaidImage {
		usesHyperVGen2 = strings.Contains(currentMarketplace.SKU, "v2") || arch == "aarch64"
	} else {
		usesHyperVGen2 = !strings.Contains(currentMarketplace.SKU, "gen1")
	}

	streamData := new(stream.Stream)
	if err := unmarshalStreamDataConfigMap(configMap, streamData); err != nil {
		return false, false, nil, err
	}

	streamArch, err := streamData.GetArchitecture(arch)
	if err != nil {
		return false, false, nil, err
	}

	if streamArch.RHELCoreOSExtensions == nil || streamArch.RHELCoreOSExtensions.Marketplace == nil {
		klog.Infof("Skipping CAPI MachineSet %s, marketplace streams are not available", msName)
		return false, true, nil, nil
	}
	if streamArch.RHELCoreOSExtensions.Marketplace.Azure == nil {
		klog.Infof("Skipping CAPI MachineSet %s, Azure marketplace streams are not available", msName)
		return false, true, nil, nil
	}

	// Reuse the MAPI helper to get the target stream image and build a CAPI marketplace image from it.
	targetMAPIImage, err := getTargetImageFromStream(streamArch, variant, usesHyperVGen2, arch)
	if err != nil {
		return false, false, nil, err
	}
	targetMarketplace := capzv1beta1.AzureMarketplaceImage{
		ImagePlan: capzv1beta1.ImagePlan{
			Publisher: targetMAPIImage.Publisher,
			Offer:     targetMAPIImage.Offer,
			SKU:       targetMAPIImage.SKU,
		},
		Version:         targetMAPIImage.Version,
		ThirdPartyImage: isPaidImage,
	}

	if reflect.DeepEqual(*currentMarketplace, targetMarketplace) {
		return false, false, nil, nil
	}

	klog.Infof("Current boot image version: %s", currentMarketplace.Version)
	klog.Infof("New target boot image version: %s", targetMarketplace.Version)

	newAzureTemplate := azureTemplate.DeepCopy()
	newAzureTemplate.Spec.Template.Spec.Image = &capzv1beta1.Image{Marketplace: &targetMarketplace}

	newObj, err := kruntime.DefaultUnstructuredConverter.ToUnstructured(newAzureTemplate)
	if err != nil {
		return false, false, nil, fmt.Errorf("failed to convert updated AzureMachineTemplate to unstructured: %w", err)
	}
	return true, false, &unstructured.Unstructured{Object: newObj}, nil
}

// reconcileGCPCAPIMachineInfraTemplate reconciles a GCPMachineTemplate for a CAPI MachineSet.
func reconcileGCPCAPIMachineInfraTemplate(msName string, currentTemplate *unstructured.Unstructured, configMap *corev1.ConfigMap, arch string) (bool, bool, *unstructured.Unstructured, error) {
	klog.Infof("Reconciling CAPI MachineSet %s on GCP with arch %s", msName, arch)

	streamData := new(stream.Stream)
	if err := unmarshalStreamDataConfigMap(configMap, streamData); err != nil {
		return false, false, nil, err
	}

	newBootImage := fmt.Sprintf("projects/%s/global/images/%s",
		streamData.Architectures[arch].Images.Gcp.Project,
		streamData.Architectures[arch].Images.Gcp.Name)

	gcpTemplate := &capgv1beta1.GCPMachineTemplate{}
	if err := kruntime.DefaultUnstructuredConverter.FromUnstructured(currentTemplate.Object, gcpTemplate); err != nil {
		return false, false, nil, fmt.Errorf("failed to convert GCPMachineTemplate %s: %w", currentTemplate.GetName(), err)
	}

	if gcpTemplate.Spec.Template.Spec.Image == nil {
		klog.Infof("current image is undefined in infrastructure template for CAPI MachineSet %s, skipping", msName)
		return false, true, nil, nil
	}
	currentImage := *gcpTemplate.Spec.Template.Spec.Image

	if newBootImage == currentImage {
		return false, false, nil, nil
	}

	if !strings.HasPrefix(currentImage, "projects/rhcos-cloud/global/images") {
		klog.Infof("current boot image %s is unknown, skipping update of CAPI MachineSet %s", currentImage, msName)
		return false, true, nil, nil
	}

	klog.Infof("Current image: %s", currentImage)
	klog.Infof("New target boot image: %s", newBootImage)

	newGCPTemplate := gcpTemplate.DeepCopy()
	newGCPTemplate.Spec.Template.Spec.Image = &newBootImage

	newObj, err := kruntime.DefaultUnstructuredConverter.ToUnstructured(newGCPTemplate)
	if err != nil {
		return false, false, nil, fmt.Errorf("failed to convert updated GCPMachineTemplate to unstructured: %w", err)
	}
	return true, false, &unstructured.Unstructured{Object: newObj}, nil
}

// reconcileVSphereCAPIMachineInfraTemplate reconciles a VSphereMachineTemplate for a CAPI MachineSet.
// Creating a new vSphere VM template requires govc infrastructure calls (identical to the MAPI path).
// TODO: implement once the vSphere template upload flow is extracted from the MAPI path.
func reconcileVSphereCAPIMachineInfraTemplate(msName string, currentTemplate *unstructured.Unstructured, configMap *corev1.ConfigMap, arch string) (bool, bool, *unstructured.Unstructured, error) {
	vsphereTemplate := &capvv1beta1.VSphereMachineTemplate{}
	if err := kruntime.DefaultUnstructuredConverter.FromUnstructured(currentTemplate.Object, vsphereTemplate); err != nil {
		return false, false, nil, fmt.Errorf("failed to convert VSphereMachineTemplate %s: %w", currentTemplate.GetName(), err)
	}
	klog.V(4).Infof("CAPI MachineSet %s: vSphere boot image reconciliation not yet implemented", msName)
	return false, false, nil, nil
}
