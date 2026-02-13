package extended

import (
	"context"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	constants "github.com/openshift/machine-config-operator/pkg/controller/common"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

const (
	// Test RHCOS & OCP versions for skew enforcement tests
	rhcosVersionExceedsSkew = "48.84.202208021106-0"
	ocpVersionExceedsSkew   = "4.12.0"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/disruptive][Serial][Disruptive][OCPFeatureGate:BootImageSkewEnforcement]", func() {
	defer g.GinkgoRecover()

	var (
		oc                   = exutil.NewCLI("mco-bootimage", exutil.KubeConfigPath()).AsAdmin()
		machineConfiguration *MachineConfiguration
		originalSpec         string
		mcoCO                *ClusterOperator
	)

	g.BeforeEach(func() {
		// Skip on single-node topologies
		exutil.SkipOnSingleNodeTopology(oc)
		machineConfiguration = GetMachineConfiguration(oc)
		// Save initial state to restore after each test
		originalSpec = machineConfiguration.GetSpecOrFail()
		mcoCO = NewClusterOperator(oc, "machine-config")
	})

	g.AfterEach(func() {
		exutil.By("Restoring MachineConfiguration to original state")
		o.Expect(machineConfiguration.SetSpec(originalSpec)).To(o.Succeed())
	})

	g.It("Verify Manual mode with RHCOSVersion and Upgradeable (Happy case) [apigroup:machineconfiguration.openshift.io]", func() {
		// Get the current RHCOS version from the coreos-bootimages configmap (guaranteed to be within skew)
		rhcosVersionWithinSkew := GetRHCOSVersionFromConfigMap(oc)
		logger.Infof("Using RHCOS version from configmap: %s", rhcosVersionWithinSkew)

		// Set manual mode with a boot image version that is within skew limits
		o.Expect(machineConfiguration.SetManualSkew(RHCOSVersionMode, rhcosVersionWithinSkew)).To(o.Succeed())

		// Wait for the controller to reflect Manual mode in skew enforcement status
		machineConfiguration.WaitForBootImageSkewEnforcementStatusMode(SkewEnforcementManualMode)

		// Check machine-config CO upgradeable status, should be set to true
		o.Eventually(mcoCO, "1m", "10s").Should(BeUpgradeable(), "co/machine-config should be upgradeable when manual skew version is within limits")
	})

	g.It("Verify Manual mode with RHCOSVersion and Upgradeable (Sad case) [apigroup:machineconfiguration.openshift.io]", func() {
		// Set manual mode with a boot image version that is NOT within skew limits
		o.Expect(machineConfiguration.SetManualSkew(RHCOSVersionMode, rhcosVersionExceedsSkew)).To(o.Succeed())

		// Wait for the controller to reflect Manual mode in skew enforcement status
		machineConfiguration.WaitForBootImageSkewEnforcementStatusMode(SkewEnforcementManualMode)

		// Check machine-config CO upgradeable status, should be set to false
		o.Eventually(mcoCO, "1m", "10s").ShouldNot(BeUpgradeable(), "co/machine-config should not be upgradeable when manual skew version exceeds limits")
	})

	g.It("Verify Manual mode with OCPVersion and Upgradeable (Happy case) [apigroup:machineconfiguration.openshift.io]", func() {
		// Set manual mode with a boot image version that is within skew limits
		o.Expect(machineConfiguration.SetManualSkew(OCPVersionMode, constants.OCPVersionBootImageSkewLimit)).To(o.Succeed())

		// Wait for the controller to reflect Manual mode in skew enforcement status
		machineConfiguration.WaitForBootImageSkewEnforcementStatusMode(SkewEnforcementManualMode)

		// Check machine-config CO upgradeable status, should be set to true
		o.Eventually(mcoCO, "1m", "10s").Should(BeUpgradeable(), "co/machine-config should be upgradeable when manual skew version is within limits")
	})

	g.It("Verify Manual mode with OCPVersion and Upgradeable (Sad case) [apigroup:machineconfiguration.openshift.io]", func() {
		// Set manual mode with a boot image version that is NOT within skew limits
		o.Expect(machineConfiguration.SetManualSkew(OCPVersionMode, ocpVersionExceedsSkew)).To(o.Succeed())

		// Wait for the controller to reflect Manual mode in skew enforcement status
		machineConfiguration.WaitForBootImageSkewEnforcementStatusMode(SkewEnforcementManualMode)

		// Check machine-config CO upgradeable status, should be set to false
		o.Eventually(mcoCO, "1m", "10s").ShouldNot(BeUpgradeable(), "co/machine-config should not be upgradeable when manual skew version exceeds limits")
	})

	g.It("Verify Automatic mode and Upgradeable (Happy Case) [apigroup:machineconfiguration.openshift.io]", func() {
		// only applicable on clusters where we support automatic updates: GCP, AWS, Azure and vSphere
		skipTestIfSupportedPlatformNotMatched(oc, GCPPlatform, AWSPlatform, AzurePlatform, VspherePlatform)

		// No opinion on skew enforcement for these platforms will result in Automatic mode
		o.Expect(machineConfiguration.RemoveSkew()).To(o.Succeed())

		// Wait for the controller to reflect Automatic mode in skew enforcement status
		machineConfiguration.WaitForBootImageSkewEnforcementStatusMode(SkewEnforcementAutomaticMode)

		// Pick a random machineset to test
		machineSetUnderTest := NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail()[0]
		logger.Infof("MachineSet under test: %s", machineSetUnderTest.name)

		// Save and restore full spec to ensure cleanup regardless of what we modify
		originalMachineSetSpec := machineSetUnderTest.GetSpecOrFail()
		defer func() {
			o.Expect(machineSetUnderTest.SetSpec(originalMachineSetSpec)).To(o.Succeed())
		}()

		// Patch the boot image to an older version to trigger an update loop
		backdatedBootImage := getBackdatedBootImage(oc)
		o.Expect(machineSetUnderTest.SetCoreOsBootImage(backdatedBootImage)).To(o.Succeed())
		logger.Infof("Set backdated boot image '%s' in MachineSet %s to trigger update loop", backdatedBootImage, machineSetUnderTest.name)

		// Verify that the boot image controller has finished processing
		machineConfiguration.WaitForBootImageControllerComplete()

		// Verify that the boot image controller is not degraded
		machineConfiguration.WaitForBootImageControllerDegradedState(false)

		// Check machine-config CO upgradeable status, should be set to True
		o.Eventually(mcoCO, "1m", "10s").Should(BeUpgradeable(), "co/machine-config should be upgradeable with restored machineset")
	})

	g.It("Verify Automatic mode and Upgradeable (Sad Case) [apigroup:machineconfiguration.openshift.io]", func(_ context.Context) {
		// only applicable on clusters where we support automatic updates: GCP, AWS, Azure and vSphere
		skipTestIfSupportedPlatformNotMatched(oc, GCPPlatform, AWSPlatform, AzurePlatform, VspherePlatform)

		// No opinion on skew enforcement for these platforms will result in Automatic mode
		o.Expect(machineConfiguration.RemoveSkew()).To(o.Succeed())

		// Wait for the controller to reflect Automatic mode in skew enforcement status
		machineConfiguration.WaitForBootImageSkewEnforcementStatusMode(SkewEnforcementAutomaticMode)

		// Pick a random machineset to test
		machineSetUnderTest := NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail()[0]
		logger.Infof("MachineSet under test: %s", machineSetUnderTest.name)

		// Save and restore full spec to ensure cleanup regardless of what we modify
		originalMachineSetSpec := machineSetUnderTest.GetSpecOrFail()
		defer func() {
			o.Expect(machineSetUnderTest.SetSpec(originalMachineSetSpec)).To(o.Succeed())
		}()

		// Set a non-existent user data secret in the machineset's providerSpec; this will cause a boot image controller degrade
		nonExistentSecret := "non-existent-user-data"
		o.Expect(machineSetUnderTest.SetUserDataSecret(nonExistentSecret)).To(o.Succeed())
		logger.Infof("Set non-existent user data secret '%s' in MachineSet %s", nonExistentSecret, machineSetUnderTest.name)

		// Patch the boot image to an older version to trigger an update loop
		backdatedBootImage := getBackdatedBootImage(oc)
		o.Expect(machineSetUnderTest.SetCoreOsBootImage(backdatedBootImage)).To(o.Succeed())
		logger.Infof("Set backdated boot image '%s' in MachineSet %s to trigger update loop", backdatedBootImage, machineSetUnderTest.name)

		// Verify that the boot image controller has finished processing
		machineConfiguration.WaitForBootImageControllerComplete()

		// Verify that the boot image controller is degraded
		machineConfiguration.WaitForBootImageControllerDegradedState(true)

		// Check machine-config CO upgradeable status, should be set to false due to the degrade
		o.Eventually(mcoCO, "1m", "10s").ShouldNot(BeUpgradeable(), "co/machine-config should not be upgradeable with broken machineset and update loop")

		// Restore machineset to original spec
		o.Expect(machineSetUnderTest.SetSpec(originalMachineSetSpec)).To(o.Succeed())

		// Verify that the boot image controller has finished processing
		machineConfiguration.WaitForBootImageControllerComplete()

		// Verify that the boot image controller is not degraded
		machineConfiguration.WaitForBootImageControllerDegradedState(false)

		// Check machine-config CO upgradeable status, should be set back to true
		o.Eventually(mcoCO, "1m", "10s").Should(BeUpgradeable(), "co/machine-config should be upgradeable with restored machineset")
	})

	g.It("Verify None mode [apigroup:machineconfiguration.openshift.io]", func() {
		// Set None mode, effectively disabling skew enforcement
		o.Expect(machineConfiguration.SetNoneSkew()).To(o.Succeed())

		// Wait for the controller to reflect None mode in skew enforcement status
		machineConfiguration.WaitForBootImageSkewEnforcementStatusMode(SkewEnforcementNoneMode)

		// Check machine-config CO upgradeable status, should be set to true
		o.Eventually(mcoCO, "1m", "10s").Should(BeUpgradeable(), "co/machine-config should be upgradeable when skew enforcement is disabled (None mode)")
	})
})
