package e2e_shared_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/wait"

	corev1 "k8s.io/api/core/v1"
)

func MutateNodeAndWait(t *testing.T, cs *framework.ClientSet, node *corev1.Node, pool *mcfgv1.MachineConfigPool) func() {
	t.Helper()

	filename := "/etc/containers/storage.conf"
	nodeFilename := filepath.Join("/rootfs", filename)
	t.Logf("Mutating %q on %s", filename, node.Name)

	mutateCmd := fmt.Sprintf("printf '%s' >> %s", "wrong-data-here", nodeFilename)
	helpers.ExecCmdOnNode(t, cs, *node, "/bin/bash", "-c", mutateCmd)
	assertNodeAndMCPIsDegraded(t, cs, *node, *pool, filename)

	return helpers.MakeIdempotent(func() {
		t.Logf("Restoring %q on %s", filename, node.Name)
		recoverCmd := fmt.Sprintf("sed -e s/wrong-data-here//g -i %s", nodeFilename)
		helpers.ExecCmdOnNode(t, cs, *node, "/bin/bash", "-c", recoverCmd)
		assertNodeAndMCPIsRecovered(t, cs, *node, *pool)
	})
}

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

	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 1*time.Minute, true, func(ctx context.Context) (bool, error) {
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

func assertLogsContain(t *testing.T, cs *framework.ClientSet, mcdPod *corev1.Pod, expectedContents string) {
	t.Helper()

	logs, err := cs.Pods(mcdPod.Namespace).GetLogs(mcdPod.Name, &corev1.PodLogOptions{
		Container: "machine-config-daemon",
	}).DoRaw(context.TODO())
	require.Nil(t, err)

	if !strings.Contains(string(logs), expectedContents) {
		t.Fatalf("expected to find '%s' in logs for %s/%s", expectedContents, mcdPod.Namespace, mcdPod.Name)
	}
}

func assertNodeIsInDoneState(t *testing.T, cs *framework.ClientSet, node corev1.Node) {
	t.Helper()

	assertNodeReachesState(t, cs, node, func(n corev1.Node) bool {
		isDone := n.Annotations[constants.MachineConfigDaemonStateAnnotationKey] == string(constants.MachineConfigDaemonStateDone)
		hasClearedReason := n.Annotations[constants.MachineConfigDaemonReasonAnnotationKey] == ""
		return isDone && hasClearedReason
	})
}

func assertNodeAndMCPIsRecovered(t *testing.T, cs *framework.ClientSet, node corev1.Node, mcp mcfgv1.MachineConfigPool) {
	t.Helper()

	t.Log("Verifying node has recovered from config mismatch")
	// Assert that the node eventually reaches a Done state and its reason is
	// cleared
	assertNodeIsInDoneState(t, cs, node)

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

func timeIt(t *testing.T, info string, timedFunc func()) {
	start := time.Now()

	defer func() {
		t.Logf("%s (took %v)", info, time.Since(start))
	}()

	timedFunc()
}
