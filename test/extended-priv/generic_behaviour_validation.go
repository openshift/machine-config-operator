package extended

import (
	"fmt"
	"time"

	o "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

type Checker interface {
	Check(checkedNodes ...Node)
}

type CommandOutputChecker struct {
	Command  []string
	Matcher  types.GomegaMatcher
	ErrorMsg string
	Desc     string
}

func (cOutChecker CommandOutputChecker) Check(checkedNodes ...Node) {
	msg := "Executing verification commands"
	if cOutChecker.Desc != "" {
		msg = cOutChecker.Desc
	}
	exutil.By(msg)
	o.Expect(checkedNodes).NotTo(o.BeEmpty(), "Refuse to check an empty list of nodes")

	for _, node := range checkedNodes {
		logger.Infof("In node %s. Executing command %s", node.GetName(), cOutChecker.Command)
		o.Expect(
			node.DebugNodeWithChroot(cOutChecker.Command...),
		).To(cOutChecker.Matcher,
			"Command %s validation failed in node %s: %s", cOutChecker.Command, node.GetName(), cOutChecker.ErrorMsg)
	}
	logger.Infof("OK!\n")

}

type RemoteFileChecker struct {
	FileFullPath string
	Matcher      types.GomegaMatcher
	ErrorMsg     string
	Desc         string
}

func (rfc RemoteFileChecker) Check(checkedNodes ...Node) {
	msg := fmt.Sprintf("Checking file: %s", rfc.FileFullPath)
	if rfc.Desc != "" {
		msg = rfc.Desc
	}
	exutil.By(msg)
	o.Expect(checkedNodes).NotTo(o.BeEmpty(), "Refuse to check an empty list of nodes")

	for _, node := range checkedNodes {
		rf := NewRemoteFile(node, rfc.FileFullPath)
		logger.Infof("Checking remote file %s", rf)
		o.Expect(rf).To(rfc.Matcher,
			"Validation of %s failed: %", rf, rfc.ErrorMsg)
	}
	logger.Infof("OK!\n")
}

// checkDrainAction checks that the drain action in the node is the expected one (drainSkipped)
func checkDrainAction(drainSkipped bool, node Node, controller *Controller) {
	checkDrainActionWithGomega(drainSkipped, node, controller, o.Default)
}

// checkDrainActionWithGomega checks that the drain action in the node is the expected one (drainSkipped). It accepts an extra Gomega parameter that allows the function to be used in the Eventually gomega matchers
func checkDrainActionWithGomega(drainExecuted bool, node Node, controller *Controller, gm o.Gomega) {
	var (
		execDrainLogMsg = "initiating drain"
	)

	// We use MCD logs to check if drain is skipped, and we use the Controller logs to check if drain is executed
	if drainExecuted {
		// When drain is executed reboot is usually executed too (since one of the reasons why MCO executes a drain operation is to be able to execute a safe reboot).
		// Hence, we cannot look for the "drain" message in the MCD logs because they have been removed in the reboot.
		// There are two options, we can look for the message in the pre-reboot MCD logs or we can look for them in the MCController pod
		// We decided to use the controller logs because it is way easier.
		logger.Infof("Checking that node %s was drained", node.GetName())
		gm.Expect(
			controller.GetLogs(),
		).Should(o.ContainSubstring("node "+node.GetName()+": "+execDrainLogMsg),
			"Error! The node %s was NOT drained, but it should be", node.GetName())
	} else {
		logger.Infof("Checking that node %s was NOT drained", node.GetName())
		gm.Expect(
			controller.GetLogs(),
		).ShouldNot(o.ContainSubstring("node "+node.GetName()+": "+execDrainLogMsg),
			"Error! The node %s was drained, but the drain operation should have been skipped", node.GetName())

	}
}

// checkRebootAction checks that the reboot action in the node is the expected one (rebootSkipped)
func checkRebootAction(rebootExecuted bool, node Node, startTime time.Time) {
	checkRebootActionWithGomega(rebootExecuted, node, startTime, o.Default)
}

// checkRebootActionWithGomega checks that the reboot action in the node is the expected one (rebootSkipped). It accepts an extra Gomega parameter that allows the function to be used in the Eventually gomega matchers
func checkRebootActionWithGomega(rebootExecuted bool, node Node, startTime time.Time, gm o.Gomega) {
	if rebootExecuted {
		logger.Infof("Checking that node %s was rebooted", node.GetName())
		gm.Expect(node.GetUptime()).Should(o.BeTemporally(">", startTime),
			"The node %s must be rebooted, but it was not. Uptime date happened before the start config time.", node.GetName())
	} else {
		logger.Infof("Checking that node %s was NOT rebooted", node.GetName())
		gm.Expect(node.GetUptime()).Should(o.BeTemporally("<", startTime),
			"The node %s must NOT be rebooted, but it was rebooted. Uptime date happened after the start config time.", node.GetName())

	}
}

// validateMcpRenderDegraded validates that creating the provided MC will cause the provided MCP to degrade during rendering
func validateMcpRenderDegraded(mc *MachineConfig, mcp *MachineConfigPool, expectedRDMessage, expectedRDReason string) {
	defer mc.DeleteWithWait()
	mc.create()

	logger.Infof("Checking that MCP is degraded after creating MC")
	o.Eventually(mcp, "10m", "30s").Should(BeDegraded(),
		"MCP %s should be degraded after creating MC %s", mcp.GetName(), mc.GetName())

	if expectedRDMessage != "" {
		renderDegradedMessage := mcp.GetOrFail(`{.status.conditions[?(@.type=="RenderDegraded")].message}`)
		o.Expect(renderDegradedMessage).To(o.MatchRegexp(expectedRDMessage),
			"RenderDegraded message does not match expected pattern")
	}

	if expectedRDReason != "" {
		renderDegradedReason := mcp.GetOrFail(`{.status.conditions[?(@.type=="RenderDegraded")].reason}`)
		o.Expect(renderDegradedReason).To(o.Equal(expectedRDReason),
			"RenderDegraded reason does not match expected value")
	}

	logger.Infof("OK!\n")
}
