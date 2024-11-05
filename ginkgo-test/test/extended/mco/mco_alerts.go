package mco

import (
	"fmt"
	"regexp"
	"time"

	"github.com/onsi/gomega/types"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util"
	logger "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/logext"
)

var _ = g.Describe("[sig-mco] MCO alerts", func() {
	defer g.GinkgoRecover()

	var (
		oc = exutil.NewCLI("mco-alerts", exutil.KubeConfigPath())
		// CoreOs compatible MachineConfigPool (if worker pool has CoreOs nodes, then it is worker pool, else it is master pool because mater nodes are always CoreOs)
		coMcp *MachineConfigPool
		// Compact compatible MCP. If the node is compact/SNO this variable will be the master pool, else it will be the worker pool
		mcp *MachineConfigPool
		// master MCP
		mMcp *MachineConfigPool
	)

	g.JustBeforeEach(func() {
		coMcp = GetCoreOsCompatiblePool(oc.AsAdmin())
		mcp = GetCompactCompatiblePool(oc.AsAdmin())
		mMcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)

		preChecks(oc)
	})

	g.It("Author:sregidor-NonHyperShiftHOST-NonPreRelease-Longduration-Medium-63865-MCDRebootError alert[Disruptive]", func() {
		var (
			mcName                 = "mco-tc-63865-reboot-alert"
			filePath               = "/etc/mco-tc-63865-test.test"
			fileContent            = "test"
			fileMode               = 420 // decimal 0644
			expectedAlertName      = "MCDRebootError"
			expectedAlertSeverity  = "critical"
			alertFiredAfter        = 5 * time.Minute
			alertStillPresentAfter = 10 * time.Minute
		)

		exutil.By("Break the reboot process in a node")
		node := mcp.GetSortedNodesOrFail()[0]
		defer func() {
			_ = FixRebootInNode(&node)
			mcp.WaitForUpdatedStatus()
		}()
		o.Expect(BreakRebootInNode(&node)).To(o.Succeed(),
			"Error breaking the reboot process in node %s", node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Create a MC to force a reboot")
		file := ign32File{
			Path: filePath,
			Contents: ign32Contents{
				Source: GetBase64EncodedFileSourceContent(fileContent),
			},
			Mode: PtrInt(fileMode),
		}

		mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName())
		mc.skipWaitForMcp = true
		defer mc.deleteNoWait()

		mc.parameters = []string{fmt.Sprintf("FILES=[%s]", MarshalOrFail(file))}
		mc.create()
		logger.Infof("OK!\n")

		// Check that the expected alert is fired with the right values
		expectedDegradedMessage := fmt.Sprintf(`Node %s is reporting: "reboot command failed, something is seriously wrong"`,
			node.GetName())

		expectedAlertLabels := expectedAlertValues{"severity": o.Equal(expectedAlertSeverity)}

		expectedAlertAnnotationDescription := fmt.Sprintf("Reboot failed on %s , update may be blocked. For more details:  oc logs -f -n openshift-machine-config-operator machine-config-daemon",
			node.GetName())
		expectedAlertAnnotationSummary := "Alerts the user that a node failed to reboot one or more times over a span of 5 minutes."

		expectedAlertAnnotations := expectedAlertValues{
			"description": o.ContainSubstring(expectedAlertAnnotationDescription),
			"summary":     o.Equal(expectedAlertAnnotationSummary),
		}

		params := checkFiredAlertParams{
			expectedAlertName:        expectedAlertName,
			expectedDegradedMessage:  regexp.QuoteMeta(expectedDegradedMessage),
			expectedAlertLabels:      expectedAlertLabels,
			expectedAlertAnnotations: expectedAlertAnnotations,
			pendingDuration:          alertFiredAfter,
			// Because of OCPBUGS-5497, we need to check that the alert is already present after 15 minutes.
			// We have waited 5 minutes to test the "firing" state, so we only have to wait 10 minutes more to test the 15 minutes needed since OCPBUGS-5497
			stillPresentDuration: alertStillPresentAfter,
		}
		checkFiredAlert(oc, mcp, params)

		exutil.By("Fix the reboot process in the node")
		o.Expect(FixRebootInNode(&node)).To(o.Succeed(),
			"Error fixing the reboot process in node %s", node.GetName())
		logger.Infof("OK!\n")

		checkFixedAlert(oc, mcp, expectedAlertName)
	})

	g.It("Author:sregidor-VMonly-NonHyperShiftHOST-NonPreRelease-Longduration-Medium-63866-MCDPivotError alert[Disruptive]", func() {
		var (
			mcName                = "mco-tc-63866-pivot-alert"
			expectedAlertName     = "MCDPivotError"
			alertFiredAfter       = 2 * time.Minute
			dockerFileCommands    = `RUN echo 'Hello world' >  /etc/hello-world-file`
			expectedAlertSeverity = "warning"
		)
		// We use master MCP because like that we make sure that we are using a CoreOs node
		exutil.By("Break the reboot process in a node")
		// We sort the coreOs list to make sure that we break the first updated not to make the test faster
		node := sortNodeList(coMcp.GetCoreOsNodesOrFail())[0]
		defer func() {
			_ = FixRebaseInNode(&node)
			coMcp.WaitForUpdatedStatus()
		}()
		o.Expect(BreakRebaseInNode(&node)).To(o.Succeed(),
			"Error breaking the rpm-ostree rebase process in node %s", node.GetName())
		logger.Infof("OK!\n")

		// Build a new osImage that we will use to force a rebase in the broken node
		exutil.By("Build new OSImage")
		osImageBuilder := OsImageBuilderInNode{node: node, dockerFileCommands: dockerFileCommands}
		digestedImage, err := osImageBuilder.CreateAndDigestOsImage()
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error creating the new osImage")
		logger.Infof("OK\n")

		// Create MC to force the rebase operation
		exutil.By("Create a MC to deploy the new osImage")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, coMcp.GetName())
		mc.parameters = []string{"OS_IMAGE=" + digestedImage}

		mc.skipWaitForMcp = true
		defer mc.deleteNoWait()
		mc.create()
		logger.Infof("OK\n")

		// Check that the expected alert is fired with the right values
		expectedDegradedMessage := fmt.Sprintf(`Node %s is reporting: "failed to update OS to %s`,
			node.GetName(), digestedImage)

		expectedAlertLabels := expectedAlertValues{"severity": o.Equal(expectedAlertSeverity)}

		expectedAlertAnnotationDescription := fmt.Sprintf("Error detected in pivot logs on %s , upgrade may be blocked. For more details:  oc logs -f -n openshift-machine-config-operator machine-config-daemon-",
			node.GetName())
		expectedAlertAnnotationSummary := "Alerts the user when an error is detected upon pivot. This triggers if the pivot errors are above zero for 2 minutes."

		expectedAlertAnnotations := expectedAlertValues{
			"description": o.ContainSubstring(expectedAlertAnnotationDescription),
			"summary":     o.Equal(expectedAlertAnnotationSummary),
		}

		params := checkFiredAlertParams{
			expectedAlertName:        expectedAlertName,
			expectedDegradedMessage:  regexp.QuoteMeta(expectedDegradedMessage),
			expectedAlertLabels:      expectedAlertLabels,
			expectedAlertAnnotations: expectedAlertAnnotations,
			pendingDuration:          alertFiredAfter,
			stillPresentDuration:     0, // We skip this validation to make the test faster
		}
		checkFiredAlert(oc, coMcp, params)

		exutil.By("Fix the rpm-ostree rebase process in the node")
		o.Expect(FixRebaseInNode(&node)).To(o.Succeed(),
			"Error fixing the rpm-ostree rebase process in node %s", node.GetName())
		logger.Infof("OK!\n")

		checkFixedAlert(oc, coMcp, expectedAlertName)
	})

	g.It("Author:sregidor-NonHyperShiftHOST-NonPreRelease-Longduration-Medium-62075-MCCPoolAlert. Test support for a node pool hierarchy [Disruptive]", func() {

		var (
			iMcpName              = "infra"
			expectedAlertName     = "MCCPoolAlert"
			expectedAlertSeverity = "warning"

			masterNode = mMcp.GetNodesOrFail()[0]
			mcc        = NewController(oc.AsAdmin())
		)

		numMasterNodes, err := mMcp.getMachineCount()
		o.Expect(err).NotTo(o.HaveOccurred(), "Cannot get the machinecount field in % MCP", mMcp.GetName())

		exutil.By("Add label as infra to the existing master node")
		infraLabel := "node-role.kubernetes.io/infra"
		defer func() {
			// ignore output, just focus on error handling, if error is occurred, fail this case
			_, deletefailure := masterNode.DeleteLabel(infraLabel)
			o.Expect(deletefailure).NotTo(o.HaveOccurred())
		}()
		err = masterNode.AddLabel(infraLabel, "")
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Could not add the label %s to node %s", infraLabel, masterNode)
		logger.Infof("OK!\n")

		exutil.By("Create custom infra mcp")
		iMcpTemplate := generateTemplateAbsolutePath("custom-machine-config-pool.yaml")
		iMcp := NewMachineConfigPool(oc.AsAdmin(), iMcpName)
		iMcp.template = iMcpTemplate
		// We need to wait for the label to be delete before removing the MCP. Otherwise the worker pool
		// becomes Degraded.
		defer func() {
			_, deletefailure := masterNode.DeleteLabel(infraLabel)

			// We don't fail if there is a problem because we need to delete the infra MCP
			// We will try to remove the label again in the next defer section
			if deletefailure != nil {
				logger.Errorf("Error deleting label '%s' in node '%s'", infraLabel, masterNode.GetName())
			}

			_ = masterNode.WaitForLabelRemoved(infraLabel)

			iMcp.delete()
		}()
		iMcp.create()
		logger.Infof("OK!\n")

		exutil.By("Check that the controller logs are reporting the conflict")
		o.Eventually(
			mcc.GetLogs, "5m", "10s",
		).Should(o.ContainSubstring("Found master node that matches selector for custom pool %s, defaulting to master. This node will not have any custom role configuration as a result. Please review the node to make sure this is intended", iMcpName),
			"The MCO controller is not reporting a machine config pool conflict in the logs")
		logger.Infof("OK!\n")

		exutil.By(`Check that the master node remains in master pool and is moved to "infra" pool or simply removed from master pool`)
		o.Consistently(mMcp.getMachineCount, "30s", "10s").Should(o.Equal(numMasterNodes),
			"The number of machines in the MCP has changed!\n%s", mMcp.PrettyString())

		o.Consistently(iMcp.getMachineCount, "30s", "10s").Should(o.Equal(0),
			"No node should be added to the custom pool!\n%s", iMcp.PrettyString())
		logger.Infof("OK!\n")

		// Check that the expected alert is fired with the right values
		exutil.By(`Check that the right alert was triggered`)

		expectedAlertLabels := expectedAlertValues{"severity": o.Equal(expectedAlertSeverity)}

		expectedAlertAnnotationDescription := fmt.Sprintf("Node .* has triggered a pool alert due to a label change")
		expectedAlertAnnotationSummary := "Triggers when nodes in a pool have overlapping labels such as master, worker, and a custom label therefore a choice must be made as to which is honored."

		expectedAlertAnnotations := expectedAlertValues{
			"description": o.MatchRegexp(expectedAlertAnnotationDescription),
			"summary":     o.Equal(expectedAlertAnnotationSummary),
		}

		params := checkFiredAlertParams{
			expectedAlertName:        expectedAlertName,
			expectedAlertLabels:      expectedAlertLabels,
			expectedAlertAnnotations: expectedAlertAnnotations,
			pendingDuration:          0,
			stillPresentDuration:     0, // We skip this validation to make the test faster
		}
		checkFiredAlert(oc, nil, params)
		logger.Infof("OK!\n")

		exutil.By("Remove the label from the master node in order to fix the problem")
		_, err = masterNode.DeleteLabel(infraLabel)
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Could not delete the %s label in node %s", infraLabel, masterNode)

		o.Expect(
			masterNode.WaitForLabelRemoved(infraLabel),
		).To(o.Succeed(),
			"The label %s was not removed from node %s", infraLabel, masterNode)
		logger.Infof("OK!\n")

		exutil.By("Check that the alert is not triggered anymore")
		checkFixedAlert(oc, coMcp, expectedAlertName)
		logger.Infof("OK!\n")
	})

	g.It("Author:sregidor-NonHyperShiftHOST-NonPreRelease-Medium-73841-KubeletHealthState alert [Disruptive]", func() {
		var (
			node                               = mcp.GetSortedNodesOrFail()[0]
			fixed                              = false
			expectedAlertName                  = "KubeletHealthState"
			expectedAlertSeverity              = "warning"
			expectedAlertAnnotationDescription = "Kubelet health failure threshold reached"
			expectedAlertAnnotationSummary     = "This keeps track of Kubelet health failures, and tallys them. The warning is triggered if 2 or more failures occur."
		)

		exutil.By("Break kubelet")
		// We stop the kubelet service to break the node and after 5 minutes we start it again to fix the node
		go func() {
			defer g.GinkgoRecover()
			_, err := node.DebugNodeWithChroot("sh", "-c", "systemctl stop kubelet.service; sleep 300; systemctl start kubelet.service")
			o.Expect(err).NotTo(o.HaveOccurred(),
				"Error stopping and restarting kubelet in %s", node)
			logger.Infof("Kubelet service has been restarted again")
			fixed = true
		}()
		logger.Infof("OK!\n")

		expectedAlertLabels := expectedAlertValues{
			"severity": o.Equal(expectedAlertSeverity),
		}

		expectedAlertAnnotations := expectedAlertValues{
			"description": o.MatchRegexp(expectedAlertAnnotationDescription),
			"summary":     o.Equal(expectedAlertAnnotationSummary),
		}

		params := checkFiredAlertParams{
			expectedAlertName:        expectedAlertName,
			expectedAlertLabels:      expectedAlertLabels,
			expectedAlertAnnotations: expectedAlertAnnotations,
			pendingDuration:          0,
			stillPresentDuration:     0, // We skip this validation to make the test faster
		}
		checkFiredAlert(oc, nil, params)

		exutil.By("Wait for the kubelet service to be restarted")
		o.Eventually(func() bool { return fixed }, "5m", "20s").Should(o.BeTrue(), "Kubelet service was not restarted")
		o.Eventually(&node).Should(HaveConditionField("Ready", "status", TrueString), "Node %s didn't become ready after kubelet was restarted", node)
		logger.Infof("OK!\n")

		exutil.By("Check that the alert is not triggered anymore")
		checkFixedAlert(oc, mcp, expectedAlertName)
		logger.Infof("OK!\n")
	})

	g.It("Author:sregidor-NonHyperShiftHOST-NonPreRelease-Medium-75862-Add alert for users of deprecating the Image Registry workaround [Disruptive]", func() {

		var (
			expectedAlertName                  = "MCODrainOverrideConfigMapAlert"
			expectedAlertSeverity              = "warning"
			expectedAlertAnnotationDescription = "Image Registry Drain Override configmap has been detected. Please use the Node Disruption Policy feature to control the cluster's drain behavior as the configmap method is currently deprecated and will be removed in a future release."
			expectedAlertAnnotationSummary     = "Alerts the user to the presence of a drain override configmap that is being deprecated and removed in a future release."

			overrideCMName = "image-registry-override-drain"
		)

		exutil.By("Create an image-registry-override-drain configmap")
		defer oc.AsAdmin().Run("delete").Args("configmap", "-n", MachineConfigNamespace, overrideCMName).Execute()
		o.Expect(
			oc.AsAdmin().Run("create").Args("configmap", "-n", MachineConfigNamespace, overrideCMName).Execute(),
		).To(o.Succeed(), "Error creating the image-registry override configmap")
		logger.Infof("OK!\n")

		expectedAlertLabels := expectedAlertValues{
			"severity": o.Equal(expectedAlertSeverity),
		}

		expectedAlertAnnotations := expectedAlertValues{
			"description": o.MatchRegexp(expectedAlertAnnotationDescription),
			"summary":     o.Equal(expectedAlertAnnotationSummary),
		}

		params := checkFiredAlertParams{
			expectedAlertName:        expectedAlertName,
			expectedAlertLabels:      expectedAlertLabels,
			expectedAlertAnnotations: expectedAlertAnnotations,
			pendingDuration:          0,
			stillPresentDuration:     0, // We skip this validation to make the test faster
		}
		checkFiredAlert(oc, nil, params)

		exutil.By("Delete the image-registry-override-drain configmap")
		o.Expect(
			oc.AsAdmin().Run("delete").Args("configmap", "-n", MachineConfigNamespace, overrideCMName).Execute(),
		).To(o.Succeed(), "Error deleting the image-registry override configmap")
		logger.Infof("OK!\n")

		exutil.By("Check that the alert is not triggered anymore")
		checkFixedAlert(oc, mcp, expectedAlertName)
		logger.Infof("OK!\n")

	})

})

type expectedAlertValues map[string]types.GomegaMatcher

type checkFiredAlertParams struct {
	expectedAlertLabels      expectedAlertValues
	expectedAlertAnnotations expectedAlertValues
	// regexp that should match the MCP degraded message
	expectedDegradedMessage string
	expectedAlertName       string
	pendingDuration         time.Duration
	stillPresentDuration    time.Duration
}

func checkFiredAlert(oc *exutil.CLI, mcp *MachineConfigPool, params checkFiredAlertParams) {
	if mcp != nil {
		exutil.By("Wait for MCP to be degraded")
		o.Eventually(mcp,
			"15m", "30s").Should(BeDegraded(),
			"The %s MCP should be degraded when the reboot process is broken. But it didn't.", mcp.GetName())
		logger.Infof("OK!\n")

		exutil.By("Verify that the pool reports the right error message")
		o.Expect(mcp).To(HaveNodeDegradedMessage(o.MatchRegexp(params.expectedDegradedMessage)),
			"The %s MCP is not reporting the right error message", mcp.GetName())
		logger.Infof("OK!\n")
	}

	exutil.By("Verify that the alert is triggered")
	o.Eventually(getAlertsByName, "5m", "20s").WithArguments(oc, params.expectedAlertName).
		Should(o.HaveLen(1),
			"Expected 1 %s alert and only 1 to be triggered!", params.expectedAlertName)

	alertJSON, err := getAlertsByName(oc, params.expectedAlertName)
	o.Expect(err).NotTo(o.HaveOccurred(),
		"Error trying to get the %s alert", params.expectedAlertName)

	logger.Infof("Found %s alerts: %s", params.expectedAlertName, alertJSON)
	alertMap := alertJSON[0].ToMap()
	annotationsMap := alertJSON[0].Get("annotations").ToMap()
	logger.Infof("OK!\n")

	if params.expectedAlertAnnotations != nil {
		exutil.By("Verify alert's annotations")

		// Check all expected annotations
		for annotation, expectedMatcher := range params.expectedAlertAnnotations {
			logger.Infof("Verifying annotation: %s", annotation)
			o.Expect(annotationsMap).To(o.HaveKeyWithValue(annotation, expectedMatcher),
				"The alert is reporting a wrong '%s' annotation value", annotation)
		}
		logger.Infof("OK!\n")
	} else {
		logger.Infof("No annotations checks needed!")
	}

	exutil.By("Verify alert's labels")
	labelsMap := alertJSON[0].Get("labels").ToMap()

	// Since OCPBUGS-904 we need to check that the namespace is reported properly in all the alerts
	o.Expect(labelsMap).To(o.HaveKeyWithValue("namespace", MachineConfigNamespace),
		"Expected the alert to report the MCO namespace")

	if params.expectedAlertLabels != nil {
		// Check all expected labels
		for label, expectedMatcher := range params.expectedAlertLabels {
			logger.Infof("Verifying label: %s", label)
			o.Expect(labelsMap).To(o.HaveKeyWithValue(label, expectedMatcher),
				"The alert is reporting a wrong '%s' label value", label)
		}
	} else {
		logger.Infof("No extra labels checks needed!")
	}

	logger.Infof("OK!\n")

	if params.pendingDuration != 0 {
		exutil.By("Verify that the alert is pending")
		o.Expect(alertMap).To(o.HaveKeyWithValue("state", "pending"),
			"Expected the alert's state to be 'pending', but it is not.")
		logger.Infof("OK!\n")
	}

	exutil.By("Verify that the alert is in firing state")
	if params.pendingDuration != 0 {
		logger.Infof("Wait %s minutes until the alert is fired", params.pendingDuration)
		time.Sleep(params.pendingDuration)
	}

	logger.Infof("Checking alert's state")
	alertJSON, err = getAlertsByName(oc, params.expectedAlertName)
	o.Expect(err).NotTo(o.HaveOccurred(),
		"Error trying to get the %s alert", params.expectedAlertName)

	logger.Infof("Found %s alerts: %s", params.expectedAlertName, alertJSON)

	alertMap = alertJSON[0].ToMap()
	o.Expect(alertMap).To(o.HaveKeyWithValue("state", "firing"),
		"Expected the alert to report 'firing' state")

	logger.Infof("OK!\n")

	if params.stillPresentDuration.Minutes() != 0 {
		exutil.By(fmt.Sprintf("Verfiy that the alert is not removed after %s", params.stillPresentDuration))
		o.Consistently(getAlertsByName, params.stillPresentDuration, params.stillPresentDuration/3).WithArguments(oc, params.expectedAlertName).
			Should(o.HaveLen(1),
				"Expected %s alert to be present, but the alert was removed for no reason!", params.expectedAlertName)
		logger.Infof("OK!\n")
	}

}

func checkFixedAlert(oc *exutil.CLI, mcp *MachineConfigPool, expectedAlertName string) {
	exutil.By("Verfiy that the pool stops being degraded")
	o.Eventually(mcp,
		"10m", "30s").ShouldNot(BeDegraded(),
		"After fixing the reboot process the %s MCP should stop being degraded", mcp.GetName())
	logger.Infof("OK!\n")

	exutil.By("Verfiy that the alert is not triggered anymore")
	o.Eventually(getAlertsByName, "5m", "20s").WithArguments(oc, expectedAlertName).
		Should(o.HaveLen(0),
			"Expected %s alert to be removed after the problem is fixed!", expectedAlertName)
	logger.Infof("OK!\n")
}
