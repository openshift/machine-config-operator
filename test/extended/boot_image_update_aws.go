package extended

import (
	"path/filepath"

	osconfigv1 "github.com/openshift/api/config/v1"

	g "github.com/onsi/ginkgo/v2"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
)

// These tests are [Serial] because they all modify the cluster/machineconfigurations.operator.openshift.io object.
var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/disruptive][Serial][OCPFeatureGate:ManagedBootImagesAWS]", g.Label("Platform:aws"), g.Ordered, func() {
	defer g.GinkgoRecover()
	var (
		AllMachineSetFixture           = filepath.Join("machineconfigurations", "managedbootimages-all.yaml")
		NoneMachineSetFixture          = filepath.Join("machineconfigurations", "managedbootimages-none.yaml")
		PartialMachineSetFixture       = filepath.Join("machineconfigurations", "managedbootimages-partial.yaml")
		EmptyMachineSetFixture         = filepath.Join("machineconfigurations", "managedbootimages-empty.yaml")
		SkewEnforcementDisabledFixture = filepath.Join("machineconfigurations", "skewenforcement-disabled.yaml")

		oc = exutil.NewCLI("mco-bootimage", exutil.KubeConfigPath()).AsAdmin()
	)

	g.BeforeEach(func() {
		// Skip this test if not on AWS platform
		skipUnlessTargetPlatform(oc, osconfigv1.AWSPlatformType)
		// Skip this test if the cluster is not using MachineAPI
		skipUnlessFunctionalMachineAPI(oc)
		// Skip this test on single node platforms
		exutil.SkipOnSingleNodeTopology(oc)
		// Skip if any MachineSet carries an unsupported OS stream label
		exutil.SkipIfUnsupportedOSStreamLabel(oc)
		// Disable boot image skew enforcement
		applyMachineConfigurationFixture(oc, SkewEnforcementDisabledFixture)
	})

	g.AfterEach(func() {
		// Clear out boot image configuration between tests
		applyMachineConfigurationFixture(oc, EmptyMachineSetFixture)
	})

	g.It("Should update boot images only on MachineSets that are opted in [apigroup:machineconfiguration.openshift.io]", func() {
		PartialMachineSetTest(oc, PartialMachineSetFixture)
	})

	g.It("Should update boot images on all MachineSets when configured [apigroup:machineconfiguration.openshift.io]", func() {
		AllMachineSetTest(oc, AllMachineSetFixture)
	})

	g.It("Should not update boot images on any MachineSet when not configured [apigroup:machineconfiguration.openshift.io]", func() {
		NoneMachineSetTest(oc, NoneMachineSetFixture)
	})

	g.It("Should stamp coreos-bootimages configmap with current MCO hash and release version [apigroup:machineconfiguration.openshift.io]", func() {
		EnsureConfigMapStampTest(oc)
	})
})
