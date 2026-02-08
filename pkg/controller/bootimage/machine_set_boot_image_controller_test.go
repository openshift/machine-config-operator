package machineset

import (
	"testing"

	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestHotLoop(t *testing.T) {
	cases := []struct {
		name                  string
		machineset            *machinev1beta1.MachineSet
		generateBootImageFunc func(string) string
		updateCount           int
		expectHotLoop         bool
	}{
		{
			name:                  "Hot loop detected due to multiple updates to same value",
			machineset:            getMachineSet("machine-set-1", "boot-image-1"),
			updateCount:           HotLoopLimit + 1,
			generateBootImageFunc: func(s string) string { return s },
			expectHotLoop:         true,
		},
		{
			name:                  "Hot loop not detected due to update count being lower than threshold",
			machineset:            getMachineSet("machine-set-1", "boot-image-1"),
			generateBootImageFunc: func(s string) string { return s },
			updateCount:           HotLoopLimit - 1,
			expectHotLoop:         false,
		},
		{
			name:                  "Hot loop not detected due to target boot image changing",
			machineset:            getMachineSet("machine-set-1", "boot-image-1"),
			generateBootImageFunc: func(s string) string { return s + "-1" },
			updateCount:           HotLoopLimit + 1,
			expectHotLoop:         false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctrl := &Controller{
				mapiBootImageState: map[string]BootImageState{},
			}
			// checkMAPIMachineSetHotLoop is only called in the controller when a patch is required,
			// so every call to it is assuming a boot image update is going to take place.

			// No hot loops should be detected in the first (updateCount - 1) calls
			var hotLoopDetected bool
			for range tc.updateCount - 1 {
				hotLoopDetected = ctrl.checkMAPIMachineSetHotLoop(tc.machineset)
				assert.Equal(t, false, hotLoopDetected)
				// Change target boot image for next iteration
				setMachineSetBootImage(tc.machineset, tc.generateBootImageFunc)
			}
			// Check for hot loop on the last iteration
			hotLoopDetected = ctrl.checkMAPIMachineSetHotLoop(tc.machineset)
			assert.Equal(t, tc.expectHotLoop, hotLoopDetected)
		})
	}
}

// Returns a machineset with a given boot image
func getMachineSet(name, bootImage string) *machinev1beta1.MachineSet {
	return &machinev1beta1.MachineSet{
		ObjectMeta: v1.ObjectMeta{
			Name: name,
		},
		Spec: machinev1beta1.MachineSetSpec{
			Template: machinev1beta1.MachineTemplateSpec{
				Spec: machinev1beta1.MachineSpec{
					ProviderSpec: machinev1beta1.ProviderSpec{
						Value: &runtime.RawExtension{
							Raw: []byte(bootImage),
						},
					},
				},
			},
		},
	}
}

// Sets the boot image in a machineset based on the generate function passed in
func setMachineSetBootImage(machineset *machinev1beta1.MachineSet, generateBootImageFunc func(string) string) {
	currentBootImage := string(machineset.Spec.Template.Spec.ProviderSpec.Value.Raw)
	machineset.Spec.Template.Spec.ProviderSpec.Value.Raw = []byte(generateBootImageFunc(currentBootImage))
}

func TestGetArchFromMachineSet(t *testing.T) {
	cases := []struct {
		name        string
		annotations map[string]string
		expectedArch string
		expectError  bool
	}{
		{
			name: "Single architecture label",
			annotations: map[string]string{
				MachineSetArchAnnotationKey: "kubernetes.io/arch=amd64",
			},
			expectedArch: "x86_64",
			expectError:  false,
		},
		{
			name: "Multiple labels with architecture first",
			annotations: map[string]string{
				MachineSetArchAnnotationKey: "kubernetes.io/arch=amd64,topology.ebs.csi.aws.com/zone=eu-central-1a",
			},
			expectedArch: "x86_64",
			expectError:  false,
		},
		{
			name: "Multiple labels with architecture last",
			annotations: map[string]string{
				MachineSetArchAnnotationKey: "topology.ebs.csi.aws.com/zone=eu-central-1a,kubernetes.io/arch=arm64",
			},
			expectedArch: "aarch64",
			expectError:  false,
		},
		{
			name: "Multiple labels with architecture in middle",
			annotations: map[string]string{
				MachineSetArchAnnotationKey: "topology.ebs.csi.aws.com/zone=eu-central-1a,kubernetes.io/arch=s390x,node.kubernetes.io/instance-type=m5.large",
			},
			expectedArch: "s390x",
			expectError:  false,
		},
		{
			name: "Multiple labels with spaces",
			annotations: map[string]string{
				MachineSetArchAnnotationKey: " topology.ebs.csi.aws.com/zone=eu-central-1a , kubernetes.io/arch=ppc64le , node.kubernetes.io/instance-type=m5.large ",
			},
			expectedArch: "ppc64le",
			expectError:  false,
		},
		{
			name: "Invalid architecture",
			annotations: map[string]string{
				MachineSetArchAnnotationKey: "kubernetes.io/arch=invalid-arch",
			},
			expectError: true,
		},
		{
			name: "No architecture label",
			annotations: map[string]string{
				MachineSetArchAnnotationKey: "topology.ebs.csi.aws.com/zone=eu-central-1a,node.kubernetes.io/instance-type=m5.large",
			},
			expectError: true,
		},
		{
			name:         "No annotation",
			annotations:  map[string]string{},
			expectedArch: "", // Will default to control plane arch, but we can't test that easily
			expectError:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			machineSet := &machinev1beta1.MachineSet{
				ObjectMeta: v1.ObjectMeta{
					Name:        "test-machineset",
					Annotations: tc.annotations,
				},
			}

			arch, err := getArchFromMachineSet(machineSet)

			if tc.expectError {
				assert.Error(t, err, "Expected error for test case: %s", tc.name)
			} else {
				assert.NoError(t, err, "Unexpected error for test case: %s", tc.name)
				if tc.expectedArch != "" {
					assert.Equal(t, tc.expectedArch, arch, "Architecture mismatch for test case: %s", tc.name)
				}
			}
		})
	}
}
