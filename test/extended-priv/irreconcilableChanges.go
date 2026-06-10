package extended

import (
	"fmt"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

const (
	workerLabelKey = "machineconfiguration.openshift.io/worker"
)

func platformBasedDisksPatch(platform string, ms *MachineSet) error {
	var err error
	switch platform {
	case AWSPlatform:
		err = ms.Patch("json", `[{"op":"add","path":"/spec/template/spec/providerSpec/value/blockDevices/-","value":{"ebs":{"encrypted":false,"iops":0,"volumeSize":120,"volumeType":"gp3"},"deviceName":"/dev/sdb"}},{"op":"add","path":"/spec/template/spec/providerSpec/value/blockDevices/-","value":{"ebs":{"encrypted":false,"iops":0,"volumeSize":120,"volumeType":"gp3"},"deviceName":"/dev/sdc"}}]`)
	case GCPPlatform:
		err = ms.Patch("json", `[{"op":"add","path":"/spec/template/spec/providerSpec/value/disks/-","value":{"autoDelete":true,"boot":false,"sizeGb":16,"type":"pd-standard"}},{"op":"add","path":"/spec/template/spec/providerSpec/value/disks/-","value":{"autoDelete":true,"boot":false,"sizeGb":16,"type":"pd-standard"}}]`)
	case AzurePlatform:
		err = ms.Patch("json", `[{"op":"add","path":"/spec/template/spec/providerSpec/value/dataDisks","value":[{"nameSuffix":"disk1","diskSizeGB":16,"lun":0,"deletionPolicy":"Delete"},{"nameSuffix":"disk2","diskSizeGB":16,"lun":1,"deletionPolicy":"Delete"}]}]`)
	case VspherePlatform:
		err = ms.Patch("json", `[{"op":"add","path":"/spec/template/spec/providerSpec/value/dataDisks","value":[{"name":"data-disk-1","sizeGiB":16},{"name":"data-disk-2","sizeGiB":16}]}]`)
	default:
		err = fmt.Errorf("Unknown platform: %v", platform)
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
	case GCPPlatform, VspherePlatform:
		return []string{
			"/dev/sdb",
			"/dev/sdc",
			"/dev/sdd",
			"/dev/sde",
		}
	case AzurePlatform:
		return []string{
			"/dev/disk/azure/scsi1/lun0",
			"/dev/disk/azure/scsi1/lun1",
			"/dev/disk/azure/scsi1/lun2",
			"/dev/disk/azure/scsi1/lun3",
		}
	}
	return []string{}
}

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/disruptive][Disruptive][OCPFeatureGate:IrreconcilableMachineConfig][Serial]", g.Ordered, func() {
	defer g.GinkgoRecover()

	var (
		oc       = exutil.NewCLI("mco-irreconcilable-changes", exutil.KubeConfigPath()).AsAdmin()
		platform = exutil.CheckPlatform(oc)
	)

	g.JustBeforeEach(func() {
		PreChecks(oc)
		skipIfNoTechPreview(oc)
		skipTestIfSupportedPlatformNotMatched(oc, AWSPlatform, GCPPlatform, AzurePlatform, VspherePlatform)
		skipTestIfSNOCluster(oc)
	})

	g.It("Base irreconcilable changes detection on existing nodes", g.Label("Platform:aws", "Platform:gce", "Platform:azure", "Platform:vsphere"), func() {
		var (
			machineconfiguration = GetMachineConfiguration(oc)
			mcName               = "irreconcilable-base-test"
			initialMcSpecs       = machineconfiguration.GetSpecOrFail()
			mcpool               = "worker"
		)

		mcp := NewMachineConfigPool(oc, mcpool)

		exutil.By("Step 1: Enable irreconcilableValidationOverrides")
		err := machineconfiguration.EnableIrreconcilableValidationOverrides()
		o.Expect(err).NotTo(o.HaveOccurred())
		err = mcp.WaitForUpdatedStatus()
		o.Expect(err).NotTo(o.HaveOccurred())

		mc := NewMachineConfig(oc, mcName, mcpool).SetMCOTemplate("extra-disks.yaml")
		disks := platformBasedDisksNames(platform)

		exutil.By("Step 2: Apply extra-disks MachineConfig")
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
			logger.Infof("Restore initial MachineConfiguration spec")
			err = machineconfiguration.SetSpec(initialMcSpecs)
			o.Expect(err).NotTo(o.HaveOccurred())
		}()
		defer mc.DeleteWithWait()

		exutil.By("Step 3: Verify all worker nodes report irreconcilable changes")
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
		logger.Infof("All worker nodes have irreconcilable changes as expected!\n")
	})

	g.It("[PolarionID:84219] Scale up with a mix of irreconcilable and supported fields [Disruptive]", g.Label("Platform:aws", "Platform:gce", "Platform:azure", "Platform:vsphere"), func() {
		var (
			machineconfiguration = GetMachineConfiguration(oc)
			mcName               = "irreconcilable-scaleup-test"
			initialMcSpecs       = machineconfiguration.GetSpecOrFail()
			mcpool               = "worker"
		)

		SkipTestIfWorkersCannotBeScaled(oc)
		mcp := NewMachineConfigPool(oc, mcpool)

		exutil.By("Step 1: Enable irreconcilableValidationOverrides")
		err := machineconfiguration.EnableIrreconcilableValidationOverrides()
		o.Expect(err).NotTo(o.HaveOccurred())
		err = mcp.WaitForUpdatedStatus()
		o.Expect(err).NotTo(o.HaveOccurred())

		mc := NewMachineConfig(oc, mcName, mcpool).SetMCOTemplate("extra-disks-with-files.yaml")
		disks := platformBasedDisksNames(platform)

		exutil.By("Step 2: Apply extra-disks-with-files MachineConfig")
		err = mc.Create(
			"-p", "NAME="+mcName,
			"-p", "POOL="+mcpool,
			"-p", "DEVICE1="+disks[0],
			"-p", "DEVICE2="+disks[1],
			"-p", "FILE_PATH=/etc/temp_config.conf",
			"-p", "FILE_CONTENT=THIS_IS_A_TEST_FILE_CONF",
		)
		o.Expect(err).NotTo(o.HaveOccurred())
		logger.Infof("MC %s created successfully", mcName)

		err = mcp.WaitForUpdatedStatus()
		o.Expect(err).NotTo(o.HaveOccurred())

		defer func() {
			logger.Infof("Restore initial MachineConfiguration spec")
			err = machineconfiguration.SetSpec(initialMcSpecs)
			o.Expect(err).NotTo(o.HaveOccurred())
		}()
		defer mc.DeleteWithWait()

		exutil.By("Step 3: Verify old nodes report only storage irreconcilable changes (not files)")
		workerNodes, err := mcp.getSelectedNodes(workerLabelKey)
		o.Expect(err).NotTo(o.HaveOccurred())

		for _, node := range workerNodes {
			o.Eventually(func() (string, error) {
				return node.GetIrreconcilableChanges()
			}, "5m", "10s").Should(o.ContainSubstring("spec.config.storage.disks"),
				"Node %s should have irreconcilable changes for storage.disks", node.GetName())
			logger.Infof("Node %s has irreconcilable changes as expected", node.GetName())
		}
		logger.Infof("All worker nodes report irreconcilable storage changes!\n")

		exutil.By("Step 4: Create duplicate machineset with custom disks")
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

		exutil.By("Step 5: Verify new node has no irreconcilable changes")
		newNodes := newMS.GetNodesOrFail()
		o.Expect(newNodes).To(o.HaveLen(1))
		newNode := newNodes[0]
		logger.Infof("New node is: %s", newNode.GetName())

		newExtNode := NewNode(oc.AsAdmin(), newNode.GetName())
		o.Eventually(func() (string, error) {
			return newExtNode.GetIrreconcilableChanges()
		}, "5m", "10s").Should(o.BeEmpty(), "New node should have no irreconcilable changes")
		logger.Infof("New node %s has no irreconcilable changes as expected\n", newNode.GetName())

		exutil.By("Step 6: Verify storage configuration on new node")
		_, err = newExtNode.DebugNodeWithChroot("mount", "/dev/md/data", "/var/lib/data")
		o.Expect(err).NotTo(o.HaveOccurred())

		stdout, err := newExtNode.DebugNodeWithChroot("mdadm", "--detail", "--scan")
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(stdout).Should(o.ContainSubstring("/dev/md/data"))
		logger.Infof("Storage configuration verified on new node\n")

		exutil.By("Step 7: Delete the irreconcilable MC")
		mc.DeleteWithWait()

		exutil.By("Step 8: Verify old nodes have irreconcilable changes cleared")
		for _, node := range workerNodes {
			o.Eventually(func() (string, error) {
				return node.GetIrreconcilableChanges()
			}, "5m", "10s").Should(o.BeEmpty(),
				"Node %s should have cleared irreconcilable changes after MC removal", node.GetName())
			logger.Infof("Node %s has cleared irreconcilable changes as expected", node.GetName())
		}
		logger.Infof("All existing worker nodes have cleared irreconcilable changes!\n")

		exutil.By("Step 9: Verify new node now reports irreconcilable changes")
		o.Eventually(func() (string, error) {
			return newExtNode.GetIrreconcilableChanges()
		}, "5m", "10s").Should(o.SatisfyAll(
			o.ContainSubstring("spec.config.storage.disks"),
			o.ContainSubstring("spec.config.storage.raid"),
			o.ContainSubstring("spec.config.storage.filesystems"),
		), "New node should report irreconcilable changes after MC removal")
		logger.Infof("New node %s correctly reports irreconcilable changes after MC removal\n", newNode.GetName())
	})

	g.It("Irreconcilable changes persist after feature has been disabled. New irreconcilable configs will be rejected", g.Label("Platform:aws", "Platform:gce", "Platform:azure", "Platform:vsphere"), func() {
		var (
			machineconfiguration = GetMachineConfiguration(oc)
			mcName               = "irreconcilable-persist-test"
			initialMcSpecs       = machineconfiguration.GetSpecOrFail()
			mcpool               = "worker"
		)

		mcp := NewMachineConfigPool(oc, mcpool)

		exutil.By("Step 1: Enable irreconcilableValidationOverrides")
		err := machineconfiguration.EnableIrreconcilableValidationOverrides()
		o.Expect(err).NotTo(o.HaveOccurred())
		err = mcp.WaitForUpdatedStatus()
		o.Expect(err).NotTo(o.HaveOccurred())

		mc := NewMachineConfig(oc, mcName, mcpool).SetMCOTemplate("extra-disks.yaml")
		disks := platformBasedDisksNames(platform)

		exutil.By("Step 2: Apply extra-disks MachineConfig")
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
			logger.Infof("Restore initial MachineConfiguration spec")
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

		exutil.By("Step 3: Verify nodes report irreconcilable changes")
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
		logger.Infof("All worker nodes have irreconcilable changes as expected!\n")

		exutil.By("Step 4: Disable irreconcilable feature")
		err = machineconfiguration.RemoveIrreconcilableValidationOverrides()
		o.Expect(err).NotTo(o.HaveOccurred())
		err = mcp.WaitForUpdatedStatus()
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Step 5: Verify irreconcilable changes persist after disabling")
		for _, node := range workerNodes {
			o.Eventually(func() (string, error) {
				return node.GetIrreconcilableChanges()
			}, "5m", "10s").Should(o.SatisfyAll(
				o.ContainSubstring("spec.config.storage.disks"),
				o.ContainSubstring("spec.config.storage.raid"),
				o.ContainSubstring("spec.config.storage.filesystems"),
			), "Node %s should still have irreconcilable changes after disabling", node.GetName())
			logger.Infof("Node %s irreconcilable changes persisted as expected", node.GetName())
		}
		logger.Infof("All worker nodes still have irreconcilable changes!\n")

		exutil.By("Step 6: Apply new irreconcilable MC and verify rejection")
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

	g.It("Irreconcilable changes cleared after reverting MC", g.Label("Platform:aws", "Platform:gce", "Platform:azure", "Platform:vsphere"), func() {
		var (
			machineconfiguration = GetMachineConfiguration(oc)
			mcName               = "irreconcilable-clear-test"
			initialMcSpecs       = machineconfiguration.GetSpecOrFail()
			mcpool               = "worker"
		)

		mcp := NewMachineConfigPool(oc, mcpool)

		exutil.By("Step 1: Enable irreconcilableValidationOverrides and apply MC")
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
			logger.Infof("Restore initial MachineConfiguration spec")
			err = machineconfiguration.SetSpec(initialMcSpecs)
			o.Expect(err).NotTo(o.HaveOccurred())
		}()
		defer mc.DeleteWithWait()

		exutil.By("Step 2: Verify nodes report irreconcilable changes")
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
		logger.Infof("All worker nodes have irreconcilable changes as expected!\n")

		exutil.By("Step 3: Delete the irreconcilable MC")
		mc.DeleteWithWait()

		exutil.By("Step 4: Verify irreconcilable changes cleared on all nodes")
		for _, node := range workerNodes {
			o.Eventually(func() (string, error) {
				return node.GetIrreconcilableChanges()
			}, "5m", "10s").Should(o.BeEmpty(),
				"Node %s should have cleared irreconcilable changes after MC removal", node.GetName())
			logger.Infof("Node %s has cleared irreconcilable changes as expected", node.GetName())
		}
		logger.Infof("All worker nodes have cleared irreconcilable changes!\n")
	})

	g.It("Irreconcilable MC rejected without override enabled", g.Label("Platform:aws", "Platform:gce", "Platform:azure", "Platform:vsphere"), func() {
		var (
			mcName = "irreconcilable-reject-test"
			mcpool = "worker"
		)

		mcp := NewMachineConfigPool(oc, mcpool)
		mc := NewMachineConfig(oc, mcName, mcpool).SetMCOTemplate("extra-disks.yaml")
		disks := platformBasedDisksNames(platform)

		exutil.By("Step 1: Apply irreconcilable MC without enabling override")
		err := mc.Create(
			"-p", "NAME="+mcName,
			"-p", "POOL="+mcpool,
			"-p", "DEVICE1="+disks[0],
			"-p", "DEVICE2="+disks[1],
		)
		o.Expect(err).NotTo(o.HaveOccurred())
		defer mc.DeleteWithWait()

		exutil.By("Step 2: Verify MCP becomes RenderDegraded")
		o.Eventually(func() (string, error) {
			return mcp.Get(`{.status.conditions[?(@.type=="RenderDegraded")].status}`)
		}, "5m", "10s").Should(o.Equal("True"), "MCP should be RenderDegraded after applying irreconcilable MC without override")

		exutil.By("Step 3: Delete MC and verify pool recovers")
		mc.DeleteWithWait()
		o.Expect(mcp.WaitForNotDegradedStatus()).To(o.Succeed())
	})
})
