package v1

import (
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

func TestMergeMachineConfigs(t *testing.T) {
	var configs []*MachineConfig
	configs = append(configs, &MachineConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "4"},
		Spec: MachineConfigSpec{
			OSImageURL: "",
		},
	})

	targetOSImageURL := "pivot://example.com/os@sha256:thetarget"
	configs = append(configs, &MachineConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "3"},
		Spec: MachineConfigSpec{
			OSImageURL: targetOSImageURL,
		},
	})

	configs = append(configs, &MachineConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "0"},
		Spec: MachineConfigSpec{
			OSImageURL: "",
		},
	})

	configs = append(configs, &MachineConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "1"},
		Spec: MachineConfigSpec{
			OSImageURL:"pivot://example.com/os@sha256:notthetarget",
		},
	})

	configs = append(configs, &MachineConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "2"},
		Spec: MachineConfigSpec{
			OSImageURL: "",
		},
	})

	merged := MergeMachineConfigs(configs)
	if merged.Spec.OSImageURL != targetOSImageURL {
		t.Errorf("OSImageURL expected: %s, received: %s", targetOSImageURL, merged.Spec.OSImageURL)
	}
}
