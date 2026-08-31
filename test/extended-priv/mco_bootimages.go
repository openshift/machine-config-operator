package extended

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/google/uuid"
	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	"github.com/openshift/machine-config-operator/test/extended-priv/util/architecture"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

const mapiBaseErrorMessageTemplate = `1 Degraded MAPI MachineSets | 0 Degraded ControlPlaneMachineSets | 0 Degraded CAPI MachineSets | 0 Degraded CAPI MachineDeployments | Error(s):` +
	` error syncing MAPI MachineSet %s: failed to reconcile machineset %s, err:`

// backdatedImageRunID is generated once per test binary process, so every vSphere backdated
// template this run uploads gets a name unique to that run. Without it, every run uploads under
// the exact same literal name regardless of which failure domain/folder it lands in — so a leftover
// template from an earlier (or crashed, uncleaned-up) run collides with the current run's upload,
// which is what caused both the MCO-side "resolves to multiple vms" bug and the machine-api-provider-
// vsphere actuator's own "multiple templates found" clone-time failure.
var backdatedImageRunID = strings.ReplaceAll(uuid.NewString(), "-", "")[:8]

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
		// Bootimages Update functionality is only available in GCP, AWS, vSphere and Azure
		skipTestIfSupportedPlatformNotMatched(oc, GCPPlatform, AWSPlatform, VspherePlatform, AzurePlatform)
		// Skip if any MachineSet carries an unsupported OS stream label
		exutil.SkipIfUnsupportedOSStreamLabel(oc.AsAdmin())
		wMcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		machineConfiguration = GetMachineConfiguration(oc.AsAdmin())
		PreChecks(oc)

		// Disable skew to avoid collisions
		exutil.By("Disabling skew functionality")
		initialMachineConfiguration := machineConfiguration.GetSpecOrFail()
		DisableSkew(machineConfiguration)

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
			backdatedImageName    = getBackdatedBootImage(oc.AsAdmin(), firstMachineSet)
			fakeImageNameNoUpdate = getFakeNoUpdateBootImage(oc.AsAdmin(), "81403")
		)

		exutil.By("Duplicate machineset for testing")
		machineSet, dErr := firstMachineSet.Duplicate(duplicatedMachinesetName)
		o.Expect(dErr).NotTo(o.HaveOccurred(), "Error duplicating %s", machineSet)
		defer machineSet.Delete()
		logger.Infof("OK!\n")

		exutil.By("Patch coreos boot image in MachineSet")
		o.Expect(machineSet.SetCoreOsBootImage(backdatedImageName)).To(o.Succeed(),
			"Error patching the value of the coreos boot image in %s", machineSet)
		logger.Infof("OK!\n")

		exutil.By("Check that the MachineSet is updated by MCO by default")
		CheckCurrentOSImageIsUpdated(machineSet, backdatedImageName)
		logger.Infof("OK!\n")

		// For none - mode i.e opt-out MachineSet are not updated with original value if we try to set with any fake value
		exutil.By("Opt-out boot images update")
		o.Expect(
			machineConfiguration.SetNoneManagedBootImagesConfig(MachineSetResource),
		).To(o.Succeed(), "Error configuring None managedBootImages in the 'cluster' MachineConfiguration resource")
		logger.Infof("OK!\n")

		exutil.By("Patch coreos boot image in MachineSet")
		o.Expect(machineSet.SetCoreOsBootImage(fakeImageNameNoUpdate)).To(o.Succeed(),
			"Error patching the value of the coreos boot image in %s", machineSet)
		logger.Infof("OK!\n")

		exutil.By("Check that the MachineSet is not updated by MCO in opt-out")
		CheckCurrentOSImageIsNotUpdated(machineSet, fakeImageNameNoUpdate)
		logger.Infof("OK!\n")

		exutil.By("Opt-in boot images update")
		o.Expect(
			machineConfiguration.SetPartialManagedBootImagesConfig(MachineSetResource, "", ""),
		).To(o.Succeed(), "Error configuring Partial managedBootImages in the 'cluster' MachineConfiguration resource")
		logger.Infof("OK!\n")

		// vSphere updates templates in-place (same name, new content), so the vSphere template behind
		// backdatedImageName was likely already reconciled to the current release by the first update
		// above. Re-upload it so this second use is genuinely backdated again, or MCO will see an
		// already-current template and never trigger the update this check expects.
		if exutil.CheckPlatform(oc) == VspherePlatform {
			exutil.By("Re-upload the backdated vSphere template so it is genuinely backdated again")
			vsInfo, vsErr := GetVSphereConnectionInfoForMachineSet(machineSet)
			o.Expect(vsErr).NotTo(o.HaveOccurred(), "Error getting the vSphere connection info for %s", machineSet)
			folder, fErr := machineSet.GetWorkspaceFolder()
			o.Expect(fErr).NotTo(o.HaveOccurred(), "Error getting the workspace folder for %s", machineSet)
			o.Expect(exutil.DeleteVsphereTemplate(backdatedImageName, folder, vsInfo)).To(o.Succeed(),
				"Error deleting the already-updated vSphere template %s", backdatedImageName)
			backdatedImageName = getBackdatedBootImage(oc.AsAdmin(), machineSet)
			logger.Infof("OK!\n")
		}

		exutil.By("Patch coreos boot image in MachineSet")
		o.Expect(machineSet.SetCoreOsBootImage(backdatedImageName)).To(o.Succeed(),
			"Error patching the value of the coreos boot image in %s", machineSet)
		logger.Infof("OK!\n")

		exutil.By("Check that the MachineSet is updated by MCO for opt-in")
		CheckCurrentOSImageIsUpdated(machineSet, backdatedImageName)
		logger.Infof("OK!\n")

	})

	g.It("[PolarionID:74240][OTP] ManagedBootImages. Restore All MachineSet images", g.Label("Platform:aws", "Platform:gce", "Platform:vsphere", "Platform:azure"), func() {
		var (
			machineSet                 = NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail()[0]
			backdatedImageName         = getBackdatedBootImage(oc.AsAdmin(), machineSet)
			fakeImageNameNoUpdate      = getFakeNoUpdateBootImage(oc.AsAdmin(), "74240")
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
		clonedWrongImageMS, err := DuplicateMachineSetWithCustomBootImage(machineSet, backdatedImageName, clonedWrongBootImageMSName)
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
			// Check that the current boot image is the right one.
			// Original machinesets were never set to backdatedImageName, so pass empty string.
			CheckCurrentOSImageIsUpdated(ms, "")
		}
		logger.Infof("OK!\n")

		// We add the owner once it has been updated to avoid race conditions
		exutil.By("Patch last cloned machineset to add an owner reference")
		o.Expect(
			clonedOwnedMS.Patch("merge", `{"metadata":{"ownerReferences": [{"apiVersion": "fake","blockOwnerDeletion": true,"controller": true,"kind": "fakekind","name": "master","uid": "fake-uuid"}]}}`),
		).To(o.Succeed(), "Error patching %s with a fake owner", clonedOwnedMS)
		logger.Infof("OK!\n")

		exutil.By("Patch cloned machinesets to use a wrong boot image")
		o.Expect(clonedMS.SetCoreOsBootImage(backdatedImageName)).To(o.Succeed(),
			"Error setting a new boot image in %s", clonedMS)

		o.Expect(clonedWrongImageMS.SetCoreOsBootImage(backdatedImageName)).To(o.Succeed(),
			"Error setting a new boot image in %s", clonedWrongImageMS)

		o.Expect(clonedOwnedMS.SetCoreOsBootImage(fakeImageNameNoUpdate)).To(o.Succeed(),
			"Error setting a new boot image in %s", clonedOwnedMS)
		logger.Infof("OK!\n")

		exutil.By("All machinesets should use the right boot image except the one with an owner")
		clonedNames := map[string]bool{clonedMSName: true, clonedWrongBootImageMSName: true, clonedOwnedMSName: true}
		for _, ms := range NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail() {
			logger.Infof("Checking boot image in machineset %s", ms.GetName())

			if ms.GetName() == clonedOwnedMSName {
				CheckCurrentOSImageIsNotUpdated(ms, fakeImageNameNoUpdate)
			} else if clonedNames[ms.GetName()] {
				// Cloned machinesets were patched with backdatedImageName
				CheckCurrentOSImageIsUpdated(ms, backdatedImageName)
				o.Eventually(ms.GetUserDataSecret, "3m", "20s").ShouldNot(o.ContainSubstring("worker-user-data-managed"),
					"%s should NOT be using the worker-user-data-managed secret after updating the image", ms)
			} else {
				// Original machinesets were never patched
				CheckCurrentOSImageIsUpdated(ms, "")
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
			machineSet              = NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail()[0]
			backdatedImageName      = getBackdatedBootImage(oc.AsAdmin(), machineSet)
			fakeImageNameNoUpdate   = getFakeNoUpdateBootImage(oc.AsAdmin(), "74239")
			clonedMSLabelName       = "cloned-tc-74239-label"
			clonedMSNoLabelName     = "cloned-tc-74239-no-label"
			clonedMSLabelOwnedName  = "cloned-tc-74239-label-owned"
			labelName               = "test"
			labelValue              = "update"
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
		o.Expect(clonedMSLabel.SetCoreOsBootImage(backdatedImageName)).To(o.Succeed(),
			"Error setting a new boot image in %s", clonedMSLabel)

		o.Expect(clonedMSNoLabel.SetCoreOsBootImage(fakeImageNameNoUpdate)).To(o.Succeed(),
			"Error setting a new boot image in %s", clonedMSNoLabel)

		o.Expect(clonedMSLabelOwned.SetCoreOsBootImage(fakeImageNameNoUpdate)).To(o.Succeed(),
			"Error setting a new boot image in %s", clonedMSLabelOwned)
		logger.Infof("OK!\n")

		exutil.By("The labeled machineset without owner should be updated")
		CheckCurrentOSImageIsUpdated(clonedMSLabel, backdatedImageName)
		// Check that the user-data secret is the right one
		o.Eventually(clonedMSLabel.GetUserDataSecret, "3m", "20s").ShouldNot(o.ContainSubstring("worker-user-data-managed"),
			"%s should NOT be using the worker-user-data-managed secret after updating the image", clonedMSLabel)

		logger.Infof("OK!\n")

		exutil.By("The labeled machineset with owner should NOT be updated")
		CheckCurrentOSImageIsNotUpdated(clonedMSLabelOwned, fakeImageNameNoUpdate)
		logger.Infof("OK!\n")

		exutil.By("The machineset without label should NOT be updated")
		CheckCurrentOSImageIsNotUpdated(clonedMSNoLabel, fakeImageNameNoUpdate)
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
			backdatedImageName          = getBackdatedBootImage(oc.AsAdmin(), machineSet)
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
		o.Expect(clonedMS.SetCoreOsBootImage(backdatedImageName)).To(o.Succeed(), "Error setting a fake boot image in %s", clonedMS)
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
		CheckCurrentOSImageIsUpdated(clonedMS, backdatedImageName)
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
				" failed to unmarshal decoded user-data to json (secret %s): invalid character 'h' in literal true (expecting 'r')", clonedMSName, clonedMSName, clonedSecretName)) + "$"
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
			backdatedImageName   = getBackdatedBootImage(oc.AsAdmin(), machineSet)
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
		o.Expect(clonedMS.SetCoreOsBootImage(backdatedImageName)).To(o.Succeed(), "Error setting a fake boot image in %s", clonedMS)
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
		CheckCurrentOSImageIsUpdated(clonedMS, backdatedImageName)
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

	g.It("[OTP] Boot image controller preserves a valid non-standard providerSpec.Template name", g.Label("Platform:vsphere"), func() {
		// This is vSphere-specific: it is the only platform where providerSpec.Template names a
		// vCenter template object directly rather than referencing an AMI/image ID, so it is the
		// only platform where the boot image controller has to resolve/preserve an existing
		// template by an arbitrary, non-standard name (see
		// https://github.com/openshift/machine-config-operator/pull/6234).
		skipTestIfSupportedPlatformNotMatched(oc, VspherePlatform)

		var (
			clonedMSName       = "cloned-tc-custom-template-name-copy"
			machineSet         = NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail()[0]
			customTemplateName = "mcotest-custom-template-name"
			labelName          = "test"
			labelValue         = "update"
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

		exutil.By("Upload the current RHCOS OVA under a non-standard (custom) template name")
		currentVersion, _, err := exutil.GetClusterVersion(oc.AsAdmin())
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the current cluster version")

		rhcosHandler, err := GetRHCOSHandler(VspherePlatform)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the rhcos handler")

		baseImageURL, err := rhcosHandler.GetBaseImageURLFromRHCOSImageInfo(currentVersion, OSImageStreamRHEL9, architecture.AMD64)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the current base image URL")

		msServer, err := machineSet.Get(`{.spec.template.spec.providerSpec.value.workspace.server}`)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting workspace.server from %s", machineSet)
		msDC, err := machineSet.Get(`{.spec.template.spec.providerSpec.value.workspace.datacenter}`)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting workspace.datacenter from %s", machineSet)
		msFolder, err := machineSet.GetWorkspaceFolder()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting workspace folder from %s", machineSet)

		o.Expect(
			uploadBaseImageToVsphereForWorkspace(oc, baseImageURL, customTemplateName, msServer, msDC, msFolder),
		).To(o.Succeed(), "Error uploading the current base image %s under the custom name %s", baseImageURL, customTemplateName)
		logger.Infof("OK!\n")

		exutil.By("Point the cloned machineset's providerSpec.Template at the custom-named, already-current template")
		o.Expect(clonedMS.SetCoreOsBootImage(customTemplateName)).To(o.Succeed(),
			"Error setting the custom template name in %s", clonedMS)
		logger.Infof("OK!\n")

		exutil.By("Label the cloned machineset so that it is reconciled")
		o.Expect(clonedMS.AddLabel(labelName, labelValue)).To(o.Succeed(),
			"Error labeling %s", clonedMS)
		logger.Infof("OK!\n")

		exutil.By("Check that no failures are reported")
		o.Eventually(machineConfiguration, "5m", "20s").Should(HaveConditionField("BootImageUpdateDegraded", "status", "False"),
			"Expected %s not to be BootImageUpdateDegraded.\n%s", machineConfiguration.PrettyString())
		o.Eventually(machineConfiguration, "5m", "20s").Should(HaveConditionField("BootImageUpdateProgressing", "status", "False"),
			"Expected %s not to be BootImageUpdateProgressing.\n%s", machineConfiguration.PrettyString())
		logger.Infof("OK!\n")

		exutil.By("Check that the custom template name is preserved rather than being renamed back to the computed name")
		// The custom name already points at a current, valid template - the controller should
		// recognize that via providerSpec.Template, not silently discard it in favor of the
		// name it would otherwise compute from the infra ID and failure domain.
		o.Consistently(clonedMS.GetCoreOsBootImage, "3m", "20s").Should(o.Equal(customTemplateName),
			"%s's providerSpec.Template was changed away from the custom name, even though it already pointed at a valid, current template", clonedMS)
		logger.Infof("OK!\n")

		exutil.By("Check that the custom-named template is recognized as up to date")
		CheckCurrentOSImageIsUpdated(clonedMS, customTemplateName)
		logger.Infof("OK!\n")
	})

	g.It("[OTP] Reconciles MachineSets spread across multiple vSphere vCenters", g.Label("Platform:vsphere"), func() {
		skipTestIfSupportedPlatformNotMatched(oc, VspherePlatform)

		groups, cleanup, err := buildWorkspaceGroupsAcrossVCenters(oc)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error building workspace groups across vCenters")
		defer cleanup()
		if len(groups) < 2 {
			g.Skip(fmt.Sprintf("This test needs at least 2 vCenters configured in the infrastructure resource, found %d", len(groups)))
		}

		reconcileOneMachineSetPerVsphereWorkspaceGroup(oc, machineConfiguration, groups, "multi-vcenter")
	})

	g.It("[OTP] Reconciles MachineSets spread across multiple vSphere failure domains", g.Label("Platform:vsphere"), func() {
		skipTestIfSupportedPlatformNotMatched(oc, VspherePlatform)

		groups, err := groupMachineSetsByVsphereWorkspaceField(oc, "datacenter", "datastore", "resourcePool", "server")
		o.Expect(err).NotTo(o.HaveOccurred(), "Error grouping machinesets by failure domain")
		if len(groups) < 2 {
			g.Skip(fmt.Sprintf("This test needs MachineSets spread across at least 2 failure domains, found %d", len(groups)))
		}

		reconcileOneMachineSetPerVsphereWorkspaceGroup(oc, machineConfiguration, groups, "multi-fd")
	})
})

// vsphereWorkspaceGroup is one distinct combination of providerSpec.value.workspace field
// values found among the cluster's existing MachineSets, along with the MachineSets that use it.
type vsphereWorkspaceGroup struct {
	key         string
	machineSets []*MachineSet
}

// groupMachineSetsByVsphereWorkspaceField groups all existing MachineSets by the given
// providerSpec.value.workspace field name(s) (e.g. "server", or
// "datacenter","datastore","resourcePool" together to key on failure domain identity). Used to
// discover, from already-existing cluster state, whether MachineSets are actually spread across
// more than one vCenter/failure domain - rather than trying to force a MachineSet onto a
// different one, which risks provisioning against topology this test doesn't control.
func groupMachineSetsByVsphereWorkspaceField(oc *exutil.CLI, fields ...string) ([]vsphereWorkspaceGroup, error) {
	allMS, err := NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAll()
	if err != nil {
		return nil, err
	}

	order := []string{}
	byKey := map[string][]*MachineSet{}
	for _, ms := range allMS {
		values := make([]string, 0, len(fields))
		for _, field := range fields {
			value, err := ms.Get(fmt.Sprintf(`{.spec.template.spec.providerSpec.value.workspace.%s}`, field))
			if err != nil {
				return nil, fmt.Errorf("error reading workspace.%s from %s: %w", field, ms, err)
			}
			values = append(values, value)
		}
		key := strings.Join(values, "|")
		if _, ok := byKey[key]; !ok {
			order = append(order, key)
		}
		byKey[key] = append(byKey[key], ms)
	}

	groups := make([]vsphereWorkspaceGroup, 0, len(order))
	for _, key := range order {
		groups = append(groups, vsphereWorkspaceGroup{key: key, machineSets: byKey[key]})
	}
	return groups, nil
}

// buildWorkspaceGroupsAcrossVCenters reads all failure domains from the
// infrastructure resource, groups them by vCenter server, and returns one
// vsphereWorkspaceGroup per server. For servers that already have existing
// MachineSets, the group uses those. For servers that have no existing
// MachineSets (e.g. because workers were only placed on a subset of vCenters),
// the group creates a synthetic MachineSet by cloning an existing one and
// patching its workspace fields to match a failure domain on that server.
func buildWorkspaceGroupsAcrossVCenters(oc *exutil.CLI) ([]vsphereWorkspaceGroup, func(), error) {
	fds, err := exutil.GetAllVSphereFailureDomains(oc.AsAdmin())
	if err != nil {
		return nil, func() {}, err
	}

	allMS, err := NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAll()
	if err != nil {
		return nil, func() {}, err
	}

	var syntheticMachineSets []*MachineSet
	cleanup := func() {
		for _, ms := range syntheticMachineSets {
			if err := ms.Delete(); err != nil {
				logger.Infof("Warning: failed to delete synthetic MachineSet %s: %v", ms.GetName(), err)
			}
		}
	}

	// Index existing MachineSets by their workspace server.
	msByServer := map[string][]*MachineSet{}
	for _, ms := range allMS {
		server, sErr := ms.Get(`{.spec.template.spec.providerSpec.value.workspace.server}`)
		if sErr != nil {
			continue
		}
		msByServer[server] = append(msByServer[server], ms)
	}

	// Group failure domains by server, preserving insertion order.
	var fdOrder []string
	fdByServer := map[string][]exutil.VSphereConnectionInfo{}
	for _, fd := range fds {
		if _, ok := fdByServer[fd.Server]; !ok {
			fdOrder = append(fdOrder, fd.Server)
		}
		fdByServer[fd.Server] = append(fdByServer[fd.Server], fd)
	}

	// Build one workspace group per server.
	var groups []vsphereWorkspaceGroup
	for _, server := range fdOrder {
		if msList, ok := msByServer[server]; ok && len(msList) > 0 {
			groups = append(groups, vsphereWorkspaceGroup{
				key:         server,
				machineSets: msList,
			})
			continue
		}

		// No existing MachineSet targets this server. Clone one from another
		// server and patch its workspace to match a failure domain here.
		fd := fdByServer[server][0]
		if len(allMS) == 0 {
			return nil, cleanup, fmt.Errorf("cannot create a synthetic MachineSet for server %s: no existing MachineSets", server)
		}

		donor := allMS[0]
		syntheticName := fmt.Sprintf("mco-synthetic-%s", fd.FailureDomainName)

		// Get the donor's folder to extract the cluster suffix
		donorFolder, fErr := donor.Get(`{.spec.template.spec.providerSpec.value.workspace.folder}`)
		if fErr != nil {
			return nil, cleanup, fmt.Errorf("error creating synthetic MachineSet for server %s: cannot read donor folder: %w", server, fErr)
		}
		// Replace the datacenter component with the target datacenter
		newFolder := donorFolder
		if parts := strings.SplitN(donorFolder, "/", 3); len(parts) >= 3 {
			newFolder = path.Join("/", fd.DataCenter, parts[2])
		}

		syntheticMS, cloneErr := CloneResource(donor, syntheticName, donor.GetNamespace(),
			func(resJSON string) (string, error) {
				s, e := sjson.Set(resJSON, "spec.replicas", 0)
				if e != nil {
					return "", e
				}
				s, e = sjson.Set(s, `spec.selector.matchLabels.machine\.openshift\.io/cluster-api-machineset`, syntheticName)
				if e != nil {
					return "", e
				}
				s, e = sjson.Set(s, `spec.template.metadata.labels.machine\.openshift\.io/cluster-api-machineset`, syntheticName)
				if e != nil {
					return "", e
				}
				s, e = sjson.Set(s, "spec.template.spec.providerSpec.value.workspace.server", fd.Server)
				if e != nil {
					return "", e
				}
				s, e = sjson.Set(s, "spec.template.spec.providerSpec.value.workspace.datacenter", fd.DataCenter)
				if e != nil {
					return "", e
				}
				s, e = sjson.Set(s, "spec.template.spec.providerSpec.value.workspace.datastore", fd.DataStore)
				if e != nil {
					return "", e
				}
				s, e = sjson.Set(s, "spec.template.spec.providerSpec.value.workspace.resourcePool", fd.ResourcePool)
				if e != nil {
					return "", e
				}
				s, e = sjson.Set(s, "spec.template.spec.providerSpec.value.workspace.folder", newFolder)
				if e != nil {
					return "", e
				}
				s, e = sjson.Set(s, "spec.template.spec.providerSpec.value.network.devices.0.networkName", fd.Network)
				if e != nil {
					return "", e
				}
				return s, nil
			},
		)
		if cloneErr != nil {
			return nil, cleanup, fmt.Errorf("error creating synthetic MachineSet for server %s: %w", server, cloneErr)
		}
		logger.Infof("Created synthetic MachineSet %s targeting server %s (failure domain %s)", syntheticMS.GetName(), server, fd.FailureDomainName)
		ms := NewMachineSet(oc.AsAdmin(), syntheticMS.GetNamespace(), syntheticMS.GetName())
		syntheticMachineSets = append(syntheticMachineSets, ms)
		groups = append(groups, vsphereWorkspaceGroup{
			key:         server,
			machineSets: []*MachineSet{ms},
		})
	}

	return groups, cleanup, nil
}

// reconcileOneMachineSetPerVsphereWorkspaceGroup clones one MachineSet from each of the given
// (already distinct) workspace groups, sets a backdated boot image on each, and verifies every
// clone is independently reconciled to a current, valid image - exercising the boot image
// controller's vCenter/failure-domain matching across more than one group in a single test run.
func reconcileOneMachineSetPerVsphereWorkspaceGroup(oc *exutil.CLI, machineConfiguration *MachineConfiguration, groups []vsphereWorkspaceGroup, testTag string) {
	exutil.By("Opt-in boot images update")
	o.Expect(
		machineConfiguration.SetAllManagedBootImagesConfig(MachineSetResource),
	).To(o.Succeed(), "Error configuring ALL managedBootImages in the 'cluster' MachineConfiguration resource")
	logger.Infof("OK!\n")

	fakeImageName, fakeImageURL := getBackdatedBootImageNameAndURL(oc.AsAdmin())

	var clonedMachineSets []*MachineSet
	for i, group := range groups {
		exutil.By(fmt.Sprintf("Clone a MachineSet from workspace group %d/%d (%s)", i+1, len(groups), group.key))
		clonedMSName := fmt.Sprintf("cloned-tc-%s-%d-%s", testTag, i, utilrand.String(5))
		representative := group.machineSets[0]
		clonedMS, err := representative.Duplicate(clonedMSName)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error duplicating %s", representative)
		defer clonedMS.Delete()
		clonedMachineSets = append(clonedMachineSets, clonedMS)

		server, err := representative.Get(`{.spec.template.spec.providerSpec.value.workspace.server}`)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting workspace.server from %s", representative)
		datacenter, err := representative.Get(`{.spec.template.spec.providerSpec.value.workspace.datacenter}`)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting workspace.datacenter from %s", representative)
		folder, err := representative.GetWorkspaceFolder()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting workspace folder from %s", representative)

		o.Expect(
			uploadBaseImageToVsphereForWorkspace(oc, fakeImageURL, fakeImageName, server, datacenter, folder),
		).To(o.Succeed(), "Error uploading backdated image %s to %s/%s", fakeImageName, server, datacenter)

		o.Expect(clonedMS.SetCoreOsBootImage(fakeImageName)).To(o.Succeed(),
			"Error setting a fake boot image in %s", clonedMS)
		logger.Infof("OK!\n")
	}

	exutil.By("Check that every cloned MachineSet is independently reconciled to a current image")
	for _, clonedMS := range clonedMachineSets {
		o.Eventually(clonedMS.GetCoreOsBootImage, "15m", "20s").ShouldNot(o.Or(o.Equal(fakeImageName), o.BeEmpty()),
			"%s was NOT updated to use the right boot image", clonedMS)
		CheckCurrentOSImageIsUpdated(clonedMS, fakeImageName)
	}
	logger.Infof("OK!\n")
}

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
			return "", fmt.Errorf("region is empty for platform %s. The region is mandatory if we want to get the boot image value", platform)
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
		backdatedImageName     = getBackdatedBootImage(oc.AsAdmin(), machineSet)
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
	o.Expect(clonedMS.SetCoreOsBootImage(backdatedImageName)).To(o.Succeed(), "Error setting a fake boot image in %s", clonedMS)
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
func getBackdatedBootImage(oc *exutil.CLI, ms *MachineSet) string {
	var (
		platform = exutil.CheckPlatform(oc)
	)

	switch platform {
	case AWSPlatform:
		// RHCOS 4.12 AMIs per region, from https://github.com/openshift/installer/blob/release-4.12/data/data/coreos/rhcos.json
		backdatedAMIs := map[string]string{
			"af-south-1":     "ami-0422676091bb78731",
			"ap-east-1":      "ami-017f906bb54acfd99",
			"ap-northeast-1": "ami-037f7e8d0dc950d11",
			"ap-northeast-2": "ami-0a18c136a1903a2e3",
			"ap-northeast-3": "ami-09beba5c87bcec024",
			"ap-south-1":     "ami-0cc4437f97ef143ec",
			"ap-south-2":     "ami-0504e7a2db47da9eb",
			"ap-southeast-1": "ami-027acff3ce48e4eed",
			"ap-southeast-2": "ami-0f4aca32cc957ea1c",
			"ap-southeast-3": "ami-0f340321ebee4b713",
			"ap-southeast-4": "ami-05381daaeaf823dd1",
			"ca-central-1":   "ami-05647a33ef035d728",
			"ca-west-1":      "ami-008dced4fde41d1f4",
			"eu-central-1":   "ami-01e1f97fd1c113991",
			"eu-central-2":   "ami-065acce84d4598954",
			"eu-north-1":     "ami-0b72ef2f4e9aca146",
			"eu-south-1":     "ami-09736dd27e69b109a",
			"eu-south-2":     "ami-04a7d232bfca8ccaf",
			"eu-west-1":      "ami-04fa8ddcead8110a9",
			"eu-west-2":      "ami-052d3c3a5a5c83a82",
			"eu-west-3":      "ami-06e9203420d48e8e9",
			"il-central-1":   "ami-0ce9a037bbd55c857",
			"me-central-1":   "ami-02d31e1160bca115c",
			"me-south-1":     "ami-07caa52515e8291fe",
			"sa-east-1":      "ami-0de793dfbf8148181",
			"us-east-1":      "ami-0c321aac14de997e3",
			"us-east-2":      "ami-0fce6015e3592d4a5",
			"us-gov-east-1":  "ami-08981a10e7aca4aef",
			"us-gov-west-1":  "ami-042544030e96bb199",
			"us-west-1":      "ami-0a0fd8c46d72e5a9d",
			"us-west-2":      "ami-011274ede94622942",
		}
		region := getCurrentRegionOrFail(oc.AsAdmin())
		ami, ok := backdatedAMIs[region]
		o.Expect(ok).To(o.BeTrue(), "No backdated AMI found for region %s", region)
		return ami
	case GCPPlatform:
		// In GCP all images located in projects/rhcos-cloud/global/images are considered valid for update
		return "projects/rhcos-cloud/global/images" + "/updateble-fake-image"
	case AzurePlatform:
		// In Azure we need to configure the whole image, not only one field. We need an image in resourceID and an empty sku field
		// We use a similar resourceID as the one generated in a normal installation. Note that it contains "gen2", so it should use "hyperVGen2"
		return `{"offer":"","publisher":"","resourceID":"/resourceGroups/fake-499nn-rg/providers/Microsoft.Compute/galleries/gallery_fake21az_499nn/images/fake-499nn-gen2/versions/latest","sku":"","version":""}`
	case VspherePlatform:
		name, url := getBackdatedBootImageNameAndURL(oc)
		o.Expect(
			uploadBaseImageToCloud(ms, platform, url, name),
		).To(o.Succeed(), "Error uploading the base image %s to the cloud", url)
		logger.Infof("Uplodated: %s", name)
		logger.Infof("OK!\n")

		return name
	default:
		return ""
	}
}

// getBackdatedBootImageNameAndURL returns the vSphere template name and OVA URL
// for a backdated RHCOS image without uploading it. The caller is responsible
// for uploading to the correct vCenter/datacenter(s).
func getBackdatedBootImageNameAndURL(_ *exutil.CLI) (string, string) {
	var (
		imageVersion = "4.16"
		arch         = architecture.AMD64
	)

	exutil.By(fmt.Sprintf("Get the base image for version %s", imageVersion))
	rhcosHandler, err := GetRHCOSHandler(VspherePlatform)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the rhcos handler")

	baseImage, err := rhcosHandler.GetBaseImageFromRHCOSImageInfo(imageVersion, OSImageStreamRHEL9, arch, "")
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the base image")
	logger.Infof("Using base image %s", baseImage)

	baseImageURL, err := rhcosHandler.GetBaseImageURLFromRHCOSImageInfo(imageVersion, OSImageStreamRHEL9, arch)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the base image URL")

	// To avoid collisions with other test runs (including leftovers from a crashed, uncleaned-up
	// run) we prefix with a per-run-unique ID in addition to the "mcotest-" marker.
	return fmt.Sprintf("mcotest-%s-%s", backdatedImageRunID, baseImage), baseImageURL
}

// getReleaseFromVsphereTemplate gets the release version from the vSphere template
// used by the given BootImageResource, using its matching failure domain.
// Only MachineSets are supported; ControlPlaneMachineSets will return an error.
func getReleaseFromVsphereTemplate(bir BootImageResource) (string, error) {
	ms, ok := bir.(*MachineSet)
	if !ok {
		return "", fmt.Errorf("getReleaseFromVsphereTemplate only supports MachineSets")
	}

	vsphereTemplate, err := bir.GetCoreOsBootImage()
	if err != nil {
		return "", err
	}

	vsInfo, err := GetVSphereConnectionInfoForMachineSet(ms)
	if err != nil {
		return "", err
	}

	folder, err := ms.GetWorkspaceFolder()
	if err != nil {
		return "", err
	}

	return exutil.GetReleaseFromVsphereTemplate(vsphereTemplate, folder, vsInfo)
}

// CheckCurrentOSImageIsUpdated checks that the machineset/controlplanemachineset is using the bootimage expected in the current cluster version.
// It also verifies that the image reference changed from fakeImageName (on non-vSphere platforms) or that the
// template name was preserved (on vSphere, where the MCO updates the OVA in-place without renaming the template).
func CheckCurrentOSImageIsUpdated(bir BootImageResource, fakeImageName string) {
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
		if fakeImageName != "" {
			o.Expect(bir.GetCoreOsBootImage()).NotTo(o.Equal(fakeImageName),
				"%s boot image was not updated, it still has the fake image", bir)
		}
	case VspherePlatform:
		o.Eventually(func() (string, error) {
			return getReleaseFromVsphereTemplate(bir)
		}, "5m", "20s").
			Should(o.Equal(currentCoreOsBootImage), "The image used to update %s doesn't have the right version", bir)
		if fakeImageName != "" {
			o.Expect(bir.GetCoreOsBootImage()).To(o.Equal(fakeImageName),
				"%s template name was changed, but MCO should update the OVA in-place without renaming the template", bir)
		}
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
		if fakeImageName != "" {
			o.Expect(bir.GetCoreOsBootImage()).NotTo(o.Equal(fakeImageName),
				"%s boot image was not updated, it still has the fake image", bir)
		}
	default:
		e2e.Failf("Platform not supported in CheckCurrentOSImageIsUpdated: %s", platform)
	}
}

// CheckCurrentOSImageIsNotUpdated checks that the machineset/controlplanemachineset is NOT using the current cluster bootimage,
// i.e. the MCO has not updated it. On vSphere, where the template name doesn't change during updates, it checks that the
// RHCOS version inside the template has not been updated to the current version.
func CheckCurrentOSImageIsNotUpdated(bir BootImageResource, fakeImageName string) {
	var (
		oc       = bir.GetOC()
		platform = exutil.CheckPlatform(oc)
	)

	switch platform {
	case VspherePlatform:
		var (
			region             = getCurrentRegionOrFail(oc)
			arch               = bir.GetArchitectureOrFail()
			coreosBootimagesCM = NewConfigMap(oc.AsAdmin(), MachineConfigNamespace, "coreos-bootimages")
		)
		currentCoreOsBootImage := getCoreOsBootImageFromConfigMapOrFail(platform, region, arch, coreosBootimagesCM)
		o.Expect(currentCoreOsBootImage).NotTo(o.BeEmpty(), "Could not find the right coreOS image for this platform")

		o.Consistently(func() string {
			release, err := getReleaseFromVsphereTemplate(bir)
			if err != nil {
				// A non-existing template means the MCO did not update it
				return ""
			}
			return release
		}, "15s", "5s").ShouldNot(o.Equal(currentCoreOsBootImage),
			"%s was updated but it should NOT have been", bir)
	case AzurePlatform:
		// Compare by resourceID only to avoid field-ordering sensitivity in the full Image JSON.
		expectedResourceID := gjson.Get(fakeImageName, "resourceID").String()
		o.Consistently(func() (string, error) {
			img, err := bir.GetCoreOsBootImage()
			if err != nil {
				return "", err
			}
			return gjson.Get(img, "resourceID").String(), nil
		}, "15s", "5s").Should(o.Equal(expectedResourceID),
			"%s was updated but it should NOT have been", bir)
	default:
		o.Consistently(bir.GetCoreOsBootImage, "15s", "5s").Should(o.Equal(fakeImageName),
			"%s was updated but it should NOT have been", bir)
	}
}

// getFakeNoUpdateBootImage returns a platform-appropriate fake boot image value that will not be
// recognised as a valid managed image by MCO, so the resource carrying it is expected to stay
// unchanged. On Azure the image field is a struct, so a plain string would be rejected by the
// MachineSet admission webhook; we wrap it in a minimal Image JSON object instead.
func getFakeNoUpdateBootImage(oc *exutil.CLI, id string) string {
	if exutil.CheckPlatform(oc) == AzurePlatform {
		return fmt.Sprintf(`{"offer":"","publisher":"","resourceID":"fake-noupdate-image-%s","sku":"","version":""}`, id)
	}
	return "fake-noupdate-image-" + id
}

// setArchitectureAndCheckStatus sets the capacity labels annotation on the cloned machineset and checks the status.
// If archValue already contains "kubernetes.io/arch=", it is used as the raw annotation value.
// Otherwise, "kubernetes.io/arch=" is prepended automatically.
func setArchitectureAndCheckStatus(clonedMS *MachineSet, machineConfiguration *MachineConfiguration, archValue string) {
	labels := archValue
	if !strings.Contains(archValue, "kubernetes.io/arch=") {
		labels = "kubernetes.io/arch=" + archValue
	}

	exutil.By(fmt.Sprintf("Set a %s architecture in the cloned machineset", labels))
	o.Expect(clonedMS.SetAutoscalerLabels(labels)).To(o.Succeed(), "Error setting architecture %s in %s", labels, clonedMS)
	logger.Infof("Architecture %s set in %s\n", labels, clonedMS)

	exutil.By("Check that no failures are being reported")
	o.Eventually(machineConfiguration, "5m", "20s").Should(HaveConditionField("BootImageUpdateDegraded", "status", "False"),
		"Expected %s not to be BootImageUpdateDegraded.\n%s", machineConfiguration.PrettyString())

	o.Eventually(machineConfiguration, "5m", "20s").Should(HaveConditionField("BootImageUpdateProgressing", "status", "False"),
		"Expected %s not to be BootImageUpdateDegraded.\n%s", machineConfiguration.PrettyString())
	logger.Infof("No failures are being reported\n")
}
