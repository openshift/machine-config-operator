package helpers

import (
	"context"
	"testing"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Holds a test case for a specific MachineConfig as well as its assertion methods.
type MachineConfigTestCase struct {
	// The name of the test case (will be output as a subtest name).
	Name string
	// The MachineConfig to apply.
	MC *mcfgv1.MachineConfig
	// The assertion function which asserts that the MachineConfig was
	// successfully applied to the node.
	ApplyAssert func(*testing.T, corev1.Node)
	// The rollback assertion function which asserts that the MachineConfig was
	// successfully removed from the node. This should verify that the file
	// contents rolled back, the file was deleted, etc.
	RollbackAssert func(*testing.T, corev1.Node)
	// If this test case should be skipped when testing against OKD, this should
	// be true.
	SkipOnOKD bool
}

// A collection of MachineConfigTestCases
type MachineConfigTestCases []MachineConfigTestCase

// Filters test cases based upon whether this is running against an OKD cluster
// and gets all of the MachineConfigs which will be applied as part of the
// test.
func (m MachineConfigTestCases) filterTestCases(t *testing.T, cs *framework.ClientSet, targetMCPName string) ([]MachineConfigTestCase, []*mcfgv1.MachineConfig) {
	t.Helper()

	isOKD, err := IsOKDCluster(cs)
	require.NoError(t, err)

	testCasesToRun := []MachineConfigTestCase{}
	mcsToAdd := []*mcfgv1.MachineConfig{}
	for _, testCase := range m {
		if isOKD && testCase.SkipOnOKD {
			t.Logf("Skipping %s since we're testing against OKD", testCase.Name)
		} else {
			// Override the role label with the provided pool name so we can ensure
			// that these MachineConfigs will land on the same node.
			testCase.MC.Labels = MCLabelForRole(targetMCPName)

			mcsToAdd = append(mcsToAdd, testCase.MC)
			testCasesToRun = append(testCasesToRun, testCase)
		}

		if testCase.ApplyAssert == nil {
			t.Fatalf("MachineConfigTestCase %q missing ApplyAssert func", testCase.Name)
		}

		if testCase.RollbackAssert == nil {
			t.Fatalf("MachineConfigTestCase %q missing RollbackAssert func", testCase.Name)
		}
	}

	return testCasesToRun, mcsToAdd
}

// Runs all of the test cases against the supplied node. This does so thusly:
// 1. Filters out any test cases which should not be run.
// 2. Gets the current MachineConfig assigned to the MachineConfigPool which the node is part of.
// 3. Creates a new MachineConfigPool, labels the provided node so that it is
// part of that MachineConfigPool, applies the MachineConfigs to that
// MachineConfigPool, and waits for them all to roll out.
// 4. Asserts that the provided node has rolled out the new rendered MachineConfig.
// 5. Runs the apply assertion for each test case.
// 6. Initiates the rollback process by unlabelling the provided node and
// waiting for it to roll back to the initial rendered MachineConfig. This
// process will also remove all of the applied MachineConfigs and the
// MachineConfigPool created for the test.
// 7. Asserts that the rollback was successful by examining the node's current MachineConfig.
// 8. Runs the rollback assertions for each of the supplied test cases (if they exist).
func (m MachineConfigTestCases) Run(t *testing.T, cs *framework.ClientSet, node corev1.Node, targetMCPName string) {
	t.Helper()

	srcPool := "worker"

	if IsSNO(node) {
		// For SNO clusters, we don't create another MachineConfigPool, so we just override this.
		srcPool = "master"
		targetMCPName = srcPool
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	testCasesToRun, mcsToAdd := m.filterTestCases(t, cs, targetMCPName)

	// Get the original worker MCP and rendered MC so we can assert that we've rolled back later.
	mcp, err := cs.MachineconfigurationV1Interface.MachineConfigPools().Get(ctx, srcPool, metav1.GetOptions{})
	require.NoError(t, err)

	originalMC, err := cs.MachineconfigurationV1Interface.MachineConfigs().Get(ctx, mcp.Status.Configuration.Name, metav1.GetOptions{})
	require.NoError(t, err)

	var undoFunc func()

	// Initiate the MachineConfig rollout
	if IsSNO(node) {
		undoFunc = ApplyMCToSNO(t, cs, mcsToAdd)
	} else {
		undoFunc = CreatePoolAndApplyMCToNode(t, cs, targetMCPName, node, mcsToAdd)
	}

	// Note: The undoFunc is idempotent and will only be invoked once. We wire it
	// up to t.Cleanup() here to ensure that it gets run if we don't make it to
	// the rollback phase of the test.
	t.Cleanup(undoFunc)

	// Get the new MCP / rendered MC so that we can assert that we've reached this configuration.
	targetMCP, err := cs.MachineconfigurationV1Interface.MachineConfigPools().Get(ctx, targetMCPName, metav1.GetOptions{})
	require.NoError(t, err)

	targetMC, err := cs.MachineconfigurationV1Interface.MachineConfigs().Get(ctx, targetMCP.Status.Configuration.Name, metav1.GetOptions{})
	require.NoError(t, err)

	// Refresh the node since the annotations have changed.
	updatedNode, err := cs.CoreV1Interface.Nodes().Get(ctx, node.Name, metav1.GetOptions{})
	require.NoError(t, err)

	// Assert that we've rolled to the current config. We do this here to avoid duplicating it across all test cases.
	assert.Equal(t, targetMC.Name, updatedNode.Annotations[constants.CurrentMachineConfigAnnotationKey])
	assert.Equal(t, targetMC.Name, updatedNode.Annotations[constants.DesiredMachineConfigAnnotationKey])
	assert.Equal(t, constants.MachineConfigDaemonStateDone, updatedNode.Annotations[constants.MachineConfigDaemonStateAnnotationKey])

	// Run the apply ssertions for each test case; this verifies that the
	// MachineConfig had the desired effect.
	for _, testCase := range testCasesToRun {
		testCase := testCase

		t.Run(testCase.Name+"/Applied", func(t *testing.T) {
			testCase.ApplyAssert(t, *updatedNode)
		})
	}

	// Initiate the rollback.
	t.Logf("Initiating rollback to %s", originalMC.Name)
	undoFunc()

	// Refresh the node annotations
	updatedNode, err = cs.CoreV1Interface.Nodes().Get(ctx, node.Name, metav1.GetOptions{})
	require.NoError(t, err)

	// Run the rollback assertions for each test case; this verifies that the
	// rollback was successful.
	for _, testCase := range testCasesToRun {
		testCase := testCase
		t.Run(testCase.Name+"/Rollback", func(t *testing.T) {
			testCase.RollbackAssert(t, *updatedNode)
		})
	}

	// Assert that we've rolled back to the old config. We do this here to avoid duplicating it across all test cases.
	assert.Equal(t, originalMC.Name, updatedNode.Annotations[constants.CurrentMachineConfigAnnotationKey])
	assert.Equal(t, originalMC.Name, updatedNode.Annotations[constants.DesiredMachineConfigAnnotationKey])
	assert.Equal(t, constants.MachineConfigDaemonStateDone, updatedNode.Annotations[constants.MachineConfigDaemonStateAnnotationKey])
}
