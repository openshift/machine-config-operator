package buildrequest

import (
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

// Validates that resource requirements are set correctly, based upon whether
// they are user-provided or the system defaults. This is done by ensuring that
// the resource request annotations from the MachineOSConfig are honored. These
// are checked both in isolation as well as on a per-container basis.
//
// In the future, the MachineOSConfig API should be modified to allow the
// corev1.ResourceRequirements struct to be embedded within it. That would make
// this test mostly moot with the exception of allowing default values.
func TestResources(t *testing.T) {
	t.Parallel()

	// Gets a ResourceList, but fails the test if err is not nil. Used as a closure to
	// ensure that it only be used during unit tests.
	mustGetResourceList := func(in map[corev1.ResourceName]string) corev1.ResourceList {
		l, err := getResourceList(in)
		require.NoError(t, err)
		return l
	}

	defaultResourceReq := &corev1.ResourceRequirements{
		Requests: mustGetResourceList(map[corev1.ResourceName]string{
			corev1.ResourceCPU:    defaultCPURequest,
			corev1.ResourceMemory: defaultMemoryRequest,
		}),
		Limits: corev1.ResourceList{},
	}

	req, err := getResourceRequirements(&mcfgv1.MachineOSConfig{})
	require.NoError(t, err)

	assert.Equal(t, req.defaults, defaultResourceReq)

	testCases := []struct {
		name              string
		annotations       map[resourceAnnotationKey]string
		expectedResources *corev1.ResourceRequirements
		errExpected       bool
	}{
		{
			name:              "No resource requests",
			annotations:       map[resourceAnnotationKey]string{},
			expectedResources: defaultResourceReq,
		},
		{
			name: "CPU request only",
			annotations: map[resourceAnnotationKey]string{
				cpuResourceRequestAnnotationKey: "750m",
			},
			expectedResources: &corev1.ResourceRequirements{
				Requests: mustGetResourceList(map[corev1.ResourceName]string{
					corev1.ResourceCPU:    "750m",
					corev1.ResourceMemory: defaultMemoryRequest,
				}),
				Limits: corev1.ResourceList{},
			},
		},
		{
			name: "Memory request only",
			annotations: map[resourceAnnotationKey]string{
				memoryResourceRequestAnnotationKey: "2Gi",
			},
			expectedResources: &corev1.ResourceRequirements{
				Requests: mustGetResourceList(map[corev1.ResourceName]string{
					corev1.ResourceCPU:    defaultCPURequest,
					corev1.ResourceMemory: "2Gi",
				}),
				Limits: corev1.ResourceList{},
			},
		},
		{
			name: "Requests only",
			annotations: map[resourceAnnotationKey]string{
				cpuResourceRequestAnnotationKey:              "750m",
				memoryResourceRequestAnnotationKey:           "2Gi",
				storageResourceRequestAnnotationKey:          "20Gi",
				ephemeralStorageResourceRequestAnnotationKey: "20Gi",
			},
			expectedResources: &corev1.ResourceRequirements{
				Requests: mustGetResourceList(map[corev1.ResourceName]string{
					corev1.ResourceCPU:              "750m",
					corev1.ResourceMemory:           "2Gi",
					corev1.ResourceStorage:          "20Gi",
					corev1.ResourceEphemeralStorage: "20Gi",
				}),
				Limits: corev1.ResourceList{},
			},
		},
		{
			name: "Limits only",
			annotations: map[resourceAnnotationKey]string{
				cpuResourceLimitAnnotationKey:              "750m",
				memoryResourceLimitAnnotationKey:           "2Gi",
				storageResourceLimitAnnotationKey:          "20Gi",
				ephemeralStorageResourceLimitAnnotationKey: "20Gi",
			},
			expectedResources: &corev1.ResourceRequirements{
				Requests: mustGetResourceList(map[corev1.ResourceName]string{
					corev1.ResourceCPU:    defaultCPURequest,
					corev1.ResourceMemory: defaultMemoryRequest,
				}),
				Limits: mustGetResourceList(map[corev1.ResourceName]string{
					corev1.ResourceCPU:              "750m",
					corev1.ResourceMemory:           "2Gi",
					corev1.ResourceStorage:          "20Gi",
					corev1.ResourceEphemeralStorage: "20Gi",
				}),
			},
		},
		{
			name: "Requests and limits",
			annotations: map[resourceAnnotationKey]string{
				cpuResourceRequestAnnotationKey:              "750m",
				memoryResourceRequestAnnotationKey:           "2Gi",
				storageResourceRequestAnnotationKey:          "20Gi",
				ephemeralStorageResourceRequestAnnotationKey: "20Gi",
				cpuResourceLimitAnnotationKey:                "750m",
				memoryResourceLimitAnnotationKey:             "2Gi",
				storageResourceLimitAnnotationKey:            "20Gi",
				ephemeralStorageResourceLimitAnnotationKey:   "20Gi",
			},
			expectedResources: &corev1.ResourceRequirements{
				Requests: mustGetResourceList(map[corev1.ResourceName]string{
					corev1.ResourceCPU:              "750m",
					corev1.ResourceMemory:           "2Gi",
					corev1.ResourceStorage:          "20Gi",
					corev1.ResourceEphemeralStorage: "20Gi",
				}),
				Limits: mustGetResourceList(map[corev1.ResourceName]string{
					corev1.ResourceCPU:              "750m",
					corev1.ResourceMemory:           "2Gi",
					corev1.ResourceStorage:          "20Gi",
					corev1.ResourceEphemeralStorage: "20Gi",
				}),
			},
		},
		{
			name: "Invalid resource quantity",
			annotations: map[resourceAnnotationKey]string{
				cpuResourceRequestAnnotationKey: "invalid-value",
			},
			errExpected: true,
		},
		{
			name: "Empty annotation value",
			annotations: map[resourceAnnotationKey]string{
				cpuResourceRequestAnnotationKey: "",
			},
			// TODO: What happens when a resource field is requested but is empty?
			// This should mimic that behavior.
			errExpected: true,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			t.Run("ResourceRequests", func(t *testing.T) {
				t.Parallel()

				opts := getBuildRequestOpts()

				if testCase.annotations != nil {
					for key, val := range testCase.annotations {
						opts.MachineOSConfig.Annotations[string(key)] = val
					}
				}

				resourceReq, err := getResourceRequirements(opts.MachineOSConfig)
				if testCase.errExpected {
					assert.Error(t, err)
					t.Log(err)
					return
				}

				assert.Equal(t, testCase.expectedResources, resourceReq.builder)
			})

			t.Run("BuildRequest", func(t *testing.T) {
				t.Parallel()

				opts := getBuildRequestOpts()

				if testCase.annotations != nil {
					for key, val := range testCase.annotations {
						opts.MachineOSConfig.Annotations[string(key)] = val
					}
				}

				br := newBuildRequest(opts)

				builder, err := br.Builder()
				if testCase.errExpected {
					assert.Error(t, err)
					t.Log(err)
					return
				}

				assert.NoError(t, err)

				buildJob := builder.GetObject().(*batchv1.Job)
				// TODO(zzlotnik): This field is still alpha and is not respected when the
				// PodLevelResources feature gate is not enabled. Additionally, this
				// field only accepts CPU and memory values, so non-CPU / memory values
				// should be stripped.
				//
				// assert.Equal(t, testCase.expectedResources, buildJob.Spec.Template.Spec.Resources)

				for _, container := range buildJob.Spec.Template.Spec.Containers {
					if container.Name == imageBuildContainerName {
						assert.Equal(t, *testCase.expectedResources, container.Resources)
					}

					if container.Name == waitForDoneContainerName {
						assert.Equal(t, *defaultResourceReq, container.Resources)
					}
				}
			})
		})
	}
}
