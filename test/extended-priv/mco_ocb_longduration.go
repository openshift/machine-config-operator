package extended

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO ocb longduration", func() {
	defer g.GinkgoRecover()

	var (
		oc = exutil.NewCLI("mco-ocb-longduration", exutil.KubeConfigPath())
	)

	g.JustBeforeEach(func() {
		PreChecks(oc)
		SkipTestIfOCBIsEnabled(oc)
	})

	g.It("[PolarionID:79172][OTP] OCB Inherit from global pull secret if baseImagePullSecret field is not specified [Disruptive]", func() {
		var (
			infraMcpName = "infra"
		)

		exutil.By("Create custom infra MCP")
		// We add no workers to the infra pool, it is not necessary
		infraMcp, err := CreateCustomMCP(oc.AsAdmin(), infraMcpName, 0)
		defer infraMcp.delete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a new custom pool: %s", infraMcpName)
		logger.Infof("OK!\n")

		testContainerFile([]ContainerFile{}, MachineConfigNamespace, infraMcp, nil, true)
	})

	g.It("[PolarionID:83136][OTP][Skipped:Disconnected] Panic Condition for Non-Matching MOSC Resources [Disruptive]", func() {
		var (
			infraMcpName = "infra"
			// MOSC has to use the same name as the mcp
			moscName = infraMcpName
		)
		exutil.By("Create New Custom MCP")
		defer DeleteCustomMCP(oc.AsAdmin(), infraMcpName)
		infraMcp, err := CreateCustomMCP(oc.AsAdmin(), infraMcpName, 1)
		o.Expect(err).NotTo(o.HaveOccurred(), "Could not create a new custom MCP")
		node := infraMcp.GetNodesOrFail()[0]
		logger.Infof("%s", node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Configure OCB functionality for the new infra MCP")
		mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, moscName, infraMcpName, nil)
		defer DisableOCL(mosc)
		// remove after this bug is fixed OCPBUGS-36810
		defer func() {
			logger.Infof("Configmaps should also be deleted ")
			cmList := NewConfigMapList(mosc.GetOC(), MachineConfigNamespace).GetAllOrFail()
			for _, cm := range cmList {
				if strings.Contains(cm.GetName(), "rendered-") {
					o.Expect(cm.Delete()).Should(o.Succeed(), "The ConfigMap related to MOSC has not been removed")
				}
			}
		}()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource")
		logger.Infof("OK!\n")

		exutil.By("Check that a new build has been triggered")
		o.Eventually(mosc.GetCurrentMachineOSBuild, "5m", "20s").Should(Exist(),
			"No build was created when OCB was enabled")
		mosb, err := mosc.GetCurrentMachineOSBuild()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting MOSB from MOSC")
		o.Eventually(mosb.GetJob, "5m", "20s").Should(Exist(),
			"No build pod was created when OCB was enabled")
		o.Eventually(mosb, "5m", "20s").Should(HaveConditionField("Building", "status", TrueString),
			"MachineOSBuild didn't report that the build has begun")
		logger.Infof("OK!\n")

		exutil.By("Delete the MCOS and check it is deleted")
		o.Expect(mosc.CleanupAndDelete()).To(o.Succeed(), "Error cleaning up %s", mosc)
		ValidateMOSCIsGarbageCollected(mosc, infraMcp)
		o.Expect(mosb).NotTo(Exist(), "Build is not deleted")
		o.Expect(mosc).NotTo(Exist(), "MOSC is not deleted")
		logger.Infof("OK!\n")

		exutil.AssertAllPodsToBeReady(oc.AsAdmin(), MachineConfigNamespace)
		checkMCCPanic(oc)
	})

	g.It("[PolarionID:78001][OTP][Skipped:Disconnected] The etc-pki-etitlement secret is created automatically for OCB Use custom Containerfile with rhel enablement [Disruptive]", func() {
		var (
			entitlementSecret    = NewSecret(oc.AsAdmin(), "openshift-config-managed", "etc-pki-entitlement")
			containerFileContent = `
        FROM configs AS final

        RUN rm -rf /etc/rhsm-host && \
          rpm-ostree install buildah && \
          ln -s /run/secrets/rhsm /etc/rhsm-host && \
          ostree container commit
`

			checkers = []Checker{
				CommandOutputChecker{
					Command:  []string{"rpm", "-q", "buildah"},
					Matcher:  o.ContainSubstring("buildah-"),
					ErrorMsg: fmt.Sprintf("Buildah package is not installed after the image was deployed"),
					Desc:     fmt.Sprintf("Check that buildah is installed"),
				},
			}

			mcp = GetCompactCompatiblePool(oc.AsAdmin())
		)

		if !entitlementSecret.Exists() {
			g.Skip(fmt.Sprintf("There is no entitlement secret available in this cluster %s. This test case cannot be executed", entitlementSecret))
		}

		testContainerFile([]ContainerFile{{Content: containerFileContent}}, MachineConfigNamespace, mcp, checkers, false)
	})

	g.It("[PolarionID:77498][OTP] OCB Trigger new build when renderedImagePushspec is updated [Disruptive]", func() {

		var (
			infraMcpName = "infra"
			// MOSC resources have to use the same names as the MCP
			moscName = infraMcpName
		)

		exutil.By("Create custom infra MCP")
		// We add no workers to the infra pool, it is not necessary
		infraMcp, err := CreateCustomMCP(oc.AsAdmin(), infraMcpName, 0)
		defer infraMcp.delete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a new custom pool: %s", infraMcpName)
		logger.Infof("OK!\n")

		exutil.By("Configure OCB functionality for the new infra MCP")
		mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, moscName, infraMcpName, nil)
		defer mosc.CleanupAndDelete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource")
		logger.Infof("OK!\n")

		ValidateSuccessfulMOSC(mosc, nil)

		exutil.By("Set a new rendered image pull spec")
		initialMOSB := OrFail[*MachineOSBuild](mosc.GetCurrentMachineOSBuild())
		initialRIPS := OrFail[string](mosc.GetRenderedImagePushspec())
		o.Expect(
			mosc.SetRenderedImagePushspec(strings.ReplaceAll(initialRIPS, "ocb-", "ocb77498-")),
		).NotTo(o.HaveOccurred(), "Error patching %s to set the new renderedImagePullSpec", mosc)
		logger.Infof("OK!\n")

		exutil.By("Check that a new build is triggered")
		checkNewBuildIsTriggered(mosc, initialMOSB)
		logger.Infof("OK!\n")

		exutil.By("Set the original rendered image pull spec")
		o.Expect(
			mosc.SetRenderedImagePushspec(initialRIPS),
		).NotTo(o.HaveOccurred(), "Error patching %s to set the new renderedImagePullSpec", mosc)
		logger.Infof("OK!\n")

		exutil.By("Check that the initial build is reused")
		var currentMOSB *MachineOSBuild
		o.Eventually(func() (string, error) {
			currentMOSB, err = mosc.GetCurrentMachineOSBuild()
			if err != nil || currentMOSB == nil {
				return "", err
			}
			return currentMOSB.GetName(), nil
		}, "5m", "20s").Should(o.Equal(initialMOSB.GetName()),
			"When the containerfiles were removed and initial MOSC configuration was restored, the initial MOSB was not used")

		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:77497][OTP] OCB Trigger new build when Containerfile is updated [Disruptive]", func() {

		var (
			infraMcpName = "infra"
			// MOSC resources have to use the same names as the MCP
			moscName         = infraMcpName
			containerFile    = ContainerFile{Content: "RUN touch /etc/test-add-containerfile" + "\n" + ExpirationDockerfileLabel}
			containerFileMod = ContainerFile{Content: "RUN touch /etc/test-modified-containerfile" + "\n" + ExpirationDockerfileLabel}

			// We need to test a first MOSC without any container file, so we cannot add the expiration label in the first MOSC
			expireImage = false
			// We don't configure the pull secret in the MOSC, since it is optional
			defaultPullSecret = true
		)

		exutil.By("Create custom infra MCP")
		// We add no workers to the infra pool, it is not necessary
		infraMcp, err := CreateCustomMCP(oc.AsAdmin(), infraMcpName, 0)
		defer infraMcp.delete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a new custom pool: %s", infraMcpName)
		logger.Infof("OK!\n")

		exutil.By("Configure OCB functionality for the new infra MCP")
		mosc, err := createMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, moscName, infraMcpName, nil, defaultPullSecret, expireImage)

		defer mosc.CleanupAndDelete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource")
		logger.Infof("OK!\n")

		ValidateSuccessfulMOSC(mosc, nil)

		exutil.By("Add new container file")
		initialMOSB := OrFail[*MachineOSBuild](mosc.GetCurrentMachineOSBuild())

		o.Expect(
			mosc.SetContainerfiles([]ContainerFile{containerFile}),
		).NotTo(o.HaveOccurred(), "Error patching %s to add a container file", mosc)
		logger.Infof("OK!\n")

		exutil.By("Check that a new build is triggered when a containerfile is added")
		checkNewBuildIsTriggered(mosc, initialMOSB)
		logger.Infof("OK!\n")

		exutil.By("Modify the container file")
		currentMOSB := OrFail[*MachineOSBuild](mosc.GetCurrentMachineOSBuild())

		o.Expect(
			mosc.SetContainerfiles([]ContainerFile{containerFileMod}),
		).NotTo(o.HaveOccurred(), "Error patching %s to modify an existing container file", mosc)
		logger.Infof("OK!\n")

		exutil.By("Check that a new build is triggered when a containerfile is modified")
		checkNewBuildIsTriggered(mosc, currentMOSB)
		logger.Infof("OK!\n")

		exutil.By("Remove the container files")
		o.Expect(
			mosc.RemoveContainerfiles(),
		).NotTo(o.HaveOccurred(), "Error patching %s to remove the configured container files", mosc)
		logger.Infof("OK!\n")

		exutil.By("Check that the initial build is reused")
		o.Eventually(func() (string, error) {
			currentMOSB, err = mosc.GetCurrentMachineOSBuild()
			if err != nil || currentMOSB == nil {
				return "", err
			}
			return currentMOSB.GetName(), nil
		}, "5m", "20s").Should(o.Equal(initialMOSB.GetName()),
			"When the containerfiles were removed and initial MOSC configuration was restored, the initial MOSB was not used")

		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:77576][OTP] In OCB. Create a new MC while a build is running [Disruptive]", func() {

		var (
			mcp      = GetCompactCompatiblePool(oc.AsAdmin())
			node     = mcp.GetSortedNodesOrFail()[0]
			moscName = mcp.GetName()

			kArgs  = "test"
			mcName = "tc-77576-testkargs"
			mc     = NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName())
		)

		exutil.By("Configure OCB functionality for the new worker MCP")
		mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, moscName, mcp.GetName(), nil)
		defer DisableOCL(mosc)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource")
		logger.Infof("OK!\n")

		exutil.By("Check that a new build has been triggered and is building")
		var mosb *MachineOSBuild
		o.Eventually(func() (*MachineOSBuild, error) {
			var err error
			mosb, err = mosc.GetCurrentMachineOSBuild()
			return mosb, err
		}, "5m", "20s").Should(Exist(),
			"No build was created when OCB was enabled")
		o.Eventually(mosb.GetJob, "5m", "20s").Should(Exist(),
			"No build job was created when OCB was enabled")
		o.Eventually(mosb, "5m", "20s").Should(HaveConditionField("Building", "status", TrueString),
			"MachineOSBuild didn't report that the build has begun")
		logger.Infof("OK!\n")

		exutil.By("Create a MC to trigger a new build")
		defer mc.DeleteWithWait()
		err = mc.Create("-p", "NAME="+mcName, "-p", "POOL="+mcp.GetName(), "-p", fmt.Sprintf(`KERNEL_ARGS=["%s"]`, kArgs))
		o.Expect(err).NotTo(o.HaveOccurred())
		logger.Infof("OK!\n")

		exutil.By("Check that a new build is triggered and the old build is removed")
		checkNewBuildIsTriggered(mosc, mosb)
		o.Eventually(mosb, "2m", "20s").ShouldNot(Exist(), "The old MOSB %s was not deleted", mosb)
		logger.Infof("OK!\n")

		exutil.By("Wait for the configuration to be applied")
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check that the MC was applied")
		o.Expect(node.IsKernelArgEnabled(kArgs)).To(o.BeTrue(),
			"Kernel argument %s was not enabled in the node %s", kArgs, node.GetName())

		logger.Infof("OK!\n")

		exutil.By("Remove the MachineOSConfig resource")
		o.Expect(DisableOCL(mosc)).To(o.Succeed(), "Error cleaning up %s", mosc)
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:77977][OTP][Skipped:Disconnected] Install extension after OCB is enabled [Disruptive]", func() {

		var (
			mcp                     = GetCompactCompatiblePool(oc.AsAdmin())
			moscName                = mcp.GetName() // MOSC resources have to use the same name as the MCP
			node                    = mcp.GetSortedNodesOrFail()[0]
			mcName                  = "test-install-extension-" + GetCurrentTestPolarionIDNumber()
			applicableExtensions, _ = GetAllApplicableExtensionsToMCPOrFail(mcp)
		)

		exutil.By("Configure OCB functionality for the new worker MCP")
		mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, moscName, mcp.GetName(), nil)
		defer DisableOCL(mosc)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource")
		logger.Infof("OK!\n")

		ValidateSuccessfulMOSC(mosc, nil)

		exutil.By("Create a MC")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName())
		defer mc.DeleteWithWait()
		mc.parameters = []string{fmt.Sprintf(`EXTENSIONS=%s`, string(MarshalOrFail(applicableExtensions)))}
		mc.create()
		logger.Infof("OK!\n")

		exutil.By("Wait for the configuration to be applied")
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		CheckExtensions(node, applicableExtensions)

		exutil.By("Delete a MC.")
		mc.DeleteWithWait()
		logger.Infof("OK!\n")

		exutil.By("Remove the MachineOSConfig resource")
		o.Expect(DisableOCL(mosc)).To(o.Succeed(), "Error cleaning up %s", mosc)
		ValidateMOSCIsGarbageCollected(mosc, mcp)
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:78196][OTP][Skipped:Disconnected] Verify for etc-pki-etitlement secret is removed for  OCB rhel enablement [Disruptive]", func() {

		var (
			entitlementSecret    = NewSecret(oc.AsAdmin(), "openshift-config-managed", "etc-pki-entitlement")
			containerFileContent = `
		FROM configs AS final
		RUN rm -rf /etc/rhsm-host && \
		  rpm-ostree install buildah && \
		  ln -s /run/secrets/rhsm /etc/rhsm-host && \
		  ostree container commit
 `

			mcp = GetCompactCompatiblePool(oc.AsAdmin())
			// MOSC resources have to use the same name as the MCP
			moscName = mcp.GetName()
		)
		if !entitlementSecret.Exists() {
			g.Skip(fmt.Sprintf("There is no entitlement secret available in this cluster %s. This test case cannot be executed", entitlementSecret))
		}

		exutil.By("Copy the entitlement secret in MCO namespace")
		mcoEntitlementSecret, err := CloneResource(entitlementSecret, "etc-pki-entitlement", MachineConfigNamespace, nil)
		defer mcoEntitlementSecret.Delete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error copying %s to the %s namespace", mcoEntitlementSecret, MachineConfigNamespace)
		logger.Infof("OK!\n")

		exutil.By("Delete the entitlement secret in the openshift-config-managed namespace")
		defer func() {
			exutil.By("Recover the entitlement secret in the openshift-config-managed namespace")
			recoverSecret, err := CloneResource(mcoEntitlementSecret, "etc-pki-entitlement", "openshift-config-managed", nil)
			o.Expect(err).NotTo(o.HaveOccurred(), "Error copying %s to the openshift-config-managed namespace", entitlementSecret)
			o.Expect(recoverSecret).To(Exist(), "Unable to recover the entitlement secret in openshift-config-managed namespace")
		}()

		entitlementSecret.Delete()
		logger.Infof("OK!\n")

		exutil.By("Create the MOSC")
		mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, moscName, mcp.GetName(), []ContainerFile{{Content: containerFileContent}})
		defer DisableOCL(mosc)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource")
		logger.Infof("OK!\n")

		exutil.By("Check that a new build has been triggered")
		o.Eventually(mosc.GetCurrentMachineOSBuild, "5m", "20s").Should(Exist(),
			"No build was created when OCB was enabled")
		mosb, err := mosc.GetCurrentMachineOSBuild()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting MOSB from MOSC")
		o.Eventually(mosb, "5m", "20s").Should(HaveConditionField("Building", "status", TrueString),
			"MachineOSBuild didn't report that the build has begun")
		logger.Infof("OK!\n")

		exutil.By("Verify the error is produced in buildPod")
		exutil.AssertAllNonJobPodsToBeReadyWithPollerParams(mosc.GetOC(), MachineConfigNamespace, 10*time.Second, 10*time.Minute)
		logger.Infof("OK!\n")

		job, err := mosb.GetJob()
		o.Expect(err).NotTo(o.HaveOccurred())
		// Currently this kind of resources are leaked. Until the leak is fixed we need to make sure that this job is removed
		//  because its pods are in "Error" status and there are other test cases checking that no pod is reporting any error.
		// TODO: remove this once the leak is fixed
		defer job.Delete()
		logger.Infof("OK!\n")

		o.Eventually(func() (string, error) {
			return job.Logs("-c", "image-build")
		}, "5m", "10s").Should(o.ContainSubstring("Found 0 entitlement certificates"), "Error getting the logs")
		o.Eventually(job, "15m", "20s").Should(HaveConditionField("Failed", "status", TrueString), "Job didn't fail")
		logger.Infof("OK!\n")

		exutil.By("Remove the MachineOSConfig resource")
		o.Expect(DisableOCL(mosc)).To(o.Succeed(), "Error cleaning up %s", mosc)
		ValidateMOSCIsGarbageCollected(mosc, mcp)
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:88801][OTP][Skipped:Disconnected] ExternalRegistry OCB Verify new nodes boot directly with OCL image without unnecessary reboots [Disruptive]", func() {
		SkipIfCompactOrSNO(oc.AsAdmin())              // This test requires scaling, which doesn't make sense in SNO or Compact
		skipTestIfWorkersCannotBeScaled(oc.AsAdmin()) // Skip test if worker node cannot be scaled

		var (
			mcp      = GetCompactCompatiblePool(oc.AsAdmin())
			moscName = mcp.GetName()
		)

		exutil.By("Configure OCB functionality using external registry (Quay)")
		mosc, err := CreateMachineOSConfigUsingExternalRegistry(oc.AsAdmin(), moscName, mcp.GetName(), nil, false, false)
		defer DisableOCL(mosc)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource")
		logger.Infof("OK!\n")

		ValidateNewNodesBootDirectlyWithOCLImage(oc.AsAdmin(), mosc, mcp)
	})

	g.It("[PolarionID:85980][OTP][Skipped:Disconnected] Check when MOSB is degraded MCP should be degraded too with right fields updated. [Disruptive]", func() {
		var (
			infraMcpName = "infra"
			moscName     = infraMcpName
			// Intentionally uses 'apt' on Alpine (which uses 'apk'), causing build to fail
			incorrectContainerFile  = "FROM alpine:3.18\nRUN apt update && apt install -y cowsay\n"
			correctContainerFile    = "FROM configs AS final\nRUN echo \"hello\" > /etc/test.txt\n"
			expectedDegradedMessage = "Failed to build OS image"
		)

		exutil.By("Create custom infra MCP")
		infraMcp, err := CreateCustomMCP(oc.AsAdmin(), infraMcpName, 1)
		defer DeleteCustomMCP(oc.AsAdmin(), infraMcpName)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating custom MCP: %s", infraMcpName)
		node := infraMcp.GetNodesOrFail()[0]
		logger.Infof("Infra node: %s\n", node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Create MOSC with incorrect containerfile")
		mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, moscName, infraMcpName,
			[]ContainerFile{{Content: incorrectContainerFile}})
		defer mosc.CleanupAndDelete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating MOSC with incorrect containerfile")
		logger.Infof("OK!\n")

		exutil.By("Verify MOSB is created and fails")
		o.Eventually(mosc.GetCurrentMachineOSBuild, "5m", "20s").Should(Exist(),
			"No MOSB was created for the incorrect containerfile")
		failedMOSB, err := mosc.GetCurrentMachineOSBuild()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting MOSB from MOSC")
		logger.Infof("MOSB created: %s\n", failedMOSB.GetName())
		o.Eventually(failedMOSB, "20m", "20s").Should(HaveConditionField("Failed", "status", TrueString),
			"MOSB should fail due to incorrect containerfile")
		logger.Infof("MOSB failed as expected\n")

		exutil.By("Verify MCP is degraded with ImageBuildDegraded condition")
		o.Eventually(infraMcp, "5m", "20s").Should(BeDegraded(), "MCP should be degraded when build fails")
		o.Eventually(infraMcp, "5m", "20s").Should(HaveConditionField("ImageBuildDegraded", "status", TrueString),
			"MCP ImageBuildDegraded should be True when build fails")
		o.Eventually(infraMcp, "5m", "20s").Should(HaveConditionField("ImageBuildDegraded", "message", o.ContainSubstring(expectedDegradedMessage)),
			"MCP degradation message should indicate OS image build failure")
		o.Eventually(infraMcp, "5m", "20s").Should(HaveConditionField("Degraded", "message", o.ContainSubstring("Custom OS image build failed")),
			"MCP Degraded message should indicate custom OS image build failed")
		logger.Infof("OK!\n")

		exutil.By("Fix the MOSC by updating containerfile")
		o.Expect(mosc.SetContainerfiles([]ContainerFile{{Content: correctContainerFile}})).NotTo(o.HaveOccurred(),
			"Error updating MOSC with correct containerfile")
		logger.Infof("OK!\n")

		exutil.By("Verify new MOSB is created and succeeds")
		checkNewBuildIsTriggered(mosc, failedMOSB)
		newMOSB, err := mosc.GetCurrentMachineOSBuild()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting new MOSB")
		logger.Infof("New MOSB created: %s\n", newMOSB.GetName())

		exutil.By("Verify old failed MOSB is deleted")
		o.Eventually(failedMOSB, "5m", "20s").ShouldNot(Exist(),
			"Old failed MOSB should be garbage collected: %s", failedMOSB.GetName())
		logger.Infof("OK!\n")

		exutil.By("Verify MCP recovers and is no longer degraded")
		// Check both ImageBuildDegraded (specific condition) and Degraded (overall status)
		// to ensure complete recovery from the failed build state
		o.Eventually(infraMcp, "5m", "20s").Should(HaveConditionField("ImageBuildDegraded", "status", FalseString),
			"MCP ImageBuildDegraded condition should be False after recovery")
		o.Eventually(infraMcp, "5m", "20s").ShouldNot(BeDegraded(),
			"MCP Degraded condition should be False after recovery")
		o.Eventually(infraMcp, "5m", "20s").Should(HaveConditionField("Updating", "status", TrueString),
			"MCP should be updating after successful build")
		logger.Infof("OK!\n")

		exutil.By("Wait for MCP to complete update")
		infraMcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Verify image is applied to node")
		currentImagePullSpec := OrFail[string](mosc.GetStatusCurrentImagePullSpec())
		o.Expect(node.GetCurrentBootOSImage()).To(o.Equal(currentImagePullSpec),
			"Node should be using the correct OCL image after recovery")
		logger.Infof("Node is using image: %s\n", currentImagePullSpec)
		logger.Infof("OK!\n")

		exutil.By("Verify file created in containerfile exists on node")
		output, err := node.DebugNodeWithChroot("cat", "/etc/test.txt")
		o.Expect(err).NotTo(o.HaveOccurred(), "Error reading test file from node")
		o.Expect(output).To(o.ContainSubstring("hello"),
			"Test file content should match what was created in containerfile")
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:87176][OTP][Skipped:Disconnected] External Registry In OCB to check when a image is removed the old build is triggered again and the MC should start updating directly. [Disruptive]", func() {
		SkipIfCompactOrSNO(oc.AsAdmin())

		var (
			infraMcpName              = "infra"
			mcName                    = fmt.Sprintf("tc-%s-ext-kernelarg", GetCurrentTestPolarionIDNumber())
			kArgs                     = "test"
			containerFileNoExpiration = "FROM configs AS final\nLABEL maintainer=\"mco@example.com\"\n"
		)

		exutil.By("Create custom infra MCP")
		infraMcp, err := CreateCustomMCP(oc.AsAdmin(), infraMcpName, 1)
		defer DeleteCustomMCP(oc.AsAdmin(), infraMcpName)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a new custom pool: %s", infraMcpName)
		logger.Infof("OK!\n")

		exutil.By("Create MOSC for custom MCP using external registry")
		moscName := infraMcp.GetName()
		mosc, err := CreateMachineOSConfigUsingExternalRegistry(oc.AsAdmin(), moscName, infraMcp.GetName(),
			[]ContainerFile{{Content: containerFileNoExpiration}}, false, false)
		defer DisableOCL(mosc)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating MOSC")
		logger.Infof("OK!\n")

		exutil.By("Validate initial MOSC and wait for MOSB-1 to succeed")
		ValidateSuccessfulMOSC(mosc, nil)

		mosb1, err := mosc.GetCurrentMachineOSBuild()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting initial MOSB")
		logger.Infof("Initial MOSB created: %s\n", mosb1.GetName())

		exutil.By("Apply MC with kernel argument to trigger new MOSB")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, infraMcp.GetName())
		mc.skipWaitForMcp = true

		defer mc.DeleteWithWait()

		err = mc.Create("-p", "NAME="+mcName, "-p", "POOL="+infraMcp.GetName(),
			"-p", fmt.Sprintf(`KERNEL_ARGS=["%s"]`, kArgs))
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating MachineConfig %s", mc.GetName())
		logger.Infof("OK!\n")

		exutil.By("Wait for MOSB-2 to be created and succeed")
		checkNewBuildIsTriggered(mosc, mosb1)
		mosb2, err := mosc.GetCurrentMachineOSBuild()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting MOSB-2")
		logger.Infof("Second MOSB created: %s\n", mosb2.GetName())

		exutil.By("Wait for MCP to complete update")
		infraMcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Delete MOSB-1 image from Quay using automated skopeo deletion")
		o.Expect(removeQuayImageUsingSkepo(oc.AsAdmin(), mosb1, infraMcp)).To(o.BeTrue(), "Error deleting Quay image for MOSB-1")
		logger.Infof("OK!\n")

		// Common verification after MOSB-1 deletion
		verifyMOSBRebuildAfterImageDeletion(infraMcp, mosc, mosb1, mosb2, mc, mcName)
	})

})

func checkNewBuildIsTriggered(mosc *MachineOSConfig, currentMOSB *MachineOSBuild) {
	var (
		newMOSB *MachineOSBuild
		err     error
	)
	logger.Infof("Current mosb: %s", currentMOSB)
	o.Eventually(func() (string, error) {
		newMOSB, err = mosc.GetCurrentMachineOSBuild()
		if err != nil || newMOSB == nil {
			return "", err
		}
		return newMOSB.GetName(), nil
	}, "5m", "20s").ShouldNot(o.Equal(currentMOSB.GetName()),
		"A new MOSB should be created after the new rendered image pull spec is configured")

	logger.Infof("New mosb: %s", newMOSB)

	o.Eventually(newMOSB, "5m", "20s").Should(HaveConditionField("Building", "status", TrueString),
		"MachineOSBuild didn't report that the build has begun")

	o.Eventually(newMOSB, "20m", "20s").Should(HaveConditionField("Building", "status", FalseString), "Build was not finished")
	o.Eventually(newMOSB, "10m", "20s").Should(HaveConditionField("Succeeded", "status", TrueString), "Build didn't succeed")
	o.Eventually(newMOSB, "2m", "20s").Should(HaveConditionField("Interrupted", "status", FalseString), "Build was interrupted")
	o.Eventually(newMOSB, "2m", "20s").Should(HaveConditionField("Failed", "status", FalseString), "Build was failed")
}

func getNewNodeRebootValueForOCL(newNode *Node, oclImage string) {
	exutil.By("Verify the new node boots directly with the OCL image")
	currentImage, err := newNode.GetCurrentBootOSImage()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting current boot OS image from node %s", newNode.GetName())
	o.Expect(currentImage).To(o.Equal(oclImage), "New node %s is not using the OCL image. Current: %s, Expected: %s", newNode.GetName(), currentImage, oclImage)
	logger.Infof("OK!\n")

	exutil.By("Verify /etc/machine-config-daemon/currentimage matches the OCL image")
	currentImageFile, err := newNode.DebugNodeWithChroot("cat", "/etc/machine-config-daemon/currentimage")
	o.Expect(err).NotTo(o.HaveOccurred(), "Error reading currentimage file from node %s", newNode.GetName())
	o.Expect(currentImageFile).To(o.ContainSubstring(oclImage), "currentimage file does not contain the OCL image")
	logger.Infof("OK!\n")

	exutil.By("Verify rpm-ostree status shows the OCL image")
	rpmOstreeStatus, err := newNode.DebugNodeWithChroot("rpm-ostree", "status")
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting rpm-ostree status from node %s", newNode.GetName())
	o.Expect(rpmOstreeStatus).To(o.ContainSubstring(oclImage), "rpm-ostree status does not show the OCL image")
	logger.Infof("OK!\n")

	exutil.By("Verify no unnecessary reboots occurred - desiredImage file should not exist")
	desiredImageFile, err := newNode.DebugNodeWithChroot("cat", "/etc/machine-config-daemon/desiredImage")
	o.Expect(err).To(o.HaveOccurred(), "desiredImage file should not exist when node boots with correct OCL image")
	o.Expect(desiredImageFile).Should(o.ContainSubstring("No such file or directory"),
		"Expected desiredImage file to not exist, indicating no OS upgrade was needed")
	logger.Infof("OK!\n")
}

func ValidateNewNodesBootDirectlyWithOCLImage(oc *exutil.CLI, mosc *MachineOSConfig, mcp *MachineConfigPool) {
	ValidateSuccessfulMOSC(mosc, nil)

	exutil.By("Get the OCL image")
	oclImage := OrFail[string](mosc.GetStatusCurrentImagePullSpec())
	logger.Infof("OCL image: %s\n", oclImage)

	isInternalRegistry := OrFail[bool](mosc.IsUsingInternalRegistry())

	exutil.By("Check able to scale the node from existing Machineset")
	msl, err := NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAll()
	o.Expect(err).NotTo(o.HaveOccurred(), "Get machinesets failed")
	o.Expect(msl).ShouldNot(o.BeEmpty(), "Machineset list is empty")
	existingMS := msl[0]

	o.Expect(existingMS.AddToScale(1)).NotTo(o.HaveOccurred())

	defer func() {
		exutil.By("Scale down the node from existing Machineset")
		existingMS.AddToScale(-1)
		mcp.waitForComplete()
	}()

	exutil.By("Create duplicate machineset and scale new node")
	machineset := OrFail[*MachineSet](GetScalableMachineSet(oc.AsAdmin()))
	duplicateMSName := machineset.GetName() + "-ocl"
	duplicateMS, err := machineset.Duplicate(duplicateMSName)
	o.Expect(err).NotTo(o.HaveOccurred())

	defer func() {
		if duplicateMS.Exists() {
			logger.Infof("Deleting duplicate machineset %s and scale down the node", duplicateMS.GetName())
			o.Expect(duplicateMS.ScaleTo(0)).To(o.Succeed())
			o.Expect(duplicateMS.WaitUntilReady("10m")).To(o.Succeed())
			o.Expect(duplicateMS.Delete()).To(o.Succeed())
		}
	}()
	o.Expect(duplicateMS.ScaleTo(1)).To(o.Succeed())

	exutil.By("Wait for both existing new node added and for duplicate new node added to get ready")

	o.Eventually(func(gm o.Gomega) {
		gm.Expect(existingMS.GetIsReady()).To(o.BeTrue(), "MachineSet %s is not ready", existingMS.GetName())
		gm.Expect(duplicateMS.GetIsReady()).To(o.BeTrue(), "MachineSet %s is not ready", duplicateMS.GetName())
	}, "30m", "2m").Should(o.Succeed(), "MachineSets are not ready")

	if isInternalRegistry {
		logger.Infof("Internal registry detected, waiting for MCP to complete (requires 2 reboots)")
		mcp.waitForComplete()
	}
	logger.Infof("OK!\n")

	exutil.By("Get the new node created by the machine set")

	existingMSNodes, nErr := existingMS.GetNodes()
	o.Expect(nErr).NotTo(o.HaveOccurred(), "Error getting the nodes created by MachineSet %s", existingMS.GetName())
	existingNode := existingMSNodes[len(existingMSNodes)-1]
	logger.Infof("Existing node: %s\n", existingNode.GetName())
	logger.Infof("OK!\n")

	duplicateMSNodes, nErr := duplicateMS.GetNodes()
	o.Expect(nErr).NotTo(o.HaveOccurred(), "Error getting the nodes created by MachineSet %s", duplicateMS.GetName())
	duplicateMSNode := duplicateMSNodes[len(duplicateMSNodes)-1]
	logger.Infof("Duplicate node: %s\n", duplicateMSNode.GetName())
	logger.Infof("OK!\n")

	getNewNodeRebootValueForOCL(duplicateMSNode, oclImage)
	getNewNodeRebootValueForOCL(existingNode, oclImage)

	exutil.By("Wait for MCP to complete before scaling down nodes")
	mcp.waitForComplete()
	logger.Infof("OK!\n")
}

func removeImageStream(oc *exutil.CLI, mosb *MachineOSBuild) bool {
	mosc, err := mosb.GetMachineOSConfig()
	if err != nil {
		logger.Errorf("Error getting MOSC from MOSB: %s", err)
		return false
	}

	pushSpec, err := mosc.GetRenderedImagePushspec()
	if err != nil {
		logger.Errorf("Error getting renderedImagePushspec from MOSC: %s", err)
		return false
	}

	parts := strings.Split(pushSpec, "/")
	if len(parts) < 3 {
		logger.Errorf("Invalid push spec format: %s", pushSpec)
		return false
	}
	imageNameWithTag := parts[len(parts)-1]
	imageName := strings.Split(imageNameWithTag, ":")[0]

	logger.Infof("Deleting imagestream tag: %s:%s", imageName, mosb.GetName())
	imagestream, err := oc.AsAdmin().WithoutNamespace().Run("tag").Args("-d", imageName+":"+mosb.GetName(), "-n", MachineConfigNamespace).Output()

	if err != nil {
		logger.Errorf("Error deleting imagestream: %s", imagestream)
		return false
	}
	return true
}

func removeQuayImageUsingSkepo(oc *exutil.CLI, mosb *MachineOSBuild, mcp *MachineConfigPool) bool {
	mosc, err := mosb.GetMachineOSConfig()
	if err != nil {
		logger.Errorf("Error getting MOSC from MOSB: %s", err)
		return false
	}

	digestedImage, err := mosb.GetStatusDigestedImagePullSpec()
	if err != nil {
		logger.Errorf("Error getting digested image from MOSB: %s", err)
		return false
	}

	logger.Infof("Attempting to delete Quay image: %s", digestedImage)

	pushSecretName, err := mosc.Get(`{.spec.renderedImagePushSecret.name}`)
	if err != nil || pushSecretName == "" {
		logger.Errorf("Error getting push secret name from MOSC: %s", err)
		return false
	}

	logger.Infof("Using push secret for deletion: %s", pushSecretName)

	pushSecret := NewSecret(oc, MachineConfigNamespace, pushSecretName)
	secretDir, err := pushSecret.Extract()
	if err != nil {
		logger.Errorf("Error extracting push secret: %s", err)
		return false
	}
	defer func() {
		logger.Infof("Cleaning up local secret directory: %s", secretDir)
		os.RemoveAll(secretDir)
	}()
	logger.Infof("Push secret extracted to local directory: %s", secretDir)

	localAuthFile := filepath.Join(secretDir, ".dockerconfigjson")

	nodes, err := mcp.GetNodes()
	if err != nil || len(nodes) == 0 {
		logger.Errorf("Error getting nodes from MCP: %s", err)
		return false
	}
	node := nodes[0]

	logger.Infof("Running skopeo delete on node: %s", node.GetName())

	tmpAuthFile := "/tmp/skopeo-delete-auth-" + exutil.GetRandomString() + ".json"

	defer func() {
		logger.Infof("Cleaning up temporary auth file on node")
		node.DebugNodeWithChroot("rm", "-f", tmpAuthFile)
	}()

	err = node.CopyFromLocal(localAuthFile, tmpAuthFile)
	if err != nil {
		logger.Errorf("Error copying auth file to node: %s", err)
		return false
	}

	skopeoCommand := fmt.Sprintf("skopeo delete --authfile %s docker://%s", tmpAuthFile, digestedImage)
	fullCommand := fmt.Sprintf("set -a; source /etc/mco/proxy.env; %s", skopeoCommand)

	logger.Infof("Executing command on node: %s", skopeoCommand)

	stdout, stderr, err := node.DebugNodeWithChrootStd("sh", "-c", fullCommand)
	if err != nil {
		logger.Errorf("Error deleting Quay image via skopeo on node. Stdout: %s, Stderr: %s, Error: %s", stdout, stderr, err)
		return false
	}

	logger.Infof("Successfully deleted Quay image: %s", digestedImage)
	logger.Infof("Skopeo output: %s", stdout)
	return true
}

func verifyMOSBRebuildAfterImageDeletion(mcp *MachineConfigPool, mosc *MachineOSConfig, mosb1, mosb2 *MachineOSBuild, mc *MachineConfig, mcName string) {
	exutil.By("Delete MC")
	err := mc.Delete()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error deleting MC")
	logger.Infof("OK!\n")

	exutil.By("Verify MOSB-1 is re-triggered (starts building again)")
	o.Eventually(mosb1, "5m", "20s").Should(HaveConditionField("Building", "status", TrueString),
		"MOSB-1 should be re-triggered and start building again after image deletion")
	logger.Infof("MOSB-1 is re-building\n")

	o.Eventually(mosb1, "35m", "20s").Should(HaveConditionField("Building", "status", FalseString),
		"MOSB-1 rebuild should complete")
	o.Eventually(mosb1, "10m", "20s").Should(HaveConditionField("Succeeded", "status", TrueString),
		"MOSB-1 rebuild should succeed")
	logger.Infof("OK!\n")

	exutil.By("Wait for MCP to complete update")
	mcp.waitForComplete()
	logger.Infof("OK!\n")

	exutil.By("Re-apply MC")
	mc.skipWaitForMcp = true
	defer mc.DeleteWithWait()
	err = mc.Create("-p", "NAME="+mcName, "-p", "POOL="+mcp.GetName(),
		"-p", fmt.Sprintf(`KERNEL_ARGS=["%s"]`, "test"))
	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating MachineConfig %s", mc.GetName())
	logger.Infof("OK!\n")

	exutil.By("Verify MOSB-2 is NOT triggered again")
	o.Consistently(func() bool {
		buildingStatus, err := mosb2.Get(`{.status.conditions[?(@.type=="Building")].status}`)
		if err != nil || buildingStatus == "" {
			return false
		}
		return buildingStatus == TrueString
	}, "2m", "10s").Should(o.BeFalse(), "MOSB-2 should NOT start building again")
	logger.Infof("OK!\n")

	exutil.By("Verify MCP starts updating instead of triggering a new/old MOSB")
	o.Eventually(mcp, "5m", "20s").Should(HaveConditionField("Updating", "status", TrueString),
		"MCP should start updating instead of triggering a new/old MOSB")
	logger.Infof("OK!\n")

	exutil.By("Wait for MCP to complete update")
	mcp.waitForComplete()
	logger.Infof("OK!\n")

	exutil.By("Verify no new MOSB-3 was created during MCP update")
	currentMosb, err := mosc.GetCurrentMachineOSBuild()
	logger.Infof("Current MOSB: %s\n", currentMosb.GetName())
	logger.Infof("MOSB-1: %s\n", mosb1.GetName())
	logger.Infof("MOSB-2: %s\n", mosb2.GetName())
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting current MOSB")
	o.Expect(currentMosb.GetName()).To(o.Or(o.Equal(mosb1.GetName()), o.Equal(mosb2.GetName())),
		"Current MOSB should be either MOSB-1 or MOSB-2, no new MOSB-3 should be triggered")
	logger.Infof("OK!\n")
}
