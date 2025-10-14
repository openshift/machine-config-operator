package extended

import (
	"context"
	"time"

	machineclient "github.com/openshift/client-go/machine/clientset/versioned"
	exutil "github.com/openshift/origin/test/extended/util"

	o "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/test/e2e/framework"
)

func AllMachineSetTest(oc *exutil.CLI, fixture string) {
	// This fixture applies a boot image update configuration that opts in all machinesets
	applyMachineConfigurationFixture(oc, fixture)

	// Step through all machinesets and verify boot images are reconciled correctly.
	machineClient, err := machineclient.NewForConfig(oc.KubeFramework().ClientConfig())
	o.Expect(err).NotTo(o.HaveOccurred())

	machineSets, err := machineClient.MachineV1beta1().MachineSets("openshift-machine-api").List(context.TODO(), metav1.ListOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())
	for _, ms := range machineSets.Items {
		verifyMachineSetUpdate(oc, ms, true)
	}
}

func PartialMachineSetTest(oc *exutil.CLI, fixture string) {

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
	// Step through all machinesets and verify that only the opted in machineset's boot images are reconciled.
	machineSets, err := machineClient.MachineV1beta1().MachineSets("openshift-machine-api").List(context.TODO(), metav1.ListOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())
	for _, ms := range machineSets.Items {
		verifyMachineSetUpdate(oc, ms, machineSetUnderTest.Name == ms.Name)
	}

}

func NoneMachineSetTest(oc *exutil.CLI, fixture string) {
	// This fixture applies a boot image update configuration that opts in no machinesets, i.e. feature is disabled.
	applyMachineConfigurationFixture(oc, fixture)

	// Step through all machinesets and verify boot images are reconciled correctly.
	machineClient, err := machineclient.NewForConfig(oc.KubeFramework().ClientConfig())
	o.Expect(err).NotTo(o.HaveOccurred())

	machineSets, err := machineClient.MachineV1beta1().MachineSets("openshift-machine-api").List(context.TODO(), metav1.ListOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())
	for _, ms := range machineSets.Items {
		verifyMachineSetUpdate(oc, ms, false)
	}
}

func EnsureConfigMapStampTest(oc *exutil.CLI) {
	// Update boot image configmap stamps with a "fake" value, wait for it to be updated back by the operator.
	err := oc.Run("patch").Args("configmap", GoldenBootImagesConfigMap, "-p", `{"data": {"MCOVersionHash": "fake-value"}}`, "-n", MachineConfigNamespace).Execute()
	o.Expect(err).NotTo(o.HaveOccurred())

	err = oc.Run("patch").Args("configmap", GoldenBootImagesConfigMap, "-p", `{"data": {"MCOReleaseImageVersion": "fake-value"}}`, "-n", MachineConfigNamespace).Execute()
	o.Expect(err).NotTo(o.HaveOccurred())

	// Ensure atleast one master node is ready
	waitForOneMasterNodeToBeReady(oc)

	// Verify that the configmap has been updated back to the correct value
	o.Eventually(func() bool {
		cm, err := oc.AdminKubeClient().CoreV1().ConfigMaps(MachineConfigNamespace).Get(context.TODO(), GoldenBootImagesConfigMap, metav1.GetOptions{})
		if err != nil {
			framework.Logf("failed to grab configmap, error :%v", err)
			return false
		}
		if cm.Data["MCOVersionHash"] == "fake-value" {
			framework.Logf("MCOVersionHash has not been restored to the original value")
			return false
		}
		if cm.Data["MCOReleaseImageVersion"] == "fake-value" {
			framework.Logf("MCOReleaseImageVersion has not been restored to the original value")
			return false
		}
		return true
	}, 2*time.Minute, 5*time.Second).Should(o.BeTrue())
	framework.Logf("Successfully verified that the configmap has been correctly stamped")
}
