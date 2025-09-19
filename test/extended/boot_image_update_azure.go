package extended

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	osconfigv1 "github.com/openshift/api/config/v1"
	machineclient "github.com/openshift/client-go/machine/clientset/versioned"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended/util"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/test/e2e/framework"
)

// These tests are [Serial] because it modifies the cluster/machineconfigurations.operator.openshift.io object in each test.
var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/disruptive][Serial][OCPFeatureGate:ManagedBootImagesAzure]", g.Ordered, func() {
	defer g.GinkgoRecover()
	var (
		AllMachineSetFixture     = filepath.Join("machineconfigurations", "managedbootimages-all.yaml")
		NoneMachineSetFixture    = filepath.Join("machineconfigurations", "managedbootimages-none.yaml")
		PartialMachineSetFixture = filepath.Join("machineconfigurations", "managedbootimages-partial.yaml")
		EmptyMachineSetFixture   = filepath.Join("machineconfigurations", "managedbootimages-empty.yaml")

		oc = exutil.NewCLI("mco-bootimage", exutil.KubeConfigPath()).AsAdmin()
	)

	g.BeforeEach(func() {
		// Skip this test if not on Azure platform
		skipUnlessTargetPlatform(oc, osconfigv1.AzurePlatformType)
		// Skip this test if the cluster is not using MachineAPI
		skipUnlessFunctionalMachineAPI(oc)
		// Skip this test on single node platforms
		skipOnSingleNodeTopology(oc)
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

	// This test is [Disruptive] because it scales up a new worker node after performing a boot image update, and the scales it down.
	g.It("[Disruptive] Should update boot images on an Azure MachineSets with a legacy boot image and scale successfully [apigroup:machineconfiguration.openshift.io]", func() {
		AzureLegacyBootImageTest(oc, PartialMachineSetFixture)
	})
})

func AzureLegacyBootImageTest(oc *exutil.CLI, fixture string) {

	// This fixture applies a boot image update configuration that opts in any machineset with the label test=boot
	applyMachineConfigurationFixture(oc, fixture)

	// Pick a random machineset to test
	machineClient, err := machineclient.NewForConfig(oc.KubeFramework().ClientConfig())
	o.Expect(err).NotTo(o.HaveOccurred())
	machineSetUnderTest := getRandomMachineSet(machineClient)
	framework.Logf("MachineSet under test: %s", machineSetUnderTest.Name)

	// Label this machineset with the test=boot label
	err = oc.Run("label").Args(MAPIMachinesetQualifiedName, machineSetUnderTest.Name, "-n", MAPINamespace, "test=boot").Execute()
	o.Expect(err).NotTo(o.HaveOccurred())
	defer func() {
		// Unlabel the machineset at the end of test
		err = oc.Run("label").Args(MAPIMachinesetQualifiedName, machineSetUnderTest.Name, "-n", MAPINamespace, "test-").Execute()
		o.Expect(err).NotTo(o.HaveOccurred())
	}()

	// Set machineset under test to a legacy boot image
	newProviderSpecPatch, originalProviderSpecPatch, legacyBootImage, originalBootImage := generateLegacyAzureProviderSpecPatch(machineSetUnderTest)
	err = oc.Run("patch").Args(MAPIMachinesetQualifiedName, machineSetUnderTest.Name, "-p", newProviderSpecPatch, "-n", MAPINamespace, "--type=json").Execute()
	o.Expect(err).NotTo(o.HaveOccurred())

	defer func() {
		// Restore machineSet to original boot image as the machineset may be used by other test variants, regardless of success/fail
		err = oc.Run("patch").Args(MAPIMachinesetQualifiedName, machineSetUnderTest.Name, "-p", originalProviderSpecPatch, "-n", MAPINamespace, "--type=json").Execute()
		o.Expect(err).NotTo(o.HaveOccurred())
		framework.Logf("Restored build name in the machineset %s to \"%s\"", machineSetUnderTest.Name, originalBootImage)
	}()
	// Ensure boot image controller is not progressing
	framework.Logf("Waiting until the boot image controller is not progressing...")
	waitForBootImageControllerToComplete(oc)

	// Fetch the providerSpec of the machineset under test again
	providerSpec, err := oc.Run("get").Args(MAPIMachinesetQualifiedName, machineSetUnderTest.Name, "-o", "template", "--template=`{{.spec.template.spec.providerSpec.value}}`", "-n", MAPINamespace).Output()
	o.Expect(err).NotTo(o.HaveOccurred())

	// Verify that the machineset does not have the legacy boot image
	o.Expect(providerSpec).ShouldNot(o.ContainSubstring(legacyBootImage))

	// Get current set of ready nodes
	nodes, err := getReadyNodes(oc)
	o.Expect(err).NotTo(o.HaveOccurred())

	// Scale up machineset under test
	err = oc.Run("scale").Args(MAPIMachinesetQualifiedName, machineSetUnderTest.Name, "-n", MAPINamespace, fmt.Sprintf("--replicas=%d", *machineSetUnderTest.Spec.Replicas+1)).Execute()
	o.Expect(err).NotTo(o.HaveOccurred())

	framework.Logf("Waiting for scale-up to complete...")
	// Scale up a new node
	o.Eventually(func() bool {
		machineset, err := machineClient.MachineV1beta1().MachineSets(MAPINamespace).Get(context.TODO(), machineSetUnderTest.Name, metav1.GetOptions{})
		if err != nil {
			framework.Logf("%v", err)
			return false
		}
		return machineset.Status.AvailableReplicas == *machineSetUnderTest.Spec.Replicas+1
	}, 15*time.Minute, 10*time.Second).Should(o.BeTrue())

	defer func() {
		// Scale-down the machineset at the end of test
		err = oc.Run("scale").Args(MAPIMachinesetQualifiedName, machineSetUnderTest.Name, "-n", MAPINamespace, fmt.Sprintf("--replicas=%d", *machineSetUnderTest.Spec.Replicas)).Execute()
		o.Expect(err).NotTo(o.HaveOccurred())

		// Wait for scaledown to complete
		framework.Logf("Waiting for scale-down to complete...")
		o.Eventually(func() bool {
			machineset, err := machineClient.MachineV1beta1().MachineSets(MAPINamespace).Get(context.TODO(), machineSetUnderTest.Name, metav1.GetOptions{})
			if err != nil {
				framework.Logf("%v", err)
				return false
			}
			return machineset.Status.AvailableReplicas == *machineSetUnderTest.Spec.Replicas
		}, 15*time.Minute, 10*time.Second).Should(o.BeTrue())

	}()

	// Retrieve aleph version from new node
	var alephVersion string
	o.Eventually(func() bool {
		// Grab newly scaled up node by diffing against the old set of nodes
		newNodes, err := getReadyNodes(oc)
		if err != nil {
			return false
		}
		scaledUpNode := newNodes.Difference(nodes)
		scaledUpNodeName, scaledUpNodeReady := scaledUpNode.PopAny()
		if !scaledUpNodeReady {
			return false
		}

		// Log aleph version from the new node
		framework.Logf("Newly scaled up node: %v", scaledUpNodeName)
		alephVersion, err = getAlephVersionFromNode(oc, scaledUpNodeName)
		if err != nil {
			framework.Logf("Failed to get aleph version from node %s: %v", scaledUpNodeName, err)
			return false
		}

		framework.Logf("CoreOS aleph version from node %s: %s", scaledUpNodeName, alephVersion)
		return true
	}, 3*time.Minute, 3*time.Second).Should(o.BeTrue())

	// Get the current release boot image for this architecture; this should match the
	// aleph version above
	arch, err := getArchFromMachineSet(&machineSetUnderTest)
	o.Expect(err).NotTo(o.HaveOccurred())

	releaseBootImageVersion, err := getReleaseBootImageVersion(oc, arch)
	o.Expect(err).NotTo(o.HaveOccurred())
	framework.Logf("Current release's boot image version: %s", releaseBootImageVersion)

	// TODO: Uncomment when https://issues.redhat.com/browse/CORS-3915 lands, so that drifts between
	// rhcos.json & rhcos-marketplace.json are resolved.
	// For now, a successful scaleup can be considered as a success.
	// o.Expect(alephVersion).To(o.Equal(releaseBootImageVersion))
}
