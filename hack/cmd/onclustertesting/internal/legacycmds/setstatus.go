package legacycmds

import (
	"context"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/hack/internal/pkg/utils"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

var (
	setStatusCmd = &cobra.Command{
		Use:   "set-build-status",
		Short: "Sets the build status on a given MachineConfigPool",
		Long:  "",
		Run:   runSetStatusCmd,
	}

	setStatusOpts struct {
		poolName string
		condType string
		status   bool
	}
)

func SetStatusCommand() *cobra.Command {
	setStatusCmd := &cobra.Command{
		Use:   "set-build-status",
		Short: "Sets the build status on a given MachineConfigPool",
		Long:  "",
		Run:   runSetStatusCmd,
	}

	setStatusCmd.PersistentFlags().StringVar(&setStatusOpts.poolName, "pool", DefaultLayeredPoolName, "Pool name to set build status on")
	setStatusCmd.PersistentFlags().StringVar(&setStatusOpts.condType, "type", "", "The condition type to set")
	setStatusCmd.PersistentFlags().BoolVar(&setStatusOpts.status, "status", false, "Condition true or false")

	return setStatusCmd
}

func runSetStatusCmd(_ *cobra.Command, _ []string) {
	utils.ParseFlags()

	if setStatusOpts.poolName == "" {
		klog.Fatalln("No pool name provided!")
	}

	validCondTypes := []mcfgv1.MachineConfigPoolConditionType{
		mcfgv1.MachineConfigPoolUpdated,
		mcfgv1.MachineConfigPoolUpdating,
		mcfgv1.MachineConfigPoolNodeDegraded,
		mcfgv1.MachineConfigPoolRenderDegraded,
		mcfgv1.MachineConfigPoolDegraded,
		mcfgv1.MachineConfigPoolBuildPending,
		mcfgv1.MachineConfigPoolBuilding,
		mcfgv1.MachineConfigPoolBuildSuccess,
		mcfgv1.MachineConfigPoolBuildFailed,
	}

	var condTypeToSet mcfgv1.MachineConfigPoolConditionType
	for _, condType := range validCondTypes {
		if string(condType) == setStatusOpts.condType {
			condTypeToSet = mcfgv1.MachineConfigPoolConditionType(setStatusOpts.condType)
			break
		}
	}

	if condTypeToSet == "" {
		klog.Fatalf("unknown condition type %q, valid options: %v", setStatusOpts.condType, validCondTypes)
	}

	status := map[bool]corev1.ConditionStatus{
		true:  corev1.ConditionTrue,
		false: corev1.ConditionFalse,
	}

	if err := setStatusOnPool(framework.NewClientSet(""), setStatusOpts.poolName, condTypeToSet, status[setStatusOpts.status]); err != nil {
		klog.Fatal(err)
	}
}

func setStatusOnPool(cs *framework.ClientSet, targetPool string, condType mcfgv1.MachineConfigPoolConditionType, status corev1.ConditionStatus) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		mcp, err := cs.MachineConfigPools().Get(context.TODO(), targetPool, metav1.GetOptions{})
		if err != nil {
			return err
		}

		newCond := apihelpers.NewMachineConfigPoolCondition(condType, status, "", "")
		apihelpers.SetMachineConfigPoolCondition(&mcp.Status, *newCond)

		_, err = cs.MachineConfigPools().UpdateStatus(context.TODO(), mcp, metav1.UpdateOptions{})
		if err != nil {
			return err
		}

		klog.Infof("Set %s / %s on %s", condType, status, targetPool)

		return nil
	})
}
