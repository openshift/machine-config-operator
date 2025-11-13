package extended

import (
	"fmt"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/disruptive][Serial][Disruptive] MCO ocb", func() {
	defer g.GinkgoRecover()

	var (
		oc = exutil.NewCLI("mco-ocb", exutil.KubeConfigPath())
	)

	g.JustBeforeEach(func() {
		PreChecks(oc)
		SkipTestIfOCBIsEnabled(oc)
	})

	g.It("[PolarionID:83141][OTP] A valid MachineOSConfig leads to a successful MachineOSBuild and cleanup of its associated resources", func() {
		var (
			mcpAndMoscName = "infra"
		)

		exutil.By("Create custom infra MCP")
		// We add no workers to the infra pool, it is not necessary
		infraMcp, err := CreateCustomMCP(oc.AsAdmin(), mcpAndMoscName, 0)
		defer infraMcp.delete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a new custom pool: %s", mcpAndMoscName)
		logger.Infof("OK!\n")

		exutil.By("Configure OCB functionality for the new infra MCP")
		mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, mcpAndMoscName, nil)
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

	g.It("[PolarionID:83138][OTP] A MachineOSConfig fails to apply or degrades if invalid inputs are given", func() {
		var (
			mcpAndMoscName = "infra"
			pushSpec       = fmt.Sprintf("%s/openshift-machine-config-operator/ocb-%s-image:latest", InternalRegistrySvcURL, mcpAndMoscName)
			pullSecret     = NewSecret(oc.AsAdmin(), "openshift-config", "pull-secret")

			fakePullSecretName         = "fake-pull-secret"
			expectedWrongPullSecretMsg = fmt.Sprintf(`could not validate baseImagePullSecret "%s" for MachineOSConfig %s: secret %s from %s is not found. Did you use the right secret name?`,
				fakePullSecretName, mcpAndMoscName, fakePullSecretName, mcpAndMoscName)
			fakePushSecretName         = "fake-push-secret"
			expectedWrongPushSecretMsg = fmt.Sprintf(`could not validate renderedImagePushSecret "%s" for MachineOSConfig %s: secret %s from %s is not found. Did you use the right secret name?`,
				fakePushSecretName, mcpAndMoscName, fakePushSecretName, mcpAndMoscName)

			fakeBuilderType             = "FakeBuilderType"
			expectedWrongBuilderTypeMsg = fmt.Sprintf(`Unsupported value: "%s": supported values: "Job"`, fakeBuilderType)
		)

		exutil.By("Create custom infra MCP")
		// We add no workers to the infra pool, it is not necessary
		infraMcp, err := CreateCustomMCP(oc.AsAdmin(), mcpAndMoscName, 0)
		defer infraMcp.delete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a new custom pool: %s", mcpAndMoscName)
		logger.Infof("OK!\n")

		exutil.By("Clone the pull-secret in MCO namespace")
		clonedSecret, err := CloneResource(pullSecret, "cloned-pull-secret-"+exutil.GetRandomString(), MachineConfigNamespace, nil)
		defer clonedSecret.Delete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error duplicating the cluster's pull-secret in MCO namespace")
		logger.Infof("OK!\n")

		// Check behaviour when wrong pullSecret
		checkMisconfiguredMOSC(oc.AsAdmin(), mcpAndMoscName, fakePullSecretName, clonedSecret.GetName(), pushSpec, nil,
			expectedWrongPullSecretMsg,
			"Check that MOSC using wrong pull secret are failing as expected")

		// Check behaviour when wrong pushSecret
		checkMisconfiguredMOSC(oc.AsAdmin(), mcpAndMoscName, clonedSecret.GetName(), fakePushSecretName, pushSpec, nil,
			expectedWrongPushSecretMsg,
			"Check that MOSC using wrong push secret are failing as expected")

		// Try to create a MOSC with a wrong pushSpec
		logger.Infof("Create a MachineOSConfig resource with a wrong builder type")

		err = NewMCOTemplate(oc, "generic-machine-os-config.yaml").Create("-p", "NAME="+mcpAndMoscName, "POOL="+mcpAndMoscName, "PULLSECRET="+clonedSecret.GetName(),
			"PUSHSECRET="+clonedSecret.GetName(), "PUSHSPEC="+pushSpec, "IMAGEBUILDERTYPE="+fakeBuilderType)
		o.Expect(err).To(o.HaveOccurred(), "Expected oc command to fail, but it didn't")
		o.Expect(err).To(o.BeAssignableToTypeOf(&exutil.ExitError{}), "Unexpected error while creating the new MOSC")
		o.Expect(err.(*exutil.ExitError).StdErr).To(o.ContainSubstring(expectedWrongBuilderTypeMsg),
			"MSOC creation using wrong image type builder should be forbidden")

		logger.Infof("OK!")
	})

	g.It("[PolarionID:83140][OTP] A MachineOSConfig with custom containerfile definition can be successfully applied", func() {
		var (
			mcp = GetCompactCompatiblePool(oc.AsAdmin())

			containerFileContent = `
	# Pull the centos base image and enable the EPEL repository.
        FROM quay.io/centos/centos:stream9 AS centos
        RUN dnf install -y epel-release

        # Pull an image containing the yq utility.
        FROM quay.io/multi-arch/yq:4.25.3 AS yq

        # Build the final OS image for this MachineConfigPool.
        FROM configs AS final

        # Copy the EPEL configs into the final image.
        COPY --from=yq /usr/bin/yq /usr/bin/yq
        COPY --from=centos /etc/yum.repos.d /etc/yum.repos.d
        COPY --from=centos /etc/pki/rpm-gpg/RPM-GPG-KEY-* /etc/pki/rpm-gpg/

        # Install cowsay and ripgrep from the EPEL repository into the final image,
        # along with a custom cow file.
        RUN sed -i 's/\$stream/9-stream/g' /etc/yum.repos.d/centos*.repo && \
            rpm-ostree install cowsay ripgrep
`

			checkers = []Checker{
				CommandOutputChecker{
					Command:  []string{"cowsay", "-t", "hello"},
					Matcher:  o.ContainSubstring("< hello >"),
					ErrorMsg: fmt.Sprintf("Cowsay is not working after installing the new image"),
					Desc:     fmt.Sprintf("Check that cowsay is installed and working"),
				},
			}
		)

		testContainerFile([]ContainerFile{{Content: containerFileContent}}, MachineConfigNamespace, mcp, checkers, false)
	})

	g.It("[PolarionID:77781][OTP] A successfully built MachineOSConfig can be re-build", func() {

		var (
			mcp = GetCompactCompatiblePool(oc.AsAdmin())
		)

		exutil.By("Configure OCB functionality for the new worker MCP")
		mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, mcp.GetName(), nil)
		defer DisableOCL(mosc)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource")
		logger.Infof("OK!\n")

		ValidateSuccessfulMOSC(mosc, nil)

		// rebuild the image and check that the image is properly applied in the nodes
		RebuildImageAndCheck(mosc)

		exutil.By("Remove the MachineOSConfig resource")
		o.Expect(DisableOCL(mosc)).To(o.Succeed(), "Error cleaning up %s", mosc)
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:77782][OTP] A MachineOSConfig with an unfinished build can be re-build", func() {

		var (
			mcp = GetCompactCompatiblePool(oc.AsAdmin())
		)

		exutil.By("Configure OCB functionality for the new worker MCP")
		mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, mcp.GetName(), nil)
		defer DisableOCL(mosc)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource")
		logger.Infof("OK!\n")

		exutil.By("Wait until MOSB starts building")
		var mosb *MachineOSBuild
		var job *Job
		o.Eventually(func() (*MachineOSBuild, error) {
			var err error
			mosb, err = mosc.GetCurrentMachineOSBuild()
			return mosb, err
		}, "5m", "20s").Should(Exist(),
			"No build was created when OCB was enabled")
		o.Eventually(func() (*Job, error) {
			var err error
			job, err = mosb.GetJob()
			return job, err
		}, "5m", "20s").Should(Exist(),
			"No build job was created when OCB was enabled")
		o.Eventually(mosb, "5m", "20s").Should(HaveConditionField("Building", "status", TrueString),
			"MachineOSBuild didn't report that the build has begun")
		logger.Infof("OK!\n")

		exutil.By("Interrupt the build")
		o.Expect(job.Delete()).To(o.Succeed(),
			"Error deleting %s", job)
		o.Eventually(mosb, "5m", "20s").Should(HaveConditionField("Interrupted", "status", TrueString),
			"MachineOSBuild didn't report that the build has begun")
		logger.Infof("OK!\n")

		// TODO: what's the intended MCP status when a build is interrupted? We need to check this status here

		// rebuild the image and check that the image is properly applied in the nodes
		RebuildImageAndCheck(mosc)

		exutil.By("Remove the MachineOSConfig resource")
		o.Expect(DisableOCL(mosc)).To(o.Succeed(), "Error cleaning up %s", mosc)
		logger.Infof("OK!\n")
	})

})

func testContainerFile(containerFiles []ContainerFile, imageNamespace string, mcp *MachineConfigPool, checkers []Checker, defaultPullSecret bool) {
	var (
		oc      = mcp.GetOC().AsAdmin()
		mcpList = NewMachineConfigPoolList(oc.AsAdmin())
		mosc    *MachineOSConfig
		err     error
	)
	switch imageNamespace {
	case MachineConfigNamespace:
		exutil.By("Configure OCB functionality for the new infra MCP. Create MOSC")
		mosc, err = createMachineOSConfigUsingExternalOrInternalRegistry(oc, MachineConfigNamespace, mcp.GetName(), containerFiles, defaultPullSecret)
	default:
		SkipTestIfCannotUseInternalRegistry(mcp.GetOC())

		exutil.By("Capture the current pull-secret value")
		// We don't use the pullSecret resource directly, instead we use auxiliary functions that will
		// extract and restore the secret's values using a file. Like that we can recover the value of the pull-secret
		// if our execution goes wrong, without printing it in the logs (for security reasons).
		secretFile, sErr := getPullSecret(oc)
		o.Expect(sErr).NotTo(o.HaveOccurred(), "Error getting the pull-secret")
		logger.Debugf("Pull-secret content stored in file %s", secretFile)
		defer func() {
			logger.Infof("Restoring initial pull-secret value")
			output, sErr := setDataForPullSecret(oc, secretFile)
			if sErr != nil {
				logger.Errorf("Error restoring the pull-secret's value. Error: %s\nOutput: %s", err, output)
			}
			mcpList.waitForComplete()
		}()
		logger.Infof("OK!\n")

		exutil.By("Create namespace to store the osImage")
		tmpNamespace := NewResource(oc.AsAdmin(), "ns", imageNamespace)
		if !tmpNamespace.Exists() {
			defer tmpNamespace.Delete()
			o.Expect(oc.AsAdmin().WithoutNamespace().Run("new-project").Args(tmpNamespace.GetName(), "--skip-config-write").Execute()).To(o.Succeed(), "Error creating a new project to store the OCL images")
		}
		logger.Infof("OK!\n")

		exutil.By("Configure OCB functionality for the new infra MCP. Create MOSC")
		mosc, err = CreateMachineOSConfigUsingInternalRegistry(oc, tmpNamespace.GetName(), mcp.GetName(), containerFiles, defaultPullSecret)
	}
	defer DisableOCL(mosc)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource")
	logger.Infof("OK!\n")

	verifyEntitlementSecretIsPresent(oc.AsAdmin(), mcp)

	ValidateSuccessfulMOSC(mosc, checkers)

	exutil.By("Remove the MachineOSConfig resource")
	o.Expect(DisableOCL(mosc)).To(o.Succeed(), "Error cleaning up %s", mosc)
	logger.Infof("OK!\n")

	ValidateMOSCIsGarbageCollected(mosc, mcp)
}

func SkipTestIfOCBIsEnabled(oc *exutil.CLI) {
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

	// Ensure all pods are running
	// Do not consider pods created by jobs, as the build pod uses an init container
	// to build that takes time to be ready and its configuration will be asserted later
	exutil.AssertAllPodsToBeReadyWithSelector(mosc.GetOC(), MachineConfigNamespace, "!batch.kubernetes.io/job-name")
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

func verifyEntitlementSecretIsPresent(oc *exutil.CLI, mcp *MachineConfigPool) {
	entitlementSecret := NewSecret(oc.AsAdmin(), "openshift-config-managed", "etc-pki-entitlement")
	secretName := fmt.Sprintf("etc-pki-entitlement-%s", mcp.GetName())
	entitlementSecretInMco := NewSecret(oc.AsAdmin(), "openshift-machine-config-operator", secretName)

	exutil.By("Verify the etc-pki-entitlement secret is present in openshift-config-managed namespace ")
	if entitlementSecret.Exists() {
		exutil.By("Verify the etc-pki-entitlement secret is present")
		logger.Infof("%s\n", entitlementSecretInMco)
		o.Eventually(entitlementSecretInMco.Exists, "5m", "30s").Should(o.BeTrue(), "Error etc-pki-entitlement should exist")
		logger.Infof("OK!\n")
	} else {
		logger.Infof("etc-pki-entitlement does not exist in openshift-config-managed namespace")
	}
}

// DisableOCL this function disables OCL.
func DisableOCL(mosc *MachineOSConfig) error {
	if !mosc.Exists() {
		logger.Infof("%s does not exist. No need to remove/disable it", mosc)
		return nil
	}

	mcp, err := mosc.GetMachineConfigPool()
	if err != nil {
		return err
	}

	currentOSImageSpec, err := mosc.GetStatusCurrentImagePullSpec()
	if err != nil {
		return err
	}

	err = mosc.CleanupAndDelete()
	if err != nil {
		return err
	}

	nodes, err := mcp.GetCoreOsNodes()
	if err != nil {
		return err
	}

	if len(nodes) > 0 {
		node := nodes[0]

		mcp.waitForComplete()

		o.Expect(node.GetCurrentBootOSImage()).NotTo(o.Equal(currentOSImageSpec),
			"OCL was disabled in %s but the OCL image is still used in %s", node)
	} else {
		logger.Infof("There is no coreos node configured in %s. We don't wait for the configuration to be applied and we don't execute any verification on the nodes", mcp)
	}

	return nil
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

func checkMisconfiguredMOSC(oc *exutil.CLI, moscAndMcpName, baseImagePullSecret, renderedImagePushSecret, pushSpec string, containerFile []ContainerFile,
	expectedMsg, stepMgs string) {
	var (
		machineConfigCO = NewResource(oc.AsAdmin(), "co", "machine-config")
	)

	exutil.By(stepMgs)
	defer logger.Infof("OK!\n")

	logger.Infof("Create a misconfiugred MOSC")
	mosc, err := CreateMachineOSConfig(oc, moscAndMcpName, baseImagePullSecret, renderedImagePushSecret, pushSpec, containerFile)
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

// RebuildImageAndCheck rebuild the latest image of the MachineOSConfig resource and checks that it is properly built and applied
func RebuildImageAndCheck(mosc *MachineOSConfig) {
	exutil.By("Rebuild the current image")

	var (
		mcp                  = exutil.OrFail[*MachineConfigPool](mosc.GetMachineConfigPool())
		mosb                 = exutil.OrFail[*MachineOSBuild](mosc.GetCurrentMachineOSBuild())
		currentImagePullSpec = exutil.OrFail[string](mosc.GetStatusCurrentImagePullSpec())
	)

	o.Expect(mosc.Rebuild()).To(o.Succeed(),
		"Error patching %s to rebuild the current image", mosc)
	logger.Infof("OK!\n")

	exutil.By("Check that the existing MOSB is reused and it builds a new image")
	o.Eventually(mosb.GetJob, "2m", "20s").Should(Exist(), "Rebuild job was not created")
	o.Eventually(mosb, "20m", "20s").Should(HaveConditionField("Building", "status", FalseString), "Rebuild was not finished")
	o.Eventually(mosb, "10m", "20s").Should(HaveConditionField("Succeeded", "status", TrueString), "Rebuild didn't succeed")
	o.Eventually(mosb, "2m", "20s").Should(HaveConditionField("Interrupted", "status", FalseString), "Reuild was interrupted")
	o.Eventually(mosb, "2m", "20s").Should(HaveConditionField("Failed", "status", FalseString), "Reuild was failed")
	logger.Infof("Check that the rebuild job was deleted")
	o.Eventually(mosb.GetJob, "2m", "20s").ShouldNot(Exist(), "Build job was not cleaned")
	logger.Infof("OK!\n")

	exutil.By("Wait for the new image to be applied")
	nodes, err := mcp.GetCoreOsNodes()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting coreos nodes from %s", mcp)
	if len(nodes) > 0 {
		node := nodes[0]

		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check that the new image is the one used in the nodes")
		newImagePullSpec := exutil.OrFail[string](mosc.GetStatusCurrentImagePullSpec())
		o.Expect(newImagePullSpec).NotTo(o.Equal(currentImagePullSpec),
			"The new image after the rebuild operation should be different fron the initial image")
		o.Expect(node.GetCurrentBootOSImage()).To(o.Equal(newImagePullSpec),
			"The new image is not being used in node %s", node)
		logger.Infof("OK!\n")
	} else {
		logger.Infof("There is no coreos node configured in %s. We don't wait for the configuration to be applied and we don't execute any verification on the nodes", mcp)
		logger.Infof("OK!\n")
	}
}
