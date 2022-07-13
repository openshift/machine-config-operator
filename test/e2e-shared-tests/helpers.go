package e2e_shared_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"
	kubeErrs "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func waitForConfigDriftMonitorStart(t *testing.T, cs *framework.ClientSet, node corev1.Node) {
	t.Helper()

	// With the way that the Config Drift Monitor is wired into the MCD,
	// "machineconfiguration.openshift.io/state" gets set to "Done" before the
	// Config Drift Monitor is started. This is fine, however it makes testing a
	// bit tricky as we rely upon "machineconfiguration.openshift.io/state" to be
	// "Done" before we commence our test.
	//
	// Because of this, we must infer that the Config Drift Monitor is started by
	// looking at the MCD pod logs, hence this function. It's also worth noting
	// that the Config Drift Monitor text may not immediately appear in the log,
	// so we have to poll for it.

	start := time.Now()

	ctx := context.Background()

	err := wait.PollImmediate(2*time.Second, 1*time.Minute, func() (bool, error) {
		mcdPod, err := helpers.MCDForNode(cs, &node)
		require.Nil(t, err)

		logs, err := cs.Pods(mcdPod.Namespace).GetLogs(mcdPod.Name, &corev1.PodLogOptions{
			Container: "machine-config-daemon",
		}).DoRaw(ctx)
		require.Nil(t, err)

		splitLogs := strings.Split(string(logs), "\n")

		// Scan the logs from the bottom up, looking for either a shutdown or startup message.
		for i := len(splitLogs) - 1; i >= 0; i-- {
			line := splitLogs[i]

			if strings.Contains(line, configDriftMonitorShutdownMsg) {
				// We've found a shutdown message before we found a startup message,
				// this means the Config Drift Monitor is not running.
				return false, nil
			}

			if strings.Contains(line, configDriftMonitorStartupMsg) {
				// We've found a startup message. This means the Config Drift Monitor is running.
				return true, nil
			}
		}

		return false, nil
	})

	end := time.Since(start)

	if err == nil {
		t.Logf("Config Drift Monitor is running (waited %v)", end)
	}

	require.Nil(t, err, "expected config drift monitor to start (waited %v)", end)
}

func assertNodeAndMCPIsDegraded(t *testing.T, cs *framework.ClientSet, node corev1.Node, mcp mcfgv1.MachineConfigPool, filename string) {
	t.Helper()

	logEntry := fmt.Sprintf("content mismatch for file \"%s\"", filename)

	// Assert that the node eventually reaches a Degraded state and has the
	// config mismatch as the reason
	t.Log("Verifying node becomes degraded due to config mismatch")

	assertNodeReachesState(t, cs, node, func(n corev1.Node) bool {
		isDegraded := n.Annotations[constants.MachineConfigDaemonStateAnnotationKey] == string(constants.MachineConfigDaemonStateDegraded)
		hasReason := strings.Contains(n.Annotations[constants.MachineConfigDaemonReasonAnnotationKey], logEntry)
		return isDegraded && hasReason
	})

	mcdPod, err := helpers.MCDForNode(cs, &node)
	require.Nil(t, err)

	assertLogsContain(t, cs, mcdPod, logEntry)

	// Assert that the MachineConfigPool eventually reaches a degraded state and has the config mismatch as the reason.
	t.Log("Verifying MachineConfigPool becomes degraded due to config mismatch")

	assertPoolReachesState(t, cs, mcp, func(m mcfgv1.MachineConfigPool) bool {
		trueConditions := []mcfgv1.MachineConfigPoolConditionType{
			mcfgv1.MachineConfigPoolDegraded,
			mcfgv1.MachineConfigPoolNodeDegraded,
			mcfgv1.MachineConfigPoolUpdating,
		}

		falseConditions := []mcfgv1.MachineConfigPoolConditionType{
			mcfgv1.MachineConfigPoolRenderDegraded,
			mcfgv1.MachineConfigPoolUpdated,
		}

		return m.Status.DegradedMachineCount == 1 &&
			allMCPConditionsTrue(trueConditions, m) &&
			allMCPConditionsFalse(falseConditions, m)
	})
}

func assertLogsContain(t *testing.T, cs *framework.ClientSet, mcdPod *corev1.Pod, expectedContents string) {
	logs, err := cs.Pods(mcdPod.Namespace).GetLogs(mcdPod.Name, &corev1.PodLogOptions{
		Container: "machine-config-daemon",
	}).DoRaw(context.TODO())
	require.Nil(t, err)

	if !strings.Contains(string(logs), expectedContents) {
		t.Fatalf("expected to find '%s' in logs for %s/%s", expectedContents, mcdPod.Namespace, mcdPod.Name)
	}
}

func assertNodeAndMCPIsRecovered(t *testing.T, cs *framework.ClientSet, node corev1.Node, mcp mcfgv1.MachineConfigPool) {
	t.Helper()

	t.Log("Verifying node has recovered from config mismatch")
	// Assert that the node eventually reaches a Done state and its reason is
	// cleared
	assertNodeReachesState(t, cs, node, func(n corev1.Node) bool {
		isDone := n.Annotations[constants.MachineConfigDaemonStateAnnotationKey] == "Done"
		hasClearedReason := n.Annotations[constants.MachineConfigDaemonReasonAnnotationKey] == ""
		return isDone && hasClearedReason
	})

	t.Log("Verifying MachineConfigPool has recovered from config mismatch")
	// Assert that the MachineConfigPool eventually recovers.
	assertPoolReachesState(t, cs, mcp, func(m mcfgv1.MachineConfigPool) bool {
		falseConditions := []mcfgv1.MachineConfigPoolConditionType{
			mcfgv1.MachineConfigPoolDegraded,
			mcfgv1.MachineConfigPoolNodeDegraded,
			mcfgv1.MachineConfigPoolRenderDegraded,
			mcfgv1.MachineConfigPoolUpdating,
		}

		trueConditions := []mcfgv1.MachineConfigPoolConditionType{
			mcfgv1.MachineConfigPoolUpdated,
		}

		return m.Status.DegradedMachineCount == 0 &&
			allMCPConditionsTrue(trueConditions, m) &&
			allMCPConditionsFalse(falseConditions, m)
	})
}

func assertNodeReachesState(t *testing.T, cs *framework.ClientSet, target corev1.Node, stateFunc func(corev1.Node) bool) {
	t.Helper()

	maxWait := 5 * time.Minute

	end, err := pollForResourceState(maxWait, func() (bool, error) {
		node, err := cs.CoreV1Interface.Nodes().Get(context.TODO(), target.Name, metav1.GetOptions{})
		return stateFunc(*node), err
	})

	if err != nil {
		t.Fatalf("Node %s did not reach expected state (took %v): %s", target.Name, end, err)
	}

	t.Logf("Node %s reached expected state (took %v)", target.Name, end)
}

func assertPoolReachesState(t *testing.T, cs *framework.ClientSet, target mcfgv1.MachineConfigPool, stateFunc func(mcfgv1.MachineConfigPool) bool) {
	t.Helper()

	maxWait := 5 * time.Minute

	end, err := pollForResourceState(maxWait, func() (bool, error) {
		mcp, err := cs.MachineConfigPools().Get(context.TODO(), target.Name, metav1.GetOptions{})
		return stateFunc(*mcp), err
	})

	if err != nil {
		t.Fatalf("MachineConfigPool %s did not reach expected state (took %v): %s", target.Name, end, err)
	}

	t.Logf("MachineConfigPool %s reached expected state (took %v)", target.Name, end)
}

func pollForResourceState(timeout time.Duration, pollFunc func() (bool, error)) (time.Duration, error) {
	// This wraps wait.PollImmediate() for the following reason:
	//
	// If the control plane is temporarily unavailable (e.g., when running in a
	// single-node OpenShift (SNO) context and the node reboots), this error will
	// not be nil, but *should* go back to nil once the control-plane becomes
	// available again. To handle that, we:
	//
	// 1. Store the error within the pollForResourceState scope.
	// 2. Run the clock out.
	// 3. Handle the error (if it does not go back to nil) when the timeout is reached.
	//
	// This was inspired by and is a more generic implementation of:
	// https://github.com/openshift/machine-config-operator/blob/master/test/e2e-single-node/sno_mcd_test.go#L355-L374
	start := time.Now()

	var lastErr error

	waitErr := wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		result, err := pollFunc()
		lastErr = err
		return result, nil
	})

	return time.Since(start), kubeErrs.NewAggregate([]error{
		lastErr,
		waitErr,
	})
}

func mutateFileOnNode(t *testing.T, cs *framework.ClientSet, node corev1.Node, filename, contents string) {
	t.Helper()

	if !strings.HasPrefix(filename, "/rootfs") {
		filename = filepath.Join("/rootfs", filename)
	}

	bashCmd := fmt.Sprintf("printf '%s' > %s", contents, filename)
	t.Logf("Setting contents of %s on %s to %s", filename, node.Name, contents)

	helpers.ExecCmdOnNode(t, cs, node, "/bin/bash", "-c", bashCmd)
}

func allMCPConditionsTrue(conditions []mcfgv1.MachineConfigPoolConditionType, mcp mcfgv1.MachineConfigPool) bool {
	for _, condition := range conditions {
		if !mcfgv1.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, condition) {
			return false
		}
	}

	return true
}

func allMCPConditionsFalse(conditions []mcfgv1.MachineConfigPoolConditionType, mcp mcfgv1.MachineConfigPool) bool {
	for _, condition := range conditions {
		if !mcfgv1.IsMachineConfigPoolConditionFalse(mcp.Status.Conditions, condition) {
			return false
		}
	}

	return true
}

func timeIt(t *testing.T, info string, timedFunc func()) {
	start := time.Now()

	defer func() {
		t.Logf("%s (took %v)", info, time.Since(start))
	}()

	timedFunc()
}
