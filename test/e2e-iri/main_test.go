package e2e_iri_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/openshift/api/features"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openshift/machine-config-operator/test/framework"
)

func TestMain(m *testing.M) {
	skip, err := skipIRITests()
	if err != nil {
		fmt.Fprintf(os.Stderr, "skip IRI check failed: %v\n", err)
		os.Exit(1)
	}
	if skip {
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func skipIRITests() (bool, error) {
	cs := framework.NewClientSet("")
	ctx := context.Background()

	// Check if NoRegistryClusterInstall feature is enabled.
	fg, err := cs.FeatureGates().Get(ctx, "cluster", v1.GetOptions{})
	if err != nil {
		return true, err
	}
	// Assume only one version has been installed.
	for _, d := range fg.Status.FeatureGates[0].Disabled {
		if d.Name == features.FeatureGateNoRegistryClusterInstall {
			return true, nil
		}
	}

	return false, nil
}
