package resourcemerge

import (
	"fmt"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

func TestMergeDaemonSetUpdateStrategy(t *testing.T) {
	// The merge functions set "modified" to true if pointer fields
	// are nil. Since this test doesn't "zero" all possible pointer
	// fields, modified will be true, so this test expects this
	// behavior
	daemonset_update_strategy_tests := []struct {
		existing appsv1.DaemonSet
		input    appsv1.DaemonSet

		expectedModified bool
		expected         appsv1.DaemonSet
	}{

		{
			existing: appsv1.DaemonSet{
				Spec: appsv1.DaemonSetSpec{
					UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
						Type: "RollingUpdate",
						RollingUpdate: &appsv1.RollingUpdateDaemonSet{
							MaxUnavailable: &intstr.IntOrString{Type: 0, IntVal: 1, StrVal: ""},
							MaxSurge:       &intstr.IntOrString{Type: 0, IntVal: 1, StrVal: ""},
						},
					},
				},
			},
			input: appsv1.DaemonSet{
				Spec: appsv1.DaemonSetSpec{
					UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
						Type: "RollingUpdate",
						RollingUpdate: &appsv1.RollingUpdateDaemonSet{
							MaxUnavailable: &intstr.IntOrString{Type: 0, IntVal: 1, StrVal: ""},
							MaxSurge:       &intstr.IntOrString{Type: 0, IntVal: 1, StrVal: ""},
						},
					},
				},
			},
			// this "true" is expected true because of the nil pointers elsewhere in the DaemonSet struct
			// it always takes new struct and sets modified to "true" in event of nil
			expectedModified: true,
			expected: appsv1.DaemonSet{
				Spec: appsv1.DaemonSetSpec{
					UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
						Type: "RollingUpdate",
						RollingUpdate: &appsv1.RollingUpdateDaemonSet{
							MaxUnavailable: &intstr.IntOrString{Type: 0, IntVal: 1, StrVal: ""},
							MaxSurge:       &intstr.IntOrString{Type: 0, IntVal: 1, StrVal: ""},
						},
					},
				},
			},
		}, {
			existing: appsv1.DaemonSet{
				Spec: appsv1.DaemonSetSpec{
					UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
						Type: "OnDelete",
					},
				},
			},
			input: appsv1.DaemonSet{
				Spec: appsv1.DaemonSetSpec{
					UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
						Type: "RollingUpdate",
						RollingUpdate: &appsv1.RollingUpdateDaemonSet{
							MaxUnavailable: &intstr.IntOrString{Type: 0, IntVal: 1, StrVal: ""},
							MaxSurge:       &intstr.IntOrString{Type: 0, IntVal: 1, StrVal: ""},
						},
					},
				},
			},

			expectedModified: true,
			expected: appsv1.DaemonSet{
				Spec: appsv1.DaemonSetSpec{
					UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
						Type: "RollingUpdate",
						RollingUpdate: &appsv1.RollingUpdateDaemonSet{
							MaxUnavailable: &intstr.IntOrString{Type: 0, IntVal: 1, StrVal: ""},
							MaxSurge:       &intstr.IntOrString{Type: 0, IntVal: 1, StrVal: ""},
						},
					},
				},
			},
		},
		{
			existing: appsv1.DaemonSet{
				Spec: appsv1.DaemonSetSpec{
					UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
						Type: "RollingUpdate",
						RollingUpdate: &appsv1.RollingUpdateDaemonSet{
							MaxUnavailable: &intstr.IntOrString{Type: 0, IntVal: 1, StrVal: ""},
							MaxSurge:       &intstr.IntOrString{Type: 0, IntVal: 1, StrVal: ""},
						},
					},
				},
			},
			input: appsv1.DaemonSet{
				Spec: appsv1.DaemonSetSpec{
					UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
						Type: "RollingUpdate",
						RollingUpdate: &appsv1.RollingUpdateDaemonSet{
							MaxUnavailable: &intstr.IntOrString{Type: 0, IntVal: 2, StrVal: ""},
							MaxSurge:       &intstr.IntOrString{Type: 0, IntVal: 3, StrVal: ""},
						},
					},
				},
			},
			expectedModified: true,
			expected: appsv1.DaemonSet{
				Spec: appsv1.DaemonSetSpec{
					UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
						Type: "RollingUpdate",
						RollingUpdate: &appsv1.RollingUpdateDaemonSet{
							MaxUnavailable: &intstr.IntOrString{Type: 0, IntVal: 2, StrVal: ""},
							MaxSurge:       &intstr.IntOrString{Type: 0, IntVal: 3, StrVal: ""},
						},
					},
				},
			},
		},
	}

	for idx, test := range daemonset_update_strategy_tests {
		t.Run(fmt.Sprintf("test#%d", idx), func(t *testing.T) {
			modified := ptr.To(false)
			EnsureDaemonSet(modified, &test.existing, test.input)

			if *modified != test.expectedModified {
				t.Fatalf("mismatch updatestrategy got: %v want: %v", *modified, test.expectedModified)
			}

			if !equality.Semantic.DeepEqual(test.existing, test.expected) {
				t.Fatalf("mismatch updatestrategy got: %v want: %v", test.existing, test.expected)
			}
		})
	}
}

func deploymentFixture(replicas int32) appsv1.Deployment {
	labels := map[string]string{"k8s-app": "mcc"}
	return appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "machine-config-controller", Namespace: "openshift-machine-config-operator"},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(replicas),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:                     "c",
						Image:                    "test",
						TerminationMessagePath:   corev1.TerminationMessagePathDefault,
						ImagePullPolicy:          corev1.PullIfNotPresent,
					}},
				},
			},
		},
	}
}

func TestEnsureDeploymentReplicasMerge(t *testing.T) {
	t.Run("updates existing replicas when required specifies a different count", func(t *testing.T) {
		existing := deploymentFixture(0)
		required := deploymentFixture(1)
		modified := ptr.To(false)
		EnsureDeployment(modified, &existing, required)
		if !*modified {
			t.Fatal("expected modified true when correcting replicas 0 -> 1")
		}
		if existing.Spec.Replicas == nil || *existing.Spec.Replicas != 1 {
			t.Fatalf("expected replicas 1, got %#v", existing.Spec.Replicas)
		}
	})

	t.Run("does not change replicas when required omits replicas", func(t *testing.T) {
		existing := deploymentFixture(3)
		required := deploymentFixture(3)
		required.Spec.Replicas = nil
		modified := ptr.To(false)
		EnsureDeployment(modified, &existing, required)
		if existing.Spec.Replicas == nil || *existing.Spec.Replicas != 3 {
			t.Fatalf("expected replicas unchanged at 3, got %#v", existing.Spec.Replicas)
		}
	})

	t.Run("replicas stay at 1 when required also specifies 1", func(t *testing.T) {
		existing := deploymentFixture(1)
		required := deploymentFixture(1)
		modified := ptr.To(false)
		EnsureDeployment(modified, &existing, required)
		// modified may still be true if merge fills defaults on metadata/template; replicas must remain 1.
		if existing.Spec.Replicas == nil || *existing.Spec.Replicas != 1 {
			t.Fatalf("expected replicas 1, got %#v", existing.Spec.Replicas)
		}
	})
}
