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

	revertCmd := &cobra.Command{
		Use:   "revert",
		Short: "Reverts the MCO image to the one in the OpenShift release",
		Long:  "",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return revertMCO(cmd.Context(), forceRestart)
		},
	}

	revertCmd.PersistentFlags().BoolVar(&forceRestart, "force", false, "Deletes the pods to forcefully restart the MCO.")

	rootCmd.AddCommand(revertCmd)
}

func revertMCO(ctx context.Context, forceRestart bool) error {
	cs := framework.NewClientSet("")
	if err := rollout.RevertToOriginalMCOImage(ctx, cs, forceRestart); err != nil {
		return err
	}

	klog.Infof("Successfully rolled back to the original MCO image")
	return nil
}
