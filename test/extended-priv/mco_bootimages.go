package extended

import (
	"bytes"
	"fmt"
	"os/exec"

	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	"github.com/openshift/machine-config-operator/test/extended-priv/util/architecture"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"github.com/tidwall/gjson"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

// getCoreOsBootImageFromConfigMap retrieves the boot image from the coreos-bootimages ConfigMap for the given platform and architecture
func getCoreOsBootImageFromConfigMap(platform, region string, arch architecture.Architecture, coreosBootimagesCM *ConfigMap) (string, error) {
	var coreOsBootImagePath string
	stringArch := arch.GNUString()

	logger.Infof("Looking for coreos boot image for architecture %s in %s", stringArch, coreosBootimagesCM)

	streamJSON, err := coreosBootimagesCM.GetDataValue("stream")
	if err != nil {
		return "", err
	}
	parsedStream := gjson.Parse(streamJSON)

	switch platform {
	case AWSPlatform:
		if region == "" {
			return "", fmt.Errorf("Region is empty for platform %s. The region is mandatory if we want to get the boot image value", platform)
		}
		coreOsBootImagePath = fmt.Sprintf(`architectures.%s.images.%s.regions.%s.image`, stringArch, platform, region)
	case GCPPlatform:
		coreOsBootImagePath = fmt.Sprintf(`architectures.%s.images.%s.name`, stringArch, platform)
	case VspherePlatform:
		// There is no such thing as a "bootimage in vsphere", we need to manually upload it always. We return the version instead, since it is the only info we can use to verify the bootimage
		// in vsphere platform, the key is "vmware" and not "vsphere"
		coreOsBootImagePath = fmt.Sprintf(`architectures.%s.artifacts.%s.release`, stringArch, "vmware")
	case AzurePlatform:
		coreOsBootImagePath = fmt.Sprintf(`architectures.%s.rhel-coreos-extensions.marketplace.%s.no-purchase-plan.hyperVGen2`, stringArch, "azure")
	default:
		return "", fmt.Errorf("Machineset.GetCoreOsBootImage method is only supported for GCP, Vsphere, Azure, and AWS platforms")
	}

	currentCoreOsBootImage := parsedStream.Get(coreOsBootImagePath).String()

	if currentCoreOsBootImage == "" {
		logger.Warnf("The coreos boot image for architecture %s in %s IS EMPTY. ImagePath: %s", stringArch, coreosBootimagesCM, coreOsBootImagePath)
	}

	return currentCoreOsBootImage, nil
}

// getCoreOsBootImageFromConfigMapOrFail gets the boot image and fails the test if there's an error
func getCoreOsBootImageFromConfigMapOrFail(platform, region string, arch architecture.Architecture, coreosBootimagesCM *ConfigMap) string {
	image, err := getCoreOsBootImageFromConfigMap(platform, region, arch, coreosBootimagesCM)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the boot image from %s for platform %s and arch %s", coreosBootimagesCM, platform, arch)
	return image
}

// GetValidUpdateBootImageValue returns a valid boot image value for testing based on platform
// MCO will only update images previously published in the installer. This function returns one of those valid images
func GetValidUpdateBootImageValue(oc *exutil.CLI) string {
	var (
		platform = exutil.CheckPlatform(oc)
	)

	switch platform {
	case AWSPlatform:
		// MCO will only update AMIS present in the list defined here https://github.com/openshift/machine-config-operator/pull/5122
		// We choose one of them
		return "ami-0ffec236307e00b94"
	case GCPPlatform:
		// In GCP all images located in projects/rhcos-cloud/global/images are considered valid for update
		return "projects/rhcos-cloud/global/images" + "/updateble-fake-image"
	case AzurePlatform:
		// In Azure we need to configure the whole image, not only one field. We need an image in resourceID and an empty sku field
		// We use a similar resourceID as the one generated in a normal installation. Note that it contains "gen2", so it should use "hyperVGen2"
		return `{"offer":"","publisher":"","resourceID":"/resourceGroups/fake-499nn-rg/providers/Microsoft.Compute/galleries/gallery_fake21az_499nn/images/fake-499nn-gen2/versions/latest","sku":"","version":""}`
	case VspherePlatform:
		// In Vsphere we need the image to be present in the vcenter, so we need to manually upload it
		var (
			// We will use 4.16 as the original version that will be updated to the current version
			imageVersion = "4.16"
			// Vsphere only support AMD64
			arch = architecture.AMD64
		)

		// Get the right base image name from the rhcos json info stored in the github repositories
		exutil.By(fmt.Sprintf("Get the base image for version %s", imageVersion))
		rhcosHandler, err := GetRHCOSHandler(platform)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the rhcos handler")

		baseImage, err := rhcosHandler.GetBaseImageFromRHCOSImageInfo(imageVersion, arch, "")
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the base image")
		logger.Infof("Using base image %s", baseImage)

		baseImageURL, err := rhcosHandler.GetBaseImageURLFromRHCOSImageInfo(imageVersion, arch)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the base image URL")

		// To avoid collisions we will add prefix to identify our image
		baseImage = "mcotest-" + baseImage
		o.Expect(
			uploadBaseImageToCloud(oc, platform, baseImageURL, baseImage),
		).To(o.Succeed(), "Error uploading the base image %s to the cloud", baseImageURL)
		logger.Infof("Uplodated: %s", baseImage)
		logger.Infof("OK!\n")

		return baseImage
	default:
		return ""
	}
}

// getReleaseFromVsphereTemplate gets the release version from a vSphere template
func getReleaseFromVsphereTemplate(oc *exutil.CLI, vsphereTemplate string) (string, error) {

	var (
		execBin       = "govc"
		vmInfoCommand = []string{"vm.info", "-json", vsphereTemplate}
		stderr        bytes.Buffer
	)

	server, dataCenter, dataStore, resourcePool, user, password, err := getvSphereCredentials(oc.AsAdmin())
	if err != nil {
		return "", err
	}

	govcExecEnv := getGovcEnv(server, dataCenter, dataStore, resourcePool, user, password)

	logger.Infof("Getting information about vsphere template %s", vsphereTemplate)
	logger.Infof("%s %s", execBin, vmInfoCommand)

	vmInfoCmd := exec.Command(execBin, vmInfoCommand...)
	vmInfoCmd.Stderr = &stderr
	vmInfoCmd.Env = govcExecEnv

	vmInfo, err := vmInfoCmd.Output()
	if err != nil {
		logger.Errorf("Output: %s", string(vmInfo))
		logger.Errorf("Stderr: %s", stderr.String())
		return "", err
	}

	gVersion := gjson.Get(string(vmInfo), "virtualMachines.0.summary.config.product.version")
	if !gVersion.Exists() {
		return "", fmt.Errorf("Cannot get config from vm info: %s", vmInfoCmd)
	}

	version := gVersion.String()
	logger.Infof("Version for vm %s: %s", vsphereTemplate, version)
	return version, nil
}

// CheckCurrentOSImageIsUpdated checks that the machineset/controlplanemachineset is using the bootimage expected in the current cluster version
func CheckCurrentOSImageIsUpdated(bir BootImageResource) {
	var (
		oc                 = bir.GetOC()
		platform           = exutil.CheckPlatform(oc)
		region             = getCurrentRegionOrFail(oc)
		arch               = bir.GetArchitectureOrFail()
		coreosBootimagesCM = NewConfigMap(oc.AsAdmin(), MachineConfigNamespace, "coreos-bootimages")
	)

	currentCoreOsBootImage := getCoreOsBootImageFromConfigMapOrFail(platform, region, arch, coreosBootimagesCM)
	logger.Infof("Current coreOsBootImage: %s", currentCoreOsBootImage)
	o.Expect(currentCoreOsBootImage).NotTo(o.BeEmpty(), "Could not find the right coreOS image for this platform")

	switch platform {
	case AWSPlatform, GCPPlatform:
		o.Eventually(bir.GetCoreOsBootImage, "5m", "20s").Should(o.ContainSubstring(currentCoreOsBootImage),
			"%s was NOT updated to use the right boot image", bir)
	case VspherePlatform:
		o.Eventually(func() (string, error) {
			bootImage, err := bir.GetCoreOsBootImage()
			if err != nil {
				return "", err
			}
			return getReleaseFromVsphereTemplate(oc.AsAdmin(), bootImage)
		}, "5m", "20s").
			Should(o.Equal(currentCoreOsBootImage), "The image used to update %s doen't have the right version", bir)
	case AzurePlatform:
		parsedImage := gjson.Parse(currentCoreOsBootImage)
		sku := parsedImage.Get("sku").String()
		version := parsedImage.Get("version").String()
		offer := parsedImage.Get("offer").String()
		publisher := parsedImage.Get("publisher").String()

		o.Eventually(bir.GetCoreOsBootImage, "5m", "20s").Should(o.And(
			HavePathWithValue("publisher", o.Equal(publisher)),
			HavePathWithValue("offer", o.Equal(offer)),
			HavePathWithValue("sku", o.Equal(sku)),
			HavePathWithValue("version", o.Equal(version)),
			HavePathWithValue("resourceID", o.BeEmpty()),
			HavePathWithValue("type", o.Equal("MarketplaceNoPlan"))),
			"%s was NOT updated to use the right boot image", bir)
	default:
		e2e.Failf("Platform not supported in CheckCurrentOSImageIsUpdated: %s", platform)
	}
}
