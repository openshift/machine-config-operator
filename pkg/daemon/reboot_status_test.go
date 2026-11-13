package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	apicfgv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgfake "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	mcfglisters "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	corelisters "k8s.io/client-go/listers/core/v1"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

// rebootTestWriter overrides only the writer operation used by reboot reporting.
type rebootTestWriter struct {
	NodeWriter
	node   *corev1.Node
	err    error
	writes int
}

func (w *rebootTestWriter) SetAnnotations(annotations map[string]string) (*corev1.Node, error) {
	w.writes++
	if w.err != nil {
		return nil, w.err
	}
	for key, value := range annotations {
		w.node.Annotations[key] = value
	}
	return w.node.DeepCopy(), nil
}

func TestReportCompletedReboot(t *testing.T) {
	const name = "node-1"
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: map[string]string{
			constants.MachineConfigDaemonPostConfigAction:                constants.MachineConfigDaemonStateRebooting,
			constants.MachineConfigDaemonLastObservedBootIDAnnotationKey: "old-boot",
		}},
	}
	node.Status.NodeInfo.BootID = "new-boot"
	mcn := &mcfgv1.MachineConfigNode{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: mcfgv1.MachineConfigNodeStatus{Conditions: []metav1.Condition{{
			Type: string(mcfgv1.MachineConfigNodeUpdateRebooted), Status: metav1.ConditionFalse,
		}}},
	}
	client := mcfgfake.NewSimpleClientset(mcn)
	writer := &rebootTestWriter{node: node.DeepCopy()}
	dn := &Daemon{
		bootID:     "new-boot",
		mcfgClient: client,
		fgHandler:  ctrlcommon.NewFeatureGatesHardcodedHandler([]apicfgv1.FeatureGateName{}, []apicfgv1.FeatureGateName{}),
		nodeWriter: writer,
	}

	// The fake client does not implement server-side apply. Persist the status
	// payload so that retries exercise the same no-op path as a real API server.
	var statusErr error
	statusWrites := 0
	client.PrependReactor("patch", "machineconfignodes", func(action clienttesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "status" {
			return false, nil, nil
		}
		statusWrites++
		if statusErr != nil {
			return true, nil, statusErr
		}
		obj, err := client.Tracker().Get(mcfgv1.GroupVersion.WithResource("machineconfignodes"), "", name)
		require.NoError(t, err)
		updated := obj.(*mcfgv1.MachineConfigNode).DeepCopy()
		require.NoError(t, json.Unmarshal(action.(clienttesting.PatchAction).GetPatch(), updated))
		require.NoError(t, client.Tracker().Update(mcfgv1.GroupVersion.WithResource("machineconfignodes"), updated, ""))
		return true, updated, nil
	})

	for _, tc := range []struct{ name, daemonBoot, nodeBoot string }{
		{"stale node boot ID", "new-boot", "old-boot"},
		{"missing node boot ID", "new-boot", ""},
		{"missing daemon boot ID", "", "new-boot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dn.bootID = tc.daemonBoot
			observed := node.DeepCopy()
			observed.Status.NodeInfo.BootID = tc.nodeBoot
			complete, err := dn.reportCompletedRebootWithContext(context.Background(), observed, "worker")
			require.NoError(t, err)
			require.False(t, complete)
			require.Zero(t, statusWrites)
			require.Zero(t, writer.writes)
		})
	}
	for _, phase := range []corev1.NodePhase{corev1.NodePending, "Provisioning"} {
		t.Run(string(phase), func(t *testing.T) {
			dn.bootID = "new-boot"
			observed := node.DeepCopy()
			observed.Status.Phase = phase
			complete, err := dn.reportCompletedRebootWithContext(context.Background(), observed, "worker")
			require.NoError(t, err)
			require.False(t, complete)
			require.Zero(t, statusWrites)
			require.Zero(t, writer.writes)
		})
	}

	dn.bootID = "new-boot"
	statusErr = errors.New("status write failed")
	complete, err := dn.reportCompletedRebootWithContext(context.Background(), node, "worker")
	require.ErrorContains(t, err, "status write failed")
	require.False(t, complete)
	require.Zero(t, writer.writes)
	require.Equal(t, constants.MachineConfigDaemonStateRebooting, writer.node.Annotations[constants.MachineConfigDaemonPostConfigAction])
	require.Equal(t, "old-boot", writer.node.Annotations[constants.MachineConfigDaemonLastObservedBootIDAnnotationKey])

	statusErr = nil
	writer.err = errors.New("annotation write failed")
	complete, err = dn.reportCompletedRebootWithContext(context.Background(), node, "worker")
	require.ErrorContains(t, err, "annotation write failed")
	require.False(t, complete)
	require.Equal(t, constants.MachineConfigDaemonStateRebooting, writer.node.Annotations[constants.MachineConfigDaemonPostConfigAction])
	require.Equal(t, "old-boot", writer.node.Annotations[constants.MachineConfigDaemonLastObservedBootIDAnnotationKey])

	persisted, err := client.MachineconfigurationV1().MachineConfigNodes().Get(context.TODO(), name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, metav1.ConditionTrue, persisted.Status.Conditions[0].Status)

	writer.err = nil
	complete, err = dn.reportCompletedRebootWithContext(context.Background(), node, "worker")
	require.NoError(t, err)
	require.True(t, complete)
	require.Equal(t, "", writer.node.Annotations[constants.MachineConfigDaemonPostConfigAction])
	require.Equal(t, "new-boot", writer.node.Annotations[constants.MachineConfigDaemonLastObservedBootIDAnnotationKey])
}

type rebootTestQueue struct {
	workqueue.TypedRateLimitingInterface[string]
	key   string
	delay time.Duration
}

func (q *rebootTestQueue) AddAfter(key string, delay time.Duration) {
	q.key, q.delay = key, delay
}

func TestSyncNodeRetriesRebootWithoutBlockingInitialization(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "node-1",
		Annotations: map[string]string{
			constants.MachineConfigDaemonPostConfigAction: constants.MachineConfigDaemonStateRebooting,
		},
	}}
	node.Status.NodeInfo.BootID = "old-boot"
	nodeIndex := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	require.NoError(t, nodeIndex.Add(node))
	queue := &rebootTestQueue{}
	dn := &Daemon{
		name:       node.Name,
		bootID:     "new-boot",
		nodeLister: corelisters.NewNodeLister(nodeIndex),
		mcpLister:  mcfglisters.NewMachineConfigPoolLister(cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})),
		ccLister:   mcfglisters.NewControllerConfigLister(cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})),
		queue:      queue,
		osImageMux: &sync.Mutex{},
	}

	// The missing ControllerConfig stops the test after node initialization;
	// it must not stop at the stale boot ID or clear the reboot annotation.
	err := dn.syncNode(context.Background(), node.Name)
	require.ErrorContains(t, err, "could not get ControllerConfig")
	require.True(t, dn.nodeInitialized)
	require.Equal(t, node.Name, queue.key)
	require.Equal(t, 10*time.Second, queue.delay)
	require.Equal(t, constants.MachineConfigDaemonStateRebooting, dn.node.Annotations[constants.MachineConfigDaemonPostConfigAction])
}
