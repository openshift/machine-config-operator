package mco

import (
	"fmt"
	"regexp"
	"strings"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util"
	"github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/architecture"
	logger "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/logext"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var _ = g.Describe("[sig-mco] MCO Bootimages", func() {
	defer g.GinkgoRecover()

	var (
		oc = exutil.NewCLI("mco-scale", exutil.KubeConfigPath())
		// worker MachineConfigPool
		wMcp                 *MachineConfigPool
		machineConfiguration *MachineConfiguration
	)

	g.JustBeforeEach(func() {
		// Skip if no machineset
		skipTestIfWorkersCannotBeScaled(oc.AsAdmin())
		// Bootimages Update functionality is only available in GCP(GA) and AWS(Techpreview)
		skipTestIfSupportedPlatformNotMatched(oc, GCPPlatform, AWSPlatform)
		if exutil.CheckPlatform(oc) == AWSPlatform {
			skipIfNoTechPreview(oc)
		}

		wMcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		machineConfiguration = GetMachineConfiguration(oc.AsAdmin())
		preChecks(oc)
	})

	g.It("Author:sregidor-NonHyperShiftHOST-NonPreRelease-Medium-74238-BootImages not updated by default [Disruptive]", func() {
		var (
			fakeImageName            = "fake-coreos-bootimage-name"
			duplicatedMachinesetName = "cloned-tc-74238"
			firstMachineSet          = NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail()[0]
		)

		exutil.By("Remove ManagedBootImages section from MachineConfiguration resource")
		defer machineConfiguration.SetSpec(machineConfiguration.GetSpecOrFail())
		o.Expect(
			machineConfiguration.RemoveManagedBootImagesConfig(),
		).To(o.Succeed(), "Error configuring an empty managedBootImage in the 'cluster' MachineConfiguration resource")
		logger.Infof("OK!\n")

		exutil.By("Duplicate machineset for testing")
		machineSet, dErr := firstMachineSet.Duplicate(duplicatedMachinesetName)
		o.Expect(dErr).NotTo(o.HaveOccurred(), "Error duplicating %s", machineSet)
		defer machineSet.Delete()
		logger.Infof("OK!\n")

		exutil.By("Patch coreos boot image in MachineSet")
		o.Expect(machineSet.SetCoreOsBootImage(fakeImageName)).To(o.Succeed(),
			"Error patching the value of the coreos boot image in %s", machineSet)
		logger.Infof("OK!\n")

		exutil.By("Check that the MachineSet is not updated by MCO by default")
		o.Consistently(machineSet.GetCoreOsBootImage, "3m", "20s").Should(o.Equal(fakeImageName),
			"The machineset should not be updated by MCO if the functionality is not enabled in the MachineConfiguration resource. %s", machineSet.PrettyString())
		logger.Infof("OK!\n")
	})

	g.It("Author:sregidor-NonHyperShiftHOST-NonPreRelease-Medium-74240-ManagedBootImages. Restore All MachineSet images [Disruptive]", func() {
		var (
			fakeImageName = "fake-coreos-bootimage-name"

			coreosBootimagesCM         = NewConfigMap(oc.AsAdmin(), MachineConfigNamespace, "coreos-bootimages")
			machineSet                 = NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail()[0]
			clonedMSName               = "cloned-tc-74240"
			clonedWrongBootImageMSName = "cloned-tc-74240-wrong-boot-image"
			clonedOwnedMSName          = "cloned-tc-74240-owned"

			platform = exutil.CheckPlatform(oc)
			region   = getCurrentRegionOrFail(oc)
		)

		exutil.By("Opt-in boot images update")
		defer machineConfiguration.SetSpec(machineConfiguration.GetSpecOrFail())
		o.Expect(
			machineConfiguration.SetAllManagedBootImagesConfig(),
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

		exutil.By("Clone first machineset and set an owner for the cloned machineset")
		logger.Infof("Cloning machineset")
		clonedOwnedMS, err := machineSet.Duplicate(clonedOwnedMSName)
		defer clonedOwnedMS.Delete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error duplicating %s", machineSet)
		logger.Infof("Setting a fake owner")

		o.Expect(
			clonedOwnedMS.Patch("merge", `{"metadata":{"ownerReferences": [{"apiVersion": "fake","blockOwnerDeletion": true,"controller": true,"kind": "fakekind","name": "master","uid": "fake-uuid"}]}}`),
		).To(o.Succeed(), "Error patching %s with a fake owner", clonedOwnedMS)
		logger.Infof("OK!\n")

		exutil.By("All machinesets should use the right boot image")
		for _, ms := range NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail() {
			logger.Infof("Checking boot image in machineset %s", ms.GetName())
			currentCoreOsBootImage := getCoreOsBootImageFromConfigMapOrFail(platform, region, *machineSet.GetArchitectureOrFail(), coreosBootimagesCM)
			logger.Infof("Current coreOsBootImage: %s", currentCoreOsBootImage)
			o.Eventually(ms.GetCoreOsBootImage, "5m", "20s").Should(o.ContainSubstring(currentCoreOsBootImage),
				"%s was NOT updated to use the right boot image", ms)
		}
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
			currentCoreOsBootImage := getCoreOsBootImageFromConfigMapOrFail(platform, region, *machineSet.GetArchitectureOrFail(), coreosBootimagesCM)
			logger.Infof("Current coreOsBootImage: %s", currentCoreOsBootImage)
			o.Expect(err).NotTo(o.HaveOccurred(),
				"Error getting the currently configured coreos boot image")

			if ms.GetName() == clonedOwnedMSName {
				o.Consistently(ms.GetCoreOsBootImage, "15s", "5s").Should(o.Equal(fakeImageName),
					"%s was patched and it is using the right boot image. Machinesets with owners should NOT be patched.", ms)

			} else {
				o.Eventually(ms.GetCoreOsBootImage, "5m", "20s").Should(o.ContainSubstring(currentCoreOsBootImage),
					"%s was NOT updated to use the right boot image", ms)
				o.Eventually(ms.GetUserDataSecret, "1m", "20s").Should(o.ContainSubstring("worker-user-data-managed"),
					"%s was NOT updated to use the right user-data secret", ms)
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
		logger.Infof("OK!\n")
	})

	g.It("Author:sregidor-NonHyperShiftHOST-NonPreRelease-Medium-74239-ManagedBootImages. Restore Partial MachineSet images [Disruptive]", func() {
		var (
			fakeImageName = "fake-coreos-bootimage-name"

			coreosBootimagesCM     = NewConfigMap(oc.AsAdmin(), MachineConfigNamespace, "coreos-bootimages")
			machineSet             = NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail()[0]
			clonedMSLabelName      = "cloned-tc-74240-label"
			clonedMSNoLabelName    = "cloned-tc-74240-no-label"
			clonedMSLabelOwnedName = "cloned-tc-74240-label-owned"
			labelName              = "test"
			labelValue             = "update"

			platform = exutil.CheckPlatform(oc)
			region   = getCurrentRegionOrFail(oc)
		)

		exutil.By("Opt-in boot images update")

		defer machineConfiguration.SetSpec(machineConfiguration.GetSpecOrFail())
		o.Expect(
			machineConfiguration.SetPartialManagedBootImagesConfig(labelName, labelValue),
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
		currentCoreOsBootImage := getCoreOsBootImageFromConfigMapOrFail(platform, region, *machineSet.GetArchitectureOrFail(), coreosBootimagesCM)
		logger.Infof("Current coreOsBootImage: %s", currentCoreOsBootImage)

		o.Eventually(clonedMSLabel.GetCoreOsBootImage, "5m", "20s").Should(o.ContainSubstring(currentCoreOsBootImage),
			"%s was NOT updated to use the right boot image", clonedMSLabel)
		o.Eventually(clonedMSLabel.GetUserDataSecret, "1m", "20s").Should(o.ContainSubstring("worker-user-data-managed"),
			"%s was NOT updated to use the right user-data secret", clonedMSLabel)
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
		logger.Infof("OK!\n")
	})

	g.It("Author:sregidor-NonHyperShiftHOST-NonPreRelease-Medium-74764-ManagedBootImages. Delete machineset when error [Disruptive]", func() {
		var (
			machineConfiguration = GetMachineConfiguration(oc.AsAdmin())
			machineSet           = NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail()[0]
			clonedMSName         = "cloned-tc-74764-copy"
			labelName            = "test"
			labelValue           = "update"

			expectedFailedMessageRegexp = regexp.QuoteMeta("Error(s): error syncing MAPI MachineSet " +
				clonedMSName +
				": unexpected OwnerReference: fakekind/master. Please remove this machineset from boot image management to avoid errors")
			expectedFailedProgressMessage = "Reconciled 0 of 1 MAPI MachineSets | Reconciled 0 of 0 CAPI MachineSets | Reconciled 0 of 0 CAPI MachineDeployments"
			expectedOKMessage             = "0 Degraded MAPI MachineSets | 0 Degraded CAPI MachineSets | 0 CAPI MachineDeployments"
		)
		exutil.By("Opt-in boot images update")

		defer machineConfiguration.SetSpec(machineConfiguration.GetSpecOrFail())
		o.Expect(
			machineConfiguration.SetPartialManagedBootImagesConfig(labelName, labelValue),
		).To(o.Succeed(), "Error configuring Partial managedBootImages in the 'cluster' MachineConfiguration resource")
		logger.Infof("OK!\n")

		exutil.By("Clone the first machineset")
		clonedMS, err := machineSet.Duplicate(clonedMSName)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error duplicating %s", machineSet)
		defer clonedMS.Delete()
		logger.Infof("OK!\n")

		exutil.By("Add a fake owner to the new cloned machineset")
		o.Expect(
			clonedMS.Patch("merge", `{"metadata":{"ownerReferences": [{"apiVersion": "fake","blockOwnerDeletion": true,"controller": true,"kind": "fakekind","name": "master","uid": "fake-uuid"}]}}`),
		).To(o.Succeed(), "Error patching %s with a fake owner", clonedMS)
		logger.Infof("OK!\n")

		exutil.By("Label the cloned machineset so that its boot image is updated by MCO")
		o.Expect(clonedMS.AddLabel(labelName, labelValue)).To(o.Succeed(),
			"Error labeling %s", clonedMS)
		logger.Infof("OK!\n")

		exutil.By("Check that an error is reported in the machineconfiguration resource")
		o.Eventually(machineConfiguration, "5m", "10s").Should(HaveConditionField("BootImageUpdateDegraded", "status", "True"),
			"Expected %s to be BootImageUpdateDegraded.\n%s", machineConfiguration.PrettyString())

		o.Eventually(machineConfiguration, "5m", "10s").Should(HaveConditionField("BootImageUpdateDegraded", "message", o.MatchRegexp(expectedFailedMessageRegexp)),
			"Expected %s to be BootImageUpdateDegraded.\n%s", machineConfiguration.PrettyString())
		logger.Infof("OK!\n")

		exutil.By("Check reported progress in machineconfiguration resource")
		o.Eventually(machineConfiguration, "5m", "20s").Should(HaveConditionField("BootImageUpdateProgressing", "message", expectedFailedProgressMessage),
			"Progress message is not the expected one.\n%s", machineConfiguration.PrettyString())
		o.Eventually(machineConfiguration, "5m", "20s").Should(HaveConditionField("BootImageUpdateProgressing", "status", "False"),
			"Progress status is not the expected one.\n%s", machineConfiguration.PrettyString())
		logger.Infof("OK!\n")

		exutil.By("Delete the new cloned machineset")
		o.Expect(clonedMS.Delete()).To(o.Succeed(), "Error deleting %s", clonedMS)
		logger.Infof("OK!\n")

		exutil.By("Check the machineconfiguration resource is not reporting errors anymore and the progress is OK")
		o.Eventually(machineConfiguration, "5m", "20s").Should(HaveConditionField("BootImageUpdateDegraded", "status", "False"),
			"Expected %s to be BootImageUpdateDegraded.\n%s", machineConfiguration.PrettyString())

		o.Eventually(machineConfiguration, "5m", "20s").Should(HaveConditionField("BootImageUpdateDegraded", "message", expectedOKMessage),
			"Expected %s to be BootImageUpdateDegraded.\n%s", machineConfiguration.PrettyString())

		o.Eventually(machineConfiguration, "5m", "20s").Should(HaveConditionField("BootImageUpdateProgressing", "status", "False"),
			"Progress status is not the expected one.\n%s", machineConfiguration.PrettyString())
		logger.Infof("OK!\n")

	})

	g.It("Author:sregidor-NonHyperShiftHOST-NonPreRelease-Medium-74751-ManagedBootImages. Fix errors [Disruptive]", func() {
		var (
			coreosBootimagesCM          = NewConfigMap(oc.AsAdmin(), MachineConfigNamespace, "coreos-bootimages")
			machineConfiguration        = GetMachineConfiguration(oc.AsAdmin())
			machineSet                  = NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail()[0]
			clonedMSName                = "cloned-tc-74751-copy"
			labelName                   = "test"
			labelValue                  = "update"
			fakearch                    = "fake-arch"
			expectedFailedMessageRegexp = regexp.QuoteMeta("Error(s): error syncing MAPI MachineSet " +
				clonedMSName +
				": failed to fetch arch during machineset sync: invalid architecture value found in annotation: kubernetes.io/arch=" + fakearch)

			platform = exutil.CheckPlatform(oc)
			region   = getCurrentRegionOrFail(oc)
		)
		exutil.By("Opt-in boot images update")

		defer machineConfiguration.SetSpec(machineConfiguration.GetSpecOrFail())
		o.Expect(
			machineConfiguration.SetPartialManagedBootImagesConfig(labelName, labelValue),
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
		o.Expect(clonedMS.SetCoreOsBootImage("fake-image")).To(o.Succeed(), "Error setting a fake boot image in %s", clonedMS)
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
		o.Expect(clonedMS.SetArchitecture(machineSet.GetArchitectureOrFail().String())).To(o.Succeed(), "Error fixing the problem in the architecture in %s", clonedMS)
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
		currentCoreOsBootImage := getCoreOsBootImageFromConfigMapOrFail(platform, region, *machineSet.GetArchitectureOrFail(), coreosBootimagesCM)
		logger.Infof("Current coreOsBootImage: %s", currentCoreOsBootImage)

		o.Eventually(clonedMS.GetCoreOsBootImage, "5m", "20s").Should(o.ContainSubstring(currentCoreOsBootImage),
			"%s was NOT updated to use the right boot image", clonedMS)
		logger.Infof("OK!\n")

	})
})

func DuplicateMachineSetWithCustomBootImage(ms MachineSet, newBootImage, newName string) (*MachineSet, error) {

	var (
		platform            = exutil.CheckPlatform(ms.GetOC().AsAdmin())
		coreOSBootImagePath = GetCoreOSBootImagePath(platform)
	)

	// Patch is given like /spec/template/spec/providerSpec/value/ami/id
	// but in sjson library we need the path like spec.template.spec.providerSpec.valude.ami.id
	// so we transform the string
	jsonCoreOSBootImagePath := strings.ReplaceAll(strings.TrimPrefix(coreOSBootImagePath, "/"), "/", ".")

	res, err := CloneResource(&ms, newName, ms.GetNamespace(),
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

			newResString, err = sjson.Set(newResString, jsonCoreOSBootImagePath, newBootImage)
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

// getCoreOsBootImageFromConfigMap look for the configured coreOs boot image in given configmap
func getCoreOsBootImageFromConfigMap(platform, region string, arch architecture.Architecture, coreosBootimagesCM *ConfigMap) (string, error) {
	var (
		coreOsBootImagePath string
		// transform amd64 naming to x86_64 naming
		stringArch = convertArch(arch)
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
		coreOsBootImagePath = fmt.Sprintf(`.architectures.%s.images.%s.regions."%s".image`, stringArch, platform, region)
	case GCPPlatform:
		coreOsBootImagePath = fmt.Sprintf(`architectures.%s.images.%s.name`, stringArch, platform)
	default:
		return "", fmt.Errorf("Machineset.GetCoreOsBootImage method is only supported for GCP and AWS platforms")
	}

	currentCoreOsBootImage := parsedStream.Get(coreOsBootImagePath).String()

	if currentCoreOsBootImage == "" {
		logger.Warnf("The coreos boot image for architecture %s in %s IS EMPTY", stringArch, coreosBootimagesCM)
	}

	return currentCoreOsBootImage, nil
}

func getCoreOsBootImageFromConfigMapOrFail(platform, region string, arch architecture.Architecture, coreosBootimagesCM *ConfigMap) string {
	image, err := getCoreOsBootImageFromConfigMap(platform, region, arch, coreosBootimagesCM)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the boot image from %s for platform %s and arch %s", coreosBootimagesCM, platform, arch)
	return image
}
