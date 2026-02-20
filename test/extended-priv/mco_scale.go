package extended

import (
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	"github.com/openshift/machine-config-operator/test/extended-priv/util/architecture"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"github.com/tidwall/gjson"
	"gopkg.in/ini.v1"
)

const (
	clonedPrefix = "user-data-"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO scale", func() {
	defer g.GinkgoRecover()

	var (
		oc = exutil.NewCLI("mco-scale", exutil.KubeConfigPath())
		// worker MachineConfigPool
		wMcp                 *MachineConfigPool
		mMcp                 *MachineConfigPool
		machineConfiguration *MachineConfiguration
	)

	g.JustBeforeEach(func() {
		// Skip if no machineset
		SkipTestIfWorkersCannotBeScaled(oc.AsAdmin())

		wMcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		mMcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
		machineConfiguration = GetMachineConfiguration(oc.AsAdmin())
		PreChecks(oc)

		failureHandler := func(message string, callerSkip ...int) {
			logger.Errorf("Gomega assertion failed!")
			logger.Errorf("Failure message: %s", message)

			msList := NewMachineSetList(oc.AsAdmin(), MachineAPINamespace)
			mList := NewMachineList(oc.AsAdmin(), MachineAPINamespace)
			nodeList := NewNodeList(oc.AsAdmin())

			logger.Infof("DEBUGGING NODES")
			nodeList.PrintDebugCommand()
			logger.Infof("\n\n")

			logger.Infof("DEBUGGING MACHINESETS")
			msList.PrintDebugCommand()
			logger.Infof("%s", msList.PrettyString())
			logger.Infof("\n\n")

			logger.Infof("DEBUGGING MACHINESETS")
			mList.PrintDebugCommand()
			logger.Infof("%s", mList.PrettyString())
			logger.Infof("\n\n")

			// We are adding an extra level to the stack here.
			// We adjust it so that the assertions can point to the right line of code
			// What we do with the callerSkip is similar to configuring all assertions with Offset(1) (increasing offset by one)
			if len(callerSkip) == 0 {
				callerSkip = []int{1} // default offset should be 1 with this failureHandler wrapper
			}

			// Increment the first value to account for this wrapper (increase the offset)
			callerSkip[0]++

			// Fail executing ginkgo failhandler
			g.Fail(message, callerSkip...)
		}

		o.RegisterFailHandler(failureHandler)

	})

	g.It("[PolarionID:63894][OTP] Scaleup using 4.1 cloud image", g.Label("Platform:aws"), func() {
		var (
			imageVersion = "4.1" // OCP4.1 ami for AWS and use-east2 zone: https://github.com/openshift/installer/blob/release-4.1/data/data/rhcos.json
			numNewNodes  = 1     // the number of nodes scaled up in the new Machineset
		)

		skipTestIfSupportedPlatformNotMatched(oc, AWSPlatform) // Scale up using 4.1 is only supported in AWS. GCP is only supported in versions 4.6+, and Vsphere in 4.2+
		skipTestIfFIPSIsEnabled(oc.AsAdmin())                  // fips was supported for the first time in 4.3, hence it is not supported to scale 4.1 and 4.2 base images in clusters with fips=true
		architecture.SkipNonAmd64SingleArch(oc)                // arm64 is not supported until 4.12

		// Apply workaround
		// Because of https://issues.redhat.com/browse/OCPBUGS-27273 this test case fails when the cluster has imagecontentsourcepolicies
		// In prow jobs clusters have 2 imagecontentsourcepolicies (brew-registry and ), we try to remove them to execute this test
		// It only happens using 4.1 base images. The issue was fixed in 4.2

		// For debugging purposes
		oc.AsAdmin().Run("get").Args("ImageContentSourcePolicy").Execute()
		oc.AsAdmin().Run("get").Args("ImageTagMirrorSet").Execute()
		oc.AsAdmin().Run("get").Args("ImageDigestMirrorSet").Execute()

		cleanedICSPs := []*Resource{NewResource(oc.AsAdmin(), "ImageContentSourcePolicy", "brew-registry"), NewResource(oc.AsAdmin(), "ImageContentSourcePolicy", "image-policy")}

		logger.Warnf("APPLYING WORKAROUND FOR  https://issues.redhat.com/browse/OCPBUGS-27273. Removing expected imageocontentsourcepolicies")

		removedICSP := false
		defer func() {
			if removedICSP {
				wMcp.waitForComplete()
				mMcp.WaitImmediateForUpdatedStatus()
			}
		}()

		for _, item := range cleanedICSPs {
			icsp := item
			if icsp.Exists() {
				logger.Infof("Cleaning the spec of %s", icsp)
				defer icsp.SetSpec(icsp.GetSpecOrFail())
				o.Expect(icsp.SetSpec("{}")).To(o.Succeed(),
					"Error cleaning %s spec", icsp)

				removedICSP = true
			}
		}

		if removedICSP {
			wMcp.waitForComplete()
			o.Expect(mMcp.WaitImmediateForUpdatedStatus()).To(o.Succeed())
		} else {
			logger.Infof("No ICSP was removed!!")
		}

		SimpleScaleUPTest(oc, wMcp, imageVersion, getUserDataIgnitionVersionFromOCPVersion(imageVersion), numNewNodes)
	})

	// 4.3 is the first image supporting fips
	g.It("[PolarionID:77051][OTP] Scaleup using 4.3 cloud image", g.Label("Platform:aws", "Platform:vsphere"), func() {
		var (
			imageVersion = "4.3"
			numNewNodes  = 1 // the number of nodes scaled up in the new Machineset
		)

		skipTestIfSupportedPlatformNotMatched(oc, AWSPlatform, VspherePlatform) // Scale up using 4.3 is only supported in AWS, and Vsphere. GCP is only supported by our automation in versions 4.6+
		architecture.SkipNonAmd64SingleArch(oc)                                 // arm64 is not supported by OCP until 4.12

		SimpleScaleUPTest(oc, wMcp, imageVersion, getUserDataIgnitionVersionFromOCPVersion(imageVersion), numNewNodes)
	})

	// 4.12 is the last version using rhel8, in 4.13 ocp starts using rhel9
	g.It("[PolarionID:76471][OTP] Scaleup using 4.12 cloud image", g.Label("Platform:aws", "Platform:gcp", "Platform:vsphere"), func() {
		var (
			imageVersion = "4.12"
			numNewNodes  = 1 // the number of nodes scaled up in the new Machineset
		)

		skipTestIfSupportedPlatformNotMatched(oc, AWSPlatform, GCPPlatform, VspherePlatform) // Scale up using 4.12 is only supported in AWS, GCP and Vsphere

		SimpleScaleUPTest(oc, wMcp, imageVersion, getUserDataIgnitionVersionFromOCPVersion(imageVersion), numNewNodes)
	})

	g.It("[PolarionID:52822][OTP] Create new config resources with 2.2.0 ignition boot image nodes", g.Label("Platform:aws", "Platform:vsphere"), func() {
		var (
			newMsName  = "copied-machineset-modified-tc-52822"
			kcName     = "change-maxpods-kubelet-config"
			kcTemplate = generateTemplateAbsolutePath(kcName + ".yaml")
			crName     = "change-ctr-cr-config"
			crTemplate = generateTemplateAbsolutePath(crName + ".yaml")
			mcName     = "generic-config-file-test-52822"
			mcpWorker  = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			// Set the 4.5 boot image ami for east-2 zone.
			// the right ami should be selected from here https://github.com/openshift/installer/blob/release-4.5/data/data/rhcos.json
			imageVersion = "4.5"
			numNewNodes  = 1 // the number of nodes scaled up in the new Machineset
		)

		skipTestIfSupportedPlatformNotMatched(oc, AWSPlatform, VspherePlatform) // Scale up using 4.5 is only supported for AWS and Vsphere. GCP is only supported in versions 4.6+
		architecture.SkipNonAmd64SingleArch(oc)                                 // arm64 is not supported until 4.11

		if IsBootImageUpdateSupported(oc.AsAdmin()) {
			exutil.By("Opt-out boot images update")
			logger.Infof("Disabling the bootimages update so that our images are not overridden by MCO")
			defer machineConfiguration.SetSpec(machineConfiguration.GetSpecOrFail())
			o.Expect(
				machineConfiguration.SetNoneManagedBootImagesConfig(MachineSetResource),
			).To(o.Succeed(), "Error configuring None managedBootImages in the 'cluster' MachineConfiguration resource")
			logger.Infof("OK!\n")
		}

		initialNumWorkers := len(wMcp.GetNodesOrFail())

		defer func() {
			logger.Infof("Start TC defer block")
			newMs := NewMachineSet(oc.AsAdmin(), MachineAPINamespace, newMsName)
			errors := o.InterceptGomegaFailures(func() { // We don't want gomega to fail and stop the deferred cleanup process
				removeClonedMachineSet(newMs, wMcp, initialNumWorkers)

				cr := NewContainerRuntimeConfig(oc.AsAdmin(), crName, crTemplate)
				if cr.Exists() {
					logger.Infof("Removing ContainerRuntimeConfig %s", cr.GetName())
					o.Expect(cr.Delete()).To(o.Succeed(), "Error removing %s", cr)
				}
				kc := NewKubeletConfig(oc.AsAdmin(), kcName, kcTemplate)
				if kc.Exists() {
					logger.Infof("Removing KubeletConfig %s", kc.GetName())
					o.Expect(kc.Delete()).To(o.Succeed(), "Error removing %s", kc)
				}

				// MachineConfig struct has not been refactored to compose the "Resource" struct
				// so there is no "Exists" method available. Use it after refactoring MachineConfig
				mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
				logger.Infof("Removing machineconfig %s", mcName)
				mc.DeleteWithWait()

			})

			if len(errors) != 0 {
				logger.Infof("There were errors restoring the original MachineSet resources in the cluster")
				for _, e := range errors {
					logger.Errorf(e)
				}
			}

			logger.Infof("Waiting for worker pool to be updated")
			mcpWorker.waitForComplete()

			// We don't want the test to pass if there were errors while restoring the initial state
			o.Expect(len(errors)).To(o.BeZero(),
				"There were %d errors while recovering the cluster's initial state", len(errors))

			logger.Infof("End TC defer block")
		}()

		// Duplicate an existing MachineSet
		allMs, err := NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAll()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting a list of MachineSet resources")
		ms := allMs[0]
		newMs := cloneMachineSet(oc.AsAdmin(), ms, newMsName, imageVersion, getUserDataIgnitionVersionFromOCPVersion(imageVersion))

		// KubeletConfig
		exutil.By("Create KubeletConfig")
		kc := NewKubeletConfig(oc.AsAdmin(), kcName, kcTemplate)
		kc.create()
		kc.waitUntilSuccess("10s")
		logger.Infof("OK!\n")

		// ContainterRuntimeConfig
		exutil.By("Create ContainterRuntimeConfig")
		cr := NewContainerRuntimeConfig(oc.AsAdmin(), crName, crTemplate)
		cr.create()
		cr.waitUntilSuccess("10s")
		logger.Infof("OK!\n")

		// Generic machineconfig
		exutil.By("Create generic config file")
		genericConfigFilePath := "/etc/test-52822"
		genericConfig := "config content for test case 52822"

		fileConfig := getURLEncodedFileConfig(genericConfigFilePath, genericConfig, "420")
		template := NewMCOTemplate(oc, "generic-machine-config-template.yml")
		errCreate := template.Create("-p", "NAME="+mcName, "-p", "POOL=worker", "-p", fmt.Sprintf("FILES=[%s]", fileConfig))
		o.Expect(errCreate).NotTo(o.HaveOccurred(), "Error creating MachineConfig %s", mcName)
		logger.Infof("OK!\n")

		// Wait for all pools to apply the configs
		exutil.By("Wait for worker MCP to be updated")
		mcpWorker.waitForComplete()
		logger.Infof("OK!\n")

		// Scale up the MachineSet
		exutil.By("Scale MachineSet up")
		logger.Infof("Scaling up machineset %s", newMs.GetName())
		scaleErr := newMs.ScaleTo(numNewNodes)
		o.Expect(scaleErr).NotTo(o.HaveOccurred(), "Error scaling up MachineSet %s", newMs.GetName())

		logger.Infof("Waiting %s machineset for being ready", newMsName)
		o.Eventually(newMs.GetIsReady, "20m", "2m").Should(o.BeTrue(), "MachineSet %s is not ready", newMs.GetName())
		logger.Infof("OK!\n")

		exutil.By("Check that worker pool is increased and updated")
		o.Eventually(wMcp.GetNodesOrFail, "5m", "30s").Should(o.HaveLen(initialNumWorkers+numNewNodes),
			"The worker pool has not added the new nodes created by the new Machineset.\n%s", wMcp.PrettyString())

		// Verify that the scaled nodes has been configured properly
		exutil.By("Check config in the new node")
		newNodes, nErr := newMs.GetNodes()
		o.Expect(nErr).NotTo(o.HaveOccurred(), "Error getting the nodes created by MachineSet %s", newMs.GetName())
		o.Expect(newNodes).To(o.HaveLen(numNewNodes), "Only %d nodes should have been created by MachineSet %s", numNewNodes, newMs.GetName())
		newNode := newNodes[0]
		logger.Infof("New node: %s", newNode.GetName())
		logger.Infof("OK!\n")

		exutil.By("Check kubelet config")
		kcFile := NewRemoteFile(newNode, "/etc/kubernetes/kubelet.conf")
		kcrErr := kcFile.Fetch()
		o.Expect(kcrErr).NotTo(o.HaveOccurred(), "Error reading kubelet config in node %s", newNode.GetName())
		o.Expect(kcFile.GetTextContent()).Should(o.Or(o.ContainSubstring(`"maxPods": 500`), o.ContainSubstring(`maxPods: 500`)),
			"File /etc/kubernetes/kubelet.conf has not the expected content")
		logger.Infof("OK!\n")

		exutil.By("Check container runtime config")
		crFile := NewRemoteFile(newNode, "/etc/containers/storage.conf")
		crrErr := crFile.Fetch()
		o.Expect(crrErr).NotTo(o.HaveOccurred(), "Error reading container runtime config in node %s", newNode.GetName())
		o.Expect(crFile.GetTextContent()).Should(o.ContainSubstring("size = \"8G\""),
			"File /etc/containers/storage.conf has not the expected content")
		logger.Infof("OK!\n")

		exutil.By("Check generic machine config")
		cFile := NewRemoteFile(newNode, genericConfigFilePath)
		crErr := cFile.Fetch()
		o.Expect(crErr).NotTo(o.HaveOccurred(), "Error reading generic config file in node %s", newNode.GetName())
		o.Expect(cFile.GetTextContent()).Should(o.Equal(genericConfig),
			"File %s has not the expected content", genericConfigFilePath)
		logger.Infof("OK!\n")

		exutil.By("Scale down and remove the cloned Machineset")
		removeClonedMachineSet(newMs, wMcp, initialNumWorkers)
		logger.Infof("OK!\n")

	})

	g.It("[PolarionID:65923][OTP] SSH key in scaled clusters", func() {

		// It is a safe assumpion that all the tested clusters will have a sshkey deployed in it.
		// If at any moment this assumption is not safe anymore, we need to check for the sshkey to exist
		// and create a MC to deploy a sskey in case of no sshkey deployed
		var (
			initialNumWorkers = len(wMcp.GetNodesOrFail())
			numNewNodes       = 1
		)

		defer wMcp.waitForComplete()

		exutil.By("Scale up a machineset")
		allMs, err := NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAll()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting a list of MachineSet resources")
		ms := allMs[0]

		initialMsNodes, err := ms.GetNodes()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting a list of nodes that belong to machineset %s", ms.GetName())

		initialNumMsNodes := len(initialMsNodes)

		logger.Infof("Scaling up machineset %s by 1", ms.GetName())
		defer func() { _ = ms.ScaleTo(initialNumMsNodes) }()
		o.Expect(ms.ScaleTo(initialNumMsNodes+numNewNodes)).NotTo(
			o.HaveOccurred(),
			"Error scaling up MachineSet %s", ms.GetName())

		logger.Infof("OK!\n")

		logger.Infof("Waiting %s machineset for being ready", ms)
		o.Eventually(ms.GetIsReady, "20m", "2m").Should(o.BeTrue(), "MachineSet %s is not ready", ms.GetName())
		logger.Infof("OK!\n")

		exutil.By("Check that worker pool is increased and updated")
		o.Eventually(wMcp.GetNodesOrFail, "5m", "30s").Should(o.HaveLen(initialNumWorkers+numNewNodes),
			"The worker pool has not added the new nodes created by the new Machineset.\n%s", wMcp.PrettyString())
		wMcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check that the sshkey exists in all nodes")
		currentWorkers := wMcp.GetNodesOrFail()
		for _, node := range currentWorkers {
			logger.Infof("Checking sshkey in node %s", node.GetName())
			remoteSSHKey := NewRemoteFile(node, "/home/core/.ssh/authorized_keys.d/ignition")
			o.Expect(remoteSSHKey.Fetch()).To(o.Succeed(),
				"Error getting the content of the sshkey file in node %s", node.GetName())

			o.Expect(remoteSSHKey.GetTextContent()).NotTo(o.BeEmpty(),
				"The sshkey file has no content in node %s", node.GetName())
			logger.Infof("Sshkey is OK in node %s", node.GetName())
		}
		logger.Infof("OK!\n")

	})

	g.It("[PolarionID:64623][OTP] Machine Config Server CA rotation. IPI.", func() {
		var (
			initialNumWorkers = len(wMcp.GetNodesOrFail())
			numNewNodes       = 1
		)

		// skip the test if fips is not enabled
		skipTestIfFIPSIsNotEnabled(oc)

		defer wMcp.waitForComplete()

		exutil.By("Rotate MCS certificate")
		initialMCSPods, err := GetMCSPodNames(oc.AsAdmin())

		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting MCS pod names")

		logger.Infof("Current MCS pod names: %s", initialMCSPods)

		o.Expect(
			RotateMCSCertificates(oc.AsAdmin()),
			//	oc.AsAdmin().WithoutNamespace().Run("adm").Args("ocp-certificates", "regenerate-machine-config-server-serving-cert").Execute(),
		).To(o.Succeed(),
			"Error rotating MCS certificates")

		logger.Infof("OK!\n")

		exutil.By("Check that MCS pods were restarted")
		o.Eventually(func(gm o.Gomega) {

			// for debugging purposes
			logger.Infof("Waiting for MCS pods to be restarted")
			_ = oc.AsAdmin().WithoutNamespace().Run("get").Args("pods", "-n", MachineConfigNamespace).Execute()

			currentMCSPods, err := GetMCSPodNames(oc.AsAdmin())

			gm.Expect(err).NotTo(o.HaveOccurred(),
				"Error getting MCS pod names")

			for _, initialMCSPod := range initialMCSPods {
				gm.Expect(currentMCSPods).NotTo(o.ContainElement(initialMCSPod),
					"MCS pod %s was not restarted after certs rotation", initialMCSPod)
			}

		}, "5m", "20s",
		).Should(o.Succeed(),
			"The MCS pods were not restarted after the MCS certificates were rotated")

		logger.Infof("OK!\n")

		exutil.By("Check that new machine-config-server-tls and machine-config-server-ca secrets are created")
		tlsSecret := NewSecret(oc.AsAdmin(), MachineConfigNamespace, "machine-config-server-tls")
		caSecret := NewSecret(oc.AsAdmin(), MachineConfigNamespace, "machine-config-server-ca")

		o.Eventually(tlsSecret, "30s", "5s").Should(Exist(),
			"%s secret does not exist in the MCO namespace after MCS cert rotations", tlsSecret.GetName())

		o.Eventually(caSecret, "30s", "5s").Should(Exist(),
			"%s secret does not exist in the MCO namespace after MCS cert rotations", tlsSecret.GetName())

		logger.Infof("OK!\n")

		exutil.By("Scale up a machineset")
		allMs, err := NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAll()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting a list of MachineSet resources")
		ms := allMs[0]

		initialMsNodes, err := ms.GetNodes()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting a list of nodes that belong to machineset %s", ms.GetName())

		initialNumMsNodes := len(initialMsNodes)

		logger.Infof("Scaling up machineset %s by 1", ms.GetName())
		defer func() { _ = ms.ScaleTo(initialNumMsNodes) }()
		o.Expect(ms.ScaleTo(initialNumMsNodes+numNewNodes)).NotTo(
			o.HaveOccurred(),
			"Error scaling up MachineSet %s", ms.GetName())

		logger.Infof("OK!\n")

		logger.Infof("Waiting %s machineset for being ready", ms)
		o.Eventually(ms.GetIsReady, "20m", "2m").Should(o.BeTrue(), "MachineSet %s is not ready", ms.GetName())
		logger.Infof("OK!\n")

		exutil.By("Check that worker pool is increased and updated")
		o.Eventually(wMcp.GetNodesOrFail, "5m", "30s").Should(o.HaveLen(initialNumWorkers+numNewNodes),
			"The worker pool has not added the new nodes created by the new Machineset.\n%s", wMcp.PrettyString())
		wMcp.waitForComplete()
		logger.Infof("All nodes are up and ready!")
		logger.Infof("OK!\n")

	})

	g.It("[PolarionID:73636][OTP] Pinned images in scaled nodes", func() {
		var (
			waitForPinned      = time.Minute * 5
			initialNumWorkers  = len(wMcp.GetNodesOrFail())
			numNewNodes        = 3
			pinnedImageSetName = "tc-73636-pinned-images-scale"
			pinnedImageName    = BusyBoxImage
		)

		exutil.By("Pin images")
		pis, err := CreateGenericPinnedImageSet(oc.AsAdmin(), pinnedImageSetName, wMcp.GetName(), []string{pinnedImageName})
		defer pis.DeleteAndWait(waitForPinned)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating pinnedimageset %s", pis)
		logger.Infof("OK!\n")

		exutil.By("Check that the pool is reporting the right pinnedimageset status")
		o.Expect(wMcp.waitForPinComplete(waitForPinned)).To(o.Succeed(), "Pinned image operation is not completed in %s", wMcp)
		logger.Infof("OK!\n")

		exutil.By("Check that the image was pinned in all nodes")
		for _, node := range wMcp.GetNodesOrFail() {
			rmi := NewRemoteImage(node, pinnedImageName)
			o.Expect(rmi.IsPinned()).To(o.BeTrue(), "%s is not pinned, but it should", rmi)
		}
		logger.Infof("OK!\n")

		exutil.By("Scale up a machineset")
		allMs, err := NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAll()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting a list of MachineSet resources")
		ms := allMs[0]

		initialNumMsNodes := len(ms.GetNodesOrFail())

		logger.Infof("Scaling up machineset %s by %d", ms.GetName(), numNewNodes)
		defer func() {
			_ = ms.ScaleTo(initialNumMsNodes)
			wMcp.waitForComplete()
		}()
		o.Expect(ms.ScaleTo(initialNumMsNodes+numNewNodes)).NotTo(
			o.HaveOccurred(),
			"Error scaling up MachineSet %s", ms.GetName())
		logger.Infof("OK!\n")

		exutil.By("Check that worker pool is increased and updated")
		o.Eventually(wMcp.GetNodesOrFail, "15m", "30s").Should(o.HaveLen(initialNumWorkers+numNewNodes),
			"The worker pool has not added the new nodes created by the new Machineset.\n%s", wMcp.PrettyString())
		wMcp.waitForComplete()
		logger.Infof("All nodes are up and ready!")
		logger.Infof("OK!\n")

		exutil.By("Check that the pool is reporting the right pinnedimageset status")
		o.Expect(wMcp.waitForPinComplete(waitForPinned)).To(o.Succeed(), "Pinned image operation is not completed in %s", wMcp)
		logger.Infof("OK!\n")

		exutil.By("Check that the image was pinned in all nodes")
		for _, node := range wMcp.GetNodesOrFail() {
			rmi := NewRemoteImage(node, pinnedImageName)
			o.Expect(rmi.IsPinned()).To(o.BeTrue(), "%s is not pinned, but it should", rmi)
		}
		logger.Infof("OK!\n")
	})
})

func cloneMachineSet(oc *exutil.CLI, ms *MachineSet, newMsName, imageVersion, ignitionVersion string) *MachineSet {
	var (
		newSecretName = getClonedSecretName(newMsName)
		platform      = exutil.CheckPlatform(oc.AsAdmin())
	)

	// Duplicate an existing MachineSet
	exutil.By("Duplicate a MachineSet resource")
	logger.Infof("Create a new machineset that will use base image %s and ignition version %s", imageVersion, ignitionVersion)
	newMs, dErr := ms.Duplicate(newMsName)
	o.Expect(dErr).NotTo(o.HaveOccurred(), "Error duplicating MachineSet %s -n %s", ms.GetName(), ms.GetNamespace())
	logger.Infof("OK!\n")

	// Create a new secret using the given ignition version
	exutil.By(fmt.Sprintf("Create a new secret with %s ignition version", ignitionVersion))
	currentSecret := ms.GetOrFail(`{.spec.template.spec.providerSpec.value.userDataSecret.name}`)
	logger.Infof("Duplicating secret %s with new name %s", currentSecret, newSecretName)

	modifyUserData := func(userData string) (string, error) { return convertUserDataToNewVersion(userData, ignitionVersion) }
	clonedSecret, sErr := duplicateMachinesetSecret(oc, currentSecret, newSecretName, modifyUserData, nil)
	o.Expect(sErr).NotTo(o.HaveOccurred(), "Error duplicating machine-api secret")
	o.Expect(clonedSecret).To(Exist(), "The secret was not duplicated for machineset %s", newMs)
	logger.Infof("OK!\n")

	// Get the right base image name from the rhcos json info stored in the github repositories
	exutil.By(fmt.Sprintf("Get the base image for version %s", imageVersion))
	rhcosHandler, err := GetRHCOSHandler(platform)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the rhcos handler")

	architecture, err := ms.GetArchitecture()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the arechitecture from machineset %s", ms.GetName())

	baseImage, err := rhcosHandler.GetBaseImageFromRHCOSImageInfo(imageVersion, architecture, getCurrentRegionOrFail(oc.AsAdmin()))
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the base image")
	logger.Infof("Using base image %s", baseImage)

	baseImageURL, err := rhcosHandler.GetBaseImageURLFromRHCOSImageInfo(imageVersion, architecture)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the base image URL")

	// In vshpere we will upload the image. To avoid collisions we will add prefix to identify our image
	if platform == VspherePlatform {
		baseImage = "mcotest-" + baseImage
	}
	o.Expect(
		uploadBaseImageToCloud(oc, platform, baseImageURL, baseImage),
	).To(o.Succeed(), "Error uploading the base image %s to the cloud", baseImageURL)
	logger.Infof("OK!\n")

	// Set the new boot base image
	exutil.By(fmt.Sprintf("Configure the duplicated MachineSet to use the %s boot image", baseImage))
	o.Expect(newMs.SetCoreOsBootImage(baseImage)).To(o.Succeed(),
		"There was an error while patching the new base image in %s", newMs)
	logger.Infof("OK!\n")

	// Use new secret
	exutil.By("Configure the duplicated MachineSet to use the new secret")
	o.Expect(newMs.SetUserDataSecret(newSecretName)).To(o.Succeed(),
		"Error patching MachineSet %s to use the new secret %s", newMs.GetName(), newSecretName)
	logger.Infof("OK!\n")

	return newMs
}

func removeClonedMachineSet(ms *MachineSet, mcp *MachineConfigPool, expectedNumWorkers int) {
	if ms.Exists() {
		logger.Infof("Scaling %s machineset to zero", ms.GetName())
		o.Expect(ms.ScaleTo(0)).To(o.Succeed(),
			"Error scaling MachineSet %s to 0", ms.GetName())

		logger.Infof("Waiting %s machineset for being ready", ms.GetName())
		o.Eventually(ms.GetIsReady, "2m", "15s").Should(o.BeTrue(), "MachineSet %s is not ready", ms.GetName())

		logger.Infof("Removing %s machineset", ms.GetName())
		o.Expect(ms.Delete()).To(o.Succeed(),
			"Error deleting MachineSet %s", ms.GetName())

		if expectedNumWorkers >= 0 {
			exutil.By("Check that worker pool is increased and updated")
			// Before calling mcp.GetNodes we wait for the MachineCount number to settle, to avoid a panic due to nodes disappearing while we calculate the number of nodes
			o.Eventually(mcp.getMachineCount, "5m", "30s").Should(o.Equal(expectedNumWorkers),
				"The MachineCount has not the expected value in pool:\n%s", mcp.PrettyString())
			o.Eventually(mcp.GetNodes, "5m", "30s").Should(o.HaveLen(expectedNumWorkers),
				"The number of nodes is not the expected one in pool:\n%s", mcp.PrettyString())
		}
	}

	clonedSecret := NewSecret(ms.oc, MachineAPINamespace, getClonedSecretName(ms.GetName()))
	if clonedSecret.Exists() {
		logger.Infof("Removing %s secret", clonedSecret)
		o.Expect(clonedSecret.Delete()).To(o.Succeed(),
			"Error deleting  %s", ms.GetName())
	}
}

func getRHCOSImagesInfo(version string) (string, error) {
	var (
		err        error
		resp       *http.Response
		numRetries = 3
		retryDelay = time.Minute
		rhcosURL   = fmt.Sprintf("https://raw.githubusercontent.com/openshift/installer/release-%s/data/data/rhcos.json", version)
	)

	if CompareVersions(version, ">=", "4.10") {
		rhcosURL = fmt.Sprintf("https://raw.githubusercontent.com/openshift/installer/release-%s/data/data/coreos/rhcos.json", version)
	}

	// To mitigate network errors we will retry in case of failure
	logger.Infof("Getting rhcos image info from: %s", rhcosURL)
	for i := 0; i < numRetries; i++ {
		if i > 0 {
			logger.Infof("Error while getting the rhcos mages json data: %s.\nWaiting %s and retrying. Num retries: %d", err, retryDelay, i)
			time.Sleep(retryDelay)
		}
		resp, err = http.Get(rhcosURL)
		if err == nil {
			break
		}
	}

	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// We Read the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// getCurrentRegionOrFail returns the current region if we are in AWS or an empty string if any other platform
func getCurrentRegionOrFail(oc *exutil.CLI) string {
	infra := NewResource(oc.AsAdmin(), "infrastructure", "cluster")
	return infra.GetOrFail(`{.status.platformStatus.aws.region}`)
}

// SimpleScaleUPTest is a generic function that tests scaling up and down worker nodes using the base image corresponding to the given version
func SimpleScaleUPTest(oc *exutil.CLI, mcp *MachineConfigPool, imageVersion, ignitionVersion string, numNewNodes int) {

	var (
		newMsName            = fmt.Sprintf("mco-tc-%s-cloned", GetCurrentTestPolarionIDNumber())
		initialNumWorkers    = len(mcp.GetNodesOrFail())
		machineConfiguration = GetMachineConfiguration(oc.AsAdmin())
	)

	if IsBootImageUpdateSupported(oc.AsAdmin()) {
		exutil.By("Opt-out boot images update")
		logger.Infof("Disabling the bootimages update so that our images are not overridden by MCO")
		defer machineConfiguration.SetSpec(machineConfiguration.GetSpecOrFail())
		o.Expect(
			machineConfiguration.SetNoneManagedBootImagesConfig(MachineSetResource),
		).To(o.Succeed(), "Error configuring None managedBootImages in the 'cluster' MachineConfiguration resource")
		logger.Infof("OK!\n")
	}

	defer func() {
		logger.Infof("Start TC defer block")
		newMs := NewMachineSet(oc.AsAdmin(), MachineAPINamespace, newMsName)
		errors := o.InterceptGomegaFailures(func() { removeClonedMachineSet(newMs, mcp, initialNumWorkers) }) // We don't want gomega to fail and stop the deferred cleanup process
		if len(errors) != 0 {
			logger.Infof("There were errors restoring the original MachineSet resources in the cluster")
			for _, e := range errors {
				logger.Errorf(e)
			}
		}

		// We don't want the test to pass if there were errors while restoring the initial state
		o.Expect(len(errors)).To(o.BeZero(),
			"There were %d errors while recovering the cluster's initial state", len(errors))

		logger.Infof("End TC defer block")
	}()

	logger.Infof("Create a new MachineSet using the right base image")
	allMs, err := NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAll()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting a list of MachineSet resources")
	ms := allMs[0]
	newMs := cloneMachineSet(oc.AsAdmin(), ms, newMsName, imageVersion, ignitionVersion)

	exutil.By("Scale MachineSet up")
	logger.Infof("Scaling up machineset %s", newMs.GetName())
	scaleErr := newMs.ScaleTo(numNewNodes)
	o.Expect(scaleErr).NotTo(o.HaveOccurred(), "Error scaling up MachineSet %s", newMs.GetName())

	logger.Infof("Waiting %s machineset for being ready", newMsName)
	o.Eventually(newMs.GetIsReady, "20m", "2m").Should(o.BeTrue(), "MachineSet %s is not ready", newMs.GetName())
	logger.Infof("OK!\n")

	exutil.By("Check that worker pool is increased and updated")
	o.Eventually(mcp.GetNodesOrFail, "5m", "30s").Should(o.HaveLen(initialNumWorkers+numNewNodes),
		"The worker pool has not added the new nodes created by the new Machineset.\n%s", mcp.PrettyString())
	mcp.waitForComplete()
	logger.Infof("OK!\n")

	exutil.By("Scale down and remove the cloned Machineset")
	removeClonedMachineSet(newMs, mcp, initialNumWorkers)
	logger.Infof("OK!\n")

}

func getClonedSecretName(msName string) string {
	return clonedPrefix + msName
}

func GetRHCOSHandler(platform string) (RHCOSHandler, error) {
	switch platform {
	case AWSPlatform:
		return AWSRHCOSHandler{}, nil
	case GCPPlatform:
		return GCPRHCOSHandler{}, nil
	case VspherePlatform:
		return VsphereRHCOSHandler{}, nil
	default:
		return nil, fmt.Errorf("Platform %s is not supported and cannot get RHCOSHandler", platform)
	}
}

type RHCOSHandler interface {
	GetBaseImageFromRHCOSImageInfo(version string, arch architecture.Architecture, region string) (string, error)
	GetBaseImageURLFromRHCOSImageInfo(version string, arch architecture.Architecture) (string, error)
}

type AWSRHCOSHandler struct{}

func (aws AWSRHCOSHandler) GetBaseImageFromRHCOSImageInfo(version string, arch architecture.Architecture, region string) (string, error) {
	var (
		path       string
		stringArch = arch.GNUString()
		platform   = AWSPlatform
	)

	rhcosImageInfo, err := getRHCOSImagesInfo(version)
	if err != nil {
		return "", err
	}

	if region == "" {
		return "", fmt.Errorf("Region cannot have an empty value when we try to get the base image in platform %s", platform)
	}
	if CompareVersions(version, "<", "4.10") {
		path = `amis.` + region + `.hvm`
	} else {
		path = fmt.Sprintf("architectures.%s.images.%s.regions.%s.image", stringArch, platform, region)

	}

	logger.Infof("Looking for rhcos base image info in path %s", path)
	baseImage := gjson.Get(rhcosImageInfo, path)
	if !baseImage.Exists() {
		logger.Infof("rhcos info:\n%s", rhcosImageInfo)
		return "", fmt.Errorf("Could not find the base image for version <%s> in platform <%s> architecture <%s> and region <%s> with path %s",
			version, platform, arch, region, path)
	}
	return baseImage.String(), nil
}

func (aws AWSRHCOSHandler) GetBaseImageURLFromRHCOSImageInfo(version string, arch architecture.Architecture) (string, error) {
	return getBaseImageURLFromRHCOSImageInfo(version, "aws", "vmdk.gz", arch.GNUString())
}

type GCPRHCOSHandler struct{}

func (gcp GCPRHCOSHandler) GetBaseImageFromRHCOSImageInfo(version string, arch architecture.Architecture, region string) (string, error) {
	var (
		imagePath   string
		projectPath string
		stringArch  = arch.GNUString()
		platform    = GCPPlatform
	)

	if CompareVersions(version, "=", "4.1") {
		return "", fmt.Errorf("There is no image base image supported for platform %s in version %s", platform, version)
	}

	rhcosImageInfo, err := getRHCOSImagesInfo(version)
	if err != nil {
		return "", err
	}

	if CompareVersions(version, "<", "4.10") {
		imagePath = "gcp.image"
		projectPath = "gcp.project"
	} else {
		imagePath = fmt.Sprintf("architectures.%s.images.%s.name", stringArch, platform)
		projectPath = fmt.Sprintf("architectures.%s.images.%s.project", stringArch, platform)
	}

	logger.Infof("Looking for rhcos base image name in path %s", imagePath)
	baseImage := gjson.Get(rhcosImageInfo, imagePath)
	if !baseImage.Exists() {
		logger.Infof("rhcos info:\n%s", rhcosImageInfo)
		return "", fmt.Errorf("Could not find the base image for version <%s> in platform <%s> architecture <%s> and region <%s> with path %s",
			version, platform, arch, region, imagePath)
	}

	logger.Infof("Looking for rhcos base image project in path %s", projectPath)
	project := gjson.Get(rhcosImageInfo, projectPath)
	if !project.Exists() {
		logger.Infof("rhcos info:\n%s", rhcosImageInfo)
		return "", fmt.Errorf("Could not find the project where the base image is stored with version <%s> in platform <%s> architecture <%s> and region <%s> with path %s",
			version, platform, arch, region, projectPath)
	}

	return fmt.Sprintf("projects/%s/global/images/%s", project.String(), baseImage.String()), nil
}

func (gcp GCPRHCOSHandler) GetBaseImageURLFromRHCOSImageInfo(version string, arch architecture.Architecture) (string, error) {
	return getBaseImageURLFromRHCOSImageInfo(version, "gcp", "tar.gz", arch.GNUString())
}

type VsphereRHCOSHandler struct{}

func (vsp VsphereRHCOSHandler) GetBaseImageFromRHCOSImageInfo(version string, arch architecture.Architecture, _ string) (string, error) {
	baseImageURL, err := vsp.GetBaseImageURLFromRHCOSImageInfo(version, arch)
	if err != nil {
		return "", err
	}

	return path.Base(baseImageURL), nil
}

func (vsp VsphereRHCOSHandler) GetBaseImageURLFromRHCOSImageInfo(version string, arch architecture.Architecture) (string, error) {
	return getBaseImageURLFromRHCOSImageInfo(version, "vmware", "ova", arch.GNUString())
}

func getBaseImageURLFromRHCOSImageInfo(version, platform, format, stringArch string) (string, error) {
	var (
		imagePath    string
		baseURIPath  string
		olderThan410 = CompareVersions(version, "<", "4.10")
	)

	rhcosImageInfo, err := getRHCOSImagesInfo(version)
	if err != nil {
		return "", err
	}

	if olderThan410 {
		imagePath = fmt.Sprintf("images.%s.path", platform)
		baseURIPath = "baseURI"
	} else {
		imagePath = fmt.Sprintf("architectures.%s.artifacts.%s.formats.%s.disk.location", stringArch, platform, strings.ReplaceAll(format, ".", `\.`))
	}

	logger.Infof("Looking for rhcos base image path name in path %s", imagePath)
	baseImageURL := gjson.Get(rhcosImageInfo, imagePath)
	if !baseImageURL.Exists() {
		logger.Infof("rhcos info:\n%s", rhcosImageInfo)
		return "", fmt.Errorf("Could not find the base image for version <%s> in platform <%s> architecture <%s> and format <%s> with path %s",
			version, platform, stringArch, format, imagePath)
	}

	if !olderThan410 {
		return baseImageURL.String(), nil
	}

	logger.Infof("Looking for baseURL in path %s", baseURIPath)
	baseURI := gjson.Get(rhcosImageInfo, baseURIPath)
	if !baseURI.Exists() {
		logger.Infof("rhcos info:\n%s", rhcosImageInfo)
		return "", fmt.Errorf("Could not find the base URI with version <%s> in platform <%s> architecture <%s> and format <%s> with path %s",
			version, platform, stringArch, format, baseURIPath)
	}

	return fmt.Sprintf("%s/%s", strings.Replace(strings.Trim(baseURI.String(), "/"), "releases-art-rhcos.svc.ci.openshift.org", "rhcos.mirror.openshift.com", 1), strings.Trim(baseImageURL.String(), "/")), nil
}

func uploadBaseImageToCloud(oc *exutil.CLI, platform, baseImageURL, baseImage string) error {

	switch platform {
	case AWSPlatform:
		logger.Infof("No need to updload images in AWS")
		return nil
	case GCPPlatform:
		logger.Infof("No need to updload images in GCP")
		return nil
	case VspherePlatform:
		server, dataCenter, dataStore, resourcePool, user, password, err := getvSphereCredentials(oc.AsAdmin())
		if err != nil {
			return err
		}

		err = exutil.UploadBaseImageToVsphere(baseImageURL, baseImage, server, dataCenter, dataStore, resourcePool, user, password)
		if err != nil {
			return err
		}

		return nil
	default:
		return fmt.Errorf("Platform %s is not supported, base image cannot be updloaded", platform)
	}
}

func getvSphereCredentials(oc *exutil.CLI) (server, dataCenter, dataStore, resourcePool, user, password string, err error) {
	var (
		configCM    = NewConfigMap(oc.AsAdmin(), "openshift-config", "cloud-provider-config")
		credsSecret = NewSecret(oc.AsAdmin(), "kube-system", "vsphere-creds")
	)
	config, err := configCM.GetDataValue("config")
	if err != nil {
		return
	}

	cfg, err := ini.Load(strings.NewReader(config))
	if err == nil {
		logger.Infof("%s config info is in ini fomart. Extracting data", configCM)
		server = cfg.Section("Workspace").Key("server").String()
		dataCenter = cfg.Section("Workspace").Key("datacenter").String()
		dataStore = cfg.Section("Workspace").Key("default-datastore").String()
		resourcePool = cfg.Section("Workspace").Key("resourcepool-path").String()

	} else {
		logger.Infof("%s config info is NOT in ini fomart. Trying to extract the information from the infrastructure resource", configCM)
		infra := NewResource(oc.AsAdmin(), "infrastructure", "cluster")
		var failureDomain string
		failureDomain, err = infra.Get(`{.spec.platformSpec.vsphere.failureDomains[0]}`)
		if err != nil {
			logger.Errorf("Cannot get the failureDomain from the infrastructure resource: %s", err)
			return
		}
		if failureDomain == "" {
			logger.Errorf("Failure domain is empty in the infrastructure resource: %s\n%s", err, infra.PrettyString())
			err = fmt.Errorf("Empty failure domain in the infrastructure resource")
			return

		}
		gserver := gjson.Get(failureDomain, "server")
		if gserver.Exists() {
			server = gserver.String()
		} else {
			err = fmt.Errorf("Cannot get the server value from failureDomain\n%s", infra.PrettyString())
			return
		}
		gdataCenter := gjson.Get(failureDomain, "topology.datacenter")
		if gdataCenter.Exists() {
			dataCenter = gdataCenter.String()
		} else {
			err = fmt.Errorf("Cannot get the data center value from failureDomain\n%s", infra.PrettyString())
			return
		}

		gdataStore := gjson.Get(failureDomain, "topology.datastore")
		if gdataStore.Exists() {
			dataStore = gdataStore.String()
		} else {
			err = fmt.Errorf("Cannot get the data store value from failureDomain\n%s", infra.PrettyString())
			return
		}

		gresourcePool := gjson.Get(failureDomain, "topology.resourcePool")
		if gresourcePool.Exists() {
			resourcePool = gresourcePool.String()
		} else {
			err = fmt.Errorf("Cannot get the resourcepool value from failureDomain\n%s", infra.PrettyString())
			return
		}
	}

	decodedData, err := credsSecret.GetDecodedDataMap()
	if err != nil {
		return
	}

	for k, v := range decodedData {
		item := v
		if strings.Contains(k, "username") {
			user = item
		}
		if strings.Contains(k, "password") {
			password = item
		}
	}

	if user == "" {
		logger.Errorf("Empty vsphere user")
		err = fmt.Errorf("The vsphere user is empty")
		return
	}

	if password == "" {
		logger.Errorf("Empty vsphere password")
		err = fmt.Errorf("The vsphere password is empty")
		return
	}

	return
}

func IsBootImageUpdateSupported(oc *exutil.CLI) bool {
	var (
		platform = exutil.CheckPlatform(oc.AsAdmin())
	)
	return platform == GCPPlatform || platform == AWSPlatform
}
