package build

import (
	"context"
	"fmt"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/controller/build/fixtures"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestValidateOnClusterBuildConfig(t *testing.T) {
	t.Parallel()

	newMosc := func() *mcfgv1.MachineOSConfig {
		lobj := fixtures.NewObjectsForTest("worker")
		return lobj.MachineOSConfig
	}

	testCases := []struct {
		name            string
		errExpected     bool
		secretsToDelete []string
		mosc            func() *mcfgv1.MachineOSConfig
	}{
		{
			name: "happy path",
			mosc: newMosc,
		},
		{
			name:            "missing secret",
			secretsToDelete: []string{"final-image-push-secret"},
			mosc:            newMosc,
			errExpected:     true,
		},
		{
			name: "missing MachineOSConfig",
			mosc: func() *mcfgv1.MachineOSConfig {
				mosc := newMosc()
				mosc.Name = "other-machineosconfig"
				mosc.Spec.MachineConfigPool.Name = "other-machineconfigpool"
				return mosc
			},
			errExpected: true,
		},
		{
			name: "malformed image pullspec",
			mosc: func() *mcfgv1.MachineOSConfig {
				mosc := newMosc()
				mosc.Spec.RenderedImagePushSpec = "malformed-image-pullspec"
				return mosc
			},
			errExpected: true,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			kubeclient, mcfgclient, _, _, lobj, _ := fixtures.GetClientsForTest(t)
			_, err := mcfgclient.MachineconfigurationV1().MachineOSConfigs().Create(context.TODO(), testCase.mosc(), metav1.CreateOptions{})
			require.NoError(t, err)

			for _, secret := range testCase.secretsToDelete {
				err := kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Delete(context.TODO(), secret, metav1.DeleteOptions{})
				require.NoError(t, err)
			}

			err = ValidateOnClusterBuildConfig(kubeclient, mcfgclient, []*mcfgv1.MachineConfigPool{lobj.MachineConfigPool})
			if testCase.errExpected {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// This test validates that we have correctly identified if the MachineOSBuild
// should be updated based upon comparing the old and current status of the
// MachineOSBuild. It is worth noting that the current MachineOSBuild status
// can come from the imagebuilder.MachineOSBuildStatus() method which maps the
// current job state to the MachineOSBuild state.
func TestIsMachineOSBuildStatusUpdateNeeded(t *testing.T) {
	t.Parallel()

	initialConditions := func() map[mcfgv1.BuildProgress][]metav1.Condition {
		return map[mcfgv1.BuildProgress][]metav1.Condition{
			// This value is not part of the OCL API and is here solely for testing purposes.
			"Initial": apihelpers.MachineOSBuildInitialConditions(),
		}
	}

	testCases := []struct {
		name     string
		old      map[mcfgv1.BuildProgress][]metav1.Condition
		current  map[mcfgv1.BuildProgress][]metav1.Condition
		expected bool
	}{
		// These are valid state transitions. In other words, when one of these
		// state transitions is identified, the MachineOSBuild status object should
		// be updated.
		{
			name:     "Initial -> Terminal",
			old:      initialConditions(),
			current:  ctrlcommon.MachineOSBuildTerminalStates(),
			expected: true,
		},
		{
			name:     "Initial -> Transient",
			old:      initialConditions(),
			current:  ctrlcommon.MachineOSBuildTransientStates(),
			expected: true,
		},
		{
			name:     "Transient -> Terminal",
			old:      ctrlcommon.MachineOSBuildTransientStates(),
			current:  ctrlcommon.MachineOSBuildTerminalStates(),
			expected: true,
		},
		{
			name: "Pending -> Running",
			old: map[mcfgv1.BuildProgress][]metav1.Condition{
				mcfgv1.MachineOSBuildPrepared: ctrlcommon.MachineOSBuildTransientStates()[mcfgv1.MachineOSBuildPrepared],
			},
			current: map[mcfgv1.BuildProgress][]metav1.Condition{
				mcfgv1.MachineOSBuilding: ctrlcommon.MachineOSBuildTransientStates()[mcfgv1.MachineOSBuilding],
			},
			expected: true,
		},
		// These are invalid state transitions. In other words, when one of these
		// state transitions is observed, the MachineOSBuild object should not be
		// updated because they are invalid and make no sense.
		{
			name:     "Terminal -> Initial",
			old:      ctrlcommon.MachineOSBuildTerminalStates(),
			current:  initialConditions(),
			expected: false,
		},
		{
			name:     "Transient -> Initial",
			old:      ctrlcommon.MachineOSBuildTransientStates(),
			current:  initialConditions(),
			expected: false,
		},
		{
			name:     "Initial -> Initial",
			old:      initialConditions(),
			current:  initialConditions(),
			expected: false,
		},
		{
			name:     "Terminal -> Terminal",
			old:      ctrlcommon.MachineOSBuildTerminalStates(),
			current:  ctrlcommon.MachineOSBuildTerminalStates(),
			expected: false,
		},
		{
			name: "Running -> Pending",
			old: map[mcfgv1.BuildProgress][]metav1.Condition{
				mcfgv1.MachineOSBuilding: ctrlcommon.MachineOSBuildTransientStates()[mcfgv1.MachineOSBuilding],
			},
			current: map[mcfgv1.BuildProgress][]metav1.Condition{
				mcfgv1.MachineOSBuildPrepared: ctrlcommon.MachineOSBuildTransientStates()[mcfgv1.MachineOSBuildPrepared],
			},
			expected: false,
		},
	}

	for _, testCase := range testCases {
		for oldName, old := range testCase.old {
			for currentName, current := range testCase.current {
				t.Run(fmt.Sprintf("%s: %s -> %s", testCase.name, oldName, currentName), func(t *testing.T) {
					oldStatus := mcfgv1.MachineOSBuildStatus{
						Conditions: old,
					}

					curStatus := mcfgv1.MachineOSBuildStatus{
						Conditions: current,
					}

					result, reason := isMachineOSBuildStatusUpdateNeeded(oldStatus, curStatus)

					if testCase.expected {
						assert.True(t, result, reason)
					} else {
						assert.False(t, result, reason)
					}
				})
			}
		}
	}
}
