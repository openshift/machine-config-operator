package extended

import (
	g "github.com/onsi/ginkgo/v2"
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
			logger.Infof(tc.description)
			testMachineConfigurationStatusUpdate(machineConfiguration, tc.patchConfig)
		}
	})
})
