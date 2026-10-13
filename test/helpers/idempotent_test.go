package helpers

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMakeIdempotent(t *testing.T) {
	newShouldRunFunc := func(shouldRun *bool, toRun func()) func() bool {
		return func() bool {
			toRun()
			return *shouldRun
		}
	}

	newRegisteredMakeIdempotent := func(shouldRun *bool, toRun func()) func() {
		m := &mockTesting{}

		f := func() {}

		if shouldRun == nil {
			f = MakeIdempotentAndRegister(m, toRun)
		} else {
			f = MakeIdempotentSkippableAndRegister(m, newShouldRunFunc(shouldRun, toRun), toRun)
		}

		return func() {
			f()
			m.cleanupFunc()
		}
	}

	newUnregisteredMakeIdempotent := func(shouldRun *bool, toRun func()) func() {
		if shouldRun == nil {
			return MakeIdempotent(toRun)
		}

		return MakeIdempotentSkippable(newShouldRunFunc(shouldRun, toRun), toRun)
	}

	newMakeIdempotent := func(registered bool, shouldRun *bool, toRun func()) func() {
		if registered {
			return newRegisteredMakeIdempotent(shouldRun, toRun)
		}

		return newUnregisteredMakeIdempotent(shouldRun, toRun)
	}

	boolToPtr := func(val bool) *bool {
		return &val
	}

	testCases := []struct {
		name                     string
		registered               bool
		shouldRun                *bool
		expectedIncrementerValue uint64
	}{
		{
			name:                     "Simple idempotent",
			expectedIncrementerValue: 1,
			registered:               false,
		},
		{
			name:                     "Skippable idempotent should run",
			shouldRun:                boolToPtr(true),
			expectedIncrementerValue: 2,
		},
		{
			name:                     "Skippable idempotent should not run",
			shouldRun:                boolToPtr(false),
			expectedIncrementerValue: 1,
		},
		{
			name:                     "Registered idempotent",
			registered:               true,
			expectedIncrementerValue: 1,
		},
		{
			name:                     "Skippable registered idempotent should run",
			shouldRun:                boolToPtr(true),
			registered:               true,
			expectedIncrementerValue: 2,
		},
		{
			name:                     "Skippable registered idempotent should not run",
			shouldRun:                boolToPtr(false),
			registered:               true,
			expectedIncrementerValue: 1,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			t.Run("Single Level", func(t *testing.T) {
				t.Parallel()

				var counter atomic.Uint64
				counter.Store(0)

				f := newMakeIdempotent(testCase.registered, testCase.shouldRun, func() {
					counter.Add(1)
				})

				for i := 0; i <= 100; i++ {
					f()
				}

				assert.Equal(t, testCase.expectedIncrementerValue, counter.Load())
			})

			t.Run("Nested", func(t *testing.T) {
				t.Parallel()

				var counter atomic.Uint64
				counter.Store(0)

				nested := newMakeIdempotent(testCase.registered, testCase.shouldRun, func() {
					counter.Add(1)
				})

				for i := 0; i <= 100; i++ {
					nested = newMakeIdempotent(testCase.registered, testCase.shouldRun, nested)
				}

				for i := 0; i <= 100; i++ {
					nested()
				}

				assert.Equal(t, testCase.expectedIncrementerValue, counter.Load())
			})
		})
	}
}

func TestIdempotentConfig(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		config   IdempotentConfig
		expected bool
	}{
		{
			name: "local failure, no flags set",
			config: IdempotentConfig{
				testFailed: true,
			},
			expected: true,
		},
		{
			name: "local pass, no flags set",
			config: IdempotentConfig{
				testFailed: false,
			},
			expected: true,
		},
		{
			name: "local pass, skip always set",
			config: IdempotentConfig{
				testFailed: false,
				SkipAlways: true,
			},
			expected: false,
		},
		{
			name: "local pass, skip on failure set",
			config: IdempotentConfig{
				testFailed:        false,
				SkipOnlyOnFailure: true,
			},
			expected: true,
		},
		{
			name: "CI failure, no flags set",
			config: IdempotentConfig{
				inCI:       true,
				testFailed: true,
			},
			expected: false,
		},
		{
			name: "CI pass, no flags set",
			config: IdempotentConfig{
				inCI:       true,
				testFailed: false,
			},
			expected: true,
		},
		{
			name: "CI pass, skip always set",
			config: IdempotentConfig{
				inCI:       true,
				testFailed: false,
				SkipAlways: true,
			},
			expected: false,
		},
		{
			name: "CI pass, skip on failure set",
			config: IdempotentConfig{
				inCI:              true,
				testFailed:        false,
				SkipOnlyOnFailure: true,
			},
			expected: true,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, testCase.expected, testCase.config.shouldRun())
		})
	}
}
