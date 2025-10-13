package extended

import (
	"context"
	"fmt"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	exutil "github.com/openshift/machine-config-operator/test/extended/util"
	logger "github.com/openshift/machine-config-operator/test/extended/util/logext"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
		// TODO: add skip if env does not have worker nodes
	})

	g.It("MachineConfigNode properties should match the associated node properties when OCB is enabled in the default MachineConfigPool", func() {
		// TODO: check on the MCP cleanup defer func
		var (
			mcp            = GetCompactCompatiblePool(oc.AsAdmin())
			mcpAndMoscName = mcp.GetName()
			clientSet      = framework.NewClientSet("")
		)

		logger.Infof("Testing with `%s`MCP", mcpAndMoscName)

		exutil.By("Get nodes to track through test")
		nodesToTest, err := helpers.GetNodesByRole(clientSet, mcpAndMoscName)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting nodes to test: %s", err)
		o.Expect(len(nodesToTest)).To(o.BeNumerically(">=", 1), "Less than one node in pool")
		logger.Infof("Using %s `%s` nodes for test", len(nodesToTest), mcpAndMoscName)
		logger.Infof("OK!\n")

		exutil.By("Validate nodes' starting MCN properties")
		for _, node := range nodesToTest {
			err = ValidateMCNForNode(oc, clientSet, node.Name, mcpAndMoscName)
			o.Expect(err).NotTo(o.HaveOccurred(), "Error validating MCN for node `%s`: %s", node.Name, err)
		}
		logger.Infof("OK!\n")

		exutil.By("Configure OCB functionality")
		mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, mcpAndMoscName, nil)
		defer mosc.CleanupAndDelete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource: %s", err)
		logger.Infof("OK!\n")

		exutil.By("Validating the  MOSC applied successfully")
		ValidateSuccessfulMOSC(mosc, nil)
		logger.Infof("OK!\n")

		exutil.By("Validate nodes' MCN properties")
		for _, node := range nodesToTest {
			err = ValidateMCNForNode(oc, clientSet, node.Name, mcpAndMoscName)
			o.Expect(err).NotTo(o.HaveOccurred(), "Error validating MCN for node `%s`: %s", node.Name, err)
		}
		logger.Infof("OK!\n")

		exutil.By("Remove the MachineOSConfig resource")
		o.Expect(DisableOCL(mosc)).To(o.Succeed(), "Error cleaning up MOSC `%s`: %s", mosc, err)
		logger.Infof("OK!\n")

		exutil.By("Validating the MOSC was removed successfully")
		ValidateMOSCIsGarbageCollected(mosc, mcp)
		logger.Infof("OK!\n")

		exutil.By("Validate nodes' MCN properties")
		for _, node := range nodesToTest {
			err = ValidateMCNForNode(oc, clientSet, node.Name, mcpAndMoscName)
			o.Expect(err).NotTo(o.HaveOccurred(), "Error validating MCN for node `%s`: %s", node.Name, err)
		}
		logger.Infof("OK!\n")

		// Enable OCB in worker MCP
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
		// Disable OCB in worker MCP
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
	})

	g.It("MachineConfigNode properties should match the associated node properties when OCB is enabled in a custom MachineConfigPool", func() {
		// TODO: test this with extension update
		// TODO: add skip for non-custom allowed env
		// TODO: check on the MCP cleanup defer func
		var (
			mcpAndMoscName = "infra"
			clientSet      = framework.NewClientSet("")
		)

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
		// defer CleanupCustomMCP(oc, clientSet, mcpAndMoscName, nodeToTestName)
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

	g.It("MachineConfigNode properties should properly transition through MCN conditions on layering update in custom MCP", func() {
		// TODO: add skip for non-custom allowed env
		// TODO: handle non-custom MCP case also for full coverage?
		var (
			mcpAndMoscName = "infra"
			clientSet      = framework.NewClientSet("")
		)

		exutil.By("Select a node to follow in this test")
		workerNodes, err := helpers.GetNodesByRole(clientSet, "worker")
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting worker nodes: %s", err)
		o.Expect(len(workerNodes)).To(o.BeNumerically(">=", 1), "Less than one worker node in pool")
		nodeToTest := workerNodes[0]
		logger.Infof("Using `%s` as node for test")
		logger.Infof("OK!\n")

		exutil.By("Create custom `infra` MCP and add the test node to it")
		// Create MCP
		infraMcp, err := CreateCustomMCP(oc.AsAdmin(), mcpAndMoscName, 0)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a new custom pool `%s`: %s", mcpAndMoscName, err)
		// TODO: add MCP cleanup
		// Label node
		err = oc.Run("label").Args(fmt.Sprintf("node/%s", nodeToTest.Name), fmt.Sprintf("node-role.kubernetes.io/%s=", mcpAndMoscName)).Execute()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error labeing node `%s` for MCP `%s`: %s", nodeToTest.Name, mcpAndMoscName, err)
		// Wait for the new `infra` MCP to be ready
		WaitForMCPToBeReady(oc, clientSet, mcpAndMoscName, 1)
		logger.Infof("OK!\n")

		exutil.By("Configure OCB functionality for the new `infra` MCP")
		mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, mcpAndMoscName, nil)
		defer mosc.CleanupAndDelete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource: %s", err)
		logger.Infof("OK!\n")

		// TODO: add here the MCN transition expectations
		// 	- Check desired and current config version remain the same
		// 	- Check desired and current image update
		// 	- Check all expected conditions transition
		// 	- Validate ending state (current = desired & updated = true)

		exutil.By("Validating the `infra` MOSC applied successfully")
		ValidateSuccessfulMOSC(mosc, nil)
		logger.Infof("OK!\n")

		// TODO: add here the MC apply and MCN transition expectations
		// 	- Check desired and current config version remain the same
		// 	- Check desired and current image update
		// 	- Check all expected conditions transition
		// 	- Validate ending state (current = desired & updated = true)

		exutil.By("Remove the MachineOSConfig resource")
		o.Expect(DisableOCL(mosc)).To(o.Succeed(), "Error cleaning up MOSC `%s`: %s", mosc, err)
		logger.Infof("OK!\n")

		// TODO: add here the MCN transition expectations for OCB disable
		// 	- Check desired and current config version remain the same
		// 	- Check desired and current image update
		// 	- Check all expected conditions transition
		// 	- Validate ending state (current and desired are removed & updated = true)

		exutil.By("Validating the `infra` MOSC was removed successfully")
		ValidateMOSCIsGarbageCollected(mosc, infraMcp)
		logger.Infof("OK!\n")

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

	g.It("MachineConfigNode properties should properly transition through MCN conditions when OCB is enabled in a non-layering update in custom MCP", func() {
		// TODO: add skip for non-custom allowed env
		// TODO: handle non-custom MCP case also for full coverage?
		var (
			mcpAndMoscName = "infra"
			clientSet      = framework.NewClientSet("")
		)

		exutil.By("Select a node to follow in this test")
		workerNodes, err := helpers.GetNodesByRole(clientSet, "worker")
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting worker nodes: %s", err)
		o.Expect(len(workerNodes)).To(o.BeNumerically(">=", 1), "Less than one worker node in pool")
		nodeToTest := workerNodes[0]
		logger.Infof("Using `%s` as node for test")
		logger.Infof("OK!\n")

		exutil.By("Create custom `infra` MCP and add the test node to it")
		// Create MCP
		infraMcp, err := CreateCustomMCP(oc.AsAdmin(), mcpAndMoscName, 0)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a new custom pool `%s`: %s", mcpAndMoscName, err)
		// TODO: add MCP cleanup
		// Label node
		err = oc.Run("label").Args(fmt.Sprintf("node/%s", nodeToTest.Name), fmt.Sprintf("node-role.kubernetes.io/%s=", mcpAndMoscName)).Execute()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error labeing node `%s` for MCP `%s`: %s", nodeToTest.Name, mcpAndMoscName, err)
		// Wait for the new `infra` MCP to be ready
		WaitForMCPToBeReady(oc, clientSet, mcpAndMoscName, 1)
		logger.Infof("OK!\n")

		exutil.By("Configure OCB functionality for the new `infra` MCP")
		mosc, err := CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, mcpAndMoscName, nil)
		defer mosc.CleanupAndDelete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource: %s", err)
		logger.Infof("OK!\n")

		// TODO: add here the MCN transition expectations
		// 	- Check desired and current config version remain the same
		// 	- Check desired and current image update
		// 	- Check all expected conditions transition
		// 	- Validate ending state (current = desired & updated = true)

		exutil.By("Validating the `infra` MOSC applied successfully")
		ValidateSuccessfulMOSC(mosc, nil)
		logger.Infof("OK!\n")

		// TODO: add here the MC apply and MCN transition expectations
		// 	- Check desired and current config update
		// 	- Check desired and current image version remain the same
		// 	- Check all expected conditions transition
		// 	- ex: image pull should not change
		// 	- Validate ending state (current = desired & updated = true)

		exutil.By("Remove the MachineOSConfig resource")
		o.Expect(DisableOCL(mosc)).To(o.Succeed(), "Error cleaning up MOSC `%s`: %s", mosc, err)
		logger.Infof("OK!\n")

		// TODO: add here the MCN transition expectations for OCB disable
		// 	- Check desired and current config version remain the same
		// 	- Check desired and current image update
		// 	- Check all expected conditions transition
		// 	- Validate ending state (current and desired are removed & updated = true)

		exutil.By("Validating the `infra` MOSC was removed successfully")
		ValidateMOSCIsGarbageCollected(mosc, infraMcp)
		logger.Infof("OK!\n")

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
})

// `ValidateMCNForNode` validates the MCN of a provided node by checking the following:
//   - Check that `mcn.Spec.Pool.Name` matches provided `poolName`
//   - Check that `mcn.Name` matches the node name
//   - Check that `mcn.Spec.ConfigVersion.Desired` matches the node desired config version
//   - Check that `nmcn.Status.ConfigVersion.Current` matches the node current config version
//   - Check that `mcn.Status.ConfigVersion.Desired` matches the node desired config version
//   - Check that `mcn.Spec.ConfigImage.DesiredImage` matches the node desired image
//   - Check that `nmcn.Status.ConfigImage.CurrentImage` matches the node current image
//   - Check that `mcn.Status.ConfigImage.DesiredImage` matches the node desired image
func ValidateMCNForNode(oc *exutil.CLI, clientSet *framework.ClientSet, nodeName, poolName string) error {
	// Get updated node
	node, nodeErr := oc.AsAdmin().KubeClient().CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
	if nodeErr != nil {
		logger.Errorf("Could not get node `%v`", nodeName)
		return nodeErr
	}

	// Get node's desired and current config versions and images
	nodeCurrentConfig := node.Annotations[constants.CurrentMachineConfigAnnotationKey]
	nodeDesiredConfig := node.Annotations[constants.DesiredMachineConfigAnnotationKey]
	nodeCurrentImage := mcfgv1.ImageDigestFormat(node.Annotations[constants.CurrentImageAnnotationKey])
	nodeDesiredImage := mcfgv1.ImageDigestFormat(node.Annotations[constants.DesiredImageAnnotationKey])

	// Get node's MCN
	logger.Infof("Getting MCN for node `%v`.", node.Name)
	mcn, mcnErr := clientSet.GetMcfgclient().MachineconfigurationV1().MachineConfigNodes().Get(context.TODO(), node.Name, metav1.GetOptions{})
	if mcnErr != nil {
		logger.Errorf("Could not get MCN for node `%v`", node.Name)
		return mcnErr
	}

	// Check MCN pool name value for default MCPs
	logger.Infof("Checking MCN pool name for node `%v` matches pool association `%v`.", node.Name, poolName)
	if mcn.Spec.Pool.Name != poolName {
		logger.Errorf("MCN pool name `%v` does not match node MCP association `%v`.", mcn.Spec.Pool.Name, poolName)
		return fmt.Errorf("MCN pool name does not match expected node MCP association.")
	}

	// Check MCN name matches node name
	logger.Infof("Checking MCN name matches node name `%v`.", node.Name)
	if mcn.Name != node.Name {
		logger.Errorf("MCN name `%v` does not match node name `%v`.", mcn.Name, node.Name)
		return fmt.Errorf("MCN name does not match node name.")
	}

	// Check desired config version in MCN spec matches desired config on node
	logger.Infof("Checking node `%v` desired config version `%v` matches desired config version in MCN spec.", node.Name, nodeDesiredConfig)
	if mcn.Spec.ConfigVersion.Desired != nodeDesiredConfig {
		logger.Errorf("MCN spec desired config version `%v` does not match node desired config version `%v`.", mcn.Spec.ConfigVersion.Desired, nodeDesiredConfig)
		return fmt.Errorf("MCN spec desired config version does not match node desired config version")
	}

	// Check current config version in MCN status matches current config on node
	logger.Infof("Checking node `%v` current config version `%v` matches current version in MCN status.", node.Name, nodeCurrentConfig)
	if mcn.Status.ConfigVersion.Current != nodeCurrentConfig {
		logger.Infof("MCN status current config version `%v` does not match node current config version `%v`.", mcn.Status.ConfigVersion.Current, nodeCurrentConfig)
		return fmt.Errorf("MCN status current config version does not match node current config version")
	}

	// Check desired config version in MCN status matches desired config on node
	logger.Infof("Checking node `%v` desired config version `%v` matches desired version in MCN status.", node.Name, nodeDesiredConfig)
	if mcn.Status.ConfigVersion.Desired != nodeDesiredConfig {
		logger.Infof("MCN status desired config version `%v` does not match node desired config version `%v`.", mcn.Status.ConfigVersion.Desired, nodeDesiredConfig)
		return fmt.Errorf("MCN status desired config version does not match node desired config version")
	}

	// Check desired image in MCN spec matches desired image on node
	logger.Infof("Checking node `%v` desired image `%v` matches desired image in MCN spec.", node.Name, nodeDesiredImage)
	if mcn.Spec.ConfigImage.DesiredImage != nodeDesiredImage {
		logger.Errorf("MCN spec desired image `%v` does not match node desired image `%v`.", mcn.Spec.ConfigImage.DesiredImage, nodeDesiredImage)
		return fmt.Errorf("MCN spec desired image does not match node desired image")
	}

	// Check current image in MCN status matches current image on node
	logger.Infof("Checking node `%v` current image `%v` matches current image in MCN status.", node.Name, nodeCurrentImage)
	if mcn.Status.ConfigImage.CurrentImage != nodeCurrentImage {
		logger.Infof("MCN status current image `%v` does not match node current image `%v`.", mcn.Status.ConfigImage.CurrentImage, nodeCurrentImage)
		return fmt.Errorf("MCN status current image does not match node current image")
	}

	// Check desired image in MCN status matches desired image on node
	logger.Infof("Checking node `%v` desired image `%v` matches desired image in MCN status.", node.Name, nodeDesiredImage)
	if mcn.Status.ConfigImage.DesiredImage != nodeDesiredImage {
		logger.Infof("MCN status desired image `%v` does not match node desired image `%v`.", mcn.Status.ConfigImage.DesiredImage, nodeDesiredImage)
		return fmt.Errorf("MCN status desired image does not match node desired image")
	}

	return nil
}

// `CleanupCustomMCP` cleans up a custom MCP through the following steps:
//  1. Remove the custom MCP role label from the node
//  2. Wait for the custom MCP to be updated with no ready machines
//  3. Wait for the node to have a current config version equal to the config version of the worker MCP
//  4. Remove the custom MCP
func CleanupCustomMCP(oc *exutil.CLI, clientSet *framework.ClientSet, customMCPName string, nodeName string) error {
	// Unlabel node
	logger.Infof("Removing label node-role.kubernetes.io/%v from node %v", customMCPName, nodeName)
	unlabelErr := oc.AsAdmin().Run("label").Args(fmt.Sprintf("node/%s", nodeName), fmt.Sprintf("node-role.kubernetes.io/%s-", customMCPName)).Execute()
	if unlabelErr != nil {
		return fmt.Errorf("could not remove label 'node-role.kubernetes.io/%v' from node '%v'; err: %v", customMCPName, nodeName, unlabelErr)
	}

	// Wait for custom MCP to report no ready nodes
	logger.Infof("Waiting for %v MCP to be updated with %v ready machines.", customMCPName, 0)
	WaitForMCPToBeReady(oc, clientSet, customMCPName, 0)

	// Wait for node to have a current config version equal to the worker MCP's config version
	workerMcp, workerMcpErr := clientSet.GetMcfgclient().MachineconfigurationV1().MachineConfigPools().Get(context.TODO(), "worker", metav1.GetOptions{})
	if workerMcpErr != nil {
		return fmt.Errorf("could not get worker MCP; err: %v", workerMcpErr)
	}
	workerMcpConfig := workerMcp.Spec.Configuration.Name
	logger.Infof("Waiting for %v node to be updated with %v config version.", nodeName, workerMcpConfig)
	WaitForNodeCurrentConfig(oc, nodeName, workerMcpConfig)

	// Delete custom MCP
	logger.Infof("Deleting MCP %v", customMCPName)
	deleteMCPErr := oc.AsAdmin().Run("delete").Args("mcp", customMCPName).Execute()
	if deleteMCPErr != nil {
		return fmt.Errorf("error deleting MCP '%v': %v", customMCPName, deleteMCPErr)
	}

	return nil
}

// `WaitForMCPToBeReady` waits up to 5 minutes for a pool to be in an updated state with a specified number of ready machines
func WaitForMCPToBeReady(oc *exutil.CLI, clientSet *framework.ClientSet, poolName string, readyMachineCount int32) {
	o.Eventually(func() bool {
		mcp, err := clientSet.GetMcfgclient().MachineconfigurationV1().MachineConfigPools().Get(context.TODO(), poolName, metav1.GetOptions{})
		if err != nil {
			logger.Infof("Failed to grab MCP '%v', error :%v", poolName, err)
			return false
		}
		// Check if the pool is in an updated state with the correct number of ready machines
		if IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolUpdated) && mcp.Status.UpdatedMachineCount == readyMachineCount {
			logger.Infof("MCP '%v' has the desired %v ready machines.", poolName, mcp.Status.UpdatedMachineCount)
			return true
		}
		// Log details of what is outstanding for the pool to be considered ready
		if mcp.Status.UpdatedMachineCount == readyMachineCount {
			logger.Infof("MCP '%v' has the desired %v ready machines, but is not in an 'Updated' state.", poolName, mcp.Status.UpdatedMachineCount)
		} else {
			logger.Infof("MCP '%v' has %v ready machines. Waiting for the desired ready machine count of %v.", poolName, mcp.Status.UpdatedMachineCount, readyMachineCount)
		}
		return false
	}, 5*time.Minute, 10*time.Second).Should(o.BeTrue(), "Timed out waiting for MCP '%v' to be in 'Updated' state with %v ready machines.", poolName, readyMachineCount)
}

// `WaitForNodeCurrentConfig` waits up to 5 minutes for a input node to have a current config equal to the `config` parameter
func WaitForNodeCurrentConfig(oc *exutil.CLI, nodeName string, config string) {
	o.Eventually(func() bool {
		node, nodeErr := oc.AsAdmin().KubeClient().CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
		if nodeErr != nil {
			logger.Infof("Failed to get node '%v', error :%v", nodeName, nodeErr)
			return false
		}

		// Check if the node's current config matches the input config version
		nodeCurrentConfig := node.Annotations[constants.CurrentMachineConfigAnnotationKey]
		if nodeCurrentConfig == config {
			logger.Infof("Node '%v' has successfully updated and has a current config version of '%v'.", nodeName, nodeCurrentConfig)
			return true
		}
		logger.Infof("Node '%v' has a current config version of '%v'. Waiting for the node's current config version to be '%v'.", nodeName, nodeCurrentConfig, config)
		return false
	}, 5*time.Minute, 10*time.Second).Should(o.BeTrue(), "Timed out waiting for node '%v' to have a current config version of '%v'.", nodeName, config)
}
