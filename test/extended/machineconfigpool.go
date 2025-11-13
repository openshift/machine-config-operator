// TODO (MCO-1960): Deduplicate these functions with the helpers defined in /extended-priv/machineconfigpool.go.
package extended

import (
	"context"
	"fmt"
	"time"

	o "github.com/onsi/gomega"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	machineconfigclient "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// `DoesMachineConfigPoolHaveMachines` returns true if the desired MCP, defined by the
// `mcpName` parameter has machines and false otherwise. It also returns an error if one occurs
// when getting the MCP.
func DoesMachineConfigPoolHaveMachines(machineConfigClient *machineconfigclient.Clientset, mcpName string) bool {
	// Get desired MCP
	mcp, mcpErr := machineConfigClient.MachineconfigurationV1().MachineConfigPools().Get(context.TODO(), mcpName, metav1.GetOptions{})
	o.Expect(mcpErr).NotTo(o.HaveOccurred(), "Error checking for compatible MCPs: %s", mcpErr)

	// Check if desired MCP has machines
	return mcp.Status.MachineCount > 0
}

// `WaitForMCPToBeReady` waits up to 5 minutes for a pool to be in an updated state with
// a specified number of ready machines
func WaitForMCPToBeReady(machineConfigClient *machineconfigclient.Clientset, poolName string, readyMachineCount int32, oldRenderedMC string) {
	o.Eventually(func() bool {
		mcp, err := machineConfigClient.MachineconfigurationV1().MachineConfigPools().Get(context.TODO(), poolName, metav1.GetOptions{})
		if err != nil {
			logger.Infof("Failed to grab MCP '%v', error :%v", poolName, err)
			return false
		}
		// Check if the pool is in an updated state with the correct number of ready machines
		if IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolUpdated) &&
			(oldRenderedMC != "" || mcp.Status.UpdatedMachineCount == readyMachineCount) {
			logger.Infof("MCP '%v' has the desired %v ready machines.", poolName, mcp.Status.UpdatedMachineCount)
			// If an old rendered MC has been provided, make sure the MCP has been updated to a new rendered MC
			if oldRenderedMC != "" {
				// If the MC in the MCP is not equal to the old rendered MC, it meas we have updated to a new MC
				if mcp.Spec.Configuration.Name != oldRenderedMC {
					logger.Infof("MCP '%v' has updated to a new rendered MC: %v.", poolName, mcp.Spec.Configuration.Name)
					return true
				}
				logger.Infof("MCP '%v' is still targeting the old rendered MC: %v. Waiting for the MCP to target a new rendered MC.", poolName, mcp.Spec.Configuration.Name)
				return false
			}
			return true
		}
		// Log details of what is outstanding for the pool to be considered ready
		if mcp.Status.UpdatedMachineCount == readyMachineCount {
			logger.Infof("MCP '%v' has the desired %v ready machines, but is not in an 'Updated' state.", poolName, mcp.Status.UpdatedMachineCount)
		} else {
			logger.Infof("MCP '%v' has %v ready machines. Waiting for the desired ready machine count of %v.", poolName, mcp.Status.UpdatedMachineCount, readyMachineCount)
		}
		return false
	}, 5*time.Minute, 10*time.Second).Should(o.BeTrue(), "Timed out waiting for MCP '%v' to be in 'Updated' state with %v ready machines.", poolName, readyMachineCount)
}

// `CleanupCustomMCP` cleans up a custom MCP if it exists through the following steps:
//  1. Remove the custom MCP role label from the node
//  2. Wait for the custom MCP to be updated with no ready machines
//  3. Wait for the node to have a current config version equal to the config version of the worker MCP
//  4. Remove the custom MCP
func CleanupCustomMCP(oc *exutil.CLI, machineConfigClient *machineconfigclient.Clientset, customMCPName, nodeName string) error {
	// Skip this if the custom MCP is already deleted
	logger.Infof("Checking if MCP `%v` still exists", customMCPName)
	customMcp, customMcpErr := machineConfigClient.MachineconfigurationV1().MachineConfigPools().Get(context.TODO(), customMCPName, metav1.GetOptions{})
	if customMcpErr != nil {
		if apierrors.IsNotFound(customMcpErr) {
			logger.Infof("Custom MCP `%v` no longer exists, no cleanup is required", customMCPName)
			return nil
		}
		return fmt.Errorf("error checking existence of %v MCP", customMCPName)
	}

	// Unlabel node if one is in the MCP
	if customMcp.Status.MachineCount > 0 {
		logger.Infof("Removing label node-role.kubernetes.io/%v from node %v", customMCPName, nodeName)
		unlabelErr := oc.AsAdmin().Run("label").Args(fmt.Sprintf("node/%s", nodeName), fmt.Sprintf("node-role.kubernetes.io/%s-", customMCPName)).Execute()
		if unlabelErr != nil {
			return fmt.Errorf("could not remove label 'node-role.kubernetes.io/%v' from node '%v'; err: %v", customMCPName, nodeName, unlabelErr)
		}
	}

	// Wait for custom MCP to report no ready nodes
	logger.Infof("Waiting for %v MCP to be updated with %v ready machines.", customMCPName, 0)
	WaitForMCPToBeReady(machineConfigClient, customMCPName, 0, "")

	// Wait for node to have a current config version equal to the worker MCP's config version
	workerMcp, workerMcpErr := machineConfigClient.MachineconfigurationV1().MachineConfigPools().Get(context.TODO(), "worker", metav1.GetOptions{})
	if workerMcpErr != nil {
		return fmt.Errorf("could not get worker MCP; err: %v", workerMcpErr)
	}
	workerMcpConfig := workerMcp.Spec.Configuration.Name
	logger.Infof("Waiting for %v node to be updated with %v config version.", nodeName, workerMcpConfig)
	WaitForNodeCurrentConfig(oc, nodeName, workerMcpConfig)

	// Delete custom MCP
	logger.Infof("Deleting MCP %v", customMCPName)
	deleteMCPErr := oc.AsAdmin().Run("delete").Args("mcp", customMCPName).Execute()
	if deleteMCPErr != nil {
		return fmt.Errorf("error deleting MCP '%v': %v", customMCPName, deleteMCPErr)
	}

	return nil
}

// `DeleteMCAndWaitForMCPUpdate` deletes the desired MC and waits for the associated MCP
// to return to an updated state
func DeleteMCAndWaitForMCPUpdate(oc *exutil.CLI, machineConfigClient *machineconfigclient.Clientset, mcName, mcpName string) {
	oldRenderedMC := ""
	// Get the rendered config of the MCP before the MC deletion, if the MCP still exists
	mcp, mcpErr := machineConfigClient.MachineconfigurationV1().MachineConfigPools().Get(context.TODO(), mcpName, metav1.GetOptions{})
	if mcpErr == nil {
		oldRenderedMC = mcp.Spec.Configuration.Name
	}

	// Delete the provided MC
	logger.Infof("Deleting MC `%v`.", mcName)
	mcDeleted, deleteMCErr := DeleteMCByName(oc, machineConfigClient, mcName)
	o.Expect(deleteMCErr).NotTo(o.HaveOccurred(), fmt.Sprintf("Could not delete MachineConfig `%v`: %v.", mcName, deleteMCErr))

	// Only wait for the MCP to return to an updated state if the MC existed and needed deletion
	// and if the targeted MCP still exists
	if mcDeleted && mcpErr == nil {
		logger.Infof("Waiting for %v MCP to be updated with %v ready machines.", mcpName, 1)
		WaitForMCPToBeReady(machineConfigClient, mcpName, 1, oldRenderedMC)
	}
}

// `MCPIsUpdatedToNewConfig` checks whether a MCP has been updated to a new config version by
// checking if the "Updated" condition in the pool's `Status` is true and if the last update time
// of the condition is from after the update start time.
func MCPIsUpdatedToNewConfig(machineConfigClient *machineconfigclient.Clientset, mcpName string, startTime metav1.Time) bool {
	// Get MCP
	mcp, mcpErr := machineConfigClient.MachineconfigurationV1().MachineConfigPools().Get(context.TODO(), mcpName, metav1.GetOptions{})
	o.Expect(mcpErr).NotTo(o.HaveOccurred(), "Error getting MCP `%v`: %s", mcpName, mcpErr)

	// Check that the MCP is in an "Updated" condition that was set after the update started
	for _, condition := range mcp.Status.Conditions {
		if condition.Type == mcfgv1.MachineConfigPoolUpdated {
			return condition.Status == corev1.ConditionTrue && condition.LastTransitionTime.After(startTime.Time)
		}
	}

	return false
}
