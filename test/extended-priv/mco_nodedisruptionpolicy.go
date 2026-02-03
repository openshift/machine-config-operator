package extended

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO NodeDisruptionPolicy", func() {

	defer g.GinkgoRecover()

	const (
		LogPrefix                           = `Performing post config change action: `
		LogPerformingPostConfigNone         = LogPrefix + "None"
		LogPerformingPostConfigReload       = LogPrefix + "Reload"
		LogPerformingPostConfigRestart      = LogPrefix + "Restart"
		LogPerformingPostConfigDaemonReload = LogPrefix + "DaemonReload"
		LogTemplateForUnitAction            = `%s service %s successfully`
	)

	var (
		oc                              = exutil.NewCLI("mco-nodedisruptionpolicy", exutil.KubeConfigPath())
		TestService                     = "crio.service"
		LogServiceReloadedSuccessfully  = fmt.Sprintf(LogTemplateForUnitAction, TestService, "reloaded")
		LogServiceRestartedSuccessfully = fmt.Sprintf(LogTemplateForUnitAction, TestService, "restarted")
		LogDaemonReloadedSuccessfully   = fmt.Sprintf(LogTemplateForUnitAction, "daemon-reload", "reloaded")
	)

	g.JustBeforeEach(func() {
		PreChecks(oc)
	})

	g.It("[PolarionID:73368][OTP] NodeDisruptionPolicy files with action None", func() {
		testFileBasedPolicy(oc, "73368", []Action{NewCommonAction(NodeDisruptionPolicyActionNone)}, []string{LogPerformingPostConfigNone})
	})

	g.It("[PolarionID:73374][OTP] NodeDisruptionPolicy files with action Reboot", func() {
		testFileBasedPolicy(oc, "73374", []Action{NewCommonAction(NodeDisruptionPolicyActionReboot)}, []string{})
	})

	g.It("[PolarionID:73375][OTP] NodeDisruptionPolicy files with action Restart", func() {
		testFileBasedPolicy(oc, "73375", []Action{NewRestartAction(TestService)}, []string{LogPerformingPostConfigRestart, LogServiceRestartedSuccessfully})
	})

	g.It("[PolarionID:73378][OTP] NodeDisruptionPolicy files with action Reload", func() {
		testFileBasedPolicy(oc, "73378", []Action{NewReloadAction(TestService)}, []string{LogPerformingPostConfigReload, LogServiceReloadedSuccessfully})
	})

	g.It("[PolarionID:73385][OTP] NodeDisruptionPolicy files with action DaemonReload", func() {
		testFileBasedPolicy(oc, "73385", []Action{NewCommonAction(NodeDisruptionPolicyActionDaemonReload)}, []string{LogPerformingPostConfigDaemonReload, LogDaemonReloadedSuccessfully})
	})

	g.It("[PolarionID:73388][OTP] NodeDisruptionPolicy files with action Drain", func() {
		testFileBasedPolicy(oc, "73388", []Action{NewCommonAction(NodeDisruptionPolicyActionDrain)}, []string{})
	})

	g.It("[PolarionID:73389][OTP] NodeDisruptionPolicy files with multiple actions", func() {
		testFileBasedPolicy(oc, "73389", []Action{
			NewCommonAction(NodeDisruptionPolicyActionDrain),
			NewCommonAction(NodeDisruptionPolicyActionDaemonReload),
			NewReloadAction(TestService),
			NewRestartAction(TestService),
		}, []string{
			LogPerformingPostConfigReload,
			LogServiceReloadedSuccessfully,
			LogPerformingPostConfigRestart,
			LogServiceRestartedSuccessfully,
			LogPerformingPostConfigDaemonReload,
			LogDaemonReloadedSuccessfully,
		})
	})

	g.It("[PolarionID:73414][OTP] NodeDisruptionPolicy units with action None", func() {
		testUnitBasedPolicy(oc, "73414", []Action{NewCommonAction(NodeDisruptionPolicyActionNone)}, []string{LogPerformingPostConfigNone})
	})

	g.It("[PolarionID:73413][OTP] NodeDisruptionPolicy units with action Reboot", func() {
		testUnitBasedPolicy(oc, "73413", []Action{NewCommonAction(NodeDisruptionPolicyActionReboot)}, []string{})
	})

	g.It("[PolarionID:73411][OTP] NodeDisruptionPolicy units with multiple actions", func() {
		testUnitBasedPolicy(oc, "73411", []Action{
			NewCommonAction(NodeDisruptionPolicyActionDrain),
			NewCommonAction(NodeDisruptionPolicyActionDaemonReload),
			NewReloadAction(TestService),
			NewRestartAction(TestService),
		}, []string{
			LogPerformingPostConfigReload,
			LogServiceReloadedSuccessfully,
			LogPerformingPostConfigRestart,
			LogServiceRestartedSuccessfully,
			LogPerformingPostConfigDaemonReload,
			LogDaemonReloadedSuccessfully,
		})
	})

	g.It("[PolarionID:73417][OTP] NodeDisruptionPolicy sshkey with action None", func() {
		testSSHKeyBasedPolicy(oc, "73417", []Action{NewCommonAction(NodeDisruptionPolicyActionNone)}, []string{LogPerformingPostConfigNone})
	})

	g.It("[PolarionID:73418][OTP] NodeDisruptionPolicy sshkey with action Reboot", func() {
		testSSHKeyBasedPolicy(oc, "73418", []Action{NewCommonAction(NodeDisruptionPolicyActionReboot)}, []string{})
	})

	g.It("[PolarionID:73415][OTP] NodeDisruptionPolicy sshkey with multiple actions", func() {
		testSSHKeyBasedPolicy(oc, "73415", []Action{
			NewCommonAction(NodeDisruptionPolicyActionDrain),
			NewCommonAction(NodeDisruptionPolicyActionDaemonReload),
			NewReloadAction(TestService),
			NewRestartAction(TestService),
		}, []string{
			LogPerformingPostConfigReload,
			LogServiceReloadedSuccessfully,
			LogPerformingPostConfigRestart,
			LogServiceRestartedSuccessfully,
			LogPerformingPostConfigDaemonReload,
			LogDaemonReloadedSuccessfully,
		})
	})

	g.It("[PolarionID:73489][OTP] NodeDisruptionPolicy MachineConfigurations is only effective with name cluster", func() {

		ndp := NewNodeDisruptionPolicy(oc)
		var (
			filePath    = generateTempFilePath(e2e.TestContext.OutputDir, "invalidmc-*")
			fileContent = strings.ReplaceAll(exutil.OrFail[string](ndp.GetCleanJSON()), "cluster", "iminvalid")
		)

		exutil.By("Create machineconfiguration.operator.openshift.io with invalid name")
		o.Expect(os.WriteFile(filePath, []byte(fileContent), 0o644)).NotTo(o.HaveOccurred(), "create invalid MC file failed")
		defer os.Remove(filePath)
		output, ocerr := oc.AsAdmin().Run("apply").Args("-f", filePath).Output()

		exutil.By("Check whether oc command is failed")
		o.Expect(ocerr).To(o.HaveOccurred(), "Expected oc command error not found")
		o.Expect(output).Should(o.ContainSubstring("Only a single object of MachineConfiguration is allowed and it must be named cluster"))

	})

	g.It("[PolarionID:75109][OTP] NodeDisruptionPolicy files allow paths to be defined for non-disruptive updates", func() {
		var (
			mcp  = GetCompactCompatiblePool(oc.AsAdmin())
			node = mcp.GetSortedNodesOrFail()[0]
			ndp  = NewNodeDisruptionPolicy(oc)

			innerDirPath         = "/etc/test-file-policy-subdir-75109/extradir/"
			innerDirFilePath     = innerDirPath + "test-file-inner.txt"
			innerDirFileConfig   = getURLEncodedFileConfig(innerDirFilePath, "test-75109.txt", "420")
			innerDirActions      = []Action{NewCommonAction(NodeDisruptionPolicyActionNone)}
			innerDirExpectedLogs = []string{LogPerformingPostConfigNone}
			innderDirMcName      = "test-75109-inner-dir-files"

			outerDirPath         = "/etc/test-file-policy-subdir-75109"
			outerDirFilePath     = outerDirPath + "/test-file-outer.txt"
			outerDirFileConfig   = getURLEncodedFileConfig(outerDirFilePath, "test-75109.txt", "420")
			outerDirActions      = []Action{NewRestartAction(TestService)}
			outerDirExpectedLogs = []string{LogPerformingPostConfigRestart, LogServiceRestartedSuccessfully}
			outerDirMcName       = "test-75109-outer-dir-files"

			filePath         = "/etc/test-file-policy-subdir-75109/test-file.txt"
			fileConfig       = getURLEncodedFileConfig(filePath, "test-75109.txt", "420")
			fileActions      = []Action{NewReloadAction(TestService)}
			fileExpectedLogs = []string{LogPerformingPostConfigReload, LogServiceReloadedSuccessfully}
			fileMcName       = "test-75109-files"

			startTime = node.GetDateOrFail()
			mcc       = NewController(oc.AsAdmin()).IgnoreLogsBeforeNowOrFail()
		)
		exutil.By("Patch ManchineConfiguration cluster")
		defer ndp.Rollback()

		o.Expect(
			ndp.AddFilePolicy(innerDirPath, innerDirActions...).AddFilePolicy(outerDirPath, outerDirActions...).AddFilePolicy(filePath, fileActions...).Apply(),
		).To(o.Succeed(), "Patch ManchineConfiguration failed")
		logger.Infof("OK!\n")

		// Test the behaviour of files created inside the inner directorty
		exutil.By("Create a test file in the inner directory")
		innerMc := NewMachineConfig(oc.AsAdmin(), innderDirMcName, mcp.GetName())
		innerMc.SetParams(fmt.Sprintf("FILES=[%s]", innerDirFileConfig))
		defer innerMc.DeleteWithWait()
		innerMc.create()
		logger.Infof("OK!\n")

		exutil.By("Check that files inside the inner directory execute the right actions")
		checkDrainAndReboot(node, startTime, mcc, innerDirActions)
		checkMachineConfigDaemonLog(node, innerDirExpectedLogs)
		logger.Infof("OK!\n")

		// Test the behaviour of files created inside the outer directorty
		exutil.By("Create a test file in the outer directory")
		startTime = node.GetDateOrFail()
		mcc.IgnoreLogsBeforeNowOrFail()

		outerMc := NewMachineConfig(oc.AsAdmin(), outerDirMcName, mcp.GetName())
		outerMc.SetParams(fmt.Sprintf("FILES=[%s]", outerDirFileConfig))
		defer outerMc.Delete()
		outerMc.create()
		logger.Infof("OK!\n")

		exutil.By("Check that files inside the outer directory execute the right actions")
		checkDrainAndReboot(node, startTime, mcc, outerDirActions)
		checkMachineConfigDaemonLog(node, outerDirExpectedLogs)
		logger.Infof("OK!\n")

		// Test the behaviour of files created inside the outer directorty but with an explicit policy for them
		exutil.By("Create a test file inside the outer directory but with an explicitly defined policy")
		startTime = node.GetDateOrFail()
		mcc.IgnoreLogsBeforeNowOrFail()

		fileMc := NewMachineConfig(oc.AsAdmin(), fileMcName, mcp.GetName())
		fileMc.SetParams(fmt.Sprintf("FILES=[%s]", fileConfig))
		defer fileMc.Delete()
		fileMc.create()
		logger.Infof("OK!\n")

		exutil.By("Check that files with explicit defined policies execute the right actions")
		checkDrainAndReboot(node, startTime, mcc, fileActions)
		checkMachineConfigDaemonLog(node, fileExpectedLogs)
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:75110][OTP] Propagate a NodeDisruptionPolicy failure condition via degrading the daemon", func() {
		var (
			invalidService = "fake.service"
			invalidActions = []Action{NewReloadAction(invalidService)}
			validActions   = []Action{NewReloadAction(TestService)}

			mcp = GetCompactCompatiblePool(oc.AsAdmin())

			mcName      = "mco-tc-75110-failed-node-disruption-policy-action"
			filePath    = "/etc/test-file-policy-tc-75110-failed-action"
			fileContent = "test"
			fileConfig  = getURLEncodedFileConfig(filePath, fileContent, "420")

			expectedNDMessage = regexp.QuoteMeta(fmt.Sprintf("error running systemctl reload %s: Failed to reload %s: Unit %s not found", invalidService, invalidService, invalidService)) // quotemeta to scape regex characters
			expectedNDReason  = "1 nodes are reporting degraded status on sync"
		)

		exutil.By("Configure and invalid action")
		ndp := NewNodeDisruptionPolicy(oc)
		defer ndp.Rollback()
		defer mcp.RecoverFromDegraded()
		o.Expect(ndp.AddFilePolicy(filePath, invalidActions...).Apply()).To(o.Succeed(), "Patch ManchineConfiguration failed")
		logger.Infof("OK!\n")

		exutil.By("Create a MC using the configured disruption policy")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		mc.SetParams(fmt.Sprintf("FILES=[%s]", fileConfig))
		mc.skipWaitForMcp = true
		defer mc.Delete()
		mc.create()
		logger.Infof("OK!\n")

		checkDegraded(mcp, expectedNDMessage, expectedNDReason, "NodeDegraded", false, 1)

		exutil.By("Fix the disruption policy configuration")
		o.Expect(ndp.AddFilePolicy(filePath, validActions...).Apply()).To(o.Succeed(), "Patch ManchineConfiguration failed")
		logger.Infof("OK!\n")

		exutil.By("Check that the configuration can be applied")
		o.Eventually(mcp, "10m", "20s").ShouldNot(BeDegraded(),
			"The node disruption policy was fixed but the MCP didn't stop being degraded")
		mcp.waitForComplete()
		o.Eventually(NewResource(oc.AsAdmin(), "co", "machine-config"), "2m", "20s").ShouldNot(BeDegraded(),
			"machine-config CO should not be degraded anymore once the configuration is applied")

		o.Eventually(NewRemoteFile(mcp.GetSortedNodesOrFail()[0], filePath)).Should(HaveContent(fileContent),
			"The configuration was applied but the deployed file doesn't have the right content")
		logger.Infof("OK!\n")
	})

})

// test func for file based policy test cases
func testFileBasedPolicy(oc *exutil.CLI, caseID string, actions []Action, expectedLogs []string) {

	var (
		mcName     = fmt.Sprintf("create-test-file-%s-%s", caseID, exutil.GetRandomString())
		filePath   = fmt.Sprintf("/etc/test-file-policy-%s-%s", caseID, exutil.GetRandomString())
		fileConfig = getURLEncodedFileConfig(filePath, fmt.Sprintf("test-%s", caseID), "420")
		workerMcp  = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		workerNode = workerMcp.GetSortedNodesOrFail()[0]
		startTime  = workerNode.GetDateOrFail()
		mcc        = NewController(oc.AsAdmin()).IgnoreLogsBeforeNowOrFail()
	)

	exutil.By("Patch ManchineConfiguration cluster")
	ndp := NewNodeDisruptionPolicy(oc)
	defer ndp.Rollback()
	o.Expect(ndp.AddFilePolicy(filePath, actions...).Apply()).To(o.Succeed(), "Patch ManchineConfiguration failed")

	exutil.By("Check the nodeDisruptionPolicyStatus, new change should be merged")
	o.Expect(ndp.IsUpdated()).To(o.BeTrue(), "New policies are not merged properly")

	exutil.By("Create a test file on worker node")
	mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
	mc.SetParams(fmt.Sprintf("FILES=[%s]", fileConfig))
	mc.skipWaitForMcp = true
	defer mc.DeleteWithWait()
	mc.create()

	// check MCN for reboot and drain
	checkMachineConfigNode(oc, workerNode.GetName(), actions)

	workerMcp.waitForComplete()
	// check reboot and drain
	checkDrainAndReboot(workerNode, startTime, mcc, actions)
	// check MCD logs if expectedLogs is not empty
	checkMachineConfigDaemonLog(workerNode, expectedLogs)
}

// test func for unit based policy test cases
func testUnitBasedPolicy(oc *exutil.CLI, caseID string, actions []Action, expectedLogs []string) {

	var (
		unitName    = fmt.Sprintf("test-ndp-%s.service", exutil.GetRandomString())
		unitContent = "[Unit]\nDescription=test service for disruption policy"
		unitEnabled = false
		unitConfig  = getSingleUnitConfig(unitName, unitEnabled, unitContent)
		mcName      = fmt.Sprintf("create-test-unit-%s-%s", caseID, exutil.GetRandomString())
		workerMcp   = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		workerNode  = workerMcp.GetSortedNodesOrFail()[0]
		startTime   = workerNode.GetDateOrFail()
		mcc         = NewController(oc.AsAdmin()).IgnoreLogsBeforeNowOrFail()
	)

	exutil.By("Patch ManchineConfiguration cluster")
	ndp := NewNodeDisruptionPolicy(oc)
	defer ndp.Rollback()
	o.Expect(ndp.AddUnitPolicy(unitName, actions...).Apply()).To(o.Succeed(), "Patch ManchineConfiguration failed")

	exutil.By("Check the nodeDisruptionPolicyStatus, new change should be merged")
	o.Expect(ndp.IsUpdated()).To(o.BeTrue(), "New policies are not merged properly")

	exutil.By("Create a test unit on worker node")
	mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
	mc.SetParams(fmt.Sprintf("UNITS=[%s]", unitConfig))
	mc.skipWaitForMcp = true
	defer mc.DeleteWithWait()
	mc.create()

	// check MCN for reboot and drain
	checkMachineConfigNode(oc, workerNode.GetName(), actions)

	workerMcp.waitForComplete()
	// check reboot and drain
	checkDrainAndReboot(workerNode, startTime, mcc, actions)
	// check MCD logs if expectedLogs is not empty
	checkMachineConfigDaemonLog(workerNode, expectedLogs)
}

// test func for sshkey based policy test cases
func testSSHKeyBasedPolicy(oc *exutil.CLI, caseID string, actions []Action, expectedLogs []string) {

	var (
		mcName = fmt.Sprintf("create-test-sshkey-%s-%s", caseID, exutil.GetRandomString())
		// sshkey change only works on coreOS node
		workerNode = sortNodeList(NewNodeList(oc.AsAdmin()).GetAllCoreOsWokerNodesOrFail())[0]
		workerMcp  = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		startTime  = workerNode.GetDateOrFail()
		mcc        = NewController(oc.AsAdmin()).IgnoreLogsBeforeNowOrFail()

		_, pubKey1 = GenerateSSHKeyPairOrFail()
		user       = ign32PaswdUser{Name: "core", SSHAuthorizedKeys: []string{pubKey1}}
	)

	exutil.By("Patch ManchineConfiguration cluster")
	ndp := NewNodeDisruptionPolicy(oc)
	defer ndp.Rollback()
	o.Expect(ndp.SetSSHKeyPolicy(actions...).Apply()).To(o.Succeed(), "Patch ManchineConfiguration failed")

	exutil.By("Check the nodeDisruptionPolicyStatus, new change should be merged")
	o.Expect(ndp.IsUpdated()).To(o.BeTrue(), "New policies are not merged properly")

	exutil.By("Create machine config with new SSH authorized key")
	mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
	mc.parameters = []string{fmt.Sprintf(`PWDUSERS=[%s]`, MarshalOrFail(user))}
	mc.skipWaitForMcp = true
	defer mc.DeleteWithWait()
	mc.create()

	// check MCN for reboot and drain
	checkMachineConfigNode(oc, workerNode.GetName(), actions)

	workerMcp.waitForComplete()
	// check reboot and drain
	checkDrainAndReboot(workerNode, startTime, mcc, actions)
	// check MCD logs if expectedLogs is not empty
	checkMachineConfigDaemonLog(workerNode, expectedLogs)
}

// test func used to check expected logs in MCD log
func checkMachineConfigDaemonLog(node *Node, expectedLogs []string) {
	if len(expectedLogs) > 0 {
		exutil.By("Check MCD log for post config actions")
		logs, err := node.GetMCDaemonLogs("update.go")
		o.Expect(err).NotTo(o.HaveOccurred(), "Get MCD log failed")
		for _, log := range expectedLogs {
			o.Expect(logs).Should(o.ContainSubstring(log), "Cannot find expected log for post config actions")
		}
	}
}

// test func to check MCN by input actions
func checkMachineConfigNode(oc *exutil.CLI, nodeName string, actions []Action) {

	hasRebootAction := hasAction(NodeDisruptionPolicyActionReboot, actions)
	hasDrainAction := hasAction(NodeDisruptionPolicyActionDrain, actions)

	mcn := NewMachineConfigNode(oc.AsAdmin(), nodeName)
	if hasDrainAction {
		exutil.By("Check whether the node is drained")
		o.Eventually(mcn.GetDrained, "5m", "2s").Should(o.Equal("True"))
	}
	if hasRebootAction {
		exutil.By("Check whether the node is rebooted")
		o.Eventually(mcn.GetRebootedNode, "10m", "2s").Should(o.Equal("True"))
	}
}

// test func to check drain and reboot actions without using MCN
func checkDrainAndReboot(node *Node, startTime time.Time, controller *Controller, actions []Action) {
	hasRebootAction := hasAction(NodeDisruptionPolicyActionReboot, actions)
	hasDrainAction := hasAction(NodeDisruptionPolicyActionDrain, actions)

	// A drain operation is always  executed when a reboot opration is executed, even if the drain action is not configured
	// In SNO clusters the drain operation is not executed if the node is rebooted
	checkDrainAction(hasDrainAction || (hasRebootAction && !IsSNO(node.GetOC())), node, controller)
	checkRebootAction(hasRebootAction, node, startTime)
}

func hasAction(actnType string, actions []Action) bool {
	found := false
	for _, a := range actions {
		if a.Type == actnType {
			found = true
			break
		}
	}
	return found
}
