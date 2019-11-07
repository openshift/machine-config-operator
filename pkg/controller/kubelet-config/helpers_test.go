package kubeletconfig

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
)

func TestWrapErrorWithCondition(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		error    error
		args     []interface{}
		expected mcfgv1.KubeletConfigCondition
	}{
		{
			name:  "no error, no args",
			expected: mcfgv1.KubeletConfigCondition{
				Type:    mcfgv1.KubeletConfigSuccess,
				Status:  corev1.ConditionTrue,
				Message: "Success",
			},
		},
		{
			name:  "error, no args",
			error: errors.New("some-error"),
			expected: mcfgv1.KubeletConfigCondition{
				Type:    mcfgv1.KubeletConfigFailure,
				Status:  corev1.ConditionFalse,
				Message: "Error: some-error",
			},
		},
		{
			name:  "no error, non-string args",
			args:  []interface{}{1, 2},
			expected: mcfgv1.KubeletConfigCondition{
				Type:    mcfgv1.KubeletConfigSuccess,
				Status:  corev1.ConditionTrue,
				Message: "Success",
			},
		},
		{
			name:  "error, non-string args",
			error: errors.New("some-error"),
			args:  []interface{}{1, 2},
			expected: mcfgv1.KubeletConfigCondition{
				Type:    mcfgv1.KubeletConfigFailure,
				Status:  corev1.ConditionFalse,
				Message: "Error: some-error",
			},
		},
		{
			name:  "no error, string formatting",
			args:  []interface{}{"look at this: %v", "stuff"},
			expected: mcfgv1.KubeletConfigCondition{
				Type:    mcfgv1.KubeletConfigSuccess,
				Status:  corev1.ConditionTrue,
				Message: "look at this: look at this: %v",
			},
		},
		{
			name:  "error, string formatting",
			error: errors.New("some-error"),
			args:  []interface{}{"look at this: %v", "stuff"},
			expected: mcfgv1.KubeletConfigCondition{
				Type:    mcfgv1.KubeletConfigFailure,
				Status:  corev1.ConditionFalse,
				Message: "look at this: look at this: %v",
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			condition := wrapErrorWithCondition(testCase.error, testCase.args...)
			testCase.expected.LastTransitionTime = condition.LastTransitionTime
			assert.Equal(t, testCase.expected, condition)
		})
	}
}
