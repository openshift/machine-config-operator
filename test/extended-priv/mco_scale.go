package extended

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"time"

	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	"github.com/openshift/machine-config-operator/test/extended-priv/util/architecture"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"github.com/tidwall/gjson"
)

// getCurrentRegionOrFail returns the current region if we are in AWS or an empty string if any other platform
func getCurrentRegionOrFail(oc *exutil.CLI) string {
	infra := NewResource(oc.AsAdmin(), "infrastructure", "cluster")
	return infra.GetOrFail(`{.status.platformStatus.aws.region}`)
}

// extractINIValue parses a simple INI file and extracts a value
//
//nolint:unparam // section is always "Worker",  but kept for flexibility
func extractINIValue(config, section, key string) string {
	// Find the section
	sectionRegex := regexp.MustCompile(`(?m)^\[` + regexp.QuoteMeta(section) + `\]`)
	sectionMatch := sectionRegex.FindStringIndex(config)
	if sectionMatch == nil {
		return ""
	}

	// Find the next section or end of file
	nextSectionRegex := regexp.MustCompile(`(?m)^\[`)
	configFromSection := config[sectionMatch[1]:]
	nextSectionMatch := nextSectionRegex.FindStringIndex(configFromSection)

	var sectionContent string
	if nextSectionMatch != nil {
		sectionContent = configFromSection[:nextSectionMatch[0]]
	} else {
		sectionContent = configFromSection
	}

	// Find the key=value pair
	keyRegex := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(key) + `\s*=\s*(.*)$`)
	keyMatch := keyRegex.FindStringSubmatch(sectionContent)
	if keyMatch != nil && len(keyMatch) > 1 {
		return strings.TrimSpace(keyMatch[1])
	}

	return ""
}

func getGovcEnv(server, dataCenter, dataStore, resourcePool, user, password string) []string {
	var (
		govcEnv = []string{
			"GOVC_URL=" + server,
			"GOVC_USERNAME=" + user,
			"GOVC_PASSWORD=" + password,
			"GOVC_DATASTORE=" + dataStore,
			"GOVC_RESOURCE_POOL=" + resourcePool,
			"GOVC_DATACENTER=" + dataCenter,
			"GOVC_INSECURE=true",
		}
		originalEnv = os.Environ()
	)

	// In prow the GOVC_TLS_CA_CERTS is not correctly set and it is making the govc command fail.
	// we remove this variable from the environment
	var execEnv []string
	for _, envVar := range originalEnv {
		if strings.HasPrefix(envVar, "GOVC_TLS_CA_CERTS=") {
			continue
		}
		execEnv = append(execEnv, envVar)
	}
	execEnv = append(execEnv, govcEnv...)

	return execEnv
}

func getvSphereCredentials(oc *exutil.CLI) (server, dataCenter, dataStore, resourcePool, user, password string, err error) {
	var (
		configCM    = NewConfigMap(oc.AsAdmin(), "openshift-config", "cloud-provider-config")
		credsSecret = NewSecret(oc.AsAdmin(), "kube-system", "vsphere-creds")
	)
	config, err := configCM.GetDataValue("config")
	if err != nil {
		return
	}

	// Try to parse as INI format (simple regex-based parsing)
	iniParsed := false
	if strings.Contains(config, "[Workspace]") {
		logger.Infof("%s config info is in ini format. Extracting data", configCM)
		server = extractINIValue(config, "Workspace", "server")
		dataCenter = extractINIValue(config, "Workspace", "datacenter")
		dataStore = extractINIValue(config, "Workspace", "default-datastore")
		resourcePool = extractINIValue(config, "Workspace", "resourcepool-path")
		if server != "" && dataCenter != "" {
			iniParsed = true
		}
	}

	if !iniParsed {
		logger.Infof("%s config info is NOT in ini fomart. Trying to extract the information from the infrastructure resource", configCM)
		infra := NewResource(oc.AsAdmin(), "infrastructure", "cluster")
		var failureDomain string
		failureDomain, err = infra.Get(`{.spec.platformSpec.vsphere.failureDomains[0]}`)
		if err != nil {
			logger.Errorf("Cannot get the failureDomain from the infrastructure resource: %s", err)
			return
		}
		if failureDomain == "" {
			logger.Errorf("Failure domain is empty in the infrastructure resource: %s\n%s", err, infra.PrettyString())
			err = fmt.Errorf("Empty failure domain in the infrastructure resource")
			return

		}
		gserver := gjson.Get(failureDomain, "server")
		if gserver.Exists() {
			server = gserver.String()
		} else {
			err = fmt.Errorf("Cannot get the server value from failureDomain\n%s", infra.PrettyString())
			return
		}
		gdataCenter := gjson.Get(failureDomain, "topology.datacenter")
		if gdataCenter.Exists() {
			dataCenter = gdataCenter.String()
		} else {
			err = fmt.Errorf("Cannot get the data center value from failureDomain\n%s", infra.PrettyString())
			return
		}

		gdataStore := gjson.Get(failureDomain, "topology.datastore")
		if gdataStore.Exists() {
			dataStore = gdataStore.String()
		} else {
			err = fmt.Errorf("Cannot get the data store value from failureDomain\n%s", infra.PrettyString())
			return
		}

		gresourcePool := gjson.Get(failureDomain, "topology.resourcePool")
		if gresourcePool.Exists() {
			resourcePool = gresourcePool.String()
		} else {
			err = fmt.Errorf("Cannot get the resourcepool value from failureDomain\n%s", infra.PrettyString())
			return
		}
	}

	decodedData, err := credsSecret.GetDecodedDataMap()
	if err != nil {
		return
	}

	for k, v := range decodedData {
		item := v
		if strings.Contains(k, "username") {
			user = item
		}
		if strings.Contains(k, "password") {
			password = item
		}
	}

	if user == "" {
		logger.Errorf("Empty vsphere user")
		err = fmt.Errorf("The vsphere user is empty")
		return
	}

	if password == "" {
		logger.Errorf("Empty vsphere password")
		err = fmt.Errorf("The vsphere password is empty")
		return
	}

	return
}

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

	// To mitigate network errors we will retry in case of failure
	logger.Infof("Getting rhcos image info from: %s", rhcosURL)
	for i := 0; i < numRetries; i++ {
		if i > 0 {
			logger.Infof("Error while getting the rhcos mages json data: %s.\nWaiting %s and retrying. Num retries: %d", err, retryDelay, i)
			time.Sleep(retryDelay)
		}
		resp, err = http.Get(rhcosURL)
		if err == nil {
			break
		}
	}

	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// We Read the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func GetRHCOSHandler(platform string) (RHCOSHandler, error) {
	switch platform {
	case AWSPlatform:
		return AWSRHCOSHandler{}, nil
	case GCPPlatform:
		return GCPRHCOSHandler{}, nil
	case VspherePlatform:
		return VsphereRHCOSHandler{}, nil
	default:
		return nil, fmt.Errorf("Platform %s is not supported and cannot get RHCOSHandler", platform)
	}
}

type RHCOSHandler interface {
	GetBaseImageFromRHCOSImageInfo(version string, arch architecture.Architecture, region string) (string, error)
	GetBaseImageURLFromRHCOSImageInfo(version string, arch architecture.Architecture) (string, error)
}

type AWSRHCOSHandler struct{}

func (aws AWSRHCOSHandler) GetBaseImageFromRHCOSImageInfo(version string, arch architecture.Architecture, region string) (string, error) {
	var (
		path       string
		stringArch = arch.GNUString()
		platform   = AWSPlatform
	)

	rhcosImageInfo, err := getRHCOSImagesInfo(version)
	if err != nil {
		return "", err
	}

	if region == "" {
		return "", fmt.Errorf("Region cannot have an empty value when we try to get the base image in platform %s", platform)
	}
	if CompareVersions(version, "<", "4.10") {
		path = `amis.` + region + `.hvm`
	} else {
		path = fmt.Sprintf("architectures.%s.images.%s.regions.%s.image", stringArch, platform, region)

	}

	logger.Infof("Looking for rhcos base image info in path %s", path)
	baseImage := gjson.Get(rhcosImageInfo, path)
	if !baseImage.Exists() {
		logger.Infof("rhcos info:\n%s", rhcosImageInfo)
		return "", fmt.Errorf("Could not find the base image for version <%s> in platform <%s> architecture <%s> and region <%s> with path %s",
			version, platform, arch, region, path)
	}
	return baseImage.String(), nil
}

func (aws AWSRHCOSHandler) GetBaseImageURLFromRHCOSImageInfo(version string, arch architecture.Architecture) (string, error) {
	return getBaseImageURLFromRHCOSImageInfo(version, "aws", "vmdk.gz", arch.GNUString())
}

type GCPRHCOSHandler struct{}

func (gcp GCPRHCOSHandler) GetBaseImageFromRHCOSImageInfo(version string, arch architecture.Architecture, region string) (string, error) {
	var (
		imagePath   string
		projectPath string
		stringArch  = arch.GNUString()
		platform    = GCPPlatform
	)

	if CompareVersions(version, "==", "4.1") {
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

func (gcp GCPRHCOSHandler) GetBaseImageURLFromRHCOSImageInfo(version string, arch architecture.Architecture) (string, error) {
	return getBaseImageURLFromRHCOSImageInfo(version, "gcp", "tar.gz", arch.GNUString())
}

type VsphereRHCOSHandler struct{}

func (vsp VsphereRHCOSHandler) GetBaseImageFromRHCOSImageInfo(version string, arch architecture.Architecture, _ string) (string, error) {
	baseImageURL, err := vsp.GetBaseImageURLFromRHCOSImageInfo(version, arch)
	if err != nil {
		return "", err
	}

	return path.Base(baseImageURL), nil
}

func (vsp VsphereRHCOSHandler) GetBaseImageURLFromRHCOSImageInfo(version string, arch architecture.Architecture) (string, error) {
	return getBaseImageURLFromRHCOSImageInfo(version, "vmware", "ova", arch.GNUString())
}

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

func uploadBaseImageToCloud(oc *exutil.CLI, platform, baseImageURL, baseImage string) error {

	switch platform {
	case AWSPlatform:
		logger.Infof("No need to updload images in AWS")
		return nil
	case GCPPlatform:
		logger.Infof("No need to updload images in GCP")
		return nil
	case VspherePlatform:
		server, dataCenter, dataStore, resourcePool, user, password, err := getvSphereCredentials(oc.AsAdmin())
		if err != nil {
			return err
		}

		err = uploadBaseImageToVsphere(baseImageURL, baseImage, server, dataCenter, dataStore, resourcePool, user, password)
		if err != nil {
			return err
		}

		return nil
	default:
		return fmt.Errorf("Platform %s is not supported, base image cannot be updloaded", platform)
	}
}

func uploadBaseImageToVsphere(baseImageSrc, baseImageDest, server, dataCenter, dataStore, resourcePool, user, password string) error {
	var (
		execBin          = "govc"
		uploadCommand    = []string{"import.ova", "--debug", "--name", baseImageDest, baseImageSrc}
		upgradeHWCommand = []string{"vm.upgrade", "-vm", baseImageDest}
		templateCommand  = []string{"vm.markastemplate", baseImageDest}
		govcExecEnv      = getGovcEnv(server, dataCenter, dataStore, resourcePool, user, password)
	)

	logger.Infof("Uploading base image %s to vsphere with name %s", baseImageSrc, baseImageDest)
	logger.Infof("%s %s", execBin, uploadCommand)

	uploadCmd := exec.Command(execBin, uploadCommand...)
	uploadCmd.Env = govcExecEnv

	out, err := uploadCmd.CombinedOutput()
	logger.Infof(string(out))
	if err != nil {
		if strings.Contains(string(out), "already exists") {
			logger.Infof("Image %s already exists in the cloud, we don't upload it again", baseImageDest)
		} else {
			return err
		}
	}

	logger.Infof("Upgrading VM's hardware")
	logger.Infof("%s %s", execBin, upgradeHWCommand)

	upgradeCmd := exec.Command(execBin, upgradeHWCommand...)
	upgradeCmd.Env = govcExecEnv

	out, err = upgradeCmd.CombinedOutput()
	logger.Infof(string(out))
	if err != nil {
		// We don't fail. We log a warning and continue.
		logger.Warnf("ERROR UPGRADING HARDWARE: %s", err)
	}

	logger.Infof("Transforming VM into template")
	logger.Infof("%s %s", execBin, templateCommand)

	templateCmd := exec.Command(execBin, templateCommand...)
	templateCmd.Env = govcExecEnv

	out, err = templateCmd.CombinedOutput()
	logger.Infof(string(out))
	if err != nil {
		// We don't fail. We log a warning and continue.
		logger.Warnf("ERROR CONVERTING INTO TEMPLATE: %s", err)
	}

	return nil
}
