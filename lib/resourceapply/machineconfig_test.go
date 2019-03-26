package resourceapply

import (
	"fmt"
	"testing"

	igntypes "github.com/coreos/ignition/config/v3_0/types"
	"github.com/davecgh/go-spew/spew"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/fake"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/diff"
	clienttesting "k8s.io/client-go/testing"
)

func TestApplyMachineConfig(t *testing.T) {
	homeDir0 := "/home/dummy-prev"
	homeDir1 := "/home/dummy"

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
				t.Error(diff.ObjectDiff(expected, actual))
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
				t.Error(diff.ObjectDiff(expected, actual))
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
				t.Error(diff.ObjectDiff(expected, actual))
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
				t.Error(diff.ObjectDiff(expected, actual))
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
				Config: igntypes.Config{
					Passwd: igntypes.Passwd{
						Users: []igntypes.PasswdUser{{
							HomeDir: &homeDir1,
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
				ObjectMeta: metav1.ObjectMeta{Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
				Spec: mcfgv1.MachineConfigSpec{
					Config: igntypes.Config{
						Passwd: igntypes.Passwd{
							Users: []igntypes.PasswdUser{{
								HomeDir: &homeDir1,
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
				ObjectMeta: metav1.ObjectMeta{Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
				Spec: mcfgv1.MachineConfigSpec{
					Config: igntypes.Config{
						Passwd: igntypes.Passwd{
							Users: []igntypes.PasswdUser{{
								HomeDir: &homeDir0,
							}},
						},
					},
				},
			},
		},
		input: &mcfgv1.MachineConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "foo"},
			Spec: mcfgv1.MachineConfigSpec{
				Config: igntypes.Config{
					Passwd: igntypes.Passwd{
						Users: []igntypes.PasswdUser{{
							HomeDir: &homeDir1,
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
				ObjectMeta: metav1.ObjectMeta{Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
				Spec: mcfgv1.MachineConfigSpec{
					Config: igntypes.Config{
						Passwd: igntypes.Passwd{
							Users: []igntypes.PasswdUser{{
								HomeDir: &homeDir1,
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
