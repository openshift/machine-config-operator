package extended

import (
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIsCompletedMCNCondition(t *testing.T) {
	tests := []struct {
		name      string
		condition metav1.Condition
		want      bool
	}{
		{
			name: "Resumed true is a completed phase",
			condition: metav1.Condition{
				Type:   string(mcfgv1.MachineConfigNodeResumed),
				Status: metav1.ConditionTrue,
			},
			want: true,
		},
		{
			name: "Resumed false is not an active condition",
			condition: metav1.Condition{
				Type:   string(mcfgv1.MachineConfigNodeResumed),
				Status: metav1.ConditionFalse,
			},
			want: false,
		},
		{
			name: "update progress condition is not completed",
			condition: metav1.Condition{
				Type:   string(mcfgv1.MachineConfigNodeUpdatePrepared),
				Status: metav1.ConditionTrue,
			},
			want: false,
		},
		{
			name: "observed cordon action is permitted",
			condition: metav1.Condition{
				Type:   string(mcfgv1.MachineConfigNodeUpdateCordoned),
				Status: metav1.ConditionTrue,
				Reason: "NodeUnschedulable",
			},
			want: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isCompletedMCNCondition(test.condition); got != test.want {
				t.Errorf("isCompletedMCNCondition() = %t, want %t", got, test.want)
			}
		})
	}
}
