package main

import (
	"fmt"

	"github.com/openshift/machine-config-operator/hack/internal/pkg/containers"
	"github.com/openshift/machine-config-operator/hack/internal/pkg/rollout"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

type replaceOpts struct {
	validatePullspec bool
	forceRestart     bool
	pullspec         string
}

func init() {
	replaceOpts := replaceOpts{}

	replaceCmd := &cobra.Command{
		Use:   "replace",
		Short: "Replaces the MCO image with the provided container image pullspec",
		Long:  "",
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("no pullspec provided")
			}

			if len(args) > 1 {
				return fmt.Errorf("only one pullspec may be provided")
			}

			replaceOpts.pullspec = args[0]

			return replace(replaceOpts)
		},
	}

	replaceCmd.PersistentFlags().BoolVar(&replaceOpts.validatePullspec, "validate-pullspec", false, "Ensures that the supplied pullspec exists.")
	replaceCmd.PersistentFlags().BoolVar(&replaceOpts.forceRestart, "force", false, "Deletes the pods to forcefully restart the MCO.")

	rootCmd.AddCommand(replaceCmd)
}

func replace(opts replaceOpts) error {
	if opts.validatePullspec {
		digestedPullspec, err := containers.ResolveToDigestedPullspec(opts.pullspec, "")
		if err != nil {
			return fmt.Errorf("could not validate pullspec %s: %w", opts.pullspec, err)
		}

		klog.Infof("Resolved to %s to validate that the pullspec exists", digestedPullspec)
	}

	cs := framework.NewClientSet("")
	if err := rollout.ReplaceMCOImage(cs, opts.pullspec, opts.forceRestart); err != nil {
		return err
	}

	klog.Infof("Successfully replaced the stock MCO image with %s.", opts.pullspec)
	return nil
}
