package extended

import (
	"fmt"
	"strings"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	"github.com/openshift/machine-config-operator/test/extended-priv/util/architecture"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/disruptive][Serial][Disruptive][OCPFeatureGate:OSStreams] MCO osImageStream", func() {
	defer g.GinkgoRecover()

	// Registered before NewCLI so it runs before SetupProject's API calls.
	// The image registry must be healthy for dockercfg secret provisioning.
	g.BeforeEach(func() {
		exutil.SkipIfImageRegistryUnhealthy(exutil.KubeConfigPath())
	})

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
			defaultStream        = GetDefaultOSImageStream(oc.AsAdmin())
		)

		defer mcp.SetSpec(mcp.GetSpecOrFail())

		exutil.By("Validate OSImageStream resource contains expected streams")
		o.Eventually(osis.GetDefaultStream, "2m", "20s").Should(o.Equal(defaultStream),
			"Expected default stream to be '%s'", defaultStream)
		logger.Infof("Default stream is correctly set to '%s'", defaultStream)

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

	// AI-assisted: Test case to validate real-time kernel configuration across OS image streams (rhel9 -> rhel10)
	g.It("[PolarionID:87095][OTP] Realtime kernel from rhel9 stream to rhel10 stream [Disruptive] [apigroup:machineconfiguration.openshift.io]", func() {
		SkipIfDefaultOSImageStream(oc.AsAdmin(), OSImageStreamRHEL10)
		architecture.SkipIfNoNodeWithArchitectures(oc.AsAdmin(), architecture.AMD64)

		testID := GetCurrentTestPolarionIDNumber()
		createdCustomPoolName := fmt.Sprintf("tc-%s-%s", testID, architecture.AMD64)
		defer DeleteCustomMCP(oc.AsAdmin(), createdCustomPoolName)

		mcp, _ := GetPoolAndNodesForArchitectureOrFail(oc.AsAdmin(), createdCustomPoolName, architecture.AMD64, 1)
		testKernelTypeAcrossOSImageStreams(oc, osis, mcp, testID, KernelTypeRealtime, "set-realtime-kernel.yaml", OSImageStreamRHEL9, OSImageStreamRHEL10)
	})

	// AI-assisted: Test case to validate real-time kernel configuration across OS image streams (rhel10 -> rhel9)
	g.It("[PolarionID:89322][OTP] Realtime kernel from rhel10 stream to rhel9 stream [Disruptive] [apigroup:machineconfiguration.openshift.io]", func() {
		SkipIfDefaultOSImageStream(oc.AsAdmin(), OSImageStreamRHEL9)
		architecture.SkipIfNoNodeWithArchitectures(oc.AsAdmin(), architecture.AMD64)

		testID := GetCurrentTestPolarionIDNumber()
		createdCustomPoolName := fmt.Sprintf("tc-%s-%s", testID, architecture.AMD64)
		defer DeleteCustomMCP(oc.AsAdmin(), createdCustomPoolName)

		mcp, _ := GetPoolAndNodesForArchitectureOrFail(oc.AsAdmin(), createdCustomPoolName, architecture.AMD64, 1)
		testKernelTypeAcrossOSImageStreams(oc, osis, mcp, testID, KernelTypeRealtime, "set-realtime-kernel.yaml", OSImageStreamRHEL10, OSImageStreamRHEL9)
	})

	// AI-assisted: Test case to validate 64k-pages kernel configuration across OS image streams (rhel9 -> rhel10)
	g.It("[PolarionID:87096][OTP] 64k pages kernel from rhel9 stream to rhel10 stream [Disruptive] [apigroup:machineconfiguration.openshift.io]", g.Label("NoPlatform:gce"), func() {
		SkipIfDefaultOSImageStream(oc.AsAdmin(), OSImageStreamRHEL10)
		architecture.SkipIfNoNodeWithArchitectures(oc.AsAdmin(), architecture.ARM64)
		skipTestIfNotSupportedPlatform(oc.AsAdmin(), GCPPlatform)

		testID := GetCurrentTestPolarionIDNumber()
		createdCustomPoolName := fmt.Sprintf("tc-%s-%s", testID, architecture.ARM64)
		defer DeleteCustomMCP(oc.AsAdmin(), createdCustomPoolName)

		mcp, _ := GetPoolAndNodesForArchitectureOrFail(oc.AsAdmin(), createdCustomPoolName, architecture.ARM64, 1)
		testKernelTypeAcrossOSImageStreams(oc, osis, mcp, testID, KernelType64kPages, "set-64k-pages-kernel.yaml", OSImageStreamRHEL9, OSImageStreamRHEL10)
	})

	// AI-assisted: Test case to validate 64k-pages kernel configuration across OS image streams (rhel10 -> rhel9)
	g.It("[PolarionID:89323][OTP] 64k pages kernel from rhel10 stream to rhel9 stream [Disruptive] [apigroup:machineconfiguration.openshift.io]", g.Label("NoPlatform:gce"), func() {
		SkipIfDefaultOSImageStream(oc.AsAdmin(), OSImageStreamRHEL9)
		architecture.SkipIfNoNodeWithArchitectures(oc.AsAdmin(), architecture.ARM64)
		skipTestIfNotSupportedPlatform(oc.AsAdmin(), GCPPlatform)

		testID := GetCurrentTestPolarionIDNumber()
		createdCustomPoolName := fmt.Sprintf("tc-%s-%s", testID, architecture.ARM64)
		defer DeleteCustomMCP(oc.AsAdmin(), createdCustomPoolName)

		mcp, _ := GetPoolAndNodesForArchitectureOrFail(oc.AsAdmin(), createdCustomPoolName, architecture.ARM64, 1)
		testKernelTypeAcrossOSImageStreams(oc, osis, mcp, testID, KernelType64kPages, "set-64k-pages-kernel.yaml", OSImageStreamRHEL10, OSImageStreamRHEL9)
	})

	// AI-assisted: Test case to validate extensions configuration survives osImageStream changes (rhel9 -> rhel10)
	g.It("[PolarionID:87259][OTP] Extensions from rhel9 stream to rhel10 stream [Disruptive] [apigroup:machineconfiguration.openshift.io]", func() {
		SkipIfDefaultOSImageStream(oc.AsAdmin(), OSImageStreamRHEL10)
		testExtensionsAcrossOSImageStreams(oc, osis, OSImageStreamRHEL9, OSImageStreamRHEL10)
	})

	// AI-assisted: Test case to validate extensions configuration survives osImageStream changes (rhel10 -> rhel9)
	g.It("[PolarionID:89324][OTP] Extensions from rhel10 stream to rhel9 stream [Disruptive] [apigroup:machineconfiguration.openshift.io]", func() {
		SkipIfDefaultOSImageStream(oc.AsAdmin(), OSImageStreamRHEL9)
		testExtensionsAcrossOSImageStreams(oc, osis, OSImageStreamRHEL10, OSImageStreamRHEL9)
	})

	g.It("[PolarionID:88366][Skipped:Disconnected] osImageStream should be empty when osImageURL is set [apigroup:machineconfiguration.openshift.io]", func() {
		var (
			testID        = GetCurrentTestPolarionIDNumber()
			osLayerMCName = fmt.Sprintf("tc-%s-os-layer-custom", testID)
			crd           = NewResource(oc.AsAdmin(), "crd", "osimagestreams.machineconfiguration.openshift.io")
			node          *Node
		)

		exutil.By("Check osimagestreams CRD exists")
		o.Eventually(crd, "10s", "2s").Should(Exist(),
			"osimagestreams CRD should exist in the cluster")
		logger.Infof("osimagestreams CRD found")
		logger.Infof("OK!\n")

		exutil.By("Build a custom OS image to use as osImageURL")
		node = mcp.GetSortedNodesOrFail()[0]
		osImageBuilder := NewOsImageBuilder(node, "RUN ostree container commit")
		digestedImage, err := osImageBuilder.CreateAndDigestOsImage()
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error creating the custom osImage")
		logger.Infof("Built custom OS image: %s", digestedImage)
		logger.Infof("OK!\n")

		exutil.By("Apply osImageURL MachineConfig to pool")
		osLayerMC := NewMachineConfig(oc.AsAdmin(), osLayerMCName, mcp.GetName())
		osLayerMC.parameters = []string{"OS_IMAGE=" + digestedImage}
		osLayerMC.skipWaitForMcp = true

		defer osLayerMC.DeleteWithWait()
		osLayerMC.create()
		logger.Infof("MachineConfig %s created with osImageURL", osLayerMC.GetName())
		logger.Infof("OK!\n")

		exutil.By("Verify osImageStream is empty while pool is updating")
		o.Eventually(mcp.GetUpdatingStatus, "2m", "10s").Should(o.Equal(TrueString),
			"%s should be updating after applying osImageURL MC", mcp)

		o.Expect(mcp.GetStatusOsImageStream()).To(o.BeEmpty(),
			"status.osImageStream should be empty when update is in progress with osImageURL set")
		logger.Infof("osImageStream is empty during update (as expected)")
		logger.Infof("OK!\n")

		exutil.By("Wait for pool update to complete")
		mcp.waitForComplete()
		logger.Infof("%s update completed", mcp)
		logger.Infof("OK!\n")

		exutil.By("Verify osImageStream remains empty after osImageURL is applied")
		o.Eventually(mcp.GetStatusOsImageStream, "2m", "20s").Should(o.BeEmpty(),
			"In %v status.osImageStream should be empty when osImageURL is set", mcp)
		logger.Infof("osImageStream is empty with osImageURL applied (as expected)")
		logger.Infof("OK!\n")

		exutil.By("Verify osImageURL is deployed on node")
		o.Expect(node.GetCurrentBootOSImage()).To(o.Equal(digestedImage),
			"Node %s should be running the custom osImageURL", node)
		logger.Infof("Custom osImageURL is deployed on %s", node)
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:88814] Invalid osImageURL degrades MCP and clears osImageStream status [apigroup:machineconfiguration.openshift.io]", func() {
		var (
			testID         = GetCurrentTestPolarionIDNumber()
			degradedMCName = fmt.Sprintf("tc-%s-degraded-test", testID)
			crd            = NewResource(oc.AsAdmin(), "crd", "osimagestreams.machineconfiguration.openshift.io")
		)

		exutil.By("Check osimagestreams CRD exists")
		o.Eventually(crd, "10s", "2s").Should(Exist(),
			"osimagestreams CRD should exist in the cluster")
		logger.Infof("osimagestreams CRD found")
		logger.Infof("OK!\n")

		exutil.By("Apply invalid MachineConfig to degrade pool")
		degradedMC := NewMachineConfig(oc.AsAdmin(), degradedMCName, mcp.GetName())
		degradedMC.parameters = []string{"IGNITION_VERSION=99.99.0"}
		degradedMC.skipWaitForMcp = true

		defer degradedMC.DeleteWithWait()
		degradedMC.create()
		logger.Infof("Invalid MachineConfig %s created", degradedMC.GetName())
		logger.Infof("OK!\n")

		exutil.By("Wait for pool to become degraded")
		o.Eventually(mcp, "5m", "20s").Should(BeDegraded(),
			"%s should become degraded after applying invalid MC", mcp)
		logger.Infof("%s is degraded (as expected)", mcp)
		logger.Infof("OK!\n")

		exutil.By("Verify osImageStream is empty when pool is degraded")
		o.Expect(mcp.GetStatusOsImageStream()).To(o.BeEmpty(),
			"In %v status.osImageStream should be empty when pool is degraded", mcp)
		logger.Infof("osImageStream is empty when degraded (as expected)")
		logger.Infof("OK!\n")

		exutil.By("Delete invalid MachineConfig to recover pool")
		degradedMC.DeleteWithWait()
		logger.Infof("Invalid MachineConfig %s deleted, pool recovered", degradedMC.GetName())
		logger.Infof("OK!\n")

		exutil.By("Verify osImageStream is restored after recovery")
		expectedStream, err := GetEffectiveOsImageStream(mcp)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting effective osImageStream from %s", mcp)
		o.Eventually(mcp.GetStatusOsImageStream, "2m", "20s").Should(o.Equal(expectedStream),
			"%s should report '%s' in status.osImageStream after recovery", mcp, expectedStream)
		logger.Infof("%s osImageStream: %s", mcp, mcp.GetSafe(`{.status.osImageStream}`, ""))
		logger.Infof("OK!\n")
	})

	// AI-assisted: Test case to validate osImageStream inheritance for custom MachineConfigPools
	g.It("[PolarionID:88122][OTP] Validate osImageStream inheritance for custom MachineConfigPools [Disruptive] [apigroup:machineconfiguration.openshift.io] [apigroup:machine.openshift.io]", func() {

		SkipIfCompactOrSNO(oc.AsAdmin())

		// Get initial and target streams based on cluster version
		initialStream, targetStream := GetInitialAndTargetStreams(oc.AsAdmin())

		testCustomMCPInheritsStreamFromWorkerPool(oc.AsAdmin(), osis, mcp, initialStream, targetStream)
	})

	g.It("[PolarionID:88203][OTP] In image-mode, verify the MOSB is triggered when osImageStream is patched [Disruptive] [apigroup:machineconfiguration.openshift.io]", func() {
		SkipIfCompactOrSNO(oc.AsAdmin())

		// Get initial and target streams based on cluster version
		initialStream, targetStream := GetInitialAndTargetStreams(oc.AsAdmin())

		testMOSBTriggeredOnStreamChange(oc.AsAdmin(), osis, initialStream, targetStream)
	})

	g.It("[PolarionID:88365][OTP] Verify when osstream and osImageurl MC is applied the MCP is degraded [Disruptive] [apigroup:machineconfiguration.openshift.io]", func() {
		var (
			testID = GetCurrentTestPolarionIDNumber()
			mcName = fmt.Sprintf("tc-%s-osimageurl", testID)
			// Use a fake image URL since it will be rejected in the render phase anyway
			fakeImageURL = "quay.io/openshift-release-dev/ocp-release:fake-test-image"
		)

		// Get initial and target streams - use targetStream to ensure a change happens
		_, targetStream := GetInitialAndTargetStreams(oc.AsAdmin())

		exutil.By("Get pool for testing")
		customMcp, cleanup, err := GetCompactCompatibleOrCustomPool(oc.AsAdmin(), 1)
		defer cleanup()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting pool for testing")
		logger.Infof("OK!\n")

		exutil.By(fmt.Sprintf("Patch the osImageStream to %s on pool", targetStream))
		o.Expect(customMcp.SetOsImageStream(targetStream)).To(o.Succeed(), "Error setting osImageStream to '%s'", targetStream)
		logger.Infof("OK!\n")

		exutil.By("Wait for MCP update to complete and verify osImageStream is applied")
		customMcp.waitForComplete()
		o.Eventually(customMcp.GetOsImageStream, "2m", "10s").Should(o.Equal(targetStream),
			"MCP should have osImageStream set to '%s'", targetStream)
		logger.Infof("OK!\n")

		exutil.By("Apply osImageURL MC on custom pool with osImageStream already configured")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, customMcp.GetName())
		mc.parameters = []string{"OS_IMAGE=" + fakeImageURL}
		mc.skipWaitForMcp = true
		defer mc.DeleteWithWait()
		mc.create()
		logger.Infof("OK!\n")

		exutil.By("Verify MCP is degraded with error about osImageURL and osImageStream conflict")
		o.Eventually(customMcp, "5m", "20s").Should(HaveConditionField("RenderDegraded", "status", TrueString),
			"MCP should be degraded when both osImageURL and osImageStream are set")

		expectedErrorMsg := "cannot override MachineConfig osImageURL and set MachineConfigPool spec.osImageStream.name simultaneously"
		o.Eventually(customMcp, "5m", "20s").Should(HaveConditionField("RenderDegraded", "message", o.ContainSubstring(expectedErrorMsg)),
			"MCP degraded message should mention osImageURL and osImageStream conflict")
		logger.Infof("MCP correctly degraded with expected error message")
		logger.Infof("OK!\n")

		exutil.By("Remove conflicting MC and verify MCP recovers from degraded state")
		mc.DeleteWithWait()
		o.Eventually(customMcp, "5m", "20s").Should(HaveConditionField("RenderDegraded", "status", FalseString),
			"MCP should recover from degraded state after removing conflicting MC")
		logger.Infof("MCP successfully recovered from degraded state")
		logger.Infof("OK!\n")
	})

	// AI-assisted: Test case to verify boot image controller logs for CPMS osstream reconciliation
})

// GetDefaultOSImageStream returns the default OS image stream based on the cluster version.
// OCP 4.x clusters default to "rhel-9", OCP 5+ default to "rhel-10".
func GetDefaultOSImageStream(oc *exutil.CLI) string {
	clusterVersion, _, err := exutil.GetClusterVersion(oc)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting cluster version")

	if clusterVersion[:1] == "4" {
		return OSImageStreamRHEL9
	}
	return OSImageStreamRHEL10
}

// GetOSImageStreamMajorVersion extracts the major version from a stream name (e.g. "rhel-9" -> "9", "rhel-10" -> "10").
func GetOSImageStreamMajorVersion(stream string) string {
	return strings.TrimPrefix(stream, "rhel-")
}

// SkipIfDefaultOSImageStream skips the test if the cluster's default OS image stream matches the given stream.
func SkipIfDefaultOSImageStream(oc *exutil.CLI, stream string) {
	if GetDefaultOSImageStream(oc) == stream {
		g.Skip(fmt.Sprintf("Skipping test: default OS image stream is '%s'", stream))
	}
}

// GetEffectiveOsImageStream returns the effective osImageStream for an MCP
func GetEffectiveOsImageStream(mcp *MachineConfigPool) (string, error) {
	var (
		oc   = mcp.GetOC().AsAdmin()
		osis = NewOSImageStream(oc)
	)

	if !osis.Exists() {
		expectedDefault := GetDefaultOSImageStream(mcp.GetOC().AsAdmin())
		logger.Infof("OSImageStream resource does not exist. Likely, no tech preview, by default we use osstream: %s", expectedDefault)
		return expectedDefault, nil
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
	expectedOsImage, err := osis.GetOsImageByName(streamName)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting osImage for stream '%s'", streamName)

	firstNode := mcp.GetSortedNodesOrFail()[0]
	logger.Infof("Checking osImage on node %s", firstNode.GetName())

	o.Eventually(firstNode.GetCurrentBootOSImage, "2m", "20s").Should(o.Equal(expectedOsImage),
		"%s should be using osImage %s from stream '%s'",
		firstNode, expectedOsImage, streamName)

	logger.Infof("%s is correctly using osImage from stream '%s'", firstNode, streamName)

	logger.Infof("Checking MCP status.osImageStream")
	o.Eventually(mcp.GetStatusOsImageStream, "2m", "20s").Should(o.Equal(streamName),
		"%s should report stream '%s' in status.osImageStream",
		mcp, streamName)
	logger.Infof("%s correctly reports osImageStream '%s' in status", mcp, streamName)

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
}

// ensureOsImageStream checks the current osImageStream and configures it to the desired stream if necessary
func ensureOsImageStream(mcp *MachineConfigPool, stream string) {
	currentStream, err := GetEffectiveOsImageStream(mcp)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting effective osImageStream from %s", mcp)
	logger.Infof("Current stream: '%s'", currentStream)

	if currentStream != stream {
		logger.Infof("Configuring osImageStream to '%s' in %s", stream, mcp)
		o.Expect(mcp.SetOsImageStream(stream)).To(o.Succeed(), "Error setting osImageStream to '%s' in %s", stream, mcp)
		mcp.waitForComplete()
	} else {
		logger.Infof("%s is already using %s stream", mcp, stream)
	}
}

// testKernelTypeAcrossOSImageStreams tests kernel type configuration across OS image streams
func testKernelTypeAcrossOSImageStreams(oc *exutil.CLI, osis *OSImageStream, mcp *MachineConfigPool, testID string, kernelType KernelType, mcTemplate, initialStream, targetStream string) {
	mcName := fmt.Sprintf("tc-%s-%s-kernel", testID, kernelType)

	defer mcp.waitForComplete()
	defer mcp.SetSpec(mcp.GetSpecOrFail())

	exutil.By(fmt.Sprintf("Verify %s and %s streams are available", initialStream, targetStream))
	o.Eventually(osis.GetAvailableStreamNames, "2m", "20s").Should(o.ContainElements(initialStream, targetStream),
		"Both streams '%s' and '%s' should be available", initialStream, targetStream)
	logger.Infof("Both %s and %s streams are available", initialStream, targetStream)
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Check current osImageStream and configure %s if necessary", initialStream))
	ensureOsImageStream(mcp, initialStream)
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Validate that %s is using osImageStream '%s'", mcp, initialStream))
	validateOsImageStreamInPool(mcp, osis, initialStream)
	logger.Infof("OK!\n")

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

	exutil.By(fmt.Sprintf("Configure osImageStream to '%s' in %s", targetStream, mcp))
	o.Expect(mcp.SetOsImageStream(targetStream)).To(o.Succeed(), "Error setting osImageStream to '%s' in %s", targetStream, mcp)
	mcp.waitForComplete()
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Verify %s stream is configured and %s kernel is still active", targetStream, kernelType))
	validateOsImageStreamInPool(mcp, osis, targetStream)

	o.Eventually(firstNode.IsKernelType, "2m", "20s").WithArguments(kernelType).Should(o.BeTrue(),
		"%s kernel should still be active on %s after switching to %s", kernelType, firstNode, targetStream)
	logger.Infof("%s kernel is still correctly configured on %s", kernelType, firstNode)
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Remove the %s kernel configuration", kernelType))
	mc.DeleteWithWait()
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Verify %s kernel is not active and %s stream is still configured", kernelType, targetStream))
	o.Eventually(firstNode.IsKernelType, "2m", "20s").WithArguments(kernelType).Should(o.BeFalse(),
		"%s kernel should not be active on %s", kernelType, firstNode)
	logger.Infof("%s kernel is correctly removed from %s", kernelType, firstNode)

	validateOsImageStreamInPool(mcp, osis, targetStream)
	logger.Infof("%s is still correctly using %s stream", firstNode, targetStream)
	logger.Infof("OK!\n")
}

// testExtensionsAcrossOSImageStreams tests extensions configuration across OS image streams
func testExtensionsAcrossOSImageStreams(oc *exutil.CLI, osis *OSImageStream, initialStream, targetStream string) {
	var (
		testID = GetCurrentTestPolarionIDNumber()
		mcName = fmt.Sprintf("tc-%s-extensions", testID)
	)

	exutil.By(fmt.Sprintf("Verify %s and %s streams are available", initialStream, targetStream))
	o.Eventually(osis.GetAvailableStreamNames, "2m", "20s").Should(o.ContainElements(initialStream, targetStream),
		"Both streams '%s' and '%s' should be available", initialStream, targetStream)
	logger.Infof("Both %s and %s streams are available", initialStream, targetStream)
	logger.Infof("OK!\n")

	exutil.By("Get the MCP used to execute this test")
	mcp, cleanup, err := GetCompactCompatibleOrCustomPool(oc.AsAdmin(), 1)
	defer cleanup()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting pool for testing")

	defer mcp.waitForComplete()
	defer mcp.SetSpec(mcp.GetSpecOrFail())
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Check current osImageStream and configure %s if necessary", initialStream))
	ensureOsImageStream(mcp, initialStream)
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Validate that %s is using osImageStream '%s'", mcp, initialStream))
	validateOsImageStreamInPool(mcp, osis, initialStream)
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Get extensions compatible with both %s and %s streams", initialStream, targetStream))
	fips := isFIPSEnabledInClusterConfig(oc.AsAdmin())
	armNodes, err := mcp.GetNodesByArchitecture(architecture.ARM64)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the list of ARM nodes in %s", mcp)
	_, compatibleExtensions, expectedRpmPackages := FilterExtensionsForStreams(AllExtenstions, len(armNodes) > 0, fips, initialStream, targetStream)
	o.Expect(compatibleExtensions).NotTo(o.BeEmpty(),
		"No extensions are compatible with both %s and %s streams", initialStream, targetStream)
	logger.Infof("Extensions compatible with both %s and %s: %v", initialStream, targetStream, compatibleExtensions)
	logger.Infof("Expected RPM packages: %v", expectedRpmPackages)
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Configure %s compatible extensions in %s while on %s", targetStream, mcp, initialStream))
	mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName())
	mc.parameters = []string{fmt.Sprintf(`EXTENSIONS=%s`, string(MarshalOrFail(compatibleExtensions)))}
	mc.skipWaitForMcp = true

	defer mc.DeleteWithWait()
	mc.create()
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Wait for %s to be updated after applying extensions", mcp))
	mcp.SetWaitingTimeForExtensionsChange()
	mcp.waitForComplete()
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Verify %s compatible extensions are installed on %s", targetStream, initialStream))
	firstNode := mcp.GetSortedNodesOrFail()[0]
	CheckExtensions(firstNode, compatibleExtensions)
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Configure osImageStream to '%s' in %s", targetStream, mcp))
	o.Expect(mcp.SetOsImageStream(targetStream)).To(o.Succeed(), "Error setting osImageStream to '%s' in %s", targetStream, mcp)
	mcp.waitForComplete()
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Verify %s stream is configured and extensions are still installed", targetStream))
	validateOsImageStreamInPool(mcp, osis, targetStream)

	CheckExtensions(firstNode, compatibleExtensions)
	logger.Infof("Extensions are still correctly installed after switching to %s", targetStream)
	logger.Infof("OK!\n")

	exutil.By("Remove the extensions configuration")
	mc.DeleteWithWait()
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Verify extensions are uninstalled and %s stream is still configured", targetStream))
	for _, pkg := range expectedRpmPackages {
		o.Expect(firstNode.RpmIsInstalled(pkg)).To(
			o.BeFalse(),
			"Package %s should be uninstalled when we remove the extensions MC", pkg)
	}
	logger.Infof("All extension packages are correctly uninstalled")

	validateOsImageStreamInPool(mcp, osis, targetStream)
	logger.Infof("%s is still correctly using %s stream", firstNode, targetStream)
	logger.Infof("OK!\n")
}

// testCustomMCPInheritsStreamFromWorkerPool tests osImageStream inheritance for custom MCPs
// Used by: [PolarionID:88122]
func testCustomMCPInheritsStreamFromWorkerPool(oc *exutil.CLI, osis *OSImageStream, mcp *MachineConfigPool, initialStream, targetStream string) {
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

	exutil.By(fmt.Sprintf("Verify %s and %s streams are available", initialStream, targetStream))
	o.Eventually(osis.GetAvailableStreamNames, "2m", "20s").Should(o.ContainElements(initialStream, targetStream),
		"Both streams '%s' and '%s' should be available", initialStream, targetStream)
	logger.Infof("Both %s and %s streams are available", initialStream, targetStream)
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Case1: Applied below custom MCP to inherit the default value of worker pool (%s)", initialStream))
	currentStream, err := GetEffectiveOsImageStream(mcp)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting effective osImageStream from %s", mcp)
	logger.Infof("Current worker pool stream: '%s'", currentStream)

	if currentStream != initialStream {
		logger.Infof("Configuring osImageStream to '%s' in %s", initialStream, mcp)
		o.Expect(mcp.SetOsImageStream(initialStream)).To(o.Succeed(), "Error setting osImageStream to '%s' in %s", initialStream, mcp)
		mcp.waitForComplete()
	}
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Create custom MCP that inherits %s from worker pool", initialStream))
	defer DeleteCustomMCP(oc.AsAdmin(), mcp1Name)
	mcp1, err := CreateCustomMCP(oc.AsAdmin(), mcp1Name, 1)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating custom MCP %s", mcp1Name)
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Check the node uses %s", initialStream))
	validateOsImageStreamInPool(mcp1, osis, initialStream)

	exutil.By(fmt.Sprintf("Create custom MCP with %s explicitly configured", targetStream))
	defer DeleteCustomMCP(oc.AsAdmin(), mcp2Name)
	mcp2, err := CreateCustomMCPWithStream(oc.AsAdmin(), mcp2Name, targetStream, 1)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating custom MCP %s with %s", mcp2Name, targetStream)
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Check the node uses %s", targetStream))
	validateOsImageStreamInPool(mcp2, osis, targetStream)

	exutil.By("Clean up Case 1 MCPs to free nodes for Case 2")
	o.Expect(DeleteCustomMCP(oc.AsAdmin(), mcp1Name)).To(o.Succeed(), "Error deleting MCP %s", mcp1Name)
	o.Expect(DeleteCustomMCP(oc.AsAdmin(), mcp2Name)).To(o.Succeed(), "Error deleting MCP %s", mcp2Name)
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Case2: Create custom mcp infra without osstream field, patch worker to %s, and verify infra inherits %s", targetStream, targetStream))

	exutil.By(fmt.Sprintf("Create custom MCP %s without osstream field to test dynamic inheritance", infraName))
	defer DeleteCustomMCP(oc.AsAdmin(), infraName)
	infraMcp, err := CreateCustomMCP(oc.AsAdmin(), infraName, 0)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating custom MCP %s without osstream", infraName)
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Patch worker pool with %s", targetStream))
	o.Expect(mcp.SetOsImageStream(targetStream)).To(o.Succeed(), "Error setting osImageStream to '%s' in %s", targetStream, mcp)
	mcp.waitForComplete()
	logger.Infof("OK!\n")

	exutil.By("Add a node to infra pool")
	err = AddNodesToMachineConfigPool(oc.AsAdmin(), infraName, []*Node{mcp.GetSortedNodesOrFail()[0]})
	o.Expect(err).NotTo(o.HaveOccurred(), "Error adding node to infra pool")
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Verify node in infra pool inherited %s from worker pool", targetStream))
	validateOsImageStreamInPool(infraMcp, osis, targetStream)

	exutil.By("Clean up Case 2 MCP to free nodes for Case 3")
	o.Expect(DeleteCustomMCP(oc.AsAdmin(), infraName)).To(o.Succeed(), "Error deleting MCP %s", infraName)
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Case3: Verify worker pool is on %s, then create cmcp1 (inherits %s) and cmcp2 (explicit %s)", targetStream, targetStream, initialStream))
	o.Expect(GetEffectiveOsImageStream(mcp)).To(o.Equal(targetStream), "Worker pool should be on %s from Case 2", targetStream)

	exutil.By(fmt.Sprintf("Create custom mcp cmcp1 (inherits %s) and cmcp2 (explicit %s)", targetStream, initialStream))
	defer DeleteCustomMCP(oc.AsAdmin(), cmcp1Name)
	cmcp1, err := CreateCustomMCP(oc.AsAdmin(), cmcp1Name, 1)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating custom MCP %s", cmcp1Name)
	logger.Infof("OK!\n")

	defer DeleteCustomMCP(oc.AsAdmin(), cmcp2Name)
	cmcp2, err := CreateCustomMCPWithStream(oc.AsAdmin(), cmcp2Name, initialStream, 1)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating custom MCP %s with %s", cmcp2Name, initialStream)
	logger.Infof("OK!\n")

	exutil.By("Verify nodes use the correct osImageStreams")
	validateOsImageStreamInPool(cmcp1, osis, targetStream)
	validateOsImageStreamInPool(cmcp2, osis, initialStream)
}

// testMOSBTriggeredOnStreamChange tests that MachineOSBuild is triggered when osImageStream changes
// Used by: [PolarionID:88203]
func testMOSBTriggeredOnStreamChange(oc *exutil.CLI, osis *OSImageStream, initialStream, targetStream string) {
	var (
		testID        = GetCurrentTestPolarionIDNumber()
		customMcpName = fmt.Sprintf("tc-%s", testID)
	)

	exutil.By(fmt.Sprintf("Verify %s and %s streams are available", initialStream, targetStream))
	o.Eventually(osis.GetAvailableStreamNames, "2m", "20s").Should(o.ContainElements(initialStream, targetStream),
		"Both streams '%s' and '%s' should be available", initialStream, targetStream)
	logger.Infof("Both %s and %s streams are available", initialStream, targetStream)
	logger.Infof("OK!\n")

	exutil.By("Create custom pool and apply OCL configuration")
	customMcp, err := CreateCustomMCP(oc.AsAdmin(), customMcpName, 1)
	defer DeleteCustomMCP(oc.AsAdmin(), customMcpName)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating custom pool %s", customMcpName)
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Set osImageStream to %s before enabling OCL to ensure known starting state", initialStream))
	o.Expect(customMcp.SetOsImageStream(initialStream)).To(o.Succeed(), "Error setting osImageStream to %s", initialStream)
	customMcp.waitForComplete()
	logger.Infof("OK!\n")

	exutil.By("Enable OCL functionality for the custom MCP")
	mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, customMcpName, customMcpName, nil)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating MachineOSConfig %s", customMcpName)
	defer mosc.CleanupAndDelete()
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Validate initial MachineOSBuild with %s is triggered and succeeds", initialStream))
	ValidateSuccessfulMOSC(mosc, nil)

	exutil.By("Get the current MachineOSBuild name before patching osImageStream")
	initialMOSB, err := mosc.GetCurrentMachineOSBuild()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting current MachineOSBuild")
	initialMOSBName := initialMOSB.GetName()
	logger.Infof("Initial MachineOSBuild: %s", initialMOSBName)
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("Patch the osImageStream from %s to %s", initialStream, targetStream))
	o.Expect(customMcp.SetOsImageStream(targetStream)).To(o.Succeed(), "Error setting osImageStream to %s", targetStream)
	logger.Infof("OK!\n")
	o.Expect(customMcp.GetOsImageStream()).To(o.Equal(targetStream), "%s pool should be on %s", customMcp, targetStream)
	logger.Infof("%s pool is correctly using %s stream", customMcp, targetStream)
	logger.Infof("OK!\n")

	exutil.By("Verify a new MachineOSBuild is triggered after osImageStream change")
	o.Eventually(func() (string, error) {
		currentMOSB, err := mosc.GetCurrentMachineOSBuild()
		if err != nil {
			return "", err
		}
		return currentMOSB.GetName(), nil
	}, "5m", "20s").ShouldNot(o.Equal(initialMOSBName), "A new MachineOSBuild should be triggered after osImageStream change")
	logger.Infof("New MachineOSBuild triggered after osImageStream change")
	logger.Infof("OK!\n")

	exutil.By("Validate the new MachineOSBuild succeeds and changes are applied")
	ValidateSuccessfulMOSC(mosc, nil)
	// Note: ValidateSuccessfulMOSC already validates the node is using the layered image.
	// For OCL, nodes boot from layered images (internal registry), not osImageStream base images.

	exutil.By("Verify MCP status reports the new osImageStream")
	o.Eventually(customMcp.GetStatusOsImageStream, "2m", "20s").Should(o.Equal(targetStream),
		"MCP should report osImageStream '%s' in status", targetStream)
	logger.Infof("MCP correctly reports osImageStream '%s' in status", targetStream)
	logger.Infof("OK!\n")
}
