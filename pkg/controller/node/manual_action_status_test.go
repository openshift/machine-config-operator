package node

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	corelisters "k8s.io/client-go/listers/core/v1"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgfake "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestManualActionConditions(t *testing.T) {
	tests := []struct {
		name              string
		node              *corev1.Node
		conditions        []metav1.Condition
		drainablePodCount int
		bootIDChanged     bool
		bootID            string
		want              map[string]metav1.ConditionStatus
		wantRebootMsg     string
	}{
		{
			name: "cordoned node with workloads remains updated but is not drained",
			node: &corev1.Node{Spec: corev1.NodeSpec{Unschedulable: true}},
			conditions: []metav1.Condition{
				{Type: string(mcfgv1.MachineConfigNodeUpdated), Status: metav1.ConditionTrue},
				{Type: string(mcfgv1.MachineConfigNodeUpdateUncordoned), Status: metav1.ConditionFalse},
				{Type: string(mcfgv1.MachineConfigNodeUpdateRebooted), Status: metav1.ConditionFalse},
			},
			drainablePodCount: 2,
			want: map[string]metav1.ConditionStatus{
				string(mcfgv1.MachineConfigNodeUpdated):          metav1.ConditionTrue,
				string(mcfgv1.MachineConfigNodeUpdateCordoned):   metav1.ConditionTrue,
				string(mcfgv1.MachineConfigNodeUpdateDrained):    metav1.ConditionFalse,
				string(mcfgv1.MachineConfigNodeUpdateUncordoned): metav1.ConditionFalse,
				string(mcfgv1.MachineConfigNodeUpdateRebooted):   metav1.ConditionFalse,
			},
		},
		{
			name: "cordoned node with no drainable pods is drained",
			node: &corev1.Node{Spec: corev1.NodeSpec{Unschedulable: true}},
			conditions: []metav1.Condition{
				{Type: string(mcfgv1.MachineConfigNodeUpdateCordoned), Status: metav1.ConditionTrue},
				{Type: string(mcfgv1.MachineConfigNodeUpdateUncordoned), Status: metav1.ConditionFalse},
				{Type: string(mcfgv1.MachineConfigNodeUpdateRebooted), Status: metav1.ConditionFalse},
			},
			want: map[string]metav1.ConditionStatus{
				string(mcfgv1.MachineConfigNodeUpdateCordoned):   metav1.ConditionTrue,
				string(mcfgv1.MachineConfigNodeUpdateDrained):    metav1.ConditionTrue,
				string(mcfgv1.MachineConfigNodeUpdateUncordoned): metav1.ConditionFalse,
				string(mcfgv1.MachineConfigNodeUpdateRebooted):   metav1.ConditionFalse,
			},
		},
		{
			name: "manual uncordon clears cordoned and records the transition",
			node: &corev1.Node{},
			conditions: []metav1.Condition{
				{Type: string(mcfgv1.MachineConfigNodeUpdated), Status: metav1.ConditionTrue},
				{Type: string(mcfgv1.MachineConfigNodeUpdateCordoned), Status: metav1.ConditionTrue},
				{Type: string(mcfgv1.MachineConfigNodeUpdateDrained), Status: metav1.ConditionTrue},
				{Type: string(mcfgv1.MachineConfigNodeUpdateUncordoned), Status: metav1.ConditionFalse},
				{Type: string(mcfgv1.MachineConfigNodeUpdateRebooted), Status: metav1.ConditionFalse},
			},
			want: map[string]metav1.ConditionStatus{
				string(mcfgv1.MachineConfigNodeUpdated):          metav1.ConditionTrue,
				string(mcfgv1.MachineConfigNodeUpdateCordoned):   metav1.ConditionFalse,
				string(mcfgv1.MachineConfigNodeUpdateDrained):    metav1.ConditionFalse,
				string(mcfgv1.MachineConfigNodeUpdateUncordoned): metav1.ConditionTrue,
				string(mcfgv1.MachineConfigNodeUpdateRebooted):   metav1.ConditionFalse,
			},
		},
		{
			name: "schedulable node does not claim an uncordon event",
			node: &corev1.Node{},
			conditions: []metav1.Condition{
				{Type: string(mcfgv1.MachineConfigNodeUpdated), Status: metav1.ConditionTrue},
				{Type: string(mcfgv1.MachineConfigNodeUpdateCordoned), Status: metav1.ConditionFalse},
				{Type: string(mcfgv1.MachineConfigNodeUpdateUncordoned), Status: metav1.ConditionFalse},
				{Type: string(mcfgv1.MachineConfigNodeUpdateRebooted), Status: metav1.ConditionFalse},
			},
			want: map[string]metav1.ConditionStatus{
				string(mcfgv1.MachineConfigNodeUpdated):          metav1.ConditionTrue,
				string(mcfgv1.MachineConfigNodeUpdateCordoned):   metav1.ConditionFalse,
				string(mcfgv1.MachineConfigNodeUpdateDrained):    metav1.ConditionFalse,
				string(mcfgv1.MachineConfigNodeUpdateUncordoned): metav1.ConditionFalse,
				string(mcfgv1.MachineConfigNodeUpdateRebooted):   metav1.ConditionFalse,
			},
		},
		{
			name:          "boot ID change records reboot without changing Updated",
			node:          &corev1.Node{},
			conditions:    []metav1.Condition{{Type: string(mcfgv1.MachineConfigNodeUpdated), Status: metav1.ConditionTrue}},
			bootIDChanged: true,
			bootID:        "boot-id-2",
			want: map[string]metav1.ConditionStatus{
				string(mcfgv1.MachineConfigNodeUpdated):          metav1.ConditionTrue,
				string(mcfgv1.MachineConfigNodeUpdateCordoned):   metav1.ConditionFalse,
				string(mcfgv1.MachineConfigNodeUpdateDrained):    metav1.ConditionFalse,
				string(mcfgv1.MachineConfigNodeUpdateUncordoned): metav1.ConditionFalse,
				string(mcfgv1.MachineConfigNodeUpdateRebooted):   metav1.ConditionTrue,
			},
			wantRebootMsg: "Node reboot observed from a changed boot ID (boot-id-2).",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mcn := &mcfgv1.MachineConfigNode{Status: mcfgv1.MachineConfigNodeStatus{Conditions: test.conditions}}
			actual := manualActionConditions(test.node, mcn, test.drainablePodCount, test.bootIDChanged, test.bootID)

			actualByType := map[string]metav1.Condition{}
			for _, condition := range test.conditions {
				actualByType[condition.Type] = condition
			}
			for _, condition := range actual {
				actualByType[condition.Type] = condition
			}

			for conditionType, status := range test.want {
				condition, ok := actualByType[conditionType]
				require.True(t, ok, "condition %q should exist", conditionType)
				assert.Equal(t, status, condition.Status, "condition %q", conditionType)
			}
			if test.wantRebootMsg != "" {
				assert.Equal(t, test.wantRebootMsg, actualByType[string(mcfgv1.MachineConfigNodeUpdateRebooted)].Message)
			}
		})
	}
}

func TestCountDrainBlockingPods(t *testing.T) {
	pods := []*corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "workload"}, Spec: corev1.PodSpec{NodeName: "worker-0"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
		{ObjectMeta: metav1.ObjectMeta{Name: "daemonset", OwnerReferences: []metav1.OwnerReference{{Kind: "DaemonSet"}}}, Spec: corev1.PodSpec{NodeName: "worker-0"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
		{ObjectMeta: metav1.ObjectMeta{Name: "mirror", Annotations: map[string]string{corev1.MirrorPodAnnotationKey: "mirror"}}, Spec: corev1.PodSpec{NodeName: "worker-0"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
		{ObjectMeta: metav1.ObjectMeta{Name: "completed"}, Spec: corev1.PodSpec{NodeName: "worker-0"}, Status: corev1.PodStatus{Phase: corev1.PodSucceeded}},
		{ObjectMeta: metav1.ObjectMeta{Name: "other-node"}, Spec: corev1.PodSpec{NodeName: "worker-1"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
	}

	assert.Equal(t, 1, countDrainBlockingPods("worker-0", pods))
}

func TestSyncManualActionStatus(t *testing.T) {
	const nodeName = "worker-0"
	const configName = "rendered-worker-abc123"

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
			Annotations: map[string]string{
				"machineconfiguration.openshift.io/currentConfig": configName,
				"machineconfiguration.openshift.io/desiredConfig": configName,
				"machineconfiguration.openshift.io/state":         "Done",
			},
		},
		Spec:   corev1.NodeSpec{Unschedulable: true},
		Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{BootID: "boot-id-1"}},
	}
	mcn := &mcfgv1.MachineConfigNode{
		ObjectMeta: metav1.ObjectMeta{Name: nodeName},
		Spec: mcfgv1.MachineConfigNodeSpec{
			Node:          mcfgv1.MCOObjectReference{Name: nodeName},
			Pool:          mcfgv1.MCOObjectReference{Name: "worker"},
			ConfigVersion: mcfgv1.MachineConfigNodeSpecMachineConfigVersion{Desired: configName},
		},
		Status: mcfgv1.MachineConfigNodeStatus{
			ConfigVersion: &mcfgv1.MachineConfigNodeStatusMachineConfigVersion{Current: configName, Desired: configName},
			Conditions: []metav1.Condition{
				{Type: string(mcfgv1.MachineConfigNodeUpdated), Status: metav1.ConditionTrue},
				{Type: string(mcfgv1.MachineConfigNodeUpdateCordoned), Status: metav1.ConditionFalse},
				{Type: string(mcfgv1.MachineConfigNodeUpdateDrained), Status: metav1.ConditionFalse},
				{Type: string(mcfgv1.MachineConfigNodeUpdateUncordoned), Status: metav1.ConditionFalse},
				{Type: string(mcfgv1.MachineConfigNodeUpdateRebooted), Status: metav1.ConditionFalse},
			},
		},
	}
	workloadPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: "default"},
		Spec:       corev1.PodSpec{NodeName: nodeName},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}

	nodeIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	require.NoError(t, nodeIndexer.Add(node))

	kubeClient := k8sfake.NewSimpleClientset(node, workloadPod)
	mcfgClient := mcfgfake.NewSimpleClientset(mcn)
	ctrl := &Controller{
		client:     mcfgClient,
		kubeClient: kubeClient,
		nodeLister: corelisters.NewNodeLister(nodeIndexer),
	}

	// A cordon is visible on the MCN, but does not clear the Updated condition.
	require.NoError(t, ctrl.syncManualActionStatus(nodeName))
	assertManualCondition(t, mcfgClient, nodeName, mcfgv1.MachineConfigNodeUpdated, metav1.ConditionTrue)
	assertManualCondition(t, mcfgClient, nodeName, mcfgv1.MachineConfigNodeUpdateCordoned, metav1.ConditionTrue)
	assertManualCondition(t, mcfgClient, nodeName, mcfgv1.MachineConfigNodeUpdateDrained, metav1.ConditionFalse)

	// Once the last drainable workload pod leaves, Drained becomes True.
	require.NoError(t, kubeClient.CoreV1().Pods(workloadPod.Namespace).Delete(t.Context(), workloadPod.Name, metav1.DeleteOptions{}))
	refreshNodeIndexer(t, nodeIndexer, kubeClient, nodeName)
	require.NoError(t, ctrl.syncManualActionStatus(nodeName))
	assertManualCondition(t, mcfgClient, nodeName, mcfgv1.MachineConfigNodeUpdateDrained, metav1.ConditionTrue)

	// Uncordoning clears the current cordon/drain states and records the transition.
	nodeFromAPI, err := kubeClient.CoreV1().Nodes().Get(t.Context(), nodeName, metav1.GetOptions{})
	require.NoError(t, err)
	nodeFromAPI.Spec.Unschedulable = false
	_, err = kubeClient.CoreV1().Nodes().Update(t.Context(), nodeFromAPI, metav1.UpdateOptions{})
	require.NoError(t, err)
	refreshNodeIndexer(t, nodeIndexer, kubeClient, nodeName)
	require.NoError(t, ctrl.syncManualActionStatus(nodeName))
	assertManualCondition(t, mcfgClient, nodeName, mcfgv1.MachineConfigNodeUpdated, metav1.ConditionTrue)
	assertManualCondition(t, mcfgClient, nodeName, mcfgv1.MachineConfigNodeUpdateCordoned, metav1.ConditionFalse)
	assertManualCondition(t, mcfgClient, nodeName, mcfgv1.MachineConfigNodeUpdateDrained, metav1.ConditionFalse)
	assertManualCondition(t, mcfgClient, nodeName, mcfgv1.MachineConfigNodeUpdateUncordoned, metav1.ConditionTrue)

	// A later boot-ID change is reported as a reboot, without implying an update.
	nodeFromAPI, err = kubeClient.CoreV1().Nodes().Get(t.Context(), nodeName, metav1.GetOptions{})
	require.NoError(t, err)
	nodeFromAPI.Status.NodeInfo.BootID = "boot-id-2"
	_, err = kubeClient.CoreV1().Nodes().Update(t.Context(), nodeFromAPI, metav1.UpdateOptions{})
	require.NoError(t, err)
	refreshNodeIndexer(t, nodeIndexer, kubeClient, nodeName)
	require.NoError(t, ctrl.syncManualActionStatus(nodeName))
	assertManualCondition(t, mcfgClient, nodeName, mcfgv1.MachineConfigNodeUpdated, metav1.ConditionTrue)
	assertManualCondition(t, mcfgClient, nodeName, mcfgv1.MachineConfigNodeUpdateRebooted, metav1.ConditionTrue)
}

func refreshNodeIndexer(t *testing.T, nodeIndexer cache.Indexer, kubeClient *k8sfake.Clientset, nodeName string) {
	t.Helper()
	node, err := kubeClient.CoreV1().Nodes().Get(t.Context(), nodeName, metav1.GetOptions{})
	require.NoError(t, err)
	require.NoError(t, nodeIndexer.Update(node))
}

func assertManualCondition(t *testing.T, client *mcfgfake.Clientset, nodeName string, conditionType mcfgv1.StateProgress, status metav1.ConditionStatus) {
	t.Helper()
	mcn, err := client.MachineconfigurationV1().MachineConfigNodes().Get(t.Context(), nodeName, metav1.GetOptions{})
	require.NoError(t, err)
	for _, condition := range mcn.Status.Conditions {
		if condition.Type == string(conditionType) {
			assert.Equal(t, status, condition.Status, "condition %q", conditionType)
			return
		}
	}
	t.Fatalf("condition %q not found on MCN %q", conditionType, nodeName)
}

func TestManualActionNodeChanged(t *testing.T) {
	oldNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}}}
	curNode := oldNode.DeepCopy()
	curNode.Status.NodeInfo.BootID = "boot-id-2"
	assert.True(t, manualActionNodeChanged(oldNode, curNode))

	oldNode = &corev1.Node{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{constants.MachineConfigDaemonLastObservedBootIDAnnotationKey: "boot-id-1"}}}
	curNode = oldNode.DeepCopy()
	curNode.Annotations[constants.MachineConfigDaemonLastObservedBootIDAnnotationKey] = "boot-id-2"
	assert.False(t, manualActionNodeChanged(oldNode, curNode), "internal boot ID marker updates must not enqueue a loop")
}

func TestIsMCOUpdateInProgress(t *testing.T) {
	const configName = "rendered-worker-current"
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		constants.CurrentMachineConfigAnnotationKey: configName,
		constants.DesiredMachineConfigAnnotationKey: configName,
	}}}
	mcn := &mcfgv1.MachineConfigNode{
		Spec: mcfgv1.MachineConfigNodeSpec{
			ConfigVersion: mcfgv1.MachineConfigNodeSpecMachineConfigVersion{Desired: configName},
		},
		Status: mcfgv1.MachineConfigNodeStatus{
			ConfigVersion: &mcfgv1.MachineConfigNodeStatusMachineConfigVersion{Current: configName, Desired: configName},
			Conditions:    []metav1.Condition{{Type: string(mcfgv1.MachineConfigNodeUpdated), Status: metav1.ConditionTrue}},
		},
	}

	assert.False(t, isMCOUpdateInProgress(node, mcn), "converged MCN and node should allow action reporting")

	updateInProgress := mcn.DeepCopy()
	updateInProgress.Spec.ConfigVersion.Desired = "rendered-worker-next"
	assert.True(t, isMCOUpdateInProgress(node, updateInProgress), "a new MCN target must prevent manual-action reporting")

	updateInProgress = mcn.DeepCopy()
	updateInProgress.Status.ConfigVersion.Desired = "rendered-worker-next"
	assert.True(t, isMCOUpdateInProgress(node, updateInProgress), "an MCN status target that has not converged must prevent manual-action reporting")
}

func TestSyncManualActionStatusDefersBootMarkerDuringMCOUpdate(t *testing.T) {
	const nodeName = "worker-0"
	const configName = "rendered-worker-current"
	const oldBootID = "boot-id-before-update"
	const currentBootID = "boot-id-after-update-reboot"

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
			Annotations: map[string]string{
				constants.CurrentMachineConfigAnnotationKey:                  configName,
				constants.DesiredMachineConfigAnnotationKey:                  configName,
				constants.MachineConfigDaemonLastObservedBootIDAnnotationKey: oldBootID,
			},
		},
		Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{BootID: currentBootID}},
	}
	mcn := &mcfgv1.MachineConfigNode{
		ObjectMeta: metav1.ObjectMeta{Name: nodeName},
		Status: mcfgv1.MachineConfigNodeStatus{
			ConfigVersion: &mcfgv1.MachineConfigNodeStatusMachineConfigVersion{Current: configName, Desired: configName},
			Conditions:    []metav1.Condition{{Type: string(mcfgv1.MachineConfigNodeUpdated), Status: metav1.ConditionFalse}},
		},
	}

	nodeIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	require.NoError(t, nodeIndexer.Add(node))
	kubeClient := k8sfake.NewSimpleClientset(node)
	mcfgClient := mcfgfake.NewSimpleClientset(mcn)
	ctrl := &Controller{
		client:     mcfgClient,
		kubeClient: kubeClient,
		nodeLister: corelisters.NewNodeLister(nodeIndexer),
	}

	require.NoError(t, ctrl.syncManualActionStatus(nodeName))
	updatedNode, err := kubeClient.CoreV1().Nodes().Get(t.Context(), nodeName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, oldBootID, updatedNode.Annotations[constants.MachineConfigDaemonLastObservedBootIDAnnotationKey], "must not consume an update reboot before it is classified")
}

func TestSyncManualActionStatusRevalidatesTargetBeforeAdvancingBootMarker(t *testing.T) {
	const nodeName = "worker-0"
	const configName = "rendered-worker-current"
	const oldBootID = "boot-id-before-update"
	const currentBootID = "boot-id-observed"

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
			Annotations: map[string]string{
				constants.CurrentMachineConfigAnnotationKey:                  configName,
				constants.DesiredMachineConfigAnnotationKey:                  configName,
				constants.MachineConfigDaemonLastObservedBootIDAnnotationKey: oldBootID,
			},
		},
		Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{BootID: currentBootID}},
	}
	mcn := &mcfgv1.MachineConfigNode{
		ObjectMeta: metav1.ObjectMeta{Name: nodeName},
		Spec: mcfgv1.MachineConfigNodeSpec{
			ConfigVersion: mcfgv1.MachineConfigNodeSpecMachineConfigVersion{Desired: configName},
		},
		Status: mcfgv1.MachineConfigNodeStatus{
			ConfigVersion: &mcfgv1.MachineConfigNodeStatusMachineConfigVersion{Current: configName, Desired: configName},
			Conditions:    []metav1.Condition{{Type: string(mcfgv1.MachineConfigNodeUpdated), Status: metav1.ConditionTrue}},
		},
	}

	nodeIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	require.NoError(t, nodeIndexer.Add(node))
	kubeClient := k8sfake.NewSimpleClientset(node)
	mcfgClient := mcfgfake.NewSimpleClientset(mcn)

	// The first two MCN GETs are the initial sync read and the action-condition
	// update. Simulate a pool target changing before the final marker check while
	// the old Updated condition is still True.
	getCount := 0
	mcfgClient.PrependReactor("get", "machineconfignodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		getCount++
		if getCount != 3 {
			return false, nil, nil
		}

		changedTarget := mcn.DeepCopy()
		changedTarget.Spec.ConfigVersion.Desired = "rendered-worker-next"
		return true, changedTarget, nil
	})

	ctrl := &Controller{
		client:     mcfgClient,
		kubeClient: kubeClient,
		nodeLister: corelisters.NewNodeLister(nodeIndexer),
	}

	require.NoError(t, ctrl.syncManualActionStatus(nodeName))
	updatedNode, err := kubeClient.CoreV1().Nodes().Get(t.Context(), nodeName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, oldBootID, updatedNode.Annotations[constants.MachineConfigDaemonLastObservedBootIDAnnotationKey], "must not consume the reboot when the MCN target changed")
}

func TestSyncManualActionStatusDoesNotAdvanceMarkerWhenRebootedWasNotPersisted(t *testing.T) {
	const nodeName = "worker-0"
	const configName = "rendered-worker-current"
	const oldBootID = "boot-id-before-reboot"
	const currentBootID = "boot-id-observed"

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
			Annotations: map[string]string{
				constants.CurrentMachineConfigAnnotationKey:                  configName,
				constants.DesiredMachineConfigAnnotationKey:                  configName,
				constants.MachineConfigDaemonLastObservedBootIDAnnotationKey: oldBootID,
			},
		},
		Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{BootID: currentBootID}},
	}
	mcn := &mcfgv1.MachineConfigNode{
		ObjectMeta: metav1.ObjectMeta{Name: nodeName, Generation: 1},
		Spec: mcfgv1.MachineConfigNodeSpec{
			ConfigVersion: mcfgv1.MachineConfigNodeSpecMachineConfigVersion{Desired: configName},
		},
		Status: mcfgv1.MachineConfigNodeStatus{
			ObservedGeneration: 1,
			ConfigVersion:      &mcfgv1.MachineConfigNodeStatusMachineConfigVersion{Current: configName, Desired: configName},
			Conditions:         []metav1.Condition{{Type: string(mcfgv1.MachineConfigNodeUpdated), Status: metav1.ConditionTrue}},
		},
	}

	nodeIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	require.NoError(t, nodeIndexer.Add(node))
	kubeClient := k8sfake.NewSimpleClientset(node)
	mcfgClient := mcfgfake.NewSimpleClientset(mcn)
	getCount := 0
	mcfgClient.PrependReactor("get", "machineconfignodes", func(k8stesting.Action) (bool, runtime.Object, error) {
		getCount++
		if getCount != 2 {
			return false, nil, nil
		}

		// The initial sync read saw Updated=True, but the action-condition write
		// observes the MCN after an update has started. Keep the underlying object
		// updated so a later fresh convergence check alone would incorrectly pass.
		mcnDuringUpdate := mcn.DeepCopy()
		mcnDuringUpdate.Status.Conditions = []metav1.Condition{{
			Type: string(mcfgv1.MachineConfigNodeUpdated), Status: metav1.ConditionFalse,
		}}
		return true, mcnDuringUpdate, nil
	})

	ctrl := &Controller{
		client:     mcfgClient,
		kubeClient: kubeClient,
		nodeLister: corelisters.NewNodeLister(nodeIndexer),
	}
	require.NoError(t, ctrl.syncManualActionStatus(nodeName))

	updatedNode, err := kubeClient.CoreV1().Nodes().Get(t.Context(), nodeName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, oldBootID, updatedNode.Annotations[constants.MachineConfigDaemonLastObservedBootIDAnnotationKey], "must not consume a boot change when Rebooted=True was skipped")
	assert.Equal(t, 2, getCount, "must not perform the marker convergence check after the action update skipped")
}

func TestRecordLastObservedBootIDDoesNotOverwriteConcurrentNewerBootID(t *testing.T) {
	const nodeName = "worker-0"
	const previousBootID = "boot-id-before-reboot"
	const observedBootID = "boot-id-observed"
	const newerBootID = "boot-id-newer"

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:            nodeName,
			ResourceVersion: "1",
			Annotations: map[string]string{
				constants.MachineConfigDaemonLastObservedBootIDAnnotationKey: previousBootID,
			},
		},
		Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{BootID: observedBootID}},
	}
	kubeClient := k8sfake.NewSimpleClientset(node)
	ctrl := &Controller{kubeClient: kubeClient}
	newerBootObserved := false
	kubeClient.PrependReactor("patch", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
		newerBootObserved = true
		gvr := corev1.SchemeGroupVersion.WithResource("nodes")
		currentObject, err := kubeClient.Tracker().Get(gvr, "", nodeName)
		if err != nil {
			return true, nil, err
		}
		currentNode := currentObject.(*corev1.Node).DeepCopy()
		currentNode.ResourceVersion = "2"
		currentNode.Status.NodeInfo.BootID = newerBootID
		if err := kubeClient.Tracker().Update(gvr, currentNode, ""); err != nil {
			return true, nil, err
		}
		return false, nil, nil
	})

	err := ctrl.recordLastObservedBootID(nodeName, observedBootID, previousBootID)
	require.Error(t, err, "a failed JSON Patch test must be returned so the workqueue can retry")
	assert.True(t, newerBootObserved, "test must inject a newer boot ID between GET and PATCH")

	updatedNode, err := kubeClient.CoreV1().Nodes().Get(t.Context(), nodeName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, newerBootID, updatedNode.Status.NodeInfo.BootID)
	assert.Equal(t, previousBootID, updatedNode.Annotations[constants.MachineConfigDaemonLastObservedBootIDAnnotationKey], "must not overwrite the marker for a newer boot ID")
}
