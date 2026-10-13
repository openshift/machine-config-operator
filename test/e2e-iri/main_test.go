package e2e_iri_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/openshift/api/features"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	configv1 "github.com/openshift/api/config/v1"
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

func skipIfNoBaremetal(t *testing.T) {
	infra, err := framework.NewClientSet("").Infrastructures().Get(context.Background(), "cluster", v1.GetOptions{})
	require.NoError(t, err)
	if infra.Status.PlatformStatus.Type != configv1.BareMetalPlatformType {
		t.Skip("Skipping non-baremetal platforms")
	}
}

// Currently some tests are not supported in the OpenShift CI
// environment (due the proxy settings)
func skipIfOpenShiftCI(t *testing.T) {
	items := []string{
		// Specific to OpenShift CI.
		"OPENSHIFT_CI",
		// Common to all CI systems.
		"CI",
	}

	for _, item := range items {
		if _, ok := os.LookupEnv(item); ok {
			t.Skip("Skipping OpenShift CI environment")
		}
	}
}
