package extended

import (
	"fmt"

	exutil "github.com/openshift/machine-config-operator/test/extended/util"
	logger "github.com/openshift/machine-config-operator/test/extended/util/logext"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
)

// TODO:
//   - See if image mode is supported in master MCP --> probably just add a skip if no worker MCP
//   - Update tests to be more functioned out --> this will probably be the same for all MCN stuff, just with different MCs passed in
//   - Update test to use allowed MCPs; sort of same as #1
//   - Move some funcs to a helper file
var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/disruptive][Disruptive][OCPFeatureGate:ImageModeStatusReporting]", func() {
	defer g.GinkgoRecover()

	var (
		// InfraMCPFixture = filepath.Join("machineconfigpool", "infra.yaml")

		oc = exutil.NewCLI("mco-ocb", exutil.KubeConfigPath())
	)

	g.JustBeforeEach(func() {
		preChecks(oc)
		skipTestIfOCBIsEnabled(oc)
	})

	g.It("MachineConfigNode properties should match the associated node properties when OCB is enabled in a custom MCP", func() {
		// Create client set for the test
		clientSet := framework.NewClientSet("")

		// Skip this test for clusters that do not have nodes in the worker MCP, since that is
		// required to create the custom MCP used throughout this test.
		SkipOnNoNodesInDesiredMCP(oc, clientSet, "worker")

		// Run the standard image mode MCN test for a custom MCP named `infra` with no MC to apply
		runImageModeMCNTest(oc, clientSet, "infra")

		// Add a node to the custom MCP
		// Enable OCB in the custom MCP
		// Wait for OCB enablement to be complete
		// Check all props in MCN match with node
		//     - Check that `mcn.Spec.Pool.Name` matches provided `poolName`
		//     - Check that `mcn.Name` matches the node name
		//     - Check that `mcn.Spec.ConfigVersion.Desired` matches the node desired config version
		//     - Check that `nmcn.Status.ConfigVersion.Current` matches the node current config version
		//     - Check that `mcn.Status.ConfigVersion.Desired` matches the node desired config version
		//     - Check that `mcn.Spec.ConfigImage.DesiredImage` matches the node desired image annotation
		//     - Check that `nmcn.Status.ConfigImage.CurrentImage` matches the node current image annotation
		//     - Check that `mcn.Status.ConfigImage.DesiredImage` matches the node desired image annotation
		// Disable OCB in the custom MCP
		// Wait for OCB disable to be complete
		// Check all props in MCN match with node
		//     - Check that `mcn.Spec.Pool.Name` matches provided `poolName`
		//     - Check that `mcn.Name` matches the node name
		//     - Check that `mcn.Spec.ConfigVersion.Desired` matches the node desired config version
		//     - Check that `nmcn.Status.ConfigVersion.Current` matches the node current config version
		//     - Check that `mcn.Status.ConfigVersion.Desired` matches the node desired config version
		//     - Check that `mcn.Spec.ConfigImage.DesiredImage` matches the node desired image annotation
		//     - Check that `nmcn.Status.ConfigImage.CurrentImage` matches the node current image annotation
		//     - Check that `mcn.Status.ConfigImage.DesiredImage` matches the node desired image annotation
		// Remove node from custom MCP & remove MCP
	})

	// g.It("MachineConfigNode properties should properly transition through MCN conditions on layering update in custom MCP", func() {
	// 	// TODO: test this with extension update
	// 	// TODO: add skip for non-custom allowed env
	// 	// TODO: handle non-custom MCP case also for full coverage?
	// 	var (
	// 		mcpAndMoscName = "infra"
	// 		clientSet      = framework.NewClientSet("")
	// 	)

	// 	exutil.By("Select a node to follow in this test")
	// 	workerNodes, err := helpers.GetNodesByRole(clientSet, "worker")
	// 	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting worker nodes: %s", err)
	// 	o.Expect(len(workerNodes)).To(o.BeNumerically(">=", 1), "Less than one worker node in pool")
	// 	nodeToTest := workerNodes[0]
	// 	logger.Infof("Using `%s` as node for test")
	// 	logger.Infof("OK!\n")

	// 	exutil.By("Create custom `infra` MCP and add the test node to it")
	// 	// Create MCP
	// 	infraMcp, err := CreateCustomMCP(oc.AsAdmin(), mcpAndMoscName, 0)
	// 	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a new custom pool `%s`: %s", mcpAndMoscName, err)
	// 	// TODO: add MCP cleanup
	// 	// Label node
	// 	err = oc.Run("label").Args(fmt.Sprintf("node/%s", nodeToTest.Name), fmt.Sprintf("node-role.kubernetes.io/%s=", mcpAndMoscName)).Execute()
	// 	o.Expect(err).NotTo(o.HaveOccurred(), "Error labeing node `%s` for MCP `%s`: %s", nodeToTest.Name, mcpAndMoscName, err)
	// 	// Wait for the new `infra` MCP to be ready
	// 	WaitForMCPToBeReady(oc, clientSet, mcpAndMoscName, 1)
	// 	logger.Infof("OK!\n")

	// 	exutil.By("Configure OCB functionality for the new `infra` MCP")
	// 	mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, mcpAndMoscName, nil)
	// 	defer mosc.CleanupAndDelete()
	// 	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource: %s", err)
	// 	logger.Infof("OK!\n")

	// 	// TODO: add here the MCN transition expectations
	// 	// 	- Check desired and current config version remain the same
	// 	// 	- Check desired and current image update
	// 	// 	- Check all expected conditions transition
	// 	// 	- Validate ending state (current = desired & updated = true)

	// 	exutil.By("Validating the `infra` MOSC applied successfully")
	// 	ValidateSuccessfulMOSC(mosc, nil)
	// 	logger.Infof("OK!\n")

	// 	// TODO: add here the MC apply and MCN transition expectations
	// 	// 	- Check desired and current config version remain the same
	// 	// 	- Check desired and current image update
	// 	// 	- Check all expected conditions transition
	// 	// 	- Validate ending state (current = desired & updated = true)

	// 	exutil.By("Remove the MachineOSConfig resource")
	// 	o.Expect(DisableOCL(mosc)).To(o.Succeed(), "Error cleaning up MOSC `%s`: %s", mosc, err)
	// 	logger.Infof("OK!\n")

	// 	// TODO: add here the MCN transition expectations for OCB disable
	// 	// 	- Check desired and current config version remain the same
	// 	// 	- Check desired and current image update
	// 	// 	- Check all expected conditions transition
	// 	// 	- Validate ending state (current and desired are removed & updated = true)

	// 	exutil.By("Validating the `infra` MOSC was removed successfully")
	// 	ValidateMOSCIsGarbageCollected(mosc, infraMcp)
	// 	logger.Infof("OK!\n")

	// 	// Add a node to the custom MCP
	// 	// Enable OCB in the custom MCP
	// 	// Wait for OCB enablement to be complete
	// 	// Check all props in MCN match with node
	// 	//     - Check that `mcn.Spec.Pool.Name` matches provided `poolName`
	// 	//     - Check that `mcn.Name` matches the node name
	// 	//     - Check that `mcn.Spec.ConfigVersion.Desired` matches the node desired config version
	// 	//     - Check that `nmcn.Status.ConfigVersion.Current` matches the node current config version
	// 	//     - Check that `mcn.Status.ConfigVersion.Desired` matches the node desired config version
	// 	//     - Check that `mcn.Spec.ConfigImage.DesiredImage` matches the node desired image annotation
	// 	//     - Check that `nmcn.Status.ConfigImage.CurrentImage` matches the node current image annotation
	// 	//     - Check that `mcn.Status.ConfigImage.DesiredImage` matches the node desired image annotation
	// 	// Disable OCB in the custom MCP
	// 	// Wait for OCB disable to be complete
	// 	// Check all props in MCN match with node
	// 	//     - Check that `mcn.Spec.Pool.Name` matches provided `poolName`
	// 	//     - Check that `mcn.Name` matches the node name
	// 	//     - Check that `mcn.Spec.ConfigVersion.Desired` matches the node desired config version
	// 	//     - Check that `nmcn.Status.ConfigVersion.Current` matches the node current config version
	// 	//     - Check that `mcn.Status.ConfigVersion.Desired` matches the node desired config version
	// 	//     - Check that `mcn.Spec.ConfigImage.DesiredImage` matches the node desired image annotation
	// 	//     - Check that `nmcn.Status.ConfigImage.CurrentImage` matches the node current image annotation
	// 	//     - Check that `mcn.Status.ConfigImage.DesiredImage` matches the node desired image annotation
	// 	// Remove node from custom MCP & remove MCP
	// })

	// g.It("MachineConfigNode properties should properly transition through MCN conditions when OCB is enabled in a non-layering update in custom MCP", func() {
	// 	// TODO: add skip for non-custom allowed env
	// 	// TODO: handle non-custom MCP case also for full coverage?
	// 	var (
	// 		mcpAndMoscName = "infra"
	// 		clientSet      = framework.NewClientSet("")
	// 	)

	// 	exutil.By("Select a node to follow in this test")
	// 	workerNodes, err := helpers.GetNodesByRole(clientSet, "worker")
	// 	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting worker nodes: %s", err)
	// 	o.Expect(len(workerNodes)).To(o.BeNumerically(">=", 1), "Less than one worker node in pool")
	// 	nodeToTest := workerNodes[0]
	// 	logger.Infof("Using `%s` as node for test")
	// 	logger.Infof("OK!\n")

	// 	exutil.By("Create custom `infra` MCP and add the test node to it")
	// 	// Create MCP
	// 	infraMcp, err := CreateCustomMCP(oc.AsAdmin(), mcpAndMoscName, 0)
	// 	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a new custom pool `%s`: %s", mcpAndMoscName, err)
	// 	// TODO: add MCP cleanup
	// 	// Label node
	// 	err = oc.Run("label").Args(fmt.Sprintf("node/%s", nodeToTest.Name), fmt.Sprintf("node-role.kubernetes.io/%s=", mcpAndMoscName)).Execute()
	// 	o.Expect(err).NotTo(o.HaveOccurred(), "Error labeing node `%s` for MCP `%s`: %s", nodeToTest.Name, mcpAndMoscName, err)
	// 	// Wait for the new `infra` MCP to be ready
	// 	WaitForMCPToBeReady(oc, clientSet, mcpAndMoscName, 1)
	// 	logger.Infof("OK!\n")

	// 	exutil.By("Configure OCB functionality for the new `infra` MCP")
	// 	mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, mcpAndMoscName, nil)
	// 	defer mosc.CleanupAndDelete()
	// 	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource: %s", err)
	// 	logger.Infof("OK!\n")

	// 	// TODO: add here the MCN transition expectations
	// 	// 	- Check desired and current config version remain the same
	// 	// 	- Check desired and current image update
	// 	// 	- Check all expected conditions transition
	// 	// 	- Validate ending state (current = desired & updated = true)

	// 	exutil.By("Validating the `infra` MOSC applied successfully")
	// 	ValidateSuccessfulMOSC(mosc, nil)
	// 	logger.Infof("OK!\n")

	// 	// TODO: add here the MC apply and MCN transition expectations
	// 	// 	- Check desired and current config update
	// 	// 	- Check desired and current image version remain the same
	// 	// 	- Check all expected conditions transition
	// 	// 	- ex: image pull should not change
	// 	// 	- Validate ending state (current = desired & updated = true)

	// 	exutil.By("Remove the MachineOSConfig resource")
	// 	o.Expect(DisableOCL(mosc)).To(o.Succeed(), "Error cleaning up MOSC `%s`: %s", mosc, err)
	// 	logger.Infof("OK!\n")

	// 	// TODO: add here the MCN transition expectations for OCB disable
	// 	// 	- Check desired and current config version remain the same
	// 	// 	- Check desired and current image update
	// 	// 	- Check all expected conditions transition
	// 	// 	- Validate ending state (current and desired are removed & updated = true)

	// 	exutil.By("Validating the `infra` MOSC was removed successfully")
	// 	ValidateMOSCIsGarbageCollected(mosc, infraMcp)
	// 	logger.Infof("OK!\n")

	// 	// Add a node to the custom MCP
	// 	// Enable OCB in the custom MCP
	// 	// Wait for OCB enablement to be complete
	// 	// Check all props in MCN match with node
	// 	//     - Check that `mcn.Spec.Pool.Name` matches provided `poolName`
	// 	//     - Check that `mcn.Name` matches the node name
	// 	//     - Check that `mcn.Spec.ConfigVersion.Desired` matches the node desired config version
	// 	//     - Check that `nmcn.Status.ConfigVersion.Current` matches the node current config version
	// 	//     - Check that `mcn.Status.ConfigVersion.Desired` matches the node desired config version
	// 	//     - Check that `mcn.Spec.ConfigImage.DesiredImage` matches the node desired image annotation
	// 	//     - Check that `nmcn.Status.ConfigImage.CurrentImage` matches the node current image annotation
	// 	//     - Check that `mcn.Status.ConfigImage.DesiredImage` matches the node desired image annotation
	// 	// Disable OCB in the custom MCP
	// 	// Wait for OCB disable to be complete
	// 	// Check all props in MCN match with node
	// 	//     - Check that `mcn.Spec.Pool.Name` matches provided `poolName`
	// 	//     - Check that `mcn.Name` matches the node name
	// 	//     - Check that `mcn.Spec.ConfigVersion.Desired` matches the node desired config version
	// 	//     - Check that `nmcn.Status.ConfigVersion.Current` matches the node current config version
	// 	//     - Check that `mcn.Status.ConfigVersion.Desired` matches the node desired config version
	// 	//     - Check that `mcn.Spec.ConfigImage.DesiredImage` matches the node desired image annotation
	// 	//     - Check that `nmcn.Status.ConfigImage.CurrentImage` matches the node current image annotation
	// 	//     - Check that `mcn.Status.ConfigImage.DesiredImage` matches the node desired image annotation
	// 	// Remove node from custom MCP & remove MCP
	// })
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
func runImageModeMCNTest(oc *exutil.CLI, clientSet *framework.ClientSet, mcpAndMoscName string) {
	exutil.By("Select a node to follow in this test")
	workerNodes, err := helpers.GetNodesByRole(clientSet, "worker")
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting worker nodes: %s", err)
	o.Expect(len(workerNodes)).To(o.BeNumerically(">=", 1), "Less than one worker node in pool")
	nodeToTestName := workerNodes[0].Name
	logger.Infof("Using `%s` as node for test", nodeToTestName)
	logger.Infof("OK!\n")

	exutil.By("Validate node's starting MCN properties")
	err = ValidateMCNForNode(oc, clientSet, nodeToTestName, "worker")
	o.Expect(err).NotTo(o.HaveOccurred(), "Error validating MCN for node `%s`: %s", nodeToTestName, err)
	logger.Infof("OK!\n")

	exutil.By("Create custom `infra` MCP and add the test node to it")
	// Create MCP
	infraMcp, err := CreateCustomMCP(oc.AsAdmin(), mcpAndMoscName, 0)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a new custom pool `%s`: %s", mcpAndMoscName, err)
	// TODO: add MCP cleanup
	defer CleanupCustomMCP(oc, clientSet, mcpAndMoscName, nodeToTestName)
	// Label node
	err = oc.AsAdmin().Run("label").Args(fmt.Sprintf("node/%s", nodeToTestName), fmt.Sprintf("node-role.kubernetes.io/%s=", mcpAndMoscName)).Execute()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error labeing node `%s` for MCP `%s`: %s", nodeToTestName, mcpAndMoscName, err)
	// Wait for the new `infra` MCP to be ready
	WaitForMCPToBeReady(oc, clientSet, mcpAndMoscName, 1)
	logger.Infof("OK!\n")

	exutil.By("Validate node's custom MCP MCN properties")
	err = ValidateMCNForNode(oc, clientSet, nodeToTestName, mcpAndMoscName)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error validating MCN for node `%s`: %s", nodeToTestName, err)
	logger.Infof("OK!\n")

	exutil.By("Configure OCB functionality for the new `infra` MCP")
	mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, mcpAndMoscName, nil)
	// update to wait for MCP to be updated again
	defer mosc.CleanupAndDelete()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource: %s", err)
	logger.Infof("OK!\n")

	exutil.By("Validating the `infra` MOSC applied successfully")
	ValidateSuccessfulMOSC(mosc, nil)
	logger.Infof("OK!\n")

	exutil.By("Validate the node in `infra` MCP has correct MCN properties")
	err = ValidateMCNForNode(oc, clientSet, nodeToTestName, mcpAndMoscName)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error validating MCN for node `%s`: %s", nodeToTestName, err)
	logger.Infof("OK!\n")

	exutil.By("Remove the MachineOSConfig resource")
	o.Expect(DisableOCL(mosc)).To(o.Succeed(), "Error cleaning up MOSC `%s`: %s", mosc, err)
	logger.Infof("OK!\n")

	exutil.By("Validating the `infra` MOSC was removed successfully")
	ValidateMOSCIsGarbageCollected(mosc, infraMcp)
	logger.Infof("OK!\n")

	exutil.By("Validate the node in `infra` MCP has correct MCN properties")
	err = ValidateMCNForNode(oc, clientSet, nodeToTestName, mcpAndMoscName)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error validating MCN for node `%s`: %s", nodeToTestName, err)
	logger.Infof("OK!\n")

	exutil.By("Delete the `infra` MCP")
	err = CleanupCustomMCP(oc, clientSet, mcpAndMoscName, nodeToTestName)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error deleting `%s` MCP: %s", mcpAndMoscName, err)
	logger.Infof("OK!\n")

	exutil.By("Validate the test node has correct MCN properties")
	err = ValidateMCNForNode(oc, clientSet, nodeToTestName, "worker")
	o.Expect(err).NotTo(o.HaveOccurred(), "Error validating MCN for node `%s`: %s", nodeToTestName, err)
	logger.Infof("OK!\n")
}
