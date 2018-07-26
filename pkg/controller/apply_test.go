package controller

import (
	"fmt"
	"testing"

	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
	"github.com/davecgh/go-spew/spew"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/fake"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/diff"
	clienttesting "k8s.io/client-go/testing"
)

func TestMergeOwnerRefs(t *testing.T) {
	tests := []struct {
		existing []metav1.OwnerReference
		input    []metav1.OwnerReference

		expectedModified bool
		expected         []metav1.OwnerReference
	}{{
		existing: []metav1.OwnerReference{},
		input:    []metav1.OwnerReference{},

		expectedModified: false,
		expected:         []metav1.OwnerReference{},
	}, {
		existing: []metav1.OwnerReference{},
		input: []metav1.OwnerReference{{
			UID: types.UID("uid-1"),
		}},

		expectedModified: true,
		expected: []metav1.OwnerReference{{
			UID: types.UID("uid-1"),
		}},
	}, {
		existing: []metav1.OwnerReference{{
			UID: types.UID("uid-2"),
		}, {
			UID: types.UID("uid-3"),
		}},
		input: []metav1.OwnerReference{{
			UID: types.UID("uid-1"),
		}},

		expectedModified: true,
		expected: []metav1.OwnerReference{{
			UID: types.UID("uid-2"),
		}, {
			UID: types.UID("uid-3"),
		}, {
			UID: types.UID("uid-1"),
		}},
	}, {
		existing: []metav1.OwnerReference{{
			UID: types.UID("uid-1"),
		}},
		input: []metav1.OwnerReference{{
			UID: types.UID("uid-1"),
		}},

		expectedModified: false,
		expected: []metav1.OwnerReference{{
			UID: types.UID("uid-1"),
		}},
	}, {
		existing: []metav1.OwnerReference{{
			UID: types.UID("uid-1"),
		}},
		input: []metav1.OwnerReference{{
			Controller: boolPtr(true),
			UID:        types.UID("uid-1"),
		}},

		expectedModified: true,
		expected: []metav1.OwnerReference{{
			Controller: boolPtr(true),
			UID:        types.UID("uid-1"),
		}},
	}, {
		existing: []metav1.OwnerReference{{
			Controller: boolPtr(false),
			UID:        types.UID("uid-1"),
		}},
		input: []metav1.OwnerReference{{
			Controller: boolPtr(true),
			UID:        types.UID("uid-1"),
		}},

		expectedModified: true,
		expected: []metav1.OwnerReference{{
			Controller: boolPtr(true),
			UID:        types.UID("uid-1"),
		}},
	}}

	for idx, test := range tests {
		t.Run(fmt.Sprintf("test#%d", idx), func(t *testing.T) {
			modified := boolPtr(false)
			mergeOwnerRefs(modified, &test.existing, test.input)
			if *modified != test.expectedModified {
				t.Fatalf("mismatch modified got: %v want: %v", *modified, test.expectedModified)
			}

			if !equality.Semantic.DeepEqual(test.existing, test.expected) {
				t.Fatalf("mismatch ownerefs got: %v want: %v", test.existing, test.expected)
			}
		})
	}
}

func TestApplyMachineConfig(t *testing.T) {
	tests := []struct {
		existing []runtime.Object
		input    *mcfgv1.MachineConfig

		expectedModified bool
		verifyActions    func(actions []clienttesting.Action, t *testing.T)
	}{{
		input: &mcfgv1.MachineConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo"},
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
				ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo"},
			}
			actual := actions[1].(clienttesting.CreateAction).GetObject().(*mcfgv1.MachineConfig)
			if !equality.Semantic.DeepEqual(expected, actual) {
				t.Error(diff.ObjectDiff(expected, actual))
			}
		},
	}, {
		existing: []runtime.Object{
			&mcfgv1.MachineConfig{
				ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
			},
		},
		input: &mcfgv1.MachineConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo"},
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
				ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
			},
		},
		input: &mcfgv1.MachineConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo", Labels: map[string]string{"new": "merge"}},
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
				ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo", Labels: map[string]string{"extra": "leave-alone", "new": "merge"}},
			}
			actual := actions[1].(clienttesting.UpdateAction).GetObject().(*mcfgv1.MachineConfig)
			if !equality.Semantic.DeepEqual(expected, actual) {
				t.Error(diff.ObjectDiff(expected, actual))
			}
		},
	}, {
		existing: []runtime.Object{
			&mcfgv1.MachineConfig{
				ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
			},
		},
		input: &mcfgv1.MachineConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo"},
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
				ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
				Spec: mcfgv1.MachineConfigSpec{
					OSImageURL: "//:dummy0",
				},
			}
			actual := actions[1].(clienttesting.UpdateAction).GetObject().(*mcfgv1.MachineConfig)
			if !equality.Semantic.DeepEqual(expected, actual) {
				t.Error(diff.ObjectDiff(expected, actual))
			}
		},
	}, {
		existing: []runtime.Object{
			&mcfgv1.MachineConfig{
				ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
				Spec: mcfgv1.MachineConfigSpec{
					OSImageURL: "//:dummy0",
				},
			},
		},
		input: &mcfgv1.MachineConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo"},
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
				ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
				Spec: mcfgv1.MachineConfigSpec{
					OSImageURL: "//:dummy1",
				},
			}
			actual := actions[1].(clienttesting.UpdateAction).GetObject().(*mcfgv1.MachineConfig)
			if !equality.Semantic.DeepEqual(expected, actual) {
				t.Error(diff.ObjectDiff(expected, actual))
			}
		},
	}, {
		existing: []runtime.Object{
			&mcfgv1.MachineConfig{
				ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
			},
		},
		input: &mcfgv1.MachineConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo"},
			Spec: mcfgv1.MachineConfigSpec{
				Config: ignv2_2types.Config{
					Passwd: ignv2_2types.Passwd{
						Users: []ignv2_2types.PasswdUser{{
							HomeDir: "/home/dummy",
						}},
					},
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
				ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
				Spec: mcfgv1.MachineConfigSpec{
					Config: ignv2_2types.Config{
						Passwd: ignv2_2types.Passwd{
							Users: []ignv2_2types.PasswdUser{{
								HomeDir: "/home/dummy",
							}},
						},
					},
				},
			}
			actual := actions[1].(clienttesting.UpdateAction).GetObject().(*mcfgv1.MachineConfig)
			if !equality.Semantic.DeepEqual(expected, actual) {
				t.Error(diff.ObjectDiff(expected, actual))
			}
		},
	}, {
		existing: []runtime.Object{
			&mcfgv1.MachineConfig{
				ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
				Spec: mcfgv1.MachineConfigSpec{
					Config: ignv2_2types.Config{
						Passwd: ignv2_2types.Passwd{
							Users: []ignv2_2types.PasswdUser{{
								HomeDir: "/home/dummy-prev",
							}},
						},
					},
				},
			},
		},
		input: &mcfgv1.MachineConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo"},
			Spec: mcfgv1.MachineConfigSpec{
				Config: ignv2_2types.Config{
					Passwd: ignv2_2types.Passwd{
						Users: []ignv2_2types.PasswdUser{{
							HomeDir: "/home/dummy",
						}},
					},
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
				ObjectMeta: metav1.ObjectMeta{Namespace: "one-ns", Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
				Spec: mcfgv1.MachineConfigSpec{
					Config: ignv2_2types.Config{
						Passwd: ignv2_2types.Passwd{
							Users: []ignv2_2types.PasswdUser{{
								HomeDir: "/home/dummy",
							}},
						},
					},
				},
			}
			actual := actions[1].(clienttesting.UpdateAction).GetObject().(*mcfgv1.MachineConfig)
			if !equality.Semantic.DeepEqual(expected, actual) {
				t.Error(diff.ObjectDiff(expected, actual))
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
