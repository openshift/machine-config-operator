package e2e_2of2_test

import (
	"bytes"
	"context"
	"os/exec"
	"testing"

	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMCNScopeSadPath(t *testing.T) {

	cs := framework.NewClientSet("")

	// Grab two random nodes from different pools, so we don't end up testing and targetting the same node.
	nodeUnderTest := helpers.GetRandomNode(t, cs, "worker")
	targetNode := helpers.GetRandomNode(t, cs, "master")

	// Attempt to patch the MCN owned by targetNode from nodeUnderTest's MCD. This should fail.
	// This oc command effectively use the service account of the nodeUnderTest's MCD pod, which should only be able to edit nodeUnderTest's MCN.
	cmdOutput, err := helpers.ExecCmdOnNodeWithError(cs, nodeUnderTest, "chroot", "/rootfs", "oc", "patch", "machineconfignodes", targetNode.Name, "--type=merge", "-p", "{\"spec\":{\"configVersion\":{\"desired\":\"rendered-worker-test\"}}}")
	require.Error(t, err, "No errors found during failure path :%v", err)
	require.Contains(t, cmdOutput, "updates to MCN "+targetNode.Name+" can only be done from the MCN's owner node")
}

func TestMCNScopeImpersonationPath(t *testing.T) {

	cs := framework.NewClientSet("")

	// Grab a random node from the worker pool
	nodeUnderTest := helpers.GetRandomNode(t, cs, "worker")

	var errb bytes.Buffer
	// Attempt to patch the MCN owned by nodeUnderTest by impersonating the MCD SA. This should fail.
	cmd := exec.Command("oc", "patch", "machineconfignodes", nodeUnderTest.Name, "--type=merge", "-p", "{\"spec\":{\"configVersion\":{\"desired\":\"rendered-worker-test\"}}}", "--as=system:serviceaccount:openshift-machine-config-operator:machine-config-daemon")
	cmd.Stderr = &errb
	err := cmd.Run()
	require.Error(t, err, "No errors found during impersonation path :%v", err)
	require.Contains(t, errb.String(), "this user must have a \"authentication.kubernetes.io/node-name\" claim")

}

func TestMCNScopeHappyPath(t *testing.T) {

	cs := framework.NewClientSet("")

	// Grab a random node from the worker pool
	nodeUnderTest := helpers.GetRandomNode(t, cs, "worker")

	// Attempt to patch the MCN owned by nodeUnderTest from nodeUnderTest's MCD. This should succeed.
	// This oc command effectively use the service account of the nodeUnderTest's MCD pod, which should only be able to edit nodeUnderTest's MCN.
	helpers.ExecCmdOnNode(t, cs, nodeUnderTest, "chroot", "/rootfs", "oc", "patch", "machineconfignodes", nodeUnderTest.Name, "--type=merge", "-p", "{\"spec\":{\"configVersion\":{\"desired\":\"rendered-worker-test\"}}}")
}

// `TestMCNPoolNameDefault` checks that the MCP name is correctly populated in a node's MCN object for default MCPs
func TestMCNPoolNameDefault(t *testing.T) {

	cs := framework.NewClientSet("")

	// Grab a random node from each default pool
	workerNode := helpers.GetRandomNode(t, cs, "worker")
	masterNode := helpers.GetRandomNode(t, cs, "master")

	// Test that MCN pool name value matches MCP association
	workerNodeMCN, workerErr := cs.MachineconfigurationV1Interface.MachineConfigNodes().Get(context.TODO(), workerNode.Name, metav1.GetOptions{})
	require.Equal(t, "worker", workerNodeMCN.Spec.Pool.Name)
	require.NoError(t, workerErr)
	masterNodeMCN, masterErr := cs.MachineconfigurationV1Interface.MachineConfigNodes().Get(context.TODO(), masterNode.Name, metav1.GetOptions{})
	require.Equal(t, "master", masterNodeMCN.Spec.Pool.Name)
	require.NoError(t, masterErr)
}

// `TestMCNPoolNameCustom` checks that the MCP name is correctly populated in a node's MCN object for custom MCPs
func TestMCNPoolNameCustom(t *testing.T) {

	cs := framework.NewClientSet("")

	// Create a custom MCP and assign a worker node to it
	customMCPName := "infra"
	customNode := helpers.GetRandomNode(t, cs, "worker")
	t.Cleanup(helpers.CreatePoolWithNode(t, cs, customMCPName, customNode))

	// Test that MCN pool name value matches MCP association
	customNodeMCN, customErr := cs.MachineconfigurationV1Interface.MachineConfigNodes().Get(context.TODO(), customNode.Name, metav1.GetOptions{})
	require.Equal(t, customMCPName, customNodeMCN.Spec.Pool.Name)
	require.NoError(t, customErr)
}
