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

func TestMakeIdempotentSkippable(t *testing.T) {
	t.Parallel()

	count := 0

	increment := func() { count++ }

	skipFunc := func(result bool) func() bool {
		return func() bool {
			return result
		}
	}

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
			name:          "Is idempotent; not skipped - Single-wrapped",
			incrementer:   MakeIdempotentSkippable(skipFunc(false), increment),
			expectedCount: 1,
		},
		{
			name:          "Is idempotent; not skipped - Double-wrapped",
			incrementer:   MakeIdempotentSkippable(skipFunc(false), MakeIdempotentSkippable(skipFunc(false), increment)),
			expectedCount: 1,
		},
		{
			name:          "Is idempotent; skipped - Single-wrapped",
			incrementer:   MakeIdempotentSkippable(skipFunc(true), increment),
			expectedCount: 0,
		},
		{
			name:          "Is idempotent; skipped - Double-wrapped",
			incrementer:   MakeIdempotentSkippable(skipFunc(true), MakeIdempotentSkippable(skipFunc(true), increment)),
			expectedCount: 0,
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
