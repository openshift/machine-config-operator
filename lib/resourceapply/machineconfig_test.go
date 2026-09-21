package resourceapply

import (
	"fmt"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	"github.com/davecgh/go-spew/spew"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	"github.com/openshift/machine-config-operator/test/helpers"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/diff"
	clienttesting "k8s.io/client-go/testing"
)

func TestApplyMachineConfig(t *testing.T) {
	tests := []struct {
		existing []runtime.Object
		input    *mcfgv1.MachineConfig

		expectedModified bool
		verifyActions    func(actions []clienttesting.Action, t *testing.T)
	}{{
		input: &mcfgv1.MachineConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "foo"},
		},
		expectedModified: true,
		verifyActions: func(actions []clienttesting.Action, t *testing.T) {
			if len(actions) != 2 {
				t.Fatal(spew.Sdump(actions))
			}
			if !actions[0].Matches("get", "machineconfigs") || actions[0].(clienttesting.GetAction).GetName() != "foo" {
				t.Error(spew.Sdump(actions))
			}
			if !actions[1].Matches("create", "machineconfigs") {
				t.Error(spew.Sdump(actions))
			}
			expected := &mcfgv1.MachineConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "foo"},
			}
			actual := actions[1].(clienttesting.CreateAction).GetObject().(*mcfgv1.MachineConfig)
			if !equality.Semantic.DeepEqual(expected, actual) {
				t.Error(diff.Diff(expected, actual))
			}
		},
	}, {
		existing: []runtime.Object{
			&mcfgv1.MachineConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
			},
		},
		input: &mcfgv1.MachineConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "foo"},
		},

		expectedModified: false,
		verifyActions: func(actions []clienttesting.Action, t *testing.T) {
			if len(actions) != 1 {
				t.Fatal(spew.Sdump(actions))
			}
			if !actions[0].Matches("get", "machineconfigs") || actions[0].(clienttesting.GetAction).GetName() != "foo" {
				t.Error(spew.Sdump(actions))
			}
		},
	}, {
		existing: []runtime.Object{
			&mcfgv1.MachineConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
			},
		},
		input: &mcfgv1.MachineConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "foo", Labels: map[string]string{"new": "merge"}},
		},

		expectedModified: true,
		verifyActions: func(actions []clienttesting.Action, t *testing.T) {
			if len(actions) != 2 {
				t.Fatal(spew.Sdump(actions))
			}
			if !actions[0].Matches("get", "machineconfigs") || actions[0].(clienttesting.GetAction).GetName() != "foo" {
				t.Error(spew.Sdump(actions))
			}
			if !actions[1].Matches("update", "machineconfigs") {
				t.Error(spew.Sdump(actions))
			}
			expected := &mcfgv1.MachineConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "foo", Labels: map[string]string{"extra": "leave-alone", "new": "merge"}},
			}
			actual := actions[1].(clienttesting.UpdateAction).GetObject().(*mcfgv1.MachineConfig)
			if !equality.Semantic.DeepEqual(expected, actual) {
				t.Error(diff.Diff(expected, actual))
			}
		},
	}, {
		existing: []runtime.Object{
			&mcfgv1.MachineConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
			},
		},
		input: &mcfgv1.MachineConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "foo"},
			Spec: mcfgv1.MachineConfigSpec{
				OSImageURL: "//:dummy0",
			},
		},

		expectedModified: true,
		verifyActions: func(actions []clienttesting.Action, t *testing.T) {
			if len(actions) != 2 {
				t.Fatal(spew.Sdump(actions))
			}
			if !actions[0].Matches("get", "machineconfigs") || actions[0].(clienttesting.GetAction).GetName() != "foo" {
				t.Error(spew.Sdump(actions))
			}
			if !actions[1].Matches("update", "machineconfigs") {
				t.Error(spew.Sdump(actions))
			}
			expected := &mcfgv1.MachineConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
				Spec: mcfgv1.MachineConfigSpec{
					OSImageURL: "//:dummy0",
				},
			}
			actual := actions[1].(clienttesting.UpdateAction).GetObject().(*mcfgv1.MachineConfig)
			if !equality.Semantic.DeepEqual(expected, actual) {
				t.Error(diff.Diff(expected, actual))
			}
		},
	}, {
		existing: []runtime.Object{
			&mcfgv1.MachineConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
				Spec: mcfgv1.MachineConfigSpec{
					OSImageURL: "//:dummy0",
				},
			},
		},
		input: &mcfgv1.MachineConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "foo"},
			Spec: mcfgv1.MachineConfigSpec{
				OSImageURL: "//:dummy1",
			},
		},

		expectedModified: true,
		verifyActions: func(actions []clienttesting.Action, t *testing.T) {
			if len(actions) != 2 {
				t.Fatal(spew.Sdump(actions))
			}
			if !actions[0].Matches("get", "machineconfigs") || actions[0].(clienttesting.GetAction).GetName() != "foo" {
				t.Error(spew.Sdump(actions))
			}
			if !actions[1].Matches("update", "machineconfigs") {
				t.Error(spew.Sdump(actions))
			}
			expected := &mcfgv1.MachineConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
				Spec: mcfgv1.MachineConfigSpec{
					OSImageURL: "//:dummy1",
				},
			}
			actual := actions[1].(clienttesting.UpdateAction).GetObject().(*mcfgv1.MachineConfig)
			if !equality.Semantic.DeepEqual(expected, actual) {
				t.Error(diff.Diff(expected, actual))
			}
		},
	}, {
		existing: []runtime.Object{
			&mcfgv1.MachineConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
			},
		},
		input: &mcfgv1.MachineConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "foo"},
			Spec: mcfgv1.MachineConfigSpec{
				Config: runtime.RawExtension{
					Raw: helpers.MarshalOrDie(&ign3types.Config{
						Passwd: ign3types.Passwd{
							Users: []ign3types.PasswdUser{{
								HomeDir: helpers.StrToPtr("/home/dummy"),
							}},
						},
					}),
				},
			},
		},

		expectedModified: true,
		verifyActions: func(actions []clienttesting.Action, t *testing.T) {
			if len(actions) != 2 {
				t.Fatal(spew.Sdump(actions))
			}
			if !actions[0].Matches("get", "machineconfigs") || actions[0].(clienttesting.GetAction).GetName() != "foo" {
				t.Error(spew.Sdump(actions))
			}
			if !actions[1].Matches("update", "machineconfigs") {
				t.Error(spew.Sdump(actions))
			}
			expected := &mcfgv1.MachineConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
				Spec: mcfgv1.MachineConfigSpec{
					Config: runtime.RawExtension{
						Raw: helpers.MarshalOrDie(&ign3types.Config{
							Passwd: ign3types.Passwd{
								Users: []ign3types.PasswdUser{{
									HomeDir: helpers.StrToPtr("/home/dummy"),
								}},
							},
						}),
					},
				},
			}
			actual := actions[1].(clienttesting.UpdateAction).GetObject().(*mcfgv1.MachineConfig)
			if !equality.Semantic.DeepEqual(expected, actual) {
				t.Error(diff.Diff(expected, actual))
			}
		},
	}, {
		existing: []runtime.Object{
			&mcfgv1.MachineConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
				Spec: mcfgv1.MachineConfigSpec{
					Config: runtime.RawExtension{
						Raw: helpers.MarshalOrDie(&ign3types.Config{
							Passwd: ign3types.Passwd{
								Users: []ign3types.PasswdUser{{
									HomeDir: helpers.StrToPtr("/home/dummy-prev"),
								}},
							},
						}),
					},
				},
			},
		},
		input: &mcfgv1.MachineConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "foo"},
			Spec: mcfgv1.MachineConfigSpec{
				Config: runtime.RawExtension{
					Raw: helpers.MarshalOrDie(&ign3types.Config{
						Passwd: ign3types.Passwd{
							Users: []ign3types.PasswdUser{{
								HomeDir: helpers.StrToPtr("/home/dummy"),
							}},
						},
					}),
				},
			},
		},

		expectedModified: true,
		verifyActions: func(actions []clienttesting.Action, t *testing.T) {
			if len(actions) != 2 {
				t.Fatal(spew.Sdump(actions))
			}
			if !actions[0].Matches("get", "machineconfigs") || actions[0].(clienttesting.GetAction).GetName() != "foo" {
				t.Error(spew.Sdump(actions))
			}
			if !actions[1].Matches("update", "machineconfigs") {
				t.Error(spew.Sdump(actions))
			}
			expected := &mcfgv1.MachineConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
				Spec: mcfgv1.MachineConfigSpec{
					Config: runtime.RawExtension{
						Raw: helpers.MarshalOrDie(&ign3types.Config{
							Passwd: ign3types.Passwd{
								Users: []ign3types.PasswdUser{{
									HomeDir: helpers.StrToPtr("/home/dummy"),
								}},
							},
						}),
					},
				},
			}
			actual := actions[1].(clienttesting.UpdateAction).GetObject().(*mcfgv1.MachineConfig)
			if !equality.Semantic.DeepEqual(expected, actual) {
				t.Error(diff.Diff(expected, actual))
			}
		},
	}}

	for idx, test := range tests {
		t.Run(fmt.Sprintf("test#%d", idx), func(t *testing.T) {
			client := fake.NewSimpleClientset(test.existing...)
			_, actualModified, err := ApplyMachineConfig(client.MachineconfigurationV1(), test.input)
			if err != nil {
				t.Fatal(err)
			}
			if test.expectedModified != actualModified {
				t.Errorf("expected %v, got %v", test.expectedModified, actualModified)
			}
			test.verifyActions(client.Actions(), t)
		})
	}
}

// TestIsApplyErrorRetriable verifies all cases that IsApplyErrorRetriable considers retriable,
// and that unrelated errors are not.
func TestIsApplyErrorRetriable(t *testing.T) {
	mcnGR := schema.GroupResource{Group: "machineconfiguration.openshift.io", Resource: "machineconfignodes"}

	tests := []struct {
		name      string
		err       error
		retriable bool
	}{
		{
			name:      "conflict error is retriable",
			err:       apierrors.NewConflict(mcnGR, "node-1", fmt.Errorf("the object has been modified")),
			retriable: true,
		},
		{
			name:      "timeout error is retriable",
			err:       apierrors.NewTimeoutError("request timed out", 10),
			retriable: true,
		},
		{
			name:      "rpc error is retriable",
			err:       fmt.Errorf("rpc error: code = Unavailable desc = transport is closing"),
			retriable: true,
		},
		{
			name:      "not found error is not retriable",
			err:       apierrors.NewNotFound(mcnGR, "node-1"),
			retriable: false,
		},
		{
			name:      "forbidden error is not retriable",
			err:       apierrors.NewForbidden(mcnGR, "node-1", fmt.Errorf("access denied")),
			retriable: false,
		},
		{
			name:      "unrelated error is not retriable",
			err:       fmt.Errorf("something went wrong"),
			retriable: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsApplyErrorRetriable(tc.err); got != tc.retriable {
				t.Errorf("IsApplyErrorRetriable(%v) = %v, want %v", tc.err, got, tc.retriable)
			}
		})
	}
}

// TestApplyMachineConfigNodeConflictIsRetriable verifies that ApplyMachineConfigNode surfaces a
// conflict error that IsApplyErrorRetriable recognises, so the retry.OnError wrapper in
// syncMachineConfigNodes can retry it correctly.
func TestApplyMachineConfigNodeConflictIsRetriable(t *testing.T) {
	existing := &mcfgv1.MachineConfigNode{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1", ResourceVersion: "100"},
		Spec: mcfgv1.MachineConfigNodeSpec{
			Pool: mcfgv1.MCOObjectReference{Name: "worker"},
		},
	}
	required := &mcfgv1.MachineConfigNode{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Spec: mcfgv1.MachineConfigNodeSpec{
			Pool: mcfgv1.MCOObjectReference{Name: "master"},
		},
	}

	client := fake.NewSimpleClientset(existing)
	conflictErr := apierrors.NewConflict(
		schema.GroupResource{Group: "machineconfiguration.openshift.io", Resource: "machineconfignodes"},
		"node-1",
		fmt.Errorf("the object has been modified; please apply your changes to the latest version and try again"),
	)
	client.PrependReactor("update", "machineconfignodes", func(_ clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, conflictErr
	})

	_, _, err := ApplyMachineConfigNode(client.MachineconfigurationV1(), required)
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	if !apierrors.IsConflict(err) {
		t.Fatalf("expected conflict error, got: %v", err)
	}
	if !IsApplyErrorRetriable(err) {
		t.Fatal("conflict error should be retriable via IsApplyErrorRetriable")
	}
}
