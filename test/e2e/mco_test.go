package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang/glog"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/controller/node"
	e2e_shared_test "github.com/openshift/machine-config-operator/test/e2e-shared-tests"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

func TestClusterOperatorRelatedObjects(t *testing.T) {
	cs := framework.NewClientSet("")

	co, err := cs.ClusterOperators().Get(context.TODO(), "machine-config", metav1.GetOptions{})
	if err != nil {
		t.Errorf("couldn't get clusteroperator %v", err)
	}
	if len(co.Status.RelatedObjects) == 0 {
		t.Error("expected RelatedObjects to be populated but it was not")
	}
	var foundNS bool
	for _, ro := range co.Status.RelatedObjects {
		if ro.Resource == "namespaces" && ro.Name == ctrlcommon.MCONamespace {
			foundNS = true
		}
	}
	if !foundNS {
		t.Error("ClusterOperator.RelatedObjects should contain the MCO namespace")
	}
}

func TestMastersSchedulable(t *testing.T) {
	cs := framework.NewClientSet("")
	schedulerCR, err := cs.ConfigV1Interface.Schedulers().Get(context.TODO(), "cluster", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Error while listing scheduler CR with error %v", err)
	}
	schedulerCR.Spec.MastersSchedulable = true
	if _, err = cs.ConfigV1Interface.Schedulers().Update(context.TODO(), schedulerCR, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("Error while updating scheduler CR with error %v", err)
	}
	err = waitForAllMastersUpdate(cs, true)
	if err != nil {
		t.Fatalf("Expected all master nodes to be schedulable but it's not the case with %v", err)
	}
	// Reset scheduler CR
	schedulerCR, err = cs.ConfigV1Interface.Schedulers().Get(context.TODO(), "cluster", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Error while listing scheduler CR with error %v", err)
	}
	schedulerCR.Spec.MastersSchedulable = false
	if _, err = cs.ConfigV1Interface.Schedulers().Update(context.TODO(), schedulerCR, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("Error while updating scheduler CR with error %v", err)
	}
	err = waitForAllMastersUpdate(cs, false)
	if err != nil {
		t.Fatalf("Expected all master nodes to be unschedulable but it's not the case with %v", err)
	}
}

func checkMasterNodesSchedulability(cs *framework.ClientSet, masterSchedulable bool) bool {
	masterNodes, err := cs.CoreV1Interface.Nodes().List(context.TODO(), metav1.ListOptions{LabelSelector: "node-role.kubernetes.io/master="})
	if err != nil {
		glog.Errorf("error while listing master nodes with %v", err)
	}
	if masterSchedulable {
		for _, masterNode := range masterNodes.Items {
			if !CheckMasterIsAlreadySchedulable(&masterNode) {
				return false
			}
		}
	} else {
		for _, masterNode := range masterNodes.Items {
			if CheckMasterIsAlreadySchedulable(&masterNode) {
				return false
			}
		}
	}
	return true
}

// CheckMasterIsAlreadySchedulable checks if the given node has a worker label and doesn't have NoSchedule master
// taint
func CheckMasterIsAlreadySchedulable(master *corev1.Node) bool {
	_, hasWorkerLabel := master.Labels[node.WorkerLabel]
	hasMasterTaint := false
	for _, taint := range master.Spec.Taints {
		if taint.Key == ctrlcommon.MasterLabel && taint.Effect == corev1.TaintEffectNoSchedule {
			hasMasterTaint = true
		}
	}
	return hasWorkerLabel && !hasMasterTaint
}

func waitForAllMastersUpdate(cs *framework.ClientSet, mastersSchedulable bool) error {
	return wait.PollImmediate(1*time.Second, 5*time.Minute, func() (bool, error) {
		if !checkMasterNodesSchedulability(cs, mastersSchedulable) {
			glog.Infof("All masters are not in desired state")
			return false, nil
		}
		return true, nil
	})
}

func TestClusterOperatorStatusExtension(t *testing.T) {
	cs := framework.NewClientSet("")
	co, err := cs.ClusterOperators().Get(context.TODO(), "machine-config", metav1.GetOptions{})
	require.Nil(t, err)
	ext := map[string]string{
		"test": "extension",
	}
	rawExt, err := json.Marshal(ext)
	require.Nil(t, err)
	co.Status.Extension.Raw = rawExt
	_, err = cs.ClusterOperators().UpdateStatus(context.TODO(), co, metav1.UpdateOptions{})
	require.Nil(t, err)
	co, err = cs.ClusterOperators().Get(context.TODO(), "machine-config", metav1.GetOptions{})
	require.Nil(t, err)
	require.NotNil(t, co.Status.Extension)
	coExt := map[string]string{}
	err = json.Unmarshal(co.Status.Extension.Raw, &coExt)
	require.Nil(t, err)
	v, ok := coExt["test"]
	require.True(t, ok)
	require.Equal(t, "extension", v)
}

func TestMetrics(t *testing.T) {
	mcpName := "test-metrics"

	cs := framework.NewClientSet("")

	node := helpers.GetRandomNode(t, cs, "worker")

	mc := helpers.CreateMC("old-"+mcpName, mcpName)

	undoMCFunc := helpers.CreatePoolAndApplyMCToNode(t, cs, mcpName, node, []*mcfgv1.MachineConfig{mc})

	// Get the machine config pool
	mcp, err := cs.MachineConfigPools().Get(context.TODO(), mcpName, metav1.GetOptions{})
	require.Nil(t, err)

	_, err = cs.ClusterOperators().Get(context.TODO(), "machine-config", metav1.GetOptions{})
	require.NoError(t, err)

	undoNodeMutateFunc := e2e_shared_test.MutateNodeAndWait(t, cs, &node, mcp)
	t.Cleanup(func() {
		undoNodeMutateFunc()
		undoMCFunc()
	})

	pollErr := wait.Poll(5*time.Second, 5*time.Minute, func() (bool, error) {
		svc, err := cs.Services("openshift-machine-config-operator").Get(context.TODO(), "machine-config-operator", metav1.GetOptions{})
		require.Nil(t, err)

		// Extract the IP and port and build the URL
		requestTarget := svc.Spec.ClusterIP
		requestPort := svc.Spec.Ports[0].Port
		metricsURL := fmt.Sprintf("https://%s:%d/metrics", requestTarget, requestPort)

		// Ensure we have a valid URL.
		_, urlParseErr := url.Parse(metricsURL)
		require.NoError(t, urlParseErr)

		t.Logf("Getting monitoring token")
		token, err := helpers.GetMonitoringToken(t, cs)
		require.Nil(t, err)

		out, err := helpers.ExecCmdOnNodeWithError(cs, node, []string{"curl", "-v", "-s", "-k", "-H", "Authorization: Bearer " + string(token), metricsURL}...)
		require.NoError(t, err)

		// The /metrics output will contain the metric if it works
		metricString := fmt.Sprintf("mco_unavailable_machine_count{pool=%q} 1", mcpName)
		if !strings.Contains(out, metricString) {
			t.Logf("%s: Metric should have been set, but were NOT", out)
			return false, nil
		}
		t.Log("Metric successfully set")
		return true, nil
	})

	require.NoError(t, pollErr)
}
