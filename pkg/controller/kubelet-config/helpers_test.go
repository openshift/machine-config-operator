package kubeletconfig

import (
	"fmt"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	corev1 "k8s.io/api/core/v1"
)

func TestWrapErrorWithCondition(t *testing.T) {
	tests := []struct {
		name            string
		err             error
		args            []interface{}
		expectedType    mcfgv1.KubeletConfigStatusConditionType
		expectedStatus  corev1.ConditionStatus
		expectedMessage string
	}{
		{
			name:            "error without args produces Failure condition with status True",
			err:             fmt.Errorf("KubeletConfiguration: swapBehavior is not allowed to be set, but contains: LimitedSwap"),
			args:            nil,
			expectedType:    mcfgv1.KubeletConfigFailure,
			expectedStatus:  corev1.ConditionTrue,
			expectedMessage: "Error: KubeletConfiguration: swapBehavior is not allowed to be set, but contains: LimitedSwap",
		},
		{
			name:            "error with formatted args produces Failure condition with status True",
			err:             fmt.Errorf("validation failed"),
			args:            []interface{}{"Failed to validate %s: %v", "kubelet config", "invalid field"},
			expectedType:    mcfgv1.KubeletConfigFailure,
			expectedStatus:  corev1.ConditionTrue,
			expectedMessage: "Failed to validate kubelet config: invalid field",
		},
		{
			name:            "nil error produces Success condition with status True",
			err:             nil,
			args:            nil,
			expectedType:    mcfgv1.KubeletConfigSuccess,
			expectedStatus:  corev1.ConditionTrue,
			expectedMessage: "Success",
		},
		{
			name:            "nil error with args still produces Success condition",
			err:             nil,
			args:            []interface{}{"Custom success message"},
			expectedType:    mcfgv1.KubeletConfigSuccess,
			expectedStatus:  corev1.ConditionTrue,
			expectedMessage: "Custom success message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			condition := wrapErrorWithCondition(tt.err, tt.args...)

			if condition.Type != tt.expectedType {
				t.Errorf("expected condition type %v, got %v", tt.expectedType, condition.Type)
			}

			if condition.Status != tt.expectedStatus {
				t.Errorf("expected condition status %v, got %v", tt.expectedStatus, condition.Status)
			}

			if condition.Message != tt.expectedMessage {
				t.Errorf("expected message %q, got %q", tt.expectedMessage, condition.Message)
			}
		})
	}
}
