package extended

import (
	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/disruptive][Serial][Disruptive][OCPFeatureGate:ManagedBootImagesAWSCAPI] MCO CAPI Bootimages", g.Label("Platform:aws"), func() {
	defer g.GinkgoRecover()

	var (
		oc                   = exutil.NewCLI("mco-capi-bootimages", exutil.KubeConfigPath())
		machineConfiguration *MachineConfiguration
	)

	g.JustBeforeEach(func() {
		exutil.SkipOnSingleNodeTopology(oc.AsAdmin())
		skipTestIfSupportedPlatformNotMatched(oc, AWSPlatform)
		SkipIfNoFeatureGate(oc.AsAdmin(), "ManagedBootImagesAWSCAPI")

		PreChecks(oc)
		machineConfiguration = GetMachineConfiguration(oc.AsAdmin())
	})

	g.It("[OTP] CAPI MachineConfiguration status correctly reflects spec changes [apigroup:machineconfiguration.openshift.io]", func() {
		var (
			testConfigs = []struct {
				description string
				patchConfig string
			}{
				{
					description: "Test Partial mode for CAPI MachineSet resource",
					patchConfig: `{"spec":{"managedBootImages":{"machineManagers":[{"resource":"machinesets","apiGroup":"cluster.x-k8s.io","selection":{"mode":"Partial","partial":{"machineResourceSelector":{"matchLabels":{"test-label":"test-value"}}}}}]}}}`,
				},
				{
					description: "Test None mode for CAPI MachineSet resource",
					patchConfig: `{"spec":{"managedBootImages":{"machineManagers":[{"resource":"machinesets","apiGroup":"cluster.x-k8s.io","selection":{"mode":"None"}}]}}}`,
				},
				{
					description: "Test All mode for CAPI MachineSet resource",
					patchConfig: `{"spec":{"managedBootImages":{"machineManagers":[{"resource":"machinesets","apiGroup":"cluster.x-k8s.io","selection":{"mode":"All"}}]}}}`,
				},
				{
					description: "Test Partial mode for CAPI MachineDeployment resource",
					patchConfig: `{"spec":{"managedBootImages":{"machineManagers":[{"resource":"machinedeployments","apiGroup":"cluster.x-k8s.io","selection":{"mode":"Partial","partial":{"machineResourceSelector":{"matchLabels":{"test-label":"test-value"}}}}}]}}}`,
				},
				{
					description: "Test None mode for CAPI MachineDeployment resource",
					patchConfig: `{"spec":{"managedBootImages":{"machineManagers":[{"resource":"machinedeployments","apiGroup":"cluster.x-k8s.io","selection":{"mode":"None"}}]}}}`,
				},
				{
					description: "Test All mode for CAPI MachineDeployment resource",
					patchConfig: `{"spec":{"managedBootImages":{"machineManagers":[{"resource":"machinedeployments","apiGroup":"cluster.x-k8s.io","selection":{"mode":"All"}}]}}}`,
				},
			}
		)

		for _, tc := range testConfigs {
			logger.Infof("%s", tc.description)
			testMachineConfigurationStatusUpdate(machineConfiguration, tc.patchConfig)
		}
	})
})

// CAPI counterparts of the MAPI bootimages longduration test cases. The common logic
// lives in the shared test functions in mco_bootimages.go; these wrappers only select
// the CAPI machine management API.
var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/disruptive][Serial][Disruptive][OCPFeatureGate:ManagedBootImagesAWSCAPI] MCO CAPI Bootimages", g.Label("Platform:aws"), func() {
	defer g.GinkgoRecover()

	var (
		oc = exutil.NewCLI("mco-capi-bootimages", exutil.KubeConfigPath())
	)

	g.JustBeforeEach(func() {
		// CAPI boot image updates are only supported on AWS
		skipTestIfSupportedPlatformNotMatched(oc, AWSPlatform)
		SkipIfNoFeatureGate(oc.AsAdmin(), "ManagedBootImagesAWSCAPI")
		SkipTestIfWorkersCannotBeScaled(oc.AsAdmin(), CAPI)
		// Registered before any other cleanup so that, cleanups being LIFO, it runs last
		g.DeferCleanup(func() { DeleteCAPIManagedTemplates(oc) })
		// The tests only manage rhel-9 machinesets, so relabel any rhel-10 ones as rhel-9
		// for the duration of the test
		relabelRhel10MachineSetsAsRhel9(oc)
		PreChecks(oc)
		machineConfiguration := GetMachineConfiguration(oc.AsAdmin())

		// Disable skew to avoid collisions
		exutil.By("Disabling skew functionality")
		initialMachineConfiguration := machineConfiguration.GetSpecOrFail()

		// Registered before DisableSkew so the spec is restored even if disabling skew fails
		g.DeferCleanup(func() {
			exutil.By("Restoring initial MachineConfiguration spec")
			o.Expect(machineConfiguration.SetSpec(initialMachineConfiguration)).To(o.Succeed(), "Error restoring initial MachineConfiguration spec")
			logger.Infof("OK!\n")
		})
		DisableSkew(machineConfiguration)
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:00][OTP] ManagedBootImages. Restore All MachineSet images", func() {
		testManagedBootImagesRestoreAll(oc, CAPI)
	})

	g.It("[PolarionID:00][OTP] ManagedBootImages. Restore Partial MachineSet images", func() {
		testManagedBootImagesRestorePartial(oc, CAPI)
	})

	g.It("[PolarionID:00][OTP] ManagedBootImages. Fix errors", func() {
		testManagedBootImagesFixErrors(oc, CAPI)
	})

	g.It("[PolarionID:00][OTP] Check in the boot image controller to work with multiple labels for annotation", func() {
		testBootImageControllerMultipleLabels(oc, CAPI)
	})

	g.It("[PolarionID:00][OTP] Correctly handle marketplace bootimages", func() {
		testCorrectlyHandleMarketplaceBootimages(oc, CAPI)
	})

	g.It("[PolarionID:00][OTP] Verify in boot-image by default update is opt-in", func() {
		testVerifyDefaultBootImageUpdateOptIn(oc, CAPI)
	})
})
