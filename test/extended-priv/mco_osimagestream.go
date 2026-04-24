package extended

import (
	"fmt"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	"github.com/openshift/machine-config-operator/test/extended-priv/util/architecture"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/disruptive][Serial][Disruptive][OCPFeatureGate:OSStreams] MCO osImageStream", func() {
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
	g.It("[PolarionID:86924][OTP] Validate OS Image Streams value [apigroup:machineconfiguration.openshift.io]", func() {
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
	g.It("[PolarionID:86495][OTP] Check default OS Image Stream [apigroup:machineconfiguration.openshift.io]", func() {
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
	g.It("[PolarionID:87095][OTP] Realtime kernel from rhel9 stream to rhel10 stream [Disruptive] [apigroup:machineconfiguration.openshift.io]", g.Label("Platform:aws", "Platform:gce"), func() {
		architecture.SkipIfNoNodeWithArchitectures(oc.AsAdmin(), architecture.AMD64)
		skipTestIfSupportedPlatformNotMatched(oc.AsAdmin(), AWSPlatform, GCPPlatform)

		testID := GetCurrentTestPolarionIDNumber()
		createdCustomPoolName := fmt.Sprintf("tc-%s-%s", testID, architecture.AMD64)
		defer DeleteCustomMCP(oc.AsAdmin(), createdCustomPoolName)

		mcp, _ := GetPoolAndNodesForArchitectureOrFail(oc.AsAdmin(), createdCustomPoolName, architecture.AMD64, 1)
		testKernelTypeAcrossOSImageStreams(oc, osis, mcp, testID, KernelTypeRealtime, "set-realtime-kernel.yaml")
	})

	// AI-assisted: Test case to validate 64k-pages kernel configuration across OS image streams
	g.It("[PolarionID:87096][OTP] 64k pages kernel from rhel9 stream to rhel10 stream [Disruptive] [apigroup:machineconfiguration.openshift.io]", g.Label("NoPlatform:gce"), func() {
		architecture.SkipIfNoNodeWithArchitectures(oc.AsAdmin(), architecture.ARM64)
		skipTestIfNotSupportedPlatform(oc.AsAdmin(), GCPPlatform)

		testID := GetCurrentTestPolarionIDNumber()
		createdCustomPoolName := fmt.Sprintf("tc-%s-%s", testID, architecture.ARM64)
		defer DeleteCustomMCP(oc.AsAdmin(), createdCustomPoolName)

		mcp, _ := GetPoolAndNodesForArchitectureOrFail(oc.AsAdmin(), createdCustomPoolName, architecture.ARM64, 1)
		testKernelTypeAcrossOSImageStreams(oc, osis, mcp, testID, KernelType64kPages, "set-64k-pages-kernel.yaml")
	})

	// AI-assisted: Test case to validate extensions configuration survives osImageStream changes
	g.It("[PolarionID:87097][OTP] Extensions from rhel9 stream to rhel10 stream [Disruptive] [apigroup:machineconfiguration.openshift.io]", func() {
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

	g.It("[PolarionID:88122][OTP] Validate osImageStream inheritance for custom MachineConfigPools [Disruptive] [apigroup:machineconfiguration.openshift.io]", func() {
		if IsCompactOrSNOCluster(oc.AsAdmin()) {
			g.Skip("The cluster is SNO/Compact. This test cannot be executed in SNO/Compact clusters")
		}

		var (
			testID    = GetCurrentTestPolarionIDNumber()
			mcp1Name  = fmt.Sprintf("tc-%s-mcp1", testID)
			mcp2Name  = fmt.Sprintf("tc-%s-mcp2", testID)
			infraName = fmt.Sprintf("tc-%s-infra", testID)
			cmcp1Name = fmt.Sprintf("tc-%s-cmcp1", testID)
			cmcp2Name = fmt.Sprintf("tc-%s-cmcp2", testID)
		)

		defer mcp.waitForComplete()
		defer mcp.SetSpec(mcp.GetSpecOrFail())

		exutil.By("Verify rhel-9 and rhel-10 streams are available")
		o.Eventually(osis.GetAvailableStreamNames, "2m", "20s").Should(o.ContainElements(OSImageStreamRHEL9, OSImageStreamRHEL10),
			"Both streams '%s' and '%s' should be available", OSImageStreamRHEL9, OSImageStreamRHEL10)
		logger.Infof("Both rhel-9 and rhel-10 streams are available")
		logger.Infof("OK!\n")

		exutil.By("Case1: Applied below custom MCP to inherit the default value of worker pool")
		currentStream, err := GetEffectiveOsImageStream(mcp)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting effective osImageStream from %s", mcp)
		logger.Infof("Current worker pool stream: '%s'", currentStream)

		if currentStream != OSImageStreamRHEL9 {
			logger.Infof("Configuring osImageStream to '%s' in %s", OSImageStreamRHEL9, mcp)
			o.Expect(mcp.SetOsImageStream(OSImageStreamRHEL9)).To(o.Succeed(), "Error setting osImageStream to '%s' in %s", OSImageStreamRHEL9, mcp)
			mcp.waitForComplete()
		}
		logger.Infof("OK!\n")

		exutil.By("Create custom MCP with rhel-9 configured")
		defer DeleteCustomMCP(oc.AsAdmin(), mcp1Name)
		mcp1, err := CreateCustomMCP(oc.AsAdmin(), mcp1Name, 1)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating custom MCP %s", mcp1Name)
		logger.Infof("OK!\n")

		exutil.By("Check the node uses rhel-9")
		validateOsImageStreamInPool(mcp1, osis, OSImageStreamRHEL9)

		exutil.By("Create custom MCP with rhel-10 configured")
		defer DeleteCustomMCP(oc.AsAdmin(), mcp2Name)
		mcp2, err := CreateCustomMCPWithStream(oc.AsAdmin(), mcp2Name, OSImageStreamRHEL10, 1)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating custom MCP %s with rhel-10", mcp2Name)
		logger.Infof("OK!\n")

		exutil.By("Check the node uses rhel-10")
		validateOsImageStreamInPool(mcp2, osis, OSImageStreamRHEL10)

		exutil.By("Clean up Case 1 MCPs to free nodes for Case 2")
		o.Expect(DeleteCustomMCP(oc.AsAdmin(), mcp1Name)).To(o.Succeed(), "Error deleting MCP %s", mcp1Name)
		o.Expect(DeleteCustomMCP(oc.AsAdmin(), mcp2Name)).To(o.Succeed(), "Error deleting MCP %s", mcp2Name)
		logger.Infof("OK!\n")

		exutil.By("Case2: Create custom mcp infra without osstream field, patch worker to rhel-10, and verify infra inherits rhel-10")

		exutil.By(fmt.Sprintf("Create custom MCP %s without osstream field to test dynamic inheritance", infraName))
		defer DeleteCustomMCP(oc.AsAdmin(), infraName)
		err = NewMCOTemplate(oc.AsAdmin(), "custom-machine-config-pool.yaml").Create(
			"-p", fmt.Sprintf("NAME=%s", infraName),
		)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating custom MCP %s without osstream", infraName)
		infraMcp := NewMachineConfigPool(oc.AsAdmin(), infraName)
		logger.Infof("OK!\n")

		exutil.By("Patch worker pool with rhel-10")
		o.Expect(mcp.SetOsImageStream(OSImageStreamRHEL10)).To(o.Succeed(), "Error setting osImageStream to '%s' in %s", OSImageStreamRHEL10, mcp)
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Add a node to infra pool")
		err = infraMcp.AddNodeToPool(mcp.GetSortedNodesOrFail()[0])
		o.Expect(err).NotTo(o.HaveOccurred(), "Error adding node to infra pool")
		logger.Infof("OK!\n")

		exutil.By("Wait for infra pool to complete update with the new node")
		infraMcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Verify node in infra pool inherited rhel-10 from worker pool")
		validateOsImageStreamInPool(infraMcp, osis, OSImageStreamRHEL10)

		exutil.By("Clean up Case 2 MCP to free nodes for Case 3")
		o.Expect(DeleteCustomMCP(oc.AsAdmin(), infraName)).To(o.Succeed(), "Error deleting MCP %s", infraName)
		logger.Infof("OK!\n")

		exutil.By("Case3: Verify worker pool is on rhel-10, then create cmcp1 (default) and cmcp2 (rhel-9)")
		o.Expect(GetEffectiveOsImageStream(mcp)).To(o.Equal(OSImageStreamRHEL10), "Worker pool should be on rhel-10 from Case 2")

		exutil.By("Create custom mcp cmcp1 with rhel-10 and cmcp2 with rhel-9")
		defer DeleteCustomMCP(oc.AsAdmin(), cmcp1Name)
		cmcp1, err := CreateCustomMCP(oc.AsAdmin(), cmcp1Name, 1)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating custom MCP %s", cmcp1Name)
		logger.Infof("OK!\n")

		defer DeleteCustomMCP(oc.AsAdmin(), cmcp2Name)
		cmcp2, err := CreateCustomMCPWithStream(oc.AsAdmin(), cmcp2Name, OSImageStreamRHEL9, 1)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating custom MCP %s with rhel-9", cmcp2Name)
		logger.Infof("OK!\n")

		exutil.By("Add the node in now created MCP and check the node osstream")
		validateOsImageStreamInPool(cmcp1, osis, OSImageStreamRHEL10)
		validateOsImageStreamInPool(cmcp2, osis, OSImageStreamRHEL9)
	})

	g.It("[PolarionID:88203][OTP] In image-mode, verify the MOSB is triggered when osImageStream is patched [Disruptive] [apigroup:machineconfiguration.openshift.io]", func() {
		if IsCompactOrSNOCluster(oc.AsAdmin()) {
			g.Skip("The cluster is SNO/Compact. This test cannot be executed in SNO/Compact clusters")
		}

		var (
			testID         = GetCurrentTestPolarionIDNumber()
			mcpAndMoscName = fmt.Sprintf("tc-%s-custom3", testID)
		)

		exutil.By("Verify rhel-9 and rhel-10 streams are available")
		o.Eventually(osis.GetAvailableStreamNames, "2m", "20s").Should(o.ContainElements(OSImageStreamRHEL9, OSImageStreamRHEL10),
			"Both streams '%s' and '%s' should be available", OSImageStreamRHEL9, OSImageStreamRHEL10)
		logger.Infof("Both rhel-9 and rhel-10 streams are available")
		logger.Infof("OK!\n")

		exutil.By("Create custom pool and apply OCL configuration")
		customMcp, err := CreateCustomMCP(oc.AsAdmin(), mcpAndMoscName, 1)
		defer DeleteCustomMCP(oc.AsAdmin(), mcpAndMoscName)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating custom pool %s", mcpAndMoscName)
		logger.Infof("OK!\n")

		exutil.By("Enable OCL functionality for the custom MCP")
		mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, mcpAndMoscName, mcpAndMoscName, nil)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating MachineOSConfig %s", mcpAndMoscName)
		defer mosc.CleanupAndDelete()
		logger.Infof("OK!\n")

		exutil.By("Validate initial MachineOSBuild is triggered and succeeds")
		ValidateSuccessfulMOSC(mosc, nil)

		exutil.By("Get the current MachineOSBuild name before patching osImageStream")
		initialMOSB, err := mosc.GetCurrentMachineOSBuild()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting current MachineOSBuild")
		initialMOSBName := initialMOSB.GetName()
		logger.Infof("Initial MachineOSBuild: %s", initialMOSBName)
		logger.Infof("OK!\n")

		exutil.By("Patch the osImageStream to rhel-10")
		o.Expect(customMcp.SetOsImageStream(OSImageStreamRHEL10)).To(o.Succeed(), "Error setting osImageStream to '%s'", OSImageStreamRHEL10)
		logger.Infof("OK!\n")
		o.Expect(customMcp.GetOsImageStream()).To(o.Equal(OSImageStreamRHEL10), "%s pool should be on rhel-10", customMcp)
		logger.Infof("%s pool is correctly using rhel-10 stream", customMcp)
		logger.Infof("OK!\n")

		exutil.By("Verify a new MachineOSBuild is triggered after osImageStream change")
		o.Eventually(func() string {
			currentMOSB, err := mosc.GetCurrentMachineOSBuild()
			if err != nil {
				return ""
			}
			return currentMOSB.GetName()
		}, "5m", "20s").ShouldNot(o.Equal(initialMOSBName), "A new MachineOSBuild should be triggered after osImageStream change")
		logger.Infof("New MachineOSBuild triggered after osImageStream change")
		logger.Infof("OK!\n")

		exutil.By("Validate the new MachineOSBuild succeeds and changes are applied")
		ValidateSuccessfulMOSC(mosc, nil)
	})

	g.It("[PolarionID:88365][OTP] Verify when osstream and osImageurl MC is applied the MCP is degraded [Disruptive] [apigroup:machineconfiguration.openshift.io]", func() {
		if IsCompactOrSNOCluster(oc.AsAdmin()) {
			g.Skip("The cluster is SNO/Compact. This test cannot be executed in SNO/Compact clusters")
		}
		SkipTestIfCannotUseInternalRegistry(oc.AsAdmin())

		var (
			testID  = GetCurrentTestPolarionIDNumber()
			mcpName = fmt.Sprintf("tc-%s-custom2", testID)
			mcName  = fmt.Sprintf("tc-%s-osimageurl", testID)
		)

		exutil.By("Create custom pool")
		customMcp, err := CreateCustomMCP(oc.AsAdmin(), mcpName, 1)
		defer DeleteCustomMCP(oc.AsAdmin(), mcpName)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating custom pool %s", mcpName)
		logger.Infof("OK!\n")

		exutil.By("Patch the osImageStream on pool")
		o.Expect(customMcp.SetOsImageStream(OSImageStreamRHEL10)).To(o.Succeed(), "Error setting osImageStream to '%s'", OSImageStreamRHEL10)
		logger.Infof("OK!\n")

		exutil.By("Wait for MCP update to complete and verify osImageStream is applied")
		customMcp.waitForComplete()
		o.Eventually(customMcp.GetOsImageStream, "2m", "10s").Should(o.Equal(OSImageStreamRHEL10),
			"MCP should have osImageStream set to '%s'", OSImageStreamRHEL10)
		logger.Infof("OK!\n")

		exutil.By("Build custom osImage on node")
		node := customMcp.GetSortedNodesOrFail()[0]
		dockerFileCommands := `RUN echo "test-88365" > /etc/test-88365.txt`
		osImageBuilder := OsImageBuilderInNode{
			node:                node,
			dockerFileCommands:  dockerFileCommands,
			UseInternalRegistry: true,
		}
		digestedImage, err := osImageBuilder.CreateAndDigestOsImage()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating custom osImage")
		logger.Infof("Custom osImage created: %s", digestedImage)
		logger.Infof("OK!\n")

		exutil.By("Apply osImageURL MC on custom pool with osImageStream already configured")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, customMcp.GetName())
		mc.parameters = []string{"OS_IMAGE=" + digestedImage}
		mc.skipWaitForMcp = true
		defer mc.DeleteWithWait()
		mc.create()
		logger.Infof("OK!\n")

		exutil.By("Verify MCP is degraded with error about osImageURL and osImageStream conflict")
		o.Eventually(customMcp, "5m", "20s").Should(HaveConditionField("RenderDegraded", "status", TrueString),
			"MCP should be degraded when both osImageURL and osImageStream are set")

		expectedErrorMsg := "cannot override MachineConfig osImageURL and set MachineConfigPool spec.osImageStream.name simultaneously"
		o.Eventually(customMcp.Get, "2m", "10s").WithArguments(`{.status.conditions[?(@.type=="RenderDegraded")].message}`).Should(
			o.ContainSubstring(expectedErrorMsg),
			"MCP degraded message should mention osImageURL and osImageStream conflict")
		logger.Infof("MCP correctly degraded with expected error message")
		logger.Infof("OK!\n")
	})

	// AI-assisted: Test case to verify boot image controller logs for unsupported osstream
	g.It("[PolarionID:88708][OTP] Verify osstream logs triggered for the boot image controller [Disruptive] [apigroup:machineconfiguration.openshift.io]", g.Label("Platform:aws"), func() {
		skipTestIfSupportedPlatformNotMatched(oc.AsAdmin(), AWSPlatform)

		var (
			testID                  = GetCurrentTestPolarionIDNumber()
			duplicateMsName         = fmt.Sprintf("tc-%s-dup", testID)
			unsupportedStreamLogMsg = fmt.Sprintf("machineset tc-%s-dup has unsupported stream", testID)
			controller              = NewController(oc.AsAdmin())
			duplicateMs             *MachineSet
			machineConfig           *MachineConfiguration
			cpms                    *ControlPlaneMachineSet
			err                     error
		)

		exutil.By(fmt.Sprintf("Create duplicate MachineSet %s from existing worker MachineSet", duplicateMsName))
		existingMsList := NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail()
		o.Expect(len(existingMsList)).To(o.BeNumerically(">", 0), "At least one MachineSet should exist")
		duplicateMs, err = existingMsList[0].Duplicate(duplicateMsName)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error duplicating MachineSet")
		defer func() {
			logger.Infof("Cleaning up duplicate MachineSet %s", duplicateMsName)
			duplicateMs.Delete()
		}()
		logger.Infof("OK!\n")

		// Ignore logs from before test starts to only check new logs triggered by our test actions
		exutil.By("Set log checkpoint before setting unsupported osstream label")
		controller.IgnoreLogsBeforeNowOrFail()
		logger.Infof("OK!\n")

		exutil.By(fmt.Sprintf("Set osstream label to %s on MachineSet", OSImageStreamRHEL10))
		err = duplicateMs.Patch("merge", fmt.Sprintf(`{"metadata":{"labels":{"machineconfiguration.openshift.io/osstream":"%s"}}}`, OSImageStreamRHEL10))
		o.Expect(err).NotTo(o.HaveOccurred(), "Error setting osstream label on MachineSet")
		logger.Infof("OK!\n")

		exutil.By("Verify boot image controller logs the unsupported stream message")
		o.Eventually(controller.GetLogs, "2m", "10s").Should(o.ContainSubstring(unsupportedStreamLogMsg),
			"Boot image controller should log message about unsupported stream")
		logger.Infof("Boot image controller correctly logged unsupported stream message for %s", duplicateMsName)
		logger.Infof("OK!\n")

		// Reset log checkpoint before changing to supported stream to verify the new behavior
		exutil.By("Change osstream label to rhel-9 and verify no unsupported stream log is triggered")
		controller.IgnoreLogsBeforeNowOrFail()
		err = duplicateMs.Patch("merge", fmt.Sprintf(`{"metadata":{"labels":{"machineconfiguration.openshift.io/osstream":"%s"}}}`, OSImageStreamRHEL9))
		o.Expect(err).NotTo(o.HaveOccurred(), "Error updating osstream label to supported stream")
		logger.Infof("OK!\n")

		exutil.By("Verify controller no longer logs unsupported stream message for this MachineSet")
		o.Eventually(controller.GetLogs, "2m", "10s").ShouldNot(o.ContainSubstring(unsupportedStreamLogMsg),
			"Controller should not log unsupported stream message after changing to supported stream")
		logger.Infof("Controller correctly stopped logging unsupported stream for %s", duplicateMsName)
		logger.Infof("OK!\n")

		exutil.By("Enable CPMS for managed boot images")
		cpms = NewControlPlaneMachineSet(oc.AsAdmin(), MachineAPINamespace, ControlPlaneMachineSetName)
		o.Expect(cpms.Exists()).To(o.BeTrue(), "ControlPlaneMachineSet should exist for this test")

		machineConfig = GetMachineConfiguration(oc.AsAdmin())
		defer machineConfig.RemoveManagedBootImagesConfig()

		o.Expect(
			machineConfig.SetAllManagedBootImagesConfig(ControlPlaneMachineSetResource),
		).To(o.Succeed(), "Error enabling CPMS for managed boot images")
		logger.Infof("OK!\n")

		exutil.By("Verify MachineConfiguration is not degraded after enabling CPMS")
		machineConfig.WaitForBootImageControllerDegradedState(false)
		logger.Infof("OK!\n")

		exutil.By("Verify boot image controller does not log unsupported stream for CPMS")
		controller.IgnoreLogsBeforeNowOrFail()
		o.Eventually(controller.GetLogs, "2m", "10s").ShouldNot(o.ContainSubstring("has unsupported stream"),
			"Boot image controller should not log unsupported stream message for CPMS")
		logger.Infof("Boot image controller correctly processed CPMS without unsupported stream errors")
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
