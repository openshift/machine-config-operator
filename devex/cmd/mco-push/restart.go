package main

import (
	"context"

	"github.com/openshift/machine-config-operator/devex/internal/pkg/rollout"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

func init() {
	var forceRestart bool

	restartCmd := &cobra.Command{
		Use:   "restart",
		Short: "Restarts all of the MCO pods",
		Long:  "",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return restartMCO(cmd.Context(), forceRestart)
		},
	}

	restartCmd.PersistentFlags().BoolVar(&forceRestart, "force", false, "Deletes the pods to forcefully restart the MCO.")
	rootCmd.AddCommand(restartCmd)
}

func restartMCO(ctx context.Context, forceRestart bool) error {
	cs := framework.NewClientSet("")

	if forceRestart {
		klog.Infof("Will delete pods to force restart")
	}

	if err := rollout.RestartMCO(ctx, cs, forceRestart); err != nil {
		return err
	}

	klog.Infof("Successfully restartd the MCO pods")
	return nil
}
