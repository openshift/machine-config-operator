package extended

import (
	"fmt"
	"strconv"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO MachineConfigNode", func() {

	defer g.GinkgoRecover()

	var (
		oc = exutil.NewCLI("mco-machineconfignode", exutil.KubeConfigPath())
	)

	g.JustBeforeEach(func() {
		PreChecks(oc)
	})

	g.It("[PolarionID:69184][OTP] Enable feature gate MachineConfigNodes [apigroup:machineconfiguration.openshift.io]", func() {
		// need to check whether featureGate MachineConfigNodes is in enabled list
		exutil.By("Check whether featureGate: MachineConfigNodes is enabled")
		featureGate := NewResource(oc.AsAdmin(), "featuregate", "cluster")
		enabled := featureGate.GetOrFail(`{.status.featureGates[*].enabled}`)
		logger.Infof("enabled featuregates: %s", enabled)
		o.Expect(enabled).Should(o.ContainSubstring("MachineConfigNodes"), "featureGate: MachineConfigNodes cannot be found")
	})

	g.It("[PolarionID:69187][OTP] Validate MachineConfigNodes properties [apigroup:machineconfiguration.openshift.io]", func() {

		nodes := NewNodeList(oc.AsAdmin()).GetAllLinuxWorkerNodesOrFail()

		for _, node := range nodes {
			mcn := NewMachineConfigNode(oc.AsAdmin(), node.GetName())

			exutil.By(fmt.Sprintf("Check MachineConfigNode properties for %s", node.GetName()))

			logger.Infof("Check spec.configVersion.desired")
			desiredOfNode := OrFail[string](node.GetDesiredMachineConfig())
			desiredOfMCNSpec := mcn.GetDesiredMachineConfigOfSpec()
			o.Expect(desiredOfNode).Should(o.Equal(desiredOfMCNSpec), "desired config of node is not same as machineconfignode.spec")

			logger.Infof("Check spec.pool")
			poolOfNode := node.GetPrimaryPoolOrFail().GetName()
			poolOfMCNSpec := OrFail[string](mcn.GetPoolName())
			o.Expect(poolOfNode).Should(o.Equal(poolOfMCNSpec), "pool of node is not same as machineconfignode.spec")

			logger.Infof("Check spec.node")
			nodeOfMCNSpec := mcn.GetNode()
			o.Expect(node.GetName()).Should(o.Equal(nodeOfMCNSpec), "node name is not same as machineconfignode.spec")

			logger.Infof("Check status.configVersion.current")
			currentOfNode := OrFail[string](node.GetCurrentMachineConfig())
			currentOfMCNStatus := mcn.GetCurrentMachineConfigOfStatus()
			o.Expect(currentOfNode).Should(o.Equal(currentOfMCNStatus), "current config of node is not same as machineconfignode.status")

			logger.Infof("Check status.configVersion.desired")
			desiredOfMCNStatus := mcn.GetDesiredMachineConfigOfStatus()
			o.Expect(desiredOfNode).Should(o.Equal(desiredOfMCNStatus), "desired config of node is not same as machineconfignode.status")

			logger.Infof("OK\n")
		}

	})

	g.It("[PolarionID:69197][OTP] Validate MachineConfigNode condition status transition [apigroup:machineconfiguration.openshift.io]", func() {

		var (
			mcName     = fmt.Sprintf("create-test-file-%s", GetCurrentTestPolarionIDNumber())
			fileConfig = getURLEncodedFileConfig("/etc/test-file", "hello", "420")
			workerMcp  = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		)

		// create machine config to apply a file change
		exutil.By("Create a test file on node")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		mc.SetMCOTemplate(GenericMCTemplate)
		mc.SetParams(fmt.Sprintf("FILES=[%s]", fileConfig))
		mc.skipWaitForMcp = true

		defer mc.DeleteWithWait()
		mc.create()

		exutil.By("Check conidition status of MachineConfigNode")
		// get 1st updating worker nodes
		workerNode := workerMcp.GetCordonedNodes()[0]
		// if test fail, need to waiting for mcp to complete, then rollback the change
		defer workerMcp.waitForComplete()

		mcn := NewMachineConfigNode(oc.AsAdmin(), workerNode.GetName())
		logger.Infof("Checking Updated False")
		o.Eventually(mcn.GetUpdated, "1m", "5s").Should(o.Equal("False"))
		logger.Infof("Checking UpdatePrepared")
		o.Eventually(mcn.GetUpdatePrepared, "1m", "3s").Should(o.Equal("True"))
		logger.Infof("Checking UpdateExecuted Unknown")
		o.Eventually(mcn.GetUpdateExecuted, "1m", "3s").Should(o.Equal("Unknown"))
		logger.Infof("Checking Cordoned")
		o.Eventually(mcn.GetCordoned, "30s", "3s").Should(o.Equal("True"))
		logger.Infof("Checking Drained Unknown")
		o.Eventually(mcn.GetDrained, "1m", "2s").Should(o.Equal("Unknown"))
		logger.Infof("Checking Drained")
		o.Eventually(mcn.GetDrained, "5m", "2s").Should(o.Equal("True"))
		// This transition is too fast and we can't check it without introducing instability. It is left commented so that we know that this transition exists.
		// logger.Infof("Checking AppliedFilesAndOS Unknown")
		// o.Eventually(mcn.GetAppliedFilesAndOS, "1m", "1s").Should(o.Equal("Unknown"))
		logger.Infof("Checking AppliedFilesAndOS")
		o.Eventually(mcn.GetAppliedFilesAndOS, "3m", "2s").Should(o.Equal("True"))
		logger.Infof("Checking UpdateExecuted")
		o.Eventually(mcn.GetUpdateExecuted, "20s", "5s").Should(o.Equal("True"))
		logger.Infof("Checking RebootedNode Unknown")
		o.Eventually(mcn.GetRebootedNode, "15s", "3s").Should(o.Equal("Unknown"))
		logger.Infof("Checking RebootedNode")
		o.Eventually(mcn.GetRebootedNode, "5m", "2s").Should(o.Equal("True"))
		logger.Infof("Checking Resumed")
		o.Eventually(mcn.GetResumed, "45s", "1s").Should(o.Equal("True"))
		logger.Infof("Checking UpdateComplete")
		o.Eventually(mcn.GetUpdateComplete, "40s", "1s").Should(o.Equal("True"))
		logger.Infof("Checking Uncordoned")
		o.Eventually(mcn.GetUncordoned, "40s", "1s").Should(o.Equal("True"))
		logger.Infof("Checking Updated")
		o.Eventually(mcn.GetUpdated, "1m", "1s").Should(o.Equal("True"))

	})

	g.It("[PolarionID:69205][OTP] MachineConfigNode corresponding condition status is Unknown when node is degraded [apigroup:machineconfiguration.openshift.io]", func() {

		var (
			wrongUserFileConfig              = `{"contents": {"source": "data:text/plain;charset=utf-8;base64,dGVzdA=="},"mode": 420,"path": "/etc/wronguser-test-file.test","user": {"name": "wronguser"}}`
			mcName                           = "mco-tc-69205-wrong-file-user"
			mcp                              = GetCompactCompatiblePool(oc.AsAdmin())
			expectedDegradedReasonAnnotation = `failed to retrieve file ownership for file "/etc/wronguser-test-file.test": failed to retrieve UserID for username: wronguser`
			expectedMCState                  = "Degraded"
			expectedMCNStatus                = "Unknown"
			degradedNode                     *Node
		)

		exutil.By("Create invalid MC")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName())
		mc.parameters = []string{fmt.Sprintf("FILES=[%s]", wrongUserFileConfig)}
		mc.skipWaitForMcp = true

		defer func() {
			logger.Infof("\nStart to recover degraded mcp")
			mc.Delete()
			mcp.RecoverFromDegraded()
		}()
		mc.create()

		exutil.By("Check mcp worker is degraded")
		o.Eventually(mcp.getDegradedMachineCount, "5m", "5s").Should(o.BeNumerically("==", 1), "Degraded machine count is not ==1")
		logger.Infof("OK\n")

		exutil.By("Get degraded node")
		allNodes := mcp.GetNodesOrFail()
		for _, node := range allNodes {
			if OrFail[string](node.GetMachineConfigState()) == expectedMCState {
				degradedNode = node
				break
			}
		}
		o.Expect(degradedNode).NotTo(o.BeNil(), "Degraded node not found")
		o.Expect(degradedNode.GetMachineConfigReason()).Should(o.ContainSubstring(expectedDegradedReasonAnnotation), "value of annotation machine config reason is not expected")
		logger.Infof("Found degraded node %s", degradedNode.GetName())
		logger.Infof("OK\n")

		exutil.By("Check corresponding MCN")
		mcn := NewMachineConfigNode(oc.AsAdmin(), degradedNode.GetName())
		o.Expect(mcn.Exists()).Should(o.BeTrue(), "Cannot find MCN %s", degradedNode.GetName())
		logger.Infof("OK\n")

		exutil.By("Check MCN conditions")
		o.Expect(mcn.GetAppliedFilesAndOS()).Should(o.Equal(expectedMCNStatus), "status of MCN condition UPDATEDFILESANDOS is not expected")
		o.Expect(mcn.GetUpdateExecuted()).Should(o.Equal(expectedMCNStatus), "status of MCN condition UPDATEEXECUTED is not expected")
		o.Expect(mcn).Should(HaveNodeDegradedMessage(o.And(
			o.ContainSubstring(degradedNode.GetName()),
			o.ContainSubstring(expectedDegradedReasonAnnotation))),
			"The MCN is not reporting the right reason for degradation. %s", mcn.PrettyString())
		logger.Infof("OK\n")

	})

	g.It("[PolarionID:81831][OTP] Verify MachineConfigNode shows degrade node status for PIS [apigroup:machineconfiguration.openshift.io]", func() {
		var (
			pinnedImageSetName                    = "tc-81831-bad-pinned-images"
			invalidPinnedImage                    = "quay.io/openshiftfake/fakeimage@sha256:0415f56ccc05526f2af5a7ae8654baec97d4a614f24736e8eef41a4591f08019"
			mcp                                   = GetCompactCompatiblePool(oc.AsAdmin())
			node                                  = mcp.GetSortedNodesOrFail()[0]
			mcn                                   = node.GetMachineConfigNode()
			expectedDegradedMessage               = `failed to execute podman manifest inspect for "` + invalidPinnedImage + `"`
			expectedPinnedImageSetDegradedReason  = "PrefetchFailed"
			expectedPinnedImageSetDegradedMessage = "One or more PinnedImageSet is experiencing an error. See PinnedImageSet list for more details."
		)

		exutil.By("Check that MCN exist")
		o.Expect(mcn).NotTo(o.BeNil(), "Machineconfignode does not exist for node %s", node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Pin invalid image")
		pis, err := CreateGenericPinnedImageSet(oc.AsAdmin(), pinnedImageSetName, mcp.GetName(), []string{invalidPinnedImage})
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating pinnedimageset %s", pis)
		defer pis.Delete()
		logger.Infof("OK!\n")

		exutil.By("Check degradation")
		o.Eventually(mcn, "10m", "10s").Should(HaveConditionField("PinnedImageSetsProgressing", "status", TrueString), "The MCN should report pinnedimagesets progressing")
		o.Eventually(mcn, "10m", "10s").Should(HaveConditionField("PinnedImageSetsDegraded", "status", TrueString), "The MCN should report pinnedimagesets degraded")
		o.Eventually(mcn, "10m", "10s").Should(HaveConditionField("PinnedImageSetsDegraded", "reason", expectedPinnedImageSetDegradedReason), "The MCN should report the right pinnedimagesets degraded reason")
		o.Eventually(mcn, "1m", "10s").Should(HaveConditionField("PinnedImageSetsDegraded", "message", expectedPinnedImageSetDegradedMessage), "The MCN should report the right pinnedimagesets degraded message")
		o.Eventually(mcn.Describe, "10m", "10s").Should(o.ContainSubstring(expectedDegradedMessage),
			"The MCN  describe command should report the right pinnedimagesets degraded message")
		logger.Infof("OK!\n")

		exutil.By("Delete the offending PIS")
		o.Expect(pis.Delete()).To(o.Succeed(), "Error deleting the offending PIS")
		logger.Infof("OK!\n")

		exutil.By("Check that the MCN is not degraded anymore")
		o.Eventually(mcn, "10m", "10s").Should(HaveConditionField("PinnedImageSetsProgressing", "status", FalseString), "The MCN should NOT report pinnedimagesets progressing")
		o.Eventually(mcn, "10m", "10s").Should(HaveConditionField("PinnedImageSetsDegraded", "status", FalseString), "The MCN should NOT report pinnedimagesets degraded")
		o.Eventually(mcn.Describe, "10m", "10s").ShouldNot(o.ContainSubstring(expectedDegradedMessage),
			"The MCN  describe command should NOT report a pinnedimagesets degraded message")
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:69755][OTP] MachineConfigNode resources should be synced when node is created/deleted [apigroup:machineconfiguration.openshift.io]", func() {

		var (
			provisioningMachine *Machine
			deletingMachine     *Machine
			mcp                 = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		)

		SkipTestIfWorkersCannotBeScaled(oc.AsAdmin())

		exutil.By("Get one machineset for testing")
		msl, err := NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAll()
		o.Expect(err).NotTo(o.HaveOccurred(), "Get machinesets failed")
		o.Expect(msl).ShouldNot(o.BeEmpty(), "Machineset list is empty")
		ms := msl[0]
		logger.Infof("Machineset %s will be used for testing", ms.GetName())
		logger.Infof("OK\n")

		replica, cerr := strconv.Atoi(ms.GetReplicaOfSpec())
		o.Expect(cerr).NotTo(o.HaveOccurred(), "Convert string to int error")

		defer func() {
			if serr := ms.ScaleTo(replica); serr != nil {
				logger.Errorf("Rollback replica for machineset %s failed: %v", ms.GetName(), serr)
			}
			mcp.waitForComplete()
		}()

		exutil.By("Scale up machineset to provision new node")
		replica++
		o.Expect(ms.ScaleTo(replica)).NotTo(o.HaveOccurred(), "Machineset %s scale up error", ms.GetName())

		exutil.By("Find new machine")
		provisioningMachine = ms.GetMachinesByPhaseOrFail(MachinePhaseProvisioning)[0]
		o.Expect(provisioningMachine).NotTo(o.BeNil(), "Cannot find provisioning machine")
		logger.Infof("New machine %s is provisioning", provisioningMachine.GetName())
		o.Eventually(ms.GetIsReady, "20m", "2m").Should(o.BeTrue(), "MachineSet %s is not ready", ms.GetName())
		logger.Infof("OK\n")

		exutil.By("Check new MCN")
		nodeToBeProvisioned := provisioningMachine.GetNodeOrFail()
		newMCN := NewMachineConfigNode(oc.AsAdmin(), nodeToBeProvisioned.GetName())
		o.Eventually(newMCN.Exists, "2m", "5s").Should(o.BeTrue(), "new MCN does not exist")
		o.Eventually(newMCN.GetDesiredMachineConfigOfSpec, "2m", "5s").Should(o.Equal(mcp.getConfigNameOfSpecOrFail()), "desired config of mcn.spec is not same as same property value in worker pool")
		o.Eventually(newMCN.GetDesiredMachineConfigOfStatus, "2m", "5s").Should(o.Equal(mcp.getConfigNameOfSpecOrFail()), "desired config of mcn.status is not same as same property value in worker pool")
		o.Eventually(newMCN.GetCurrentMachineConfigOfStatus, "2m", "5s").Should(o.Equal(mcp.getConfigNameOfStatusOrFail()), "current config of mcn.status is not same as same property value in worker pool")
		logger.Infof("OK\n")

		exutil.By("Scale down machineset to remove node")
		replica--
		o.Expect(ms.ScaleTo(replica)).NotTo(o.HaveOccurred(), "Machineset %s scale down error", ms.GetName())
		deletingMachine = ms.GetMachinesByPhaseOrFail(MachinePhaseDeleting)[0]
		o.Expect(deletingMachine).ShouldNot(o.BeNil(), "Cannot find deleting machine")
		logger.Infof("Machine %s is being deleted", deletingMachine.GetName())
		nodeToBeDeleted := deletingMachine.GetNodeOrFail()
		o.Eventually(ms.GetIsReady, "20m", "2m").Should(o.BeTrue(), "MachineSet %s is not ready", ms.GetName())
		logger.Infof("OK\n")

		exutil.By("Check Node and MCN are removed")
		o.Eventually(nodeToBeDeleted.Exists, "10m", "1m").Should(o.BeFalse(), "Node %s is not deleted successfully", nodeToBeDeleted.GetName())
		deletedMCN := NewMachineConfigNode(oc.AsAdmin(), nodeToBeDeleted.GetName())
		o.Eventually(deletedMCN.Exists, "5m", "10s").Should(o.BeFalse(), "MCN % is not deleted successfully", deletedMCN.GetName())
		logger.Infof("OK\n")

	})

	g.It("[PolarionID:74644][OTP] Scope each MCN object to only be accessible from its associated MCD [apigroup:machineconfiguration.openshift.io]", func() {
		SkipIfSNO(oc.AsAdmin())
		var (
			nodes              = OrFail[[]*Node](NewNodeList(oc.AsAdmin()).GetAllLinux())
			node0              = nodes[0]
			node1              = nodes[1]
			machineConfigNode0 = nodes[0].GetMachineConfigNode()
			machineConfigNode1 = nodes[1].GetMachineConfigNode()
			poolNode1          = machineConfigNode1.GetOrFail(`{.spec.pool.name}`)
			patchMCN1          = `{"spec":{"pool":{"name":"` + poolNode1 + `"}}}` // Actually we are not patching anything since we are setting the current value
		)

		exutil.By("Check that a machineconfignode cannot be patched from another MCD")
		cmd := []string{"./rootfs/usr/bin/oc", "patch", "machineconfignodes/" + machineConfigNode1.GetName(), "--type=merge", "-p", patchMCN1}
		_, err := exutil.RemoteShContainer(oc.AsAdmin(), MachineConfigNamespace, node0.GetMachineConfigDaemon(), MachineConfigDaemon, cmd...)
		o.Expect(err).To(o.HaveOccurred(), "It should not be allowed to patch a machineconfignode from a different machineconfigdaemon")
		o.Expect(err).To(o.BeAssignableToTypeOf(&exutil.ExitError{}), "Unexpected error while patching the machineconfignode resource from another MCD. %s", err)
		o.Expect(err.(*exutil.ExitError).StdErr).Should(o.ContainSubstring(`updates to MCN %s can only be done from the MCN's owner node`, machineConfigNode1.GetName()),
			"Unexpected error message when patching the machineconfignode from another MCD")
		logger.Infof("OK!\n")

		exutil.By("Check that a machineconfignode cannot be patched by MCD's SA")
		err = machineConfigNode0.Patch("merge", patchMCN1, "--as=system:serviceaccount:openshift-machine-config-operator:machine-config-daemon")
		o.Expect(err).To(o.HaveOccurred(), "MCD's SA should not be allowed to patch MachineConfigNode resources")
		o.Expect(err).To(o.BeAssignableToTypeOf(&exutil.ExitError{}), "Unexpected error while patching the machineconfignode resource")
		o.Expect(err.(*exutil.ExitError).StdErr).Should(o.ContainSubstring(`this user must have a "authentication.kubernetes.io/node-name" claim`),
			"Unexpected error message when patching the machineconfignode resource with the MCD's SA")
		logger.Infof("OK!\n")

		exutil.By("Check able to patch the MCN by same MCD running on node")
		_, err = exutil.RemoteShContainer(oc.AsAdmin(), MachineConfigNamespace, node1.GetMachineConfigDaemon(), MachineConfigDaemon, cmd...)
		o.Expect(err).NotTo(o.HaveOccurred(),
			"A MCD should be allowed to patch its own machineconfignode")
		logger.Infof("OK!\n")

		exutil.By("Check patch directly from oc using your admin SA")
		o.Expect(
			machineConfigNode0.Patch("merge", patchMCN1),
		).To(o.Succeed(), "Admin SA should be allowed to patch machineconfignodes")
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:80333][OTP] MachineConfigNodes. Support custom MCPs [apigroup:machineconfiguration.openshift.io]", func() {

		if IsCompactOrSNOCluster(oc.AsAdmin()) {
			g.Skip("The cluster is SNO/Compact. This test cannot be executed in SNO/Compact clusters")
		}

		var (
			infraMcpName  = "infra"
			customMcpName = "custom"
			workerMcp     = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			node          = workerMcp.GetNodesOrFail()[0]
			mcn           = node.GetMachineConfigNode()
		)

		exutil.By("Create infra MachineConfigPools")
		// We add no workers to the infra pool, it is not necessary
		_, err := CreateCustomMCP(oc.AsAdmin(), infraMcpName, 0)
		defer DeleteCustomMCP(oc.AsAdmin(), infraMcpName)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a new custom pool: %s", infraMcpName)
		logger.Infof("OK!\n")

		exutil.By("Create custom MachineConfigPools")
		// We add no workers to the custom pool, it is not necessary
		_, err = CreateCustomMCP(oc.AsAdmin(), customMcpName, 0)
		defer DeleteCustomMCP(oc.AsAdmin(), customMcpName)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a new custom pool: %s", customMcpName)
		logger.Infof("OK!\n")

		exutil.By("Check that the MCN resports that the worker nodes belong to the worker pool")
		o.Eventually(mcn.GetPoolName, "1m", "10s").Should(o.Equal(MachineConfigPoolWorker),
			"The MCN %s do not report the right %s pool for node %s", mcn.PrettyString(), MachineConfigPoolWorker, node.PrettyString())
		logger.Infof("OK!\n")

		exutil.By(`Move a node to the "infra" pool`)
		o.Expect(
			node.AddLabel(fmt.Sprintf("node-role.kubernetes.io/%s", infraMcpName), ""),
		).To(o.Succeed(),
			`Error moving the node to the "%s" pool`, infraMcpName)
		logger.Infof("OK!\n")

		exutil.By("Check that the MCN resports that the worker nodes belong to the infra pool")
		o.Eventually(mcn.GetPoolName, "1m", "10s").Should(o.Equal(infraMcpName),
			"The MCN %s do not report the right %s pool for node %s", mcn.PrettyString(), infraMcpName, node.PrettyString())
		logger.Infof("OK!\n")

		exutil.By(`Move a node to the "custom" pool`)
		o.Expect(
			MoveNodeToAnotherCustomPool(node, infraMcpName, customMcpName),
		).To(o.Succeed(),
			`Error moving the node to the "%s" pool`, customMcpName)
		logger.Infof("OK!\n")

		exutil.By("Check that the MCN resports that the worker nodes belong to the infra pool")
		o.Eventually(mcn.GetPoolName, "1m", "10s").Should(o.Equal(customMcpName),
			"The MCN %s do not report the right %s pool for node %s", mcn.PrettyString(), customMcpName, node.PrettyString())
		logger.Infof("OK!\n")

		exutil.By(`Move a node to the "worker" pool again`)
		o.Expect(
			node.RemoveLabel(fmt.Sprintf("node-role.kubernetes.io/%s", customMcpName)),
		).To(o.Succeed(),
			`Error moving the node to the "%s" pool`, MachineConfigPoolWorker)

		// We wait for complete to make sure that the node fully belongs to the worker pool,
		// and it contains no references to infra or custom MCs, since is would degrade the pool
		// This way of checking that the worker pool is ready is faster than node.waitForComplete(), that's the
		// reason we have used it, but if it results that it is unstable we can  use node.waitForComplete for the sake
		// of stability
		o.Eventually(workerMcp, "30s", "1s").Should(HaveConditionField("Updated", "status", FalseString),
			"The worker pool didn't report that a new node is joining")
		o.Eventually(workerMcp, "5m", "10s").Should(HaveConditionField("Updated", "status", TrueString),
			"The worker pool was not updated after adding the new node")
		logger.Infof("OK!\n")

		exutil.By("Check that the MCN resports that the worker nodes belong to the infra pool")
		o.Eventually(mcn.GetPoolName, "1m", "10s").Should(o.Equal(MachineConfigPoolWorker),
			"The MCN %s do not report the right %s pool for node %s", mcn.PrettyString(), MachineConfigPoolWorker, node.PrettyString())
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:85901][OTP] Apply MOSC to Existing Nodes and Verify MCN Image Status [apigroup:machineconfiguration.openshift.io]", func() {
		// This test is only supported in tech preview and ImageModeStatusReporting feature gate is enabled
		SkipIfNoFeatureGate(oc, "ImageModeStatusReporting")

		var (
			mcp      = GetCompactCompatiblePool(oc.AsAdmin())
			moscName = mcp.GetName() // MOSC resources have to use the same name as the MCP
			node     = mcp.GetSortedNodesOrFail()[0]
			mcn      = node.GetMachineConfigNode()
		)

		exutil.By("Configure OCB functionality for the MCP")
		mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, moscName, mcp.GetName(), nil)
		defer DisableOCL(mosc)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource")
		ValidateSuccessfulMOSC(mosc, nil)
		logger.Infof("OK!\n")

		exutil.By("Verify MCN image fields exist and are populated after OCL is applied")
		moscEnabledSpecDesiredImage, err := mcn.GetSpecDesiredImage()
		o.Expect(err).NotTo(o.HaveOccurred(), "MCN spec.configImage.desiredImage field does not exist")
		o.Expect(moscEnabledSpecDesiredImage).NotTo(o.BeEmpty(), "MCN spec.configImage.desiredImage should not be empty")

		moscEnabledStatusDesiredImage, err := mcn.GetStatusDesiredImage()
		o.Expect(err).NotTo(o.HaveOccurred(), "MCN status.configImage.desiredImage field does not exist")
		o.Expect(moscEnabledStatusDesiredImage).NotTo(o.BeEmpty(), "MCN status.configImage.desiredImage should not be empty")

		moscEnabledStatusCurrentImage, err := mcn.GetStatusCurrentImage()
		o.Expect(err).NotTo(o.HaveOccurred(), "MCN status.configImage.currentImage field does not exist")
		o.Expect(moscEnabledStatusCurrentImage).NotTo(o.BeEmpty(), "MCN status.configImage.currentImage should not be empty")

		o.Expect(moscEnabledSpecDesiredImage).To(o.And(o.Equal(moscEnabledStatusCurrentImage), o.Equal(moscEnabledStatusDesiredImage)),
			"MCN spec.configImage.desiredImage, status.configImage.desiredImage, and status.configImage.currentImage should be the same")
		logger.Infof("OK!\n")

		exutil.By("Verify rpm-ostree status shows correct OCL image on node")
		currentImagePullSpec := OrFail[string](mosc.GetStatusCurrentImagePullSpec())
		o.Expect(node.GetCurrentBootOSImage()).To(o.Equal(currentImagePullSpec),
			"Node is not using the expected OCL image")
		logger.Infof("OK!\n")

		exutil.By("Remove the MachineOSConfig resource and verify rollback")
		o.Expect(DisableOCL(mosc)).To(o.Succeed(), "Error cleaning up %s", mosc)
		ValidateMOSCIsGarbageCollected(mosc, mcp)
		logger.Infof("OK!\n")

		exutil.By("Verify MCN image fields are removed after MOSC deletion")
		moscDisabledSpecDesiredImage, err := mcn.GetSpecDesiredImage()
		o.Expect(err).NotTo(o.HaveOccurred(), "MCN spec.configImage.desiredImage field does not exist")
		o.Expect(moscDisabledSpecDesiredImage).To(o.BeEmpty(), "MCN spec.configImage.desiredImage should be empty after MOSC deletion")

		moscDisabledStatusDesiredImage, err := mcn.GetStatusDesiredImage()
		o.Expect(err).NotTo(o.HaveOccurred(), "MCN status.configImage.desiredImage field does not exist")
		o.Expect(moscDisabledStatusDesiredImage).To(o.BeEmpty(), "MCN status.configImage.desiredImage should be empty after MOSC deletion")

		moscDisabledStatusCurrentImage, err := mcn.GetStatusCurrentImage()
		o.Expect(err).NotTo(o.HaveOccurred(), "MCN status.configImage.currentImage field does not exist")
		o.Expect(moscDisabledStatusCurrentImage).To(o.BeEmpty(), "MCN status.configImage.currentImage should be empty after MOSC deletion")
	})

})
