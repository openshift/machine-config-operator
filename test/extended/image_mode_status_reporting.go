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
		"90-infra-extension":  filepath.Join("machineconfigs", "infra-extension-mc.yaml"),
		"90-infra-testfile":   filepath.Join("machineconfigs", "infra-testfile-mc.yaml"),
		"90-master-extension": filepath.Join("machineconfigs", "master-extension-mc.yaml"),
		"90-master-testfile":  filepath.Join("machineconfigs", "master-testfile-mc.yaml"),
	}
	nodeDisruptionFixture      = filepath.Join("machineconfigurations", "nodedisruptionpolicy-rebootless-path.yaml")
	nodeDisruptionEmptyFixture = filepath.Join("machineconfigurations", "managedbootimages-empty.yaml")
)

// These tests verify ImageModeStatusReporting feature gate functionality.
// They are [Serial] and [Disruptive] because they modify cluster state and interact with custom MCPs.
var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/disruptive][Serial][Disruptive][OCPFeatureGate:ImageModeStatusReporting]", g.Ordered, func() {
	defer g.GinkgoRecover()

	oc := exutil.NewCLI("mco-image-mode-status", exutil.KubeConfigPath()).AsAdmin()

	g.JustBeforeEach(func() {
		extpriv.PreChecks(oc)
		extpriv.SkipTestIfOCBIsEnabled(oc)
	})

	g.It("MachineConfigNode properties should match the associated node properties when OCB is enabled in a custom MCP [apigroup:machineconfiguration.openshift.io]", func() {
		// Create machine config client for test
		machineConfigClient, clientErr := machineconfigclient.NewForConfig(oc.KubeFramework().ClientConfig())
		o.Expect(clientErr).NotTo(o.HaveOccurred(), "Error creating client set for test: %s", clientErr)

		// If there are no machines in the worker MCP, we cannot create a custom MCP. In these
		// cases, run the test against the default `master` MCP.
		if !DoesMachineConfigPoolHaveMachines(machineConfigClient, "worker") {
			logger.Infof("Cluster has no `worker` machines, running test against the `master` MCP.")
			runImageModeMCNTestDefaultMCP(oc, machineConfigClient, "master", "", false)
		} else {
			// Run the standard image mode MCN test for a custom MCP named `infra` with no MC to apply
			runImageModeMCNTestCustomMCP(oc, machineConfigClient, "infra", "", false)
		}
	})

	g.It("MachineConfigNode conditions should properly transition on an image based update when OCB is enabled in a custom MCP [apigroup:machineconfiguration.openshift.io]", func() {
		// Create machine config client for test
		machineConfigClient, clientErr := machineconfigclient.NewForConfig(oc.KubeFramework().ClientConfig())
		o.Expect(clientErr).NotTo(o.HaveOccurred(), "Error creating client set for test: %s", clientErr)

		// If there are no machines in the worker MCP, we cannot create a custom MCP. In these
		// cases, run the test against the default `master` MCP.
		if !DoesMachineConfigPoolHaveMachines(machineConfigClient, "worker") {
			logger.Infof("Cluster has no `worker` machines, running test against the `master` MCP.")
			runImageModeMCNTestDefaultMCP(oc, machineConfigClient, "master", "90-master-extension", true)
		} else {
			// Run the standard image mode MCN test for a custom MCP named `infra` with no MC to apply
			runImageModeMCNTestCustomMCP(oc, machineConfigClient, "infra", "90-infra-extension", true)
		}
	})

	g.It("MachineConfigNode conditions should properly transition on a non-image based update when OCB is enabled in a custom MCP [apigroup:machineconfiguration.openshift.io]", func() {
		// Create machine config client for test
		machineConfigClient, clientErr := machineconfigclient.NewForConfig(oc.KubeFramework().ClientConfig())
		o.Expect(clientErr).NotTo(o.HaveOccurred(), "Error creating client set for test: %s", clientErr)

		// If there are no machines in the worker MCP, we cannot create a custom MCP. In these
		// cases, run the test against the default `master` MCP.
		if !DoesMachineConfigPoolHaveMachines(machineConfigClient, "worker") {
			logger.Infof("Cluster has no `worker` machines, running test against the `master` MCP.")
			runImageModeMCNTestDefaultMCP(oc, machineConfigClient, "master", "90-master-testfile", false)
		} else {
			// Run the standard image mode MCN test for a custom MCP named `infra`
			runImageModeMCNTestCustomMCP(oc, machineConfigClient, "infra", "90-infra-testfile", false)
		}
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
			logger.Infof("`worker` MCP has machines, running test against that MCP.")
			mcpName = "worker"
		}

		// Run the machine count test for the master MCP when enabling on-cluster image mode
		runMachineCountTest(machineConfigClient, oc, "", mcpName)
	})
})

// `runImageModeMCNTestCustomMCP` runs through the general flow of validating the MCN of a node for
// image mode enabled workflows in a custom MCP. The steps for the test are as follows:
//  1. Select a worker node to use throughout the test
//  2. Validate the starting properties of the MCN associated with the test node
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
func runImageModeMCNTestCustomMCP(oc *exutil.CLI, machineConfigClient *machineconfigclient.Clientset, mcpAndMoscName, mcName string, isImageUpdate bool) {
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
	WaitForMCPToBeReady(machineConfigClient, mcpAndMoscName, 1, "", 5*time.Minute)
	logger.Infof("OK!\n")

	exutil.By("Validate node's custom MCP MCN properties")
	err = ValidateMCNForNode(oc, machineConfigClient, nodeToTestName, mcpAndMoscName)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error validating MCN for node `%s`: %s", nodeToTestName, err)
	logger.Infof("OK!\n")

	exutil.By("Configure OCB functionality for the new `infra` MCP")
	mosc, err := extpriv.CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, mcpAndMoscName, mcpAndMoscName, nil)
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
		// If a rebootless (non-image based) update is desired, apply the NodeDisruptionPolicy to prevent reboots.
		if !isImageUpdate {
			exutil.By("Applying the NodeDisruptionPolicy")
			err = ApplyMachineConfigFixture(oc, nodeDisruptionFixture)
			defer ApplyMachineConfigFixture(oc, nodeDisruptionEmptyFixture)
			o.Expect(err).NotTo(o.HaveOccurred(), "Error applying the NodeDisruptionPolicy: %s", err)
			logger.Infof("OK!\n")
		}

		exutil.By("Applying the MC")
		err = ApplyMachineConfigFixture(oc, mcNameToFixtureMap[mcName])
		defer DeleteMCAndWaitForMCPUpdate(oc, machineConfigClient, mcName, mcpAndMoscName, false)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error applying MC `%s`: %s", mcName, err)
		logger.Infof("OK!\n")

		exutil.By("Validating the MCN condition transitions")
		validateMCNTransitions(oc, machineConfigClient, nodeToTestName, isImageUpdate)
		logger.Infof("OK!\n")
	}

	// Remove the MOSC before the MC to avoid an unnecessary OCB rebuild.
	exutil.By("Remove the MachineOSConfig resource")
	o.Expect(extpriv.DisableOCL(mosc)).To(o.Succeed(), "Error cleaning up MOSC `%s`", mosc)
	logger.Infof("OK!\n")

	exutil.By("Validating the `infra` MOSC was removed successfully")
	extpriv.ValidateMOSCIsGarbageCollected(mosc, infraMcp)
	logger.Infof("OK!\n")

	if mcName != "" {
		exutil.By("Removing the MC")
		DeleteMCAndWaitForMCPUpdate(oc, machineConfigClient, mcName, mcpAndMoscName, false)
		logger.Infof("OK!\n")
	}

	exutil.By("Validate the node in `infra` MCP has correct MCN properties")
	o.Eventually(func() error {
		return ValidateMCNForNode(oc, machineConfigClient, nodeToTestName, mcpAndMoscName)
	}, 1*time.Minute, 5*time.Second).Should(o.Succeed(), "Error validating MCN for node `%s`", nodeToTestName)
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

// `runImageModeMCNTestDefaultMCP` runs through the general flow of validating the MCN of a node
// for image mode enabled workflows in a default MCP. The steps for the test are as follows:
//  1. Select a node in the desired MCP to follow throughout the test
//  2. Validate the starting properties of the MCN associated with the test node
//  3. Configure on cluster image mode in the desired MCP & validate the MOSC applied successfully
//  4. Validate the properties of the MCN associated with the test node
//  5. If a MachineConfig has been provided, apply it, validate the MCN conditions trasnition
//     throughout the update, then remove the MC
//  6. Disable on cluster image mode in the desired MCP & validate the MOSC removal was successful
//  7. Validate the properties of the MCN associated with the test node
func runImageModeMCNTestDefaultMCP(oc *exutil.CLI, machineConfigClient *machineconfigclient.Clientset, mcpAndMoscName, mcName string, isImageUpdate bool) {
	exutil.By("Select a node to follow in this test")
	mcpNodes, err := GetNodesByRole(oc, mcpAndMoscName)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting nodes from `%v` MCP: %s", mcpAndMoscName, err)
	o.Expect(len(mcpNodes)).To(o.BeNumerically(">=", 1), "Less than one node in desired pool")
	nodeToTestName := mcpNodes[0].Name
	logger.Infof("Using `%s` as node for test", nodeToTestName)
	logger.Infof("OK!\n")

	exutil.By("Validate node's starting MCN properties")
	err = ValidateMCNForNode(oc, machineConfigClient, nodeToTestName, mcpAndMoscName)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error validating MCN for node `%s`: %s", nodeToTestName, err)
	logger.Infof("OK!\n")

	exutil.By("Configure OCB functionality for the desired MCP")
	mosc, err := extpriv.CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, mcpAndMoscName, mcpAndMoscName, nil)
	defer extpriv.DisableOCL(mosc)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource: %s", err)
	logger.Infof("OK!\n")

	exutil.By("Validating the MOSC applied successfully")
	extpriv.ValidateSuccessfulMOSC(mosc, nil)
	logger.Infof("OK!\n")

	exutil.By("Validate the test node has correct MCN properties")
	err = ValidateMCNForNode(oc, machineConfigClient, nodeToTestName, mcpAndMoscName)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error validating MCN for node `%s`: %s", nodeToTestName, err)
	logger.Infof("OK!\n")

	// If an MC has been provided, apply it and validate the MCN conditions transition correctly
	// throughout the update.
	if mcName != "" {
		// If a rebootless (non image-based) update is desired, apply the NodeDisruptionPolicy to prevent reboots.
		if !isImageUpdate {
			exutil.By("Applying the NodeDisruptionPolicy")
			err = ApplyMachineConfigFixture(oc, nodeDisruptionFixture)
			defer ApplyMachineConfigFixture(oc, nodeDisruptionEmptyFixture)
			o.Expect(err).NotTo(o.HaveOccurred(), "Error applying the NodeDisruptionPolicy: %s", err)
			logger.Infof("OK!\n")
		}

		exutil.By("Applying the MC")
		err = ApplyMachineConfigFixture(oc, mcNameToFixtureMap[mcName])
		defer DeleteMCAndWaitForMCPUpdate(oc, machineConfigClient, mcName, mcpAndMoscName, true)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error applying MC `%s`: %s", mcName, err)
		logger.Infof("OK!\n")

		exutil.By("Validating the MCN condition transitions")
		validateMCNTransitions(oc, machineConfigClient, nodeToTestName, isImageUpdate)
		logger.Infof("OK!\n")
	}

	// Remove the MOSC before the MC to avoid an unnecessary OCB rebuild. Removing the
	// MC while image mode is active would trigger a new image build, consuming additional
	// disk space that can cause DiskPressure on SNO nodes. By exiting image mode first,
	// the subsequent MC deletion is a standard non-layered update.
	exutil.By("Remove the MachineOSConfig resource")
	o.Expect(extpriv.DisableOCL(mosc)).To(o.Succeed(), "Error cleaning up MOSC `%s`", mosc)
	logger.Infof("OK!\n")

	exutil.By("Validating the MOSC was removed successfully")
	mcp := extpriv.NewMachineConfigPool(oc.AsAdmin(), mcpAndMoscName)
	extpriv.ValidateMOSCIsGarbageCollected(mosc, mcp)
	logger.Infof("OK!\n")

	if mcName != "" {
		exutil.By("Removing the MC")
		DeleteMCAndWaitForMCPUpdate(oc, machineConfigClient, mcName, mcpAndMoscName, true)
		logger.Infof("OK!\n")
	}

	exutil.By("Validate the test node has correct MCN properties")
	o.Eventually(func() error {
		return ValidateMCNForNode(oc, machineConfigClient, nodeToTestName, mcpAndMoscName)
	}, 1*time.Minute, 5*time.Second).Should(o.Succeed(), "Error validating MCN for node `%s`", nodeToTestName)
	logger.Infof("OK!\n")
}

// `validateMCNTransitions` applies a MC, validates that the MCN conditions properly
// transition during the update, removes the MC, then validates that the MCN conditions properly
// transition during the update.
func validateMCNTransitions(oc *exutil.CLI, machineConfigClient *machineconfigclient.Clientset, nodeToTestName string, isImageUpdate bool) {
	// Validate transition through conditions for MCN
	exutil.By("Validating the transitions through MCN conditions")
	ValidateTransitionThroughConditions(oc, machineConfigClient, nodeToTestName, isImageUpdate)
	logger.Infof("OK!\n")

	// When an update is complete, all conditions other than `Updated` must be false
	logger.Infof("Checking all conditions other than 'Updated' are False.")
	o.Expect(ConfirmUpdatedMCNStatus(machineConfigClient, nodeToTestName)).Should(o.BeTrue(), "Error, all conditions must be 'False' when Updated=True.")
	logger.Infof("OK!\n")
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
		defer DeleteMCAndWaitForMCPUpdate(oc, machineConfigClient, mcName, mcpName, false)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error applying MC `%s`: %s", mcName, err)
		logger.Infof("OK!\n")
	} else { // Handle the layered MCP case
		layered = true
		// Enable image mode in the desired pool
		exutil.By("Configure OCB functionality in the `master` MCP")
		mosc, err = extpriv.CreateMachineOSConfigUsingExternalOrInternalRegistry(oc.AsAdmin(), MachineConfigNamespace, mcpName, mcpName, nil)
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
//   - The MCP machine counts have not matched the expected counts for longer than the mismatch
//     deadline of 2 minutes for non-SNO clusters and 5 minutes for SNO clusters
//   - The MCP is fully updated
//   - The overall timeout for this function has been met
//   - Some other error has occurred in a step of the function
func validateMCPMachineCountTransitions(machineConfigClient *machineconfigclient.Clientset, oc *exutil.CLI, mcpName string, startTime metav1.Time, mosc *extpriv.MachineOSConfig, layered bool) {
	timeout := 15 * time.Minute
	interval := 10 * time.Second
	mismatchDeadline := 2 * time.Minute
	if layered {
		logger.Infof("Layered update, setting longer update timeout.")
		timeout = 45 * time.Minute
		interval = 30 * time.Second
		mismatchDeadline = 5 * time.Minute
	}

	var firstMismatchTime time.Time
	o.Eventually(func() bool {
		logger.Infof("Checking if the MCP machine counts match the expected values...")
		countsMatch, isConnErr := mcnAndNodeAnnotationMachineCountsMatch(machineConfigClient, oc, mcpName, mosc)

		// If we hit a connection error, continue to the next iterration
		if isConnErr {
			return false
		}

		// Handle the case when counts don't match, which can happen due to latency in MCP syncs
		if !countsMatch {
			if firstMismatchTime.IsZero() {
				firstMismatchTime = time.Now()
			}
			elapsed := time.Since(firstMismatchTime)
			if elapsed > mismatchDeadline {
				o.StopTrying(fmt.Sprintf("MCP '%v' machine counts do not match expected values.", mcpName)).Now()
			}
			logger.Infof("Counts do not match yet. Will retry.")
			return false
		}

		// Counts match — reset tracker and check if the MCP update is complete
		firstMismatchTime = time.Time{}
		return MCPIsUpdatedToNewConfig(machineConfigClient, mcpName, startTime)
	}, timeout, interval).Should(o.BeTrue(), "Timed out waiting for MCP '%v' to complete update.", mcpName)
}

// `mcnAndNodeAnnotationMachineCountsMatch` checks whether the updated and degraded machine counts
// in the desired MCP match the expected machine counts calculated by checking the annotations of
// each node in the pool. The first boolean return is `true` if the counts match and `false`
// otherwise. The second boolean return is `true`if there was a connection error when trying to get
// a resource (this allows for resiliency in SNO clsuters).
func mcnAndNodeAnnotationMachineCountsMatch(machineConfigClient *machineconfigclient.Clientset, oc *exutil.CLI,
	mcpName string, mosc *extpriv.MachineOSConfig,
) (countsMatch, isConnErr bool) {
	// Get machine counts from MCP
	mcp, mcpErr := machineConfigClient.MachineconfigurationV1().MachineConfigPools().Get(context.TODO(), mcpName, metav1.GetOptions{})
	// If we fail to get the MCP, it could be due to a connection error or other infrastructure
	// instability, so we should log a warning but not error out. This is especially important for SNO.
	if mcpErr != nil {
		if isTransientConnectionError(mcpErr) {
			logger.Infof("Transient error getting MCP `%v`, will retry: %v", mcpName, mcpErr)
			return false, true
		}
		logger.Infof("Non-transient error getting MCP `%v`: %v", mcpName, mcpErr)
		return false, false
	}
	actualUpdatedCount := mcp.Status.UpdatedMachineCount
	actualDegradedCount := mcp.Status.DegradedMachineCount

	// Get the expected machine counts from the node annotation values
	nodes, nodesErr := GetNodesByRole(oc, mcpName)
	// If we fail to get the nodes, it could be due to a connection error or other infrastructure
	// instability, so we should log a warning but not error out. This is especially important for SNO.
	if nodesErr != nil {
		if isTransientConnectionError(nodesErr) {
			logger.Infof("Transient error getting nodes in MCP `%v`, will retry: %v", mcpName, nodesErr)
			return false, true
		}
		logger.Infof("Non-transient error getting nodes in MCP `%v`: %v", mcpName, nodesErr)
		return false, false
	}
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
		return false, false
	}
	return true, false
}
