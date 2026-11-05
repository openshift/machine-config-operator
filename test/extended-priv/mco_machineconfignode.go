package extended

import (
	"context"
	"fmt"
	"strconv"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	machineconfigclient "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"k8s.io/apimachinery/pkg/util/wait"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO MachineConfigNode", func() {

	defer g.GinkgoRecover()

	var (
		oc = exutil.NewCLI("mco-machineconfignode", exutil.KubeConfigPath())
	)

	g.JustBeforeEach(func() {
		PreChecks(oc)
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
		// Check APPLIEDFILESANDOS condition for non-TechPreviewNoUpgrade cluster
		// Check APPLIEDFILES condition for TechPreviewNoUpgrade cluster
		// Use dynamic check based on which condition exists in the MCN resource
		// This transition is too fast and we can't check it without introducing instability. It is left commented so that we know that this transition exists.
		//	logger.Infof("Checking Applied condition Unknown")
		//	o.Eventually(mcn.GetAppliedConditionStatus, "1m", "1s").Should(o.Equal("Unknown"))
		logger.Infof("Checking Applied condition")
		o.Eventually(mcn.GetAppliedConditionStatus, "3m", "2s").Should(o.Equal("True"))
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
		// Check APPLIEDFILESANDOS condition for non-TechPreviewNoUpgrade cluster
		// Check APPLIEDFILES condition for TechPreviewNoUpgrade cluster
		// Use dynamic check based on which condition exists in the MCN resource
		o.Expect(mcn.GetAppliedConditionStatus()).Should(o.Equal(expectedMCNStatus), "status of MCN Applied condition is not expected")
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
			provisioningMachine ManagedMachine
			deletingMachine     ManagedMachine
			mcp                 = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			initialNodeCount    = len(mcp.GetNodesOrFail())
		)

		SkipTestIfWorkersCannotBeScaled(oc.AsAdmin())

		exutil.By("Get one machineset for testing")
		ms := GetValidManagedMachineResource(oc.AsAdmin())
		logger.Infof("Machineset %s will be used for testing", ms.GetName())
		logger.Infof("OK\n")

		replica, cerr := strconv.Atoi(OrFail[string](ms.GetReplicaOfSpec()))
		o.Expect(cerr).NotTo(o.HaveOccurred(), "Convert string to int error")
		originalReplica := replica

		defer SafeCleanup(func() {
			o.Expect(ms.ScaleTo(originalReplica)).NotTo(o.HaveOccurred(),
				"Rollback replica for machineset %s failed", ms.GetName())
			o.Expect(mcp.WaitForPoolToBeSettled(initialNodeCount)).To(o.Succeed(),
				"MCP counts did not settle after scale-down.\n%s", mcp.PrettyString())
		})

		exutil.By("Scale up machineset to provision new node")
		replica++
		o.Expect(ms.ScaleTo(replica)).NotTo(o.HaveOccurred(), "Machineset %s scale up error", ms.GetName())

		exutil.By("Find new machine")
		provisioningMachine = OrFail[[]ManagedMachine](ms.GetMachinesByPhase(MachinePhaseProvisioning))[0]
		o.Expect(provisioningMachine).NotTo(o.BeNil(), "Cannot find provisioning machine")
		logger.Infof("New machine %s is provisioning", provisioningMachine.GetName())
		o.Eventually(ms.GetIsReady, "20m", "2m").Should(o.BeTrue(), "MachineSet %s is not ready", ms.GetName())
		logger.Infof("OK\n")

		exutil.By("Check new MCN")
		nodeToBeProvisioned := OrFail[*Node](provisioningMachine.GetNode())
		newMCN := NewMachineConfigNode(oc.AsAdmin(), nodeToBeProvisioned.GetName())
		o.Eventually(newMCN.Exists, "2m", "5s").Should(o.BeTrue(), "new MCN does not exist")
		o.Eventually(newMCN.GetDesiredMachineConfigOfSpec, "2m", "5s").Should(o.Equal(mcp.getConfigNameOfSpecOrFail()), "desired config of mcn.spec is not same as same property value in worker pool")
		o.Eventually(newMCN.GetDesiredMachineConfigOfStatus, "2m", "5s").Should(o.Equal(mcp.getConfigNameOfSpecOrFail()), "desired config of mcn.status is not same as same property value in worker pool")
		o.Eventually(newMCN.GetCurrentMachineConfigOfStatus, "2m", "5s").Should(o.Equal(mcp.getConfigNameOfStatusOrFail()), "current config of mcn.status is not same as same property value in worker pool")
		logger.Infof("OK\n")

		exutil.By("Scale down machineset to remove node")
		replica--
		o.Expect(ms.ScaleTo(replica)).NotTo(o.HaveOccurred(), "Machineset %s scale down error", ms.GetName())
		deletingMachine = OrFail[[]ManagedMachine](ms.GetMachinesByPhase(MachinePhaseDeleting))[0]
		o.Expect(deletingMachine).ShouldNot(o.BeNil(), "Cannot find deleting machine")
		logger.Infof("Machine %s is being deleted", deletingMachine.GetName())
		nodeToBeDeleted := OrFail[*Node](deletingMachine.GetNode())
		o.Eventually(ms.GetIsReady, "20m", "2m").Should(o.BeTrue(), "MachineSet %s is not ready", ms.GetName())
		logger.Infof("OK\n")

		exutil.By("Check Node and MCN are removed")
		o.Eventually(nodeToBeDeleted.Exists, "10m", "1m").Should(o.BeFalse(), "Node %s is not deleted successfully", nodeToBeDeleted.GetName())
		deletedMCN := NewMachineConfigNode(oc.AsAdmin(), nodeToBeDeleted.GetName())
		o.Eventually(deletedMCN.Exists, "5m", "10s").Should(o.BeFalse(), "MCN % is not deleted successfully", deletedMCN.GetName())
		logger.Infof("OK\n")

	})

	g.It("[PolarionID:74644][OTP] Scope each MCN object to only be accessible from its associated MCD [apigroup:machineconfiguration.openshift.io]", func() {
		exutil.SkipOnSingleNodeTopology(oc.AsAdmin())
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
		cmd := []string{"chroot", "/rootfs", "oc", "patch", "machineconfignodes/" + machineConfigNode1.GetName(), "--type=merge", "-p", patchMCN1}
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

	g.It("Manual cordon and uncordon should be reported without making an updated pool updating [apigroup:machineconfiguration.openshift.io]", func() {
		mcp := GetCompactCompatiblePool(oc.AsAdmin())

		exutil.By("Wait until the pool is updated and not updating")
		if !waitForPoolToBeStable(mcp, 5*time.Minute) {
			g.Skip(fmt.Sprintf("Pool %s did not reach Updated=True and Updating=False in time, skipping manual action test", mcp.GetName()))
		}

		exutil.By("Get a ready and schedulable node of the pool whose MCN is updated")
		nodeToTest, mcn := readyUpdatedPoolNode(mcp, time.Minute)
		if nodeToTest == nil {
			g.Skip("No ready and schedulable node in the pool reached MCN Updated=True in time, skipping manual action test")
		}
		machineConfigClient, err := machineconfigclient.NewForConfig(oc.KubeFramework().ClientConfig())
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating machine config client")
		logger.Infof("Testing with node %s\n", nodeToTest.GetName())

		exutil.By("Record the pool machine counts before the manual action")
		readyMachineCountBefore, readyErr := strconv.Atoi(OrFail[string](mcp.GetReadyMachineCountStatus()))
		o.Expect(readyErr).NotTo(o.HaveOccurred(), "Error getting the readyMachineCount of pool %s", mcp.GetName())
		updatedMachineCountBefore := OrFail[int](mcp.GetUpdatedMachineCountStatusAsInt())

		// poolStateUnchanged checks that the pool stays updated and that only the ready machine
		// count moved to the expected value after the manual node action
		poolStateUnchanged := func(expectedReadyMachineCount int) bool {
			updatedStatus, err := mcp.GetUpdatedStatus()
			if err != nil {
				return false
			}
			updatingStatus, err := mcp.GetUpdatingStatus()
			if err != nil {
				return false
			}
			readyMachineCount, convErr := mcp.GetReadyMachineCountStatusAsInt()
			if convErr != nil {
				return false
			}
			updatedMachineCount, err := mcp.GetUpdatedMachineCountStatusAsInt()
			if err != nil {
				return false
			}
			return updatedStatus == TrueString && updatingStatus == FalseString &&
				readyMachineCount == expectedReadyMachineCount &&
				updatedMachineCount == updatedMachineCountBefore
		}

		// Recover the initial node state if any assertion fails
		defer func() {
			if err := nodeToTest.Uncordon(); err != nil {
				logger.Errorf("Failed to uncordon node %s during cleanup: %v", nodeToTest.GetName(), err)
			}
		}()

		exutil.By("Manually cordon the node")
		o.Expect(nodeToTest.Cordon()).To(o.Succeed(), "Error cordoning node %s", nodeToTest.PrettyString())

		exutil.By("Check that the MCN reports the manual cordon while keeping Updated=True")
		o.Eventually(mcn, "2m", "2s").Should(o.And(
			HaveConditionField("Updated", "status", TrueString),
			HaveConditionField("Cordoned", "status", TrueString),
		), "The MCN %s should report Cordoned=True while keeping Updated=True", mcn.PrettyString())
		o.Expect(ConfirmUpdatedMCNStatus(machineConfigClient, nodeToTest.GetName())).To(o.BeTrue(), "A manual cordon must not make the MCN appear to be updating")

		exutil.By("Check that the manually cordoned node does not make the updated pool updating")
		o.Eventually(func() bool { return poolStateUnchanged(readyMachineCountBefore - 1) }, "2m", "5s").Should(o.BeTrue(),
			"Cordoning node %s should lower the ready machine count of pool %s by one without making the updated pool updating",
			nodeToTest.GetName(), mcp.GetName())

		exutil.By("Manually uncordon the node")
		o.Expect(nodeToTest.Uncordon()).To(o.Succeed(), "Error uncordoning node %s", nodeToTest.PrettyString())

		exutil.By("Check that the MCN reports the manual uncordon without clearing Updated")
		o.Eventually(mcn, "2m", "2s").Should(o.And(
			HaveConditionField("Updated", "status", TrueString),
			HaveConditionField("Cordoned", "status", FalseString),
			HaveConditionField("Uncordoned", "status", TrueString),
		), "The MCN %s should report the uncordon transition without clearing Updated", mcn.PrettyString())
		o.Expect(ConfirmUpdatedMCNStatus(machineConfigClient, nodeToTest.GetName())).To(o.BeTrue(), "A manual uncordon must not make the MCN appear to be updating")

		exutil.By("Check that the uncordoned node restores the pool ready machine count")
		o.Eventually(func() bool { return poolStateUnchanged(readyMachineCountBefore) }, "2m", "5s").Should(o.BeTrue(),
			"Uncordoning node %s should restore the ready machine count of pool %s", nodeToTest.GetName(), mcp.GetName())
	})

	g.It("Manual drain should be reported on the MachineConfigNode [apigroup:machineconfiguration.openshift.io]", func() {
		if IsCompactOrSNOCluster(oc.AsAdmin()) {
			g.Skip("Manual drain requires a dedicated worker node")
		}

		mcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		nodeToTest := readySchedulablePoolNode(mcp)
		if nodeToTest == nil {
			g.Skip("Manual drain requires a ready and schedulable worker node")
		}
		mcn := nodeToTest.GetMachineConfigNode()

		exutil.By("Check that the worker pool and MCN are updated")
		o.Eventually(mcp, "5m", "5s").Should(o.And(
			HaveConditionField("Updated", "status", TrueString),
			HaveConditionField("Updating", "status", FalseString),
		), "The pool %s should be updated before the manual drain", mcp)
		o.Eventually(mcn, "1m", "5s").Should(HaveConditionField("Updated", "status", TrueString),
			"The MCN %s should be updated before the manual drain", mcn)

		exutil.By("Create a workload on the selected worker")
		oc.SetupProject()
		deploymentName := "mco-mcn-manual-drain"
		deployment := NewNamespacedResource(oc.AsAdmin(), "deployment", oc.Namespace(), deploymentName)
		defer func() {
			o.Expect(deployment.Delete("--ignore-not-found=true")).To(o.Succeed(), "Error deleting %s", deployment)
		}()
		o.Expect(NewMCOTemplate(oc.AsAdmin(), "create-deployment.yaml").Create(
			"-p", "NAME="+deploymentName, "-p", "NAMESPACE="+oc.Namespace(), "-p", "REPLICAS=0",
		)).To(o.Succeed(), "Error creating %s", deployment)
		hostname, err := nodeToTest.GetNodeHostname()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting hostname for %s", nodeToTest)
		o.Expect(deployment.Patch("merge", fmt.Sprintf(`{"spec":{"replicas":1,"template":{"spec":{"nodeSelector":{"kubernetes.io/hostname":%q}}}}}`, hostname))).To(o.Succeed(),
			"Error scheduling %s on %s", deployment, nodeToTest)
		o.Eventually(deployment, "6m", "15s").Should(BeAvailable(), "The workload %s should be available", deployment)
		o.Eventually(func() (string, error) {
			return exutil.GetPodName(oc.AsAdmin(), oc.Namespace(), "app=test", nodeToTest.GetName())
		}, "2m", "5s").ShouldNot(o.BeEmpty(), "The workload must run on %s", nodeToTest)
		podName, err := exutil.GetPodName(oc.AsAdmin(), oc.Namespace(), "app=test", nodeToTest.GetName())
		o.Expect(err).NotTo(o.HaveOccurred(), "Error finding the workload pod on %s", nodeToTest)
		o.Expect(podName).NotTo(o.BeEmpty(), "The workload must run on %s", nodeToTest)
		pod := NewNamespacedResource(oc.AsAdmin(), "pod", oc.Namespace(), podName)

		exutil.By("Cordon the worker and check that its workload blocks Drained")
		defer func() {
			o.Expect(nodeToTest.Uncordon()).To(o.Succeed(), "Error uncordoning %s during cleanup", nodeToTest)
		}()
		o.Expect(nodeToTest.Cordon()).To(o.Succeed(), "Error cordoning %s", nodeToTest)
		o.Eventually(mcn, "2m", "5s").Should(o.And(
			HaveConditionField("Updated", "status", TrueString),
			HaveConditionField("Cordoned", "status", TrueString),
			HaveConditionField("Drained", "status", FalseString),
			HaveConditionField("Drained", "reason", "DrainablePodsRemain"),
		), "The MCN %s should report a cordoned node with drainable workloads", mcn)

		exutil.By("Drain the worker")
		// Like the MCO drain helper, this permits evicting pods with emptyDir
		// volumes. Their node-local data, including other workloads', is lost.
		o.Expect(oc.AsAdmin().Run("adm").Args("drain", nodeToTest.GetName(), "--ignore-daemonsets", "--delete-emptydir-data", "--timeout=5m").Execute()).To(o.Succeed(),
			"Error draining %s", nodeToTest)
		o.Eventually(pod.Get, "2m", "5s").WithArguments(`{.metadata.uid}`, "--ignore-not-found=true").Should(o.BeEmpty(),
			"The workload pod %s should have been evicted", pod)

		exutil.By("Check that the MCN reports Drained without clearing Updated")
		o.Eventually(mcn, "3m", "5s").Should(o.And(
			HaveConditionField("Updated", "status", TrueString),
			HaveConditionField("Cordoned", "status", TrueString),
			HaveConditionField("Drained", "status", TrueString),
			HaveConditionField("Drained", "reason", "NoDrainablePods"),
		), "The MCN %s should report the completed manual drain", mcn)

		exutil.By("Uncordon the worker and check that Drained is cleared")
		o.Expect(nodeToTest.Uncordon()).To(o.Succeed(), "Error uncordoning %s", nodeToTest)
		o.Eventually(mcn, "2m", "5s").Should(o.And(
			HaveConditionField("Updated", "status", TrueString),
			HaveConditionField("Cordoned", "status", FalseString),
			HaveConditionField("Drained", "status", FalseString),
			HaveConditionField("Uncordoned", "status", TrueString),
		), "The MCN %s should report the uncordon after the drain", mcn)
	})

	g.It("Manual reboot should be reported on the MachineConfigNode [apigroup:machineconfiguration.openshift.io]", func() {
		if IsCompactOrSNOCluster(oc.AsAdmin()) {
			g.Skip("Manual reboot requires a dedicated worker node")
		}

		mcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)

		exutil.By("Wait until the pool is updated and not updating")
		if !waitForPoolToBeStable(mcp, 5*time.Minute) {
			g.Skip(fmt.Sprintf("Pool %s did not reach Updated=True and Updating=False in time, skipping manual reboot test", mcp.GetName()))
		}

		exutil.By("Get a ready and schedulable node of the pool whose MCN is updated")
		nodeToTest, mcn := readyUpdatedPoolNode(mcp, time.Minute)
		if nodeToTest == nil {
			g.Skip("No ready and schedulable worker node reached MCN Updated=True in time, skipping manual reboot test")
		}

		exutil.By("Record the boot ID and verify that the controller has observed it")
		oldBootID, err := nodeToTest.Get(`{.status.nodeInfo.bootID}`)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the boot ID of %s", nodeToTest)
		o.Expect(oldBootID).NotTo(o.BeEmpty(), "The boot ID of %s should not be empty", nodeToTest)
		o.Eventually(nodeToTest.GetAnnotation, "2m", "5s").WithArguments(constants.MachineConfigDaemonLastObservedBootIDAnnotationKey).Should(o.Equal(oldBootID),
			"The controller should observe the current boot ID of %s before the reboot", nodeToTest)

		exutil.By("Reboot the worker")
		rebootStarted := false
		defer func() {
			if rebootStarted {
				o.Eventually(nodeToTest.Get, "15m", "20s").WithArguments(`{.status.nodeInfo.bootID}`).Should(o.And(o.Not(o.BeEmpty()), o.Not(o.Equal(oldBootID))),
					"The boot ID of %s did not change", nodeToTest)
			}
			o.Eventually(nodeToTest, "15m", "20s").Should(HaveConditionField("Ready", "status", TrueString),
				"The rebooted node %s did not become ready", nodeToTest)
		}()
		err = nodeToTest.Reboot()
		rebootStarted = err == nil
		o.Expect(err).NotTo(o.HaveOccurred(), "Error rebooting %s", nodeToTest)

		exutil.By("Verify that the worker rebooted and is ready")
		o.Eventually(nodeToTest.Get, "15m", "20s").WithArguments(`{.status.nodeInfo.bootID}`).Should(o.And(o.Not(o.BeEmpty()), o.Not(o.Equal(oldBootID))),
			"The boot ID of %s did not change", nodeToTest)
		newBootID, err := nodeToTest.Get(`{.status.nodeInfo.bootID}`)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the new boot ID of %s", nodeToTest)
		o.Expect(newBootID).To(o.And(o.Not(o.BeEmpty()), o.Not(o.Equal(oldBootID))), "The boot ID of %s did not change", nodeToTest)
		o.Eventually(nodeToTest, "15m", "20s").Should(HaveConditionField("Ready", "status", TrueString),
			"The rebooted node %s did not become ready", nodeToTest)

		exutil.By("Check that the MCN reports the new reboot without clearing Updated")
		o.Eventually(mcn, "5m", "5s").Should(o.And(
			HaveConditionField("Updated", "status", TrueString),
			HaveConditionField("RebootedNode", "status", TrueString),
			HaveConditionField("RebootedNode", "reason", "BootIDChanged"),
			HaveConditionField("RebootedNode", "message", o.ContainSubstring(newBootID)),
		), "The MCN %s should report the observed reboot", mcn)
		exutil.By("Verify the reboot condition remains true after the node is ready")
		o.Consistently(mcn, "45s", "5s").Should(o.And(
			HaveConditionField("Updated", "status", TrueString),
			HaveConditionField("RebootedNode", "status", TrueString),
			HaveConditionField("RebootedNode", "reason", "BootIDChanged"),
			HaveConditionField("RebootedNode", "message", o.ContainSubstring(newBootID)),
		), "The MCN %s should keep reporting the observed reboot", mcn)
		o.Eventually(nodeToTest.GetAnnotation, "2m", "5s").WithArguments(constants.MachineConfigDaemonLastObservedBootIDAnnotationKey).Should(o.Equal(newBootID),
			"The controller should record the new boot ID of %s", nodeToTest)

		exutil.By("Check that the worker pool remains updated")
		o.Eventually(mcp, "5m", "5s").Should(o.And(
			HaveConditionField("Updated", "status", TrueString),
			HaveConditionField("Updating", "status", FalseString),
		), "The pool %s should be updated after the manual reboot", mcp)
	})

})

// readySchedulablePoolNode selects a ready node whose primary pool is mcp.
func readySchedulablePoolNode(mcp *MachineConfigPool) *Node {
	for _, node := range FilterSchedulableNodesOrFail(mcp.GetSortedNodesOrFail()) {
		if node.IsReady() {
			return node
		}
	}
	return nil
}

// waitForPoolToBeStable polls, up to timeout, until mcp is Updated=True and Updating=False.
// It returns false instead of failing the test if the pool never becomes stable in time.
func waitForPoolToBeStable(mcp *MachineConfigPool, timeout time.Duration) bool {
	matcher := o.And(
		HaveConditionField("Updated", "status", TrueString),
		HaveConditionField("Updating", "status", FalseString),
	)
	stable := false
	_ = wait.PollUntilContextTimeout(context.TODO(), 5*time.Second, timeout, true, func(_ context.Context) (bool, error) {
		ok, err := matcher.Match(mcp)
		if err != nil {
			return false, nil
		}
		stable = ok
		return ok, nil
	})
	return stable
}

// readyUpdatedPoolNode returns the first ready and schedulable node of mcp whose MachineConfigNode
// reaches Updated=True within timeout, trying each candidate node in turn. It returns nil, nil
// instead of failing the test if no node becomes updated in time.
func readyUpdatedPoolNode(mcp *MachineConfigPool, timeout time.Duration) (*Node, *MachineConfigNode) {
	matcher := HaveConditionField("Updated", "status", TrueString)
	for _, candidate := range FilterSchedulableNodesOrFail(mcp.GetSortedNodesOrFail()) {
		if !candidate.IsReady() {
			continue
		}
		candidateMcn := candidate.GetMachineConfigNode()
		updated := false
		_ = wait.PollUntilContextTimeout(context.TODO(), 5*time.Second, timeout, true, func(_ context.Context) (bool, error) {
			ok, err := matcher.Match(candidateMcn)
			if err != nil {
				return false, nil
			}
			updated = ok
			return ok, nil
		})
		if updated {
			return candidate, candidateMcn
		}
		logger.Infof("Node %s MCN did not reach Updated=True in time, trying another node\n", candidate.GetName())
	}
	return nil, nil
}
