package internalreleaseimage

import (
	"testing"

	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/stretchr/testify/assert"
)

func TestRunInternalReleaseImageBootstrap(t *testing.T) {
	configs, err := RunInternalReleaseImageBootstrap(&mcfgv1alpha1.InternalReleaseImage{}, iriCertSecret().obj, nil, cconfig().obj)
	assert.NoError(t, err)
	verifyAllInternalReleaseImageMachineConfigs(t, configs)
}

func TestRunInternalReleaseImageBootstrapWithAuth(t *testing.T) {
	configs, err := RunInternalReleaseImageBootstrap(&mcfgv1alpha1.InternalReleaseImage{}, iriCertSecret().obj, iriAuthSecret().obj, cconfig().obj)
	assert.NoError(t, err)
	assert.Len(t, configs, 2)
	verifyInternalReleaseMasterMachineConfigWithAuth(t, configs[0])
	verifyInternalReleaseWorkerMachineConfig(t, configs[1])
}
