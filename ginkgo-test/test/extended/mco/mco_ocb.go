package mco

import (
	"fmt"
	"strings"
	"sync"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util"
	logger "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/logext"
)

var _ = g.Describe("[sig-mco] MCO ocb", func() {
	defer g.GinkgoRecover()

	var (
		oc = exutil.NewCLI("mco-ocb", exutil.KubeConfigPath())
	)

	g.JustBeforeEach(func() {
		preChecks(oc)
		// According to https://issues.redhat.com/browse/MCO-831, featureSet:TechPreviewNoUpgrade is required
		// xref: featureGate: OnClusterBuild
		if !exutil.IsTechPreviewNoUpgrade(oc) {
			g.Skip("featureSet: TechPreviewNoUpgrade is required for this test")
		}

		skipTestIfOCBIsEnabled(oc)
	})

	g.It("Author:sregidor-NonPreRelease-High-73494-OCB Wiring up Productionalized Build Controller. New 4.16 OCB API [Disruptive]", func() {
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
		mosc, err := CreateMachineOSConfigUsingInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, moscName, infraMcpName, nil)
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

	g.It("Author:sregidor-NonPreRelease-Medium-73599-OCB Validate MachineOSConfig. New 41.6 OCB API [Disruptive]", func() {
		var (
			infraMcpName = "infra"
			moscName     = "tc-73599-infra"
			pushSpec     = fmt.Sprintf("%s/openshift-machine-config-operator/ocb-%s-image:latest", InternalRegistrySvcURL, infraMcpName)
			pullSecret   = NewSecret(oc.AsAdmin(), "openshift-config", "pull-secret")

			fakePullSecretName         = "fake-pull-secret"
			expectedWrongPullSecretMsg = fmt.Sprintf(`invalid MachineOSConfig %s: could not validate baseImagePullSecret "%s" for MachineOSConfig %s: secret %s from %s is not found. Did you use the right secret name?`,
				moscName, fakePullSecretName, moscName, fakePullSecretName, moscName)
			fakePushSecretName         = "fake-push-secret"
			expectedWrongPushSecretMsg = fmt.Sprintf(`invalid MachineOSConfig %s: could not validate renderedImagePushSecret "%s" for MachineOSConfig %s: secret %s from %s is not found. Did you use the right secret name?`,
				moscName, fakePushSecretName, moscName, fakePushSecretName, moscName)

			fakeBuilderType             = "FakeBuilderType"
			expectedWrongBuilderTypeMsg = fmt.Sprintf(`Unsupported value: "%s": supported values: "PodImageBuilder"`, fakeBuilderType)
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
		checkMisconfiguredMOSC(oc.AsAdmin(), moscName, infraMcpName, clonedSecret.GetName(), fakePullSecretName, clonedSecret.GetName(), pushSpec, nil,
			expectedWrongPullSecretMsg,
			"Check that MOSC using wrong pull secret are failing as expected")

		// Check behaviour when wrong pushSecret
		checkMisconfiguredMOSC(oc.AsAdmin(), moscName, infraMcpName, clonedSecret.GetName(), clonedSecret.GetName(), fakePushSecretName, pushSpec, nil,
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

	g.It("Author:ptalgulk-ConnectedOnly-Longduration-NonPreRelease-Critical-74645-Panic Condition for Non-Matching MOSC Resources [Disruptive]", func() {

		skipTestIfWorkersCannotBeScaled(oc.AsAdmin())

		var (
			infraMcpName = "infra"
			moscName     = "tc-74645"
			mcc          = NewController(oc.AsAdmin())
		)
		exutil.By("Create New Custom MCP")
		defer DeleteCustomMCP(oc.AsAdmin(), infraMcpName)
		infraMcp, err := CreateCustomMCP(oc.AsAdmin(), infraMcpName, 1)
		o.Expect(err).NotTo(o.HaveOccurred(), "Could not create a new custom MCP")
		node := infraMcp.GetNodesOrFail()[0]
		logger.Infof("%s", node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Configure OCB functionality for the new infra MCP")
		mosc, err := CreateMachineOSConfigUsingInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, moscName, infraMcpName, nil)
		defer DisableOCL(mosc)
		// remove after this bug is fixed OCPBUGS-36810
		defer func() {
			logger.Infof("Configmaps should also be deleted ")
			cmList := NewConfigMapList(mosc.GetOC(), MachineConfigNamespace).GetAllOrFail()
			for _, cm := range cmList {
				if strings.Contains(cm.GetName(), "rendered-") {
					o.Expect(cm.Delete()).Should(o.Succeed(), "The ConfigMap related to MOSC has not been removed")
				}
			}
		}()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource")
		logger.Infof("OK!\n")

		exutil.By("Check that a new build has been triggered")
		o.Eventually(infraMcp.GetLatestMachineOSBuildOrFail(), "5m", "20s").Should(Exist(),
			"No build was created when OCB was enabled")
		mosb := infraMcp.GetLatestMachineOSBuildOrFail()
		o.Eventually(mosb.GetPod).Should(Exist(),
			"No build pod was created when OCB was enabled")
		o.Eventually(mosb, "5m", "20s").Should(HaveConditionField("Building", "status", TrueString),
			"MachineOSBuild didn't report that the build has begun")
		logger.Infof("OK!\n")

		exutil.By("Delete the MCOS and check it is deleted")
		o.Expect(mosc.CleanupAndDelete()).To(o.Succeed(), "Error cleaning up %s", mosc)
		ValidateMOSCIsGarbageCollected(mosc, infraMcp)
		o.Expect(mosb).NotTo(Exist(), "Build is not deleted")
		o.Expect(mosc).NotTo(Exist(), "MOSC is not deleted")
		logger.Infof("OK!\n")

		exutil.By("Check MCC Logs for Panic is not produced")
		exutil.AssertAllPodsToBeReady(oc.AsAdmin(), MachineConfigNamespace)

		o.Expect(mcc.GetPreviousLogs()).NotTo(o.Or(o.ContainSubstring("panic"), o.ContainSubstring("Panic")), "Panic is seen in MCC pod after deleting OCB resources")
		o.Expect(mcc.GetLogs()).NotTo(o.Or(o.ContainSubstring("panic"), o.ContainSubstring("Panic")), "Panic is seen in MCC pod after deleting OCB resources")
		logger.Infof("OK!\n")
	})

	g.It("Author:sregidor-ConnectedOnly-Longduration-NonPreRelease-Critical-73496-OCB use custom Containerfile. New 4.16 OCB API[Disruptive]", func() {
		// Remove this "skip" checks once the functionality to disable OCL is implemented
		skipTestIfWorkersCannotBeScaled(oc.AsAdmin()) // Right now the only way to disable OCL in a pool is to delete all pods and recreate them from scratch.

		SkipIfSNO(oc.AsAdmin()) // We have to skip this test case in SNO until the functionality to disable OCL is ready to work on master nodes
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

		testContainerFile([]ContainerFile{{Content: containerFileContent}}, MachineConfigNamespace, mcp, checkers)
	})

	g.It("Author:sregidor-ConnectedOnly-Longduration-NonPreRelease-High-73436-OCB Use custom Containerfile with rhel enablement [Disruptive]", func() {
		// Remove this "skip" checks once the functionality to disable OCL is implemented
		skipTestIfWorkersCannotBeScaled(oc.AsAdmin()) // Right now the only way to disable OCL in a pool is to delete all pods and recreate them from scratch.

		SkipIfSNO(oc.AsAdmin()) // We have to skip this test case in SNO until the functionality to disable OCL is ready to work on master nodes
		var (
			entitlementSecret = NewSecret(oc.AsAdmin(), "openshift-config-managed", "etc-pki-entitlement")
			mcp               = GetCompactCompatiblePool(oc.AsAdmin())

			containerFileContent = `
        FROM configs AS final
    
        RUN rm -rf /etc/rhsm-host && \
          rpm-ostree install buildah && \
          ln -s /run/secrets/rhsm /etc/rhsm-host && \
          ostree container commit
`

			checkers = []Checker{
				CommandOutputChecker{
					Command:  []string{"rpm", "-q", "buildah"},
					Matcher:  o.ContainSubstring("buildah-"),
					ErrorMsg: fmt.Sprintf("Buildah package is not installed after the image was deployed"),
					Desc:     fmt.Sprintf("Check that buildah is installed"),
				},
			}
		)

		if !entitlementSecret.Exists() {
			g.Skip(fmt.Sprintf("There is no entitlement secret available in this cluster %s. This test case cannot be executed", entitlementSecret))
		}

		exutil.By("Create the entitlement secret in the MCO namespace")
		mcoEntitlementSecret, err := CloneResource(entitlementSecret, "etc-pki-entitlement", MachineConfigNamespace, nil)
		defer mcoEntitlementSecret.Delete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error copying %s to the %s namespace", mcoEntitlementSecret, MachineConfigNamespace)
		logger.Infof("OK!\n")

		testContainerFile([]ContainerFile{{Content: containerFileContent}}, MachineConfigNamespace, mcp, checkers)
	})

	g.It("Author:sregidor-ConnectedOnly-Longduration-NonPreRelease-High-73947-OCB use OutputImage CurrentImagePullSecret [Disruptive]", func() {
		// Remove this "skip" checks once the functionality to disable OCL is implemented
		skipTestIfWorkersCannotBeScaled(oc.AsAdmin()) // Right now the only way to disable OCL in a pool is to delete all pods and recreate them from scratch.

		SkipIfSNO(oc.AsAdmin()) // We have to skip this test case in SNO until the functionality to disable OCL is ready to work on master nodes
		var (
			mcp              = GetCompactCompatiblePool(oc.AsAdmin())
			tmpNamespaceName = "tc-73947-mco-ocl-images"
			checkers         = []Checker{
				CommandOutputChecker{
					Command:  []string{"rpm-ostree", "status"},
					Matcher:  o.ContainSubstring(fmt.Sprintf("%s/%s/ocb-%s-image", InternalRegistrySvcURL, tmpNamespaceName, mcp.GetName())),
					ErrorMsg: fmt.Sprintf("The nodes are not using the expected OCL image stored in the internal registry"),
					Desc:     fmt.Sprintf("Check that the nodes are using the righ OS image"),
				},
			}
		)

		testContainerFile([]ContainerFile{}, tmpNamespaceName, mcp, checkers)
	})

	g.It("Author:sregidor-ConnectedOnly-Longduration-NonPreRelease-High-72003-OCB Opting into on-cluster builds must respect maxUnavailable setting. Workers.[Disruptive]", func() {
		// Remove this "skip" checks once the functionality to disable OCL is implemented
		skipTestIfWorkersCannotBeScaled(oc.AsAdmin()) // Right now the only way to disable OCL in a pool is to delete all pods and recreate them from scratch.
		SkipIfSNO(oc.AsAdmin())                       // This test makes no sense in SNO

		var (
			moscName    = "test-" + GetCurrentTestPolarionIDNumber()
			wMcp        = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			workerNodes = wMcp.GetSortedNodesOrFail()
		)

		exutil.By("Configure maxUnavailable if worker pool has more than 2 nodes")
		if len(workerNodes) > 2 {
			wMcp.SetMaxUnavailable(2)
			defer wMcp.RemoveMaxUnavailable()
		}

		maxUnavailable := exutil.OrFail[int](wMcp.GetMaxUnavailableInt())
		logger.Infof("Current maxUnavailable value %d", maxUnavailable)
		logger.Infof("OK!\n")

		exutil.By("Configure OCB functionality for the new worker MCP")
		mosc, err := CreateMachineOSConfigUsingInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, moscName, wMcp.GetName(), nil)
		defer DisableOCL(mosc)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource")
		logger.Infof("OK!\n")

		exutil.By("Configure OCB functionality for the new worker MCP")
		o.Eventually(wMcp.GetUpdatingStatus, "15m", "15s").Should(o.Equal("True"),
			"The worker MCP did not start updating")
		logger.Infof("OK!\n")

		exutil.By("Poll the nodes sorted by the order they are updated")
		updatedNodes := wMcp.GetSortedUpdatedNodes(maxUnavailable)
		for _, n := range updatedNodes {
			logger.Infof("updated node: %s created: %s zone: %s", n.GetName(), n.GetOrFail(`{.metadata.creationTimestamp}`), n.GetOrFail(`{.metadata.labels.topology\.kubernetes\.io/zone}`))
		}
		logger.Infof("OK!\n")

		exutil.By("Wait for the configuration to be applied in all nodes")
		wMcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check that nodes were updated in the right order")
		rightOrder := checkUpdatedLists(workerNodes, updatedNodes, maxUnavailable)
		o.Expect(rightOrder).To(o.BeTrue(), "Expected update order %s, but found order %s", workerNodes, updatedNodes)
		logger.Infof("OK!\n")

		exutil.By("Remove the MachineOSConfig resource")
		o.Expect(DisableOCL(mosc)).To(o.Succeed(), "Error cleaning up %s", mosc)
		logger.Infof("OK!\n")
	})
	g.It("Author:sregidor-ConnectedOnly-Longduration-NonPreRelease-High-73497-OCB build images in many MCPs at the same time [Disruptive]", func() {
		SkipIfSNO(oc.AsAdmin()) // This test makes no sense in SNO

		var (
			customMCPNames = "infra"
			numCustomPools = 5
			moscList       = []*MachineOSConfig{}
			mcpList        = []*MachineConfigPool{}
			wg             sync.WaitGroup
		)

		exutil.By("Create custom MCPS")
		for i := 0; i < numCustomPools; i++ {
			infraMcpName := fmt.Sprintf("%s-%d", customMCPNames, i)
			infraMcp, err := CreateCustomMCP(oc.AsAdmin(), infraMcpName, 0)
			defer infraMcp.delete()
			o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a new custom pool: %s", infraMcpName)
			mcpList = append(mcpList, infraMcp)

		}
		logger.Infof("OK!\n")

		exutil.By("Checking that all MOSCs were executed properly")
		for _, infraMcp := range mcpList {
			moscName := fmt.Sprintf("mosc-%s", infraMcp.GetName())

			mosc, err := CreateMachineOSConfigUsingInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, moscName, infraMcp.GetName(), nil)
			defer mosc.CleanupAndDelete()
			o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource")
			moscList = append(moscList, mosc)

			wg.Add(1)
			go func() {
				defer g.GinkgoRecover()
				defer wg.Done()

				ValidateSuccessfulMOSC(mosc, nil)
			}()
		}

		wg.Wait()
		logger.Infof("OK!\n")

		exutil.By("Removing all MOSC resources")
		for _, mosc := range moscList {
			o.Expect(mosc.CleanupAndDelete()).To(o.Succeed(), "Error cleaning up %s", mosc)
		}
		logger.Infof("OK!\n")

		exutil.By("Validate that all resources were garbage collected")
		for i := 0; i < numCustomPools; i++ {
			ValidateMOSCIsGarbageCollected(moscList[i], mcpList[i])
		}
		logger.Infof("OK!\n")

		exutil.AssertAllPodsToBeReady(oc.AsAdmin(), MachineConfigNamespace)
		logger.Infof("OK!\n")
	})

})

func testContainerFile(containerFiles []ContainerFile, imageNamespace string, mcp *MachineConfigPool, checkers []Checker) {
	var (
		oc       = mcp.GetOC().AsAdmin()
		moscName = "test-" + GetCurrentTestPolarionIDNumber()
		mosc     *MachineOSConfig
		err      error
	)
	exutil.By("Configure OCB functionality for the new infra MCP. Create MOSC")
	switch imageNamespace {
	case MachineConfigNamespace:
		mosc, err = CreateMachineOSConfigUsingInternalRegistry(oc, MachineConfigNamespace, moscName, mcp.GetName(), containerFiles)
	default:
		tmpNamespace := NewResource(oc.AsAdmin(), "ns", imageNamespace)
		if !tmpNamespace.Exists() {
			defer tmpNamespace.Delete()
			o.Expect(oc.AsAdmin().WithoutNamespace().Run("new-project").Args(tmpNamespace.GetName(), "--skip-config-write").Execute()).To(o.Succeed(), "Error creating a new project to store the OCL images")
		}
		mosc, err = CreateMachineOSConfigUsingInternalRegistry(oc, tmpNamespace.GetName(), moscName, mcp.GetName(), containerFiles)
	}
	defer DisableOCL(mosc)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource")
	logger.Infof("OK!\n")

	ValidateSuccessfulMOSC(mosc, checkers)

	exutil.By("Remove the MachineOSConfig resource")
	o.Expect(DisableOCL(mosc)).To(o.Succeed(), "Error cleaning up %s", mosc)
	logger.Infof("OK!\n")

	ValidateMOSCIsGarbageCollected(mosc, mcp)

}

func skipTestIfOCBIsEnabled(oc *exutil.CLI) {
	moscl := NewMachineOSConfigList(oc)
	allMosc := moscl.GetAllOrFail()
	if len(allMosc) != 0 {
		moscl.PrintDebugCommand()
		g.Skip(fmt.Sprintf("To run this test case we need that OCB is not enabled in any pool. At least %s OBC is enabled in this cluster.", allMosc[0]))
	}
}

func checkMisconfiguredMOSC(oc *exutil.CLI, moscName, poolName, currentImagePullSecret, baseImagePullSecret, renderedImagePushSecret, pushSpec string, containerFile []ContainerFile,
	expectedMsg, stepMgs string) {
	var (
		machineConfigCO = NewResource(oc.AsAdmin(), "co", "machine-config")
	)

	exutil.By(stepMgs)
	defer logger.Infof("OK!\n")

	logger.Infof("Create a misconfiugred MOSC")
	mosc, err := CreateMachineOSConfig(oc, moscName, poolName, currentImagePullSecret, baseImagePullSecret, renderedImagePushSecret, pushSpec, containerFile)
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

// ValidateMOSCIsGarbageCollected makes sure that all resources related to the provided MOSC have been removed
func ValidateMOSCIsGarbageCollected(mosc *MachineOSConfig, mcp *MachineConfigPool) {
	exutil.By("Check that the OCB resources are cleaned up")

	logger.Infof("Validating that MOSB resources were garbage collected")
	NewMachineOSBuildList(mosc.GetOC()).PrintDebugCommand() // for debugging purposes
	mosbs, err := mosc.GetMachineOSBuildList()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting MOSBs linked to %s", mosc)

	o.Eventually(mosbs, "2m", "20s").Should(o.HaveLen(0), "MachineSOBuilds were not cleaned when %s was removed", mosc)

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
	o.Eventually(mcp.GetLatestMachineOSBuildOrFail(), "5m", "20s").Should(Exist(),
		"No build was created when OCB was enabled")
	mosb := mcp.GetLatestMachineOSBuildOrFail()
	o.Eventually(mosb.GetPod).Should(Exist(),
		"No build pod was created when OCB was enabled")
	o.Eventually(mosb, "5m", "20s").Should(HaveConditionField("Building", "status", TrueString),
		"MachineOSBuild didn't report that the build has begun")
	logger.Infof("OK!\n")

	exutil.By("Check that a new build is successfully executed")
	o.Eventually(mosb, "10m", "20s").Should(HaveConditionField("Building", "status", FalseString), "Build was not finished")
	o.Eventually(mosb, "10m", "20s").Should(HaveConditionField("Succeeded", "status", TrueString), "Build didn't succeed")
	o.Eventually(mosb, "2m", "20s").Should(HaveConditionField("Interrupted", "status", FalseString), "Build was interrupted")
	o.Eventually(mosb, "2m", "20s").Should(HaveConditionField("Failed", "status", FalseString), "Build was failed")
	logger.Infof("Check that the build pod was deleted")
	o.Eventually(mosb.GetPod, "2m", "20s").ShouldNot(Exist(), "Build pod was not cleaned")
	logger.Infof("OK!\n")

	numNodes, err := mcp.getMachineCount()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting MachineCount from %s", mcp)
	if numNodes > 0 {
		exutil.By("Wait for the new image to be applied")
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		node := mcp.GetSortedNodesOrFail()[0]
		for _, checker := range checkers {
			checker.Check(node)
		}
	} else {
		logger.Infof("There is no node configured in %s. We don't wait for the configuration to be applied", mcp)
	}
}

// DisableOCL this function disables OCL. There is no way to disable it in a controlled way, it needs to be implemented.
// The only way to disable OCL in a pool is to delete all nodes and recreate them from scratch.
// Hence, we cant automate OCL tests using master pool
func DisableOCL(mosc *MachineOSConfig) error {
	if !mosc.Exists() {
		logger.Infof("%s does not exist. No need to remove/disable it", mosc)
		return nil
	}

	mcp, err := mosc.GetMachineConfigPool()
	if err != nil {
		return err

	}

	err = mosc.CleanupAndDelete()
	if err != nil {
		return err
	}

	allNodes, err := mcp.GetNodes()
	if err != nil {
		return err
	}

	logger.Infof("Removing all machines in pool %s to recreate the nodes", mcp.GetName())

	for _, node := range allNodes {
		machine, err := node.GetMachine()
		if err != nil {
			return err
		}

		if !machine.Exists() {
			return fmt.Errorf("%s was created using %s, but %s does not exist. Something went wrong", node, machine, machine)
		}

		err = machine.Delete("--wait=false")
		if err != nil {
			return err
		}
	}

	logger.Infof("Waiting for all Machinesets to be ready")

	allMachineSets, err := NewMachineSetList(mosc.GetOC(), "openshift-machine-api").GetAll()
	if err != nil {
		return err
	}

	for _, machineSet := range allMachineSets {
		err := machineSet.WaitUntilReady("15m")
		if err != nil {
			return err
		}
	}

	logger.Infof("Manually remove all nodes to make things faster")

	for _, node := range allNodes {
		err := node.Delete("--ignore-not-found=true")
		if err != nil {
			return err
		}
	}

	mcp.WaitImmediateForUpdatedStatus()
	return nil
}
