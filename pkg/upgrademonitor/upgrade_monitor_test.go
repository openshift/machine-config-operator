package upgrademonitor

import (
	"testing"

	apicfgv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8stesting "k8s.io/client-go/testing"
)

func newFakeHandler() ctrlcommon.FeatureGatesHandler {
	return ctrlcommon.NewFeatureGatesHardcodedHandler(
		[]apicfgv1.FeatureGateName{},
		[]apicfgv1.FeatureGateName{},
	)
}

// countSSAActions counts patch actions with ApplyPatchType on machineconfignodes
// for the given subresource ("" = spec Apply, "status" = ApplyStatus).
func countSSAActions(actions []k8stesting.Action, subresource string) int {
	count := 0
	for _, a := range actions {
		pa, ok := a.(k8stesting.PatchAction)
		if !ok {
			continue
		}
		if pa.GetPatchType() == types.ApplyPatchType &&
			pa.GetResource().Resource == "machineconfignodes" &&
			pa.GetSubresource() == subresource {
			count++
		}
	}
	return count
}

// TestSpecNoDiffGuardSkipsApply verifies that GenerateAndApplyMachineConfigNodeSpec
// does not issue an SSA Apply when Node, Pool, and ConfigVersion.Desired are unchanged.
func TestSpecNoDiffGuardSkipsApply(t *testing.T) {
	const nodeName = "worker-1"
	const poolName = "worker"
	const desiredConfig = "rendered-worker-abc123"

	existingMCN := &mcfgv1.MachineConfigNode{
		ObjectMeta: metav1.ObjectMeta{Name: nodeName},
		Spec: mcfgv1.MachineConfigNodeSpec{
			Node:          mcfgv1.MCOObjectReference{Name: nodeName},
			Pool:          mcfgv1.MCOObjectReference{Name: poolName},
			ConfigVersion: mcfgv1.MachineConfigNodeSpecMachineConfigVersion{Desired: desiredConfig},
		},
	}
	fakeClient := fake.NewClientset([]runtime.Object{existingMCN}...)

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
			Annotations: map[string]string{
				daemonconsts.DesiredMachineConfigAnnotationKey: desiredConfig,
			},
		},
	}

	if err := GenerateAndApplyMachineConfigNodeSpec(newFakeHandler(), poolName, node, fakeClient); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if n := countSSAActions(fakeClient.Actions(), ""); n != 0 {
		t.Errorf("expected 0 SSA Apply actions when spec unchanged, got %d", n)
	}
}

// TestSpecNoDiffGuardAllowsApplyOnChange verifies that GenerateAndApplyMachineConfigNodeSpec
// does issue an SSA Apply when ConfigVersion.Desired differs.
func TestSpecNoDiffGuardAllowsApplyOnChange(t *testing.T) {
	const nodeName = "worker-1"
	const poolName = "worker"

	existingMCN := &mcfgv1.MachineConfigNode{
		ObjectMeta: metav1.ObjectMeta{Name: nodeName},
		Spec: mcfgv1.MachineConfigNodeSpec{
			Node:          mcfgv1.MCOObjectReference{Name: nodeName},
			Pool:          mcfgv1.MCOObjectReference{Name: poolName},
			ConfigVersion: mcfgv1.MachineConfigNodeSpecMachineConfigVersion{Desired: "rendered-worker-old"},
		},
	}
	fakeClient := fake.NewClientset([]runtime.Object{existingMCN}...)

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
			Annotations: map[string]string{
				daemonconsts.DesiredMachineConfigAnnotationKey: "rendered-worker-new",
			},
		},
	}

	if err := GenerateAndApplyMachineConfigNodeSpec(newFakeHandler(), poolName, node, fakeClient); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if n := countSSAActions(fakeClient.Actions(), ""); n != 1 {
		t.Errorf("expected 1 SSA Apply action when spec changed, got %d", n)
	}
}

// TestStatusNoDiffGuardSkipsApplyStatus verifies that GenerateAndApplyMachineConfigNodes
// does not issue an SSA ApplyStatus when conditions, ConfigVersion, and ConfigImage
// are all unchanged.
func TestStatusNoDiffGuardSkipsApplyStatus(t *testing.T) {
	const nodeName = "worker-1"
	const poolName = "worker"
	const desiredConfig = "rendered-worker-abc123"

	existingMCN := &mcfgv1.MachineConfigNode{
		ObjectMeta: metav1.ObjectMeta{Name: nodeName},
		Spec: mcfgv1.MachineConfigNodeSpec{
			Node:          mcfgv1.MCOObjectReference{Name: nodeName},
			Pool:          mcfgv1.MCOObjectReference{Name: poolName},
			ConfigVersion: mcfgv1.MachineConfigNodeSpecMachineConfigVersion{Desired: desiredConfig},
		},
		Status: mcfgv1.MachineConfigNodeStatus{
			Conditions: []metav1.Condition{{
				Type:               string(mcfgv1.MachineConfigNodeUpdatePrepared),
				Status:             metav1.ConditionTrue,
				Reason:             "Prepared",
				Message:            "node is prepared",
				LastTransitionTime: metav1.Now(),
			}},
			ConfigVersion: &mcfgv1.MachineConfigNodeStatusMachineConfigVersion{
				Desired: desiredConfig,
			},
		},
	}
	fakeClient := fake.NewClientset([]runtime.Object{existingMCN}...)

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
			Annotations: map[string]string{
				daemonconsts.DesiredMachineConfigAnnotationKey: desiredConfig,
			},
		},
	}

	parentCondition := &Condition{
		State:   mcfgv1.MachineConfigNodeUpdatePrepared,
		Reason:  "Prepared",
		Message: "node is prepared",
	}

	err := GenerateAndApplyMachineConfigNodes(parentCondition, nil, metav1.ConditionTrue, metav1.ConditionFalse, node, fakeClient, newFakeHandler(), poolName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if n := countSSAActions(fakeClient.Actions(), "status"); n != 0 {
		t.Errorf("expected 0 SSA ApplyStatus actions when status unchanged, got %d", n)
	}
}

// TestStatusNoDiffGuardAllowsApplyStatusOnChange verifies that
// GenerateAndApplyMachineConfigNodes does issue an SSA ApplyStatus when a
// condition's Status field changes.
func TestStatusNoDiffGuardAllowsApplyStatusOnChange(t *testing.T) {
	const nodeName = "worker-1"
	const poolName = "worker"
	const desiredConfig = "rendered-worker-abc123"

	existingMCN := &mcfgv1.MachineConfigNode{
		ObjectMeta: metav1.ObjectMeta{Name: nodeName},
		Spec: mcfgv1.MachineConfigNodeSpec{
			Node:          mcfgv1.MCOObjectReference{Name: nodeName},
			Pool:          mcfgv1.MCOObjectReference{Name: poolName},
			ConfigVersion: mcfgv1.MachineConfigNodeSpecMachineConfigVersion{Desired: desiredConfig},
		},
		Status: mcfgv1.MachineConfigNodeStatus{
			Conditions: []metav1.Condition{{
				Type:               string(mcfgv1.MachineConfigNodeUpdatePrepared),
				Status:             metav1.ConditionFalse,
				Reason:             "NotPrepared",
				Message:            "not yet prepared",
				LastTransitionTime: metav1.Now(),
			}},
			ConfigVersion: &mcfgv1.MachineConfigNodeStatusMachineConfigVersion{
				Desired: desiredConfig,
			},
		},
	}
	fakeClient := fake.NewClientset([]runtime.Object{existingMCN}...)

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
			Annotations: map[string]string{
				daemonconsts.DesiredMachineConfigAnnotationKey: desiredConfig,
			},
		},
	}

	parentCondition := &Condition{
		State:   mcfgv1.MachineConfigNodeUpdatePrepared,
		Reason:  "Prepared",
		Message: "node is now prepared",
	}

	err := GenerateAndApplyMachineConfigNodes(parentCondition, nil, metav1.ConditionTrue, metav1.ConditionFalse, node, fakeClient, newFakeHandler(), poolName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if n := countSSAActions(fakeClient.Actions(), "status"); n != 1 {
		t.Errorf("expected 1 SSA ApplyStatus action when condition changed, got %d", n)
	}
}

// TestStatusNoDiffGuardAppliesWhenObservedGenerationStale verifies that the
// no-diff guard issues an SSA ApplyStatus when ObservedGeneration is stale,
// even if conditions and ConfigVersion are otherwise unchanged.
//
// A singleton condition is used so the existing per-condition early-return
// (line 239) is bypassed and mcnStatusUnchanged is actually reached.
func TestStatusNoDiffGuardAppliesWhenObservedGenerationStale(t *testing.T) {
	const nodeName = "worker-1"
	const poolName = "worker"
	const desiredConfig = "rendered-worker-abc123"

	existingMCN := &mcfgv1.MachineConfigNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:       nodeName,
			Generation: 1, // spec generation; SSA will send ObservedGeneration = 2
		},
		Spec: mcfgv1.MachineConfigNodeSpec{
			Node:          mcfgv1.MCOObjectReference{Name: nodeName},
			Pool:          mcfgv1.MCOObjectReference{Name: poolName},
			ConfigVersion: mcfgv1.MachineConfigNodeSpecMachineConfigVersion{Desired: desiredConfig},
		},
		Status: mcfgv1.MachineConfigNodeStatus{
			ObservedGeneration: 0, // stale: does not match Generation+1 = 2
			Conditions: []metav1.Condition{{
				Type:               string(mcfgv1.MachineConfigNodePinnedImageSetsDegraded),
				Status:             metav1.ConditionFalse,
				Reason:             "NotDegraded",
				Message:            "all sets healthy",
				LastTransitionTime: metav1.Now(),
			}},
			ConfigVersion: &mcfgv1.MachineConfigNodeStatusMachineConfigVersion{
				Desired: desiredConfig,
			},
		},
	}
	fakeClient := fake.NewClientset([]runtime.Object{existingMCN}...)

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
			Annotations: map[string]string{
				daemonconsts.DesiredMachineConfigAnnotationKey: desiredConfig,
			},
		},
	}

	// Same condition type/status/message as stored — only ObservedGeneration is stale.
	parentCondition := &Condition{
		State:   mcfgv1.MachineConfigNodePinnedImageSetsDegraded,
		Reason:  "NotDegraded",
		Message: "all sets healthy",
	}

	err := GenerateAndApplyMachineConfigNodes(parentCondition, nil, metav1.ConditionFalse, metav1.ConditionFalse, node, fakeClient, newFakeHandler(), poolName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if n := countSSAActions(fakeClient.Actions(), "status"); n != 1 {
		t.Errorf("expected 1 SSA ApplyStatus action when ObservedGeneration is stale, got %d", n)
	}
}

func TestUpdateMachineConfigNodeActionConditionsPreservesUpdateStatus(t *testing.T) {
	const nodeName = "worker-1"
	const configName = "rendered-worker-abc123"

	existing := &mcfgv1.MachineConfigNode{
		ObjectMeta: metav1.ObjectMeta{Name: nodeName},
		Status: mcfgv1.MachineConfigNodeStatus{
			Conditions: []metav1.Condition{
				{Type: string(mcfgv1.MachineConfigNodeUpdated), Status: metav1.ConditionTrue, Reason: "Updated"},
				{Type: string(mcfgv1.MachineConfigNodeUpdatePrepared), Status: metav1.ConditionFalse, Reason: "NotYetOccurred"},
				{Type: string(mcfgv1.MachineConfigNodeUpdateCordoned), Status: metav1.ConditionFalse, Reason: "NotYetOccurred"},
			},
			ConfigVersion: &mcfgv1.MachineConfigNodeStatusMachineConfigVersion{
				Current: configName,
				Desired: configName,
			},
		},
	}
	fakeClient := fake.NewClientset(existing)

	cordoned := metav1.Condition{
		Type:    string(mcfgv1.MachineConfigNodeUpdateCordoned),
		Status:  metav1.ConditionTrue,
		Reason:  "NodeUnschedulable",
		Message: "Node is currently unschedulable.",
	}
	rebooted := metav1.Condition{
		Type:    string(mcfgv1.MachineConfigNodeUpdateRebooted),
		Status:  metav1.ConditionTrue,
		Reason:  "BootIDChanged",
		Message: "A reboot was observed.",
	}
	if persisted, err := UpdateMachineConfigNodeActionConditions(fakeClient, nodeName, cordoned, rebooted); err != nil {
		t.Fatalf("unexpected error updating action condition: %v", err)
	} else if !persisted {
		t.Fatal("expected Rebooted=True to be reported as persisted")
	}

	updated, err := fakeClient.MachineconfigurationV1().MachineConfigNodes().Get(t.Context(), nodeName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed getting MCN: %v", err)
	}
	assertConditionStatus(t, updated.Status.Conditions, mcfgv1.MachineConfigNodeUpdated, metav1.ConditionTrue)
	assertConditionStatus(t, updated.Status.Conditions, mcfgv1.MachineConfigNodeUpdatePrepared, metav1.ConditionFalse)
	assertConditionStatus(t, updated.Status.Conditions, mcfgv1.MachineConfigNodeUpdateCordoned, metav1.ConditionTrue)
	assertConditionStatus(t, updated.Status.Conditions, mcfgv1.MachineConfigNodeUpdateRebooted, metav1.ConditionTrue)
	if updated.Status.ConfigVersion == nil || updated.Status.ConfigVersion.Current != configName || updated.Status.ConfigVersion.Desired != configName {
		t.Fatalf("action condition update changed configVersion: %#v", updated.Status.ConfigVersion)
	}

	statusUpdates := countMachineConfigNodeStatusUpdates(fakeClient.Actions())
	if persisted, err := UpdateMachineConfigNodeActionConditions(fakeClient, nodeName, cordoned, rebooted); err != nil {
		t.Fatalf("unexpected error on idempotent action condition update: %v", err)
	} else if !persisted {
		t.Fatal("expected already-persisted Rebooted=True to be reported as persisted")
	}
	if got := countMachineConfigNodeStatusUpdates(fakeClient.Actions()); got != statusUpdates {
		t.Fatalf("expected idempotent update to avoid another status write, got %d writes after %d", got, statusUpdates)
	}
}

func TestUpdateMachineConfigNodeActionConditionsSkipsUnupdatedNode(t *testing.T) {
	const nodeName = "worker-1"
	existing := &mcfgv1.MachineConfigNode{
		ObjectMeta: metav1.ObjectMeta{Name: nodeName},
		Status: mcfgv1.MachineConfigNodeStatus{
			Conditions: []metav1.Condition{{Type: string(mcfgv1.MachineConfigNodeUpdated), Status: metav1.ConditionFalse}},
		},
	}
	fakeClient := fake.NewClientset(existing)

	persisted, err := UpdateMachineConfigNodeActionConditions(fakeClient, nodeName,
		metav1.Condition{Type: string(mcfgv1.MachineConfigNodeUpdateCordoned), Status: metav1.ConditionTrue},
		metav1.Condition{Type: string(mcfgv1.MachineConfigNodeUpdateRebooted), Status: metav1.ConditionTrue},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if persisted {
		t.Fatal("expected no action conditions to be reported as persisted for an unupdated node")
	}
	if got := countMachineConfigNodeStatusUpdates(fakeClient.Actions()); got != 0 {
		t.Fatalf("expected no status update for a node not marked Updated, got %d", got)
	}
}

func TestUpdateMachineConfigNodeActionConditionsRejectsUpdatedCondition(t *testing.T) {
	const nodeName = "worker-1"
	existing := &mcfgv1.MachineConfigNode{
		ObjectMeta: metav1.ObjectMeta{Name: nodeName},
		Status: mcfgv1.MachineConfigNodeStatus{
			Conditions: []metav1.Condition{{Type: string(mcfgv1.MachineConfigNodeUpdated), Status: metav1.ConditionTrue}},
		},
	}
	fakeClient := fake.NewClientset(existing)

	_, err := UpdateMachineConfigNodeActionConditions(fakeClient, nodeName, metav1.Condition{
		Type:   string(mcfgv1.MachineConfigNodeUpdated),
		Status: metav1.ConditionFalse,
	})
	if err == nil {
		t.Fatal("expected Updated to be rejected as an action condition")
	}
	if got := countMachineConfigNodeStatusUpdates(fakeClient.Actions()); got != 0 {
		t.Fatalf("expected no status update when Updated was supplied, got %d", got)
	}
}

func TestResumedConditionDoesNotMarkUpdatedNodeAsUpdating(t *testing.T) {
	const nodeName = "worker-1"
	const configName = "rendered-worker-abc123"

	newNode := func() (*corev1.Node, *mcfgv1.MachineConfigNode) {
		return &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
					Annotations: map[string]string{
						daemonconsts.CurrentMachineConfigAnnotationKey: configName,
						daemonconsts.DesiredMachineConfigAnnotationKey: configName,
					},
				},
			}, &mcfgv1.MachineConfigNode{
				ObjectMeta: metav1.ObjectMeta{Name: nodeName},
				Spec: mcfgv1.MachineConfigNodeSpec{
					ConfigVersion: mcfgv1.MachineConfigNodeSpecMachineConfigVersion{Desired: configName},
				},
				Status: mcfgv1.MachineConfigNodeStatus{
					ConfigVersion: &mcfgv1.MachineConfigNodeStatusMachineConfigVersion{Current: configName, Desired: configName},
					Conditions: []metav1.Condition{
						{Type: string(mcfgv1.MachineConfigNodeUpdated), Status: metav1.ConditionTrue, Reason: "Updated"},
						{Type: string(mcfgv1.MachineConfigNodeUpdatePrepared), Status: metav1.ConditionFalse, Reason: "NotYetOccurred"},
					},
				},
			}
	}

	tests := []struct {
		name              string
		parent            mcfgv1.StateProgress
		wantUpdatedStatus metav1.ConditionStatus
	}{
		{name: "resumed after a manual reboot", parent: mcfgv1.MachineConfigNodeResumed, wantUpdatedStatus: metav1.ConditionTrue},
		{name: "update phase still marks node as updating", parent: mcfgv1.MachineConfigNodeUpdatePrepared, wantUpdatedStatus: metav1.ConditionFalse},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			node, mcn := newNode()
			fakeClient := fake.NewSimpleClientset(mcn)
			err := GenerateAndApplyMachineConfigNodes(
				&Condition{State: test.parent, Reason: string(test.parent), Message: "test"},
				nil,
				metav1.ConditionTrue,
				metav1.ConditionFalse,
				node,
				fakeClient,
				newFakeHandler(),
				"worker",
			)
			if err != nil {
				t.Fatalf("unexpected error applying %s condition: %v", test.parent, err)
			}

			updated, err := fakeClient.MachineconfigurationV1().MachineConfigNodes().Get(t.Context(), nodeName, metav1.GetOptions{})
			if err != nil {
				t.Fatalf("failed getting MCN: %v", err)
			}
			assertConditionStatus(t, updated.Status.Conditions, mcfgv1.MachineConfigNodeUpdated, test.wantUpdatedStatus)
			if test.parent == mcfgv1.MachineConfigNodeResumed {
				assertConditionStatus(t, updated.Status.Conditions, mcfgv1.MachineConfigNodeResumed, metav1.ConditionTrue)
			}
		})
	}
}

func assertConditionStatus(t *testing.T, conditions []metav1.Condition, conditionType mcfgv1.StateProgress, expected metav1.ConditionStatus) {
	t.Helper()
	for _, condition := range conditions {
		if condition.Type == string(conditionType) {
			if condition.Status != expected {
				t.Fatalf("condition %q status = %q, want %q", conditionType, condition.Status, expected)
			}
			return
		}
	}
	t.Fatalf("condition %q not found", conditionType)
}

func countMachineConfigNodeStatusUpdates(actions []k8stesting.Action) int {
	count := 0
	for _, action := range actions {
		if action.Matches("update", "machineconfignodes") && action.GetSubresource() == "status" {
			count++
		}
	}
	return count
}
