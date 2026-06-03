package extended

import (
	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

const (
	IrreconcilableChangesLabelKey = "machineconfiguration.openshift.io/irreconcilable"
	workerLabelKey                = "machineconfiguration.openshift.io/worker"
)

func platformBasedDisksPatch(platform string, ms *MachineSet) error {
	var err error
	if platform == AWSPlatform {
		err = ms.Patch("json", `[{"op":"add","path":"/spec/template/spec/providerSpec/value/blockDevices/-","value":{"ebs":{"encrypted":false,"iops":0,"volumeSize":120,"volumeType":"gp3"},"deviceName":"/dev/sdb"}},{"op":"add","path":"/spec/template/spec/providerSpec/value/blockDevices/-","value":{"ebs":{"encrypted":false,"iops":0,"volumeSize":120,"volumeType":"gp3"},"deviceName":"/dev/sdc"}}]`)
	} else {
		err = ms.Patch("json", `[{"op":"add","path":"/spec/template/spec/providerSpec/value/disks/-","value":{"autoDelete":true,"boot":false,"sizeGb":16,"type":"pd-standard"}},{"op":"add","path":"/spec/template/spec/providerSpec/value/disks/-","value":{"autoDelete":true,"boot":false,"sizeGb":16,"type":"pd-standard"}}]`)
	}
	return err
}

func platformBasedDisksNames(platform string) []string {
	switch platform {
	case AWSPlatform:
		return []string{
			"/dev/nvme1n1",
			"/dev/nvme2n1",
			"/dev/nvme3n1",
			"/dev/nvme4n1",
		}
	case GCPPlatform:
		return []string{
			"/dev/sda",
			"/dev/sdb",
			"/dev/sdc",
			"/dev/sdd",
		}
	}
	return []string{}
}

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/disruptive][Disruptive][OCPFeatureGate:IrreconcilableMachineConfig]", g.Ordered, func() {
	defer g.GinkgoRecover()

	var (
		oc       = exutil.NewCLI("mco-irreconcilable-changes", exutil.KubeConfigPath()).AsAdmin()
		platform = exutil.CheckPlatform(oc)
	)

	g.JustBeforeEach(func() {
		PreChecks(oc)
		skipIfNoTechPreview(oc)
		skipTestIfSupportedPlatformNotMatched(oc, AWSPlatform, GCPPlatform)
		skipTestIfSNOCluster(oc)
	})

	g.It("[PolarionID:84219][OTP] Ensure new nodes adopt irreconcilable config while in old nodes surf [Disruptive]", g.Label("Platform:aws", "Platform:gce"), func() {
		var (
			mcp                  = GetCompactCompatiblePool(oc.AsAdmin())
			machineconfiguration = GetMachineConfiguration(oc.AsAdmin())
			mcName               = "extra-disks-" + GetCurrentTestPolarionIDNumber()
			mc                   = NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName()).SetMCOTemplate("extra-disks.yaml")
			nodes                = mcp.GetSortedNodesOrFail()
		)

		exutil.By("Save initial MachineConfiguration spec")
		initialMachineConfigSpec := machineconfiguration.GetSpecOrFail()
		logger.Infof("Initial MachineConfiguration spec: %s", initialMachineConfigSpec)

		defer func() {
			logger.Infof("Restore initial MachineConfiguration spec")
			err := machineconfiguration.SetSpec(initialMachineConfigSpec)
			o.Expect(err).NotTo(o.HaveOccurred())
		}()

		exutil.By("Step 1: Enable irreconcilableValidationOverrides")
		err := machineconfiguration.EnableIrreconcilableValidationOverrides()
		o.Expect(err).NotTo(o.HaveOccurred())
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Step 2: Apply extra-disks MachineConfig")
		platform := exutil.CheckPlatform(oc)

		disks := platformBasedDisksNames(platform)

		mc.SetParams("-p", "DEVICE1="+disks[0], "-p", "DEVICE2="+disks[1])
		defer mc.DeleteWithWait()
		mc.create()
		logger.Infof("OK!\n")

		exutil.By("Step 3: Check irreconcilable differences for existing worker nodes")
		for _, node := range nodes {
			irreconcilableChanges := OrFail[string](node.GetIrreconcilableChanges())
			o.Expect(irreconcilableChanges).To(o.ContainSubstring("spec.config.storage.disks"))
			o.Expect(irreconcilableChanges).To(o.ContainSubstring("spec.config.storage.raid"))
			o.Expect(irreconcilableChanges).To(o.ContainSubstring("spec.config.storage.filesystems"))
			logger.Infof("Node %s has irreconcilable changes as expected", node.GetName())
		}
		logger.Infof("All worker nodes have irreconcilable changes as expected!\n")

		exutil.By("Step 4: Create duplicate machineset with custom disks")
		machineset := OrFail[*MachineSet](GetScalableMachineSet(oc.AsAdmin()))
		newMSName := machineset.GetName() + "-ms-ic-t1"
		newMS, err := machineset.Duplicate(newMSName)
		o.Expect(err).NotTo(o.HaveOccurred())

		defer func() {
			if newMS.Exists() {
				err := newMS.ScaleTo(0)
				o.Expect(err).NotTo(o.HaveOccurred())
				err = newMS.WaitUntilReady("10m")
				o.Expect(err).NotTo(o.HaveOccurred())
				err = newMS.Delete()
				o.Expect(err).NotTo(o.HaveOccurred())
			}
		}()

		err = platformBasedDisksPatch(platform, newMS)
		o.Expect(err).NotTo(o.HaveOccurred())

		o.Expect(newMS.ScaleTo(1)).To(o.Succeed())
		o.Expect(newMS.WaitUntilReady("15m")).To(o.Succeed())

		exutil.By("Step 5: Verify new node has no irreconcilable differences")
		newNodes := newMS.GetNodesOrFail()
		o.Expect(newNodes).To(o.HaveLen(1))
		newNode := newNodes[0]
		logger.Infof("New node is: %s", newNode.GetName())

		newNodeMCN := NewMachineConfigNode(oc.AsAdmin(), newNode.GetName())
		o.Eventually(func() string {
			return newNodeMCN.GetOrFail(`{.status.irreconcilableChanges}`)
		}, "5m", "10s").Should(o.Or(o.BeEmpty(), o.Equal("[]")), "New node should have no irreconcilable changes")

		logger.Infof("OK!\n")

		exutil.By("Step 6: Verify storage configuration on new node")

		// Mount the RAID device
		_, err = newNode.DebugNodeWithChroot("mount", "/dev/md/data", "/var/lib/data")
		o.Expect(err).NotTo(o.HaveOccurred())

		// Check mdadm details
		stdout, err := newNode.DebugNodeWithChroot("mdadm", "--detail", "--scan")
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(stdout).Should(o.ContainSubstring("/dev/md/data"))

		logger.Infof("OK!\n")
	})

	g.It("Scale up with a mix of irreconcilable and supported fields", g.Label("Platform:aws", "Platform:gce"), func() {
		var (
			machineconfiguration = GetMachineConfiguration(oc)
			mcName               = "irreconcilable-mix-test-1"
			initialMcSpecs       = machineconfiguration.GetSpecOrFail()
			mcpool               = "worker"
		)

		SkipTestIfWorkersCannotBeScaled(oc)

		mcp := NewMachineConfigPool(oc, mcpool)

		exutil.By("Step 1: Enable irreconcilablevalidationoverride")

		err := machineconfiguration.EnableIrreconcilableValidationOverrides()
		o.Expect(err).NotTo(o.HaveOccurred())

		err = mcp.WaitForUpdatedStatus()
		o.Expect(err).NotTo(o.HaveOccurred())

		mc := NewMachineConfig(oc, mcName, mcpool).SetMCOTemplate("extra-disks-with-files.yaml")

		disks := platformBasedDisksNames(platform)

		err = mc.Create(
			"-p", "NAME="+mcName,
			"-p", "POOL="+mcpool,
			"-p", "DEVICE1="+disks[0],
			"-p", "DEVICE2="+disks[1],
			"-p", "FILE_PATH="+"/etc/temp_config.conf",
			"-p", "FILE_CONTENT="+"THIS_IS_A_TEST_FILE_CONF",
		)
		o.Expect(err).NotTo(o.HaveOccurred())
		logger.Infof("MC %s created successfully", mcName)

		err = mcp.WaitForUpdatedStatus()
		o.Expect(err).NotTo(o.HaveOccurred())

		defer func() {
			logger.Infof("restore initial machineConfiguration specs")
			err = machineconfiguration.SetSpec(initialMcSpecs)
			o.Expect(err).NotTo(o.HaveOccurred())
		}()
		defer mc.DeleteWithWait()

		exutil.By("Step 2: Verify old nodes report irreconcilablevalidationoverride changes in their MCN")
		workerNodes, err := mcp.getSelectedNodes(workerLabelKey)
		o.Expect(err).NotTo(o.HaveOccurred())

		for _, node := range workerNodes {
			o.Eventually(func() (string, error) {
				return node.GetIrreconcilableChanges()
			}, "5m", "10s").Should(o.ContainSubstring("spec.config.storage.disks"),
				"Node %s should have irreconcilable changes for storage.disks", node.GetName())
			logger.Infof("Node %s has irreconcilable changes as expected", node.GetName())
		}

		logger.Infof("All worker nodes have irreconcilable changes as expected!\n")

		exutil.By("Step 3: Create duplicate machineset with custom disks")
		machineset := OrFail[*MachineSet](GetScalableMachineSet(oc.AsAdmin()))
		newMSName := machineset.GetName() + "-ms-ic-t2"
		newMS, err := machineset.Duplicate(newMSName)
		o.Expect(err).NotTo(o.HaveOccurred())

		defer func() {
			if newMS.Exists() {
				err := newMS.ScaleTo(0)
				o.Expect(err).NotTo(o.HaveOccurred())
				err = newMS.WaitUntilReady("10m")
				o.Expect(err).NotTo(o.HaveOccurred())
				err = newMS.Delete()
				o.Expect(err).NotTo(o.HaveOccurred())
			}
		}()

		err = platformBasedDisksPatch(platform, newMS)
		o.Expect(err).NotTo(o.HaveOccurred())

		o.Expect(newMS.ScaleTo(1)).To(o.Succeed())
		o.Expect(newMS.WaitUntilReady("15m")).To(o.Succeed())

		exutil.By("Step 4: Verify new node has no irreconcilable differences")
		newNodes := newMS.GetNodesOrFail()
		o.Expect(newNodes).To(o.HaveLen(1))
		newNode := newNodes[0]
		logger.Infof("New node is: %s", newNode.GetName())

		newNodeMCN := NewMachineConfigNode(oc.AsAdmin(), newNode.GetName())
		o.Eventually(func() string {
			return newNodeMCN.GetOrFail(`{.status.irreconcilableChanges}`)
		}, "5m", "10s").Should(o.Or(o.BeEmpty(), o.Equal("[]")), "New node should have no irreconcilable changes")

		logger.Infof("OK!\n")
	})

	g.It("Irreconcilable changes persists after feature has been disabled. New Irreconcilable configs will be rejected", g.Label("Platform:aws", "Platform:gce"), func() {
		var (
			machineconfiguration = GetMachineConfiguration(oc)
			mcName               = "irreconcilable-mix-test-3"
			initialMcSpecs       = machineconfiguration.GetSpecOrFail()
			mcpool               = "worker"
		)

		SkipTestIfWorkersCannotBeScaled(oc)

		mcp := NewMachineConfigPool(oc, mcpool)

		exutil.By("Step 1: Enable irreconcilablevalidationoverride")

		err := machineconfiguration.EnableIrreconcilableValidationOverrides()
		o.Expect(err).NotTo(o.HaveOccurred())

		err = mcp.WaitForUpdatedStatus()
		o.Expect(err).NotTo(o.HaveOccurred())

		mc := NewMachineConfig(oc, mcName, mcpool).SetMCOTemplate("extra-disks.yaml")

		disks := platformBasedDisksNames(platform)

		err = mc.Create(
			"-p", "NAME="+mcName,
			"-p", "POOL="+mcpool,
			"-p", "DEVICE1="+disks[0],
			"-p", "DEVICE2="+disks[1],
		)
		o.Expect(err).NotTo(o.HaveOccurred())
		logger.Infof("MC %s created successfully", mcName)

		err = mcp.WaitForUpdatedStatus()
		o.Expect(err).NotTo(o.HaveOccurred())

		defer func() {
			logger.Infof("restore initial machineConfiguration specs")
			err = machineconfiguration.SetSpec(initialMcSpecs)
			o.Expect(err).NotTo(o.HaveOccurred())
		}()
		defer func() {
			// Re-enable the override before deleting mc: without it, SetSpec (which replaces
			// the entire /spec via op:add) triggers a full MCO reconciliation that detects the
			// irreconcilable disk changes in the current rendered config → RenderDegraded.
			err := machineconfiguration.EnableIrreconcilableValidationOverrides()
			o.Expect(err).NotTo(o.HaveOccurred())
			mc.DeleteWithWait()
		}()

		exutil.By("Step 2: Verify nodes report irreconcilablevalidationoverride changes in their MCN")
		workerNodes, err := mcp.getSelectedNodes(workerLabelKey)
		o.Expect(err).NotTo(o.HaveOccurred())

		for _, node := range workerNodes {
			irreconcilableChanges := OrFail[string](node.GetIrreconcilableChanges())
			o.Expect(irreconcilableChanges).To(o.ContainSubstring("spec.config.storage.disks"))
			o.Expect(irreconcilableChanges).To(o.ContainSubstring("spec.config.storage.raid"))
			o.Expect(irreconcilableChanges).To(o.ContainSubstring("spec.config.storage.filesystems"))
			logger.Infof("Node %s has irreconcilable changes as expected", node.GetName())
		}

		logger.Infof("All worker nodes have irreconcilable changes as expected!\n")

		exutil.By("Step 3: Disable Irreconcilable feature")

		err = machineconfiguration.RemoveIrreconcilableValidationOverrides()
		o.Expect(err).NotTo(o.HaveOccurred())

		err = mcp.WaitForUpdatedStatus()
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Step 4: Verify nodes report irreconcilablevalidationoverride changes in their MCN")

		for _, node := range workerNodes {
			irreconcilableChanges := OrFail[string](node.GetIrreconcilableChanges())
			o.Expect(irreconcilableChanges).To(o.ContainSubstring("spec.config.storage.disks"))
			o.Expect(irreconcilableChanges).To(o.ContainSubstring("spec.config.storage.raid"))
			o.Expect(irreconcilableChanges).To(o.ContainSubstring("spec.config.storage.filesystems"))
			logger.Infof("Node %s has irreconcilable changes as expected", node.GetName())
		}

		logger.Infof("All worker nodes have irreconcilable changes as expected!\n")

		newMcName := mcName + "-2"

		mc2 := NewMachineConfig(oc, newMcName, mcpool).SetMCOTemplate("extra-disks.yaml")

		err = mc2.Create(
			"-p", "NAME="+newMcName,
			"-p", "POOL="+mcpool,
			"-p", "DEVICE1="+disks[2],
			"-p", "DEVICE2="+disks[3],
		)
		o.Expect(err).NotTo(o.HaveOccurred())

		o.Eventually(func() (string, error) {
			return mcp.Get(`{.status.conditions[?(@.type=="RenderDegraded")].status}`)
		}, "5m", "10s").Should(o.Equal("True"), "MCP should be RenderDegraded after applying irreconcilable MC without override")

		mc2.DeleteWithWait()
		o.Expect(mcp.WaitForNotDegradedStatus()).To(o.Succeed())
	})

	g.It("Irreconcilable changes cleared after reverting MC. New node reports changes", g.Label("Platform:aws", "Platform:gce"), func() {
		var (
			machineconfiguration = GetMachineConfiguration(oc)
			mcName               = "irreconcilable-mix-test-4"
			initialMcSpecs       = machineconfiguration.GetSpecOrFail()
			mcpool               = "worker"
		)
		SkipTestIfWorkersCannotBeScaled(oc)
		mcp := NewMachineConfigPool(oc, mcpool)
		exutil.By("Step 1: Enable irreconcilablevalidationoverride and apply irreconcilable MC")
		err := machineconfiguration.EnableIrreconcilableValidationOverrides()
		o.Expect(err).NotTo(o.HaveOccurred())
		err = mcp.WaitForUpdatedStatus()
		o.Expect(err).NotTo(o.HaveOccurred())
		mc := NewMachineConfig(oc, mcName, mcpool).SetMCOTemplate("extra-disks.yaml")

		disks := platformBasedDisksNames(platform)

		err = mc.Create(
			"-p", "NAME="+mcName,
			"-p", "POOL="+mcpool,
			"-p", "DEVICE1="+disks[0],
			"-p", "DEVICE2="+disks[1],
		)
		o.Expect(err).NotTo(o.HaveOccurred())
		logger.Infof("MC %s created successfully", mcName)
		err = mcp.WaitForUpdatedStatus()
		o.Expect(err).NotTo(o.HaveOccurred())
		defer func() {
			logger.Infof("restore initial machineConfiguration specs")
			err = machineconfiguration.SetSpec(initialMcSpecs)
			o.Expect(err).NotTo(o.HaveOccurred())
		}()
		defer mc.DeleteWithWait()

		exutil.By("Step 2: Verify existing nodes report irreconcilable changes in their MCN")
		workerNodes, err := mcp.getSelectedNodes(workerLabelKey)
		o.Expect(err).NotTo(o.HaveOccurred())

		for _, node := range workerNodes {
			o.Eventually(func() (string, error) {
				return node.GetIrreconcilableChanges()
			}, "5m", "10s").Should(o.SatisfyAll(
				o.ContainSubstring("spec.config.storage.disks"),
				o.ContainSubstring("spec.config.storage.raid"),
				o.ContainSubstring("spec.config.storage.filesystems"),
			), "Node %s should have irreconcilable changes", node.GetName())
			logger.Infof("Node %s has irreconcilable changes as expected", node.GetName())
		}
		logger.Infof("All existing worker nodes have irreconcilable changes as expected!\n")

		exutil.By("Step 3: Scale up new machineset with extra EBS disks")
		machineset := OrFail[*MachineSet](GetScalableMachineSet(oc.AsAdmin()))
		newMSName := machineset.GetName() + "-ms-ic-t4"
		newMS, err := machineset.Duplicate(newMSName)
		o.Expect(err).NotTo(o.HaveOccurred())

		defer func() {
			if newMS.Exists() {
				err := newMS.ScaleTo(0)
				o.Expect(err).NotTo(o.HaveOccurred())
				err = newMS.WaitUntilReady("10m")
				o.Expect(err).NotTo(o.HaveOccurred())
				err = newMS.Delete()
				o.Expect(err).NotTo(o.HaveOccurred())
			}
		}()
		err = platformBasedDisksPatch(platform, newMS)
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(newMS.ScaleTo(1)).To(o.Succeed())
		o.Expect(newMS.WaitUntilReady("15m")).To(o.Succeed())
		newNodes := newMS.GetNodesOrFail()
		o.Expect(newNodes).To(o.HaveLen(1))
		newNode := newNodes[0]
		logger.Infof("New node is: %s", newNode.GetName())

		exutil.By("Step 4: Verify new node has no irreconcilable changes (booted with disks MC active)")
		newExtNode := NewNode(oc.AsAdmin(), newNode.GetName())
		o.Eventually(func() (string, error) {
			return newExtNode.GetIrreconcilableChanges()
		}, "5m", "10s").Should(o.BeEmpty(), "New node should have no irreconcilable changes since it was provisioned with the disks MC")
		logger.Infof("New node %s has no irreconcilable changes as expected\n", newNode.GetName())

		exutil.By("Step 5: Delete the irreconcilable MC")
		mc.DeleteWithWait()

		exutil.By("Step 6: Verify existing nodes have irreconcilable changes cleared")
		for _, node := range workerNodes {
			o.Eventually(func() (string, error) {
				return node.GetIrreconcilableChanges()
			}, "5m", "10s").Should(o.BeEmpty(),
				"Node %s should have cleared irreconcilable changes after MC removal", node.GetName())
			logger.Infof("Node %s has cleared irreconcilable changes as expected", node.GetName())
		}
		logger.Infof("All existing worker nodes have cleared irreconcilable changes as expected!\n")

		exutil.By("Step 7: Verify new node now reports irreconcilable changes")
		o.Eventually(func() (string, error) {
			return newExtNode.GetIrreconcilableChanges()
		}, "5m", "10s").Should(o.SatisfyAll(
			o.ContainSubstring("spec.config.storage.disks"),
			o.ContainSubstring("spec.config.storage.raid"),
			o.ContainSubstring("spec.config.storage.filesystems"),
		), "New node should report irreconcilable changes after MC removal (its install-time MC had disks, current rendered does not)")

		logger.Infof("New node %s correctly reports irreconcilable changes after MC removal\n", newNode.GetName())
	})
})
