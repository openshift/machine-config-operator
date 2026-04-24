package extended

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"k8s.io/apimachinery/pkg/util/wait"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO daemon", func() {
	defer g.GinkgoRecover()

	var oc = exutil.NewCLI("mco-daemon", exutil.KubeConfigPath())

	g.JustBeforeEach(func() {
		PreChecks(oc)
	})

	g.It("[PolarionID:43084][OTP] shutdown machine config daemon with SIGTERM", func() {
		exutil.By("Create new machine config to add additional ssh key")
		var (
			mcp    = GetCompactCompatiblePool(oc.AsAdmin())
			node   = mcp.GetSortedNodesOrFail()[0]
			mcName = "tc-43084-ssh-authorized-key"

			_, pubKey1 = GenerateSSHKeyPairOrFail()
			_, pubKey2 = GenerateSSHKeyPairOrFail()
			user       = ign32PaswdUser{Name: "core", SSHAuthorizedKeys: []string{pubKey1, pubKey2}}

			mc = NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName())
		)

		exutil.By("Creating MachineConfig to observe SIGTERM protection")
		mc.parameters = []string{fmt.Sprintf(`PWDUSERS=[%s]`, MarshalOrFail(user))}
		mc.skipWaitForMcp = true

		defer mc.DeleteWithWait()
		mc.create()
		logger.Infof("OK!\n")

		exutil.By("Check MCD logs to make sure shutdown machine config daemon with SIGTERM")
		logger.Infof("No reboot will happen, wait until MCD logs show the expected messages")
		o.Eventually(node.GetMCDaemonLogs, "20m", "20s").WithArguments("").Should(
			o.And(
				o.ContainSubstring("Adding SIGTERM protection"),
				o.ContainSubstring("Removing SIGTERM protection")),
			"Node %s MCD logs should contain messages telling that SIGTERM protection was added and removed", node)
		logger.Infof("OK!\n")

		exutil.By("Wait for the configuration to be fully applied")
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Kill MCD process")
		mcdKillLogs, err := node.DebugNodeWithChroot("pgrep", "-f", "machine-config-daemon_")
		o.Expect(err).NotTo(o.HaveOccurred())
		mcpPid := regexp.MustCompile("(?m)^[0-9]+").FindString(mcdKillLogs)
		_, err = node.DebugNodeWithChroot("kill", mcpPid)
		o.Expect(err).NotTo(o.HaveOccurred())
		logger.Infof("OK!\n")

		exutil.By("Check MCD logs to make sure machine config daemon without SIGTERM")
		mcDaemon := node.GetMachineConfigDaemon()

		exutil.AssertPodToBeReady(oc, mcDaemon, MachineConfigNamespace)
		mcdLogs, err := exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigDaemon, mcDaemon, "")
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(mcdLogs).ShouldNot(o.ContainSubstring("SIGTERM"))
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:42704][OTP] disable auto reboot for mco", func() {
		exutil.By("pause mcp worker")
		mcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		defer mcp.pause(false)
		mcp.pause(true)

		exutil.By("create new mc")
		mcName := "ztc-42704-change-workers-chrony-configuration"
		mcTemplate := "change-workers-chrony-configuration.yaml"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker).SetMCOTemplate(mcTemplate)
		mc.skipWaitForMcp = true
		defer mc.DeleteWithWait()
		mc.create()

		exutil.By("compare config name b/w spec.configuration.name and status.configuration.name, they're different")
		specConf, specErr := mcp.getConfigNameOfSpec()
		o.Expect(specErr).NotTo(o.HaveOccurred())
		statusConf, statusErr := mcp.getConfigNameOfStatus()
		o.Expect(statusErr).NotTo(o.HaveOccurred())
		o.Expect(specConf).ShouldNot(o.Equal(statusConf))

		exutil.By("check mcp status condition, expected: UPDATED=False && UPDATING=False")
		var updated, updating string
		immediate := false
		pollerr := wait.PollUntilContextTimeout(context.TODO(), 5*time.Second, 10*time.Second, immediate, func(_ context.Context) (bool, error) {
			stdouta, erra := mcp.Get(`{.status.conditions[?(@.type=="Updated")].status}`)
			stdoutb, errb := mcp.Get(`{.status.conditions[?(@.type=="Updating")].status}`)
			updated = strings.Trim(stdouta, "'")
			updating = strings.Trim(stdoutb, "'")
			if erra != nil || errb != nil {
				logger.Errorf("error occurred %v%v", erra, errb)
				return false, nil
			}
			if updated != "" && updating != "" {
				logger.Infof("updated: %v, updating: %v", updated, updating)
				return true, nil
			}
			return false, nil
		})
		exutil.AssertWaitPollNoErr(pollerr, "polling status conditions of mcp: [Updated,Updating] failed")
		o.Expect(updated).Should(o.Equal("False"))
		o.Expect(updating).Should(o.Equal("False"))

		exutil.By("unpause mcp worker, then verify whether the new mc can be applied on mcp/worker")
		mcp.pause(false)
		mcp.waitForComplete()
	})

	g.It("[PolarionID:43085][OTP] check mcd crash-loop-back-off error in log", func() {
		exutil.By("get master and worker nodes")
		workerNode := NewNodeList(oc.AsAdmin()).GetAllLinuxWorkerNodesOrFail()[0]
		masterNode := NewNodeList(oc.AsAdmin()).GetAllMasterNodesOrFail()[0]
		logger.Infof("master node %s", masterNode)
		logger.Infof("worker node %s", workerNode)

		exutil.By("check error messages in mcd logs for both master and worker nodes")
		expectedStrings := []string{"unable to update node", "cannot apply annotation for SSH access due to"}
		masterMcdLogs, masterMcdLogErr := exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigDaemon, masterNode.GetMachineConfigDaemon(), "")
		o.Expect(masterMcdLogErr).NotTo(o.HaveOccurred())
		workerMcdLogs, workerMcdLogErr := exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigDaemon, workerNode.GetMachineConfigDaemon(), "")
		o.Expect(workerMcdLogErr).NotTo(o.HaveOccurred())
		foundOnMaster := containsMultipleStrings(masterMcdLogs, expectedStrings)
		o.Expect(foundOnMaster).Should(o.BeFalse())
		logger.Infof("mcd log on master node %s does not contain error messages: %v", masterNode.name, expectedStrings)
		foundOnWorker := containsMultipleStrings(workerMcdLogs, expectedStrings)
		o.Expect(foundOnWorker).Should(o.BeFalse())
		logger.Infof("mcd log on worker node %s does not contain error messages: %v", workerNode.name, expectedStrings)
	})

	g.It("[PolarionID:68687][OTP] HostToContainer propagation in MCD", func() {

		platform := exutil.CheckPlatform(oc)
		assertFunc := func(gm o.Gomega, mountPropagations string) {
			logger.Infof("mountPropagations:\n %s", mountPropagations)
			for _, mp := range strings.Split(mountPropagations, " ") {
				gm.Expect(mp).Should(o.Equal("HostToContainer"), "mountPropagation value is not expected [%s]", mp)
			}
		}

		exutil.By("Check mountPropagation for the pods under mco namespace")
		mountPropagations := NewNamespacedResourceList(oc.AsAdmin(), "pod", MachineConfigNamespace).GetOrFail(`{.items[*].spec.containers[*].volumeMounts[?(@.mountPath=="/rootfs")].mountPropagation}`)
		o.Eventually(assertFunc).WithArguments(mountPropagations).Should(o.Succeed())

		if ns, ok := OnPremPlatforms[platform]; ok {
			exutil.By(fmt.Sprintf("Check mountPropagation for the pods on platform %s", platform))
			mountPropagations = NewNamespacedResourceList(oc.AsAdmin(), "pod", ns).GetOrFail(`{.items[*].spec.containers[*].volumeMounts[*].mountPropagation}`)
			if strings.TrimSpace(mountPropagations) != "" {
				o.Eventually(assertFunc).WithArguments(mountPropagations).Should(o.Succeed())
			}
		}

		if platform == GCPPlatform || platform == AzurePlatform || platform == AlibabaCloudPlatform {
			exutil.By("Check mountPropagation for the apiserver-watcher pods under openshift-kube-apiserver namespace")
			pods, err := NewNamespacedResourceList(oc.AsAdmin(), "pod", "openshift-kube-apiserver").GetAll()
			o.Expect(err).NotTo(o.HaveOccurred(), "Get pod list under ns/openshift-kube-apiserver failed")
			for _, pod := range pods {
				if strings.HasPrefix(pod.GetName(), "apiserver-watcher") {
					mountPropagations = pod.GetOrFail(`{.spec.containers[*].volumeMounts[?(@.mountPath=="/rootfs")].mountPropagation}`)
					o.Eventually(assertFunc).WithArguments(mountPropagations).Should(o.Succeed())
				}
			}

		}
	})

	g.It("[PolarionID:68688][OTP] kubeconfig must have 600 permissions in all nodes", func() {
		var (
			filePath = "/etc/kubernetes/kubeconfig"
		)

		exutil.By(fmt.Sprintf("Check file permission of %s on all nodes, 0600 is expected", filePath))
		nodes, err := NewNodeList(oc.AsAdmin()).GetAllLinux()
		o.Expect(err).NotTo(o.HaveOccurred(), "Get all cluster nodes failed")
		for _, node := range nodes {
			logger.Infof("Checking file permission of %s on node %s", filePath, node.GetName())
			file := NewRemoteFile(node, filePath)
			o.Expect(file.Stat()).NotTo(o.HaveOccurred(), "stat cmd is failed on node %s", node.GetName())
			o.Expect(file.GetNpermissions()).Should(o.Equal("0600"), "file permission is not expected %s", file.GetNpermissions())
			logger.Infof("File permission is expected")
		}

	})

	g.It("[PolarionID:69091][OTP] Machine-Config-Operator skips reboot when configuration matches during node bootstrap pivot", func() {
		var (
			MachineConfigDaemonFirstbootService = "machine-config-daemon-firstboot.service"
		)

		if !IsInstalledWithAssistedInstallerOrFail(oc.AsAdmin()) {
			g.Skip("This test can only be executed in clusters installed with assisted-installer. This cluster was not installed using assisted-installer.")
		}

		exutil.By("Check that the first reboot is skipped")
		coreOsNode := NewNodeList(oc.AsAdmin()).GetAllCoreOsNodesOrFail()[0]

		logger.Infof("Using node %s", coreOsNode.GetName())
		o.Eventually(coreOsNode.GetJournalLogs, "30s", "10s").WithArguments("-u", MachineConfigDaemonFirstbootService).
			Should(o.And(
				o.ContainSubstring("Starting Machine Config Daemon Firstboot"),
				o.Not(o.ContainSubstring(`Changes queued for next boot. Run "systemctl reboot" to start a reboot`)),
				o.Not(o.ContainSubstring(`initiating reboot`)),
			),
				"The %s service should have skipped the first reboot, but it didn't", MachineConfigDaemonFirstbootService)
		exutil.By("OK!\n")
	})

	g.It("[PolarionID:68682][OTP] daemon should not pull baremetalRuntimeCfg every time", func() {
		SkipIfNotOnPremPlatform(oc.AsAdmin())
		skipTestIfNotIPI(oc.AsAdmin())

		resolvPrependerService := "on-prem-resolv-prepender.service"

		nodes, err := NewNodeList(oc.AsAdmin()).GetAllLinux()
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Could not get the list of Linux nodes")

		for _, node := range nodes {
			exutil.By(fmt.Sprintf("Check %s in node %s", resolvPrependerService, node.GetName()))

			o.Eventually(node.GetJournalLogs, "5s", "1s").WithArguments("-u", resolvPrependerService).Should(
				o.ContainSubstring("Image exists, no need to download"),
				"%s should not try to download images more than once. Check OCPBUGS-18772.", resolvPrependerService,
			)

			logger.Infof("OK!\n")
		}
	})

	g.It("[PolarionID:68686][OTP] MCD No invalid memory address or nil pointer dereference when kubeconfig file is not present in a node", func() {
		var (
			node           = GetCompactCompatiblePool(oc.AsAdmin()).GetNodesOrFail()[0]
			kubeconfig     = "/etc/kubernetes/kubeconfig"
			kubeconfigBack = kubeconfig + ".back"
		)
		logger.Infof("Using node %s for testing", node.GetName())

		defer func() {
			logger.Infof("Starting defer logic")
			_, err := node.DebugNodeWithChroot("mv", kubeconfigBack, kubeconfig)
			if err != nil {
				logger.Errorf("Error restoring the original kubeconfigfile: %s", err)
			}

			err = NewNamespacedResource(oc.AsAdmin(), "pod", MachineConfigNamespace, node.GetMachineConfigDaemon()).Delete()
			if err != nil {
				logger.Errorf("Error deleting the MCD pod to restore the original kubeconfigfile: %s", err)
			}

			exutil.AssertAllPodsToBeReady(oc.AsAdmin(), MachineConfigNamespace)
			logger.Infof("Defer logic finished")

		}()

		exutil.By(fmt.Sprintf("Remove the %s file", kubeconfig))
		_, err := node.DebugNodeWithChroot("mv", kubeconfig, kubeconfigBack)
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error removing the file %s from node %s", kubeconfig, node.GetName())

		logger.Infof("File %s was moved to %s", kubeconfig, kubeconfigBack)
		logger.Infof("OK!\n")

		exutil.By("Remove the MCDs pod")
		mcdPodName := node.GetMachineConfigDaemon()
		mcdPod := NewNamespacedResource(oc.AsAdmin(), "pod", MachineConfigNamespace, mcdPodName)
		o.Expect(
			mcdPod.Delete(),
		).To(
			o.Succeed(),
			"Error deleting the MCD pod %s for node %s", mcdPod.GetName(), node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Check that the pod failed but did not panic")
		logger.Infof("Check that the pod is failing")
		o.Eventually(
			node.GetMachineConfigDaemon, "2m", "10s",
		).ShouldNot(
			o.Equal(mcdPodName),
			"A new MCD pod should be created after removing the old one, but no new MCD pod was created")

		mcdPod = NewNamespacedResource(oc.AsAdmin(), "pod", MachineConfigNamespace, node.GetMachineConfigDaemon())
		o.Eventually(
			mcdPod.Get, "2m", "10s",
		).WithArguments(`{.status.containerStatuses[?(@.name=="machine-config-daemon")].state.terminated}`).ShouldNot(o.Or(
			o.BeEmpty(),
			o.ContainSubstring("panic:"),
		), "The new MCD pod should fail without panic because the file %s is not available", kubeconfig)

		logger.Infof("Check pod logs to make sure that it did not panic")
		o.Consistently(
			node.GetMCDaemonLogs, "1m", "20s",
		).WithArguments("").ShouldNot(
			o.ContainSubstring("panic:"),
			"The new MCD pod should not panic")

		logger.Infof("OK!\n")

	})
})
