package helpers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Tests helpers.MakeIdempotent() to ensure that any function that is wrapped
// by it is only called once.
func TestMakeIdempotent(t *testing.T) {
	t.Parallel()

	count := 0

	increment := func() { count++ }

	testCases := []struct {
		name          string
		incrementer   func()
		expectedCount int
	}{
		{
			name:          "Not idempotent",
			incrementer:   increment,
			expectedCount: 10,
		},
		{
			name:          "Is idempotent - Single-wrapped",
			incrementer:   MakeIdempotent(increment),
			expectedCount: 1,
		},
		{
			name:          "Is idempotent - Double-wrapped",
			incrementer:   MakeIdempotent(MakeIdempotent(increment)),
			expectedCount: 1,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			count = 0

			for i := 0; i < 10; i++ {
				testCase.incrementer()
			}

			assert.Equal(t, testCase.expectedCount, count)
		})
	}
}
