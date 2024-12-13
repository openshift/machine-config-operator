package main

import (
	"github.com/openshift/machine-config-operator/hack/internal/pkg/rollout"
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
		RunE: func(_ *cobra.Command, _ []string) error {
			return revert(forceRestart)
		},
	}

	revertCmd.PersistentFlags().BoolVar(&forceRestart, "force", false, "Deletes the pods to forcefully restart the MCO.")

	rootCmd.AddCommand(revertCmd)
}

func revert(forceRestart bool) error {
	cs := framework.NewClientSet("")
	if err := rollout.RevertToOriginalMCOImage(cs, forceRestart); err != nil {
		return err
	}

	klog.Infof("Successfully rolled back to the original MCO image")
	return nil
}
