package internalreleaseimage

import (
	"testing"

	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/stretchr/testify/assert"
)

func TestRunInternalReleaseImageBootstrap(t *testing.T) {
	configs, err := RunInternalReleaseImageBootstrap(&mcfgv1alpha1.InternalReleaseImage{}, iriCertSecret().obj, iriRegistryCredentialsSecret().obj, cconfig().withDNS("example.com").obj)
	assert.NoError(t, err)
	assert.Len(t, configs, 2)
	verifyInternalReleaseMasterMachineConfig(t, configs[0])
	verifyInternalReleaseWorkerMachineConfig(t, configs[1])
}
