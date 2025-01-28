package common

import (
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	v1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/stretchr/testify/assert"
)

func TestMachineOSConfigState(t *testing.T) {
	t.Parallel()

	mosc := NewMachineOSConfigState(&v1.MachineOSConfig{
		Status: v1.MachineOSConfigStatus{
			CurrentImagePullSpec: "registry.host.com/org/repo:tag",
		},
	})

	assert.Equal(t, "registry.host.com/org/repo:tag", mosc.GetOSImage())
}

// This test validates that the MachineOSBuild conditions are correctly
// identified as initial, transient, or terminal.
func TestMachineOSBuildState(t *testing.T) {
	t.Parallel()

	// Determines if the helper associated with the given state returns true
	// while others associated with other states returns false. If an empty state
	// is given, it will not match any of these states and will therefore be
	// false.
	assertBuildState := func(t *testing.T, mosbState *MachineOSBuildState, givenState mcfgv1.BuildProgress) {
		t.Helper()

		states := map[mcfgv1.BuildProgress]func() bool{
			mcfgv1.MachineOSBuildPrepared:    mosbState.IsBuildPrepared,
			mcfgv1.MachineOSBuilding:         mosbState.IsBuilding,
			mcfgv1.MachineOSBuildSucceeded:   mosbState.IsBuildSuccess,
			mcfgv1.MachineOSBuildFailed:      mosbState.IsBuildFailure,
			mcfgv1.MachineOSBuildInterrupted: mosbState.IsBuildInterrupted,
		}

		for state, helper := range states {
			// If the current state matches the given state, then that helper should
			// return true. Otherwise, it should be false.
			if state == givenState {
				assert.True(t, helper())
			} else {
				assert.False(t, helper())
			}
		}

		degradedStates := map[mcfgv1.BuildProgress]struct{}{
			mcfgv1.MachineOSBuildFailed:      struct{}{},
			mcfgv1.MachineOSBuildInterrupted: struct{}{},
		}

		if _, isDegradedState := degradedStates[givenState]; isDegradedState {
			assert.True(t, mosbState.IsAnyDegraded())
		} else {
			assert.False(t, mosbState.IsAnyDegraded())
		}
	}

	// For the initial condition, ensure that it is correctly identified as an
	// initial condition and not a transient or terminal condition.
	t.Run("Initial Conditions", func(t *testing.T) {
		t.Parallel()

		mosbState := NewMachineOSBuildState(&mcfgv1.MachineOSBuild{})
		mosbState.SetBuildConditions(apihelpers.MachineOSBuildInitialConditions())

		assert.True(t, mosbState.IsInInitialState())
		assert.False(t, mosbState.IsInTransientState())
		assert.False(t, mosbState.IsInTerminalState())

		assert.Equal(t, mosbState.GetTerminalState(), mcfgv1.BuildProgress(""))
		assert.Equal(t, mosbState.GetTransientState(), mcfgv1.BuildProgress(""))
		assertBuildState(t, mosbState, mcfgv1.BuildProgress(""))
	})

	// For each transient condition, ensure that it is correctly identified as
	// a transient condition and not an initial or terminal condition.
	// Additionally, check that each helper associated with a given condition
	// correctly identifies that condition as being true and all others false.
	t.Run("Transient Conditions", func(t *testing.T) {
		t.Parallel()

		mosbState := NewMachineOSBuildState(&mcfgv1.MachineOSBuild{})

		for transientState, transientCondition := range MachineOSBuildTransientStates() {
			mosbState.SetBuildConditions(transientCondition)

			assert.False(t, mosbState.IsInInitialState())
			assert.True(t, mosbState.IsInTransientState())
			assert.False(t, mosbState.IsInTerminalState())

			assert.Equal(t, mosbState.GetTransientState(), transientState)
			assert.Equal(t, mosbState.GetTerminalState(), mcfgv1.BuildProgress(""))
			assertBuildState(t, mosbState, transientState)
		}
	})

	// For each terminal condition, ensure that it is correctly identified as a
	// terminal condition and not as a transient or initial condition.
	// Additionally, check that each helper associated with a given condition
	// correctly identifies that condition as being true and all others false.
	t.Run("Terminal Conditions", func(t *testing.T) {
		t.Parallel()

		mosbState := NewMachineOSBuildState(&mcfgv1.MachineOSBuild{})

		for terminalState, terminalCondition := range MachineOSBuildTerminalStates() {
			mosbState.SetBuildConditions(terminalCondition)

			assert.False(t, mosbState.IsInInitialState())
			assert.False(t, mosbState.IsInTransientState())
			assert.True(t, mosbState.IsInTerminalState())

			assert.Equal(t, mosbState.GetTransientState(), mcfgv1.BuildProgress(""))
			assert.Equal(t, mosbState.GetTerminalState(), terminalState)

			assertBuildState(t, mosbState, terminalState)
		}
	})
}
