package extended

import (
	"fmt"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	"github.com/openshift/machine-config-operator/test/extended-priv/util/architecture"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/disruptive][Serial][Disruptive] MCO osImageStream", func() {
	defer g.GinkgoRecover()

	var (
		oc = exutil.NewCLI("mco-osimagestream", exutil.KubeConfigPath())
		// Compact compatible MCP. If the node is compact/SNO this variable will be the master pool, else it will be the worker pool
		mcp  *MachineConfigPool
		osis *OSImageStream
	)

	g.JustBeforeEach(func() {
		SkipIfNoFeatureGate(oc.AsAdmin(), "OSStreams")
		mcp = GetCompactCompatiblePool(oc.AsAdmin())
		osis = NewOSImageStream(oc.AsAdmin())
		PreChecks(oc)
		osis.LogStreamInfo()
	})

	// AI-assisted: Test case to validate osImageStream configuration in MachineConfigPools
	g.It("[PolarionID:86924][OTP] Validate OS Image Streams value", func() {
		var (
			testID               = GetCurrentTestPolarionIDNumber()
			infraMcpName         = fmt.Sprintf("infra-tc-%s", testID)
			invalidStreamName    = "rhel-10-invalid"
			emptyStreamName      = ""
			expectedInvalidError = "The specified osImageStream name must reference a valid stream from the cluster OSImageStream resource"
			expectedEmptyError   = "spec.osImageStream.name in body should be at least 1 chars long"
		)

		defer mcp.SetSpec(mcp.GetSpecOrFail())

		exutil.By("Validate OSImageStream resource contains expected streams")
		o.Eventually(osis.GetDefaultStream, "2m", "20s").Should(o.Equal(DefaultOSImageStream),
			"Expected default stream to be '%s'", DefaultOSImageStream)
		logger.Infof("Default stream is correctly set to '%s'", DefaultOSImageStream)

		o.Eventually(osis.GetAvailableStreamNames, "2m", "20s").Should(o.ConsistOf(SupportedOSImageStreams),
			"Available streams should match supported streams %v", SupportedOSImageStreams)
		logger.Infof("Available streams match expected streams %v", SupportedOSImageStreams)
		logger.Infof("OK!\n")

		exutil.By("Configure invalid osImageStream")
		err := mcp.SetOsImageStream(invalidStreamName)
		o.Expect(err).To(o.HaveOccurred(), "Expected error when configuring invalid osImageStream '%s' but command succeeded", invalidStreamName)
		o.Expect(err).To(o.BeAssignableToTypeOf(&exutil.ExitError{}), "Unexpected error while patching %s with invalid osImageStream. %s", mcp, err)
		o.Expect(err.(*exutil.ExitError).StdErr).Should(o.ContainSubstring(expectedInvalidError),
			"Unexpected error message when patching %s with invalid osImageStream '%s'", mcp, invalidStreamName)
		logger.Infof("OK!\n")

		exutil.By("Configure empty osImageStream")
		err = mcp.SetOsImageStream(emptyStreamName)
		o.Expect(err).To(o.HaveOccurred(), "Expected error when configuring empty osImageStream but command succeeded")
		o.Expect(err).To(o.BeAssignableToTypeOf(&exutil.ExitError{}), "Unexpected error while patching %s with empty osImageStream. %s", mcp, err)
		o.Expect(err.(*exutil.ExitError).StdErr).Should(o.ContainSubstring(expectedEmptyError),
			"Unexpected error message when patching %s with empty osImageStream", mcp)
		logger.Infof("OK!\n")

		exutil.By("Create custom MCP with invalid osImageStream")
		defer DeleteCustomMCP(oc.AsAdmin(), infraMcpName)

		err = NewMCOTemplate(oc.AsAdmin(), "custom-machine-config-pool-osimagestream.yaml").Create(
			"-p", fmt.Sprintf("NAME=%s", infraMcpName),
			"-p", fmt.Sprintf("OSIMAGESTREAM=%s", invalidStreamName),
		)
		o.Expect(err).To(o.HaveOccurred(), "Expected error when creating MCP with invalid osImageStream '%s' but command succeeded", invalidStreamName)
		o.Expect(err).To(o.BeAssignableToTypeOf(&exutil.ExitError{}), "Unexpected error while creating MCP with invalid osImageStream. %s", err)
		o.Expect(err.(*exutil.ExitError).StdErr).Should(o.ContainSubstring(expectedInvalidError),
			"Unexpected error message when creating MCP with invalid osImageStream '%s'", invalidStreamName)
		logger.Infof("OK!\n")
	})

	// AI-assisted: Test case to validate default OS Image Stream is used by MCPs
	g.It("[PolarionID:86495][OTP] Check default OS Image Stream", func() {
		exutil.By("Get the default osImageStream")
		defaultStream, err := osis.GetDefaultStream()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting default stream from %s", osis)
		logger.Infof("Default OS Image Stream: %s", defaultStream)
		logger.Infof("OK!\n")

		exutil.By("Check that MCPs with no .spec.osImageStream configured use the default stream")
		allMcps := NewMachineConfigPoolList(oc.AsAdmin()).GetAllOrFail()
		for _, mcpItem := range allMcps {
			// Get the osImageStream spec value using the new method
			osImageStreamSpec, err := mcpItem.GetOsImageStream()
			o.Expect(err).NotTo(o.HaveOccurred(), "Error getting osImageStream spec from %s", mcpItem)

			// Skip MCPs that have an explicit osImageStream configured
			if osImageStreamSpec != "" {
				logger.Infof("Skipping %s - has explicit osImageStream: %s", mcpItem, osImageStreamSpec)
				continue
			}

			// Get the nodes in the pool
			nodes, err := mcpItem.GetNodes()
			o.Expect(err).NotTo(o.HaveOccurred(), "Error getting nodes from %s", mcpItem)
			if len(nodes) == 0 {
				logger.Infof("Skipping %s - no nodes in pool", mcpItem)
				continue
			}

			validateOsImageStreamInPool(mcpItem, osis, defaultStream)
		}
		logger.Infof("OK!\n")
	})

	// AI-assisted: Test case to validate real-time kernel configuration across OS image streams
	g.It("[PolarionID:87095][OTP] Realtime kernel from rhel9 stream to rhel10 stream [Disruptive]", g.Label("Platform:aws", "Platform:gce"), func() {
		architecture.SkipIfNoNodeWithArchitectures(oc.AsAdmin(), architecture.AMD64)
		skipTestIfSupportedPlatformNotMatched(oc.AsAdmin(), AWSPlatform, GCPPlatform)

		testID := GetCurrentTestPolarionIDNumber()
		createdCustomPoolName := fmt.Sprintf("tc-%s-%s", testID, architecture.AMD64)
		defer DeleteCustomMCP(oc.AsAdmin(), createdCustomPoolName)

		mcp, _ := GetPoolAndNodesForArchitectureOrFail(oc.AsAdmin(), createdCustomPoolName, architecture.AMD64, 1)
		testKernelTypeAcrossOSImageStreams(oc, osis, mcp, testID, KernelTypeRealtime, "set-realtime-kernel.yaml")
	})

	// AI-assisted: Test case to validate 64k-pages kernel configuration across OS image streams
	g.It("[PolarionID:87096][OTP] 64k pages kernel from rhel9 stream to rhel10 stream [Disruptive]", g.Label("NoPlatform:gce"), func() {
		architecture.SkipIfNoNodeWithArchitectures(oc.AsAdmin(), architecture.ARM64)
		skipTestIfNotSupportedPlatform(oc.AsAdmin(), GCPPlatform)

		testID := GetCurrentTestPolarionIDNumber()
		createdCustomPoolName := fmt.Sprintf("tc-%s-%s", testID, architecture.ARM64)
		defer DeleteCustomMCP(oc.AsAdmin(), createdCustomPoolName)

		mcp, _ := GetPoolAndNodesForArchitectureOrFail(oc.AsAdmin(), createdCustomPoolName, architecture.ARM64, 1)
		testKernelTypeAcrossOSImageStreams(oc, osis, mcp, testID, KernelType64kPages, "set-64k-pages-kernel.yaml")
	})

	// AI-assisted: Test case to validate extensions configuration survives osImageStream changes
	g.It("[PolarionID:87097][OTP] Extensions from rhel9 stream to rhel10 stream [Disruptive]", func() {
		var (
			testID = GetCurrentTestPolarionIDNumber()
			mcName = fmt.Sprintf("tc-%s-extensions", testID)
		)

		mcp, cleanup, err := GetCompactCompatibleOrCustomPool(oc.AsAdmin(), 1)
		defer cleanup()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting pool for testing")

		defer mcp.waitForComplete()
		defer mcp.SetSpec(mcp.GetSpecOrFail())

		exutil.By("Verify rhel-9 and rhel-10 streams are available")
		o.Eventually(osis.GetAvailableStreamNames, "2m", "20s").Should(o.ContainElements(OSImageStreamRHEL9, OSImageStreamRHEL10),
			"Both streams '%s' and '%s' should be available", OSImageStreamRHEL9, OSImageStreamRHEL10)
		logger.Infof("Both rhel-9 and rhel-10 streams are available")
		logger.Infof("OK!\n")

		exutil.By("Check current osImageStream and configure rhel-9 if necessary")
		currentStream, err := GetEffectiveOsImageStream(mcp)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting effective osImageStream from %s", mcp)
		logger.Infof("Current stream: '%s'", currentStream)

		if currentStream != OSImageStreamRHEL9 {
			logger.Infof("Configuring osImageStream to '%s' in %s", OSImageStreamRHEL9, mcp)
			o.Expect(mcp.SetOsImageStream(OSImageStreamRHEL9)).To(o.Succeed(), "Error setting osImageStream to '%s' in %s", OSImageStreamRHEL9, mcp)
			mcp.waitForComplete()
			logger.Infof("OK!\n")
		} else {
			logger.Infof("%s is already using rhel-9 stream", mcp)
			logger.Infof("OK!\n")
		}

		validateOsImageStreamInPool(mcp, osis, OSImageStreamRHEL9)

		exutil.By("Get extensions compatible with rhel-10 stream")
		fips := isFIPSEnabledInClusterConfig(oc.AsAdmin())
		armNodes, err := mcp.GetNodesByArchitecture(architecture.ARM64)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the list of ARM nodes in %s", mcp)
		_, rhel10CompatibleExtensions, expectedRpmPackages := FilterExtensions(AllExtenstions, len(armNodes) > 0, fips, OSImageStreamRHEL10)
		logger.Infof("Extensions compatible with rhel-10: %v", rhel10CompatibleExtensions)
		logger.Infof("Expected RPM packages: %v", expectedRpmPackages)
		logger.Infof("OK!\n")

		exutil.By(fmt.Sprintf("Configure rhel-10 compatible extensions in %s while on rhel-9", mcp))
		mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName())
		mc.parameters = []string{fmt.Sprintf(`EXTENSIONS=%s`, string(MarshalOrFail(rhel10CompatibleExtensions)))}
		mc.skipWaitForMcp = true

		defer mc.DeleteWithWait()
		mc.create()
		logger.Infof("OK!\n")

		exutil.By(fmt.Sprintf("Wait for %s to be updated after applying extensions", mcp))
		mcp.SetWaitingTimeForExtensionsChange()
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Verify rhel-10 compatible extensions are installed on rhel-9")
		firstNode := mcp.GetSortedNodesOrFail()[0]
		CheckExtensions(firstNode, rhel10CompatibleExtensions)
		logger.Infof("OK!\n")

		exutil.By(fmt.Sprintf("Configure osImageStream to '%s' in %s", OSImageStreamRHEL10, mcp))
		o.Expect(mcp.SetOsImageStream(OSImageStreamRHEL10)).To(o.Succeed(), "Error setting osImageStream to '%s' in %s", OSImageStreamRHEL10, mcp)
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Verify rhel-10 stream is configured and extensions are still installed")
		validateOsImageStreamInPool(mcp, osis, OSImageStreamRHEL10)

		CheckExtensions(firstNode, rhel10CompatibleExtensions)
		logger.Infof("Extensions are still correctly installed after switching to rhel-10")
		logger.Infof("OK!\n")

		exutil.By("Remove the extensions configuration")
		mc.DeleteWithWait()
		logger.Infof("OK!\n")

		exutil.By("Verify extensions are uninstalled and rhel-10 stream is still configured")
		for _, pkg := range expectedRpmPackages {
			o.Expect(firstNode.RpmIsInstalled(pkg)).To(
				o.BeFalse(),
				"Package %s should be uninstalled when we remove the extensions MC", pkg)
		}
		logger.Infof("All extension packages are correctly uninstalled")

		validateOsImageStreamInPool(mcp, osis, OSImageStreamRHEL10)
		logger.Infof("%s is still correctly using rhel-10 stream", firstNode)
		logger.Infof("OK!\n")
	})
})

// GetEffectiveOsImageStream returns the effective osImageStream for an MCP
func GetEffectiveOsImageStream(mcp *MachineConfigPool) (string, error) {

	osis := NewOSImageStream(mcp.GetOC().AsAdmin())
	if !osis.Exists() {
		logger.Infof("OSImageStream resource does not exist. Likely, no tech preview, by default we use osstream: %s", DefaultOSImageStream)
		return DefaultOSImageStream, nil
	}

	streamName, err := mcp.GetOsImageStream()
	if err != nil {
		return "", err
	}
	logger.Infof("Initial osImageStream configured: '%s'", streamName)

	// If no stream is configured, use the default stream
	if streamName == "" {
		streamName, err = osis.GetDefaultStream()
		if err != nil {
			return "", err
		}
		logger.Infof("No osImageStream configured, using default stream: '%s'", streamName)
	}

	return streamName, nil
}

// validateOsImageStreamInPool validates that a pool is using the expected osImage for a stream
func validateOsImageStreamInPool(mcp *MachineConfigPool, osis *OSImageStream, streamName string) {
	exutil.By(fmt.Sprintf("Validate that %s is using osImageStream '%s'", mcp, streamName))

	expectedOsImage, err := osis.GetOsImageByName(streamName)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting osImage for stream '%s'", streamName)

	firstNode := mcp.GetSortedNodesOrFail()[0]
	logger.Infof("Checking osImage on node %s", firstNode.GetName())

	o.Eventually(firstNode.GetCurrentBootOSImage, "2m", "20s").Should(o.Equal(expectedOsImage),
		"%s should be using osImage %s from stream '%s'",
		firstNode, expectedOsImage, streamName)

	logger.Infof("%s is correctly using osImage from stream '%s'", firstNode, streamName)

	// TODO: Uncomment when status.osImageStream is populated by MCO
	// logger.Infof("Checking MCP status.osImageStream")
	// o.Eventually(mcp.GetStatusOsImageStream, "2m", "20s").Should(o.Equal(streamName),
	// 	"%s should report stream '%s' in status.osImageStream",
	// 	mcp, streamName)
	// logger.Infof("%s correctly reports osImageStream '%s' in status", mcp, streamName)

	// If stream name is of form "rhel-X", validate RHEL version starts with X
	var expectedMajorVersion string
	if _, err := fmt.Sscanf(streamName, "rhel-%s", &expectedMajorVersion); err == nil && expectedMajorVersion != "" {
		logger.Infof("Checking RHEL version starts with '%s' to match stream name pattern", expectedMajorVersion)
		o.Eventually(firstNode.GetRHELVersion, "2m", "20s").Should(o.HavePrefix(expectedMajorVersion+"."),
			"%s should be running RHEL version starting with %s to match stream '%s'",
			firstNode, expectedMajorVersion, streamName)
		logger.Infof("%s is correctly running RHEL version starting with %s", firstNode, expectedMajorVersion)
	} else {
		logger.Infof("Skipping RHEL version check - stream name '%s' doesn't match 'rhel-X' pattern", streamName)
	}

	logger.Infof("OK!\n")
}

// testKernelTypeAcrossOSImageStreams tests kernel type configuration across OS image streams
func testKernelTypeAcrossOSImageStreams(oc *exutil.CLI, osis *OSImageStream, mcp *MachineConfigPool, testID string, kernelType KernelType, mcTemplate string) {
	mcName := fmt.Sprintf("tc-%s-%s-kernel", testID, kernelType)

	defer mcp.waitForComplete()
	defer mcp.SetSpec(mcp.GetSpecOrFail())

	exutil.By("Verify rhel-9 and rhel-10 streams are available")
	o.Eventually(osis.GetAvailableStreamNames, "2m", "20s").Should(o.ContainElements(OSImageStreamRHEL9, OSImageStreamRHEL10),
		"Both streams '%s' and '%s' should be available", OSImageStreamRHEL9, OSImageStreamRHEL10)
	logger.Infof("Both rhel-9 and rhel-10 streams are available")
	logger.Infof("OK!\n")

	exutil.By("Check current osImageStream and configure rhel-9 if necessary")
	currentStream, err := GetEffectiveOsImageStream(mcp)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting effective osImageStream from %s", mcp)
	logger.Infof("Current stream: '%s'", currentStream)

	if currentStream != OSImageStreamRHEL9 {
		logger.Infof("Configuring osImageStream to '%s' in %s", OSImageStreamRHEL9, mcp)
		o.Expect(mcp.SetOsImageStream(OSImageStreamRHEL9)).To(o.Succeed(), "Error setting osImageStream to '%s' in %s", OSImageStreamRHEL9, mcp)
		mcp.waitForComplete()
		logger.Infof("OK!\n")
	} else {
		logger.Infof("%s is already using rhel-9 stream", mcp)
		logger.Infof("OK!\n")
	}

	validateOsImageStreamInPool(mcp, osis, OSImageStreamRHEL9)

	exutil.By(fmt.Sprintf("Configure %s kernel in %s", kernelType, mcp))
	mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName())
	mc.SetMCOTemplate(mcTemplate)
	mc.skipWaitForMcp = true

	defer mc.DeleteWithWait()
	mc.create()
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Wait for %s to be updated after applying %s kernel", mcp, kernelType))
	mcp.SetWaitingTimeForKernelChange()
	mcp.waitForComplete()
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Verify %s kernel is configured", kernelType))
	firstNode := mcp.GetSortedNodesOrFail()[0]
	o.Eventually(firstNode.IsKernelType, "2m", "20s").WithArguments(kernelType).Should(o.BeTrue(),
		"%s kernel should be active on %s", kernelType, firstNode)
	logger.Infof("%s kernel is correctly configured on %s", kernelType, firstNode)
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Configure osImageStream to '%s' in %s", OSImageStreamRHEL10, mcp))
	o.Expect(mcp.SetOsImageStream(OSImageStreamRHEL10)).To(o.Succeed(), "Error setting osImageStream to '%s' in %s", OSImageStreamRHEL10, mcp)
	mcp.waitForComplete()
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Verify rhel-10 stream is configured and %s kernel is still active", kernelType))
	validateOsImageStreamInPool(mcp, osis, OSImageStreamRHEL10)

	o.Eventually(firstNode.IsKernelType, "2m", "20s").WithArguments(kernelType).Should(o.BeTrue(),
		"%s kernel should still be active on %s after switching to rhel-10", kernelType, firstNode)
	logger.Infof("%s kernel is still correctly configured on %s", kernelType, firstNode)
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Remove the %s kernel configuration", kernelType))
	mc.DeleteWithWait()
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Verify %s kernel is not active and rhel-10 stream is still configured", kernelType))
	o.Eventually(firstNode.IsKernelType, "2m", "20s").WithArguments(kernelType).Should(o.BeFalse(),
		"%s kernel should not be active on %s", kernelType, firstNode)
	logger.Infof("%s kernel is correctly removed from %s", kernelType, firstNode)

	validateOsImageStreamInPool(mcp, osis, OSImageStreamRHEL10)
	logger.Infof("%s is still correctly using rhel-10 stream", firstNode)
	logger.Infof("OK!\n")
}
