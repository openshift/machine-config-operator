package common

import (
	"context"
	"fmt"
	"time"

	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"k8s.io/klog/v2"
)

func WaitForFeatureGatesReady(ctx context.Context, featureGateAccess featuregates.FeatureGateAccess) error {
	timeout := time.After(1 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return fmt.Errorf("timed out waiting for FeatureGates to be ready")
		default:
			features, err := featureGateAccess.CurrentFeatureGates()
			if err == nil {
				enabled, disabled := GetEnabledDisabledFeatures(features)
				klog.Infof("FeatureGates initialized: enabled=%v, disabled=%v", enabled, disabled)
				return nil
			}
			klog.Infof("Waiting for FeatureGates to be ready...")
			time.Sleep(1 * time.Second)
		}
	}
}

// getEnabledDisabledFeatures extracts enabled and disabled features from the feature gate.
func GetEnabledDisabledFeatures(features featuregates.FeatureGate) ([]string, []string) {
	var enabled []string
	var disabled []string

	for _, feature := range features.KnownFeatures() {
		if features.Enabled(feature) {
			enabled = append(enabled, string(feature))
		} else {
			disabled = append(disabled, string(feature))
		}
	}

	return enabled, disabled
}
