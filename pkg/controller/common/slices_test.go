package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInSlice(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		input    []string
		element  string
		expected bool
	}{
		{
			name:     "Has element",
			input:    []string{"value1", "value2"},
			element:  "value2",
			expected: true,
		},
		{
			name:     "Does not have element",
			input:    []string{"value1", "value2"},
			element:  "value3",
			expected: false,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Equal(t, testCase.expected, InSlice(testCase.element, testCase.input))
		})
	}
}

func TestDedupeSlice(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "Duplicates removed",
			input:    []string{"value1", "value1", "value1"},
			expected: []string{"value1"},
		},
		{
			name:     "Order preserved while removing duplicates",
			input:    []string{"value1", "value2", "value3", "value2", "value1", "value4", "value3", "value1", "value5"},
			expected: []string{"value1", "value2", "value3", "value4", "value5"},
		},
		{
			name:     "Unsorted output",
			input:    []string{"efg", "zyx", "abc", "efg", "abc", "zyx"},
			expected: []string{"efg", "zyx", "abc"},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			output := DedupeSlice(testCase.input)
			assert.Equal(t, testCase.expected, DedupeSlice(testCase.input))
			// Call again on its output to ensure that nothing changes.
			assert.Equal(t, testCase.expected, DedupeSlice(output))
		})
	}
}

func TestIsSliceElementsEqual(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		a        []string
		b        []string
		expected bool
	}{
		{
			name:     "Equal",
			a:        []string{"value1", "value2"},
			b:        []string{"value1", "value2"},
			expected: true,
		},
		{
			name:     "Equal but different order",
			a:        []string{"value1", "value2"},
			b:        []string{"value2", "value1"},
			expected: true,
		},
		{
			name:     "Equal with duplicates",
			a:        []string{"value1", "value1", "value2"},
			b:        []string{"value1", "value2"},
			expected: true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Equal(t, testCase.expected, IsSliceElementsEqual(testCase.a, testCase.b))
		})
	}
}

func TestIsSliceElementsAndLengthEqual(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		a        []string
		b        []string
		expected bool
	}{
		{
			name:     "Equal",
			a:        []string{"value1", "value2"},
			b:        []string{"value1", "value2"},
			expected: true,
		},
		{
			name:     "Equal but different order",
			a:        []string{"value1", "value2"},
			b:        []string{"value2", "value1"},
			expected: true,
		},
		{
			name:     "Equal with duplicates",
			a:        []string{"value1", "value1", "value2"},
			b:        []string{"value1", "value2", "value2"},
			expected: true,
		},
		{
			name: "Not equal with duplicates due to length",
			a:    []string{"value1", "value1", "value2"},
			b:    []string{"value1", "value2"},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Equal(t, testCase.expected, IsSliceElementsAndLengthEqual(testCase.a, testCase.b))
		})
	}
}

func TestIsSliceElementsUnique(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		input    []string
		expected bool
	}{
		{
			name:     "Unique items",
			input:    []string{"value1", "value2"},
			expected: true,
		},
		{
			name:     "Non-unique items",
			input:    []string{"value1", "value1"},
			expected: false,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Equal(t, testCase.expected, IsSliceElementsUnique(testCase.input))
		})
	}
}
