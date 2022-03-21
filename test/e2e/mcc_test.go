package e2e_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Test case to make sure the MCC alerts properly when pools are
// paused and a certificate rotation happens
func TestMCCPausedKubeletCAAlert(t *testing.T) {
	var testPool = "master"

	cs := framework.NewClientSet("")

	// Get the machine config pool
	mcp, err := cs.MachineConfigPools().Get(context.TODO(), testPool, metav1.GetOptions{})
	require.Nil(t, err)

	// Update our copy of the pool
	newMcp := mcp.DeepCopy()
	newMcp.Spec.Paused = true

	t.Logf("Pausing pool")

	// Update the pool to be paused
	_, err = cs.MachineConfigPools().Update(context.TODO(), newMcp, metav1.UpdateOptions{})
	require.Nil(t, err)
	t.Logf("Paused")

	// Rotate the certificates
	t.Logf("Patching certificate")
	err = helpers.ForceKubeApiserverCertificateRotation(cs)
	require.Nil(t, err)
	t.Logf("Patched")

	// Verify the pool is paused and the config is pending but not rolling out
	t.Logf("Waiting for rendered config to get stuck behind pause")
	err = helpers.WaitForPausedConfig(t, cs, testPool)
	require.Nil(t, err)
	t.Logf("Certificate stuck behind paused (as expected)")

	// Retrieve the token from the prometheus secret, we need it to auth to the oauth proxy
	t.Logf("Getting monitoring token")
	token, err := helpers.GetMonitoringToken(t, cs)
	require.Nil(t, err)

	// Get the service details so we can ge the IP and port to check for the metrics
	t.Logf("Getting metrics listener service details")
	svc, err := cs.Services("openshift-machine-config-operator").Get(context.TODO(), "machine-config-controller", metav1.GetOptions{})
	require.Nil(t, err)

	// Extract the IP and port and build the URL
	requestTarget := svc.Spec.ClusterIP
	requestPort := svc.Spec.Ports[0].Port
	url := fmt.Sprintf("https://%s:%d/metrics", requestTarget, requestPort)

	// Get a node to execute on (we really just need cluster network access, it doesn't matter what pod)
	checkNode, err := helpers.GetNodesByRole(cs, testPool)
	require.Nil(t, err)

	// Run our curl command inside the pod to check metrics
	out := helpers.ExecCmdOnNode(t, cs, checkNode[0], []string{"curl", "-s", "-k", "-H", "Authorization: Bearer " + string(token), url}...)

	// The /metrics output will contain the metric if it works
	if !strings.Contains(out, `machine_config_controller_paused_pool_kubelet_ca{pool="`+testPool+`"} 1`) {
		t.Errorf("Metric should have been set after configuration was paused, but it was NOT")
	} else {
		t.Log("Metric successfully set")
	}

	// Get the pool again so we can update it back to unpaosed
	mcp2, err := cs.MachineConfigPools().Get(context.TODO(), testPool, metav1.GetOptions{})
	require.Nil(t, err)

	// Set it back to unpaused
	newMcp2 := mcp2.DeepCopy()
	newMcp2.Spec.Paused = false

	t.Logf("Unpausing pool\n")
	// Perform the update
	_, err = cs.MachineConfigPools().Update(context.TODO(), newMcp2, metav1.UpdateOptions{})
	require.Nil(t, err)
	t.Logf("Waiting for config to sync after unpause...")

	// Wait for the pools to settle again
	err = helpers.WaitForPoolCompleteAny(t, cs, testPool)
	require.Nil(t, err)

	t.Logf("Unpaused + Synced")

	t.Logf("Checking for metric value...")

	// Get a node again so we can check if the metric shut off
	checkNode, err = helpers.GetNodesByRole(cs, testPool)
	require.Nil(t, err)

	out = helpers.ExecCmdOnNode(t, cs, checkNode[0], []string{"curl", "-s", "-k", "-H", "Authorization: Bearer " + string(token), url}...)
	// The /metrics output will contain the metric if it works
	if !strings.Contains(out, `machine_config_controller_paused_pool_kubelet_ca{pool="master"} 0`) {
		t.Errorf("Metric should be zero after pool unpaused, but it was NOT")
	} else {
		t.Logf("Metric has correctly been reset after pool unpaused")
	}
}
