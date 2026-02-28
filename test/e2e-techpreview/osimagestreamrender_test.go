package e2e_techpreview

import (
	"context"
	"testing"
	"time"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	"github.com/stretchr/testify/require"

	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
)

// This test sets the OSImageStream name on a MachineConfigPool, then it
// creates a MachineConfig which overrides OSImageURL. The test ensures that
// the MachineConfigPool becomes degraded in this scenario and that recovery
// from this state is possible.
func TestOSImageStreamOSImageURL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute*5)
	t.Cleanup(cancel)

	cs := framework.NewClientSet("")

	testCases := []struct {
		// Human-friendly name of the test
		name string
		// Name of the object(s) to create for the test
		objName string
		// The test function to execute.
		testFunc func(*testing.T, string)
	}{
		{
			name:    "Delete overriding MachineConfig",
			objName: "osimageurl-delete",
			testFunc: func(t *testing.T, objName string) {
				require.NoError(t, cs.MachineconfigurationV1Interface.MachineConfigs().Delete(ctx, objName, metav1.DeleteOptions{}))
				t.Logf("Deleted MachineConfig %s which overrides OSImageURL", objName)
			},
		},
		{
			name:    "Clear OSImageStream name from MachineConfigPool",
			objName: "osimageurl-mcp-clear",
			testFunc: func(t *testing.T, objName string) {
				require.NoError(t, setOSImageStreamOnMachineConfigPool(ctx, cs, objName, ""))
				t.Logf("Cleared OSImageStream from MachineConfigPool %s", objName)
			},
		},
		{
			name:    "Clear OSImageURL override from MachineConfig",
			objName: "osimageurl-mc-clear",
			testFunc: func(t *testing.T, objName string) {
				mc, err := cs.MachineconfigurationV1Interface.MachineConfigs().Get(ctx, objName, metav1.GetOptions{})
				require.NoError(t, err)

				mc.Spec.OSImageURL = ""

				_, err = cs.MachineconfigurationV1Interface.MachineConfigs().Update(ctx, mc, metav1.UpdateOptions{})
				require.NoError(t, err)
				t.Logf("Cleared OSImageURL from MachineConfig %s", objName)
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			mcName := testCase.objName
			poolName := testCase.objName

			// Create the MachineConfigPool.
			t.Cleanup(helpers.CreateMCP(t, cs, poolName))

			// Set the OSImageStream name on the MachineConfigPool.
			require.NoError(t, setOSImageStreamOnMachineConfigPool(ctx, cs, poolName, "rhel-9"))

			// Wait for the pool to render its first MachineConfig.
			helpers.WaitForRenderedConfig(t, cs, poolName, "00-worker")

			// Create and apply the MachineConfig which overrides OSImageURL.
			require.NoError(t, createAndApplyMC(ctx, t, cs, mcName))

			// Wait for the MachineConfigPool to degrade.
			start := time.Now()
			require.NoError(t, waitForPoolDegradation(ctx, cs, poolName))
			t.Logf("Pool %q has reached expected degraded state after %s", poolName, time.Since(start))

			// Execute the test function.
			testCase.testFunc(t, testCase.objName)

			// Wait for the MachineConfigPool to lose the degraded status.
			start = time.Now()
			require.NoError(t, helpers.WaitForPoolCompleteAny(t, cs, poolName))
			t.Logf("Pool %q has cleared the degraded state after %s", poolName, time.Since(start))
		})
	}
}

// Cleanup needs to tolerate the MachineConfig being missing, since one of the
// test cases deletes the MachineConfig. The helpers.ApplyMC() function does
// not offer that functionality, hence this function.
func createAndApplyMC(ctx context.Context, t *testing.T, cs *framework.ClientSet, mcName string) error {
	pullspec := "quay.io/org/repo@sha256:6ae587dc7f5ae2d836df80ee85118c374478408aa439aafd44ab8ab877fc41cd"

	mc := helpers.NewMachineConfig(mcName, helpers.MCLabelForRole(mcName), pullspec, []ign3types.File{})
	helpers.SetMetadataOnObject(t, mc)

	_, err := cs.MachineconfigurationV1Interface.MachineConfigs().Create(ctx, mc, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	t.Cleanup(func() {
		err := cs.MachineconfigurationV1Interface.MachineConfigs().Delete(ctx, mc.Name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			t.Fatalf("%s", err)
		}
	})

	return nil
}

// Sets the OSImageStream name on the given MachineConfigPool. Can also be used to clear this value.
func setOSImageStreamOnMachineConfigPool(ctx context.Context, cs *framework.ClientSet, mcpName, osImageStreamName string) error {
	mcp, err := cs.MachineconfigurationV1Interface.MachineConfigPools().Get(ctx, mcpName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	mcp.Spec.OSImageStream.Name = osImageStreamName

	_, err = cs.MachineconfigurationV1Interface.MachineConfigPools().Update(ctx, mcp, metav1.UpdateOptions{})
	return err
}

// Waits for the MachineConfigPool to enter a degraded state.
func waitForPoolDegradation(ctx context.Context, cs *framework.ClientSet, poolName string) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		mcp, err := cs.MachineConfigPools().Get(ctx, poolName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		return apihelpers.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolDegraded), nil
	})
}
