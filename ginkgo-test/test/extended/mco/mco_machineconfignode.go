package mco

import (
	"fmt"
	"strconv"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util"
	logger "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/logext"
)

var _ = g.Describe("[sig-mco] MCO MachineConfigNode", func() {

	defer g.GinkgoRecover()

	var (
		oc = exutil.NewCLI("mco-machineconfignode", exutil.KubeConfigPath())
	)

	g.JustBeforeEach(func() {
		preChecks(oc)
		// featureGate MachineConfigNode in included in featureSet: TechPreviewNoUpgrade
		// skip the test if featureSet is not there
		if !exutil.IsTechPreviewNoUpgrade(oc) {
			g.Skip("featureSet: TechPreviewNoUpgrade is required for this test")
		}
	})

	g.It("Author:rioliu-NonPreRelease-Critical-69184-Enable feature gate MachineConfigNodes [Serial]", func() {
		// need to check whether featureGate MachineConfigNodes is in enabled list
		exutil.By("Check whether featureGate: MachineConfigNodes is enabled")
		featureGate := NewResource(oc.AsAdmin(), "featuregate", "cluster")
		enabled := featureGate.GetOrFail(`{.status.featureGates[*].enabled}`)
		logger.Infof("enabled featuregates: %s", enabled)
		o.Expect(enabled).Should(o.ContainSubstring("MachineConfigNodes"), "featureGate: MachineConfigNodes cannot be found")
	})

	g.It("Author:rioliu-NonPreRelease-High-69187-validate MachineConfigNodes properties [Serial]", func() {

		nodes := NewNodeList(oc.AsAdmin()).GetAllLinuxWorkerNodesOrFail()

		for _, node := range nodes {
			mcn := NewMachineConfigNode(oc.AsAdmin(), node.GetName())

			exutil.By(fmt.Sprintf("Check MachineConfigNode properties for %s", node.GetName()))

			logger.Infof("Check spec.configVersion.desired")
			desiredOfNode := node.GetDesiredMachineConfig()
			desiredOfMCNSpec := mcn.GetDesiredMachineConfigOfSpec()
			o.Expect(desiredOfNode).Should(o.Equal(desiredOfMCNSpec), "desired config of node is not same as machineconfignode.spec")

			logger.Infof("Check spec.pool")
			poolOfNode := node.GetPrimaryPoolOrFail().GetName()
			poolOfMCNSpec := mcn.GetPool()
			o.Expect(poolOfNode).Should(o.Equal(poolOfMCNSpec), "pool of node is not same as machineconfignode.spec")

			logger.Infof("Check spec.node")
			nodeOfMCNSpec := mcn.GetNode()
			o.Expect(node.GetName()).Should(o.Equal(nodeOfMCNSpec), "node name is not same as machineconfignode.spec")

			logger.Infof("Check status.configVersion.current")
			currentOfNode := node.GetCurrentMachineConfig()
			currentOfMCNStatus := mcn.GetCurrentMachineConfigOfStatus()
			o.Expect(currentOfNode).Should(o.Equal(currentOfMCNStatus), "current config of node is not same as machineconfignode.status")

			logger.Infof("Check status.configVersion.desired")
			desiredOfMCNStatus := mcn.GetDesiredMachineConfigOfStatus()
			o.Expect(desiredOfNode).Should(o.Equal(desiredOfMCNStatus), "desired config of node is not same as machineconfignode.status")

			logger.Infof("OK\n")
		}

	})

	g.It("Author:rioliu-NonPreRelease-Longduration-High-69197-validate MachineConfigNode condition status transition [Disruptive]", func() {

		var (
			mcName     = "create-test-file"
			fileConfig = getURLEncodedFileConfig("/etc/test-file", "hello", "420")
			workerMcp  = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		)

		// create machine config to apply a file change
		exutil.By("Create a test file on node")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		mc.SetMCOTemplate(GenericMCTemplate)
		mc.SetParams(fmt.Sprintf("FILES=[%s]", fileConfig))
		mc.skipWaitForMcp = true

		defer mc.delete()
		mc.create()

		exutil.By("Check conidition status of MachineConfigNode")
		// get 1st updating worker nodes
		workerNode := workerMcp.GetCordonedNodes()[0]
		// if test fail, need to waiting for mcp to complete, then rollback the change
		defer workerMcp.waitForComplete()

		mcn := NewMachineConfigNode(oc.AsAdmin(), workerNode.GetName())
		o.Eventually(mcn.GetUpdated, "1m", "5s").Should(o.Equal("False"))
		o.Eventually(mcn.GetUpdatePrepared, "1m", "3s").Should(o.Equal("True"))
		o.Eventually(mcn.GetUpdateCompatible, "3m", "3s").Should(o.Equal("True"))
		o.Eventually(mcn.GetUpdateExecuted, "1m", "3s").Should(o.Equal("Unknown"))
		o.Eventually(mcn.GetCordoned, "30s", "3s").Should(o.Equal("True"))
		o.Eventually(mcn.GetDrained, "30s", "2s").Should(o.Equal("Unknown"))
		o.Eventually(mcn.GetDrained, "5m", "2s").Should(o.Equal("True"))
		o.Eventually(mcn.GetAppliedFilesAndOS, "1m", "1s").Should(o.Equal("Unknown"))
		o.Eventually(mcn.GetAppliedFilesAndOS, "3m", "2s").Should(o.Equal("True"))
		o.Eventually(mcn.GetUpdateExecuted, "20s", "5s").Should(o.Equal("True"))
		o.Eventually(mcn.GetUpdatePostActionComplete, "30m", "5s").Should(o.Equal("Unknown"))
		o.Eventually(mcn.GetRebootedNode, "15s", "3s").Should(o.Equal("Unknown"))
		o.Eventually(mcn.GetRebootedNode, "5m", "5s").Should(o.Equal("True"))
		o.Eventually(mcn.GetResumed, "15s", "5s").Should(o.Equal("True"))
		o.Eventually(mcn.GetUpdateComplete, "10s", "5s").Should(o.Equal("True"))
		o.Eventually(mcn.GetUncordoned, "10s", "2s").Should(o.Equal("True"))
		o.Eventually(mcn.GetUpdated, "1m", "5s").Should(o.Equal("True"))

	})

	// After this PR https://github.com/openshift/machine-config-operator/pull/4158 MCs are checked before applying it to the nodes, and if they cannot be rendered
	// then the MCP is drectly degraded without modifying any node. Hence, we cannot check  "UpdatePrepared/UpdateCompatible" condition using this test case.
	// We deprecate the test case until finding a way to test this condition properly.
	g.It("Author:rioliu-DEPRECATED-NonPreRelease-Medium-69216-MachineConfigNode UpdateCompatible is Unknown when MC contains kernel argument in ignition section [Disruptive]", func() {

		var (
			mcName            = "mco-tc-66376-reject-ignition-kernel-arguments"
			mcTemplate        = "add-ignition-kernel-arguments.yaml"
			mcp               = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			expectedMessage   = "ignition kargs section contains changes"
			expectedMCNStatus = "Unknown"
		)

		mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName()).SetMCOTemplate(mcTemplate)
		mc.skipWaitForMcp = true

		exutil.By("Create invalid MC")
		defer func() {
			mc.deleteNoWait()
			o.Expect(mcp.WaitForNotDegradedStatus()).NotTo(o.HaveOccurred(), "mcp worker is not recovered from degraded sate")
		}()
		mc.create()

		exutil.By("Check condition NodeDegraded of worker pool")
		o.Eventually(mcp, "2m", "5s").Should(HaveConditionField("NodeDegraded", "status", o.Equal("True")))
		o.Expect(mcp).Should(HaveNodeDegradedMessage(o.MatchRegexp(expectedMessage)))
		logger.Infof("OK\n")

		exutil.By("Get Unreconcilable node")
		allUnreconcilableNodes := mcp.GetUnreconcilableNodesOrFail()
		o.Expect(allUnreconcilableNodes).NotTo(o.BeEmpty(), "Can not find any unreconcilable node from mcp %s", mcp.GetName())
		unreconcilableNode := allUnreconcilableNodes[0]
		logger.Infof("Unreconcilable node is %s", unreconcilableNode.GetName())
		logger.Infof("OK\n")

		exutil.By("Check machineconfignode conditions [UpdatePrepared/UpdateCompatible]")
		mcn := NewMachineConfigNode(oc.AsAdmin(), unreconcilableNode.GetName())
		o.Expect(mcn.GetUpdatePrepared()).Should(o.Equal(expectedMCNStatus))
		o.Expect(mcn.GetUpdateCompatible()).Should(o.Equal(expectedMCNStatus))
		o.Expect(mcn).Should(HaveConditionField("UpdateCompatible", "message", o.MatchRegexp(expectedMessage)))
		logger.Infof("OK\n")

	})

	g.It("Author:rioliu-NonPreRelease-Medium-69205-MachineConfigNode corresponding condition status is Unknown when node is degraded [Disruptive]", func() {

		var (
			mcName            = "change-workers-extension-usbguard"
			mcp               = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			expectedMCState   = "Degraded"
			expectedMCNStatus = "Unknown"
			degradedNode      Node
		)

		exutil.By("Create invalid MC")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		mc.SetMCOTemplate(GenericMCTemplate)
		mc.SetParams(`EXTENSIONS=["usbguard","zsh"]`)
		mc.skipWaitForMcp = true

		defer func() {
			logger.Infof("\nStart to recover degraded mcp")
			mc.deleteNoWait()
			mcp.RecoverFromDegraded()
		}()
		mc.create()

		exutil.By("Check mcp worker is degraded")
		o.Eventually(mcp.getDegradedMachineCount, "5m", "5s").Should(o.BeNumerically("==", 1), "Degraded machine count is not ==1")
		logger.Infof("OK\n")

		exutil.By("Get degraded node")
		allNodes := mcp.GetNodesOrFail()
		for _, node := range allNodes {
			if node.GetMachineConfigState() == expectedMCState {
				degradedNode = node
				break
			}
		}
		o.Expect(degradedNode).NotTo(o.BeNil(), "Degraded node not found")
		o.Expect(degradedNode.GetMachineConfigReason()).Should(o.ContainSubstring(`invalid extensions found: [zsh]`), "value of annotation machine config reason is not expected")
		logger.Infof("Found degraded node %s", degradedNode.GetName())
		logger.Infof("OK\n")

		exutil.By("Check corresponding MCN")
		mcn := NewMachineConfigNode(oc.AsAdmin(), degradedNode.GetName())
		o.Expect(mcn.Exists()).Should(o.BeTrue(), "Cannot find MCN %s", degradedNode.GetName())
		logger.Infof("OK\n")

		exutil.By("Check MCN conditions")
		o.Expect(mcn.GetAppliedFilesAndOS()).Should(o.Equal(expectedMCNStatus), "status of MCN condition UPDATEDFILESANDOS is not expected")
		o.Expect(mcn.GetUpdateExecuted()).Should(o.Equal(expectedMCNStatus), "status of MCN condition UPDATEEXECUTED is not expected")
		logger.Infof("OK\n")

	})

	g.It("Author:rioliu-NonPreRelease-Longduration-High-69755-MachineConfigNode resources should be synced when node is created/deleted [Disruptive]", func() {

		var (
			provisioningMachine Machine
			deletingMachine     Machine
			mcp                 = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		)

		skipTestIfWorkersCannotBeScaled(oc.AsAdmin())

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

	g.It("Author:sregidor-NonHyperShiftHOST-NonPreRelease-High-74644-Scope each MCN object to only be accessible from its associated MCD [Disruptive]", func() {
		SkipIfSNO(oc.AsAdmin())
		var (
			nodes              = exutil.OrFail[[]Node](NewNodeList(oc.AsAdmin()).GetAllLinux())
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
		o.Expect(err).To(o.BeAssignableToTypeOf(&exutil.ExitError{}), "Unexpected error while patching the machineconfignode resource from another MCD")
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

})
