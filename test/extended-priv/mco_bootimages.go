package extended

import (
	"fmt"
	"regexp"
	"strings"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	"github.com/openshift/machine-config-operator/test/extended-priv/util/architecture"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

const mapiBaseErrorMessageTemplate = `1 Degraded MAPI MachineSets | 0 Degraded ControlPlaneMachineSets | 0 Degraded CAPI MachineSets | 0 Degraded CAPI MachineDeployments | Error(s):` +
	` error syncing MAPI MachineSet %s: failed to reconcile machineset %s, err:`

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO Bootimages", func() {
	defer g.GinkgoRecover()

	var (
		oc = exutil.NewCLI("mco-bootimages", exutil.KubeConfigPath())
		// worker MachineConfigPool
		wMcp                 *MachineConfigPool
		machineConfiguration *MachineConfiguration
	)

	g.JustBeforeEach(func() {
		// Skip if no machineset
		SkipTestIfWorkersCannotBeScaled(oc.AsAdmin())
		// Bootimages Update functionality is only available in GCP(GA), AWS(GA), Vsphere(TP) and Azure(TP)
		skipTestIfSupportedPlatformNotMatched(oc, GCPPlatform, AWSPlatform, VspherePlatform, AzurePlatform)
		platform := exutil.CheckPlatform(oc)
		if platform == VspherePlatform {
			SkipIfNoFeatureGate(oc.AsAdmin(), "ManagedBootImagesvSphere")
		}
		if platform == AzurePlatform {
			SkipIfNoFeatureGate(oc.AsAdmin(), "ManagedBootImagesAzure")
		}

		wMcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		machineConfiguration = GetMachineConfiguration(oc.AsAdmin())
		PreChecks(oc)

		// Disable skew to avoid collisions
		exutil.By("Disabling skew functionality")
		initialMachineConfiguration := machineConfiguration.GetSpecOrFail()
		o.Expect(machineConfiguration.SetNoneSkew()).To(o.Succeed(), "Error disabing the skew functionality")
		o.Eventually(machineConfiguration.IsGenerationUpToDate, "2m", "10s").Should(o.BeTrue(), "MachineConfiguration observedGeneration did not catch up to generation")

		g.DeferCleanup(func() {
			exutil.By("Restoring initial MachineConfiguration spec")
			o.Expect(machineConfiguration.SetSpec(initialMachineConfiguration)).To(o.Succeed(), "Error restoring initial MachineConfiguration spec")
			logger.Infof("OK!\n")
		})
		logger.Infof("OK!\n")

	})

	g.It("[PolarionID:81403][OTP] In BootImages Machineset should update by default", g.Label("Platform:aws", "Platform:gce", "Platform:vsphere", "Platform:azure"), func() {

		// Not supported in Vsphere
		skipTestIfSupportedPlatformNotMatched(oc, GCPPlatform, AWSPlatform, VspherePlatform, AzurePlatform)

		var (
			duplicatedMachinesetName = fmt.Sprintf("cloned-tc-%s", GetCurrentTestPolarionIDNumber())
			firstMachineSet          = NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail()[0]
			fakeImageName            = getBackdatedBootImage(oc.AsAdmin())
		)

		exutil.By("Duplicate machineset for testing")
		machineSet, dErr := firstMachineSet.Duplicate(duplicatedMachinesetName)
		o.Expect(dErr).NotTo(o.HaveOccurred(), "Error duplicating %s", machineSet)
		defer machineSet.Delete()
		logger.Infof("OK!\n")

		exutil.By("Patch coreos boot image in MachineSet")
		o.Expect(machineSet.SetCoreOsBootImage(fakeImageName)).To(o.Succeed(),
			"Error patching the value of the coreos boot image in %s", machineSet)
		logger.Infof("OK!\n")

		exutil.By("Check that the MachineSet is updated by MCO by default")
		o.Eventually(machineSet.GetCoreOsBootImage, "3m", "20s").ShouldNot(o.Equal(fakeImageName),
			"The machineset should be updated by MCO if the functionality is not enabled in the MachineConfiguration resource. %s", machineSet.PrettyString())
		logger.Infof("OK!\n")

		// For none - mode i.e opt-out MachineSet are not updated with original value if we try to set with any fake value
		exutil.By("Opt-out boot images update")
		o.Expect(
			machineConfiguration.SetNoneManagedBootImagesConfig(MachineSetResource),
		).To(o.Succeed(), "Error configuring None managedBootImages in the 'cluster' MachineConfiguration resource")
		logger.Infof("OK!\n")

		exutil.By("Patch coreos boot image in MachineSet")
		o.Expect(machineSet.SetCoreOsBootImage(fakeImageName)).To(o.Succeed(),
			"Error patching the value of the coreos boot image in %s", machineSet)
		logger.Infof("OK!\n")

		exutil.By("Check that the MachineSet is not updated by MCO in opt-out")
		o.Eventually(machineSet.GetCoreOsBootImage, "3m", "20s").Should(o.Equal(fakeImageName),
			"The machineset should not be updated by MCO as we are opt-out in the MachineConfiguration resource. %s", machineSet.PrettyString())
		logger.Infof("OK!\n")

		exutil.By("Opt-in boot images update")
		o.Expect(
			machineConfiguration.SetPartialManagedBootImagesConfig(MachineSetResource, "", ""),
		).To(o.Succeed(), "Error configuring Partial managedBootImages in the 'cluster' MachineConfiguration resource")
		logger.Infof("OK!\n")

		exutil.By("Patch coreos boot image in MachineSet")
		o.Expect(machineSet.SetCoreOsBootImage(fakeImageName)).To(o.Succeed(),
			"Error patching the value of the coreos boot image in %s", machineSet)
		logger.Infof("OK!\n")

		exutil.By("Check that the MachineSet is updated by MCO for opt-in")
		o.Eventually(machineSet.GetCoreOsBootImage, "3m", "20s").ShouldNot(o.Equal(fakeImageName),
			"The machineset should not be updated by MCO if the functionality is not enabled in the MachineConfiguration resource. %s", machineSet.PrettyString())
		logger.Infof("OK!\n")

	})

	g.It("[PolarionID:74240][OTP] ManagedBootImages. Restore All MachineSet images", g.Label("Platform:aws", "Platform:gce", "Platform:vsphere", "Platform:azure"), func() {
		var (
			machineSet                 = NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail()[0]
			fakeImageName              = getBackdatedBootImage(oc.AsAdmin())
			clonedMSName               = "cloned-tc-74240"
			clonedWrongBootImageMSName = "cloned-tc-74240-wrong-boot-image"
			clonedOwnedMSName          = "cloned-tc-74240-owned"
		)

		exutil.By("Prepare to restore the original Machinesets")
		for _, item := range NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail() {
			ms := item
			logger.Infof("Preparing to restore machineset %s", ms.GetName())
			defer ms.SetSpec(ms.GetSpecOrFail())
		}
		logger.Infof("OK!\n")

		exutil.By("Opt-in boot images update")
		o.Expect(
			machineConfiguration.SetAllManagedBootImagesConfig(MachineSetResource),
		).To(o.Succeed(), "Error configuring ALL managedBootImages in the 'cluster' MachineConfiguration resource")
		logger.Infof("OK!\n")

		exutil.By("Clone first machineset")
		clonedMS, err := machineSet.Duplicate(clonedMSName)
		defer clonedMS.Delete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error duplicating %s", machineSet)
		logger.Infof("OK!\n")

		exutil.By("Clone first machineset but using a wrong ")
		clonedWrongImageMS, err := DuplicateMachineSetWithCustomBootImage(machineSet, fakeImageName, clonedWrongBootImageMSName)
		defer clonedWrongImageMS.Delete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error duplicating %s using a custom boot image", machineSet)
		logger.Infof("OK!\n")

		exutil.By("Clone first machineset, an owner reference will be added later to this new machineset")
		logger.Infof("Cloning machineset")
		clonedOwnedMS, err := machineSet.Duplicate(clonedOwnedMSName)
		defer clonedOwnedMS.Delete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error duplicating %s", machineSet)
		logger.Infof("Setting a fake owner")

		logger.Infof("OK!\n")

		exutil.By("All machinesets should use the right boot image")
		for _, ms := range NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail() {
			logger.Infof("Checking boot image in machineset %s", ms.GetName())
			// Check that the current boot image is the right one
			CheckCurrentOSImageIsUpdated(ms)
		}
		logger.Infof("OK!\n")

		// We add the owner once it has been updated to avoid race conditions
		exutil.By("Patch last cloned machineset to add an owner reference")
		o.Expect(
			clonedOwnedMS.Patch("merge", `{"metadata":{"ownerReferences": [{"apiVersion": "fake","blockOwnerDeletion": true,"controller": true,"kind": "fakekind","name": "master","uid": "fake-uuid"}]}}`),
		).To(o.Succeed(), "Error patching %s with a fake owner", clonedOwnedMS)
		logger.Infof("OK!\n")

		exutil.By("Patch cloned machinesets to use a wrong boot image")
		o.Expect(clonedMS.SetCoreOsBootImage(fakeImageName)).To(o.Succeed(),
			"Error setting a new boot image in %s", clonedMS)

		o.Expect(clonedWrongImageMS.SetCoreOsBootImage(fakeImageName)).To(o.Succeed(),
			"Error setting a new boot image in %s", clonedWrongImageMS)

		o.Expect(clonedOwnedMS.SetCoreOsBootImage(fakeImageName)).To(o.Succeed(),
			"Error setting a new boot image in %s", clonedOwnedMS)
		logger.Infof("OK!\n")

		exutil.By("All machinesets should use the right boot image except the one with an owner")
		for _, ms := range NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail() {
			logger.Infof("Checking boot image in machineset %s", ms.GetName())

			if ms.GetName() == clonedOwnedMSName {
				o.Consistently(ms.GetCoreOsBootImage, "15s", "5s").Should(o.Equal(fakeImageName),
					"%s was patched and it is using the right boot image. Machinesets with owners should NOT be patched.", ms)

			} else {
				// Check that it was actually updated
				o.Eventually(ms.GetCoreOsBootImage, "15m", "20s").ShouldNot(o.Or(o.Equal(fakeImageName), o.BeEmpty()),
					"%s was NOT updated to use the right boot image", ms)
				// Check that the updated image is the right one
				CheckCurrentOSImageIsUpdated(ms)
				// Check that the user-data secret is the right one
				o.Eventually(ms.GetUserDataSecret, "3m", "20s").ShouldNot(o.ContainSubstring("worker-user-data-managed"),
					"%s should NOT be using the worker-user-data-managed secret after updating the image", ms)
			}
		}
		logger.Infof("OK!\n")

		exutil.By("Scale up one of the fixed machinesets to make sure that they are working fine")
		logger.Infof("Scaling up machineset %s", clonedMS.GetName())
		defer wMcp.waitForComplete()
		defer clonedMS.ScaleTo(0)
		o.Expect(clonedMS.ScaleTo(1)).To(o.Succeed(),
			"Error scaling up MachineSet %s", clonedMS.GetName())
		logger.Infof("Waiting %s machineset for being ready", clonedMS)
		o.Eventually(clonedMS.GetIsReady, "20m", "2m").Should(o.BeTrue(), "MachineSet %s is not ready", clonedMS.GetName())
		// When the node is created it is still executing rpm-ostree commands before joining
		// If we delete the node (scale to 0) before MCO has fully finished its job, it can degrade the MCP
		// Hence, we wait for ndoes to be updated before reverting to the initial state
		o.Eventually(clonedMS.AllNodesUpdated, "10m", "30s").Should(o.BeTrue(), "Machineset's nodes were never updated")
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:74239][OTP] ManagedBootImages. Restore Partial MachineSet images", g.Label("Platform:aws", "Platform:gce", "Platform:vsphere", "Platform:azure"), func() {
		var (
			machineSet             = NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail()[0]
			fakeImageName          = getBackdatedBootImage(oc.AsAdmin())
			clonedMSLabelName      = "cloned-tc-74239-label"
			clonedMSNoLabelName    = "cloned-tc-74239-no-label"
			clonedMSLabelOwnedName = "cloned-tc-74239-label-owned"
			labelName              = "test"
			labelValue             = "update"
		)

		exutil.By("Opt-in boot images update")

		o.Expect(
			machineConfiguration.SetPartialManagedBootImagesConfig(MachineSetResource, labelName, labelValue),
		).To(o.Succeed(), "Error configuring Partial managedBootImages in the 'cluster' MachineConfiguration resource")
		logger.Infof("OK!\n")

		exutil.By("Clone the first machineset twice")
		clonedMSLabel, err := machineSet.Duplicate(clonedMSLabelName)
		defer clonedMSLabel.Delete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error duplicating %s", machineSet)

		clonedMSNoLabel, err := machineSet.Duplicate(clonedMSNoLabelName)
		defer clonedMSNoLabel.Delete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error duplicating %s", machineSet)
		logger.Infof("OK!\n")

		exutil.By("Clone first machineset again and set an owner for the cloned machineset")
		logger.Infof("Cloning machineset")
		clonedMSLabelOwned, err := machineSet.Duplicate(clonedMSLabelOwnedName)
		defer clonedMSLabelOwned.Delete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error duplicating %s", machineSet)
		logger.Infof("Setting a fake owner")

		o.Expect(
			clonedMSLabelOwned.Patch("merge", `{"metadata":{"ownerReferences": [{"apiVersion": "fake","blockOwnerDeletion": true,"controller": true,"kind": "fakekind","name": "master","uid": "fake-uuid"}]}}`),
		).To(o.Succeed(), "Error patching %s with a fake owner", clonedMSLabelOwned)
		logger.Infof("OK!\n")

		exutil.By("Label one of the cloned images and the clonned image with the owner configuration")
		o.Expect(clonedMSLabel.AddLabel(labelName, labelValue)).To(o.Succeed(),
			"Error labeling %s", clonedMSLabel)
		o.Expect(clonedMSLabelOwned.AddLabel(labelName, labelValue)).To(o.Succeed(),
			"Error labeling %s", clonedMSLabel)
		logger.Infof("OK!\n")

		exutil.By("Patch the clonned machineset to configure a new boot image")
		o.Expect(clonedMSLabel.SetCoreOsBootImage(fakeImageName)).To(o.Succeed(),
			"Error setting a new boot image in %s", clonedMSLabel)

		o.Expect(clonedMSNoLabel.SetCoreOsBootImage(fakeImageName)).To(o.Succeed(),
			"Error setting a new boot image in %s", clonedMSNoLabel)

		o.Expect(clonedMSLabelOwned.SetCoreOsBootImage(fakeImageName)).To(o.Succeed(),
			"Error setting a new boot image in %s", clonedMSLabelOwned)
		logger.Infof("OK!\n")

		exutil.By("The labeled machineset without owner should be updated")
		// Check that it was actually updated
		o.Eventually(clonedMSLabel.GetCoreOsBootImage, "15m", "20s").ShouldNot(o.Or(o.Equal(fakeImageName), o.BeEmpty()),
			"%s was NOT updated to use the right boot image", clonedMSLabel)
		// Check that the updated image is the right one
		CheckCurrentOSImageIsUpdated(clonedMSLabel)
		// Check that the user-data secret is the right one
		o.Eventually(clonedMSLabel.GetUserDataSecret, "3m", "20s").ShouldNot(o.ContainSubstring("worker-user-data-managed"),
			"%s should NOT be using the worker-user-data-managed secret after updating the image", clonedMSLabel)

		logger.Infof("OK!\n")

		exutil.By("The labeled machineset with owner should NOT be updated")
		o.Consistently(clonedMSLabelOwned.GetCoreOsBootImage, "15s", "5s").Should(o.Equal(fakeImageName),
			"%s was patched and it is using the right boot image. Machinesets with owners should NOT be patched.", clonedMSLabelOwned)
		logger.Infof("OK!\n")

		exutil.By("The machineset without label should NOT be updated")
		o.Consistently(clonedMSNoLabel.GetCoreOsBootImage, "15s", "5s").Should(o.Equal(fakeImageName),
			"%s was patched and it is using the right boot image. Machinesets with owners should NOT be patched.", clonedMSNoLabel)
		logger.Infof("OK!\n")

		exutil.By("Scale up the fixed machinessetset to make sure that it is working fine")
		logger.Infof("Scaling up machineset %s", clonedMSLabel.GetName())
		defer wMcp.waitForComplete()
		defer clonedMSLabel.ScaleTo(0)
		o.Expect(clonedMSLabel.ScaleTo(1)).To(o.Succeed(),
			"Error scaling up MachineSet %s", clonedMSLabel.GetName())
		logger.Infof("Waiting %s machineset for being ready", clonedMSLabel)
		o.Eventually(clonedMSLabel.GetIsReady, "20m", "2m").Should(o.BeTrue(), "MachineSet %s is not ready", clonedMSLabel.GetName())
		// When the node is created it is still executing rpm-ostree commands before joining
		// If we delete the node (scale to 0) before MCO has fully finished its job, it can degrade the MCP
		// Hence, we wait for ndoes to be updated before reverting to the initial state
		o.Eventually(clonedMSLabel.AllNodesUpdated, "10m", "30s").Should(o.BeTrue(), "Machineset's nodes were never updated")
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:74751][OTP] ManagedBootImages. Fix errors", g.Label("Platform:aws", "Platform:gce", "Platform:vsphere", "Platform:azure"), func() {
		var (
			machineConfiguration        = GetMachineConfiguration(oc.AsAdmin())
			machineSet                  = NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail()[0]
			fakeImageName               = getBackdatedBootImage(oc.AsAdmin())
			clonedMSName                = "cloned-tc-74751-copy"
			labelName                   = "test"
			labelValue                  = "update"
			fakearch                    = "fake-arch"
			expectedFailedMessageRegexp = regexp.QuoteMeta("Error(s): error syncing MAPI MachineSet " +
				clonedMSName +
				": failed to fetch arch during machineset sync: invalid architecture value found in annotation: " + fakearch)

			arch = machineSet.GetArchitectureOrFail()
		)
		exutil.By("Opt-in boot images update")

		o.Expect(
			machineConfiguration.SetPartialManagedBootImagesConfig(MachineSetResource, labelName, labelValue),
		).To(o.Succeed(), "Error configuring Partial managedBootImages in the 'cluster' MachineConfiguration resource")
		logger.Infof("OK!\n")

		exutil.By("Clone the first machineset")
		clonedMS, err := machineSet.Duplicate(clonedMSName)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error duplicating %s", machineSet)
		defer clonedMS.Delete()
		logger.Infof("OK!\n")

		exutil.By("Set a wrong architecture in the cloned image")
		o.Expect(clonedMS.SetArchitecture(fakearch)).To(o.Succeed(), "Error setting a fake architecture in %s", clonedMS)
		logger.Infof("OK!\n")

		exutil.By("Set a wrong boot image in the cloned image")
		o.Expect(clonedMS.SetCoreOsBootImage(fakeImageName)).To(o.Succeed(), "Error setting a fake boot image in %s", clonedMS)
		logger.Infof("OK!\n")

		exutil.By("Check that no failures are being reported")
		o.Eventually(machineConfiguration, "5m", "20s").Should(HaveConditionField("BootImageUpdateDegraded", "status", "False"),
			"Expected %s not to be BootImageUpdateDegraded.\n%s", machineConfiguration.PrettyString())

		o.Eventually(machineConfiguration, "5m", "20s").Should(HaveConditionField("BootImageUpdateProgressing", "status", "False"),
			"Expected %s not to be BootImageUpdateDegraded.\n%s", machineConfiguration.PrettyString())
		logger.Infof("OK!\n")

		exutil.By("Label the cloned machineset so that its boot image is updated by MCO")
		o.Expect(clonedMS.AddLabel(labelName, labelValue)).To(o.Succeed(),
			"Error labeling %s", clonedMS)
		logger.Infof("OK!\n")

		exutil.By("Check that an error is reported in the machineconfiguration resource and that there is no progress")
		o.Eventually(machineConfiguration, "5m", "20s").Should(HaveConditionField("BootImageUpdateDegraded", "status", "True"),
			"Expected %s to be BootImageUpdateDegraded.\n%s", machineConfiguration.PrettyString())

		o.Eventually(machineConfiguration, "5m", "20s").Should(HaveConditionField("BootImageUpdateDegraded", "message", o.MatchRegexp(expectedFailedMessageRegexp)),
			"Expected %s to be BootImageUpdateDegraded.\n%s", machineConfiguration.PrettyString())

		o.Eventually(machineConfiguration, "5m", "20s").Should(HaveConditionField("BootImageUpdateProgressing", "status", "False"),
			"Progress status is not the expected one.\n%s", machineConfiguration.PrettyString())

		// since it will be in "progressing" status for a very short time, we cant poll the value. We need to use the lasttransition date
		lastProgressTransition := machineConfiguration.GetOrFail(`{.status.conditions[?(@.type=="BootImageUpdateProgressing")].lastTransitionTime}`)
		logger.Infof("OK!\n")

		exutil.By("Set the right architecture in the cloneed machineset")
		o.Expect(clonedMS.SetArchitecture(arch.String())).To(o.Succeed(), "Error fixing the problem in the architecture in %s", clonedMS)
		logger.Infof("OK!\n")

		exutil.By("Check that no error is reported anymore in the machineconfiguration resource and the progress was OK")
		// We need to poll requently since it will be on "progressing" status a very short time
		o.Eventually(machineConfiguration, "20s", "1s").ShouldNot(HaveConditionField("BootImageUpdateProgressing", "lastTransitionTime", lastProgressTransition),
			"Progress status did not change, but it should have been moved to 'true' and back to 'false' .\n%s", machineConfiguration.PrettyString())

		o.Eventually(machineConfiguration, "2m", "10s").Should(HaveConditionField("BootImageUpdateProgressing", "status", "False"),
			"Progress status is not the expected one.\n%s", machineConfiguration.PrettyString())

		o.Eventually(machineConfiguration, "5m", "20s").Should(HaveConditionField("BootImageUpdateDegraded", "status", "False"),
			"Expected %s to be BootImageUpdateDegraded.\n%s", machineConfiguration.PrettyString())
		logger.Infof("OK!\n")

		exutil.By("Check that the boot image was updated")
		// Check that it was actually updated
		o.Eventually(clonedMS.GetCoreOsBootImage, "15m", "20s").ShouldNot(o.Or(o.Equal(fakeImageName), o.BeEmpty()),
			"%s was NOT updated to use the right boot image", clonedMS)
		// Check that the updated image is the right one
		CheckCurrentOSImageIsUpdated(clonedMS)
		logger.Infof("OK!\n")

	})

	g.It("[PolarionID:80436][OTP] Bootimage secret doesn't exist error upgrading stub ignition to spec 3", g.Label("Platform:aws", "Platform:gce", "Platform:vsphere", "Platform:azure"), func() {
		var (
			clonedMSName     = fmt.Sprintf("cloned-tc-%s-copy", GetCurrentTestPolarionIDNumber())
			clonedSecretName = fmt.Sprintf("cloned-user-data-%s-copy", GetCurrentTestPolarionIDNumber())
			// We make the the regexp end in a "$" to make sure that no more versions than the expected ones are present
			expectedFailedMessageRegexp = regexp.QuoteMeta(fmt.Sprintf(mapiBaseErrorMessageTemplate+
				` error grabbing user data secret referenced in machineset: secrets "%s" not found`, clonedMSName, clonedMSName, clonedSecretName)) + "$"
		)

		testUserDataUpdateFailure(oc, clonedMSName, clonedSecretName, expectedFailedMessageRegexp, nil)

	})

	g.It("[PolarionID:80435][OTP] Bootimage no json data error upgrading stub ignition to spec 3", g.Label("Platform:aws", "Platform:gce", "Platform:vsphere", "Platform:azure"), func() {
		var (
			clonedMSName     = fmt.Sprintf("cloned-tc-%s-copy", GetCurrentTestPolarionIDNumber())
			clonedSecretName = fmt.Sprintf("cloned-user-data-%s-copy", GetCurrentTestPolarionIDNumber())
			// We make the the regexp end in a "$" to make sure that no more versions than the expected ones are present
			expectedFailedMessageRegexp = regexp.QuoteMeta(fmt.Sprintf(mapiBaseErrorMessageTemplate+
				" failed to unmarshal decoded user-data to json (secret %s): invalid character 'h' in literal true (expecting 'r')t", clonedMSName, clonedMSName, clonedSecretName)) + "$"
		)

		setNotJSONUserData := func(_ string) (string, error) {
			logger.Infof("Setting a wrong not-json ignition data in the user-data secret")
			return "this is not json {data}", nil
		}

		testUserDataUpdateFailure(oc, clonedMSName, clonedSecretName, expectedFailedMessageRegexp, setNotJSONUserData)
	})

	g.It("[PolarionID:80434][OTP] Bootimage wrong version error upgrading stub ignition to spec 3", g.Label("Platform:aws", "Platform:gce", "Platform:vsphere", "Platform:azure"), func() {
		var (
			wrongIgnitionVersion = "1.2.0"
			clonedMSName         = fmt.Sprintf("cloned-tc-%s-copy", GetCurrentTestPolarionIDNumber())
			clonedSecretName     = fmt.Sprintf("cloned-user-data-%s-copy", GetCurrentTestPolarionIDNumber())
			// We make the the regexp end in a "$" to make sure that no more versions than the expected ones are present
			expectedFailedMessageRegexp = regexp.QuoteMeta(fmt.Sprintf(mapiBaseErrorMessageTemplate+
				" converting ignition stub failed: failed to parse Ignition config: parsing Ignition config failed:"+
				" unknown version. Supported spec versions: 2.2,3.0,3.1,3.2,3.3,3.4,3.5", clonedMSName, clonedMSName)) + "$"
		)

		setWrongIgnitionVersion := func(userData string) (string, error) {
			logger.Infof("Setting a wrong ignition version in the user-data secret")
			userDataV2, err := ConvertUserDataIgnition3ToIgnition2(userData)
			if err != nil {
				logger.Errorf("Error converting the userData info to ignition V2")
				return "", err
			}
			userDataV2, err = sjson.Set(userDataV2, "ignition.version", wrongIgnitionVersion)
			if err != nil {
				logger.Errorf("Error setting the new ignition version")
				return "", err
			}
			return userDataV2, nil
		}

		testUserDataUpdateFailure(oc, clonedMSName, clonedSecretName, expectedFailedMessageRegexp, setWrongIgnitionVersion)
	})

	g.It("[PolarionID:81395][OTP] Verify in boot-image by default update is opt-in", g.Label("Platform:aws", "Platform:gce", "Platform:vsphere", "Platform:azure"), func() {

		// Not supported in Vsphere
		skipTestIfSupportedPlatformNotMatched(oc, GCPPlatform, AWSPlatform, VspherePlatform, AzurePlatform)

		exutil.By("To check the default opt-in in machieconfiguration")
		if !strings.Contains(machineConfiguration.GetSpecOrFail(), "managedBootImages") {
			checkManagedBootImagesStatus(machineConfiguration, "All")
		} else {
			o.Expect(machineConfiguration.GetSpecOrFail()).Should(o.ContainSubstring(machineConfiguration.GetOrFail(`{.status.managedBootImagesStatus.machineManagers[0].selection.mode}`)))
		}
		logger.Infof("OK!\n")

		exutil.By("To patch the Partial Mode in machineConfiguration")
		o.Expect(
			machineConfiguration.SetPartialManagedBootImagesConfig(MachineSetResource, "", ""),
		).To(o.Succeed(), "Error configuring Partial managedBootImages in the 'cluster' MachineConfiguration resource")
		logger.Infof("OK!\n")
		checkManagedBootImagesStatus(machineConfiguration, "Partial")
		logger.Infof("OK\n")

		exutil.By("To patch the All Mode in machieConfiguration")
		o.Expect(
			machineConfiguration.SetAllManagedBootImagesConfig(MachineSetResource),
		).To(o.Succeed(), "Error configuring All managedBootImages in the 'cluster' MachineConfiguration resource")
		logger.Infof("OK!\n")
		checkManagedBootImagesStatus(machineConfiguration, "All")
		logger.Infof("OK\n")

		exutil.By("Opt-out boot images update")
		o.Expect(machineConfiguration.SetNoneManagedBootImagesConfig(MachineSetResource)).To(o.Succeed(), "Error configuring None managedBootImages in the 'cluster' MachineConfiguration resource")
		logger.Infof("OK!\n")
		checkManagedBootImagesStatus(machineConfiguration, "None")
		logger.Infof("OK\n")
	})

	g.It("[PolarionID:80437][OTP] Bootimage upgrade stub ignition to spec 3", g.Label("Platform:aws", "Platform:gce", "Platform:vsphere", "Platform:azure"), func() {

		var (
			clonedMSName     = fmt.Sprintf("cloned-tc-%s-copy", GetCurrentTestPolarionIDNumber())
			clonedSecretName = fmt.Sprintf("cloned-user-data-%s-copy", GetCurrentTestPolarionIDNumber())

			machineConfiguration = GetMachineConfiguration(oc.AsAdmin())
			machineSet           = NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail()[0]
			fakeImageName        = getBackdatedBootImage(oc.AsAdmin())
			labelName            = "test"
			labelValue           = "update"

			userDataJSONVersionPath = `ignition.version`
		)

		exutil.By("Opt-in boot images update")

		o.Expect(
			machineConfiguration.SetPartialManagedBootImagesConfig(MachineSetResource, labelName, labelValue),
		).To(o.Succeed(), "Error configuring Partial managedBootImages in the 'cluster' MachineConfiguration resource")
		logger.Infof("OK!\n")

		exutil.By("Clone the first machineset")
		clonedMS, err := machineSet.Duplicate(clonedMSName)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error duplicating %s", machineSet)
		defer clonedMS.Delete()
		logger.Infof("OK!\n")

		exutil.By("Set a 2.2.0 user-data secet in the new machine config")
		logger.Infof("Duplicating the user-data secret")
		userDataSecret, err := clonedMS.GetUserDataSecret()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting user-data secret from %s", clonedMS)

		userDataModifyFunc := func(userData string) (string, error) { return convertUserDataToNewVersion(userData, "2.2.0") }
		clonedSecret, err := duplicateMachinesetSecret(oc.AsAdmin(), userDataSecret.GetName(), clonedSecretName, userDataModifyFunc, nil)
		defer clonedSecret.Delete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error duplicating %s with a wrong ignition V2 version", userDataSecret)
		logger.Infof("OK!\n")

		logger.Infof("Configuring the cloned machineset to use the new user-data secret")
		o.Expect(clonedMS.SetUserDataSecret(clonedSecretName)).To(o.Succeed(),
			"Error patching MachineSet %s to use the new secret %s", clonedMS.GetName(), clonedSecretName)
		logger.Infof("OK!\n")

		exutil.By("Set a wrong boot image in the cloned image. Not Marketplace image. Updateable")
		o.Expect(clonedMS.SetCoreOsBootImage(fakeImageName)).To(o.Succeed(), "Error setting a fake boot image in %s", clonedMS)
		logger.Infof("OK!\n")

		exutil.By("Label the cloned machineset so that its boot image is updated by MCO")
		o.Expect(clonedMS.AddLabel(labelName, labelValue)).To(o.Succeed(),
			"Error labeling %s", clonedMS)
		logger.Infof("OK!\n")

		exutil.By("Check that the cloned user-data secret is updated to the latest ignintion version")
		// We wait 15 minutes because in vsphere platforms we need to give time to MCO so that it can upload the ova file to cloud
		o.Eventually(clonedSecret.GetDataValue, "15m", "15s").WithArguments("userData").Should(
			HavePathWithValue(userDataJSONVersionPath, o.Equal(IgnitionDefaultVersion)),
			"The user-data secret was not updated to the latest ignition version")

		logger.Infof("OK!\n")

		exutil.By("Check that the boot image was updated with the right version")
		// Check that it was actually updated
		o.Eventually(clonedMS.GetCoreOsBootImage, "15m", "20s").ShouldNot(o.Or(o.Equal(fakeImageName), o.BeEmpty()),
			"%s was NOT updated to use the right boot image", clonedMS)
		// Check that the updated image is the right one
		CheckCurrentOSImageIsUpdated(clonedMS)
		logger.Infof("OK!\n")

		exutil.By("Scale up the updated machineset to make sure that they are working fine")
		logger.Infof("Scaling up machineset %s", clonedMS.GetName())
		defer wMcp.waitForComplete()
		defer clonedMS.ScaleTo(0)
		o.Expect(clonedMS.ScaleTo(1)).To(o.Succeed(),
			"Error scaling up MachineSet %s", clonedMS.GetName())
		logger.Infof("Waiting %s machineset for being ready", clonedMS)
		o.Eventually(clonedMS.GetIsReady, "20m", "2m").Should(o.BeTrue(), "MachineSet %s is not ready", clonedMS.GetName())
		// When the node is created it is still executing rpm-ostree commands before joining
		// If we delete the node (scale to 0) before MCO has fully finished its job, it can degrade the MCP
		// Hence, we wait for ndoes to be updated before reverting to the initial state
		o.Eventually(clonedMS.AllNodesUpdated, "10m", "30s").Should(o.BeTrue(), "Machineset's nodes were never updated")
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:82747][OTP] Correctly handle marketplace bootimages", g.Label("Platform:aws", "Platform:gce"), func() {
		// There is no marketplace exemption for Vsphere, we skip the test case
		// After talking with devs this test case doesn't make sense in Azure.
		// In Azure we shouldn't be allowed to manipulate the values to set invalid values, and we will always update legacy images. Hence, we skip this test case.
		skipTestIfSupportedPlatformNotMatched(oc, GCPPlatform, AWSPlatform)

		var (
			clonedMSName     = fmt.Sprintf("cloned-tc-%s-copy", GetCurrentTestPolarionIDNumber())
			clonedSecretName = fmt.Sprintf("cloned-user-data-%s-copy", GetCurrentTestPolarionIDNumber())

			fakeImageName = "fake-image" // not updateable

			machineConfiguration = GetMachineConfiguration(oc.AsAdmin())
			machineSet           = NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail()[0]
			labelName            = "test"
			labelValue           = "update"

			userDataJSONVersionPath = `ignition.version`
		)

		exutil.By("Opt-in boot images update")
		o.Expect(
			machineConfiguration.SetPartialManagedBootImagesConfig(MachineSetResource, labelName, labelValue),
		).To(o.Succeed(), "Error configuring Partial managedBootImages in the 'cluster' MachineConfiguration resource")
		logger.Infof("OK!\n")

		exutil.By("Clone the first machineset")
		clonedMS, err := machineSet.Duplicate(clonedMSName)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error duplicating %s", machineSet)
		defer clonedMS.Delete()
		logger.Infof("OK!\n")

		exutil.By("Set a 2.2.0 user-data secet in the new machine config")
		logger.Infof("Duplicating the user-data secret")
		userDataSecret, err := clonedMS.GetUserDataSecret()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting user-data secret from %s", clonedMS)

		userDataModifyFunc := func(userData string) (string, error) { return convertUserDataToNewVersion(userData, "2.2.0") }
		clonedSecret, err := duplicateMachinesetSecret(oc.AsAdmin(), userDataSecret.GetName(), clonedSecretName, userDataModifyFunc, nil)
		defer clonedSecret.Delete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error duplicating %s with a wrong ignition V2 version", userDataSecret)
		logger.Infof("OK!\n")

		logger.Infof("Configuring the cloned machineset to use the new user-data secret")
		o.Expect(clonedMS.SetUserDataSecret(clonedSecretName)).To(o.Succeed(),
			"Error patching MachineSet %s to use the new secret %s", clonedMS.GetName(), clonedSecretName)
		logger.Infof("OK!\n")

		exutil.By("Set a wrong boot image in the cloned image. Non-updateable image")
		o.Expect(clonedMS.SetCoreOsBootImage(fakeImageName)).To(o.Succeed(), "Error setting a fake boot image in %s", clonedMS)
		logger.Infof("OK!\n")

		exutil.By("Label the cloned machineset so that its boot image is updated by MCO")
		o.Expect(clonedMS.AddLabel(labelName, labelValue)).To(o.Succeed(),
			"Error labeling %s", clonedMS)
		logger.Infof("OK!\n")

		exutil.By("Check that the bootimage was not updated")
		o.Consistently(clonedMS.GetCoreOsBootImage, "5m", "20s").Should(o.ContainSubstring(fakeImageName),
			"%s was updated, but it shouldn't be updated", clonedMS)
		logger.Infof("OK!\n")

		exutil.By("Check that the cloned user-data secret was not updated")
		o.Consistently(clonedSecret.GetDataValue, "2m", "15s").WithArguments("userData").Should(
			HavePathWithValue(userDataJSONVersionPath, o.Equal("2.2.0")),
			"The user-data secret was not updated, but it shouldn't be updated")
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:83998][OTP] Check in the boot image controller to work with multiple labels for annotation", g.Label("Platform:aws", "Platform:gce", "Platform:vsphere", "Platform:azure"), func() {
		var (
			clonedMSName         = fmt.Sprintf("cloned-tc-%s-copy", GetCurrentTestPolarionIDNumber())
			machineSet           = NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail()[0]
			machineConfiguration = GetMachineConfiguration(oc.AsAdmin())
			arch                 = machineSet.GetArchitectureOrFail()
		)

		exutil.By("Clone the first machineset")
		clonedMS, err := machineSet.Duplicate(clonedMSName)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error duplicating %s", machineSet)
		defer clonedMS.Delete()
		logger.Infof("OK!\n")

		exutil.By("Patch different architecture in the cloneed machineset to check error is not reported")
		setArchitectureAndCheckStatus(clonedMS, machineConfiguration, "kubernetes.io/arch=amd64,topology.ebs.csi.aws.com/zone=eu-central-1a")

		setArchitectureAndCheckStatus(clonedMS, machineConfiguration, "kubernetes.io/arch=amd64, topology.ebs.csi.aws.com/zone=eu-central-1a")

		setArchitectureAndCheckStatus(clonedMS, machineConfiguration, "topology.ebs.csi.aws.com/zone=eu-central-1a,kubernetes.io/arch=amd64")

		setArchitectureAndCheckStatus(clonedMS, machineConfiguration, "kubernetes.io/arch=s390x,topology.ebs.csi.aws.com/zone=eu-central-1a,node.kubernetes.io/instance-type=m5.large")

		exutil.By("Set the original architecture in the cloneed machineset")
		setArchitectureAndCheckStatus(clonedMS, machineConfiguration, arch.String())
	})
})

func DuplicateMachineSetWithCustomBootImage(ms *MachineSet, newBootImage, newName string) (*MachineSet, error) {

	var (
		platform = exutil.CheckPlatform(ms.GetOC().AsAdmin())
	)

	coreOSBootImagePath, err := ms.GetCoreOSBootImagePath(platform)
	if err != nil {
		return nil, err
	}

	// Patch is given like /spec/template/spec/providerSpec/value/ami/id
	// but in sjson library we need the path like spec.template.spec.providerSpec.valude.ami.id
	// so we transform the string
	jsonCoreOSBootImagePath := strings.ReplaceAll(strings.TrimPrefix(coreOSBootImagePath, "/"), "/", ".")

	res, err := CloneResource(ms, newName, ms.GetNamespace(),
		// Extra modifications to
		// 1. Create the resource with 0 replicas
		// 2. modify the selector matchLabels
		// 3. modify the selector template metadata labels
		// 4. set the provided boot image
		func(resString string) (string, error) {
			newResString, err := sjson.Set(resString, "spec.replicas", 0)
			if err != nil {
				return "", err
			}

			newResString, err = sjson.Set(newResString, `spec.selector.matchLabels.machine\.openshift\.io/cluster-api-machineset`, newName)
			if err != nil {
				return "", err
			}

			newResString, err = sjson.Set(newResString, `spec.template.metadata.labels.machine\.openshift\.io/cluster-api-machineset`, newName)
			if err != nil {
				return "", err
			}

			newResString, err = sjson.SetRaw(newResString, jsonCoreOSBootImagePath, QuoteIfNotJSON(newBootImage))
			if err != nil {
				return "", err
			}

			return newResString, nil
		},
	)

	if err != nil {
		return nil, err
	}

	logger.Infof("A new machineset %s has been created by cloning %s", res.GetName(), ms.GetName())
	return NewMachineSet(ms.oc, res.GetNamespace(), res.GetName()), nil
}

// getCoreOsBootImageFromConfigMap retrieves the boot image from the coreos-bootimages ConfigMap for the given platform and architecture
func getCoreOsBootImageFromConfigMap(platform, region string, arch architecture.Architecture, coreosBootimagesCM *ConfigMap) (string, error) {
	var (
		coreOsBootImagePath string
		// transform amd64 naming to x86_64 naming
		stringArch = arch.GNUString()
	)

	logger.Infof("Looking for coreos boot image for architecture %s in %s", stringArch, coreosBootimagesCM)

	streamJSON, err := coreosBootimagesCM.GetDataValue("stream")
	if err != nil {
		return "", err
	}
	parsedStream := gjson.Parse(streamJSON)

	switch platform {
	case AWSPlatform:
		if region == "" {
			return "", fmt.Errorf("Region is empty for platform %s. The region is mandatory if we want to get the boot image value", platform)
		}
		coreOsBootImagePath = fmt.Sprintf(`architectures.%s.images.%s.regions.%s.image`, stringArch, platform, region)
	case GCPPlatform:
		coreOsBootImagePath = fmt.Sprintf(`architectures.%s.images.%s.name`, stringArch, platform)
	case VspherePlatform:
		// There is no such thing as a "bootimage in vsphere", we need to manually upload it always. We return the version instead, since it is the only info we can use to verify the bootimage
		// in vsphere platform, the key is "vmware" and not "vsphere"
		coreOsBootImagePath = fmt.Sprintf(`architectures.%s.artifacts.%s.release`, stringArch, "vmware")
	case AzurePlatform:
		coreOsBootImagePath = fmt.Sprintf(`architectures.%s.rhel-coreos-extensions.marketplace.%s.no-purchase-plan.hyperVGen2`, stringArch, "azure")
	default:
		return "", fmt.Errorf("Machineset.GetCoreOsBootImage method is only supported for GCP, Vsphere, Azure, and AWS platforms")
	}

	currentCoreOsBootImage := parsedStream.Get(coreOsBootImagePath).String()

	if currentCoreOsBootImage == "" {
		logger.Warnf("The coreos boot image for architecture %s in %s IS EMPTY. ImagePath: %s", stringArch, coreosBootimagesCM, coreOsBootImagePath)
	}

	return currentCoreOsBootImage, nil
}

// getCoreOsBootImageFromConfigMapOrFail gets the boot image and fails the test if there's an error
func getCoreOsBootImageFromConfigMapOrFail(platform, region string, arch architecture.Architecture, coreosBootimagesCM *ConfigMap) string {
	image, err := getCoreOsBootImageFromConfigMap(platform, region, arch, coreosBootimagesCM)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the boot image from %s for platform %s and arch %s", coreosBootimagesCM, platform, arch)
	return image
}

// GetRHCOSVersionFromConfigMap retrieves the RHCOS release version from the coreos-bootimages ConfigMap
func GetRHCOSVersionFromConfigMap(oc *exutil.CLI) string {
	coreosBootimagesCM := NewConfigMap(oc.AsAdmin(), MachineConfigNamespace, "coreos-bootimages")
	streamJSON, err := coreosBootimagesCM.GetDataValue("stream")
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting stream data from coreos-bootimages configmap")

	parsedStream := gjson.Parse(streamJSON)
	// Get the release version from  aws artifacts
	rhcosVersion := parsedStream.Get("architectures.x86_64.artifacts.aws.release").String()
	o.Expect(rhcosVersion).NotTo(o.BeEmpty(), "RHCOS version not found in coreos-bootimages configmap")

	return rhcosVersion
}

// testUserDataUpdateFailure function that executes the common parts of the update spec v3 negative test cases
func testUserDataUpdateFailure(oc *exutil.CLI, clonedMSName, clonedSecretName, expectedFailedMessageRegexp string, userDataModifyFunc func(userData string) (string, error)) {

	var (
		machineConfiguration   = GetMachineConfiguration(oc.AsAdmin())
		machineSet             = NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail()[0]
		fakeImageName          = getBackdatedBootImage(oc.AsAdmin())
		labelName              = "test"
		labelValue             = "update"
		secondLabelValue       = "update2"
		machineClusterOperator = NewResource(oc.AsAdmin(), "ClusterOperator", "machine-config")
		clonedSecret           *Secret
	)

	exutil.By("Opt-in boot images update")
	o.Expect(
		machineConfiguration.SetPartialManagedBootImagesConfig(MachineSetResource, labelName, labelValue),
	).To(o.Succeed(), "Error configuring Partial managedBootImages in the 'cluster' MachineConfiguration resource")
	logger.Infof("OK!\n")

	exutil.By("Clone the first machineset")
	clonedMS, err := machineSet.Duplicate(clonedMSName)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error duplicating %s", machineSet)
	defer clonedMS.Delete()
	logger.Infof("OK!\n")

	exutil.By("Set a wrong user-data secret in the cloned machineset")
	if userDataModifyFunc != nil {
		logger.Infof("Duplicating the user-data secret")
		userDataSecret, err := clonedMS.GetUserDataSecret()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting user-data secret from %s", clonedMS)

		clonedSecret, err = duplicateMachinesetSecret(oc.AsAdmin(), userDataSecret.GetName(), clonedSecretName, userDataModifyFunc, nil)
		defer clonedSecret.Delete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error duplicating %s with a wrong ignition V2 version", userDataSecret)

	} else {
		logger.Infof("The %s user-data secret will not be created. Testing with a non-existing user-data secret", clonedSecretName)
	}

	logger.Infof("Configuring the cloned machineset to use the new user-data secret")
	o.Expect(clonedMS.SetUserDataSecret(clonedSecretName)).To(o.Succeed(),
		"Error patching MachineSet %s to use the new secret %s", clonedMS.GetName(), clonedSecretName)
	logger.Infof("OK!\n")
	exutil.By("Set a wrong boot image in the cloned image")
	o.Expect(clonedMS.SetCoreOsBootImage(fakeImageName)).To(o.Succeed(), "Error setting a fake boot image in %s", clonedMS)
	logger.Infof("OK!\n")

	exutil.By("Label the cloned machineset so that its boot image is updated by MCO")
	o.Expect(clonedMS.AddLabel(labelName, labelValue)).To(o.Succeed(),
		"Error labeling %s", clonedMS)
	logger.Infof("OK!\n")

	exutil.By("Check that an error is reported in the machineconfiguration resource")
	o.Eventually(machineConfiguration, "5m", "20s").Should(HaveConditionField("BootImageUpdateDegraded", "status", "True"),
		"Expected %s to be BootImageUpdateDegraded.\n%s", machineConfiguration.PrettyString())

	o.Eventually(machineConfiguration, "5m", "20s").Should(HaveConditionField("BootImageUpdateDegraded", "message", o.MatchRegexp(expectedFailedMessageRegexp)),
		"Expected %s to be BootImageUpdateDegraded.\n%s", machineConfiguration.PrettyString())

	logger.Infof("OK!\n")

	exutil.By("Check that the machine-config CO is degraded reporting the right message")
	o.Eventually(machineClusterOperator, "5m", "10s").Should(BeDegraded(),
		"%s is not degraded when the user-data uses a wrong ignition version", machineClusterOperator)
	o.Eventually(machineClusterOperator, "5m", "10s").Should(HaveDegradedMessage(o.MatchRegexp(expectedFailedMessageRegexp)),
		"%s is not degraded when the user-data uses a wrong ignition version", machineClusterOperator)

	logger.Infof("OK!\n")

	exutil.By("Remove the machineset from the updated list")
	o.Expect(
		machineConfiguration.SetPartialManagedBootImagesConfig(MachineSetResource, labelName, secondLabelValue),
	).To(o.Succeed(), "Error re-configuring the Partial managedBootImages in the 'cluster' MachineConfiguration resource")
	logger.Infof("OK!\n")

	exutil.By("Check that the status is restored")
	o.Eventually(machineConfiguration, "5m", "20s").Should(HaveConditionField("BootImageUpdateDegraded", "status", "False"),
		"Expected %s NOT to be BootImageUpdateDegraded.\n%s", machineConfiguration.PrettyString())
	o.Eventually(machineClusterOperator, "5m", "10s").ShouldNot(BeDegraded(),
		"%s is still degraded after removing the machineset is not updated anymore", machineClusterOperator)
	logger.Infof("OK!\n")

	checkMCCPanic(oc)
}

// checkManagedBootImagesStatus helps to verify the mode is updated in ManagedBootStatus after we patch the new changes in managedBootImages spec field
func checkManagedBootImagesStatus(mc *MachineConfiguration, mode string) {
	exutil.By("Check the ManagedBootImage Status")
	o.Eventually(func() (string, error) {
		mbiStatus, err := mc.Get(`{.status.managedBootImagesStatus.machineManagers[0].selection.mode}`)
		logger.Infof("%s", mbiStatus)
		return mbiStatus, err
	}, "5m", "10s").
		Should(o.Equal(mode), "Error: The %s mode does not match even after patched", mode)
}

// getBackdatedBootImage returns a valid boot image value for testing based on platform
// MCO will only update images previously published in the installer. This function returns one of those valid images
func getBackdatedBootImage(oc *exutil.CLI) string {
	var (
		platform = exutil.CheckPlatform(oc)
	)

	switch platform {
	case AWSPlatform:
		// MCO will only update AMIS present in the list defined here https://github.com/openshift/machine-config-operator/pull/5122
		// We choose one of them
		return "ami-0ffec236307e00b94"
	case GCPPlatform:
		// In GCP all images located in projects/rhcos-cloud/global/images are considered valid for update
		return "projects/rhcos-cloud/global/images" + "/updateble-fake-image"
	case AzurePlatform:
		// In Azure we need to configure the whole image, not only one field. We need an image in resourceID and an empty sku field
		// We use a similar resourceID as the one generated in a normal installation. Note that it contains "gen2", so it should use "hyperVGen2"
		return `{"offer":"","publisher":"","resourceID":"/resourceGroups/fake-499nn-rg/providers/Microsoft.Compute/galleries/gallery_fake21az_499nn/images/fake-499nn-gen2/versions/latest","sku":"","version":""}`
	case VspherePlatform:
		// In Vsphere we need the image to be present in the vcenter, so we need to manually upload it
		var (
			// We will use 4.16 as the original version that will be updated to the current version
			imageVersion = "4.16"
			// Vsphere only support AMD64
			arch = architecture.AMD64
		)

		// Get the right base image name from the rhcos json info stored in the github repositories
		exutil.By(fmt.Sprintf("Get the base image for version %s", imageVersion))
		rhcosHandler, err := GetRHCOSHandler(platform)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the rhcos handler")

		baseImage, err := rhcosHandler.GetBaseImageFromRHCOSImageInfo(imageVersion, arch, "")
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the base image")
		logger.Infof("Using base image %s", baseImage)

		baseImageURL, err := rhcosHandler.GetBaseImageURLFromRHCOSImageInfo(imageVersion, arch)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the base image URL")

		// To avoid collisions we will add prefix to identify our image
		baseImage = "mcotest-" + baseImage
		o.Expect(
			uploadBaseImageToCloud(oc, platform, baseImageURL, baseImage),
		).To(o.Succeed(), "Error uploading the base image %s to the cloud", baseImageURL)
		logger.Infof("Uplodated: %s", baseImage)
		logger.Infof("OK!\n")

		return baseImage
	default:
		return ""
	}
}

// getReleaseFromVsphereTemplate gets the release version from a vSphere template
func getReleaseFromVsphereTemplate(oc *exutil.CLI, vsphereTemplate string) (string, error) {
	vsInfo, err := exutil.GetVSphereConnectionInfo(oc.AsAdmin())
	if err != nil {
		return "", err
	}

	return exutil.GetReleaseFromVsphereTemplate(vsphereTemplate, vsInfo.Server, vsInfo.DataCenter, vsInfo.User, vsInfo.Password)
}

// CheckCurrentOSImageIsUpdated checks that the machineset/controlplanemachineset is using the bootimage expected in the current cluster version
func CheckCurrentOSImageIsUpdated(bir BootImageResource) {
	var (
		oc                 = bir.GetOC()
		platform           = exutil.CheckPlatform(oc)
		region             = getCurrentRegionOrFail(oc)
		arch               = bir.GetArchitectureOrFail()
		coreosBootimagesCM = NewConfigMap(oc.AsAdmin(), MachineConfigNamespace, "coreos-bootimages")
	)

	currentCoreOsBootImage := getCoreOsBootImageFromConfigMapOrFail(platform, region, arch, coreosBootimagesCM)
	logger.Infof("Current coreOsBootImage: %s", currentCoreOsBootImage)
	o.Expect(currentCoreOsBootImage).NotTo(o.BeEmpty(), "Could not find the right coreOS image for this platform")

	switch platform {
	case AWSPlatform, GCPPlatform:
		o.Eventually(bir.GetCoreOsBootImage, "5m", "20s").Should(o.ContainSubstring(currentCoreOsBootImage),
			"%s was NOT updated to use the right boot image", bir)
	case VspherePlatform:
		o.Eventually(func() (string, error) {
			bootImage, err := bir.GetCoreOsBootImage()
			if err != nil {
				return "", err
			}
			return getReleaseFromVsphereTemplate(oc.AsAdmin(), bootImage)
		}, "5m", "20s").
			Should(o.Equal(currentCoreOsBootImage), "The image used to update %s doen't have the right version", bir)
	case AzurePlatform:
		parsedImage := gjson.Parse(currentCoreOsBootImage)
		sku := parsedImage.Get("sku").String()
		version := parsedImage.Get("version").String()
		offer := parsedImage.Get("offer").String()
		publisher := parsedImage.Get("publisher").String()

		o.Eventually(bir.GetCoreOsBootImage, "5m", "20s").Should(o.And(
			HavePathWithValue("publisher", o.Equal(publisher)),
			HavePathWithValue("offer", o.Equal(offer)),
			HavePathWithValue("sku", o.Equal(sku)),
			HavePathWithValue("version", o.Equal(version)),
			HavePathWithValue("resourceID", o.BeEmpty()),
			HavePathWithValue("type", o.Equal("MarketplaceNoPlan"))),
			"%s was NOT updated to use the right boot image", bir)
	default:
		e2e.Failf("Platform not supported in CheckCurrentOSImageIsUpdated: %s", platform)
	}
}

// setArchitectureAndCheckStatus sets a different architecture in the cloned machineset and checks the status
func setArchitectureAndCheckStatus(clonedMS *MachineSet, machineConfiguration *MachineConfiguration, archValue string) {
	exutil.By(fmt.Sprintf("Set a %s architecture in the cloned machineset", archValue))
	o.Expect(clonedMS.SetArchitecture(archValue)).To(o.Succeed(), "Error setting architecture %s in %s", archValue, clonedMS)
	logger.Infof("Architecture %s set in %s\n", archValue, clonedMS)

	exutil.By("Check that no failures are being reported")
	o.Eventually(machineConfiguration, "5m", "20s").Should(HaveConditionField("BootImageUpdateDegraded", "status", "False"),
		"Expected %s not to be BootImageUpdateDegraded.\n%s", machineConfiguration.PrettyString())

	o.Eventually(machineConfiguration, "5m", "20s").Should(HaveConditionField("BootImageUpdateProgressing", "status", "False"),
		"Expected %s not to be BootImageUpdateDegraded.\n%s", machineConfiguration.PrettyString())
	logger.Infof("No failures are being reported\n")
}
