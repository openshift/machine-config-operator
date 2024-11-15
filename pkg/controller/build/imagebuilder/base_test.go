package imagebuilder

import (
	"context"
	"testing"
	"time"

	"github.com/openshift/machine-config-operator/pkg/controller/build/fixtures"
	state "github.com/openshift/machine-config-operator/pkg/controller/common/state"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBase(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	t.Cleanup(cancel)

	kubeclient, mcfgclient, lobj, kubeassert := fixtures.GetClientsForTest(t)
	kubeassert = kubeassert.WithContext(ctx)

	base := newBaseImageBuilder(kubeclient, mcfgclient, lobj.MachineOSBuild, lobj.MachineOSConfig, nil)

	terminalConditions := [][]metav1.Condition{
		base.succeededConditions(),
		base.failedConditions(),
		base.interruptedConditions(),
	}

	for _, terminalCondition := range terminalConditions {
		mosb := lobj.MachineOSBuild.DeepCopy()
		mosb.Status.Conditions = terminalCondition
		mosbState := state.NewMachineOSBuildState(mosb)
		assert.True(t, mosbState.IsInTerminalState())
		assert.False(t, mosbState.IsInTransientState())
		assert.False(t, mosbState.IsInInitialState())
	}

	transientConditions := [][]metav1.Condition{
		base.pendingConditions(),
		base.runningConditions(),
	}

	for _, transientCondition := range transientConditions {
		mosb := lobj.MachineOSBuild.DeepCopy()
		mosb.Status.Conditions = transientCondition
		mosbState := state.NewMachineOSBuildState(mosb)
		assert.True(t, mosbState.IsInTransientState())
		assert.False(t, mosbState.IsInInitialState())
		assert.False(t, mosbState.IsInTerminalState())
	}

	mosb := lobj.MachineOSBuild.DeepCopy()
	mosb.Status.Conditions = base.initialConditions()
	mosbState := state.NewMachineOSBuildState(mosb)
	assert.False(t, mosbState.IsInTransientState())
	assert.True(t, mosbState.IsInInitialState())
	assert.False(t, mosbState.IsInTerminalState())
}
