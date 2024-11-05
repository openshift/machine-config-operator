package mco

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/architecture"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util"
	clusterinfra "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/clusterinfra"
	logger "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/logext"
)

var _ = g.Describe("[sig-mco] MCO Layering", func() {
	defer g.GinkgoRecover()

	var (
		// init cli object, temp namespace contains prefix mco.
		// tip: don't put this in BeforeEach/JustBeforeEach, you will get error
		// "You may only call AfterEach from within a Describe, Context or When"
		oc = exutil.NewCLI("mco-layering", exutil.KubeConfigPath())
		// temp dir to store all test files, and it will be recycled when test is finished
		tmpdir string
	)

	g.JustBeforeEach(func() {
		tmpdir = createTmpDir()
		preChecks(oc)
	})

	g.JustAfterEach(func() {
		os.RemoveAll(tmpdir)
		logger.Infof("test dir %s is cleaned up", tmpdir)
	})

	g.It("Author:sregidor-ConnectedOnly-Longduration-NonPreRelease-Critical-54085-Update osImage changing /etc /usr and rpm [Disruptive]", func() {

		architecture.SkipArchitectures(oc, architecture.MULTI, architecture.S390X, architecture.PPC64LE)
		// Because of https proxies using their own user-ca certificate, we need to take into account the openshift-config-user-ca-bundle.crt file
		coreOSMcp := GetCoreOsCompatiblePool(oc.AsAdmin())
		node := coreOSMcp.GetCoreOsNodesOrFail()[0]
		dockerFileCommands := `
RUN mkdir /etc/tc_54085 && chmod 3770 /etc/tc_54085 && ostree container commit

RUN echo 'Test case 54085 test file' > /etc/tc54085.txt && chmod 5400 /etc/tc54085.txt && ostree container commit

RUN echo 'echo "Hello world"' > /usr/bin/tc54085_helloworld && chmod 5770 /usr/bin/tc54085_helloworld && ostree container commit

COPY openshift-config-user-ca-bundle.crt /etc/pki/ca-trust/source/anchors/openshift-config-user-ca-bundle.crt

RUN update-ca-trust && \
    rm /etc/pki/ca-trust/source/anchors/openshift-config-user-ca-bundle.crt && \
    cd /etc/yum.repos.d/ && curl -LO https://pkgs.tailscale.com/stable/fedora/tailscale.repo && \
    rpm-ostree install tailscale && rpm-ostree cleanup -m && \
    systemctl enable tailscaled && \
    ostree container commit
`
		// Capture current rpm-ostree status
		exutil.By("Capture the current ostree deployment")
		initialBootedImage, err := node.GetCurrentBootOSImage()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the initial booted image")
		logger.Infof("OK\n")

		// Build the new osImage
		osImageBuilder := OsImageBuilderInNode{node: node, dockerFileCommands: dockerFileCommands}
		digestedImage, err := osImageBuilder.CreateAndDigestOsImage()
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error creating the new osImage")
		logger.Infof("OK\n")

		// Create MC and wait for MCP
		exutil.By("Create a MC to deploy the new osImage")
		layeringMcName := "layering-mc"
		layeringMC := NewMachineConfig(oc.AsAdmin(), layeringMcName, coreOSMcp.GetName())
		layeringMC.parameters = []string{"OS_IMAGE=" + digestedImage}
		layeringMC.skipWaitForMcp = true

		defer layeringMC.delete()
		layeringMC.create()

		coreOSMcp.waitForComplete()
		logger.Infof("The new osImage was deployed successfully\n")

		// Check rpm-ostree status
		exutil.By("Check that the rpm-ostree status is reporting the right booted image")
		o.Expect(node.GetCurrentBootOSImage()).To(o.Equal(digestedImage),
			"The booted image resported by rpm-ostree status is not the expected one")
		logger.Infof("OK!\n")

		// Check image content
		exutil.By("Load remote resources to verify that the osImage content has been deployed properly")

		tc54085Dir := NewRemoteFile(node, "/etc/tc_54085")
		tc54085File := NewRemoteFile(node, "/etc/tc54085.txt")
		binHelloWorld := NewRemoteFile(node, "/usr/bin/tc54085_helloworld")

		o.Expect(tc54085Dir.Fetch()).ShouldNot(o.HaveOccurred(),
			"Error getting information about file %s in node %s", tc54085Dir.GetFullPath(), node.GetName())
		o.Expect(tc54085File.Fetch()).ShouldNot(o.HaveOccurred(),
			"Error getting information about file %s in node %s", tc54085File.GetFullPath(), node.GetName())
		o.Expect(binHelloWorld.Fetch()).ShouldNot(o.HaveOccurred(),
			"Error getting information about file %s in node %s", binHelloWorld.GetFullPath(), node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Check that the directory in /etc exists and has the right permissions")
		o.Expect(tc54085Dir.IsDirectory()).To(o.BeTrue(),
			"Error, %s in node %s is not a directory", tc54085Dir.GetFullPath(), node.GetName())
		o.Expect(tc54085Dir.GetNpermissions()).To(o.Equal("3770"),
			"Error, permissions of %s in node %s are not the expected ones", tc54085Dir.GetFullPath(), node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Check that the file in /etc exists and has the right permissions")
		o.Expect(tc54085File.GetNpermissions()).To(o.Equal("5400"),
			"Error, permissions of %s in node %s are not the expected ones", tc54085File.GetFullPath(), node.GetName())
		o.Expect(tc54085File.GetTextContent()).To(o.Equal("Test case 54085 test file\n"),
			"Error, content of %s in node %s are not the expected one", tc54085File.GetFullPath(), node.GetName())

		exutil.By("Check that the file in /usr/bin exists, has the right permissions and can be executed")
		o.Expect(binHelloWorld.GetNpermissions()).To(o.Equal("5770"),
			"Error, permissions of %s in node %s are not the expected ones", tc54085File.GetFullPath(), node.GetName())

		output, herr := node.DebugNodeWithChroot("/usr/bin/tc54085_helloworld")
		o.Expect(herr).NotTo(o.HaveOccurred(),
			"Error executing 'hello world' executable file /usr/bin/tc54085_helloworld")
		o.Expect(output).To(o.ContainSubstring("Hello world"),
			"Error, 'Hellow world' executable file's output was not the expected one")
		logger.Infof("OK!\n")

		exutil.By("Check that the tailscale rpm has been deployed")
		tailscaledRpm, rpmErr := node.DebugNodeWithChroot("rpm", "-q", "tailscale")
		o.Expect(rpmErr).NotTo(o.HaveOccurred(),
			"Error, getting the installed rpms in node %s.  'tailscale' rpm is not installed.", node.GetName())
		o.Expect(tailscaledRpm).To(o.ContainSubstring("tailscale-"),
			"Error, 'tailscale' rpm is not installed in node %s", node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Check that the tailscaled.service unit is loaded, active and enabled")
		tailscaledStatus, unitErr := node.GetUnitStatus("tailscaled.service")
		o.Expect(unitErr).NotTo(o.HaveOccurred(),
			"Error getting the status of the 'tailscaled.service' unit in node %s", node.GetName())
		o.Expect(tailscaledStatus).Should(
			o.And(
				o.ContainSubstring("tailscaled.service"),
				o.ContainSubstring("Active: active"), // is active
				o.ContainSubstring("Loaded: loaded"), // is loaded
				o.ContainSubstring("; enabled;")),    // is enabled
			"tailscaled.service unit should be loaded, active and enabled and it is not")
		logger.Infof("OK!\n")

		// Delete the MC and wait for MCP
		exutil.By("Delete the MC so that the original osImage is restored")
		layeringMC.delete()
		coreOSMcp.waitForComplete()
		logger.Infof("MC was successfully deleted\n")

		// Check the rpm-ostree status after the MC deletion
		exutil.By("Check that the original ostree deployment was restored")
		o.Expect(node.GetCurrentBootOSImage()).To(o.Equal(initialBootedImage),
			"Error! the initial osImage was not properly restored after deleting the MachineConfig")
		logger.Infof("OK!\n")

		// Check the image content after the MC deletion
		exutil.By("Check that the directory in /etc does not exist anymore")
		o.Expect(tc54085Dir.Fetch()).Should(o.HaveOccurred(),
			"Error, file %s should not exist in node %s, but it exists", tc54085Dir.GetFullPath(), node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Check that the file in /etc does not exist anymore")
		o.Expect(tc54085File.Fetch()).Should(o.HaveOccurred(),
			"Error, file %s should not exist in node %s, but it exists", tc54085File.GetFullPath(), node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Check that the file in /usr/bin does not exist anymore")
		o.Expect(binHelloWorld.Fetch()).Should(o.HaveOccurred(),
			"Error, file %s should not exist in node %s, but it exists", binHelloWorld.GetFullPath(), node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Check that the tailscale rpm is not installed anymore")
		tailscaledRpm, rpmErr = node.DebugNodeWithChroot("rpm", "-q", "tailscale")
		o.Expect(rpmErr).To(o.HaveOccurred(),
			"Error,  'tailscale' rpm should not be installed in node %s, but it is installed.\n Output %s", node.GetName(), tailscaledRpm)
		logger.Infof("OK!\n")

		exutil.By("Check that the tailscaled.service is not present anymore")
		tailscaledStatus, unitErr = node.GetUnitStatus("tailscaled.service")
		o.Expect(unitErr).To(o.HaveOccurred(),
			"Error,  'tailscaled.service'  unit should not be available in node %s, but it is.\n Output %s", node.GetName(), tailscaledStatus)
		logger.Infof("OK!\n")

	})
	g.It("Author:sregidor-ConnectedOnly-NonPreRelease-Longduration-Medium-54052-Not bootable layered osImage provided[Disruptive]", func() {
		var (
			nonBootableImage = "quay.io/openshifttest/hello-openshift:1.2.0"
			layeringMcName   = "not-bootable-image-tc54052"

			expectedNDMessage = ".*failed to update OS to " + regexp.QuoteMeta(nonBootableImage) + ".*error running rpm-ostree rebase.*ostree.bootable.*"
			expectedNDReason  = "1 nodes are reporting degraded status on sync"
		)

		checkInvalidOsImagesDegradedStatus(oc.AsAdmin(), nonBootableImage, layeringMcName, expectedNDMessage, expectedNDReason)
	})

	g.It("Author:sregidor-NonPreRelease-Longduration-Medium-54054-Not pullable layered osImage provided[Disruptive]", func() {
		var (
			nonPullableImage  = "quay.io/openshifttest/tc54054fakeimage:latest"
			layeringMcName    = "not-pullable-image-tc54054"
			expectedNDMessage = ".*" + regexp.QuoteMeta(nonPullableImage) + ".*error"

			expectedNDReason = "1 nodes are reporting degraded status on sync"
		)

		checkInvalidOsImagesDegradedStatus(oc.AsAdmin(), nonPullableImage, layeringMcName, expectedNDMessage, expectedNDReason)
	})

	g.It("Author:sregidor-ConnectedOnly-Longduration-NonPreRelease-Critical-54159-Apply a new osImage on a cluster with already installed rpms [Disruptive]", func() {
		var (
			rpmName         = "wget"
			yumRepoTemplate = generateTemplateAbsolutePath("centos.repo")
			yumRepoFile     = "/etc/yum.repos.d/tc-54159-centos.repo"
			proxy           = NewResource(oc.AsAdmin(), "proxy", "cluster")
			coreOSMcp       = GetCoreOsCompatiblePool(oc.AsAdmin())
			node            = coreOSMcp.GetCoreOsNodesOrFail()[0]
		)

		architecture.SkipArchitectures(oc, architecture.MULTI, architecture.S390X, architecture.PPC64LE)

		dockerFileCommands := `
RUN echo "echo 'Hello world! '$(whoami)" > /usr/bin/tc_54159_rpm_and_osimage && chmod 1755 /usr/bin/tc_54159_rpm_and_osimage
`
		// Build the new osImage
		osImageBuilder := OsImageBuilderInNode{node: node, dockerFileCommands: dockerFileCommands}
		digestedImage, berr := osImageBuilder.CreateAndDigestOsImage()
		o.Expect(berr).NotTo(o.HaveOccurred(),
			"Error creating the new osImage")
		logger.Infof("OK\n")

		// Install rpm in first node
		exutil.By("Installing rpm package in first working node")

		logger.Infof("Copy yum repo to node")
		o.Expect(node.CopyFromLocal(yumRepoTemplate, yumRepoFile)).
			NotTo(o.HaveOccurred(),
				"Error copying  %s to %s in node %s", yumRepoTemplate, yumRepoFile, node.GetName())

		// rpm-ostree only uses the proxy from the yum.repos.d configuration, it ignores the env vars.
		logger.Infof("Configure proxy in yum")
		_, err := node.DebugNodeWithChroot("sed", "-i", "s#proxy=#proxy="+proxy.GetOrFail(`{.status.httpProxy}`)+"#g", yumRepoFile)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error configuring the proxy in the centos yum repo config")

		defer func() {
			logger.Infof("Start defer logic to uninstall the %s rpm", rpmName)
			waitErr := node.WaitUntilRpmOsTreeIsIdle()
			if waitErr != nil {
				node.CancelRpmOsTreeTransactions()
			}
			node.UninstallRpm(rpmName)
			node.DebugNodeWithChroot("rm", yumRepoFile)
			node.Reboot()
			coreOSMcp.waitForComplete()
			// Because of a bug in SNO after a reboot the controller cannot get the lease properly
			// We wait until the controller gets the lease. We make sure that the next test will receive a fully clean environment with the controller ready
			o.Eventually(NewController(oc.AsAdmin()).HasAcquiredLease, "10m", "20s").Should(o.BeTrue(),
				"Controller never acquired lease after the nodes was rebooted")

			// Printing the status, apart from tracing the exact status of rpm-ostree,
			// is a way of waiting for the node to be ready after the reboot, so that the next test case
			// can be executed without problems. Because the status cannot be retreived until the node is ready.
			status, _ := node.GetRpmOstreeStatus(false)
			logger.Infof(status)
		}()
		// We wait, but we dont fail, if it does not become idle we cancel the transaction in the installation command
		waitErr := node.WaitUntilRpmOsTreeIsIdle()
		if waitErr != nil {
			logger.Infof("rpm-ostree state is NOT IDLE. We cancel the current transactions to continue the test!!!")
			cOut, err := node.CancelRpmOsTreeTransactions()
			o.Expect(err).
				NotTo(o.HaveOccurred(),
					"Error cancelling transactions in node %s.\n%s", node.GetName(), cOut)

		}
		instOut, err := node.InstallRpm(rpmName)
		logger.Debugf("Install rpm output: %s", instOut)
		o.Expect(err).
			NotTo(o.HaveOccurred(),
				"Error installing '%s' rpm in node %s", rpmName, node.GetName())

		o.Expect(node.Reboot()).To(o.Succeed(),
			"Error rebooting node %s", node.GetName())

		// In SNO clusters when we reboot the only node the connectivity is broken.
		// Because the exutils.debugNode function fails the test if any problem happens
		// we need to wait until the pool is stable (waitForComplete) before trying to debug the node again, even if we do it inside an Eventually instruction
		coreOSMcp.waitForComplete()

		logger.Infof("Check that the wget binary is available")
		o.Eventually(func() error {
			_, err := node.DebugNodeWithChroot("which", "wget")
			return err
		}, "15m", "20s").Should(o.Succeed(),
			"Error. wget binay is not available after installing '%s' rpm in node %s.", rpmName, node.GetName())

		logger.Infof("OK\n")

		// Capture current rpm-ostree status
		exutil.By("Capture the current ostree deployment")
		o.Eventually(node.IsRpmOsTreeIdle, "10m", "20s").
			Should(o.BeTrue(), "rpm-ostree status didn't become idle after installing wget")

		initialDeployment, derr := node.GetBootedOsTreeDeployment(false)
		o.Expect(derr).NotTo(o.HaveOccurred(),
			"Error getting the rpm-ostree status value in node %s", node.GetName())
		logger.Infof("Current status with date:\n %s", initialDeployment)

		o.Expect(initialDeployment).
			To(o.MatchRegexp("LayeredPackages: .*%s", rpmName),
				"rpm-ostree is not reporting the installed '%s' package in the rpm-ostree status command", rpmName)

		initialBootedImage, err := node.GetCurrentBootOSImage()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the initial booted image")
		logger.Infof("Initial booted osImage: %s", initialBootedImage)
		logger.Infof("OK\n")

		// Create MC and wait for MCP
		exutil.By("Create a MC to deploy the new osImage")
		layeringMcName := "layering-mc-54159"
		layeringMC := NewMachineConfig(oc.AsAdmin(), layeringMcName, coreOSMcp.GetName())
		layeringMC.parameters = []string{"OS_IMAGE=" + digestedImage}
		layeringMC.skipWaitForMcp = true

		defer layeringMC.delete()
		layeringMC.create()

		// Because of a bug in SNO after a reboot the controller cannot get the lease properly
		// We wait until the controller gets the lease
		o.Eventually(NewController(oc.AsAdmin()).HasAcquiredLease, "10m", "20s").Should(o.BeTrue(),
			"Controller never acquired lease after the nodes was rebooted")

		coreOSMcp.waitForComplete()
		logger.Infof("The new osImage was deployed successfully\n")

		// Check rpm-ostree status
		exutil.By("Check that the rpm-ostree status is reporting the right booted image and installed rpm")
		bootedDeployment, err := node.GetBootedOsTreeDeployment(false)
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the rpm-ostree status value in node %s", node.GetName())
		logger.Infof("Current rpm-ostree booted status:\n%s\n", bootedDeployment)

		o.Expect(bootedDeployment).
			To(o.MatchRegexp("LayeredPackages: .*%s", rpmName),
				"rpm-ostree is not reporting the installed 'wget' package in the rpm-ostree status command")

		o.Expect(node.GetCurrentBootOSImage()).To(o.Equal(digestedImage),
			"container reference in the status is not reporting the right booted image")
		logger.Infof("OK!\n")

		// Check rpm is installed
		exutil.By("Check that the rpm is installed even if we use the new osImage")
		rpmOut, err := node.DebugNodeWithChroot("rpm", "-q", "wget")
		o.Expect(err).
			NotTo(o.HaveOccurred(),
				"Error. %s rpm is not installed after changing the osImage in node %s.\n%s", rpmName, node.GetName(), rpmOut)

		wOut, err := node.DebugNodeWithChroot("which", "wget")
		o.Expect(err).
			NotTo(o.HaveOccurred(),
				"Error. wget binay is not available after installing '%s' rpm in node %s.\n%s", rpmName, node.GetName(), wOut)

		logger.Infof("OK\n")

		// Check osImage content
		exutil.By("Check that the new osImage content was deployed properly")
		rf := NewRemoteFile(node, "/usr/bin/tc_54159_rpm_and_osimage")
		o.Expect(rf.Fetch()).
			ShouldNot(o.HaveOccurred(),
				"Error getting information about file %s in node %s", rf.GetFullPath(), node.GetName())
		o.Expect(rf.GetNpermissions()).To(o.Equal("1755"),
			"Error, permissions of %s in node %s are not the expected ones", rf.GetFullPath(), node.GetName())
		o.Expect(rf.GetTextContent()).To(o.ContainSubstring("Hello world"),
			"Error, content of %s in node %s is not the expected ones", rf.GetFullPath(), node.GetName())
		logger.Infof("OK\n")

		// Delete the MC and wait for MCP
		exutil.By("Delete the MC so that original osImage is restored")
		layeringMC.delete()
		logger.Infof("MC was successfully deleted\n")

		// Check the rpm-ostree status after the MC deletion
		exutil.By("Check that the original ostree deployment was restored")
		logger.Infof("Waiting for rpm-ostree status to be idle")
		o.Eventually(node.IsRpmOsTreeIdle, "10m", "20s").
			Should(o.BeTrue(), "rpm-ostree status didn't become idle after installing wget")

		logger.Infof("Checking original status")
		deployment, derr := node.GetBootedOsTreeDeployment(false) // for debugging
		o.Expect(derr).NotTo(o.HaveOccurred(),
			"Error getting the rpm-ostree status value in node %s", node.GetName())
		logger.Infof("Current status with date:\n %s", deployment)

		o.Expect(node.GetCurrentBootOSImage()).To(o.Equal(initialBootedImage),
			"Error! the initial osImage was not properly restored after deleting the MachineConfig")
		logger.Infof("OK!\n")

	})

	g.It("Author:sregidor-NonPreRelease-Medium-54049-Verify base images in the release image", func() {
		var (
			oldMachineConfigOsImage = "machine-os-content"
			coreExtensions          = "rhel-coreos-extensions"
		)

		exutil.By("Extract pull-secret")
		pullSecret := GetPullSecret(oc.AsAdmin())
		// TODO: when the code to create a tmp directory in the beforeEach section is merged, use ExtractToDir method instead
		secretExtractDir, err := pullSecret.Extract()
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error extracting pull-secret")
		logger.Infof("Pull secret has been extracted to: %s\n", secretExtractDir)
		dockerConfigFile := filepath.Join(secretExtractDir, ".dockerconfigjson")

		exutil.By("Get base image for layering")
		baseImage, err := getImageFromReleaseInfo(oc.AsAdmin(), LayeringBaseImageReleaseInfo, dockerConfigFile)
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the base image to build new osImages")
		logger.Infof("Base image: %s\n", baseImage)

		exutil.By("Inspect base image information")
		skopeoCLI := NewSkopeoCLI().SetAuthFile(dockerConfigFile)
		inspectInfo, err := skopeoCLI.Run("inspect").Args("--tls-verify=false", "--config", "docker://"+baseImage).Output()
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error using 'skopeo' to inspect base image %s", baseImage)

		logger.Infof("Check if image is bootable")
		inspectJSON := JSON(inspectInfo)
		ostreeBootable := inspectJSON.Get("config").Get("Labels").Get("ostree.bootable").ToString()
		o.Expect(ostreeBootable).To(o.Equal("true"),
			`The base image %s is expected to be bootable (.config.Labels.ostree\.bootable == "true", but skopeo information says that it is not bootable. %s`,
			baseImage, inspectInfo)
		logger.Infof("OK!\n")

		exutil.By("Verify that old machine config os content is not present in the release info")
		mcOsIMage, _ := getImageFromReleaseInfo(oc.AsAdmin(), oldMachineConfigOsImage, dockerConfigFile)
		o.Expect(mcOsIMage).To(o.ContainSubstring(`no image tag "`+oldMachineConfigOsImage+`" exists`),
			"%s image should not be present in the release image, but we can find it with value %s", oldMachineConfigOsImage, mcOsIMage)
		logger.Infof("OK!\n")

		exutil.By("Verify that new core extensions image is present in the release info")
		coreExtensionsValue, exErr := getImageFromReleaseInfo(oc.AsAdmin(), coreExtensions, dockerConfigFile)
		o.Expect(exErr).NotTo(o.HaveOccurred(),
			"Error getting the new core extensions image")
		o.Expect(coreExtensionsValue).NotTo(o.BeEmpty(),
			"%s image should be present in the release image, but we cannot find it with value %s", coreExtensions)
		logger.Infof("%s is present in the release infor with value %s", coreExtensions, coreExtensionsValue)
		logger.Infof("OK!\n")

	})
	g.It("Author:sregidor-ConnectedOnly-Longduration-NonPreRelease-High-54909-Configure extensions while using a custom osImage [Disruptive]", func() {
		// Due to https://issues.redhat.com/browse/OCPBUGS-31255 in this test case pools will be degraded intermittently. They will be degraded and automatically fixed in a few minutes/seconds
		// Because of that we need to use WaitForUpdatedStatus instead of waitForComplete, since WaitForUpdatedStatus will not fail if a pool is degraded for just a few minutes but the configuration is applied properly
		architecture.SkipArchitectures(oc, architecture.MULTI, architecture.S390X, architecture.PPC64LE)
		var (
			rpmName            = "zsh"
			extensionRpmName   = "usbguard"
			dockerFileCommands = fmt.Sprintf(`
COPY openshift-config-user-ca-bundle.crt /etc/pki/ca-trust/source/anchors/openshift-config-user-ca-bundle.crt
RUN update-ca-trust && \
    rm /etc/pki/ca-trust/source/anchors/openshift-config-user-ca-bundle.crt
RUN printf '[baseos]\nname=CentOS-$releasever - Base\nbaseurl=http://mirror.stream.centos.org/$releasever-stream/BaseOS/$basearch/os/\ngpgcheck=0\nenabled=1\nproxy='$HTTPS_PROXY'\n\n[appstream]\nname=CentOS-$releasever - AppStream\nbaseurl=http://mirror.stream.centos.org/$releasever-stream/AppStream/$basearch/os/\ngpgcheck=0\nenabled=1\nproxy='$HTTPS_PROXY'\n\n' > /etc/yum.repos.d/centos.repo && \
    rpm-ostree install %s && \
    rpm-ostree cleanup -m && \
    ostree container commit
`, rpmName)
			workerNode = NewNodeList(oc).GetAllCoreOsWokerNodesOrFail()[0]
			masterNode = NewNodeList(oc).GetAllMasterNodesOrFail()[0]
			wMcp       = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			mMcp       = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
		)

		mMcp.SetWaitingTimeForExtensionsChange()
		wMcp.SetWaitingTimeForExtensionsChange()
		defer mMcp.WaitForUpdatedStatus()
		defer wMcp.WaitForUpdatedStatus()

		// Build the new osImage
		osImageBuilder := OsImageBuilderInNode{node: workerNode, dockerFileCommands: dockerFileCommands}
		defer func() { _ = osImageBuilder.CleanUp() }()
		digestedImage, err := osImageBuilder.CreateAndDigestOsImage()
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error creating the new osImage")
		logger.Infof("OK\n")

		// Create MC to apply the config to worker nodes
		exutil.By("Create a MC to deploy the new osImage in 'worker' pool")
		wLayeringMcName := "tc-54909-layering-extensions-worker"
		wLayeringMC := NewMachineConfig(oc.AsAdmin(), wLayeringMcName, MachineConfigPoolWorker)
		wLayeringMC.parameters = []string{"OS_IMAGE=" + digestedImage}
		wLayeringMC.skipWaitForMcp = true

		defer wLayeringMC.deleteNoWait()
		wLayeringMC.create()

		// Create MC to apply the config to master nodes
		exutil.By("Create a MC to deploy the new osImage in 'master' pool")
		mLayeringMcName := "tc-54909-layering-extensions-master"
		mLayeringMC := NewMachineConfig(oc.AsAdmin(), mLayeringMcName, MachineConfigPoolMaster)
		mLayeringMC.parameters = []string{"OS_IMAGE=" + digestedImage}
		mLayeringMC.skipWaitForMcp = true

		defer mLayeringMC.deleteNoWait()
		mLayeringMC.create()

		// Wait for pools
		o.Expect(wMcp.WaitForUpdatedStatus()).To(o.Succeed())
		logger.Infof("The new osImage was deployed successfully in 'worker' pool\n")

		o.Expect(mMcp.WaitForUpdatedStatus()).To(o.Succeed())
		logger.Infof("The new osImage was deployed successfully in 'master' pool\n")

		// Check rpm-ostree status in worker node
		exutil.By("Check that the rpm-ostree status is reporting the right booted image in worker nodes")

		wStatus, err := workerNode.GetRpmOstreeStatus(false)
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the rpm-ostree status value in worker node %s", workerNode.GetName())
		logger.Infof("Current rpm-ostree status in worker node:\n%s\n", wStatus)

		wDeployment, err := workerNode.GetBootedOsTreeDeployment(true)
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the rpm-ostree status value in worker node %s", workerNode.GetName())

		wContainerRef, jerr := JSON(wDeployment).GetSafe("container-image-reference")
		o.Expect(jerr).NotTo(o.HaveOccurred(),
			"We cant get 'container-image-reference' from the deployment status in worker node. Wrong rpm-ostree status!")
		o.Expect(wContainerRef.ToString()).To(o.Equal("ostree-unverified-registry:"+digestedImage),
			"container reference in the worker node's status is not the exepeced one")
		logger.Infof("OK!\n")

		// Check rpm-ostree status in master node
		exutil.By("Check that the rpm-ostree status is reporting the right booted image in master nodes")

		mStatus, err := masterNode.GetRpmOstreeStatus(false)
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the rpm-ostree status value in master node %s", masterNode.GetName())
		logger.Infof("Current rpm-ostree status in master node:\n%s\n", mStatus)

		mDeployment, err := masterNode.GetBootedOsTreeDeployment(true)
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the rpm-ostree status value in master node %s", masterNode.GetName())

		mContainerRef, jerr := JSON(mDeployment).GetSafe("container-image-reference")
		o.Expect(jerr).NotTo(o.HaveOccurred(),
			"We cant get 'container-image-reference' from the deployment status in master node. Wrong rpm-ostree status!")
		o.Expect(mContainerRef.ToString()).To(o.Equal("ostree-unverified-registry:"+digestedImage),
			"container reference in the master node's status is not the exepeced one")
		logger.Infof("OK!\n")

		// Check rpm is installed in worker node
		exutil.By(fmt.Sprintf("Check that the %s rpm is installed in worker node", rpmName))
		o.Expect(workerNode.RpmIsInstalled(rpmName)).
			To(o.BeTrue(),
				"Error. %s rpm is not installed after changing the osImage in worker node %s.", rpmName, workerNode.GetName())
		logger.Infof("OK\n")

		// Check rpm is installed in master node
		exutil.By(fmt.Sprintf("Check that the %s rpm is installed in master node", rpmName))
		o.Expect(masterNode.RpmIsInstalled(rpmName)).
			To(o.BeTrue(),
				"Error. %s rpm is not installed after changing the osImage in master node %s.", rpmName, workerNode.GetName())
		logger.Infof("OK\n")

		// Create MC to apply usbguard extension to worker nodes
		exutil.By("Create a MC to deploy the usbgard extension in 'worker' pool")
		wUsbguardMcName := "tc-54909-extension-usbguard-worker"
		wUsbguardMC := NewMachineConfig(oc.AsAdmin(), wUsbguardMcName, MachineConfigPoolWorker).SetMCOTemplate("change-worker-extension-usbguard.yaml")
		wUsbguardMC.skipWaitForMcp = true

		defer wUsbguardMC.deleteNoWait()
		wUsbguardMC.create()

		// Create MC to apply usbguard extension to master nodes
		exutil.By("Create a MC to deploy the usbguard extension in 'master' pool")
		mUsbguardMcName := "tc-54909-extension-usbguard-master"
		mUsbguardMC := NewMachineConfig(oc.AsAdmin(), mUsbguardMcName, MachineConfigPoolMaster).SetMCOTemplate("change-worker-extension-usbguard.yaml")
		mUsbguardMC.skipWaitForMcp = true

		defer mUsbguardMC.deleteNoWait()
		mUsbguardMC.create()

		// Wait for pools
		o.Expect(wMcp.WaitForUpdatedStatus()).To(o.Succeed())
		logger.Infof("The new config was applied successfully in 'worker' pool\n")

		o.Expect(mMcp.WaitForUpdatedStatus()).To(o.Succeed())
		logger.Infof("The new config was applied successfully in 'master' pool\n")

		// Check that rpms are installed in worker node after the extension
		exutil.By("Check that both rpms are installed in worker node after the extension")
		o.Expect(workerNode.RpmIsInstalled(rpmName)).
			To(o.BeTrue(),
				"Error. %s rpm is not installed after changing the osImage in worker node %s.", rpmName, workerNode.GetName())

		o.Expect(workerNode.RpmIsInstalled(extensionRpmName)).
			To(o.BeTrue(),
				"Error. %s rpm is not installed after changing the osImage in worker node %s.", extensionRpmName, workerNode.GetName())
		logger.Infof("OK\n")

		// Check that rpms are installed in master node after the extension
		exutil.By("Check that both rpms are installed in master node after the extension")
		o.Expect(masterNode.RpmIsInstalled(rpmName)).
			To(o.BeTrue(),
				"Error. %s rpm is not installed after changing the osImage in master node %s.", rpmName, masterNode.GetName())

		o.Expect(masterNode.RpmIsInstalled(extensionRpmName)).
			To(o.BeTrue(),
				"Error. %s rpm is not installed after changing the osImage in master node %s.", extensionRpmName, masterNode.GetName())
		logger.Infof("OK\n")

		// Check rpm-ostree status in worker node after extension
		exutil.By("Check that the rpm-ostree status is reporting the right booted image in worker nodes after the extension is installed")

		wStatus, err = workerNode.GetRpmOstreeStatus(false)
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the rpm-ostree status value in master node %s after the extension is installed", workerNode.GetName())
		logger.Infof("Current rpm-ostree status in worker node after extension:\n%s\n", wStatus)
		o.Expect(wStatus).To(o.MatchRegexp("(?s)LayeredPackages:.*usbguard"),
			"Status in worker node %s is not reporting the Layered %s package", workerNode.GetName(), extensionRpmName)

		wDeployment, err = workerNode.GetBootedOsTreeDeployment(true)
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the rpm-ostree status value in worker node %s after the extension is installed", workerNode.GetName())

		wContainerRef, jerr = JSON(wDeployment).GetSafe("container-image-reference")
		o.Expect(jerr).NotTo(o.HaveOccurred(),
			"We cant get 'container-image-reference' from the deployment status in worker node after the extension is installed. Wrong rpm-ostree status!")
		o.Expect(wContainerRef.ToString()).To(o.Equal("ostree-unverified-registry:"+digestedImage),
			"container reference in the worker node's status is not the exepeced one after the extension is installed")
		logger.Infof("OK!\n")

		// Check rpm-ostree status in master node after the extension
		exutil.By("Check that the rpm-ostree status is reporting the right booted image in master nodes after the extension is installed")

		mStatus, err = masterNode.GetRpmOstreeStatus(false)
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the rpm-ostree status value in master node %s after the extension is installed", masterNode.GetName())
		logger.Infof("Current rpm-ostree status in master node:\n%s\n", mStatus)
		o.Expect(mStatus).To(o.MatchRegexp("(?s)LayeredPackages:.*usbguard"),
			"Status in master node %s is not reporting the Layered %s package", workerNode.GetName(), extensionRpmName)

		mDeployment, err = masterNode.GetBootedOsTreeDeployment(true)
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the rpm-ostree status value in master node %s the extension is installed", masterNode.GetName())

		mContainerRef, jerr = JSON(mDeployment).GetSafe("container-image-reference")
		o.Expect(jerr).NotTo(o.HaveOccurred(),
			"We cant get 'container-image-reference' from the deployment status in master node after the extension is installed. Wrong rpm-ostree status!")
		o.Expect(mContainerRef.ToString()).To(o.Equal("ostree-unverified-registry:"+digestedImage),
			"container reference in the master node's status is not the exepeced one after the extension is installed")
		logger.Infof("OK!\n")

		exutil.By("Remove custom layering MCs")
		wLayeringMC.deleteNoWait()
		mLayeringMC.deleteNoWait()
		logger.Infof("OK!\n")

		// Wait for pools
		o.Expect(wMcp.WaitForUpdatedStatus()).To(o.Succeed())
		logger.Infof("The new config was applied successfully in 'worker' pool\n")

		o.Expect(mMcp.WaitForUpdatedStatus()).To(o.Succeed())
		logger.Infof("The new config was applied successfully in 'master' pool\n")

		// Check that extension rpm is installed in the worker node, but custom layering rpm is not
		exutil.By("Check that extension rpm is installed in worker node but custom layering rpm is not")
		o.Expect(workerNode.RpmIsInstalled(rpmName)).
			To(o.BeFalse(),
				"Error. %s rpm is  installed in worker node %s but it should not be installed.", rpmName, workerNode.GetName())

		o.Expect(workerNode.RpmIsInstalled(extensionRpmName)).
			To(o.BeTrue(),
				"Error. %s rpm is not installed after changing the osImage in worker node %s.\n%s", extensionRpmName, workerNode.GetName())
		logger.Infof("OK\n")

		// Check that extension rpm is installed in the master node, but custom layering rpm is not
		exutil.By("Check that both rpms are installed in master node")

		o.Expect(masterNode.RpmIsInstalled(rpmName)).
			To(o.BeFalse(),
				"Error. %s rpm is installed in master node %s but it should not be installed.", rpmName, masterNode.GetName())

		o.Expect(masterNode.RpmIsInstalled(extensionRpmName)).
			To(o.BeTrue(),
				"Error. %s rpm is not installed after changing the osImage in master node %s.", extensionRpmName, masterNode.GetName())
		logger.Infof("OK\n")

		// Check rpm-ostree status in worker node after deleting custom osImage
		exutil.By("Check that the rpm-ostree status is reporting the right booted image in worker nodes after deleting custom osImage")

		wStatus, err = workerNode.GetRpmOstreeStatus(false)
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the rpm-ostree status value in worker node %s after deleting custom osImage", workerNode.GetName())
		logger.Infof("Current rpm-ostree status in worker node after deleting custom osImage:\n%s\n", wStatus)
		o.Expect(wStatus).To(o.MatchRegexp("(?s)LayeredPackages:.*usbguard"),
			"Status in worker node %s is not reporting the Layered %s package after deleting custom osImage", workerNode.GetName(), extensionRpmName)
		o.Expect(wStatus).NotTo(o.ContainSubstring(digestedImage),
			"Status in worker node %s is reporting the custom osImage, but it shouldn't because custom osImage was deleted", workerNode.GetName(), extensionRpmName)

		logger.Infof("OK!\n")

		// Check rpm-ostree status in master node after deleting custom  osImage
		exutil.By("Check that the rpm-ostree status is reporting the right booted image in master nodes after deleting custom osImage")

		mStatus, err = masterNode.GetRpmOstreeStatus(false)
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the rpm-ostree status value in master node %s after deleting custom osIMage", masterNode.GetName())
		logger.Infof("Current rpm-ostree status in master node:\n%s\n", mStatus)
		o.Expect(mStatus).To(o.MatchRegexp("(?s)LayeredPackages:.*usbguard"),
			"Status in master node %s is not reporting the Layered %s package after deleting custom osImage", workerNode.GetName(), extensionRpmName)
		o.Expect(mStatus).NotTo(o.ContainSubstring(digestedImage),
			"Status in master node %s is reporting the custom osImage, but it shouldn't because custom osImage was deleted", workerNode.GetName(), extensionRpmName)

		logger.Infof("OK!\n")

	})

	g.It("Author:sregidor-ConnectedOnly-Longduration-NonPreRelease-High-54915-Configure kerneltype while using a custom osImage [Disruptive]", func() {
		// Due to https://issues.redhat.com/browse/OCPBUGS-31255 in this test case pools will be degraded intermittently. They will be degraded and automatically fixed in a few minutes/seconds
		// Because of that we need to use WaitForUpdatedStatus instead of waitForComplete, since WaitForUpdatedStatus will not fail if a pool is degraded for just a few minutes but the configuration is applied properly

		architecture.SkipArchitectures(oc, architecture.MULTI, architecture.S390X, architecture.PPC64LE, architecture.ARM64)
		skipTestIfSupportedPlatformNotMatched(oc, AWSPlatform, GCPPlatform)

		var (
			rpmName            = "zsh"
			dockerFileCommands = fmt.Sprintf(`
RUN printf '[baseos]\nname=CentOS-$releasever - Base\nbaseurl=http://mirror.stream.centos.org/$releasever-stream/BaseOS/$basearch/os/\ngpgcheck=0\nenabled=1\nproxy='$HTTPS_PROXY'\n\n[appstream]\nname=CentOS-$releasever - AppStream\nbaseurl=http://mirror.stream.centos.org/$releasever-stream/AppStream/$basearch/os/\ngpgcheck=0\nenabled=1\nproxy='$HTTPS_PROXY'\n\n' > /etc/yum.repos.d/centos.repo && \
    rpm-ostree install %s && \
    rpm-ostree cleanup -m && \
    ostree container commit
`, rpmName)
			rtMcTemplate = "set-realtime-kernel.yaml"
			workerNode   = NewNodeList(oc).GetAllCoreOsWokerNodesOrFail()[0]
			masterNode   = NewNodeList(oc).GetAllMasterNodesOrFail()[0]
			wMcp         = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			mMcp         = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
		)

		mMcp.SetWaitingTimeForKernelChange()
		wMcp.SetWaitingTimeForKernelChange()
		defer mMcp.WaitForUpdatedStatus()
		defer wMcp.WaitForUpdatedStatus()

		// Create a MC to use realtime kernel in the worker pool
		exutil.By("Create machine config to enable RT kernel in worker pool")
		wRtMcName := "50-realtime-kernel-worker"
		wRtMc := NewMachineConfig(oc.AsAdmin(), wRtMcName, MachineConfigPoolWorker).SetMCOTemplate(rtMcTemplate)
		wRtMc.skipWaitForMcp = true

		defer wRtMc.deleteNoWait()
		// TODO: When we extract the "mcp.waitForComplete" from the "create" method, we need to take into account that if
		// we are configuring a rt-kernel we need to wait longer.
		wRtMc.create()
		logger.Infof("OK!\n")

		// Create a MC to use realtime kernel in the master pool
		exutil.By("Create machine config to enable RT kernel in master pool")
		mRtMcName := "50-realtime-kernel-master"
		mRtMc := NewMachineConfig(oc.AsAdmin(), mRtMcName, MachineConfigPoolMaster).SetMCOTemplate(rtMcTemplate)
		mRtMc.skipWaitForMcp = true

		defer mRtMc.deleteNoWait()
		mRtMc.create()
		logger.Infof("OK!\n")

		// Wait for the pools to be updated
		exutil.By("Wait for pools to be updated after applying the new realtime kernel")
		o.Expect(wMcp.WaitForUpdatedStatus()).To(o.Succeed())
		o.Expect(mMcp.WaitForUpdatedStatus()).To(o.Succeed())
		logger.Infof("OK!\n")

		// Check that realtime kernel is active in worker nodes
		exutil.By("Check realtime kernel in worker nodes")
		o.Expect(workerNode.IsRealTimeKernel()).Should(o.BeTrue(),
			"Kernel is not realtime kernel in worker node %s", workerNode.GetName())
		logger.Infof("OK!\n")

		// Check that realtime kernel is active in master nodes
		exutil.By("Check realtime kernel in master nodes")
		o.Expect(masterNode.IsRealTimeKernel()).Should(o.BeTrue(),
			"Kernel is not realtime kernel in master node %s", masterNode.GetName())
		logger.Infof("OK!\n")

		// Build the new osImage
		exutil.By("Build a custom osImage")
		osImageBuilder := OsImageBuilderInNode{node: workerNode, dockerFileCommands: dockerFileCommands}
		digestedImage, err := osImageBuilder.CreateAndDigestOsImage()
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error creating the new osImage")
		logger.Infof("OK\n")

		// Create MC to apply the config to worker nodes
		exutil.By("Create a MC to deploy the new osImage in 'worker' pool")
		wLayeringMcName := "tc-54915-layering-kerneltype-worker"
		wLayeringMC := NewMachineConfig(oc.AsAdmin(), wLayeringMcName, MachineConfigPoolWorker)
		wLayeringMC.parameters = []string{"OS_IMAGE=" + digestedImage}
		wLayeringMC.skipWaitForMcp = true

		defer wLayeringMC.deleteNoWait()
		wLayeringMC.create()
		logger.Infof("OK!\n")

		// Create MC to apply the config to master nodes
		exutil.By("Create a MC to deploy the new osImage in 'master' pool")
		mLayeringMcName := "tc-54915-layering-kerneltype-master"
		mLayeringMC := NewMachineConfig(oc.AsAdmin(), mLayeringMcName, MachineConfigPoolMaster)
		mLayeringMC.parameters = []string{"OS_IMAGE=" + digestedImage}
		mLayeringMC.skipWaitForMcp = true

		defer mLayeringMC.deleteNoWait()
		mLayeringMC.create()
		logger.Infof("OK!\n")

		// Wait for the pools to be updated
		exutil.By("Wait for pools to be updated after applying the new osImage")
		o.Expect(wMcp.WaitForUpdatedStatus()).To(o.Succeed())
		o.Expect(mMcp.WaitForUpdatedStatus()).To(o.Succeed())
		logger.Infof("OK!\n")

		// Check rpm is installed in worker node
		exutil.By("Check that the rpm is installed in worker node")
		o.Expect(workerNode.RpmIsInstalled(rpmName)).
			To(o.BeTrue(),
				"Error. %s rpm is not installed after changing the osImage in worker node %s.", rpmName, workerNode.GetName())

		wStatus, err := workerNode.GetRpmOstreeStatus(false)
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the rpm-ostree status value in worker node %s", masterNode.GetName())

		o.Expect(wStatus).Should(o.And(
			o.MatchRegexp("(?s)LayeredPackages:.*kernel-rt-core"),
			o.MatchRegexp("(?s)LayeredPackages:.*kernel-rt-kvm"),
			o.MatchRegexp("(?s)LayeredPackages:.*kernel-rt-modules"),
			o.MatchRegexp("(?s)LayeredPackages:.*kernel-rt-modules-extra"),

			o.MatchRegexp("(?s)RemovedBasePackages:.*kernel-core"),
			o.MatchRegexp("(?s)RemovedBasePackages:.*kernel-modules"),
			o.MatchRegexp("(?s)RemovedBasePackages:.*kernel"),
			o.MatchRegexp("(?s)RemovedBasePackages:.*kernel-modules-extra")),
			"rpm-ostree status is not reporting the kernel layered packages properly")
		logger.Infof("OK\n")

		// Check rpm is installed in master node
		exutil.By("Check that the rpm is installed in master node")
		o.Expect(masterNode.RpmIsInstalled(rpmName)).
			To(o.BeTrue(),
				"Error. %s rpm is not installed after changing the osImage in master node %s.", rpmName, workerNode.GetName())

		mStatus, err := masterNode.GetRpmOstreeStatus(false)
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the rpm-ostree status value in master node %s", masterNode.GetName())

		o.Expect(mStatus).Should(o.And(
			o.MatchRegexp("(?s)LayeredPackages:.*kernel-rt-core"),
			o.MatchRegexp("(?s)LayeredPackages:.*kernel-rt-kvm"),
			o.MatchRegexp("(?s)LayeredPackages:.*kernel-rt-modules"),
			o.MatchRegexp("(?s)LayeredPackages:.*kernel-rt-modules-extra"),

			o.MatchRegexp("(?s)RemovedBasePackages: .*kernel-core"),
			o.MatchRegexp("(?s)RemovedBasePackages: .*kernel-modules"),
			o.MatchRegexp("(?s)RemovedBasePackages: .*kernel"),
			o.MatchRegexp("(?s)RemovedBasePackages: .*kernel-modules-extra")),
			"rpm-ostree status is not reporting the kernel layered packages properly")
		logger.Infof("OK\n")

		// Check that realtime kernel is active in worker nodes
		exutil.By("Check realtime kernel in worker nodes")
		o.Expect(workerNode.IsRealTimeKernel()).Should(o.BeTrue(),
			"Kernel is not realtime kernel in worker node %s", workerNode.GetName())
		logger.Infof("OK!\n")

		// Check that realtime kernel is active in master nodes
		exutil.By("Check realtime kernel in master nodes")
		o.Expect(masterNode.IsRealTimeKernel()).Should(o.BeTrue(),
			"Kernel is not realtime kernel in master node %s", masterNode.GetName())
		logger.Infof("OK!\n")

		// Delete realtime configs
		exutil.By("Delete the realtime kernel MCs")
		wRtMc.deleteNoWait()
		mRtMc.deleteNoWait()
		logger.Infof("OK!\n")

		// Wait for the pools to be updated
		exutil.By("Wait for pools to be updated after deleting the realtime kernel configs")
		o.Expect(wMcp.WaitForUpdatedStatus()).To(o.Succeed())
		o.Expect(mMcp.WaitForUpdatedStatus()).To(o.Succeed())
		logger.Infof("OK!\n")

		// Check that realtime kernel is not active in worker nodes anymore
		exutil.By("Check realtime kernel in worker nodes")
		o.Expect(workerNode.IsRealTimeKernel()).Should(o.BeFalse(),
			"Realtime kernel should not be active anymore in worker node %s", workerNode.GetName())
		logger.Infof("OK!\n")

		// Check that realtime kernel is not active in master nodes anymore
		exutil.By("Check realtime kernel in master nodes")
		o.Expect(masterNode.IsRealTimeKernel()).Should(o.BeFalse(),
			"Realtime kernel should not be active anymore in master node %s", masterNode.GetName())
		logger.Infof("OK!\n")

		// Check rpm is installed in worker node
		exutil.By("Check that the rpm is installed in worker node")
		o.Expect(workerNode.RpmIsInstalled(rpmName)).
			To(o.BeTrue(),
				"Error. %s rpm is not installed after changing the osImage in worker node %s.", rpmName, workerNode.GetName())

		wStatus, err = workerNode.GetRpmOstreeStatus(false)
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the rpm-ostree status value in worker node %s", masterNode.GetName())

		o.Expect(wStatus).ShouldNot(o.And(
			o.ContainSubstring("LayeredPackages"),
			o.ContainSubstring("RemovedBasePackages")),
			"rpm-ostree status is not reporting the kernel layered packages properly in worker node %s", workerNode.GetName())
		logger.Infof("OK\n")

		// Check rpm is installed in master node
		exutil.By("Check that the rpm is installed in master node")
		o.Expect(masterNode.RpmIsInstalled(rpmName)).
			To(o.BeTrue(),
				"Error. %s rpm is not installed after changing the osImage in master node %s.", rpmName, workerNode.GetName())

		mStatus, err = masterNode.GetRpmOstreeStatus(false)
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the rpm-ostree status value in worker node %s", masterNode.GetName())

		o.Expect(mStatus).ShouldNot(o.And(
			o.ContainSubstring("LayeredPackages"),
			o.ContainSubstring("RemovedBasePackages")),
			"rpm-ostree status is not reporting the kernel layered packages properly in master node %s", workerNode.GetName())
		logger.Infof("OK\n")

	})

	g.It("Author:sregidor-ConnectedOnly-Longduration-NonPreRelease-Medium-55002-Get OSImageURL override related metric data available in telemetry [Disruptive]", func() {
		// Due to https://issues.redhat.com/browse/OCPBUGS-31255 in this test case pools will be degraded intermittently. They will be degraded and automatically fixed in a few minutes/seconds
		// Because of that we need to use WaitForUpdatedStatus instead of waitForComplete, since WaitForUpdatedStatus will not fail if a pool is degraded for just a few minutes but the configuration is applied properly
		var (
			osImageURLOverrideQuery = `os_image_url_override`

			dockerFileCommands = "RUN touch /etc/hello-world-file"

			workerNode = NewNodeList(oc).GetAllCoreOsWokerNodesOrFail()[0]
			wMcp       = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			mMcp       = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
		)

		exutil.By("Check that the metric is exposed to telemetry")
		expectedExposedMetric := fmt.Sprintf(`{__name__=\"%s:sum\"}`, osImageURLOverrideQuery)
		telemetryConfig := NewNamespacedResource(oc.AsAdmin(), "Configmap", "openshift-monitoring", "telemetry-config")
		o.Expect(telemetryConfig.Get(`{.data}`)).To(o.ContainSubstring(expectedExposedMetric),
			"Metric %s, is not exposed to telemetry", osImageURLOverrideQuery)

		exutil.By("Validating initial os_image_url_override values")
		mon, err := exutil.NewPrometheusMonitor(oc.AsAdmin())
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error creating new thanos monitor")

		osImageOverride, err := mon.SimpleQuery(osImageURLOverrideQuery)
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error querying metric: %s", osImageURLOverrideQuery)

		// Here we are logging both master and worker pools
		logger.Infof("Initial %s query: %s", osImageURLOverrideQuery, osImageOverride)

		logger.Infof("Validate worker pool's %s value", osImageURLOverrideQuery)
		o.Expect(wMcp.GetReportedOsImageOverrideValue()).To(o.Equal("0"),
			"Worker pool's %s initial value should be 0. Instead reported metric is: %s",
			osImageURLOverrideQuery, osImageOverride)

		logger.Infof("Validate master pool's %s value", osImageURLOverrideQuery)
		o.Expect(mMcp.GetReportedOsImageOverrideValue()).To(o.Equal("0"),
			"Master pool's %s initial value should be 0. Instead reported metric is: %s",
			osImageURLOverrideQuery, osImageOverride)
		logger.Infof("OK!\n")

		// Build the new osImage
		exutil.By("Build a custom osImage")
		osImageBuilder := OsImageBuilderInNode{node: workerNode, dockerFileCommands: dockerFileCommands}
		digestedImage, err := osImageBuilder.CreateAndDigestOsImage()
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error creating the new osImage")
		logger.Infof("OK\n")

		// Create MC to apply the config to worker nodes
		exutil.By("Create a MC to deploy the new osImage in 'worker' pool")
		wLayeringMcName := "tc-55002-layering-telemetry-worker"
		wLayeringMC := NewMachineConfig(oc.AsAdmin(), wLayeringMcName, MachineConfigPoolWorker)
		wLayeringMC.parameters = []string{"OS_IMAGE=" + digestedImage}
		wLayeringMC.skipWaitForMcp = true

		defer mMcp.WaitForUpdatedStatus()
		defer wMcp.WaitForUpdatedStatus()
		defer wLayeringMC.deleteNoWait()
		wLayeringMC.create()
		logger.Infof("OK!\n")

		// Create MC to apply the config to master nodes
		exutil.By("Create a MC to deploy the new osImage in 'master' pool")
		mLayeringMcName := "tc-55002-layering-telemetry-master"
		mLayeringMC := NewMachineConfig(oc.AsAdmin(), mLayeringMcName, MachineConfigPoolMaster)
		mLayeringMC.parameters = []string{"OS_IMAGE=" + digestedImage}
		mLayeringMC.skipWaitForMcp = true

		defer mLayeringMC.deleteNoWait()
		mLayeringMC.create()
		logger.Infof("OK!\n")

		// Wait for the pools to be updated
		exutil.By("Wait for pools to be updated after applying the new osImage")
		o.Expect(wMcp.WaitForUpdatedStatus()).To(o.Succeed())
		o.Expect(mMcp.WaitForUpdatedStatus()).To(o.Succeed())
		logger.Infof("OK!\n")

		exutil.By("Validating os_image_url_override values with overridden master and worker pools")
		osImageOverride, err = mon.SimpleQuery(osImageURLOverrideQuery)
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error querying metric: %s", osImageURLOverrideQuery)

		// Here we are logging both master and worker pools
		logger.Infof("Executed %s query: %s", osImageURLOverrideQuery, osImageOverride)

		logger.Infof("Validate worker pool's %s value", osImageURLOverrideQuery)
		o.Expect(wMcp.GetReportedOsImageOverrideValue()).To(o.Equal("1"),
			"Worker pool's %s value with overridden master and worker pools should be 1. Instead reported metric is: %s",
			osImageURLOverrideQuery, osImageOverride)

		logger.Infof("Validate master pool's %s value", osImageURLOverrideQuery)
		o.Expect(mMcp.GetReportedOsImageOverrideValue()).To(o.Equal("1"),
			"Master pool's %s value with overridden master and worker pools should be 1. Instead reported metric is: %s",
			osImageURLOverrideQuery, osImageOverride)
		logger.Infof("OK!\n")

		exutil.By("Delete the MC that overrides worker pool's osImage and wait for the pool to be updated")
		wLayeringMC.deleteNoWait()
		o.Expect(wMcp.WaitForUpdatedStatus()).To(o.Succeed())
		logger.Infof("OK!\n")

		exutil.By("Validating os_image_url_override values with overridden master pool only")
		osImageOverride, err = mon.SimpleQuery(osImageURLOverrideQuery)
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error querying metric: %s", osImageURLOverrideQuery)

		// Here we are logging both master and worker pools
		logger.Infof("Executed %s query: %s", osImageURLOverrideQuery, osImageOverride)

		logger.Infof("Validate worker pool's %s value", osImageURLOverrideQuery)
		o.Expect(wMcp.GetReportedOsImageOverrideValue()).To(o.Equal("0"),
			"Worker pool's %s value should be 0 when only the master pool is overridden. Instead reported metric is: %s",
			osImageURLOverrideQuery, osImageOverride)

		logger.Infof("Validate master pool's %s value", osImageURLOverrideQuery)
		o.Expect(mMcp.GetReportedOsImageOverrideValue()).To(o.Equal("1"),
			"Master pool's %s value should be 1 when only the master pool is overridden. Instead reported metric is: %s",
			osImageURLOverrideQuery, osImageOverride)
		logger.Infof("OK!\n")

		exutil.By("Delete the MC that overrides master pool's osImage and wait for the pool to be updated")
		mLayeringMC.deleteNoWait()
		o.Expect(mMcp.WaitForUpdatedStatus()).To(o.Succeed())
		logger.Infof("OK!\n")

		exutil.By("Validating os_image_url_override when no pool is overridden")
		osImageOverride, err = mon.SimpleQuery(osImageURLOverrideQuery)
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error querying metric: %s", osImageURLOverrideQuery)

		// Here we are logging both master and worker pools
		logger.Infof("Executed %s query: %s", osImageURLOverrideQuery, osImageOverride)

		logger.Infof("Validate worker pool's %s value", osImageURLOverrideQuery)
		o.Expect(wMcp.GetReportedOsImageOverrideValue()).To(o.Equal("0"),
			"Worker pool's %s value should be 0 when no pool is overridden. Instead reported metric is: %s",
			osImageURLOverrideQuery, osImageOverride)

		logger.Infof("Validate master pool's %s value", osImageURLOverrideQuery)
		o.Expect(mMcp.GetReportedOsImageOverrideValue()).To(o.Equal("0"),
			"Master pool's %s value should be 0 when no pool is overridden. Instead reported metric is: %s",
			osImageURLOverrideQuery, osImageOverride)
		logger.Infof("OK!\n")

	})

	g.It("Author:sregidor-ConnectedOnly-Longduration-NonPreRelease-Medium-54056-Update osImage using the internal registry to store the image [Disruptive]", func() {
		var (
			osImageNewFilePath = "/etc/hello-tc-54056"
			dockerFileCommands = fmt.Sprintf(`
RUN touch %s
`, osImageNewFilePath)
			mMcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
		)

		architecture.SkipArchitectures(oc, architecture.MULTI, architecture.S390X, architecture.PPC64LE)

		// Select the nodes
		builderNode := mMcp.GetNodesOrFail()[0] // We always build the image in a master node to make sure it is CoreOs

		// We test the image in a compact/sno compatible pool
		mcp := GetCompactCompatiblePool(oc.AsAdmin())
		if len(mcp.GetCoreOsNodesOrFail()) == 0 {
			logger.Infof("The worker pool has no CoreOs nodes, we will use master pool for testing the osImage")
			mcp = mMcp
		}

		node := mcp.GetCoreOsNodesOrFail()[0]

		logger.Infof("Using pool %s and node %s for testing", mcp.GetName(), node.GetName())

		// Build the new osImage
		osImageBuilder := OsImageBuilderInNode{node: builderNode, dockerFileCommands: dockerFileCommands}
		osImageBuilder.UseInternalRegistry = true
		defer func() { _ = osImageBuilder.CleanUp() }()
		digestedImage, err := osImageBuilder.CreateAndDigestOsImage()
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error creating the new osImage")
		logger.Infof("Digested Image: %s", digestedImage)
		logger.Infof("OK\n")

		// Create MC and wait for MCP
		exutil.By("Create a MC to deploy the new osImage")
		layeringMcName := "layering-mc"
		layeringMC := NewMachineConfig(oc.AsAdmin(), layeringMcName, mcp.GetName())
		layeringMC.parameters = []string{"OS_IMAGE=" + digestedImage}

		defer layeringMC.delete()
		layeringMC.create()

		mcp.waitForComplete()
		logger.Infof("The new osImage was deployed successfully\n")
		logger.Infof("OK\n")

		// Check image content
		exutil.By("Load remote resources to verify that the osImage content has been deployed properly")

		tc54056File := NewRemoteFile(node, osImageNewFilePath)
		o.Expect(tc54056File.Exists()).To(o.BeTrue(),
			"The file %s included in the osImage should exist in the node %s, but it does not", osImageNewFilePath, node.GetName())

		o.Expect(tc54056File.Fetch()).To(o.Succeed(),
			"The content of file %s could not be retreived from node %s", osImageNewFilePath, node.GetName())

		o.Expect(tc54056File.GetTextContent()).To(o.BeEmpty(),
			"The file %s should be empty, but it is not. Current content: %s", osImageNewFilePath, tc54056File.GetTextContent())
		logger.Infof("OK\n")

		// Delete the MC and wait for MCP
		exutil.By("Delete the MC so that the original osImage is restored")
		layeringMC.delete()
		mcp.waitForComplete()
		logger.Infof("MC was successfully deleted\n")
		logger.Infof("OK\n")

		exutil.By("Check that the included new content is not present anymore")
		o.Expect(tc54056File.Exists()).To(o.BeFalse(),
			"The file %s included in the osImage should exist in the node %s, but it does not", osImageNewFilePath, node.GetName())
		logger.Infof("OK\n")
	})

	g.It("Author:sregidor-ConnectedOnly-Longduration-NonPreRelease-High-67789-Configure 64k-pages kerneltype while using a custom osImage [Disruptive]", func() {
		var (
			mcTemplate64k      = "set-64k-pages-kernel.yaml"
			rpmName            = "zsh"
			dockerFileCommands = fmt.Sprintf(`
RUN printf '[baseos]\nname=CentOS-$releasever - Base\nbaseurl=http://mirror.stream.centos.org/$releasever-stream/BaseOS/$basearch/os/\ngpgcheck=0\nenabled=1\nproxy='$HTTPS_PROXY'\n\n[appstream]\nname=CentOS-$releasever - AppStream\nbaseurl=http://mirror.stream.centos.org/$releasever-stream/AppStream/$basearch/os/\ngpgcheck=0\nenabled=1\nproxy='$HTTPS_PROXY'\n\n' > /etc/yum.repos.d/centos.repo && \
    rpm-ostree install %s && \
    rpm-ostree cleanup -m && \
    ostree container commit
`, rpmName)
		)

		architecture.SkipIfNoNodeWithArchitectures(oc.AsAdmin(), architecture.ARM64)
		clusterinfra.SkipTestIfNotSupportedPlatform(oc.AsAdmin(), clusterinfra.GCP)

		createdCustomPoolName := fmt.Sprintf("mco-test-%s", architecture.ARM64)
		defer DeleteCustomMCP(oc.AsAdmin(), createdCustomPoolName)

		mcp, nodes := GetPoolAndNodesForArchitectureOrFail(oc.AsAdmin(), createdCustomPoolName, architecture.ARM64, 1)
		node := nodes[0]

		mcp.SetWaitingTimeForKernelChange() // Increase waiting time

		// Create a MC to use 64k-pages kernel
		exutil.By("Create machine config to enable 64k-pages kernel")
		mcName64k := fmt.Sprintf("tc-67789-64k-pages-kernel-%s", mcp.GetName())
		mc64k := NewMachineConfig(oc.AsAdmin(), mcName64k, mcp.GetName()).SetMCOTemplate(mcTemplate64k)

		defer mc64k.delete()
		mc64k.create()
		logger.Infof("OK!\n")

		// Check that 64k-pages kernel is active
		exutil.By("Check 64k-pages kernel")
		o.Expect(node.Is64kPagesKernel()).Should(o.BeTrue(),
			"Kernel is not 64k-pages kernel in node %s", node.GetName())
		logger.Infof("OK!\n")

		// Build the new osImage
		exutil.By("Build a custom osImage")
		osImageBuilder := OsImageBuilderInNode{node: node, dockerFileCommands: dockerFileCommands}
		digestedImage, err := osImageBuilder.CreateAndDigestOsImage()
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error creating the new osImage")
		logger.Infof("OK\n")

		// Create a MC to apply the config
		exutil.By("Create a MC to deploy the new osImage")
		layeringMcName := fmt.Sprintf("tc-67789-layering-64kpages-%s", mcp.GetName())
		layeringMC := NewMachineConfig(oc.AsAdmin(), layeringMcName, mcp.GetName())
		layeringMC.parameters = []string{"OS_IMAGE=" + digestedImage}
		layeringMC.skipWaitForMcp = true

		defer layeringMC.deleteNoWait()
		layeringMC.create()
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		// Check that the expected (zsh+64k-pages kernel) rpms are installed
		exutil.By("Check that all the expected rpms are installed")
		o.Expect(
			node.RpmIsInstalled(rpmName),
		).To(o.BeTrue(),
			"Error. %s rpm is not installed after changing the osImage in node %s.", rpmName, node.GetName())

		o.Expect(
			node.GetRpmOstreeStatus(false),
		).Should(o.And(
			o.MatchRegexp("(?s)LayeredPackages:.*kernel-64k-core"),
			o.MatchRegexp("(?s)LayeredPackages:.*kernel-64k-modules"),
			o.MatchRegexp("(?s)LayeredPackages:.*kernel-64k-modules-extra"),
			o.MatchRegexp("(?s)RemovedBasePackages:.*kernel-core"),
			o.MatchRegexp("(?s)RemovedBasePackages:.*kernel-modules"),
			o.MatchRegexp("(?s)RemovedBasePackages:.*kernel "),
			o.MatchRegexp("(?s)RemovedBasePackages:.*kernel-modules-extra")),
			"rpm-ostree status is not reporting the kernel layered packages properly")
		logger.Infof("OK\n")

		// Check that 64k-pages kernel is active
		exutil.By("Check 64k-pages kernel")
		o.Expect(node.Is64kPagesKernel()).Should(o.BeTrue(),
			"Kernel is not 64k-pages kernel in node %s", node.GetName())
		logger.Infof("OK!\n")

		// Delete 64k-pages config
		exutil.By("Delete the 64k-pages kernel MC")
		mc64k.delete()
		logger.Infof("OK!\n")

		// Check that 64k-pages kernel is not installed anymore
		exutil.By("Check that 64k-pages kernel is not installed anymore")
		o.Expect(node.Is64kPagesKernel()).Should(o.BeFalse(),
			"Huge pages kernel should not be installed anymore in node %s", node.GetName())
		logger.Infof("OK!\n")

		// Check zsh rpm is installed
		exutil.By("Check that the zsh rpm is still installed after we removed the 64k-pages kernel")
		o.Expect(node.RpmIsInstalled(rpmName)).
			To(o.BeTrue(),
				"Error. %s rpm is not installed after changing the osImage in node %s.", rpmName, node.GetName())

		o.Expect(
			node.GetRpmOstreeStatus(false),
		).ShouldNot(o.And(
			o.ContainSubstring("LayeredPackages"),
			o.ContainSubstring("RemovedBasePackages")),
			"rpm-ostree status is not reporting the layered packages properly in node %s", node.GetName())
		logger.Infof("OK\n")
	})
})

// oc: the CLI
// image: the layered image that will be configured in the MC
// layeringMcName: the name of the MC
// expectedNDMessage: expected value for the message in the MCP NodeDegraded condition
// expectedNDReason: expected value for the reason in the MCP NodeDegraded condition
func checkInvalidOsImagesDegradedStatus(oc *exutil.CLI, image, layeringMcName, expectedNDMessage, expectedNDReason string) {
	var (
		mcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
	)
	// Create MC and wait for MCP
	layeringMC := NewMachineConfig(oc.AsAdmin(), layeringMcName, mcp.GetName())
	layeringMC.parameters = []string{"OS_IMAGE=" + image}
	layeringMC.skipWaitForMcp = true

	validateMcpNodeDegraded(layeringMC, mcp, expectedNDMessage, expectedNDReason, false)

}
