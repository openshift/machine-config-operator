package common

import (
	"testing"

	"github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/stretchr/testify/assert"
)

func TestMachineOSConfigState(t *testing.T) {
	t.Parallel()

	mosc := NewMachineOSConfigState(&v1alpha1.MachineOSConfig{
		Status: v1alpha1.MachineOSConfigStatus{
			CurrentImagePullspec: "registry.host.com/org/repo:tag",
		},
	})

	assert.Equal(t, "registry.host.com/org/repo:tag", mosc.GetOSImage())
}
