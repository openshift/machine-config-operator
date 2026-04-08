package e2e_techpreview

import (
	"context"
	"fmt"
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
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
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
		// Human-friendly name of the test.
		name string
		// Name of the object(s) to create for the test. Although this value is
		// passed into each test function, the test function may use a different
		// variable name for the sake of clarity.
		objName string
		// The recovery function to execute which should cause the
		// MachineConfigPool to go back to a non-degraded state.
		recoverFunc func(*testing.T, string)
	}{
		{
			name:    "Delete overriding MachineConfig",
			objName: "osimageurl-delete",
			recoverFunc: func(t *testing.T, mcName string) {
				require.NoError(t, cs.MachineconfigurationV1Interface.MachineConfigs().Delete(ctx, mcName, metav1.DeleteOptions{}))
				t.Logf("Deleted MachineConfig %s which overrides OSImageURL", mcName)
			},
		},
		{
			name:    "Clear OSImageStream name from MachineConfigPool",
			objName: "osimageurl-mcp-clear",
			recoverFunc: func(t *testing.T, poolName string) {
				require.NoError(t, setOSImageStreamOnMachineConfigPool(ctx, cs, poolName, ""))
				t.Logf("Cleared OSImageStream from MachineConfigPool %s", poolName)
			},
		},
		{
			name:    "Clear OSImageURL override from MachineConfig",
			objName: "osimageurl-mc-clear",
			recoverFunc: func(t *testing.T, mcName string) {
				mc, err := cs.MachineconfigurationV1Interface.MachineConfigs().Get(ctx, mcName, metav1.GetOptions{})
				require.NoError(t, err)

				mc.Spec.OSImageURL = ""

				_, err = cs.MachineconfigurationV1Interface.MachineConfigs().Update(ctx, mc, metav1.UpdateOptions{})
				require.NoError(t, err)
				t.Logf("Cleared OSImageURL from MachineConfig %s", mcName)
			},
		},
	}

	// Fetch the OSImageStream so that we can use the default value to populate
	// the MachineConfigPool field.
	osi, err := getOSImageStream(ctx, cs)
	require.NoError(t, err)

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			// For simplicity, we use the same name for the MachineConfig and the
			// MachineConfigPool. However, we assign it to different variables so
			// that the context around which value is used where is preserved.
			mcName := testCase.objName
			poolName := testCase.objName

			// Create the MachineConfigPool.
			t.Cleanup(helpers.CreateMCP(t, cs, poolName))

			// Set the OSImageStream name on the MachineConfigPool to the default OSImageStream value.
			require.NoError(t, setOSImageStreamOnMachineConfigPool(ctx, cs, poolName, osi.Status.DefaultStream))

			// Wait for the pool to render its first MachineConfig.
			helpers.WaitForRenderedConfig(t, cs, poolName, "00-worker")

			// Create and apply the MachineConfig which overrides OSImageURL.
			require.NoError(t, createAndApplyMC(ctx, t, cs, mcName))

			// Wait for the MachineConfigPool to degrade.
			start := time.Now()
			require.NoError(t, waitForPoolDegradation(ctx, cs, poolName))
			t.Logf("Pool %q has reached expected degraded state after %s", poolName, time.Since(start))

			// Execute the test function to initiate MachineConfigPool recovery.
			testCase.recoverFunc(t, testCase.objName)

			// Wait for the MachineConfigPool to lose the degraded status.
			start = time.Now()
			require.NoError(t, helpers.WaitForPoolCompleteAny(t, cs, poolName))
			t.Logf("Pool %q has cleared the degraded state after %s", poolName, time.Since(start))
		})
	}
}

// Fetches the OSImageStream, checks that only a single OSImageStream instance
// is present, and that the defaultStream value is populated.
func getOSImageStream(ctx context.Context, cs *framework.ClientSet) (*mcfgv1alpha1.OSImageStream, error) {
	osiList, err := cs.GetMcfgclient().MachineconfigurationV1alpha1().OSImageStreams().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not get OSImageStream: %w", err)
	}

	expectedCount := 1
	actualCount := len(osiList.Items)
	if actualCount != expectedCount {
		return nil, fmt.Errorf("expected %d OSImageStream(s), got: %d", expectedCount, actualCount)
	}

	osi := osiList.Items[0]
	if osi.Status.DefaultStream == "" {
		return nil, fmt.Errorf("status.defaultStream empty on OSImageStream %s", osi.Name)
	}

	return &osi, nil
}

// Cleanup needs to tolerate the MachineConfig being missing, since one of the
// test cases deletes the MachineConfig. The helpers.ApplyMC() function does
// not offer that functionality, hence this function.
func createAndApplyMC(ctx context.Context, t *testing.T, cs *framework.ClientSet, mcName string) error {
	t.Helper()

	// This is a dummy pullspec.
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

// Waits for the MachineConfigPool to reach a degraded state.
func waitForPoolDegradation(ctx context.Context, cs *framework.ClientSet, poolName string) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		mcp, err := cs.MachineConfigPools().Get(ctx, poolName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		return apihelpers.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolDegraded), nil
	})
}
