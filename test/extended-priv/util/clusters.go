package util

import (
	"context"
	"strings"

	o "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	e2eskipper "k8s.io/kubernetes/test/e2e/framework/skipper"
)

// GetInfraID returns the infra id
func GetInfraID(oc *CLI) (string, error) {
	infraID, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("infrastructure", "cluster", "-o", "jsonpath='{.status.infrastructureName}'").Output()
	if err != nil {
		return "", err
	}
	return strings.Trim(infraID, "'"), err
}

// IsTechPreviewNoUpgrade checks if the cluster has TechPreviewNoUpgrade feature set enabled
func IsTechPreviewNoUpgrade(oc *CLI) bool {
	featureGate, err := oc.AdminConfigClient().ConfigV1().FeatureGates().Get(context.Background(), "cluster", metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false
		}
		o.Expect(err).NotTo(o.HaveOccurred(), "could not retrieve feature-gate: %v", err)
	}

	return featureGate.Spec.FeatureSet == configv1.TechPreviewNoUpgrade
}

// IsSingleNodeTopology returns true if the cluster is a SNO cluster
func IsSingleNodeTopology(oc *CLI) bool {
	output, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("infrastructure", "cluster", "-o=jsonpath={.status.platformStatus.controlPlaneTopology}").Output()
	o.Expect(err).NotTo(o.HaveOccurred())
	return output == string(configv1.SingleReplicaTopologyMode)
}

// SkipOnSingleNodeTopology skips the test if the cluster is using single-node topology
func SkipOnSingleNodeTopology(oc *CLI) {
	if IsSingleNodeTopology(oc) {
		e2eskipper.Skipf("This test does not apply to single-node topologies")
	}
}
