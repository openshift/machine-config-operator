package machineset

import (
	"fmt"
	"strings"

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
			klog.Infof("current boot image %s is unknown, skipping update of MachineSet %s", disk.Image, machineSet.Name)
			return false, nil, nil
		}
		patchRequired = true
		newProviderSpec.Disks[idx].Image = newBootImage
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
		// On a region not found error, log and skip this MachineSet
		klog.Infof("failed to get AMI for region %s: %v, skipping update of MachineSet %s", region, err, machineSet.Name)
		return false, nil, nil
	}

	newami := awsRegionImage.Image

	// Perform rest of bootimage logic here

	patchRequired = false
	newProviderSpec := providerSpec.DeepCopy()

	// If the MachineSet does not use an AMI ID, this is unsupported, log and skip the MachineSet
	// This happens when the installer has copied an AMI at install-time
	// Related bug: https://issues.redhat.com/browse/OCPBUGS-57506
	if newProviderSpec.AMI.ID == nil {
		klog.Infof("current AMI.ID is undefined, skipping update of MachineSet %s", machineSet.Name)
		return false, nil, nil
	}

	currentAMI := *newProviderSpec.AMI.ID

	if newami != currentAMI {
		// Validate that we're allowed to update from the current AMI
		if !AllowedAMIs.Has(currentAMI) {
			klog.Infof("current AMI %s is unknown, skipping update of MachineSet %s", currentAMI, machineSet.Name)
			return false, nil, nil
		}

		klog.Infof("New target boot image: %s: %s", region, newami)
		klog.Infof("Current image: %s: %s", region, currentAMI)
		patchRequired = true
		// Only one of ID, ARN or Filters in the AMI may be specified, so define
		// a new AMI object with only an ID field.
		newProviderSpec.AMI = machinev1beta1.AWSResourceReference{
			ID: &newami,
		}
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

func reconcileVSphere(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type VSphere with %s arch", machineSet.Name, arch)
	return false, nil, nil
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
