package e2e_sno_test

import (
	"testing"

	e2eShared "github.com/openshift/machine-config-operator/test/e2e-shared-tests"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
)

func TestRunShared(t *testing.T) {
	mcpName := "master"

	cs := framework.NewClientSet("")

	configOpts := e2eShared.ConfigDriftTestOpts{
		MCPName:       mcpName,
		ClientSet:     cs,
		Node:          helpers.GetSingleNodeByRole(t, cs, mcpName),
		SkipForcefile: true,
	}

	sharedOpts := e2eShared.SharedTestOpts{
		ConfigDriftTestOpts:   configOpts,
		MachineConfigTestOpts: e2eShared.MachineConfigTestOpts{},
	}

	e2eShared.Run(t, sharedOpts)
}
