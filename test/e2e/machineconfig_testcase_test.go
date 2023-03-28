package e2e_test

import (
	"context"
	"testing"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type machineConfigTestCase struct {
	name           string
	mc             *mcfgv1.MachineConfig
	applyAssert    func(*testing.T, corev1.Node)
	rollbackAssert func(*testing.T, corev1.Node)
	skipOnOKD      bool
}

type machineConfigTestCases []machineConfigTestCase

func (m machineConfigTestCases) filterTestCases(t *testing.T, cs *framework.ClientSet, mcpName string) ([]machineConfigTestCase, []*mcfgv1.MachineConfig) {
	isOKD, err := helpers.IsOKDCluster(cs)
	require.NoError(t, err)

	testCasesToRun := []machineConfigTestCase{}
	mcsToAdd := []*mcfgv1.MachineConfig{}
	for _, testCase := range m {
		if isOKD && testCase.skipOnOKD {
			t.Logf("Skipping %s since we're testing against OKD", testCase.name)
		} else {
			// Override the role label with the provided pool name so we can ensure
			// that these MachineConfigs will land on the same node.
			testCase.mc.Labels = helpers.MCLabelForRole(mcpName)

			mcsToAdd = append(mcsToAdd, testCase.mc)
			testCasesToRun = append(testCasesToRun, testCase)
		}
	}

	return testCasesToRun, mcsToAdd
}

func (m machineConfigTestCases) run(t *testing.T, cs *framework.ClientSet, node corev1.Node, mcpName string) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	testCasesToRun, mcsToAdd := m.filterTestCases(t, cs, mcpName)

	// Get the original worker MCP and rendered MC so we can assert that we've rolled back later.
	workerMCP, err := cs.MachineconfigurationV1Interface.MachineConfigPools().Get(ctx, "worker", metav1.GetOptions{})
	require.NoError(t, err)

	workerMC, err := cs.MachineconfigurationV1Interface.MachineConfigs().Get(ctx, workerMCP.Status.Configuration.Name, metav1.GetOptions{})
	require.NoError(t, err)

	// Initiate the MachineConfig rollout
	undoFunc := helpers.CreatePoolAndApplyMCToNode(t, cs, mcpName, node, mcsToAdd)
	t.Cleanup(undoFunc)

	// Get the new MCP / rendered MC so that we can assert that we've reached this configuration.
	targetMCP, err := cs.MachineconfigurationV1Interface.MachineConfigPools().Get(ctx, mcpName, metav1.GetOptions{})
	require.NoError(t, err)

	targetMC, err := cs.MachineconfigurationV1Interface.MachineConfigs().Get(ctx, targetMCP.Status.Configuration.Name, metav1.GetOptions{})
	require.NoError(t, err)

	// Refresh the node since the annotations have changed.
	updatedNode, err := cs.CoreV1Interface.Nodes().Get(ctx, node.Name, metav1.GetOptions{})
	require.NoError(t, err)

	// Assert that we've rolled to the current config. We do this here to avoid duplicating it across all test cases.
	assert.Equal(t, targetMC.Name, updatedNode.Annotations[constants.CurrentMachineConfigAnnotationKey])
	assert.Equal(t, constants.MachineConfigDaemonStateDone, updatedNode.Annotations[constants.MachineConfigDaemonStateAnnotationKey])

	// Run the apply ssertions for each test case; this verifies that the
	// MachineConfig had the desired effect.
	for _, testCase := range testCasesToRun {
		testCase := testCase

		t.Run(testCase.name+"/Applied", func(t *testing.T) {
			testCase.applyAssert(t, *updatedNode)
		})
	}

	// Initiate the rollback.
	t.Logf("Initiating rollback to %s", workerMC.Name)
	undoFunc()

	// Refresh the node annotations
	updatedNode, err = cs.CoreV1Interface.Nodes().Get(ctx, node.Name, metav1.GetOptions{})
	require.NoError(t, err)

	// Run the rollback assertions for each test case; this verifies that the
	// rollback was successful.
	for _, testCase := range testCasesToRun {
		testCase := testCase
		t.Run(testCase.name+"/Rollback", func(t *testing.T) {
			if testCase.rollbackAssert == nil {
				t.Skipf("Undefined rollback assert func for %s; skipping", testCase.name)
			}

			testCase.rollbackAssert(t, *updatedNode)
		})
	}

	// Assert that we've rolled back to the old config. We do this here to avoid duplicating it across all test cases.
	assert.Equal(t, workerMC.Name, updatedNode.Annotations[constants.CurrentMachineConfigAnnotationKey])
	assert.Equal(t, constants.MachineConfigDaemonStateDone, updatedNode.Annotations[constants.MachineConfigDaemonStateAnnotationKey])
}
