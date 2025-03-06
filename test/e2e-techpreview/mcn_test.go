package e2e_techpreview_test

import (
	"bytes"
	"os/exec"
	"testing"

	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"
)

func TestMCNScopeSadPath(t *testing.T) {

	cs := framework.NewClientSet("")

	// Grab two random nodes from different pools, so we don't end up testing and targetting the same node.
	nodeUnderTest := helpers.GetRandomNode(t, cs, "worker")
	targetNode := helpers.GetRandomNode(t, cs, "master")

	// Attempt to patch the MCN owned by targetNode from nodeUnderTest's MCD. This should fail.
	// This oc command effectively use the service account of the nodeUnderTest's MCD pod, which should only be able to edit nodeUnderTest's MCN.
	cmdOutput, err := helpers.ExecCmdOnNodeWithError(t, cs, nodeUnderTest, "chroot", "/rootfs", "oc", "patch", "machineconfignodes", targetNode.Name, "--type=merge", "-p", "{\"spec\":{\"configVersion\":{\"desired\":\"rendered-worker-test\"}}}")
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
