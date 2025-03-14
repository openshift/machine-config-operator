package e2e_shared_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	kubeErrs "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	configDriftSystemdUnitFilename       string = "/etc/systemd/system/e2etest.service"
	configDriftSystemdUnitFileContents   string = "e2etest-service-unit-contents"
	configDriftSystemdDropinFilename     string = "/etc/systemd/system/e2etest.service.d/10-e2etest-service.conf"
	configDriftSystemdDropinFileContents string = "e2etest-service-dropin-contents"
	configDriftCompressedFilename        string = "/etc/compressed-file"
	configDriftFilename                  string = "/etc/etc-file"
	configDriftFileContents              string = "expected-file-data"
	configDriftCompressedFilenameTwo     string = "/etc/compressed-file-two"
	configDriftFilenameTwo               string = "/etc/etc-file-two"
	configDriftFileContentsTwo           string = "expected-file-data-two"
	configDriftMCPrefix                  string = "mcd-config-drift"
	configDriftMonitorStartupMsg         string = "Config Drift Monitor started"
	configDriftMonitorShutdownMsg        string = "Config Drift Monitor has shut down"
)

// This test does the following:
// 1. Creates a MachineConfig whose application is deferred to the setup
// function. This is because the setup function may need to set up other things
// such as the MachineConfigPool, label the node, etc. This allows reuse in
// both a highly available context as well as single-node context.
// 2. Mutates the file specified by the MachineConfig on the target node so its
// contents do not match what the MachineConfig specifies.
// 3. Creates a new MachineConfig to trigger an update.
// 4. Verifies that the target node and MachineConfigPool become degraded.
// 5. Mutates the file specified by the MachineConfig to make its contents
// equal to that specified by the MachineConfig.
// 6. Verifies that the target node and MachineConfigPool recover from this.
// 7. Again mutates the file specified by the MachineConfig on the target node so its
// contents do not match what the MachineConfig specifies.
// 8. Sets the forcefile on the target node.
// 9. Verifies that the target node and MachineConfigPool recover from this.
//
// The setup function is used to set up the desired MachineConfigPool (if
// needed), apply the config drift MachineConfig, wait for the
// MachineConfigPool to be available, etc.
//
// The teardown function is used to delete the MachineConfigPool (if desired),
// delete the MachineConfig, etc.

// Holds the options used to configure the Config Drift Test
type ConfigDriftTestOpts struct {
	// The target MachineConfigPool name
	MCPName string
	// The setup function
	SetupFunc func(*mcfgv1.MachineConfig)
	// The teardown function
	TeardownFunc func()
	// ClientSet
	ClientSet *framework.ClientSet
	// Skips the forcefile recovery portion
	SkipForcefile bool
}

// Holds the Config Drift Test functions.
type configDriftTest struct {
	ConfigDriftTestOpts

	// The Machine Config generated for this test
	mc *mcfgv1.MachineConfig

	// This is the node this test will target
	node corev1.Node

	// This is the pod this test will target
	pod corev1.Pod

	// This is the Machine Config Pool this test will target
	mcp mcfgv1.MachineConfigPool
}

// Does some basic setup, delegates to the attached SetupFunc, and retrieves
// the target objects for this test.
func (c *configDriftTest) Setup(t *testing.T) {
	if c.SetupFunc == nil {
		t.Fatalf("no setup function")
	}

	if c.TeardownFunc == nil {
		t.Fatalf("no teardown function")
	}

	if c.MCPName == "" {
		t.Fatalf("no machine config pool name")
	}

	// This is the Machine Config that we drift from
	c.mc = c.getMachineConfig(t)

	// Delegate to the attached Setup Func for MachineConfig, MachineConfigPool
	// creation.
	c.SetupFunc(c.mc)

	mcp, err := c.ClientSet.MachineConfigPools().Get(context.TODO(), c.MCPName, metav1.GetOptions{})
	require.Nil(t, err)
	c.mcp = *mcp

	// Get the target node
	c.node = helpers.GetSingleNodeByRole(t, c.ClientSet, c.MCPName)

	// Get the MCD pod
	pod, err := helpers.MCDForNode(c.ClientSet, &c.node)
	require.Nil(t, err)

	c.pod = *pod
}

func (c configDriftTest) getMachineConfig(t *testing.T) *mcfgv1.MachineConfig {
	compressedFile, err := helpers.CreateGzippedIgn3File(configDriftCompressedFilename, configDriftFileContents, 420)
	require.Nil(t, err)

	compressedFileTwo, err := helpers.CreateGzippedIgn3File(configDriftCompressedFilenameTwo, configDriftFileContentsTwo, 420)
	require.Nil(t, err)

	return helpers.NewMachineConfigExtended(
		fmt.Sprintf("%s-%s", configDriftMCPrefix, string(uuid.NewUUID())),
		helpers.MCLabelForRole(c.MCPName),
		nil,
		[]ign3types.File{
			helpers.CreateEncodedIgn3File(configDriftFilename, configDriftFileContents, 420),
			compressedFile,
			helpers.CreateEncodedIgn3File(configDriftFilenameTwo, configDriftFileContentsTwo, 420),
			compressedFileTwo,
		},
		[]ign3types.Unit{
			{
				Name:     "e2etest.service",
				Contents: helpers.StrToPtr(configDriftSystemdUnitFileContents),
				Dropins: []ign3types.Dropin{
					{
						Contents: helpers.StrToPtr(configDriftSystemdDropinFileContents),
						Name:     "10-e2etest-service.conf",
					},
					{
						Name: "20-e2etest-service.conf",
					},
				},
			},
			// Add a masked systemd unit to ensure that we don't inadvertently drift.
			// See: https://issues.redhat.com/browse/OCPBUGS-3909
			{
				Name:     "mask-and-contents.service",
				Contents: helpers.StrToPtr("[Unit]\nDescription=Just random content"),
				Mask:     helpers.BoolToPtr(true),
			},
		},
		[]ign3types.SSHAuthorizedKey{},
		[]string{},
		false,
		[]string{},
		"",
		"",
	)
}

// Tears down the test objects by delegating to the attached TeardownFunc
func (c configDriftTest) Teardown(_ *testing.T) {
	c.TeardownFunc()
}

// Runs the Config Drift Test
func (c configDriftTest) Run(t *testing.T) {
	testCases := []struct {
		name           string
		rebootExpected bool
		testFunc       func(t *testing.T)
	}{
		// 1. Mutates a file on the node.
		// 2. Verifies that we can recover from it by reverting the contents to
		// their original state.
		{
			name:           "revert file content recovery for ignition file",
			rebootExpected: false,
			testFunc: func(t *testing.T) {
				c.runDegradeAndRecoverContentRevert(t, configDriftFilename, configDriftFileContents)
			},
		},
		{
			name:           "revert file content recovery for systemd dropin",
			rebootExpected: false,
			testFunc: func(t *testing.T) {
				c.runDegradeAndRecoverContentRevert(t, configDriftSystemdDropinFilename, configDriftSystemdDropinFileContents)
			},
		},
		{
			name:           "revert file content recovery for systemd unit",
			rebootExpected: false,
			testFunc: func(t *testing.T) {
				c.runDegradeAndRecoverContentRevert(t, configDriftSystemdUnitFilename, configDriftSystemdUnitFileContents)
			},
		},
		// Targets a regression identified by:
		// https://bugzilla.redhat.com/show_bug.cgi?id=2032565
		{
			name:           "revert file content recovery for compressed file",
			rebootExpected: false,
			testFunc: func(t *testing.T) {
				c.runDegradeAndRecoverContentRevert(t, configDriftCompressedFilename, configDriftFileContents)
			},
		},
		// 1. Mutates a file on the node.
		// 2. Creates the forcefile to cause recovery.
		{
			name:           "forcefile recovery",
			rebootExpected: true,
			testFunc: func(t *testing.T) {
				if c.SkipForcefile {
					t.Skip()
				}

				c.runDegradeAndRecover(t, configDriftFilename, configDriftFileContents, func() {
					t.Logf("Setting forcefile to initiate recovery (%s)", constants.MachineConfigDaemonForceFile)
					helpers.ExecCmdOnNode(t, c.ClientSet, c.node, "touch", filepath.Join("/rootfs", constants.MachineConfigDaemonForceFile))
				})
			},
		},
		// Checks that a masked systemd unit does not cause config drift.
		// See: https://issues.redhat.com/browse/OCPBUGS-3909
		{
			name:           "masked systemd unit does not degrade",
			rebootExpected: false,
			testFunc: func(t *testing.T) {
				assertNodeIsInDoneState(t, c.ClientSet, c.node)
			},
		},
		// Test the ability of the mcd to change the degraded state message if it updated.
		// 1. Mutate a file on the node.
		// 2. Recover from Degradation by reverting the change.
		// 3. Mutate a second file on the node with different content.
		// 4. While still degraded, mutate the first file again. The degradation message should be due to the most recent change (the first file).
		// 5. Revert all files.
		// https://bugzilla.redhat.com/show_bug.cgi?id=2104978
		{
			name:           "update degraded reason on drift",
			rebootExpected: false,
			testFunc: func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				mutateFileOnNode(t, c.ClientSet, c.node, configDriftFilename, "not-the-data")
				assertNodeAndMCPIsDegraded(t, c.ClientSet, c.node, c.mcp, configDriftFilename)

				expectedReason := fmt.Sprintf("content mismatch for file %q", configDriftFilename)

				mutateFileOnNode(t, c.ClientSet, c.node, configDriftFilename, configDriftFileContents)
				assertNodeAndMCPIsRecovered(t, c.ClientSet, c.node, c.mcp)

				mutateFileOnNode(t, c.ClientSet, c.node, configDriftFilenameTwo, "incorrect data 2")

				mutateFileOnNode(t, c.ClientSet, c.node, configDriftFilename, "not-the-data")
				assertNodeAndMCPIsDegraded(t, c.ClientSet, c.node, c.mcp, configDriftFilename)

				node, err := c.ClientSet.CoreV1Interface.Nodes().Get(ctx, c.node.Name, metav1.GetOptions{})
				require.Nil(t, err)

				assert.Contains(t, node.Annotations[constants.MachineConfigDaemonReasonAnnotationKey], expectedReason)

				assertPoolReachesState(t, c.ClientSet, c.mcp, isPoolDegraded)

				mcp, err := c.ClientSet.MachineConfigPools().Get(ctx, c.mcp.Name, metav1.GetOptions{})
				require.Nil(t, err)
				for _, condition := range mcp.Status.Conditions {
					if condition.Type == mcfgv1.MachineConfigPoolNodeDegraded {
						assert.Contains(t, condition.Message, configDriftFilename)
					}
				}

				mutateFileOnNode(t, c.ClientSet, c.node, configDriftFilename, configDriftFileContents)
				mutateFileOnNode(t, c.ClientSet, c.node, configDriftFilenameTwo, configDriftFileContentsTwo)
				assertNodeAndMCPIsRecovered(t, c.ClientSet, c.node, c.mcp)
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			// Get the node's uptime to check for reboots.
			initialUptime := helpers.GetNodeUptime(t, c.ClientSet, c.node)

			// With the way that the Config Drift Monitor is wired into the MCD,
			// "machineconfiguration.openshift.io/state" gets set to "Done" before the
			// Config Drift Monitor is started.
			waitForConfigDriftMonitorStart(t, c.ClientSet, c.node)

			testCase.testFunc(t)

			// Verify our reboot expectations
			if testCase.rebootExpected {
				helpers.AssertNodeReboot(t, c.ClientSet, c.node, initialUptime)
			} else {
				helpers.AssertNodeNotReboot(t, c.ClientSet, c.node, initialUptime)
			}
		})
	}
}

func isPoolDegraded(m mcfgv1.MachineConfigPool) bool {
	trueConditions := []mcfgv1.MachineConfigPoolConditionType{
		mcfgv1.MachineConfigPoolDegraded,
		mcfgv1.MachineConfigPoolNodeDegraded,
		mcfgv1.MachineConfigPoolUpdating,
	}

	falseConditions := []mcfgv1.MachineConfigPoolConditionType{
		mcfgv1.MachineConfigPoolRenderDegraded,
		mcfgv1.MachineConfigPoolUpdated,
	}

	return m.Status.DegradedMachineCount == 1 &&
		allMCPConditionsTrue(trueConditions, m) &&
		allMCPConditionsFalse(falseConditions, m)
}

func (c configDriftTest) runDegradeAndRecoverContentRevert(t *testing.T, filename, expectedContents string) {
	c.runDegradeAndRecover(t, filename, expectedContents, func() {
		t.Logf("Reverting %s to expected contents to initiate recovery", filename)
		mutateFileOnNode(t, c.ClientSet, c.node, filename, expectedContents)
	})
}

func (c configDriftTest) runDegradeAndRecover(t *testing.T, filename, expectedFileContents string, recoverFunc func()) {
	mutateFileOnNode(t, c.ClientSet, c.node, filename, "not-the-data")
	defer mutateFileOnNode(t, c.ClientSet, c.node, filename, expectedFileContents)

	// Ensure that the node and MCP reach a degraded state before we recover.
	assertNodeAndMCPIsDegraded(t, c.ClientSet, c.node, c.mcp, filename)

	// Run the recovery function.
	recoverFunc()

	// Verify that the node and MCP recover.
	assertNodeAndMCPIsRecovered(t, c.ClientSet, c.node, c.mcp)
}

func mutateFileOnNode(t *testing.T, cs *framework.ClientSet, node corev1.Node, filename, contents string) {
	t.Helper()

	if !strings.HasPrefix(filename, "/rootfs") {
		filename = filepath.Join("/rootfs", filename)
	}

	bashCmd := fmt.Sprintf("printf '%s' > %s", contents, filename)
	t.Logf("Setting contents of %s on %s to %s", filename, node.Name, contents)

	helpers.ExecCmdOnNode(t, cs, node, "/bin/bash", "-c", bashCmd)
}

func assertNodeAndMCPIsDegraded(t *testing.T, cs *framework.ClientSet, node corev1.Node, mcp mcfgv1.MachineConfigPool, filename string) {
	t.Helper()

	logEntry := fmt.Sprintf("content mismatch for file %q", filename)

	// Assert that the node eventually reaches a Degraded state and has the
	// config mismatch as the reason
	t.Log("Verifying node becomes degraded due to config mismatch")

	assertNodeReachesState(t, cs, node, func(n corev1.Node) bool {
		isDegraded := n.Annotations[constants.MachineConfigDaemonStateAnnotationKey] == string(constants.MachineConfigDaemonStateDegraded)
		hasReason := strings.Contains(n.Annotations[constants.MachineConfigDaemonReasonAnnotationKey], logEntry)
		return isDegraded && hasReason
	})

	mcdPod, err := helpers.MCDForNode(cs, &node)
	require.Nil(t, err)

	helpers.AssertMCDLogsContain(t, cs, mcdPod, &node, logEntry)

	// Assert that the MachineConfigPool eventually reaches a degraded state and has the config mismatch as the reason.
	t.Log("Verifying MachineConfigPool becomes degraded due to config mismatch")

	assertPoolReachesState(t, cs, mcp, isPoolDegraded)
}

func assertNodeReachesState(t *testing.T, cs *framework.ClientSet, target corev1.Node, stateFunc func(corev1.Node) bool) {
	t.Helper()

	maxWait := 5 * time.Minute

	end, err := pollForResourceState(maxWait, func() (bool, error) {
		node, err := cs.CoreV1Interface.Nodes().Get(context.TODO(), target.Name, metav1.GetOptions{})
		return stateFunc(*node), err
	})

	if err != nil {
		t.Fatalf("Node %s did not reach expected state (took %v): %s", target.Name, end, err)
	}

	t.Logf("Node %s reached expected state (took %v)", target.Name, end)
}

func assertPoolReachesState(t *testing.T, cs *framework.ClientSet, target mcfgv1.MachineConfigPool, stateFunc func(mcfgv1.MachineConfigPool) bool) {
	t.Helper()

	maxWait := 5 * time.Minute

	end, err := pollForResourceState(maxWait, func() (bool, error) {
		mcp, err := cs.MachineConfigPools().Get(context.TODO(), target.Name, metav1.GetOptions{})
		return stateFunc(*mcp), err
	})

	if err != nil {
		t.Fatalf("MachineConfigPool %s did not reach expected state (took %v): %s", target.Name, end, err)
	}

	t.Logf("MachineConfigPool %s reached expected state (took %v)", target.Name, end)
}

func pollForResourceState(timeout time.Duration, pollFunc func() (bool, error)) (time.Duration, error) {
	// This wraps wait.PollImmediate() for the following reason:
	//
	// If the control plane is temporarily unavailable (e.g., when running in a
	// single-node OpenShift (SNO) context and the node reboots), this error will
	// not be nil, but *should* go back to nil once the control-plane becomes
	// available again. To handle that, we:
	//
	// 1. Store the error within the pollForResourceState scope.
	// 2. Run the clock out.
	// 3. Handle the error (if it does not go back to nil) when the timeout is reached.
	//
	// This was inspired by and is a more generic implementation of:
	// https://github.com/openshift/machine-config-operator/blob/master/test/e2e-single-node/sno_mcd_test.go#L355-L374
	start := time.Now()

	var lastErr error

	ctx := context.Background()

	waitErr := wait.PollUntilContextTimeout(ctx, 1*time.Second, timeout, true, func(_ context.Context) (bool, error) {
		result, err := pollFunc()
		lastErr = err
		return result, nil
	})

	return time.Since(start), kubeErrs.NewAggregate([]error{
		lastErr,
		waitErr,
	})
}

func allMCPConditionsTrue(conditions []mcfgv1.MachineConfigPoolConditionType, mcp mcfgv1.MachineConfigPool) bool {
	for _, condition := range conditions {
		if !apihelpers.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, condition) {
			return false
		}
	}

	return true
}

func allMCPConditionsFalse(conditions []mcfgv1.MachineConfigPoolConditionType, mcp mcfgv1.MachineConfigPool) bool {
	for _, condition := range conditions {
		if !apihelpers.IsMachineConfigPoolConditionFalse(mcp.Status.Conditions, condition) {
			return false
		}
	}

	return true
}
