package extended

import (
	"strings"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"github.com/tidwall/gjson"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/disruptive][Serial][Disruptive][OCPFeatureGate:ManagedBootImagesCPMS] MCO ControlPlaneMachineSet", g.Label("Platform:gce", "Platform:aws", "Platform:azure"), func() {
	defer g.GinkgoRecover()

	var (
		oc = exutil.NewCLI("mco-controlplanemachineset", exutil.KubeConfigPath())
		// Common variables
		cpms                 *ControlPlaneMachineSet
		machines             []*Machine
		machineConfiguration *MachineConfiguration
	)

	g.JustBeforeEach(func() {
		// Skip if single node
		exutil.SkipOnSingleNodeTopology(oc.AsAdmin())
		// Skip if no machineset
		SkipTestIfWorkersCannotBeScaled(oc.AsAdmin())
		// ControlPlaneMachineSet Bootimages Update functionality is only available in GCP, AWS, and Azure (Tech Preview)
		skipTestIfSupportedPlatformNotMatched(oc, GCPPlatform, AWSPlatform, AzurePlatform)
		// Skip if ManagedBootImagesCPMS feature gate is not enabled
		SkipIfNoFeatureGate(oc.AsAdmin(), "ManagedBootImagesCPMS")

		PreChecks(oc)

		failureHandler := func(message string, callerSkip ...int) {
			logger.Errorf("Gomega assertion failed!")
			logger.Errorf("Failure message: %s", message)

			// debug the machinesets
			oc.AsAdmin().Run("get", "-n", MachineAPINamespace, "machine.m", "-owide").Execute()

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

		cpms = NewControlPlaneMachineSet(oc.AsAdmin(), MachineAPINamespace, ControlPlaneMachineSetName)
		if !cpms.Exists() {
			g.Skip(` "cluster" ControlPlaneMachineset does not exist`)
		}

		machines = cpms.GetMachinesOrFail()
		machineConfiguration = GetMachineConfiguration(oc.AsAdmin())
	})

	g.JustAfterEach(func() {
		o.RegisterFailHandler(g.Fail)
	})

	// AI-assisted: This test case validates that marketplace boot images are correctly handled and NOT updated
	g.It("[PolarionID:85478][OTP] ControlPlaneMachineSets. Correctly handle marketplace boot-images [apigroup:machineconfiguration.openshift.io]", func() {
		// After talking with devs this test case doesn't make sense in Azure.
		// In Azure we shouldn't be allowed to manipulate the values to set invalid values, and we will always update legacy images. Hence, we skip this test case.
		skipTestIfSupportedPlatformNotMatched(oc, GCPPlatform, AWSPlatform)

		var (
			fakeImageName           = "fake-image" // non-updateable marketplace image
			userDataJSONVersionPath = `ignition.version`
		)

		o.Expect(machines).To(o.HaveLen(3), "Unexpected number of control plane machines")

		exutil.By("Backup the original ControlPlaneMachineSet spec for restoration")
		originalCPMSSpec := cpms.GetSpecOrFail()
		defer cpms.SetSpec(originalCPMSSpec)
		logger.Infof("OK!\n")

		exutil.By("Set a 2.2.0 user-data secret in the ControlPlaneMachineSet")
		logger.Infof("Getting the user-data secret and backing up its content")
		userDataSecret, err := cpms.GetUserDataSecret()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting user-data secret from %s", cpms)

		// Backup original user-data content to restore later
		originalUserData, err := userDataSecret.GetDataValue("userData")
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting userData from secret %s", userDataSecret)
		defer userDataSecret.SetDataValue("userData", originalUserData)

		logger.Infof("Converting user-data to version 2.2.0")
		convertedUserData, err := convertUserDataToNewVersion(originalUserData, "2.2.0")
		o.Expect(err).NotTo(o.HaveOccurred(), "Error converting userData to version 2.2.0")

		logger.Infof("Updating the user-data secret with version 2.2.0")
		o.Expect(userDataSecret.SetDataValue("userData", convertedUserData)).To(o.Succeed(),
			"Error setting userData in secret %s", userDataSecret)
		logger.Infof("OK!\n")

		exutil.By("Set a fake/non-updateable boot image in the ControlPlaneMachineSet")
		o.Expect(cpms.SetCoreOsBootImage(fakeImageName)).To(o.Succeed(), "Error setting a fake boot image in %s", cpms)
		logger.Infof("OK!\n")

		exutil.By("Opt-in boot images update with All mode")
		defer machineConfiguration.SetSpec(machineConfiguration.GetSpecOrFail())
		o.Expect(
			machineConfiguration.SetAllManagedBootImagesConfig(ControlPlaneMachineSetResource),
		).To(o.Succeed(), "Error configuring All managedBootImages in the 'cluster' MachineConfiguration resource")
		logger.Infof("OK!\n")

		exutil.By("Check that the bootimage was NOT updated")
		// Marketplace/fake images should not be updated even with All mode configured
		o.Consistently(cpms.GetCoreOsBootImage, "5m", "20s").Should(o.ContainSubstring(fakeImageName),
			"%s was updated, but it shouldn't be updated for marketplace images", cpms)
		logger.Infof("OK!\n")

		exutil.By("Check that the user-data secret was NOT updated")
		// User-data should remain at 2.2.0 when boot image cannot be updated
		o.Consistently(userDataSecret.GetDataValue, "1m", "15s").WithArguments("userData").Should(
			HavePathWithValue(userDataJSONVersionPath, o.Equal("2.2.0")),
			"The user-data secret was updated, but it shouldn't be updated for marketplace images")
		logger.Infof("OK!\n")
	})

	// AI-assisted: This test case validates that Partial mode is not allowed for ControlPlaneMachineSets
	g.It("[PolarionID:85399][OTP] ControlPlaneMachineSets boot-image update. Partial mode not allowed [apigroup:machineconfiguration.openshift.io]", func() {

		var (
			expectedError = "Only All or None selection mode is permitted for ControlPlaneMachineSets"
		)

		o.Expect(machines).To(o.HaveLen(3), "Unexpected number of control plane machines")

		exutil.By("Configure MachineConfiguration resource to use Partial mode for ControlPlaneMachineSet resources")
		defer machineConfiguration.SetSpec(machineConfiguration.GetSpecOrFail())

		err := machineConfiguration.SetPartialManagedBootImagesConfig(ControlPlaneMachineSetResource, "test-label", "test-value")
		o.Expect(err).To(o.HaveOccurred(), "Expected error when configuring Partial mode for ControlPlaneMachineSets")
		o.Expect(err).To(o.BeAssignableToTypeOf(&exutil.ExitError{}), "Unexpected error when configuring controlplanemachineset partial mode in MachineConfiguration")
		o.Expect(err.(*exutil.ExitError).StdErr).To(o.ContainSubstring(expectedError),
			"Error message does not match expected: %v", err)
		logger.Infof("OK!\n")
	})

	// AI-assisted: This test case was created to validate ControlPlaneMachineSet boot-image update with All mode
	g.It("[PolarionID:85467][OTP] ControlPlaneMachineSets. Bootimage upgrade stub ignition to spec 3 [apigroup:machineconfiguration.openshift.io]", func() {

		var (
			fakeImageName = getBackdatedBootImage(oc.AsAdmin())

			userDataJSONVersionPath = `ignition.version`
		)

		o.Expect(machines).To(o.HaveLen(3), "Unexpected number of control plane machines")

		exutil.By("Backup the original ControlPlaneMachineSet spec for restoration")
		originalCPMSSpec := cpms.GetSpecOrFail()
		defer cpms.SetSpec(originalCPMSSpec)
		logger.Infof("OK!\n")

		exutil.By("Opt-in boot images update with All mode")
		defer machineConfiguration.SetSpec(machineConfiguration.GetSpecOrFail())
		o.Expect(
			machineConfiguration.SetAllManagedBootImagesConfig(ControlPlaneMachineSetResource),
		).To(o.Succeed(), "Error configuring All managedBootImages in the 'cluster' MachineConfiguration resource")
		logger.Infof("OK!\n")

		exutil.By("Set a 2.2.0 user-data secret in the ControlPlaneMachineSet")
		logger.Infof("Getting the user-data secret and backing up its content")
		userDataSecret, err := cpms.GetUserDataSecret()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting user-data secret from %s", cpms)

		// Backup original user-data content to restore later
		originalUserData, err := userDataSecret.GetDataValue("userData")
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting userData from secret %s", userDataSecret)
		defer userDataSecret.SetDataValue("userData", originalUserData)

		logger.Infof("Converting user-data to version 2.2.0")
		convertedUserData, err := convertUserDataToNewVersion(originalUserData, "2.2.0")
		o.Expect(err).NotTo(o.HaveOccurred(), "Error converting userData to version 2.2.0")

		logger.Infof("Updating the user-data secret with version 2.2.0")
		o.Expect(userDataSecret.SetDataValue("userData", convertedUserData)).To(o.Succeed(),
			"Error setting userData in secret %s", userDataSecret)
		logger.Infof("OK!\n")

		exutil.By("Set a wrong boot image in the ControlPlaneMachineSet")
		o.Expect(cpms.SetCoreOsBootImage(fakeImageName)).To(o.Succeed(), "Error setting a fake boot image in %s", cpms)
		logger.Infof("OK!\n")

		exutil.By("Check that the user-data secret is updated to the latest ignition version")
		o.Eventually(userDataSecret.GetDataValue, "5m", "15s").WithArguments("userData").Should(
			HavePathWithValue(userDataJSONVersionPath, o.Equal(IgnitionDefaultVersion)),
			"The user-data secret was not updated to the latest ignition version")

		logger.Infof("OK!\n")

		exutil.By("Check that the boot image was updated with the right version")
		// Check that it was actually updated
		o.Eventually(cpms.GetCoreOsBootImage, "5m", "20s").ShouldNot(o.Or(o.Equal(fakeImageName), o.BeEmpty()),
			"%s was NOT updated to use the right boot image", cpms)
		// Check that the updated image is the right one
		CheckCurrentOSImageIsUpdated(cpms)
		logger.Infof("OK!\n")

		exutil.By("Delete one machine and wait for it to be recreated")
		if cpms.IsActive() {
			// Only delete machine if original userData does NOT have storage or systemd sections
			hasStorage := strings.Contains(originalUserData, "storage")
			hasSystemd := strings.Contains(originalUserData, "systemd")

			if !hasStorage && !hasSystemd {
				DeleteOneMachineAndWaitForRecreation(cpms)
			} else {
				logger.Infof("Original user-data secret had storage or systemd sections, skipping machine deletion test")
			}
		} else {
			logger.Infof("ControlPlaneMachineSet is not active, skipping machine deletion test")
		}
		logger.Infof("OK!\n")
	})

	// AI-assisted: This test case validates that boot images and user-data are NOT updated when using Mode: None
	g.It("[PolarionID:85479][OTP] ControlPlaneMachineSets. Not updated when using Mode: None [apigroup:machineconfiguration.openshift.io]", func() {

		var (
			machineConfiguration = GetMachineConfiguration(oc.AsAdmin())
			fakeImageName        = getBackdatedBootImage(oc.AsAdmin())

			userDataJSONVersionPath = `ignition.version`
		)

		o.Expect(machines).To(o.HaveLen(3), "Unexpected number of control plane machines")

		exutil.By("Backup the original ControlPlaneMachineSet spec for restoration")
		originalCPMSSpec := cpms.GetSpecOrFail()
		defer cpms.SetSpec(originalCPMSSpec)
		logger.Infof("OK!\n")

		exutil.By("Configure MachineConfiguration resource with mode None for controlplanemachinesets")
		defer machineConfiguration.SetSpec(machineConfiguration.GetSpecOrFail())
		o.Expect(
			machineConfiguration.SetNoneManagedBootImagesConfig(ControlPlaneMachineSetResource),
		).To(o.Succeed(), "Error configuring None managedBootImages in the 'cluster' MachineConfiguration resource")
		logger.Infof("OK!\n")

		exutil.By("Set a 2.2.0 user-data secret in the ControlPlaneMachineSet")
		logger.Infof("Getting the user-data secret and backing up its content")
		userDataSecret, err := cpms.GetUserDataSecret()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting user-data secret from %s", cpms)

		// Backup original user-data content to restore later
		originalUserData, err := userDataSecret.GetDataValue("userData")
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting userData from secret %s", userDataSecret)
		defer userDataSecret.SetDataValue("userData", originalUserData)

		logger.Infof("Converting user-data to version 2.2.0")
		convertedUserData, err := convertUserDataToNewVersion(originalUserData, "2.2.0")
		o.Expect(err).NotTo(o.HaveOccurred(), "Error converting userData to version 2.2.0")

		logger.Infof("Updating the user-data secret with version 2.2.0")
		o.Expect(userDataSecret.SetDataValue("userData", convertedUserData)).To(o.Succeed(),
			"Error setting userData in secret %s", userDataSecret)
		logger.Infof("OK!\n")

		exutil.By("Set a wrong boot image in the ControlPlaneMachineSet")
		o.Expect(cpms.SetCoreOsBootImage(fakeImageName)).To(o.Succeed(), "Error setting a fake boot image in %s", cpms)
		logger.Infof("OK!\n")

		exutil.By("Check that the boot image was NOT updated")
		// With Mode: None, the boot image should remain unchanged (still using the fake image)
		o.Consistently(cpms.GetCoreOsBootImage, "3m", "30s").Should(o.Equal(fakeImageName),
			"The boot image was unexpectedly updated when Mode: None was configured")
		logger.Infof("OK!\n")

		exutil.By("Check that the master-user-data secret was NOT updated and is still using ignition 2.2.0")
		// With Mode: None, the user-data should remain at version 2.2.0
		o.Consistently(userDataSecret.GetDataValue, "1m", "20s").WithArguments("userData").Should(
			HavePathWithValue(userDataJSONVersionPath, o.Equal("2.2.0")),
			"The user-data secret was unexpectedly updated when Mode: None was configured")
		logger.Infof("OK!\n")
	})

	// AI-assisted: This test case validates that boot images and user-data are NOT updated when CPMS has an owner reference
	g.It("[PolarionID:85480][OTP] ControlPlaneMachineSets. Not updated when owner reference [apigroup:machineconfiguration.openshift.io]", func() {

		var (
			fakeImageName = getBackdatedBootImage(oc.AsAdmin())

			userDataJSONVersionPath = `ignition.version`
		)

		o.Expect(machines).To(o.HaveLen(3), "Unexpected number of control plane machines")

		exutil.By("Backup the original ControlPlaneMachineSet spec for restoration")
		originalCPMSSpec := cpms.GetSpecOrFail()
		defer cpms.SetSpec(originalCPMSSpec)
		logger.Infof("OK!\n")

		exutil.By("Add owner reference to the ControlPlaneMachineSet resource")
		// Backup original ownerReferences to restore later
		originalOwnerRefs := cpms.GetOrFail(`{.metadata.ownerReferences}`)
		defer func() {
			if originalOwnerRefs == "" {
				cpms.Patch("json", `[{"op": "remove", "path": "/metadata/ownerReferences"}]`)
			} else {
				cpms.Patch("json", `[{"op": "replace", "path": "/metadata/ownerReferences", "value": `+originalOwnerRefs+`}]`)
			}
		}()

		o.Expect(
			cpms.Patch("merge", `{"metadata":{"ownerReferences": [{"apiVersion": "fake","blockOwnerDeletion": true,"controller": true,"kind": "fakekind","name": "master","uid": "fake-uuid"}]}}`),
		).To(o.Succeed(), "Error patching %s with a fake owner reference", cpms)
		logger.Infof("OK!\n")

		exutil.By("Set a 2.2.0 user-data secret in the ControlPlaneMachineSet")
		logger.Infof("Getting the user-data secret and backing up its content")
		userDataSecret, err := cpms.GetUserDataSecret()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting user-data secret from %s", cpms)

		// Backup original user-data content to restore later
		originalUserData, err := userDataSecret.GetDataValue("userData")
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting userData from secret %s", userDataSecret)
		defer userDataSecret.SetDataValue("userData", originalUserData)

		logger.Infof("Converting user-data to version 2.2.0")
		convertedUserData, err := convertUserDataToNewVersion(originalUserData, "2.2.0")
		o.Expect(err).NotTo(o.HaveOccurred(), "Error converting userData to version 2.2.0")

		logger.Infof("Updating the user-data secret with version 2.2.0")
		o.Expect(userDataSecret.SetDataValue("userData", convertedUserData)).To(o.Succeed(),
			"Error setting userData in secret %s", userDataSecret)
		logger.Infof("OK!\n")

		exutil.By("Set a wrong boot image in the ControlPlaneMachineSet")
		o.Expect(cpms.SetCoreOsBootImage(fakeImageName)).To(o.Succeed(), "Error setting a fake boot image in %s", cpms)
		logger.Infof("OK!\n")

		exutil.By("Configure MachineConfiguration resource with mode All for controlplanemachinesets")
		defer machineConfiguration.SetSpec(machineConfiguration.GetSpecOrFail())
		o.Expect(
			machineConfiguration.SetAllManagedBootImagesConfig(ControlPlaneMachineSetResource),
		).To(o.Succeed(), "Error configuring All managedBootImages in the 'cluster' MachineConfiguration resource")
		logger.Infof("OK!\n")

		exutil.By("Check that the boot image was NOT updated")
		// With owner reference, the boot image should remain unchanged even with Mode: All
		o.Consistently(cpms.GetCoreOsBootImage, "3m", "30s").Should(o.Equal(fakeImageName),
			"The boot image was unexpectedly updated when owner reference was present")
		logger.Infof("OK!\n")

		exutil.By("Check that the master-user-data secret was NOT updated and is still using ignition 2.2.0")
		// With owner reference, the user-data should remain at version 2.2.0 even with Mode: All
		o.Consistently(userDataSecret.GetDataValue, "1m", "20s").WithArguments("userData").Should(
			HavePathWithValue(userDataJSONVersionPath, o.Equal("2.2.0")),
			"The user-data secret was unexpectedly updated when owner reference was present")
		logger.Infof("OK!\n")
	})

	// AI-assisted: This test case validates that MachineConfiguration status correctly reflects spec changes
	g.It("[PolarionID:87023][OTP] MachineConfiguration status correctly reflects spec changes [apigroup:machineconfiguration.openshift.io]", func() {

		var (
			testConfigs = []struct {
				description string
				patchConfig string
			}{
				{
					description: "Test Partial mode for MachineSet resource",
					patchConfig: `{"spec":{"managedBootImages":{"machineManagers":[{"resource":"machinesets","apiGroup":"machine.openshift.io","selection":{"mode":"Partial","partial":{"machineResourceSelector":{"matchLabels":{"test-label":"test-value"}}}}}]}}}`,
				},
				{
					description: "Test None mode for MachineSet resource",
					patchConfig: `{"spec":{"managedBootImages":{"machineManagers":[{"resource":"machinesets","apiGroup":"machine.openshift.io","selection":{"mode":"None"}}]}}}`,
				},
				{
					description: "Test All mode for MachineSet resource",
					patchConfig: `{"spec":{"managedBootImages":{"machineManagers":[{"resource":"machinesets","apiGroup":"machine.openshift.io","selection":{"mode":"All"}}]}}}`,
				},
				{
					description: "Test None mode for ControlPlaneMachineSet resource",
					patchConfig: `{"spec":{"managedBootImages":{"machineManagers":[{"resource":"controlplanemachinesets","apiGroup":"machine.openshift.io","selection":{"mode":"None"}}]}}}`,
				},
				{
					description: "Test All mode for ControlPlaneMachineSet resource",
					patchConfig: `{"spec":{"managedBootImages":{"machineManagers":[{"resource":"controlplanemachinesets","apiGroup":"machine.openshift.io","selection":{"mode":"All"}}]}}}`,
				},
			}
		)

		for _, tc := range testConfigs {
			logger.Infof(tc.description)
			testMachineConfigurationStatusUpdate(machineConfiguration, tc.patchConfig)
		}
	})
})

// testMachineConfigurationStatusUpdate validates that MachineConfiguration status updates correctly
func testMachineConfigurationStatusUpdate(mc *MachineConfiguration, patchConfig string) {
	exutil.By("Capture original MachineConfiguration status")

	originalSpec := mc.GetSpecOrFail()
	defer mc.SetSpec(originalSpec)

	// Disable skew enforcement for the test to avoid boot image configurations causing conflicts
	o.Expect(mc.SetNoneSkew()).To(o.Succeed(), "Error disabling skew enforcement on %s", mc)

	// Wait for the controller to observe and process the spec change
	o.Eventually(func() (bool, error) {
		generation, err := mc.Get(`{.metadata.generation}`)
		if err != nil {
			return false, err
		}
		observedGeneration, err := mc.Get(`{.status.observedGeneration}`)
		if err != nil {
			return false, err
		}
		return generation == observedGeneration, nil
	}, "2m", "10s").Should(o.BeTrue(), "MachineConfiguration observedGeneration did not catch up to generation")

	originalStatus, err := mc.GetManagedBootImagesStatus()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting original status from %s", mc)
	logger.Infof("Original status: %s", originalStatus)

	// Capture the status for each resource before applying the config
	resourcesBefore, err := mc.GetAllManagedBootImagesResources()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting all resources from status")

	originalResourceStatus := make(map[string]string)
	for _, res := range resourcesBefore {
		if res != "" {
			status, err := mc.GetManagedBootImagesStatusForResource(res)
			o.Expect(err).NotTo(o.HaveOccurred(), "Error getting status for resource %s", res)
			originalResourceStatus[res] = status
			logger.Infof("Captured original status for resource %s", res)
		}
	}
	logger.Infof("OK!\n")

	exutil.By("Extract resource and expected status config from the patch config")
	expectedStatusConfig := gjson.Get(patchConfig, "spec.managedBootImages.machineManagers.0").String()
	o.Expect(expectedStatusConfig).NotTo(o.BeEmpty(), "Failed to extract expected status config from patchConfig")

	resource := gjson.Get(patchConfig, "spec.managedBootImages.machineManagers.0.resource").String()
	o.Expect(resource).NotTo(o.BeEmpty(), "Failed to extract resource from patchConfig")

	logger.Infof("Resource: %s", resource)
	logger.Infof("Expected status config: %s", expectedStatusConfig)
	logger.Infof("OK!\n")

	exutil.By("Apply the new configuration")
	o.Expect(mc.Patch("merge", patchConfig)).To(o.Succeed(), "Error applying configuration to %s", mc)
	logger.Infof("OK!\n")

	exutil.By("Check that the configured resource is correctly reported in the status")
	o.Eventually(mc.GetManagedBootImagesStatusForResource, "5m", "10s").WithArguments(resource).Should(o.MatchJSON(expectedStatusConfig),
		"Resource %s status does not match the expected configuration. Expected: %s", resource, expectedStatusConfig)
	logger.Infof("OK!\n")

	exutil.By("Check that other managedBootImagesStatus configurations remain the same as before")
	for res, originalStatusValue := range originalResourceStatus {
		if res != resource {
			o.Eventually(mc.GetManagedBootImagesStatusForResource, "5m", "10s").WithArguments(res).Should(o.MatchJSON(originalStatusValue),
				"Resource %s status changed unexpectedly. Expected: %s", res, originalStatusValue)
			logger.Infof("Resource %s status unchanged", res)
		}
	}
	logger.Infof("OK!\n")

	exutil.By("Remove the applied configuration and restore original spec")
	o.Expect(mc.SetSpec(originalSpec)).To(o.Succeed(), "Error restoring original spec in %s", mc)
	logger.Infof("OK!\n")

	exutil.By("Check that the original status values were restored")
	o.Eventually(mc.GetManagedBootImagesStatus, "5m", "10s").Should(o.MatchJSON(originalStatus),
		"Status was not restored to original value")
	logger.Infof("OK!\n")
}
