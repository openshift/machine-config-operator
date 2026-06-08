package extended

import (
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	"github.com/openshift/machine-config-operator/test/extended-priv/util/architecture"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"github.com/tidwall/gjson"
)

const (
	clonedPrefix = "user-data-"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO scale", func() {
	defer g.GinkgoRecover()

	var (
		oc = exutil.NewCLI("mco-scale", exutil.KubeConfigPath())
		// worker MachineConfigPool
		wMcp *MachineConfigPool
	)

	g.JustBeforeEach(func() {
		// Skip if no machineset
		SkipTestIfWorkersCannotBeScaled(oc.AsAdmin())

		wMcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		PreChecks(oc)

		failureHandler := func(message string, callerSkip ...int) {
			logger.Errorf("Gomega assertion failed!")
			logger.Errorf("Failure message: %s", message)

			msList := NewMachineSetList(oc.AsAdmin(), MachineAPINamespace)
			mList := NewMachineList(oc.AsAdmin(), MachineAPINamespace)
			nodeList := NewNodeList(oc.AsAdmin())

			logger.Infof("DEBUGGING NODES")
			nodeList.PrintDebugCommand()
			logger.Infof("\n\n")

			logger.Infof("DEBUGGING MACHINESETS")
			msList.PrintDebugCommand()
			logger.Infof("%s", msList.PrettyString())
			logger.Infof("\n\n")

			logger.Infof("DEBUGGING MACHINESETS")
			mList.PrintDebugCommand()
			logger.Infof("%s", mList.PrettyString())
			logger.Infof("\n\n")

			// We are adding an extra level to the stack here.
			// We adjust it so that the assertions can point to the right line of code
			// What we do with the callerSkip is similar to configuring all assertions with Offset(1) (increasing offset by one)
			if len(callerSkip) == 0 {
				callerSkip = []int{1} // default offset should be 1 with this failureHandler wrapper
			}

			// Increment the first value to account for this wrapper (increase the offset)
			callerSkip[0]++

			// Fail executing ginkgo failhandler
			g.Fail(message, callerSkip...)
		}

		o.RegisterFailHandler(failureHandler)

	})

	// OCPBUGS-86332: Starting in 4.22 we will no longer fix any bootimage related bugs from bootimages earlier than 4.13
	// Removed tests: PolarionID:63894 (4.1), PolarionID:77051 (4.3), PolarionID:76471 (4.12), PolarionID:52822 (4.5)

	// 4.13 is the oldest boot image supported. Starting in 4.22 we will no longer fix any bootimage related bugs from bootimages earlier than 4.13
	g.It("[PolarionID:89097][OTP] Scaleup using 4.13 cloud image", g.Label("Platform:aws", "Platform:gce", "Platform:vsphere"), func() {
		var (
			imageVersion = "4.13"
			numNewNodes  = 1 // the number of nodes scaled up in the new Machineset
		)

		skipTestIfSupportedPlatformNotMatched(oc, AWSPlatform, GCPPlatform, VspherePlatform)

		SimpleScaleUPTest(oc, wMcp, imageVersion, getUserDataIgnitionVersionFromOCPVersion(imageVersion), numNewNodes)
	})

	g.It("[PolarionID:65923][OTP] SSH key in scaled clusters", func() {

		// It is a safe assumpion that all the tested clusters will have a sshkey deployed in it.
		// If at any moment this assumption is not safe anymore, we need to check for the sshkey to exist
		// and create a MC to deploy a sskey in case of no sshkey deployed
		var (
			initialNumWorkers = len(wMcp.GetNodesOrFail())
			numNewNodes       = 1
		)

		defer wMcp.waitForComplete()

		exutil.By("Scale up a machineset")
		allMs, err := NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAll()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting a list of MachineSet resources")
		ms := allMs[0]

		initialMsNodes, err := ms.GetNodes()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting a list of nodes that belong to machineset %s", ms.GetName())

		initialNumMsNodes := len(initialMsNodes)

		logger.Infof("Scaling up machineset %s by 1", ms.GetName())
		defer func() { _ = ms.ScaleTo(initialNumMsNodes) }()
		o.Expect(ms.ScaleTo(initialNumMsNodes+numNewNodes)).NotTo(
			o.HaveOccurred(),
			"Error scaling up MachineSet %s", ms.GetName())

		logger.Infof("OK!\n")

		logger.Infof("Waiting %s machineset for being ready", ms)
		o.Eventually(ms.GetIsReady, "20m", "2m").Should(o.BeTrue(), "MachineSet %s is not ready", ms.GetName())
		logger.Infof("OK!\n")

		exutil.By("Check that worker pool is increased and updated")
		o.Eventually(wMcp.GetNodesOrFail, "5m", "30s").Should(o.HaveLen(initialNumWorkers+numNewNodes),
			"The worker pool has not added the new nodes created by the new Machineset.\n%s", wMcp.PrettyString())
		wMcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check that the sshkey exists in all nodes")
		currentWorkers := wMcp.GetNodesOrFail()
		for _, node := range currentWorkers {
			logger.Infof("Checking sshkey in node %s", node.GetName())
			remoteSSHKey := NewRemoteFile(node, "/home/core/.ssh/authorized_keys.d/ignition")
			o.Expect(remoteSSHKey.Fetch()).To(o.Succeed(),
				"Error getting the content of the sshkey file in node %s", node.GetName())

			o.Expect(remoteSSHKey.GetTextContent()).NotTo(o.BeEmpty(),
				"The sshkey file has no content in node %s", node.GetName())
			logger.Infof("Sshkey is OK in node %s", node.GetName())
		}
		logger.Infof("OK!\n")

	})

	g.It("[PolarionID:64623][OTP] Machine Config Server CA rotation. IPI.", func() {
		var (
			initialNumWorkers = len(wMcp.GetNodesOrFail())
			numNewNodes       = 1
		)

		// skip the test if fips is not enabled
		skipTestIfFIPSIsNotEnabled(oc)

		defer wMcp.waitForComplete()

		exutil.By("Rotate MCS certificate")
		initialMCSPods, err := GetMCSPodNames(oc.AsAdmin())

		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting MCS pod names")

		logger.Infof("Current MCS pod names: %s", initialMCSPods)

		o.Expect(
			RotateMCSCertificates(oc.AsAdmin()),
			//	oc.AsAdmin().WithoutNamespace().Run("adm").Args("ocp-certificates", "regenerate-machine-config-server-serving-cert").Execute(),
		).To(o.Succeed(),
			"Error rotating MCS certificates")

		logger.Infof("OK!\n")

		exutil.By("Check that MCS pods were restarted")
		o.Eventually(func(gm o.Gomega) {

			// for debugging purposes
			logger.Infof("Waiting for MCS pods to be restarted")
			_ = oc.AsAdmin().WithoutNamespace().Run("get").Args("pods", "-n", MachineConfigNamespace).Execute()

			currentMCSPods, err := GetMCSPodNames(oc.AsAdmin())

			gm.Expect(err).NotTo(o.HaveOccurred(),
				"Error getting MCS pod names")

			for _, initialMCSPod := range initialMCSPods {
				gm.Expect(currentMCSPods).NotTo(o.ContainElement(initialMCSPod),
					"MCS pod %s was not restarted after certs rotation", initialMCSPod)
			}

		}, "5m", "20s",
		).Should(o.Succeed(),
			"The MCS pods were not restarted after the MCS certificates were rotated")

		logger.Infof("OK!\n")

		exutil.By("Check that new machine-config-server-tls and machine-config-server-ca secrets are created")
		tlsSecret := NewSecret(oc.AsAdmin(), MachineConfigNamespace, "machine-config-server-tls")
		caSecret := NewSecret(oc.AsAdmin(), MachineConfigNamespace, "machine-config-server-ca")

		o.Eventually(tlsSecret, "30s", "5s").Should(Exist(),
			"%s secret does not exist in the MCO namespace after MCS cert rotations", tlsSecret.GetName())

		o.Eventually(caSecret, "30s", "5s").Should(Exist(),
			"%s secret does not exist in the MCO namespace after MCS cert rotations", tlsSecret.GetName())

		logger.Infof("OK!\n")

		exutil.By("Scale up a machineset")
		allMs, err := NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAll()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting a list of MachineSet resources")
		ms := allMs[0]

		initialMsNodes, err := ms.GetNodes()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting a list of nodes that belong to machineset %s", ms.GetName())

		initialNumMsNodes := len(initialMsNodes)

		logger.Infof("Scaling up machineset %s by 1", ms.GetName())
		defer func() { _ = ms.ScaleTo(initialNumMsNodes) }()
		o.Expect(ms.ScaleTo(initialNumMsNodes+numNewNodes)).NotTo(
			o.HaveOccurred(),
			"Error scaling up MachineSet %s", ms.GetName())

		logger.Infof("OK!\n")

		logger.Infof("Waiting %s machineset for being ready", ms)
		o.Eventually(ms.GetIsReady, "20m", "2m").Should(o.BeTrue(), "MachineSet %s is not ready", ms.GetName())
		logger.Infof("OK!\n")

		exutil.By("Check that worker pool is increased and updated")
		o.Eventually(wMcp.GetNodesOrFail, "5m", "30s").Should(o.HaveLen(initialNumWorkers+numNewNodes),
			"The worker pool has not added the new nodes created by the new Machineset.\n%s", wMcp.PrettyString())
		wMcp.waitForComplete()
		logger.Infof("All nodes are up and ready!")
		logger.Infof("OK!\n")

	})

	g.It("[PolarionID:73636][OTP] Pinned images in scaled nodes", func() {
		var (
			waitForPinned      = time.Minute * 5
			initialNumWorkers  = len(wMcp.GetNodesOrFail())
			numNewNodes        = 3
			pinnedImageSetName = "tc-73636-pinned-images-scale"
			pinnedImageName    = BusyBoxImage
		)

		exutil.By("Pin images")
		pis, err := CreateGenericPinnedImageSet(oc.AsAdmin(), pinnedImageSetName, wMcp.GetName(), []string{pinnedImageName})
		defer pis.DeleteAndWait(waitForPinned)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating pinnedimageset %s", pis)
		logger.Infof("OK!\n")

		exutil.By("Check that the pool is reporting the right pinnedimageset status")
		o.Expect(wMcp.waitForPinComplete(waitForPinned)).To(o.Succeed(), "Pinned image operation is not completed in %s", wMcp)
		logger.Infof("OK!\n")

		exutil.By("Check that the image was pinned in all nodes")
		for _, node := range wMcp.GetNodesOrFail() {
			rmi := NewRemoteImage(node, pinnedImageName)
			o.Expect(rmi.IsPinned()).To(o.BeTrue(), "%s is not pinned, but it should", rmi)
		}
		logger.Infof("OK!\n")

		exutil.By("Scale up a machineset")
		allMs, err := NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAll()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting a list of MachineSet resources")
		ms := allMs[0]

		initialNumMsNodes := len(ms.GetNodesOrFail())

		logger.Infof("Scaling up machineset %s by %d", ms.GetName(), numNewNodes)
		defer func() {
			_ = ms.ScaleTo(initialNumMsNodes)
			wMcp.waitForComplete()
		}()
		o.Expect(ms.ScaleTo(initialNumMsNodes+numNewNodes)).NotTo(
			o.HaveOccurred(),
			"Error scaling up MachineSet %s", ms.GetName())
		logger.Infof("OK!\n")

		exutil.By("Check that worker pool is increased and updated")
		o.Eventually(wMcp.GetNodesOrFail, "15m", "30s").Should(o.HaveLen(initialNumWorkers+numNewNodes),
			"The worker pool has not added the new nodes created by the new Machineset.\n%s", wMcp.PrettyString())
		wMcp.waitForComplete()
		logger.Infof("All nodes are up and ready!")
		logger.Infof("OK!\n")

		exutil.By("Check that the pool is reporting the right pinnedimageset status")
		o.Expect(wMcp.waitForPinComplete(waitForPinned)).To(o.Succeed(), "Pinned image operation is not completed in %s", wMcp)
		logger.Infof("OK!\n")

		exutil.By("Check that the image was pinned in all nodes")
		for _, node := range wMcp.GetNodesOrFail() {
			rmi := NewRemoteImage(node, pinnedImageName)
			o.Expect(rmi.IsPinned()).To(o.BeTrue(), "%s is not pinned, but it should", rmi)
		}
		logger.Infof("OK!\n")
	})
})

func cloneMachineSet(oc *exutil.CLI, ms *MachineSet, newMsName, imageVersion, ignitionVersion string) *MachineSet {
	var (
		newSecretName = getClonedSecretName(newMsName)
		platform      = exutil.CheckPlatform(oc.AsAdmin())
	)

	// Duplicate an existing MachineSet
	exutil.By("Duplicate a MachineSet resource")
	logger.Infof("Create a new machineset that will use base image %s and ignition version %s", imageVersion, ignitionVersion)
	newMs, dErr := ms.Duplicate(newMsName)
	o.Expect(dErr).NotTo(o.HaveOccurred(), "Error duplicating MachineSet %s -n %s", ms.GetName(), ms.GetNamespace())
	logger.Infof("OK!\n")

	// Create a new secret using the given ignition version
	exutil.By(fmt.Sprintf("Create a new secret with %s ignition version", ignitionVersion))
	currentSecret := ms.GetOrFail(`{.spec.template.spec.providerSpec.value.userDataSecret.name}`)
	logger.Infof("Duplicating secret %s with new name %s", currentSecret, newSecretName)

	modifyUserData := func(userData string) (string, error) { return convertUserDataToNewVersion(userData, ignitionVersion) }
	clonedSecret, sErr := duplicateMachinesetSecret(oc, currentSecret, newSecretName, modifyUserData, nil)
	o.Expect(sErr).NotTo(o.HaveOccurred(), "Error duplicating machine-api secret")
	o.Expect(clonedSecret).To(Exist(), "The secret was not duplicated for machineset %s", newMs)
	logger.Infof("OK!\n")

	// Get the right base image name from the rhcos json info stored in the github repositories
	exutil.By(fmt.Sprintf("Get the base image for version %s", imageVersion))
	rhcosHandler, err := GetRHCOSHandler(platform)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the rhcos handler")

	architecture, err := ms.GetArchitecture()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the arechitecture from machineset %s", ms.GetName())

	baseImage, err := rhcosHandler.GetBaseImageFromRHCOSImageInfo(imageVersion, architecture, getCurrentRegionOrFail(oc.AsAdmin()))
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the base image")
	logger.Infof("Using base image %s", baseImage)

	baseImageURL, err := rhcosHandler.GetBaseImageURLFromRHCOSImageInfo(imageVersion, architecture)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the base image URL")

	// In vshpere we will upload the image. To avoid collisions we will add prefix to identify our image
	if platform == VspherePlatform {
		baseImage = "mcotest-" + baseImage
	}
	o.Expect(
		uploadBaseImageToCloud(oc, platform, baseImageURL, baseImage),
	).To(o.Succeed(), "Error uploading the base image %s to the cloud", baseImageURL)
	logger.Infof("OK!\n")

	// Set the new boot base image
	exutil.By(fmt.Sprintf("Configure the duplicated MachineSet to use the %s boot image", baseImage))
	o.Expect(newMs.SetCoreOsBootImage(baseImage)).To(o.Succeed(),
		"There was an error while patching the new base image in %s", newMs)
	logger.Infof("OK!\n")

	// Use new secret
	exutil.By("Configure the duplicated MachineSet to use the new secret")
	o.Expect(newMs.SetUserDataSecret(newSecretName)).To(o.Succeed(),
		"Error patching MachineSet %s to use the new secret %s", newMs.GetName(), newSecretName)
	logger.Infof("OK!\n")

	return newMs
}

func removeClonedMachineSet(ms *MachineSet, mcp *MachineConfigPool, expectedNumWorkers int) {
	if ms.Exists() {
		logger.Infof("Scaling %s machineset to zero", ms.GetName())
		o.Expect(ms.ScaleTo(0)).To(o.Succeed(),
			"Error scaling MachineSet %s to 0", ms.GetName())

		logger.Infof("Waiting %s machineset for being ready", ms.GetName())
		o.Eventually(ms.GetIsReady, "2m", "15s").Should(o.BeTrue(), "MachineSet %s is not ready", ms.GetName())

		logger.Infof("Removing %s machineset", ms.GetName())
		o.Expect(ms.Delete()).To(o.Succeed(),
			"Error deleting MachineSet %s", ms.GetName())

		if expectedNumWorkers >= 0 {
			exutil.By("Check that worker pool is increased and updated")
			// Before calling mcp.GetNodes we wait for the MachineCount number to settle, to avoid a panic due to nodes disappearing while we calculate the number of nodes
			o.Eventually(mcp.getMachineCount, "5m", "30s").Should(o.Equal(expectedNumWorkers),
				"The MachineCount has not the expected value in pool:\n%s", mcp.PrettyString())
			o.Eventually(mcp.GetNodes, "5m", "30s").Should(o.HaveLen(expectedNumWorkers),
				"The number of nodes is not the expected one in pool:\n%s", mcp.PrettyString())
		}
	}

	clonedSecret := NewSecret(ms.oc, MachineAPINamespace, getClonedSecretName(ms.GetName()))
	if clonedSecret.Exists() {
		logger.Infof("Removing %s secret", clonedSecret)
		o.Expect(clonedSecret.Delete()).To(o.Succeed(),
			"Error deleting  %s", ms.GetName())
	}
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

// getCurrentRegionOrFail returns the current region if we are in AWS or an empty string if any other platform
func getCurrentRegionOrFail(oc *exutil.CLI) string {
	infra := NewResource(oc.AsAdmin(), "infrastructure", "cluster")
	return infra.GetOrFail(`{.status.platformStatus.aws.region}`)
}

// SimpleScaleUPTest is a generic function that tests scaling up and down worker nodes using the base image corresponding to the given version
func SimpleScaleUPTest(oc *exutil.CLI, mcp *MachineConfigPool, imageVersion, ignitionVersion string, numNewNodes int) {

	var (
		newMsName            = fmt.Sprintf("mco-tc-%s-cloned", GetCurrentTestPolarionIDNumber())
		initialNumWorkers    = len(mcp.GetNodesOrFail())
		machineConfiguration = GetMachineConfiguration(oc.AsAdmin())
	)

	if IsBootImageUpdateSupported(oc.AsAdmin()) {

		defer machineConfiguration.SetSpec(machineConfiguration.GetSpecOrFail())
		exutil.By("Disabling skew functionality")
		DisableSkew(machineConfiguration)
		logger.Infof("OK!\n")

		exutil.By("Opt-out boot images update")
		logger.Infof("Disabling the bootimages update so that our images are not overridden by MCO")
		o.Expect(
			machineConfiguration.SetNoneManagedBootImagesConfig(MachineSetResource),
		).To(o.Succeed(), "Error configuring None managedBootImages in the 'cluster' MachineConfiguration resource")
		logger.Infof("OK!\n")
	}

	defer func() {
		logger.Infof("Start TC defer block")
		newMs := NewMachineSet(oc.AsAdmin(), MachineAPINamespace, newMsName)
		errors := o.InterceptGomegaFailures(func() { removeClonedMachineSet(newMs, mcp, initialNumWorkers) }) // We don't want gomega to fail and stop the deferred cleanup process
		if len(errors) != 0 {
			logger.Infof("There were errors restoring the original MachineSet resources in the cluster")
			for _, e := range errors {
				logger.Errorf(e)
			}
		}

		// We don't want the test to pass if there were errors while restoring the initial state
		o.Expect(len(errors)).To(o.BeZero(),
			"There were %d errors while recovering the cluster's initial state", len(errors))

		logger.Infof("End TC defer block")
	}()

	logger.Infof("Create a new MachineSet using the right base image")
	allMs, err := NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAll()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting a list of MachineSet resources")
	ms := allMs[0]
	newMs := cloneMachineSet(oc.AsAdmin(), ms, newMsName, imageVersion, ignitionVersion)

	exutil.By("Scale MachineSet up")
	logger.Infof("Scaling up machineset %s", newMs.GetName())
	scaleErr := newMs.ScaleTo(numNewNodes)
	o.Expect(scaleErr).NotTo(o.HaveOccurred(), "Error scaling up MachineSet %s", newMs.GetName())

	logger.Infof("Waiting %s machineset for being ready", newMsName)
	o.Eventually(newMs.GetIsReady, "20m", "2m").Should(o.BeTrue(), "MachineSet %s is not ready", newMs.GetName())
	logger.Infof("OK!\n")

	exutil.By("Check that worker pool is increased and updated")
	o.Eventually(mcp.GetNodesOrFail, "5m", "30s").Should(o.HaveLen(initialNumWorkers+numNewNodes),
		"The worker pool has not added the new nodes created by the new Machineset.\n%s", mcp.PrettyString())
	mcp.waitForComplete()
	logger.Infof("OK!\n")

	exutil.By("Scale down and remove the cloned Machineset")
	removeClonedMachineSet(newMs, mcp, initialNumWorkers)
	logger.Infof("OK!\n")

}

func getClonedSecretName(msName string) string {
	return clonedPrefix + msName
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
		return nil, fmt.Errorf("platform %s is not supported and cannot get RHCOSHandler", platform)
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
		return "", fmt.Errorf("region cannot have an empty value when we try to get the base image in platform %s", platform)
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
		return "", fmt.Errorf("could not find the base image for version <%s> in platform <%s> architecture <%s> and region <%s> with path %s",
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

	if CompareVersions(version, "=", "4.1") {
		return "", fmt.Errorf("there is no image base image supported for platform %s in version %s", platform, version)
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
		return "", fmt.Errorf("could not find the base image for version <%s> in platform <%s> architecture <%s> and region <%s> with path %s",
			version, platform, arch, region, imagePath)
	}

	logger.Infof("Looking for rhcos base image project in path %s", projectPath)
	project := gjson.Get(rhcosImageInfo, projectPath)
	if !project.Exists() {
		logger.Infof("rhcos info:\n%s", rhcosImageInfo)
		return "", fmt.Errorf("could not find the project where the base image is stored with version <%s> in platform <%s> architecture <%s> and region <%s> with path %s",
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
		return "", fmt.Errorf("could not find the base image for version <%s> in platform <%s> architecture <%s> and format <%s> with path %s",
			version, platform, stringArch, format, imagePath)
	}

	if !olderThan410 {
		return baseImageURL.String(), nil
	}

	logger.Infof("Looking for baseURL in path %s", baseURIPath)
	baseURI := gjson.Get(rhcosImageInfo, baseURIPath)
	if !baseURI.Exists() {
		logger.Infof("rhcos info:\n%s", rhcosImageInfo)
		return "", fmt.Errorf("could not find the base URI with version <%s> in platform <%s> architecture <%s> and format <%s> with path %s",
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
		vsInfo, err := exutil.GetVSphereConnectionInfo(oc.AsAdmin())
		if err != nil {
			return err
		}

		err = exutil.UploadBaseImageToVsphere(baseImageURL, baseImage, vsInfo)
		if err != nil {
			return err
		}

		return nil
	default:
		return fmt.Errorf("platform %s is not supported, base image cannot be updloaded", platform)
	}
}

func IsBootImageUpdateSupported(oc *exutil.CLI) bool {
	var (
		platform = exutil.CheckPlatform(oc.AsAdmin())
	)
	return platform == GCPPlatform || platform == AWSPlatform
}
