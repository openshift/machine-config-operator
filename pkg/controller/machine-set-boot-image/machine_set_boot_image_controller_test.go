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
