package extended

import (
	"fmt"
	"path/filepath"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	machineconfigclient "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	exutil "github.com/openshift/machine-config-operator/test/extended/util"
	logger "github.com/openshift/machine-config-operator/test/extended/util/logext"
)

var (
	mcNameToFixtureMap = map[string]string{
		"90-infra-extension": filepath.Join("machineconfigs", "infra-extension-mc.yaml"),
		"90-infra-testfile":  filepath.Join("machineconfigs", "infra-testfile-mc.yaml"),
	}
)

// These tests verify ImageModeStatusReporting feature gate functionality.
// They are [Serial] and [Disruptive] because they modify cluster state and interact with custom MCPs.
var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/disruptive][Serial][Disruptive][OCPFeatureGate:ImageModeStatusReporting]", g.Ordered, func() {
	defer g.GinkgoRecover()

	var (
		oc = exutil.NewCLI("mco-image-mode-status", exutil.KubeConfigPath()).AsAdmin()
	)

	g.JustBeforeEach(func() {
		preChecks(oc)
		skipTestIfOCBIsEnabled(oc)
	})

	g.It("MachineConfigNode properties should match the associated node properties when OCB is enabled in a custom MCP [apigroup:machineconfiguration.openshift.io]", func() {
		// Create machine config client for test
		machineConfigClient, clientErr := machineconfigclient.NewForConfig(oc.KubeFramework().ClientConfig())
		o.Expect(clientErr).NotTo(o.HaveOccurred(), "Error creating client set for test: %s", clientErr)

		// Skip this test if there are no machines in the worker MCP, as this will mean we cannot
		// create a custom MCP.
		if !DoesMachineConfigPoolHaveMachinesOriginPort(machineConfigClient, "worker") {
			g.Skip(fmt.Sprintf("Skipping this test since the cluster does not have nodes in the `worker` MCP."))
		}

		// Run the standard image mode MCN test for a custom MCP named `infra` with no MC to apply
		runImageModeMCNTest(oc, machineConfigClient, "infra", "", false)
	})

	g.It("MachineConfigNode conditions should properly transition on an image based update when OCB is enabled in a custom MCP [apigroup:machineconfiguration.openshift.io]", func() {
		// Create machine config client for test
		machineConfigClient, clientErr := machineconfigclient.NewForConfig(oc.KubeFramework().ClientConfig())
		o.Expect(clientErr).NotTo(o.HaveOccurred(), "Error creating client set for test: %s", clientErr)

		// Skip this test if there are no machines in the worker MCP, as this will mean we cannot
		// create a custom MCP.
		if !DoesMachineConfigPoolHaveMachinesOriginPort(machineConfigClient, "worker") {
			g.Skip(fmt.Sprintf("Skipping this test since the cluster does not have nodes in the `worker` MCP."))
		}

		// Run the standard image mode MCN test for a custom MCP named `infra` with no MC to apply
		runImageModeMCNTest(oc, machineConfigClient, "infra", "90-infra-extension", true)
	})

	g.It("MachineConfigNode conditions should properly transition on a non-image based update when OCB is enabled in a custom MCP [apigroup:machineconfiguration.openshift.io]", func() {
		// Create machine config client for test
		machineConfigClient, clientErr := machineconfigclient.NewForConfig(oc.KubeFramework().ClientConfig())
		o.Expect(clientErr).NotTo(o.HaveOccurred(), "Error creating client set for test: %s", clientErr)

		// Skip this test if there are no machines in the worker MCP, as this will mean we cannot
		// create a custom MCP.
		if !DoesMachineConfigPoolHaveMachinesOriginPort(machineConfigClient, "worker") {
			g.Skip(fmt.Sprintf("Skipping this test since the cluster does not have nodes in the `worker` MCP."))
		}

		// Run the standard image mode MCN test for a custom MCP named `infra` with no MC to apply
		runImageModeMCNTest(oc, machineConfigClient, "infra", "90-infra-testfile", false)
	})
})

// `runImageModeMCNTest` runs through the general flow of validating the MCN of a node for image
// mode enabled workflows. The steps for the test are as follows:
//  1. Select a worker node to use throughout the test
//  2. Validate the starting properties of te MCN associated with the test node
//  3. Create a custom MCP named the value of `mcpAndMoscName` and add the test node to it
//  4. Validate the properties of the MCN associated with the test node
//  5. Configure on cluster image mode in the custom MCP & validate the MOSC applied successfully
//  6. Validate the properties of the MCN associated with the test node
//  7. If a MachineConfig has been provided, apply it, validate the MCN conditions trasnition
//     throughout the update, then remove the MC
//  8. Disable on cluster image mode in the custom MCP & validate the MOSC removal was successful
//  9. Validate the properties of the MCN associated with the test node
//  10. Remove the custom MCP
//  11. Validate the properties of the MCN associated with the test node
func runImageModeMCNTest(oc *exutil.CLI, machineConfigClient *machineconfigclient.Clientset, mcpAndMoscName, mcName string, isImageUpdate bool) {
	exutil.By("Select a node to follow in this test")
	workerNodes, err := GetNodesByRoleOriginPort(oc, "worker")
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting worker nodes: %s", err)
	o.Expect(len(workerNodes)).To(o.BeNumerically(">=", 1), "Less than one worker node in pool")
	nodeToTestName := workerNodes[0].Name
	logger.Infof("Using `%s` as node for test", nodeToTestName)
	logger.Infof("OK!\n")

	exutil.By("Validate node's starting MCN properties")
	err = ValidateMCNForNodeOriginPort(oc, machineConfigClient, nodeToTestName, "worker")
	o.Expect(err).NotTo(o.HaveOccurred(), "Error validating MCN for node `%s`: %s", nodeToTestName, err)
	logger.Infof("OK!\n")

	exutil.By("Create custom `infra` MCP and add the test node to it")
	// Create MCP
	infraMcp, err := CreateCustomMCP(oc.AsAdmin(), mcpAndMoscName, 0)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a new custom pool `%s`: %s", mcpAndMoscName, err)
	defer CleanupCustomMCPOriginPort(oc, machineConfigClient, mcpAndMoscName, nodeToTestName)
	// Label node
	err = oc.AsAdmin().Run("label").Args(fmt.Sprintf("node/%s", nodeToTestName), fmt.Sprintf("node-role.kubernetes.io/%s=", mcpAndMoscName)).Execute()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error labeing node `%s` for MCP `%s`: %s", nodeToTestName, mcpAndMoscName, err)
	// Wait for the new `infra` MCP to be ready
	WaitForMCPToBeReadyOriginPort(machineConfigClient, mcpAndMoscName, 1, "")
	logger.Infof("OK!\n")

	exutil.By("Validate node's custom MCP MCN properties")
	err = ValidateMCNForNodeOriginPort(oc, machineConfigClient, nodeToTestName, mcpAndMoscName)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error validating MCN for node `%s`: %s", nodeToTestName, err)
	logger.Infof("OK!\n")

	exutil.By("Configure OCB functionality for the new `infra` MCP")
	mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, mcpAndMoscName, nil)
	defer mosc.CleanupAndDelete()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource: %s", err)
	logger.Infof("OK!\n")

	exutil.By("Validating the `infra` MOSC applied successfully")
	ValidateSuccessfulMOSC(mosc, nil)
	logger.Infof("OK!\n")

	exutil.By("Validate the node in `infra` MCP has correct MCN properties")
	err = ValidateMCNForNodeOriginPort(oc, machineConfigClient, nodeToTestName, mcpAndMoscName)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error validating MCN for node `%s`: %s", nodeToTestName, err)
	logger.Infof("OK!\n")

	// If an MC has been provided, apply it and validate the MCN conditions transition correctly
	// throughout the update.
	if mcName != "" {
		exutil.By("Applying the MC")
		err = ApplyMachineConfigFixtureOriginPort(oc, mcNameToFixtureMap[mcName])
		o.Expect(err).NotTo(o.HaveOccurred(), "Error applying MC `%s`: %s", mcName, err)
		defer DeleteMCAndWaitForMCPUpdateOriginPort(oc, machineConfigClient, mcName, mcpAndMoscName)
		logger.Infof("OK!\n")

		exutil.By("Validating the MCN condition transitions")
		validateMCNTransitions(machineConfigClient, nodeToTestName, isImageUpdate)
		logger.Infof("OK!\n")

		exutil.By("Removing the MC")
		DeleteMCAndWaitForMCPUpdateOriginPort(oc, machineConfigClient, mcName, mcpAndMoscName)
		logger.Infof("OK!\n")
	}

	exutil.By("Remove the MachineOSConfig resource")
	o.Expect(DisableOCL(mosc)).To(o.Succeed(), "Error cleaning up MOSC `%s`: %s", mosc, err)
	logger.Infof("OK!\n")

	exutil.By("Validating the `infra` MOSC was removed successfully")
	ValidateMOSCIsGarbageCollected(mosc, infraMcp)
	logger.Infof("OK!\n")

	exutil.By("Validate the node in `infra` MCP has correct MCN properties")
	err = ValidateMCNForNodeOriginPort(oc, machineConfigClient, nodeToTestName, mcpAndMoscName)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error validating MCN for node `%s`: %s", nodeToTestName, err)
	logger.Infof("OK!\n")

	exutil.By("Delete the `infra` MCP")
	err = CleanupCustomMCPOriginPort(oc, machineConfigClient, mcpAndMoscName, nodeToTestName)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error deleting `%s` MCP: %s", mcpAndMoscName, err)
	logger.Infof("OK!\n")

	exutil.By("Validate the test node has correct MCN properties")
	err = ValidateMCNForNodeOriginPort(oc, machineConfigClient, nodeToTestName, "worker")
	o.Expect(err).NotTo(o.HaveOccurred(), "Error validating MCN for node `%s`: %s", nodeToTestName, err)
	logger.Infof("OK!\n")
}

// `validateMCNTransitions` applies a MC, validates that the MCN conditions properly
// transition during the update, removes the MC, then validates that the MCN conditions properly
// transition during the update.
func validateMCNTransitions(machineConfigClient *machineconfigclient.Clientset, nodeToTestName string, isImageUpdate bool) {
	// Validate transition through conditions for MCN
	exutil.By("Validating the transitions through MCN conditions")
	ValidateTransitionThroughConditionsOriginPort(machineConfigClient, nodeToTestName, false, isImageUpdate)
	logger.Infof("OK!\n")

	// When an update is complete, all conditions other than `Updated` must be false
	logger.Infof("Checking all conditions other than 'Updated' are False.")
	o.Expect(ConfirmUpdatedMCNStatusOriginPort(machineConfigClient, nodeToTestName)).Should(o.BeTrue(), "Error, all conditions must be 'False' when Updated=True.")
	logger.Infof("OK!\n")

	return
}
