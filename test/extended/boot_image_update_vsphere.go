package extended

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	osconfigv1 "github.com/openshift/api/config/v1"
	machineclient "github.com/openshift/client-go/machine/clientset/versioned"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"

	streammeta "github.com/coreos/stream-metadata-go/stream"
	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/kubernetes/test/e2e/framework"
)

// This test is [Serial] because it modifies the cluster/machineconfigurations.operator.openshift.io object in each test.
var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/disruptive][Serial][OCPFeatureGate:ManagedBootImagesvSphere]", g.Label("Platform:vsphere"), g.Ordered, func() {
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
		// Skip this test if not on vSphere platform
		skipUnlessTargetPlatform(oc, osconfigv1.VSpherePlatformType)
		// Skip this test if the cluster is not using MachineAPI
		skipUnlessFunctionalMachineAPI(oc)
		// Skip this test on single node platforms
		exutil.SkipOnSingleNodeTopology(oc)
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

	g.It("Should upload the latest bootimage to the appropriate vCentre [apigroup:machineconfiguration.openshift.io]", func() {
		UploadTovCentreTest(oc, PartialMachineSetFixture)
	})
})

func UploadTovCentreTest(oc *exutil.CLI, fixture string) {

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

	// Modify coreos-bootimage cm to an older known version (transient application since CVO should revert immediately)
	cm, err := oc.AdminKubeClient().CoreV1().ConfigMaps(MachineConfigNamespace).Get(context.TODO(), GoldenBootImagesConfigMap, metav1.GetOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())
	var stream streammeta.Stream
	err = json.Unmarshal([]byte(cm.Data["stream"]), &stream)
	o.Expect(err).NotTo(o.HaveOccurred())
	vmware := stream.Architectures["x86_64"].Artifacts["vmware"]
	currentVmwareRelease := vmware.Release
	vmware.Release = "418.94.202501221327-0"
	vmware.Formats["ova"].Disk.Location = "https://rhcos.mirror.openshift.com/art/storage/prod/streams/4.18-9.4/builds/418.94.202501221327-0/x86_64/rhcos-418.94.202501221327-0-vmware.x86_64.ova"
	vmware.Formats["ova"].Disk.Sha256 = "6fd6e9fa2ff949154d54572c3f6a0c400f3e801457aa88b585b751a0955bda19"
	stream.Architectures["x86_64"].Artifacts["vmware"] = vmware
	updatedStreamBytes, err := json.MarshalIndent(stream, "", "  ")
	o.Expect(err).NotTo(o.HaveOccurred())
	escapedStreamBytes, err := json.Marshal(string(updatedStreamBytes))
	o.Expect(err).NotTo(o.HaveOccurred())
	patchPayload := fmt.Sprintf(`{"data":{"stream":%s}}`, string(escapedStreamBytes))
	err = oc.Run("patch").Args("configmap", GoldenBootImagesConfigMap, "-n", MachineConfigNamespace, "-p", patchPayload).Execute()
	o.Expect(err).NotTo(o.HaveOccurred())

	// Ensure boot image controller is not progressing
	framework.Logf("Waiting until the boot image controller is not progressing...")
	waitForBootImageControllerToComplete(oc)

	// Ensure MSBIC moves successfully from current bootimage -> known old bootimage -> current bootimage
	currentToOldLog := fmt.Sprintf("Existing RHCOS v%s does not match current RHCOS v%s. Starting reconciliation process.", currentVmwareRelease, vmware.Release)
	oldToCurrentLog := fmt.Sprintf("Existing RHCOS v%s does not match current RHCOS v%s. Starting reconciliation process.", vmware.Release, currentVmwareRelease)
	successfullyPatchedLog := fmt.Sprintf("Successfully patched machineset %s", machineSetUnderTest.Name)
	o.Eventually(func() bool {
		podNames, err := oc.Run("get").Args(
			"pods",
			"-n", MachineConfigNamespace,
			"-l", "k8s-app=machine-config-controller",
			"-o", "go-template={{range .items}}{{.metadata.name}}{{\"\\n\"}}{{end}}",
		).Output()
		if err != nil {
			return false
		}
		podNamesArr := strings.Split(podNames, "\n")
		if len(podNamesArr) == 0 {
			return false
		}
		mccPodName := podNamesArr[0]
		logs, err := oc.Run("logs").Args(mccPodName, "-n", MachineConfigNamespace, "--tail=50").Output()
		if err != nil {
			return false
		}
		return exutil.LogsContainInOrder(logs, currentToOldLog, successfullyPatchedLog, oldToCurrentLog, successfullyPatchedLog)
	}, 15*time.Minute, 10*time.Second).Should(o.BeTrue())

	// Scale-up the machineset to verify we have not accidentally broken the bootimage updates
	mcdPods, err := exutil.GetAllPodsWithLabel(oc, MachineConfigNamespace, "k8s-app=machine-config-daemon")
	o.Expect(err).NotTo(o.HaveOccurred())
	mcdPodsBefore := sets.New(mcdPods...)
	err = oc.Run("scale").Args(MAPIMachinesetQualifiedName, machineSetUnderTest.Name, "-n", MAPINamespace, fmt.Sprintf("--replicas=%d", *machineSetUnderTest.Spec.Replicas+1)).Execute()
	o.Expect(err).NotTo(o.HaveOccurred())

	defer func() {
		// Scale-down the machineset at the end of test
		err = oc.Run("scale").Args(MAPIMachinesetQualifiedName, machineSetUnderTest.Name, "-n", MAPINamespace, fmt.Sprintf("--replicas=%d", *machineSetUnderTest.Spec.Replicas)).Execute()
		o.Expect(err).NotTo(o.HaveOccurred())
	}()

	o.Eventually(func() bool {
		machineset, err := machineClient.MachineV1beta1().MachineSets(MAPINamespace).Get(context.TODO(), machineSetUnderTest.Name, metav1.GetOptions{})
		if err != nil {
			return false
		}
		return machineset.Status.AvailableReplicas == *machineSetUnderTest.Spec.Replicas+1
	}, 15*time.Minute, 10*time.Second).Should(o.BeTrue())

	o.Eventually(func() bool {
		updatedMcdPods, err := exutil.GetAllPodsWithLabel(oc, MachineConfigNamespace, "k8s-app=machine-config-daemon")
		if err != nil {
			return false
		}
		newPods := sets.New(updatedMcdPods...).Difference(mcdPodsBefore)
		if newPods.Len() == 0 {
			return false
		}
		for _, pod := range newPods.UnsortedList() {
			logs, err := oc.Run("logs").Args(pod, "-n", MachineConfigNamespace).Output()
			if err != nil {
				continue
			}
			lines := strings.Split(logs, "\n")
			if len(lines) > 50 {
				lines = lines[:50]
			}
			if strings.Contains(strings.Join(lines, "\n"), currentVmwareRelease) {
				return true
			}
		}
		return false
	}, 15*time.Minute, 10*time.Second).Should(o.BeTrue())
}


