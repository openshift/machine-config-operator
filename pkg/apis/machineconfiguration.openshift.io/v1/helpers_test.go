package v1

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	condUpdatedTrue = func() MachineConfigPoolCondition {
		return MachineConfigPoolCondition{
			Type:   MachineConfigPoolUpdated,
			Status: corev1.ConditionTrue,
			Reason: "AwesomeController",
		}
	}

	condUpdatedFalse = func() MachineConfigPoolCondition {
		return MachineConfigPoolCondition{
			Type:   MachineConfigPoolUpdated,
			Status: corev1.ConditionFalse,
			Reason: "ForSomeReason",
		}
	}

	status = func() *MachineConfigPoolStatus {
		return &MachineConfigPoolStatus{
			Conditions: []MachineConfigPoolCondition{condUpdatedFalse()},
		}
	}
)

func TestGetMachineConfigPoolCondition(t *testing.T) {
	s := status()
	tests := []struct {
		status   MachineConfigPoolStatus
		condType MachineConfigPoolConditionType

		expected bool
	}{{
		status:   *s,
		condType: MachineConfigPoolUpdated,

		expected: true,
	}, {
		status:   *s,
		condType: MachineConfigPoolUpdating,

		expected: false,
	}}

	for idx, test := range tests {
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			cond := GetMachineConfigPoolCondition(test.status, test.condType)
			exists := cond != nil
			if exists != test.expected {
				t.Fatalf("expected condition to exist: %t, got: %t", test.expected, exists)
			}
		})
	}
}

func TestSetMachineConfigPoolCondition(t *testing.T) {
	tests := []struct {
		status *MachineConfigPoolStatus
		cond   MachineConfigPoolCondition

		expectedStatus *MachineConfigPoolStatus
	}{{
		status: &MachineConfigPoolStatus{},
		cond:   condUpdatedTrue(),

		expectedStatus: &MachineConfigPoolStatus{Conditions: []MachineConfigPoolCondition{condUpdatedTrue()}},
	}, {
		status: &MachineConfigPoolStatus{Conditions: []MachineConfigPoolCondition{condUpdatedFalse()}},
		cond:   condUpdatedTrue(),

		expectedStatus: &MachineConfigPoolStatus{Conditions: []MachineConfigPoolCondition{condUpdatedTrue()}},
	}}

	for idx, test := range tests {
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			SetMachineConfigPoolCondition(test.status, test.cond)
			if !reflect.DeepEqual(test.status, test.expectedStatus) {
				t.Errorf("expected status: %v, got: %v", test.expectedStatus, test.status)
			}
		})
	}
}

func TestRemoveMachineConfigPoolCondition(t *testing.T) {
	tests := []struct {
		status   *MachineConfigPoolStatus
		condType MachineConfigPoolConditionType

		expectedStatus *MachineConfigPoolStatus
	}{
		{
			status:   &MachineConfigPoolStatus{Conditions: []MachineConfigPoolCondition{condUpdatedTrue()}},
			condType: MachineConfigPoolUpdated,

			expectedStatus: &MachineConfigPoolStatus{},
		},
		{
			status:   status(),
			condType: MachineConfigPoolUpdating,

			expectedStatus: status(),
		},
	}

	for idx, test := range tests {
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			RemoveMachineConfigPoolCondition(test.status, test.condType)
			if !reflect.DeepEqual(test.status, test.expectedStatus) {
				t.Errorf("expected status: %v, got: %v", test.expectedStatus, test.status)
			}
		})
	}
}
func TestIsControllerConfigCompleted(t *testing.T) {
	tests := []struct {
		obsrvdGen int64
		completed bool
		running   bool
		failing   bool

		err error
	}{{
		obsrvdGen: 0,
		err:       errors.New("status for ControllerConfig dummy is being reported for 0, expecting it for 1"),
	}, {
		obsrvdGen: 1,
		running:   true,
		err:       errors.New("ControllerConfig has not completed: completed(false) running(true) failing(false)"),
	}, {
		obsrvdGen: 1,
		completed: true,
	}, {
		obsrvdGen: 1,
		completed: true,
		running:   true,
		err:       errors.New("ControllerConfig has not completed: completed(true) running(true) failing(false)"),
	}, {
		obsrvdGen: 1,
		failing:   true,
		err:       errors.New("ControllerConfig has not completed: completed(false) running(false) failing(true)"),
	}}
	for idx, test := range tests {
		t.Run(fmt.Sprintf("#%d", idx), func(t *testing.T) {
			getter := func(name string) (*ControllerConfig, error) {
				var conds []ControllerConfigStatusCondition
				if test.completed {
					conds = append(conds, *NewControllerConfigStatusCondition(TemplateContollerCompleted, corev1.ConditionTrue, "", ""))
				}
				if test.running {
					conds = append(conds, *NewControllerConfigStatusCondition(TemplateContollerRunning, corev1.ConditionTrue, "", ""))
				}
				if test.failing {
					conds = append(conds, *NewControllerConfigStatusCondition(TemplateContollerFailing, corev1.ConditionTrue, "", ""))
				}
				return &ControllerConfig{
					ObjectMeta: metav1.ObjectMeta{Generation: 1, Name: name},
					Status: ControllerConfigStatus{
						ObservedGeneration: test.obsrvdGen,
						Conditions:         conds,
					},
				}, nil
			}

			err := IsControllerConfigCompleted("dummy", getter)
			if !reflect.DeepEqual(err, test.err) {
				t.Fatalf("expected %v got %v", test.err, err)
			}
		})
	}
}
