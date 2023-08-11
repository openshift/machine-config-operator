package main

import (
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
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

func init() {
	rootCmd.AddCommand(setStatusCmd)
	setStatusCmd.PersistentFlags().StringVar(&setStatusOpts.poolName, "pool", defaultLayeredPoolName, "Pool name to set build status on")
	setStatusCmd.PersistentFlags().StringVar(&setStatusOpts.condType, "type", "", "The condition type to set")
	setStatusCmd.PersistentFlags().BoolVar(&setStatusOpts.status, "status", false, "Condition true or false")
}

func runSetStatusCmd(_ *cobra.Command, _ []string) {
	common(setStatusOpts)

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
