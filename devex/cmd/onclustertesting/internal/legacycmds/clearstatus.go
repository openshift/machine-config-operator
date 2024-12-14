package legacycmds

import (
	"context"
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/devex/internal/pkg/utils"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

var (
	clearStatusOpts struct {
		poolName string
	}
)

func ClearStatusCommand() *cobra.Command {
	clearStatusCmd := &cobra.Command{
		Use:   "clear-build-status",
		Short: "Tears down the pool for on-cluster build testing",
		Long:  "",
		RunE:  runClearStatusCmd,
	}

	clearStatusCmd.PersistentFlags().StringVar(&clearStatusOpts.poolName, "pool", DefaultLayeredPoolName, "Pool name to clear build status on")

	return clearStatusCmd
}

func runClearStatusCmd(_ *cobra.Command, _ []string) error {
	utils.ParseFlags()

	if clearStatusOpts.poolName == "" {
		return fmt.Errorf("no pool name provided")
	}

	return clearBuildStatusesOnPool(framework.NewClientSet(""), clearStatusOpts.poolName)
}

func clearBuildStatusesOnPool(cs *framework.ClientSet, targetPool string) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		mcp, err := cs.MachineConfigPools().Get(context.TODO(), targetPool, metav1.GetOptions{})
		if err != nil {
			return err
		}

		buildConditions := map[mcfgv1.MachineConfigPoolConditionType]struct{}{
			mcfgv1.MachineConfigPoolBuildSuccess: {},
			mcfgv1.MachineConfigPoolBuildFailed:  {},
			mcfgv1.MachineConfigPoolBuildPending: {},
			mcfgv1.MachineConfigPoolBuilding:     {},
		}

		filtered := []mcfgv1.MachineConfigPoolCondition{}
		for _, cond := range mcp.Status.Conditions {
			if _, ok := buildConditions[cond.Type]; !ok {
				filtered = append(filtered, cond)
			}
		}

		mcp.Status.Conditions = filtered
		_, err = cs.MachineConfigPools().UpdateStatus(context.TODO(), mcp, metav1.UpdateOptions{})
		if err != nil {
			return err
		}

		klog.Infof("Cleared build statuses on MachineConfigPool %s", targetPool)
		return nil
	})
}
