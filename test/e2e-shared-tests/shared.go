package e2e_shared_test

import (
	"testing"
)

// Options for all shared tests
type SharedTestOpts struct {
	ConfigDriftTestOpts ConfigDriftTestOpts
}

// All shared tests must define this interface
type SharedTest interface {
	Setup(t *testing.T)
	Run(t *testing.T)
	Teardown(t *testing.T)
}

// Runs all of the shared tests
func Run(t *testing.T, opts SharedTestOpts) {
	testCases := []struct {
		name       string
		sharedTest SharedTest
	}{
		{
			name: "MCD Config Drift",
			sharedTest: &configDriftTest{
				ConfigDriftTestOpts: opts.ConfigDriftTestOpts,
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			defer timeIt(t, "Teardown complete", func() {
				t.Log("Starting teardown")
				testCase.sharedTest.Teardown(t)
			})

			timeIt(t, "Setup complete", func() {
				t.Log("Starting setup")
				testCase.sharedTest.Setup(t)
			})

			testCase.sharedTest.Run(t)
		})
	}
}
