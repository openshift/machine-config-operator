package e2e_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

// This test was inspired by the old TestReconcileAfterBadMC test. It checks
// that the Machine Config Controller (MCC) can reconcile after a bad
// MachineConfig. Previously, this test would infer reconciliation based upon
// the state of the node. Now that we're checking for reconciliation in both
// the MCC and the Machine Config Daemon (MCD), it is unlikely that an
// unreconcilable config could make it to the MCD. So instead, we must infer
// this failure and recovery from the MachineConfigPool (MCP) object instead.
func TestMCCReconcileAfterBadMC(t *testing.T) {
	cs := framework.NewClientSet("")

	// create a MC that contains a valid ignition config but is not reconcilable
	mcadd := createMCToAddFile("add-a-file", "/etc/mytestconfs", "test")
	ignCfg, err := ctrlcommon.ParseAndConvertConfig(mcadd.Spec.Config.Raw)
	require.Nil(t, err, "failed to parse ignition config")
	ignCfg.Storage.Disks = []ign3types.Disk{
		{
			Device: "/one",
		},
	}
	rawIgnCfg := helpers.MarshalOrDie(ignCfg)
	mcadd.Spec.Config.Raw = rawIgnCfg

	workerOldMc := helpers.GetMcName(t, cs, "worker")

	// create the dummy MC now
	_, err = cs.MachineConfigs().Create(context.TODO(), mcadd, metav1.CreateOptions{})
	if err != nil {
		t.Errorf("failed to create machine config %v", err)
	}

	// Wait for the MachineConfigPool to degrade
	err = wait.Poll(2*time.Second, 5*time.Minute, func() (bool, error) {
		mcp, err := cs.MachineConfigPools().Get(context.TODO(), "worker", metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		// The MachineConfigPool should not change MachineConfigs since it was
		// unable to reconcile this bad MachineConfig.
		if mcp.Spec.Configuration.Name != workerOldMc {
			return false, fmt.Errorf("expected rendered MachineConfig on pool %s to not change from %q, got %q", mcp.Name, workerOldMc, mcp.Spec.Configuration.Name)
		}

		// Check that the MachineConfigPool degrades.
		if apihelpers.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolRenderDegraded) {
			return true, nil
		}

		return false, nil
	})

	require.NoError(t, err)

	// Next, we delete the bad MachineConfig
	require.NoError(t, cs.MachineConfigs().Delete(context.TODO(), mcadd.Name, metav1.DeleteOptions{}))

	// Wait for the MachineConfigPool to stop being degraded.
	err = wait.Poll(2*time.Second, 5*time.Minute, func() (bool, error) {
		mcp, err := cs.MachineConfigPools().Get(context.TODO(), "worker", metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		// The MachineConfigPool should not change MachineConfigs since it was
		// unable to reconcile this bad MachineConfig.
		if mcp.Spec.Configuration.Name != workerOldMc {
			return false, fmt.Errorf("expected rendered MachineConfig on pool %s to not change from %q, got %q", mcp.Name, workerOldMc, mcp.Spec.Configuration.Name)
		}

		// Check that the MachineConfigPool cleras the degraded status and switches back to updated.
		if apihelpers.IsMachineConfigPoolConditionFalse(mcp.Status.Conditions, mcfgv1.MachineConfigPoolRenderDegraded) &&
			apihelpers.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolUpdated) {
			return true, nil
		}

		return false, nil
	})

	require.NoError(t, err)
}
