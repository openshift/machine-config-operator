package mco

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/architecture"
	clusterinfra "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/clusterinfra"

	logger "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/logext"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	ogs "github.com/onsi/gomega/gstruct"

	exutil "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util"
	bootstrap "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/bootstrap"
	"k8s.io/apimachinery/pkg/util/wait"
)

var _ = g.Describe("[sig-mco] MCO", func() {
	defer g.GinkgoRecover()

	var oc = exutil.NewCLI("mco", exutil.KubeConfigPath())

	g.JustBeforeEach(func() {
		preChecks(oc)
	})

	g.It("NonHyperShiftHOST-Author:rioliu-Critical-42347-health check for machine-config-operator [Serial]", func() {
		exutil.By("make sure that pools are fully updated before executing the checks")
		// If any previous test created a MC, it can happen that pools are updating and will report Upgradeable=False, failing the test
		// The checks in this test case only make sense in a cluster that is not updating any pool
		mMcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
		mMcp.waitForComplete()
		wMcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		wMcp.waitForComplete()

		exutil.By("checking mco status")
		co := NewResource(oc.AsAdmin(), "co", "machine-config")
		coStatus := co.GetOrFail(`{range .status.conditions[*]}{.type}{.status}{"\n"}{end}`)
		logger.Infof(coStatus)
		o.Expect(coStatus).Should(
			o.And(
				o.ContainSubstring("ProgressingFalse"),
				o.ContainSubstring("UpgradeableTrue"),
				o.ContainSubstring("DegradedFalse"),
				o.ContainSubstring("AvailableTrue"),
			), "CO machine-config does not have the right condition status.\n%s", co.PrettyString())

		logger.Infof("machine config operator is healthy")

		exutil.By("checking mco pod status")
		pod := NewNamespacedResource(oc.AsAdmin(), "pods", MachineConfigNamespace, "")
		podStatus := pod.GetOrFail(`{.items[*].status.conditions[?(@.type=="Ready")].status}`)
		logger.Infof(podStatus)
		o.Expect(podStatus).ShouldNot(o.ContainSubstring("False"))
		logger.Infof("mco pods are healthy")

		exutil.By("checking mcp status")
		mcp := NewResource(oc.AsAdmin(), "mcp", "")
		mcpStatus := mcp.GetOrFail(`{.items[*].status.conditions[?(@.type=="Degraded")].status}`)
		logger.Infof(mcpStatus)
		o.Expect(mcpStatus).ShouldNot(o.ContainSubstring("True"))
		logger.Infof("mcps are not degraded")

	})

	g.It("Author:rioliu-Longduration-NonPreRelease-Critical-42361-add chrony systemd config [Disruptive]", func() {
		exutil.By("create new mc to apply chrony config on worker nodes")
		mcp := GetCompactCompatiblePool(oc)
		node := mcp.GetSortedNodesOrFail()[0]
		mcName := "ztc-42361-change-workers-chrony-configuration"
		mcTemplate := "change-workers-chrony-configuration.yaml"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName()).SetMCOTemplate(mcTemplate)

		defer mc.delete()
		mc.create()

		exutil.By("get one node to verify the config changes")
		stdout, err := node.DebugNodeWithChroot("cat", "/etc/chrony.conf")
		o.Expect(err).NotTo(o.HaveOccurred())
		logger.Infof(stdout)
		o.Expect(stdout).Should(
			o.And(
				o.ContainSubstring("pool 0.rhel.pool.ntp.org iburst"),
				o.ContainSubstring("driftfile /var/lib/chrony/drift"),
				o.ContainSubstring("makestep 1.0 3"),
				o.ContainSubstring("rtcsync"),
				o.ContainSubstring("logdir /var/log/chrony"),
			))

	})

	g.It("Author:rioliu-Longduration-NonPreRelease-High-42520-retrieve mc with large size from mcs [Disruptive]", func() {
		exutil.By("create new mc to add 100+ dummy files to /var/log")
		mcName := "bz1866117-add-dummy-files"
		mcTemplate := "bz1866117-add-dummy-files.yaml"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker).SetMCOTemplate(mcTemplate)
		defer mc.delete()
		mc.create()

		exutil.By("get one master node to do mc query")
		masterNode := NewNodeList(oc).GetAllMasterNodesOrFail()[0]
		stdout, err := masterNode.DebugNode("curl", "-w", "'Total: %{time_total}'", "-k", "-s", "-o", "/dev/null", "https://localhost:22623/config/worker")
		o.Expect(err).NotTo(o.HaveOccurred())

		var timecost float64
		for _, line := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
			if strings.Contains(line, "Total") {
				substr := line[8 : len(line)-1]
				timecost, _ = strconv.ParseFloat(substr, 64)
				break
			}
		}
		logger.Infof("api time cost is: %f", timecost)
		o.Expect(timecost).Should(o.BeNumerically("<", 10.0))
	})

	g.It("Author:mhanss-LEVEL0-NonPreRelease-Longduration-Critical-43048-Critical-43064-create/delete custom machine config pool [Disruptive]", func() {
		if IsCompactOrSNOCluster(oc) {
			g.Skip("This test case cannot be executed in SNO or Compact clusters")
		}
		exutil.By("get worker node to change the label")
		nodeList := NewNodeList(oc)
		workerNode := nodeList.GetAllLinuxWorkerNodesOrFail()[0]

		exutil.By("Add label as infra to the existing node")
		infraLabel := "node-role.kubernetes.io/infra"
		err := workerNode.AddLabel(infraLabel, "")
		defer func() {
			// ignore output, just focus on error handling, if error is occurred, fail this case
			_, deletefailure := workerNode.DeleteLabel(infraLabel)
			o.Expect(deletefailure).NotTo(o.HaveOccurred())
		}()
		o.Expect(err).NotTo(o.HaveOccurred())
		nodeLabel, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("nodes/" + workerNode.name).Output()
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(nodeLabel).Should(o.ContainSubstring("infra"))

		exutil.By("Create custom infra mcp")
		mcpName := "infra"
		mcpTemplate := generateTemplateAbsolutePath("custom-machine-config-pool.yaml")
		mcp := NewMachineConfigPool(oc.AsAdmin(), mcpName)
		mcp.template = mcpTemplate
		defer mcp.delete()
		// We need to wait for the label to be delete before removing the MCP. Otherwise the worker pool
		// becomes Degraded.
		defer func() {
			// ignore output, just focus on error handling, if error is occurred, fail this case
			_, deletefailure := workerNode.DeleteLabel(infraLabel)
			o.Expect(deletefailure).NotTo(o.HaveOccurred())
			_ = workerNode.WaitForLabelRemoved(infraLabel)
			if mcp.Exists() {
				_ = mcp.WaitForMachineCount(0, 5*time.Minute)
			}
		}()
		mcp.create()

		exutil.By("Check MCP status")
		o.Consistently(mcp.pollDegradedMachineCount(), "30s", "10s").Should(o.Equal("0"), "There are degraded nodes in pool")
		o.Eventually(mcp.pollDegradedStatus(), "1m", "20s").Should(o.Equal("False"), "The pool status is 'Degraded'")
		o.Eventually(mcp.pollUpdatedStatus(), "1m", "20s").Should(o.Equal("True"), "The pool is reporting that it is not updated")
		o.Eventually(mcp.pollMachineCount(), "1m", "10s").Should(o.Equal("1"), "The pool should report 1 machine count")
		o.Eventually(mcp.pollReadyMachineCount(), "1m", "10s").Should(o.Equal("1"), "The pool should report 1 machine ready")

		logger.Infof("Custom mcp is created successfully!")

		exutil.By("Remove custom label from the node")
		unlabeledOutput, err := workerNode.DeleteLabel(infraLabel)
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(unlabeledOutput).Should(o.ContainSubstring(workerNode.name))
		o.Expect(workerNode.WaitForLabelRemoved(infraLabel)).Should(o.Succeed(),
			fmt.Sprintf("Label %s has not been removed from node %s", infraLabel, workerNode.GetName()))
		logger.Infof("Label removed")

		exutil.By("Check custom infra label is removed from the node")
		nodeList.ByLabel(infraLabel)
		infraNodes, err := nodeList.GetAll()
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(len(infraNodes)).Should(o.Equal(0))

		exutil.By("Verify that the information is updated in MCP")
		o.Eventually(mcp.pollUpdatedStatus(), "5m", "20s").Should(o.Equal("True"), "The pool is reporting that it is not updated")
		o.Eventually(mcp.pollMachineCount(), "5m", "20s").Should(o.Equal("0"), "The pool should report 0 machine count")
		o.Eventually(mcp.pollReadyMachineCount(), "5m", "20s").Should(o.Equal("0"), "The pool should report 0 machine ready")
		o.Consistently(mcp.pollDegradedMachineCount(), "30s", "10s").Should(o.Equal("0"), "There are degraded nodes in pool")
		o.Eventually(mcp.pollDegradedStatus(), "5m", "20s").Should(o.Equal("False"), "The pool status is 'Degraded'")

		exutil.By("Remove custom infra mcp")
		mcp.delete()

		exutil.By("Check custom infra mcp is deleted")
		mcpOut, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("mcp/" + mcpName).Output()
		o.Expect(err).Should(o.HaveOccurred())
		o.Expect(mcpOut).Should(o.ContainSubstring("NotFound"))
		logger.Infof("Custom mcp is deleted successfully!")
	})

	g.It("Author:mhanss-Longduration-NonPreRelease-Critical-42365-add real time kernel argument [Disruptive]", func() {
		architecture.SkipNonAmd64SingleArch(oc)
		skipTestIfSupportedPlatformNotMatched(oc, AWSPlatform, GCPPlatform)
		coreOSMcp := GetCoreOsCompatiblePool(oc)

		node := coreOSMcp.GetCoreOsNodesOrFail()[0]
		textToVerify := TextToVerify{
			textToVerifyForMC:   "realtime",
			textToVerifyForNode: "PREEMPT_RT",
			needBash:            true,
		}

		createMcAndVerifyMCValue(oc, "realtime kernel", "set-realtime-kernel", node, textToVerify, "uname -a")
	})

	g.It("Author:sregidor-Longduration-NonPreRelease-High-67787-switch kernel type to 64k-pages for clusters with arm64 nodes [Disruptive]", func() {
		architecture.SkipIfNoNodeWithArchitectures(oc.AsAdmin(), architecture.ARM64)
		clusterinfra.SkipTestIfNotSupportedPlatform(oc.AsAdmin(), clusterinfra.GCP)

		// If arm64 Compact/SNO we use master
		// Else if possible we create a custom MCP if there are arm64 nodes in the worker pool
		// Else if possible we use the first exsisting custom MCP with all its nodes using arm64
		// Else master is arm64 we use master
		// Else we fail the test
		createdCustomPoolName := fmt.Sprintf("mco-test-%s", architecture.ARM64)
		defer DeleteCustomMCP(oc.AsAdmin(), createdCustomPoolName)

		mcp, nodes := GetPoolAndNodesForArchitectureOrFail(oc.AsAdmin(), createdCustomPoolName, architecture.ARM64, 1)
		node := nodes[0]
		logger.Infof("Using node %s from pool %s", node.GetName(), mcp.GetName())

		textToVerify := TextToVerify{
			textToVerifyForMC:   "64k-pages",
			textToVerifyForNode: `\+64k\n65536`,
			needBash:            true,
		}
		createMcAndVerifyMCValue(oc, "64k pages kernel", "set-64k-pages-kernel", node, textToVerify, "uname -r; getconf PAGESIZE")
	})

	g.It("Author:mhanss-LEVEL0-Longduration-NonPreRelease-Critical-42364-add selinux kernel argument [Disruptive]", func() {
		var (
			coreOSMcp    = GetCoreOsCompatiblePool(oc)
			node         = coreOSMcp.GetCoreOsNodesOrFail()[0]
			textToVerify = TextToVerify{
				textToVerifyForMC:   "enforcing=0",
				textToVerifyForNode: "enforcing=0",
			}
		)

		createMcAndVerifyMCValue(oc, "Kernel argument", "change-worker-kernel-selinux", node, textToVerify, "cat", "/rootfs/proc/cmdline")
	})

	g.It("Author:sregidor-Longduration-NonPreRelease-High-67825-Use duplicated kernel arguments[Disruptive]", func() {
		workerNode := skipTestIfOsIsNotCoreOs(oc)
		textToVerify := TextToVerify{
			textToVerifyForMC:   "(?s)y=0.*z=4.*y=1.*z=4",
			textToVerifyForNode: "y=0 z=4 y=1 z=4",
		}
		createMcAndVerifyMCValue(oc, "Duplicated kernel argument", "change-worker-duplicated-kernel-argument", workerNode, textToVerify, "cat", "/rootfs/proc/cmdline")
	})

	g.It("Author:mhanss-Longduration-NonPreRelease-Critical-42367-add extension to RHCOS [Disruptive]", func() {
		var (
			coreOSMcp          = GetCoreOsCompatiblePool(oc.AsAdmin())
			mcName             = "change-worker-extension-usbguard"
			mcTemplate         = "change-worker-extension-usbguard.yaml"
			skipDrainChecks    = IsSNO(oc.AsAdmin()) // SNO clusters should NOT drain the nodes before rebooting them. The validator is not prepared for that.
			behaviourValidator = UpdateBehaviourValidator{
				SkipDrainNodesValidation: skipDrainChecks,
				PreRebootMCDLogsCheckers: []PreRebootMCDLogChecker{
					{
						Desc:     "Check that extensions are extracted using podman",
						ErrorMsg: "Extensions are not extracted using podman",
						Matcher:  o.MatchRegexp(regexp.QuoteMeta(`Running: nice -- ionice -c 3 podman cp `) + `.*/run/mco-extensions/os-extensions-content`),
					},
				},
				Checkers: []Checker{
					CommandOutputChecker{
						Desc:     "Check that usbguard rpm is installed",
						ErrorMsg: "usbguard has not been installed",
						Command:  []string{"rpm", "-q", "usbguard"},
						Matcher:  o.ContainSubstring("usbguard"),
					},
				},
			}
		)

		coreOSMcp.SetWaitingTimeForExtensionsChange()
		behaviourValidator.Initialize(coreOSMcp, coreOSMcp.GetCoreOsNodesOrFail())

		exutil.By("Create a MC to deploy a usbguard extension")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, coreOSMcp.GetName()).SetMCOTemplate(mcTemplate)
		mc.skipWaitForMcp = true
		defer mc.delete()
		mc.create()
		logger.Infof("OK!\n")

		behaviourValidator.Validate()
	})

	/* Map of extensions and packages for each extension
	{
		"wasm":                 {"crun-wasm"},
		"ipsec":                {"NetworkManager-libreswan", "libreswan"},
		"usbguard":             {"usbguard"},
		"kerberos":             {"krb5-workstation", "libkadm5"},
		"kernel-devel":         {"kernel-devel", "kernel-headers"},
		"sandboxed-containers": {"kata-containers"},
		"sysstat":              {"sysstat"},
	} */
	g.It("Author:sregidor-LEVEL0-Longduration-NonPreRelease-Critical-56131-Install all extensions [Disruptive]", func() {
		architecture.SkipNonAmd64SingleArch(oc)
		var (
			coreOSMcp                    = GetCoreOsCompatiblePool(oc.AsAdmin())
			node                         = coreOSMcp.GetCoreOsNodesOrFail()[0]
			stepText                     = "available extensions"
			mcName                       = "change-worker-all-extensions"
			allExtensions                = []string{"usbguard", "kerberos", "kernel-devel", "sandboxed-containers", "ipsec", "wasm", "sysstat"}
			expectedRpmInstalledPackages = []string{"usbguard", "krb5-workstation", "libkadm5", "kernel-devel", "kernel-headers",
				"kata-containers", "NetworkManager-libreswan", "libreswan", "crun-wasm", "sysstat"}
			textToVerify = TextToVerify{
				textToVerifyForMC:   "(?s)" + strings.Join(allExtensions, ".*"),
				textToVerifyForNode: "(?s)" + strings.Join(expectedRpmInstalledPackages, ".*"),
				needChroot:          true,
			}

			cmd = append([]string{"rpm", "-q"}, expectedRpmInstalledPackages...)
		)
		createMcAndVerifyMCValue(oc, stepText, mcName, node, textToVerify, cmd...)

		exutil.By("Verify that extension packages where uninstalled after MC deletion")
		for _, pkg := range expectedRpmInstalledPackages {
			o.Expect(node.RpmIsInstalled(pkg)).To(
				o.BeFalse(),
				"Package %s should be uninstalled when we remove the extensions MC", pkg)
		}
	})

	g.It("Author:mhanss-Longduration-NonPreRelease-Critical-43310-add kernel arguments, kernel type and extension to the RHCOS and RHEL [Disruptive]", func() {
		if IsCompactOrSNOCluster(oc.AsAdmin()) {
			g.Skip("The cluster is SNO/Compact. This test cannot be executed in SNO/Compact clusters")
		}
		// skip if not amd64. realtime kernel is not supported.
		architecture.SkipNonAmd64SingleArch(oc)

		nodeList := NewNodeList(oc)
		allRhelOs := nodeList.GetAllRhelWokerNodesOrFail()
		allCoreOs := nodeList.GetAllCoreOsWokerNodesOrFail()

		if len(allRhelOs) == 0 || len(allCoreOs) == 0 {
			g.Skip("Both Rhel and CoreOs are required to execute this test case!")
		}

		rhelOs := allRhelOs[0]
		coreOs := allCoreOs[0]

		exutil.By("Create new MC to add the kernel arguments, kernel type and extension")
		mcName := "change-worker-karg-ktype-extension"
		mcTemplate := mcName + ".yaml"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker).SetMCOTemplate(mcTemplate)
		// TODO: When we extract the "mcp.waitForComplete" from the "create" and "delete" methods, we need to take into account that if
		// we are configuring a rt-kernel we need to wait longer.
		defer mc.delete()
		mc.create()

		exutil.By("Check kernel arguments, kernel type and extension on the created machine config")
		mcOut, err := getMachineConfigDetails(oc, mc.name)
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(mcOut).Should(
			o.And(
				o.ContainSubstring("usbguard"),
				o.ContainSubstring("z=10"),
				o.ContainSubstring("realtime")))

		exutil.By("Check kernel arguments, kernel type and extension on the rhel worker node")
		o.Expect(rhelOs.RpmIsInstalled(
			"kernel-tools",
			"kernel-tools-libs",
			"kernel-core",
			"kernel-modules",
			"kernel")).Should(o.BeTrue(), "cannot find package kernel-tools|kernel-tools-libs|kernel-core|kernel-modules|kernel")
		o.Expect(rhelOs.IsKernelArgEnabled("PREEMPT_RT")).Should(o.BeFalse(), "kernel arg PREEMPT_RT found in output of uname -a, it is not expected")
		o.Expect(rhelOs.IsKernelArgEnabled("z=10")).Should(o.BeFalse(), "kernel arg z=10 found in /proc/cmdline, it is not expected")

		exutil.By("Check kernel arguments, kernel type and extension on the rhcos worker node")
		o.Expect(coreOs.RpmIsInstalled(
			"kernel-rt-kvm",
			"kernel-rt-core",
			"kernel-rt-modules-core",
			"kernel-rt-modules-extra",
			"kernel-rt-modules")).Should(o.BeTrue(), "cannot find package kernel-rt-kvm|kernel-rt-core|kernel-rt-modules.*")
		o.Expect(coreOs.IsKernelArgEnabled("PREEMPT_RT")).Should(o.BeTrue(), "kernel arg PREEMPT_RT not found in output of uname -a")
		o.Expect(coreOs.IsKernelArgEnabled("z=10")).Should(o.BeTrue(), "kernel arg z=10 not found in /proc/cmdline")

		logger.Infof("Kernel argument, kernel type and extension changes are verified on both rhcos and rhel worker nodes!")
	})

	g.It("Author:mhanss-Longduration-NonPreRelease-Critical-42368-add max pods to the kubelet config [Disruptive]", func() {
		exutil.By("create kubelet config to add 500 max pods")
		var (
			mcp        = GetCompactCompatiblePool(oc.AsAdmin())
			node       = mcp.GetSortedNodesOrFail()[0]
			kcName     = "change-maxpods-kubelet-config"
			kcTemplate = generateTemplateAbsolutePath(kcName + ".yaml")
			kc         = NewKubeletConfig(oc.AsAdmin(), kcName, kcTemplate)
		)

		defer func() {
			kc.DeleteOrFail()
			mcp.waitForComplete()
		}()
		kc.create("POOL=" + mcp.GetName())
		kc.waitUntilSuccess("10s")
		mcp.waitForComplete()
		logger.Infof("Kubelet config is created successfully!")

		exutil.By("Check max pods in the created kubelet config")
		kcOut := kc.GetOrFail(`{.spec}`)
		o.Expect(kcOut).Should(o.ContainSubstring(`"maxPods":500`))
		logger.Infof("Max pods are verified in the created kubelet config!")

		exutil.By("Check kubelet config in the worker node")
		maxPods, err := node.DebugNodeWithChroot("cat", "/etc/kubernetes/kubelet.conf")
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(maxPods).Should(o.Or(o.ContainSubstring("\"maxPods\": 500"), o.ContainSubstring("maxPods: 500")))
		logger.Infof("Max pods are verified in the worker node!")
	})

	g.It("Author:mhanss-Longduration-NonPreRelease-Critical-42369-add container runtime config [Disruptive]", func() {

		var (
			mcp    = GetCompactCompatiblePool(oc.AsAdmin())
			node   = mcp.GetSortedNodesOrFail()[0]
			crName = "change-ctr-cr-config"
		)

		if len(mcp.GetRhelNodesOrFail()) > 0 {
			// ctrcfg test cannot be executed on rhel nodes
			mcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
		}

		exutil.By("Create container runtime config")
		crTemplate := generateTemplateAbsolutePath(crName + ".yaml")
		cr := NewContainerRuntimeConfig(oc.AsAdmin(), crName, crTemplate)
		defer func() {
			cr.DeleteOrFail()
			mcp.waitForComplete()
		}()
		cr.create("POOL=" + mcp.GetName())
		mcp.waitForComplete()
		logger.Infof("Container runtime config is created successfully!")

		exutil.By("Check container runtime config values in the created config")
		o.Expect(cr.Get(`{.spec}`)).Should(
			o.And(
				o.ContainSubstring(`"logLevel":"debug"`),
				o.ContainSubstring(`"logSizeMax":"-1"`),
				o.ContainSubstring(`"pidsLimit":2048`),
				o.ContainSubstring(`"overlaySize":"8G"`)))
		logger.Infof("Container runtime config values are verified in the created config!")

		exutil.By("Check container runtime config values in the worker node")
		o.Expect(
			node.DebugNodeWithChroot("head", "-n", "7", "/etc/containers/storage.conf"),
		).Should(o.ContainSubstring("size = \"8G\""))

		o.Expect(
			node.DebugNodeWithChroot("crio", "config"),
		).Should(
			o.And(
				o.ContainSubstring("log_level = \"debug\""),
				o.ContainSubstring("pids_limit = 2048")))
		logger.Infof("Container runtime config values are verified in the worker node!")
	})

	g.It("Author:mhanss-LEVEL0-Longduration-NonPreRelease-Critical-42438-add journald systemd config [Disruptive]", func() {
		exutil.By("Create journald systemd config")
		encodedConf, err := exec.Command("bash", "-c", "cat "+generateTemplateAbsolutePath("journald.conf")+" | base64 | tr -d '\n'").Output()
		o.Expect(err).NotTo(o.HaveOccurred())
		conf := string(encodedConf)
		jcName := "change-worker-jrnl-configuration"
		jcTemplate := jcName + ".yaml"
		journaldConf := []string{"CONFIGURATION=" + conf}
		mcp := GetCompactCompatiblePool(oc.AsAdmin())
		jc := NewMachineConfig(oc.AsAdmin(), jcName, mcp.GetName()).SetMCOTemplate(jcTemplate)
		jc.parameters = journaldConf
		defer jc.delete()
		jc.create()
		logger.Infof("Journald systemd config is created successfully!")

		exutil.By("Check journald config value in the created machine config!")
		jcOut, err := getMachineConfigDetails(oc, jc.name)
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(jcOut).Should(o.ContainSubstring(conf))
		logger.Infof("Journald config is verified in the created machine config!")

		exutil.By("Check journald config values in the worker node")
		node := mcp.GetSortedNodesOrFail()[0]
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(
			node.DebugNodeWithChroot("cat", "/etc/systemd/journald.conf"),
		).Should(
			o.And(
				o.ContainSubstring("RateLimitInterval=1s"),
				o.ContainSubstring("RateLimitBurst=10000"),
				o.ContainSubstring("Storage=volatile"),
				o.ContainSubstring("Compress=no"),
				o.ContainSubstring("MaxRetentionSec=30s")))
		logger.Infof("Journald config values are verified in the worker node!")
	})

	g.It("Author:mhanss-Longduration-NonPreRelease-High-43405-High-50508-node drain is not needed for mirror config change in container registry. Nodes not tainted. [Disruptive]", func() {
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
		workerNode := NewNodeList(oc).GetAllLinuxWorkerNodesOrFail()[0]
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
			o.Expect(node.IsCordoned()).To(o.BeFalse(), "% is tainted and cordoned. There should be no cordoned worker node after applying the configuration.",
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

	g.It("Author:rioliu-NonPreRelease-Longduration-High-42390-Critical-45318-add machine config without ignition version. Block the MCO upgrade rollout if any of the pools are Degraded [Serial]", func() {
		createMcAndVerifyIgnitionVersion(oc, "empty ign version", "change-worker-ign-version-to-empty", "")
	})

	g.It("Author:mhanss-NonPreRelease-Longduration-High-43124-add machine config with invalid ignition version [Serial]", func() {
		createMcAndVerifyIgnitionVersion(oc, "invalid ign version", "change-worker-ign-version-to-invalid", "3.9.0")
	})

	g.It("Author:sregidor-NonPreRelease-Longduration-High-46304-add new ssh authorized keys RHEL. OCP<4.10 [Serial]", func() {
		skipTestIfClusterVersion(oc, ">=", "4.10")
		workerNode := skipTestIfOsIsNotRhelOs(oc)

		exutil.By("Create new machine config with new authorized key")
		mcName := "add-ssh-authorized-key-for-worker"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker).SetMCOTemplate(TmplAddSSHAuthorizedKeyForWorker)
		defer mc.delete()
		mc.create()

		exutil.By("Check content of file authorized_keys to verify whether new one is added successfully")
		sshKeyOut, err := workerNode.DebugNodeWithChroot("cat", "/home/core/.ssh/authorized_keys")
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(sshKeyOut).Should(o.ContainSubstring("mco_test@redhat.com"))
	})

	g.It("Author:sregidor-WRS-NonPreRelease-Longduration-High-46897-V-BR.26-add new ssh authorized keys RHEL. OCP>=4.10 [Serial]", func() {
		skipTestIfClusterVersion(oc, "<", "4.10")
		workerNode := skipTestIfOsIsNotRhelOs(oc)

		exutil.By("Create new machine config with new authorized key")
		mcName := "add-ssh-authorized-key-for-worker"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker).SetMCOTemplate(TmplAddSSHAuthorizedKeyForWorker)
		defer mc.delete()
		mc.create()

		exutil.By("Check that the logs are reporting correctly that the 'core' user does not exist")
		errorString := "core user does not exist, and creating users is not supported, so ignoring configuration specified for core user"
		podLogs, err := exutil.WaitAndGetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigDaemon,
			workerNode.GetMachineConfigDaemon(), "'"+errorString+"'")
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(podLogs).Should(o.ContainSubstring(errorString))

		exutil.By("Check that the authorized keys have not been created")
		rf := NewRemoteFile(workerNode, "/home/core/.ssh/authorized_keys")
		rferr := rf.Fetch().(*exutil.ExitError)
		// There should be no "/home/core" directory, so the result of trying to read the keys should be a failure
		o.Expect(rferr).To(o.HaveOccurred())
		o.Expect(rferr.StdErr).Should(o.ContainSubstring("No such file or directory"))

	})

	g.It("Author:mhanss-NonPreRelease-Longduration-Medium-43084-shutdown machine config daemon with SIGTERM [Disruptive]", func() {
		exutil.By("Create new machine config to add additional ssh key")
		mcName := "add-additional-ssh-authorized-key"
		mcTemplate := mcName + ".yaml"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker).SetMCOTemplate(mcTemplate)
		defer mc.delete()
		mc.create()

		exutil.By("Check MCD logs to make sure shutdown machine config daemon with SIGTERM")
		workerNode := NewNodeList(oc).GetAllLinuxWorkerNodesOrFail()[0]
		podLogs, err := exutil.WaitAndGetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigDaemon, workerNode.GetMachineConfigDaemon(), "SIGTERM")
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(podLogs).Should(
			o.And(
				o.ContainSubstring("Adding SIGTERM protection"),
				o.ContainSubstring("Removing SIGTERM protection")))

		exutil.By("Kill MCD process")
		mcdKillLogs, err := workerNode.DebugNodeWithChroot("pgrep", "-f", "machine-config-daemon_")
		o.Expect(err).NotTo(o.HaveOccurred())
		mcpPid := regexp.MustCompile("(?m)^[0-9]+").FindString(mcdKillLogs)
		_, err = workerNode.DebugNodeWithChroot("kill", mcpPid)
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Check MCD logs to make sure machine config daemon without SIGTERM")
		mcDaemon := workerNode.GetMachineConfigDaemon()

		exutil.AssertPodToBeReady(oc, mcDaemon, MachineConfigNamespace)
		mcdLogs, err := exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigDaemon, mcDaemon, "")
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(mcdLogs).ShouldNot(o.ContainSubstring("SIGTERM"))
	})

	g.It("Author:mhanss-Longduration-NonPreRelease-High-42682-change container registry config on ocp 4.6+ [Disruptive]", func() {

		skipTestIfClusterVersion(oc, "<", "4.6")

		registriesConfPath := "/etc/containers/registries.conf"
		newSearchRegistry := "quay.io"

		exutil.By("Generate new registries.conf information")
		workerNode := NewNodeList(oc).GetAllLinuxWorkerNodesOrFail()[0]
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
		defer mc.delete()

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

	g.It("Author:rioliu-Longduration-NonPreRelease-High-42704-disable auto reboot for mco [Disruptive]", func() {
		exutil.By("pause mcp worker")
		mcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		defer mcp.pause(false)
		mcp.pause(true)

		exutil.By("create new mc")
		mcName := "ztc-42704-change-workers-chrony-configuration"
		mcTemplate := "change-workers-chrony-configuration.yaml"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker).SetMCOTemplate(mcTemplate)
		mc.skipWaitForMcp = true
		defer mc.delete()
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

	g.It("Author:sregidor-NonPreRelease-Longduration-Critical-67395-rotate kubernetes certificate authority. Certificates managed via non-MC path.[Disruptive]", func() {

		var (
			wMcp       = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			mMcp       = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
			mcList     = NewMachineConfigList(oc.AsAdmin())
			certSecret = NewSecret(oc.AsAdmin(), "openshift-kube-apiserver-operator", "kube-apiserver-to-kubelet-signer")
		)

		logger.Infof("Get currently rendered MCs")
		initialMCs, err := mcList.GetAll()
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the list of rendered MCs")
		logger.Infof("%d rendered MCs", len(initialMCs))
		logger.Infof("OK!\n")

		exutil.By("Get start time and start collecting events.")
		// be aware that windows nodes do not belong to any pool, we are skipping them
		checkedNodes := append(mMcp.GetNodesOrFail(), wMcp.GetNodesOrFail()...)

		startTime, dErr := checkedNodes[0].GetDate()
		o.Expect(dErr).ShouldNot(o.HaveOccurred(), "Error getting date in node %s", checkedNodes[0].GetName())

		for i := range checkedNodes {
			o.Expect(checkedNodes[i].IgnoreEventsBeforeNow()).NotTo(o.HaveOccurred(),
				"Error getting the latest event in node %s", checkedNodes[i].GetName())
		}
		logger.Infof("OK!\n")

		exutil.By("Rotate certificate")
		newCert := rotateTLSSecretOrFail(certSecret)
		logger.Infof("OK!\n")

		exutil.By("Check that no new MC is created")
		o.Consistently(mcList.GetAll, "3m", "1m").Should(o.HaveLen(len(initialMCs)),
			"New machine configs have been created, but they should not be created")
		logger.Infof("OK!\n")

		for _, node := range checkedNodes {
			exutil.By(fmt.Sprintf("Checking that the certificate is rotated in node: %s", node.GetName()))

			rfCert := NewRemoteFile(node, "/etc/kubernetes/kubelet-ca.crt")

			// Eventually the certificate file in all nodes should contain the new rotated certificate
			o.Eventually(func(gm o.Gomega) string { // Passing o.Gomega as parameter we can use assertions inside the Eventually function without breaking the retries.
				gm.Expect(rfCert.Fetch()).To(o.Succeed(),
					"Cannot read the certificate file in node:%s ", node.GetName())
				return rfCert.GetTextContent()
			}, "5m", "10s").
				Should(o.ContainSubstring(newCert),
					"The certificate file %s in node %s does not contain the new rotated certificate.", rfCert.GetFullPath(), node.GetName())
			logger.Infof("OK!\n")

			exutil.By(fmt.Sprintf("Checking that node: %s was not rebooted", node.GetName()))
			o.Expect(node.GetUptime()).Should(o.BeTemporally("<", startTime),
				"The node %s must NOT be rebooted after rotating the certificate, but it was rebooted. Uptime date happened after the start config time.", node.GetName())

			logger.Infof("OK!\n")

			exutil.By(fmt.Sprintf("Checking events in node: %s", node.GetName()))
			o.Expect(node.GetEvents()).NotTo(HaveEventsSequence("Drain"),
				"Error, a Drain event was triggered but it shouldn't")
			o.Expect(node.GetEvents()).NotTo(HaveEventsSequence("Reboot"),
				"Error, a Reboot event was triggered but it shouldn't")

			logger.Infof("OK!\n")

		}
	})

	g.It("Author:rioliu-NonPreRelease-Longduration-High-43085-check mcd crash-loop-back-off error in log [Serial]", func() {
		exutil.By("get master and worker nodes")
		workerNode := NewNodeList(oc).GetAllLinuxWorkerNodesOrFail()[0]
		masterNode := NewNodeList(oc).GetAllMasterNodesOrFail()[0]
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

	g.It("Author:mhanss-Longduration-NonPreRelease-Medium-43245-bump initial drain sleeps down to 1min [Disruptive]", func() {
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
		schedulableNodes := FilterSchedulableNodesOrFail(NewNodeList(oc).GetAllLinuxWorkerNodesOrFail())
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
		defer mc.delete()
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
			logs, _ := mcc.GetFilteredLogsAsList(workerNode.GetName() + ".*Drain failed")
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
			logs, _ := mcc.GetFilteredLogsAsList(workerNode.GetName() + ".*Drain has been failing for more than 10 minutes. Waiting 5 minutes")
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

	g.It("Author:rioliu-NonPreRelease-Longduration-High-43278-security fix for unsafe cipher [Serial]", func() {
		exutil.By("check go version >= 1.15")
		_, clusterVersion, cvErr := exutil.GetClusterVersion(oc)
		o.Expect(cvErr).NotTo(o.HaveOccurred())
		o.Expect(clusterVersion).NotTo(o.BeEmpty())
		logger.Infof("cluster version is %s", clusterVersion)
		commitID, commitErr := getCommitID(oc, "machine-config", clusterVersion)
		o.Expect(commitErr).NotTo(o.HaveOccurred())
		// there is a case that in the payload no commit id from mco
		if commitID == "" {
			g.Skip("No code change from MCO, skip this case")
		}
		logger.Infof("machine config commit id is %s", commitID)
		goVersion, verErr := getGoVersion("machine-config-operator", commitID)
		o.Expect(verErr).NotTo(o.HaveOccurred())
		logger.Infof("go version is: %f", goVersion)
		o.Expect(goVersion).Should(o.BeNumerically(">", 1.15))

		exutil.By("verify TLS protocol version is 1.3")
		intAPIServerURI, err := GetAPIServerInternalURI(oc.AsAdmin())
		o.Expect(err).NotTo(o.HaveOccurred())
		masterNode := NewNodeList(oc).GetAllMasterNodesOrFail()[0]
		sslOutput, sslErr := masterNode.DebugNodeWithChroot("bash", "-c", "echo 'Q'|openssl s_client -connect "+intAPIServerURI+":6443")
		logger.Infof("ssl protocol version is:\n %s", sslOutput)
		o.Expect(sslErr).NotTo(o.HaveOccurred())
		o.Expect(sslOutput).Should(o.ContainSubstring("TLSv1.3"))

		exutil.By("verify whether the unsafe cipher is disabled")
		cipherOutput, cipherErr := masterNode.DebugNodeWithOptions([]string{"--image=" + TestSSLImage, "-n", MachineConfigNamespace}, "testssl.sh", "--quiet", "--sweet32", "localhost:6443")
		logger.Infof("test ssh script output:\n %s", cipherOutput)
		o.Expect(cipherErr).NotTo(o.HaveOccurred())
		o.Expect(cipherOutput).Should(o.ContainSubstring("not vulnerable (OK)"))
	})

	g.It("Author:sregidor-NonPreRelease-Longduration-High-43151-add node label to service monitor [Serial]", func() {
		exutil.By("Get current mcd_ metrics from machine-config-daemon service")

		svcMCD := NewNamespacedResource(oc.AsAdmin(), "service", MachineConfigNamespace, MachineConfigDaemon)
		clusterIP, ipErr := WrapWithBracketsIfIpv6(svcMCD.GetOrFail("{.spec.clusterIP}"))
		o.Expect(ipErr).ShouldNot(o.HaveOccurred(), "No valid IP")
		port := svcMCD.GetOrFail("{.spec.ports[?(@.name==\"metrics\")].port}")

		token := getSATokenFromContainer(oc, "prometheus-k8s-0", "openshift-monitoring", "prometheus")

		statsCmd := fmt.Sprintf("curl -s -k  -H 'Authorization: Bearer %s' https://%s:%s/metrics | grep 'mcd_' | grep -v '#'", token, clusterIP, port)
		logger.Infof("stats output:\n %s", statsCmd)
		statsOut, err := exutil.RemoteShPod(oc, "openshift-monitoring", "prometheus-k8s-0", "sh", "-c", statsCmd)
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(statsOut).Should(o.ContainSubstring("mcd_host_os_and_version"))
		o.Expect(statsOut).Should(o.ContainSubstring("mcd_kubelet_state"))
		o.Expect(statsOut).Should(o.ContainSubstring("mcd_pivot_errors_total"))
		o.Expect(statsOut).Should(o.ContainSubstring("mcd_reboots_failed_total"))
		o.Expect(statsOut).Should(o.ContainSubstring("mcd_state"))
		o.Expect(statsOut).Should(o.ContainSubstring("mcd_update_state"))
		o.Expect(statsOut).Should(o.ContainSubstring("mcd_update_state"))

		exutil.By("Check relabeling section in machine-config-daemon")
		sourceLabels, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("servicemonitor/machine-config-daemon", "-n", MachineConfigNamespace,
			"-o", "jsonpath='{.spec.endpoints[*].relabelings[*].sourceLabels}'").Output()
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(sourceLabels).Should(o.ContainSubstring("__meta_kubernetes_pod_node_name"))

		exutil.By("Check node label in mcd_state metrics")
		stateQuery := getPrometheusQueryResults(oc, "mcd_state")
		logger.Infof("metrics:\n %s", stateQuery)
		firstMasterNode := NewNodeList(oc).GetAllMasterNodesOrFail()[0]
		firstWorkerNode := NewNodeList(oc).GetAllLinuxWorkerNodesOrFail()[0]
		o.Expect(stateQuery).Should(o.ContainSubstring(`"node":"` + firstMasterNode.name + `"`))
		o.Expect(stateQuery).Should(o.ContainSubstring(`"node":"` + firstWorkerNode.name + `"`))
	})

	g.It("Author:sregidor-NonPreRelease-Longduration-High-43726-Azure ControllerConfig Infrastructure does not match cluster Infrastructure resource [Serial]", func() {
		exutil.By("Get machine-config-controller platform status.")
		mccPlatformStatus := NewResource(oc.AsAdmin(), "controllerconfig", "machine-config-controller").GetOrFail("{.spec.infra.status.platformStatus}")
		logger.Infof("test mccPlatformStatus:\n %s", mccPlatformStatus)

		if exutil.CheckPlatform(oc) == AzurePlatform {
			exutil.By("check cloudName field.")

			var jsonMccPlatformStatus map[string]interface{}
			errparseinfra := json.Unmarshal([]byte(mccPlatformStatus), &jsonMccPlatformStatus)
			o.Expect(errparseinfra).NotTo(o.HaveOccurred())
			o.Expect(jsonMccPlatformStatus).Should(o.HaveKey("azure"))

			azure := jsonMccPlatformStatus["azure"].(map[string]interface{})
			o.Expect(azure).Should(o.HaveKey("cloudName"))
		}

		exutil.By("Get infrastructure platform status.")
		infraPlatformStatus := NewResource(oc.AsAdmin(), "infrastructures", "cluster").GetOrFail("{.status.platformStatus}")
		logger.Infof("infraPlatformStatus:\n %s", infraPlatformStatus)

		exutil.By("Check same status in infra and machine-config-controller.")
		o.Expect(mccPlatformStatus).To(o.Equal(infraPlatformStatus))
	})

	g.It("Author:mhanss-NonPreRelease-Longduration-High-42680-change pull secret in the openshift-config namespace [Serial]", func() {
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
		masterNode := NewNodeList(oc).GetAllMasterNodesOrFail()[0]
		workerNode := NewNodeList(oc).GetAllLinuxWorkerNodesOrFail()[0]
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

	g.It("Author:sregidor-NonPreRelease-Longduration-High-45239-KubeletConfig has a limit of 10 per cluster [Disruptive]", func() {
		kcsLimit := 10

		exutil.By("Pause mcp worker")
		mcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		defer mcp.pause(false)
		defer mcp.waitForComplete() // wait before unpausing, or an update will be triggered

		mcp.pause(true)

		exutil.By("Calculate number of existing KubeletConfigs")
		kcList := NewKubeletConfigList(oc.AsAdmin())
		kcs, kclErr := kcList.GetAll()
		o.Expect(kclErr).ShouldNot(o.HaveOccurred(), "Error getting existing KubeletConfig resources")
		existingKcs := len(kcs)
		logger.Infof("%d existing KubeletConfigs. We need to create %d KubeletConfigs to reach the %d configs limit",
			existingKcs, kcsLimit-existingKcs, kcsLimit)

		exutil.By(fmt.Sprintf("Create %d kubelet config to reach the limit", kcsLimit-existingKcs))
		createdKcs := []ResourceInterface{}
		kcTemplate := generateTemplateAbsolutePath("change-maxpods-kubelet-config.yaml")
		for n := existingKcs + 1; n <= kcsLimit; n++ {
			kcName := fmt.Sprintf("change-maxpods-kubelet-config-%d", n)
			kc := NewKubeletConfig(oc.AsAdmin(), kcName, kcTemplate)
			defer kc.DeleteOrFail()
			kc.create()
			createdKcs = append(createdKcs, kc)
			logger.Infof("Created:\n %s", kcName)
		}

		exutil.By("Created kubeletconfigs must be successful")
		for _, kcItem := range createdKcs {
			kcItem.(*KubeletConfig).waitUntilSuccess("15s")
		}

		exutil.By(fmt.Sprintf("Check that %d machine configs were created", kcsLimit-existingKcs))
		renderedKcConfigsSuffix := "worker-generated-kubelet"
		verifyRenderedMcs(oc, renderedKcConfigsSuffix, createdKcs)

		exutil.By(fmt.Sprintf("Create a new Kubeletconfig. The %dth one", kcsLimit+1))
		kcName := fmt.Sprintf("change-maxpods-kubelet-config-%d", kcsLimit+1)
		kc := NewKubeletConfig(oc.AsAdmin(), kcName, kcTemplate)
		defer kc.DeleteOrFail()
		kc.create()

		exutil.By(fmt.Sprintf("Created kubeletconfigs over the limit must report a failure regarding the %d configs limit", kcsLimit))
		expectedMsg := fmt.Sprintf("could not get kubelet config key: max number of supported kubelet config (%d) has been reached. Please delete old kubelet configs before retrying", kcsLimit)
		kc.waitUntilFailure(expectedMsg, "10s")

		exutil.By("Created kubeletconfigs inside the limit must be successful")
		for _, kcItem := range createdKcs {
			kcItem.(*KubeletConfig).waitUntilSuccess("10s")
		}

		exutil.By("Check that only the right machine configs were created")
		// Check all ContainerRuntimeConfigs, the one created by this TC and the already existing ones too
		allKcs := []ResourceInterface{}
		allKcs = append(allKcs, createdKcs...)
		for _, kcItem := range kcs {
			key := kcItem
			allKcs = append(allKcs, &key)
		}
		allMcs := verifyRenderedMcs(oc, renderedKcConfigsSuffix, allKcs)

		kcCounter := 0
		for _, mc := range allMcs {
			if strings.HasPrefix(mc.name, "99-"+renderedKcConfigsSuffix) {
				kcCounter++
			}
		}
		o.Expect(kcCounter).Should(o.Equal(10), "Only %d Kubeletconfig resources should be generated", kcsLimit)

	})

	g.It("Author:sregidor-NonPreRelease-Longduration-High-48468-ContainerRuntimeConfig has a limit of 10 per cluster [Disruptive]", func() {
		crsLimit := 10

		exutil.By("Pause mcp worker")
		mcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		defer mcp.pause(false)
		defer mcp.waitForComplete() // wait before unpausing, or an update will be triggered

		mcp.pause(true)

		exutil.By("Calculate number of existing ContainerRuntimeConfigs")
		crList := NewContainerRuntimeConfigList(oc.AsAdmin())
		crs, crlErr := crList.GetAll()
		o.Expect(crlErr).ShouldNot(o.HaveOccurred(), "Error getting existing ContainerRuntimeConfig resources")
		existingCrs := len(crs)
		logger.Infof("%d existing ContainerRuntimeConfig. We need to create %d ContainerRuntimeConfigs to reach the %d configs limit",
			existingCrs, crsLimit-existingCrs, crsLimit)

		exutil.By(fmt.Sprintf("Create %d container runtime configs to reach the limit", crsLimit-existingCrs))
		createdCrs := []ResourceInterface{}
		crTemplate := generateTemplateAbsolutePath("change-ctr-cr-config.yaml")
		for n := existingCrs + 1; n <= crsLimit; n++ {
			crName := fmt.Sprintf("change-ctr-cr-config-%d", n)
			cr := NewContainerRuntimeConfig(oc.AsAdmin(), crName, crTemplate)
			defer cr.DeleteOrFail()
			cr.create()
			createdCrs = append(createdCrs, cr)
			logger.Infof("Created:\n %s", crName)
		}

		exutil.By("Created ContainerRuntimeConfigs must be successful")
		for _, crItem := range createdCrs {
			crItem.(*ContainerRuntimeConfig).waitUntilSuccess("10s")
		}

		exutil.By(fmt.Sprintf("Check that %d machine configs were created", crsLimit-existingCrs))
		renderedCrConfigsSuffix := "worker-generated-containerruntime"

		logger.Infof("Pre function res: %v", createdCrs)
		verifyRenderedMcs(oc, renderedCrConfigsSuffix, createdCrs)

		exutil.By(fmt.Sprintf("Create a new ContainerRuntimeConfig. The %dth one", crsLimit+1))
		crName := fmt.Sprintf("change-ctr-cr-config-%d", crsLimit+1)
		cr := NewContainerRuntimeConfig(oc.AsAdmin(), crName, crTemplate)
		defer cr.DeleteOrFail()
		cr.create()

		exutil.By(fmt.Sprintf("Created container runtime configs over the limit must report a failure regarding the %d configs limit", crsLimit))
		expectedMsg := fmt.Sprintf("could not get ctrcfg key: max number of supported ctrcfgs (%d) has been reached. Please delete old ctrcfgs before retrying", crsLimit)
		cr.waitUntilFailure(expectedMsg, "10s")

		exutil.By("Created kubeletconfigs inside the limit must be successful")
		for _, crItem := range createdCrs {
			crItem.(*ContainerRuntimeConfig).waitUntilSuccess("10s")
		}

		exutil.By("Check that only the right machine configs were created")
		// Check all ContainerRuntimeConfigs, the one created by this TC and the already existing ones too
		allCrs := []ResourceInterface{}
		allCrs = append(allCrs, createdCrs...)
		for _, crItem := range crs {
			key := crItem
			allCrs = append(allCrs, &key)
		}

		allMcs := verifyRenderedMcs(oc, renderedCrConfigsSuffix, allCrs)

		crCounter := 0
		for _, mc := range allMcs {
			if strings.HasPrefix(mc.name, "99-"+renderedCrConfigsSuffix) {
				crCounter++
			}
		}
		o.Expect(crCounter).Should(o.Equal(10), "Only %d containerruntime resources should be generated", crsLimit)

	})

	g.It("Author:sregidor-Longduration-NonPreRelease-High-46314-Incorrect file contents if compression field is specified [Serial]", func() {
		exutil.By("Create a new MachineConfig to provision a config file in zipped format")

		fileContent := `Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do
eiusmod tempor incididunt ut labore et dolore magna aliqua.  Ut
enim ad minim veniam, quis nostrud exercitation ullamco laboris
nisi ut aliquip ex ea commodo consequat.  Duis aute irure dolor in
reprehenderit in voluptate velit esse cillum dolore eu fugiat
nulla pariatur.  Excepteur sint occaecat cupidatat non proident,
sunt in culpa qui officia deserunt mollit anim id est laborum.


nulla pariatur.`

		mcName := "99-gzip-test"
		destPath := "/etc/test-file"
		fileConfig := getGzipFileJSONConfig(destPath, fileContent)

		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		defer mc.delete()

		err := mc.Create("-p", "NAME="+mcName, "-p", "POOL=worker", "-p", fmt.Sprintf("FILES=[%s]", fileConfig))
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Wait until worker MachineConfigPool has finished the configuration")
		mcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		mcp.waitForComplete()

		exutil.By("Verfiy that the file has been properly provisioned")
		node := NewNodeList(oc).GetAllLinuxWorkerNodesOrFail()[0]
		rf := NewRemoteFile(node, destPath)
		err = rf.Fetch()
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(rf.GetTextContent()).To(o.Equal(fileContent))
		o.Expect(rf.GetNpermissions()).To(o.Equal("0644"))
		o.Expect(rf.GetUIDName()).To(o.Equal("root"))
		o.Expect(rf.GetGIDName()).To(o.Equal("root"))
	})

	g.It("NonHyperShiftHOST-Author:sregidor-High-46424-Check run level", func() {
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
	g.It("Author:sregidor-Longduration-NonPreRelease-High-46434-Mask service [Serial]", func() {
		activeString := "Active: active (running)"
		inactiveString := "Active: inactive (dead)"
		maskedString := "Loaded: masked"

		exutil.By("Validate that the chronyd service is active")
		workerNode := NewNodeList(oc).GetAllLinuxWorkerNodesOrFail()[0]
		svcOuput, err := workerNode.DebugNodeWithChroot("systemctl", "status", "chronyd")
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(svcOuput).Should(o.ContainSubstring(activeString))
		o.Expect(svcOuput).ShouldNot(o.ContainSubstring(inactiveString))

		exutil.By("Create a MachineConfig resource to mask the chronyd service")
		mcName := "99-test-mask-services"
		maskSvcConfig := getMaskServiceConfig("chronyd.service", true)
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		defer mc.delete()

		err = mc.Create("-p", "NAME="+mcName, "-p", "POOL=worker", "-p", fmt.Sprintf("UNITS=[%s]", maskSvcConfig))
		o.Expect(err).NotTo(o.HaveOccurred())
		// if service is masked, but node drain is failed, unmask chronyd service on all worker nodes in this defer block
		// then clean up logic will delete this mc, node will be rebooted, when the system is back online, chronyd service
		// can be started automatically, unmask command can be executed w/o error with loaded & active service
		defer func() {
			workersNodes := NewNodeList(oc).GetAllLinuxWorkerNodesOrFail()
			for _, worker := range workersNodes {
				svcName := "chronyd"
				_, err := worker.UnmaskService(svcName)
				// just print out unmask op result here, make sure unmask op can be executed on all the worker nodes
				if err != nil {
					logger.Errorf("unmask %s failed on node %s: %v", svcName, worker.name, err)
				} else {
					logger.Infof("unmask %s success on node %s", svcName, worker.name)
				}
			}
		}()

		exutil.By("Wait until worker MachineConfigPool has finished the configuration")
		mcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		mcp.waitForComplete()

		exutil.By("Validate that the chronyd service is masked")
		svcMaskedOuput, _ := workerNode.DebugNodeWithChroot("systemctl", "status", "chronyd")
		// Since the service is masked, the "systemctl status chronyd" command will return a value != 0 and an error will be reported
		// So we dont check the error, only the output
		o.Expect(svcMaskedOuput).ShouldNot(o.ContainSubstring(activeString))
		o.Expect(svcMaskedOuput).Should(o.ContainSubstring(inactiveString))
		o.Expect(svcMaskedOuput).Should(o.ContainSubstring(maskedString))

		exutil.By("Patch the MachineConfig resource to unmaskd the svc")
		// This part needs to be changed once we refactor MachineConfig to embed the Resource struct.
		// We will use here the 'mc' object directly
		mcresource := NewResource(oc.AsAdmin(), "mc", mc.name)
		err = mcresource.Patch("json", `[{ "op": "replace", "path": "/spec/config/systemd/units/0/mask", "value": false}]`)
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Wait until worker MachineConfigPool has finished the configuration")
		mcp.waitForComplete()

		exutil.By("Validate that the chronyd service is unmasked")
		svcUnMaskedOuput, err := workerNode.DebugNodeWithChroot("systemctl", "status", "chronyd")
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(svcUnMaskedOuput).Should(o.ContainSubstring(activeString))
		o.Expect(svcUnMaskedOuput).ShouldNot(o.ContainSubstring(inactiveString))
	})

	g.It("Author:sregidor-Longduration-NonPreRelease-High-46943-Config Drift. Config file. [Serial]", func() {
		exutil.By("Create a MC to deploy a config file")
		filePath := "/etc/mco-test-file"
		fileContent := "MCO test file\n"
		fileConfig := getURLEncodedFileConfig(filePath, fileContent, "")

		mcName := "mco-drift-test-file"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		defer mc.delete()

		err := mc.Create("-p", "NAME="+mcName, "-p", "POOL=worker", "-p", fmt.Sprintf("FILES=[%s]", fileConfig))
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Wait until worker MCP has finished the configuration. No machine should be degraded.")
		mcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		mcp.waitForComplete()

		exutil.By("Verfiy file content and permissions")
		workerNode := NewNodeList(oc).GetAllLinuxWorkerNodesOrFail()[0]

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

	g.It("Author:rioliu-NonPreRelease-Longduration-High-46965-Avoid workload disruption for GPG Public Key Rotation [Serial]", func() {

		exutil.By("create new machine config with base64 encoded gpg public key")
		workerNode := NewNodeList(oc).GetAllLinuxWorkerNodesOrFail()[0]
		startTime := workerNode.GetDateOrFail()
		mcName := "add-gpg-pub-key"
		mcTemplate := "add-gpg-pub-key.yaml"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker).SetMCOTemplate(mcTemplate)
		defer mc.delete()
		mc.create()

		exutil.By("checkout machine config daemon logs to verify ")
		log, err := exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigDaemon, workerNode.GetMachineConfigDaemon(), "")
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(log).Should(o.ContainSubstring("/etc/machine-config-daemon/no-reboot/containers-gpg.pub"))
		o.Expect(log).Should(o.ContainSubstring("Changes do not require drain, skipping"))
		o.Expect(log).Should(o.MatchRegexp(MCDCrioReloadedRegexp))

		o.Expect(workerNode.GetUptime()).Should(o.BeTemporally("<", startTime),
			"The node %s must NOT be rebooted, but it was rebooted. Uptime date happened after the start config time.", workerNode.GetName())

		exutil.By("verify crio.service status")
		cmdOut, cmdErr := workerNode.DebugNodeWithChroot("systemctl", "is-active", "crio.service")
		o.Expect(cmdErr).NotTo(o.HaveOccurred())
		o.Expect(cmdOut).Should(o.ContainSubstring("active"))

	})

	g.It("Author:rioliu-NonPreRelease-Longduration-High-47062-change policy.json on worker nodes [Serial]", func() {

		exutil.By("create new machine config to change /etc/containers/policy.json")
		workerNode := NewNodeList(oc).GetAllLinuxWorkerNodesOrFail()[0]
		startTime := workerNode.GetDateOrFail()
		mcName := "change-policy-json"
		mcTemplate := "change-policy-json.yaml"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker).SetMCOTemplate(mcTemplate)
		defer mc.delete()
		mc.create()

		exutil.By("verify file content changes")
		fileContent, fileErr := workerNode.DebugNodeWithChroot("cat", "/etc/containers/policy.json")
		o.Expect(fileErr).NotTo(o.HaveOccurred())
		logger.Infof(fileContent)
		o.Expect(fileContent).Should(o.ContainSubstring(`{"default": [{"type": "insecureAcceptAnything"}]}`))
		o.Expect(fileContent).ShouldNot(o.ContainSubstring("transports"))

		exutil.By("checkout machine config daemon logs to make sure node drain/reboot are skipped")
		log, err := exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigDaemon, workerNode.GetMachineConfigDaemon(), "")
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(log).Should(o.ContainSubstring("/etc/containers/policy.json"))
		o.Expect(log).Should(o.ContainSubstring("Changes do not require drain, skipping"))
		o.Expect(log).Should(o.MatchRegexp(MCDCrioReloadedRegexp))

		o.Expect(workerNode.GetUptime()).Should(o.BeTemporally("<", startTime),
			"The node %s must NOT be rebooted, but it was rebooted. Uptime date happened after the start config time.", workerNode.GetName())

		exutil.By("verify crio.service status")
		cmdOut, cmdErr := workerNode.DebugNodeWithChroot("systemctl", "is-active", "crio.service")
		o.Expect(cmdErr).NotTo(o.HaveOccurred())
		o.Expect(cmdOut).Should(o.ContainSubstring("active"))

	})

	g.It("Author:sregidor-Longduration-NonPreRelease-High-46999-Config Drift. Config file permissions. [Serial]", func() {
		exutil.By("Create a MC to deploy a config file")
		filePath := "/etc/mco-test-file"
		fileContent := "MCO test file\n"
		fileMode := "0400" // decimal 256
		fileConfig := getURLEncodedFileConfig(filePath, fileContent, fileMode)

		mcName := "mco-drift-test-file-permissions"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		defer mc.delete()

		err := mc.Create("-p", "NAME="+mcName, "-p", "POOL=worker", "-p", fmt.Sprintf("FILES=[%s]", fileConfig))
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Wait until worker MCP has finished the configuration. No machine should be degraded.")
		mcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		mcp.waitForComplete()

		exutil.By("Verfiy file content and permissions")
		workerNode := NewNodeList(oc).GetAllLinuxWorkerNodesOrFail()[0]

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

	g.It("Author:sregidor-Longduration-NonPreRelease-High-47045-Config Drift. Compressed files. [Serial]", func() {
		exutil.By("Create a MC to deploy a config file using compression")
		filePath := "/etc/mco-compressed-test-file"
		fileContent := "MCO test file\nusing compression"
		fileConfig := getGzipFileJSONConfig(filePath, fileContent)

		mcName := "mco-drift-test-compressed-file"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		defer mc.delete()

		err := mc.Create("-p", "NAME="+mcName, "-p", "POOL=worker", "-p", fmt.Sprintf("FILES=[%s]", fileConfig))
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Wait until worker MCP has finished the configuration. No machine should be degraded.")
		mcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		mcp.waitForComplete()

		exutil.By("Verfiy file content and permissions")
		workerNode := NewNodeList(oc).GetAllLinuxWorkerNodesOrFail()[0]

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
	g.It("Author:sregidor-Longduration-NonPreRelease-High-47008-Config Drift. Dropin file. [Serial]", func() {
		exutil.By("Create a MC to deploy a unit with a dropin file")
		dropinFileName := "10-chrony-drop-test.conf"
		filePath := "/etc/systemd/system/chronyd.service.d/" + dropinFileName
		fileContent := "[Service]\nEnvironment=\"FAKE_OPTS=fake-value\""
		unitEnabled := true
		unitName := "chronyd.service"
		unitConfig := getDropinFileConfig(unitName, unitEnabled, dropinFileName, fileContent)

		mcName := "drifted-dropins-test"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		defer mc.delete()

		err := mc.Create("-p", "NAME="+mcName, "-p", "POOL=worker", "-p", fmt.Sprintf("UNITS=[%s]", unitConfig))
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Wait until worker MCP has finished the configuration. No machine should be degraded.")
		mcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		mcp.waitForComplete()

		exutil.By("Verfiy file content and permissions")
		workerNode := NewNodeList(oc).GetAllLinuxWorkerNodesOrFail()[0]

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

	g.It("Author:sregidor-Longduration-NonPreRelease-High-47009-Config Drift. New Service Unit. [Serial]", func() {
		exutil.By("Create a MC to deploy a unit.")
		unitEnabled := true
		unitName := "example.service"
		filePath := "/etc/systemd/system/" + unitName
		fileContent := "[Service]\nType=oneshot\nExecStart=/usr/bin/echo Hello from MCO test service\n\n[Install]\nWantedBy=multi-user.target"
		unitConfig := getSingleUnitConfig(unitName, unitEnabled, fileContent)

		mcName := "drifted-new-service-test"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		defer mc.delete()

		err := mc.Create("-p", "NAME="+mcName, "-p", "POOL=worker", "-p", fmt.Sprintf("UNITS=[%s]", unitConfig))
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Wait until worker MCP has finished the configuration. No machine should be degraded.")
		mcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		mcp.waitForComplete()

		exutil.By("Verfiy file content and permissions")
		workerNode := NewNodeList(oc).GetAllLinuxWorkerNodesOrFail()[0]

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

	g.It("Author:sregidor-Longduration-NonPreRelease-High-51381-cordon node before node drain. OCP >= 4.11 [Serial]", func() {
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
		defer mc.delete()

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
			podAllLogs, _ := exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, ctrlerContainer, ctrlerPod, "")
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

	g.It("Author:sregidor-Longduration-NonPreRelease-High-49568-Check nodes updating order maxUnavailable=1 [Serial]", func() {
		// Skip if no machinesets
		skipTestIfWorkersCannotBeScaled(oc.AsAdmin())

		exutil.By("Scale machinesets and 1 more replica to make sure we have at least 2 nodes per machineset")
		platform := exutil.CheckPlatform(oc)
		logger.Infof("Platform is %s", platform)
		if platform != "none" && platform != "" {
			err := AddToAllMachineSets(oc, 1)
			o.Expect(err).NotTo(o.HaveOccurred())
			defer func() { o.Expect(AddToAllMachineSets(oc, -1)).NotTo(o.HaveOccurred()) }()
		} else {
			logger.Infof("Platform is %s, skipping the MachineSets replica configuration", platform)
		}

		exutil.By("Get the nodes in the worker pool sorted by update order")
		mcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		workerNodes, errGet := mcp.GetSortedNodes()
		o.Expect(errGet).NotTo(o.HaveOccurred())

		exutil.By("Create a MC to deploy a config file")
		filePath := "/etc/TC-49568-mco-test-file-order"
		fileContent := "MCO test file order\n"
		fileMode := "0400" // decimal 256
		fileConfig := getURLEncodedFileConfig(filePath, fileContent, fileMode)

		mcName := "mco-test-file-order"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		defer mc.delete()

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

	g.It("Author:sregidor-Longduration-NonPreRelease-High-49672-Check nodes updating order maxUnavailable>1 [Serial]", func() {
		// Skip if no machinesets
		skipTestIfWorkersCannotBeScaled(oc.AsAdmin())

		exutil.By("Scale machinesets and 1 more replica to make sure we have at least 2 nodes per machineset")
		platform := exutil.CheckPlatform(oc)
		logger.Infof("Platform is %s", platform)
		if platform != "none" && platform != "" {
			err := AddToAllMachineSets(oc, 1)
			o.Expect(err).NotTo(o.HaveOccurred())
			defer func() { o.Expect(AddToAllMachineSets(oc, -1)).NotTo(o.HaveOccurred()) }()
		} else {
			logger.Infof("Platform is %s, skipping the MachineSets replica configuration", platform)
		}

		// If the number of nodes is 2, since we are using maxUnavailable=2, all nodes will be cordoned at
		//  the same time and the eviction process will be stuck. In this case we need to skip the test case.
		numWorkers := len(NewNodeList(oc).GetAllLinuxWorkerNodesOrFail())
		if numWorkers <= 2 {
			g.Skip(fmt.Sprintf("The test case needs at least 3 worker nodes, because eviction will be stuck if not. Current num worker is %d, we skip the case",
				numWorkers))
		}

		exutil.By("Get the nodes in the worker pool sorted by update order")
		mcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
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
		defer mc.delete()

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

	g.It("Author:sregidor-NonPreRelease-Longduration-High-51219-Check ClusterRole rules", func() {
		expectedServiceAcc := MachineConfigDaemon
		eventsRoleBinding := MachineConfigDaemonEvents
		eventsClusterRole := MachineConfigDaemonEvents
		daemonClusterRoleBinding := MachineConfigDaemon
		daemonClusterRole := MachineConfigDaemon

		exutil.By(fmt.Sprintf("Check %s service account", expectedServiceAcc))
		serviceAccount := NewNamespacedResource(oc.AsAdmin(), "ServiceAccount", MachineConfigNamespace, expectedServiceAcc)
		o.Expect(serviceAccount.Exists()).To(o.BeTrue(), "Service account %s should exist in namespace %s", expectedServiceAcc, MachineConfigNamespace)

		exutil.By("Check service accounts in daemon pods")
		checkNodePermissions := func(node Node) {
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
		machineConfigSubject := JSON(defaultEventsRoleBindings.GetOrFail(fmt.Sprintf(`{.subjects[?(@.name=="%s")]}`, expectedServiceAcc)))
		o.Expect(machineConfigSubject.ToMap()).Should(o.HaveKeyWithValue("name", expectedServiceAcc),
			"'%s' in 'default' namespace should bind %s SA in namespace %s", eventsRoleBinding, expectedServiceAcc, MachineConfigNamespace)
		o.Expect(machineConfigSubject.ToMap()).Should(o.HaveKeyWithValue("namespace", MachineConfigNamespace),
			"'%s' in 'default' namespace should bind %s SA in namespace %s", eventsRoleBinding, expectedServiceAcc, MachineConfigNamespace)

		// Check the ClusterRole
		machineConfigClusterRole := JSON(defaultEventsRoleBindings.GetOrFail(`{.roleRef}`))
		o.Expect(machineConfigClusterRole.ToMap()).Should(o.HaveKeyWithValue("kind", "ClusterRole"),
			"'%s' in 'default' namespace should bind a ClusterRole", eventsRoleBinding)
		o.Expect(machineConfigClusterRole.ToMap()).Should(o.HaveKeyWithValue("name", eventsClusterRole),
			"'%s' in 'default' namespace should bind %s ClusterRole", eventsRoleBinding, eventsClusterRole)

		exutil.By(fmt.Sprintf("Check events rolebindings in %s namespace", MachineConfigNamespace))
		mcoEventsRoleBindings := NewNamespacedResource(oc.AsAdmin(), "RoleBinding", MachineConfigNamespace, "machine-config-daemon-events")
		o.Expect(defaultEventsRoleBindings.Exists()).Should(o.BeTrue(), "'%s' Rolebinding not found in '%s' namespace", eventsRoleBinding, MachineConfigNamespace)
		// Check the bound SA
		machineConfigSubject = JSON(mcoEventsRoleBindings.GetOrFail(fmt.Sprintf(`{.subjects[?(@.name=="%s")]}`, expectedServiceAcc)))
		o.Expect(machineConfigSubject.ToMap()).Should(o.HaveKeyWithValue("name", expectedServiceAcc),
			"'%s' in '%s' namespace should bind %s SA in namespace %s", eventsRoleBinding, MachineConfigNamespace, expectedServiceAcc, MachineConfigNamespace)
		o.Expect(machineConfigSubject.ToMap()).Should(o.HaveKeyWithValue("namespace", MachineConfigNamespace),
			"'%s' in '%s' namespace should bind %s SA in namespace %s", eventsRoleBinding, MachineConfigNamespace, expectedServiceAcc, MachineConfigNamespace)

		// Check the ClusterRole
		machineConfigClusterRole = JSON(mcoEventsRoleBindings.GetOrFail(`{.roleRef}`))
		o.Expect(machineConfigClusterRole.ToMap()).Should(o.HaveKeyWithValue("kind", "ClusterRole"),
			"'%s' in '%s' namespace should bind a ClusterRole", eventsRoleBinding, MachineConfigNamespace)
		o.Expect(machineConfigClusterRole.ToMap()).Should(o.HaveKeyWithValue("name", eventsClusterRole),
			"'%s' in '%s' namespace should bind %s CLusterRole", eventsRoleBinding, MachineConfigNamespace, eventsClusterRole)

		exutil.By(fmt.Sprintf("Check MCO cluseterrolebindings in %s namespace", MachineConfigNamespace))
		mcoCRB := NewResource(oc.AsAdmin(), "ClusterRoleBinding", daemonClusterRoleBinding)
		o.Expect(mcoCRB.Exists()).Should(o.BeTrue(), "'%s' ClusterRolebinding not found.", daemonClusterRoleBinding)
		// Check the bound SA
		machineConfigSubject = JSON(mcoCRB.GetOrFail(fmt.Sprintf(`{.subjects[?(@.name=="%s")]}`, expectedServiceAcc)))
		o.Expect(machineConfigSubject.ToMap()).Should(o.HaveKeyWithValue("name", expectedServiceAcc),
			"'%s' ClusterRoleBinding should bind %s SA in namespace %s", daemonClusterRoleBinding, expectedServiceAcc, MachineConfigNamespace)
		o.Expect(machineConfigSubject.ToMap()).Should(o.HaveKeyWithValue("namespace", MachineConfigNamespace),
			"'%s' ClusterRoleBinding should bind %s SA in namespace %s", daemonClusterRoleBinding, expectedServiceAcc, MachineConfigNamespace)

		// Check the ClusterRole
		machineConfigClusterRole = JSON(mcoCRB.GetOrFail(`{.roleRef}`))
		o.Expect(machineConfigClusterRole.ToMap()).Should(o.HaveKeyWithValue("kind", "ClusterRole"),
			"'%s' ClusterRoleBinding should bind a ClusterRole", daemonClusterRoleBinding)
		o.Expect(machineConfigClusterRole.ToMap()).Should(o.HaveKeyWithValue("name", daemonClusterRole),
			"'%s' ClusterRoleBinding should bind %s CLusterRole", daemonClusterRoleBinding, daemonClusterRole)

		exutil.By("Check events clusterrole")
		eventsCR := NewResource(oc.AsAdmin(), "ClusterRole", eventsClusterRole)
		o.Expect(eventsCR.Exists()).To(o.BeTrue(), "ClusterRole %s should exist", eventsClusterRole)

		stringRules := eventsCR.GetOrFail(`{.rules}`)
		o.Expect(stringRules).ShouldNot(o.ContainSubstring("pod"),
			"ClusterRole %s should grant no pod permissions at all", eventsClusterRole)

		rules := JSON(stringRules)
		for _, rule := range rules.Items() {
			describesEvents := false
			resources := rule.Get("resources")
			for _, resource := range resources.Items() {
				if resource.ToString() == "events" {
					describesEvents = true
				}
			}

			if describesEvents {
				verbs := rule.Get("verbs").ToList()
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

		rules = JSON(stringRules)
		for _, rule := range rules.Items() {
			describesNodes := false
			resources := rule.Get("resources")
			for _, resource := range resources.Items() {
				if resource.ToString() == "nodes" {
					describesNodes = true
				}
			}

			if describesNodes {
				verbs := rule.Get("verbs").ToList()
				o.Expect(verbs).Should(o.ContainElement("get"), "In ClusterRole %s 'nodes' rule should have 'get' permissions", daemonClusterRole)
				o.Expect(verbs).Should(o.ContainElement("list"), "In ClusterRole %s 'nodes' rule should have 'list' permissions", daemonClusterRole)
				o.Expect(verbs).Should(o.ContainElement("watch"), "In ClusterRole %s 'nodes' rule should have 'watch' permissions", daemonClusterRole)
				o.Expect(verbs).Should(o.HaveLen(3), "In ClusterRole %s 'events' rule should ONLY Have 'get', 'list' and 'watch' permissions", daemonClusterRole)
			}
		}

	})
	g.It("Author:sregidor-NonPreRelease-Longduration-Medium-52373-Modify proxy configuration in paused pools [Disruptive]", func() {

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

	g.It("Author:sregidor-NonPreRelease-Longduration-Medium-52520-Configure unqualified-search-registries in Image.config resource [Disruptive]", func() {
		expectedDropinFilePath := "/etc/containers/registries.conf.d/01-image-searchRegistries.conf"
		expectedDropinContent := "unqualified-search-registries = [\"quay.io\"]\nshort-name-mode = \"\"\n"

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
			"Error, the expected sequence of events is not found in node %s", firstUpdatedWorker.GetName())

		exutil.By("Verify that the node was actually rebooted")
		o.Expect(firstUpdatedWorker.GetUptime()).Should(o.BeTemporally(">", startTime),
			"The node %s should have been rebooted after the configurion. Uptime didnt happen after start config time.")
		o.Expect(firstUpdatedMaster.GetUptime()).Should(o.BeTemporally(">", startTime),
			"The node %s should have been rebooted after the configurion. Uptime didnt happen after start config time.")

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

	g.It("Author:rioliu-NonPreRelease-Longduration-High-53668-when FIPS and realtime kernel are both enabled node should NOT be degraded [Disruptive]", func() {
		// skip if arm64. realtime kernel is not supported.
		architecture.SkipNonAmd64SingleArch(oc)
		// skip the test if fips is not enabled
		skipTestIfFIPSIsNotEnabled(oc)
		// skip the test if platform is not aws or gcp. realtime kargs currently supported on these platforms
		skipTestIfSupportedPlatformNotMatched(oc, AWSPlatform, GCPPlatform)

		exutil.By("create machine config to enable fips ")
		fipsMcName := "50-fips-bz-poc"
		fipsMcTemplate := "bz2096496-dummy-mc-for-fips.yaml"
		fipsMc := NewMachineConfig(oc.AsAdmin(), fipsMcName, MachineConfigPoolMaster).SetMCOTemplate(fipsMcTemplate)

		defer fipsMc.delete()
		fipsMc.create()

		exutil.By("create machine config to enable RT kernel")
		rtMcName := "50-realtime-kernel"
		rtMcTemplate := "set-realtime-kernel.yaml"
		rtMc := NewMachineConfig(oc.AsAdmin(), rtMcName, MachineConfigPoolMaster).SetMCOTemplate(rtMcTemplate)
		// TODO: When we extract the "mcp.waitForComplete" from the "create" and "delete" methods, we need to take into account that if
		// we are configuring a rt-kernel we need to wait longer.
		defer rtMc.delete()
		rtMc.create()

		masterNode := NewNodeList(oc).GetAllMasterNodesOrFail()[0]

		exutil.By("check whether fips is enabled")
		fipsEnabled, fipsErr := masterNode.IsFIPSEnabled()
		o.Expect(fipsErr).NotTo(o.HaveOccurred())
		o.Expect(fipsEnabled).Should(o.BeTrue(), "fips is not enabled on node %s", masterNode.GetName())

		exutil.By("check whether fips related kernel arg is enabled")
		fipsKarg := "trigger-fips-issue=1"
		fipsKargEnabled, fipsKargErr := masterNode.IsKernelArgEnabled(fipsKarg)
		o.Expect(fipsKargErr).NotTo(o.HaveOccurred())
		o.Expect(fipsKargEnabled).Should(o.BeTrue(), "fips related kernel arg %s is not enabled on node %s", fipsKarg, masterNode.GetName())

		exutil.By("check whether RT kernel is enabled")
		rtEnabled, rtErr := masterNode.IsKernelArgEnabled("PREEMPT_RT")
		o.Expect(rtErr).NotTo(o.HaveOccurred())
		o.Expect(rtEnabled).Should(o.BeTrue(), "RT kernel is not enabled on node %s", masterNode.GetName())
	})

	g.It("Author:sregidor-NonPreRelease-Longduration-Critical-53960-No failed units in the bootstrap machine", func() {
		skipTestIfSupportedPlatformNotMatched(oc, AWSPlatform, AzurePlatform)

		failedUnitsCommand := "sudo systemctl list-units --failed --all"

		// If no bootstrap is found, we skip the case.
		// The  test can only be executed in deployments that didn't remove the bootstrap machine
		bs, err := bootstrap.GetBootstrap(oc)
		if err != nil {
			if _, notFound := err.(*bootstrap.InstanceNotFound); notFound {
				g.Skip("skip test because bootstrap machine does not exist in the current cluster")
			}
		}
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Verify that there is no failed units in the bootstrap machine")
		// ssh client is a bit unstable, and it can return an empty string for no apparent reason every now and then.
		// Hence we use 'Eventually' to verify the command to make the test robust.
		o.Eventually(func() string {
			logger.Infof("Executing command in bootstrap: %s", failedUnitsCommand)
			failedUnits, err := bs.SSH.RunOutput(failedUnitsCommand)
			logger.Infof("Command output:\n%s", failedUnits)
			if err != nil {
				logger.Errorf("Command Error:\n%s", err)
			}
			return failedUnits
		}).Should(o.ContainSubstring("0 loaded units listed"),
			"There are failed units in the bootstrap machine")

	})
	g.It("Author:sregidor-NonPreRelease-Longduration-Medium-72129-Don't allow creating the force file via MachineConfig [Disruptive]", func() {
		var (
			filePath    = "/run/machine-config-daemon-force"
			fileContent = ""
			fileMode    = "0420" // decimal 272
			fileConfig  = getURLEncodedFileConfig(filePath, fileContent, fileMode)
			mcName      = "mco-tc-55879-create-force-file"
			mcp         = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)

			expectedRDMessage = regexp.QuoteMeta(fmt.Sprintf("cannot create %s via Ignition", filePath)) // quotemeta to scape regex characters in the file path
			expectedRDReason  = ""
		)

		exutil.By("Create the force file using a MC")

		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		mc.parameters = []string{fmt.Sprintf("FILES=[%s]", fileConfig)}
		mc.skipWaitForMcp = true

		validateMcpRenderDegraded(mc, mcp, expectedRDMessage, expectedRDReason)

	})

	g.It("NonHyperShiftHOST-Author:rioliu-Medium-54937-logs and events are flood with clusterrole and clusterrolebinding [Disruptive]", func() {

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

	g.It("Author:sregidor-NonPreRelease-Longduration-Medium-54922-daemon: add check before updating kernelArgs [Disruptive]", func() {
		var (
			mcNameArg1         = "tc-54922-kernel-args-1"
			mcNameArg2         = "tc-54922-kernel-args-2"
			mcNameExt          = "tc-54922-extension"
			kernelArg1         = "test1"
			kernelArg2         = "test2"
			usbguardMCTemplate = "change-worker-extension-usbguard.yaml"

			expectedLogArg1Regex = regexp.QuoteMeta("Running rpm-ostree [kargs") + ".*" + regexp.QuoteMeta(fmt.Sprintf("--append=%s", kernelArg1)) +
				".*" + regexp.QuoteMeta("]")
			expectedLogArg2Regex = regexp.QuoteMeta("Running rpm-ostree [kargs") + ".*" + regexp.QuoteMeta(fmt.Sprintf("--delete=%s", kernelArg1)) +
				".*" + regexp.QuoteMeta(fmt.Sprintf("--append=%s", kernelArg1)) +
				".*" + regexp.QuoteMeta(fmt.Sprintf("--append=%s", kernelArg2)) +
				".*" + regexp.QuoteMeta("]")

			// Expr: "kargs .*--append|kargs .*--delete"
			// We need to scape the "--" characters
			expectedNotLogExtensionRegex = "kargs .*" + regexp.QuoteMeta("--") + "append|kargs .*" + regexp.QuoteMeta("--") + "delete"

			mcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		)

		skipTestIfSupportedPlatformNotMatched(oc, AWSPlatform, GCPPlatform)
		workerNode := skipTestIfOsIsNotCoreOs(oc)

		mcp.SetWaitingTimeForExtensionsChange()

		// Create MC to add kernel arg 'test1'
		exutil.By(fmt.Sprintf("Create a MC to add a kernel arg: %s", kernelArg1))
		mcArgs1 := NewMachineConfig(oc.AsAdmin(), mcNameArg1, MachineConfigPoolWorker)
		mcArgs1.parameters = []string{fmt.Sprintf(`KERNEL_ARGS=["%s"]`, kernelArg1)}
		mcArgs1.skipWaitForMcp = true

		defer mcArgs1.delete()
		mcArgs1.create()
		logger.Infof("OK!\n")

		exutil.By("Check that the MCD logs are tracing the new kernel argument")
		// We don't know if the selected node will be updated first or last, so we have to wait
		// the same time we would wait for the mcp to be updated. Aprox.
		timeToWait := mcp.estimateWaitDuration()
		logger.Infof("waiting time: %s", timeToWait.String())
		o.Expect(workerNode.CaptureMCDaemonLogsUntilRestartWithTimeout(timeToWait.String())).To(
			o.MatchRegexp(expectedLogArg1Regex),
			"A log line reporting new kernel arguments should be present in the MCD logs when we add a kernel argument via MC")
		logger.Infof("OK!\n")

		exutil.By("Wait for worker pool to be updated")
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check that the new kernel argument was added")
		o.Expect(workerNode.IsKernelArgEnabled(kernelArg1)).To(o.BeTrue(),
			"Kernel argument %s was not enabled in the node %s", kernelArg1, workerNode.GetName())
		logger.Infof("OK!\n")

		// Create MC to add kernel arg 'test2'
		exutil.By(fmt.Sprintf("Create a MC to add a kernel arg: %s", kernelArg2))
		mcArgs2 := NewMachineConfig(oc.AsAdmin(), mcNameArg2, MachineConfigPoolWorker)
		mcArgs2.parameters = []string{fmt.Sprintf(`KERNEL_ARGS=["%s"]`, kernelArg2)}
		mcArgs2.skipWaitForMcp = true

		defer mcArgs2.deleteNoWait()
		mcArgs2.create()
		logger.Infof("OK!\n")

		exutil.By("Check that the MCD logs are tracing both kernel arguments")
		// We don't know if the selected node will be updated first or last, so we have to wait
		// the same time we would wait for the mcp to be updated. Aprox.
		logger.Infof("waiting time: %s", timeToWait.String())
		o.Expect(workerNode.CaptureMCDaemonLogsUntilRestartWithTimeout(timeToWait.String())).To(
			o.MatchRegexp(expectedLogArg2Regex),
			"A log line reporting the new kernel arguments configuration should be present in MCD logs")
		logger.Infof("OK!\n")

		exutil.By("Wait for worker pool to be updated")
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check that the both kernel arguments were added")
		o.Expect(workerNode.IsKernelArgEnabled(kernelArg1)).To(o.BeTrue(),
			"Kernel argument %s was not enabled in the node %s", kernelArg1, workerNode.GetName())
		o.Expect(workerNode.IsKernelArgEnabled(kernelArg2)).To(o.BeTrue(),
			"Kernel argument %s was not enabled in the node %s", kernelArg2, workerNode.GetName())
		logger.Infof("OK!\n")

		// Create MC to deploy an usbguard extension
		exutil.By("Create MC to add usbguard extension")
		mcUsb := NewMachineConfig(oc.AsAdmin(), mcNameExt, MachineConfigPoolWorker).SetMCOTemplate(usbguardMCTemplate)
		mcUsb.skipWaitForMcp = true

		defer mcUsb.deleteNoWait()
		mcUsb.create()
		logger.Infof("OK!\n")

		exutil.By("Check that the MCD logs do not make any reference to add or delete kargs")
		o.Expect(workerNode.CaptureMCDaemonLogsUntilRestartWithTimeout(timeToWait.String())).NotTo(
			o.MatchRegexp(expectedNotLogExtensionRegex),
			"MCD logs should not make any reference to kernel arguments addition/deletion when no new kernel arg is added/deleted")

		logger.Infof("OK!\n")

		exutil.By("Wait for worker pool to be updated")
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check that the usbguard extension was added")
		o.Expect(workerNode.RpmIsInstalled("usbguard")).To(
			o.BeTrue(),
			"usbguard rpm should be installed in node %s", workerNode.GetName())
		logger.Infof("OK!\n")
	})

	g.It("Author:sregidor-NonPreRelease-Longduration-Medium-56123-Invalid extensions should degrade the machine config pool [Disruptive]", func() {
		var (
			validExtension   = "usbguard"
			invalidExtension = "zsh"
			mcName           = "mco-tc-56123-invalid-extension"
			mcp              = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)

			expectedNDMessage = regexp.QuoteMeta(fmt.Sprintf("invalid extensions found: [%s]", invalidExtension)) // quotemeta to scape regex characters
			expectedNDReason  = "1 nodes are reporting degraded status on sync"
		)

		exutil.By("Create a MC with invalid extensions")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		mc.parameters = []string{fmt.Sprintf(`EXTENSIONS=["%s", "%s"]`, validExtension, invalidExtension)}
		mc.skipWaitForMcp = true

		validateMcpNodeDegraded(mc, mcp, expectedNDMessage, expectedNDReason, true)

	})

	g.It("NonHyperShiftHOST-Author:rioliu-Medium-54974-silence audit log events for container infra", func() {

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

	g.It("Author:sregidor-DEPRECATED-NonPreRelease-Longduration-Medium-56706-Move MCD drain alert into the MCC, revisit error mode[Disruptive]", func() {
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
		defer mc.delete()
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
		nodeDegradedConditionJSON := JSON(nodeDegradedCondition)
		nodeDegradedMessage := nodeDegradedConditionJSON.Get("message").ToString()
		expectedDegradedNodeMessage := fmt.Sprintf("failed to drain node: %s after 1 hour. Please see machine-config-controller logs for more information", workerNode.GetName())

		logger.Infof("MCP NodeDegraded condition: %s", nodeDegradedCondition)
		o.Expect(nodeDegradedMessage).To(o.ContainSubstring(expectedDegradedNodeMessage),
			"The error reported in the MCP NodeDegraded condition in not the expected one")
		logger.Infof("OK!\n")

		exutil.By("Verify that the alert is triggered")
		o.Eventually(getAlertsByName, "5m", "20s").WithArguments(oc, expectedAlertName).
			Should(o.HaveLen(1),
				"1 %s alert and only 1 should have been triggered!", expectedAlertName)
		logger.Infof("OK!\n")

		exutil.By("Verify that the alert has the right message")
		alertJSON, err := getAlertsByName(oc, expectedAlertName)

		logger.Infof("Found %s alerts: %s", expectedAlertName, alertJSON)

		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error trying to get the %s alert", expectedAlertName)
		o.Expect(alertJSON).To(o.HaveLen(1),
			"One and only one %s alert should be reported because of the eviction problems", expectedAlertName)

		expectedDescription := fmt.Sprintf("Drain failed on %s , updates may be blocked. For more details check MachineConfigController pod logs: oc logs -f -n openshift-machine-config-operator machine-config-controller-xxxxx -c machine-config-controller", workerNode.GetName())
		o.Expect(alertJSON[0].Get("annotations").Get("description").ToString()).Should(o.ContainSubstring(expectedDescription),
			"The error description should make a reference to the pod info")

		expectedSummary := "Alerts the user to a failed node drain. Always triggers when the failure happens one or more times."
		o.Expect(alertJSON[0].Get("annotations").Get("summary").ToString()).Should(o.Equal(expectedSummary),
			"The alert has a wrong 'summary' annotation value")

		// Since OCPBUGS-904 we need to check that the namespace is reported properly
		o.Expect(alertJSON[0].Get("labels").Get("namespace").ToString()).Should(o.Equal(MachineConfigNamespace),
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

	g.It("Author:sregidor-NonPreRelease-Longduration-Medium-56614-Create unit with content and mask=true[Disruptive]", func() {
		var (
			workerNode     = NewNodeList(oc).GetAllLinuxWorkerNodesOrFail()[0]
			maskedString   = "Loaded: masked"
			inactiveString = "Active: inactive (dead)"
			mcName         = "tc-56614-maks-and-contents"
			svcName        = "tc-56614-maks-and-contents.service"
			svcContents    = "[Unit]\nDescription=Just random content for test case 56614"
			maskSvcConfig  = getMaskServiceWithContentsConfig(svcName, true, svcContents)
		)

		exutil.By("Create a MachineConfig resource to mask the chronyd service")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		mc.parameters = []string{fmt.Sprintf("UNITS=[%s]", maskSvcConfig)}
		defer mc.delete()

		mc.create()
		logger.Infof("OK!\n")

		exutil.By("Wait until worker MachineConfigPool has finished the configuration")
		mcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Validate that the service is masked")
		output, _ := workerNode.DebugNodeWithChroot("systemctl", "status", svcName)
		// Since the service is masked, the "systemctl status" command will return a value != 0 and an error will be reported
		// So we dont check the error, only the output
		o.Expect(output).Should(o.And(
			o.ContainSubstring(inactiveString),
			o.ContainSubstring(maskedString),
		),
			"Service %s should be inactive and masked, but it is not.", svcName)
		logger.Infof("OK!\n")

		exutil.By("Validate the content")
		rf := NewRemoteFile(workerNode, "/etc/systemd/system/"+svcName)
		rferr := rf.Stat()
		o.Expect(rferr).NotTo(o.HaveOccurred())
		o.Expect(rf.GetSymLink()).Should(o.Equal(fmt.Sprintf("'/etc/systemd/system/%s' -> '/dev/null'", svcName)),
			"The service is masked, so service's file should be a link to /dev/null")
		logger.Infof("OK!\n")

	})

	g.It("Author:sregidor-NonPreRelease-Longduration-Medium-57595-Use empty pull-secret[Disruptive]", func() {
		var (
			pullSecret = GetPullSecret(oc.AsAdmin())
			wMcp       = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			mMcp       = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
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
	})

	g.It("Author:sregidor-NonHyperShiftHOST-NonPreRelease-Longduration-Medium-72132-enable FIPS by MCO not supported [Disruptive]", func() {
		var (
			mcTemplate = "change-fips.yaml"
			mcName     = "mco-tc-25819-master-fips"
			wMcp       = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			mMcp       = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)

			expectedRDMessage = regexp.QuoteMeta("detected change to FIPS flag; refusing to modify FIPS on a running cluster")
			expectedRDReason  = ""
		)

		// If FIPS is already enabled, we skip the test case
		skipTestIfFIPSIsEnabled(oc.AsAdmin())

		exutil.By("Try to enable FIPS in master pool")
		mMc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolMaster).SetMCOTemplate(mcTemplate)
		mMc.parameters = []string{"FIPS=true"}
		mMc.skipWaitForMcp = true

		validateMcpRenderDegraded(mMc, mMcp, expectedRDMessage, expectedRDReason)
		logger.Infof("OK!\n")

		exutil.By("Try to enable FIPS in worker pool")
		wMc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker).SetMCOTemplate(mcTemplate)
		wMc.parameters = []string{"FIPS=true"}
		wMc.skipWaitForMcp = true

		validateMcpRenderDegraded(wMc, wMcp, expectedRDMessage, expectedRDReason)
		logger.Infof("OK!\n")

	})

	g.It("Author:sregidor-NonHyperShiftHOST-NonPreRelease-Longduration-Low-72135-Refuse to disable FIPS mode by MCO[Disruptive]", func() {
		var (
			mMcName = "99-master-fips"
			wMcName = "99-worker-fips"
			wMcp    = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			mMcp    = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
			mMc     = NewMachineConfig(oc.AsAdmin(), mMcName, MachineConfigPoolMaster)
			wMc     = NewMachineConfig(oc.AsAdmin(), wMcName, MachineConfigPoolWorker)

			expectedRDMessage = regexp.QuoteMeta("detected change to FIPS flag; refusing to modify FIPS on a running cluster")
			expectedRDReason  = ""
		)

		// If FIPS is already disabled, we skip the test case
		skipTestIfFIPSIsNotEnabled(oc.AsAdmin())

		defer func() {
			logger.Infof("Starting defer logic")
			mMc.Patch("merge", `{"spec":{"fips": true}}`)
			wMc.Patch("merge", `{"spec":{"fips": true}}`)
			wMcp.RecoverFromDegraded()
			mMcp.RecoverFromDegraded()
		}()

		exutil.By("Patch the master-fips MC and set fips=false")
		mMc.Patch("merge", `{"spec":{"fips": false}}`)
		checkDegraded(mMcp, expectedRDMessage, expectedRDReason, "RenderDegraded", false, 1)

		exutil.By("Try to disasble FIPS in worker pool")
		wMc.Patch("merge", `{"spec":{"fips": false}}`)
		checkDegraded(wMcp, expectedRDMessage, expectedRDReason, "RenderDegraded", false, 1)

	})

	g.It("Author:sregidor-NonHyperShiftHOST-NonPreRelease-Longduration-Medium-59837-Use wrong user when creating a file [Disruptive]", func() {
		var (
			mcName              = "mco-tc-59837-create-file-with-wrong-user"
			wrongUserFileConfig = `{"contents": {"source": "data:text/plain;charset=utf-8;base64,dGVzdA=="},"mode": 420,"path": "/etc/wronguser-test-file.test","user": {"name": "wronguser"}}`
			mcp                 = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			// quotemeta to scape regex characters in the file path
			expectedNDMessage = regexp.QuoteMeta(`failed to retrieve file ownership for file \"/etc/wronguser-test-file.test\": failed to retrieve UserID for username: wronguser`)
			expectedNDReason  = "1 nodes are reporting degraded status on sync"
		)

		exutil.By("Create the force file using a MC")

		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		mc.parameters = []string{fmt.Sprintf("FILES=[%s]", wrongUserFileConfig)}
		mc.skipWaitForMcp = true

		validateMcpNodeDegraded(mc, mcp, expectedNDMessage, expectedNDReason, false)

	})

	g.It("Author:sregidor-NonHyperShiftHOST-NonPreRelease-Longduration-Medium-59867-Create files specifying user and group [Disruptive]", func() {
		var (
			filesContent = "test"
			coreUserID   = 1000
			coreGroupID  = 1000
			rootUserID   = 0
			admGroupID   = 4

			allFiles = []ign32File{
				{
					Path: "/etc/core-core-name-test-file.test",
					Contents: ign32Contents{
						Source: GetBase64EncodedFileSourceContent(filesContent),
					},
					Mode: PtrInt(420), // decimal 0644
					User: &ign32FileUser{
						Name: "core",
					},
					Group: &ign32FileGroup{
						Name: "core",
					},
				},
				{
					Path: "/etc/core-core-id-test-file.test",
					Contents: ign32Contents{
						Source: GetBase64EncodedFileSourceContent(filesContent),
					},
					Mode: PtrInt(416), // decimal 0640
					User: &ign32FileUser{
						ID: PtrInt(coreUserID), // core user ID number
					},
					Group: &ign32FileGroup{
						ID: PtrInt(coreGroupID), // core group ID number
					},
				},
				{
					Path: "/etc/root-adm-id-test-file.test",
					Contents: ign32Contents{
						Source: GetBase64EncodedFileSourceContent(filesContent),
					},
					Mode: PtrInt(384), // decimal 0600
					User: &ign32FileUser{
						ID: PtrInt(rootUserID),
					},
					Group: &ign32FileGroup{
						ID: PtrInt(admGroupID),
					},
				},
				{
					Path: "/etc/nouser-test-file.test",
					Contents: ign32Contents{
						Source: GetBase64EncodedFileSourceContent(filesContent),
					},
					Mode: PtrInt(420), // decimal 0644
					User: &ign32FileUser{
						ID: PtrInt(12343), // this user does not exist
					},
					Group: &ign32FileGroup{
						ID: PtrInt(34321), // this group does not exist
					},
				},
			}
			wMcp       = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			workerNode = wMcp.GetNodesOrFail()[0] // we don't want to get "windows" nodes
		)

		// Maybe in the future we can add some logic to create the "core" user and the "core" group with the right IDs
		// Now we will skip the test case to avoid breaking executions with RHEL nodes.
		if len(NewNodeList(oc).GetAllRhelWokerNodesOrFail()) != 0 {
			g.Skip("There are yum based RHEL nodes in the cluster. This test cannot be executed because no 'core' user/group exist in RHEL nodes")
		}

		exutil.By("Create new machine config to create files with different users and groups")
		fileConfig := MarshalOrFail(allFiles)
		mcName := "tc-59867-create-files-with-users"

		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		mc.parameters = []string{fmt.Sprintf("FILES=%s", fileConfig)}
		defer mc.delete()

		mc.create()
		wMcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check that all files have been created with the right user, group, permissions and data")
		for _, file := range allFiles {
			logger.Infof("")
			logger.Infof("CHecking file: %s", file.Path)
			rf := NewRemoteFile(workerNode, file.Path)
			o.Expect(rf.Fetch()).NotTo(o.HaveOccurred(), "Error getting the file %s in node %s", file.Path, workerNode.GetName())

			logger.Infof("Checking content: %s", rf.GetTextContent())
			o.Expect(rf.GetTextContent()).To(o.Equal(filesContent),
				"The content of file %s is wrong!", file.Path)

			// Verify that the defined user name or user id (only one can be defined in the config) is the expected one
			if file.User.Name != "" {
				logger.Infof("Checking user name: %s", rf.GetUIDName())
				o.Expect(rf.GetUIDName()).To(o.Equal(file.User.Name),
					"The user who owns file %s is wrong!", file.Path)
			} else {
				logger.Infof("Checking user id: %s", rf.GetUIDNumber())
				o.Expect(rf.GetUIDNumber()).To(o.Equal(fmt.Sprintf("%d", *file.User.ID)),
					"The user id what owns file %s is wrong!", file.Path)
			}

			// Verify that if the user ID is defined and its value is the core user's one. Then the name should be "core"
			if file.User.ID != nil && *file.User.ID == coreUserID {
				logger.Infof("Checking core user name for ID: %s", rf.GetUIDNumber())
				o.Expect(rf.GetUIDName()).To(o.Equal("core"),
					"The user name who owns file %s is wrong! User name for Id %s should be 'core'",
					file.Path, rf.GetUIDNumber())
			}

			// Verify that if the user ID is defined and its value is the root user's one. Then the name should be "root"
			if file.User.ID != nil && *file.User.ID == rootUserID {
				logger.Infof("Checking root user name: %s", rf.GetUIDName())
				o.Expect(rf.GetUIDName()).To(o.Equal("root"),
					"The user name who owns file %s is wrong! User name for Id %s should be 'root'",
					file.Path, rf.GetUIDNumber())
			}

			// Verify that the defined group name or group id (only one can be defined in the config) is the expected one
			if file.Group.Name != "" {
				logger.Infof("Checking group name: %s", rf.GetGIDName())
				o.Expect(rf.GetGIDName()).To(o.Equal(file.Group.Name),
					"The group that owns file %s is wrong!", file.Path)
			} else {
				logger.Infof("Checking group id: %s", rf.GetGIDNumber())
				o.Expect(rf.GetGIDNumber()).To(o.Equal(fmt.Sprintf("%d", *file.Group.ID)),
					"The group id what owns file %s is wrong!", file.Path)
			}

			// Verify that if the group ID is defined and its value is the core group's one. Then the name should be "core"
			if file.Group.ID != nil && *file.Group.ID == coreGroupID {
				logger.Infof("Checking core group name for ID: %s", rf.GetUIDNumber())
				o.Expect(rf.GetGIDName()).To(o.Equal("core"),
					"The group name who owns file %s is wrong! Group name for Id %s should be 'core'",
					file.Path, rf.GetGIDNumber())
			}

			// Verify that if the group ID is defined and its value is the adm group's one. Then the name should be "adm"
			if file.Group.ID != nil && *file.Group.ID == admGroupID {
				logger.Infof("Checking adm group name: %s", rf.GetUIDNumber())
				o.Expect(rf.GetGIDName()).To(o.Equal("adm"),
					"The group name who owns file %s is wrong! Group name for Id %s should be 'adm'",
					file.Path, rf.GetGIDNumber())
			}

			logger.Infof("Checking file permissions: %s", rf.GetNpermissions())
			decimalPerm := ConvertOctalPermissionsToDecimalOrFail(rf.GetNpermissions())
			o.Expect(decimalPerm).To(o.Equal(*file.Mode),
				"The permssions of file %s are wrong", file.Path)

			logger.Infof("OK!\n")
		}

	})

	g.It("Author:sregidor-NonHyperShiftHOST-NonPreRelease-Longduration-Medium-61555-ImageDigestMirrorSet test [Disruptive]", func() {
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

	g.It("Author:sregidor-NonHyperShiftHOST-NonPreRelease-Longduration-Medium-61558-ImageTagMirrorSet test [Disruptive]", func() {
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
		o.Expect(nodeEvents).To(HaveEventsSequence("Drain"), "Error, a Drain event was triggered but it shouldn't")
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

	g.It("Author:sregidor-NonHyperShiftHOST-NonPreRelease-Longduration-Critical-62084-Certificate rotation in paused pools[Disruptive]", func() {
		var (
			wMcp       = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			mMcp       = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
			certSecret = NewSecret(oc.AsAdmin(), "openshift-kube-apiserver-operator", "kube-apiserver-to-kubelet-signer")
		)

		exutil.By("Pause MachineConfigPools")
		defer mMcp.waitForComplete()
		defer wMcp.waitForComplete()

		defer wMcp.pause(false)
		wMcp.pause(true)
		defer mMcp.pause(false)
		mMcp.pause(true)
		logger.Infof("OK!\n")

		exutil.By("Get current kube-apiserver certificate")
		initialCert := certSecret.GetDataValueOrFail("tls.crt")
		logger.Infof("Current certificate length: %d", len(initialCert))
		logger.Infof("OK!\n")

		exutil.By("Rotate certificate")
		o.Expect(
			certSecret.Patch("merge", `{"metadata": {"annotations": {"auth.openshift.io/certificate-not-after": null}}}`),
		).To(o.Succeed(),
			"The secret could not be patched in order to rotate the certificate")
		logger.Infof("OK!\n")

		exutil.By("Get current kube-apiserver certificate")
		logger.Infof("Wait for certificate rotation")
		o.Eventually(certSecret.GetDataValueOrFail).WithArguments("tls.crt").
			ShouldNot(o.Equal(initialCert),
				"The certificate was not rotated")

		newCert := certSecret.GetDataValueOrFail("tls.crt")
		logger.Infof("New certificate length: %d", len(newCert))
		logger.Infof("OK!\n")

		o.Expect(initialCert).NotTo(o.Equal(newCert),
			"The certificate was not rotated")
		logger.Infof("OK!\n")

		// We verify all nodes in the pools (be aware that windows nodes do not belong to any pool, we are skipping them)
		for _, node := range append(wMcp.GetNodesOrFail(), mMcp.GetNodesOrFail()...) {
			logger.Infof("Checking certificate in node: %s", node.GetName())

			rfCert := NewRemoteFile(node, "/etc/kubernetes/kubelet-ca.crt")

			// Eventually the certificate file in all nodes should contain the new rotated certificate
			o.Eventually(func(gm o.Gomega) string { // Passing o.Gomega as parameter we can use assertions inside the Eventually function without breaking the retries.
				gm.Expect(rfCert.Fetch()).To(o.Succeed(),
					"Cannot read the certificate file in node:%s ", node.GetName())
				return rfCert.GetTextContent()
			}, "5m", "10s").
				Should(o.ContainSubstring(newCert),
					"The certificate file %s in node %s does not contain the new rotated certificate.", rfCert.GetFullPath(), node.GetName())
			logger.Infof("OK!\n")
		}

		exutil.By("Unpause MachineConfigPools")
		logger.Infof("Check that once we unpause the pools the pending config can be applied without problems")
		wMcp.pause(false)
		mMcp.pause(false)
		wMcp.waitForComplete()
		mMcp.waitForComplete()

		logger.Infof("OK!\n")
	})

	g.It("Author:sregidor-NonHyperShiftHOST-Low-63784-MCD pivot command should be deprecated", func() {
		var (
			mMcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
			// Note the last space in the first line.
			expectedOutput = "ERROR: pivot no longer forces a system upgrade. It will be fully removed in a later y release. \n" +
				"\tIf you are attempting a manual OS upgrade, please try the following steps:\n" +
				"\t-delete the currentconfig(rm /etc/machine-config-daemon/currentconfig)\n" +
				"\t-create a forcefile(touch /run/machine-config-daemon-force) to retry the OS upgrade.\n" +
				"\n" +
				"\tMore instructions can be found here: https://access.redhat.com/solutions/5598401\n"
		)
		masterNode := mMcp.GetNodesOrFail()[0]

		output, _ := masterNode.DebugNodeWithChroot("/run/bin/machine-config-daemon", "pivot", "test")
		logger.Infof("expect:\n%s", expectedOutput)
		logger.Infof("output:\n%s", output)
		o.Expect(
			output,
		).To(
			o.ContainSubstring(expectedOutput),
			"MCD pivot command should show a deprecation warning. But it doesn't",
		)
	})

	g.It("Author:sregidor-NonHyperShiftHOST-NonPreRelease-Longduration-Medium-63477-Deploy files using all available ignition configs. Default 3.4.0[Disruptive]", func() {
		var (
			wMcp                   = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			mcNames                = "mc-tc-63477"
			allVersions            = []string{"2.2.0", "3.0.0", "3.1.0", "3.2.0", "3.3.0", "3.4.0"}
			defaultIgnitionVersion = "3.4.0" // default version is 3.4.0 for OCP > 4.13
		)
		defer wMcp.waitForComplete()

		exutil.By("Create MCs with all available ignition versions")
		for _, version := range allVersions {
			vID := strings.ReplaceAll(version, ".", "-")
			fileConfig := ""
			filePath := "/etc/" + vID + ".test"
			mcName := mcNames + "-" + vID

			mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
			mc.skipWaitForMcp = true
			defer mc.deleteNoWait()

			logger.Infof("Create MC %s", mc.name)
			// 2.2.0 ignition config defines a different config for files
			if version == "2.2.0" {
				logger.Infof("Generating 2.2.0 file config!")
				file := ign22File{
					Path: filePath,
					Contents: ign22Contents{
						Source: GetBase64EncodedFileSourceContent(version + " test file"),
					},
					Mode:       PtrInt(420), // decimal 0644
					Filesystem: "root",
				}

				fileConfig = string(MarshalOrFail(file))
			} else {
				logger.Debugf("Generating 3.x file config!")
				file := ign32File{
					Path: filePath,
					Contents: ign32Contents{
						Source: GetBase64EncodedFileSourceContent(version + " test file"),
					},
					Mode: PtrInt(420), // decimal 0644
				}

				fileConfig = string(MarshalOrFail(file))
			}

			mc.parameters = []string{fmt.Sprintf("FILES=[%s]", fileConfig), "IGNITION_VERSION=" + version}
			mc.create()
		}
		logger.Infof("OK!\n")

		exutil.By("Wait for MCP to be updated")
		wMcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Verify default rendered ignition version")
		renderedMC, err := wMcp.GetConfiguredMachineConfig()
		o.Expect(err).NotTo(o.HaveOccurred(), "Cannot get the rendered config for pool %s", wMcp.GetName())
		o.Expect(renderedMC.GetIgnitionVersion()).To(o.Equal(defaultIgnitionVersion),
			"Rendered MC should use %s default ignition version", defaultIgnitionVersion)
		logger.Infof("OK!\n")

		exutil.By("Verify that all files were created")
		node := wMcp.GetNodesOrFail()[0]
		for _, version := range allVersions {
			vID := strings.ReplaceAll(version, ".", "-")
			filePath := "/etc/" + vID + ".test"

			logger.Infof("Checking file %s", filePath)
			rf := NewRemoteFile(node, filePath)
			o.Expect(rf.Fetch()).NotTo(o.HaveOccurred(),
				"Cannot get information about file %s in node %s", filePath, node.GetName())

			o.Expect(rf.GetTextContent()).To(o.Equal(version+" test file"),
				"File %s in node %s ha not the right content")

		}
		logger.Infof("OK!\n")
	})

	g.It("Author:sregidor-NonHyperShiftHOST-NonPreRelease-Longduration-High-63868-ControllerConfig sync after Infrastructure objects are updated[Disruptive]", func() {
		var (
			label      = "break.the.mco"
			labelValue = "yes-tc-63868"
			infra      = NewResource(oc.AsAdmin(), "Infrastructure", "cluster")
			mcCO       = NewResource(oc.AsAdmin(), "ClusterOperator", "machine-config")
		)

		exutil.By("Label a Infrastructure resource")
		defer func() {
			// In case of error, the machine-config ClusterOperator will become degraded,
			// so we need to recover the machine-config CO from degraded state.
			// It is done by removing the machine-config-operator pod.
			_ = infra.RemoveLabel(label)
			oc.AsAdmin().WithoutNamespace().Run("delete").Args("pod", "-n", MachineConfigNamespace,
				"-l", "k8s-app=machine-config-operator", "--ignore-not-found=true").Execute()
			o.Eventually(mcCO, "5m", "30s").ShouldNot(BeDegraded(), "Could not recover the machine-config CO from degraded status")

		}()
		o.Expect(
			infra.AddLabel(label, labelValue),
		).To(
			o.Succeed(),
			"%s/%s could not be labeled", infra.GetKind(), infra.GetName())
		logger.Infof("OK!\n")

		exutil.By("Check that machine-config ClusterOperator is not degraded")
		o.Consistently(mcCO,
			"5m", "30s").ShouldNot(BeDegraded(),
			"machine-config ClusterOperator is degraded.\n%s", mcCO.PrettyString())
		logger.Infof("OK!\n")

	})

	g.It("Author:sregidor-NonHyperShiftHOST-NonPreRelease-Low-72136-Reject MCs with ignition containing kernelArguments [Disruptive]", func() {
		var (
			mcName = "mco-tc-66376-reject-ignition-kernel-arguments"
			mcp    = GetCompactCompatiblePool(oc.AsAdmin())
			// quotemeta to scape regex characters in the file path
			expectedRDMessage = regexp.QuoteMeta(`ignition kargs section contains changes`)
			expectedRDReason  = ""
		)

		exutil.By("Create a MC with an ignition section that declares kernel arguments")

		mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName()).SetMCOTemplate("add-ignition-kernel-arguments.yaml")
		mc.skipWaitForMcp = true

		validateMcpRenderDegraded(mc, mcp, expectedRDMessage, expectedRDReason)

	})

	g.It("Author:sregidor-NonHyperShiftHOST-NonPreRelease-Medium-66436-disable weak SSH cipher suites [Serial]", func() {

		var (
			// the list of weak cipher suites can be found here:  https://issues.redhat.com/browse/OCPBUGS-15202
			weakSuites = []string{"TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA",
				"TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA",
				"TLS_RSA_WITH_AES_128_CBC_SHA",
				"TLS_RSA_WITH_AES_256_CBC_SHA",
				"TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA256"}
		)

		exutil.By("Verify that the controller pod is not using weakSuites")
		ccRbacProxyArgs, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("pod", "-n", MachineConfigNamespace, "-l", ControllerLabel+"="+ControllerLabelValue,
			"-o", `jsonpath={.items[0].spec.containers[?(@.name=="kube-rbac-proxy")].args}`).Output()

		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the arguments used in kube-rbac-proxy container in the controller pod")

		o.Expect(ccRbacProxyArgs).To(o.ContainSubstring("--tls-cipher-suites"),
			"Controller's kube-rbac-proxy container is not declaring the list of allowed cipher suites")

		for _, weakSuite := range weakSuites {
			logger.Infof("Verifying that %s is not used", weakSuite)
			o.Expect(ccRbacProxyArgs).NotTo(o.ContainSubstring(weakSuite),
				"Controller's kube-rbac-proxy container is using the weak cipher suite %s, and it should not", weakSuite)
			logger.Infof("Suite ok")
		}
		logger.Infof("OK!\n")

		exutil.By("Connect to the rbac-proxy service to verify the cipher")
		mMcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
		masterNode := mMcp.GetNodesOrFail()[0]
		cipherOutput, cipherErr := masterNode.DebugNodeWithOptions([]string{"--image=" + TestSSLImage, "-n", MachineConfigNamespace}, "testssl.sh", "--color", "0", "localhost:9001")
		logger.Infof("test ssh script output:\n %s", cipherOutput)
		o.Expect(cipherErr).NotTo(o.HaveOccurred())
		o.Expect(cipherOutput).Should(o.MatchRegexp(`Obsoleted CBC ciphers \(AES, ARIA etc.\) +not offered`))

		for _, weakSuite := range weakSuites {
			logger.Infof("Verifying that %s is not used", weakSuite)
			o.Expect(cipherOutput).NotTo(o.ContainSubstring(weakSuite),
				"The rbac-proxy service cipher test is reporting weak cipher suite: %s", weakSuite)
			logger.Infof("Suite ok")
		}
		logger.Infof("OK!\n")
	})

	g.It("Author:sregidor-NonHyperShiftHOST-Low-65208-Check the visibility of certificates", func() {
		var (
			cc   = NewControllerConfig(oc.AsAdmin(), "machine-config-controller")
			mMcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
		)

		exutil.By("Check that the ControllerConfig resource is storing the right kube-apiserver-client-ca information")
		kubeAPIServerClientCACM := NewNamespacedResource(oc.AsAdmin(), "ConfigMap", "openshift-config-managed", "kube-apiserver-client-ca")
		kubeAPIServerClientCA := kubeAPIServerClientCACM.GetOrFail(`{.data.ca-bundle\.crt}`)

		ccKubeAPIServerClientCA, err := cc.GetKubeAPIServerServingCAData()
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the kubeAPIServerServingCAData information from the ControllerConfig")

		// We write the Expect command so that the certificates are not printed in case of failure
		o.Expect(strings.Trim(ccKubeAPIServerClientCA, "\n") == strings.Trim(kubeAPIServerClientCA, "\n")).To(o.BeTrue(),
			"The value of kubeAPIServerServingCAData in the ControllerConfig does not equal the value of configmap -n openshift-config-managed kube-apiserver-client-ca")
		logger.Infof("OK!\n")

		exutil.By("Check that the ControllerConfig resource is storing the right rootCAData  information")
		rootCADataCM := NewNamespacedResource(oc.AsAdmin(), "ConfigMap", "kube-system", "root-ca")
		rootCAData := rootCADataCM.GetOrFail(`{.data.ca\.crt}`)

		ccRootCAData, err := cc.GetRootCAData()
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the rootCAData information from the ControllerConfig")

		// We write the Expect command so that the certificates are not printed in case of failure
		o.Expect(strings.Trim(ccRootCAData, "\n") == strings.Trim(rootCAData, "\n")).To(o.BeTrue(),
			"The value of rootCAData in the ControllerConfig does not equal the value of configmap -n kube-system root-ca")
		logger.Infof("OK!\n")

		exutil.By("Check the information from the KubeAPIServerServingCAData certificates")

		ccKCertsInfo, err := cc.GetCertificatesInfoByBundleFileName("KubeAPIServerServingCAData")
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the controller config information for KubeAPIServerServingCAData certificates")

		kubeAPIServerCertsInfo, err := GetCertificatesInfoFromPemBundle("KubeAPIServerServingCAData", []byte(ccKubeAPIServerClientCA))
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error extracting certificate info from KubeAPIServerServingCAData pem bundle")

		o.Expect(kubeAPIServerCertsInfo).To(o.Equal(ccKCertsInfo),
			"The ControllerConfig is not reporting the right information about the certificates in KubeAPIServerServingCAData bundle")

		logger.Infof("OK!\n")

		exutil.By("Check the information from the rootCAData certificates")

		ccRCertsInfo, err := cc.GetCertificatesInfoByBundleFileName("RootCAData")
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the controller config information for rootCAData certificates")

		rootCACertsInfo, err := GetCertificatesInfoFromPemBundle("RootCAData", []byte(ccRootCAData))
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error extracting certificate info from RootCAData pem bundle")

		o.Expect(rootCACertsInfo).To(o.Equal(ccRCertsInfo),
			"The ControllerConfig is not reporting the right information about the certificates in rootCAData pem bundle")
		logger.Infof("OK!\n")

		exutil.By("Check that MCPs are reporting information regarding kubeapiserverserviccadata certificates")
		certsExpiry, err := mMcp.GetCertsExpiry()
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the certificates expiry information from master MCP")

		o.Expect(certsExpiry).To(o.HaveLen(len(ccKCertsInfo)),
			"The expry certs info reported in master MCP has len %d, but the list of kubeAPIServer certs has len %d.\nExpiry:%s\nKubeAPIServer:%s",
			len(certsExpiry), len(ccKCertsInfo), certsExpiry, ccKCertsInfo)

		for i, certInfo := range ccKCertsInfo {
			certExpry := certsExpiry[i]

			logger.Infof("%s", certExpry)

			o.Expect(certExpry).To(ogs.MatchAllFields(ogs.Fields{
				"Bundle": o.Equal(certInfo.BundleFile),

				// Date fields have been temporarily removed by devs:  https://github.com/openshift/machine-config-operator/pull/3866
				"Expiry":  o.Equal(certInfo.NotAfter),
				"Subject": o.Equal(certInfo.Subject),
			}),
				"Exipirty information does not match the information repoted in the ControllerConfig")
		}

		logger.Infof("OK!\n")

		exutil.By("Check that the description of ControllerConfig includes the certificates info")
		ccDesc, err := cc.Describe()
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error describing the ControllerConfig resource")

		o.Expect(ccDesc).To(o.And(
			o.ContainSubstring("Controller Certificates:"),
			o.ContainSubstring("Bundle File"),
			// Date fields have been temporarily removed by devs:  https://github.com/openshift/machine-config-operator/pull/3866
			o.ContainSubstring("Not After"),
			o.ContainSubstring("Not Before"),
			o.ContainSubstring("Signer"),
			o.ContainSubstring("Subject"),
		),
			"The ControllerConfig description should include information about the certificate, but it does not:\n%s", ccDesc)
		logger.Infof("OK!\n")

		exutil.By("Check that the description of MCP includes the certificates info")
		mMcpDesc, err := mMcp.Describe()
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error describing the master MCP resource")

		o.Expect(mMcpDesc).To(o.And(
			o.ContainSubstring("Cert Expirys"),
			o.ContainSubstring("Bundle"),
			// Date fields have been temporarily removed by devs:  https://github.com/openshift/machine-config-operator/pull/3866
			o.ContainSubstring("Expiry"),
		),
			"The master MCP description should include information about the certificate, but it does not:\n%s", ccDesc)
		logger.Infof("OK!\n")
	})

	g.It("Author:sregidor-NonHyperShiftHOST-Low-66046-Check image registry certificates", func() {

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
				"The certificate for file %s has not been included in the configmap merged-trusted-image-registry-ca -n openshift-config-managed")

			mmtImageRegistryCert, err := GetManagedMergedTrustedImageRegistryCertificates(oc.AsAdmin())
			o.Expect(err).NotTo(o.HaveOccurred(), "Error getting managed merged trusted image registry certificates values")

			o.Expect(mmtImageRegistryCert[certFile] == certValue).To(o.BeTrue(),
				"The certificate in file %s was added to configmap merged-trusted-image-registry-ca -n openshift-config-managed but it has the wrong content")

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

	g.It("Author:sregidor-NonHyperShiftHOST-NonPreRelease-Medium-64833-Do not make an 'orig' copy for config.json file [Serial]", func() {

		var (
			mMcp                = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
			wMcp                = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			configJSONFile      = "/var/lib/kubelet/config.json"
			configJSONOringFile = "/etc/machine-config-daemon/orig/var/lib/kubelet/config.json.mcdorig"
		)

		for _, node := range append(wMcp.GetNodesOrFail(), mMcp.GetNodesOrFail()...) {
			exutil.By(fmt.Sprintf("Check that the /var/lib/kubelet/config.json is preset in node %s", node.GetName()))

			configJSONRemoteFile := NewRemoteFile(node, configJSONFile)
			configJSONOringRemoteFile := NewRemoteFile(node, configJSONOringFile)

			o.Eventually(configJSONRemoteFile.Exists, "20s", "2s").Should(o.BeTrue(),
				"The file %s does not exist in node %s", configJSONRemoteFile.GetFullPath(), node.GetName())
			logger.Infof("OK!\n")

			exutil.By(fmt.Sprintf("Check that the /etc/machine-config-daemon/orig/var/lib/kubelet/config.json.mcdorig is NOT preset in node %s",
				node.GetName()))
			o.Eventually(configJSONOringRemoteFile.Exists, "20s", "2s").Should(o.BeFalse(),
				"The file %s exists in node %s, but it should NOT", configJSONOringRemoteFile.GetFullPath(), node.GetName())
			logger.Infof("OK!\n")
		}
	})

	g.It("Author:sregidor-NonPreRelease-Longduration-High-67788-kernel type 64k-pages is not supported on non-arm64 nodes [Disruptive]", func() {
		var (
			mcName = "mco-tc-67788-invalid-64k-pages-kernel"

			expectedNDMessage = `64k-pages is only supported for aarch64 architecture"`
			expectedNDReason  = "1 nodes are reporting degraded status on sync"
		)

		architecture.SkipArchitectures(oc.AsAdmin(), architecture.ARM64)
		mcp := GetPoolWithArchDifferentFromOrFail(oc.AsAdmin(), architecture.ARM64)

		exutil.By("Create a MC with invalid 64k-pages kernel")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName()).SetMCOTemplate("set-64k-pages-kernel.yaml")
		mc.skipWaitForMcp = true

		validateMcpNodeDegraded(mc, mcp, expectedNDMessage, expectedNDReason, false)
	})

	g.It("Author:rioliu-NonPreRelease-Critical-68695-MCO should not be degraded when image-registry is not installed [Serial]", func() {

		// for cluster setup, we need to use upi-on-aws + baselineCapabilitySet: None, because ipi needs known capability `machine-api`
		exutil.By("check whether capability ImageRegistry is enabled, if yes, skip the test")
		if IsCapabilityEnabled(oc, "ImageRegistry") {
			g.Skip("image registry is installed, skip this test")
		}

		exutil.By("check operator status, it should not be degraded")
		mco := NewResource(oc.AsAdmin(), "co", "machine-config")
		o.Expect(mco).ShouldNot(BeDegraded(),
			"co/machine-config Degraded condition status is not the expected one: %s", mco.GetConditionByType("Degraded"))
	})

	g.It("Author:rioliu-NonPreRelease-High-68687-HostToContainer propagation in MCD [Serial]", func() {

		platform := exutil.CheckPlatform(oc)
		assertFunc := func(gm o.Gomega, mountPropagations string) {
			logger.Infof("mountPropagations:\n %s", mountPropagations)
			for _, mp := range strings.Split(mountPropagations, " ") {
				gm.Expect(mp).Should(o.Equal("HostToContainer"), "mountPropagation value is not expected [%s]", mp)
			}
		}

		exutil.By("Check mountPropagation for the pods under mco namespace")
		mountPropagations := NewNamespacedResourceList(oc, "pod", MachineConfigNamespace).GetOrFail(`{.items[*].spec.containers[*].volumeMounts[?(@.mountPath=="/rootfs")].mountPropagation}`)
		o.Eventually(assertFunc).WithArguments(mountPropagations).Should(o.Succeed())

		if ns, ok := OnPremPlatforms[platform]; ok {
			exutil.By(fmt.Sprintf("Check mountPropagation for the pods on platform %s", platform))
			mountPropagations = NewNamespacedResourceList(oc, "pod", ns).GetOrFail(`{.items[*].spec.containers[*].volumeMounts[*].mountPropagation}`)
			o.Eventually(assertFunc).WithArguments(mountPropagations).Should(o.Succeed())
		}

		if platform == GCPPlatform || platform == AzurePlatform || platform == AlibabaCloudPlatform {
			exutil.By("Check mountPropagation for the apiserver-watcher pods under openshift-kube-apiserver namespace")
			pods, err := NewNamespacedResourceList(oc, "pod", "openshift-kube-apiserver").GetAll()
			o.Expect(err).NotTo(o.HaveOccurred(), "Get pod list under ns/openshift-kube-apiserver failed")
			for _, pod := range pods {
				if strings.HasPrefix(pod.GetName(), "apiserver-watcher") {
					mountPropagations = pod.GetOrFail(`{.spec.containers[*].volumeMounts[?(@.mountPath=="/rootfs")].mountPropagation}`)
					o.Eventually(assertFunc).WithArguments(mountPropagations).Should(o.Succeed())
				}
			}

		}
	})
	g.It("Author:sregidor-Longduration-NonPreRelease-Critical-67790-create MC with extensions, 64k-pages kernel type and kernel argument [Disruptive]", func() {

		architecture.SkipIfNoNodeWithArchitectures(oc.AsAdmin(), architecture.ARM64)
		clusterinfra.SkipTestIfNotSupportedPlatform(oc.AsAdmin(), clusterinfra.GCP)

		// If arm64 Compact/SNO we use master
		// Else if possible we create a custom MCP if there are arm64 nodes in the worker pool
		// Else if possible we use the first exsisting custom MCP with all its nodes using arm64
		// Else master is arm64 we use master
		// Else we fail the test
		createdCustomPoolName := fmt.Sprintf("mco-test-%s", architecture.ARM64)
		defer DeleteCustomMCP(oc.AsAdmin(), createdCustomPoolName)

		mcp, nodes := GetPoolAndNodesForArchitectureOrFail(oc.AsAdmin(), createdCustomPoolName, architecture.ARM64, 1)
		node := nodes[0]
		logger.Infof("Using node %s from pool %s", node.GetName(), mcp.GetName())

		exutil.By("Create new MC to add the kernel arguments, kernel type and extension")
		mcName := "change-worker-karg-ktype-extension"
		mcTemplate := mcName + ".yaml"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName()).SetMCOTemplate(mcTemplate)
		mc.parameters = []string{"KERNELTYPE=64k-pages"}
		defer mc.delete()
		mc.create()

		exutil.By("Check kernel arguments, kernel type and extension on the created machine config")
		o.Expect(
			getMachineConfigDetails(oc, mc.name),
		).Should(
			o.And(
				o.ContainSubstring("usbguard"),
				o.ContainSubstring("z=10"),
				o.ContainSubstring("64k-pages"),
			),
			"The new MC is not using the expected configuration")
		logger.Infof("OK!\n")

		exutil.By("Check kernel type")
		o.Expect(node.Is64kPagesKernel()).To(o.BeTrue(),
			"The installed kernel is not the expected one")

		o.Expect(
			node.RpmIsInstalled("kernel-64k-core", "kernel-64k-modules-core", "kernel-64k-modules-extra", "kernel-64k-modules"),
		).Should(o.BeTrue(),
			"The installed kernel rpm packages are not the expected ones")
		logger.Infof("OK!\n")

		exutil.By("Check installed extensions")
		o.Expect(
			node.RpmIsInstalled("usbguard"),
		).Should(
			o.BeTrue(),
			"The usbguard extension rpm is not installed")
		logger.Infof("OK!\n")

		exutil.By("Check kernel arguments")
		o.Expect(node.IsKernelArgEnabled("z=10")).To(o.BeTrue(),
			"The kernel arguments are not the expected ones")
		logger.Infof("OK!\n")
	})

	g.It("Author:rioliu-NonPreRelease-Medium-68688-kubeconfig must have 600 permissions in all nodes [Serial]", func() {
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

	g.It("Author:sregidor-NonPreRelease-Medium-69091-MCO skips reboot when configuration matches during node bootstrap pivot [Serial]", func() {
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

	g.It("Author:sregidor-NonPreRelease-High-68736-machine config server supports bootstrap with IR certs [Serial]", func() {
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

	g.It("Author:sregidor-NonPreRelease-High-68682-daemon should not pull baremetalRuntimeCfg every time [Serial]", func() {
		SkipIfNotOnPremPlatform(oc.AsAdmin())
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

	g.It("Author:sregidor-NonPreRelease-Medium-68686-MCD No invalid memory address or nil pointer dereference when kubeconfig file is not present in a node [Disruptive]", func() {
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

	g.It("Author:sregidor-NonPreRelease-Medium-68684-machine-config-controller pod restart should not make nodes unschedulable [Disruptive]", func() {
		var (
			controller = NewController(oc.AsAdmin())
			masterNode = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster).GetNodesOrFail()[0]
		)

		exutil.By("Check that nodes are not modified when the controller pod is removed")
		labels, err := masterNode.Get(`{.metadata.labels}`)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the labels in node %s", masterNode.GetName())

		masterNode.oc.NotShowInfo()        // avoid spamming the logs
		o.Consistently(func(gm o.Gomega) { // Passing o.Gomega as parameter we can use assertions inside the Consistently function without breaking the retries.
			logger.Infof("Remove controller pod")
			gm.Expect(controller.RemovePod()).To(o.Succeed(), "Could not remove the controller pod")

			logger.Infof("Check that the node was not modified")
			gm.Consistently(func(gm o.Gomega) {
				gm.Expect(masterNode.Get(`{.metadata.labels}`)).To(o.MatchJSON(labels),
					"Labels in node %s have changed after removing the controller pod, and they should not change", masterNode.GetName())
				gm.Expect(masterNode.IsCordoned()).To(o.BeFalse(),
					"The node %s was cordoned after removing the controller pod. Node: \n%s",
					masterNode.GetName(), masterNode.PrettyString())
			}, "10s", "0s").
				Should(o.Succeed(),
					"The node %s was modified when the controller pod was removed")
		}, "4m", "1s").
			Should(o.Succeed(),
				"When we remove the controller pod the node %s is modified")

		logger.Infof("OK!\n")
	})

	g.It("Author:sregidor-NonPreRelease-Medium-68797-Custom pool configs take priority over worker configs [Disruptive]", func() {
		if IsCompactOrSNOCluster(oc.AsAdmin()) {
			g.Skip("The cluster is SNO/Compact. This test cannot be executed in SNO/Compact clusters")
		}

		var (
			kubeletConfPath           = "/etc/kubernetes/kubelet.conf"
			wMcp                      = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			createdCustomPoolName     = "mco-test-68797"
			kcTemplate                = generateTemplateAbsolutePath("generic-kubelet-config.yaml")
			workerKcName              = "worker-tc-68797-kubeburst"
			workerKubeletConfig       = `{"kubeAPIBurst": 7000}`
			infraFirstKcName          = "infra-first-tc-68797-kubeburst"
			infraFirstKubeletConfig   = `{"kubeAPIBurst": 8000}`
			infraSecondKcName         = "infra-second-tc-68797-kubeburst"
			infraSeconddKubeletConfig = `{"kubeAPIBurst": 9000}`
		)

		// In DeleteCustomMCP deffered function, when we delete a MCP, we wait first for the worker MCP to be updated.
		//   No need to defer the worker MCP WaitForComplete logic.
		defer DeleteCustomMCP(oc.AsAdmin(), createdCustomPoolName)
		infraMcp, err := CreateCustomMCP(oc.AsAdmin(), createdCustomPoolName, 1)
		o.Expect(err).NotTo(o.HaveOccurred(), "Could not create a new custom MCP")

		exutil.By("Create Kubelet Configurations")
		logger.Infof("Create worker KubeletConfig")
		wKc := NewKubeletConfig(oc.AsAdmin(), workerKcName, kcTemplate)
		defer wKc.Delete()
		wKc.create("KUBELETCONFIG="+workerKubeletConfig, "POOL="+wMcp.GetName())

		exutil.By("Wait for configurations to be applied in worker pool")
		wMcp.waitForComplete()
		infraMcp.waitForComplete()
		logger.Infof("OK!\n")

		logger.Infof("Create first infra KubeletConfig")
		infraFirstKc := NewKubeletConfig(oc.AsAdmin(), infraFirstKcName, kcTemplate)
		defer infraFirstKc.Delete()
		infraFirstKc.create("KUBELETCONFIG="+infraFirstKubeletConfig, "POOL="+infraMcp.GetName())

		logger.Infof("Create second infra KubeletConfig")
		infraSecondKc := NewKubeletConfig(oc.AsAdmin(), infraSecondKcName, kcTemplate)
		defer infraSecondKc.Delete()
		infraSecondKc.create("KUBELETCONFIG="+infraSeconddKubeletConfig, "POOL="+infraMcp.GetName())
		logger.Infof("OK!\n")

		exutil.By("Wait for configurations to be applied in custom pool")
		infraMcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check kubelet configuration in worker pool")
		o.Expect(
			NewRemoteFile(wMcp.GetNodesOrFail()[0], kubeletConfPath).Read(),
		).To(
			HaveContent(o.Or(o.ContainSubstring(`"kubeAPIBurst": 7000`), o.ContainSubstring(`kubeAPIBurst: 7000`))),
		)
		logger.Infof("OK!\n")

		exutil.By("Check kubelet configuration in infra pool")
		o.Expect(
			NewRemoteFile(infraMcp.GetNodesOrFail()[0], kubeletConfPath).Read(),
		).To(o.And(
			HaveContent(o.Or(o.ContainSubstring(`"kubeAPIBurst": 9000`), o.ContainSubstring(`kubeAPIBurst: 9000`))),
			o.Not(HaveContent(o.Or(o.ContainSubstring(`"kubeAPIBurst": 8000`), o.ContainSubstring(`kubeAPIBurst: 8000`)))),
		))
		logger.Infof("OK!\n")
	})

	g.It("NonHyperShiftHOST-Author:rioliu-Critical-70090-apiserver-url.env file can be created on all cluster nodes [Serial]", func() {
		exutil.By("Check file apiserver-url.env on all linux nodes")
		apiserverURLEnvFile := "/etc/kubernetes/apiserver-url.env"
		allNodes, err := NewNodeList(oc.AsAdmin()).GetAllLinux()
		o.Expect(err).NotTo(o.HaveOccurred(), "Get all linux nodes failed")
		for _, node := range allNodes {
			if node.HasTaintEffectOrFail("NoExecute") {
				logger.Infof("Node %s is tainted with 'NoExecute'. Validation skipped.", node.GetName())
				continue
			}
			logger.Infof("Check apiserver-url.env file on node %s", node.GetName())
			rf := NewRemoteFile(node, apiserverURLEnvFile)
			o.Expect(rf.Exists()).Should(o.BeTrue(), "file %s not found on node %s", apiserverURLEnvFile, node.GetName())
			logger.Infof("OK\n")
		}
	})

	g.It("Author:rioliu-NonPreRelease-Longduration-High-70125-Test patch annotation way of updating a paused pool [Disruptive]", func() {

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

		defer mc.delete()
		// unpause the mcp first in defer logic, so nodes can be recovered automatically
		defer workerMcp.pause(false)
		mc.create()

		exutil.By("Patch desired MC annotation to trigger update")
		// get desired rendered mc from mcp.spec.configuration.name
		currentConfig, ccerr := workerMcp.getConfigNameOfStatus()
		o.Expect(ccerr).NotTo(o.HaveOccurred(), "Get current MC of worker pool failed")
		o.Eventually(workerMcp.getConfigNameOfSpec, "2m", "5s").ShouldNot(o.Equal(currentConfig))
		desireConfig, dcerr := workerMcp.getConfigNameOfSpec()
		o.Expect(dcerr).NotTo(o.HaveOccurred(), "Get desired MC of worker pool failed")
		o.Expect(desireConfig).NotTo(o.BeEmpty(), "Cannot get desired MC")
		logger.Infof("Desired MC is: %s\n", desireConfig)

		allWorkerNodes := NewNodeList(oc.AsAdmin()).GetAllLinuxWorkerNodesOrFail()
		o.Expect(allWorkerNodes).NotTo(o.BeEmpty(), "Cannot get any worker node from worker pool")
		workerNode := allWorkerNodes[0]
		logger.Infof("Start to patch annotation [machineconfiguration.openshift.io/desiredConfig] for worker node %s", workerNode.GetName())
		defer workerMcp.RecoverFromDegraded()
		workerNode.PatchDesiredConfig(desireConfig)
		// wait update to complete
		o.Eventually(workerNode.IsUpdating, "5m", "5s").Should(o.BeTrue(), "Node is not updating")
		o.Eventually(workerNode.IsUpdated, "10m", "10s").Should(o.BeTrue(), "Node is not updated")
		logger.Infof("Node %s is updated to desired MC %s", workerNode.GetName(), desireConfig)

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

	g.It("Author:sregidor-NonHyperShiftHOST-NonPreRelease-Longduration-Medium-71277-ImageTagMirrorSet. Skip image registry change disruption[Disruptive]", func() {
		var (
			itmsName                           = "tc-71277-tag-mirror-skip-drain"
			overrideDrainConfigMapTemplateName = "image-registry-override-drain-configmap.yaml"
			overrideDrainConfigMap             = NewConfigMap(oc.AsAdmin(), MachineConfigNamespace, "image-registry-override-drain")
			mcp                                = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
			node                               = mcp.GetNodesOrFail()[0]
		)

		// ImageTagMirrorSet is not compatible with ImageContentSourcePolicy.
		// If any ImageContentSourcePolicy exists we skip this test case.
		skipTestIfImageContentSourcePolicyExists(oc.AsAdmin())
		// If techpreview is enabled, then this behaviour is controlled by the new node disruption policy
		if exutil.IsTechPreviewNoUpgrade(oc) {
			g.Skip("featureSet: TechPreviewNoUpgrade is enabled. This test case cannot be executed with TechPreviewNoUpgrade because this behaviour is contrller by the new node disruption policy")
		}

		exutil.By("Start capturing events and clean pods logs")
		startTime, dErr := node.GetDate()
		o.Expect(dErr).ShouldNot(o.HaveOccurred(), "Error getting date in node %s", node.GetName())

		o.Expect(node.IgnoreEventsBeforeNow()).NotTo(o.HaveOccurred(),
			"Error getting the latest event in node %s", node.GetName())

		logger.Infof("Removing all MCD pods to clean the logs.")
		o.Expect(RemoveAllMCDPods(oc)).To(o.Succeed(), "Error removing all MCD pods in %s namespace", MachineConfigNamespace)
		logger.Infof("OK!\n")

		exutil.By("Create image-registry-override-drain configmap")
		defer overrideDrainConfigMap.Delete()
		o.Expect(
			NewMCOTemplate(oc.AsAdmin(), overrideDrainConfigMapTemplateName).Create(),
		).To(o.Succeed(),
			"Error creating the  image-registry-override-drain configmap to override the drain behavior")
		logger.Infof("OK!\n")

		exutil.By("Create new machine config to deploy a ImageTagMirrorSet configuring a mirror registry")
		itms := NewImageTagMirrorSet(oc.AsAdmin(), itmsName, *NewMCOTemplate(oc, "add-image-tag-mirror-set.yaml"))
		defer mcp.waitForComplete()
		defer itms.Delete()

		itms.Create("-p", "NAME="+itmsName)
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check logs to verify that a drain operation was skipped reporting the reason. No reboot happened. Crio was restarted")
		o.Expect(
			exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigDaemon, node.GetMachineConfigDaemon(), ""),
		).Should(o.And(
			o.ContainSubstring("Drain was skipped for this image registry update due to the configmap image-registry-override-drain being present. This may not be a safe change"),
			o.MatchRegexp(MCDCrioReloadedRegexp)),
			"The right actions could not be found in the logs. No drain should happen, no reboot should happen and crio should be restarted")
		logger.Infof("OK!\n")

		exutil.By("Verify that the node was NOT rebooted")
		o.Expect(node.GetUptime()).Should(o.BeTemporally("<", startTime),
			"The node %s must NOT be rebooted after applying the configuration, but it was rebooted. Uptime date happened after the start config time.", node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Check that no drain nor reboot events were triggered")
		o.Expect(node.GetEvents()).NotTo(o.Or(
			HaveEventsSequence("Drain"),
			HaveEventsSequence("Reboot")),
			"No Drain and no Reboot events should be triggered")
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

		exutil.By("Delete the ImageDigestMirrorSet resource")
		logger.Infof("Removing all MCD pods to clean the logs.")
		o.Expect(RemoveAllMCDPods(oc)).To(o.Succeed(), "Error removing all MCD pods in %s namespace", MachineConfigNamespace)
		itms.Delete()
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check that the configuration in file /etc/containers/registries.conf was restored")
		o.Expect(rf.Fetch()).To(o.Succeed(),
			"Error getting file /etc/containers/registries.conf")

		o.Expect(rf.GetTextContent()).NotTo(o.ContainSubstring(`example.io/digest-example/ubi-minimal`),
			"The configuration in file /etc/containers/registries.conf was not restored after deleting the ImageDigestMirrorSet resource")
		logger.Infof("OK!\n")

		exutil.By("Check logs to verify that, after deleting the ImageTagMirrorSet, a drain operation was skipped reporting the reason. No reboot happened. Crio was restarted")
		o.Expect(
			exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigDaemon, node.GetMachineConfigDaemon(), ""),
		).Should(o.And(
			o.ContainSubstring("Drain was skipped for this image registry update due to the configmap image-registry-override-drain being present. This may not be a safe change"),
			o.MatchRegexp(MCDCrioReloadedRegexp)),
			"The right actions could not be found in the logs. No drain should happen, no reboot should happen and crio should be restarted")
		logger.Infof("OK!\n")

		exutil.By("Verify that the node was NOT rebooted")
		o.Expect(node.GetUptime()).Should(o.BeTemporally("<", startTime),
			"The node %s must NOT be rebooted after applying the configuration, but it was rebooted. Uptime date happened after the start config time.", node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Check that no drain nor reboot events were triggered")
		o.Expect(node.GetEvents()).NotTo(o.Or(
			HaveEventsSequence("Drain"),
			HaveEventsSequence("Reboot")),
			"No Drain and no Reboot events should be triggered")
		logger.Infof("OK!\n")
	})

	g.It("Author:sregidor-NonHyperShiftHOST-NonPreRelease-Critical-72025-nmstate keeps service yamls[Disruptive]", func() {
		var (
			node                             = GetCompactCompatiblePool(oc.AsAdmin()).GetNodesOrFail()[0]
			nmstateConfigFileFullPath        = "/etc/nmstate/mco-tc-72025-basic-nmsconfig.yml"
			nmstateConfigFileAppliedFullPath = strings.ReplaceAll(nmstateConfigFileFullPath, ".yml", ".applied")

			nmstateConfigRemote        = NewRemoteFile(node, nmstateConfigFileFullPath)
			nmstateConfigAppliedRemote = NewRemoteFile(node, nmstateConfigFileAppliedFullPath)

			nmstateBasicConfig = `
desiredState:
  interfaces:
  - name: dummytc72025
    type: dummy
    state: absent
`
		)

		exutil.By(fmt.Sprintf("Create a config file for nmstate in node %s", node.GetName()))

		logger.Infof("Config content:\n%s", nmstateBasicConfig)
		defer func() {
			nmstateConfigRemote.Rm("-f")
			nmstateConfigAppliedRemote.Rm("-f")
			logger.Infof("Restarting nmstate service")
			_, err := node.DebugNodeWithChroot("systemctl", "restart", "nmstate")
			o.Expect(err).NotTo(o.HaveOccurred(), "Error restarting the nsmtate service in node %s", node.GetName())
		}()
		logger.Infof("Creating the config file")
		o.Expect(nmstateConfigRemote.Create([]byte(nmstateBasicConfig), 0o600)).To(o.Succeed(),
			"Error creating the basic config file %s in node %s", nmstateConfigFileFullPath, node.GetName())

		nmstateConfigRemote.PrintDebugInfo()
		logger.Infof("OK!\n")

		exutil.By(fmt.Sprintf("Restart nmstate service in node %s", node.GetName()))
		_, err := node.DebugNodeWithChroot("systemctl", "restart", "nmstate")
		o.Expect(err).NotTo(o.HaveOccurred(), "Error restarting the nsmtate service in node %s", node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Check that the configuration file was not removed and it was correctly cloned")
		o.Expect(nmstateConfigRemote.Exists()).To(o.BeTrue(),
			"The configuration file %s does not exist after restarting the nmstate service, but it should exist", nmstateConfigRemote.GetFullPath())

		o.Expect(nmstateConfigAppliedRemote.Exists()).To(o.BeTrue(),
			"The applied configuration file %s does not exist after restarting the nmstate service, but it should exist", nmstateConfigAppliedRemote.GetFullPath())
		logger.Infof("OK!\n")
	})

	g.It("Author:ptalgulk-NonHyperShiftHOST-NonPreRelease-Longduration-Medium-72008-recreate currentconfig missing on the filesystem [Disruptive]", func() {
		var (
			mcp               = GetCompactCompatiblePool(oc.AsAdmin())
			mcName            = "mco-tc-72008"
			node              = mcp.GetSortedNodesOrFail()[0]
			currentConfigFile = "/etc/machine-config-daemon/currentconfig"
			filePath          = "/etc/mco-test-case-72008"
			fileMode          = "420"
			fileContent       = "test"
			fileConfig        = getURLEncodedFileConfig(filePath, fileContent, fileMode)
		)

		exutil.By("Remove the file /etc/machine-config-daemon/currentconfig") // remove the currentconfig file
		rmCurrentConfig := NewRemoteFile(node, currentConfigFile)
		o.Expect(rmCurrentConfig.Rm()).To(o.Succeed(), "Not able to remove %s", rmCurrentConfig.GetFullPath())
		o.Expect(rmCurrentConfig).NotTo(Exist(), "%s removed but still exist", rmCurrentConfig)
		logger.Infof("OK \n")

		exutil.By("Create new Machine config") // new Machineconfig file
		mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName())
		mc.parameters = []string{fmt.Sprintf("FILES=[%s]", fileConfig)}
		defer mc.delete() // clean
		mc.create()
		logger.Infof("OK \n")

		exutil.By("Check that the file /etc/machine-config-daemon/currentconfig is recreated") // After update currentconfig exist
		o.Expect(rmCurrentConfig).To(Exist(), "%s Not exist", rmCurrentConfig)
		o.Expect(rmCurrentConfig.Read()).NotTo(HaveContent(o.BeEmpty()), "%s should not be empty file", rmCurrentConfig)
		logger.Infof("OK \n")

		exutil.By("Check Machine-config is applied") // machine-config is applied
		newFile := NewRemoteFile(node, filePath)
		o.Expect(newFile.Read()).To(o.And(HaveContent(fileContent), HaveOctalPermissions("0644")), "%s Does not have expected content or permissions", newFile)
		logger.Infof("OK\n")
	})

	g.It("Author:ptalgulk-NonHyperShiftHOST-NonPreRelease-High-72007-check node update frequencies", func() {

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
	g.It("Author:ptalgulk-NonHyperShiftHOST-NonPreRelease-Longduration-Medium-73148-prune renderedmachineconfigs [Disruptive]", func() {
		var (
			mcName                    = "fake-worker-pass-1"
			mcList                    = NewMachineConfigList(oc.AsAdmin())
			wMcp                      = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			mMcp                      = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
			NewSortedRenderedMCMaster []MachineConfig
			matchString               string
		)

		// create machine config
		exutil.By("Create a new MachineConfig")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		mc.parameters = []string{fmt.Sprintf(`PWDUSERS=[{"name":"%s", "passwordHash": "%s" }]`, "core", "fake-b")}
		defer mc.delete()
		mc.create()
		logger.Infof("OK!\n")

		wSpecConf, specErr := wMcp.getConfigNameOfSpec() // get worker MCP name
		o.Expect(specErr).NotTo(o.HaveOccurred())
		mSpecConf, specErr := mMcp.getConfigNameOfSpec() // get master MCP name
		o.Expect(specErr).NotTo(o.HaveOccurred())
		logger.Infof("%s %s \n", wSpecConf, mSpecConf)

		// sort mcList by time and get rendered machine config
		mcList.SortByTimestamp()
		sortedRenderedMCs := mcList.GetMCPRenderedMachineConfigsOrFail()
		logger.Infof(" %s", sortedRenderedMCs)

		sortedMCListMaster := mcList.GetRenderedMachineConfigForMasterOrFail() // to get master rendered machine config
		// 1 To check for `oc adm prune renderedmachineconfigs` cmd
		exutil.By("To run prune cmd to know which rendered machineconfigs would be deleted")
		pruneMCOutput, err := oc.AsAdmin().WithoutNamespace().Run("adm").Args("prune", "renderedmachineconfigs").Output()
		o.Expect(err).NotTo(o.HaveOccurred(), "Cannot get the rendered config for pool")
		logger.Infof(pruneMCOutput)

		for _, mc := range sortedRenderedMCs {
			matchString := "dry-run deleting rendered MachineConfig "
			if mc.GetName() == wSpecConf || mc.GetName() == mSpecConf {
				matchString = "Skip dry-run deleting rendered MachineConfig "
			}
			o.Expect(pruneMCOutput).To(o.ContainSubstring(matchString+mc.GetName()), "The %s is not same as in-use renderedMC in MCP", mc.GetName()) // to check correct rendered MC will be deleted or skipped

			o.Expect(mc.Exists()).To(o.BeTrue(), "The dry run deleted rendered MC is removed but should exist.")
		}
		logger.Infof("OK!\n")

		// 2 To check for `oc adm prune renderedmachineconfigs --count=1 --pool-name master` cmd
		exutil.By("To get the rendered machineconfigs based on count and MCP name")
		pruneMCOutput, err = oc.AsAdmin().WithoutNamespace().Run("adm").Args("prune", "renderedmachineconfigs", "--count=1", "--pool-name", "master").Output()
		o.Expect(err).NotTo(o.HaveOccurred(), "Cannot get the rendered config for pool")
		logger.Infof(pruneMCOutput)
		NewSortedRenderedMCMaster = mcList.GetRenderedMachineConfigForMasterOrFail()

		matchString = "dry-run deleting rendered MachineConfig "
		if sortedMCListMaster[0].GetName() == mSpecConf {
			matchString = "Skip dry-run deleting rendered MachineConfig "
		}
		o.Expect(pruneMCOutput).To(o.ContainSubstring(matchString+sortedMCListMaster[0].GetName()), "Oldest RenderedMachineConfig is not deleted") // to check old rendered  master MC will be getting deleted

		o.Expect(NewSortedRenderedMCMaster).To(o.Equal(sortedMCListMaster), "The dry run deleted rendered MC is removed but should exist.")
		logger.Infof("OK!\n")

		// 3 To check for 'oc adm prune renderedmachineconfigs list' cmd
		exutil.By("Get the rendered machineconfigs list")
		pruneMCOutput, err = oc.AsAdmin().WithoutNamespace().Run("adm").Args("prune", "renderedmachineconfigs", "list").Output()
		o.Expect(err).NotTo(o.HaveOccurred(), "Cannot get the rendered config list")
		logger.Infof(pruneMCOutput)
		o.Expect(pruneMCOutput).To(o.And(o.ContainSubstring(wSpecConf), o.ContainSubstring(mSpecConf)), "Error: Deleted in-use rendered machine configs")
		for _, mc := range sortedRenderedMCs {
			used := "Currently in use: false"
			if mc.GetName() == wSpecConf || mc.GetName() == mSpecConf {
				used = "Currently in use: true"
			}
			o.Expect(pruneMCOutput).To(o.MatchRegexp(regexp.QuoteMeta(mc.GetName()) + ".*-- .*" + regexp.QuoteMeta(used) + ".*")) // to check correct rendered MC is in-use or not
		}
		logger.Infof("OK!\n")

		// 4  To check for 'oc adm prune renderedmachineconfigs list --in-use --pool-name master' cmd
		exutil.By("To get the in use rendered machineconfigs  for each MCP")
		pruneMCOutput, err = oc.AsAdmin().WithoutNamespace().Run("adm").Args("prune", "renderedmachineconfigs", "list", "--in-use", "--pool-name", "master").Output()
		o.Expect(err).NotTo(o.HaveOccurred(), "Cannot get the rendered config list")
		logger.Infof("%s", mSpecConf)
		mStatusConf, err := mMcp.getConfigNameOfStatus()
		o.Expect(err).NotTo(o.HaveOccurred())
		logger.Infof("%s", mStatusConf)
		// to check renderedMC is same as `spec` and `status`
		o.Expect(pruneMCOutput).To(o.ContainSubstring("spec: "+mSpecConf), "Value for `spec` is not same as expected")
		o.Expect(pruneMCOutput).To(o.ContainSubstring("status: "+mStatusConf), "Value for `status` is not same as expected ")
		logger.Infof("%s", pruneMCOutput)
		logger.Infof("OK!\n")

		// 5 To check for `oc adm prune renderedmachineconfigs --count=1 --pool-name master --confirm` cmd
		exutil.By("To delete the  rendered machineconfigs based on count and MCP name")
		pruneMCOutput, err = oc.AsAdmin().WithoutNamespace().Run("adm").Args("prune", "renderedmachineconfigs", "--count=1", "--pool-name", "master", "--confirm").Output()
		o.Expect(err).NotTo(o.HaveOccurred(), "Cannot get the rendered config list")
		NewSortedRenderedMCMaster = mcList.GetRenderedMachineConfigForMasterOrFail()

		logger.Infof(pruneMCOutput)

		if sortedMCListMaster[0].GetName() == mSpecConf {
			matchString = "Skip deleting rendered MachineConfig "
		} else {
			matchString = "deleting rendered MachineConfig "
			for _, newMc := range NewSortedRenderedMCMaster {
				o.Expect(newMc.GetName()).NotTo(o.ContainSubstring(sortedMCListMaster[0].GetName()), "Deleted rendered MachineConfig is still present in the new list") // check expected rendered-master MC is been deleted
			}
		}
		o.Expect(pruneMCOutput).To(o.ContainSubstring(matchString+sortedMCListMaster[0].GetName()), "Oldest RenderedMachineConfig is not deleted") // check oldest rendered master MC is been deleted

		logger.Infof("OK!\n")

		// 6 To check for `oc adm prune renderedmachineconfigs --confirm` cmd
		sortedRenderedMCs = mcList.GetMCPRenderedMachineConfigsOrFail() // Get the current list of rendered machine configs
		exutil.By("To delete the  rendered machineconfigs based on count and MCP name")
		pruneMCOutput, err = oc.AsAdmin().WithoutNamespace().Run("adm").Args("prune", "renderedmachineconfigs", "--confirm").Output()
		o.Expect(err).NotTo(o.HaveOccurred(), "Cannot get the rendered config list")
		logger.Infof(pruneMCOutput)

		for _, mc := range sortedRenderedMCs {
			if mc.GetName() == mSpecConf || mc.GetName() == wSpecConf {
				matchString = "Skip deleting rendered MachineConfig "
				o.Expect(mc.Exists()).To(o.BeTrue(), "Deleted the in-use rendered MC") // check in-use rendered MC is not been deleted
			} else {
				matchString = "deleting rendered MachineConfig "
				o.Expect(mc.Exists()).To(o.BeFalse(), "The expected rendered MC is not deleted") // check expected rendered MC is been deleted
			}
			o.Expect(pruneMCOutput).To(o.ContainSubstring(matchString+mc.GetName()), "Oldest RenderedMachineConfig is not deleted")
		}

		logger.Infof("OK!\n")

	})

	g.It("Author:ptalgulk-NonHyperShiftHOST-NonPreRelease-Longduration-Medium-73155-prune renderedmachineconfigs in updating pools[Disruptive]", func() {
		var (
			wMcp        = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			mcList      = NewMachineConfigList(oc.AsAdmin())
			node        = wMcp.GetSortedNodesOrFail()[0]
			fileMode    = "420"
			fileContent = "test1"
			filePath    = "/etc/mco-test-case-73155-"
			mcName      = "mco-tc-73155-"
		)

		mcList.SortByTimestamp()                         // sort by time
		wSpecConf, specErr := wMcp.getConfigNameOfSpec() // get worker MCP name
		o.Expect(specErr).NotTo(o.HaveOccurred())

		exutil.By("Create new Machine config")
		mc := NewMachineConfig(oc.AsAdmin(), mcName+"1", MachineConfigPoolWorker)
		fileConfig := getBase64EncodedFileConfig(filePath+"1", fileContent, fileMode)
		mc.parameters = []string{fmt.Sprintf("FILES=[%s]", fileConfig)}
		mc.skipWaitForMcp = true // to wait to execute command

		defer func() {
			exutil.By("Check Machine Config are deleted")
			o.Expect(NewRemoteFile(node, filePath+"1")).NotTo(Exist(),
				"The file %s should NOT exists", filePath+"1")
			o.Expect(NewRemoteFile(node, filePath+"2")).NotTo(Exist(),
				"The file %s should NOT exists", filePath+"2")

			exutil.By("Check the MCP status is not been degreaded")
			wMcp.waitForComplete()
		}()

		defer mc.delete() // Clean up after creation
		mc.create()

		logger.Infof("OK\n")

		exutil.By("Wait for first nodes to be configured")

		o.Eventually(node.IsUpdating, "10m", "20s").Should(o.BeTrue())
		o.Eventually(node.IsUpdated, "10m", "20s").Should(o.BeTrue()) // check for first node is updated

		initialRenderedMC, specErr := wMcp.getConfigNameOfSpec() // check for new worker rendered MC configured
		o.Expect(specErr).NotTo(o.HaveOccurred())
		logger.Infof("OK\n")

		exutil.By("Create new second Machine configs")

		fileConfig = getBase64EncodedFileConfig(filePath+"2", fileContent, fileMode)
		mc = NewMachineConfig(oc.AsAdmin(), mcName+"2", MachineConfigPoolWorker)
		mc.parameters = []string{fmt.Sprintf("FILES=[%s]", fileConfig)}
		mc.skipWaitForMcp = true // to wait to execute command

		defer mc.deleteNoWait() // Clean up after creation
		mc.create()
		logger.Infof("OK\n")

		exutil.By("Run prune command and check new rendered MC is generated with MCP is still updating")

		o.Eventually(wMcp.getConfigNameOfSpec, "5m", "20s").ShouldNot(o.Equal(initialRenderedMC), "Second worker renderedMC is not configured yet")
		newRenderedMC, specErr := wMcp.getConfigNameOfSpec()
		o.Expect(specErr).NotTo(o.HaveOccurred(), "Get desired MC of worker pool failed")

		pruneMCOutput, err := oc.AsAdmin().WithoutNamespace().Run("adm").Args("prune", "renderedmachineconfigs", "--pool-name", "worker", "--confirm").Output()
		o.Expect(err).NotTo(o.HaveOccurred(), "Cannot get the rendered config list")
		logger.Infof(pruneMCOutput)

		renderedMCs := []string{wSpecConf, initialRenderedMC, newRenderedMC}
		// as  wMCP is still updating with previous in-use  rendered MC and new generated MC from 1st and 2nd MC created are also in-use so we need to check they are not deleted

		for _, mc := range renderedMCs {
			o.Expect(pruneMCOutput).To(o.ContainSubstring("Skip deleting rendered MachineConfig "+mc), "Deleted the in-use rendered MC: "+mc)
		}
		logger.Infof("OK\n")

		exutil.By("Check no worker MCP is degreaded")
		wMcp.waitForComplete()

		exutil.By("Execute the prune command again after complete update")
		pruneMCOutput, err = oc.AsAdmin().WithoutNamespace().Run("adm").Args("prune", "renderedmachineconfigs", "--pool-name", "worker", "--confirm").Output()
		o.Expect(err).NotTo(o.HaveOccurred(), "Cannot get the rendered config list")
		logger.Infof(pruneMCOutput)
		o.Expect(pruneMCOutput).To(o.ContainSubstring("Skip deleting rendered MachineConfig "+newRenderedMC), "Deleted the in-use rendered MC")

		logger.Infof("OK\n")

	})

	g.It("Author:ptalgulk-NonHyperShiftHOST-NonPreRelease-Low-74606-'oc adm prune' report failures consistently when using wrong pool name", func() {
		var expectedErrorMsg = "error: MachineConfigPool with name 'fake' not found"

		out, err := oc.AsAdmin().Run("adm").Args("prune", "renderedmachineconfigs", "list", "--pool-name", "fake").Output()
		o.Expect(err).To(o.HaveOccurred(), "Expected oc command error to fail but it didn't")
		o.Expect(err).To(o.BeAssignableToTypeOf(&exutil.ExitError{}), "Unexpected error while executing prune command")
		o.Expect(err.(*exutil.ExitError).ExitCode()).ShouldNot(o.Equal(0), "Unexpected return code when executing the prune command with a wrong pool name")
		o.Expect(out).To(o.Equal(expectedErrorMsg), "Unexecpted error message when using wrong pool name in the prune command")

		out, err = oc.AsAdmin().Run("adm").Args("prune", "renderedmachineconfigs", "list", "--in-use", "--pool-name", "fake").Output()
		o.Expect(err).To(o.HaveOccurred(), "Expected oc command error to fail but it didn't")
		o.Expect(err).To(o.BeAssignableToTypeOf(&exutil.ExitError{}), "Unexpected error while executing prune command with in-use flag")
		o.Expect(err.(*exutil.ExitError).ExitCode()).ShouldNot(o.Equal(0), "Unexpected return code when executing the prune command with the in-use flag and a wrong pool name")
		o.Expect(out).To(o.Equal(expectedErrorMsg), "Unexecpted error message when using in-use flag and a wrong pool name in the prune command")

	})

	// We deprecate this test case because of https://issues.redhat.com/browse/OCPBUGS-36739
	// Once this issue is fixed we will be able to set this test case as not deprecated again
	// Before removing the deprecation label we need to make sure that we add a check to skip this test case if the cluster is IPV6, since it makes no sense to disable ipv6 in a cluster deployed to use ipv6
	g.It("Author:sregidor-DEPRECATED-NonHyperShiftHOST-NonPreRelease-Longduration-Medium-73309-disable ipv6 on worker nodes[Disruptive]", func() {

		var (
			wMcp      = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			mcName    = "mco-tc-73309-disable-ipv6"
			kernelArg = "ipv6.disable=1"

			behaviourValidatorApply = UpdateBehaviourValidator{
				Checkers: []Checker{
					CommandOutputChecker{
						Command:  []string{"cat", "/proc/cmdline"},
						Matcher:  o.ContainSubstring(kernelArg),
						ErrorMsg: fmt.Sprintf("The kernel argument to disable ipv6 %s was not properly applied", kernelArg),
						Desc:     fmt.Sprintf("Check that the kernel argument to disable ipv6 %s was properly applied", kernelArg),
					},
				},
			}
			behaviourValidatorRemove = UpdateBehaviourValidator{
				Checkers: []Checker{
					CommandOutputChecker{
						Command:  []string{"cat", "/proc/cmdline"},
						Matcher:  o.Not(o.ContainSubstring(kernelArg)),
						ErrorMsg: fmt.Sprintf("The kernel argument to disable ipv6 %s was not properly removed", kernelArg),
						Desc:     fmt.Sprintf("Check that the kernel argument to disable ipv6 %s was properly removed", kernelArg),
					},
				},
			}
		)

		if IsCompactOrSNOCluster(oc.AsAdmin()) {
			g.Skip("This test case can only be executed in clusters with worker pool. Disable IPV6 is not supported in master nodes")
		}

		behaviourValidatorApply.Initialize(wMcp, nil)

		exutil.By("Create a MC to disable ipv6 in worker pool")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		mc.parameters = []string{fmt.Sprintf(`KERNEL_ARGS=["%s"]`, kernelArg)}
		mc.skipWaitForMcp = true

		defer mc.delete()
		mc.create()
		logger.Infof("OK!\n")

		// Check that the MC is applied according to the expected behaviour
		behaviourValidatorApply.Validate()

		behaviourValidatorRemove.Initialize(wMcp, nil)

		exutil.By("Delete the MC created to disable ipv6 in worker pool")
		mc.deleteNoWait()
		logger.Infof("OK!\n")

		behaviourValidatorRemove.Validate()
	})

	g.It("Author:ptalgulk-NonHyperShiftHOST-NonPreRelease-Longduration-High-74540-kubelet does not start after reboot due to dependency issue[Disruptive]", func() {
		var (
			unitEnabled    = true
			unitName       = "hello.service"
			filePath       = "/etc/systemd/system/default.target.wants/hello.service"
			fileContent    = "[Unit]\nDescription=A hello world unit\nAfter=network-online.target\nRequires=network-online.target\n[Service]\nType=oneshot\nRemainAfterExit=yes\nExecStart=/usr/bin/echo Hello, World\n[Install]\nWantedBy="
			unitConfig     = getSingleUnitConfig(unitName, unitEnabled, fileContent+"default.target")
			mcName         = "tc-74540"
			mcp            = GetCompactCompatiblePool(oc.AsAdmin())
			node           = mcp.GetSortedNodesOrFail()[0]
			activeString   = "Active: active"
			inactiveString = "Active: inactive"
		)

		exutil.By("Create a MC to deploy a unit.")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		mc.parameters = []string{fmt.Sprintf("UNITS=[%s]", unitConfig)}
		defer mc.delete()
		mc.create()
		logger.Infof("OK \n")

		exutil.By("Wait until worker MCP has finished the configuration. No machine should be degraded.")
		mcp.waitForComplete()
		logger.Infof("OK \n")

		exutil.By("Verfiy file content is properly applied")
		rf := NewRemoteFile(node, filePath)
		o.Expect(rf.Read()).To(HaveContent(fileContent + "default.target"))
		logger.Infof("OK \n")

		exutil.By("Validate that the hello unit service is active")
		o.Expect(
			node.DebugNodeWithChroot("systemctl", "status", unitName),
		).To(o.And(o.ContainSubstring(activeString), o.Not(o.ContainSubstring(inactiveString))),
			"%s unit is not active", unitName)
		logger.Infof("OK \n")

		exutil.By("Update the unit of MC")

		o.Expect(
			mc.Patch("json", fmt.Sprintf(`[{ "op": "replace", "path": "/spec/config/systemd/units/0/contents", "value": %s}]`, jsonEncode(fileContent+"multi-user.target"))),
		).To(o.Succeed(), "Error patching %s with the new WantedBy value", fileContent+"multi-user.target")

		logger.Infof("OK \n")

		exutil.By("Wait until worker MCP has finished the configuration. No machine should be degraded.")
		mcp.waitForComplete()
		logger.Infof("OK \n")

		exutil.By("To verify that previous file does not exist")
		o.Expect(rf).NotTo(Exist())
		logger.Infof("OK \n")

		exutil.By("To verify that new file does exist")
		filePath = "/etc/systemd/system/multi-user.target.wants/hello.service"
		rf = NewRemoteFile(node, filePath)
		o.Expect(rf.Read()).To(HaveContent(fileContent + "multi-user.target"))
		logger.Infof("OK \n")

		exutil.By("Validate that the hello unit service is active after update")
		o.Expect(
			node.DebugNodeWithChroot("systemctl", "status", unitName),
		).To(o.And(o.ContainSubstring(activeString), o.Not(o.ContainSubstring(inactiveString))),
			"%s unit is not active", unitName)
		logger.Infof("OK \n")
	})

	g.It("Author:sregidor-NonHyperShiftHOST-NonPreRelease-Critical-74608-Env file /etc/kubernetes/node.env should not be overwritten after a node restart [Disruptive]", func() {
		// /etc/kubernetes/node.env only exists in AWS
		skipTestIfSupportedPlatformNotMatched(oc, AWSPlatform)

		var (
			node      = GetCompactCompatiblePool(oc.AsAdmin()).GetSortedNodesOrFail()[0]
			rEnvFile  = NewRemoteFile(node, "/etc/kubernetes/node.env")
			extraLine = "\nDUMMYKEY=DUMMYVAL"
		)
		exutil.By("Get current node.env content")
		o.Expect(rEnvFile.Fetch()).To(o.Succeed(),
			"Error getting information about %s", rEnvFile)
		initialContent := rEnvFile.GetTextContent()
		initialUser := rEnvFile.GetUIDName()
		initialGroup := rEnvFile.GetGIDName()
		initialPermissions := rEnvFile.GetOctalPermissions()
		logger.Infof("Initial content: %s", initialContent)
		logger.Infof("OK!\n")

		exutil.By("Modify the content of the node.env file")
		defer rEnvFile.PushNewTextContent(initialContent)
		newContent := initialContent + extraLine
		logger.Infof("New content: %s", newContent)
		o.Expect(rEnvFile.PushNewTextContent(newContent)).To(o.Succeed(),
			"Error writing new content in %s", rEnvFile)
		logger.Infof("OK!\n")

		exutil.By("Reboot node")
		o.Expect(node.Reboot()).To(o.Succeed(),
			"Error rebooting %s", node)
		logger.Infof("OK!\n")

		exutil.By("Check that the content was not changed after the node reboot")
		o.Eventually(rEnvFile.Read, "15m", "20s").Should(o.And(
			HaveContent(newContent),
			HaveOwner(initialUser),
			HaveGroup(initialGroup),
			HaveOctalPermissions(initialPermissions)),
			"The information in %s is not the expected one", rEnvFile)
		logger.Infof("OK!\n")

		exutil.By("Restore initial content")
		o.Eventually(rEnvFile.PushNewTextContent, "5m", "20s").WithArguments(initialContent).Should(o.Succeed(),
			"Error writing the initial content in %s", rEnvFile)
		o.Expect(node.Reboot()).To(o.Succeed(),
			"Error rebooting %s", node)
		o.Eventually(rEnvFile.Read, "15m", "20s").Should(o.And(
			HaveContent(initialContent),
			HaveOwner(initialUser),
			HaveGroup(initialGroup),
			HaveOctalPermissions(initialPermissions)),
			"The inforamtion of %s is not the expected one after restoring the initial content", rEnvFile)
		logger.Infof("OK!\n")
	})

	g.It("Author:sregidor-NonHyperShiftHOST-NonPreRelease-Critical-75258-No ordering cycle issues should exist [Disruptive]", func() {
		exutil.By("Check that there are no ordering cycle problems in the nodes")
		for _, node := range exutil.OrFail[[]Node](NewNodeList(oc.AsAdmin()).GetAllLinux()) {
			if node.HasTaintEffectOrFail("NoExecute") {
				logger.Infof("Skipping node %s since it is tainted with NoExecute and no debug pod can be run in it", node.GetName())
				continue
			}
			logger.Infof("Checking node %s", node.GetName())
			// For debugging purposes. We ignore the error here
			logMsg, _ := node.DebugNodeWithChroot("sh", "-c", `journalctl -o with-unit | grep "Found ordering cycle" -A 10 || true`)
			logger.Infof("Orderging cycle messages: %s", logMsg)

			o.Expect(node.DebugNodeWithChroot(`journalctl`, `-o`, `with-unit`)).NotTo(o.ContainSubstring("Found ordering cycle"),
				"Ordering cycle problems found in node %s", node.GetName())
		}
		logger.Infof("OK!\n")
	})

	g.It("Author:sregidor-NonHyperShiftHOST-Longduration-NonPreRelease-Medium-75149-Update pool with manually cordoned nodes [Disruptive]", func() {
		SkipIfSNO(oc.AsAdmin())
		var (
			mcp    = GetCompactCompatiblePool(oc.AsAdmin())
			mcName = "mco-test-75149"
			// to make the test execution faster we will use a password configuration for the automation
			passwordHash = "fake-hash"
			user         = "core"
			nodeList     = NewNodeList(oc.AsAdmin())
		)
		if len(mcp.GetNodesOrFail()) < 3 {
			logger.Infof("There are less than 3 nodes available in the worker node. Since we need at least 3 nodes we use the master pool for testing")
			mcp = NewMachineConfigPool(mcp.GetOC(), MachineConfigPoolMaster)
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
		mc.parameters = []string{fmt.Sprintf(`PWDUSERS=[{"name":"%s", "passwordHash": "%s" }]`, user, passwordHash)}
		mc.skipWaitForMcp = true

		defer mc.delete()
		defer cordonedNode.Uncordon()
		mc.create()
		logger.Infof("OK!\n")

		exutil.By("Check that only one node is updated at a time (instead of 2) because the manually cordoned node counts as unavailable")
		// get all nodes with status != Done
		nodeList.SetItemsFilter(`?(@.metadata.annotations.machineconfiguration\.openshift\.io/state!="Done")`)
		o.Consistently(func() (int, error) {
			nodes, err := nodeList.GetAll()
			return len(nodes), err
		}, "3m", "10s").Should(o.BeNumerically("<", 2),
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

	g.It("Author:sregidor-NonHyperShiftHOST-Longduration-NonPreRelease-Critical-76108-MachineConfig inheritance. Canary rollout update [Disruptive]", func() {
		SkipIfCompactOrSNO(oc) // We can't create custom pools if only the master pool exists

		var (
			customMCPName = "worker-perf"
			canaryMCPName = "worker-perf-canary"
			mcName        = "06-kdump-enable-worker-perf-tc-76108"
			mcUnit        = `{"enabled": true, "name": "kdump.service"}`
			mcKernelArgs  = "crashkernel=512M"
		)

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
		mc := NewMachineConfig(oc.AsAdmin(), mcName, customMCPName)
		defer mc.delete()

		err = mc.Create("-p", "NAME="+mcName, "-p", "POOL="+customMCPName, "-p", fmt.Sprintf("UNITS=[%s]", mcUnit), fmt.Sprintf(`KERNEL_ARGS=["%s"]`, mcKernelArgs))
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating MachineConfig %s", mc.GetName())

		customMcp.waitForComplete()

		exutil.By("Check that the configuration was applied the nodes")
		canaryNode := customMcp.GetNodesOrFail()[0]
		o.Expect(canaryNode.IsKernelArgEnabled(mcKernelArgs)).Should(o.BeTrue(), "Kernel argument %s is not set in node %s", mcKernelArgs, canaryNode)
		logger.Infof("OK!\n")

		exutil.By("Move one node from the custom pool to the canary custom pool")
		startTime := canaryNode.GetDateOrFail()
		defer canaryNode.RemoveLabel("node-role.kubernetes.io/" + canaryMCPName)
		o.Expect(
			canaryNode.AddLabel("node-role.kubernetes.io/"+canaryMCPName, ""),
		).To(o.Succeed(), "Error labeling node %s", canaryNode)
		o.Expect(
			canaryNode.RemoveLabel("node-role.kubernetes.io/"+customMCPName),
		).To(o.Succeed(), "Error removing label from node %s", canaryNode)

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

	g.It("Author:ptalgulk-NonHyperShiftHOST-NonPreRelease-Medium-68683-node-logs feature works fine [Disruptive]", func() {
		var (
			wMcp       = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			workerNode = wMcp.GetNodesOrFail()[0]
			mMcp       = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
			masterNode = mMcp.GetNodesOrFail()[0]
		)
		verifyCmd := func(node Node) {
			exutil.By(fmt.Sprintf("Check that the node-logs cmd work for %s node", node.name))
			nodeLogs, err := oc.AsAdmin().WithoutNamespace().Run("adm").Args("node-logs", node.name, "--tail=20").Output()
			o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("Cannot get the node-logs cmd for %s node", node.name))
			o.Expect(len(strings.Split(nodeLogs, "\n"))).To(o.BeNumerically(">=", 5)) // check the logs line are greater than 5
		}
		verifyCmd(workerNode)
		verifyCmd(masterNode)

	})
})

// validate that the machine config 'mc' degrades machineconfigpool 'mcp', due to NodeDegraded error matching expectedNDMessage, expectedNDReason
func validateMcpNodeDegraded(mc *MachineConfig, mcp *MachineConfigPool, expectedNDMessage, expectedNDReason string, checkCODegraded bool) {
	defer func() {
		o.Expect(mcp.RecoverFromDegraded()).To(o.Succeed(), "The MCP could not be recovered from Degraded status")
	}()
	defer o.Eventually(mc.deleteNoWait).Should(o.Succeed(), "Could not delete the offending MC")
	mc.create()
	logger.Infof("OK!\n")

	checkDegraded(mcp, expectedNDMessage, expectedNDReason, "NodeDegraded", checkCODegraded, 2)
}

// validate that the machine config 'mc' degrades machineconfigpool 'mcp', due to RenderDegraded error matching expectedNDMessage, expectedNDReason
func validateMcpRenderDegraded(mc *MachineConfig, mcp *MachineConfigPool, expectedRDMessage, expectedRDReason string) {
	defer o.Eventually(mcp, "5m", "20s").ShouldNot(BeDegraded(), "The MCP was not recovered from Degraded status after deleting the offending MC")
	defer o.Eventually(mc.deleteNoWait).Should(o.Succeed(), "Could not delete the offending MC")
	mc.create()
	logger.Infof("OK!\n")

	checkDegraded(mcp, expectedRDMessage, expectedRDReason, "RenderDegraded", false, 2)
}

func checkDegraded(mcp *MachineConfigPool, expectedMessage, expectedReason, degradedConditionType string, checkCODegraded bool, offset int) {
	oc := mcp.oc
	expectedNumDegradedMachines := 0
	if degradedConditionType == "NodeDegraded" {
		expectedNumDegradedMachines = 1
	}

	exutil.By("Wait until MCP becomes degraded")
	o.EventuallyWithOffset(offset, mcp, mcp.estimateWaitDuration().String(), "30s").Should(BeDegraded(),
		"The '%s' MCP should become degraded when we try to create an invalid MC, but it didn't.", mcp.GetName())
	o.EventuallyWithOffset(offset, mcp.getDegradedMachineCount, "5m", "30s").Should(o.Equal(expectedNumDegradedMachines),
		"The '%s' MCP should report '%d' degraded machine count, but it doesn't.", expectedNumDegradedMachines, mcp.GetName())

	logger.Infof("OK!\n")

	exutil.By("Validate the reported error")
	degradedCondition := mcp.GetConditionByType(degradedConditionType)

	o.ExpectWithOffset(offset, mcp).Should(HaveConditionField(degradedConditionType, "status", o.Equal("True")),
		"'worker' MCP should report degraded status in the %s condition: %s", degradedConditionType, degradedCondition)

	o.ExpectWithOffset(offset, mcp).Should(HaveConditionField(degradedConditionType, "message", o.MatchRegexp(expectedMessage)),
		"'worker' MCP is not reporting the expected message in the %s condition: %s", degradedConditionType, degradedCondition)

	o.ExpectWithOffset(offset, mcp).Should(HaveConditionField(degradedConditionType, "reason", o.MatchRegexp(expectedReason)),
		"'worker' MCP is not reporting the expected reason in the NodeDegraded condition: %s", degradedConditionType, degradedCondition)
	logger.Infof("OK!\n")

	exutil.By("Get co machine config to verify status and reason for Upgradeable type")

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

func createMcAndVerifyMCValue(oc *exutil.CLI, stepText, mcName string, node Node, textToVerify TextToVerify, cmd ...string) {
	exutil.By(fmt.Sprintf("Create new MC to add the %s", stepText))
	mcTemplate := mcName + ".yaml"
	mc := NewMachineConfig(oc.AsAdmin(), mcName, node.GetPrimaryPoolOrFail().GetName()).SetMCOTemplate(mcTemplate)
	defer mc.delete()
	// TODO: When we extract the "mcp.waitForComplete" from the "create" method, we need to take into account that if
	// we are configuring a rt-kernel we need to wait longer. Same for extensions, we need to wait longer if an extension is configured.
	mc.create()
	logger.Infof("Machine config is created successfully!")

	exutil.By(fmt.Sprintf("Check %s in the created machine config", stepText))
	mcOut, err := getMachineConfigDetails(oc, mc.name)
	o.Expect(err).NotTo(o.HaveOccurred())
	o.Expect(mcOut).Should(o.MatchRegexp(textToVerify.textToVerifyForMC))
	logger.Infof("%s is verified in the created machine config!", stepText)

	exutil.By(fmt.Sprintf("Check %s in the machine config daemon", stepText))
	var podOut string
	if textToVerify.needBash { // nolint:all
		podOut, err = exutil.RemoteShPodWithBash(oc, MachineConfigNamespace, node.GetMachineConfigDaemon(), cmd...)
	} else if textToVerify.needChroot {
		podOut, err = exutil.RemoteShPodWithChroot(oc, MachineConfigNamespace, node.GetMachineConfigDaemon(), cmd...)
	} else {
		podOut, err = exutil.RemoteShPod(oc, MachineConfigNamespace, node.GetMachineConfigDaemon(), cmd...)
	}
	o.Expect(err).NotTo(o.HaveOccurred())
	o.Expect(podOut).Should(o.MatchRegexp(textToVerify.textToVerifyForNode))
	logger.Infof("%s is verified in the machine config daemon!", stepText)
}

// skipTestIfRHELVersion skips the test case if the provided RHEL version matches the constraints.
func skipTestIfRHELVersion(node Node, operator, constraintVersion string) {
	rhelVersion, err := node.GetRHELVersion()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting RHEL version from node %s", node.GetName())

	if CompareVersions(rhelVersion, operator, constraintVersion) {
		g.Skip(fmt.Sprintf("Test case skipped because current RHEL version %s %s %s",
			rhelVersion, operator, constraintVersion))
	}
}

// skipTestIfClusterVersion skips the test case if the provided version matches the constraints.
func skipTestIfClusterVersion(oc *exutil.CLI, operator, constraintVersion string) {
	clusterVersion, _, err := exutil.GetClusterVersion(oc)
	o.Expect(err).NotTo(o.HaveOccurred())

	if CompareVersions(clusterVersion, operator, constraintVersion) {
		g.Skip(fmt.Sprintf("Test case skipped because current cluster version %s %s %s",
			clusterVersion, operator, constraintVersion))
	}
}

// skipTestIfOsIsNotCoreOs it will either skip the test case in case of worker node is not CoreOS or will return the CoreOS worker node
func skipTestIfOsIsNotCoreOs(oc *exutil.CLI) Node {
	allCoreOs := NewNodeList(oc).GetAllCoreOsWokerNodesOrFail()
	if len(allCoreOs) == 0 {
		g.Skip("CoreOs is required to execute this test case!")
	}
	return allCoreOs[0]
}

// skipTestIfOsIsNotCoreOs it will either skip the test case in case of worker node is not CoreOS or will return the CoreOS worker node
func skipTestIfOsIsNotRhelOs(oc *exutil.CLI) Node {
	allRhelOs := NewNodeList(oc).GetAllRhelWokerNodesOrFail()
	if len(allRhelOs) == 0 {
		g.Skip("RhelOs is required to execute this test case!")
	}
	return allRhelOs[0]
}

// skipTestIfFIPSIsNotEnabled skip the test if fips is not enabled
func skipTestIfFIPSIsNotEnabled(oc *exutil.CLI) {
	if !isFIPSEnabledInClusterConfig(oc) {
		g.Skip("fips is not enabled, skip this test")
	}
}

// skipTestIfFIPSIstEnabled skip the test if fips is not enabled
func skipTestIfFIPSIsEnabled(oc *exutil.CLI) {
	if isFIPSEnabledInClusterConfig(oc) {
		g.Skip("fips is enabled, skip this test")
	}
}

// skipTestIfImageContentSourcePolicyExists skip the test if any ImageContentSourcePolicy exists in the cluster
func skipTestIfImageContentSourcePolicyExists(oc *exutil.CLI) {
	output, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("ImageContentSourcePolicy").Output()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error checking if ImageContentSourcePolicy exist in the cluster or not")

	if output != "No resources found" {
		logger.Infof("ImageContentSourcePolicy in cluster:\n%s", output)
		g.Skip("There are ImageContentSourcePolicies resources in the cluster. This test case is not compatible with ImageContentSourcePolicies resources.")
	}
}

func createMcAndVerifyIgnitionVersion(oc *exutil.CLI, stepText, mcName, ignitionVersion string) {
	var (
		mcTemplate        = "change-worker-ign-version.yaml"
		mcp               = GetCompactCompatiblePool(oc.AsAdmin())
		expectedRDMessage = `parsing Ignition config failed: (unknown|invalid) version\. Supported spec versions: 2\.2, 3\.0, 3\.1, 3\.2, 3\.3, 3\.4$`
		expectedRDReason  = "^$" // empty string
	)
	exutil.By(fmt.Sprintf("Create machine config with %s", stepText))

	mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName()).SetMCOTemplate(mcTemplate)
	mc.parameters = []string{"IGNITION_VERSION=" + ignitionVersion}
	mc.skipWaitForMcp = true

	validateMcpRenderDegraded(mc, mcp, expectedRDMessage, expectedRDReason)
}

// verifyRenderedMcs verifies that the resources provided in the parameter "allRes" have created a
// a new MachineConfig owned by those resources
func verifyRenderedMcs(oc *exutil.CLI, renderSuffix string, allRes []ResourceInterface) []Resource {
	// TODO: Use MachineConfigList when MC code is refactored
	allMcs, err := NewResourceList(oc.AsAdmin(), "mc").GetAll()
	o.Expect(err).NotTo(o.HaveOccurred())
	o.Expect(allMcs).NotTo(o.BeEmpty())

	// cache all MCs owners to avoid too many oc binary executions while searching
	mcOwners := make(map[Resource]*JSONData, len(allMcs))
	for _, mc := range allMcs {
		owners := JSON(mc.GetOrFail(`{.metadata.ownerReferences}`))
		mcOwners[mc] = owners
	}

	// Every resource should own one MC
	for _, res := range allRes {
		var ownedMc *Resource
		for mc, owners := range mcOwners {
			if owners.Exists() {
				for _, owner := range owners.Items() {
					if !(strings.EqualFold(owner.Get("kind").ToString(), res.GetKind()) && strings.EqualFold(owner.Get("name").ToString(), res.GetName())) {
						continue
					}
					logger.Infof("Resource '%s' '%s' owns MC '%s'", res.GetKind(), res.GetName(), mc.GetName())
					// Each resource can only own one MC
					o.Expect(ownedMc).To(o.BeNil(), "Resource %s owns more than 1 MC: %s and %s", res.GetName(), mc.GetName(), ownedMc)
					// we need to do this to avoid the loop variable to override our value
					key := mc
					ownedMc = &key
					break
				}
			} else {
				logger.Infof("MC '%s' has no owner.", mc.name)
			}

		}
		o.Expect(ownedMc).NotTo(o.BeNil(), fmt.Sprintf("Resource '%s' '%s' should have generated a MC but it has not. It owns no MC.", res.GetKind(), res.GetName()))
		o.Expect(ownedMc.name).To(o.ContainSubstring(renderSuffix), "Mc '%s' is owned by '%s' '%s' but its name does not contain the expected substring '%s'",
			ownedMc.GetName(), res.GetKind(), res.GetName(), renderSuffix)
	}

	return allMcs
}

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
	o.Eventually(mcp.pollUpdatedStatus(), "10m", "30s").Should(o.Equal("True"), "The worker MCP should report a True Updated status")
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

// checkUpdatedLists Compares that 2 lists are ordered in steps.
// when we update nodes with maxUnavailable>1, since we are polling, we cannot make sure
// that the sorted lists have the same order one by one. We can only make sure that the steps
// defined by maxUnavailable have the right order.
// If step=1, it is the same as comparing that both lists are equal.
func checkUpdatedLists(l, r []Node, step int) bool {
	if len(l) != len(r) {
		logger.Errorf("Compared lists have different size")
		return false
	}

	indexStart := 0
	for i := 0; i < len(l); i += step {
		indexEnd := i + step
		if (i + step) > (len(l)) {
			indexEnd = len(l)
		}

		// Create 2 sublists with the size of the step
		stepL := l[indexStart:indexEnd]
		stepR := r[indexStart:indexEnd]
		indexStart += step

		// All elements in one sublist should exist in the other one
		// but they dont have to be in the same order.
		for _, nl := range stepL {
			found := false
			for _, nr := range stepR {
				if nl.GetName() == nr.GetName() {
					found = true
					break
				}

			}
			if !found {
				logger.Errorf("Nodes were not updated in the right order. Comparing steps %s and %s\n", stepL, stepR)
				return false
			}
		}

	}
	return true

}

// checkMirrorRemovalDefaultEvents checks that after a mirror configuaration is removed the node is drained, reboot is skipped and crio service is restarted
func checkMirrorRemovalDefaultEvents(node Node, removeTime time.Time) {
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
