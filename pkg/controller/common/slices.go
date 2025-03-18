package common

import "k8s.io/apimachinery/pkg/util/sets"

// InSlice search for an element in slice and return true if found, otherwise return false
func InSlice(elem string, slice []string) bool {
	for _, k := range slice {
		if k == elem {
			return true
		}
	}
	return false
}

// DedupeSlice will ensure that all elements within a slice are unique while
// preserving the slice's original order and the first appearance of any unique
// elements.
func DedupeSlice(slice []string) []string {
	tracker := map[string]struct{}{}

	out := []string{}

	for _, item := range slice {
		if _, ok := tracker[item]; ok {
			continue
		}

		tracker[item] = struct{}{}
		out = append(out, item)
	}

	return out
}

// Determines if two slices have the same unique elements without considering
// order or duplicates.
func IsSliceElementsEqual(a, b []string) bool {
	return sets.New(a...).Equal(sets.New(b...))
}

// Determines if two slices have the same unique elements without considering
// order. Two slices of different lengths will return false even if they have
// the same unique elements.
func IsSliceElementsAndLengthEqual(a, b []string) bool {
	return len(a) == len(b) && IsSliceElementsEqual(a, b)
}

// Determines if a given slice has duplicate entries.
func IsSliceElementsUnique(slice []string) bool {
	tracker := map[string]struct{}{}

	for _, item := range slice {
		if _, ok := tracker[item]; ok {
			return false
		}

		tracker[item] = struct{}{}
	}

	return true
}
