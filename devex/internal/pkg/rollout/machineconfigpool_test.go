package rollout

import (
	"context"
	"fmt"
	"testing"

	errhelpers "github.com/openshift/machine-config-operator/devex/internal/pkg/errors"
	"github.com/stretchr/testify/assert"
)

func TestHandleQueryErr(t *testing.T) {
	testCases := []struct {
		name             string
		err              error
		errExpected      bool
		expected         bool
		thresholdReached bool
	}{
		{
			name:        "no error",
			errExpected: false,
			expected:    true,
		},
		{
			name:        "retryable error - threshold not reached",
			errExpected: false,
			err:         fmt.Errorf("retryable"),
			expected:    false,
		},
		{
			name:             "retryable error - threshold reached",
			errExpected:      true,
			err:              fmt.Errorf("retryable"),
			expected:         false,
			thresholdReached: true,
		},
		{
			name:        "non-retryable error",
			errExpected: true,
			err:         context.Canceled,
			expected:    false,
		},
		{
			name:             "nil error clears threshold",
			errExpected:      false,
			err:              nil,
			expected:         true,
			thresholdReached: true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			retryer := errhelpers.NewMaxAttemptRetryer(1)

			if testCase.thresholdReached {
				assert.True(t, retryer.IsEmpty())
				assert.False(t, retryer.IsReached())
				assert.False(t, retryer.IsEmpty())
				assert.True(t, retryer.IsReached())
			}

			shouldContinue, err := handleQueryErr(testCase.err, retryer)
			if testCase.errExpected {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, testCase.expected, shouldContinue)
		})
	}
}
