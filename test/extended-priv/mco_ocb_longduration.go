package extended

import (
	"fmt"
	"os"
	"path/filepath"
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

func checkNewBuildIsTriggered(mosc *MachineOSConfig, currentMOSB *MachineOSBuild) {
	var (
		newMOSB *MachineOSBuild
		err     error
	)
	logger.Infof("Current mosb: %s", currentMOSB)
	o.Eventually(func() (string, error) {
		newMOSB, err = mosc.GetCurrentMachineOSBuild()
		if err != nil || newMOSB == nil {
			return "", err
		}
		return newMOSB.GetName(), nil
	}, "5m", "20s").ShouldNot(o.Equal(currentMOSB.GetName()),
		"A new MOSB should be created after the new rendered image pull spec is configured")

	logger.Infof("New mosb: %s", newMOSB)

	o.Eventually(newMOSB, "5m", "20s").Should(HaveConditionField("Building", "status", TrueString),
		"MachineOSBuild didn't report that the build has begun")

	o.Eventually(newMOSB, "20m", "20s").Should(HaveConditionField("Building", "status", FalseString), "Build was not finished")
	o.Eventually(newMOSB, "10m", "20s").Should(HaveConditionField("Succeeded", "status", TrueString), "Build didn't succeed")
	o.Eventually(newMOSB, "2m", "20s").Should(HaveConditionField("Interrupted", "status", FalseString), "Build was interrupted")
	o.Eventually(newMOSB, "2m", "20s").Should(HaveConditionField("Failed", "status", FalseString), "Build was failed")
}

func getNewNodeRebootValueForOCL(newNode *Node, oclImage string) {
	exutil.By("Verify the new node boots directly with the OCL image")
	currentImage, err := newNode.GetCurrentBootOSImage()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting current boot OS image from node %s", newNode.GetName())
	o.Expect(currentImage).To(o.Equal(oclImage), "New node %s is not using the OCL image. Current: %s, Expected: %s", newNode.GetName(), currentImage, oclImage)
	logger.Infof("OK!\n")

	exutil.By("Verify /etc/machine-config-daemon/currentimage matches the OCL image")
	currentImageFile, err := newNode.DebugNodeWithChroot("cat", "/etc/machine-config-daemon/currentimage")
	o.Expect(err).NotTo(o.HaveOccurred(), "Error reading currentimage file from node %s", newNode.GetName())
	o.Expect(currentImageFile).To(o.ContainSubstring(oclImage), "currentimage file does not contain the OCL image")
	logger.Infof("OK!\n")

	exutil.By("Verify rpm-ostree status shows the OCL image")
	rpmOstreeStatus, err := newNode.DebugNodeWithChroot("rpm-ostree", "status")
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting rpm-ostree status from node %s", newNode.GetName())
	o.Expect(rpmOstreeStatus).To(o.ContainSubstring(oclImage), "rpm-ostree status does not show the OCL image")
	logger.Infof("OK!\n")

	exutil.By("Verify no unnecessary reboots occurred - desiredImage file should not exist")
	desiredImageFile, err := newNode.DebugNodeWithChroot("cat", "/etc/machine-config-daemon/desiredImage")
	o.Expect(err).To(o.HaveOccurred(), "desiredImage file should not exist when node boots with correct OCL image")
	o.Expect(desiredImageFile).Should(o.ContainSubstring("No such file or directory"),
		"Expected desiredImage file to not exist, indicating no OS upgrade was needed")
	logger.Infof("OK!\n")
}

func ValidateNewNodesBootDirectlyWithOCLImage(oc *exutil.CLI, mosc *MachineOSConfig, mcp *MachineConfigPool) {
	ValidateSuccessfulMOSC(mosc, nil)

	exutil.By("Get the OCL image")
	oclImage := OrFail[string](mosc.GetStatusCurrentImagePullSpec())
	logger.Infof("OCL image: %s\n", oclImage)

	isInternalRegistry := OrFail[bool](mosc.IsUsingInternalRegistry())

	exutil.By("Check able to scale the node from existing Machineset")
	msl, err := NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAll()
	o.Expect(err).NotTo(o.HaveOccurred(), "Get machinesets failed")
	o.Expect(msl).ShouldNot(o.BeEmpty(), "Machineset list is empty")
	existingMS := msl[0]

	o.Expect(existingMS.AddToScale(1)).NotTo(o.HaveOccurred())

	defer func() {
		exutil.By("Scale down the node from existing Machineset")
		existingMS.AddToScale(-1)
		mcp.waitForComplete()
	}()

	exutil.By("Create duplicate machineset and scale new node")
	machineset := OrFail[*MachineSet](GetScalableMachineSet(oc.AsAdmin()))
	duplicateMSName := machineset.GetName() + "-ocl"
	duplicateMS, err := machineset.Duplicate(duplicateMSName)
	o.Expect(err).NotTo(o.HaveOccurred())

	defer func() {
		if duplicateMS.Exists() {
			logger.Infof("Deleting duplicate machineset %s and scale down the node", duplicateMS.GetName())
			o.Expect(duplicateMS.ScaleTo(0)).To(o.Succeed())
			o.Expect(duplicateMS.WaitUntilReady("10m")).To(o.Succeed())
			o.Expect(duplicateMS.Delete()).To(o.Succeed())
		}
	}()
	o.Expect(duplicateMS.ScaleTo(1)).To(o.Succeed())

	exutil.By("Wait for both existing new node added and for duplicate new node added to get ready")

	o.Eventually(func(gm o.Gomega) {
		gm.Expect(existingMS.GetIsReady()).To(o.BeTrue(), "MachineSet %s is not ready", existingMS.GetName())
		gm.Expect(duplicateMS.GetIsReady()).To(o.BeTrue(), "MachineSet %s is not ready", duplicateMS.GetName())
	}, "30m", "2m").Should(o.Succeed(), "MachineSets are not ready")

	if isInternalRegistry {
		logger.Infof("Internal registry detected, waiting for MCP to complete (requires 2 reboots)")
		mcp.waitForComplete()
	}
	logger.Infof("OK!\n")

	exutil.By("Get the new node created by the machine set")

	existingMSNodes, nErr := existingMS.GetNodes()
	o.Expect(nErr).NotTo(o.HaveOccurred(), "Error getting the nodes created by MachineSet %s", existingMS.GetName())
	existingNode := existingMSNodes[len(existingMSNodes)-1]
	logger.Infof("Existing node: %s\n", existingNode.GetName())
	logger.Infof("OK!\n")

	duplicateMSNodes, nErr := duplicateMS.GetNodes()
	o.Expect(nErr).NotTo(o.HaveOccurred(), "Error getting the nodes created by MachineSet %s", duplicateMS.GetName())
	duplicateMSNode := duplicateMSNodes[len(duplicateMSNodes)-1]
	logger.Infof("Duplicate node: %s\n", duplicateMSNode.GetName())
	logger.Infof("OK!\n")

	getNewNodeRebootValueForOCL(duplicateMSNode, oclImage)
	getNewNodeRebootValueForOCL(existingNode, oclImage)

	exutil.By("Wait for MCP to complete before scaling down nodes")
	mcp.waitForComplete()
	logger.Infof("OK!\n")
}

func removeImageStream(oc *exutil.CLI, mosb *MachineOSBuild) bool {
	mosc, err := mosb.GetMachineOSConfig()
	if err != nil {
		logger.Errorf("Error getting MOSC from MOSB: %s", err)
		return false
	}

	pushSpec, err := mosc.GetRenderedImagePushspec()
	if err != nil {
		logger.Errorf("Error getting renderedImagePushspec from MOSC: %s", err)
		return false
	}

	parts := strings.Split(pushSpec, "/")
	if len(parts) < 3 {
		logger.Errorf("Invalid push spec format: %s", pushSpec)
		return false
	}
	imageNameWithTag := parts[len(parts)-1]
	imageName := strings.Split(imageNameWithTag, ":")[0]

	logger.Infof("Deleting imagestream tag: %s:%s", imageName, mosb.GetName())
	imagestream, err := oc.AsAdmin().WithoutNamespace().Run("tag").Args("-d", imageName+":"+mosb.GetName(), "-n", MachineConfigNamespace).Output()

	if err != nil {
		logger.Errorf("Error deleting imagestream: %s", imagestream)
		return false
	}
	return true
}

func removeQuayImageUsingSkepo(oc *exutil.CLI, mosb *MachineOSBuild, mcp *MachineConfigPool) bool {
	mosc, err := mosb.GetMachineOSConfig()
	if err != nil {
		logger.Errorf("Error getting MOSC from MOSB: %s", err)
		return false
	}

	digestedImage, err := mosb.GetStatusDigestedImagePullSpec()
	if err != nil {
		logger.Errorf("Error getting digested image from MOSB: %s", err)
		return false
	}

	logger.Infof("Attempting to delete Quay image: %s", digestedImage)

	pushSecretName, err := mosc.Get(`{.spec.renderedImagePushSecret.name}`)
	if err != nil || pushSecretName == "" {
		logger.Errorf("Error getting push secret name from MOSC: %s", err)
		return false
	}

	logger.Infof("Using push secret for deletion: %s", pushSecretName)

	pushSecret := NewSecret(oc, MachineConfigNamespace, pushSecretName)
	secretDir, err := pushSecret.Extract()
	if err != nil {
		logger.Errorf("Error extracting push secret: %s", err)
		return false
	}
	defer func() {
		logger.Infof("Cleaning up local secret directory: %s", secretDir)
		os.RemoveAll(secretDir)
	}()
	logger.Infof("Push secret extracted to local directory: %s", secretDir)

	localAuthFile := filepath.Join(secretDir, ".dockerconfigjson")

	nodes, err := mcp.GetNodes()
	if err != nil || len(nodes) == 0 {
		logger.Errorf("Error getting nodes from MCP: %s", err)
		return false
	}
	node := nodes[0]

	logger.Infof("Running skopeo delete on node: %s", node.GetName())

	tmpAuthFile := "/tmp/skopeo-delete-auth-" + exutil.GetRandomString() + ".json"

	defer func() {
		logger.Infof("Cleaning up temporary auth file on node")
		node.DebugNodeWithChroot("rm", "-f", tmpAuthFile)
	}()

	err = node.CopyFromLocal(localAuthFile, tmpAuthFile)
	if err != nil {
		logger.Errorf("Error copying auth file to node: %s", err)
		return false
	}

	skopeoCommand := fmt.Sprintf("skopeo delete --authfile %s docker://%s", tmpAuthFile, digestedImage)
	fullCommand := fmt.Sprintf("set -a; source /etc/mco/proxy.env; %s", skopeoCommand)

	logger.Infof("Executing command on node: %s", skopeoCommand)

	stdout, stderr, err := node.DebugNodeWithChrootStd("sh", "-c", fullCommand)
	if err != nil {
		logger.Errorf("Error deleting Quay image via skopeo on node. Stdout: %s, Stderr: %s, Error: %s", stdout, stderr, err)
		return false
	}

	logger.Infof("Successfully deleted Quay image: %s", digestedImage)
	logger.Infof("Skopeo output: %s", stdout)
	return true
}

func verifyMOSBRebuildAfterImageDeletion(mcp *MachineConfigPool, mosc *MachineOSConfig, mosb1, mosb2 *MachineOSBuild, mc *MachineConfig, mcName string) {
	exutil.By("Delete MC")
	err := mc.Delete()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error deleting MC")
	logger.Infof("OK!\n")

	exutil.By("Verify MOSB-1 is re-triggered (starts building again)")
	o.Eventually(mosb1, "5m", "20s").Should(HaveConditionField("Building", "status", TrueString),
		"MOSB-1 should be re-triggered and start building again after image deletion")
	logger.Infof("MOSB-1 is re-building\n")

	o.Eventually(mosb1, "35m", "20s").Should(HaveConditionField("Building", "status", FalseString),
		"MOSB-1 rebuild should complete")
	o.Eventually(mosb1, "10m", "20s").Should(HaveConditionField("Succeeded", "status", TrueString),
		"MOSB-1 rebuild should succeed")
	logger.Infof("OK!\n")

	exutil.By("Wait for MCP to complete update")
	mcp.waitForComplete()
	logger.Infof("OK!\n")

	exutil.By("Re-apply MC")
	mc.skipWaitForMcp = true
	defer mc.DeleteWithWait()
	err = mc.Create("-p", "NAME="+mcName, "-p", "POOL="+mcp.GetName(),
		"-p", fmt.Sprintf(`KERNEL_ARGS=["%s"]`, "test"))
	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating MachineConfig %s", mc.GetName())
	logger.Infof("OK!\n")

	exutil.By("Verify MOSB-2 is NOT triggered again")
	o.Consistently(func() bool {
		buildingStatus, err := mosb2.Get(`{.status.conditions[?(@.type=="Building")].status}`)
		if err != nil || buildingStatus == "" {
			return false
		}
		return buildingStatus == TrueString
	}, "2m", "10s").Should(o.BeFalse(), "MOSB-2 should NOT start building again")
	logger.Infof("OK!\n")

	exutil.By("Verify MCP starts updating instead of triggering a new/old MOSB")
	o.Eventually(mcp, "5m", "20s").Should(HaveConditionField("Updating", "status", TrueString),
		"MCP should start updating instead of triggering a new/old MOSB")
	logger.Infof("OK!\n")

	exutil.By("Wait for MCP to complete update")
	mcp.waitForComplete()
	logger.Infof("OK!\n")

	exutil.By("Verify no new MOSB-3 was created during MCP update")
	currentMosb, err := mosc.GetCurrentMachineOSBuild()
	logger.Infof("Current MOSB: %s\n", currentMosb.GetName())
	logger.Infof("MOSB-1: %s\n", mosb1.GetName())
	logger.Infof("MOSB-2: %s\n", mosb2.GetName())
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting current MOSB")
	o.Expect(currentMosb.GetName()).To(o.Or(o.Equal(mosb1.GetName()), o.Equal(mosb2.GetName())),
		"Current MOSB should be either MOSB-1 or MOSB-2, no new MOSB-3 should be triggered")
	logger.Infof("OK!\n")
}
