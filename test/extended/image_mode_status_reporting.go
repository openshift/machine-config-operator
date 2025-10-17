package extended

import (
	"fmt"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	machineconfigclient "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	exutil "github.com/openshift/machine-config-operator/test/extended/util"
	logger "github.com/openshift/machine-config-operator/test/extended/util/logext"
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
		if !DoesMachineConfigPoolHaveMachines(machineConfigClient, "worker") {
			g.Skip(fmt.Sprintf("Skipping this test since the cluster does not have nodes in the `worker` MCP."))
		}

		// Run the standard image mode MCN test for a custom MCP named `infra` with no MC to apply
		runImageModeMCNTest(oc, machineConfigClient, "infra")
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
//  7. Disable on cluster image mode in the custom MCP & validate the MOSC removal was successful
//  8. Validate the properties of the MCN associated with the test node
//  9. Remove the custom MCP
//  10. Validate the properties of the MCN associated with the test node
func runImageModeMCNTest(oc *exutil.CLI, machineConfigClient *machineconfigclient.Clientset, mcpAndMoscName string) {
	exutil.By("Select a node to follow in this test")
	workerNodes, err := GetNodesByRole(oc, "worker")
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting worker nodes: %s", err)
	o.Expect(len(workerNodes)).To(o.BeNumerically(">=", 1), "Less than one worker node in pool")
	nodeToTestName := workerNodes[0].Name
	logger.Infof("Using `%s` as node for test", nodeToTestName)
	logger.Infof("OK!\n")

	exutil.By("Validate node's starting MCN properties")
	err = ValidateMCNForNode(oc, machineConfigClient, nodeToTestName, "worker")
	o.Expect(err).NotTo(o.HaveOccurred(), "Error validating MCN for node `%s`: %s", nodeToTestName, err)
	logger.Infof("OK!\n")

	exutil.By("Create custom `infra` MCP and add the test node to it")
	// Create MCP
	infraMcp, err := CreateCustomMCP(oc.AsAdmin(), mcpAndMoscName, 0)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a new custom pool `%s`: %s", mcpAndMoscName, err)
	defer CleanupCustomMCP(oc, machineConfigClient, mcpAndMoscName, nodeToTestName)
	// Label node
	err = oc.AsAdmin().Run("label").Args(fmt.Sprintf("node/%s", nodeToTestName), fmt.Sprintf("node-role.kubernetes.io/%s=", mcpAndMoscName)).Execute()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error labeing node `%s` for MCP `%s`: %s", nodeToTestName, mcpAndMoscName, err)
	// Wait for the new `infra` MCP to be ready
	WaitForMCPToBeReady(oc, machineConfigClient, mcpAndMoscName, 1)
	logger.Infof("OK!\n")

	exutil.By("Validate node's custom MCP MCN properties")
	err = ValidateMCNForNode(oc, machineConfigClient, nodeToTestName, mcpAndMoscName)
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
	err = ValidateMCNForNode(oc, machineConfigClient, nodeToTestName, mcpAndMoscName)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error validating MCN for node `%s`: %s", nodeToTestName, err)
	logger.Infof("OK!\n")

	exutil.By("Remove the MachineOSConfig resource")
	o.Expect(DisableOCL(mosc)).To(o.Succeed(), "Error cleaning up MOSC `%s`: %s", mosc, err)
	logger.Infof("OK!\n")

	exutil.By("Validating the `infra` MOSC was removed successfully")
	ValidateMOSCIsGarbageCollected(mosc, infraMcp)
	logger.Infof("OK!\n")

	exutil.By("Validate the node in `infra` MCP has correct MCN properties")
	err = ValidateMCNForNode(oc, machineConfigClient, nodeToTestName, mcpAndMoscName)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error validating MCN for node `%s`: %s", nodeToTestName, err)
	logger.Infof("OK!\n")

	exutil.By("Delete the `infra` MCP")
	err = CleanupCustomMCP(oc, machineConfigClient, mcpAndMoscName, nodeToTestName)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error deleting `%s` MCP: %s", mcpAndMoscName, err)
	logger.Infof("OK!\n")

	exutil.By("Validate the test node has correct MCN properties")
	err = ValidateMCNForNode(oc, machineConfigClient, nodeToTestName, "worker")
	o.Expect(err).NotTo(o.HaveOccurred(), "Error validating MCN for node `%s`: %s", nodeToTestName, err)
	logger.Infof("OK!\n")
}
