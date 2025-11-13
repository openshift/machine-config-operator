package extended

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	machineconfigclient "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	extpriv "github.com/openshift/machine-config-operator/test/extended-priv"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	mcNameToFixtureMap = map[string]string{
		"90-infra-extension": filepath.Join("machineconfigs", "infra-extension-mc.yaml"),
		"90-infra-testfile":  filepath.Join("machineconfigs", "infra-testfile-mc.yaml"),
		"90-master-testfile": filepath.Join("machineconfigs", "master-testfile-mc.yaml"),
	}
	nodeDisruptionFixture      = filepath.Join("machineconfigurations", "nodedisruptionpolicy-rebootless-path.yaml")
	nodeDisruptionEmptyFixture = filepath.Join("machineconfigurations", "managedbootimages-empty.yaml")
)

// These tests verify ImageModeStatusReporting feature gate functionality.
// They are [Serial] and [Disruptive] because they modify cluster state and interact with custom MCPs.
var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/disruptive][Serial][Disruptive][OCPFeatureGate:ImageModeStatusReporting]", g.Ordered, func() {
	defer g.GinkgoRecover()

	var (
		oc = exutil.NewCLI("mco-image-mode-status", exutil.KubeConfigPath()).AsAdmin()
	)

	g.JustBeforeEach(func() {
		extpriv.PreChecks(oc)
		extpriv.SkipTestIfOCBIsEnabled(oc)
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
		runImageModeMCNTest(oc, machineConfigClient, "infra", "", false)
	})

	g.It("MachineConfigNode conditions should properly transition on an image based update when OCB is enabled in a custom MCP [apigroup:machineconfiguration.openshift.io]", func() {
		// Create machine config client for test
		machineConfigClient, clientErr := machineconfigclient.NewForConfig(oc.KubeFramework().ClientConfig())
		o.Expect(clientErr).NotTo(o.HaveOccurred(), "Error creating client set for test: %s", clientErr)

		// Skip this test if there are no machines in the worker MCP, as this will mean we cannot
		// create a custom MCP.
		if !DoesMachineConfigPoolHaveMachines(machineConfigClient, "worker") {
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
		if !DoesMachineConfigPoolHaveMachines(machineConfigClient, "worker") {
			g.Skip(fmt.Sprintf("Skipping this test since the cluster does not have nodes in the `worker` MCP."))
		}

		// Run the standard image mode MCN test for a custom MCP named `infra` with no MC to apply
		runImageModeMCNTest(oc, machineConfigClient, "infra", "90-infra-testfile", false)
	})

	g.It("MachineConfigPool machine counts should transition correctly on an update in a default MCP [apigroup:machineconfiguration.openshift.io]", func() {
		// Create machine config client for test
		machineConfigClient, clientErr := machineconfigclient.NewForConfig(oc.KubeFramework().ClientConfig())
		o.Expect(clientErr).NotTo(o.HaveOccurred(), "Error creating client set for test: %s", clientErr)

		// Run the machine count test for a default MCP when applying a standard MachineConfig
		runMachineCountTest(machineConfigClient, oc, "90-master-testfile", "master")
	})

	g.It("MachineConfigPool machine counts should transition when OCB is enabled in a default MCP [apigroup:machineconfiguration.openshift.io]", func() {
		// Create machine config client for test
		machineConfigClient, clientErr := machineconfigclient.NewForConfig(oc.KubeFramework().ClientConfig())
		o.Expect(clientErr).NotTo(o.HaveOccurred(), "Error creating client set for test: %s", clientErr)

		// Run this test against the `worker` MCP if it has machines, otherwise default to `master`
		mcpName := "master"
		if DoesMachineConfigPoolHaveMachines(machineConfigClient, "worker") {
			logger.Infof("`worker` MCP has machines, running test in against that MCP.")
			mcpName = "worker"
		}

		// Run the machine count test for the master MCP when enabling on-cluster image mode
		runMachineCountTest(machineConfigClient, oc, "", mcpName)
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
	infraMcp, err := extpriv.CreateCustomMCP(oc.AsAdmin(), mcpAndMoscName, 0)
	defer CleanupCustomMCP(oc, machineConfigClient, mcpAndMoscName, nodeToTestName)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a new custom pool `%s`: %s", mcpAndMoscName, err)
	// Label node
	err = oc.AsAdmin().Run("label").Args(fmt.Sprintf("node/%s", nodeToTestName), fmt.Sprintf("node-role.kubernetes.io/%s=", mcpAndMoscName)).Execute()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error labeing node `%s` for MCP `%s`: %s", nodeToTestName, mcpAndMoscName, err)
	// Wait for the new `infra` MCP to be ready
	WaitForMCPToBeReady(machineConfigClient, mcpAndMoscName, 1, "")
	logger.Infof("OK!\n")

	exutil.By("Validate node's custom MCP MCN properties")
	err = ValidateMCNForNode(oc, machineConfigClient, nodeToTestName, mcpAndMoscName)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error validating MCN for node `%s`: %s", nodeToTestName, err)
	logger.Infof("OK!\n")

	exutil.By("Configure OCB functionality for the new `infra` MCP")
	mosc, err := extpriv.CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, mcpAndMoscName, nil)
	defer extpriv.DisableOCL(mosc)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource: %s", err)
	logger.Infof("OK!\n")

	exutil.By("Validating the `infra` MOSC applied successfully")
	extpriv.ValidateSuccessfulMOSC(mosc, nil)
	logger.Infof("OK!\n")

	exutil.By("Validate the node in `infra` MCP has correct MCN properties")
	err = ValidateMCNForNode(oc, machineConfigClient, nodeToTestName, mcpAndMoscName)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error validating MCN for node `%s`: %s", nodeToTestName, err)
	logger.Infof("OK!\n")

	// If an MC has been provided, apply it and validate the MCN conditions transition correctly
	// throughout the update.
	if mcName != "" {
		exutil.By("Applying the MC")
		err = ApplyMachineConfigFixture(oc, mcNameToFixtureMap[mcName])
		defer DeleteMCAndWaitForMCPUpdate(oc, machineConfigClient, mcName, mcpAndMoscName)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error applying MC `%s`: %s", mcName, err)
		logger.Infof("OK!\n")

		exutil.By("Validating the MCN condition transitions")
		validateMCNTransitions(machineConfigClient, nodeToTestName, isImageUpdate)
		logger.Infof("OK!\n")

		exutil.By("Removing the MC")
		DeleteMCAndWaitForMCPUpdate(oc, machineConfigClient, mcName, mcpAndMoscName)
		logger.Infof("OK!\n")
	}

	exutil.By("Remove the MachineOSConfig resource")
	o.Expect(extpriv.DisableOCL(mosc)).To(o.Succeed(), "Error cleaning up MOSC `%s`", mosc)
	logger.Infof("OK!\n")

	exutil.By("Validating the `infra` MOSC was removed successfully")
	extpriv.ValidateMOSCIsGarbageCollected(mosc, infraMcp)
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

// `validateMCNTransitions` applies a MC, validates that the MCN conditions properly
// transition during the update, removes the MC, then validates that the MCN conditions properly
// transition during the update.
func validateMCNTransitions(machineConfigClient *machineconfigclient.Clientset, nodeToTestName string, isImageUpdate bool) {
	// Validate transition through conditions for MCN
	exutil.By("Validating the transitions through MCN conditions")
	ValidateTransitionThroughConditions(machineConfigClient, nodeToTestName, false, isImageUpdate)
	logger.Infof("OK!\n")

	// When an update is complete, all conditions other than `Updated` must be false
	logger.Infof("Checking all conditions other than 'Updated' are False.")
	o.Expect(ConfirmUpdatedMCNStatus(machineConfigClient, nodeToTestName)).Should(o.BeTrue(), "Error, all conditions must be 'False' when Updated=True.")
	logger.Infof("OK!\n")

	return
}

// `runMachineCountTest` runs through the general flow of validating machine count transitions in
// MCPs after an update has been triggered and on the subsequent cleanup. The steps are as follows:
//  1. Get the starting rendered MC version to track the update completion
//  2. Trigger the MCP updating process
//     - If a MC is provided, apply a node disruption policy to limit reboots then apply the MC
//     - If an MC is not provided, enable on-cluster image mode
//  3. Validate the machine counts transition as expected throughout the update
//  4. Get the updated rendered MC version to track the cleanup completion
//  5. Cleanup the update triggered in step 2
//     - For the MC apply case, delete the MC
//     - For the on-cluster image mode case, delete the created MOSC
//  6. Validate the machine counts transition as expected throughout the cleanup
func runMachineCountTest(machineConfigClient *machineconfigclient.Clientset, oc *exutil.CLI, mcName, mcpName string) {
	// Get the starting time of the test
	startTime := metav1.Now()

	var mosc *extpriv.MachineOSConfig
	var err error
	var layered bool
	if mcName != "" { // Handle a standard MC apply case
		// Apply a node disruption policy to allow for a rebootless update
		exutil.By("Applying the NodeDisruptionPolicy")
		err = ApplyMachineConfigFixture(oc, nodeDisruptionFixture)
		defer ApplyMachineConfigFixture(oc, nodeDisruptionEmptyFixture)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error applying the NodeDisruptionPolicy: %s", err)
		logger.Infof("OK!\n")

		// Apply machine config
		exutil.By("Applying the MC")
		err = ApplyMachineConfigFixture(oc, mcNameToFixtureMap[mcName])
		defer DeleteMCAndWaitForMCPUpdate(oc, machineConfigClient, mcName, mcpName)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error applying MC `%s`: %s", mcName, err)
		logger.Infof("OK!\n")
	} else { // Handle the layered MCP case
		layered = true
		// Enable image mode in the desired pool
		exutil.By("Configure OCB functionality in the `master` MCP")
		mosc, err = extpriv.CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, mcpName, nil)
		defer extpriv.DisableOCL(mosc)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource: %s", err)
		logger.Infof("OK!\n")
	}

	// Track that the machine counts transition properly
	validateMCPMachineCountTransitions(machineConfigClient, oc, mcpName, startTime, mosc, layered)

	// Get the starting time of the cleanup
	startTime = metav1.Now()

	if mcName != "" { // Handle a standard MC apply case
		// Delete the machine config
		exutil.By("Removing the MC")
		_, mcErr := DeleteMCByName(oc, machineConfigClient, mcName)
		o.Expect(mcErr).NotTo(o.HaveOccurred(), "Error deleting MC `%s`: %s", mcName, mcErr)
		logger.Infof("OK!\n")
	} else { // Handle the layered MCP case
		// Disable image mode
		exutil.By("Remove the MachineOSConfig resource")
		err = mosc.CleanupAndDelete()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error deleting MOSC: %s", err)
		logger.Infof("OK!\n")
	}

	// Track that the machine counts transition properly during cleanup
	validateMCPMachineCountTransitions(machineConfigClient, oc, mcpName, startTime, nil, layered)
}

// `validateMCPMachineCountTransitions` validates the actual machine counts reported in a MCP's
// status matches the expected machine counts determined by checking the node annotations of the
// targeted nodes. This function runs until one of the following termination conditions is met:
//   - The expected and actual machine counts do not match within a 8 second tolerance window
//   - The MCP is fully updated
//   - The overall timeout for this function has been met
//   - Some other error has occurred in a step of the function
func validateMCPMachineCountTransitions(machineConfigClient *machineconfigclient.Clientset, oc *exutil.CLI, mcpName string, startTime metav1.Time, mosc *extpriv.MachineOSConfig, layered bool) {
	timeout := 15 * time.Minute
	interval := 10 * time.Second
	// For on-cluster image mode updates, set the timeout to be longer
	if layered {
		logger.Infof("Layered update, setting longer update timeout.")
		timeout = 45 * time.Minute
		interval = 30 * time.Second
	}
	o.Eventually(func() bool {
		// Check if the MCP machine counts match what is expected from the pool's node annotation
		// values. Note that there is some latency between when the node annotations are set and
		// when the MCP machine counts are updates, so we'll try this a few times to make sure we
		// don't fail just because of unlucky timing.
		for i := 0; i < 3; i++ {
			logger.Infof("Checking if the MCP machine counts match the expected values...")

			// Handle success case
			if mcnAndNodeAnnotationMachineCountsMatch(machineConfigClient, oc, mcpName, mosc) {
				break
			}

			// Handle case when the counts are not as expected. If we reach this point in the third
			// itteration, we've exhausted our attempts and are likely seeing a true discrepancy
			// between the expected and actual machine counts.
			o.Expect(i).NotTo(o.BeNumerically("==", 2), "The actual MCP machine counts did not match the expected machine counts")
			// If we have not exhausted our attempts, wait a few seconds and check again
			if i != 2 {
				logger.Infof("The MCP machine counts did match the expected values. Waiting 4 seconds then trying again.")
				time.Sleep(4 * time.Second)
			}
		}

		// Check if the MCP is updated
		// TODO: check if this properly handles the OCL case
		return MCPIsUpdatedToNewConfig(machineConfigClient, mcpName, startTime)
	}, timeout, interval).Should(o.BeTrue(), "Timed out waiting for MCP '%v' to complete update.", mcpName)

}

// `mcnAndNodeAnnotationMachineCountsMatch` checks whether the updated and degraded machine counts
// in the desired MCP match the expected machine counts calculated by checking the annotations of
// each node in the pool. It returns `true` if the counts match and false otherwise.
func mcnAndNodeAnnotationMachineCountsMatch(machineConfigClient *machineconfigclient.Clientset, oc *exutil.CLI, mcpName string, mosc *extpriv.MachineOSConfig) bool {
	// Get machine counts from MCP
	mcp, mcpErr := machineConfigClient.MachineconfigurationV1().MachineConfigPools().Get(context.TODO(), mcpName, metav1.GetOptions{})
	o.Expect(mcpErr).NotTo(o.HaveOccurred(), "Error getting MCP `%v`: %s", mcpName, mcpErr)
	actualUpdatedCount := mcp.Status.UpdatedMachineCount
	actualDegradedCount := mcp.Status.DegradedMachineCount

	// Get the expected machine counts from the node annotation values
	nodes, nodesErr := GetNodesByRole(oc, mcpName)
	o.Expect(nodesErr).NotTo(o.HaveOccurred(), "Error getting nodes in MCP `%v`: %s", mcpName, nodesErr)
	var expectedUpdatedCount int32
	var expectedDegradedCount int32
	for _, node := range nodes {
		desiredConfig := node.Annotations[DesiredMachineConfigAnnotationKey]
		currentConfig := node.Annotations[CurrentMachineConfigAnnotationKey]
		desiredImage := node.Annotations[DesiredImageAnnotationKey]
		currentImage := node.Annotations[CurrentImageAnnotationKey]
		mcdState := node.Annotations[MachineConfigDaemonStateAnnotationKey]

		// Check if node is degraded Note that a node is considered degraded when the
		// MachineConfigDaemon state is either "Degraded" or "Unreconcilable"
		if mcdState == MachineConfigDaemonStateDegraded || mcdState == MachineConfigDaemonStateUnreconcilable {
			expectedDegradedCount++
		}

		// Check if node is updated. Note that a node is considered updated when the following
		// annotation conditions are met:
		// 	- The current and desired config versions match
		// 	- The current and desired images match
		// 	- The MachineConfigDaemon state is "Done"
		// 	- The node's desired config version matches the config version in the MCP's `Spec`
		// 	- (non-layered MCP) The node's desired image is empty
		// 	- (layered MCP) The node's desired image should not be empty
		// 	- (layered MCP) The node's desired image matches the CurrentImagePullSpec in the MOSC's `Status`
		// 	- (layered MCP) The node's desired config matches the config version in the MOSB's `Spec`
		if desiredConfig == currentConfig && desiredImage == currentImage &&
			mcdState == MachineConfigDaemonStateDone && desiredConfig == mcp.Spec.Configuration.Name {
			if mosc == nil && desiredImage == "" { // Handle updated non-layered MCP case
				expectedUpdatedCount++
			} else if mosc != nil { // Handle the layered MCP case
				// Get the config version from the MOSB for the MCP
				mosb, mosbErr := mosc.GetCurrentMachineOSBuild()
				// If the MOSB DNE yet, the node is not updated
				if mosbErr != nil || mosb == nil {
					continue
				}
				mosbConfigName, mosbErr := mosb.GetMachineConfigName()
				o.Expect(mosbErr).NotTo(o.HaveOccurred(), "Error getting machine config from MOSB `%v`: %s", mosb.GetName(), mosbErr)

				// Get the config image from the MOSC
				moscConfigImage, moscErr := mosc.GetStatusCurrentImagePullSpec()
				o.Expect(moscErr).NotTo(o.HaveOccurred(), "Error getting config image from MOSC `%v`: %s", mosc.GetName(), moscErr)

				// Check if the node is updated
				if desiredImage != "" && desiredImage == moscConfigImage && desiredConfig == mosbConfigName {
					expectedUpdatedCount++
				}
			}
		}
	}

	// Check that actual and expected degraded and updated machine counts match
	if actualUpdatedCount != expectedUpdatedCount || actualDegradedCount != expectedDegradedCount {
		// Log when the counts are not the same so debugging is easier
		logger.Infof("actualUpdatedCount: %v; expectedUpdatedCount: %v; actualDegradedCount: %v; expectedDegradedCount: %v", actualUpdatedCount, expectedUpdatedCount, actualDegradedCount, expectedDegradedCount)
		return false
	}
	return true
}
