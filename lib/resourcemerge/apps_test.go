package resourcemerge

import (
	"fmt"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/util/intstr"
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
			modified := false
			EnsureDaemonSet(&modified, &test.existing, test.input)

			if modified != test.expectedModified {
				t.Fatalf("mismatch updatestrategy got: %v want: %v", modified, test.expectedModified)
			}

			if !equality.Semantic.DeepEqual(test.existing, test.expected) {
				t.Fatalf("mismatch updatestrategy got: %v want: %v", test.existing, test.expected)
			}
		})
	}
}
