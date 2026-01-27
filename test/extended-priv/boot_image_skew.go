package extended

import (
	"context"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	osconfigv1 "github.com/openshift/api/config/v1"
	opv1 "github.com/openshift/api/operator/v1"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	"k8s.io/kubernetes/test/e2e/framework"

	constants "github.com/openshift/machine-config-operator/pkg/controller/common"
)

const (
	// Test RHCOS & OCP versions for skew enforcement tests
	rhcosVersionExceedsSkew = "48.84.202208021106-0"
	ocpVersionExceedsSkew   = "4.12.0"

	// Backdated boot images for testing upgrade blocks
	// See: https://issues.redhat.com/browse/OCPBUGS-57426
	backdatedAWSBootImage = "ami-000145e5a91e9ac22"
	backdatedGCPBootImage = "projects/rhcos-cloud/global/images/rhcos-410-84-202210040010-0-gcp-x86-64"
)

// getBackdatedBootImage returns a known outdated boot image for the current platform
func getBackdatedBootImage(oc *exutil.CLI) string {
	switch exutil.CheckPlatform(oc) {
	case AWSPlatform:
		return backdatedAWSBootImage
	case GCPPlatform:
		return backdatedGCPBootImage
	default:
		framework.Failf("getBackdatedBootImage: unsupported platform")
		return ""
	}
}

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/disruptive][Serial][Disruptive][OCPFeatureGate:BootImageSkewEnforcement]", func() {
	defer g.GinkgoRecover()

	var (
		oc                   = exutil.NewCLI("mco-bootimage", exutil.KubeConfigPath()).AsAdmin()
		machineConfiguration *MachineConfiguration
		originalSpec         string
	)

	g.BeforeEach(func() {
		// Skip on single-node topologies
		SkipOnSingleNodeTopology(oc)
		machineConfiguration = GetMachineConfiguration(oc)
		// Save initial state to restore after each test
		originalSpec = machineConfiguration.GetSpecOrFail()
	})

	g.AfterEach(func() {
		exutil.By("Restoring MachineConfiguration to original state")
		o.Expect(machineConfiguration.SetSpec(originalSpec)).To(o.Succeed())
	})

	g.It("Verify Manual mode with RHCOSVersion and Upgradeable (Happy case) [apigroup:machineconfiguration.openshift.io]", func() {
		// Get the current RHCOS version from the coreos-bootimages configmap (guaranteed to be within skew)
		rhcosVersionWithinSkew := GetRHCOSVersionFromConfigMap(oc)
		framework.Logf("Using RHCOS version from configmap: %s", rhcosVersionWithinSkew)

		// Set manual mode with a boot image version that is within skew limits
		o.Expect(machineConfiguration.SetManualSkew(opv1.ClusterBootImageSpecModeRHCOSVersion, rhcosVersionWithinSkew)).To(o.Succeed())

		// Wait for the controller to reflect Manual mode in skew enforcement status
		machineConfiguration.WaitForBootImageSkewEnforcementStatusMode(opv1.BootImageSkewEnforcementModeStatusManual)

		// Check machine-config CO upgradeable status, should be set to true
		o.Eventually(NewClusterOperator(oc, "machine-config").IsConditionStatusTrue, "1m", "10s").
			WithArguments("Upgradeable").Should(o.BeTrue(), "co/machine-config should be upgradeable when manual skew version is within limits")
	})

	g.It("Verify Manual mode with RHCOSVersion and Upgradeable (Sad case) [apigroup:machineconfiguration.openshift.io]", func() {
		// Set manual mode with a boot image version that is NOT within skew limits
		o.Expect(machineConfiguration.SetManualSkew(opv1.ClusterBootImageSpecModeRHCOSVersion, rhcosVersionExceedsSkew)).To(o.Succeed())

		// Wait for the controller to reflect Manual mode in skew enforcement status
		machineConfiguration.WaitForBootImageSkewEnforcementStatusMode(opv1.BootImageSkewEnforcementModeStatusManual)

		// Check machine-config CO upgradeable status, should be set to false
		o.Eventually(NewClusterOperator(oc, "machine-config").IsConditionStatusTrue, "1m", "10s").
			WithArguments("Upgradeable").Should(o.BeFalse(), "co/machine-config should not be upgradeable when manual skew version exceeds limits")
	})

	g.It("Verify Manual mode with OCPVersion and Upgradeable (Happy case) [apigroup:machineconfiguration.openshift.io]", func() {
		// Set manual mode with a boot image version that is within skew limits
		o.Expect(machineConfiguration.SetManualSkew(opv1.ClusterBootImageSpecModeOCPVersion, constants.OCPVersionBootImageSkewLimit)).To(o.Succeed())

		// Wait for the controller to reflect Manual mode in skew enforcement status
		machineConfiguration.WaitForBootImageSkewEnforcementStatusMode(opv1.BootImageSkewEnforcementModeStatusManual)

		// Check machine-config CO upgradeable status, should be set to true
		o.Eventually(NewClusterOperator(oc, "machine-config").IsConditionStatusTrue, "1m", "10s").
			WithArguments("Upgradeable").Should(o.BeTrue(), "co/machine-config should be upgradeable when manual skew version is within limits")
	})

	g.It("Verify Manual mode with OCPVersion and Upgradeable (Sad case) [apigroup:machineconfiguration.openshift.io]", func() {
		// Set manual mode with a boot image version that is NOT within skew limits
		o.Expect(machineConfiguration.SetManualSkew(opv1.ClusterBootImageSpecModeOCPVersion, ocpVersionExceedsSkew)).To(o.Succeed())

		// Wait for the controller to reflect Manual mode in skew enforcement status
		machineConfiguration.WaitForBootImageSkewEnforcementStatusMode(opv1.BootImageSkewEnforcementModeStatusManual)

		// Check machine-config CO upgradeable status, should be set to false
		o.Eventually(NewClusterOperator(oc, "machine-config").IsConditionStatusTrue, "1m", "10s").
			WithArguments("Upgradeable").Should(o.BeFalse(), "co/machine-config should not be upgradeable when manual skew version exceeds limits")
	})

	g.It("Verify Automatic mode and Upgradeable (Happy Case) [apigroup:machineconfiguration.openshift.io]", func() {
		// only applicable on GCP, AWS clusters
		SkipUnlessTargetPlatform(oc, osconfigv1.GCPPlatformType, osconfigv1.AWSPlatformType)

		// No opinion on skew enforcement for these platforms will result in Automatic mode
		o.Expect(machineConfiguration.RemoveSkew()).To(o.Succeed())

		// Wait for the controller to reflect Automatic mode in skew enforcement status
		machineConfiguration.WaitForBootImageSkewEnforcementStatusMode(opv1.BootImageSkewEnforcementModeStatusAutomatic)

		// Pick a random machineset to test
		machineSetUnderTest := NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail()[0]
		framework.Logf("MachineSet under test: %s", machineSetUnderTest.name)

		machineSet := NewMachineSet(oc, MachineAPINamespace, machineSetUnderTest.name)
		// Save and restore full spec to ensure cleanup regardless of what we modify
		originalMachineSetSpec := machineSet.GetSpecOrFail()
		defer func() {
			o.Expect(machineSet.SetSpec(originalMachineSetSpec)).To(o.Succeed())
		}()

		// Patch the boot image to an older version to trigger an update loop
		backdatedBootImage := getBackdatedBootImage(oc)
		o.Expect(machineSet.SetCoreOsBootImage(backdatedBootImage)).To(o.Succeed())
		framework.Logf("Set backdated boot image '%s' in MachineSet %s to trigger update loop", backdatedBootImage, machineSetUnderTest.name)

		// Verify that the boot image controller has finished processing
		machineConfiguration.WaitForBootImageControllerComplete()

		// Verify that the boot image controller is not degraded
		machineConfiguration.WaitForBootImageControllerDegradedState(false)

		// Check machine-config CO upgradeable status, should be set to True
		o.Eventually(NewClusterOperator(oc, "machine-config").IsConditionStatusTrue, "2m", "10s").
			WithArguments("Upgradeable").Should(o.BeTrue(), "co/machine-config should be upgradeable with restored machineset")
	})

	g.It("Verify Automatic mode and Upgradeable (Sad Case) [apigroup:machineconfiguration.openshift.io]", func(_ context.Context) {
		// only applicable on GCP, AWS clusters
		SkipUnlessTargetPlatform(oc, osconfigv1.GCPPlatformType, osconfigv1.AWSPlatformType)

		// No opinion on skew enforcement for these platforms will result in Automatic mode
		o.Expect(machineConfiguration.RemoveSkew()).To(o.Succeed())

		// Wait for the controller to reflect Automatic mode in skew enforcement status
		machineConfiguration.WaitForBootImageSkewEnforcementStatusMode(opv1.BootImageSkewEnforcementModeStatusAutomatic)

		// Pick a random machineset to test
		machineSetUnderTest := NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail()[0]
		framework.Logf("MachineSet under test: %s", machineSetUnderTest.name)

		machineSet := NewMachineSet(oc, MachineAPINamespace, machineSetUnderTest.name)
		// Save and restore full spec to ensure cleanup regardless of what we modify
		originalMachineSetSpec := machineSet.GetSpecOrFail()
		defer func() {
			o.Expect(machineSet.SetSpec(originalMachineSetSpec)).To(o.Succeed())
		}()

		// Set a non-existent user data secret in the machineset's providerSpec; this will cause a boot image controller degrade
		nonExistentSecret := "non-existent-user-data"
		o.Expect(machineSet.SetUserDataSecret(nonExistentSecret)).To(o.Succeed())
		framework.Logf("Set non-existent user data secret '%s' in MachineSet %s", nonExistentSecret, machineSetUnderTest.name)

		// Patch the boot image to an older version to trigger an update loop
		backdatedBootImage := getBackdatedBootImage(oc)
		o.Expect(machineSet.SetCoreOsBootImage(backdatedBootImage)).To(o.Succeed())
		framework.Logf("Set backdated boot image '%s' in MachineSet %s to trigger update loop", backdatedBootImage, machineSetUnderTest.name)

		// Verify that the boot image controller has finished processing
		machineConfiguration.WaitForBootImageControllerComplete()

		// Verify that the boot image controller is degraded
		machineConfiguration.WaitForBootImageControllerDegradedState(true)

		// Check machine-config CO upgradeable status, should be set to false due to the degrade
		o.Eventually(NewClusterOperator(oc, "machine-config").IsConditionStatusTrue, "2m", "10s").
			WithArguments("Upgradeable").Should(o.BeFalse(), "co/machine-config should not be upgradeable with broken machineset and update loop")

		// Restore machineset to original spec
		o.Expect(machineSet.SetSpec(originalMachineSetSpec)).To(o.Succeed())

		// Verify that the boot image controller has finished processing
		machineConfiguration.WaitForBootImageControllerComplete()

		// Verify that the boot image controller is not degraded
		machineConfiguration.WaitForBootImageControllerDegradedState(false)

		// Check machine-config CO upgradeable status, should be set back to true
		o.Eventually(NewClusterOperator(oc, "machine-config").IsConditionStatusTrue, "2m", "10s").
			WithArguments("Upgradeable").Should(o.BeFalse(), "co/machine-config should be upgradeable with restored machineset")
	})

	g.It("Verify None mode [apigroup:machineconfiguration.openshift.io]", func() {
		// Set None mode, effectively disabling skew enforcement
		o.Expect(machineConfiguration.SetNoneSkew()).To(o.Succeed())

		// Wait for the controller to reflect None mode in skew enforcement status
		machineConfiguration.WaitForBootImageSkewEnforcementStatusMode(opv1.BootImageSkewEnforcementModeStatusNone)

		// Check machine-config CO upgradeable status, should be set to true
		o.Eventually(NewClusterOperator(oc, "machine-config").IsConditionStatusTrue, "1m", "10s").
			WithArguments("Upgradeable").Should(o.BeTrue(), "co/machine-config should be upgradeable when skew enforcement is disabled (None mode)")
	})
})
