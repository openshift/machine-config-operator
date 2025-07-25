package e2e_2of2_test

import (
	"os"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	corev1 "k8s.io/api/core/v1"
)

// This test targets the osImageURL functionality by creating a MachineConfig
// that points to a custom OS image containing third-party content. The test
// works like this:
// 1. Ensure that the node does not have the third-party binaries on it.
// 2. Create a new MachineConfig that points to a custom OS image which
// contains those binaries.
// 3. Wait for the node to boot into the new custom OS image.
// 4. Assert that the node now has the binaries in place.
// 5. Revert back to the previous state.
// 6. Wait for the node to roll back.
// 7. Assert that the binaries are no longer present.
func TestOSImageURLOverride(t *testing.T) {
	envVarName := "MCO_OS_IMAGE_URL"

	osImageURL, ok := os.LookupEnv(envVarName)
	if ok && osImageURL != "" {
		t.Logf("%s=%q, will use as custom OS image", envVarName, osImageURL)
	} else {
		t.Skipf("%s not set; skipping!", envVarName)
		return
	}

	cs := framework.NewClientSet("")

	node := helpers.GetRandomNode(t, cs, "worker")

	binaries := []string{
		"/usr/bin/tailscale",
		"/usr/bin/rg",
		"/usr/bin/yq",
	}

	t.Logf("Node %q has not yet booted into the new OS, running pre-test assertions", node.Name)

	assertNodeDoesNotHaveBinaries(t, cs, node, binaries)

	undoFunc := applyCustomOSToNode(t, cs, node, osImageURL, "infra")
	t.Cleanup(undoFunc)

	assertNodeHasBinaries(t, cs, node, binaries)

	// We're done with our test assertions at this point, so lets roll back.
	undoFunc()

	assertNodeDoesNotHaveBinaries(t, cs, node, binaries)
}

func assertNodeHasBinaries(t *testing.T, cs *framework.ClientSet, node corev1.Node, binaries []string) {
	for _, binary := range binaries {
		t.Logf("Checking for presence of: %q", binary)
		helpers.AssertFileOnNode(t, cs, node, binary)
	}
}

func assertNodeDoesNotHaveBinaries(t *testing.T, cs *framework.ClientSet, node corev1.Node, binaries []string) {
	for _, binary := range binaries {
		t.Logf("Checking for absence of: %q", binary)
		helpers.AssertFileNotOnNode(t, cs, node, binary)
	}
}

// Creates a new MachineConfigPool, adds the given node to it, and overrides
// the osImageURL with the provided OS image name.
func applyCustomOSToNode(t *testing.T, cs *framework.ClientSet, node corev1.Node, osImageURL, poolName string) func() {
	// Do a pre-run assertion to ensure that we are not in the new OS image.
	helpers.AssertNodeNotBootedIntoImage(t, cs, node, osImageURL)

	mc := helpers.NewMachineConfig("custom-os-image", helpers.MCLabelForRole(poolName), osImageURL, []ign3types.File{})

	t.Logf("Applying custom OS image %q to node %q", osImageURL, node.Name)

	undoFunc := helpers.CreatePoolAndApplyMCToNode(t, cs, poolName, node, mc)

	// Assert that we've booted into the new custom OS image.
	helpers.AssertNodeBootedIntoImage(t, cs, node, osImageURL)

	t.Logf("Node %q has booted into %q", node.Name, osImageURL)

	return helpers.MakeIdempotent(func() {
		// Roll back the MachineConfig that introduced the custom OS image.
		undoFunc()

		// Assert that rpm-ostree indicates we're not running the custom OS image anymore.
		helpers.AssertNodeNotBootedIntoImage(t, cs, node, osImageURL)

		t.Logf("Node %q has returned to its previous OS image", node.Name)
	})
}
