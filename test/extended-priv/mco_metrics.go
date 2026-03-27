package extended

import (
	"fmt"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO metrics", func() {
	defer g.GinkgoRecover()

	var (
		oc = exutil.NewCLI("mco-metrics", exutil.KubeConfigPath())
		// Compact compatible MCP. If the node is compact/SNO this variable will be the master pool, else it will be the worker pool
		mcp *MachineConfigPool
	)

	g.JustBeforeEach(func() {
		mcp = GetCompactCompatiblePool(oc.AsAdmin())

		PreChecks(oc)
	})

	g.It("[PolarionID:77356][OTP] Test mcd_local_unsupported_packages metric [Disruptive]", func() {
		var (
			node                        = mcp.GetSortedNodesOrFail()[0]
			query                       = `mcd_local_unsupported_packages{node="` + node.GetName() + `"}`
			resultJSONPath              = `data.result`
			valueJSONPath               = `data.result.0.value.1`
			expectedUnsupportedPackages = "5"
			deferredReesetNeeded        = true
		)

		exutil.By("Configure coreos stream repo in a node")
		defer RemoveConfiguredStreamCentosRepo(node)
		o.Expect(ConfigureStreamCentosRepo(node, "9-stream")).To(o.Succeed(),
			"Error configuring the centos repo in %s", node)
		logger.Infof("OK!\n")

		exutil.By("Check that no unsupported packages are reported")
		monitor, err := exutil.NewMonitor(oc.AsAdmin())
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the monitor to query the metricts")

		o.Eventually(monitor.SimpleQuery, "10s", "2s").WithArguments(query).Should(HavePathWithValue(valueJSONPath, o.Equal("0")),
			"There are reported unsupported packages in %s", node)
		logger.Infof("OK!\n")

		exutil.By("Install package from repo")
		defer func() {
			if deferredReesetNeeded {
				if err := node.OSReset(); err != nil {
					logger.Errorf("Error in the OS Reset: %s", err)
				}

				if err := node.Reboot(); err != nil {
					logger.Errorf("Error in the reboot: %s", err)
				}

				mcp.waitForComplete()
			} else {
				logger.Infof("The OS Reset has been already executed. No need to execute it again in the deferred section")
			}
		}()

		installedPackage := "man-db"
		_, err = node.InstallRpm(installedPackage)
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error installing %s package in %s", installedPackage, node)
		logger.Infof("OK!\n")

		exutil.By("Install package locally")
		logger.Infof("Dowload package")
		_, err = node.DebugNodeWithChroot("sh", "-c", useProxyInCommand("curl -kL https://dl.fedoraproject.org/pub/epel/epel-release-latest-9.noarch.rpm -o /tmp/epel-release-latest-9.noarch.rpm"))
		o.Expect(err).NotTo(o.HaveOccurred(), "Error downloading the epel rpm package")
		logger.Infof("Install")
		_, err = node.InstallRpm("/tmp/epel-release-latest-9.noarch.rpm")
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error installing %s package in %s", "epel-release", node)
		logger.Infof("OK!\n")

		exutil.By("Remove package from OS")
		removedPackage := "git-core"
		_, err = node.DebugNodeWithChroot("rpm-ostree", "override", "remove", removedPackage)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error removing package %s from %s", removedPackage, node)
		logger.Infof("OK!\n")

		exutil.By("Replace an existing package in the OS")
		if exutil.OrFail[bool](node.CanUseDnfDownload()) {
			replacedPackage := "nano"
			pkgPath, err := node.DnfDownload(replacedPackage, "/tmp")
			o.Expect(err).NotTo(o.HaveOccurred(), "Error downloading %s package", replacedPackage)

			_, err = node.DebugNodeWithChroot("rpm-ostree", "override", "replace", "--experimental", pkgPath)
			o.Expect(err).NotTo(o.HaveOccurred(), "Error replacing %s package in the OS", replacedPackage)
		} else {
			expectedUnsupportedPackages = "4"
			logger.Infof("It is not possible to use dnf to download the right package. We skip the package removal. Expected unsupported packages will be %s now", expectedUnsupportedPackages)
		}
		logger.Infof("OK!\n")

		exutil.By("Override package locally")
		logger.Infof("Dowload package")
		_, err = node.DebugNodeWithChroot("sh", "-c", useProxyInCommand("curl -kL https://mirrors.rpmfusion.org/free/el/rpmfusion-free-release-9.noarch.rpm -o /tmp/rpmfusion-free-release-9.noarch.rpm"))
		o.Expect(err).NotTo(o.HaveOccurred(), "Error downloading the rpmfusion rpm package")

		_, err = node.DebugNodeWithChroot("rpm-ostree", "install", "--force-replacefiles", "/tmp/rpmfusion-free-release-9.noarch.rpm")
		o.Expect(err).NotTo(o.HaveOccurred(), "Error installing forcing a replace")
		logger.Infof("OK!\n")

		exutil.By("Check that no unsupported packages are reported before rebooting the node")
		o.Eventually(monitor.SimpleQuery, "10s", "2s").WithArguments(query).Should(HavePathWithValue(valueJSONPath, o.Equal("0")),
			"There are reported unsupported packages in %s after restoring the original status", node)
		logger.Infof("OK!\n")

		exutil.By("Reboot the node to apply the changes")
		o.Expect(node.Reboot()).To(o.Succeed(),
			"Error rebooting %s to apply the changes", node)
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check that the right number of unsupported packages is reported")
		o.Eventually(monitor.SimpleQuery, "3m", "5s").WithArguments(query).Should(HavePathWithValue(valueJSONPath, o.Equal(expectedUnsupportedPackages)),
			"The metric is not reporting the right number of unsupported packages in %s", node)
		logger.Infof("OK!\n")

		exutil.By("Restore original OS status in the node")
		o.Eventually(node.IsRpmOsTreeIdle, "10m", "20s").
			Should(o.BeTrue(), "rpm-ostree status didn't become idle")
		o.Expect(node.OSReset()).To(o.Succeed(),
			"Error restoring the original OS status in %s", node)
		o.Expect(node.Reboot()).To(o.Succeed(),
			"Error rebooting %s to restore the initial state", node)
		mcp.waitForComplete()
		deferredReesetNeeded = false
		logger.Infof("OK!\n")

		exutil.By("Check that no unsupported packages are reported after restoring the original OS status")
		o.Eventually(monitor.SimpleQuery, "3m", "5s").WithArguments(query).Should(o.Or(
			HavePathWithValue(valueJSONPath, o.Equal("0")),
			HavePathWithValue(resultJSONPath, o.HaveLen(0))),
			"There are reported unsupported packages in %s after restoring the original status", node)
		logger.Infof("OK!\n")

	})
})

func useProxyInCommand(cmd string) string {
	return fmt.Sprintf("set -a; source /etc/mco/proxy.env && %s", cmd)
}
