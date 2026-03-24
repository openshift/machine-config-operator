package util

import (
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	o "github.com/onsi/gomega"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"github.com/tidwall/gjson"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

// padToSemver pads versions with less than 3 identifiers to 3 using zeros. For example: 2.4 -> 2.4.0
func padToSemver(version string) string {
	parts := strings.Split(version, ".")
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	return strings.Join(parts, ".")
}

// CompareVersions returns the result of comparing 2 versions using the given operator.
// e.g. CompareVersions("3.1", ">", "3.0") returns true
func CompareVersions(l, operator, r string) bool {
	paddedL := padToSemver(l)
	paddedR := padToSemver(r)

	constraint, err := semver.NewConstraint(operator + paddedR)
	if err != nil {
		e2e.Failf("Error parsing version constraint %s%s: %v", operator, r, err)
	}

	version, err := semver.NewVersion(paddedL)
	if err != nil {
		e2e.Failf("Error parsing version %s (padded from %s): %v", paddedL, l, err)
	}

	return constraint.Check(version)
}

// getRHCOSImagesInfo fetches the RHCOS image metadata JSON for the given version from GitHub.
func getRHCOSImagesInfo(version string) (string, error) {
	var (
		err        error
		resp       *http.Response
		numRetries = 3
		retryDelay = time.Minute
		rhcosURL   = fmt.Sprintf("https://raw.githubusercontent.com/openshift/installer/release-%s/data/data/rhcos.json", version)
	)

	if CompareVersions(version, ">=", "4.10") {
		rhcosURL = fmt.Sprintf("https://raw.githubusercontent.com/openshift/installer/release-%s/data/data/coreos/rhcos.json", version)
	}

	logger.Infof("Getting rhcos image info from: %s", rhcosURL)
	for i := 0; i < numRetries; i++ {
		if i > 0 {
			logger.Infof("Error while getting the rhcos images json data: %s.\nWaiting %s and retrying. Num retries: %d", err, retryDelay, i)
			time.Sleep(retryDelay)
		}
		resp, err = http.Get(rhcosURL) // #nosec G107
		if err == nil {
			break
		}
	}

	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// getBaseImageURLFromRHCOSImageInfo returns the download URL for an RHCOS image artifact.
func getBaseImageURLFromRHCOSImageInfo(version, platform, format, stringArch string) (string, error) {
	var (
		imagePath    string
		baseURIPath  string
		olderThan410 = CompareVersions(version, "<", "4.10")
	)

	rhcosImageInfo, err := getRHCOSImagesInfo(version)
	if err != nil {
		return "", err
	}

	if olderThan410 {
		imagePath = fmt.Sprintf("images.%s.path", platform)
		baseURIPath = "baseURI"
	} else {
		imagePath = fmt.Sprintf("architectures.%s.artifacts.%s.formats.%s.disk.location", stringArch, platform, strings.ReplaceAll(format, ".", `\.`))
	}

	logger.Infof("Looking for rhcos base image path name in path %s", imagePath)
	baseImageURL := gjson.Get(rhcosImageInfo, imagePath)
	if !baseImageURL.Exists() {
		logger.Infof("rhcos info:\n%s", rhcosImageInfo)
		return "", fmt.Errorf("Could not find the base image for version <%s> in platform <%s> architecture <%s> and format <%s> with path %s",
			version, platform, stringArch, format, imagePath)
	}

	if !olderThan410 {
		return baseImageURL.String(), nil
	}

	logger.Infof("Looking for baseURL in path %s", baseURIPath)
	baseURI := gjson.Get(rhcosImageInfo, baseURIPath)
	if !baseURI.Exists() {
		logger.Infof("rhcos info:\n%s", rhcosImageInfo)
		return "", fmt.Errorf("Could not find the base URI with version <%s> in platform <%s> architecture <%s> and format <%s> with path %s",
			version, platform, stringArch, format, baseURIPath)
	}

	return fmt.Sprintf("%s/%s", strings.Replace(strings.Trim(baseURI.String(), "/"), "releases-art-rhcos.svc.ci.openshift.org", "rhcos.mirror.openshift.com", 1), strings.Trim(baseImageURL.String(), "/")), nil
}

// RHCOSHandler provides platform-specific methods for retrieving RHCOS image information.
type RHCOSHandler interface {
	GetBaseImageFromRHCOSImageInfo(version string, arch Architecture, region string) (string, error)
	GetBaseImageURLFromRHCOSImageInfo(version string, arch Architecture) (string, error)
}

// AWSRHCOSHandler implements RHCOSHandler for AWS.
type AWSRHCOSHandler struct{}

func (aws AWSRHCOSHandler) GetBaseImageFromRHCOSImageInfo(version string, arch Architecture, region string) (string, error) {
	var (
		imagePath  string
		stringArch = arch.GNUString()
		platform   = "aws"
	)

	rhcosImageInfo, err := getRHCOSImagesInfo(version)
	if err != nil {
		return "", err
	}

	if region == "" {
		return "", fmt.Errorf("Region cannot have an empty value when we try to get the base image in platform %s", platform)
	}
	if CompareVersions(version, "<", "4.10") {
		imagePath = `amis.` + region + `.hvm`
	} else {
		imagePath = fmt.Sprintf("architectures.%s.images.%s.regions.%s.image", stringArch, platform, region)
	}

	logger.Infof("Looking for rhcos base image info in path %s", imagePath)
	baseImage := gjson.Get(rhcosImageInfo, imagePath)
	if !baseImage.Exists() {
		logger.Infof("rhcos info:\n%s", rhcosImageInfo)
		return "", fmt.Errorf("Could not find the base image for version <%s> in platform <%s> architecture <%s> and region <%s> with path %s",
			version, platform, arch, region, imagePath)
	}
	return baseImage.String(), nil
}

func (aws AWSRHCOSHandler) GetBaseImageURLFromRHCOSImageInfo(version string, arch Architecture) (string, error) {
	return getBaseImageURLFromRHCOSImageInfo(version, "aws", "vmdk.gz", arch.GNUString())
}

// GCPRHCOSHandler implements RHCOSHandler for GCP.
type GCPRHCOSHandler struct{}

func (gcp GCPRHCOSHandler) GetBaseImageFromRHCOSImageInfo(version string, arch Architecture, region string) (string, error) {
	var (
		imagePath   string
		projectPath string
		stringArch  = arch.GNUString()
		platform    = "gcp"
	)

	if CompareVersions(version, "=", "4.1") {
		return "", fmt.Errorf("There is no image base image supported for platform %s in version %s", platform, version)
	}

	rhcosImageInfo, err := getRHCOSImagesInfo(version)
	if err != nil {
		return "", err
	}

	if CompareVersions(version, "<", "4.10") {
		imagePath = "gcp.image"
		projectPath = "gcp.project"
	} else {
		imagePath = fmt.Sprintf("architectures.%s.images.%s.name", stringArch, platform)
		projectPath = fmt.Sprintf("architectures.%s.images.%s.project", stringArch, platform)
	}

	logger.Infof("Looking for rhcos base image name in path %s", imagePath)
	baseImage := gjson.Get(rhcosImageInfo, imagePath)
	if !baseImage.Exists() {
		logger.Infof("rhcos info:\n%s", rhcosImageInfo)
		return "", fmt.Errorf("Could not find the base image for version <%s> in platform <%s> architecture <%s> and region <%s> with path %s",
			version, platform, arch, region, imagePath)
	}

	logger.Infof("Looking for rhcos base image project in path %s", projectPath)
	project := gjson.Get(rhcosImageInfo, projectPath)
	if !project.Exists() {
		logger.Infof("rhcos info:\n%s", rhcosImageInfo)
		return "", fmt.Errorf("Could not find the project where the base image is stored with version <%s> in platform <%s> architecture <%s> and region <%s> with path %s",
			version, platform, arch, region, projectPath)
	}

	return fmt.Sprintf("projects/%s/global/images/%s", project.String(), baseImage.String()), nil
}

func (gcp GCPRHCOSHandler) GetBaseImageURLFromRHCOSImageInfo(version string, arch Architecture) (string, error) {
	return getBaseImageURLFromRHCOSImageInfo(version, "gcp", "tar.gz", arch.GNUString())
}

// VsphereRHCOSHandler implements RHCOSHandler for vSphere.
type VsphereRHCOSHandler struct{}

func (vsp VsphereRHCOSHandler) GetBaseImageFromRHCOSImageInfo(version string, arch Architecture, _ string) (string, error) {
	baseImageURL, err := vsp.GetBaseImageURLFromRHCOSImageInfo(version, arch)
	if err != nil {
		return "", err
	}
	return path.Base(baseImageURL), nil
}

func (vsp VsphereRHCOSHandler) GetBaseImageURLFromRHCOSImageInfo(version string, arch Architecture) (string, error) {
	return getBaseImageURLFromRHCOSImageInfo(version, "vmware", "ova", arch.GNUString())
}

// GetRHCOSHandler returns the RHCOSHandler for the given platform string.
func GetRHCOSHandler(platform string) (RHCOSHandler, error) {
	switch platform {
	case "aws":
		return AWSRHCOSHandler{}, nil
	case "gcp":
		return GCPRHCOSHandler{}, nil
	case "vsphere":
		return VsphereRHCOSHandler{}, nil
	default:
		return nil, fmt.Errorf("Platform %s is not supported and cannot get RHCOSHandler", platform)
	}
}

// UploadBaseImageToCloud uploads a base image to the given cloud platform.
// AWS and GCP do not require an upload; only vSphere needs the image pre-staged.
func UploadBaseImageToCloud(oc *CLI, platform, baseImageURL, baseImage string) error {
	switch platform {
	case "aws":
		logger.Infof("No need to upload images in AWS")
		return nil
	case "gcp":
		logger.Infof("No need to upload images in GCP")
		return nil
	case "vsphere":
		vsInfo, err := GetVSphereConnectionInfo(oc.AsAdmin())
		if err != nil {
			return err
		}
		return UploadBaseImageToVsphere(baseImageURL, baseImage, vsInfo)
	default:
		return fmt.Errorf("Platform %s is not supported, base image cannot be uploaded", platform)
	}
}

// GetBackdatedBootImage returns a valid backdated boot image value for testing based on platform.
// MCO will only update images previously published by the installer; this returns one of those valid images.
// For vSphere the image must be physically uploaded to vCenter before use.
func GetBackdatedBootImage(oc *CLI) string {
	platform := CheckPlatform(oc)
	switch platform {
	case "aws":
		// MCO will only update AMIs present in the list defined here https://github.com/openshift/machine-config-operator/pull/5122
		return "ami-0ffec236307e00b94"
	case "gcp":
		// In GCP all images located in projects/rhcos-cloud/global/images are considered valid for update
		return "projects/rhcos-cloud/global/images/updateble-fake-image"
	case "azure":
		// In Azure we need to configure the whole image, not only one field. We need an image in resourceID and an empty sku field.
		// Note that the resourceID contains "gen2", so it should use "hyperVGen2".
		return `{"offer":"","publisher":"","resourceID":"/resourceGroups/fake-499nn-rg/providers/Microsoft.Compute/galleries/gallery_fake21az_499nn/images/fake-499nn-gen2/versions/latest","sku":"","version":""}`
	case "vsphere":
		// In vSphere the image must be present in vCenter, so we upload it first.
		var (
			imageVersion = "4.16"
			arch         = AMD64
		)

		By(fmt.Sprintf("Get the base image for version %s", imageVersion))
		rhcosHandler, err := GetRHCOSHandler(platform)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the rhcos handler")

		baseImage, err := rhcosHandler.GetBaseImageFromRHCOSImageInfo(imageVersion, arch, "")
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the base image")
		logger.Infof("Using base image %s", baseImage)

		baseImageURL, err := rhcosHandler.GetBaseImageURLFromRHCOSImageInfo(imageVersion, arch)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the base image URL")

		baseImage = "mcotest-" + baseImage
		o.Expect(
			UploadBaseImageToCloud(oc, platform, baseImageURL, baseImage),
		).To(o.Succeed(), "Error uploading the base image %s to the cloud", baseImageURL)
		logger.Infof("Uploaded: %s", baseImage)

		return baseImage
	default:
		return ""
	}
}
