package extended

import (
	"os"
	"path/filepath"
	"regexp"

	exutil "github.com/openshift/machine-config-operator/test/extended/util"
	logger "github.com/openshift/machine-config-operator/test/extended/util/logext"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
)

var _ = g.Describe("[sig-mco] MCO Layering", func() {
	defer g.GinkgoRecover()

	var (
		// init cli object, temp namespace contains prefix mco.
		// tip: don't put this in BeforeEach/JustBeforeEach, you will get error
		// "You may only call AfterEach from within a Describe, Context or When"
		oc = exutil.NewCLI("mco-layering", exutil.KubeConfigPath())
		// temp dir to store all test files, and it will be recycled when test is finished
		tmpdir string
	)

	g.JustBeforeEach(func() {
		tmpdir = createTmpDir()
		preChecks(oc)
	})

	g.JustAfterEach(func() {
		os.RemoveAll(tmpdir)
		logger.Infof("test dir %s is cleaned up", tmpdir)
	})

	g.It("Author:sregidor-ConnectedOnly-NonPreRelease-Longduration-Medium-54052-[P2] Not bootable layered osImage provided[Disruptive]", func() {
		var (
			nonBootableImage = "quay.io/openshifttest/hello-openshift:1.2.0"
			layeringMcName   = "not-bootable-image-tc54052"

			expectedNDMessage = ".*failed to update OS to " + regexp.QuoteMeta(nonBootableImage) + ".*error running rpm-ostree rebase.*ostree.bootable.*"
			expectedNDReason  = "1 nodes are reporting degraded status on sync"
		)

		checkInvalidOsImagesDegradedStatus(oc.AsAdmin(), nonBootableImage, layeringMcName, expectedNDMessage, expectedNDReason)
	})
	g.It("Author:sregidor-NonPreRelease-Longduration-Medium-54054-Not pullable layered osImage provided[Disruptive]", func() {
		var (
			nonPullableImage  = "quay.io/openshifttest/tc54054fakeimage:latest"
			layeringMcName    = "not-pullable-image-tc54054"
			expectedNDMessage = ".*" + regexp.QuoteMeta(nonPullableImage) + ".*error"

			expectedNDReason = "1 nodes are reporting degraded status on sync"
		)

		checkInvalidOsImagesDegradedStatus(oc.AsAdmin(), nonPullableImage, layeringMcName, expectedNDMessage, expectedNDReason)
	})
	g.It("Author:sregidor-NonPreRelease-Medium-54049-[P2] Verify base images in the release image", func() {
		var (
			oldMachineConfigOsImage = "machine-os-content"
			coreExtensions          = "rhel-coreos-extensions"
		)

		exutil.By("Extract pull-secret")
		pullSecret := GetPullSecret(oc.AsAdmin())
		// TODO: when the code to create a tmp directory in the beforeEach section is merged, use ExtractToDir method instead
		secretExtractDir, err := pullSecret.Extract()
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error extracting pull-secret")
		logger.Infof("Pull secret has been extracted to: %s\n", secretExtractDir)
		dockerConfigFile := filepath.Join(secretExtractDir, ".dockerconfigjson")

		exutil.By("Get base image for layering")
		baseImage, err := getImageFromReleaseInfo(oc.AsAdmin(), LayeringBaseImageReleaseInfo, dockerConfigFile)
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the base image to build new osImages")
		logger.Infof("Base image: %s\n", baseImage)

		exutil.By("Inspect base image information")
		skopeoCLI := NewSkopeoCLI().SetAuthFile(dockerConfigFile)
		inspectInfo, err := skopeoCLI.Run("inspect").Args("--tls-verify=false", "--config", "docker://"+baseImage).Output()
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error using 'skopeo' to inspect base image %s", baseImage)

		logger.Infof("Check if image is bootable")
		inspectJSON := JSON(inspectInfo)
		ostreeBootable := inspectJSON.Get("config").Get("Labels").Get("ostree.bootable").ToString()
		o.Expect(ostreeBootable).To(o.Equal("true"),
			`The base image %s is expected to be bootable (.config.Labels.ostree\.bootable == "true", but skopeo information says that it is not bootable. %s`,
			baseImage, inspectInfo)
		logger.Infof("OK!\n")

		exutil.By("Verify that old machine config os content is not present in the release info")
		mcOsIMage, _ := getImageFromReleaseInfo(oc.AsAdmin(), oldMachineConfigOsImage, dockerConfigFile)
		o.Expect(mcOsIMage).To(o.ContainSubstring(`no image tag "`+oldMachineConfigOsImage+`" exists`),
			"%s image should not be present in the release image, but we can find it with value %s", oldMachineConfigOsImage, mcOsIMage)
		logger.Infof("OK!\n")

		exutil.By("Verify that new core extensions image is present in the release info")
		coreExtensionsValue, exErr := getImageFromReleaseInfo(oc.AsAdmin(), coreExtensions, dockerConfigFile)
		o.Expect(exErr).NotTo(o.HaveOccurred(),
			"Error getting the new core extensions image")
		o.Expect(coreExtensionsValue).NotTo(o.BeEmpty(),
			"%s image should be present in the release image, but we cannot find it with value %s", coreExtensions)
		logger.Infof("%s is present in the release infor with value %s", coreExtensions, coreExtensionsValue)
		logger.Infof("OK!\n")

	})
})

// oc: the CLI
// image: the layered image that will be configured in the MC
// layeringMcName: the name of the MC
// expectedNDMessage: expected value for the message in the MCP NodeDegraded condition
// expectedNDReason: expected value for the reason in the MCP NodeDegraded condition
func checkInvalidOsImagesDegradedStatus(oc *exutil.CLI, image, layeringMcName, expectedNDMessage, expectedNDReason string) {
	var (
		mcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
	)
	// Create MC and wait for MCP
	layeringMC := NewMachineConfig(oc.AsAdmin(), layeringMcName, mcp.GetName())
	layeringMC.parameters = []string{"OS_IMAGE=" + image}
	layeringMC.skipWaitForMcp = true

	validateMcpNodeDegraded(layeringMC, mcp, expectedNDMessage, expectedNDReason, false)

}

// validate that the machine config 'mc' degrades machineconfigpool 'mcp', due to NodeDegraded error matching expectedNDMessage, expectedNDReason
func validateMcpNodeDegraded(mc *MachineConfig, mcp *MachineConfigPool, expectedNDMessage, expectedNDReason string, checkCODegraded bool) {
	defer func() {
		o.Expect(mcp.RecoverFromDegraded()).To(o.Succeed(), "The MCP could not be recovered from Degraded status")
	}()
	defer o.Eventually(mc.deleteNoWait).Should(o.Succeed(), "Could not delete the offending MC")
	mc.create()
	logger.Infof("OK!\n")

	checkDegraded(mcp, expectedNDMessage, expectedNDReason, "NodeDegraded", checkCODegraded, 2)
}
