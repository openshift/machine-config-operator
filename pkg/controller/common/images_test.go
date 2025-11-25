package common

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func TestParseOSImageURLConfigMap(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		cm          *corev1.ConfigMap
		expectError bool
		expected    *OSImageURLConfig
	}{
		{
			name: "valid ConfigMap with streams.json",
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      MachineConfigOSImageURLConfigMapName,
					Namespace: MCONamespace,
				},
				Data: map[string]string{
					"baseOSContainerImage":           "quay.io/openshift/base:latest",
					"baseOSExtensionsContainerImage": "quay.io/openshift/extensions:latest",
					"releaseVersion":                 "4.21.0",
					"streams.json": `{
						"default": "rhel-coreos",
						"streams": {
							"rhel-coreos": {
								"baseOSContainerImage": "quay.io/openshift/rhcos:latest",
								"baseOSExtensionsContainerImage": "quay.io/openshift/rhcos-ext:latest"
							},
							"rhel10-coreos": {
								"baseOSContainerImage": "quay.io/openshift/rhel10:latest",
								"baseOSExtensionsContainerImage": "quay.io/openshift/rhel10-ext:latest"
							}
						}
					}`,
				},
			},
			expectError: false,
			expected: &OSImageURLConfig{
				ReleaseVersion: "4.21.0",
				StreamsConfig: OSImageURLStreamsConfig{
					Default: "rhel-coreos",
					Streams: map[string]OSImageURLStreamConfig{
						"rhel-coreos": {
							BaseOSContainerImage:           "quay.io/openshift/rhcos:latest",
							BaseOSExtensionsContainerImage: "quay.io/openshift/rhcos-ext:latest",
						},
						"rhel10-coreos": {
							BaseOSContainerImage:           "quay.io/openshift/rhel10:latest",
							BaseOSExtensionsContainerImage: "quay.io/openshift/rhel10-ext:latest",
						},
					},
				},
			},
		},
		{
			name: "valid ConfigMap without streams.json (uses default)",
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      MachineConfigOSImageURLConfigMapName,
					Namespace: MCONamespace,
				},
				Data: map[string]string{
					"baseOSContainerImage":           "quay.io/openshift/base:latest",
					"baseOSExtensionsContainerImage": "quay.io/openshift/extensions:latest",
					"releaseVersion":                 "4.21.0",
				},
			},
			expectError: false,
			expected: &OSImageURLConfig{
				ReleaseVersion: "4.21.0",
				StreamsConfig: OSImageURLStreamsConfig{
					Default: "rhel-coreos",
					Streams: map[string]OSImageURLStreamConfig{
						"rhel-coreos": {
							BaseOSContainerImage:           "quay.io/openshift/base:latest",
							BaseOSExtensionsContainerImage: "quay.io/openshift/extensions:latest",
						},
					},
				},
			},
		},
		{
			name: "invalid streams.json format",
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      MachineConfigOSImageURLConfigMapName,
					Namespace: MCONamespace,
				},
				Data: map[string]string{
					"baseOSContainerImage":           "quay.io/openshift/base:latest",
					"baseOSExtensionsContainerImage": "quay.io/openshift/extensions:latest",
					"releaseVersion":                 "4.21.0",
					"streams.json":                   `{"invalid json`,
				},
			},
			expectError: true,
		},
		{
			name: "missing required field baseOSContainerImage",
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      MachineConfigOSImageURLConfigMapName,
					Namespace: MCONamespace,
				},
				Data: map[string]string{
					"baseOSExtensionsContainerImage": "quay.io/openshift/extensions:latest",
					"releaseVersion":                 "4.21.0",
				},
			},
			expectError: true,
		},
		{
			name: "missing required field baseOSExtensionsContainerImage",
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      MachineConfigOSImageURLConfigMapName,
					Namespace: MCONamespace,
				},
				Data: map[string]string{
					"baseOSContainerImage": "quay.io/openshift/base:latest",
					"releaseVersion":       "4.21.0",
				},
			},
			expectError: true,
		},
		{
			name: "missing required field releaseVersion",
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      MachineConfigOSImageURLConfigMapName,
					Namespace: MCONamespace,
				},
				Data: map[string]string{
					"baseOSContainerImage":           "quay.io/openshift/base:latest",
					"baseOSExtensionsContainerImage": "quay.io/openshift/extensions:latest",
				},
			},
			expectError: true,
		},
		{
			name: "wrong ConfigMap name",
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "wrong-configmap-name",
					Namespace: MCONamespace,
				},
				Data: map[string]string{
					"baseOSContainerImage":           "quay.io/openshift/base:latest",
					"baseOSExtensionsContainerImage": "quay.io/openshift/extensions:latest",
					"releaseVersion":                 "4.21.0",
				},
			},
			expectError: true,
		},
		{
			name: "wrong namespace",
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      MachineConfigOSImageURLConfigMapName,
					Namespace: "wrong-namespace",
				},
				Data: map[string]string{
					"baseOSContainerImage":           "quay.io/openshift/base:latest",
					"baseOSExtensionsContainerImage": "quay.io/openshift/extensions:latest",
					"releaseVersion":                 "4.21.0",
				},
			},
			expectError: true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result, err := ParseOSImageURLConfigMap(testCase.cm)
			if testCase.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, testCase.expected, result)
			}
		})
	}
}

func TestGetOSImageURLConfig(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		configMaps  []*corev1.ConfigMap
		expectError bool
		expected    *OSImageURLConfig
	}{
		{
			name: "successfully retrieve and parse ConfigMap",
			configMaps: []*corev1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      MachineConfigOSImageURLConfigMapName,
						Namespace: MCONamespace,
					},
					Data: map[string]string{
						"baseOSContainerImage":           "quay.io/openshift/base:latest",
						"baseOSExtensionsContainerImage": "quay.io/openshift/extensions:latest",
						"releaseVersion":                 "4.21.0",
					},
				},
			},
			expectError: false,
			expected: &OSImageURLConfig{
				ReleaseVersion: "4.21.0",
				StreamsConfig: OSImageURLStreamsConfig{
					Default: "rhel-coreos",
					Streams: map[string]OSImageURLStreamConfig{
						"rhel-coreos": {
							BaseOSContainerImage:           "quay.io/openshift/base:latest",
							BaseOSExtensionsContainerImage: "quay.io/openshift/extensions:latest",
						},
					},
				},
			},
		},
		{
			name: "successfully retrieve and parse ConfigMap with streams.json",
			configMaps: []*corev1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      MachineConfigOSImageURLConfigMapName,
						Namespace: MCONamespace,
					},
					Data: map[string]string{
						"baseOSContainerImage":           "quay.io/openshift/base:latest",
						"baseOSExtensionsContainerImage": "quay.io/openshift/extensions:latest",
						"releaseVersion":                 "4.15.0",
						"streams.json": `{
							"default": "rhel10-coreos",
							"streams": {
								"rhel10-coreos": {
									"baseOSContainerImage": "quay.io/openshift/rhel10:latest",
									"baseOSExtensionsContainerImage": "quay.io/openshift/rhel10-ext:latest"
								}
							}
						}`,
					},
				},
			},
			expectError: false,
			expected: &OSImageURLConfig{
				ReleaseVersion: "4.15.0",
				StreamsConfig: OSImageURLStreamsConfig{
					Default: "rhel10-coreos",
					Streams: map[string]OSImageURLStreamConfig{
						"rhel10-coreos": {
							BaseOSContainerImage:           "quay.io/openshift/rhel10:latest",
							BaseOSExtensionsContainerImage: "quay.io/openshift/rhel10-ext:latest",
						},
					},
				},
			},
		},
		{
			name:        "ConfigMap not found",
			configMaps:  []*corev1.ConfigMap{},
			expectError: true,
		},
		{
			name: "ConfigMap in wrong namespace",
			configMaps: []*corev1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      MachineConfigOSImageURLConfigMapName,
						Namespace: "default",
					},
					Data: map[string]string{
						"baseOSContainerImage":           "quay.io/openshift/base:latest",
						"baseOSExtensionsContainerImage": "quay.io/openshift/extensions:latest",
						"releaseVersion":                 "4.15.0",
					},
				},
			},
			expectError: true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			// Convert ConfigMaps to runtime.Object slice for fake client
			objects := make([]runtime.Object, len(testCase.configMaps))
			for i, cm := range testCase.configMaps {
				objects[i] = cm
			}

			// Create fake client with ConfigMaps
			client := fake.NewClientset(objects...)

			result, err := GetOSImageURLConfig(context.Background(), client)

			if testCase.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, testCase.expected, result)
			}
		})
	}
}
