package extended

import (
	"fmt"
	"regexp"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	"github.com/openshift/machine-config-operator/test/extended-priv/util/architecture"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO kernel", func() {
	defer g.GinkgoRecover()

	var oc = exutil.NewCLI("mco-kernel", exutil.KubeConfigPath())

	g.JustBeforeEach(func() {
		PreChecks(oc)
	})

	g.It("[PolarionID:42365][OTP] add real time kernel argument [Disruptive]", g.Label("Platform:aws", "Platform:gce"), func() {
		architecture.SkipNonAmd64SingleArch(oc)
		skipTestIfSupportedPlatformNotMatched(oc, AWSPlatform, GCPPlatform)
		mcp := GetCompactCompatiblePool(oc.AsAdmin())

		node := mcp.GetSortedNodesOrFail()[0]
		textToVerify := TextToVerify{
			textToVerifyForMC:   "realtime",
			textToVerifyForNode: "PREEMPT_RT",
			needBash:            true,
		}

		createMcAndVerifyMCValue(oc, "realtime kernel", "set-realtime-kernel", node, textToVerify, "uname -a")
	})

	g.It("[PolarionID:67787][OTP] switch kernel type to 64k-pages for clusters with arm64 nodes [Disruptive]", g.Label("Platform:aws"), func() {
		architecture.SkipIfNoNodeWithArchitectures(oc.AsAdmin(), architecture.ARM64)
		skipTestIfNotSupportedPlatform(oc.AsAdmin(), GCPPlatform)

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

	g.It("[PolarionID:42364][OTP] add selinux kernel argument [Disruptive]", func() {
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

	g.It("[PolarionID:67825][OTP] Use duplicated kernel arguments [Disruptive]", func() {
		workerNode := skipTestIfOsIsNotCoreOs(oc)
		textToVerify := TextToVerify{
			textToVerifyForMC:   "(?s)y=0.*z=4.*y=1.*z=4",
			textToVerifyForNode: "y=0 z=4 y=1 z=4",
		}
		createMcAndVerifyMCValue(oc, "Duplicated kernel argument", "change-worker-duplicated-kernel-argument", workerNode, textToVerify, "cat", "/rootfs/proc/cmdline")
	})

	g.It("[PolarionID:53668][OTP] when FIPS and realtime kernel are both enabled node should NOT be degraded [Disruptive]", g.Label("Platform:aws", "Platform:gce"), func() {
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

		defer fipsMc.DeleteWithWait()
		fipsMc.create()

		exutil.By("create machine config to enable RT kernel")
		rtMcName := "50-realtime-kernel"
		rtMcTemplate := "set-realtime-kernel.yaml"
		rtMc := NewMachineConfig(oc.AsAdmin(), rtMcName, MachineConfigPoolMaster).SetMCOTemplate(rtMcTemplate)
		// TODO: When we extract the "mcp.waitForComplete" from the "create" and "delete" methods, we need to take into account that if
		// we are configuring a rt-kernel we need to wait longer.
		defer rtMc.DeleteWithWait()
		rtMc.create()

		masterNode := NewNodeList(oc.AsAdmin()).GetAllMasterNodesOrFail()[0]

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

	g.It("[PolarionID:54922][OTP] daemon: add check before updating kernelArgs [Disruptive]", g.Label("Platform:aws", "Platform:gce"), func() {
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

		defer mcArgs1.DeleteWithWait()
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

		defer mcArgs2.DeleteWithWait()
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

		defer mcUsb.DeleteWithWait()
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

	g.It("[PolarionID:72136][OTP] Reject MCs with ignition containing kernelArguments [Disruptive]", func() {
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

	g.It("[PolarionID:67788][OTP] kernel type 64k-pages is not supported on non-arm64 nodes [Disruptive]", func() {
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

	g.It("[PolarionID:67790][OTP] create MC with extensions, 64k-pages kernel type and kernel argument [Disruptive]", g.Label("Platform:aws"), func() {

		architecture.SkipIfNoNodeWithArchitectures(oc.AsAdmin(), architecture.ARM64)
		skipTestIfNotSupportedPlatform(oc.AsAdmin(), GCPPlatform)

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
		defer mc.DeleteWithWait()
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
})
