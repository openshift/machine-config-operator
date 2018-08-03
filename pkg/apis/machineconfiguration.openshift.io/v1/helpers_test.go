package v1

import (
	"fmt"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
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

	condDegraded = func() MachineConfigPoolCondition {
		return MachineConfigPoolCondition{
			Type:   MachineConfigPoolDegraded,
			Status: corev1.ConditionTrue,
			Reason: "ForSomeReason",
		}
	}

	status = func() *MachineConfigPoolStatus {
		return &MachineConfigPoolStatus{
			Conditions: []MachineConfigPoolCondition{condUpdatedFalse(), condDegraded()},
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
		cond:   condDegraded(),

		expectedStatus: status(),
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
			status:   &MachineConfigPoolStatus{},
			condType: MachineConfigPoolDegraded,

			expectedStatus: &MachineConfigPoolStatus{},
		},
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
