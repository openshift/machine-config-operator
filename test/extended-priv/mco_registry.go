package extended

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO Registry", func() {
	defer g.GinkgoRecover()

	var oc = exutil.NewCLI("mco", exutil.KubeConfigPath())

	g.JustBeforeEach(func() {
		PreChecks(oc)
	})

	g.It("[PolarionID:43405][OTP] node drain is not needed for mirror config change in container registry. Nodes not tainted. [Disruptive]", func() {
		logger.Infof("Removing all MCD pods to clean the logs.")
		o.Expect(RemoveAllMCDPods(oc)).To(o.Succeed(), "Error removing all MCD pods in %s namespace", MachineConfigNamespace)
		logger.Infof("OK!\n")

		exutil.By("Create image content source policy for mirror changes")
		icspName := "repository-mirror"
		icspTemplate := generateTemplateAbsolutePath(icspName + ".yaml")
		icsp := ImageContentSourcePolicy{name: icspName, template: icspTemplate}
		defer icsp.delete(oc)
		icsp.create(oc)
		logger.Infof("OK!\n")

		exutil.By("Check registry changes in the worker node")
		workerNode := NewNodeList(oc.AsAdmin()).GetAllLinuxWorkerNodesOrFail()[0]
		startTime := workerNode.GetDateOrFail()
		o.Expect(workerNode.DebugNodeWithChroot("cat", "/etc/containers/registries.conf")).Should(
			o.And(
				o.ContainSubstring(`pull-from-mirror = "digest-only"`),
				o.ContainSubstring("example.com/example/ubi-minimal"),
				o.ContainSubstring("example.io/example/ubi-minimal")))
		logger.Infof("OK!\n")

		exutil.By("Check MCD logs to make sure drain is skipped")
		o.Expect(exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigDaemon, workerNode.GetMachineConfigDaemon(), "")).Should(
			o.And(
				o.ContainSubstring("/etc/containers/registries.conf: changes made are safe to skip drain"),
				o.ContainSubstring("Changes do not require drain, skipping"),
				o.MatchRegexp(MCDCrioReloadedRegexp)))

		o.Expect(workerNode.GetUptime()).Should(o.BeTemporally("<", startTime),
			"The node %s must NOT be rebooted, but it was rebooted. Uptime date happened after the start config time.", workerNode.GetName())
		logger.Infof("OK!\n")

		exutil.By("Check that worker nodes are not cordoned after applying the MC")
		workerMcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		workerNodes, err := workerMcp.GetNodes()
		o.Expect(err).ShouldNot(o.HaveOccurred(), "Error getting nodes linked to the worker pool")
		for _, node := range workerNodes {
			// We look for the cordon taint. We can't look for "any taint", because in "edge" clusters the "edge" nodes are tained and unschedulable.
			o.Expect(node.IsCordoned()).To(o.BeFalse(), "%s is tainted and cordoned. There should be no cordoned worker node after applying the configuration.",
				node.GetName())
		}
		logger.Infof("OK!\n")

		exutil.By("Check that master nodes have only the NoSchedule taint after applying the the MC")
		masterMcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
		masterNodes, err := masterMcp.GetNodes()
		o.Expect(err).ShouldNot(o.HaveOccurred(), "Error getting nodes linked to the master pool")
		expectedTaint := `[{"effect":"NoSchedule","key":"node-role.kubernetes.io/master"}]`
		for _, node := range masterNodes {
			taint, err := node.Get("{.spec.taints}")
			o.Expect(err).ShouldNot(o.HaveOccurred(), "Error getting taints in node %s", node.GetName())
			o.Expect(taint).Should(o.MatchJSON(expectedTaint), "%s is tainted. The only taint allowed in master nodes is NoSchedule.",
				node.GetName())
		}
		logger.Infof("OK!\n")

		exutil.By("Delete the ImageContentSourcePoolicy resource")
		removeTime := workerNode.GetDateOrFail()
		icsp.delete(oc)
		logger.Infof("OK!\n")

		exutil.By("Check that the changes in the registry config were removed")
		o.Expect(workerNode.DebugNodeWithChroot("cat", "/etc/containers/registries.conf")).ShouldNot(
			o.Or(
				o.ContainSubstring("example.com/example/ubi-minimal"),
				o.ContainSubstring("example.io/example/ubi-minimal")))
		logger.Infof("OK!\n")

		exutil.By("Check MCD logs to make sure drain is executed, crio restarted and reboot skipped")
		o.Expect(exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigDaemon, workerNode.GetMachineConfigDaemon(), "")).Should(
			o.And(
				o.ContainSubstring("requesting cordon and drain via annotation to controller"),
				o.ContainSubstring("drain complete"),
				o.MatchRegexp(MCDCrioReloadedRegexp)))

		o.Expect(workerNode.GetUptime()).Should(o.BeTemporally("<", removeTime),
			"The node %s must NOT be rebooted, but it was rebooted. Uptime date happened after the start config time.", workerNode.GetName())
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:42682][OTP] change container registry config on ocp 4.6+ [Disruptive]", func() {

		skipTestIfClusterVersion(oc, "<", "4.6")

		registriesConfPath := "/etc/containers/registries.conf"
		newSearchRegistry := "quay.io"

		exutil.By("Generate new registries.conf information")
		workerNode := NewNodeList(oc.AsAdmin()).GetAllLinuxWorkerNodesOrFail()[0]
		registriesConf := NewRemoteFile(workerNode, registriesConfPath)
		o.Expect(registriesConf.Fetch()).Should(o.Succeed(), "Error getting %s file content", registriesConfPath)

		currentSearch, sErr := registriesConf.GetFilteredTextContent("unqualified-search-registries")
		o.Expect(sErr).ShouldNot(o.HaveOccurred())
		logger.Infof("Initial search configuration: %s", strings.Join(currentSearch, "\n"))

		logger.Infof("Adding %s registry to the initial search configuration", newSearchRegistry)
		currentConfig := registriesConf.GetTextContent()
		// add the "quay.io" registry to the unqualified-search-registries list defined in the registries.conf file
		// this regexp inserts `, "quay.io"` before  `]` in the unqualified-search-registries line.
		regx := regexp.MustCompile(`(unqualified-search-registries.*=.*\[.*)](.*)`)
		newConfig := regx.ReplaceAllString(currentConfig, fmt.Sprintf(`$1, "%s"] $2`, newSearchRegistry))

		exutil.By("Create new machine config to add quay.io to unqualified-search-registries list")
		mcName := "change-workers-container-reg"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		defer mc.DeleteWithWait()

		fileConfig := getURLEncodedFileConfig(registriesConfPath, newConfig, "420")
		errCreate := mc.Create("-p", "NAME="+mcName, "-p", "POOL=worker", "-p", fmt.Sprintf("FILES=[%s]", fileConfig))
		o.Expect(errCreate).NotTo(o.HaveOccurred(), "Error creating MachineConfig %s", mcName)

		exutil.By("Wait for MCP to be updated")
		mcpWorker := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		mcpWorker.waitForComplete()

		exutil.By("Check content of registries file to verify quay.io added to unqualified-search-registries list")
		regOut, errDebug := workerNode.DebugNodeWithChroot("cat", registriesConfPath)
		logger.Infof("File content of registries conf: %v", regOut)
		o.Expect(errDebug).NotTo(o.HaveOccurred(), "Error executing debug command on node %s", workerNode.GetName())
		o.Expect(regOut).Should(o.ContainSubstring(newSearchRegistry),
			"registry %s has not been added to the %s file", newSearchRegistry, registriesConfPath)

		exutil.By("Check MCD logs to make sure drain is successful and pods are evicted")
		podLogs, errLogs := exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigDaemon, workerNode.GetMachineConfigDaemon(), "\"evicted\\|drain\\|crio\"")
		o.Expect(errLogs).NotTo(o.HaveOccurred(), "Error getting logs from node %s", workerNode.GetName())
		logger.Infof("Pod logs for node drain, pods evicted and crio service reload :\n %v", podLogs)
		// get clusterversion
		cv, _, cvErr := exutil.GetClusterVersion(oc)
		o.Expect(cvErr).NotTo(o.HaveOccurred())
		// check node drain is skipped for cluster 4.7+
		if CompareVersions(cv, ">", "4.7") {
			o.Expect(podLogs).Should(
				o.And(
					o.ContainSubstring("/etc/containers/registries.conf: changes made are safe to skip drain"),
					o.ContainSubstring("Changes do not require drain, skipping")))
		} else {
			// check node drain can be triggered for 4.6 & 4.7
			o.Expect(podLogs).Should(
				o.And(
					o.ContainSubstring("Update prepared; beginning drain"),
					o.ContainSubstring("Evicted pod openshift-image-registry/image-registry"),
					o.ContainSubstring("drain complete")))
		}
		// check whether crio.service is reloaded in 4.6+ env
		if CompareVersions(cv, ">", "4.6") {
			logger.Infof("cluster version is > 4.6, need to check crio service is reloaded or not")
			o.Expect(podLogs).Should(o.MatchRegexp(MCDCrioReloadedRegexp))
		}

	})

	g.It("[PolarionID:42680][OTP] change pull secret in the openshift-config namespace [Serial]", func() {
		exutil.By("Add a dummy credential in pull secret")
		secretFile, err := getPullSecret(oc)
		o.Expect(err).NotTo(o.HaveOccurred())
		newSecretFile := generateTmpFile(oc, "pull-secret.dockerconfigjson")
		_, copyErr := exec.Command("bash", "-c", "cp "+secretFile+" "+newSecretFile).Output()
		o.Expect(copyErr).NotTo(o.HaveOccurred())
		newPullSecret, err := oc.AsAdmin().WithoutNamespace().Run("registry").Args("login", `--registry="quay.io"`, `--auth-basic="mhans-redhat:redhat123"`, "--to="+newSecretFile, "--skip-check").Output()
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(newPullSecret).Should(o.ContainSubstring(`Saved credentials for "quay.io"`))
		setData, err := setDataForPullSecret(oc, newSecretFile)
		defer func() {
			_, err := setDataForPullSecret(oc, secretFile)
			o.Expect(err).NotTo(o.HaveOccurred())
		}()
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(setData).Should(o.Equal("secret/pull-secret data updated"))

		exutil.By("Wait for configuration to be applied in master and worker pools")
		mcpWorker := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		mcpMaster := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
		mcpWorker.waitForComplete()
		mcpMaster.waitForComplete()

		exutil.By("Check new generated rendered configs for newly added pull secret")
		renderedConfs, renderedErr := oc.AsAdmin().WithoutNamespace().Run("get").Args("mc", "--sort-by=metadata.creationTimestamp", "-o", "jsonpath='{.items[-2:].metadata.name}'").Output()
		o.Expect(renderedErr).NotTo(o.HaveOccurred())
		o.Expect(renderedConfs).NotTo(o.BeEmpty())
		slices := strings.Split(strings.Trim(renderedConfs, "'"), " ")
		var renderedMasterConf, renderedWorkerConf string
		for _, conf := range slices {
			if strings.Contains(conf, MachineConfigPoolMaster) {
				renderedMasterConf = conf
			} else if strings.Contains(conf, MachineConfigPoolWorker) {
				renderedWorkerConf = conf
			}
		}
		logger.Infof("New rendered config generated for master: %s", renderedMasterConf)
		logger.Infof("New rendered config generated for worker: %s", renderedWorkerConf)

		exutil.By("Check logs of machine-config-daemon on master-n-worker nodes, make sure pull secret changes are detected, drain and reboot are skipped")
		masterNode := NewNodeList(oc.AsAdmin()).GetAllMasterNodesOrFail()[0]
		workerNode := NewNodeList(oc.AsAdmin()).GetAllLinuxWorkerNodesOrFail()[0]
		commonExpectedStrings := []string{`Writing file "/var/lib/kubelet/config.json"`, "Changes do not require drain, skipping"}
		expectedStringsForMaster := append(commonExpectedStrings, "Node has Desired Config "+renderedMasterConf+", skipping reboot")
		expectedStringsForWorker := append(commonExpectedStrings, "Node has Desired Config "+renderedWorkerConf+", skipping reboot")
		masterMcdLogs, masterMcdLogErr := exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigDaemon, masterNode.GetMachineConfigDaemon(), "")
		o.Expect(masterMcdLogErr).NotTo(o.HaveOccurred())
		workerMcdLogs, workerMcdLogErr := exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigDaemon, workerNode.GetMachineConfigDaemon(), "")
		o.Expect(workerMcdLogErr).NotTo(o.HaveOccurred())
		foundOnMaster := containsMultipleStrings(masterMcdLogs, expectedStringsForMaster)
		o.Expect(foundOnMaster).Should(o.BeTrue())
		logger.Infof("MCD log on master node %s contains expected strings: %v", masterNode.name, expectedStringsForMaster)
		foundOnWorker := containsMultipleStrings(workerMcdLogs, expectedStringsForWorker)
		o.Expect(foundOnWorker).Should(o.BeTrue())
		logger.Infof("MCD log on worker node %s contains expected strings: %v", workerNode.name, expectedStringsForWorker)
	})

	g.It("[PolarionID:52520][OTP] Configure unqualified-search-registries in Image.config resource [Disruptive]", func() {
		expectedDropinFilePath := "/etc/containers/registries.conf.d/01-image-searchRegistries.conf"
		expectedDropinContent := "unqualified-search-registries = [\"quay.io\"]\nshort-name-mode = \"\"\nadditional-layer-store-auth-helper = \"\"\n"

		exutil.By("Get current image.config cluster configuration")
		ic := NewResource(oc.AsAdmin(), "image.config", "cluster")
		icInitialConfig := ic.GetOrFail(`{.spec}`)
		logger.Infof("Initial image.config cluster configuration: %s", icInitialConfig)

		wmcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		mmcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)

		workers, wsErr := wmcp.GetSortedNodes()
		o.Expect(wsErr).ShouldNot(o.HaveOccurred(), "Error getting the nodes in worker pool")

		masters, msErr := mmcp.GetSortedNodes()
		o.Expect(msErr).ShouldNot(o.HaveOccurred(), "Error getting the nodes in master pool")

		firstUpdatedWorker := workers[0]
		firstUpdatedMaster := masters[0]

		defer func() {
			logger.Infof("Start TC defer block")

			logger.Infof("Restore original image.config cluster config %s", icInitialConfig)
			_ = ic.Patch("json", `[{ "op": "add", "path": "/spec", "value": `+icInitialConfig+`}]`)

			logger.Infof("Wait for the original configuration to be applied")
			wmcp.waitForComplete()
			mmcp.waitForComplete()

			logger.Infof("End TC defer block")
		}()

		exutil.By("Add quay.io to unqualified-search-regisitries list in image.config cluster resource")
		startTime, dErr := firstUpdatedMaster.GetDate()
		o.Expect(dErr).ShouldNot(o.HaveOccurred(), "Error getting date in node %s", firstUpdatedMaster.GetName())

		o.Expect(firstUpdatedWorker.IgnoreEventsBeforeNow()).NotTo(o.HaveOccurred(),
			"Error getting the last event in node %s", firstUpdatedWorker.GetName())

		o.Expect(firstUpdatedMaster.IgnoreEventsBeforeNow()).NotTo(o.HaveOccurred(),
			"Error getting the last event in node %s", firstUpdatedMaster.GetName())

		patchErr := ic.Patch("merge", `{"spec": {"registrySources": {"containerRuntimeSearchRegistries":["quay.io"]}}}`)
		o.Expect(patchErr).ShouldNot(o.HaveOccurred(), "Error while partching the image.config cluster resource")

		exutil.By("Wait for first nodes to be configured")
		// Worker and master nodes should go into 'working' status
		o.Eventually(firstUpdatedWorker.IsUpdating, "8m", "20s").Should(o.BeTrue(),
			"Node %s is not in 'working' status after the new image.conig is configured", firstUpdatedWorker.GetName())
		o.Eventually(firstUpdatedMaster.IsUpdating, "8m", "20s").Should(o.BeTrue(),
			"Node %s is not in 'working' status after the new image.conig is configured", firstUpdatedMaster.GetName())

		// We dont actually wait for the whole configuration to be applied
		//  we will only wait for those nodes to be unpdated
		// Not waiting for the MCPs to finish the configuration makes this test case faster
		// If it causes unstability, just wait here for the MCPs to complete the configuration instead
		// Worker and master nodes should go into 'working' status
		o.Eventually(firstUpdatedWorker.IsUpdated, "10m", "20s").Should(o.BeTrue(),
			"Node %s is not in 'Done' status after the configuration is applied", firstUpdatedWorker.GetName())
		o.Eventually(firstUpdatedMaster.IsUpdated, "10m", "20s").Should(o.BeTrue(),
			"Node %s is not in 'Done' status after the configuration is applied", firstUpdatedMaster.GetName())

		exutil.By("Print all events for the verified worker node")
		el := NewEventList(oc.AsAdmin(), MachineConfigNamespace)
		el.ByFieldSelector(`involvedObject.name=` + firstUpdatedWorker.GetName())
		events, _ := el.GetAll()
		printString := ""
		for _, event := range events {
			printString += fmt.Sprintf("-  %s\n", event)
		}
		logger.Infof("All events for node %s:\n%s", firstUpdatedWorker.GetName(), printString)
		logger.Infof("OK!\n")

		exutil.By("Verify that a drain and reboot events were triggered for worker node")
		wEvents, weErr := firstUpdatedWorker.GetEvents()

		logger.Infof("All events for  node %s since: %s", firstUpdatedWorker.GetName(), firstUpdatedWorker.eventCheckpoint)
		for _, event := range wEvents {
			logger.Infof("-         %s", event)
		}
		o.Expect(weErr).ShouldNot(o.HaveOccurred(), "Error getting events for node %s", firstUpdatedWorker.GetName())
		o.Expect(wEvents).To(HaveEventsSequence("Drain", "Reboot"),
			"Error, the expected sequence of events is not found in node %s", firstUpdatedWorker.GetName())

		exutil.By("Verify that a drain and reboot events were triggered for master node")
		mEvents, meErr := firstUpdatedMaster.GetEvents()
		o.Expect(meErr).ShouldNot(o.HaveOccurred(), "Error getting drain events for node %s", firstUpdatedMaster.GetName())
		o.Expect(mEvents).To(HaveEventsSequence("Drain", "Reboot"),
			"Error, the expected sequence of events is not found in node %s", firstUpdatedMaster.GetName())

		exutil.By("Verify that the node was actually rebooted")
		o.Expect(firstUpdatedWorker.GetUptime()).Should(o.BeTemporally(">", startTime),
			"The node %s should have been rebooted after the configurion. Uptime didnt happen after start config time.", firstUpdatedWorker.GetName())
		o.Expect(firstUpdatedMaster.GetUptime()).Should(o.BeTemporally(">", startTime),
			"The node %s should have been rebooted after the configurion. Uptime didnt happen after start config time.", firstUpdatedMaster.GetName())

		exutil.By("Verify dropin file's content in worker node")
		wdropinFile := NewRemoteFile(firstUpdatedWorker, expectedDropinFilePath)
		wfetchErr := wdropinFile.Fetch()
		o.Expect(wfetchErr).ShouldNot(o.HaveOccurred(), "Error getting the content offile %s in node %s",
			expectedDropinFilePath, firstUpdatedWorker.GetName())

		o.Expect(wdropinFile.GetTextContent()).Should(o.Equal(expectedDropinContent))

		exutil.By("Verify dropin file's content in master node")
		mdropinFile := NewRemoteFile(firstUpdatedMaster, expectedDropinFilePath)
		mfetchErr := mdropinFile.Fetch()
		o.Expect(mfetchErr).ShouldNot(o.HaveOccurred(), "Error getting the content offile %s in node %s",
			expectedDropinFilePath, firstUpdatedMaster.GetName())

		o.Expect(mdropinFile.GetTextContent()).Should(o.Equal(expectedDropinContent))

	})

	g.It("[PolarionID:57595][OTP] Use empty pull-secret[Disruptive]", func() {
		var (
			pullSecret = GetPullSecret(oc.AsAdmin())
			wMcp       = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			mMcp       = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
			mco        = NewResource(oc.AsAdmin(), "co", "machine-config")
		)

		// If the cluster is using extensions, empty pull-secret will break the pools because images' validation is mandatory
		skipTestIfExtensionsAreUsed(oc.AsAdmin())
		// If RT kernel is enabled, empty pull-secrets will break the pools because the image's validation is mandatory
		skipTestIfRTKernel(oc.AsAdmin())

		exutil.By("Capture the current pull-secret value")
		// We don't use the pullSecret resource directly, instead we use auxiliary functions that will
		// extract and restore the secret's values using a file. Like that we can recover the value of the pull-secret
		// if our execution goes wrong, without printing it in the logs (for security reasons).
		secretFile, err := getPullSecret(oc)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the pull-secret")
		logger.Debugf("Pull-secret content stored in file %s", secretFile)
		defer func() {
			logger.Infof("Start defer func")
			logger.Infof("Restoring initial pull-secret value")
			output, err := setDataForPullSecret(oc, secretFile)
			if err != nil {
				logger.Errorf("Error restoring the pull-secret's value. Error: %s\nOutput: %s", err, output)
			}
			wMcp.waitForComplete()
			mMcp.waitForComplete()
			logger.Infof("End defer func")
		}()
		logger.Infof("OK!\n")

		exutil.By("Set an empty pull-secret")
		o.Expect(pullSecret.SetDataValue(".dockerconfigjson", "{}")).To(o.Succeed(),
			"Error setting an empty pull-secret value")

		logger.Infof("OK!\n")

		exutil.By("Wait for machine config poools to be udated")
		logger.Infof("Wait for worker pool to be updated")
		wMcp.waitForComplete()
		logger.Infof("Wait for master pool to be updated")
		mMcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Verify that the machine-config clusteroperator is not degraded")
		o.Consistently(mco, "3m", "30s").ShouldNot(BeDegraded(),
			"co/machine-config Degraded condition status is not the expected one: %s", mco)
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:61555][OTP] ImageDigestMirrorSet test [Disruptive]", func() {
		var (
			idmsName = "tc-61555-digest-mirror"
			mcp      = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
			node     = mcp.GetNodesOrFail()[0]
		)
		// ImageDigetsMirrorSet is not compatible with ImageContentSourcePolicy.
		// If any ImageContentSourcePolicy exists we skip this test case.
		skipTestIfImageContentSourcePolicyExists(oc.AsAdmin())

		exutil.By("Start capturing events and clean pods logs")
		startTime, dErr := node.GetDate()
		o.Expect(dErr).ShouldNot(o.HaveOccurred(), "Error getting date in node %s", node.GetName())

		o.Expect(node.IgnoreEventsBeforeNow()).NotTo(o.HaveOccurred(),
			"Error getting the latest event in node %s", node.GetName())

		logger.Infof("Removing all MCD pods to clean the logs.")
		o.Expect(RemoveAllMCDPods(oc)).To(o.Succeed(), "Error removing all MCD pods in %s namespace", MachineConfigNamespace)
		logger.Infof("OK!\n")

		exutil.By("Create new machine config to deploy a ImageDigestMirrorSet configuring a mirror registry")
		idms := NewImageDigestMirrorSet(oc.AsAdmin(), idmsName, *NewMCOTemplate(oc, "add-image-digest-mirror-set.yaml"))
		defer mcp.waitForComplete()
		defer idms.Delete()

		idms.Create("-p", "NAME="+idmsName)
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check logs to verify that no drain operation happened.")
		log, err := exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigDaemon, node.GetMachineConfigDaemon(), "")
		o.Expect(err).NotTo(o.HaveOccurred())

		o.Expect(log).Should(o.ContainSubstring("Changes do not require drain, skipping"))
		logger.Infof("OK!\n")

		exutil.By("Check logs to verify that crio service was reloaded.")
		o.Expect(log).Should(o.MatchRegexp(MCDCrioReloadedRegexp))
		logger.Infof("OK!\n")

		exutil.By("Verify that the node was NOT rebooted")
		o.Expect(node.GetUptime()).Should(o.BeTemporally("<", startTime),
			"The node %s must NOT be rebooted after applying the configuration, but it was rebooted. Uptime date happened after the start config time.", node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Check that no drain nor reboot events were triggered")
		nodeEvents, eErr := node.GetEvents()
		o.Expect(eErr).ShouldNot(o.HaveOccurred(), "Error getting drain events for node %s", node.GetName())
		o.Expect(nodeEvents).NotTo(HaveEventsSequence("Drain"), "Error, a Drain event was triggered but it shouldn't")
		o.Expect(nodeEvents).NotTo(HaveEventsSequence("Reboot"), "Error, a Reboot event was triggered but it shouldn't")
		logger.Infof("OK!\n")

		exutil.By("Check that the  /etc/containers/registries.conf file was configured")
		rf := NewRemoteFile(node, "/etc/containers/registries.conf")
		o.Expect(rf.Fetch()).To(o.Succeed(),
			"Error getting file /etc/containers/registries.conf")

		configRegex := `(?s)` + regexp.QuoteMeta(`[[registry]]`) + ".*" +
			regexp.QuoteMeta(`registry.access.redhat.com/ubi8/ubi-minimal`) + ".*" +
			regexp.QuoteMeta(`[[registry.mirror]]`) + ".*" +
			regexp.QuoteMeta(`example.io/digest-example/ubi-minimal`) + ".*" +
			`pull-from-mirror *= *"digest-only"`

		o.Expect(rf.GetTextContent()).To(o.MatchRegexp(configRegex),
			"The file /etc/containers/registries.conf has not been properly configured with the new mirror information")
		logger.Infof("OK!\n")

		exutil.By("Delete the ImageDigestMirrorSet resource")
		removeTime := node.GetDateOrFail()
		idms.Delete()
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check that the configuration in file /etc/containers/registries.conf was restored")
		o.Expect(rf.Fetch()).To(o.Succeed(),
			"Error getting file /etc/containers/registries.conf")

		o.Expect(rf.GetTextContent()).NotTo(o.ContainSubstring(`example.io/digest-example/ubi-minimal`),
			"The configuration in file /etc/containers/registries.conf was not restored after deleting the ImageDigestMirrorSet resource")
		logger.Infof("OK!\n")

		checkMirrorRemovalDefaultEvents(node, removeTime)

	})

	g.It("[PolarionID:61558][OTP] ImageTagMirrorSet test [Disruptive]", func() {
		var (
			itmsName = "tc-61558-tag-mirror"
			mcp      = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
			node     = mcp.GetNodesOrFail()[0]
		)

		// ImageTagMirrorSet is not compatible with ImageContentSourcePolicy.
		// If any ImageContentSourcePolicy exists we skip this test case.
		skipTestIfImageContentSourcePolicyExists(oc.AsAdmin())

		exutil.By("Start capturing events and clean pods logs")
		startTime, dErr := node.GetDate()
		o.Expect(dErr).ShouldNot(o.HaveOccurred(), "Error getting date in node %s", node.GetName())

		o.Expect(node.IgnoreEventsBeforeNow()).NotTo(o.HaveOccurred(),
			"Error getting the latest event in node %s", node.GetName())

		logger.Infof("Removing all MCD pods to clean the logs.")
		o.Expect(RemoveAllMCDPods(oc)).To(o.Succeed(), "Error removing all MCD pods in %s namespace", MachineConfigNamespace)
		logger.Infof("OK!\n")

		exutil.By("Create new machine config to deploy a ImageTagMirrorSet configuring a mirror registry")
		itms := NewImageTagMirrorSet(oc.AsAdmin(), itmsName, *NewMCOTemplate(oc, "add-image-tag-mirror-set.yaml"))
		defer mcp.waitForComplete()
		defer itms.Delete()

		itms.Create("-p", "NAME="+itmsName)
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check logs to verify that a drain operation was executed.")
		log, err := exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigDaemon, node.GetMachineConfigDaemon(), "")
		o.Expect(err).NotTo(o.HaveOccurred())

		o.Expect(log).Should(o.ContainSubstring("requesting cordon and drain"))
		logger.Infof("OK!\n")

		exutil.By("Check logs to verify that crio service was reloaded.")
		o.Expect(log).Should(o.MatchRegexp(MCDCrioReloadedRegexp))
		logger.Infof("OK!\n")

		exutil.By("Verify that the node was NOT rebooted")
		o.Expect(node.GetUptime()).Should(o.BeTemporally("<", startTime),
			"The node %s must NOT be rebooted after applying the configuration, but it was rebooted. Uptime date happened after the start config time.", node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Check no reboot events were triggered")
		nodeEvents, eErr := node.GetEvents()
		o.Expect(eErr).ShouldNot(o.HaveOccurred(), "Error getting drain events for node %s", node.GetName())
		o.Expect(nodeEvents).NotTo(HaveEventsSequence("Reboot"), "Error, a Reboot event was triggered but it shouldn't")
		logger.Infof("OK!\n")

		exutil.By("Check that drain events were triggered")
		o.Expect(nodeEvents).To(HaveEventsSequence("Drain"), "Error, a Drain event was expected but none was triggered")
		logger.Infof("OK!\n")

		exutil.By("Check that the  /etc/containers/registries.conf file was configured")
		rf := NewRemoteFile(node, "/etc/containers/registries.conf")
		o.Expect(rf.Fetch()).To(o.Succeed(),
			"Error getting file /etc/containers/registries.conf")

		configRegex := `(?s)` + regexp.QuoteMeta(`[[registry]]`) + ".*" +
			regexp.QuoteMeta(`registry.redhat.io/openshift4`) + ".*" +
			regexp.QuoteMeta(`[[registry.mirror]]`) + ".*" +
			regexp.QuoteMeta(`mirror.example.com/redhat`) + ".*" +
			`pull-from-mirror *= *"tag-only"`

		o.Expect(rf.GetTextContent()).To(o.MatchRegexp(configRegex),
			"The file /etc/containers/registries.conf has not been properly configured with the new mirror information")
		logger.Infof("OK!\n")

		exutil.By("Delete the ImageTagMirrorSet resource")
		removeTime := node.GetDateOrFail()
		itms.Delete()
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check that the configuration in file /etc/containers/registries.conf was restored")
		o.Expect(rf.Fetch()).To(o.Succeed(),
			"Error getting file /etc/containers/registries.conf")

		o.Expect(rf.GetTextContent()).NotTo(o.ContainSubstring(`example.io/digest-example/ubi-minimal`),
			"The configuration in file /etc/containers/registries.conf was not restored after deleting the ImageTagMirrorSet resource")
		logger.Infof("OK!\n")

		checkMirrorRemovalDefaultEvents(node, removeTime)
	})

	g.It("[PolarionID:66046][OTP] Check image registry certificates", func() {

		if !IsCapabilityEnabled(oc, "ImageRegistry") {
			g.Skip("ImageRegistry is not installed, skip this test")
		}

		var (
			mcp  = GetCompactCompatiblePool(oc.AsAdmin())
			node = mcp.GetNodesOrFail()[0]
			cc   = NewControllerConfig(oc.AsAdmin(), "machine-config-controller")
		)

		imageRegistryCerts, err := GetImageRegistryCertificates(oc.AsAdmin())
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the image registry certificates")

		for certFile, certValue := range imageRegistryCerts {
			logger.Infof("Checking Certfile: %s", certFile)

			exutil.By(fmt.Sprintf("Check that the ControllerConfig resource has the right value for bundle file %s", certFile))
			ccImageRegistryBundle, err := cc.GetImageRegistryBundleDataByFileName(certFile)
			o.Expect(err).NotTo(o.HaveOccurred(),
				"Error getting the image registry bundle in file %s in the ControllerConfig resource",
				certFile)

			o.Expect(ccImageRegistryBundle == certValue).To(o.BeTrue(),
				"The ControllerConfig resource does not have the right value for the image registry bundle %s",
				certFile)

			logger.Infof("OK!\n")

			exutil.By(fmt.Sprintf("Check that the ControllerConfig resource reports the right information about bundle file %s", certFile))
			certInfo, err := GetCertificatesInfoFromPemBundle(certFile, []byte(certValue))
			o.Expect(err).NotTo(o.HaveOccurred(),
				"Error extracting certificate info from %s pem bundle", certFile)

			ccCertInfo, err := cc.GetCertificatesInfoByBundleFileName(certFile)
			o.Expect(err).NotTo(o.HaveOccurred(),
				"Error getting the controller config information for %s certificates", certFile)

			o.Expect(certInfo).To(o.Equal(ccCertInfo),
				"The ControllerConfig is not reporting the right information about the certificates in %s bundle",
				certFile)

			logger.Infof("OK!\n")

			exutil.By(fmt.Sprintf("Check that the file %s has been added to the managed merged trusted image registry configmap", certFile))

			o.Eventually(GetManagedMergedTrustedImageRegistryCertificates, "20s", "10s").WithArguments(oc.AsAdmin()).Should(o.HaveKey(certFile),
				"The certificate for file %s has not been included in the configmap merged-trusted-image-registry-ca -n openshift-config-managed", certFile)

			mmtImageRegistryCert, err := GetManagedMergedTrustedImageRegistryCertificates(oc.AsAdmin())
			o.Expect(err).NotTo(o.HaveOccurred(), "Error getting managed merged trusted image registry certificates values")

			o.Expect(mmtImageRegistryCert[certFile] == certValue).To(o.BeTrue(),
				"The certificate in file %s was added to configmap merged-trusted-image-registry-ca -n openshift-config-managed but it has the wrong content", certFile)

			logger.Infof("OK!\n")

			exutil.By(fmt.Sprintf("Check that the file %s has been added nodes", certFile))
			// the filename stored in configmap uses "..", but it is translated to ":" in the node.
			// so we replace the ".." with ":"
			decodedFileName := strings.ReplaceAll(certFile, "..", ":")
			remotePath := ImageRegistryCertificatesDir + "/" + decodedFileName + "/" + ImageRegistryCertificatesFileName
			rfCert := NewRemoteFile(node, remotePath)

			o.Eventually(func(gm o.Gomega) { // Passing o.Gomega as parameter we can use assertions inside the Eventually function without breaking the retries.
				gm.Expect(rfCert.Fetch()).To(o.Succeed(),
					"Cannot read the certificate file %s in node:%s ", rfCert.fullPath, node.GetName())

				gm.Expect(rfCert.GetTextContent() == certValue).To(o.BeTrue(),
					"the certificate stored in file %s does not match the expected value", rfCert.fullPath)
			}, "1m", "10s").
				Should(o.Succeed(),
					"The file %s in node %s does not contain the right certificate.", rfCert.GetFullPath(), node.GetName())
			logger.Infof("OK!\n")
		}

		// If there is no certificate configured, we check that the controlleconfig has empty data
		if len(imageRegistryCerts) == 0 {
			exutil.By("No certificates configured. Check that ControllerConfig has empty certificates too")
			o.Eventually(cc.GetImageRegistryBundleData, "30s", "10s").Should(o.BeEmpty(),
				"There are no certificates configured in 'image-registry-ca' configmap but ControllerConfig is not showing empty data")
			logger.Infof("OK!\n")
		}
	})

	g.It("[PolarionID:68736][OTP] machine config server supports bootstrap with IR certs [Serial]", func() {
		var (
			mcsBinary              = "/usr/bin/machine-config-server"
			bootstrapSubCmd        = "bootstrap"
			expectedBootstrapHelp  = "--bootstrap-certs stringArray   a certificate bundle formatted in a string array with the format key=value,key=value"
			controllerPodName, err = NewController(oc.AsAdmin()).GetPodName()
		)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the MCO controller pod to check the bootstrap-certs flag in machine-config-server")

		exutil.By(fmt.Sprintf("Check that the bootstrap-certs flag is present in the command: %s %s -h", mcsBinary, bootstrapSubCmd))
		o.Eventually(exutil.RemoteShPod, "2m", "20s").
			WithArguments(oc.AsAdmin(), MachineConfigNamespace, controllerPodName, mcsBinary, bootstrapSubCmd, "-h").
			Should(o.ContainSubstring(expectedBootstrapHelp),
				"The --bootstrap-certs flag is not available in the machine-config-server binary")
		exutil.By("OK!\n")
	})
})

func skipTestIfImageContentSourcePolicyExists(oc *exutil.CLI) {
	output, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("ImageContentSourcePolicy").Output()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error checking if ImageContentSourcePolicy exist in the cluster or not")

	if output != "No resources found" {
		logger.Infof("ImageContentSourcePolicy in cluster:\n%s", output)
		g.Skip("There are ImageContentSourcePolicies resources in the cluster. This test case is not compatible with ImageContentSourcePolicies resources.")
	}
}

func checkMirrorRemovalDefaultEvents(node *Node, removeTime time.Time) {
	exutil.By("Check that a drain event was triggered but no Reboot event happened")
	o.Expect(node.GetEvents()).To(
		o.And(
			HaveEventsSequence("Drain"),
			o.Not(HaveEventsSequence("Reboot"))),
		"The triggered events are not the expected ones")
	logger.Infof("OK!\n")

	exutil.By("Check that a drain was executed, reboot was skipped and crio was restarted")
	o.Expect(exutil.GetSpecificPodLogs(node.oc, MachineConfigNamespace, MachineConfigDaemon, node.GetMachineConfigDaemon(), "")).Should(
		o.And(
			o.ContainSubstring("requesting cordon and drain via annotation to controller"),
			o.ContainSubstring("drain complete"),
			o.MatchRegexp(MCDCrioReloadedRegexp)))

	o.Expect(node.GetUptime()).Should(o.BeTemporally("<", removeTime),
		"The node %s must NOT be rebooted after removing the configuration, but it was rebooted. Uptime date happened after the start config time.", node.GetName())

	logger.Infof("OK!\n")
}
