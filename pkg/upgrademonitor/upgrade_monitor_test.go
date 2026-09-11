package upgrademonitor

import (
	"context"
	"errors"
	"testing"

	apicfgv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

	if err := GenerateAndApplyMachineConfigNodeSpec(context.Background(), newFakeHandler(), poolName, node, fakeClient); err != nil {
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

	if err := GenerateAndApplyMachineConfigNodeSpec(context.Background(), newFakeHandler(), poolName, node, fakeClient); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if n := countSSAActions(fakeClient.Actions(), ""); n != 1 {
		t.Errorf("expected 1 SSA Apply action when spec changed, got %d", n)
	}
}

func TestSpecGetErrorIsPropagated(t *testing.T) {
	const nodeName = "worker-1"
	fakeClient := fake.NewSimpleClientset()
	fakeClient.PrependReactor("get", "machineconfignodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewServiceUnavailable("temporarily unavailable")
	})
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}

	err := GenerateAndApplyMachineConfigNodeSpec(context.Background(), newFakeHandler(), "worker", node, fakeClient)
	if !apierrors.IsServiceUnavailable(err) {
		t.Fatalf("expected service unavailable error, got %v", err)
	}
	if actions := fakeClient.Actions(); len(actions) != 1 || !actions[0].Matches("get", "machineconfignodes") {
		t.Fatalf("expected only the failed get action, got %v", actions)
	}
}

func TestSpecCreatesMachineConfigNodeWhenMissing(t *testing.T) {
	const nodeName = "worker-1"
	fakeClient := fake.NewSimpleClientset()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}

	if err := GenerateAndApplyMachineConfigNodeSpec(context.Background(), newFakeHandler(), "worker", node, fakeClient); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	created, err := fakeClient.MachineconfigurationV1().MachineConfigNodes().Get(context.Background(), nodeName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting created MachineConfigNode: %v", err)
	}
	if created.Spec.Node.Name != nodeName || created.Spec.Pool.Name != "worker" {
		t.Fatalf("unexpected created MachineConfigNode spec: %#v", created.Spec)
	}
}

func TestMachineConfigNodeOperationsStopOnCanceledContext(t *testing.T) {
	const nodeName = "worker-1"
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
	tests := []struct {
		name string
		run  func(context.Context, *fake.Clientset) error
	}{
		{
			name: "status",
			run: func(ctx context.Context, client *fake.Clientset) error {
				return GenerateAndApplyMachineConfigNodesWithContext(
					ctx,
					&Condition{State: mcfgv1.MachineConfigNodeUpdated, Reason: "Updated", Message: "Node updated"},
					nil,
					metav1.ConditionTrue,
					metav1.ConditionFalse,
					node,
					client,
					newFakeHandler(),
					"worker",
				)
			},
		},
		{
			name: "spec",
			run: func(ctx context.Context, client *fake.Clientset) error {
				return GenerateAndApplyMachineConfigNodeSpec(ctx, newFakeHandler(), "worker", node, client)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			ctx, cancel := context.WithCancel(context.Background())
			client.PrependReactor("get", "machineconfignodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
				cancel()
				return false, nil, nil
			})

			err := test.run(ctx, client)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("expected context cancellation, got %v", err)
			}
			actions := client.Actions()
			if len(actions) != 1 || !actions[0].Matches("get", "machineconfignodes") {
				t.Fatalf("expected cancellation after the initial get with no subsequent MachineConfigNode API actions, got %v", actions)
			}
		})
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
