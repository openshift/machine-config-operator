package extended

import (
	"fmt"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO config drift", func() {
	defer g.GinkgoRecover()

	var oc = exutil.NewCLI("mco-config-drift", exutil.KubeConfigPath())

	g.JustBeforeEach(func() {
		PreChecks(oc)
	})

	g.It("[PolarionID:46943][OTP] Config Drift. Config file. [Disruptive]", func() {
		exutil.By("Create a MC to deploy a config file")
		filePath := "/etc/mco-test-file"
		fileContent := "MCO test file\n"
		fileConfig := getURLEncodedFileConfig(filePath, fileContent, "")

		mcName := "mco-drift-test-file"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		defer mc.DeleteWithWait()

		err := mc.Create("-p", "NAME="+mcName, "-p", "POOL=worker", "-p", fmt.Sprintf("FILES=[%s]", fileConfig))
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Wait until worker MCP has finished the configuration. No machine should be degraded.")
		mcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		mcp.waitForComplete()

		exutil.By("Verfiy file content and permissions")
		workerNode := NewNodeList(oc.AsAdmin()).GetAllLinuxWorkerNodesOrFail()[0]

		defaultMode := "0644"
		rf := NewRemoteFile(workerNode, filePath)
		rferr := rf.Fetch()
		o.Expect(rferr).NotTo(o.HaveOccurred())

		o.Expect(rf.GetTextContent()).To(o.Equal(fileContent))
		o.Expect(rf.GetNpermissions()).To(o.Equal(defaultMode))

		exutil.By("Verify drift config behavior")
		defer func() {
			_ = rf.PushNewPermissions(defaultMode)
			_ = rf.PushNewTextContent(fileContent)
			_ = mcp.WaitForNotDegradedStatus()
		}()

		newMode := "0400"
		useForceFile := false
		verifyDriftConfig(mcp, rf, newMode, useForceFile)
	})

	g.It("[PolarionID:46999][OTP] Config Drift. Config file permissions. [Disruptive]", func() {
		exutil.By("Create a MC to deploy a config file")
		filePath := "/etc/mco-test-file"
		fileContent := "MCO test file\n"
		fileMode := "0400" // decimal 256
		fileConfig := getURLEncodedFileConfig(filePath, fileContent, fileMode)

		mcName := "mco-drift-test-file-permissions"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		defer mc.DeleteWithWait()

		err := mc.Create("-p", "NAME="+mcName, "-p", "POOL=worker", "-p", fmt.Sprintf("FILES=[%s]", fileConfig))
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Wait until worker MCP has finished the configuration. No machine should be degraded.")
		mcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		mcp.waitForComplete()

		exutil.By("Verfiy file content and permissions")
		workerNode := NewNodeList(oc.AsAdmin()).GetAllLinuxWorkerNodesOrFail()[0]

		rf := NewRemoteFile(workerNode, filePath)
		rferr := rf.Fetch()
		o.Expect(rferr).NotTo(o.HaveOccurred())

		o.Expect(rf.GetTextContent()).To(o.Equal(fileContent))
		o.Expect(rf.GetNpermissions()).To(o.Equal(fileMode))

		exutil.By("Verify drift config behavior")
		defer func() {
			_ = rf.PushNewPermissions(fileMode)
			_ = rf.PushNewTextContent(fileContent)
			_ = mcp.WaitForNotDegradedStatus()
		}()

		newMode := "0644"
		useForceFile := true
		verifyDriftConfig(mcp, rf, newMode, useForceFile)
	})

	g.It("[PolarionID:47045][OTP] Config Drift. Compressed files. [Disruptive]", func() {
		exutil.By("Create a MC to deploy a config file using compression")
		filePath := "/etc/mco-compressed-test-file"
		fileContent := "MCO test file\nusing compression"
		fileConfig := getGzipFileJSONConfig(filePath, fileContent)

		mcName := "mco-drift-test-compressed-file"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		defer mc.DeleteWithWait()

		err := mc.Create("-p", "NAME="+mcName, "-p", "POOL=worker", "-p", fmt.Sprintf("FILES=[%s]", fileConfig))
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Wait until worker MCP has finished the configuration. No machine should be degraded.")
		mcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		mcp.waitForComplete()

		exutil.By("Verfiy file content and permissions")
		workerNode := NewNodeList(oc.AsAdmin()).GetAllLinuxWorkerNodesOrFail()[0]

		rf := NewRemoteFile(workerNode, filePath)
		rferr := rf.Fetch()
		o.Expect(rferr).NotTo(o.HaveOccurred())

		defaultMode := "0644"
		o.Expect(rf.GetTextContent()).To(o.Equal(fileContent))
		o.Expect(rf.GetNpermissions()).To(o.Equal(defaultMode))

		exutil.By("Verfy drift config behavior")
		defer func() {
			_ = rf.PushNewPermissions(defaultMode)
			_ = rf.PushNewTextContent(fileContent)
			_ = mcp.WaitForNotDegradedStatus()
		}()

		newMode := "0400"
		useForceFile := true
		verifyDriftConfig(mcp, rf, newMode, useForceFile)
	})

	g.It("[PolarionID:47008][OTP] Config Drift. Dropin file. [Disruptive]", func() {
		exutil.By("Create a MC to deploy a unit with a dropin file")
		dropinFileName := "10-chrony-drop-test.conf"
		filePath := "/etc/systemd/system/chronyd.service.d/" + dropinFileName
		fileContent := "[Service]\nEnvironment=\"FAKE_OPTS=fake-value\""
		unitEnabled := true
		unitName := "chronyd.service"
		unitConfig := getDropinFileConfig(unitName, unitEnabled, dropinFileName, fileContent)

		mcName := "drifted-dropins-test"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		defer mc.DeleteWithWait()

		err := mc.Create("-p", "NAME="+mcName, "-p", "POOL=worker", "-p", fmt.Sprintf("UNITS=[%s]", unitConfig))
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Wait until worker MCP has finished the configuration. No machine should be degraded.")
		mcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		mcp.waitForComplete()

		exutil.By("Verfiy file content and permissions")
		workerNode := NewNodeList(oc.AsAdmin()).GetAllLinuxWorkerNodesOrFail()[0]

		rf := NewRemoteFile(workerNode, filePath)
		rferr := rf.Fetch()
		o.Expect(rferr).NotTo(o.HaveOccurred())

		defaultMode := "0644"
		o.Expect(rf.GetTextContent()).To(o.Equal(fileContent))
		o.Expect(rf.GetNpermissions()).To(o.Equal(defaultMode))

		exutil.By("Verify drift config behavior")
		defer func() {
			_ = rf.PushNewPermissions(defaultMode)
			_ = rf.PushNewTextContent(fileContent)
			_ = mcp.WaitForNotDegradedStatus()
		}()

		newMode := "0400"
		useForceFile := true
		verifyDriftConfig(mcp, rf, newMode, useForceFile)
	})

	g.It("[PolarionID:47009][OTP] Config Drift. New Service Unit. [Disruptive]", func() {
		exutil.By("Create a MC to deploy a unit.")
		unitEnabled := true
		unitName := "example.service"
		filePath := "/etc/systemd/system/" + unitName
		fileContent := "[Service]\nType=oneshot\nExecStart=/usr/bin/echo Hello from MCO test service\n\n[Install]\nWantedBy=multi-user.target"
		unitConfig := getSingleUnitConfig(unitName, unitEnabled, fileContent)

		mcName := "drifted-new-service-test"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		defer mc.DeleteWithWait()

		err := mc.Create("-p", "NAME="+mcName, "-p", "POOL=worker", "-p", fmt.Sprintf("UNITS=[%s]", unitConfig))
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Wait until worker MCP has finished the configuration. No machine should be degraded.")
		mcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		mcp.waitForComplete()

		exutil.By("Verfiy file content and permissions")
		workerNode := NewNodeList(oc.AsAdmin()).GetAllLinuxWorkerNodesOrFail()[0]

		rf := NewRemoteFile(workerNode, filePath)
		rferr := rf.Fetch()
		o.Expect(rferr).NotTo(o.HaveOccurred())

		defaultMode := "0644"
		o.Expect(rf.GetTextContent()).To(o.Equal(fileContent))
		o.Expect(rf.GetNpermissions()).To(o.Equal(defaultMode))

		exutil.By("Verfiy deployed unit")
		unitStatus, _ := workerNode.GetUnitStatus(unitName)
		// since it is a one-shot "hello world" service the execution will end
		// after the hello message and the unit will become inactive. So we dont check the error code.
		o.Expect(unitStatus).Should(
			o.And(
				o.ContainSubstring(unitName),
				o.ContainSubstring("Active: inactive (dead)"),
				o.ContainSubstring("Hello from MCO test service"),
				o.ContainSubstring("example.service: Deactivated successfully.")))

		exutil.By("Verify drift config behavior")
		defer func() {
			_ = rf.PushNewPermissions(defaultMode)
			_ = rf.PushNewTextContent(fileContent)
			_ = mcp.WaitForNotDegradedStatus()
		}()

		newMode := "0400"
		useForceFile := true
		verifyDriftConfig(mcp, rf, newMode, useForceFile)
	})
})

func verifyDriftConfig(mcp *MachineConfigPool, rf *RemoteFile, newMode string, forceFile bool) {
	workerNode := rf.node
	origContent := rf.GetTextContent()
	origMode := rf.GetNpermissions()

	exutil.By("Modify file content and check degraded status")
	newContent := origContent + "Extra Info"
	o.Expect(rf.PushNewTextContent(newContent)).NotTo(o.HaveOccurred())
	rferr := rf.Fetch()
	o.Expect(rferr).NotTo(o.HaveOccurred())

	o.Expect(rf.GetTextContent()).To(o.Equal(newContent), "File content should be updated")
	o.Eventually(mcp.pollDegradedMachineCount(), "10m", "30s").Should(o.Equal("1"), "There should be 1 degraded machine")
	o.Eventually(mcp.pollDegradedStatus(), "10m", "30s").Should(o.Equal("True"), "The worker MCP should report a True Degraded status")
	o.Eventually(mcp.pollUpdatedStatus(), "10m", "30s").Should(o.Equal("False"), "The worker MCP should report a False Updated status")

	exutil.By("Verify that node annotations describe the reason for the Degraded status")
	reason := workerNode.GetAnnotationOrFail("machineconfiguration.openshift.io/reason")
	o.Expect(reason).To(o.ContainSubstring(fmt.Sprintf(`content mismatch for file "%s"`, rf.fullPath)))

	if forceFile {
		exutil.By("Restore original content using the ForceFile and wait until pool is ready again")
		o.Expect(workerNode.ForceReapplyConfiguration()).NotTo(o.HaveOccurred())
	} else {
		exutil.By("Restore original content manually and wait until pool is ready again")
		o.Expect(rf.PushNewTextContent(origContent)).NotTo(o.HaveOccurred())
	}

	o.Eventually(mcp.pollDegradedMachineCount(), "10m", "30s").Should(o.Equal("0"), "There should be no degraded machines")
	o.Eventually(mcp.pollDegradedStatus(), "10m", "30s").Should(o.Equal("False"), "The worker MCP should report a False Degraded status")
	o.Eventually(mcp.pollUpdatedStatus(), "15m", "30s").Should(o.Equal("True"), "The worker MCP should report a True Updated status")
	rferr = rf.Fetch()
	o.Expect(rferr).NotTo(o.HaveOccurred())
	o.Expect(rf.GetTextContent()).To(o.Equal(origContent), "Original file content should be restored")

	exutil.By("Verify that node annotations have been cleaned")
	reason = workerNode.GetAnnotationOrFail("machineconfiguration.openshift.io/reason")
	o.Expect(reason).To(o.Equal(``))

	exutil.By(fmt.Sprintf("Manually modify the file permissions to %s", newMode))
	o.Expect(rf.PushNewPermissions(newMode)).NotTo(o.HaveOccurred())
	rferr = rf.Fetch()
	o.Expect(rferr).NotTo(o.HaveOccurred())

	o.Expect(rf.GetNpermissions()).To(o.Equal(newMode), "%s File permissions should be %s", rf.fullPath, newMode)
	o.Eventually(mcp.pollDegradedMachineCount(), "10m", "30s").Should(o.Equal("1"), "There should be 1 degraded machine")
	o.Eventually(mcp.pollDegradedStatus(), "10m", "30s").Should(o.Equal("True"), "The worker MCP should report a True Degraded status")
	o.Eventually(mcp.pollUpdatedStatus(), "10m", "30s").Should(o.Equal("False"), "The worker MCP should report a False Updated status")

	exutil.By("Verify that node annotations describe the reason for the Degraded status")
	reason = workerNode.GetAnnotationOrFail("machineconfiguration.openshift.io/reason")
	o.Expect(reason).To(o.MatchRegexp(fmt.Sprintf(`mode mismatch for file: "%s"; expected: .+/%s; received: .+/%s`, rf.fullPath, origMode, newMode)))

	exutil.By("Restore the original file permissions")
	o.Expect(rf.PushNewPermissions(origMode)).NotTo(o.HaveOccurred())
	rferr = rf.Fetch()
	o.Expect(rferr).NotTo(o.HaveOccurred())

	o.Expect(rf.GetNpermissions()).To(o.Equal(origMode), "%s File permissions should be %s", rf.fullPath, origMode)
	o.Eventually(mcp.pollDegradedMachineCount(), "10m", "30s").Should(o.Equal("0"), "There should be no degraded machines")
	o.Eventually(mcp.pollDegradedStatus(), "10m", "30s").Should(o.Equal("False"), "The worker MCP should report a False Degraded status")
	o.Eventually(mcp.pollUpdatedStatus(), "10m", "30s").Should(o.Equal("True"), "The worker MCP should report a True Updated status")

	exutil.By("Verify that node annotations have been cleaned")
	reason = workerNode.GetAnnotationOrFail("machineconfiguration.openshift.io/reason")
	o.Expect(reason).To(o.Equal(``))
}
