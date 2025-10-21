package extended

import (
	"path/filepath"

	osconfigv1 "github.com/openshift/api/config/v1"

	g "github.com/onsi/ginkgo/v2"
	exutil "github.com/openshift/machine-config-operator/test/extended/util"
)

// These tests are [Serial] because it modifies the cluster/machineconfigurations.operator.openshift.io object in each test.
var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/disruptive][Serial][OCPFeatureGate:ManagedBootImagesAWS]", g.Ordered, func() {
	defer g.GinkgoRecover()
	var (
		AllControlPlaneMachineSetFixture  = filepath.Join("machineconfigurations", "managedbootimages-cpms-all.yaml")
		NoneControlPlaneMachineSetFixture = filepath.Join("machineconfigurations", "managedbootimages-cpms-none.yaml")
		EmptyMachineSetFixture            = filepath.Join("machineconfigurations", "managedbootimages-empty.yaml")

		oc = exutil.NewCLI("mco-bootimage", exutil.KubeConfigPath()).AsAdmin()
	)

	g.BeforeEach(func() {
		// Skip this test if not on AWS platform
		skipUnlessTargetPlatform(oc, osconfigv1.AWSPlatformType)
		// Skip this test if the cluster is not using MachineAPI
		skipUnlessFunctionalMachineAPI(oc)
		// Skip this test on single node platforms
		skipOnSingleNodeTopology(oc)
	})

	g.AfterEach(func() {
		// Clear out boot image configuration between tests
		applyMachineConfigurationFixture(oc, EmptyMachineSetFixture)
	})

	// This test is [Disruptive] because it scales up a new control plane node after performing a boot image update, and the scales it down.
	g.It("[OCPFeatureGate:ManagedBootImagesCPMS][Disruptive] Should update boot images on ControlPlaneMachineSets and resize properly [apigroup:machineconfiguration.openshift.io]", func() {
		AllControlPlaneMachineSetTest(oc, AllControlPlaneMachineSetFixture)
	})

	g.It("[OCPFeatureGate:ManagedBootImagesCPMS] Should not update boot images on ControlPlaneMachineSets when not configured [apigroup:machineconfiguration.openshift.io]", func() {
		NoneControlPlaneMachineSetTest(oc, NoneControlPlaneMachineSetFixture)
	})
})
