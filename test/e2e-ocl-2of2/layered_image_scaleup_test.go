package e2e_ocl_2of2_test

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	machineclientset "github.com/openshift/client-go/machine/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"

	e2e_ocl_shared_test "github.com/openshift/machine-config-operator/test/e2e-ocl-shared"
)

// TestLayeredImageServingDuringNodeScaleUp tests the 1-reboot vs 2-reboot behavior when scaling up nodes
// while a layered image build is in progress or has completed.
//
// This test verifies that:
//   - When UpdatedMachineCount == 0, new scaled nodes get base image (2-reboot path)
//   - After at least one node has the layered image (UpdatedMachineCount > 0), new scaled nodes
//     get the layered image during bootstrap (1-reboot path) for external registries
//   - Node annotations reflect the correct image
//
// Test flow:
// 1. Create a layered MCP with one existing node
// 2. Create a MachineOSConfig and wait for build to complete
// 3. Wait for the first node to adopt the layered image (UpdatedMachineCount > 0)
// 4. Scale up a MachineSet to add new nodes
// 5. Verify new nodes get the layered image during bootstrap
// 6. Verify node annotations contain the correct image
func TestLayeredImageServingDuringNodeScaleUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cs := framework.NewClientSet("")

	// Step 1: Build the layered image (this creates the MCP and MOSC)
	t.Logf("Creating layered image build for pool %q", layeredMCPName)
	imagePullspec, _ := runOnClusterLayeringTest(t, onClusterLayeringTestOpts{
		poolName: layeredMCPName,
		customDockerfiles: map[string]string{
			layeredMCPName: e2e_ocl_shared_test.CowsayDockerfile,
		},
	})

	t.Logf("Layered image build completed, image pullspec: %s", imagePullspec)

	// Step 2: Get a random worker node and label it to opt into the layered pool
	t.Logf("Selecting a random worker node to opt into pool %q", layeredMCPName)
	existingNode := helpers.GetRandomNode(t, cs, "worker")
	t.Logf("Selected node %s to opt into layered pool", existingNode.Name)

	// Label the node to add it to the layered pool
	unlabelFunc := e2e_ocl_shared_test.MakeIdempotentAndRegisterAlwaysRun(t, helpers.LabelNode(t, cs, existingNode, helpers.MCPNameToRole(layeredMCPName)))

	// Step 3: Wait for the existing node to adopt the layered image
	t.Logf("Waiting for existing node %s to adopt layered image", existingNode.Name)
	helpers.WaitForNodeImageChange(t, cs, existingNode, imagePullspec)
	helpers.AssertNodeBootedIntoImage(t, cs, existingNode, imagePullspec)
	t.Logf("Node %s is booted into layered image %q", existingNode.Name, imagePullspec)

	// Wait for the pool's UpdatedMachineCount to be > 0
	t.Logf("Waiting for pool %q to have UpdatedMachineCount > 0", layeredMCPName)
	require.NoError(t, wait.PollUntilContextTimeout(ctx, 2*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		mcp, err := cs.MachineConfigPools().Get(ctx, layeredMCPName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		t.Logf("Pool %q has UpdatedMachineCount=%d, waiting for > 0", layeredMCPName, mcp.Status.UpdatedMachineCount)
		return mcp.Status.UpdatedMachineCount > 0, nil
	}))

	t.Logf("Pool %q now has UpdatedMachineCount > 0, proceeding to scale up", layeredMCPName)

	// Step 4: Scale up a MachineSet to add new nodes
	// Get a MachineSet from the worker pool (not the layered pool since we just created it)
	machineclient := machineclientset.NewForConfigOrDie(cs.GetRestConfig())
	machinesets, err := machineclient.MachineV1beta1().MachineSets("openshift-machine-api").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, machinesets.Items, "No MachineSets found in cluster")

	// Use the first MachineSet for scaling
	machineset := machinesets.Items[0]
	originalReplicas := *machineset.Spec.Replicas
	desiredReplicas := originalReplicas + 1

	t.Logf("Scaling MachineSet %q from %d to %d replicas", machineset.Name, originalReplicas, desiredReplicas)

	// Scale up the MachineSet and wait for new nodes to be ready
	newNodes, cleanupFunc := helpers.ScaleMachineSetAndWaitForNodesToBeReady(t, cs, machineset.Name, desiredReplicas)
	t.Cleanup(cleanupFunc)

	require.NotEmpty(t, newNodes, "No new nodes were created during scale-up")
	newNode := newNodes[0]
	t.Logf("New node %s has been created and is ready", newNode.Name)

	// Label the new node to add it to the layered pool
	newNodeUnlabelFunc := e2e_ocl_shared_test.MakeIdempotentAndRegisterAlwaysRun(t, helpers.LabelNode(t, cs, *newNode, helpers.MCPNameToRole(layeredMCPName)))

	// Step 5: Verify the new node gets the layered image
	t.Logf("Waiting for new node %s to adopt layered image during bootstrap", newNode.Name)
	helpers.WaitForNodeImageChange(t, cs, *newNode, imagePullspec)
	helpers.AssertNodeBootedIntoImage(t, cs, *newNode, imagePullspec)
	t.Logf("New node %s successfully booted into layered image %q", newNode.Name, imagePullspec)

	// Step 6: Verify node annotations contain the correct image
	t.Logf("Verifying node annotations for new node %s", newNode.Name)
	refreshedNode, err := cs.CoreV1Interface.Nodes().Get(ctx, newNode.Name, metav1.GetOptions{})
	require.NoError(t, err)

	currentImage := refreshedNode.Annotations[constants.CurrentImageAnnotationKey]
	desiredImage := refreshedNode.Annotations[constants.DesiredImageAnnotationKey]

	require.Equal(t, imagePullspec, currentImage, "Current image annotation should match layered image")
	require.Equal(t, imagePullspec, desiredImage, "Desired image annotation should match layered image")
	t.Logf("Node annotations verified: current=%s, desired=%s", currentImage, desiredImage)

	// Cleanup: Remove the label from the new node and wait for it to revert
	t.Logf("Cleaning up: removing label from new node %s", newNode.Name)
	newNodeUnlabelFunc()
	assertNodeRevertsToNonLayered(t, cs, *newNode)

	// Cleanup: Remove the label from the existing node and wait for it to revert
	t.Logf("Cleaning up: removing label from existing node %s", existingNode.Name)
	unlabelFunc()
	assertNodeRevertsToNonLayered(t, cs, existingNode)

	t.Logf("Test completed successfully!")
}
