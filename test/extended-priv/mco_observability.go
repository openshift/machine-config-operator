package extended

import (
	"fmt"
	"strings"

	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"

	"github.com/tidwall/gjson"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO Observability", func() {
	defer g.GinkgoRecover()

	var oc = exutil.NewCLI("mco", exutil.KubeConfigPath())

	g.JustBeforeEach(func() {
		PreChecks(oc)
	})

	g.It("[PolarionID:43151][OTP] add node label to service monitor", func() {
		exutil.By("Get current mcd_ metrics from machine-config-daemon service")

		svcMCD := NewNamespacedResource(oc.AsAdmin(), "service", MachineConfigNamespace, MachineConfigDaemon)
		clusterIP, ipErr := WrapWithBracketsIfIpv6(svcMCD.GetOrFail("{.spec.clusterIP}"))
		o.Expect(ipErr).ShouldNot(o.HaveOccurred(), "No valid IP")
		port := svcMCD.GetOrFail("{.spec.ports[?(@.name==\"metrics\")].port}")

		token := getSATokenFromContainer(oc, "prometheus-k8s-0", "openshift-monitoring", "prometheus")

		statsCmd := fmt.Sprintf(`HTTPS_PROXY="" curl -s -k  -H 'Authorization: Bearer %s' https://%s:%s/metrics | grep 'mcd_' | grep -v '#'`, token, clusterIP, port)
		logger.Infof("command: %s", statsCmd)
		logger.Infof("stats output:\n %s", statsCmd)

		o.Eventually(exutil.RemoteShPod, "5m", "20s").WithArguments(oc, "openshift-monitoring", "prometheus-k8s-0", "sh", "-c", statsCmd).Should(o.And(
			o.ContainSubstring("mcd_host_os_and_version"),
			o.ContainSubstring("mcd_kubelet_state"),
			o.ContainSubstring("mcd_pivot_errors_total"),
			o.ContainSubstring("mcd_reboots_failed_total"),
			o.ContainSubstring("mcd_state"),
			o.ContainSubstring("mcd_update_state"),
			o.ContainSubstring("mcd_update_state"),
		))

		exutil.By("Check relabeling section in machine-config-daemon")
		sourceLabels, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("servicemonitor/machine-config-daemon", "-n", MachineConfigNamespace,
			"-o", "jsonpath='{.spec.endpoints[*].relabelings[*].sourceLabels}'").Output()
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(sourceLabels).Should(o.ContainSubstring("__meta_kubernetes_pod_node_name"))

		exutil.By("Check node label in mcd_state metrics")
		stateQuery := getPrometheusQueryResults(oc, "mcd_state")
		logger.Infof("metrics:\n %s", stateQuery)
		firstMasterNode := NewNodeList(oc.AsAdmin()).GetAllMasterNodesOrFail()[0]
		firstWorkerNode := NewNodeList(oc.AsAdmin()).GetAllLinuxWorkerNodesOrFail()[0]
		o.Expect(stateQuery).Should(o.ContainSubstring(`"node":"` + firstMasterNode.name + `"`))
		o.Expect(stateQuery).Should(o.ContainSubstring(`"node":"` + firstWorkerNode.name + `"`))
	})

	g.It("[PolarionID:46424][OTP] Check run level", func() {
		exutil.By("Validate openshift-machine-config-operator run level")
		mcoNs := NewResource(oc.AsAdmin(), "ns", MachineConfigNamespace)
		runLevel := mcoNs.GetOrFail(`{.metadata.labels.openshift\.io/run-level}`)

		logger.Debugf("Namespace definition:\n%s", mcoNs.PrettyString())
		o.Expect(runLevel).To(o.Equal(""), `openshift-machine-config-operator namespace should have run-level annotation equal to ""`)

		exutil.By("Validate machine-config-operator SCC")
		podsList := NewNamespacedResourceList(oc.AsAdmin(), "pods", mcoNs.name)
		podsList.ByLabel("k8s-app=machine-config-operator")
		mcoPods, err := podsList.GetAll()
		o.Expect(err).NotTo(o.HaveOccurred())
		logger.Infof("Validating that there is only one machine-config-operator pod")
		o.Expect(mcoPods).To(o.HaveLen(1))
		mcoPod := mcoPods[0]
		scc := mcoPod.GetOrFail(`{.metadata.annotations.openshift\.io/scc}`)

		logger.Infof("Validating that the operator pod has the right SCC")
		logger.Debugf("Machine-config-operator pod definition:\n%s", mcoPod.PrettyString())
		// on baremetal cluster, value of openshift.io/scc is nfs-provisioner, on AWS cluster it is hostmount-anyuid
		o.Expect(scc).Should(o.SatisfyAny(o.Equal("hostmount-anyuid"), o.Equal("nfs-provisioner"), o.Equal("anyuid")),
			`machine-config-operator pod is not using the right SCC`)

		exutil.By("Validate machine-config-daemon clusterrole")
		mcdCR := NewResource(oc.AsAdmin(), "clusterrole", "machine-config-daemon")
		mcdRules := mcdCR.GetOrFail(`{.rules[?(@.apiGroups[0]=="security.openshift.io")]}`)

		logger.Debugf("Machine-config-operator clusterrole definition:\n%s", mcdCR.PrettyString())
		o.Expect(mcdRules).Should(o.ContainSubstring("privileged"),
			`machine-config-daemon clusterrole has not the right configuration for ApiGroup "security.openshift.io"`)

		exutil.By("Validate machine-config-server clusterrole")
		mcsCR := NewResource(oc.AsAdmin(), "clusterrole", "machine-config-server")
		mcsRules := mcsCR.GetOrFail(`{.rules[?(@.apiGroups[0]=="security.openshift.io")]}`)
		logger.Debugf("Machine-config-server clusterrole definition:\n%s", mcdCR.PrettyString())
		o.Expect(mcsRules).Should(o.ContainSubstring("hostnetwork"),
			`machine-config-server clusterrole has not the right configuration for ApiGroup "security.openshift.io"`)

	})

	g.It("[PolarionID:51219][OTP] Check ClusterRole rules", func() {
		expectedServiceAcc := MachineConfigDaemon
		eventsRoleBinding := MachineConfigDaemonEvents
		eventsClusterRole := MachineConfigDaemonEvents
		daemonClusterRoleBinding := MachineConfigDaemon
		daemonClusterRole := MachineConfigDaemon

		exutil.By(fmt.Sprintf("Check %s service account", expectedServiceAcc))
		serviceAccount := NewNamespacedResource(oc.AsAdmin(), "ServiceAccount", MachineConfigNamespace, expectedServiceAcc)
		o.Expect(serviceAccount.Exists()).To(o.BeTrue(), "Service account %s should exist in namespace %s", expectedServiceAcc, MachineConfigNamespace)

		exutil.By("Check service accounts in daemon pods")
		checkNodePermissions := func(node *Node) {
			daemonPodName := node.GetMachineConfigDaemon()
			logger.Infof("Checking permissions in daemon pod %s", daemonPodName)
			daemonPod := NewNamespacedResource(node.oc, "pod", MachineConfigNamespace, daemonPodName)

			o.Expect(daemonPod.GetOrFail(`{.spec.serviceAccount}`)).Should(o.Equal(expectedServiceAcc),
				"Pod %s should use service account: %s", daemonPodName, expectedServiceAcc)

			o.Expect(daemonPod.GetOrFail(`{.spec.serviceAccountName}`)).Should(o.Equal(expectedServiceAcc),
				"Pod %s should use service account name: %s", daemonPodName, expectedServiceAcc)

		}
		nodes, err := NewNodeList(oc.AsAdmin()).GetAllLinux()
		o.Expect(err).ShouldNot(o.HaveOccurred(), "Error getting the list of nodes")
		for _, node := range nodes {
			exutil.By(fmt.Sprintf("Checking node %s", node.GetName()))
			checkNodePermissions(node)
		}

		exutil.By("Check events rolebindings in default namespace")
		defaultEventsRoleBindings := NewNamespacedResource(oc.AsAdmin(), "RoleBinding", "default", "machine-config-daemon-events")
		o.Expect(defaultEventsRoleBindings.Exists()).Should(o.BeTrue(), "'%s' Rolebinding not found in 'default' namespace", eventsRoleBinding)
		// Check the bound SA
		machineConfigSubjectJSON := defaultEventsRoleBindings.GetOrFail(fmt.Sprintf(`{.subjects[?(@.name=="%s")]}`, expectedServiceAcc))
		o.Expect(gjson.Get(machineConfigSubjectJSON, "name").String()).Should(o.Equal(expectedServiceAcc),
			"'%s' in 'default' namespace should bind %s SA in namespace %s", eventsRoleBinding, expectedServiceAcc, MachineConfigNamespace)
		o.Expect(gjson.Get(machineConfigSubjectJSON, "namespace").String()).Should(o.Equal(MachineConfigNamespace),
			"'%s' in 'default' namespace should bind %s SA in namespace %s", eventsRoleBinding, expectedServiceAcc, MachineConfigNamespace)

		// Check the ClusterRole
		machineConfigClusterRoleJSON := defaultEventsRoleBindings.GetOrFail(`{.roleRef}`)
		o.Expect(gjson.Get(machineConfigClusterRoleJSON, "kind").String()).Should(o.Equal("ClusterRole"),
			"'%s' in 'default' namespace should bind a ClusterRole", eventsRoleBinding)
		o.Expect(gjson.Get(machineConfigClusterRoleJSON, "name").String()).Should(o.Equal(eventsClusterRole),
			"'%s' in 'default' namespace should bind %s ClusterRole", eventsRoleBinding, eventsClusterRole)

		exutil.By(fmt.Sprintf("Check events rolebindings in %s namespace", MachineConfigNamespace))
		mcoEventsRoleBindings := NewNamespacedResource(oc.AsAdmin(), "RoleBinding", MachineConfigNamespace, "machine-config-daemon-events")
		o.Expect(defaultEventsRoleBindings.Exists()).Should(o.BeTrue(), "'%s' Rolebinding not found in '%s' namespace", eventsRoleBinding, MachineConfigNamespace)
		// Check the bound SA
		machineConfigSubjectJSON = mcoEventsRoleBindings.GetOrFail(fmt.Sprintf(`{.subjects[?(@.name=="%s")]}`, expectedServiceAcc))
		o.Expect(gjson.Get(machineConfigSubjectJSON, "name").String()).Should(o.Equal(expectedServiceAcc),
			"'%s' in '%s' namespace should bind %s SA in namespace %s", eventsRoleBinding, MachineConfigNamespace, expectedServiceAcc, MachineConfigNamespace)
		o.Expect(gjson.Get(machineConfigSubjectJSON, "namespace").String()).Should(o.Equal(MachineConfigNamespace),
			"'%s' in '%s' namespace should bind %s SA in namespace %s", eventsRoleBinding, MachineConfigNamespace, expectedServiceAcc, MachineConfigNamespace)

		// Check the ClusterRole
		machineConfigClusterRoleJSON = mcoEventsRoleBindings.GetOrFail(`{.roleRef}`)
		o.Expect(gjson.Get(machineConfigClusterRoleJSON, "kind").String()).Should(o.Equal("ClusterRole"),
			"'%s' in '%s' namespace should bind a ClusterRole", eventsRoleBinding, MachineConfigNamespace)
		o.Expect(gjson.Get(machineConfigClusterRoleJSON, "name").String()).Should(o.Equal(eventsClusterRole),
			"'%s' in '%s' namespace should bind %s CLusterRole", eventsRoleBinding, MachineConfigNamespace, eventsClusterRole)

		exutil.By(fmt.Sprintf("Check MCO cluseterrolebindings in %s namespace", MachineConfigNamespace))
		mcoCRB := NewResource(oc.AsAdmin(), "ClusterRoleBinding", daemonClusterRoleBinding)
		o.Expect(mcoCRB.Exists()).Should(o.BeTrue(), "'%s' ClusterRolebinding not found.", daemonClusterRoleBinding)
		// Check the bound SA
		machineConfigSubjectJSON = mcoCRB.GetOrFail(fmt.Sprintf(`{.subjects[?(@.name=="%s")]}`, expectedServiceAcc))
		o.Expect(gjson.Get(machineConfigSubjectJSON, "name").String()).Should(o.Equal(expectedServiceAcc),
			"'%s' ClusterRoleBinding should bind %s SA in namespace %s", daemonClusterRoleBinding, expectedServiceAcc, MachineConfigNamespace)
		o.Expect(gjson.Get(machineConfigSubjectJSON, "namespace").String()).Should(o.Equal(MachineConfigNamespace),
			"'%s' ClusterRoleBinding should bind %s SA in namespace %s", daemonClusterRoleBinding, expectedServiceAcc, MachineConfigNamespace)

		// Check the ClusterRole
		machineConfigClusterRoleJSON = mcoCRB.GetOrFail(`{.roleRef}`)
		o.Expect(gjson.Get(machineConfigClusterRoleJSON, "kind").String()).Should(o.Equal("ClusterRole"),
			"'%s' ClusterRoleBinding should bind a ClusterRole", daemonClusterRoleBinding)
		o.Expect(gjson.Get(machineConfigClusterRoleJSON, "name").String()).Should(o.Equal(daemonClusterRole),
			"'%s' ClusterRoleBinding should bind %s CLusterRole", daemonClusterRoleBinding, daemonClusterRole)

		exutil.By("Check events clusterrole")
		eventsCR := NewResource(oc.AsAdmin(), "ClusterRole", eventsClusterRole)
		o.Expect(eventsCR.Exists()).To(o.BeTrue(), "ClusterRole %s should exist", eventsClusterRole)

		stringRules := eventsCR.GetOrFail(`{.rules}`)
		o.Expect(stringRules).ShouldNot(o.ContainSubstring("pod"),
			"ClusterRole %s should grant no pod permissions at all", eventsClusterRole)

		rulesArray := gjson.Parse(stringRules).Array()
		for _, rule := range rulesArray {
			describesEvents := false
			resourcesArray := rule.Get("resources").Array()
			for _, resource := range resourcesArray {
				if resource.String() == "events" {
					describesEvents = true
				}
			}

			if describesEvents {
				verbs := ToInterfaceSlice(rule.Get("verbs"))
				o.Expect(verbs).Should(o.ContainElement("create"), "In ClusterRole %s 'events' rule should have 'create' permissions", eventsClusterRole)
				o.Expect(verbs).Should(o.ContainElement("patch"), "In ClusterRole %s 'events' rule should have 'patch' permissions", eventsClusterRole)
				o.Expect(verbs).Should(o.HaveLen(2), "In ClusterRole %s 'events' rule should ONLY Have 'create' and 'patch' permissions", eventsClusterRole)
			}
		}

		exutil.By("Check daemon clusterrole")
		daemonCR := NewResource(oc.AsAdmin(), "ClusterRole", daemonClusterRole)
		stringRules = daemonCR.GetOrFail(`{.rules}`)
		o.Expect(stringRules).ShouldNot(o.ContainSubstring("pod"),
			"ClusterRole %s should grant no pod permissions at all", daemonClusterRole)
		o.Expect(stringRules).ShouldNot(o.ContainSubstring("daemonsets"),
			"ClusterRole %s should grant no daemonsets permissions at all", daemonClusterRole)

		rulesArray = gjson.Parse(stringRules).Array()
		for _, rule := range rulesArray {
			describesNodes := false
			resourcesArray := rule.Get("resources").Array()
			for _, resource := range resourcesArray {
				if resource.String() == "nodes" {
					describesNodes = true
				}
			}

			if describesNodes {
				verbs := ToInterfaceSlice(rule.Get("verbs"))
				o.Expect(verbs).Should(o.ContainElement("get"), "In ClusterRole %s 'nodes' rule should have 'get' permissions", daemonClusterRole)
				o.Expect(verbs).Should(o.ContainElement("list"), "In ClusterRole %s 'nodes' rule should have 'list' permissions", daemonClusterRole)
				o.Expect(verbs).Should(o.ContainElement("watch"), "In ClusterRole %s 'nodes' rule should have 'watch' permissions", daemonClusterRole)
				o.Expect(verbs).Should(o.HaveLen(3), "In ClusterRole %s 'events' rule should ONLY Have 'get', 'list' and 'watch' permissions", daemonClusterRole)
			}
		}

	})

	g.It("[PolarionID:54937][OTP] logs and events are flood with clusterrole and clusterrolebinding", func() {

		exutil.By("get machine-config-operator pod name")
		mcoPod, getMcoPodErr := getMachineConfigOperatorPod(oc)
		o.Expect(getMcoPodErr).NotTo(o.HaveOccurred(), "get mco pod failed")

		if exutil.CheckPlatform(oc) == "vsphere" { // check platformStatus.VSphere related log on vpshere cluster only
			exutil.By("check infra/cluster info, make sure platformStatus.VSphere does not exist")
			infra := NewResource(oc.AsAdmin(), "infrastructure", "cluster")
			vsphereStatus, getStatusErr := infra.Get(`{.status.platformStatus.VSphere}`)
			o.Expect(getStatusErr).NotTo(o.HaveOccurred(), "check vsphere status failed")
			// check vsphereStatus exists or not, only check logs if it exists, otherwise skip the test
			if vsphereStatus == "" {
				exutil.By("check vsphere related log in machine-config-operator pod")
				filteredVsphereLog, filterVsphereLogErr := exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigOperator, mcoPod, "PlatformStatus.VSphere")
				// if no platformStatus.Vsphere log found, the func will return error, that's expected
				logger.Debugf("filtered vsphere log:\n %s", filteredVsphereLog)
				o.Expect(filterVsphereLogErr).Should(o.HaveOccurred(), "found vsphere related log in mco pod")
			}
		}

		// check below logs for all platforms
		exutil.By("check clusterrole and clusterrolebinding related logs in machine-config-operator pod")
		filteredClusterRoleLog, filterClusterRoleLogErr := exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigOperator, mcoPod, "ClusterRoleUpdated")
		logger.Debugf("filtered clusterrole log:\n %s", filteredClusterRoleLog)
		o.Expect(filterClusterRoleLogErr).Should(o.HaveOccurred(), "found ClusterRoleUpdated log in mco pod")

		filteredClusterRoleBindingLog, filterClusterRoleBindingLogErr := exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigOperator, mcoPod, "ClusterRoleBindingUpdated")
		logger.Debugf("filtered clusterrolebinding log:\n %s", filteredClusterRoleBindingLog)
		o.Expect(filterClusterRoleBindingLogErr).Should(o.HaveOccurred(), "found ClusterRoleBindingUpdated log in mco pod")

	})

	g.It("[PolarionID:54974][OTP] silence audit log events for container infra", func() {

		auditRuleFile := "/etc/audit/rules.d/mco-audit-quiet-containers.rules"
		auditLogFile := "/var/log/audit/audit.log"

		allCoreOsNodes := NewNodeList(oc.AsAdmin()).GetAllCoreOsNodesOrFail()
		for _, node := range allCoreOsNodes {
			if node.HasTaintEffectOrFail("NoExecute") {
				logger.Infof("Node %s is tainted with 'NoExecute'. Validation skipped.", node.GetName())
				continue
			}

			exutil.By(fmt.Sprintf("log into node %s to check audit rule file exists or not", node.GetName()))
			o.Expect(node.DebugNodeWithChroot("stat", auditRuleFile)).ShouldNot(
				o.ContainSubstring("No such file or directory"),
				"The audit rules file %s should exist in the nodes", auditRuleFile)

			exutil.By("check expected msgtype in audit log rule file")
			grepOut, _ := node.DebugNodeWithOptions([]string{"--quiet"}, "chroot", "/host", "bash", "-c", fmt.Sprintf("grep -E 'NETFILTER_CFG|ANOM_PROMISCUOUS' %s", auditRuleFile))

			o.Expect(grepOut).NotTo(o.BeEmpty(), "expected excluded audit log msgtype not found")
			o.Expect(grepOut).Should(o.And(
				o.ContainSubstring("NETFILTER_CFG"),
				o.ContainSubstring("ANOM_PROMISCUOUS"),
			), "audit log rules does not have excluded msstype NETFILTER_CFG and ANOM_PROMISCUOUS")

			exutil.By(fmt.Sprintf("check audit log on node %s, make sure msg types NETFILTER_CFG and ANOM_PROMISCUOUS are excluded", node.GetName()))
			filteredLog, _ := node.DebugNodeWithChroot("bash", "-c", fmt.Sprintf("grep -E 'NETFILTER_CFG|ANOM_PROMISCUOUS' %s", auditLogFile))
			o.Expect(filteredLog).ShouldNot(o.Or(
				o.ContainSubstring("NETFILTER_CFG"),
				o.ContainSubstring("ANOM_PROMISCUOUS"),
			), "audit log contains excluded msgtype NETFILTER_CFG or ANOM_PROMISCUOUS")
		}
	})

	g.It("[PolarionID:56706][OTP] Move MCD drain alert into the MCC, revisit error mode", func() {
		var (
			mcp               = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			mcc               = NewController(oc.AsAdmin())
			nsName            = oc.Namespace()
			pdbName           = "dont-evict-43279"
			podName           = "dont-evict-43279"
			podTemplate       = generateTemplateAbsolutePath("create-pod.yaml")
			mcName            = "test-file"
			mcTemplate        = "add-mc-to-trigger-node-drain.yaml"
			expectedAlertName = "MCCDrainError"
		)
		// Get the first node that will be updated
		// Not all nodes are valid. We need to deploy the "dont-evict-pod" and we can only do that in schedulable nodes
		// In "edge" clusters, the "edge" nodes are not schedulable, so we need to be careful and not to use them to deploy our pod
		schedulableNodes := FilterSchedulableNodesOrFail(mcp.GetSortedNodesOrFail())
		o.Expect(schedulableNodes).NotTo(o.BeEmpty(), "There are no schedulable worker nodes!!")
		workerNode := schedulableNodes[0]

		exutil.By("Start machine-config-controller logs capture")
		ignoreMccLogErr := mcc.IgnoreLogsBeforeNow()
		o.Expect(ignoreMccLogErr).NotTo(o.HaveOccurred(), "Ignore mcc log failed")
		logger.Infof("OK!\n")

		exutil.By("Create a pod disruption budget to set minAvailable to 1")
		pdbTemplate := generateTemplateAbsolutePath("pod-disruption-budget.yaml")
		pdb := PodDisruptionBudget{name: pdbName, namespace: nsName, template: pdbTemplate}
		defer pdb.delete(oc)
		pdb.create(oc)
		logger.Infof("OK!\n")

		exutil.By("Create new pod for pod disruption budget")
		hostname, err := workerNode.GetNodeHostname()
		o.Expect(err).NotTo(o.HaveOccurred())
		pod := exutil.Pod{Name: podName, Namespace: nsName, Template: podTemplate, Parameters: []string{"HOSTNAME=" + hostname}}
		defer func() { o.Expect(pod.Delete(oc)).NotTo(o.HaveOccurred()) }()
		pod.Create(oc)
		logger.Infof("OK!\n")

		exutil.By("Create new mc to add new file on the node and trigger node drain")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker).SetMCOTemplate(mcTemplate)
		mc.skipWaitForMcp = true
		defer mc.DeleteWithWait()
		defer func() {
			_ = pod.Delete(oc)
			mcp.WaitForNotDegradedStatus()
		}()
		mc.create()
		logger.Infof("OK!\n")

		exutil.By("Wait until node is cordoned")
		o.Eventually(workerNode.Poll(`{.spec.taints[?(@.effect=="NoSchedule")].effect}`),
			"20m", "1m").Should(o.Equal("NoSchedule"), fmt.Sprintf("Node %s was not cordoned", workerNode.name))
		logger.Infof("OK!\n")

		exutil.By("Verify that node is not degraded until the alarm timeout")
		o.Consistently(mcp.pollDegradedStatus(),
			"58m", "5m").Should(o.Equal("False"),
			"The worker MCP was degraded too soon. The worker MCP should not be degraded until 1 hour timeout happens")
		logger.Infof("OK!\n")

		exutil.By("Verify that node is degraded after the 1h timeout")
		o.Eventually(mcp.pollDegradedStatus(),
			"5m", "1m").Should(o.Equal("True"),
			"1 hour passed since the eviction problems were reported and the worker MCP has not been degraded")
		logger.Infof("OK!\n")

		exutil.By("Verify that the error is properly reported in the controller pod's logs")
		logger.Debugf("CONTROLLER LOGS BEGIN!\n")
		logger.Debugf(mcc.GetFilteredLogs(workerNode.GetName()))
		logger.Debugf("CONTROLLER LOGS END!\n")

		o.Expect(mcc.GetFilteredLogs(workerNode.GetName())).Should(
			o.ContainSubstring("node %s: drain exceeded timeout: 1h0m0s. Will continue to retry.",
				workerNode.GetName()),
			"The eviction problem is not properly reported in the MCController pod logs")
		logger.Infof("OK!\n")

		exutil.By("Verify that the error is properly reported in the MachineConfigPool status")
		nodeDegradedCondition := mcp.GetConditionByType("NodeDegraded")
		nodeDegradedConditionResult := gjson.Parse(nodeDegradedCondition)
		nodeDegradedMessage := nodeDegradedConditionResult.Get("message").String()
		expectedDegradedNodeMessage := fmt.Sprintf("failed to drain node: %s after 1 hour. Please see machine-config-controller logs for more information", workerNode.GetName())

		logger.Infof("MCP NodeDegraded condition: %s", nodeDegradedCondition)
		o.Expect(nodeDegradedMessage).To(o.ContainSubstring(expectedDegradedNodeMessage),
			"The error reported in the MCP NodeDegraded condition in not the expected one")
		logger.Infof("OK!\n")

		exutil.By("Verify that the alert is triggered")
		var alertJSON []map[string]interface{}
		var alertErr error
		o.Eventually(func() ([]map[string]interface{}, error) {
			alertJSON, alertErr = getAlertsByName(oc, expectedAlertName)
			return alertJSON, alertErr
		}, "5m", "20s").Should(o.HaveLen(1),
			"Expected 1 %s alert and only 1 to be triggered!", expectedAlertName)
		logger.Infof("OK!\n")

		exutil.By("Verify that the alert has the right message")
		logger.Infof("Found %s alerts: %v", expectedAlertName, alertJSON)

		alert := alertJSON[0]
		annotationsMap := alert["annotations"].(map[string]interface{})
		labelsMap := alert["labels"].(map[string]interface{})

		expectedDescription := fmt.Sprintf("Drain failed on %s , updates may be blocked. For more details check MachineConfigController pod logs: oc logs -f -n openshift-machine-config-operator machine-config-controller-xxxxx -c machine-config-controller", workerNode.GetName())
		o.Expect(annotationsMap).To(o.HaveKeyWithValue("description", o.ContainSubstring(expectedDescription)),
			"The error description should make a reference to the pod info")

		expectedSummary := "Alerts the user to a failed node drain. Always triggers when the failure happens one or more times."
		o.Expect(annotationsMap).To(o.HaveKeyWithValue("summary", expectedSummary),
			"The alert has a wrong 'summary' annotation value")

		// Since OCPBUGS-904 we need to check that the namespace is reported properly
		o.Expect(labelsMap).To(o.HaveKeyWithValue("namespace", MachineConfigNamespace),
			"The alert's namespace has not the right value")
		logger.Infof("OK!\n")

		exutil.By("Remove the  pod disruption budget")
		pdb.delete(oc)
		logger.Infof("OK!\n")

		exutil.By("Verfiy that the pool stops being degraded")
		o.Eventually(mcp.pollDegradedStatus(),
			"10m", "30s").Should(o.Equal("False"),
			"After removing the PodDisruptionBudget the eviction should have succeeded and the worker pool should stop being degraded")
		logger.Infof("OK!\n")

		exutil.By("Verfiy that the alert is not triggered anymore")
		o.Eventually(getAlertsByName, "5m", "20s").WithArguments(oc, expectedAlertName).
			Should(o.HaveLen(0),
				"Alert is not removed after the problem is fixed!")
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:68683][OTP] nodelogs feature works fine", func() {
		var (
			wMcp       = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			workerNode = wMcp.GetNodesOrFail()[0]
			mMcp       = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
			masterNode = mMcp.GetNodesOrFail()[0]
		)
		verifyCmd := func(node *Node) {
			exutil.By(fmt.Sprintf("Check that the node-logs cmd work for %s node", node.name))
			nodeLogs, err := oc.AsAdmin().WithoutNamespace().Run("adm").Args("node-logs", node.name, "--tail=20").Output()
			o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("Cannot get the node-logs cmd for %s node", node.name))
			o.Expect(len(strings.Split(nodeLogs, "\n"))).To(o.BeNumerically(">=", 5)) // check the logs line are greater than 5
		}
		verifyCmd(workerNode)
		verifyCmd(masterNode)

	})
})
