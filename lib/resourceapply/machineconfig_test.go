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

func TestApplyMachineConfigNode(t *testing.T) {
	const name = "worker-0"

	t.Run("create", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		required := &mcfgv1.MachineConfigNode{ObjectMeta: metav1.ObjectMeta{Name: name}}

		actual, modified, err := ApplyMachineConfigNode(client.MachineconfigurationV1(), required)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !modified {
			t.Fatal("expected create to report modified")
		}
		if actual == nil || actual.Name != name {
			t.Fatalf("expected created MachineConfigNode %q, got %#v", name, actual)
		}
		if actions := client.Actions(); len(actions) != 2 || !actions[0].Matches("get", "machineconfignodes") || !actions[1].Matches("create", "machineconfignodes") {
			t.Fatalf("unexpected actions: %s", spew.Sdump(actions))
		}
	})

	t.Run("no change", func(t *testing.T) {
		existing := &mcfgv1.MachineConfigNode{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"preserve": "true"}},
			Spec: mcfgv1.MachineConfigNodeSpec{
				Node: mcfgv1.MCOObjectReference{Name: name},
				Pool: mcfgv1.MCOObjectReference{Name: "worker"},
			},
		}
		client := fake.NewSimpleClientset(existing)
		required := &mcfgv1.MachineConfigNode{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       existing.Spec,
		}

		actual, modified, err := ApplyMachineConfigNode(client.MachineconfigurationV1(), required)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if modified {
			t.Fatal("expected unchanged MachineConfigNode to report unmodified")
		}
		if actual == nil || actual.Labels["preserve"] != "true" {
			t.Fatalf("expected existing MachineConfigNode to be returned, got %#v", actual)
		}
		if actions := client.Actions(); len(actions) != 1 || !actions[0].Matches("get", "machineconfignodes") {
			t.Fatalf("unexpected actions: %s", spew.Sdump(actions))
		}
	})

	t.Run("conflict re-fetches and re-merges", func(t *testing.T) {
		existing := &mcfgv1.MachineConfigNode{
			ObjectMeta: metav1.ObjectMeta{Name: name, ResourceVersion: "1", Labels: map[string]string{"original": "preserved"}},
			Spec: mcfgv1.MachineConfigNodeSpec{
				Node:          mcfgv1.MCOObjectReference{Name: name},
				Pool:          mcfgv1.MCOObjectReference{Name: "old-pool"},
				ConfigVersion: mcfgv1.MachineConfigNodeSpecMachineConfigVersion{Desired: "concurrent-config"},
			},
		}
		client := fake.NewSimpleClientset(existing)
		required := &mcfgv1.MachineConfigNode{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"required": "merged"}},
			Spec: mcfgv1.MachineConfigNodeSpec{
				Node: mcfgv1.MCOObjectReference{Name: name},
				Pool: mcfgv1.MCOObjectReference{Name: "new-pool"},
			},
		}

		updateCalls := 0
		client.PrependReactor("update", "machineconfignodes", func(action clienttesting.Action) (bool, runtime.Object, error) {
			updateCalls++
			updated := action.(clienttesting.UpdateAction).GetObject().(*mcfgv1.MachineConfigNode)
			if updateCalls == 1 {
				if updated.ResourceVersion != "1" {
					t.Fatalf("expected first update with resource version 1, got %q", updated.ResourceVersion)
				}
				latest := existing.DeepCopy()
				latest.ResourceVersion = "2"
				latest.Labels["concurrent"] = "preserved"
				if err := client.Tracker().Update(mcfgv1.SchemeGroupVersion.WithResource("machineconfignodes"), latest, ""); err != nil {
					t.Fatalf("updating tracker: %v", err)
				}
				return true, nil, apierrors.NewConflict(schema.GroupResource{Group: mcfgv1.GroupName, Resource: "machineconfignodes"}, name, fmt.Errorf("resource version changed"))
			}
			if updated.ResourceVersion != "2" {
				t.Fatalf("expected retry with resource version 2, got %q", updated.ResourceVersion)
			}
			return false, nil, nil
		})

		actual, modified, err := ApplyMachineConfigNode(client.MachineconfigurationV1(), required)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !modified {
			t.Fatal("expected conflict retry update to report modified")
		}
		if updateCalls != 2 {
			t.Fatalf("expected 2 update attempts, got %d", updateCalls)
		}
		if actual.Spec.Pool.Name != "new-pool" || actual.Spec.ConfigVersion.Desired != "concurrent-config" {
			t.Fatalf("expected required pool and concurrent config to be preserved, got %#v", actual.Spec)
		}
		for key, value := range map[string]string{"original": "preserved", "concurrent": "preserved", "required": "merged"} {
			if actual.Labels[key] != value {
				t.Errorf("expected label %s=%s, got %q", key, value, actual.Labels[key])
			}
		}
		actions := client.Actions()
		if len(actions) != 4 || !actions[0].Matches("get", "machineconfignodes") || !actions[1].Matches("update", "machineconfignodes") || !actions[2].Matches("get", "machineconfignodes") || !actions[3].Matches("update", "machineconfignodes") {
			t.Fatalf("unexpected actions: %s", spew.Sdump(actions))
		}
	})
}
