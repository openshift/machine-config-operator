package resourcemerge

import (
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestMergeOwnerRefs(t *testing.T) {
	tests := []struct {
		existing []metav1.OwnerReference
		input    []metav1.OwnerReference

		expectedModified bool
		expected         []metav1.OwnerReference
	}{{
		existing: []metav1.OwnerReference{},
		input:    []metav1.OwnerReference{},

		expectedModified: false,
		expected:         []metav1.OwnerReference{},
	}, {
		existing: []metav1.OwnerReference{},
		input: []metav1.OwnerReference{{
			UID: types.UID("uid-1"),
		}},

		expectedModified: true,
		expected: []metav1.OwnerReference{{
			UID: types.UID("uid-1"),
		}},
	}, {
		existing: []metav1.OwnerReference{{
			UID: types.UID("uid-2"),
		}, {
			UID: types.UID("uid-3"),
		}},
		input: []metav1.OwnerReference{{
			UID: types.UID("uid-1"),
		}},

		expectedModified: true,
		expected: []metav1.OwnerReference{{
			UID: types.UID("uid-2"),
		}, {
			UID: types.UID("uid-3"),
		}, {
			UID: types.UID("uid-1"),
		}},
	}, {
		existing: []metav1.OwnerReference{{
			UID: types.UID("uid-1"),
		}},
		input: []metav1.OwnerReference{{
			UID: types.UID("uid-1"),
		}},

		expectedModified: false,
		expected: []metav1.OwnerReference{{
			UID: types.UID("uid-1"),
		}},
	}, {
		existing: []metav1.OwnerReference{{
			UID: types.UID("uid-1"),
		}},
		input: []metav1.OwnerReference{{
			Controller: BoolPtr(true),
			UID:        types.UID("uid-1"),
		}},

		expectedModified: true,
		expected: []metav1.OwnerReference{{
			Controller: BoolPtr(true),
			UID:        types.UID("uid-1"),
		}},
	}, {
		existing: []metav1.OwnerReference{{
			Controller: BoolPtr(false),
			UID:        types.UID("uid-1"),
		}},
		input: []metav1.OwnerReference{{
			Controller: BoolPtr(true),
			UID:        types.UID("uid-1"),
		}},

		expectedModified: true,
		expected: []metav1.OwnerReference{{
			Controller: BoolPtr(true),
			UID:        types.UID("uid-1"),
		}},
	}}

	for idx, test := range tests {
		t.Run(fmt.Sprintf("test#%d", idx), func(t *testing.T) {
			modified := BoolPtr(false)
			mergeOwnerRefs(modified, &test.existing, test.input)
			if *modified != test.expectedModified {
				t.Fatalf("mismatch modified got: %v want: %v", *modified, test.expectedModified)
			}

			if !equality.Semantic.DeepEqual(test.existing, test.expected) {
				t.Fatalf("mismatch ownerefs got: %v want: %v", test.existing, test.expected)
			}
		})
	}
}
