package main

import (
	"context"
	"fmt"

	"github.com/openshift/machine-config-operator/devex/internal/pkg/rollout"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/spf13/cobra"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func init() {
	revertCmd := &cobra.Command{
		Use:   "revert",
		Short: "Reverts the changes to the sandbox cluster and rolls back to the stock MCO image.",
		Long:  "",
		RunE:  doRevert,
	}

	rootCmd.AddCommand(revertCmd)
}

func doRevert(_ *cobra.Command, _ []string) error {
	cs := framework.NewClientSet("")

	if err := cs.ImageStreams(ctrlcommon.MCONamespace).Delete(context.TODO(), imagestreamName, metav1.DeleteOptions{}); err != nil && !apierrs.IsNotFound(err) {
		return fmt.Errorf("could not remove imagestream %s: %w", imagestreamName, err)
	}

	if err := rollout.RevertToOriginalMCOImage(cs, false); err != nil {
		return fmt.Errorf("could not revert to original MCO image: %w", err)
	}

	if err := rollout.UnexposeClusterImageRegistry(cs); err != nil {
		return fmt.Errorf("could not unexpose cluster image registry: %w", err)
	}

	return nil
}
