package extended

import (
	"context"
	"time"

	machineclient "github.com/openshift/client-go/machine/clientset/versioned"
	exutil "github.com/openshift/machine-config-operator/test/extended/util"

	o "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/kubernetes/test/e2e/framework"

	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
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

func AllControlPlaneMachineSetTest(oc *exutil.CLI, fixture string) {
	// This fixture applies a boot image update configuration that opts in all controlplanemachinesets
	// However, since CPMS is typically a singleton, it is just targeting a single resource
	applyMachineConfigurationFixture(oc, fixture)

	// Grab the CPMS and verify that the boot image was reconciled correctly.
	machineClient, err := machineclient.NewForConfig(oc.KubeFramework().ClientConfig())
	o.Expect(err).NotTo(o.HaveOccurred())

	cpms, err := machineClient.MachineV1().ControlPlaneMachineSets("openshift-machine-api").Get(context.TODO(), "cluster", metav1.GetOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())
	verifyControlPlaneMachineSetUpdate(oc, *cpms, true)

	// Delete a control plane machine to verify that CPMS reconciles it with the updated boot image
	// Get the list of control plane machines
	machines, err := machineClient.MachineV1beta1().Machines(MAPINamespace).List(context.TODO(), metav1.ListOptions{LabelSelector: MAPIMasterMachineLabelSelector})
	o.Expect(err).NotTo(o.HaveOccurred())
	o.Expect(machines.Items).NotTo(o.BeEmpty(), "No control plane machines found")

	// Capture the initial set of control plane machine names and count before deletion
	initialMachineNames := sets.New[string]()
	for _, machine := range machines.Items {
		initialMachineNames.Insert(machine.Name)
	}
	initialMachineCount := initialMachineNames.Len()

	// Delete the first control plane machine
	machineToDelete := machines.Items[0].Name
	framework.Logf("Deleting control plane machine: %s", machineToDelete)
	err = machineClient.MachineV1beta1().Machines(MAPINamespace).Delete(context.TODO(), machineToDelete, metav1.DeleteOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())

	// Wait until the new control plane machine is running and the old one is deleted
	// Arbitrarily picking 25 minutes timeout as scale-up time varies based on platform
	framework.Logf("Waiting for CPMS to reconcile and create a new control plane machine (up to 25 minutes)...")
	o.Eventually(func() bool {
		currentMachines, err := machineClient.MachineV1beta1().Machines(MAPINamespace).List(context.TODO(), metav1.ListOptions{LabelSelector: MAPIMasterMachineLabelSelector})
		if err != nil {
			framework.Logf("Error listing machines: %v", err)
			return false
		}

		// Check that the deleted control plane machine is gone and all current machines are running
		currentMachineNames := sets.New[string]()
		runningMachines := sets.New[string]()

		for _, machine := range currentMachines.Items {
			currentMachineNames.Insert(machine.Name)
			phase := ""
			if machine.Status.Phase != nil {
				phase = *machine.Status.Phase
			}
			if phase == machinev1beta1.PhaseRunning {
				runningMachines.Insert(machine.Name)
			} else {
				framework.Logf("Machine %s is in phase: %s", machine.Name, phase)
			}
		}

		// All machines must be running
		if runningMachines.Len() != initialMachineCount {
			framework.Logf("Only %d out of %d machines are running", runningMachines.Len(), initialMachineCount)
			return false
		}

		// The deleted machine should not be in the current set
		if currentMachineNames.Has(machineToDelete) {
			framework.Logf("Deleted machine %s still exists", machineToDelete)
			return false
		}

		framework.Logf("All %d control plane machines are running and the deleted machine is gone", initialMachineCount)

		// Ensure master MCP is done updating and has the correct ready count
		masterMCP := NewMachineConfigPool(oc, MachineConfigPoolMaster)
		updatedStatus, err := masterMCP.GetUpdatedStatus()
		if err != nil {
			framework.Logf("Error getting master MCP updated status: %v", err)
			return false
		}
		if updatedStatus != TrueString {
			framework.Logf("Master MCP is not yet updated (Updated=%s)", updatedStatus)
			return false
		}

		readyMachineCount, err := masterMCP.getUpdatedMachineCount()
		if err != nil {
			framework.Logf("Error getting master MCP ready machine count: %v", err)
			return false
		}
		if readyMachineCount != initialMachineCount {
			framework.Logf("Master MCP ready machine count %d does not match initial count %d", readyMachineCount, initialMachineCount)
			return false
		}

		framework.Logf("Master MCP is updated with %d ready machines", readyMachineCount)
		return true
	}, 25*time.Minute, 2*time.Minute).Should(o.BeTrue(), "CPMS failed to reconcile control plane machines within 25 minutes")
}

func NoneControlPlaneMachineSetTest(oc *exutil.CLI, fixture string) {
	// This fixture applies a boot image update configuration that opts in no controlplanemachineset, i.e. feature is disabled.
	applyMachineConfigurationFixture(oc, fixture)

	// Grab the CPMS and verify that the boot image was reconciled correctly.
	machineClient, err := machineclient.NewForConfig(oc.KubeFramework().ClientConfig())
	o.Expect(err).NotTo(o.HaveOccurred())

	cpms, err := machineClient.MachineV1().ControlPlaneMachineSets("openshift-machine-api").Get(context.TODO(), "cluster", metav1.GetOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())

	verifyControlPlaneMachineSetUpdate(oc, *cpms, false)
}
