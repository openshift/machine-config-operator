package extended

import (
	"fmt"
	"math"
	"strings"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO Pinnedimages", func() {
	defer g.GinkgoRecover()

	var (
		oc   = exutil.NewCLI("mco-pinnedimages", exutil.KubeConfigPath())
		wMcp *MachineConfigPool
		mMcp *MachineConfigPool
		// Compact compatible MCP. If the node is compact/SNO this variable will be the master pool, else it will be the worker pool
		mcp *MachineConfigPool
	)

	g.JustBeforeEach(func() {

		wMcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		mMcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
		mcp = GetCompactCompatiblePool(oc.AsAdmin())
		logger.Infof("%s %s %s", wMcp, mMcp, mcp)

		PreChecks(oc)
	})

	g.It("[PolarionID:81917][OTP] Pinned images when disk-pressure", func() {
		var (
			waitForPinned                        = time.Minute * 5
			pinnedImageSetName                   = "tc-73659-pin-images-disk-pressure"
			pinnedImageName                      = BusyBoxImage
			allNodes                             = mcp.GetNodesOrFail()
			node                                 = allNodes[0]
			cleanFileTimedService                = generateTemplateAbsolutePath("tc-73659-clean-file-timed.service")
			cleanFileTimedServiceDestinationPath = "/etc/systemd/system/tc-73659-clean-file-timed.service"
		)

		exutil.By("Get disk usage in node")
		diskUsage, err := node.GetFileSystemSpaceUsage("/var/lib/containers/storage/")
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Cannot get the disk usage in node %s", node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Create a timed service that will restore the original disk usage after 5 minutes")
		logger.Infof("Copy the service in the node")
		defer node.DebugNodeWithChroot("rm", cleanFileTimedServiceDestinationPath)
		o.Expect(node.CopyFromLocal(cleanFileTimedService, cleanFileTimedServiceDestinationPath)).
			NotTo(o.HaveOccurred(),
				"Error copying  %s to %s in node %s", cleanFileTimedService, cleanFileTimedServiceDestinationPath, node.GetName())
		// We create transient timer that will execute the sercive, this service will restore the disk usage to its original usage
		logger.Infof("Create a transient timer to execute the service after 5 mintues")
		// If an error happens, the transient timer will not be deleted unless we execute this command
		defer node.DebugNodeWithChroot("systemctl", "reset-failed", "tc-73659-clean-file-timed.service")
		defer node.DebugNodeWithChroot("systemctl", "stop", "tc-73659-clean-file-timed.service")
		_, err = node.DebugNodeWithChroot("systemd-run", `--on-active=5minutes`, `--unit=tc-73659-clean-file-timed.service`)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the transient timer")
		logger.Infof("OK!\n")

		exutil.By("Create a huge file so that the node reports disk pressure. Use about 90 per cent of the free space in the disk")
		fileSize := ((diskUsage.Avail + diskUsage.Used) * 9 / 10) - diskUsage.Used // calculate the file size to use a 90% of the disk space
		o.Expect(fileSize).To(o.And(
			o.BeNumerically("<", diskUsage.Avail),
			o.BeNumerically(">", 0)),
			"Error not enough space on device to execute this test. Available: %d, Used %d", diskUsage.Avail, diskUsage.Used)
		_, err = node.DebugNodeWithChroot("fallocate", "-l", fmt.Sprintf("%d", fileSize), "/var/lib/containers/storage/tc-73659-huge-test-file.file")
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a file to trigger disk pressure")
		logger.Infof("OK!\n")

		exutil.By("Wait for disk pressure to be reported")
		// It makes no sense to wait longer than the 5 minutes time out that we use to fix the disk usage.
		// If we need to increse this timeout, we need to increase the transiente timer too
		o.Eventually(node, "5m", "20s").Should(HaveConditionField("DiskPressure", "status", TrueString),
			"Node is not reporting DiskPressure, but it should.\n%s", node.PrettyString())
		logger.Infof("OK!\n")

		exutil.By("Pin images")
		pis, err := CreateGenericPinnedImageSet(oc.AsAdmin(), pinnedImageSetName, mcp.GetName(), []string{pinnedImageName})
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating pinnedimageset %s", pis)
		defer pis.DeleteAndWait(waitForPinned)
		logger.Infof("OK!\n")

		exutil.By("Check the degraded status")
		logger.Infof("Check that the node with disk pressure is reporting pinnedimagesetdegraded status")
		mcn := node.GetMachineConfigNode()
		o.Eventually(mcn, "2m", "20s").Should(HaveConditionField("PinnedImageSetsDegraded", "status", TrueString),
			"MachineConfigNode was not degraded.\n%s\n%s", mcn.PrettyString(), node.PrettyString())
		o.Eventually(mcn, "2m", "20s").Should(HaveConditionField("PinnedImageSetsDegraded", "reason", "PrefetchFailed"),
			"MachineConfigNode was not degraded with the expected reason.\n%s\n%s", mcn.PrettyString(), node.PrettyString())
		o.Eventually(mcn, "2m", "20s").Should(HaveConditionField("PinnedImageSetsDegraded", "message", "One or more PinnedImageSet is experiencing an error. See PinnedImageSet list for more details."),
			"MachineConfigNode was not degraded with the expected message.\n%s\n%s", mcn.PrettyString(), node.PrettyString())
		o.Eventually(mcn.GetPinnedImageSetLastFailedError, "2m", "20s").Should(o.ContainSubstring(`node `+node.GetName()+` is reporting OutOfDisk=True`),
			"MachineConfigNode was not degraded with the expected message.\n%s\n%s", mcn.PrettyString(), node.PrettyString())
		logger.Infof("Check that the rest of the nodes could pin the image and are not degraded")
		for _, n := range allNodes {
			if n.GetName() != node.GetName() {
				logger.Infof("Checking node %s", n.GetName())
				o.Eventually(n.GetMachineConfigNode, "2m", "20s").Should(HaveConditionField("PinnedImageSetsDegraded", "status", FalseString),
					"MachineConfigNode was degraded.\n%s\n%s", node.GetMachineConfigNode().PrettyString(), node.PrettyString())
				rmi := NewRemoteImage(n, pinnedImageName)
				o.Eventually(rmi.IsPinned, "5m", "20s").Should(o.BeTrue(), "%s should be pinned but it is not", rmi)
			}
		}
		logger.Infof("OK!\n")

		exutil.By("Wait for disk pressure to be fixed") // It should be fixed by the timed service that was created before
		o.Eventually(node, "20m", "20s").Should(HaveConditionField("DiskPressure", "status", FalseString),
			"Node is reporting DiskPressure, but it should not.\n%s", node.PrettyString())
		logger.Infof("OK!\n")

		exutil.By("Check that the degraded status was fixed")
		o.Eventually(mcn, "6m", "20s").Should(HaveConditionField("PinnedImageSetsDegraded", "status", FalseString),
			"MachineConfigNode was not degraded.\n%s\n%s", mcn.PrettyString(), node.PrettyString())
		o.Eventually(NewRemoteImage(node, pinnedImageName).IsPinned, "5m", "20s").Should(o.BeTrue(),
			"The degraded status was fixed, but the image was not pinned")
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:73623][OTP] Pin images", func() {
		var (
			pinnedImageSetName = fmt.Sprintf("tc-%s-pinned-image", GetCurrentTestPolarionIDNumber())
		)
		basicPinnedImageTest(mcp, pinnedImageSetName)

	})

	// Disconnected clusters use an imagecontentsourcepolicy to mirror the images in openshifttest. In this test cases we create an ImageDigestMirrorSet to mirror the same images and it is not supported
	// Hence we skip this test case in disconnected clusters
	g.It("[PolarionID:81921][OTP][Skipped:Disconnected] Pinned images with a ImageDigestMirrorSet mirroring a single repository", func() {
		var (
			idmsName    = "tc-73653-mirror-single-repository"
			idmsMirrors = `[{"mirrors":["quay.io/openshifttest/busybox"], "source": "example-repo.io/digest-example/mybusy", "mirrorSourcePolicy":"NeverContactSource"}]`
			// actually  quay.io/openshifttest/busybox@sha256:c5439d7db88ab5423999530349d327b04279ad3161d7596d2126dfb5b02bfd1f but using our configured mirror instead
			pinnedImage        = strings.Replace(BusyBoxImage, "quay.io/openshifttest/busybox", "example-repo.io/digest-example/mybusy", 1)
			pinnedImageSetName = "tc-73653-mirror-single-repository"
		)

		DigestMirrorTest(oc, mcp, idmsName, idmsMirrors, pinnedImage, pinnedImageSetName)
	})

	// Disconnected clusters use an imagecontentsourcepolicy to mirror the images in openshifttest. In this test cases we create an ImageDigestMirrorSet to mirror the same images and it is not supported
	// Hence we skip this test case in disconnected clusters
	g.It("[PolarionID:81919][OTP][Skipped:Disconnected] Pinned images with a ImageDigestMirrorSet mirroring a domain", func() {
		var (
			idmsName    = "tc-73657-mirror-domain"
			idmsMirrors = `[{"mirrors":["quay.io:443"], "source": "example-domain.io:443", "mirrorSourcePolicy":"NeverContactSource"}]`
			// actually  quay.io/openshifttest/busybox@sha256:c5439d7db88ab5423999530349d327b04279ad3161d7596d2126dfb5b02bfd1f but using our configured mirror instead
			pinnedImage        = strings.Replace(BusyBoxImage, "quay.io", "example-domain.io:443", 1)
			pinnedImageSetName = "tc-73657-mirror-domain"
		)

		DigestMirrorTest(oc, mcp, idmsName, idmsMirrors, pinnedImage, pinnedImageSetName)
	})

	g.It("[PolarionID:81955][OTP] Pinnedimageset invalid pinned images", func() {
		var (
			invalidPinnedImage = "quay.io/openshiftfake/fakeimage@sha256:0415f56ccc05526f2af5a7ae8654baec97d4a614f24736e8eef41a4591f08019"
			pinnedImageSetName = "tc-73361-invalid-pinned-image"
			waitForPinned      = 10 * time.Minute
		)

		exutil.By("Pin invalid image")
		pis, err := CreateGenericPinnedImageSet(oc.AsAdmin(), pinnedImageSetName, mcp.GetName(), []string{invalidPinnedImage})
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating pinnedimageset %s", pis)
		defer pis.DeleteAndWait(waitForPinned)
		logger.Infof("OK!\n")

		exutil.By("Check that MCNs are PinnedImageSetDegraded")
		for _, node := range mcp.GetNodesOrFail() {
			mcn := node.GetMachineConfigNode()
			o.Eventually(mcn, "2m", "20s").Should(HaveConditionField("PinnedImageSetsDegraded", "status", TrueString))
			o.Eventually(mcn, "2m", "20s").Should(HaveConditionField("PinnedImageSetsDegraded", "reason", "PrefetchFailed"))
		}
		logger.Infof("OK!\n")

		exutil.By("Remove the pinnedimageset")
		o.Expect(pis.Delete()).To(o.Succeed(), "Error removing %s", pis)
		logger.Infof("OK!\n")

		exutil.By("Check that MCNs are not PinnedImageSetDegraded anymore")
		for _, node := range mcp.GetNodesOrFail() {
			mcn := node.GetMachineConfigNode()
			o.Eventually(mcn, "2m", "20s").Should(HaveConditionField("PinnedImageSetsDegraded", "status", FalseString))
		}
		logger.Infof("OK!\n")
	})

	// Due to https://issues.redhat.com/browse/OCPBUGS-57473 if this test cases is executed after an OCL test is executed the cluster becomes unhealthy
	g.It("[PolarionID:81926][OTP] Pinned images garbage collection", func() {
		var (
			waitForPinned       = time.Minute * 5
			pinnedImageSetName  = "tc-73631-pinned-images-garbage-collector"
			gcKubeletConfig     = `{"imageMinimumGCAge": "0s", "imageGCHighThresholdPercent": 2, "imageGCLowThresholdPercent": 1}`
			kcTemplate          = generateTemplateAbsolutePath("generic-kubelet-config.yaml")
			kcName              = "tc-73631-pinned-garbage-collector"
			node                = mcp.GetNodesOrFail()[0]
			startTime           = node.GetDateOrFail()
			pinnedImage         = NewRemoteImage(node, BusyBoxImage)
			manuallyPulledImage = NewRemoteImage(node, AlpineImage)
		)

		exutil.By("Remove the test images")
		_ = pinnedImage.Rmi()
		_ = manuallyPulledImage.Rmi()
		logger.Infof("OK!\n")

		exutil.By("Configure kubelet to start garbage collection")
		logger.Infof("Create worker KubeletConfig")
		kc := NewKubeletConfig(oc.AsAdmin(), kcName, kcTemplate)
		defer mcp.waitForComplete()
		defer kc.Delete()
		kc.create("KUBELETCONFIG="+gcKubeletConfig, "POOL="+mcp.GetName())

		exutil.By("Wait for configurations to be applied in worker pool")
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		logger.Infof("Pin image")
		pis, err := CreateGenericPinnedImageSet(oc.AsAdmin(), pinnedImageSetName, mcp.GetName(), []string{pinnedImage.ImageName})
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating pinnedimageset %s", pis)
		defer pis.DeleteAndWait(waitForPinned)
		logger.Infof("OK!\n")

		exutil.By("Wait for all images to be pinned")
		o.Expect(mcp.waitForPinComplete(waitForPinned)).To(o.Succeed(), "Pinned image operation is not completed in %s", mcp)
		logger.Infof("OK!\n")

		exutil.By("Manually pull image")
		o.Expect(manuallyPulledImage.Pull()).To(o.Succeed(),
			"Error pulling %s", manuallyPulledImage)
		logger.Infof("Check that the manually pulled image is not pinned")
		o.Expect(manuallyPulledImage.IsPinned()).To(o.BeFalse(),
			"Error, %s is pinned, but it should not", manuallyPulledImage)
		logger.Infof("OK!\n")

		exutil.By("Check that the manually pulled image is garbage collected")
		o.Eventually(manuallyPulledImage, "25m", "20s").ShouldNot(Exist(),
			"Error, %s has not been garbage collected", manuallyPulledImage)
		logger.Infof("OK!\n")

		exutil.By("Check that the pinned image is still pinned after garbage collection")
		o.Eventually(pinnedImage.IsPinned, "2m", "10s").Should(o.BeTrue(),
			"Error, after the garbage collection happened %s is not pinned anymore", pinnedImage)
		logger.Infof("OK!\n")

		exutil.By("Reboot node")
		o.Expect(node.Reboot()).To(o.Succeed(),
			"Error rebooting node %s", node.GetName())
		o.Eventually(node.GetUptime, "15m", "30s").Should(o.BeTemporally(">", startTime),
			"%s was not properly rebooted", node)
		logger.Infof("OK!\n")

		exutil.By("Check that the pinned image is still pinned after reboot")
		o.Expect(pinnedImage.IsPinned()).To(o.BeTrue(),
			"Error, after the garbage collection happened %s is not pinned anymore", pinnedImage)
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:81925][OTP] Pod can use pinned images while no access to the registry", func() {
		var (
			waitForPinned      = time.Minute * 5
			pinnedImageSetName = "tc-73635-pinned-images-no-registry"
			// We pin the current release's tools image
			// if we cannot get the "tools" image it means we are in a disconnected cluster
			// and in disconnected clusters openshifttest images are mirrored and need the credentials for the mirror too
			// so if we cannot get the "tools" image we can use the "busybox" one.
			pinnedImage = getCurrentReleaseInfoImageSpecOrDefault(oc.AsAdmin(), "tools", BusyBoxImage)
			allNodes    = mcp.GetNodesOrFail()
			pullSecret  = GetPullSecret(oc.AsAdmin())

			deploymentName      = "tc-73635-test"
			deploymentNamespace = oc.Namespace()
			deployment          = NewNamespacedResource(oc, "deployment", deploymentNamespace, deploymentName)
			scaledReplicas      = 5
			nodeList            = NewNamespacedResourceList(oc.AsAdmin(), "pod", deploymentNamespace)
		)
		defer nodeList.PrintDebugCommand() // for debugging purpose in case of failed deployment

		exutil.By("Remove the image from all nodes in the pool")
		for _, node := range allNodes {
			// We ignore errors, since the image can be present or not in the nodes
			_ = NewRemoteImage(node, pinnedImage).Rmi()
		}
		logger.Infof("OK!\n")

		exutil.By("Create pinnedimageset")
		pis, err := CreateGenericPinnedImageSet(oc.AsAdmin(), pinnedImageSetName, mcp.GetName(), []string{pinnedImage})
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating pinnedimageset %s", pis)
		defer pis.DeleteAndWait(waitForPinned)
		logger.Infof("OK!\n")

		exutil.By("Wait for all images to be pinned")
		o.Expect(mcp.waitForPinComplete(waitForPinned)).To(o.Succeed(), "Pinned image operation is not completed in %s", mcp)
		logger.Infof("OK!\n")

		exutil.By("Check that the image was pinned in all nodes in the pool")
		for _, node := range allNodes {
			ri := NewRemoteImage(node, pinnedImage)
			logger.Infof("Checking %s", ri)
			o.Expect(ri.IsPinned()).To(o.BeTrue(),
				"%s is not pinned, but it should. %s")
		}
		logger.Infof("OK!\n")

		exutil.By("Capture the current pull-secret value")
		// We don't use the pullSecret resource directly, instead we use auxiliary functions that will
		// extract and restore the secret's values using a file. Like that we can recover the value of the pull-secret
		// if our execution goes wrong, without printing it in the logs (for security reasons).
		secretFile, err := getPullSecret(oc)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the pull-secret")
		logger.Debugf("Pull-secret content stored in file %s", secretFile)
		defer func() {
			logger.Infof("Restoring initial pull-secret value")
			output, err := setDataForPullSecret(oc, secretFile)
			if err != nil {
				logger.Errorf("Error restoring the pull-secret's value. Error: %s\nOutput: %s", err, output)
			}
			wMcp.waitForComplete()
			mMcp.waitForComplete()
		}()
		logger.Infof("OK!\n")

		exutil.By("Set an empty pull-secret")
		o.Expect(pullSecret.SetDataValue(".dockerconfigjson", "{}")).To(o.Succeed(),
			"Error setting an empty pull-secret value")
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check that the image is pinned")
		for _, node := range allNodes {
			logger.Infof("Checking node %s", node.GetName())
			ri := NewRemoteImage(node, pinnedImage)
			o.Expect(ri.IsPinned()).To(o.BeTrue(),
				"%s is not pinned, but it should. %s")
		}
		logger.Infof("OK!\n")

		exutil.By("Create test deployment")
		defer deployment.Delete()
		o.Expect(
			NewMCOTemplate(oc.AsAdmin(), "create-deployment.yaml").Create("-p", "NAME="+deploymentName, "IMAGE="+pinnedImage, "NAMESPACE="+deploymentNamespace),
		).To(o.Succeed(),
			"Error creating the deployment")
		o.Eventually(deployment, "6m", "15s").Should(BeAvailable(),
			"Resource is NOT available:\n/%s", deployment.PrettyString())
		o.Eventually(deployment.Get, "6m", "15s").WithArguments(`{.status.readyReplicas}`).Should(o.Equal(deployment.GetOrFail(`{.spec.replicas}`)),
			"Resource is NOT stable, still creating replicas:\n/%s", deployment.PrettyString())
		logger.Infof("OK!\n")

		exutil.By("Scale app")
		o.Expect(
			deployment.Patch("merge", fmt.Sprintf(`{"spec":{"replicas":%d}}`, scaledReplicas)),
		).To(o.Succeed(),
			"Error scaling %s", deployment)
		o.Eventually(deployment, "6m", "15s").Should(BeAvailable(),
			"Resource is NOT available:\n/%s", deployment.PrettyString())
		o.Eventually(deployment.Get, "6m", "15s").WithArguments(`{.status.readyReplicas}`).Should(o.Equal(deployment.GetOrFail(`{.spec.replicas}`)),
			"Resource is NOT stable, still creating replicas:\n/%s", deployment.PrettyString())
		logger.Infof("OK!\n")

		exutil.By("Reboot nodes")
		for _, node := range allNodes {
			o.Expect(node.Reboot()).To(o.Succeed(), "Error rebooting node %s", node)
		}
		for _, node := range allNodes {
			_, err := node.DebugNodeWithChroot("hostname")
			o.Expect(err).NotTo(o.HaveOccurred(), "Node %s was not recovered after rebot", node)
		}
		logger.Infof("OK!\n")

		exutil.By("Check that the applicaion is OK after the reboot")
		o.Eventually(deployment, "6m", "15s").Should(BeAvailable(),
			"Resource is NOT available:\n/%s", deployment.PrettyString())
		o.Eventually(deployment.Get, "6m", "15s").WithArguments(`{.status.readyReplicas}`).Should(o.Equal(deployment.GetOrFail(`{.spec.replicas}`)),
			"Resource is NOT stable, still creating replicas:\n/%s", deployment.PrettyString())
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:81956][OTP][Skipped:Disconnected] Pin release images", func() {
		var (
			waitForPinned            = time.Minute * 30
			pinnedImageSetName       = "tc-73630-pinned-imageset-release"
			pinnedImages             = RemoveDuplicates(getReleaseInfoPullspecOrFail(oc.AsAdmin()))
			node                     = mcp.GetNodesOrFail()[0]
			minGigasAvailableInNodes = 40
		)

		skipIfDiskSpaceLessThanBytes(node, "/var/lib/containers/storage/", int64(float64(minGigasAvailableInNodes)*(math.Pow(1024, 3))))

		exutil.By("Create pinnedimageset to pin all pullSpec images")
		pis, err := CreateGenericPinnedImageSet(oc.AsAdmin(), pinnedImageSetName, mcp.GetName(), pinnedImages)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating pinnedimageset %s", pis)
		defer pis.DeleteAndWait(waitForPinned)
		logger.Infof("OK!\n")

		exutil.By("Wait for all images to be pinned")
		o.Expect(mcp.waitForPinComplete(waitForPinned)).To(o.Succeed(), "Pinned image operation is not completed in %s", mcp)
		logger.Infof("OK!\n")

		exutil.By("Check that all images were pinned")
		for _, image := range pinnedImages {
			ri := NewRemoteImage(node, image)
			o.Expect(ri.IsPinned()).To(o.BeTrue(),
				"%s is not pinned, but it should. %s")
		}
		logger.Infof("OK!\n")

	})

	g.It("[PolarionID:73648][OTP] A rebooted node reconciles with the pinned images status", func() {
		var (
			waitForPinned      = time.Minute * 5
			pinnedImageSetName = "tc-73648-pinned-image"
			pinnedImage        = BusyBoxImage
			allMasters         = mMcp.GetNodesOrFail()
			pullSecret         = GetPullSecret(oc.AsAdmin())
		)

		exutil.By("Create pinnedimageset")
		pis, err := CreateGenericPinnedImageSet(oc.AsAdmin(), pinnedImageSetName, mMcp.GetName(), []string{pinnedImage})
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating pinnedimageset %s", pis)
		defer pis.DeleteAndWait(waitForPinned)
		logger.Infof("OK!\n")

		exutil.By("Wait for all images to be pinned")
		o.Expect(mMcp.waitForPinComplete(waitForPinned)).To(o.Succeed(), "Pinned image operation is not completed in %s", mMcp)
		logger.Infof("OK!\n")

		exutil.By("Check that the image was pinned in all nodes in the pool")
		for _, node := range allMasters {
			ri := NewRemoteImage(node, pinnedImage)
			logger.Infof("Checking %s", ri)
			o.Expect(ri.IsPinned()).To(o.BeTrue(),
				"%s is not pinned, but it should", ri)
		}
		logger.Infof("OK!\n")

		exutil.By("Capture the current pull-secret value")
		secretFile, err := getPullSecret(oc)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the pull-secret")
		logger.Debugf("Pull-secret content stored in file %s", secretFile)
		defer func() {
			logger.Infof("Restoring initial pull-secret value")
			output, err := setDataForPullSecret(oc, secretFile)
			if err != nil {
				logger.Errorf("Error restoring the pull-secret's value. Error: %v\nOutput: %s", err, output)
			}
			wMcp.waitForComplete()
			mMcp.waitForComplete()
		}()
		logger.Infof("OK!\n")

		exutil.By("Set an empty pull-secret")
		o.Expect(pullSecret.SetDataValue(".dockerconfigjson", "{}")).To(o.Succeed(),
			"Error setting an empty pull-secret value")
		mMcp.waitForComplete()
		wMcp.waitForComplete()
		logger.Infof("OK!\n")

		// find the node with the machine-config-controller
		exutil.By("Get the mcc node")
		var mcc = NewController(oc.AsAdmin())
		mccMaster, err := mcc.GetNode()
		o.Expect(err).NotTo(o.HaveOccurred(), "Cannot get the node where the MCO controller is running")
		logger.Infof("OK!\n")

		// reboot the node with mcc
		exutil.By("Reboot node")
		startTime := mccMaster.GetDateOrFail()
		o.Expect(mccMaster.Reboot()).To(o.Succeed(), "Error rebooting node %s", mccMaster)
		logger.Infof("OK!\n")

		// delete the pinnedImageSet
		exutil.By("Delete the pinnedimageset")
		o.Eventually(pis.Delete, "13m", "20s").ShouldNot(o.HaveOccurred(), "Error deleting pinnedimageset %s", pis)
		logger.Infof("OK!\n")

		// wait for the rebooted node
		exutil.By("Wait for the rebooted node")
		o.Eventually(mccMaster.GetUptime, "15m", "30s").Should(o.BeTemporally(">", startTime),
			"%s was not properly rebooted", mccMaster)
		mMcp.waitForComplete()
		o.Expect(mMcp.waitForPinComplete(waitForPinned)).To(o.Succeed(), "Pinned image operation is not completed in %s", mMcp)
		logger.Infof("OK!\n")

		// check pinned imageset is deleted in all nodes in the pool
		exutil.By("Check that the images are not pinned in all nodes in the pool")
		for _, node := range allMasters {
			ri := NewRemoteImage(node, pinnedImage)
			logger.Infof("Checking %s", ri)
			o.Eventually(ri.IsPinned, "5m", "20s").Should(o.BeFalse(),
				"%s is pinned, but it should not", ri)
		}
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:80334][OTP] Pin images in custom MCP", func() {
		var (
			customPoolName     = fmt.Sprintf("infra-mcp-%s", GetCurrentTestPolarionIDNumber())
			pinnedImageSetName = fmt.Sprintf("tc-%s-pinned-image", GetCurrentTestPolarionIDNumber())
		)
		defer DeleteCustomMCP(oc.AsAdmin(), customPoolName)
		infraMcp, err := CreateCustomMCP(oc.AsAdmin(), customPoolName, 1)
		o.Expect(err).NotTo(o.HaveOccurred(), "Could not create a new custom MCP")
		node := infraMcp.GetNodesOrFail()[0]
		logger.Infof("%s", node)

		basicPinnedImageTest(infraMcp, pinnedImageSetName)

	})
})

// getReleaseInfoPullspecOrFail returns a list of strings containing the names of the pullspec images
func getReleaseInfoPullspecOrFail(oc *exutil.CLI) []string {
	mMcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
	master := mMcp.GetNodesOrFail()[0]

	remoteAdminKubeConfig := fmt.Sprintf("/root/remoteKubeConfig-%s", exutil.GetRandomString())
	adminKubeConfig := exutil.KubeConfigPath()

	defer master.RemoveFile(remoteAdminKubeConfig)
	o.Expect(master.CopyFromLocal(adminKubeConfig, remoteAdminKubeConfig)).To(o.Succeed(),
		"Error copying kubeconfig file to master node")

	releaseInfoCommand := fmt.Sprintf("oc adm release info -o pullspec --registry-config /var/lib/kubelet/config.json --kubeconfig %s", remoteAdminKubeConfig)
	stdout, _, err := master.DebugNodeWithChrootStd("sh", "-c", "set -a; source /etc/mco/proxy.env; "+releaseInfoCommand)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting image release pull specs")
	return strings.Split(stdout, "\n")
}

// skipIfDiskSpaceLessThanBytes skip test case if there is less than minNumBytes space available in the given path
func skipIfDiskSpaceLessThanBytes(node *Node, path string, minNumBytes int64) {
	diskUsage, err := node.GetFileSystemSpaceUsage(path)

	o.Expect(err).NotTo(o.HaveOccurred(),
		"Cannot get the disk usage in node %s", node.GetName())

	if minNumBytes > diskUsage.Avail {
		g.Skip(fmt.Sprintf("Available diskspace in %s is %d bytes, which is less than the required %d bytes",
			node.GetName(), diskUsage.Avail, minNumBytes))
	}

	logger.Infof("Required disk space %d bytes, available disk space %d", minNumBytes, diskUsage.Avail)

}

// getCurrentReleaseInfoImageSpecOrDefault returns the image spec for the given image in the release. If there is any error, it returns the given default image
// In disconnected clusters the release image is mirrored. Unfortunately, "oc adm release info" command does not take /etc/containers/registries.conf mirrors into account,
// hence in disconnected clusters we cannot get the release image specs unless we apply the mirror manually.
// TODO: When the "oc adm release info" command spec fails:
// 1. parse the output to get the release image name
// 2. search in all imagecontentsourcepolicies and all imagedigestimirrorsets if there is any mirror for the release image (it should)
// 3. use the mirror manually to get the image specs
func getCurrentReleaseInfoImageSpecOrDefault(oc *exutil.CLI, imageName, defaultImageName string) string {
	image, err := getCurrentReleaseInfoImageSpec(oc, imageName)
	if err != nil {
		return defaultImageName
	}
	return image
}

// getCurrentReleaseInfoImageSpec returns the image spec for the given image in the release
func getCurrentReleaseInfoImageSpec(oc *exutil.CLI, imageName string) (string, error) {
	mMcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
	allNodes, err := mMcp.GetNodes()
	if err != nil {
		return "", err
	}
	master := allNodes[0]

	remoteAdminKubeConfig := fmt.Sprintf("/root/remoteKubeConfig-%s", exutil.GetRandomString())
	adminKubeConfig := exutil.KubeConfigPath()

	defer master.RemoveFile(remoteAdminKubeConfig)
	err = master.CopyFromLocal(adminKubeConfig, remoteAdminKubeConfig)
	if err != nil {
		return "", err
	}

	stdout, _, err := master.DebugNodeWithChrootStd("oc", "adm", "release", "info", "--image-for", imageName, "--registry-config", "/var/lib/kubelet/config.json", "--kubeconfig", remoteAdminKubeConfig)
	if err != nil {
		return "", err
	}
	return stdout, nil
}

// DigestMirrorTest generic instructions for DigestImageMirrorSet tests
func DigestMirrorTest(oc *exutil.CLI, mcp *MachineConfigPool, idmsName, idmsMirrors, pinnedImage, pinnedImageSetName string) {
	var (
		allNodes      = mcp.GetNodesOrFail()
		waitForPinned = 10 * time.Minute
		mcpsList      = NewMachineConfigPoolList(oc.AsAdmin())
	)

	exutil.By("Remove the image from all nodes in the pool")
	for _, node := range allNodes {
		// We ignore errors, since the image can be present or not in the nodes
		_ = NewRemoteImage(node, pinnedImage).Rmi()
	}
	logger.Infof("OK!\n")

	exutil.By("Create new machine config to deploy a ImageDigestMirrorSet configuring a mirror registry")
	idms := NewImageDigestMirrorSet(oc.AsAdmin(), idmsName, *NewMCOTemplate(oc, "add-image-digest-mirror-set.yaml"))
	defer mcpsList.waitForComplete() // An ImageDisgestMirrorSet resource impacts all the pools in the cluster
	defer idms.Delete()

	idms.Create("-p", "NAME="+idmsName, "IMAGEDIGESTMIRRORS="+idmsMirrors)
	mcpsList.waitForComplete()
	logger.Infof("OK!\n")

	exutil.By("Pin the mirrored image")
	pis, err := CreateGenericPinnedImageSet(oc.AsAdmin(), pinnedImageSetName, mcp.GetName(), []string{pinnedImage})
	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating pinnedimageset %s", pis)
	defer pis.DeleteAndWait(waitForPinned)
	logger.Infof("OK!\n")

	exutil.By("Wait for all images to be pinned")
	o.Expect(mcp.waitForPinComplete(waitForPinned)).To(o.Succeed(), "Pinned image operation is not completed in %s", mcp)
	logger.Infof("OK!\n")

	exutil.By("Check that the image is pinned")
	for _, node := range allNodes {
		ri := NewRemoteImage(node, pinnedImage)
		logger.Infof("Checking %s", ri)
		o.Expect(ri.IsPinned()).To(o.BeTrue(),
			"%s is not pinned, but it should. %s")
	}
	logger.Infof("OK!\n")
}

// basic function to add pinnedimageset for TC-73623 and TC-80334
func basicPinnedImageTest(mcp *MachineConfigPool, pinnedImageSetName string) {

	var (
		waitForPinned     = time.Minute * 15
		node              = mcp.GetNodesOrFail()[0]
		firstPinnedImage  = NewRemoteImage(node, BusyBoxImage)
		secondPinnedImage = NewRemoteImage(node, AlpineImage)
		oc                = mcp.GetOC()
	)

	exutil.By("Remove images")
	_ = firstPinnedImage.Rmi()
	_ = secondPinnedImage.Rmi()
	logger.Infof("OK!\n")

	exutil.By("Pin images")
	pis, err := CreateGenericPinnedImageSet(oc.AsAdmin(), pinnedImageSetName, mcp.GetName(), []string{firstPinnedImage.ImageName, secondPinnedImage.ImageName})
	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating pinnedimageset %s", pis)
	defer pis.DeleteAndWait(waitForPinned)
	logger.Infof("OK!\n")

	exutil.By("Wait for all images to be pinned")
	o.Expect(mcp.waitForPinComplete(waitForPinned)).To(o.Succeed(), "Pinned image operation is not completed in %s", mcp)
	logger.Infof("OK!\n")

	exutil.By("Check that the images are pinned")
	o.Expect(firstPinnedImage.IsPinned()).To(o.BeTrue(), "%s is not pinned, but it should", firstPinnedImage)
	o.Expect(secondPinnedImage.IsPinned()).To(o.BeTrue(), "%s is not pinned, but it should", secondPinnedImage)
	logger.Infof("OK!\n")

	exutil.By("Patch the pinnedimageset and remove one image")
	o.Expect(
		pis.Patch("json", fmt.Sprintf(`[{"op": "replace", "path": "/spec/pinnedImages", "value": [{"name": "%s"}]}]`, firstPinnedImage.ImageName)),
	).To(o.Succeed(),
		"Error patching %s to remove one image")
	logger.Infof("OK!\n")

	exutil.By("Wait for the pinnedimageset changes to be applied")
	o.Expect(mcp.waitForPinComplete(waitForPinned)).To(o.Succeed(), "Pinned image operation is not completed in %s", mcp)
	logger.Infof("OK!\n")

	exutil.By("Check that only the image remaining in the pinnedimageset is pinned")
	o.Expect(firstPinnedImage.IsPinned()).To(o.BeTrue(), "%s is not pinned, but it should", firstPinnedImage)
	o.Expect(secondPinnedImage.IsPinned()).To(o.BeFalse(), "%s is pinned, but it should NOT", secondPinnedImage)
	logger.Infof("OK!\n")

	exutil.By("Remove the pinnedimageset")
	o.Expect(pis.Delete()).To(o.Succeed(), "Error removing %s", pis)
	logger.Infof("OK!\n")

	exutil.By("Wait for the pinnedimageset removal to be applied")
	o.Expect(mcp.waitForPinComplete(waitForPinned)).To(o.Succeed(), "Pinned image operation is not completed in %s", mcp)
	logger.Infof("OK!\n")

	exutil.By("Check that only the image remaining in the pinnedimageset is pinned")
	o.Expect(firstPinnedImage.IsPinned()).To(o.BeFalse(), "%s is pinned, but it should NOT", firstPinnedImage)
	o.Expect(secondPinnedImage.IsPinned()).To(o.BeFalse(), "%s is pinned, but it should NOT", secondPinnedImage)
	logger.Infof("OK!\n")
}
