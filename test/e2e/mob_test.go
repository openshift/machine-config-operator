package e2e_test

import (
	"context"
	"testing"
	"time"

	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
)

func TestMachineOSBuilder(t *testing.T) {
	//add an assertion to see if deployment object is present or absent
	//correlate that with pod detection

	cs := framework.NewClientSet("")
	mcpName := "test-mcp"
	namespace := "openshift-machine-config-operator"
	mobPodNamePrefix := "machine-os-builder"

	cleanup := helpers.CreateMCP(t, cs, mcpName)
	time.Sleep(10 * time.Second) // Wait a bit to ensure MCP is fully created

	// added retry because another process was modifying the MCP concurrently
	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {

		// Get the latest MCP
		mcp, err := cs.MachineConfigPools().Get(context.TODO(), mcpName, metav1.GetOptions{})
		if err != nil {
			return err
		}

		// Set the label
		mcp.ObjectMeta.Labels[ctrlcommon.LayeringEnabledPoolLabel] = ""

		// Try to update the MCP
		_, err = cs.MachineConfigPools().Update(context.TODO(), mcp, metav1.UpdateOptions{})
		return err
	})
	require.Nil(t, retryErr)

	// assertion to see if the deployment object is present after setting the label
	ctx := context.TODO()
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, time.Minute, true, func(ctx context.Context) (bool, error) {
		exists, err := helpers.CheckDeploymentExists(cs, "machine-os-builder", namespace)
		return exists, err
	})
	require.NoError(t, err, "Failed to check the existence of the Machine OS Builder deployment")

	// wait for Machine OS Builder pod to start
	err = helpers.WaitForPodStart(cs, mobPodNamePrefix, namespace)
	require.NoError(t, err, "Failed to start the Machine OS Builder pod")

	// delete the MachineConfigPool
	cleanup()
	time.Sleep(20 * time.Second)

	// assertion to see if the deployment object is absent after deleting the MCP
	err = wait.PollUntilContextTimeout(ctx, 2*time.Second, time.Minute, true, func(ctx context.Context) (bool, error) {
		exists, err := helpers.CheckDeploymentExists(cs, "machine-os-builder", namespace)
		return !exists, err
	})
	require.NoError(t, err, "Failed to check the absence of the Machine OS Builder deployment")

	// wait for Machine OS Builder pod to stop
	err = helpers.WaitForPodStop(cs, mobPodNamePrefix, namespace)
	require.NoError(t, err, "Failed to stop the Machine OS Builder pod")
}
