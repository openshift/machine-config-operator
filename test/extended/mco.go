package extended

import (
	o "github.com/onsi/gomega"
	compat_otp "github.com/openshift/origin/test/extended/util/compat_otp"
	logger "github.com/openshift/origin/test/extended/util/compat_otp/logext"
)

func checkDegraded(mcp *MachineConfigPool, expectedMessage, expectedReason, degradedConditionType string, checkCODegraded bool, offset int) {
	oc := mcp.oc
	expectedNumDegradedMachines := 0
	if degradedConditionType == "NodeDegraded" {
		expectedNumDegradedMachines = 1
	}

	compat_otp.By("Wait until MCP becomes degraded")
	o.EventuallyWithOffset(offset, mcp, mcp.estimateWaitDuration().String(), "30s").Should(BeDegraded(),
		"The '%s' MCP should become degraded when we try to create an invalid MC, but it didn't.", mcp.GetName())
	o.EventuallyWithOffset(offset, mcp.getDegradedMachineCount, "5m", "30s").Should(o.Equal(expectedNumDegradedMachines),
		"The '%s' MCP should report '%d' degraded machine count, but it doesn't.", expectedNumDegradedMachines, mcp.GetName())

	logger.Infof("OK!\n")

	compat_otp.By("Validate the reported error")
	degradedCondition := mcp.GetConditionByType(degradedConditionType)

	o.ExpectWithOffset(offset, mcp).Should(HaveConditionField(degradedConditionType, "status", o.Equal("True")),
		"'worker' MCP should report degraded status in the %s condition: %s", degradedConditionType, degradedCondition)

	o.ExpectWithOffset(offset, mcp).Should(HaveConditionField(degradedConditionType, "message", o.MatchRegexp(expectedMessage)),
		"'worker' MCP is not reporting the expected message in the %s condition: %s", degradedConditionType, degradedCondition)

	o.ExpectWithOffset(offset, mcp).Should(HaveConditionField(degradedConditionType, "reason", o.MatchRegexp(expectedReason)),
		"'worker' MCP is not reporting the expected reason in the NodeDegraded condition: %s", degradedConditionType, degradedCondition)
	logger.Infof("OK!\n")

	compat_otp.By("Get co machine config to verify status and reason for Upgradeable type")

	// If the pool is degraded, then co/machine-config should not be upgradeable
	// It's unlikely, but it can happen that the MCP is degraded, but the CO has not been already updated with the right error message.
	// So we need to poll for the right reason
	mco := NewResource(oc, "co", "machine-config")
	o.EventuallyWithOffset(offset, mco, "5m", "10s").Should(HaveConditionField("Upgradeable", "reason", o.Equal("DegradedPool")),
		"co/machine-config Upgradeable condition reason is not the expected one: %s", mco.GetConditionByType("Upgradeable"))

	o.EventuallyWithOffset(offset, mco, "5m", "10s").Should(HaveConditionField("Upgradeable", "status", o.Equal("False")),
		"co/machine-config Upgradeable condition status is not the expected one: %s", mco.GetConditionByType("Upgradeable"))

	expectedCOMessage := "One or more machine config pools are degraded, please see `oc get mcp` for further details and resolve before upgrading"
	o.EventuallyWithOffset(offset, mco, "5m", "10s").Should(HaveConditionField("Upgradeable", "message", o.ContainSubstring(expectedCOMessage)),
		"co/machine-config Upgradeable condition message is not the expected one: %s", mco.GetConditionByType("Upgradeable"))

	o.ConsistentlyWithOffset(offset, mco, "1m", "10s").Should(HaveConditionField("Available", "status", o.Equal("True")),
		"co/machine-config should never have condition Available=false")

	// Because of https://github.com/openshift/machine-config-operator/pull/4617#issuecomment-2385929278 it takes 30 minutes for the CO to become degraded
	// We cannot afford to spend 30 minutes in every negative test case just waiting for the machine-config CO to become degraded
	// We cannot afford not to test it either. We should have caught the issue linked above
	// What we will do is to check for the CO degraded status in only one of our negative test cases trying to reach a good compromise.
	if checkCODegraded {
		o.EventuallyWithOffset(offset, mco, "35m", "30s").Should(BeDegraded(),
			"co/macihne-config should be degraded because the MCP is degraded, but it wasn't")
		// Double check that when we degraded CO we didn't se a wrong Available value
		o.EventuallyWithOffset(offset, mco, "1m", "10s").Should(HaveConditionField("Available", "status", o.Equal("True")),
			"co/machine-config should never have condition Available=false")
	}
	logger.Infof("OK!\n")
}
