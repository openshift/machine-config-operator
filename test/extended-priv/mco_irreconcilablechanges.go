package extended

import (
	"errors"
	"fmt"
	"slices"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"github.com/tidwall/gjson"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/disruptive][Disruptive][OCPFeatureGate:IrreconcilableMachineConfig][Serial]", g.Ordered, func() {
	defer g.GinkgoRecover()

	var (
		oc       = exutil.NewCLI("mco-irreconcilable-changes", exutil.KubeConfigPath()).AsAdmin()
		platform = exutil.CheckPlatform(oc)
	)

	g.JustBeforeEach(func() {
		PreChecks(oc)
		skipTestIfSupportedPlatformNotMatched(oc, AWSPlatform, GCPPlatform, AzurePlatform, VspherePlatform, BaremetalPlatform)
		SkipIfCompactOrSNO(oc)
	})

	g.It("Base irreconcilable changes detection on existing nodes", g.Label("Platform:aws", "Platform:gce", "Platform:azure", "Platform:vsphere", "Platform:baremetal"), func() {
		var (
			machineconfiguration = GetMachineConfiguration(oc)
			mcName               = "irreconcilable-base-test"
			initialMcSpecs       = machineconfiguration.GetSpecOrFail()
		)

		mcp := NewMachineConfigPool(oc, MachineConfigPoolWorker)

		defer func() {
			logger.Infof("Restore initial MachineConfiguration spec")
			err := machineconfiguration.SetSpec(initialMcSpecs)
			o.Expect(err).NotTo(o.HaveOccurred())
		}()

		exutil.By("Step 1: Enable irreconcilableValidationOverrides")
		err := machineconfiguration.EnableIrreconcilableValidationOverrides()
		o.Expect(err).NotTo(o.HaveOccurred())
		err = mcp.WaitForUpdatedStatus()
		o.Expect(err).NotTo(o.HaveOccurred())

		mc := NewMachineConfig(oc, mcName, MachineConfigPoolWorker).SetMCOTemplate("extra-disks.yaml")
		disks := platformBasedDisksNames(platform)

		defer mc.DeleteWithWait()

		exutil.By("Step 2: Apply extra-disks MachineConfig")
		err = mc.Create(
			"-p", "NAME="+mcName,
			"-p", "POOL="+MachineConfigPoolWorker,
			"-p", "DEVICE1="+disks[0],
			"-p", "DEVICE2="+disks[1],
		)
		o.Expect(err).NotTo(o.HaveOccurred())
		logger.Infof("MC %s created successfully", mcName)

		err = mcp.WaitForUpdatedStatus()
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Step 3: Verify all worker nodes report irreconcilable changes")
		workerNodes := mcp.GetSortedNodesOrFail()

		for _, node := range workerNodes {
			o.Eventually(node.GetIrreconcilableChanges, "5m", "10s").Should(o.SatisfyAll(
				o.ContainSubstring("spec.config.storage.disks"),
				o.ContainSubstring("spec.config.storage.raid"),
				o.ContainSubstring("spec.config.storage.filesystems"),
			), "Node %s should have irreconcilable changes", node.GetName())
			logger.Infof("Node %s has irreconcilable changes as expected", node.GetName())
		}
		logger.Infof("All worker nodes have irreconcilable changes as expected!\n")
	})

	g.It("[PolarionID:84219] Verify irreconcilable changes on new and existing nodes after scale up [Disruptive]", g.Label("Lifecycle:informing", "Platform:aws", "Platform:gce", "Platform:azure", "Platform:vsphere", "Platform:baremetal"), func() {
		var (
			machineconfiguration = GetMachineConfiguration(oc)
			mcName               = "irreconcilable-scaleup-test"
			initialMcSpecs       = machineconfiguration.GetSpecOrFail()
			machineset           ManagedMachineResource
			bareMetalHost        *Resource
			bareMetalDisks       []string
			hostLabel            string
			removeHostLabel      func() error
			testNode             *Node
			scaleAttempted       bool
		)
		if platform == BaremetalPlatform {
			if !IsCapabilityEnabled(oc, "MachineAPI") {
				g.Skip("MachineAPI capability is disabled; bare-metal workers cannot be scaled")
			}
			candidates, err := GetAllManagedMachineResources(oc, MAPI)
			o.Expect(err).To(o.Succeed(), "Error listing bare-metal MachineSets")
			for _, candidate := range candidates {
				ms := candidate.(*MachineSet)
				data, err := ms.GetCleanJSON()
				o.Expect(err).To(o.Succeed(), "Error reading MachineSet %s", ms)
				o.Expect(gjson.Valid(data)).To(o.BeTrue(), "Invalid MachineSet JSON for %s", ms)
				replicas := gjson.Get(data, "spec.replicas")
				if gjson.Get(data, `spec.template.metadata.labels.machine\.openshift\.io/cluster-api-machine-role`).String() != "worker" ||
					replicas.Type != gjson.Number || replicas.Int() <= 0 ||
					gjson.Get(data, "status.replicas").Int() != replicas.Int() ||
					gjson.Get(data, "status.readyReplicas").Int() != replicas.Int() {
					continue
				}
				bareMetalHost, err = FindAvailableBareMetalHost(oc, candidate, func(data string) error {
					var diskErr error
					bareMetalDisks, diskErr = bareMetalTestDisks(data)
					return diskErr
				})
				o.Expect(err).To(o.Succeed(), "Error checking bare-metal test hosts")
				if bareMetalHost != nil {
					machineset = candidate
					break
				}
			}
			if bareMetalHost == nil {
				g.Skip("No free CI spare worker has verified non-root test disks and a matching host selector")
			}
			defer SafeCleanup(func() {
				if removeHostLabel != nil {
					o.Expect(removeHostLabel()).To(o.Succeed(), "Error removing test label from host %s", bareMetalHost)
				}
			})
		} else {
			SkipTestIfWorkersCannotBeScaled(oc)
		}
		mcp := NewMachineConfigPool(oc, MachineConfigPoolWorker)
		initialNodeCount := len(mcp.GetNodesOrFail())

		defer func() {
			logger.Infof("Restore initial MachineConfiguration spec")
			err := machineconfiguration.SetSpec(initialMcSpecs)
			o.Expect(err).NotTo(o.HaveOccurred())
		}()

		exutil.By("Step 1: Enable irreconcilableValidationOverrides")
		err := machineconfiguration.EnableIrreconcilableValidationOverrides()
		o.Expect(err).NotTo(o.HaveOccurred())
		err = mcp.WaitForUpdatedStatus()
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Step 2: Create duplicate machineset with custom disks")
		if machineset == nil {
			machineset = OrFail[ManagedMachineResource](GetValidManagedMachineResource(oc.AsAdmin()))
		}
		newMSName := machineset.GetName() + "-ms-ic-t2"
		newMS, err := machineset.Duplicate(newMSName)
		o.Expect(err).NotTo(o.HaveOccurred())
		defer SafeCleanup(func() {
			machineSetExists := false
			var lookupErr error
			if newMS != nil {
				lookupErr = Retry(3, 5*time.Second, func() error {
					name, err := newMS.Get(`{.metadata.name}`, "--ignore-not-found=true", "--request-timeout=20s")
					if err != nil {
						return err
					}
					machineSetExists = name == newMS.GetName()
					if name != "" && !machineSetExists {
						return fmt.Errorf("unexpected MachineSet name %q", name)
					}
					return nil
				})
				o.Expect(lookupErr).To(o.Succeed(), "Cannot determine whether test MachineSet %s exists", newMS)
			}
			if machineSetExists {
				if bareMetalHost != nil && scaleAttempted {
					if testNode == nil {
						nodes, nodeErr := newMS.GetNodes()
						if nodeErr == nil && len(nodes) == 1 {
							testNode = nodes[0]
						}
					}
					if testNode == nil {
						o.Expect(testNode).NotTo(o.BeNil(), "Cannot verify test disk cleanup: the bare-metal test node is inaccessible")
					} else {
						identityErr := VerifyBareMetalHostNode(oc, bareMetalHost, newMS, testNode)
						o.Expect(identityErr).To(o.Succeed(), "Cannot verify host identity for disk cleanup")
						if identityErr == nil {
							o.Expect(cleanBareMetalTestDisks(testNode, bareMetalDisks)).To(o.Succeed(), "Error cleaning test disks")
						}
					}
				}
				err := newMS.ScaleTo(0)
				o.Expect(err).NotTo(o.HaveOccurred())
				readyTimeout := "10m"
				if bareMetalHost != nil {
					readyTimeout = "20m"
				}
				err = newMS.WaitUntilReady(readyTimeout)
				o.Expect(err).NotTo(o.HaveOccurred())
				err = newMS.Delete()
				o.Expect(err).NotTo(o.HaveOccurred())
				o.Expect(mcp.WaitForPoolToBeSettled(initialNodeCount)).To(o.Succeed(),
					"MCP counts did not settle after scale-down.\n%s", mcp.PrettyString())
				if bareMetalHost != nil && scaleAttempted {
					o.Eventually(func() (bool, error) {
						data, err := bareMetalHost.GetCleanJSON()
						if err != nil {
							return false, err
						}
						return bareMetalHostAvailable(data), nil
					}, "20m", "30s").Should(o.BeTrue(), "BareMetalHost %s did not become available after scale-down", bareMetalHost)
				}
			} else if lookupErr == nil && bareMetalHost != nil && scaleAttempted {
				o.Expect(machineSetExists).To(o.BeTrue(), "Cannot verify test disk cleanup: the test MachineSet is inaccessible")
			}
		})
		if bareMetalHost != nil {
			hostLabel, removeHostLabel, err = PinBareMetalHost(newMS, bareMetalHost)
			if errors.Is(err, ErrBareMetalHostUnavailable) {
				g.Skip(fmt.Sprintf("Cannot select host: %s", err))
			}
			o.Expect(err).To(o.Succeed(), "Error selecting host %s", bareMetalHost)
		}

		err = platformBasedDisksPatch(platform, newMS)
		o.Expect(err).NotTo(o.HaveOccurred())

		// On AWS, NVMe device enumeration is non-deterministic. Scale up a probe
		// node first to discover the stable /dev/disk/by-path/ entries, then use
		// those paths in the MachineConfig so Ignition can reliably find the disks.
		disks := platformBasedDisksNames(platform)
		if bareMetalHost != nil {
			disks = bareMetalDisks
		}
		var probeNode *Node
		if platform == AWSPlatform {
			exutil.By("Step 2.5: Scale up probe node to discover disk paths")
			o.Expect(newMS.ScaleTo(1)).To(o.Succeed())
			o.Expect(newMS.WaitUntilReady("10m")).To(o.Succeed())

			probeNodes := newMS.GetNodesOrFail()
			o.Expect(probeNodes).To(o.HaveLen(1))
			probeNode = probeNodes[0]
			logger.Infof("Probe node is: %s", probeNode.GetName())

			discoveredDisks := discoverNVMeByPathDisks(probeNode)
			o.Expect(discoveredDisks).To(o.HaveLen(2), "Expected exactly 2 non-boot NVMe disks on probe node %s", probeNode.GetName())
			disks = discoveredDisks
		}

		mc := NewMachineConfig(oc, mcName, MachineConfigPoolWorker).SetMCOTemplate("extra-disks-with-files.yaml")
		if bareMetalHost != nil {
			o.Expect(mc.Exists()).To(o.BeFalse(), "Refusing to replace existing MachineConfig %s", mc)
		}

		defer SafeCleanup(func() {
			if mc.Exists() {
				err := mc.Delete()
				o.Expect(err).NotTo(o.HaveOccurred())
			}
		})

		exutil.By("Step 3: Apply extra-disks-with-files MachineConfig")
		err = mc.Create(
			"-p", "NAME="+mcName,
			"-p", "POOL="+MachineConfigPoolWorker,
			"-p", "DEVICE1="+disks[0],
			"-p", "DEVICE2="+disks[1],
			"-p", "FILE_PATH=/etc/temp_config.conf",
			"-p", "FILE_CONTENT=THIS_IS_A_TEST_FILE_CONF",
		)
		o.Expect(err).NotTo(o.HaveOccurred())
		logger.Infof("MC %s created successfully", mcName)

		err = mcp.WaitForUpdatedStatus()
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Step 4: Verify existing nodes report storage irreconcilable changes")
		workerNodes := mcp.GetSortedNodesOrFail()

		for _, node := range workerNodes {
			o.Eventually(node.GetIrreconcilableChanges, "5m", "10s").Should(o.ContainSubstring("spec.config.storage.disks"),
				"Node %s should have irreconcilable changes for storage.disks", node.GetName())
			logger.Infof("Node %s has irreconcilable changes as expected", node.GetName())
		}
		logger.Infof("All worker nodes report irreconcilable storage changes!\n")

		exutil.By("Step 5: Scale up test node with MC applied via Ignition")
		if bareMetalHost != nil {
			data, err := bareMetalHost.GetCleanJSON()
			o.Expect(err).To(o.Succeed(), "Error rechecking host %s", bareMetalHost)
			verifiedDisks, err := bareMetalTestDisks(data)
			if err != nil || !slices.Equal(verifiedDisks, bareMetalDisks) {
				g.Skip(fmt.Sprintf("Host %s no longer has verified test disks: %v", bareMetalHost, err))
			}
			matches, err := BareMetalHostPinned(data, newMS.GetSpecOrFail(), hostLabel)
			if err != nil || !matches {
				g.Skip(fmt.Sprintf("Host %s no longer matches the test MachineSet selector: %v", bareMetalHost, err))
			}
		}
		if platform == AWSPlatform {
			o.Expect(newMS.ScaleTo(2)).To(o.Succeed())
			o.Expect(newMS.WaitUntilReady("10m")).To(o.Succeed())

			allNodes := newMS.GetNodesOrFail()
			o.Expect(allNodes).To(o.HaveLen(2))
			for _, n := range allNodes {
				if n.GetName() != probeNode.GetName() {
					testNode = n
				}
			}
			o.Expect(testNode).NotTo(o.BeNil(), "Could not identify test node among scaled-up nodes")
		} else {
			if bareMetalHost != nil {
				scaleAttempted = true
			}
			o.Expect(newMS.ScaleTo(1)).To(o.Succeed())
			readyTimeout := "10m"
			if bareMetalHost != nil {
				readyTimeout = "20m"
			}
			o.Expect(newMS.WaitUntilReady(readyTimeout)).To(o.Succeed())

			testNodes := newMS.GetNodesOrFail()
			o.Expect(testNodes).To(o.HaveLen(1))
			testNode = testNodes[0]
		}
		logger.Infof("Test node is: %s", testNode.GetName())
		if bareMetalHost != nil {
			o.Expect(VerifyBareMetalHostNode(oc, bareMetalHost, newMS, testNode)).To(o.Succeed(), "Test node is not on the selected host")
			o.Expect(mcp.WaitForPoolToBeSettled(initialNodeCount+1)).To(o.Succeed(),
				"MCP counts did not settle after scale-up.\n%s", mcp.PrettyString())
		}

		exutil.By("Step 6: Verify test node has no irreconcilable changes")
		o.Eventually(testNode.GetIrreconcilableChanges, "5m", "10s").Should(o.BeEmpty(), "Test node should have no irreconcilable changes")
		logger.Infof("Test node %s has no irreconcilable changes as expected\n", testNode.GetName())

		exutil.By("Step 7: Verify storage configuration on test node")
		_, err = testNode.DebugNodeWithChroot("mount", "/dev/md/data", "/var/lib/data")
		o.Expect(err).NotTo(o.HaveOccurred())

		o.Expect(testNode.DebugNodeWithChroot("mdadm", "--detail", "--scan")).Should(o.ContainSubstring("/dev/md/data"))
		logger.Infof("Storage configuration verified on test node\n")

		exutil.By("Step 8: Delete the irreconcilable MC")
		mc.DeleteWithWait()

		exutil.By("Step 9: Verify existing nodes have irreconcilable changes cleared")
		for _, node := range workerNodes {
			o.Eventually(node.GetIrreconcilableChanges, "5m", "10s").Should(o.BeEmpty(),
				"Node %s should have cleared irreconcilable changes after MC removal", node.GetName())
			logger.Infof("Node %s has cleared irreconcilable changes as expected", node.GetName())
		}
		logger.Infof("All existing worker nodes have cleared irreconcilable changes!\n")

		exutil.By("Step 10: Verify test node now reports irreconcilable changes")
		o.Eventually(testNode.GetIrreconcilableChanges, "5m", "10s").Should(o.SatisfyAll(
			o.ContainSubstring("spec.config.storage.disks"),
			o.ContainSubstring("spec.config.storage.raid"),
			o.ContainSubstring("spec.config.storage.filesystems"),
		), "Test node should report irreconcilable changes after MC removal")
		logger.Infof("Test node %s correctly reports irreconcilable changes after MC removal\n", testNode.GetName())
	})

	g.It("Verify MCS Ignition contents for irreconcilable MachineConfig", g.Label("Platform:aws", "Platform:gce", "Platform:azure", "Platform:vsphere", "Platform:baremetal"), func() {
		var (
			machineconfiguration = GetMachineConfiguration(oc)
			mcName               = "irreconcilable-ignition-test"
			initialMcSpecs       = machineconfiguration.GetSpecOrFail()
			ignitionVersion      = "3.4.0"
			filePath             = "/etc/temp_config.conf"
			fileContent          = "THIS_IS_A_TEST_FILE_CONF"
		)

		mcp := NewMachineConfigPool(oc, MachineConfigPoolWorker)

		defer func() {
			logger.Infof("Restore initial MachineConfiguration spec")
			err := machineconfiguration.SetSpec(initialMcSpecs)
			o.Expect(err).NotTo(o.HaveOccurred())
		}()

		exutil.By("Step 1: Enable irreconcilableValidationOverrides")
		err := machineconfiguration.EnableIrreconcilableValidationOverrides()
		o.Expect(err).NotTo(o.HaveOccurred())
		err = mcp.WaitForUpdatedStatus()
		o.Expect(err).NotTo(o.HaveOccurred())

		disks := platformBasedDisksNames(platform)
		o.Expect(disks).To(o.HaveLen(4), "Expected platform-specific disk names for %s", platform)

		mc := NewMachineConfig(oc, mcName, MachineConfigPoolWorker).SetMCOTemplate("extra-disks-with-files.yaml")
		defer mc.DeleteWithWait()

		exutil.By("Step 2: Apply extra-disks-with-files MachineConfig")
		err = mc.Create(
			"-p", "NAME="+mcName,
			"-p", "POOL="+MachineConfigPoolWorker,
			"-p", "DEVICE1="+disks[0],
			"-p", "DEVICE2="+disks[1],
			"-p", "FILE_PATH="+filePath,
			"-p", "FILE_CONTENT="+fileContent,
		)
		o.Expect(err).NotTo(o.HaveOccurred())
		logger.Infof("MC %s created successfully", mcName)

		err = mcp.WaitForUpdatedStatus()
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Step 3: Verify MCS Ignition contains the expected storage and file configuration")
		diskOnePath := fmt.Sprintf(`storage.disks.#(device==%q)`, disks[0])
		diskTwoPath := fmt.Sprintf(`storage.disks.#(device==%q)`, disks[1])
		renderedFilePath := fmt.Sprintf(`storage.files.#(path==%q)`, filePath)
		o.Eventually(mcp.GetMCSIgnitionConfig, "1m", "20s").WithArguments(true, ignitionVersion).Should(o.SatisfyAll(
			HavePathWithValue("ignition.version", o.Equal(ignitionVersion)),
			HavePathWithValue(diskOnePath+`.partitions.0.label`, o.Equal("raid.1.1")),
			HavePathWithValue(diskTwoPath+`.partitions.0.label`, o.Equal("raid.1.2")),
			HavePathWithValue(`storage.raid.#(name=="data").level`, o.Equal("stripe")),
			HavePathWithValue(`storage.raid.#(name=="data").devices.0`, o.Equal("/dev/disk/by-partlabel/raid.1.1")),
			HavePathWithValue(`storage.raid.#(name=="data").devices.1`, o.Equal("/dev/disk/by-partlabel/raid.1.2")),
			HavePathWithValue(`storage.filesystems.#(path=="/var/lib/data").device`, o.Equal("/dev/md/data")),
			HavePathWithValue(`storage.filesystems.#(path=="/var/lib/data").format`, o.Equal("ext4")),
			HavePathWithValue(`storage.filesystems.#(path=="/var/lib/data").label`, o.Equal("DATA")),
			HavePathWithValue(renderedFilePath+`.contents.source`, o.Equal("data:,"+fileContent)),
			HavePathWithValue(renderedFilePath+`.mode`, o.BeEquivalentTo(0o644)),
			HavePathWithValue(renderedFilePath+`.overwrite`, o.BeTrue()),
		), "Worker Ignition should contain the expected MachineConfig contents")
		logger.Infof("MCS Ignition contains the irreconcilable storage and file configuration")

		exutil.By("Step 4: Delete the irreconcilable MachineConfig")
		mc.Delete()
		err = mcp.WaitForUpdatedStatus()
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Step 5: Verify the removed MachineConfig contents are absent from MCS Ignition")
		o.Eventually(mcp.GetMCSIgnitionConfig, "1m", "20s").WithArguments(true, ignitionVersion).Should(
			o.And(
				HavePathWithValue("ignition.version", o.Equal(ignitionVersion)),
				o.Not(HavePath(diskOnePath)),
				o.Not(HavePath(diskTwoPath)),
				o.Not(HavePath(`storage.raid.#(name=="data")`)),
				o.Not(HavePath(`storage.filesystems.#(path=="/var/lib/data")`)),
				o.Not(HavePath(renderedFilePath)),
			),
			"Removed MachineConfig contents should be absent from valid worker Ignition version %s", ignitionVersion)
		logger.Infof("MCS Ignition no longer contains the removed MachineConfig contents\n")
	})

	g.It("Irreconcilable changes persist after feature has been disabled. New irreconcilable configs will be rejected", g.Label("Platform:aws", "Platform:gce", "Platform:azure", "Platform:vsphere", "Platform:baremetal"), func() {
		var (
			machineconfiguration = GetMachineConfiguration(oc)
			mcName               = "irreconcilable-persist-test"
			initialMcSpecs       = machineconfiguration.GetSpecOrFail()
		)

		mcp := NewMachineConfigPool(oc, MachineConfigPoolWorker)

		defer func() {
			logger.Infof("Restore initial MachineConfiguration spec")
			err := machineconfiguration.SetSpec(initialMcSpecs)
			o.Expect(err).NotTo(o.HaveOccurred())
		}()

		exutil.By("Step 1: Enable irreconcilableValidationOverrides")
		err := machineconfiguration.EnableIrreconcilableValidationOverrides()
		o.Expect(err).NotTo(o.HaveOccurred())
		err = mcp.WaitForUpdatedStatus()
		o.Expect(err).NotTo(o.HaveOccurred())

		mc := NewMachineConfig(oc, mcName, MachineConfigPoolWorker).SetMCOTemplate("extra-disks.yaml")
		disks := platformBasedDisksNames(platform)

		defer func() {
			// Re-enable the override before deleting mc: without it, SetSpec (which replaces
			// the entire /spec via op:add) triggers a full MCO reconciliation that detects the
			// irreconcilable disk changes in the current rendered config → RenderDegraded.
			err := machineconfiguration.EnableIrreconcilableValidationOverrides()
			o.Expect(err).NotTo(o.HaveOccurred())
			mc.DeleteWithWait()
		}()

		exutil.By("Step 2: Apply extra-disks MachineConfig")
		err = mc.Create(
			"-p", "NAME="+mcName,
			"-p", "POOL="+MachineConfigPoolWorker,
			"-p", "DEVICE1="+disks[0],
			"-p", "DEVICE2="+disks[1],
		)
		o.Expect(err).NotTo(o.HaveOccurred())
		logger.Infof("MC %s created successfully", mcName)

		err = mcp.WaitForUpdatedStatus()
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Step 3: Verify nodes report irreconcilable changes")
		workerNodes := mcp.GetSortedNodesOrFail()

		for _, node := range workerNodes {
			o.Eventually(node.GetIrreconcilableChanges, "5m", "10s").Should(o.SatisfyAll(
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
			o.Eventually(node.GetIrreconcilableChanges, "5m", "10s").Should(o.SatisfyAll(
				o.ContainSubstring("spec.config.storage.disks"),
				o.ContainSubstring("spec.config.storage.raid"),
				o.ContainSubstring("spec.config.storage.filesystems"),
			), "Node %s should still have irreconcilable changes after disabling", node.GetName())
			logger.Infof("Node %s irreconcilable changes persisted as expected", node.GetName())
		}
		logger.Infof("All worker nodes still have irreconcilable changes!\n")

		exutil.By("Step 6: Apply new irreconcilable MC and verify rejection")
		newMcName := mcName + "-2"
		mc2 := NewMachineConfig(oc, newMcName, MachineConfigPoolWorker).SetMCOTemplate("extra-disks.yaml")

		defer func() {
			mc2.DeleteWithWait()
			o.Expect(mcp.WaitForNotDegradedStatus()).To(o.Succeed())
		}()

		err = mc2.Create(
			"-p", "NAME="+newMcName,
			"-p", "POOL="+MachineConfigPoolWorker,
			"-p", "DEVICE1="+disks[2],
			"-p", "DEVICE2="+disks[3],
		)
		o.Expect(err).NotTo(o.HaveOccurred())

		o.Eventually(mcp, "5m", "10s").Should(o.SatisfyAll(
			HaveConditionField("RenderDegraded", "status", o.Equal("True")),
			HaveConditionField("RenderDegraded", "message", o.ContainSubstring("ignition disks section contains changes")),
		), "MCP should be RenderDegraded after applying irreconcilable MC without override")
	})

	g.It("Irreconcilable changes cleared after reverting MC", g.Label("Platform:aws", "Platform:gce", "Platform:azure", "Platform:vsphere", "Platform:baremetal"), func() {
		var (
			machineconfiguration = GetMachineConfiguration(oc)
			mcName               = "irreconcilable-clear-test"
			initialMcSpecs       = machineconfiguration.GetSpecOrFail()
		)

		mcp := NewMachineConfigPool(oc, MachineConfigPoolWorker)

		defer func() {
			logger.Infof("Restore initial MachineConfiguration spec")
			err := machineconfiguration.SetSpec(initialMcSpecs)
			o.Expect(err).NotTo(o.HaveOccurred())
		}()

		exutil.By("Step 1: Enable irreconcilableValidationOverrides and apply MC")
		err := machineconfiguration.EnableIrreconcilableValidationOverrides()
		o.Expect(err).NotTo(o.HaveOccurred())
		err = mcp.WaitForUpdatedStatus()
		o.Expect(err).NotTo(o.HaveOccurred())

		mc := NewMachineConfig(oc, mcName, MachineConfigPoolWorker).SetMCOTemplate("extra-disks.yaml")
		disks := platformBasedDisksNames(platform)

		defer mc.DeleteWithWait()

		err = mc.Create(
			"-p", "NAME="+mcName,
			"-p", "POOL="+MachineConfigPoolWorker,
			"-p", "DEVICE1="+disks[0],
			"-p", "DEVICE2="+disks[1],
		)
		o.Expect(err).NotTo(o.HaveOccurred())
		logger.Infof("MC %s created successfully", mcName)

		err = mcp.WaitForUpdatedStatus()
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Step 2: Verify nodes report irreconcilable changes")
		workerNodes := mcp.GetSortedNodesOrFail()

		for _, node := range workerNodes {
			o.Eventually(node.GetIrreconcilableChanges, "5m", "10s").Should(o.SatisfyAll(
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
			o.Eventually(node.GetIrreconcilableChanges, "5m", "10s").Should(o.BeEmpty(),
				"Node %s should have cleared irreconcilable changes after MC removal", node.GetName())
			logger.Infof("Node %s has cleared irreconcilable changes as expected", node.GetName())
		}
		logger.Infof("All worker nodes have cleared irreconcilable changes!\n")
	})

	g.It("Irreconcilable MC rejected without override enabled", g.Label("Platform:aws", "Platform:gce", "Platform:azure", "Platform:vsphere", "Platform:baremetal"), func() {
		var (
			mcName = "irreconcilable-reject-test"
		)

		mcp := NewMachineConfigPool(oc, MachineConfigPoolWorker)
		mc := NewMachineConfig(oc, mcName, MachineConfigPoolWorker).SetMCOTemplate("extra-disks.yaml")
		disks := platformBasedDisksNames(platform)

		defer mc.DeleteWithWait()

		exutil.By("Step 1: Apply irreconcilable MC without enabling override")
		err := mc.Create(
			"-p", "NAME="+mcName,
			"-p", "POOL="+MachineConfigPoolWorker,
			"-p", "DEVICE1="+disks[0],
			"-p", "DEVICE2="+disks[1],
		)
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Step 2: Verify MCP becomes RenderDegraded")
		o.Eventually(mcp, "5m", "10s").Should(o.SatisfyAll(
			HaveConditionField("RenderDegraded", "status", o.Equal("True")),
			HaveConditionField("RenderDegraded", "message", o.ContainSubstring("ignition disks section contains changes")),
		), "MCP should be RenderDegraded after applying irreconcilable MC without override")

		exutil.By("Step 3: Delete MC and verify pool recovers")
		mc.DeleteWithWait()
		o.Expect(mcp.WaitForNotDegradedStatus()).To(o.Succeed())
	})
})
