package internalreleaseimage

import (
	"testing"

	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/stretchr/testify/assert"
)

func TestRunInternalReleaseImageBootstrap(t *testing.T) {
	configs, err := RunInternalReleaseImageBootstrap(&mcfgv1alpha1.InternalReleaseImage{}, iriCertSecret().obj, cconfig().obj)
	assert.NoError(t, err)
	verifyInternalReleaseMasterMachineConfig(t, configs[0])
}
