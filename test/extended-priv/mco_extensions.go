package extended

import (
	"fmt"
	"regexp"
	"strings"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO extensions", func() {
	defer g.GinkgoRecover()

	var oc = exutil.NewCLI("mco-extensions", exutil.KubeConfigPath())

	g.JustBeforeEach(func() {
		PreChecks(oc)
	})

	g.It("[PolarionID:88729] Verify USBGuard extension can be installed and enabled via MachineConfig on worker nodes [Disruptive]", func() {
		testID := GetCurrentTestPolarionIDNumber()
		mcp := GetCompactCompatiblePool(oc.AsAdmin())

		exutil.By("Create a MachineConfig to install the usbguard extension on worker nodes")
		mcExt := NewMachineConfig(oc.AsAdmin(), fmt.Sprintf("test-%s-ext", testID), mcp.GetName()).
			SetMCOTemplate("change-worker-extension-usbguard.yaml")
		defer mcExt.DeleteWithWait()
		mcExt.create()
		logger.Infof("OK!\n")

		exutil.By("Create a MachineConfig to enable the usbguard systemd unit on worker nodes")
		mcEnable := NewMachineConfig(oc.AsAdmin(), fmt.Sprintf("test-%s-enable", testID), mcp.GetName())
		mcEnable.SetParams(fmt.Sprintf(`UNITS=[{"enabled": true, "name": "usbguard.service"}]`))
		defer mcEnable.Delete()
		mcEnable.create()
		logger.Infof("OK!\n")

		exutil.By("Verify usbguard extension is installed on all worker nodes")
		nodes := mcp.GetSortedNodesOrFail()
		for _, node := range nodes {
			o.Expect(node.RpmIsInstalled("usbguard")).To(o.BeTrue(),
				"usbguard rpm should be installed on node %s", node.GetName())
		}
		logger.Infof("OK!\n")

		exutil.By("Verify usbguard.service is enabled on all worker nodes")
		for _, node := range nodes {
			o.Expect(node.IsUnitEnabled("usbguard")).To(o.BeTrue(),
				"usbguard.service should be enabled on node %s", node.GetName())
		}
		logger.Infof("OK!\n")
	})

	/* Map of extensions and packages for each extension
	{
		"ipsec":                {"NetworkManager-libreswan", "libreswan"},
		"usbguard":             {"usbguard"},
		"kerberos":             {"krb5-workstation", "libkadm5"},
		"kernel-devel":         {"kernel-devel", "kernel-headers"},
		"sandboxed-containers": {"kata-containers"},
		"sysstat":              {"sysstat"},
	} */
	g.It("[PolarionID:56131][PolarionID:77354][OTP][LEVEL0] Install all extensions", func() {
		var (
			coreOSMcp = GetCoreOsCompatiblePool(oc.AsAdmin())
			node      = coreOSMcp.GetCoreOsNodesOrFail()[0]

			query         = `mcd_local_unsupported_packages{node="` + node.GetName() + `"}`
			valueJSONPath = `data.result.0.value.1`

			mcName = fmt.Sprintf("mco-tc-%s-all-extensions", GetCurrentTestPolarionIDNumber())

			applicableExtensions, expectedRpmInstalledPackages = GetAllApplicableExtensionsToMCPOrFail(coreOSMcp)

			skipDrainChecks         = IsSNO(oc.AsAdmin()) // SNO clusters should NOT drain the nodes before rebooting them. The validator is not prepared for that.
			behaviourValidatorApply = UpdateBehaviourValidator{
				SkipDrainNodesValidation: skipDrainChecks,
				Checkers: []Checker{
					CommandOutputChecker{
						Command:  append([]string{"rpm", "-q"}, expectedRpmInstalledPackages...),
						Matcher:  o.MatchRegexp("(?s)" + strings.Join(expectedRpmInstalledPackages, ".*")),
						ErrorMsg: "Extensions were not properly installed",
						Desc:     "Checking that all available extensions were properly installed",
					},
				},
			}
		)

		coreOSMcp.SetWaitingTimeForExtensionsChange()
		behaviourValidatorApply.Initialize(coreOSMcp, nil)

		exutil.By("Create a MC to install all available extensions")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, coreOSMcp.GetName())
		mc.parameters = []string{fmt.Sprintf(`EXTENSIONS=%s`, string(MarshalOrFail(applicableExtensions)))}
		mc.skipWaitForMcp = true

		defer mc.DeleteWithWait()
		mc.create()
		logger.Infof("OK!\n")

		behaviourValidatorApply.Validate()

		exutil.By("Check that no unsupported packages are reported")
		monitor, err := exutil.NewMonitor(oc.AsAdmin())
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the monitor to query the metricts")

		o.Eventually(monitor.SimpleQuery, "10s", "2s").WithArguments(query).Should(HavePathWithValue(valueJSONPath, o.Equal("0")),
			"There are reported unsupported packages in %s", node)
		logger.Infof("OK!\n")

		CheckExtensions(node, applicableExtensions)

		exutil.By("Delete the MC")
		mc.DeleteWithWait()
		logger.Infof("OK!\n")

		exutil.By("Verify that extension packages where uninstalled after MC deletion")
		for _, pkg := range expectedRpmInstalledPackages {
			o.Expect(node.RpmIsInstalled(pkg)).To(
				o.BeFalse(),
				"Package %s should be uninstalled when we remove the extensions MC", pkg)
		}
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:56123][OTP] Invalid extensions should degrade the machine config pool", func() {
		var (
			validExtension   = "usbguard"
			invalidExtension = "zsh"
			mcName           = "mco-tc-56123-invalid-extension"
			mcp              = GetCompactCompatiblePool(oc)

			expectedRDMessage = regexp.QuoteMeta(fmt.Sprintf("invalid extensions found: [%s]", invalidExtension)) // quotemeta to scape regex characters
			expectedRDReason  = ""
		)

		exutil.By("Create a MC with invalid extensions")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName())
		mc.parameters = []string{fmt.Sprintf(`EXTENSIONS=["%s", "%s"]`, validExtension, invalidExtension)}
		mc.skipWaitForMcp = true

		validateMcpRenderDegraded(mc, mcp, expectedRDMessage, expectedRDReason)
	})

	g.It("[PolarionID:89090][OTP] verifyExtensionsStaged detects missing staged deployment after applying extensions [Disruptive]", func() {
		var (
			testID            = GetCurrentTestPolarionIDNumber()
			expectedNDMessage = "no staged deployment found after applying extensions"
		)

		exutil.By("Get a pool for testing")
		mcp, cleanup, err := GetCompactCompatibleOrCustomPool(oc.AsAdmin(), 1)
		defer cleanup()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting pool for testing")
		node := mcp.GetSortedNodesOrFail()[0]
		logger.Infof("MCP: %s, node: %s", mcp.GetName(), node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Replace rpm-ostree with fake script that silently succeeds on install/override")
		o.Expect(ReplaceRpmOstree(node, generateTemplateAbsolutePath("rpm-ostree-fake-install-noop.sh"))).To(o.Succeed(),
			"Failed to replace rpm-ostree on node %s", node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Apply MachineConfig with usbguard extension")
		mc := NewMachineConfig(oc.AsAdmin(), fmt.Sprintf("test-%s-ext", testID), mcp.GetName()).
			SetMCOTemplate("change-worker-extension-usbguard.yaml")
		mc.skipWaitForMcp = true
		defer func() {
			exutil.By("Restore rpm-ostree, delete MC, and recover MCP")
			o.Expect(RestoreRpmOstree(node)).To(o.Succeed(), "Failed to restore rpm-ostree on node %s", node.GetName())
			o.Eventually(mc.Delete).Should(o.Succeed(), "Could not delete the extension MC")
			o.Expect(mcp.RecoverFromDegraded()).To(o.Succeed(), "The MCP could not be recovered from Degraded status")
		}()
		mc.create()
		logger.Infof("OK!\n")

		exutil.By("Restart MCD pod on the node to pick up fake rpm-ostree")
		mcdPod := node.GetMachineConfigDaemon()
		err = NewNamespacedResource(oc.AsAdmin(), "pod", MachineConfigNamespace, mcdPod).Delete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Failed to delete MCD pod %s", mcdPod)
		logger.Infof("Deleted MCD pod %s to trigger re-sync with fake rpm-ostree", mcdPod)
		logger.Infof("OK!\n")

		exutil.By("Wait for MCP to degrade with missing staged deployment error")
		o.Eventually(mcp, mcp.estimateWaitDuration().String(), "30s").Should(BeDegraded(),
			"The '%s' MCP should become degraded when no staged deployment is found after applying extensions", mcp.GetName())
		o.Expect(mcp).To(HaveNodeDegradedMessage(o.ContainSubstring(expectedNDMessage)),
			"The '%s' MCP should report the staged deployment error in the NodeDegraded condition", mcp.GetName())
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:89095][OTP] verifyExtensionPackages detects extension missing from RPM database after reboot [Disruptive]", func() {
		var (
			testID            = GetCurrentTestPolarionIDNumber()
			expectedNDMessage = "extension package verification failed"
			fakeRpmLocalPath  = generateTemplateAbsolutePath("rpm-fake-usbguard-missing.sh")
		)

		exutil.By("Get a pool for testing")
		mcp, cleanup, err := GetCompactCompatibleOrCustomPool(oc.AsAdmin(), 1)
		defer cleanup()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting pool for testing")
		node := mcp.GetSortedNodesOrFail()[0]
		logger.Infof("MCP: %s, node: %s", mcp.GetName(), node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Apply MachineConfig with usbguard extension")
		mc := NewMachineConfig(oc.AsAdmin(), fmt.Sprintf("test-%s-ext", testID), mcp.GetName()).
			SetMCOTemplate("change-worker-extension-usbguard.yaml")
		mc.skipWaitForMcp = true
		defer func() {
			exutil.By("Restore rpm, delete MC, and recover MCP")
			o.Expect(RestoreRpm(node)).To(o.Succeed(), "Failed to restore rpm on node %s", node.GetName())
			o.Eventually(mc.Delete).Should(o.Succeed(), "Could not delete the extension MC")
			o.Expect(mcp.RecoverFromDegraded()).To(o.Succeed(), "The MCP could not be recovered from Degraded status")
		}()
		mc.create()
		logger.Infof("OK!\n")

		exutil.By("Wait for MCP to start updating")
		o.Expect(mcp.WaitForUpdatingStatus()).To(o.Succeed(),
			"The MCP should start updating after applying usbguard extension")

		exutil.By("Wait for node to reboot and MCP to finish updating")
		o.Expect(mcp.WaitForUpdatedStatus()).To(o.Succeed(),
			"The MCP should complete the update after installing usbguard extension")
		logger.Infof("OK!\n")

		exutil.By("Re-apply fake rpm after reboot (bind mount is lost on reboot)")
		o.Expect(ReplaceRpm(node, fakeRpmLocalPath)).To(o.Succeed(),
			"Failed to re-apply fake rpm on node %s", node.GetName())
		logger.Infof("OK!\n")

		exutil.By("DEBUG: Verify bind mount is visible from host mount namespace")
		debugOut, debugErr := node.DebugNodeWithChroot("sh", "-c",
			"echo '--- mount info for /usr/bin/rpm ---' && "+
				"nsenter --mount=/proc/1/ns/mnt stat /usr/bin/rpm && "+
				"echo '--- file type ---' && "+
				"nsenter --mount=/proc/1/ns/mnt file /usr/bin/rpm && "+
				"echo '--- head of rpm ---' && "+
				"nsenter --mount=/proc/1/ns/mnt head -2 /usr/bin/rpm && "+
				"echo '--- mount points containing rpm ---' && "+
				"nsenter --mount=/proc/1/ns/mnt cat /proc/self/mountinfo | grep rpm && "+
				"echo '--- composefs check ---' && "+
				"nsenter --mount=/proc/1/ns/mnt cat /proc/self/mountinfo | grep -E 'composefs|erofs|overlay' | head -5 && "+
				"echo '--- rpm -q usbguard from host ns ---' && "+
				"nsenter --mount=/proc/1/ns/mnt /usr/bin/rpm -q usbguard; echo \"exit code: $?\"")
		logger.Infof("DEBUG bind mount check output:\n%s", debugOut)
		if debugErr != nil {
			logger.Infof("DEBUG bind mount check error: %v", debugErr)
		}

		exutil.By("Restart MCD pod on the node to pick up fake rpm")
		mcdPod := node.GetMachineConfigDaemon()
		err = NewNamespacedResource(oc.AsAdmin(), "pod", MachineConfigNamespace, mcdPod).Delete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Failed to delete MCD pod %s", mcdPod)
		logger.Infof("Deleted MCD pod %s to trigger re-sync", mcdPod)
		logger.Infof("OK!\n")

		exutil.By("DEBUG: Check what host mount namespace sees after MCD restart")
		o.Eventually(func() string {
			return node.GetMachineConfigDaemon()
		}, "2m", "10s").ShouldNot(o.Equal(mcdPod), "New MCD pod should be created")
		newMcdPod := node.GetMachineConfigDaemon()
		logger.Infof("DEBUG: New MCD pod: %s", newMcdPod)
		mcdExecOut, mcdExecErr := node.DebugNodeWithChroot("sh", "-c",
			"echo '--- /usr/bin/rpm from host ns after MCD restart ---' && "+
				"nsenter --mount=/proc/1/ns/mnt file /usr/bin/rpm && "+
				"nsenter --mount=/proc/1/ns/mnt head -2 /usr/bin/rpm && "+
				"echo '--- mount points for rpm after restart ---' && "+
				"nsenter --mount=/proc/1/ns/mnt cat /proc/self/mountinfo | grep rpm && "+
				"echo '--- rpm -q usbguard after restart ---' && "+
				"nsenter --mount=/proc/1/ns/mnt /usr/bin/rpm -q usbguard; echo \"exit code: $?\" && "+
				"echo '--- RHCOS version ---' && "+
				"nsenter --mount=/proc/1/ns/mnt cat /etc/os-release | grep -E 'PRETTY_NAME|VERSION_ID'")
		logger.Infof("DEBUG MCD view after restart:\n%s", mcdExecOut)
		if mcdExecErr != nil {
			logger.Infof("DEBUG MCD view error: %v", mcdExecErr)
		}

		exutil.By("Wait for MCP to degrade with extension verification error")
		o.Eventually(mcp, mcp.estimateWaitDuration().String(), "30s").Should(BeDegraded(),
			"The '%s' MCP should become degraded when extension packages are missing from the RPM database", mcp.GetName())
		o.Expect(mcp).To(HaveNodeDegradedMessage(o.ContainSubstring(expectedNDMessage)),
			"The '%s' MCP should report the extension verification error in the NodeDegraded condition", mcp.GetName())
		logger.Infof("OK!\n")

		exutil.By("Check MCD logs for extension verification error")
		o.Eventually(node.GetMCDaemonLogs, "2m", "10s").WithArguments("").Should(
			o.ContainSubstring(expectedNDMessage),
			"MCD logs should contain the extension verification error")
		logger.Infof("OK!\n")
	})
})
