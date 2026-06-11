package extended

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO", func() {
	defer g.GinkgoRecover()

	var oc = exutil.NewCLI("mco", exutil.KubeConfigPath())

	g.JustBeforeEach(func() {
		PreChecks(oc)
	})

	g.It("[PolarionID:43048][PolarionID:43064][OTP][LEVEL0] create/delete custom machine config pool", func() {
		if IsCompactOrSNOCluster(oc) {
			g.Skip("This test case cannot be executed in SNO or Compact clusters")
		}

		mcpName := "infra"

		exutil.By("get worker node to change the label")
		wMcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		allWorkers := wMcp.GetNodesOrFail()
		workerNode := allWorkers[0]
		initialNumWorkers := len(allWorkers)
		logger.Infof("OK!\n")

		exutil.By("Create custom infra mcp")
		mcp, err := CreateCustomMCP(oc.AsAdmin(), mcpName, 0)
		defer DeleteCustomMCP(oc.AsAdmin(), mcpName)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a custom MCP")
		logger.Infof("OK!\n")

		exutil.By("Check MCP status")
		o.Consistently(mcp.pollDegradedMachineCount(), "30s", "10s").Should(o.Equal("0"), "There are degraded nodes in pool")
		o.Eventually(mcp.pollDegradedStatus(), "1m", "20s").Should(o.Equal("False"), "The pool status is 'Degraded'")
		o.Eventually(mcp.pollUpdatedStatus(), "1m", "20s").Should(o.Equal("True"), "The pool is reporting that it is not updated")
		o.Eventually(mcp.pollMachineCount(), "1m", "10s").Should(o.Equal("0"), "The pool should report 0 machine count")
		o.Eventually(mcp.pollReadyMachineCount(), "1m", "10s").Should(o.Equal("0"), "The pool should report 0 machine ready")
		o.Eventually(wMcp.pollMachineCount(), "1m", "10s").Should(o.Equal(strconv.Itoa(initialNumWorkers)),
			"The worker pool should report %d machine count", initialNumWorkers)

		logger.Infof("Custom mcp is created successfully!")
		logger.Infof("OK!\n")

		exutil.By("Add label as infra to the existing node")
		infraLabel := "node-role.kubernetes.io/infra"
		o.Expect(workerNode.AddLabel(infraLabel, "")).To(o.Succeed())
		nodeLabel, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("nodes/" + workerNode.name).Output()
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(nodeLabel).Should(o.ContainSubstring("infra"))
		logger.Infof("OK!\n")

		exutil.By("Check MCP status")
		o.Consistently(mcp.pollDegradedMachineCount(), "30s", "10s").Should(o.Equal("0"), "There are degraded nodes in pool")
		o.Eventually(mcp.pollDegradedStatus(), "1m", "20s").Should(o.Equal("False"), "The pool status is 'Degraded'")
		o.Eventually(mcp.pollUpdatedStatus(), "1m", "20s").Should(o.Equal("True"), "The pool is reporting that it is not updated")
		o.Eventually(mcp.pollMachineCount(), "1m", "10s").Should(o.Equal("1"), "The pool should report 1 machine count")
		o.Eventually(mcp.pollReadyMachineCount(), "1m", "10s").Should(o.Equal("1"), "The pool should report 1 machine ready")
		o.Eventually(wMcp.pollMachineCount(), "1m", "10s").Should(o.Equal(strconv.Itoa(initialNumWorkers-1)),
			"The worker pool should report %d machine count", initialNumWorkers-1)

		logger.Infof("Custom mcp is created successfully!")
		logger.Infof("OK!\n")

		exutil.By("Remove custom label from the node")
		o.Expect(workerNode.RemoveLabel(infraLabel)).To(o.Succeed(), "Error removing the infra label from %s", workerNode)
		logger.Infof("Label removed")
		logger.Infof("OK!\n")

		exutil.By("Check that the node was properly returned to the worker pool")
		wMcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Verify that the information is updated in MCP")
		o.Eventually(mcp.pollUpdatedStatus(), "5m", "20s").Should(o.Equal("True"), "The pool is reporting that it is not updated")
		o.Eventually(mcp.pollMachineCount(), "5m", "20s").Should(o.Equal("0"), "The pool should report 0 machine count")
		o.Eventually(mcp.pollReadyMachineCount(), "5m", "20s").Should(o.Equal("0"), "The pool should report 0 machine ready")
		o.Consistently(mcp.pollDegradedMachineCount(), "30s", "10s").Should(o.Equal("0"), "There are degraded nodes in pool")
		o.Eventually(mcp.pollDegradedStatus(), "5m", "20s").Should(o.Equal("False"), "The pool status is 'Degraded'")
		logger.Infof("OK!\n")

		exutil.By("Remove custom infra mcp")
		mcp.delete()
		logger.Infof("OK!\n")

		exutil.By("Check custom infra mcp is deleted")
		mcpOut, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("mcp/" + mcpName).Output()
		o.Expect(err).Should(o.HaveOccurred())
		o.Expect(mcpOut).Should(o.ContainSubstring("NotFound"))
		logger.Infof("Custom mcp is deleted successfully!")
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

	g.It("[PolarionID:42390][PolarionID:45318][OTP] add machine config without ignition version. Block the Machine-Config-Operator upgrade rollout if any of the pools are Degraded", func() {
		createMcAndVerifyIgnitionVersion(oc, "empty ign version", "change-worker-ign-version-to-empty", "")
	})

	g.It("[PolarionID:52373][OTP] Modify proxy configuration in paused pools", func() {

		proxyValue := "http://user:pass@proxy-fake:1111"
		noProxyValue := "test.52373.no-proxy.com"

		exutil.By("Get current proxy configuration")
		proxy := NewResource(oc.AsAdmin(), "proxy", "cluster")
		proxyInitialConfig := proxy.GetOrFail(`{.spec}`)
		logger.Infof("Initial proxy configuration: %s", proxyInitialConfig)

		wmcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		mmcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)

		defer func() {
			logger.Infof("Start TC defer block")

			logger.Infof("Restore original proxy config %s", proxyInitialConfig)
			_ = proxy.Patch("json", `[{ "op": "add", "path": "/spec", "value": `+proxyInitialConfig+`}]`)

			logger.Infof("Wait for new machine configs to be rendered and paused pools to report updated status")
			// We need to make sure that the config will NOT be applied, since the proxy is a fake one and if
			// we dont make sure that the config proxy is reverted, the nodes will be broken and go into
			// NotReady status
			_ = wmcp.WaitForUpdatedStatus()
			_ = mmcp.WaitForUpdatedStatus()

			logger.Infof("Unpause worker pool")
			wmcp.pause(false)

			logger.Infof("Unpause master pool")
			mmcp.pause(false)

			logger.Infof("End TC defer block")
		}()

		exutil.By("Pause MCPs")
		wmcp.pause(true)
		mmcp.pause(true)

		exutil.By("Configure new proxy")
		err := proxy.Patch("json",
			`[{ "op": "add", "path": "/spec/httpProxy", "value": "`+proxyValue+`" }]`)
		o.Expect(err).ShouldNot(o.HaveOccurred(), "Error patching http proxy")

		err = proxy.Patch("json",
			`[{ "op": "add", "path": "/spec/httpsProxy", "value": "`+proxyValue+`" }]`)
		o.Expect(err).ShouldNot(o.HaveOccurred(), "Error patching https proxy")

		err = proxy.Patch("json",
			`[{ "op": "add", "path": "/spec/noProxy", "value":  "`+noProxyValue+`" }]`)
		o.Expect(err).ShouldNot(o.HaveOccurred(), "Error patching noproxy")

		exutil.By("Verify that the proxy configuration was applied to daemonsets")
		mcoDs := NewNamespacedResource(oc.AsAdmin(), "DaemonSet", MachineConfigNamespace, "machine-config-daemon")
		// it should never take longer than 5 minutes to apply the proxy config under any circumstance,
		// it should be considered a bug.
		o.Eventually(mcoDs.Poll(`{.spec}`), "5m", "30s").Should(o.ContainSubstring(proxyValue),
			"machine-config-daemon is not using the new proxy configuration: %s", proxyValue)
		o.Eventually(mcoDs.Poll(`{.spec}`), "5m", "30s").Should(o.ContainSubstring(noProxyValue),
			"machine-config-daemon is not using the new no-proxy value: %s", noProxyValue)

		exutil.By("Check that the operator has been marked as degraded")
		mco := NewResource(oc.AsAdmin(), "co", "machine-config")
		o.Eventually(mco.Poll(`{.status.conditions[?(@.type=="Degraded")].status}`),
			"5m", "30s").Should(o.Equal("True"),
			"machine-config Operator should report degraded status")

		o.Eventually(mco.Poll(`{.status.conditions[?(@.type=="Degraded")].message}`),
			"5m", "30s").Should(o.ContainSubstring(`required MachineConfigPool master is paused and cannot sync until it is unpaused`),
			"machine-config Operator is not reporting the right reason for degraded status")

		exutil.By("Restore original proxy configuration")
		err = proxy.Patch("json", `[{ "op": "add", "path": "/spec", "value": `+proxyInitialConfig+`}]`)
		o.Expect(err).ShouldNot(o.HaveOccurred(), "Error patching and restoring original proxy config")

		exutil.By("Verify that the new configuration is applied to the daemonset")
		// it should never take longer than 5 minutes to apply the proxy config under any circumstance,
		// it should be considered a bug.
		o.Eventually(mcoDs.Poll(`{.spec}`), "5m", "30s").ShouldNot(o.ContainSubstring(proxyValue),
			"machine-config-daemon has not restored the original proxy configuration")
		o.Eventually(mcoDs.Poll(`{.spec}`), "5m", "30s").ShouldNot(o.ContainSubstring(noProxyValue),
			"machine-config-daemon has not restored the original proxy configuration for 'no-proxy'")

		exutil.By("Check that the operator is not marked as degraded anymore")
		o.Eventually(mco.Poll(`{.status.conditions[?(@.type=="Degraded")].status}`),
			"5m", "30s").Should(o.Equal("False"),
			"machine-config Operator should not report degraded status anymore")

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

	g.It("[PolarionID:70125][OTP] Test patch annotation way of updating a paused pool", func() {

		var (
			workerMcp  = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			mcName     = "create-test-file-70125"
			filePath   = "/etc/test-file-70125"
			fileConfig = getURLEncodedFileConfig(filePath, "test-70125", "420")
		)

		exutil.By("Pause worker pool")
		workerMcp.pause(true)
		o.Expect(workerMcp.IsPaused()).Should(o.BeTrue(), "worker pool is not paused")

		exutil.By("Create a MC for worker nodes")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		mc.SetMCOTemplate(GenericMCTemplate)
		mc.SetParams(fmt.Sprintf("FILES=[%s]", fileConfig))
		mc.skipWaitForMcp = true

		defer workerMcp.RecoverFromDegraded()
		defer mc.DeleteWithWait()
		// unpause the mcp first in defer logic, so nodes can be recovered automatically
		defer workerMcp.pause(false)
		mc.create()

		exutil.By("Patch desired MC annotation to trigger update")
		// get desired rendered mc from mcp.spec.configuration.name
		currentConfig, ccerr := workerMcp.getConfigNameOfStatus()
		o.Expect(ccerr).NotTo(o.HaveOccurred(), "Get current MC of worker pool failed")
		o.Eventually(workerMcp.getConfigNameOfSpec, "2m", "5s").ShouldNot(o.Equal(currentConfig))
		desiredConfig, dcerr := workerMcp.getConfigNameOfSpec()
		o.Expect(dcerr).NotTo(o.HaveOccurred(), "Get desired MC of worker pool failed")
		o.Expect(desiredConfig).NotTo(o.BeEmpty(), "Cannot get desired MC")
		logger.Infof("Desired MC is: %s\n", desiredConfig)

		allWorkerNodes := NewNodeList(oc.AsAdmin()).GetAllLinuxWorkerNodesOrFail()
		o.Expect(allWorkerNodes).NotTo(o.BeEmpty(), "Cannot get any worker node from worker pool")
		workerNode := allWorkerNodes[0]

		logger.Infof("Start to patch annotation [machineconfiguration.openshift.io/desiredConfig] for worker node %s", workerNode.GetName())
		o.Expect(workerNode.PatchDesiredConfig(desiredConfig)).To(o.Succeed(),
			"Failed to patch desiredConfig annotation on node %s", workerNode.GetName())

		// wait update to complete
		o.Eventually(workerNode.IsUpdating, "5m", "5s").Should(o.BeTrue(), "Node is not updating")
		o.Eventually(workerNode.IsUpdated, "10m", "10s").Should(o.BeTrue(), "Node is not updated")
		o.Eventually(workerMcp.getUpdatedMachineCount, "2m", "15s").Should(o.Equal(1), "The  MCP is not properly reporting the updated node")
		o.Eventually(NewRemoteFile(workerNode, filePath).Exists, "2m", "20s").Should(o.BeTrue(), "Cannot find expected file %s on node %s", filePath, workerNode.GetName())
		logger.Infof("Node %s is updated to desired MC %s", workerNode.GetName(), desiredConfig)

		exutil.By("Unpause worker pool")
		workerMcp.pause(false)
		o.Expect(workerMcp.IsPaused()).Should(o.BeFalse(), "worker pool is not unpaused")
		logger.Infof("MCP worker is unpaused\n")

		exutil.By("Check worker pool is updated")
		workerMcp.waitForComplete()

		exutil.By("Check file exists on all worker nodes")
		for _, node := range allWorkerNodes {
			o.Expect(NewRemoteFile(node, filePath).Exists()).Should(o.BeTrue(), "Cannot find expected file %s on node %s", filePath, node.GetName())
			logger.Infof("File %s can be found on node %s\n", filePath, node.GetName())
		}

	})

	g.It("[PolarionID:72007][OTP] check node update frequencies", func() {

		exutil.By("To get node and display its nodeupdate frequiences")

		var (
			file = "/etc/kubernetes/kubelet.conf"
			cmd  = "nodeStatusUpdateFrequency|nodeStatusReportFrequency"
		)
		nodeList, err := NewNodeList(oc.AsAdmin()).GetAllLinux() // Get all nodes
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the list of nodes")

		for _, node := range nodeList {
			if node.HasTaintEffectOrFail("NoExecute") {
				logger.Infof("Node %s is tainted with 'NoExecute'. Validation skipped.", node.GetName())
				continue
			}
			nodeUpdate, err := node.DebugNodeWithChroot("grep", "-E", cmd, file) // To get nodeUpdate frequencies value
			o.Expect(err).NotTo(o.HaveOccurred(), "Error getting nodeupdate frequencies for %s", node.GetName())
			o.Expect(nodeUpdate).To(o.Or(o.ContainSubstring(`"nodeStatusUpdateFrequency": "10s"`), o.ContainSubstring(`nodeStatusUpdateFrequency: 10s`)), "Value for 'nodeStatusUpdateFrequency' is not same as expected.")
			o.Expect(nodeUpdate).To(o.Or(o.ContainSubstring(`"nodeStatusReportFrequency": "5m0s"`), o.ContainSubstring(`nodeStatusReportFrequency: 5m0s`)), "Value for 'nodeStatusReportFrequency' is not same as expected.")
			logger.Infof("node/%s %s", node, nodeUpdate)
		}
	})

	g.It("[PolarionID:75149][OTP] Update pool with manually cordoned nodes", func() {
		if IsCompactOrSNOCluster(oc.AsAdmin()) {
			g.Skip("The cluster is SNO/Compact. This test cannot be executed in SNO/Compact clusters")
		}

		var (
			mcp          = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			mcName       = "mco-test-75149"
			password     = exutil.GetRandomString()
			passwordHash = OrFail[string](getHashPasswd(password))
			nodeList     = NewNodeList(oc.AsAdmin())
			initNumNodes = len(mcp.GetNodesOrFail())

			checkDuration = "3m"
		)
		if initNumNodes < 3 {
			if !exutil.OrFail[bool](WorkersCanBeScaled(oc.AsAdmin())) {
				g.Skip("A minimum of 3 worker nodes are needed to execute this test. The worker pool has less than 3 nodes and cannot be scaled up to create new nodes")
			}
			// TODO add only the necessary machines to reach 3 nodes
			numAdd := 3 - initNumNodes
			machineset := exutil.OrFail[*MachineSet](GetScalableMachineSet(oc.AsAdmin()))
			o.Expect(machineset.AddToScale(numAdd)).To(o.Succeed(),
				"Error addind new nodes to the cluster")
			o.Expect(machineset.WaitUntilReady("15m")).To(o.Succeed(),
				"Error waiting for the machineset to become ready. %s", machineset.PrettyString())
			mcp.waitForComplete()

			defer func() {
				o.Expect(machineset.AddToScale(-numAdd)).To(o.Succeed(),
					"Error removing the extra nodes from the cluster")
				o.Eventually(mcp.GetNodes, "5m", "30s").Should(o.HaveLen(initNumNodes),
					"The worker pool has not removed the new nodes created by the new Machineset.\n%s", mcp.PrettyString())
				mcp.waitForComplete()
			}()
		}

		exutil.By("Set the maxUnavailable value to 2")
		mcp.SetMaxUnavailable(2)
		defer mcp.RemoveMaxUnavailable()
		logger.Infof("OK!\n")

		exutil.By("Manually cordon one of the nodes")
		nodes := mcp.GetNodesOrFail()
		cordonedNode := nodes[0]
		defer cordonedNode.Uncordon()
		o.Expect(cordonedNode.Cordon()).To(o.Succeed(),
			"Could not cordon node %s", cordonedNode.GetName())
		logger.Infof("OK!\n")

		exutil.By("Create a new MachineConfiguration resource")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName())
		mc.parameters = []string{fmt.Sprintf(`PWDUSERS=[{"name": "core", "passwordHash": "%s" }]`, passwordHash)}
		mc.skipWaitForMcp = true

		defer mc.DeleteWithWait()
		defer cordonedNode.Uncordon()
		mc.create()
		logger.Infof("OK!\n")

		exutil.By("Check that only one node is updated at a time (instead of 2) because the manually cordoned node counts as unavailable")
		// get all nodes with status != Done
		nodeList.SetItemsFilter(`?(@.metadata.annotations.machineconfiguration\.openshift\.io/state!="Done")`)
		o.Consistently(func() (int, error) {
			nodes, err := nodeList.GetAll()
			return len(nodes), err
		}, checkDuration, "10s").Should(o.BeNumerically("<", 2),
			"The maximun number of nodes updated at a time should be 1, because the manually cordoned node should count as unavailable too")
		logger.Infof("OK!\n")

		exutil.By("Check that all nodes are updated but the manually cordoned one")
		numNodes := len(nodes)
		waitDuration := mcp.estimateWaitDuration().String()
		o.Eventually(mcp.getUpdatedMachineCount, waitDuration, "15s").Should(o.Equal(numNodes-1),
			"All nodes but one should be udated. %d total nodes, expecting %d to be updated", numNodes, numNodes-1)

		// We check that the desired config for the manually cordoned node is the old config, and not the new one
		o.Consistently(cordonedNode.GetDesiredMachineConfig, "2m", "20s").Should(o.Equal(mcp.getConfigNameOfStatusOrFail()),
			"The manually cordoned node should not be updated. The desiredConfig value should be the old one.")
		logger.Infof("OK!\n")

		exutil.By("Manually undordon the cordoned node")
		o.Expect(cordonedNode.Uncordon()).To(o.Succeed(),
			"Could not uncordon the manually cordoned node")
		logger.Infof("OK!\n")

		exutil.By("All nodes should be updated now")
		mcp.waitForComplete()
		// Make sure that the cordoned node is now using the new configuration
		o.Eventually(cordonedNode.GetDesiredMachineConfig, "30s", "10s").Should(o.Equal(mcp.getConfigNameOfSpecOrFail()),
			"The manually cordoned node should not be updated. The desiredConfig value should be the old one.")
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:76108][OTP] MachineConfig inheritance. Canary rollout update", func() {
		SkipIfCompactOrSNO(oc) // We can't create custom pools if only the master pool exists

		var (
			customMCPName = "worker-perf"
			canaryMCPName = "worker-perf-canary"
			mcName        = "06-kdump-enable-worker-perf-tc-76108"
			mcUnit        = `{"enabled": true, "name": "kdump.service"}`
			mcKernelArgs  = "crashkernel=512M"
			mc            = NewMachineConfig(oc.AsAdmin(), mcName, customMCPName)
		)

		defer mc.Delete()

		exutil.By("Create custom MCP")
		defer DeleteCustomMCP(oc.AsAdmin(), customMCPName)
		customMcp, err := CreateCustomMCP(oc.AsAdmin(), customMCPName, 2)
		o.Expect(err).NotTo(o.HaveOccurred(), "Could not create a new custom MCP")
		logger.Infof("OK!\n")

		exutil.By("Create canary custom MCP")
		defer DeleteCustomMCP(oc.AsAdmin(), canaryMCPName)
		canaryMcp, err := CreateCustomMCP(oc.AsAdmin(), canaryMCPName, 0)
		o.Expect(err).NotTo(o.HaveOccurred(), "Could not create a new custom MCP")
		logger.Infof("OK!\n")

		exutil.By("Patch the canary MCP so that it uses the MCs of the custom MCP too")
		o.Expect(
			canaryMcp.Patch("json", `[{ "op": "add", "path": "/spec/machineConfigSelector/matchExpressions/0/values/-", "value":"`+customMCPName+`"}]`),
		).To(o.Succeed(), "Error patching MCP %s so that it uses the same MCs as MCP %s", canaryMcp.GetName(), customMcp.GetName())
		logger.Infof("OK!\n")

		exutil.By("Apply a new MC to the custom pool")

		err = mc.Create("-p", "NAME="+mcName, "-p", "POOL="+customMCPName, "-p", fmt.Sprintf("UNITS=[%s]", mcUnit), fmt.Sprintf(`KERNEL_ARGS=["%s"]`, mcKernelArgs))
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating MachineConfig %s", mc.GetName())

		customMcp.waitForComplete()

		exutil.By("Check that the configuration was applied the nodes")
		canaryNode := customMcp.GetNodesOrFail()[0]
		o.Expect(canaryNode.IsKernelArgEnabled(mcKernelArgs)).Should(o.BeTrue(), "Kernel argument %s is not set in node %s", mcKernelArgs, canaryNode)
		logger.Infof("OK!\n")

		exutil.By("Move one node from the custom pool to the canary custom pool")
		startTime := canaryNode.GetDateOrFail()

		// We need to add and remove the label at the same time to avoid the node belonging to 2 custom nodes at the same time, which is forbidden
		o.Expect(
			MoveNodeToAnotherCustomPool(canaryNode, customMCPName, canaryMCPName),
		).To(o.Succeed(), "Error labeling node %s", canaryNode)

		o.Eventually(canaryMcp.getMachineCount, "5m", "20s").Should(o.Equal(1),
			"A machine should be added to the canary MCP, but no machine was added: %s", canaryMcp.PrettyString())
		o.Eventually(customMcp.getMachineCount, "5m", "20s").Should(o.Equal(1),
			"A machine should be removed from the custom MCP: %s", customMcp.PrettyString())
		canaryMcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check that the configuration is still applied to the canary node")
		o.Expect(canaryNode.IsKernelArgEnabled(mcKernelArgs)).Should(o.BeTrue(), "Kernel argument %s is not set in node %s", mcKernelArgs, canaryNode)
		logger.Infof("OK!\n")

		exutil.By("Check that the node was not restarted when it was added to the canary pool")
		checkRebootAction(false, canaryNode, startTime)
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:85073][OTP] automatically re-cordons manually uncordoned node during update", func() {
		var (
			tcID       = GetCurrentTestPolarionIDNumber()
			mcName     = fmt.Sprintf("mco-tc-%s", tcID)
			filePath   = fmt.Sprintf("/etc/test-file-%s.test", tcID)
			fileConfig = getBase64EncodedFileConfig(filePath, fmt.Sprintf("test-content-%s", tcID), "0644")
			mcp        = GetCompactCompatiblePool(oc.AsAdmin())
			firstNode  = mcp.GetSortedNodesOrFail()[0]
		)

		exutil.By(fmt.Sprintf("Create MC that creates a file %s in worker pool nodes", filePath))
		mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName())
		mc.SetParams(fmt.Sprintf("FILES=[%s]", fileConfig))
		mc.skipWaitForMcp = true
		defer mc.DeleteWithWait()
		mc.create()
		logger.Infof("OK!\n")

		exutil.By("Wait until the first worker node is cordoned")
		o.Eventually(firstNode.IsCordoned, "20m", "30s").Should(o.BeTrue(),
			"Node %s was not cordoned after creating the MC", firstNode.GetName())
		logger.Infof("OK!\n")

		exutil.By("Uncordon the cordoned node")
		o.Expect(firstNode.Uncordon()).To(o.Succeed(), "Could not uncordon node %s", firstNode.GetName())
		logger.Infof("OK!\n")

		exutil.By("Wait for the uncordoned node to be automatically cordoned again by MCO")
		o.Eventually(firstNode.IsCordoned, "5m", "10s").Should(o.BeTrue(),
			"Node %s was not automatically re-cordoned by MCO after manual uncordon", firstNode.GetName())
		logger.Infof("OK!\n")

		exutil.By("Wait for MCP to complete the update")
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Verify that the file was correctly created in the node")
		o.Expect(NewRemoteFile(firstNode, filePath).Exists()).Should(o.BeTrue(),
			"File %s was not created in node %s", filePath, firstNode.GetName())
		logger.Infof("OK!\n")
	})

})
