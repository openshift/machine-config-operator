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
			testID              = GetCurrentTestPolarionIDNumber()
			fakeRpmOstreePath   = "/var/tmp/rpm-ostree-fake.sh"
			expectedNDMessage   = "no staged deployment found after applying extensions"
			fakeRpmOstreeScript = `printf '#!/bin/bash\nif [[ "$@" == *"install"* ]] || [[ "$@" == *"override"* ]]; then\n  echo "Installing package (fake)"\n  exit 0\nfi\nexec /var/tmp/rpm-ostree "$@"\n' > ` + fakeRpmOstreePath + ` && ` +
				`chmod +x ` + fakeRpmOstreePath
		)

		exutil.By("Get a pool for testing")
		mcp, cleanup, err := GetCompactCompatibleOrCustomPool(oc.AsAdmin(), 1)
		defer cleanup()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting pool for testing")
		node := mcp.GetSortedNodesOrFail()[0]
		logger.Infof("MCP: %s, node: %s", mcp.GetName(), node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Create fake rpm-ostree script that silently succeeds on install/override")
		out, err := node.DebugNodeWithChroot("bash", "-c", fakeRpmOstreeScript)
		o.Expect(err).NotTo(o.HaveOccurred(), "Failed to create fake rpm-ostree script on node %s: %s", node.GetName(), out)

		exutil.By("Replace rpm-ostree with fake script")
		o.Expect(ReplaceRpmOstree(node, fakeRpmOstreePath)).To(o.Succeed(),
			"Failed to replace rpm-ostree on node %s", node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Apply MachineConfig with usbguard extension")
		mc := NewMachineConfig(oc.AsAdmin(), fmt.Sprintf("test-%s-ext", testID), mcp.GetName()).
			SetMCOTemplate("change-worker-extension-usbguard.yaml")
		mc.skipWaitForMcp = true
		defer func() {
			exutil.By("Restore rpm-ostree, delete MC, and recover MCP")
			RestoreRpmOstree(node)
			node.DebugNodeWithChroot("sh", "-c", "rm -f "+fakeRpmOstreePath)
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
			fakeRpmPath       = "/var/tmp/rpm-fake.sh"
			expectedNDMessage = "extension package verification failed"
			fakeRpmScript     = `printf '#!/bin/bash\nif [ "$1" = "-q" ] && [ "$2" = "usbguard" ]; then\n  exit 1\nfi\n/var/tmp/rpm-real "$@"\n' > ` + fakeRpmPath + ` && ` +
				`chmod +x ` + fakeRpmPath
		)

		exutil.By("Get a pool for testing")
		mcp, cleanup, err := GetCompactCompatibleOrCustomPool(oc.AsAdmin(), 1)
		defer cleanup()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting pool for testing")
		node := mcp.GetSortedNodesOrFail()[0]
		logger.Infof("MCP: %s, node: %s", mcp.GetName(), node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Create fake rpm script that reports usbguard as not installed")
		out, err := node.DebugNodeWithChroot("bash", "-c", fakeRpmScript)
		o.Expect(err).NotTo(o.HaveOccurred(), "Failed to create fake rpm script on node %s: %s", node.GetName(), out)

		exutil.By("Replace rpm with fake script")
		o.Expect(ReplaceRpm(node, fakeRpmPath)).To(o.Succeed(),
			"Failed to replace rpm on node %s", node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Apply MachineConfig with usbguard extension")
		mc := NewMachineConfig(oc.AsAdmin(), fmt.Sprintf("test-%s-ext", testID), mcp.GetName()).
			SetMCOTemplate("change-worker-extension-usbguard.yaml")
		mc.skipWaitForMcp = true
		defer func() {
			exutil.By("Restore rpm, delete MC, and recover MCP")
			RestoreRpm(node)
			node.DebugNodeWithChroot("sh", "-c", "rm -f "+fakeRpmPath)
			o.Eventually(mc.Delete).Should(o.Succeed(), "Could not delete the extension MC")
			o.Expect(mcp.RecoverFromDegraded()).To(o.Succeed(), "The MCP could not be recovered from Degraded status")
		}()
		mc.create()
		logger.Infof("OK!\n")

		exutil.By("Wait for node to reboot and MCP to finish updating")
		o.Expect(mcp.WaitForUpdatedStatus()).To(o.Succeed(),
			"The MCP should complete the update after installing usbguard extension")
		logger.Infof("OK!\n")

		exutil.By("Re-apply fake rpm after reboot (bind mount is lost on reboot)")
		o.Expect(ReplaceRpm(node, fakeRpmPath)).To(o.Succeed(),
			"Failed to re-apply fake rpm on node %s", node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Restart MCD pod on the node to pick up fake rpm")
		mcdPod := node.GetMachineConfigDaemon()
		err = NewNamespacedResource(oc.AsAdmin(), "pod", MachineConfigNamespace, mcdPod).Delete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Failed to delete MCD pod %s", mcdPod)
		logger.Infof("Deleted MCD pod %s to trigger re-sync", mcdPod)
		logger.Infof("OK!\n")

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
