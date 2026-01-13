package extended

import (
	"context"
	"fmt"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	machineclient "github.com/openshift/client-go/machine/clientset/versioned"
	machineconfigclient "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	extpriv "github.com/openshift/machine-config-operator/test/extended-priv"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/disruptive][Serial][Disruptive]", func() {
	defer g.GinkgoRecover()

	var (
		oc                  = exutil.NewCLI("custom-pool-booting", exutil.KubeConfigPath()).AsAdmin()
		customMCPName       = "infra"
		clonedMachineSet    *machinev1beta1.MachineSet
		newNodeName         string
		machineClient       *machineclient.Clientset
		machineConfigClient *machineconfigclient.Clientset
		err                 error
	)

	g.BeforeEach(func() {
		// Skip if worker scale-ups aren't possible
		extpriv.SkipTestIfWorkersCannotBeScaled(oc)

		// Skip on single-node topologies
		skipOnSingleNodeTopology(oc)

		machineClient, err = machineclient.NewForConfig(oc.KubeFramework().ClientConfig())
		o.Expect(err).NotTo(o.HaveOccurred())

		machineConfigClient, err = machineconfigclient.NewForConfig(oc.KubeFramework().ClientConfig())
		o.Expect(err).NotTo(o.HaveOccurred())
	})

	g.AfterEach(func(ctx context.Context) {
		exutil.By("Performing cleanup")
		logger.Infof("Cleaning up custom MCP %s", customMCPName)
		// Cleanup: Delete custom MCP if it exists
		deleteMCPErr := extpriv.DeleteCustomMCP(oc, customMCPName)
		if deleteMCPErr != nil {
			logger.Infof("Failed to delete MCP %s: %v", customMCPName, deleteMCPErr)
		} else {
			logger.Infof("Successfully deleted MCP %s", customMCPName)
		}

		// Cleanup: Scale down and delete the cloned machineset if it exists
		clonedMachineSet, err := machineClient.MachineV1beta1().MachineSets(MAPINamespace).Get(ctx, clonedMachineSet.Name, metav1.GetOptions{})
		if err != nil {
			logger.Infof("Failed to get machineset: %v", err)
			return
		}

		logger.Infof("Scaling down machineset %s to 0", clonedMachineSet.Name)
		clonedMachineSet.Spec.Replicas = new(int32)
		*clonedMachineSet.Spec.Replicas = 0
		_, err = machineClient.MachineV1beta1().MachineSets(MAPINamespace).Update(ctx, clonedMachineSet, metav1.UpdateOptions{})
		if err != nil {
			logger.Infof("Failed to scale down machineset: %v", err)
		}

		// Wait for the machine to be deleted
		o.Eventually(func() bool {
			machines, err := machineClient.MachineV1beta1().Machines(MAPINamespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("machine.openshift.io/cluster-api-machineset=%s", clonedMachineSet.Name),
			})
			if err != nil {
				logger.Infof("Failed to list machines: %v", err)
				return false
			}
			return len(machines.Items) == 0
		}, 5*time.Minute, 10*time.Second).Should(o.BeTrue(), "Timed out waiting for machine to be deleted")

		// Delete the machineset
		logger.Infof("Deleting machineset %s", clonedMachineSet.Name)
		err = machineClient.MachineV1beta1().MachineSets(MAPINamespace).Delete(ctx, clonedMachineSet.Name, metav1.DeleteOptions{})
		if err != nil {
			logger.Infof("Failed to delete machineset: %v", err)
		}
	})

	// 1. Create a custom MCP named infra
	// 2. Clone an existing machineset, edit the user-data-secret field to point to infra-user-data-managed
	// 3. Scale up a new node from this machineset
	// 4. Verify labels and annotations and ensure custom pool count after that.
	// 5. Verify that the node moves back to worker pool if the label is removed after this point.
	// 6. Clean up test resources
	g.It("Node booted into custom pool should have appropriate labels and not moved back to worker pool [apigroup:machineconfiguration.openshift.io]", func(ctx context.Context) {
		exutil.By("Create custom `infra` MCP and add the test node to it")
		// Create MCP
		_, err := extpriv.CreateCustomMCP(oc.AsAdmin(), customMCPName, 0)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a new custom pool `%s`: %s", customMCPName, err)

		exutil.By("Cloning an existing machineset, edit the user-data-secret field to point to infra-user-data-managed")

		// Get a random machineset to clone
		originalMachineSet := getRandomMachineSet(machineClient)

		// Create a clone without modifying user-data-secret yet
		clonedMachineSet = originalMachineSet.DeepCopy()
		clonedMachineSet.Name = fmt.Sprintf("%s-custom-pool-test", customMCPName)
		clonedMachineSet.ResourceVersion = ""
		clonedMachineSet.UID = ""
		clonedMachineSet.Spec.Replicas = new(int32)
		*clonedMachineSet.Spec.Replicas = 0 // Start with 0 replicas
		clonedMachineSet.Spec.Selector.MatchLabels["machine.openshift.io/cluster-api-machineset"] = clonedMachineSet.Name
		clonedMachineSet.Spec.Template.Labels["machine.openshift.io/cluster-api-machineset"] = clonedMachineSet.Name

		// Create the cloned machineset
		clonedMachineSet, err = machineClient.MachineV1beta1().MachineSets(MAPINamespace).Create(ctx, clonedMachineSet, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred(), "Failed to create cloned machineset")
		logger.Infof("Successfully created cloned machineset %s", clonedMachineSet.Name)

		// Use JSON patch to modify the user-data-secret field to point to infra-user-data-managed
		userDataSecretName := fmt.Sprintf("%s-user-data-managed", customMCPName)
		jsonPatch := fmt.Sprintf(`[{"op": "replace", "path": "/spec/template/spec/providerSpec/value/userDataSecret/name", "value": "%s"}]`, userDataSecretName)
		err = oc.AsAdmin().Run("patch").Args(MAPIMachinesetQualifiedName, clonedMachineSet.Name, "-p", jsonPatch, "-n", MAPINamespace, "--type=json").Execute()
		o.Expect(err).NotTo(o.HaveOccurred(), "Failed to patch user-data-secret in machineset")
		logger.Infof("Successfully patched machineset %s to use user-data-secret %s", clonedMachineSet.Name, userDataSecretName)

		exutil.By("Scale up a new node from cloned machineset")
		// Scale up the machineset to 1 replica
		logger.Infof("Scaling up machineset %s to 1 replica", clonedMachineSet.Name)
		err = oc.AsAdmin().Run("scale").Args(MAPIMachinesetQualifiedName, clonedMachineSet.Name, "--replicas=1", "-n", MAPINamespace).Execute()
		o.Expect(err).NotTo(o.HaveOccurred(), "Failed to scale up machineset")
		logger.Infof("Successfully scaled up machineset %s", clonedMachineSet.Name)

		logger.Infof("Waiting for new machine to be running from machineset %s", clonedMachineSet.Name)
		machineSetHelper := extpriv.NewMachineSet(oc.AsAdmin(), MAPINamespace, clonedMachineSet.Name)
		runningMachines := machineSetHelper.WaitForRunningMachines(1, 15*time.Minute, 30*time.Second)
		o.Expect(runningMachines).To(o.HaveLen(1), "Expected 1 running machine from machineset %s", clonedMachineSet.Name)

		// Grab node name from the new machine object
		machine := runningMachines[0]
		newNodeName = machine.GetNodeOrFail().GetName()
		logger.Infof("Machine %s is running with node %s", machine.GetName(), newNodeName)

		exutil.By("Verifying node labels and annotations")
		// Wait for the node to have the FirstPivot annotation
		o.Eventually(func() bool {
			node, err := oc.AsAdmin().KubeClient().CoreV1().Nodes().Get(ctx, newNodeName, metav1.GetOptions{})
			if err != nil {
				return false
			}

			// Check if FirstPivot annotation exists
			if _, exists := node.Annotations[daemonconsts.FirstPivotMachineConfigAnnotationKey]; exists {
				return true
			}

			logger.Infof("Node %s does not have FirstPivot annotation yet", newNodeName)
			return false
		}, 5*time.Minute, 10*time.Second).Should(o.BeTrue(), "Timed out waiting for FirstPivot annotation")

		// Verify the node has the custom pool label
		o.Eventually(func() bool {
			node, err := oc.AsAdmin().KubeClient().CoreV1().Nodes().Get(ctx, newNodeName, metav1.GetOptions{})
			if err != nil {
				return false
			}

			// Check for custom pool label
			expectedLabel := fmt.Sprintf("node-role.kubernetes.io/%s", customMCPName)
			if _, exists := node.Labels[expectedLabel]; !exists {
				logger.Infof("Node %s does not have custom pool label %s yet", newNodeName, expectedLabel)
				return false
			}

			// Verify CustomPoolLabelsApplied annotation
			if _, exists := node.Annotations[daemonconsts.CustomPoolLabelsAppliedAnnotationKey]; !exists {
				logger.Infof("Node %s does not have CustomPoolLabelsApplied annotation yet", newNodeName)
				return false
			}

			logger.Infof("Node %s has custom pool label %s and CustomPoolLabelsApplied annotation", newNodeName, expectedLabel)
			return true
		}, 5*time.Minute, 10*time.Second).Should(o.BeTrue(), "Node should have custom pool labels and annotation")

		// Verify the custom MCP has 1 machine
		err = extpriv.NewMachineConfigPool(oc, customMCPName).WaitForMachineCount(1, 2*time.Minute)
		o.Expect(err).NotTo(o.HaveOccurred(), "Custom MCP should have 1 machine")

		exutil.By("Removing custom pool label and verifying node leaves the custom pool")

		// Get the worker pool count before removing the label
		workerMCP, err := machineConfigClient.MachineconfigurationV1().MachineConfigPools().Get(ctx, "worker", metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred(), "Failed to get worker MCP")
		initialWorkerCount := workerMCP.Status.MachineCount
		logger.Infof("Worker pool machine count before label removal: %d", initialWorkerCount)

		expectedLabel := fmt.Sprintf("node-role.kubernetes.io/%s", customMCPName)
		logger.Infof("Removing label %s from node %s", expectedLabel, newNodeName)

		unlabelErr := oc.AsAdmin().Run("label").Args(fmt.Sprintf("node/%s", newNodeName), fmt.Sprintf("%s-", expectedLabel)).Execute()
		o.Expect(unlabelErr).NotTo(o.HaveOccurred(), "Failed to remove custom pool label from node")

		// Wait a bit and verify the node does NOT get the label automatically re-applied
		time.Sleep(30 * time.Second)

		node, err := oc.AsAdmin().KubeClient().CoreV1().Nodes().Get(ctx, newNodeName, metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred(), "Failed to get node")

		// The label should NOT be present (node should not be moved back automatically)
		_, labelExists := node.Labels[expectedLabel]
		o.Expect(labelExists).To(o.BeFalse(), "Custom pool label should not be automatically re-applied after removal")

		// Verify that the node is in the worker pool now and custom pool count is 0
		logger.Infof("Verifying that node %s was removed from the custom pool", newNodeName)

		// Verify the custom MCP count is 0
		customMCPHelper := extpriv.NewMachineConfigPool(oc, customMCPName)
		err = customMCPHelper.WaitForMachineCount(0, 5*time.Minute)
		o.Expect(err).NotTo(o.HaveOccurred(), "Custom MCP should have 0 machines after label removal")

		// Verify that the custom MCP is ready after the node left
		logger.Infof("Verifying that custom MCP %s is ready after node left", customMCPName)
		err = customMCPHelper.WaitForUpdatedStatus()
		o.Expect(err).NotTo(o.HaveOccurred(), "Custom MCP should be ready after node left the pool")

		// Verify that the worker pool went up by 1 after move
		logger.Infof("Verifying that worker pool machine count increased by 1")
		workerMCPHelper := extpriv.NewMachineConfigPool(oc, "worker")
		err = workerMCPHelper.WaitForMachineCount(int(initialWorkerCount)+1, 5*time.Minute)
		o.Expect(err).NotTo(o.HaveOccurred(), "Worker pool should have increased by 1 machine after node moved from custom pool")

		// Verify that the worker pool is ready after accepting the node
		logger.Infof("Verifying that worker pool is ready after accepting the node")
		err = workerMCPHelper.WaitForUpdatedStatus()
		o.Expect(err).NotTo(o.HaveOccurred(), "Worker pool should be ready after accepting the node from custom pool")

		logger.Infof("Verified that node %s is now in the worker pool with custom pool count at 0", newNodeName)
	})
})
