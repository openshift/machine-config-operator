package e2e_techpreview_test

import (
	"os"
	"testing"

	"github.com/openshift/machine-config-operator/test/helpers"
)

func TestMain(m *testing.M) {

	// Ensure required feature gates are set.
	// Add any new feature gates to the test here, and remove them as features are GAed.
	helpers.MustHaveFeatureGatesEnabled("ManagedBootImages", "OnClusterBuild", "MachineConfigNodes", "NodeDisruptionPolicy")

	os.Exit(m.Run())
}
