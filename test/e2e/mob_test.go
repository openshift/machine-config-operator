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
	"k8s.io/client-go/util/retry"
)

func TestMachineOSBuilder(t *testing.T) {
	cs := framework.NewClientSet("")
	mcpName := "test-mcp"
	namespace := "openshift-machine-config-operator"
	mobPodNamePrefix := "machine-os-builder"

	cleanup := helpers.CreateMCP(t, cs, mcpName)
	time.Sleep(5 * time.Second) // Wait a bit to ensure MCP is fully created

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

	// wait for Machine OS Builder pod to start
	err := helpers.WaitForPodStart(cs, mobPodNamePrefix, namespace)
	require.NoError(t, err, "Failed to start the Machine OS Builder pod")

	// delete the MachineConfigPool
	cleanup()
	time.Sleep(5 * time.Second)

	// wait for Machine OS Builder pod to stop
	err = helpers.WaitForPodStop(cs, mobPodNamePrefix, namespace)
	require.NoError(t, err, "Failed to stop the Machine OS Builder pod")
}
