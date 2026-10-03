package internalreleaseimage

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRunInternalReleaseImageBootstrap(t *testing.T) {
	configs, err := RunInternalReleaseImageBootstrap(iriCertSecret().obj, iriAuthSecret().obj, cconfig().withDNS("example.com").obj)
	assert.NoError(t, err)
	assert.Len(t, configs, 2)
	verifyInternalReleaseMasterMachineConfig(t, configs[0])
	verifyInternalReleaseWorkerMachineConfig(t, configs[1])
}
