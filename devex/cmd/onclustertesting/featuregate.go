package main

import (
	"context"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"

	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
)

func init() {
	featureGateCmd := &cobra.Command{
		Use:   "enable-featuregate",
		Short: "Enables the appropriate feature gates for on=cluster layering to work",
		Long:  "",
		RunE: func(_ *cobra.Command, _ []string) error {
			return enableFeatureGate(framework.NewClientSet(""))
		},
	}

	rootCmd.AddCommand(featureGateCmd)
}

func checkForRequiredFeatureGates(cs *framework.ClientSet, setupOpts opts) error {
	if err := validateFeatureGatesEnabled(cs, "OnClusterBuild"); err != nil {
		if setupOpts.enableFeatureGate {
			return enableFeatureGate(cs)
		}

		prompt := `You may need to enable TechPreview feature gates on your cluster. Try the following: $ oc patch featuregate/cluster --type=merge --patch='{"spec":{"featureSet":"TechPreviewNoUpgrade"}}'`
		klog.Info(prompt)
		klog.Info("Alternatively, rerun this command with the --enable-feature-gate flag")
		return err
	}

	return nil
}

func enableFeatureGate(cs *framework.ClientSet) error {
	fg, err := cs.ConfigV1Interface.FeatureGates().Get(context.TODO(), "cluster", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("could not enable feature gate(s): %w", err)
	}

	fg.Spec.FeatureSet = "TechPreviewNoUpgrade"

	_, err = cs.ConfigV1Interface.FeatureGates().Update(context.TODO(), fg, metav1.UpdateOptions{})
	if err == nil {
		klog.Infof("Enabled FeatureGate %s", fg.Spec.FeatureSet)
	}

	return err
}

// Cribbed from: https://github.com/openshift/machine-config-operator/blob/master/test/helpers/utils.go
func validateFeatureGatesEnabled(cs *framework.ClientSet, requiredFeatureGates ...configv1.FeatureGateName) error {
	currentFeatureGates, err := cs.ConfigV1Interface.FeatureGates().Get(context.TODO(), "cluster", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to fetch feature gates: %w", err)
	}

	// This uses the new Go generics to construct a typed set of
	// FeatureGateNames. Under the hood, sets are map[T]struct{}{} where
	// only the keys matter and one cannot have duplicate keys. Perfect for our use-case!
	enabledFeatures := sets.New[configv1.FeatureGateName]()
	disabledFeatures := sets.New[configv1.FeatureGateName]()

	// Load all of the feature gate names into our set. Duplicates will be
	// automatically be ignored.
	for _, currentFeatureGateDetails := range currentFeatureGates.Status.FeatureGates {
		for _, enabled := range currentFeatureGateDetails.Enabled {
			enabledFeatures.Insert(enabled.Name)
		}

		for _, disabled := range currentFeatureGateDetails.Disabled {
			disabledFeatures.Insert(disabled.Name)
		}
	}

	// If we have all of the required feature gates, we're done!
	if enabledFeatures.HasAll(requiredFeatureGates...) && !disabledFeatures.HasAny(requiredFeatureGates...) {
		klog.Infof("All required feature gates %v are enabled", requiredFeatureGates)
		return nil
	}

	// Now, lets validate that our FeatureGates are just disabled and not unknown.
	requiredFeatures := sets.New[configv1.FeatureGateName](requiredFeatureGates...)
	allFeatures := enabledFeatures.Union(disabledFeatures)
	if !allFeatures.HasAll(requiredFeatureGates...) {
		return fmt.Errorf("unknown FeatureGate(s): %v, available FeatureGate(s): %v", sets.List(requiredFeatures.Difference(allFeatures)), sets.List(allFeatures))
	}

	// If we don't, lets diff against what we have vs. what we want and return that information.
	disabledRequiredFeatures := requiredFeatures.Difference(enabledFeatures)
	return fmt.Errorf("required FeatureGate(s) %v not enabled; have: %v", sets.List(disabledRequiredFeatures), sets.List(enabledFeatures))
}
