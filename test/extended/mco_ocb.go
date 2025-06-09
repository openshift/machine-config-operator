package extended

import (
	"fmt"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended/util"
	logger "github.com/openshift/machine-config-operator/test/extended/util/logext"
)

var _ = g.Describe("[Suite:openshift/machine-config-operator/disruptive][sig-mco] MCO ocb", func() {
	defer g.GinkgoRecover()

	var (
		oc = exutil.NewCLI("mco-ocb", exutil.KubeConfigPath())
	)

	g.JustBeforeEach(func() {
		preChecks(oc)

		skipTestIfOCBIsEnabled(oc)
	})

	g.It("Author:sregidor-NonPreRelease-High-73494-OCB Wiring up Productionalized Build Controller. New 4.16 OCB API", func() {
		var (
			infraMcpName = "infra"
			moscName     = "tc-73494-infra"
		)

		exutil.By("Create custom infra MCP")
		// We add no workers to the infra pool, it is not necessary
		infraMcp, err := CreateCustomMCP(oc.AsAdmin(), infraMcpName, 0)
		defer infraMcp.delete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a new custom pool: %s", infraMcpName)
		logger.Infof("OK!\n")

		exutil.By("Configure OCB functionality for the new infra MCP")
		mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, moscName, infraMcpName, nil)
		defer mosc.CleanupAndDelete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource")
		logger.Infof("OK!\n")

		ValidateSuccessfulMOSC(mosc, nil)

		exutil.By("Remove the MachineOSConfig resource")
		o.Expect(mosc.CleanupAndDelete()).To(o.Succeed(), "Error cleaning up %s", mosc)
		logger.Infof("OK!\n")

		ValidateMOSCIsGarbageCollected(mosc, infraMcp)

		exutil.AssertAllPodsToBeReady(oc.AsAdmin(), MachineConfigNamespace)
		logger.Infof("OK!\n")

	})

	g.It("Author:sregidor-NonPreRelease-Medium-73599-OCB Validate MachineOSConfig. New 41.6 OCB API", func() {
		var (
			infraMcpName = "infra"
			moscName     = "tc-73599-infra"
			pushSpec     = fmt.Sprintf("%s/openshift-machine-config-operator/ocb-%s-image:latest", InternalRegistrySvcURL, infraMcpName)
			pullSecret   = NewSecret(oc.AsAdmin(), "openshift-config", "pull-secret")

			fakePullSecretName         = "fake-pull-secret"
			expectedWrongPullSecretMsg = fmt.Sprintf(`could not validate baseImagePullSecret "%s" for MachineOSConfig %s: secret %s from %s is not found. Did you use the right secret name?`,
				fakePullSecretName, moscName, fakePullSecretName, moscName)
			fakePushSecretName         = "fake-push-secret"
			expectedWrongPushSecretMsg = fmt.Sprintf(`could not validate renderedImagePushSecret "%s" for MachineOSConfig %s: secret %s from %s is not found. Did you use the right secret name?`,
				fakePushSecretName, moscName, fakePushSecretName, moscName)

			fakeBuilderType             = "FakeBuilderType"
			expectedWrongBuilderTypeMsg = fmt.Sprintf(`Unsupported value: "%s": supported values: "Job"`, fakeBuilderType)
		)

		exutil.By("Create custom infra MCP")
		// We add no workers to the infra pool, it is not necessary
		infraMcp, err := CreateCustomMCP(oc.AsAdmin(), infraMcpName, 0)
		defer infraMcp.delete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a new custom pool: %s", infraMcpName)
		logger.Infof("OK!\n")

		exutil.By("Clone the pull-secret in MCO namespace")
		clonedSecret, err := CloneResource(pullSecret, "cloned-pull-secret-"+exutil.GetRandomString(), MachineConfigNamespace, nil)
		defer clonedSecret.Delete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error duplicating the cluster's pull-secret in MCO namespace")
		logger.Infof("OK!\n")

		// Check behaviour when wrong pullSecret
		checkMisconfiguredMOSC(oc.AsAdmin(), moscName, infraMcpName, fakePullSecretName, clonedSecret.GetName(), pushSpec, nil,
			expectedWrongPullSecretMsg,
			"Check that MOSC using wrong pull secret are failing as expected")

		// Check behaviour when wrong pushSecret
		checkMisconfiguredMOSC(oc.AsAdmin(), moscName, infraMcpName, clonedSecret.GetName(), fakePushSecretName, pushSpec, nil,
			expectedWrongPushSecretMsg,
			"Check that MOSC using wrong push secret are failing as expected")

		// Try to create a MOSC with a wrong pushSpec
		logger.Infof("Create a MachineOSConfig resource with a wrong builder type")

		err = NewMCOTemplate(oc, "generic-machine-os-config.yaml").Create("-p", "NAME="+moscName, "POOL="+infraMcpName, "PULLSECRET="+clonedSecret.GetName(),
			"PUSHSECRET="+clonedSecret.GetName(), "PUSHSPEC="+pushSpec, "IMAGEBUILDERTYPE="+fakeBuilderType)
		o.Expect(err).To(o.HaveOccurred(), "Expected oc command to fail, but it didn't")
		o.Expect(err).To(o.BeAssignableToTypeOf(&exutil.ExitError{}), "Unexpected error while creating the new MOSC")
		o.Expect(err.(*exutil.ExitError).StdErr).To(o.ContainSubstring(expectedWrongBuilderTypeMsg),
			"MSOC creation using wrong image type builder should be forbidden")

		logger.Infof("OK!")
	})
})

func skipTestIfOCBIsEnabled(oc *exutil.CLI) {
	moscl := NewMachineOSConfigList(oc)
	allMosc := moscl.GetAllOrFail()
	if len(allMosc) != 0 {
		moscl.PrintDebugCommand()
		g.Skip(fmt.Sprintf("To run this test case we need that OCB is not enabled in any pool. At least %s OBC is enabled in this cluster.", allMosc[0]))
	}
}

// ValidateSuccessfulMOSC check that the provided MOSC is successfully applied
func ValidateSuccessfulMOSC(mosc *MachineOSConfig, checkers []Checker) {
	mcp, err := mosc.GetMachineConfigPool()
	o.Expect(err).NotTo(o.HaveOccurred(), "Cannot get the MCP for %s", mosc)

	exutil.By("Check that the deployment machine-os-builder is created")
	mOSBuilder := NewNamespacedResource(mosc.GetOC(), "deployment", MachineConfigNamespace, "machine-os-builder")

	o.Eventually(mOSBuilder, "5m", "30s").Should(Exist(),
		"The machine-os-builder deployment was not created when the OCB functionality was enabled in the infra pool")

	o.Expect(mOSBuilder.Get(`{.spec.template.spec.containers[?(@.name=="machine-os-builder")].command}`)).To(o.ContainSubstring("machine-os-builder"),
		"Error the machine-os-builder is not invoking the machine-os-builder binary")

	o.Eventually(mOSBuilder.Get, "3m", "30s").WithArguments(`{.spec.replicas}`).Should(o.Equal("1"),
		"The machine-os-builder deployment was created but the configured number of replicas is not the expected one")
	o.Eventually(mOSBuilder.Get, "2m", "30s").WithArguments(`{.status.availableReplicas}`).Should(o.Equal("1"),
		"The machine-os-builder deployment was created but the available number of replicas is not the expected one")

	exutil.AssertAllPodsToBeReady(mosc.GetOC(), MachineConfigNamespace)
	logger.Infof("OK!\n")

	exutil.By("Check that the  machine-os-builder is using leader election without failing")
	o.Expect(mOSBuilder.Logs()).To(o.And(
		o.ContainSubstring("attempting to acquire leader lease openshift-machine-config-operator/machine-os-builder"),
		o.ContainSubstring("successfully acquired lease openshift-machine-config-operator/machine-os-builder")),
		"The machine os builder pod is not using the leader election without failures")
	logger.Infof("OK!\n")

	exutil.By("Check that a new build has been triggered")
	o.Eventually(mosc.GetCurrentMachineOSBuild, "5m", "20s").Should(Exist(),
		"No build was created when OCB was enabled")
	mosb, err := mosc.GetCurrentMachineOSBuild()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting MOSB from MOSC")
	o.Eventually(mosb.GetJob, "2m", "20s").Should(Exist(),
		"No build job was created when OCB was enabled")
	o.Eventually(mosb, "5m", "20s").Should(HaveConditionField("Building", "status", TrueString),
		"MachineOSBuild didn't report that the build has begun")
	logger.Infof("OK!\n")

	exutil.By("Check that a new build is successfully executed")
	o.Eventually(mosb, "20m", "20s").Should(HaveConditionField("Building", "status", FalseString), "Build was not finished")
	o.Eventually(mosb, "10m", "20s").Should(HaveConditionField("Succeeded", "status", TrueString), "Build didn't succeed")
	o.Eventually(mosb, "2m", "20s").Should(HaveConditionField("Interrupted", "status", FalseString), "Build was interrupted")
	o.Eventually(mosb, "2m", "20s").Should(HaveConditionField("Failed", "status", FalseString), "Build was failed")
	logger.Infof("Check that the build job was deleted")
	o.Eventually(mosb.GetJob, "2m", "20s").ShouldNot(Exist(), "Build job was not cleaned")
	logger.Infof("OK!\n")

	exutil.By("Check that the digested image value was properly updated in MOSB and MOSC resources")
	mosbStatusDigestedImagePullSpec := ""
	o.Eventually(func() (string, error) {
		var err error
		mosbStatusDigestedImagePullSpec, err = mosb.GetStatusDigestedImagePullSpec()
		return mosbStatusDigestedImagePullSpec, err
	}, "5m", "10s").ShouldNot(o.BeEmpty(),
		"The MOSB resource was not updated with the digestedImagePullSpec value.\n%s", mosb.PrettyString())

	o.Eventually(mosc.GetStatusCurrentImagePullSpec, "5m", "10s").Should(o.Equal(mosbStatusDigestedImagePullSpec),
		"The MOSC resource was not updated with the MOSB's digestedImagePullSpec.\n%s", mosc.PrettyString())
	logger.Infof("OK!\n")

	numNodes, err := mcp.getMachineCount()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting MachineCount from %s", mcp)
	if numNodes > 0 {
		exutil.By("Wait for the new image to be applied")
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		node := mcp.GetSortedNodesOrFail()[0]
		exutil.By("Check that the right image is deployed in the nodes")
		currentImagePullSpec, err := mosc.GetStatusCurrentImagePullSpec()
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the current image pull spec in %s", mosc)
		o.Expect(node.GetCurrentBootOSImage()).To(o.Equal(currentImagePullSpec),
			"The image installed in node %s is not the expected one", mosc)
		logger.Infof("OK!\n")

		for _, checker := range checkers {
			checker.Check(node)
		}
	} else {
		logger.Infof("There is no node configured in %s. We don't wait for the configuration to be applied", mcp)
	}
}

// ValidateMOSCIsGarbageCollected makes sure that all resources related to the provided MOSC have been removed
func ValidateMOSCIsGarbageCollected(mosc *MachineOSConfig, mcp *MachineConfigPool) {
	exutil.By("Check that the OCB resources are cleaned up")

	logger.Infof("Validating that MOSB resources were garbage collected")
	NewMachineOSBuildList(mosc.GetOC()).PrintDebugCommand() // for debugging purposes

	o.Eventually(mosc.GetMachineOSBuildList, "2m", "20s").Should(o.HaveLen(0), "MachineSOBuilds were not cleaned when %s was removed", mosc)

	logger.Infof("Validating that machine-os-builder pod was garbage collected")
	mOSBuilder := NewNamespacedResource(mosc.GetOC().AsAdmin(), "deployment", MachineConfigNamespace, "machine-os-builder")
	o.Eventually(mOSBuilder, "2m", "30s").ShouldNot(Exist(),
		"The machine-os-builder deployment was not removed when the infra pool was unlabeled")

	logger.Infof("Validating that configmaps were garbage collected")
	for _, cm := range NewConfigMapList(mosc.GetOC(), MachineConfigNamespace).GetAllOrFail() {
		o.Expect(cm.GetName()).NotTo(o.ContainSubstring("rendered-"+mcp.GetName()),
			"%s should have been garbage collected by OCB when the %s was deleted", cm, mosc)
	}
	logger.Infof("OK!")

	exutil.By("Verify the etc-pki-entitlement secret is removed")
	oc := mosc.GetOC()
	secretName := fmt.Sprintf("etc-pki-entitlement-%s", mcp.GetName())
	entitlementSecretInMco := NewSecret(oc.AsAdmin(), "openshift-machine-config-operator", secretName)
	o.Eventually(entitlementSecretInMco.Exists, "5m", "30s").Should(o.BeFalse(), "Error etc-pki-entitlement should not exist")
	logger.Infof("OK!\n")

}

func checkMisconfiguredMOSC(oc *exutil.CLI, moscName, poolName, baseImagePullSecret, renderedImagePushSecret, pushSpec string, containerFile []ContainerFile,
	expectedMsg, stepMgs string) {
	var (
		machineConfigCO = NewResource(oc.AsAdmin(), "co", "machine-config")
	)

	exutil.By(stepMgs)
	defer logger.Infof("OK!\n")

	logger.Infof("Create a misconfiugred MOSC")
	mosc, err := CreateMachineOSConfig(oc, moscName, poolName, baseImagePullSecret, renderedImagePushSecret, pushSpec, containerFile)
	defer mosc.Delete()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating MOSC with wrong pull secret")
	logger.Infof("OK!")

	logger.Infof("Expect machine-config CO to be degraded")
	o.Eventually(machineConfigCO, "5m", "20s").Should(BeDegraded(),
		"%s should be degraded when a MOSC is configured with a wrong pull secret", machineConfigCO)
	o.Eventually(machineConfigCO, "1m", "20s").Should(HaveConditionField("Degraded", "message", o.ContainSubstring(expectedMsg)),
		"%s should be degraded when a MOSC is configured with a wrong pull secret", machineConfigCO)
	logger.Infof("OK!")

	logger.Infof("Delete the offending MOSC")
	o.Expect(mosc.Delete()).To(o.Succeed(), "Error deleing the offendint MOSC %s", mosc)
	logger.Infof("OK!")

	logger.Infof("CHeck that machine-config CO is not degraded anymore")
	o.Eventually(machineConfigCO, "5m", "20s").ShouldNot(BeDegraded(),
		"%s should stop being degraded when the offending MOSC is deleted", machineConfigCO)

}
