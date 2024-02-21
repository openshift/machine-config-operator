package common

import (
	"testing"

	"github.com/davecgh/go-spew/spew"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
)

func TestMachineOSBuildState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		pool           *mcfgv1.MachineConfigPool
		isLayered      bool
		hasOSImage     bool
		isBuildSuccess bool
		isBuildPending bool
		isBuilding     bool
		isBuildFailure bool
		buildCondition mcfgv1.MachineConfigPoolConditionType
	}{
		{
			name: "unlayered pool",
			pool: newMachineConfigPool(""),
		},
		{
			name:      "layered pool, no OS image",
			pool:      newLayeredMachineConfigPool(""),
			isLayered: true,
		},
		{
			name:       "layered pool, with OS image",
			pool:       newLayeredMachineConfigPoolWithImage("", imageV1),
			isLayered:  true,
			hasOSImage: true,
		},
		{
			name:           "layered pool, with OS image, building",
			pool:           newLayeredMachineConfigPoolWithImage("", imageV1),
			isLayered:      true,
			hasOSImage:     true,
			buildCondition: mcfgv1.MachineConfigPoolBuilding,
			isBuilding:     true,
		},
		{
			name:           "layered pool, with OS image, build pending",
			pool:           newLayeredMachineConfigPoolWithImage("", imageV1),
			isLayered:      true,
			hasOSImage:     true,
			buildCondition: mcfgv1.MachineConfigPoolBuildPending,
			isBuildPending: true,
		},
		{
			name:           "layered pool, with OS image, build success",
			pool:           newLayeredMachineConfigPoolWithImage("", imageV1),
			isLayered:      true,
			hasOSImage:     true,
			buildCondition: mcfgv1.MachineConfigPoolBuildSuccess,
			isBuildSuccess: true,
		},
		{
			name:           "layered pool, with OS image, build failed",
			pool:           newLayeredMachineConfigPoolWithImage("", imageV1),
			isLayered:      true,
			hasOSImage:     true,
			buildCondition: mcfgv1.MachineConfigPoolBuildFailed,
			isBuildFailure: true,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if test.buildCondition != "" {
				cond := apihelpers.NewMachineConfigPoolCondition(test.buildCondition, corev1.ConditionTrue, "", "")
				apihelpers.SetMachineConfigPoolCondition(&test.pool.Status, *cond)
			}

			lps := NewMachineOSBuildState(test.pool)

			assert.Equal(t, test.isLayered, lps.IsLayered(), "is layered mismatch %s", spew.Sdump(test.pool.Labels))
			assert.Equal(t, test.hasOSImage, lps.HasOSImage(), "has OS image mismatch %s", spew.Sdump(test.pool.Annotations))
			assert.Equal(t, test.isBuildSuccess, lps.IsBuildSuccess(), "is build success mismatch %s", spew.Sdump(test.pool.Status))
			assert.Equal(t, test.isBuildPending, lps.IsBuildPending(), "is build pending mismatch %s", spew.Sdump(test.pool.Status))
			assert.Equal(t, test.isBuilding, lps.IsBuilding(), "is building mismatch %s", spew.Sdump(test.pool.Status))
			assert.Equal(t, test.isBuildFailure, lps.IsBuildFailure(), "is build failure mismatch %s", spew.Sdump(test.pool.Status))
		})
	}
}
