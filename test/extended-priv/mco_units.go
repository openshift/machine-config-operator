package extended

import (
	"fmt"
	"os/exec"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

// AI-assisted: This test suite focuses on systemd unit management through MCO
var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO units", func() {
	defer g.GinkgoRecover()

	var oc = exutil.NewCLI("mco-systemd-units", exutil.KubeConfigPath())

	g.JustBeforeEach(func() {
		PreChecks(oc)
	})

	g.It("[PolarionID:42361][OTP] add chrony systemd config [Disruptive]", func() {
		exutil.By("create new mc to apply chrony config on worker nodes")
		mcp := GetCompactCompatiblePool(oc)
		node := mcp.GetSortedNodesOrFail()[0]
		mcName := "ztc-42361-change-workers-chrony-configuration"
		mcTemplate := "change-workers-chrony-configuration.yaml"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName()).SetMCOTemplate(mcTemplate)

		defer mc.DeleteWithWait()
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

	g.It("[PolarionID:42438][OTP][LEVEL0] add journald systemd config [Disruptive]", func() {
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
		defer jc.DeleteWithWait()
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

	g.It("[PolarionID:46434][OTP] Mask service [Serial]", func() {
		activeString := "Active: active (running)"
		inactiveString := "Active: inactive (dead)"
		maskedString := "Loaded: masked"

		exutil.By("Validate that the chronyd service is active")
		workerNode := NewNodeList(oc.AsAdmin()).GetAllLinuxWorkerNodesOrFail()[0]
		svcOuput, err := workerNode.DebugNodeWithChroot("systemctl", "status", "chronyd")
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(svcOuput).Should(o.ContainSubstring(activeString))
		o.Expect(svcOuput).ShouldNot(o.ContainSubstring(inactiveString))

		exutil.By("Create a MachineConfig resource to mask the chronyd service")
		mcName := "99-test-mask-services"
		maskSvcConfig := getMaskServiceConfig("chronyd.service", true)
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		defer mc.DeleteWithWait()

		err = mc.Create("-p", "NAME="+mcName, "-p", "POOL=worker", "-p", fmt.Sprintf("UNITS=[%s]", maskSvcConfig))
		o.Expect(err).NotTo(o.HaveOccurred())
		// if service is masked, but node drain is failed, unmask chronyd service on all worker nodes in this defer block
		// then clean up logic will delete this mc, node will be rebooted, when the system is back online, chronyd service
		// can be started automatically, unmask command can be executed w/o error with loaded & active service
		defer func() {
			workersNodes := NewNodeList(oc.AsAdmin()).GetAllLinuxWorkerNodesOrFail()
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

	g.It("[PolarionID:56614][OTP] Create unit with content and mask=true[Disruptive]", func() {
		var (
			workerNode     = NewNodeList(oc.AsAdmin()).GetAllLinuxWorkerNodesOrFail()[0]
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
		defer mc.DeleteWithWait()

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

	g.It("[PolarionID:86869][OTP] kdump.service to be enabled after applying the MC [Disruptive]", func() {

		var (
			mcp          = GetCompactCompatiblePool(oc.AsAdmin())
			mcName       = fmt.Sprintf("mco-tc-%s-enable-kdump", GetCurrentTestPolarionIDNumber())
			mcUnit       = `{"enabled": true, "name": "kdump.service"}`
			mcKernelArgs = "crashkernel=256M"

			behaviourValidatorApply = UpdateBehaviourValidator{

				Checkers: []Checker{
					CommandOutputChecker{
						Command:  []string{"systemctl", "is-enabled", "kdump.service"},
						Matcher:  o.ContainSubstring("enabled"),
						ErrorMsg: "The kdump.service should be enabled but it is not",
						Desc:     "Check that the kdump.service is enabled",
					},
					CommandOutputChecker{
						Command:  []string{"systemctl", "is-active", "kdump.service"},
						Matcher:  o.Or(o.ContainSubstring("active"), o.ContainSubstring("exited")),
						ErrorMsg: "The kdump.service should be active or exited but it is not",
						Desc:     "Check that the kdump.service is active",
					},
					CommandOutputChecker{
						Command: []string{"systemctl", "status", "kdump.service"},
						Matcher: o.And(
							o.ContainSubstring("Loaded:"),
							o.ContainSubstring("enabled"),
							o.Or(
								o.ContainSubstring("Active: active"),
								o.ContainSubstring("active (exited)"),
							),
						),
						ErrorMsg:         "The kdump.service status should show enabled and active",
						Desc:             "Check that the kdump.service status shows enabled and active",
						IgnoreReturnCode: true,
					},
					CommandOutputChecker{
						Command:  []string{"cat", "/proc/cmdline"},
						Matcher:  o.ContainSubstring(mcKernelArgs),
						ErrorMsg: fmt.Sprintf("The kernel argument %s should be set but it is not", mcKernelArgs),
						Desc:     fmt.Sprintf("Check that the kernel argument %s is set", mcKernelArgs),
					},
				},
			}
			behaviourValidatorRemove = UpdateBehaviourValidator{

				Checkers: []Checker{
					CommandOutputChecker{
						Command:          []string{"systemctl", "is-enabled", "kdump.service"},
						Matcher:          o.Or(o.ContainSubstring("disabled"), o.ContainSubstring("masked")),
						ErrorMsg:         "The kdump.service should be disabled or masked but it is not",
						Desc:             "Check that the kdump.service is disabled",
						IgnoreReturnCode: true,
					},
					CommandOutputChecker{
						Command:  []string{"cat", "/proc/cmdline"},
						Matcher:  o.Not(o.ContainSubstring(mcKernelArgs)),
						ErrorMsg: fmt.Sprintf("The kernel argument %s should not be set but it is", mcKernelArgs),
						Desc:     fmt.Sprintf("Check that the kernel argument %s is removed", mcKernelArgs),
					},
				},
			}
		)

		behaviourValidatorApply.Initialize(mcp, nil)

		exutil.By("Create a MC to enable kdump.service")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName())
		mc.skipWaitForMcp = true

		defer mc.DeleteWithWait()

		err := mc.Create("-p", "NAME="+mcName, "-p", "POOL="+mcp.GetName(), "-p", fmt.Sprintf("UNITS=[%s]", mcUnit), "-p", fmt.Sprintf(`KERNEL_ARGS=["%s"]`, mcKernelArgs))
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating MachineConfig %s", mc.GetName())
		logger.Infof("OK!\n")

		// Check that the MC is applied according to the expected behaviour
		behaviourValidatorApply.Validate()
		behaviourValidatorRemove.Initialize(mcp, nil)

		exutil.By("Delete the MC created to enable kdump.service")
		mc.Delete()
		logger.Infof("OK!\n")

		behaviourValidatorRemove.Validate()
	})

	g.It("[PolarionID:80547][OTP] Disable chrony time service[Disruptive]", func() {

		var (
			mcp                   = GetCompactCompatiblePool(oc.AsAdmin())
			disableChronyTemplate = "disable-chrony.yaml"
			mcName                = fmt.Sprintf("mco-tc-%s-disable-chrony", GetCurrentTestPolarionIDNumber())

			behaviourValidatorApply = UpdateBehaviourValidator{
				Checkers: []Checker{
					CommandOutputChecker{
						Command:          []string{"systemctl", "is-enabled", "chronyd"},
						Matcher:          o.ContainSubstring("disabled"),
						ErrorMsg:         fmt.Sprintf("The chronyd service should be disabled but it is not"),
						Desc:             fmt.Sprintf("Check that the chronyd service is disabled"),
						IgnoreReturnCode: true,
					},
					CommandOutputChecker{
						Command:          []string{"systemctl", "is-active", "chronyd"},
						Matcher:          o.ContainSubstring("inactive"),
						ErrorMsg:         fmt.Sprintf("The chronyd service should be inactive but it is not"),
						Desc:             fmt.Sprintf("Check that the chronyd service is inactive"),
						IgnoreReturnCode: true,
					},
					CommandOutputChecker{
						Command:          []string{"systemctl", "is-failed", "chronyd"},
						Matcher:          o.ContainSubstring("inactive"),
						ErrorMsg:         fmt.Sprintf("The chronyd service should not be failed, but inactive"),
						Desc:             fmt.Sprintf("Check that the chronyd service is not failed"),
						IgnoreReturnCode: true,
					},
				},
			}
			behaviourValidatorRemove = UpdateBehaviourValidator{
				Checkers: []Checker{
					CommandOutputChecker{
						Command:  []string{"systemctl", "is-enabled", "chronyd"},
						Matcher:  o.ContainSubstring("enabled"),
						ErrorMsg: fmt.Sprintf("The chronyd service should be enabled but it is not"),
						Desc:     fmt.Sprintf("Check that the chronyd service is enabled"),
					},
					CommandOutputChecker{
						Command:  []string{"systemctl", "is-active", "chronyd"},
						Matcher:  o.ContainSubstring("active"),
						ErrorMsg: fmt.Sprintf("The chronyd service should be active but it is not"),
						Desc:     fmt.Sprintf("Check that the chronyd service is active"),
					},
					CommandOutputChecker{
						Command:          []string{"systemctl", "is-failed", "chronyd"},
						Matcher:          o.ContainSubstring("active"),
						ErrorMsg:         fmt.Sprintf("The chronyd service should not be failed, but active"),
						Desc:             fmt.Sprintf("Check that the chronyd service is not failed"),
						IgnoreReturnCode: true,
					},
				},
			}
		)

		behaviourValidatorApply.Initialize(mcp, nil)

		exutil.By("Create a MC to disable chrony")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker).SetMCOTemplate(disableChronyTemplate)
		mc.skipWaitForMcp = true

		defer mc.DeleteWithWait()
		mc.create()
		logger.Infof("OK!\n")

		// Check that the MC is applied according to the expected behaviour
		behaviourValidatorApply.Validate()

		behaviourValidatorRemove.Initialize(mcp, nil)

		exutil.By("Delete the MC created to disable chrony")
		mc.Delete()
		logger.Infof("OK!\n")

		behaviourValidatorRemove.Validate()
	})
})
