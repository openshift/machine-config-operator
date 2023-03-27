package e2e_shared_test

import (
	"testing"
)

// Options for all shared tests
type SharedTestOpts struct {
	ConfigDriftTestOpts   ConfigDriftTestOpts
	MachineConfigTestOpts MachineConfigTestOpts
}

// All shared tests must define this interface
type sharedTest interface {
	Setup(t *testing.T)
	Run(t *testing.T)
	Teardown(t *testing.T)
}

type sharedTestCase struct {
	name       string
	sharedTest sharedTest
}

// Runs all of the shared tests
func Run(t *testing.T, opts SharedTestOpts) {
	testCases := []sharedTestCase{
		{
			name: "MCD Config Drift",
			sharedTest: &configDriftTest{
				ConfigDriftTestOpts: opts.ConfigDriftTestOpts,
			},
		},
		{
			name:       "No Reboot",
			sharedTest: &noRebootTest{},
		},
		{
			name: "MachineConfig",
			sharedTest: &machineConfigTest{
				MachineConfigTestOpts: opts.MachineConfigTestOpts,
			},
		},
		{
			name:       "MCDToken",
			sharedTest: &mcdTokenTest{},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Cleanup(func() {
				timeIt(t, "Teardown complete", func() {
					t.Log("Starting teardown")
					testCase.sharedTest.Teardown(t)
				})
			})

			timeIt(t, "Setup complete", func() {
				t.Log("Starting setup")
				testCase.sharedTest.Setup(t)
			})

			testCase.sharedTest.Run(t)
		})
	}
}
