package extended

import (
	"context"
	"fmt"
	"strings"
	"time"

	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	"k8s.io/apimachinery/pkg/util/wait"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO Drain", func() {
	defer g.GinkgoRecover()

	var oc = exutil.NewCLI("mco", exutil.KubeConfigPath())

	g.JustBeforeEach(func() {
		PreChecks(oc)
	})

	g.It("[PolarionID:43245][OTP] bump initial drain sleeps down to 1min", func() {
		exutil.By("Start machine-config-controller logs capture")
		mcc := NewController(oc.AsAdmin())
		ignoreMccLogErr := mcc.IgnoreLogsBeforeNow()
		o.Expect(ignoreMccLogErr).NotTo(o.HaveOccurred(), "Ignore mcc log failed")

		exutil.By("Create a pod disruption budget to set minAvailable to 1")
		oc.SetupProject()
		nsName := oc.Namespace()
		pdbName := "dont-evict-43245"
		pdbTemplate := generateTemplateAbsolutePath("pod-disruption-budget.yaml")
		pdb := PodDisruptionBudget{name: pdbName, namespace: nsName, template: pdbTemplate}
		defer pdb.delete(oc)
		pdb.create(oc)

		exutil.By("Create new pod for pod disruption budget")
		// Not all nodes are valid. We need to deploy the "dont-evict-pod" and we can only do that in schedulable nodes
		// In "edge" clusters, the "edge" nodes are not schedulable, so we need to be careful and not to use them to deploy our pod
		schedulableNodes := FilterSchedulableNodesOrFail(NewNodeList(oc.AsAdmin()).GetAllLinuxWorkerNodesOrFail())
		o.Expect(schedulableNodes).NotTo(o.BeEmpty(), "There are no schedulable worker nodes!!")
		workerNode := schedulableNodes[0]
		hostname, err := workerNode.GetNodeHostname()
		o.Expect(err).NotTo(o.HaveOccurred())
		podName := "dont-evict-43245"
		podTemplate := generateTemplateAbsolutePath("create-pod.yaml")
		pod := exutil.Pod{Name: podName, Namespace: nsName, Template: podTemplate, Parameters: []string{"HOSTNAME=" + hostname}}
		defer func() { o.Expect(pod.Delete(oc)).NotTo(o.HaveOccurred()) }()
		pod.Create(oc)

		exutil.By("Create new mc to add new file on the node and trigger node drain")
		mcName := "test-file"
		mcTemplate := "add-mc-to-trigger-node-drain.yaml"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker).SetMCOTemplate(mcTemplate)
		mc.skipWaitForMcp = true
		defer mc.DeleteWithWait()
		defer func() { o.Expect(pod.Delete(oc)).NotTo(o.HaveOccurred()) }()
		mc.create()

		exutil.By("Wait until node is cordoned")
		o.Eventually(workerNode.Poll(`{.spec.taints[?(@.effect=="NoSchedule")].effect}`),
			"20m", "1m").Should(o.Equal("NoSchedule"), fmt.Sprintf("Node %s was not cordoned", workerNode.name))

		exutil.By("Check MCC logs to see the early sleep interval b/w failed drains")
		var podLogs string
		// Wait until trying drain for 3 times
		// Early sleep interval will last for 10m. During this interval MCO will wait 1 minute before every retry.
		// Every failed drain operation will last 1 minute. So 1 minute execution + 1 minute delay = 2 minutes for every try.
		// In a 10 minutes span we only have time for at most 5 "Drain failed" early sleep failures
		// To have a stable test case we will take only 3 of them
		immediate := false
		waitErr := wait.PollUntilContextTimeout(context.TODO(), 1*time.Minute, 15*time.Minute, immediate, func(_ context.Context) (bool, error) {
			logs, err := mcc.GetFilteredLogsAsList(workerNode.GetName() + ".*Drain failed")
			if err != nil {
				return false, fmt.Errorf("Error getting filtered logs for node %s from %s: %w", workerNode.GetName(), mcc, err)
			}
			if len(logs) > 2 {
				// Get only 3 lines to avoid flooding the test logs, ignore the rest if any.
				podLogs = strings.Join(logs[0:3], "\n")
				return true, nil
			}

			return false, nil
		})
		logger.Infof("Drain log lines for node %s:\n %s", workerNode.GetName(), podLogs)
		o.Expect(waitErr).NotTo(o.HaveOccurred(), fmt.Sprintf("Cannot get 'Drain failed' log lines from controller for node %s", workerNode.GetName()))
		timestamps := filterTimestampFromLogs(podLogs, 3)
		logger.Infof("Timestamps %s", timestamps)
		// First 3 retries should be queued every 1 minute. We check 1 min < time < 2.7 min
		o.Expect(getTimeDifferenceInMinute(timestamps[0], timestamps[1])).Should(o.BeNumerically("<=", 2.7))
		o.Expect(getTimeDifferenceInMinute(timestamps[0], timestamps[1])).Should(o.BeNumerically(">=", 1))
		o.Expect(getTimeDifferenceInMinute(timestamps[1], timestamps[2])).Should(o.BeNumerically("<=", 2.7))
		o.Expect(getTimeDifferenceInMinute(timestamps[1], timestamps[2])).Should(o.BeNumerically(">=", 1))

		exutil.By("Check MCC logs to see the increase in the sleep interval b/w failed drains")
		lWaitErr := wait.PollUntilContextTimeout(context.TODO(), 1*time.Minute, 15*time.Minute, immediate, func(_ context.Context) (bool, error) {
			logs, err := mcc.GetFilteredLogsAsList(workerNode.GetName() + ".*Drain has been failing for more than 10 minutes. Waiting 5 minutes")
			if err != nil {
				return false, fmt.Errorf("Error getting filtered logs for node %s from %s: %w", workerNode.GetName(), mcc, err)
			}
			if len(logs) > 1 {
				// Get only 2 lines to avoid flooding the test logs, ignore the rest if any.
				podLogs = strings.Join(logs[0:2], "\n")
				return true, nil
			}

			return false, nil
		})
		logger.Infof("Long wait drain log lines for node %s:\n %s", workerNode.GetName(), podLogs)
		o.Expect(lWaitErr).NotTo(o.HaveOccurred(),
			fmt.Sprintf("Cannot get 'Drain has been failing for more than 10 minutes. Waiting 5 minutes' log lines from controller for node %s",
				workerNode.GetName()))
		// Following developers' advice we dont check the time spam between long wait log lines. Read:
		// https://github.com/openshift/machine-config-operator/pull/3178
		// https://bugzilla.redhat.com/show_bug.cgi?id=2092442
	})

	g.It("[PolarionID:51381][OTP] cordon node before node drain. OCP >= 4.11", func() {
		exutil.By("Capture initial migration-controller logs")
		ctrlerContainer := "machine-config-controller"
		ctrlerPod, podsErr := getMachineConfigControllerPod(oc)
		o.Expect(podsErr).NotTo(o.HaveOccurred())
		o.Expect(ctrlerPod).NotTo(o.BeEmpty())

		initialCtrlerLogs, initErr := exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, ctrlerContainer, ctrlerPod, "")
		o.Expect(initErr).NotTo(o.HaveOccurred())

		exutil.By("Create a MC to deploy a config file")
		fileMode := "0644" // decimal 420
		filePath := "/etc/chrony.conf"
		fileContent := "pool 0.rhel.pool.ntp.org iburst\ndriftfile /var/lib/chrony/drift\nmakestep 1.0 3\nrtcsync\nlogdir /var/log/chrony"
		fileConfig := getBase64EncodedFileConfig(filePath, fileContent, fileMode)

		mcName := "ztc-51381-change-workers-chrony-configuration"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		defer mc.DeleteWithWait()

		err := mc.Create("-p", "NAME="+mcName, "-p", "POOL=worker", "-p", fmt.Sprintf("FILES=[%s]", fileConfig))
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Check MCD logs to make sure that the node is cordoned before being drained")
		mcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		workerNode := mcp.GetSortedNodesOrFail()[0]

		o.Eventually(workerNode.IsCordoned, mcp.estimateWaitDuration().String(), "20s").Should(o.BeTrue(), "Worker node must be cordoned")

		searchRegexp := fmt.Sprintf("(?s)%s: initiating cordon", workerNode.GetName())
		if !workerNode.IsEdgeOrFail() {
			// In edge nodes there is no node evicted because they are unschedulable so no pod is running
			searchRegexp += fmt.Sprintf(".*node %s: Evicted pod", workerNode.GetName())
		}
		searchRegexp += fmt.Sprintf(".*node %s: operation successful; applying completion annotation", workerNode.GetName())

		o.Eventually(func() string {
			podAllLogs, err := exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, ctrlerContainer, ctrlerPod, "")
			if err != nil {
				return fmt.Sprintf("Error getting pod logs: %v", err)
			}
			// Remove the part of the log captured at the beginning of the test.
			// We only check the part of the log that this TC generates and ignore the previously generated logs
			return strings.Replace(podAllLogs, initialCtrlerLogs, "", 1)
		}, "5m", "10s").Should(o.MatchRegexp(searchRegexp), "Node should be cordoned before being drained")

		exutil.By("Wait until worker MCP has finished the configuration. No machine should be degraded.")
		mcp.waitForComplete()

		exutil.By("Verfiy file content and permissions")
		rf := NewRemoteFile(workerNode, filePath)
		rferr := rf.Fetch()
		o.Expect(rferr).NotTo(o.HaveOccurred())

		o.Expect(rf.GetTextContent()).To(o.Equal(fileContent))
		o.Expect(rf.GetNpermissions()).To(o.Equal(fileMode))
	})

	g.It("[PolarionID:49568][OTP] Check nodes updating order maxUnavailable=1", func() {

		mcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)

		// In OCL nodes are scaled up using the original osImage, and then MCO applies an update on them
		// To avoid problems we do not scale up new nodes if OCL is enabled
		// Once OCL is able to boot the nodes directly with the right image we can scale up nodes if the pool is OCL
		if exutil.OrFail[bool](WorkersCanBeScaled(oc.AsAdmin())) && !exutil.OrFail[bool](mcp.IsOCL()) {
			exutil.By("Scale machinesets and 1 more replica to make sure we have at least 2 nodes per machineset")
			platform := exutil.CheckPlatform(oc)
			logger.Infof("Platform is %s", platform)
			if platform != NonePlatform && platform != "" {
				err := AddToAllMachineSets(oc, 1)
				o.Expect(err).NotTo(o.HaveOccurred())
				defer func() {
					o.Expect(AddToAllMachineSets(oc, -1)).NotTo(o.HaveOccurred())
					mcp.waitForComplete()
				}()
			} else {
				logger.Infof("Platform is %s, skipping the MachineSets replica configuration", platform)
			}
		} else {
			logger.Infof("The worker pool cannot be scaled using machinesets or it is OCL. Skip adding new nodes")
		}

		exutil.By("Get the nodes in the worker pool sorted by update order")
		workerNodes, errGet := mcp.GetSortedNodes()
		o.Expect(errGet).NotTo(o.HaveOccurred())

		exutil.By("Create a MC to deploy a config file")
		filePath := "/etc/TC-49568-mco-test-file-order"
		fileContent := "MCO test file order\n"
		fileMode := "0400" // decimal 256
		fileConfig := getURLEncodedFileConfig(filePath, fileContent, fileMode)

		mcName := "mco-test-file-order"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		defer mc.DeleteWithWait()

		err := mc.Create("-p", "NAME="+mcName, "-p", "POOL=worker", "-p", fmt.Sprintf("FILES=[%s]", fileConfig))
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Poll the nodes sorted by the order they are updated")
		maxUnavailable := 1
		updatedNodes := mcp.GetSortedUpdatedNodes(maxUnavailable)
		for _, n := range updatedNodes {
			logger.Infof("updated node: %s created: %s zone: %s", n.GetName(), n.GetOrFail(`{.metadata.creationTimestamp}`), n.GetOrFail(`{.metadata.labels.topology\.kubernetes\.io/zone}`))
		}

		exutil.By("Wait for the configuration to be applied in all nodes")
		mcp.waitForComplete()

		exutil.By("Check that nodes were updated in the right order")
		rightOrder := checkUpdatedLists(workerNodes, updatedNodes, maxUnavailable)
		o.Expect(rightOrder).To(o.BeTrue(), "Expected update order %s, but found order %s", workerNodes, updatedNodes)

		exutil.By("Verfiy file content and permissions")
		rf := NewRemoteFile(workerNodes[0], filePath)
		rferr := rf.Fetch()
		o.Expect(rferr).NotTo(o.HaveOccurred())

		o.Expect(rf.GetTextContent()).To(o.Equal(fileContent))
		o.Expect(rf.GetNpermissions()).To(o.Equal(fileMode))
	})

	g.It("[PolarionID:49672][OTP] Check nodes updating order maxUnavailable>1", func() {

		mcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)

		// In OCL nodes are scaled up using the original osImage, and then MCO applies an update on them
		// To avoid problems we do not scale up new nodes if OCL is enabled
		// Once OCL is able to boot the nodes directly with the right image we can scale up nodes if the pool is OCL
		if exutil.OrFail[bool](WorkersCanBeScaled(oc.AsAdmin())) && !exutil.OrFail[bool](mcp.IsOCL()) {
			exutil.By("Scale machinesets and 1 more replica to make sure we have at least 2 nodes per machineset")
			platform := exutil.CheckPlatform(oc)
			logger.Infof("Platform is %s", platform)
			if platform != NonePlatform && platform != "" {
				err := AddToAllMachineSets(oc, 1)
				o.Expect(err).NotTo(o.HaveOccurred())
				defer func() {
					o.Expect(AddToAllMachineSets(oc, -1)).NotTo(o.HaveOccurred())
					mcp.waitForComplete()
				}()
			} else {
				logger.Infof("Platform is %s, skipping the MachineSets replica configuration", platform)
			}
		} else {
			logger.Infof("The worker pool cannot be scaled using machinesets or it is OCL. Skip adding new nodes")
		}

		// If the number of nodes is 2, since we are using maxUnavailable=2, all nodes will be cordoned at
		//  the same time and the eviction process will be stuck. In this case we need to skip the test case.
		numWorkers := len(NewNodeList(oc.AsAdmin()).GetAllLinuxWorkerNodesOrFail())
		if numWorkers <= 2 {
			g.Skip(fmt.Sprintf("The test case needs at least 3 worker nodes, because eviction will be stuck if not. Current num worker is %d, we skip the case",
				numWorkers))
		}

		exutil.By("Get the nodes in the worker pool sorted by update order")
		workerNodes, errGet := mcp.GetSortedNodes()
		o.Expect(errGet).NotTo(o.HaveOccurred())

		exutil.By("Set maxUnavailable value")
		maxUnavailable := 2
		mcp.SetMaxUnavailable(maxUnavailable)
		defer mcp.RemoveMaxUnavailable()

		exutil.By("Create a MC to deploy a config file")
		filePath := "/etc/TC-49672-mco-test-file-order"
		fileContent := "MCO test file order 2\n"
		fileMode := "0400" // decimal 256
		fileConfig := getURLEncodedFileConfig(filePath, fileContent, fileMode)

		mcName := "mco-test-file-order2"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		defer mc.DeleteWithWait()

		err := mc.Create("-p", "NAME="+mcName, "-p", "POOL=worker", "-p", fmt.Sprintf("FILES=[%s]", fileConfig))
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Poll the nodes sorted by the order they are updated")
		updatedNodes := mcp.GetSortedUpdatedNodes(maxUnavailable)
		for _, n := range updatedNodes {
			logger.Infof("updated node: %s created: %s zone: %s", n.GetName(), n.GetOrFail(`{.metadata.creationTimestamp}`), n.GetOrFail(`{.metadata.labels.topology\.kubernetes\.io/zone}`))
		}

		exutil.By("Wait for the configuration to be applied in all nodes")
		mcp.waitForComplete()

		exutil.By("Check that nodes were updated in the right order")
		rightOrder := checkUpdatedLists(workerNodes, updatedNodes, maxUnavailable)
		o.Expect(rightOrder).To(o.BeTrue(), "Expected update order %s, but found order %s", workerNodes, updatedNodes)

		exutil.By("Verfiy file content and permissions")
		rf := NewRemoteFile(workerNodes[0], filePath)
		rferr := rf.Fetch()
		o.Expect(rferr).NotTo(o.HaveOccurred())

		o.Expect(rf.GetTextContent()).To(o.Equal(fileContent))
		o.Expect(rf.GetNpermissions()).To(o.Equal(fileMode))
	})

})
