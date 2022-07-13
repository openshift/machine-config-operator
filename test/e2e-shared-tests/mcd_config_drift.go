package e2e_shared_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/uuid"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	configDriftSystemdUnitFilename       string = "/etc/systemd/system/unittest.service"
	configDriftSystemdUnitFileContents   string = "unittest-service-unit-contents"
	configDriftSystemdDropinFilename     string = "/etc/systemd/system/unittest.service.d/10-unittest-service.conf"
	configDriftSystemdDropinFileContents string = "unittest-service-dropin-contents"
	configDriftCompressedFilename        string = "/etc/compressed-file"
	configDriftFilename                  string = "/etc/etc-file"
	configDriftFileContents              string = "expected-file-data"
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

	return helpers.NewMachineConfigExtended(
		fmt.Sprintf("%s-%s", configDriftMCPrefix, string(uuid.NewUUID())),
		helpers.MCLabelForRole(c.MCPName),
		[]ign3types.File{
			helpers.CreateEncodedIgn3File(configDriftFilename, configDriftFileContents, 420),
			compressedFile,
		},
		[]ign3types.Unit{
			{
				Name:     "unittest.service",
				Contents: helpers.StrToPtr(configDriftSystemdUnitFileContents),
				Dropins: []ign3types.Dropin{
					{
						Contents: helpers.StrToPtr(configDriftSystemdDropinFileContents),
						Name:     "10-unittest-service.conf",
					},
					{
						Name: "20-unittest-service.conf",
					},
				},
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
func (c configDriftTest) Teardown(t *testing.T) {
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
