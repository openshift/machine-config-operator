package extended

import (
	"fmt"
	"strings"
	"sync"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO ocb longduration", func() {
	defer g.GinkgoRecover()

	var (
		oc = exutil.NewCLI("mco-ocb-longduration", exutil.KubeConfigPath())
	)

	g.JustBeforeEach(func() {
		PreChecks(oc)
		SkipTestIfOCBIsEnabled(oc)
	})

	g.It("[PolarionID:88202][Skipped:Disconnected] Both off-cluster and on-cluster layering can coexist on the same pool [Disruptive]", func() {
		var (
			testID         = GetCurrentTestPolarionIDNumber()
			osLayerMCName  = fmt.Sprintf("tc-%s-os-layer", testID)
			offClusterFile = "/etc/test-offlayering.test"
			onClusterFile  = "/etc/test-onlayering.test"
			containerFiles = []ContainerFile{{Content: fmt.Sprintf("RUN echo \"on-cluster layering %s\" > %s", testID, onClusterFile)}}
		)

		mcp := GetCompactCompatiblePool(oc.AsAdmin())
		node := mcp.GetSortedNodesOrFail()[0]
		offClusterRF := NewRemoteFile(node, offClusterFile)
		onClusterRF := NewRemoteFile(node, onClusterFile)

		exutil.By("Build a custom OS image for off-cluster layering")
		osImageBuilder := OsImageBuilderInNode{
			node:               node,
			dockerFileCommands: fmt.Sprintf("RUN echo \"off-cluster layering %s\" > %s\nRUN ostree container commit", testID, offClusterFile),
		}
		digestedImage, err := osImageBuilder.CreateAndDigestOsImage()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the custom osImage for off-cluster layering")
		logger.Infof("Built custom OS image: %s", digestedImage)
		logger.Infof("OK!\n")

		exutil.By("Apply osImageURL MachineConfig to pool (off-cluster layering)")
		osLayerMC := NewMachineConfig(oc.AsAdmin(), osLayerMCName, mcp.GetName())
		osLayerMC.parameters = []string{"OS_IMAGE=" + digestedImage}
		defer osLayerMC.DeleteWithWait()
		osLayerMC.create()
		logger.Infof("MachineConfig %s created with osImageURL %s", osLayerMC.GetName(), digestedImage)
		logger.Infof("OK!\n")

		exutil.By("Verify off-cluster layering is applied on the node")
		o.Expect(node.GetCurrentBootOSImage()).To(o.Equal(digestedImage),
			"Node %s should be running the off-cluster osImageURL %s", node, digestedImage)
		o.Expect(offClusterRF.Exists()).To(o.BeTrue(),
			"%s should exist on %s after off-cluster layering", offClusterFile, node)
		logger.Infof("Off-cluster layering verified: %s exists on %s", offClusterFile, node)
		logger.Infof("OK!\n")

		exutil.By("Enable on-cluster layering (OCL) with a containerFile")
		mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, mcp.GetName(), mcp.GetName(), containerFiles)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating MachineOSConfig for %s", mcp.GetName())
		defer DisableOCL(mosc)
		logger.Infof("OK!\n")

		exutil.By("Validate OCL build succeeds")
		ValidateSuccessfulMOSC(mosc, nil)
		logger.Infof("OK!\n")

		exutil.By("Verify both off-cluster and on-cluster layering files are present")
		o.Expect(offClusterRF.Exists()).To(o.BeTrue(),
			"%s should still exist alongside on-cluster layering", offClusterFile)
		o.Expect(onClusterRF.Exists()).To(o.BeTrue(),
			"%s should exist after on-cluster layering is applied", onClusterFile)
		logger.Infof("Both %s and %s are present on %s", offClusterFile, onClusterFile, node)
		logger.Infof("OK!\n")

		exutil.By("Record current MOSB before deleting off-cluster MC")
		currentMOSB, err := mosc.GetCurrentMachineOSBuild()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting current MOSB")
		currentMOSBName := currentMOSB.GetName()
		logger.Infof("Current MOSB before MC deletion: %s", currentMOSBName)
		logger.Infof("OK!\n")

		exutil.By("Delete the off-cluster MC")
		o.Expect(osLayerMC.Delete()).To(o.Succeed(),
			"Error deleting off-cluster MachineConfig %s", osLayerMCName)
		logger.Infof("Off-cluster MachineConfig %s deleted", osLayerMCName)
		logger.Infof("OK!\n")

		exutil.By("Wait for a new MachineOSBuild to be triggered after MC deletion")
		var newMOSB *MachineOSBuild
		o.Eventually(func() string {
			mosb, err := mosc.GetCurrentMachineOSBuild()
			if err != nil {
				return currentMOSBName
			}
			newMOSB = mosb
			return mosb.GetName()
		}, "5m", "20s").ShouldNot(o.Equal(currentMOSBName),
			"A new MOSB should be created after deleting the off-cluster MC")
		logger.Infof("OK!\n")

		exutil.By("Wait for the new build to succeed and deploy")
		o.Eventually(newMOSB, "20m", "20s").Should(HaveConditionField("Succeeded", "status", TrueString),
			"New MachineOSBuild didn't succeed after MC deletion")
		logger.Infof("New MOSB %s built successfully", newMOSB.GetName())
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Verify only on-cluster layering content remains after MC deletion")
		o.Expect(offClusterRF.Exists()).To(o.BeFalse(),
			"%s should be removed after off-cluster MC is deleted", offClusterFile)
		o.Expect(onClusterRF.Exists()).To(o.BeTrue(),
			"%s should still exist after off-cluster MC is deleted", onClusterFile)
		logger.Infof("Only on-cluster layering file remains on %s (as expected)", node)
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:79172][OTP] OCB Inherit from global pull secret if baseImagePullSecret field is not specified [Disruptive]", func() {
		var (
			infraMcpName = "infra"
		)

		exutil.By("Create custom infra MCP")
		// We add no workers to the infra pool, it is not necessary
		infraMcp, err := CreateCustomMCP(oc.AsAdmin(), infraMcpName, 0)
		defer infraMcp.delete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a new custom pool: %s", infraMcpName)
		logger.Infof("OK!\n")

		testContainerFile([]ContainerFile{}, MachineConfigNamespace, infraMcp, nil, true)
	})

	g.It("[PolarionID:83136][OTP][Skipped:Disconnected] Panic Condition for Non-Matching MOSC Resources [Disruptive]", func() {
		var (
			infraMcpName = "infra"
			// MOSC has to use the same name as the mcp
			moscName = infraMcpName
		)
		exutil.By("Create New Custom MCP")
		defer DeleteCustomMCP(oc.AsAdmin(), infraMcpName)
		infraMcp, err := CreateCustomMCP(oc.AsAdmin(), infraMcpName, 1)
		o.Expect(err).NotTo(o.HaveOccurred(), "Could not create a new custom MCP")
		node := infraMcp.GetNodesOrFail()[0]
		logger.Infof("%s", node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Configure OCB functionality for the new infra MCP")
		mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, moscName, infraMcpName, nil)
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
		o.Eventually(mosc.GetCurrentMachineOSBuild, "5m", "20s").Should(Exist(),
			"No build was created when OCB was enabled")
		mosb, err := mosc.GetCurrentMachineOSBuild()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting MOSB from MOSC")
		o.Eventually(mosb.GetJob, "5m", "20s").Should(Exist(),
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

		exutil.AssertAllPodsToBeReady(oc.AsAdmin(), MachineConfigNamespace)
		checkMCCPanic(oc)
	})

	g.It("[PolarionID:78001][OTP][Skipped:Disconnected] The etc-pki-etitlement secret is created automatically for OCB Use custom Containerfile with rhel enablement [Disruptive]", func() {
		var (
			entitlementSecret    = NewSecret(oc.AsAdmin(), "openshift-config-managed", "etc-pki-entitlement")
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

			mcp = GetCompactCompatiblePool(oc.AsAdmin())
		)

		if !entitlementSecret.Exists() {
			g.Skip(fmt.Sprintf("There is no entitlement secret available in this cluster %s. This test case cannot be executed", entitlementSecret))
		}

		testContainerFile([]ContainerFile{{Content: containerFileContent}}, MachineConfigNamespace, mcp, checkers, false)
	})

	g.It("[PolarionID:83137][OTP][Skipped:Disconnected] OCB use OutputImage CurrentImagePullSecret [Disruptive]", func() {
		var (
			mcp              = GetCompactCompatiblePool(oc.AsAdmin())
			tmpNamespaceName = fmt.Sprintf("tc-%s-mco-ocl-images", GetCurrentTestPolarionIDNumber())
			checkers         = []Checker{
				CommandOutputChecker{
					Command:  []string{"rpm-ostree", "status"},
					Matcher:  o.ContainSubstring(fmt.Sprintf("%s/%s/ocb-%s-image", InternalRegistrySvcURL, tmpNamespaceName, mcp.GetName())),
					ErrorMsg: fmt.Sprintf("The nodes are not using the expected OCL image stored in the internal registry"),
					Desc:     fmt.Sprintf("Check that the nodes are using the right OS image"),
				},
			}
		)

		testContainerFile([]ContainerFile{}, tmpNamespaceName, mcp, checkers, false)
	})

	g.It("[PolarionID:79137][OTP][Skipped:Disconnected] OCB Opting into on-cluster builds must respect maxUnavailable setting. Workers.[Disruptive]", func() {
		SkipIfCompactOrSNO(oc.AsAdmin()) // This test makes no sense in SNO or Compact

		var (
			wMcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			// MOSC has to use the same name as the mcp
			moscName    = wMcp.GetName()
			workerNodes = wMcp.GetSortedNodesOrFail()
		)

		exutil.By("Configure maxUnavailable if worker pool has more than 2 nodes")
		if len(workerNodes) > 2 {
			wMcp.SetMaxUnavailable(2)
			defer wMcp.RemoveMaxUnavailable()
		}

		maxUnavailable := OrFail[int](wMcp.GetMaxUnavailableInt())
		logger.Infof("Current maxUnavailable value %d", maxUnavailable)
		logger.Infof("OK!\n")

		exutil.By("Configure OCB functionality for the new worker MCP")
		mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, moscName, wMcp.GetName(), nil)
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

	g.It("[PolarionID:83139][OTP][Skipped:Disconnected] OCB build images in many MCPs at the same time [Disruptive]", func() {
		SkipIfCompactOrSNO(oc.AsAdmin()) // This test makes no sense in SNO or compact

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
			// MOSCs resources have to use the same name as the MCP
			moscName := infraMcp.GetName()

			mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, moscName, infraMcp.GetName(), nil)
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

		waitForAllMCOPodsReady(oc.AsAdmin(), 10*time.Minute)
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:77498][OTP] OCB Trigger new build when renderedImagePushspec is updated [Disruptive]", func() {

		var (
			infraMcpName = "infra"
			// MOSC resources have to use the same names as the MCP
			moscName = infraMcpName
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

		exutil.By("Set a new rendered image pull spec")
		initialMOSB := OrFail[*MachineOSBuild](mosc.GetCurrentMachineOSBuild())
		initialRIPS := OrFail[string](mosc.GetRenderedImagePushspec())
		o.Expect(
			mosc.SetRenderedImagePushspec(strings.ReplaceAll(initialRIPS, "ocb-", "ocb77498-")),
		).NotTo(o.HaveOccurred(), "Error patching %s to set the new renderedImagePullSpec", mosc)
		logger.Infof("OK!\n")

		exutil.By("Check that a new build is triggered")
		checkNewBuildIsTriggered(mosc, initialMOSB)
		logger.Infof("OK!\n")

		exutil.By("Set the original rendered image pull spec")
		o.Expect(
			mosc.SetRenderedImagePushspec(initialRIPS),
		).NotTo(o.HaveOccurred(), "Error patching %s to set the new renderedImagePullSpec", mosc)
		logger.Infof("OK!\n")

		exutil.By("Check that the initial build is reused")
		var currentMOSB *MachineOSBuild
		o.Eventually(func() (string, error) {
			currentMOSB, err = mosc.GetCurrentMachineOSBuild()
			return currentMOSB.GetName(), err
		}, "5m", "20s").Should(o.Equal(initialMOSB.GetName()),
			"When the containerfiles were removed and initial MOSC configuration was restored, the initial MOSB was not used")

		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:77497][OTP] OCB Trigger new build when Containerfile is updated [Disruptive]", func() {

		var (
			infraMcpName = "infra"
			// MOSC resources have to use the same names as the MCP
			moscName         = infraMcpName
			containerFile    = ContainerFile{Content: "RUN touch /etc/test-add-containerfile" + "\n" + ExpirationDockerfileLabel}
			containerFileMod = ContainerFile{Content: "RUN touch /etc/test-modified-containerfile" + "\n" + ExpirationDockerfileLabel}

			// We need to test a first MOSC without any container file, so we cannot add the expiration label in the first MOSC
			expireImage = false
			// We don't configure the pull secret in the MOSC, since it is optional
			defaultPullSecret = true
		)

		exutil.By("Create custom infra MCP")
		// We add no workers to the infra pool, it is not necessary
		infraMcp, err := CreateCustomMCP(oc.AsAdmin(), infraMcpName, 0)
		defer infraMcp.delete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a new custom pool: %s", infraMcpName)
		logger.Infof("OK!\n")

		exutil.By("Configure OCB functionality for the new infra MCP")
		mosc, err := createMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, moscName, infraMcpName, nil, defaultPullSecret, expireImage)

		defer mosc.CleanupAndDelete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource")
		logger.Infof("OK!\n")

		ValidateSuccessfulMOSC(mosc, nil)

		exutil.By("Add new container file")
		initialMOSB := OrFail[*MachineOSBuild](mosc.GetCurrentMachineOSBuild())

		o.Expect(
			mosc.SetContainerfiles([]ContainerFile{containerFile}),
		).NotTo(o.HaveOccurred(), "Error patching %s to add a container file", mosc)
		logger.Infof("OK!\n")

		exutil.By("Check that a new build is triggered when a containerfile is added")
		checkNewBuildIsTriggered(mosc, initialMOSB)
		logger.Infof("OK!\n")

		exutil.By("Modify the container file")
		currentMOSB := OrFail[*MachineOSBuild](mosc.GetCurrentMachineOSBuild())

		o.Expect(
			mosc.SetContainerfiles([]ContainerFile{containerFileMod}),
		).NotTo(o.HaveOccurred(), "Error patching %s to modify an existing container file", mosc)
		logger.Infof("OK!\n")

		exutil.By("Check that a new build is triggered when a containerfile is modified")
		checkNewBuildIsTriggered(mosc, currentMOSB)
		logger.Infof("OK!\n")

		exutil.By("Remove the container files")
		o.Expect(
			mosc.RemoveContainerfiles(),
		).NotTo(o.HaveOccurred(), "Error patching %s to remove the configured container files", mosc)
		logger.Infof("OK!\n")

		exutil.By("Check that the initial build is reused")
		o.Eventually(func() (string, error) {
			currentMOSB, err = mosc.GetCurrentMachineOSBuild()
			return currentMOSB.GetName(), err
		}, "5m", "20s").Should(o.Equal(initialMOSB.GetName()),
			"When the containerfiles were removed and initial MOSC configuration was restored, the initial MOSB was not used")

		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:77576][OTP] In OCB. Create a new MC while a build is running [Disruptive]", func() {

		var (
			mcp      = GetCompactCompatiblePool(oc.AsAdmin())
			node     = mcp.GetSortedNodesOrFail()[0]
			moscName = mcp.GetName()

			kArgs  = "test"
			mcName = "tc-77576-testkargs"
			mc     = NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName())
		)

		exutil.By("Configure OCB functionality for the new worker MCP")
		mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, moscName, mcp.GetName(), nil)
		defer DisableOCL(mosc)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource")
		logger.Infof("OK!\n")

		exutil.By("Check that a new build has been triggered and is building")
		var mosb *MachineOSBuild
		o.Eventually(func() (*MachineOSBuild, error) {
			var err error
			mosb, err = mosc.GetCurrentMachineOSBuild()
			return mosb, err
		}, "5m", "20s").Should(Exist(),
			"No build was created when OCB was enabled")
		o.Eventually(mosb.GetJob, "5m", "20s").Should(Exist(),
			"No build job was created when OCB was enabled")
		o.Eventually(mosb, "5m", "20s").Should(HaveConditionField("Building", "status", TrueString),
			"MachineOSBuild didn't report that the build has begun")
		logger.Infof("OK!\n")

		exutil.By("Create a MC to trigger a new build")
		defer mc.DeleteWithWait()
		err = mc.Create("-p", "NAME="+mcName, "-p", "POOL="+mcp.GetName(), "-p", fmt.Sprintf(`KERNEL_ARGS=["%s"]`, kArgs))
		o.Expect(err).NotTo(o.HaveOccurred())
		logger.Infof("OK!\n")

		exutil.By("Check that a new build is triggered and the old build is removed")
		checkNewBuildIsTriggered(mosc, mosb)
		o.Eventually(mosb, "2m", "20s").ShouldNot(Exist(), "The old MOSB %s was not deleted", mosb)
		logger.Infof("OK!\n")

		exutil.By("Wait for the configuration to be applied")
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check that the MC was applied")
		o.Expect(node.IsKernelArgEnabled(kArgs)).To(o.BeTrue(),
			"Kernel argument %s was not enabled in the node %s", kArgs, node.GetName())

		logger.Infof("OK!\n")

		exutil.By("Remove the MachineOSConfig resource")
		o.Expect(DisableOCL(mosc)).To(o.Succeed(), "Error cleaning up %s", mosc)
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:77977][OTP][Skipped:Disconnected] Install extension after OCB is enabled [Disruptive]", func() {

		var (
			mcp                     = GetCompactCompatiblePool(oc.AsAdmin())
			moscName                = mcp.GetName() // MOSC resources have to use the same name as the MCP
			node                    = mcp.GetSortedNodesOrFail()[0]
			mcName                  = "test-install-extension-" + GetCurrentTestPolarionIDNumber()
			applicableExtensions, _ = GetAllApplicableExtensionsToMCPOrFail(mcp)
		)

		exutil.By("Configure OCB functionality for the new worker MCP")
		mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, moscName, mcp.GetName(), nil)
		defer DisableOCL(mosc)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource")
		logger.Infof("OK!\n")

		ValidateSuccessfulMOSC(mosc, nil)

		exutil.By("Create a MC")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName())
		defer mc.DeleteWithWait()
		mc.parameters = []string{fmt.Sprintf(`EXTENSIONS=%s`, string(MarshalOrFail(applicableExtensions)))}
		mc.create()
		logger.Infof("OK!\n")

		exutil.By("Wait for the configuration to be applied")
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		CheckExtensions(node, applicableExtensions)

		exutil.By("Delete a MC.")
		mc.DeleteWithWait()
		logger.Infof("OK!\n")

		exutil.By("Remove the MachineOSConfig resource")
		o.Expect(DisableOCL(mosc)).To(o.Succeed(), "Error cleaning up %s", mosc)
		ValidateMOSCIsGarbageCollected(mosc, mcp)
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:78196][OTP][Skipped:Disconnected] Verify for etc-pki-etitlement secret is removed for  OCB rhel enablement [Disruptive]", func() {

		var (
			entitlementSecret    = NewSecret(oc.AsAdmin(), "openshift-config-managed", "etc-pki-entitlement")
			containerFileContent = `
		FROM configs AS final
		RUN rm -rf /etc/rhsm-host && \
		  rpm-ostree install buildah && \
		  ln -s /run/secrets/rhsm /etc/rhsm-host && \
		  ostree container commit
 `

			mcp = GetCompactCompatiblePool(oc.AsAdmin())
			// MOSC resources have to use the same name as the MCP
			moscName = mcp.GetName()
		)
		if !entitlementSecret.Exists() {
			g.Skip(fmt.Sprintf("There is no entitlement secret available in this cluster %s. This test case cannot be executed", entitlementSecret))
		}

		exutil.By("Copy the entitlement secret in MCO namespace")
		mcoEntitlementSecret, err := CloneResource(entitlementSecret, "etc-pki-entitlement", MachineConfigNamespace, nil)
		defer mcoEntitlementSecret.Delete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error copying %s to the %s namespace", mcoEntitlementSecret, MachineConfigNamespace)
		logger.Infof("OK!\n")

		exutil.By("Delete the entitlement secret in the openshift-config-managed namespace")
		defer func() {
			exutil.By("Recover the entitlement secret in the openshift-config-managed namespace")
			recoverSecret, err := CloneResource(mcoEntitlementSecret, "etc-pki-entitlement", "openshift-config-managed", nil)
			o.Expect(err).NotTo(o.HaveOccurred(), "Error copying %s to the openshift-config-managed namespace", entitlementSecret)
			o.Expect(recoverSecret).To(Exist(), "Unable to recover the entitlement secret in openshift-config-managed namespace")
		}()

		entitlementSecret.Delete()
		logger.Infof("OK!\n")

		exutil.By("Create the MOSC")
		mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, moscName, mcp.GetName(), []ContainerFile{{Content: containerFileContent}})
		defer DisableOCL(mosc)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource")
		logger.Infof("OK!\n")

		exutil.By("Check that a new build has been triggered")
		o.Eventually(mosc.GetCurrentMachineOSBuild, "5m", "20s").Should(Exist(),
			"No build was created when OCB was enabled")
		mosb, err := mosc.GetCurrentMachineOSBuild()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting MOSB from MOSC")
		o.Eventually(mosb, "5m", "20s").Should(HaveConditionField("Building", "status", TrueString),
			"MachineOSBuild didn't report that the build has begun")
		logger.Infof("OK!\n")

		exutil.By("Verify the error is produced in buildPod")
		exutil.AssertAllNonJobPodsToBeReadyWithPollerParams(mosc.GetOC(), MachineConfigNamespace, 10*time.Second, 10*time.Minute)
		logger.Infof("OK!\n")

		job, err := mosb.GetJob()
		o.Expect(err).NotTo(o.HaveOccurred())
		// Currently this kind of resources are leaked. Until the leak is fixed we need to make sure that this job is removed
		//  because its pods are in "Error" status and there are other test cases checking that no pod is reporting any error.
		// TODO: remove this once the leak is fixed
		defer job.Delete()
		logger.Infof("OK!\n")

		o.Eventually(func() (string, error) {
			return job.Logs("-c", "image-build")
		}, "5m", "10s").Should(o.ContainSubstring("Found 0 entitlement certificates"), "Error getting the logs")
		o.Eventually(job, "15m", "20s").Should(HaveConditionField("Failed", "status", TrueString), "Job didn't fail")
		logger.Infof("OK!\n")

		exutil.By("Remove the MachineOSConfig resource")
		o.Expect(DisableOCL(mosc)).To(o.Succeed(), "Error cleaning up %s", mosc)
		ValidateMOSCIsGarbageCollected(mosc, mcp)
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:83755][OTP] In OCL check no new image is applied on node after applying ssh/password/file MC .[Disruptive]", func() {
		var (
			mcp  = GetCompactCompatiblePool(oc.AsAdmin())
			node = mcp.GetSortedNodesOrFail()[0]

			moscName = mcp.GetName()
			mcName   = fmt.Sprintf("test-ssh-%s", GetCurrentTestPolarionIDNumber())

			_, key = GenerateSSHKeyPairOrFail()
			user   = ign32PaswdUser{Name: "core", SSHAuthorizedKeys: []string{key}}
		)

		exutil.By("Configure OCB functionality for the new worker MCP")
		mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, moscName, mcp.GetName(), nil)
		defer DisableOCL(mosc)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource")
		logger.Infof("Applied MOSC!\n")

		ValidateSuccessfulMOSC(mosc, nil)
		logger.Infof("MOSC is applied!\n")

		exutil.By("Get the image that is currently applied on nodes")
		initialImage := OrFail[string](node.GetRpmOstreeStatus(false))
		logger.Infof("Initial image: %s", initialImage)
		logger.Infof("Got the initial image!\n")

		exutil.By("Create a new MC to deploy new authorized keys")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName())
		mc.parameters = []string{fmt.Sprintf(`PWDUSERS=[%s]`, MarshalOrFail(user))}
		mc.skipWaitForMcp = true

		mc.create()
		defer mc.DeleteWithWait()
		logger.Infof("Created MC!\n")

		exutil.By("Check that the build is triggered with succeed status and not building")
		mosb, err := mosc.GetCurrentMachineOSBuild()
		logger.Infof("MOSB: %s\n", mosb)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting MOSB from MOSC")
		o.Expect(mosb).To(HaveConditionField("Building", "status", FalseString), "Build is still building")
		o.Expect(mosb).To(HaveConditionField("Succeeded", "status", TrueString), "Build didn't succeed")
		logger.Infof("Checked that the build does not take place!\n")

		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check that the image is not updated")
		o.Expect(OrFail[string](node.GetRpmOstreeStatus(false))).To(o.Equal(initialImage), "Image was updated")
		logger.Infof("Image is not updated!\n")

		exutil.By("Check that all expected keys are present and with the right permissions and owners")
		currentMc := OrFail[*MachineConfig](mcp.GetConfiguredMachineConfig())
		initialKeys := OrFail[[]string](currentMc.GetAuthorizedKeysByUserAsList("core"))
		checkAuthorizedKeyInNode(node, append(initialKeys, key))
		logger.Infof("MC is configured with the expected keys!\n")

	})

	g.It("[PolarionID:88801][OTP][Skipped:Disconnected] ExternalRegistry OCB Verify new nodes boot directly with OCL image without unnecessary reboots [Disruptive]", func() {
		SkipIfCompactOrSNO(oc.AsAdmin())              // This test requires scaling, which doesn't make sense in SNO or Compact
		skipTestIfWorkersCannotBeScaled(oc.AsAdmin()) // Skip test if worker node cannot be scaled

		var (
			mcp      = GetCompactCompatiblePool(oc.AsAdmin())
			moscName = mcp.GetName()
		)

		exutil.By("Configure OCB functionality using external registry (Quay)")
		mosc, err := CreateMachineOSConfigUsingExternalRegistry(oc.AsAdmin(), moscName, mcp.GetName(), nil, false, false)
		defer DisableOCL(mosc)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource")
		logger.Infof("OK!\n")

		ValidateNewNodesBootDirectlyWithOCLImage(oc.AsAdmin(), mosc, mcp)
	})

	g.It("[PolarionID:85843][OTP][Skipped:Disconnected] InternalRegistry OCB Verify new nodes boot directly with OCL image without unnecessary reboots [Disruptive]", func() {
		SkipIfCompactOrSNO(oc.AsAdmin())                  // This test requires scaling, which doesn't make sense in SNO or Compact
		skipTestIfWorkersCannotBeScaled(oc.AsAdmin())     // Skip test if worker node cannot be scaled
		SkipTestIfCannotUseInternalRegistry(oc.AsAdmin()) // Skip test if cannot use internal registry

		var (
			mcp      = GetCompactCompatiblePool(oc.AsAdmin())
			moscName = mcp.GetName()
		)

		exutil.By("Configure OCB functionality for the compatible MCP")
		mosc, err := CreateMachineOSConfigUsingInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, moscName, mcp.GetName(), nil, true)
		defer DisableOCL(mosc)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource")
		logger.Infof("OK!\n")

		ValidateNewNodesBootDirectlyWithOCLImage(oc.AsAdmin(), mosc, mcp)
	})

	g.It("[PolarionID:85980][OTP][Skipped:Disconnected] Check when MOSB is degraded MCP should be degraded too with right fields updated. [Disruptive]", func() {
		var (
			infraMcpName = "infra"
			moscName     = infraMcpName
			// Intentionally uses 'apt' on Alpine (which uses 'apk'), causing build to fail
			incorrectContainerFile  = "FROM alpine:3.18\nRUN apt update && apt install -y cowsay\n"
			correctContainerFile    = "FROM configs AS final\nRUN echo \"hello\" > /etc/test.txt\n"
			expectedDegradedMessage = "Failed to build OS image"
		)

		exutil.By("Create custom infra MCP")
		infraMcp, err := CreateCustomMCP(oc.AsAdmin(), infraMcpName, 1)
		defer DeleteCustomMCP(oc.AsAdmin(), infraMcpName)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating custom MCP: %s", infraMcpName)
		node := infraMcp.GetNodesOrFail()[0]
		logger.Infof("Infra node: %s\n", node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Create MOSC with incorrect containerfile")
		mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, moscName, infraMcpName,
			[]ContainerFile{{Content: incorrectContainerFile}})
		defer mosc.CleanupAndDelete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating MOSC with incorrect containerfile")
		logger.Infof("OK!\n")

		exutil.By("Verify MOSB is created and fails")
		o.Eventually(mosc.GetCurrentMachineOSBuild, "5m", "20s").Should(Exist(),
			"No MOSB was created for the incorrect containerfile")
		failedMOSB, err := mosc.GetCurrentMachineOSBuild()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting MOSB from MOSC")
		logger.Infof("MOSB created: %s\n", failedMOSB.GetName())
		o.Eventually(failedMOSB, "20m", "20s").Should(HaveConditionField("Failed", "status", TrueString),
			"MOSB should fail due to incorrect containerfile")
		logger.Infof("MOSB failed as expected\n")

		exutil.By("Verify MCP is degraded with ImageBuildDegraded condition")
		o.Eventually(infraMcp, "5m", "20s").Should(BeDegraded(), "MCP should be degraded when build fails")
		o.Eventually(infraMcp, "5m", "20s").Should(HaveConditionField("ImageBuildDegraded", "status", TrueString),
			"MCP ImageBuildDegraded should be True when build fails")
		o.Eventually(infraMcp, "5m", "20s").Should(HaveConditionField("ImageBuildDegraded", "message", o.ContainSubstring(expectedDegradedMessage)),
			"MCP degradation message should indicate OS image build failure")
		o.Eventually(infraMcp, "5m", "20s").Should(HaveConditionField("Degraded", "message", o.ContainSubstring("Custom OS image build failed")),
			"MCP Degraded message should indicate custom OS image build failed")
		logger.Infof("OK!\n")

		exutil.By("Fix the MOSC by updating containerfile")
		o.Expect(mosc.SetContainerfiles([]ContainerFile{{Content: correctContainerFile}})).NotTo(o.HaveOccurred(),
			"Error updating MOSC with correct containerfile")
		logger.Infof("OK!\n")

		exutil.By("Verify new MOSB is created and succeeds")
		checkNewBuildIsTriggered(mosc, failedMOSB)
		newMOSB, err := mosc.GetCurrentMachineOSBuild()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting new MOSB")
		logger.Infof("New MOSB created: %s\n", newMOSB.GetName())

		exutil.By("Verify old failed MOSB is deleted")
		o.Eventually(failedMOSB, "5m", "20s").ShouldNot(Exist(),
			"Old failed MOSB should be garbage collected: %s", failedMOSB.GetName())
		logger.Infof("OK!\n")

		exutil.By("Verify MCP recovers and is no longer degraded")
		// Check both ImageBuildDegraded (specific condition) and Degraded (overall status)
		// to ensure complete recovery from the failed build state
		o.Eventually(infraMcp, "5m", "20s").Should(HaveConditionField("ImageBuildDegraded", "status", FalseString),
			"MCP ImageBuildDegraded condition should be False after recovery")
		o.Eventually(infraMcp, "5m", "20s").ShouldNot(BeDegraded(),
			"MCP Degraded condition should be False after recovery")
		o.Eventually(infraMcp, "5m", "20s").Should(HaveConditionField("Updating", "status", TrueString),
			"MCP should be updating after successful build")
		logger.Infof("OK!\n")

		exutil.By("Wait for MCP to complete update")
		infraMcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Verify image is applied to node")
		currentImagePullSpec := OrFail[string](mosc.GetStatusCurrentImagePullSpec())
		o.Expect(node.GetCurrentBootOSImage()).To(o.Equal(currentImagePullSpec),
			"Node should be using the correct OCL image after recovery")
		logger.Infof("Node is using image: %s\n", currentImagePullSpec)
		logger.Infof("OK!\n")

		exutil.By("Verify file created in containerfile exists on node")
		output, err := node.DebugNodeWithChroot("cat", "/etc/test.txt")
		o.Expect(err).NotTo(o.HaveOccurred(), "Error reading test file from node")
		o.Expect(output).To(o.ContainSubstring("hello"),
			"Test file content should match what was created in containerfile")
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:87176][OTP][Skipped:Disconnected] External Registry In OCB to check when a image is removed the old build is triggered again and the MC should start updating directly. [Disruptive]", func() {
		SkipIfCompactOrSNO(oc.AsAdmin())

		var (
			infraMcpName              = "infra"
			mcName                    = fmt.Sprintf("tc-%s-ext-kernelarg", GetCurrentTestPolarionIDNumber())
			kArgs                     = "test"
			containerFileNoExpiration = "FROM configs AS final\nLABEL maintainer=\"mco@example.com\"\n"
		)

		exutil.By("Create custom infra MCP")
		infraMcp, err := CreateCustomMCP(oc.AsAdmin(), infraMcpName, 1)
		defer DeleteCustomMCP(oc.AsAdmin(), infraMcpName)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a new custom pool: %s", infraMcpName)
		logger.Infof("OK!\n")

		exutil.By("Create MOSC for custom MCP using external registry")
		moscName := infraMcp.GetName()
		mosc, err := CreateMachineOSConfigUsingExternalRegistry(oc.AsAdmin(), moscName, infraMcp.GetName(),
			[]ContainerFile{{Content: containerFileNoExpiration}}, false, false)
		defer DisableOCL(mosc)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating MOSC")
		logger.Infof("OK!\n")

		exutil.By("Validate initial MOSC and wait for MOSB-1 to succeed")
		ValidateSuccessfulMOSC(mosc, nil)

		mosb1, err := mosc.GetCurrentMachineOSBuild()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting initial MOSB")
		logger.Infof("Initial MOSB created: %s\n", mosb1.GetName())

		exutil.By("Apply MC with kernel argument to trigger new MOSB")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, infraMcp.GetName())
		mc.skipWaitForMcp = true

		defer mc.DeleteWithWait()

		err = mc.Create("-p", "NAME="+mcName, "-p", "POOL="+infraMcp.GetName(),
			"-p", fmt.Sprintf(`KERNEL_ARGS=["%s"]`, kArgs))
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating MachineConfig %s", mc.GetName())
		logger.Infof("OK!\n")

		exutil.By("Wait for MOSB-2 to be created and succeed")
		checkNewBuildIsTriggered(mosc, mosb1)
		mosb2, err := mosc.GetCurrentMachineOSBuild()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting MOSB-2")
		logger.Infof("Second MOSB created: %s\n", mosb2.GetName())

		exutil.By("Wait for MCP to complete update")
		infraMcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Delete MOSB-1 image from Quay using automated skopeo deletion")
		o.Expect(removeQuayImageUsingSkepo(oc.AsAdmin(), mosb1, infraMcp)).To(o.BeTrue(), "Error deleting Quay image for MOSB-1")
		logger.Infof("OK!\n")

		// Common verification after MOSB-1 deletion
		verifyMOSBRebuildAfterImageDeletion(infraMcp, mosc, mosb1, mosb2, mc, mcName)
	})

	g.It("[PolarionID:82536][OTP][Skipped:Disconnected] Internal Registry In OCB to check when a image is removed the old build is triggered again and the MC should start updating directly. [Disruptive]", func() {
		SkipIfCompactOrSNO(oc.AsAdmin())
		SkipTestIfCannotUseInternalRegistry(oc.AsAdmin()) // This test case requires the internal registry to be enabled

		var (
			infraMcpName = "infra"
			mcName       = fmt.Sprintf("tc-%s-int-kernelarg", GetCurrentTestPolarionIDNumber())
			kArgs        = "test"
		)

		exutil.By("Create custom infra MCP")
		infraMcp, err := CreateCustomMCP(oc.AsAdmin(), infraMcpName, 1)
		defer DeleteCustomMCP(oc.AsAdmin(), infraMcpName)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a new custom pool: %s", infraMcpName)
		logger.Infof("OK!\n")

		exutil.By("Create MOSC for custom MCP using internal registry")
		moscName := infraMcp.GetName()
		mosc, err := CreateMachineOSConfigUsingInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, moscName, infraMcp.GetName(), nil, false)
		defer DisableOCL(mosc)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating MOSC")
		logger.Infof("OK!\n")

		exutil.By("Validate initial MOSC and wait for MOSB-1 to succeed")
		ValidateSuccessfulMOSC(mosc, nil)

		mosb1, err := mosc.GetCurrentMachineOSBuild()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting initial MOSB")
		logger.Infof("Initial MOSB created: %s\n", mosb1.GetName())

		exutil.By("Apply MC with kernel argument to trigger new MOSB")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, infraMcp.GetName())
		mc.skipWaitForMcp = true

		defer mc.DeleteWithWait()

		err = mc.Create("-p", "NAME="+mcName, "-p", "POOL="+infraMcp.GetName(),
			"-p", fmt.Sprintf(`KERNEL_ARGS=["%s"]`, kArgs))
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating MachineConfig %s", mc.GetName())
		logger.Infof("OK!\n")

		exutil.By("Wait for MOSB-2 to be created and succeed")
		checkNewBuildIsTriggered(mosc, mosb1)
		mosb2, err := mosc.GetCurrentMachineOSBuild()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting MOSB-2")
		logger.Infof("Second MOSB created: %s\n", mosb2.GetName())

		exutil.By("Delete MOSB-1 image from internal registry using oc tag -d")
		o.Expect(removeImageStream(oc.AsAdmin(), mosb1)).To(o.BeTrue(), "Error deleting imagestream tag for MOSB-1")
		logger.Infof("OK!\n")

		// Common verification after MOSB-1 deletion
		verifyMOSBRebuildAfterImageDeletion(infraMcp, mosc, mosb1, mosb2, mc, mcName)
	})

})
