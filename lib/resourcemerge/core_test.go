package resourcemerge

import (
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
)

// Test added for now to test ensureContainer (mostly so we don't break Env Vars - proxy)
func TestEnsureContainer(t *testing.T) {
	tests := []struct {
		existing corev1.Container
		input    corev1.Container

		expectedModified bool
		expected         corev1.Container
	}{
		// Change in proxy
		{
			existing: corev1.Container{
				Env: []corev1.EnvVar{
					{
						Name:  "Proxy",
						Value: "Proxy1",
					},
				},
			},
			input: corev1.Container{
				Env: []corev1.EnvVar{
					{
						Name:  "Proxy",
						Value: "Proxy2",
					},
				},
			},
			expectedModified: true,
			expected: corev1.Container{
				Env: []corev1.EnvVar{
					{
						Name:  "Proxy",
						Value: "Proxy2",
					},
				},
			},
		},
		// Add Proxy
		{
			existing: corev1.Container{
				Env: []corev1.EnvVar{},
			},
			input: corev1.Container{
				Env: []corev1.EnvVar{
					{
						Name:  "Proxy",
						Value: "Proxy1",
					},
				},
			},
			expectedModified: true,
			expected: corev1.Container{
				Env: []corev1.EnvVar{
					{
						Name:  "Proxy",
						Value: "Proxy1",
					},
				},
			},
		},
		// Remove Proxy
		{
			existing: corev1.Container{
				Env: []corev1.EnvVar{
					{
						Name:  "Proxy",
						Value: "Proxy1",
					},
				},
			},
			input: corev1.Container{
				Env: []corev1.EnvVar{},
			},
			expectedModified: true,
			expected: corev1.Container{
				Env: []corev1.EnvVar{},
			},
		},
		// No change
		{
			existing: corev1.Container{
				Env: []corev1.EnvVar{
					{
						Name:  "Proxy",
						Value: "Proxy1",
					},
				},
			},
			input: corev1.Container{
				Env: []corev1.EnvVar{
					{
						Name:  "Proxy",
						Value: "Proxy1",
					},
				},
			},
			expectedModified: false,
			expected: corev1.Container{
				Env: []corev1.EnvVar{
					{
						Name:  "Proxy",
						Value: "Proxy1",
					},
				},
			},
		},
	}

	for idx, test := range tests {
		t.Run(fmt.Sprintf("test#%d", idx), func(t *testing.T) {
			modified := false
			ensureContainer(&modified, &test.existing, test.input)

			if modified != test.expectedModified {
				t.Fatalf("mismatch container got: %v want: %v", modified, test.expectedModified)
			}

			if !equality.Semantic.DeepEqual(test.existing, test.expected) {
				t.Fatalf("mismatch container got: %v want: %v", test.existing, test.expected)
			}
		})
	}
}
