package common

import (
	"testing"

	v1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/stretchr/testify/assert"
)

func TestMachineOSConfigState(t *testing.T) {
	t.Parallel()

	mosc := NewMachineOSConfigState(&v1.MachineOSConfig{
		Status: v1.MachineOSConfigStatus{
			CurrentImagePullSpec: "registry.host.com/org/repo:tag",
		},
	})

	assert.Equal(t, "registry.host.com/org/repo:tag", mosc.GetOSImage())
}
